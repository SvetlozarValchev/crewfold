package store

import (
	"context"
	"testing"
)

func TestAppendEventCanonicalizesNestedObjectsBeforeJournalInsert(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name: "canonical-event", IdempotencyKey: "canonical-event-workspace", CorrelationID: "canonical-event-workspace",
	})
	if err != nil {
		t.Fatalf("InitWorkspace() error = %v", err)
	}

	type deliberatelyNonLexical struct {
		Zeta  string `json:"zeta"`
		Alpha string `json:"alpha"`
	}
	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin event transaction: %v", err)
	}
	sequence, err := appendEvent(context.Background(), tx, workspace.Workspace.ID, "workspace", workspace.Workspace.ID,
		workspace.Workspace.Revision, "workspace.initialized", "canonical-event-nested", storage.nowText(),
		map[string]any{"outer": "value", "nested": deliberatelyNonLexical{Zeta: "z", Alpha: "a"}})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("appendEvent() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit event transaction: %v", err)
	}

	var data string
	if err := storage.db.QueryRow(`SELECT data_json FROM events WHERE sequence=?`, sequence).Scan(&data); err != nil {
		t.Fatalf("read event data: %v", err)
	}
	const want = `{"nested":{"alpha":"a","zeta":"z"},"outer":"value"}`
	if data != want {
		t.Fatalf("event data = %s, want exact canonical %s", data, want)
	}
	if !canonicalJSONObject([]byte(data)) {
		t.Fatal("append boundary stored data rejected by canonical verifier")
	}
}

func TestAppendEventRejectsNonObjectDataWithoutJournalWrite(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name: "event-object", IdempotencyKey: "event-object-workspace", CorrelationID: "event-object-workspace",
	})
	if err != nil {
		t.Fatalf("InitWorkspace() error = %v", err)
	}
	var before int
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&before); err != nil {
		t.Fatalf("count events before: %v", err)
	}
	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin event transaction: %v", err)
	}
	if _, err := appendEvent(context.Background(), tx, workspace.Workspace.ID, "workspace", workspace.Workspace.ID,
		workspace.Workspace.Revision, "workspace.initialized", "event-array", storage.nowText(), []string{"not", "an", "object"}); err == nil {
		_ = tx.Rollback()
		t.Fatal("appendEvent(non-object) succeeded")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback rejected event: %v", err)
	}
	var after int
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&after); err != nil {
		t.Fatalf("count events after: %v", err)
	}
	if after != before {
		t.Fatalf("events after rejected append = %d, want unchanged %d", after, before)
	}
}
