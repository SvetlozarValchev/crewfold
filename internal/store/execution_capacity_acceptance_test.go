package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestM20ManualAndSupervisorAdmissionShareEveryExactCapacityCeiling(t *testing.T) {
	tests := []struct {
		name      string
		dimension string
		limit     int
		limits    domain.SupervisorLimits
		countSQL  string
		countArgs func(managerGrantAdversarialFixture) []any
	}{
		{
			name: "workspace unresolved 8", dimension: "workspace_unresolved", limit: 8,
			limits:    domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8},
			countSQL:  `SELECT COUNT(*) FROM runs WHERE workspace_id=? AND status IN ('requested','starting','active','blocked','stopping','lost')`,
			countArgs: func(fixture managerGrantAdversarialFixture) []any { return []any{fixture.workspace.ID} },
		},
		{
			name: "workspace starting 2", dimension: "workspace_starting", limit: 2,
			limits:    domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 2, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8},
			countSQL:  `SELECT COUNT(*) FROM runs WHERE workspace_id=? AND status IN ('requested','starting')`,
			countArgs: func(fixture managerGrantAdversarialFixture) []any { return []any{fixture.workspace.ID} },
		},
		{
			name: "project unresolved 4", dimension: "project_unresolved", limit: 4,
			limits:    domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 4, DefaultProviderConcurrency: 8},
			countSQL:  `SELECT COUNT(*) FROM runs WHERE project_id=? AND status IN ('requested','starting','active','blocked','stopping','lost')`,
			countArgs: func(fixture managerGrantAdversarialFixture) []any { return []any{fixture.project.ID} },
		},
		{
			name: "provider unresolved 4", dimension: "provider_unresolved", limit: 4,
			limits:    domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 4},
			countSQL:  `SELECT COUNT(*) FROM runs WHERE workspace_id=? AND provider=? AND status IN ('requested','starting','active','blocked','stopping','lost')`,
			countArgs: func(fixture managerGrantAdversarialFixture) []any { return []any{fixture.workspace.ID, "fake"} },
		},
		{
			name: "node unresolved 20", dimension: "node_unresolved", limit: NodeExecutionCapacityLimit,
			limits:    domain.SupervisorLimits{MaxActiveRuns: 100, MaxStartingRuns: 100, DefaultProjectConcurrency: 100, DefaultProviderConcurrency: 100},
			countSQL:  `SELECT COUNT(*) FROM runs WHERE status IN ('requested','starting','active','blocked','stopping','lost')`,
			countArgs: func(managerGrantAdversarialFixture) []any { return nil },
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			slug := strings.ReplaceAll(test.dimension, "_", "-")
			storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
				TargetMaxConcurrency: 100,
				SharedTargetCheckout: true,
			})
			// The fixture predates node-bound runtime bindings. Give its manager
			// completion the exact same canonical node identity a daemon supplies.
			storage.runtimeNodeID = strings.Repeat("1", 32)
			storage.runtimeNodeFingerprint = strings.Repeat("2", 64)

			acceptOneAdversarialSchedulingIntent(t, storage, fixture, "m20-capacity-"+slug)
			configureSupervisorForContention(t, storage, fixture.workspace.ID, test.limits, "m20-capacity-"+slug)

			for index := 0; index < test.limit-1; index++ {
				runID := queueBoundaryCreateReservedRun(t, storage, fixture, fixture.target.AgentID, "fake", "fake", "",
					fmt.Sprintf("m20-capacity-%s-seed-%02d", slug, index))
				if test.dimension == "workspace_unresolved" {
					if _, err := storage.MarkRunStarting(context.Background(), runID, fmt.Sprintf("request-%s-seed-%02d-starting", slug, index)); err != nil {
						t.Fatalf("MarkRunStarting(%s seed %d) = %v", test.dimension, index, err)
					}
					if _, err := storage.MarkRunStarted(context.Background(), runID,
						fmt.Sprintf("%s-runtime-%02d", slug, index), fmt.Sprintf("%s-provider-%02d", slug, index),
						fmt.Sprintf("request-%s-seed-%02d-started", slug, index)); err != nil {
						t.Fatalf("MarkRunStarted(%s seed %d) = %v", test.dimension, index, err)
					}
				}
			}
			manualTask := createM20CapacityManualTask(t, storage, fixture, "m20-capacity-"+slug+"-manual")

			var before int
			if err := storage.db.QueryRow(test.countSQL, test.countArgs(fixture)...).Scan(&before); err != nil {
				t.Fatalf("read %s count before race: %v", test.dimension, err)
			}
			if before != test.limit-1 {
				t.Fatalf("%s count before race = %d, want %d", test.dimension, before, test.limit-1)
			}

			// Stop the supervisor after its full read-only placement decision but
			// before it publishes the run. A concurrent manual start then waits on
			// the same single writer. Releasing the barrier proves the second
			// transaction observes the committed reservation rather than a stale
			// preflight count.
			supervisorInside := make(chan struct{})
			releaseSupervisor := make(chan struct{})
			var barrierOnce sync.Once
			storage.mutationHook = func(stage string) error {
				if stage == MutationAfterSchedulingAuthority {
					barrierOnce.Do(func() { close(supervisorInside) })
					<-releaseSupervisor
				}
				return nil
			}
			t.Cleanup(func() { storage.mutationHook = nil })

			type supervisorOutcome struct {
				result SupervisorRunResult
				err    error
			}
			supervisorResult := make(chan supervisorOutcome, 1)
			go func() {
				result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
					WorkspaceIdentifier: fixture.workspace.ID,
					Limit:               100,
					IdempotencyKey:      "m20-capacity-" + slug + "-supervisor",
					CorrelationID:       "request-m20-capacity-" + slug + "-supervisor",
				})
				supervisorResult <- supervisorOutcome{result: result, err: err}
			}()
			<-supervisorInside

			manualCorrelation := "request-m20-capacity-" + slug + "-manual"
			manualResult := make(chan error, 1)
			writerWaitsBefore := storage.writeDB.Stats().WaitCount
			go func() {
				_, err := storage.CreateRun(context.Background(), CreateRunCommand{
					WorkspaceIdentifier: fixture.workspace.ID,
					TaskID:              manualTask.Task.ID,
					Runtime:             "fake",
					Provider:            "fake",
					Scenario: domain.FakeScenario{
						Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1",
						Name:   "m20-manual-capacity-race",
						Steps:  []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "must not launch past capacity"}},
					},
					ExpectedTaskRevision: manualTask.Task.Revision,
					IdempotencyKey:       "m20-capacity-" + slug + "-manual",
					CorrelationID:        manualCorrelation,
				})
				manualResult <- err
			}()
			waitDeadline := time.Now().Add(time.Second)
			for storage.writeDB.Stats().WaitCount == writerWaitsBefore && time.Now().Before(waitDeadline) {
				time.Sleep(time.Millisecond)
			}
			if storage.writeDB.Stats().WaitCount == writerWaitsBefore {
				close(releaseSupervisor)
				t.Fatal("concurrent manual admission did not wait on the supervisor's serialized transaction")
			}
			close(releaseSupervisor)

			scheduled := <-supervisorResult
			if scheduled.err != nil || len(scheduled.result.ScheduledRunIDs) != 1 {
				t.Fatalf("RunSupervisor(%s final slot) = %#v, %v; want one admitted run", test.dimension, scheduled.result, scheduled.err)
			}
			refused := <-manualResult
			if ErrorCode(refused) != CodeExecutionCapacityExhausted {
				t.Fatalf("CreateRun(%s concurrent refusal) error = %v, code = %q", test.dimension, refused, ErrorCode(refused))
			}
			details, ok := ExecutionCapacityErrorDetails(refused)
			if !ok || details.Dimension != test.dimension || details.Actual != test.limit || details.Limit != test.limit {
				t.Fatalf("CreateRun(%s refusal) details = %#v, found=%t", test.dimension, details, ok)
			}

			var after, refusalEvents, admittedRuns int
			if err := storage.db.QueryRow(test.countSQL, test.countArgs(fixture)...).Scan(&after); err != nil {
				t.Fatalf("read %s count after race: %v", test.dimension, err)
			}
			if err := storage.db.QueryRow(`SELECT COUNT(*) FROM events WHERE correlation_id=?`, manualCorrelation).Scan(&refusalEvents); err != nil {
				t.Fatalf("count %s refusal events: %v", test.dimension, err)
			}
			if err := storage.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE id=?`, scheduled.result.ScheduledRunIDs[0]).Scan(&admittedRuns); err != nil {
				t.Fatalf("count %s admitted supervisor run: %v", test.dimension, err)
			}
			if after != test.limit || refusalEvents != 0 || admittedRuns != 1 {
				t.Fatalf("%s race result = count %d, refusal events %d, admitted runs %d; want %d/0/1",
					test.dimension, after, refusalEvents, admittedRuns, test.limit)
			}
		})
	}
}

func acceptOneAdversarialSchedulingIntent(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture, key string) {
	t.Helper()
	runID, asOfEventSequence := invokeAdversarialManager(t, storage, fixture, key)
	submitted, err := storage.SubmitManagerProposal(context.Background(), SubmitManagerProposalCommand{
		RunID: runID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		Kind: domain.ManagerProposalTaskDecomposition, Summary: "Create one dependency-ready exact-profile task.",
		AsOfEventSequence: asOfEventSequence,
		Actions: []domain.ManagerProposalAction{{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "only", LaunchProfileID: fixture.target.ID, Title: key + " scheduled",
			Priority: 100, Budget: domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
		}}},
		IdempotencyKey: key + "-submit", CorrelationID: "request-" + key + "-submit",
	})
	if err != nil || submitted.Proposal.Status != domain.ManagerProposalPending {
		t.Fatalf("SubmitManagerProposal(%s) = %#v, %v", key, submitted.Proposal, err)
	}
	accepted, err := storage.AcceptManagerProposal(context.Background(), AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ManagerProposalID: submitted.Proposal.ID,
		ExpectedRevision: submitted.Proposal.Revision, DecisionNote: "Accept exact admission-race fixture.",
		IdempotencyKey: key + "-accept", CorrelationID: "request-" + key + "-accept",
	})
	if err != nil || accepted.Proposal.Status != domain.ManagerProposalAccepted {
		t.Fatalf("AcceptManagerProposal(%s) = %#v, %v", key, accepted.Proposal, err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), runID, key+"-runtime", key+"-provider", "request-"+key+"-started"); err != nil {
		t.Fatalf("MarkRunStarted(%s manager) = %v", key, err)
	}
	if _, err := storage.ApplyRunObservation(context.Background(), runID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "Submitted one exact admission-race task.",
		Handoff: "The accepted scheduling intent is ready.", LogArchive: prepareTestRunLogArchive(t, storage, runID),
	}, true, nil, "request-"+key+"-completed"); err != nil {
		t.Fatalf("ApplyRunObservation(%s manager completion) = %v", key, err)
	}
}

func createM20CapacityManualTask(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture, key string) domain.TaskDetail {
	t.Helper()
	created, err := storage.CreateTask(context.Background(), CreateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, ObjectiveID: fixture.objective.ID,
		Title: key, Priority: 100, Budget: domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
		IdempotencyKey: key + "-task", CorrelationID: "request-" + key + "-task",
	})
	if err != nil {
		t.Fatalf("CreateTask(%s) = %v", key, err)
	}
	assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: created.Detail.Task.ID, AgentIdentifier: fixture.target.AgentID,
		LeaseSeconds: 900, ExpectedRevision: created.Detail.Task.Revision,
		IdempotencyKey: key + "-assign", CorrelationID: "request-" + key + "-assign",
	})
	if err != nil {
		t.Fatalf("AssignTask(%s) = %v", key, err)
	}
	return assigned.Detail
}
