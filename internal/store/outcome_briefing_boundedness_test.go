package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const unassessedBriefingBuildStatementCeiling = 5 + maximumBriefingClaims*4

type briefingStatementCounter struct {
	database   dbgen.DBTX
	statements int
}

func (counter *briefingStatementCounter) ExecContext(ctx context.Context, query string, arguments ...interface{}) (sql.Result, error) {
	counter.statements++
	return counter.database.ExecContext(ctx, query, arguments...)
}

func (counter *briefingStatementCounter) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	counter.statements++
	return counter.database.PrepareContext(ctx, query)
}

func (counter *briefingStatementCounter) QueryContext(ctx context.Context, query string, arguments ...interface{}) (*sql.Rows, error) {
	counter.statements++
	return counter.database.QueryContext(ctx, query, arguments...)
}

func (counter *briefingStatementCounter) QueryRowContext(ctx context.Context, query string, arguments ...interface{}) *sql.Row {
	counter.statements++
	return counter.database.QueryRowContext(ctx, query, arguments...)
}

func TestManagementBriefingCandidateWorkIsFixedAtPersonalScale(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	for index := 0; index < 160; index++ {
		fixture.createCommitment(t, fmt.Sprintf("bounded-source-%04d", index))
	}
	firstCount, firstOmitted, firstElapsed := measuredUnassessedBriefingBuild(t, storage, fixture, 160)
	for index := 160; index < 1000; index++ {
		fixture.createCommitment(t, fmt.Sprintf("bounded-source-%04d", index))
	}
	secondCount, secondOmitted, secondElapsed := measuredUnassessedBriefingBuild(t, storage, fixture, 1000)

	if firstCount != unassessedBriefingBuildStatementCeiling || secondCount != unassessedBriefingBuildStatementCeiling {
		t.Fatalf("briefing build statements at 160/1000 sources = %d/%d, want fixed ceiling %d", firstCount, secondCount, unassessedBriefingBuildStatementCeiling)
	}
	if firstOmitted != 32 || secondOmitted != 872 {
		t.Fatalf("briefing exact omissions at 160/1000 sources = %d/%d, want 32/872", firstOmitted, secondOmitted)
	}
	if firstElapsed > 10*time.Second || secondElapsed > 10*time.Second {
		t.Fatalf("bounded briefing build exceeded workspace maximum: 160=%s 1000=%s", firstElapsed, secondElapsed)
	}
	t.Logf("bounded briefing profile: sources=160 statements=%d elapsed=%s; sources=1000 statements=%d elapsed=%s", firstCount, firstElapsed, secondCount, secondElapsed)
}

func measuredUnassessedBriefingBuild(t *testing.T, storage *Store, fixture outcomeAdversarialFixture, sourceCount int) (int, int, time.Duration) {
	t.Helper()
	ctx := context.Background()
	cursor, err := dbgen.New(storage.db).MaxWorkspaceEventSequence(ctx, fixture.workspace.ID)
	if err != nil {
		t.Fatalf("MaxWorkspaceEventSequence(%d sources) = %v", sourceCount, err)
	}
	counter := &briefingStatementCounter{database: storage.db}
	started := time.Now()
	candidates, omitted, err := buildBriefingCandidates(ctx, dbgen.New(counter), fixture.workspace.ID, domain.BriefingScope{
		Type: domain.OwnerCheckpointTask, WorkspaceID: fixture.workspace.ID, ProjectID: fixture.project.ID,
		ObjectiveID: fixture.objective.ID, TaskID: fixture.task.Task.ID,
	}, 0, cursor, storage.nowText())
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("buildBriefingCandidates(%d sources) = %v", sourceCount, err)
	}
	if len(candidates) != maximumBriefingClaims {
		t.Fatalf("bounded candidates at %d sources = %d, want %d", sourceCount, len(candidates), maximumBriefingClaims)
	}
	omittedCount := 0
	for _, omission := range omitted {
		if omission.Section == domain.BriefingSectionDeviationsUnmet && omission.Reason == domain.BriefingOmittedClaimLimit {
			omittedCount += omission.Count
		}
	}
	return counter.statements, omittedCount, elapsed
}

func TestWorkspaceBriefingNoisyProjectCannotHideQuietUrgentFacts(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	for index := 0; index < 200; index++ {
		fixture.createCommitment(t, fmt.Sprintf("noisy-source-%04d", index))
	}
	quietIDs := make([]string, 0, 2)
	for index := 0; index < 2; index++ {
		project, task := createBriefingProjectTask(t, storage, fixture.workspace, fmt.Sprintf("quiet-%d", index))
		created, err := storage.CreateDeliverableCommitment(context.Background(), CreateDeliverableCommitmentCommand{
			WorkspaceIdentifier: fixture.workspace.ID, TaskID: task.Task.ID,
			Key: fmt.Sprintf("quiet-source-%d", index), Title: fmt.Sprintf("Quiet urgent fact %d", index),
			AcceptanceCriteria: []string{"the quiet project remains visible under noisy pressure"},
			IdempotencyKey:     fmt.Sprintf("quiet-source-%d", index), CorrelationID: fmt.Sprintf("request-quiet-source-%d", index),
		})
		if err != nil {
			t.Fatalf("CreateDeliverableCommitment(%s) = %v", project.ID, err)
		}
		quietIDs = append(quietIDs, created.Commitment.ID)
	}

	briefing, err := storage.ShowManagementBriefing(context.Background(), ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointWorkspace,
		ScopeIdentifier: fixture.workspace.ID,
	})
	if err != nil {
		t.Fatalf("ShowManagementBriefing(workspace fairness) = %v", err)
	}
	found := make(map[string]bool)
	for _, claim := range briefing.Claims {
		for _, source := range claim.Sources {
			if source.EntityType == "deliverable_commitment" {
				found[source.EntityID] = true
			}
		}
	}
	for _, quietID := range quietIDs {
		if !found[quietID] {
			t.Fatalf("workspace noisy-project pressure hid quiet urgent commitment %s", quietID)
		}
	}
	claimLimitOmitted := 0
	byteLimitOmitted := 0
	for _, omission := range briefing.Omitted {
		if omission.Section != domain.BriefingSectionDeviationsUnmet {
			continue
		}
		switch omission.Reason {
		case domain.BriefingOmittedClaimLimit:
			claimLimitOmitted += omission.Count
		case domain.BriefingOmittedByteLimit:
			byteLimitOmitted += omission.Count
		}
	}
	if len(briefing.Claims) > maximumBriefingClaims || claimLimitOmitted != 74 || len(briefing.Claims)+claimLimitOmitted+byteLimitOmitted != 202 {
		t.Fatalf("workspace bounded selection claims/claim-limit/byte-limit = %d/%d/%d, want <=128/74 and exact total 202", len(briefing.Claims), claimLimitOmitted, byteLimitOmitted)
	}
}

func createBriefingProjectTask(t *testing.T, storage *Store, workspace domain.Workspace, key string) (domain.Project, domain.TaskDetail) {
	t.Helper()
	registered, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{
		WorkspaceIdentifier: workspace.ID, Name: key, WriteMode: domain.WriteModeShared,
		IdempotencyKey: "briefing-project-" + key, CorrelationID: "request-briefing-project-" + key,
		Observation: sourceTestObservation(filepath.Join(t.TempDir(), key), "main"),
	})
	if err != nil {
		t.Fatalf("RegisterProject(%s) = %v", key, err)
	}
	objective, err := storage.CreateObjective(context.Background(), CreateObjectiveCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: registered.Project.ID,
		Title: "Briefing objective " + key, Budget: domain.Budget{TokenLimit: 10000, CostCents: 1000, TimeSeconds: 3600},
		IdempotencyKey: "briefing-objective-" + key, CorrelationID: "request-briefing-objective-" + key,
	})
	if err != nil {
		t.Fatalf("CreateObjective(%s) = %v", key, err)
	}
	task, err := storage.CreateTask(context.Background(), CreateTaskCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: registered.Project.ID,
		ObjectiveID: objective.Value.ID, Title: "Briefing task " + key,
		Budget:         domain.Budget{TokenLimit: 1000, CostCents: 100, TimeSeconds: 600},
		IdempotencyKey: "briefing-task-" + key, CorrelationID: "request-briefing-task-" + key,
	})
	if err != nil {
		t.Fatalf("CreateTask(%s) = %v", key, err)
	}
	return registered.Project, task.Detail
}
