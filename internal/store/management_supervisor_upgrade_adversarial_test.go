package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// Schema 17's supervisor-state trigger calls a Crewfold SQLite function. The
// function must exist on the connection before migration creates/uses that
// trigger, and it must be installed again on every later reopen.
func TestSupervisorEventClassifierIsRegisteredAcrossUpgradeAndReopen(t *testing.T) {
	dataDir := t.TempDir()
	fixture, err := os.ReadFile("testdata/schema-v001.sql")
	if err != nil {
		t.Fatalf("read version-one upgrade fixture = %v", err)
	}
	database, err := sql.Open("sqlite3", filepath.Join(dataDir, databaseFilename))
	if err != nil {
		t.Fatalf("open version-one upgrade fixture = %v", err)
	}
	if _, err := database.Exec(string(fixture)); err != nil {
		_ = database.Close()
		t.Fatalf("apply version-one upgrade fixture = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close version-one upgrade fixture = %v", err)
	}

	assertClassifier := func(label string, storage *Store) {
		t.Helper()
		var known, unknown int
		if err := storage.db.QueryRow(`SELECT crewfold_supervisor_event_known('workspace.created'),crewfold_supervisor_event_known('workspace.future_authority_changed')`).Scan(&known, &unknown); err != nil {
			t.Fatalf("%s supervisor event classifier = %v", label, err)
		}
		if known != 1 || unknown != 0 {
			t.Fatalf("%s supervisor event classifier = %d/%d; want 1/0", label, known, unknown)
		}
		var triggerCount int
		if err := storage.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name='supervisor_state_validate_update'`).Scan(&triggerCount); err != nil || triggerCount != 1 {
			t.Fatalf("%s supervisor cursor trigger count = %d, %v; want 1", label, triggerCount, err)
		}
	}

	storage, err := Open(context.Background(), dataDir, Options{})
	if err != nil {
		t.Fatalf("upgrade version-one fixture through schema 17 = %v", err)
	}
	assertClassifier("upgrade connection", storage)
	if err := storage.Close(); err != nil {
		t.Fatalf("close upgraded store = %v", err)
	}

	storage, err = Open(context.Background(), dataDir, Options{})
	if err != nil {
		t.Fatalf("reopen schema-17 store = %v", err)
	}
	defer storage.Close()
	assertClassifier("reopened connection", storage)
}
