package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestSemanticValidatorRegistryMatchesEveryCanonicalFamily(t *testing.T) {
	t.Parallel()

	want := make([]string, 0, len(semanticFamilyRegistry))
	for _, family := range semanticFamilyRegistry {
		want = append(want, family.name)
	}
	sort.Strings(want)
	if got := semanticValidatorFamilyNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("semantic validator families = %v, want exact canonical registry %v", got, want)
	}
}

func TestCanonicalVerifierPassesPopulatedManagementAndOutcomeGraphs(t *testing.T) {
	t.Run("management", func(t *testing.T) {
		storage, _ := createManagerGrantAdversarialFixture(t)
		assertCanonicalSemanticPass(t, storage)
	})
	t.Run("applied supervisor schedule", func(t *testing.T) {
		storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
			TargetMaxConcurrency: 8,
			SharedTargetCheckout: true,
		})
		acceptAdversarialSchedulingPair(t, storage, fixture, "canonical-applied-schedule", "")
		configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
			MaxActiveRuns: 8, MaxStartingRuns: 2, DefaultProjectConcurrency: 4, DefaultProviderConcurrency: 4,
		}, "canonical-applied-schedule")
		result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: fixture.workspace.ID,
			Limit:               1,
			IdempotencyKey:      "canonical-applied-schedule-run",
			CorrelationID:       "request-canonical-applied-schedule-run",
		})
		if err != nil || len(result.ScheduledRunIDs) != 1 || len(result.Actions) != 1 || result.Actions[0].Status != domain.SupervisorActionApplied {
			t.Fatalf("RunSupervisor(applied schedule) = %#v, %v; want one applied scheduling action", result, err)
		}
		assertCanonicalSemanticPass(t, storage)
	})
	t.Run("outcome", func(t *testing.T) {
		storage, fixture := newOutcomeAdversarialFixture(t, false)
		fixture.createCommitment(t, "canonical-populated-commitment")
		assertCanonicalSemanticPass(t, storage)
	})
}

func assertCanonicalSemanticPass(t *testing.T, storage *Store) {
	t.Helper()
	report, err := storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
	if err != nil {
		t.Fatalf("VerifyCanonical(populated) error = %v", err)
	}
	if !report.Complete || report.Status != "ok" {
		t.Fatalf("VerifyCanonical(populated) status=%q complete=%t failures=%#v families=%#v", report.Status, report.Complete, report.Failures, report.SemanticFamilies)
	}
	if len(report.SemanticFamilies) != len(semanticFamilyRegistry) {
		t.Fatalf("semantic family result count = %d, want %d", len(report.SemanticFamilies), len(semanticFamilyRegistry))
	}
}

func TestCanonicalVerifierRejectsForgedEventEnvelopeWithExactCatalog(t *testing.T) {
	for _, test := range []struct {
		name      string
		statement string
	}{
		{name: "unknown type", statement: `UPDATE events SET type='future.forged_fact' WHERE sequence=1`},
		{name: "present empty causation", statement: `UPDATE events SET causation_id='' WHERE sequence=1`},
		{name: "noncanonical data", statement: `UPDATE events SET data_json='{ "name": "personal" }' WHERE sequence=1`},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			if _, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
				Name: "personal", IdempotencyKey: "event-integrity", CorrelationID: "event-integrity",
			}); err != nil {
				t.Fatalf("InitWorkspace() error = %v", err)
			}
			var triggerSQL string
			if err := storage.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='events_reject_update'`).Scan(&triggerSQL); err != nil {
				t.Fatalf("read immutable event trigger: %v", err)
			}
			tx, err := storage.db.Begin()
			if err != nil {
				t.Fatalf("begin physical corruption fixture: %v", err)
			}
			if _, err := tx.Exec(`DROP TRIGGER events_reject_update`); err != nil {
				_ = tx.Rollback()
				t.Fatalf("drop trigger inside corruption fixture: %v", err)
			}
			if _, err := tx.Exec(test.statement); err != nil {
				_ = tx.Rollback()
				t.Fatalf("forge event row: %v", err)
			}
			if _, err := tx.Exec(triggerSQL); err != nil {
				_ = tx.Rollback()
				t.Fatalf("restore exact event trigger: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit corruption fixture: %v", err)
			}
			report, err := storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
			if err != nil {
				t.Fatalf("VerifyCanonical(forged event) error = %v", err)
			}
			if !report.Complete || report.Status != "failed" || !integrityFailureNamed(report, "semantic_core") {
				t.Fatalf("VerifyCanonical(forged event) = status %q complete %t failures %#v", report.Status, report.Complete, report.Failures)
			}
			occurrences := 0
			for _, family := range report.SemanticFamilies {
				if family.Name != "core" {
					continue
				}
				for _, violation := range family.Violations {
					if violation.Check == "known_canonical_event_envelope" {
						occurrences++
						if violation.Count != 1 || len(violation.Samples) != 1 {
							t.Fatalf("canonical event violation = %#v, want one exact corrupt row", violation)
						}
					}
				}
			}
			if occurrences != 1 {
				t.Fatalf("known_canonical_event_envelope occurrences = %d, want exactly one", occurrences)
			}
		})
	}
}

func integrityFailureNamed(report CanonicalIntegrityReport, name string) bool {
	for _, failure := range report.Failures {
		if failure.Check == name {
			return true
		}
	}
	return false
}

func TestCanonicalVerifierOwnsAndStreamsEveryCurrentApplicationTable(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})

	report, err := storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
	if err != nil {
		t.Fatalf("VerifyCanonical() error = %v", err)
	}
	if report.Status != "ok" || !report.Complete || report.PhysicalIntegrity != "ok" || report.ForeignKeyViolationCount != 0 {
		t.Fatalf("VerifyCanonical() = %#v, want complete canonical pass", report)
	}
	if report.Baseline.SchemaVersion != CurrentSchemaVersion || len(report.Baseline.SourceSHA256) != 64 || len(report.Baseline.CatalogSHA256) != 64 || len(report.LogicalSHA256) != 64 {
		t.Fatalf("VerifyCanonical() identity = %#v, want exact current SHA identities", report)
	}
	expectedTables := 0
	for _, family := range semanticFamilyRegistry {
		expectedTables += len(family.tables)
	}
	if report.ApplicationTableCount != expectedTables || len(report.SemanticFamilies) != len(semanticFamilyRegistry) {
		t.Fatalf("VerifyCanonical() coverage = %d tables/%d families, want %d/%d", report.ApplicationTableCount, len(report.SemanticFamilies), expectedTables, len(semanticFamilyRegistry))
	}
	for _, family := range report.SemanticFamilies {
		if family.Status != "ok" || len(family.LogicalSHA256) != 64 || len(family.Tables) == 0 {
			t.Fatalf("semantic family = %#v, want streamed exact pass", family)
		}
	}
	if !report.Quiescence.Quiescent || report.Quiescence.EventHighWater != report.EventHighWater || len(report.Quiescence.ProofSHA256) != 64 {
		t.Fatalf("VerifyCanonical() quiescence = %#v, want exact empty cut", report.Quiescence)
	}

	tables, err := applicationTableNames(context.Background(), storage.db)
	if err != nil {
		t.Fatalf("applicationTableNames() error = %v", err)
	}
	if failures := semanticOwnershipFailures(tables); len(failures) != 0 {
		t.Fatalf("semanticOwnershipFailures(current) = %v", failures)
	}
	if failures := semanticOwnershipFailures(append(tables, "unowned_application_table")); len(failures) != 1 {
		t.Fatalf("semanticOwnershipFailures(unowned) = %v, want one exact failure", failures)
	}
}

func TestIntegrityTableAndDurableQueueRegistriesExactlyCoverCurrentBaseline(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})

	if failures, err := tableClassificationFailures(context.Background(), storage.db); err != nil || len(failures) != 0 {
		t.Fatalf("tableClassificationFailures() = %v, %v", failures, err)
	}
	rows, err := storage.db.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("enumerate baseline tables: %v", err)
	}
	classCounts := map[integrityTableClass]int{}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			t.Fatalf("scan baseline table: %v", err)
		}
		class, ok := integrityTableClassFor(table)
		if !ok {
			rows.Close()
			t.Fatalf("baseline table %q has no integrity class", table)
		}
		classCounts[class]++
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close baseline tables: %v", err)
	}
	for _, class := range []integrityTableClass{integrityTableDomain, integrityTableControl, integrityTableDerived} {
		if classCounts[class] == 0 {
			t.Fatalf("integrity class %q owns no current baseline tables: %#v", class, classCounts)
		}
	}

	wantQueues := []durableQueueDefinition{
		{name: "run_job", healthName: "run", table: "run_jobs", idColumn: "run_id", openPredicate: "status IN ('pending','leased')", terminalRule: "status='complete'", blockerKind: "unsettled_run_job", statuses: []string{"pending", "leased", "complete"}},
		{name: "check_job", healthName: "check", table: "check_jobs", idColumn: "check_run_id", openPredicate: "status IN ('pending','leased')", terminalRule: "status='complete'", blockerKind: "unsettled_check_job", statuses: []string{"pending", "leased", "complete"}},
		{name: "message_wake", healthName: "message_wake", table: "message_wake_jobs", idColumn: "id", openPredicate: "status IN ('pending','leased')", terminalRule: "status IN ('succeeded','failed','failed_unknown')", blockerKind: "open_wake_job", statuses: []string{"pending", "leased", "succeeded", "failed", "failed_unknown"}},
		{name: "scheduling_intent", healthName: "scheduling_intent", table: "scheduling_intents", idColumn: "id", openPredicate: "status IN ('pending','deferred','awaiting_approval','run_requested')", terminalRule: "status IN ('satisfied','failed','cancelled')", blockerKind: "open_scheduling_intent", statuses: []string{"pending", "deferred", "awaiting_approval", "run_requested", "satisfied", "failed", "cancelled"}},
		{name: "supervisor_action", healthName: "supervisor_action", table: "supervisor_actions", idColumn: "id", openPredicate: "status IN ('proposed','awaiting_approval','deferred')", terminalRule: "status IN ('applied','dismissed','failed')", blockerKind: "open_supervisor_action", statuses: []string{"proposed", "awaiting_approval", "deferred", "applied", "dismissed", "failed"}},
		{name: "approval", healthName: "approval", table: "approval_requests", idColumn: "id", openPredicate: "status IN ('pending','granted')", terminalRule: "status IN ('denied','expired','consumed')", blockerKind: "open_approval", statuses: []string{"pending", "granted", "denied", "expired", "consumed"}},
		{name: "owner_manager_review", healthName: "owner_manager_review", table: "owner_manager_review_jobs", idColumn: "project_id", openPredicate: "status IN ('pending','leased')", terminalRule: "status IN ('idle','failed')", blockerKind: "open_owner_manager_review", statuses: []string{"idle", "pending", "leased", "failed"}},
		{name: "owner_executive_exchange", healthName: "owner_executive_exchange", table: "owner_executive_exchanges", idColumn: "id", openPredicate: "status IN ('pending','leased','running')", terminalRule: "status IN ('responded','failed')", blockerKind: "open_owner_executive_exchange", statuses: []string{"pending", "leased", "running", "responded", "failed"}},
		{name: "managed_service_job", healthName: "managed_service", table: "managed_service_jobs", idColumn: "id", openPredicate: "status IN ('pending','leased')", terminalRule: "status IN ('complete','failed_unknown')", blockerKind: "unsettled_managed_service_job", statuses: []string{"pending", "leased", "complete", "failed_unknown"}},
	}
	if !reflect.DeepEqual(durableQueueRegistry, wantQueues) {
		t.Fatalf("durableQueueRegistry = %#v, want exact exhaustive contract %#v", durableQueueRegistry, wantQueues)
	}
}

func TestCanonicalVerifierReportsQueueRowOutsideRegistryPartition(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "queue registry corruption")
	created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "queue-registry-corruption",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "wait"}},
	}, "queue-registry-corruption")

	connection, err := storage.writeDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire corruption fixture connection: %v", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatalf("enable corruption fixture: %v", err)
	}
	if _, err := connection.ExecContext(context.Background(), `UPDATE run_jobs SET status='corrupt' WHERE run_id=?`, created.Run.ID); err != nil {
		t.Fatalf("forge out-of-partition queue row: %v", err)
	}
	if _, err := connection.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatalf("disable corruption fixture: %v", err)
	}

	report, err := storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
	if err != nil {
		t.Fatalf("VerifyCanonical(corrupt queue) error = %v", err)
	}
	var runQueue *DurableQueueIntegrity
	for index := range report.DurableQueues {
		if report.DurableQueues[index].Name == "run_job" {
			runQueue = &report.DurableQueues[index]
			break
		}
	}
	if report.Complete == false || report.Status != "failed" || runQueue == nil || runQueue.Status != "failed" ||
		runQueue.RowCount != 1 || runQueue.OpenCount != 0 || runQueue.TerminalCount != 0 || runQueue.ViolationCount != 1 ||
		!reflect.DeepEqual(runQueue.Samples, []string{created.Run.ID}) || !integrityFailureNamed(report, "durable_queue_run_job") {
		t.Fatalf("VerifyCanonical(corrupt queue) = %#v, run queue %#v", report, runQueue)
	}
}

func TestOnlineSnapshotIsIndependentExactAndReadOnlyVerifiable(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	created, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name: "snapshot", IdempotencyKey: "snapshot-workspace", CorrelationID: "snapshot-workspace-request",
	})
	if err != nil {
		t.Fatalf("InitWorkspace() error = %v", err)
	}
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	metadata, err := storage.BackupSnapshot(context.Background(), destination)
	if err != nil {
		t.Fatalf("BackupSnapshot() error = %v", err)
	}
	if metadata.Path != destination || metadata.EventHighWater != created.EventSequence || metadata.ByteSize <= 0 || len(metadata.SHA256) != 64 {
		t.Fatalf("BackupSnapshot() = %#v, want independent exact snapshot", metadata)
	}
	info, err := os.Lstat(destination)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot file = %#v, %v, want exact 0600 regular file", info, err)
	}
	report, err := VerifyDatabaseSnapshot(context.Background(), destination, CanonicalVerifyOptions{Full: true})
	if err != nil || !report.Complete || report.Status != "ok" || report.EventHighWater != created.EventSequence || report.Baseline != metadata.Baseline {
		t.Fatalf("VerifyDatabaseSnapshot() = %#v, %v, want exact snapshot pass", report, err)
	}
	if _, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{Name: "later", IdempotencyKey: "later-workspace", CorrelationID: "later-workspace-request"}); err != nil {
		t.Fatalf("InitWorkspace(later) error = %v", err)
	}
	repeated, err := VerifyDatabaseSnapshot(context.Background(), destination, CanonicalVerifyOptions{Full: true})
	if err != nil || repeated.EventHighWater != created.EventSequence || repeated.LogicalSHA256 != report.LogicalSHA256 {
		t.Fatalf("snapshot changed with live database = %#v, %v; initial %#v", repeated, err, report)
	}
	if _, err := storage.BackupSnapshot(context.Background(), destination); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("BackupSnapshot(existing) error = %v, code = %q", err, ErrorCode(err))
	}
}
