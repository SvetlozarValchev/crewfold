package daemon

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func TestExecutionCapacityErrorIsRetryableAndStructured(t *testing.T) {
	t.Parallel()
	response := storeErrorResponse(localapi.Request{ID: "capacity-request", Protocol: localapi.MaxProtocol}, &store.ExecutionCapacityError{
		Details: store.ExecutionCapacityDetails{Dimension: "provider_unresolved", Scope: "fake", Actual: 4, Limit: 4},
	})
	if response.Error == nil || response.Error.Code != store.CodeExecutionCapacityExhausted || !response.Error.Retryable {
		t.Fatalf("capacity response = %#v", response)
	}
	want := map[string]any{"dimension": "provider_unresolved", "scope": "fake", "actual": 4, "limit": 4}
	for key, value := range want {
		if response.Error.Details[key] != value {
			t.Fatalf("capacity details[%q] = %#v, want %#v", key, response.Error.Details[key], value)
		}
	}
	if len(response.Error.Details) != len(want) {
		t.Fatalf("capacity details = %#v", response.Error.Details)
	}
}

func TestDatabaseBusyErrorIsRetryable(t *testing.T) {
	t.Parallel()
	response := storeErrorResponse(localapi.Request{ID: "busy-request", Protocol: localapi.MaxProtocol}, &store.Error{
		Code:    store.CodeDatabaseBusy,
		Message: "database is busy",
	})
	if response.Error == nil || response.Error.Code != store.CodeDatabaseBusy || !response.Error.Retryable {
		t.Fatalf("database busy response = %#v", response)
	}
}

func TestM20DatabaseBusyReachesOrdinaryClientAndExactRetryCommitsOnce(t *testing.T) {
	config := testConfig(t)
	config.DisableRunWorker = true
	config.DisableCheckWorker = true
	config.DisableCheckWatcher = true
	config.DisableClaimWatcher = true
	config.DisableSupervisor = true
	config.DisableLeaseReconciler = true
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	seed, err := client.WorkspaceInit(context.Background(), "busy-seed", "m20-public-busy-seed")
	if err != nil {
		t.Fatalf("WorkspaceInit(seed) error = %v", err)
	}

	database, err := sql.Open("sqlite3", filepath.Join(config.DataDir, "crewfold.db"))
	if err != nil {
		t.Fatalf("open external SQLite writer: %v", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	heldWriter, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin external SQLite writer: %v", err)
	}
	defer heldWriter.Rollback()
	if _, err := heldWriter.ExecContext(context.Background(), "UPDATE workspaces SET updated_at=updated_at WHERE id=?", seed.Workspace.ID); err != nil {
		t.Fatalf("acquire external SQLite write reservation: %v", err)
	}

	const name = "after-public-busy"
	const idempotencyKey = "m20-public-busy-retry"
	started := time.Now()
	_, err = client.WorkspaceInit(context.Background(), name, idempotencyKey)
	elapsed := time.Since(started)
	var apiError *localapi.APIError
	if !errors.As(err, &apiError) || apiError.Code != store.CodeDatabaseBusy || !apiError.Retryable {
		t.Fatalf("WorkspaceInit(held writer) error after %s = %#v, want retryable %s", elapsed, err, store.CodeDatabaseBusy)
	}
	if elapsed < 4*time.Second || elapsed > 9*time.Second {
		t.Fatalf("WorkspaceInit(held writer) elapsed = %s, want the documented five-second busy bound inside the ordinary client window", elapsed)
	}
	if err := heldWriter.Rollback(); err != nil {
		t.Fatalf("release external SQLite writer: %v", err)
	}

	committed, err := client.WorkspaceInit(context.Background(), name, idempotencyKey)
	if err != nil {
		t.Fatalf("WorkspaceInit(retry) error = %v", err)
	}
	replayed, err := client.WorkspaceInit(context.Background(), name, idempotencyKey)
	if err != nil {
		t.Fatalf("WorkspaceInit(replay) error = %v", err)
	}
	if !reflect.DeepEqual(replayed, committed) {
		t.Fatalf("WorkspaceInit(replay) = %#v, want %#v", replayed, committed)
	}
	var workspaces, events, receipts int
	if err := database.QueryRow("SELECT COUNT(*) FROM workspaces WHERE name=?", name).Scan(&workspaces); err != nil {
		t.Fatalf("count retried workspaces: %v", err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM events WHERE type='workspace.created' AND entity_id=?", committed.Workspace.ID).Scan(&events); err != nil {
		t.Fatalf("count retried workspace events: %v", err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM idempotency_keys WHERE key=?", idempotencyKey).Scan(&receipts); err != nil {
		t.Fatalf("count retried workspace receipts: %v", err)
	}
	if workspaces != 1 || events != 1 || receipts != 1 {
		t.Fatalf("idempotent busy retry counts = workspaces:%d events:%d receipts:%d, want 1/1/1", workspaces, events, receipts)
	}

	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
