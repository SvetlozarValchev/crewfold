package daemon

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/recovery"
	"crewfold/internal/store"
)

func TestM20FullDoctorUsesOneExactCurrentVerifierRegistry(t *testing.T) {
	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	result, err := client.SystemDoctorFull(context.Background())
	if err != nil {
		t.Fatalf("SystemDoctorFull() error = %v", err)
	}
	if result.Schema != localapi.FullDoctorSchema || result.Type != "full_doctor" || result.Status != "ok" {
		t.Fatalf("SystemDoctorFull() identity/status = %#v", result)
	}
	wantOrder := localapi.FullDoctorCheckOrder()
	if len(result.Checks) != len(wantOrder) {
		t.Fatalf("SystemDoctorFull() checks = %d, want %d", len(result.Checks), len(wantOrder))
	}
	for index, check := range result.Checks {
		if check.Code != wantOrder[index] || check.Status != "ok" || check.IssueCount != 0 || check.CheckedCount < 1 {
			t.Fatalf("SystemDoctorFull() check[%d] = %#v", index, check)
		}
	}
	if result.Limits.BriefingClaims != 128 || result.Limits.BriefingBytes != 65536 || result.Limits.NodeUnresolvedRuns != 20 {
		t.Fatalf("SystemDoctorFull() limits = %#v", result.Limits)
	}
	if result.Resources.DatabaseBytes < 1 || result.Resources.RSSBytes < 1 || result.Resources.OpenFDs < 1 || result.Resources.FilesystemFreeBytes < 1 {
		t.Fatalf("SystemDoctorFull() resources = %#v", result.Resources)
	}
}

func TestFullDoctorFailsClosedForUnsafeOrMissingLiveDatabasePath(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		wantCode string
		mutate   func(*testing.T, string)
	}{
		{name: "public mode", wantCode: "database_unsafe_mode", mutate: func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("chmod live database: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		}},
		{name: "unlinked", wantCode: "database_missing", mutate: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatalf("unlink live database: %v", err)
			}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			config := testConfig(t)
			running := startTestServer(t, config)
			client := localapi.NewClient(config.SocketPath)
			databasePath := filepath.Join(config.DataDir, "crewfold.db")
			testCase.mutate(t, databasePath)

			result, err := client.SystemDoctorFull(context.Background())
			if err != nil {
				t.Fatalf("SystemDoctorFull(%s) error = %v", testCase.name, err)
			}
			filesystemCheck := findFullDoctorCheck(t, result.Checks, "filesystem_permissions")
			resourceCheck := findFullDoctorCheck(t, result.Checks, "resource_budget")
			for _, check := range []localapi.FullDoctorCheck{filesystemCheck, resourceCheck} {
				if check.Status != "failed" || check.IssueCount == 0 || len(check.Samples) == 0 || check.Samples[0].Code != testCase.wantCode {
					t.Fatalf("SystemDoctorFull(%s) check = %#v", testCase.name, check)
				}
			}
			if result.Status != "failed" || result.Resources.DatabaseBytes != 0 {
				t.Fatalf("SystemDoctorFull(%s) = status %q resources %#v", testCase.name, result.Status, result.Resources)
			}
			if testCase.name == "public mode" {
				if err := os.Chmod(databasePath, 0o600); err != nil {
					t.Fatalf("restore live database mode: %v", err)
				}
			}
			if _, err := client.Stop(context.Background()); err != nil {
				t.Fatalf("Stop(%s) error = %v", testCase.name, err)
			}
			if err := running.wait(); err != nil {
				t.Fatalf("Run(%s) error = %v", testCase.name, err)
			}
		})
	}
}

func TestFullDoctorDerivesDurableQueueFailureFromCanonicalRegistryReport(t *testing.T) {
	t.Parallel()
	integrity := store.CanonicalIntegrityReport{DurableQueues: []store.DurableQueueIntegrity{
		{Name: "run_job", Table: "run_jobs", RowCount: 3, OpenCount: 2, TerminalCount: 0, Status: "failed", ViolationCount: 1, Samples: []string{"run_bad"}},
	}}
	checks := buildFullDoctorChecks(integrity, recovery.ArtifactFilesystemReport{}, databaseFileProbe{ByteSize: 1}, 0, 0, 0, 0, nil)
	for _, check := range checks {
		if check.Code != "durable_queues" {
			continue
		}
		if check.Status != "failed" || check.CheckedCount != 4 || check.IssueCount != 1 || len(check.Samples) != 1 ||
			check.Samples[0].EntityType != "durable_queue" || check.Samples[0].EntityID != "run_bad" || check.Samples[0].Code != "queue_partition_violation" {
			t.Fatalf("durable queue doctor check = %#v", check)
		}
		return
	}
	t.Fatal("durable_queues doctor check missing")
}

func TestFullDoctorRejectsForeignNodeRuntimeBindingFromCanonicalSemanticReport(t *testing.T) {
	t.Parallel()
	integrity := store.CanonicalIntegrityReport{
		Complete: true,
		Quiescence: store.QuiescentCut{Counts: store.QuiescenceCounts{
			NonterminalRuns: 1, RuntimeBindings: 1,
		}},
		SemanticFamilies: []store.SemanticIntegrityFamily{{
			Name: "run", RowsStreamed: 2, Status: "failed", ViolationCount: 1,
			Violations: []store.SemanticIntegrityViolation{{
				Check: "current_node_runtime_binding", Count: 1,
				Samples: []string{"run_runtime_binding:run_foreign"},
			}},
		}},
	}
	checks := buildFullDoctorChecks(integrity, recovery.ArtifactFilesystemReport{}, databaseFileProbe{ByteSize: 1}, 1, 1, 0, 1, nil)
	runtimeCheck := findFullDoctorCheck(t, checks, "runtime_bindings")
	if runtimeCheck.Status != "failed" || runtimeCheck.IssueCount != 1 || len(runtimeCheck.Samples) != 1 ||
		runtimeCheck.Samples[0].EntityType != "runtime_binding" || runtimeCheck.Samples[0].EntityID != "run_runtime_binding:run_foreign" ||
		runtimeCheck.Samples[0].Code != "foreign_node_binding" {
		t.Fatalf("runtime binding doctor check = %#v", runtimeCheck)
	}
}

func TestFullDoctorReportsMissingRequiredLiveRunAndCheckBindingsExactlyOnce(t *testing.T) {
	if _, err := os.Stat("../../test/fixtures/git/create.sh"); err != nil {
		t.Skipf("Git fixture is unavailable: %v", err)
	}

	t.Run("run", func(t *testing.T) {
		fixtureRoot := t.TempDir()
		createGitFixture(t, fixtureRoot)
		config := testConfig(t)
		first := startTestServer(t, config)
		client := localapi.NewClient(config.SocketPath)
		project, agent := initializeRunWorkerAPI(t, client, fixtureRoot)
		task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "doctor missing run binding")
		started, err := client.RunStart(context.Background(), localapi.RunStartParams{
			Workspace: "personal", Task: task.Detail.Task.ID, Checkout: project.Checkout.ID,
			Runtime: "fake", Provider: "fake",
			Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "doctor-missing-run-binding", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "remain active"}}},
			ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "doctor-missing-run-binding",
		})
		if err != nil {
			t.Fatalf("RunStart() error = %v", err)
		}
		waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunActive)
		stopDoctorFixtureDaemon(t, first, client)

		config.DisableRunWorker = true
		config.DisableCheckWorker = true
		second := startTestServer(t, config)
		restarted := localapi.NewClient(config.SocketPath)
		deleteDoctorRuntimeBinding(t, config.DataDir, "run", started.Detail.Run.ID)
		result, err := restarted.SystemDoctorFull(context.Background())
		if err != nil {
			t.Fatalf("SystemDoctorFull(missing run binding) error = %v", err)
		}
		assertOnlyRuntimeBindingDoctorFailure(t, result, "run_runtime_binding:"+started.Detail.Run.ID)
		stopDoctorFixtureDaemon(t, second, restarted)
	})

	t.Run("check", func(t *testing.T) {
		fixtureRoot := t.TempDir()
		createGitFixture(t, fixtureRoot)
		config, _ := checkWorkerTestConfig(t)
		first := startTestServer(t, config)
		client := localapi.NewClient(config.SocketPath)
		started := createOwnerCheckRun(t, client, fixtureRoot, "/bin/sleep", ".", []string{"30"}, 4096)
		waitForCondition(t, 8*time.Second, func() bool {
			inspected, err := client.CheckInspect(context.Background(), "personal", started.Run.ID)
			return err == nil && inspected.Detail.Run.Status == domain.CheckRunRunning
		}, "check run to reach its binding-required running state")
		stopDoctorFixtureDaemon(t, first, client)

		config.DisableCheckWorker = true
		second := startTestServer(t, config)
		restarted := localapi.NewClient(config.SocketPath)
		handle := deleteDoctorRuntimeBinding(t, config.DataDir, "check", started.Run.ID)
		t.Cleanup(func() {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = config.CheckRuntimeDriver.Stop(cleanupContext, started.Run.ID, handle, execution.StopSpec{GracePeriod: 100 * time.Millisecond})
		})
		result, err := restarted.SystemDoctorFull(context.Background())
		if err != nil {
			t.Fatalf("SystemDoctorFull(missing check binding) error = %v", err)
		}
		assertOnlyRuntimeBindingDoctorFailure(t, result, "check_runtime_binding:"+started.Run.ID)
		stopDoctorFixtureDaemon(t, second, restarted)
	})
}

func deleteDoctorRuntimeBinding(t *testing.T, dataDir, kind, operationID string) string {
	t.Helper()
	database, err := sql.Open("sqlite3", filepath.Join(dataDir, "crewfold.db"))
	if err != nil {
		t.Fatalf("open live database for %s binding corruption: %v", kind, err)
	}
	defer database.Close()
	table, idColumn := "run_runtime_bindings", "run_id"
	if kind == "check" {
		table, idColumn = "check_runtime_bindings", "check_run_id"
	}
	var handle string
	if err := database.QueryRow("SELECT runtime_handle FROM "+table+" WHERE "+idColumn+"=?", operationID).Scan(&handle); err != nil {
		t.Fatalf("read live %s runtime binding: %v", kind, err)
	}
	result, err := database.Exec("DELETE FROM "+table+" WHERE "+idColumn+"=?", operationID)
	if err != nil {
		t.Fatalf("delete live %s runtime binding: %v", kind, err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("delete live %s runtime binding affected %d rows: %v", kind, affected, err)
	}
	return handle
}

func assertOnlyRuntimeBindingDoctorFailure(t *testing.T, result localapi.FullDoctorResult, entityID string) {
	t.Helper()
	runtimeCheck := findFullDoctorCheck(t, result.Checks, "runtime_bindings")
	if result.Status != "failed" || runtimeCheck.Status != "failed" || runtimeCheck.CheckedCount != 1 || runtimeCheck.IssueCount != 1 ||
		len(runtimeCheck.Samples) != 1 || runtimeCheck.Samples[0].EntityType != "runtime_binding" ||
		runtimeCheck.Samples[0].EntityID != entityID || runtimeCheck.Samples[0].Code != "runtime_binding_parity" {
		t.Fatalf("full doctor missing binding result = status %q, runtime check %#v", result.Status, runtimeCheck)
	}
	projectionCheck := findFullDoctorCheck(t, result.Checks, "projection_receipt_parity")
	if projectionCheck.Status != "ok" || projectionCheck.IssueCount != 0 || len(projectionCheck.Samples) != 0 {
		t.Fatalf("missing runtime binding was double-counted as projection parity: %#v", projectionCheck)
	}
}

func stopDoctorFixtureDaemon(t *testing.T, running *runningServer, client *localapi.Client) {
	t.Helper()
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestFullDoctorNeverPassesUnexecutedChecksAndRetainsBoundedCanonicalSamples(t *testing.T) {
	t.Parallel()
	foreignKeys := make([]store.ForeignKeyViolation, maximumDoctorSamples+3)
	semanticSamples := make([]string, maximumDoctorSamples+4)
	for index := range foreignKeys {
		rowID := int64(index + 1)
		foreignKeys[index] = store.ForeignKeyViolation{Table: "tasks", RowID: &rowID, ParentTable: "projects", ForeignKey: 2}
	}
	for index := range semanticSamples {
		semanticSamples[index] = "task:corrupt-" + string(rune('a'+index))
	}
	complete := store.CanonicalIntegrityReport{
		Complete: true, ForeignKeyViolationCount: int64(len(foreignKeys)), ForeignKeyViolations: foreignKeys,
		SemanticFamilies: []store.SemanticIntegrityFamily{{
			Name: "project", RowsStreamed: 50, Status: "failed", ViolationCount: int64(len(semanticSamples)),
			Violations: []store.SemanticIntegrityViolation{{Check: "scope_parity", Count: int64(len(semanticSamples)), Samples: semanticSamples}},
		}},
	}
	checks := buildFullDoctorChecks(complete, recovery.ArtifactFilesystemReport{}, databaseFileProbe{ByteSize: 1}, 0, 1, 0, 1, nil)
	foreignKeyCheck := findFullDoctorCheck(t, checks, "foreign_keys")
	projectionCheck := findFullDoctorCheck(t, checks, "projection_receipt_parity")
	if foreignKeyCheck.IssueCount != int64(len(foreignKeys)) || len(foreignKeyCheck.Samples) != maximumDoctorSamples {
		t.Fatalf("foreign-key doctor samples = %#v", foreignKeyCheck)
	}
	if projectionCheck.IssueCount != int64(len(semanticSamples)) || len(projectionCheck.Samples) != maximumDoctorSamples {
		t.Fatalf("semantic doctor samples = %#v", projectionCheck)
	}

	incomplete := store.CanonicalIntegrityReport{
		Complete: false,
		Failures: []store.CanonicalIntegrityIssue{{Check: "current_baseline", Detail: "baseline scan stopped"}},
	}
	incompleteChecks := buildFullDoctorChecks(incomplete, recovery.ArtifactFilesystemReport{}, databaseFileProbe{ByteSize: 1}, 0, 1, 0, 1, nil)
	for _, code := range []string{
		"current_baseline", "sqlite_integrity_check", "foreign_keys", "canonical_integrity", "event_contract",
		"projection_receipt_parity", "artifact_integrity", "derived_knowledge_index", "runtime_bindings",
		"durable_queues", "filesystem_permissions",
	} {
		check := findFullDoctorCheck(t, incompleteChecks, code)
		if check.Status == "ok" || check.IssueCount == 0 {
			t.Fatalf("incomplete canonical report left %s passing: %#v", code, check)
		}
	}
}

func findFullDoctorCheck(t *testing.T, checks []localapi.FullDoctorCheck, code string) localapi.FullDoctorCheck {
	t.Helper()
	for _, check := range checks {
		if check.Code == code {
			return check
		}
	}
	t.Fatalf("full doctor check %q missing", code)
	return localapi.FullDoctorCheck{}
}

func TestM20BackupCreatePublishesAndReplaysOneVerifiedQuiescentCut(t *testing.T) {
	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	target := filepath.Join(filepath.Dir(running.config.DataDir), "bundle")
	params := localapi.BackupCreateParams{TargetPath: target, IdempotencyKey: "m20-exact-cut"}
	first, err := client.BackupCreate(context.Background(), params)
	if err != nil {
		t.Fatalf("BackupCreate(first) error = %v", err)
	}
	if first.Backup.Path != target || first.Backup.ID == "" || first.Backup.TotalBytes < 1 || first.Backup.ManifestSHA256 == "" {
		t.Fatalf("BackupCreate(first) = %#v", first)
	}
	verified, err := recovery.VerifyBundle(context.Background(), target)
	if err != nil {
		t.Fatalf("VerifyBundle() error = %v", err)
	}
	if verified.Manifest.BackupID != first.Backup.ID || verified.Manifest.EventHighWater != first.Backup.EventSequence ||
		verified.ManifestSHA256 != first.Backup.ManifestSHA256 || verified.Manifest.LogicalSHA256 != first.Backup.LogicalStateSHA256 {
		t.Fatalf("verified bundle = %#v, create = %#v", verified.Manifest, first.Backup)
	}
	replayed, err := client.BackupCreate(context.Background(), params)
	if err != nil {
		t.Fatalf("BackupCreate(replay) error = %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("BackupCreate(replay) = %#v, want %#v", replayed, first)
	}
	_, err = client.BackupCreate(context.Background(), localapi.BackupCreateParams{
		TargetPath: filepath.Join(filepath.Dir(running.config.DataDir), "different"), IdempotencyKey: params.IdempotencyKey,
	})
	var apiError *localapi.APIError
	if !errors.As(err, &apiError) || apiError.Code != recovery.CodeIdempotencyConflict || apiError.Retryable {
		t.Fatalf("BackupCreate(conflict) error = %#v", err)
	}
}

func TestM20RecoveryCancellationIsExplicitlyRetryable(t *testing.T) {
	t.Parallel()
	request := localapi.Request{ID: "req-m20-cancel", Protocol: localapi.MaxProtocol}
	response := recoveryErrorResponse(request, &recovery.Error{Code: recovery.CodeOperationCancelled, Message: "backup cancelled before publication"})
	if response.Error == nil || response.Error.Code != recovery.CodeOperationCancelled || !response.Error.Retryable || response.Result != nil {
		t.Fatalf("recoveryErrorResponse(cancelled) = %#v", response)
	}
}
