package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
	"github.com/ncruces/go-sqlite3"
)

func TestOpenCreatesHealthyCurrentDatabase(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	storage := openTestStore(t, dataDir, Options{})
	health, err := storage.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if health.Status != "ok" || health.SchemaVersion != CurrentSchemaVersion || health.SQLiteVersion == "" {
		t.Fatalf("Health() = %#v, want healthy schema %d", health, CurrentSchemaVersion)
	}
	if health.JournalMode != "wal" || !health.ForeignKeys || health.IntegrityCheck != "ok" {
		t.Fatalf("Health() = %#v, want WAL, foreign keys, and integrity ok", health)
	}
	var applicationID int
	if err := storage.db.QueryRow("PRAGMA application_id").Scan(&applicationID); err != nil {
		t.Fatalf("read application id: %v", err)
	}
	if applicationID != sqliteApplicationID {
		t.Fatalf("application id = %#x, want %#x", applicationID, sqliteApplicationID)
	}
	var busyTimeout, synchronous int
	if err := storage.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy timeout: %v", err)
	}
	if err := storage.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("read synchronous mode: %v", err)
	}
	if busyTimeout != 5000 || synchronous != 2 {
		t.Fatalf("connection pragmas = busy_timeout %d, synchronous %d; want 5000, FULL(2)", busyTimeout, synchronous)
	}

	info, err := os.Stat(filepath.Join(dataDir, databaseFilename))
	if err != nil {
		t.Fatalf("os.Stat(database) error = %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("database permissions = %04o, want 0600", permissions)
	}

	if len(health.SourceSHA256) != 64 || len(health.CatalogSHA256) != 64 {
		t.Fatalf("Health() baseline identity = %#v, want exact SHA-256 fields", health)
	}
	var baselineCount int
	var sourceSHA256, catalogSHA256 string
	if err := storage.db.QueryRow("SELECT COUNT(*),MIN(source_sha256),MIN(catalog_sha256) FROM schema_baseline").Scan(&baselineCount, &sourceSHA256, &catalogSHA256); err != nil {
		t.Fatalf("read current schema baseline: %v", err)
	}
	if baselineCount != 1 || sourceSHA256 != health.SourceSHA256 || catalogSHA256 != health.CatalogSHA256 {
		t.Fatalf("schema baseline = %d/%q/%q, want exact health identity", baselineCount, sourceSHA256, catalogSHA256)
	}

	foreignKeyRows, err := storage.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	if foreignKeyRows.Next() {
		_ = foreignKeyRows.Close()
		t.Fatal("current baseline has a foreign-key violation")
	}
	if err := foreignKeyRows.Close(); err != nil {
		t.Fatalf("close foreign_key_check: %v", err)
	}

	var criticalTriggers, obsoleteSchema int
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name IN (
'context_packet_validate_insert','run_context_binding_validate_insert','check_repair_effect_validate_insert','check_watch_state_validate_update')`).Scan(&criticalTriggers); err != nil {
		t.Fatalf("inspect current boundary triggers: %v", err)
	}
	if criticalTriggers != 4 {
		t.Fatalf("current boundary trigger count = %d, want 4", criticalTriggers)
	}
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE sql GLOB '*context-packet:v[2-9]*'
OR sql LIKE '%scheduling_intents_m17%' OR sql LIKE '%scheduling_intents_before_%'
OR sql LIKE '%messages_m17%' OR sql LIKE '%messages_before_%'`).Scan(&obsoleteSchema); err != nil {
		t.Fatalf("inspect baseline for obsolete schema: %v", err)
	}
	if obsoleteSchema != 0 {
		t.Fatalf("current baseline retains %d obsolete schema objects", obsoleteSchema)
	}
	indexTx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin clean index inspection: %v", err)
	}
	indexStatus := storage.knowledgeIndexStatusInTransaction(context.Background(), indexTx)
	_ = indexTx.Rollback()
	if indexStatus.Status != domain.KnowledgeIndexOK || indexStatus.Generation != 1 || indexStatus.SourceCount != 0 {
		t.Fatalf("clean current knowledge index = %#v, want generation-one empty OK projection", indexStatus)
	}
}

func TestWorkspaceInitIsAtomicIdempotentAndPersistent(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	storage := openTestStore(t, dataDir, Options{})
	command := InitWorkspaceCommand{
		Name:           "personal",
		IdempotencyKey: "initialize-personal",
		CorrelationID:  "request-one",
	}
	first, err := storage.InitWorkspace(context.Background(), command)
	if err != nil {
		t.Fatalf("InitWorkspace(first) error = %v", err)
	}
	second, err := storage.InitWorkspace(context.Background(), command)
	if err != nil {
		t.Fatalf("InitWorkspace(replay) error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("idempotent result changed:\nfirst = %#v\nsecond = %#v", first, second)
	}
	if first.Workspace.Revision != 1 || first.EventSequence != 1 {
		t.Fatalf("InitWorkspace() = %#v, want revision and sequence 1", first)
	}

	events := testWorkspaceEvents(t, storage, first.Workspace.ID, 0, 100)
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Type != workspaceCreated || events[0].Entity.ID != first.Workspace.ID || events[0].CorrelationID != "request-one" {
		t.Fatalf("event = %#v, want workspace.created with matching entity/correlation", events[0])
	}
	var data map[string]string
	if err := json.Unmarshal(events[0].Data, &data); err != nil {
		t.Fatalf("json.Unmarshal(event data) error = %v", err)
	}
	if data["name"] != "personal" {
		t.Fatalf("event data = %#v, want personal", data)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(context.Background(), dataDir, Options{})
	if err != nil {
		t.Fatalf("Open(restart) error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	workspace, err := reopened.Workspace(context.Background(), "personal")
	if err != nil {
		t.Fatalf("Workspace(after restart) error = %v", err)
	}
	if !reflect.DeepEqual(workspace, first.Workspace) {
		t.Fatalf("Workspace(after restart) = %#v, want %#v", workspace, first.Workspace)
	}
	byID, err := reopened.Workspace(context.Background(), first.Workspace.ID)
	if err != nil || !reflect.DeepEqual(byID, first.Workspace) {
		t.Fatalf("Workspace(by ID) = %#v, %v, want %#v", byID, err, first.Workspace)
	}
}

func TestM20BusyWriterDoesNotStarveReadsAndIdempotentRetryCommitsOnce(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	seed, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name: "seed", IdempotencyKey: "m20-db-seed", CorrelationID: "m20-db-seed",
	})
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := sql.Open("sqlite3", databaseDSN(storage.Path()))
	if err != nil {
		t.Fatal(err)
	}
	blocker.SetMaxOpenConns(1)
	blocker.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = blocker.Close() })
	heldWriter, err := blocker.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = heldWriter.Rollback() })
	if _, err := heldWriter.ExecContext(context.Background(), "UPDATE workspaces SET updated_at = updated_at WHERE id = ?", seed.Workspace.ID); err != nil {
		t.Fatal(err)
	}

	command := InitWorkspaceCommand{
		Name: "after-busy", IdempotencyKey: "m20-db-retry", CorrelationID: "m20-db-retry",
	}
	type mutationAttempt struct {
		result  WorkspaceInitResult
		err     error
		elapsed time.Duration
	}
	attempts := make(chan mutationAttempt, 1)
	go func() {
		started := time.Now()
		result, err := storage.InitWorkspace(context.Background(), command)
		attempts <- mutationAttempt{result: result, err: err, elapsed: time.Since(started)}
	}()

	deadline := time.Now().Add(time.Second)
	for storage.writeDB.Stats().InUse != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if storage.writeDB.Stats().InUse != 1 {
		t.Fatal("mutation did not enter the dedicated writer connection")
	}

	readContext, cancelRead := context.WithTimeout(context.Background(), time.Second)
	defer cancelRead()
	readStarted := time.Now()
	readSeed, err := storage.Workspace(readContext, seed.Workspace.ID)
	if err != nil {
		t.Fatalf("Workspace(while writer busy) error = %v", err)
	}
	if readSeed.ID != seed.Workspace.ID || time.Since(readStarted) >= time.Second {
		t.Fatalf("Workspace(while writer busy) = %#v after %s, want responsive seed read", readSeed, time.Since(readStarted))
	}

	healthContext, cancelHealth := context.WithTimeout(context.Background(), time.Second)
	defer cancelHealth()
	healthStarted := time.Now()
	health, err := storage.Health(healthContext)
	if err != nil {
		t.Fatalf("Health(while writer busy) error = %v", err)
	}
	if health.Status != "ok" || time.Since(healthStarted) >= time.Second {
		t.Fatalf("Health(while writer busy) = %#v after %s, want responsive ok status", health, time.Since(healthStarted))
	}

	failed := <-attempts
	if ErrorCode(failed.err) != CodeDatabaseBusy || !errors.Is(failed.err, sqlite3.BUSY) {
		t.Fatalf("InitWorkspace(held writer) error = %v, code = %q; want typed %s", failed.err, ErrorCode(failed.err), CodeDatabaseBusy)
	}
	if failed.elapsed < sqliteBusyTimeout-time.Second || failed.elapsed > sqliteBusyTimeout+3*time.Second {
		t.Fatalf("InitWorkspace(held writer) elapsed = %s, want documented %s busy bound", failed.elapsed, sqliteBusyTimeout)
	}
	if _, err := storage.Workspace(context.Background(), command.Name); ErrorCode(err) != CodeWorkspaceNotFound {
		t.Fatalf("busy mutation published workspace = %#v, code = %q", failed.result, ErrorCode(err))
	}

	if err := heldWriter.Rollback(); err != nil {
		t.Fatal(err)
	}
	committed, err := storage.InitWorkspace(context.Background(), command)
	if err != nil {
		t.Fatalf("InitWorkspace(retry) error = %v", err)
	}
	replayed, err := storage.InitWorkspace(context.Background(), command)
	if err != nil {
		t.Fatalf("InitWorkspace(replay) error = %v", err)
	}
	if !reflect.DeepEqual(replayed, committed) {
		t.Fatalf("idempotent busy retry changed result:\ncommitted = %#v\nreplayed = %#v", committed, replayed)
	}
	var workspaces, events, receipts int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE name = ?", command.Name).Scan(&workspaces); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE workspace_id = ? AND type = ?", committed.Workspace.ID, workspaceCreated).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys WHERE key = ?", command.IdempotencyKey).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if workspaces != 1 || events != 1 || receipts != 1 {
		t.Fatalf("busy retry persisted workspace/event/receipt = %d/%d/%d, want exactly 1/1/1", workspaces, events, receipts)
	}
}

func TestWorkspaceInvariantAndIdempotencyFailuresWriteNothing(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	first, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name:           "personal",
		IdempotencyKey: "key-one",
		CorrelationID:  "request-one",
	})
	if err != nil {
		t.Fatalf("InitWorkspace(first) error = %v", err)
	}

	_, err = storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name:           "personal",
		IdempotencyKey: "key-two",
		CorrelationID:  "request-two",
	})
	if ErrorCode(err) != CodeWorkspaceExists {
		t.Fatalf("duplicate name error = %v, code = %q", err, ErrorCode(err))
	}
	_, err = storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name:           "different",
		IdempotencyKey: "key-one",
		CorrelationID:  "request-three",
	})
	if ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("reused key error = %v, code = %q", err, ErrorCode(err))
	}
	_, err = storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name:           "Not Valid",
		IdempotencyKey: "key-three",
		CorrelationID:  "request-four",
	})
	if ErrorCode(err) != CodeInvalidWorkspace {
		t.Fatalf("invalid name error = %v, code = %q", err, ErrorCode(err))
	}

	assertCounts(t, storage, 1, 1, 1)
	workspace, err := storage.Workspace(context.Background(), "personal")
	if err != nil || workspace.ID != first.Workspace.ID {
		t.Fatalf("persisted workspace = %#v, %v, want %s", workspace, err, first.Workspace.ID)
	}
}

func TestMutationFailuresRollBackProjectionEventAndIdempotency(t *testing.T) {
	t.Parallel()

	for _, stage := range []string{MutationAfterProjection, MutationAfterEvent} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			injected := errors.New("injected transaction interruption")
			storage := openTestStore(t, t.TempDir(), Options{MutationHook: func(current string) error {
				if current == stage {
					return injected
				}
				return nil
			}})
			_, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
				Name:           "personal",
				IdempotencyKey: "key-one",
				CorrelationID:  "request-one",
			})
			if !errors.Is(err, injected) {
				t.Fatalf("InitWorkspace() error = %v, want injected error", err)
			}
			assertCounts(t, storage, 0, 0, 0)
		})
	}
}

func TestEventCursorIsStrictlyAfterAndOrdered(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	var beta Workspace
	for index, name := range []string{"alpha", "beta"} {
		result, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
			Name:           name,
			IdempotencyKey: "key-" + name,
			CorrelationID:  "request-" + name,
		})
		if err != nil {
			t.Fatalf("InitWorkspace(%d) error = %v", index, err)
		}
		if name == "beta" {
			beta = result.Workspace
		}
	}
	events := testWorkspaceEvents(t, storage, beta.ID, 1, 100)
	if len(events) != 1 || events[0].Sequence != 2 || events[0].Data == nil {
		t.Fatalf("Events(after 1) = %#v, want only sequence 2", events)
	}
}

func TestEventRowsAreImmutableAndForeignKeysAreEnforced(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	created, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name:           "personal",
		IdempotencyKey: "key-one",
		CorrelationID:  "request-one",
	})
	if err != nil {
		t.Fatalf("InitWorkspace() error = %v", err)
	}
	if _, err := storage.db.Exec("UPDATE events SET type = 'tampered' WHERE sequence = 1"); err == nil {
		t.Fatal("UPDATE events succeeded, want immutable journal rejection")
	}
	if _, err := storage.db.Exec("DELETE FROM events WHERE sequence = 1"); err == nil {
		t.Fatal("DELETE events succeeded, want immutable journal rejection")
	}
	if _, err := storage.db.Exec(`
INSERT INTO events(
    event_id, type, schema_version, occurred_at, recorded_at,
    actor_id, actor_type, workspace_id, entity_type, entity_id,
    entity_revision, correlation_id, data_json
) VALUES ('evt_00000000000000000000000000000000', 'test.created', 1, 'now', 'now',
          'local-owner', 'human', 'ws_00000000000000000000000000000000',
          'workspace', 'ws_00000000000000000000000000000000', 1, 'test', '{}')`); err == nil {
		t.Fatal("event with missing workspace succeeded, want foreign-key rejection")
	}
	events := testWorkspaceEvents(t, storage, created.Workspace.ID, 0, 100)
	if len(events) != 1 || events[0].EventID != created.EventID || events[0].Type != workspaceCreated {
		t.Fatalf("events after rejected mutations = %#v, want original event", events)
	}
}

func testWorkspaceEvents(t *testing.T, storage *Store, workspace string, after int64, limit int) []Event {
	t.Helper()
	page, err := storage.ListEvents(context.Background(), ListEventsQuery{WorkspaceIdentifier: workspace, After: after, Limit: limit})
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	return page.Events
}

func TestDatabaseSymlinkAndNoncurrentIdentityAreRefused(t *testing.T) {
	t.Parallel()

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		target := filepath.Join(t.TempDir(), "user-file")
		if err := os.WriteFile(target, []byte("preserve me"), 0o600); err != nil {
			t.Fatalf("os.WriteFile(target) error = %v", err)
		}
		if err := os.Symlink(target, filepath.Join(dataDir, databaseFilename)); err != nil {
			t.Fatalf("os.Symlink(database) error = %v", err)
		}
		_, err := Open(context.Background(), dataDir, Options{})
		if ErrorCode(err) != CodeStorageFailed {
			t.Fatalf("Open(symlink) error = %v, code = %q", err, ErrorCode(err))
		}
		contents, readErr := os.ReadFile(target)
		if readErr != nil || string(contents) != "preserve me" {
			t.Fatalf("target = %q, %v, want preserved", contents, readErr)
		}
	})

	t.Run("newer schema", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		database, err := sql.Open("sqlite3", filepath.Join(dataDir, databaseFilename))
		if err != nil {
			t.Fatalf("sql.Open() error = %v", err)
		}
		if _, err := database.Exec("PRAGMA application_id = 0x43524644; PRAGMA user_version = 999"); err != nil {
			t.Fatalf("set future user_version: %v", err)
		}
		if err := database.Close(); err != nil {
			t.Fatalf("database.Close() error = %v", err)
		}
		if err := os.Chmod(filepath.Join(dataDir, databaseFilename), 0o600); err != nil {
			t.Fatalf("chmod future database: %v", err)
		}
		_, err = Open(context.Background(), dataDir, Options{})
		if ErrorCode(err) != CodeCurrentBaselineMismatch {
			t.Fatalf("Open(future schema) error = %v, code = %q", err, ErrorCode(err))
		}
	})

	t.Run("unidentified database", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		databasePath := filepath.Join(dataDir, databaseFilename)
		database, err := sql.Open("sqlite3", databasePath)
		if err != nil {
			t.Fatalf("sql.Open() error = %v", err)
		}
		if _, err := database.Exec("CREATE TABLE belongs_to_user(value TEXT); INSERT INTO belongs_to_user VALUES ('preserve')"); err != nil {
			t.Fatalf("create unidentified database: %v", err)
		}
		if err := database.Close(); err != nil {
			t.Fatalf("database.Close() error = %v", err)
		}
		if err := os.Chmod(databasePath, 0o600); err != nil {
			t.Fatalf("chmod unidentified database: %v", err)
		}
		_, err = Open(context.Background(), dataDir, Options{})
		if ErrorCode(err) != CodeCurrentBaselineMismatch {
			t.Fatalf("Open(unidentified) error = %v, code = %q", err, ErrorCode(err))
		}

		database, err = sql.Open("sqlite3", databasePath)
		if err != nil {
			t.Fatalf("sql.Open(reinspect) error = %v", err)
		}
		defer database.Close()
		var value string
		if err := database.QueryRow("SELECT value FROM belongs_to_user").Scan(&value); err != nil || value != "preserve" {
			t.Fatalf("unidentified database value = %q, %v, want preserved", value, err)
		}
		var baselineTableCount int
		if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name = 'schema_baseline'").Scan(&baselineTableCount); err != nil {
			t.Fatalf("inspect schema_baseline: %v", err)
		}
		if baselineTableCount != 0 {
			t.Fatal("Crewfold mutated unidentified database with current baseline metadata")
		}
	})

	t.Run("tampered installed catalog", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		storage := openTestStore(t, dataDir, Options{})
		if _, err := storage.db.Exec("CREATE TABLE rogue_current_shape(value TEXT) STRICT"); err != nil {
			t.Fatalf("tamper installed catalog: %v", err)
		}
		if err := storage.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		_, err := Open(context.Background(), dataDir, Options{})
		if ErrorCode(err) != CodeCurrentBaselineMismatch {
			t.Fatalf("Open(tampered catalog) error = %v, code = %q", err, ErrorCode(err))
		}
	})

	t.Run("derived retrieval projection remains rebuildable", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		storage := openTestStore(t, dataDir, Options{})
		workspace, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
			Name: "personal", IdempotencyKey: "derived-projection-workspace", CorrelationID: "derived-projection-workspace-request",
		})
		if err != nil {
			t.Fatalf("InitWorkspace() error = %v", err)
		}
		before, err := storage.BaselineIdentity(context.Background())
		if err != nil {
			t.Fatalf("BaselineIdentity(before) error = %v", err)
		}
		if _, err := storage.db.Exec(`INSERT INTO knowledge_search(revision_id,workspace_id,title,body) VALUES ('probe','probe','probe','probe')`); err != nil {
			t.Fatalf("insert disposable retrieval row: %v", err)
		}
		if _, err := storage.db.Exec(`DELETE FROM knowledge_search WHERE revision_id='probe'`); err != nil {
			t.Fatalf("delete disposable retrieval row: %v", err)
		}
		afterMutation, err := storage.BaselineIdentity(context.Background())
		if err != nil || afterMutation != before {
			t.Fatalf("BaselineIdentity(after FTS rewrite) = %#v, %v, want %#v", afterMutation, err, before)
		}
		if _, err := storage.db.Exec("DROP TABLE knowledge_search"); err != nil {
			t.Fatalf("drop disposable retrieval projection: %v", err)
		}
		if err := storage.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}

		reopened, err := Open(context.Background(), dataDir, Options{})
		if err != nil {
			t.Fatalf("Open(with missing derived projection) error = %v", err)
		}
		defer reopened.Close()
		afterReopen, err := reopened.BaselineIdentity(context.Background())
		if err != nil || afterReopen != before {
			t.Fatalf("BaselineIdentity(after reopen) = %#v, %v, want %#v", afterReopen, err, before)
		}
		status, err := reopened.KnowledgeIndexStatus(context.Background(), workspace.Workspace.ID)
		if err != nil || status.Diagnosis != domain.KnowledgeIndexMissing {
			t.Fatalf("KnowledgeIndexStatus(missing projection) = %#v, %v", status, err)
		}
	})

	t.Run("user objects attached to derived tables remain catalog-bound", func(t *testing.T) {
		for _, test := range []struct {
			name      string
			statement string
		}{
			{name: "index", statement: `CREATE INDEX rogue_derived_index ON knowledge_search_metadata(generation)`},
			{name: "trigger", statement: `CREATE TRIGGER rogue_derived_trigger AFTER UPDATE ON knowledge_search_metadata BEGIN SELECT 1; END`},
		} {
			test := test
			t.Run(test.name, func(t *testing.T) {
				t.Parallel()
				dataDir := t.TempDir()
				storage := openTestStore(t, dataDir, Options{})
				if _, err := storage.db.Exec(test.statement); err != nil {
					t.Fatalf("create rogue derived %s: %v", test.name, err)
				}
				if err := storage.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
				_, err := Open(context.Background(), dataDir, Options{})
				if ErrorCode(err) != CodeCurrentBaselineMismatch {
					t.Fatalf("Open(rogue derived %s) error = %v, code = %q", test.name, err, ErrorCode(err))
				}
			})
		}
	})

	t.Run("existing empty database", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		databasePath := filepath.Join(dataDir, databaseFilename)
		file, err := os.OpenFile(databasePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("create empty database target: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close empty database target: %v", err)
		}
		_, err = Open(context.Background(), dataDir, Options{})
		if ErrorCode(err) != CodeCurrentBaselineMismatch {
			t.Fatalf("Open(empty existing target) error = %v, code = %q", err, ErrorCode(err))
		}
		info, statErr := os.Stat(databasePath)
		if statErr != nil {
			t.Fatalf("stat existing empty target: %v", statErr)
		}
		if info.Size() != 0 {
			t.Fatalf("existing empty target was adopted or replaced: size=%d", info.Size())
		}
	})
}

func TestSchemaBaselineIsImmutableAndFreshCreationIsAtomic(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	if _, err := storage.db.Exec("UPDATE schema_baseline SET source_sha256 = lower(hex(randomblob(32))) WHERE singleton=1"); err == nil {
		t.Fatal("UPDATE schema_baseline succeeded, want immutable identity")
	}
	if _, err := storage.db.Exec("DELETE FROM schema_baseline WHERE singleton=1"); err == nil {
		t.Fatal("DELETE schema_baseline succeeded, want immutable identity")
	}

	for _, stage := range []string{MutationAfterBaselineCatalog, MutationAfterBaselineIdentity, MutationBeforeBaselinePublish} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			injected := errors.New("interrupt baseline creation")
			_, err := Open(context.Background(), dataDir, Options{MutationHook: func(current string) error {
				if current == stage {
					return injected
				}
				return nil
			}})
			if !errors.Is(err, injected) {
				t.Fatalf("Open(interrupted %s) error = %v, want injected", stage, err)
			}
			for _, suffix := range []string{"", "-wal", "-shm"} {
				path := filepath.Join(dataDir, databaseFilename) + suffix
				if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("interrupted creation retained %s: %v", path, statErr)
				}
			}
			entries, readErr := os.ReadDir(dataDir)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("interrupted creation entries = %v, %v; want empty", entries, readErr)
			}
			reopened, openErr := Open(context.Background(), dataDir, Options{})
			if openErr != nil {
				t.Fatalf("Open(after interrupted %s) error = %v", stage, openErr)
			}
			_ = reopened.Close()
		})
	}

	t.Run(MutationAfterBaselinePublish, func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		injected := errors.New("interrupt after baseline publication")
		_, err := Open(context.Background(), dataDir, Options{MutationHook: func(current string) error {
			if current == MutationAfterBaselinePublish {
				return injected
			}
			return nil
		}})
		if !errors.Is(err, injected) {
			t.Fatalf("Open(interrupted after publication) error = %v, want injected", err)
		}
		databasePath := filepath.Join(dataDir, databaseFilename)
		if info, statErr := os.Lstat(databasePath); statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("published baseline missing or incomplete: info=%v error=%v", info, statErr)
		}
		reopened, openErr := Open(context.Background(), dataDir, Options{})
		if openErr != nil {
			t.Fatalf("Open(after published interruption) error = %v", openErr)
		}
		_ = reopened.Close()
	})

	t.Run("reopen reconciles deterministic process-loss stage", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		stage := filepath.Join(dataDir, databaseCreationStageName)
		for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
			if err := os.WriteFile(stage+suffix, []byte("interrupted"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		reopened, err := Open(context.Background(), dataDir, Options{})
		if err != nil {
			t.Fatalf("Open(after process-loss stage) error = %v", err)
		}
		defer reopened.Close()
		for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
			if _, err := os.Lstat(stage + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("reconciled stage %q remains: %v", stage+suffix, err)
			}
		}
		health, err := reopened.Health(context.Background())
		if err != nil || health.Status != "ok" {
			t.Fatalf("Health(after process-loss stage) = %#v, %v", health, err)
		}
	})
}

func openTestStore(t *testing.T, dataDir string, options Options) *Store {
	t.Helper()
	if options.RuntimeNodeID == "" {
		options.RuntimeNodeID = "11111111111111111111111111111111"
		options.RuntimeNodeFingerprint = "2222222222222222222222222222222222222222222222222222222222222222"
	}
	storage, err := Open(context.Background(), dataDir, options)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	return storage
}

func assertCounts(t *testing.T, storage *Store, workspaces, events, idempotency int) {
	t.Helper()
	for table, expected := range map[string]int{
		"workspaces":       workspaces,
		"events":           events,
		"idempotency_keys": idempotency,
	} {
		var count int
		if err := storage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != expected {
			t.Fatalf("%s count = %d, want %d", table, count, expected)
		}
	}
}
