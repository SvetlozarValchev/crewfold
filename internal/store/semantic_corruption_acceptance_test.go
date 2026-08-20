package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

type m20SemanticCorruptionFixture struct {
	storage   *Store
	table     string
	statement string
	arguments []any
	sample    string
}

func TestM20FullScanDetectsOneRawCorruptionInEverySemanticFamily(t *testing.T) {
	tests := map[string]struct {
		check string
		setup func(*testing.T) m20SemanticCorruptionFixture
	}{
		"core":         {check: "projection_and_receipt_envelopes", setup: m20CoreCorruptionFixture},
		"project":      {check: "canonical_metadata", setup: m20ProjectCorruptionFixture},
		"run":          {check: "lifecycle_job_and_binding_parity", setup: m20RunCorruptionFixture},
		"coordination": {check: "claim_overlap_graph", setup: m20CoordinationCorruptionFixture},
		"meeting":      {check: "participant_action_graph", setup: m20MeetingCorruptionFixture},
		"knowledge":    {check: "content_scope_and_authority_graph", setup: m20KnowledgeCorruptionFixture},
		"context":      {check: "delta_chain_and_seal", setup: m20ContextCorruptionFixture},
		"management":   {check: "sealed_content_and_child_counts", setup: m20ManagementCorruptionFixture},
		"messaging":    {check: "delivery_and_wake_state", setup: m20MessagingCorruptionFixture},
		"services":     {check: "canonical_metadata", setup: m20ServiceCorruptionFixture},
		"checks":       {check: "content_hashes_and_children", setup: m20CheckCorruptionFixture},
		"outcomes":     {check: "receipts_governance_and_child_counts", setup: m20OutcomeCorruptionFixture},
	}
	if len(tests) != len(semanticFamilyRegistry) {
		t.Fatalf("semantic corruption matrix has %d families, registry has %d", len(tests), len(semanticFamilyRegistry))
	}

	for _, definition := range semanticFamilyRegistry {
		definition := definition
		test, ok := tests[definition.name]
		if !ok {
			t.Fatalf("semantic corruption matrix omits registry family %q", definition.name)
		}
		t.Run(definition.name, func(t *testing.T) {
			fixture := test.setup(t)
			before, err := fixture.storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
			if err != nil || !before.Complete || before.Status != "ok" {
				t.Fatalf("VerifyCanonical(%s valid public fixture) = %#v, %v", definition.name, before, err)
			}
			if family := m20SemanticFamily(t, before, definition.name); family.Status != "ok" || family.ViolationCount != 0 {
				t.Fatalf("semantic family %s before corruption = %#v", definition.name, family)
			}

			applyM20RawSemanticCorruption(t, fixture)
			after, err := fixture.storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
			if err != nil || !after.Complete || after.Status != "failed" {
				t.Fatalf("VerifyCanonical(%s corruption) = %#v, %v", definition.name, after, err)
			}
			family := m20SemanticFamily(t, after, definition.name)
			if family.Status != "failed" || family.ViolationCount != 1 || len(family.Violations) != 1 ||
				family.Violations[0].Check != test.check || family.Violations[0].Count != 1 ||
				!reflect.DeepEqual(family.Violations[0].Samples, []string{fixture.sample}) {
				t.Fatalf("semantic family %s after corruption = %#v; want one %s sample %q", definition.name, family, test.check, fixture.sample)
			}
		})
	}
}

func m20CoreCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name: "m20-semantic-core", IdempotencyKey: "m20-semantic-core", CorrelationID: "m20-semantic-core",
	})
	if err != nil {
		t.Fatal(err)
	}
	return m20SemanticCorruptionFixture{storage: storage, table: "workspaces",
		statement: "UPDATE workspaces SET updated_at='not-a-canonical-timestamp' WHERE id=?", arguments: []any{workspace.Workspace.ID},
		sample: "workspace:" + workspace.Workspace.ID}
}

func m20ProjectCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage := openTestStore(t, t.TempDir(), Options{})
	_, project := initializeWorkTestProject(t, storage)
	return m20SemanticCorruptionFixture{storage: storage, table: "projects",
		statement: "UPDATE projects SET updated_at='not-a-canonical-timestamp' WHERE id=?", arguments: []any{project.ID},
		sample: "project:" + project.ID}
}

func m20RunCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "m20 semantic run")
	run := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "m20-semantic-run",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "remain requested"}},
	}, "m20-semantic-run")
	return m20SemanticCorruptionFixture{storage: storage, table: "runs",
		statement: "UPDATE runs SET updated_at='not-a-canonical-timestamp' WHERE id=?", arguments: []any{run.Run.ID},
		sample: "unfinished_timestamp:" + run.Run.ID}
}

func m20CoordinationCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "m20 semantic claim", "m20-semantic-claim-task")
	claim := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: task.Task.ID,
		Kind: domain.ClaimKindComponent, Target: "m20-semantic-component", Mode: domain.ClaimModeExclusive,
		ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Hour,
		IdempotencyKey: "m20-semantic-claim", CorrelationID: "m20-semantic-claim",
	})
	other, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name: "m20-semantic-other", IdempotencyKey: "m20-semantic-other", CorrelationID: "m20-semantic-other",
	})
	if err != nil {
		t.Fatal(err)
	}
	return m20SemanticCorruptionFixture{storage: storage, table: "work_claims",
		statement: "UPDATE work_claims SET workspace_id=? WHERE id=?", arguments: []any{other.Workspace.ID, claim.Claim.ID},
		sample: "claim:" + claim.Claim.ID}
}

func m20MeetingCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 2)
	meeting := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
	return m20SemanticCorruptionFixture{storage: storage, table: "meetings",
		statement: "UPDATE meetings SET updated_at='not-a-canonical-timestamp' WHERE id=?", arguments: []any{meeting.Detail.Meeting.ID},
		sample: "meeting:" + meeting.Detail.Meeting.ID}
}

func m20KnowledgeCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "m20 semantic knowledge")
	revision := proposeTaskKnowledge(t, storage, workspace.ID, assigned.Task.ID, "m20-semantic-knowledge",
		"M20 semantic knowledge", "One exact proposed revision.", "")
	return m20SemanticCorruptionFixture{storage: storage, table: "knowledge_revisions",
		statement: "UPDATE knowledge_revisions SET content_hash=? WHERE id=?", arguments: []any{strings.Repeat("0", 64), revision.Revision.ID},
		sample: "revision_hash:" + revision.Revision.ID}
}

func m20ContextCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "m20-semantic-context")
	if _, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "m20-semantic-context-refresh", CorrelationID: "m20-semantic-context-refresh",
	}); err != nil {
		t.Fatal(err)
	}
	return m20SemanticCorruptionFixture{storage: storage, table: "run_context_delta_state",
		statement: "UPDATE run_context_delta_state SET delta_count=1 WHERE run_id=?", arguments: []any{run.ID},
		sample: "state:" + run.ID}
}

func m20ManagementCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage, fixture := createManagerGrantAdversarialFixture(t)
	return m20SemanticCorruptionFixture{storage: storage, table: "manager_grants",
		statement: "UPDATE manager_grants SET content_sha256=? WHERE id=?", arguments: []any{strings.Repeat("0", 64), fixture.grant.ID},
		sample: "grant:" + fixture.grant.ID}
}

func m20MessagingCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, agent, _, _ := initializeRunTest(t, storage, "m20 semantic messaging")
	message, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: workspace.ID, RecipientAgent: agent.ID, Kind: domain.MessageInform,
		Subject: "M20 semantic message", Body: "One exact queued delivery.",
		IdempotencyKey: "m20-semantic-message", CorrelationID: "m20-semantic-message",
	})
	if err != nil {
		t.Fatal(err)
	}
	return m20SemanticCorruptionFixture{storage: storage, table: "message_recipients",
		statement: "UPDATE message_recipients SET status='delivered' WHERE message_id=? AND recipient_agent_id=?",
		arguments: []any{message.Value.Message.ID, agent.ID}, sample: "recipient:" + message.Value.Message.ID + ":" + agent.ID}
}

func m20ServiceCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	inspection, err := storage.InspectProject(context.Background(), workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	definition, err := storage.CreateManagedServiceDefinition(context.Background(), CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: inspection.Checkouts[0].ID,
		Name: "m20-semantic-service", Description: "one exact local service", Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: ".",
		Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkNone,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
		RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 100,
		OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "m20-semantic-service", CorrelationID: "m20-semantic-service",
	})
	if err != nil {
		t.Fatal(err)
	}
	return m20SemanticCorruptionFixture{storage: storage, table: "managed_service_definitions",
		statement: "UPDATE managed_service_definitions SET updated_at='not-a-canonical-timestamp' WHERE id=?", arguments: []any{definition.Value.ID},
		sample: "service_definition:" + definition.Value.ID}
}

func m20CheckCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	definition, err := storage.CreateCheckDefinition(context.Background(), CreateCheckDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
		Name: "m20-semantic-check", Executable: "/bin/true", Arguments: []string{"--version"}, WorkingDirectory: ".",
		TimeoutMillis: 1000, OutputByteLimit: 1024,
		IdempotencyKey: "m20-semantic-check", CorrelationID: "m20-semantic-check",
	})
	if err != nil {
		t.Fatal(err)
	}
	return m20SemanticCorruptionFixture{storage: storage, table: "check_definitions",
		statement: "UPDATE check_definitions SET content_sha256=? WHERE id=?", arguments: []any{strings.Repeat("0", 64), definition.Value.ID},
		sample: "definition:" + definition.Value.ID}
}

func m20OutcomeCorruptionFixture(t *testing.T) m20SemanticCorruptionFixture {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "m20-semantic-outcome")
	return m20SemanticCorruptionFixture{storage: storage, table: "deliverable_commitments",
		statement: "UPDATE deliverable_commitments SET content_sha256=? WHERE id=?", arguments: []any{strings.Repeat("0", 64), commitment.Commitment.ID},
		sample: "commitment:" + commitment.Commitment.ID}
}

func applyM20RawSemanticCorruption(t *testing.T, fixture m20SemanticCorruptionFixture) {
	t.Helper()
	ctx := context.Background()
	connection, err := fixture.storage.writeDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = connection.ExecContext(context.Background(), "PRAGMA ignore_check_constraints=OFF")
		_ = connection.Close()
	}()
	if _, err := connection.ExecContext(ctx, "PRAGMA ignore_check_constraints=ON"); err != nil {
		t.Fatal(err)
	}
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, "SELECT name,sql FROM sqlite_schema WHERE type='trigger' AND tbl_name=? ORDER BY name", fixture.table)
	if err != nil {
		t.Fatal(err)
	}
	type triggerDefinition struct{ name, statement string }
	triggers := []triggerDefinition{}
	for rows.Next() {
		var trigger triggerDefinition
		if err := rows.Scan(&trigger.name, &trigger.statement); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		triggers = append(triggers, trigger)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, trigger := range triggers {
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER "+quoteSQLiteIdentifier(trigger.name)); err != nil {
			t.Fatalf("drop %s trigger %s: %v", fixture.table, trigger.name, err)
		}
	}
	result, err := tx.ExecContext(ctx, fixture.statement, fixture.arguments...)
	if err != nil {
		t.Fatalf("apply %s semantic corruption: %v", fixture.table, err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("apply %s semantic corruption affected %d rows: %v", fixture.table, affected, err)
	}
	for _, trigger := range triggers {
		if _, err := tx.ExecContext(ctx, trigger.statement); err != nil {
			t.Fatalf("restore %s trigger %s: %v", fixture.table, trigger.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func m20SemanticFamily(t *testing.T, report CanonicalIntegrityReport, name string) SemanticIntegrityFamily {
	t.Helper()
	for _, family := range report.SemanticFamilies {
		if family.Name == name {
			return family
		}
	}
	t.Fatalf("canonical report omitted semantic family %q", name)
	return SemanticIntegrityFamily{}
}
