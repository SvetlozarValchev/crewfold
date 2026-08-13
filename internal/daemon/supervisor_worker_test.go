package daemon

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

func TestSupervisorWorkerConfigurationIsBoundedAndEnabledByDefault(t *testing.T) {
	t.Parallel()

	base := testConfig(t)
	base.RuntimeDrivers = map[string]execution.RuntimeDriver{}
	base.ProviderAdapters = map[string]execution.ProviderAdapter{}

	resolved, err := resolveConfig(base)
	if err != nil {
		t.Fatalf("resolveConfig(default supervisor) error = %v", err)
	}
	if resolved.DisableSupervisor || resolved.SupervisorScanInterval != 250*time.Millisecond {
		t.Fatalf("default supervisor configuration = disabled %t, interval %s", resolved.DisableSupervisor, resolved.SupervisorScanInterval)
	}

	tooFast := base
	tooFast.SupervisorScanInterval = 19 * time.Millisecond
	if _, err := resolveConfig(tooFast); ErrorCode(err) != CodeInvalidConfiguration {
		t.Fatalf("resolveConfig(too-fast supervisor) error = %v, code = %q", err, ErrorCode(err))
	}

	disabled := tooFast
	disabled.DisableSupervisor = true
	if _, err := resolveConfig(disabled); err != nil {
		t.Fatalf("resolveConfig(disabled supervisor) error = %v", err)
	}
}

func TestIdleSupervisorTicksDoNotCreateDurableChurn(t *testing.T) {
	config := testConfig(t)
	config.DisableRunWorker = true
	config.DisableClaimWatcher = true
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "idle-supervisor-workspace"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	if _, err := client.SupervisorPolicyConfigure(context.Background(), localapi.SupervisorPolicyConfigureParams{
		Workspace: "personal", Enabled: true, AutoSchedule: true, AutoRetryLimit: 0,
		RetryCooldownSeconds: 0, ExpectedRevision: 1, IdempotencyKey: "idle-supervisor-policy",
		Limits: domain.SupervisorLimits{
			MaxActiveRuns: 2, MaxStartingRuns: 1, DefaultProjectConcurrency: 1, DefaultProviderConcurrency: 1,
			ProjectConcurrency: map[string]int{}, ProviderConcurrency: map[string]int{},
		},
	}); err != nil {
		t.Fatalf("SupervisorPolicyConfigure() error = %v", err)
	}

	database, err := sql.Open("sqlite3", filepath.Join(config.DataDir, "crewfold.db"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer database.Close()
	type durableCounts struct {
		Events, StateRows, StateRevisions, StateCursor, Idempotency int64
	}
	readCounts := func() durableCounts {
		t.Helper()
		var counts durableCounts
		if err := database.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&counts.Events); err != nil {
			t.Fatalf("count events: %v", err)
		}
		if err := database.QueryRow(`SELECT COUNT(*),COALESCE(SUM(revision),0),COALESCE(SUM(last_event_sequence),0) FROM supervisor_state`).Scan(&counts.StateRows, &counts.StateRevisions, &counts.StateCursor); err != nil {
			t.Fatalf("count supervisor state: %v", err)
		}
		if err := database.QueryRow(`SELECT COUNT(*) FROM idempotency_keys`).Scan(&counts.Idempotency); err != nil {
			t.Fatalf("count idempotency keys: %v", err)
		}
		return counts
	}

	// Let the enabled worker perform several default 250 ms sweeps, establish a
	// baseline, then prove several more empty sweeps remain read-only.
	time.Sleep(750 * time.Millisecond)
	baseline := readCounts()
	time.Sleep(750 * time.Millisecond)
	after := readCounts()
	if !reflect.DeepEqual(after, baseline) {
		t.Fatalf("idle supervisor durable counts changed across empty ticks: before=%#v after=%#v", baseline, after)
	}

	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
