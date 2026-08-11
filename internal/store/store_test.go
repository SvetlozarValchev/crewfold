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
)

func TestOpenCreatesHealthyMigratedOwnerOnlyDatabase(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	storage := openTestStore(t, dataDir, Options{})
	health, err := storage.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if health.Status != "ok" || health.SchemaVersion != LatestSchemaVersion {
		t.Fatalf("Health() = %#v, want healthy schema %d", health, LatestSchemaVersion)
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

	var migrationCount int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("count schema migrations: %v", err)
	}
	if migrationCount != LatestSchemaVersion {
		t.Fatalf("migration count = %d, want %d", migrationCount, LatestSchemaVersion)
	}
}

func TestMigrationUpgradesCheckedInVersionZeroFixture(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	fixture, err := os.ReadFile("testdata/schema-v000.sql")
	if err != nil {
		t.Fatalf("os.ReadFile(fixture) error = %v", err)
	}
	database, err := sql.Open("sqlite3", filepath.Join(dataDir, databaseFilename))
	if err != nil {
		t.Fatalf("sql.Open(fixture) error = %v", err)
	}
	if _, err := database.Exec(string(fixture)); err != nil {
		_ = database.Close()
		t.Fatalf("apply fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	storage := openTestStore(t, dataDir, Options{})
	health, err := storage.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() after migration error = %v", err)
	}
	if health.SchemaVersion != LatestSchemaVersion {
		t.Fatalf("schema version = %d, want %d", health.SchemaVersion, LatestSchemaVersion)
	}
	if _, err := storage.Workspace(context.Background(), "missing"); ErrorCode(err) != CodeWorkspaceNotFound {
		t.Fatalf("Workspace(missing) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestMigrationUpgradesCheckedInVersionOneFixture(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	fixture, err := os.ReadFile("testdata/schema-v001.sql")
	if err != nil {
		t.Fatalf("os.ReadFile(fixture) error = %v", err)
	}
	database, err := sql.Open("sqlite3", filepath.Join(dataDir, databaseFilename))
	if err != nil {
		t.Fatalf("sql.Open(fixture) error = %v", err)
	}
	if _, err := database.Exec(string(fixture)); err != nil {
		_ = database.Close()
		t.Fatalf("apply fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	storage := openTestStore(t, dataDir, Options{})
	health, err := storage.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() after migration error = %v", err)
	}
	if health.SchemaVersion != LatestSchemaVersion {
		t.Fatalf("schema version = %d, want %d", health.SchemaVersion, LatestSchemaVersion)
	}
	for _, table := range []string{"projects", "repositories", "project_repositories", "checkouts"} {
		var count int
		if err := storage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("query migrated table %s: %v", table, err)
		}
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

	events, err := storage.Events(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
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
	for index, name := range []string{"alpha", "beta"} {
		if _, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
			Name:           name,
			IdempotencyKey: "key-" + name,
			CorrelationID:  "request-" + name,
		}); err != nil {
			t.Fatalf("InitWorkspace(%d) error = %v", index, err)
		}
	}
	events, err := storage.Events(context.Background(), 1, 100)
	if err != nil {
		t.Fatalf("Events(after 1) error = %v", err)
	}
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
	events, err := storage.Events(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 1 || events[0].EventID != created.EventID || events[0].Type != workspaceCreated {
		t.Fatalf("events after rejected mutations = %#v, want original event", events)
	}
}

func TestDatabaseSymlinkAndNewerSchemaAreRefused(t *testing.T) {
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
		_, err = Open(context.Background(), dataDir, Options{})
		if ErrorCode(err) != CodeStorageFailed {
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
		_, err = Open(context.Background(), dataDir, Options{})
		if ErrorCode(err) != CodeStorageFailed {
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
		var migrationTableCount int
		if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name = 'schema_migrations'").Scan(&migrationTableCount); err != nil {
			t.Fatalf("inspect schema_migrations: %v", err)
		}
		if migrationTableCount != 0 {
			t.Fatal("Crewfold mutated unidentified database with migration metadata")
		}
	})

	t.Run("tampered migration metadata", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		storage := openTestStore(t, dataDir, Options{})
		if _, err := storage.db.Exec("UPDATE schema_migrations SET name = 'not-the-embedded-migration' WHERE version = 1"); err != nil {
			t.Fatalf("tamper migration metadata: %v", err)
		}
		if err := storage.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		_, err := Open(context.Background(), dataDir, Options{})
		if ErrorCode(err) != CodeStorageFailed {
			t.Fatalf("Open(tampered metadata) error = %v, code = %q", err, ErrorCode(err))
		}
	})
}

func openTestStore(t *testing.T, dataDir string, options Options) *Store {
	t.Helper()
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
