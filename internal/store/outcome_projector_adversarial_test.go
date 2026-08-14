package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestOutcomeProjectorUnknownEventStopsAtPriorCursorWithDiagnostic(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	fixedTime := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	storage.clock = func() time.Time { return fixedTime }
	fixture.createCommitment(t, "projector-unknown-event")
	query := ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID,
		ScopeType:           domain.OwnerCheckpointTask,
		ScopeIdentifier:     fixture.task.Task.ID,
	}
	initial, err := storage.ShowManagementBriefing(context.Background(), query)
	if err != nil || !initial.CaughtUp || initial.EventCursor != initial.CutoffEventSequence {
		t.Fatalf("ShowManagementBriefing(initial) = %#v, %v", initial, err)
	}
	eventsBefore := countOutcomeFaultRows(t, storage, "events")
	unknownSequence := appendOutcomeProjectorFixtureEvent(t, storage, fixture.workspace.ID, "future.outcome_fact", "projector-unknown-event")
	unknown, err := storage.ShowManagementBriefing(context.Background(), query)
	if err != nil {
		t.Fatalf("ShowManagementBriefing(unknown event) = %v", err)
	}
	if unknown.CaughtUp || unknown.EventCursor != initial.EventCursor || unknown.CutoffEventSequence != unknownSequence ||
		unknown.UnknownEventType != "future.outcome_fact" || unknown.UnknownEventSequence != unknownSequence {
		t.Fatalf("unknown-event briefing = %#v; want stopped cursor %d and explicit diagnostic at %d", unknown, initial.EventCursor, unknownSequence)
	}
	if cursor := outcomeProjectorCursorForFaultTest(t, storage, fixture.workspace.ID); cursor != initial.EventCursor {
		t.Fatalf("durable cursor after unknown event = %d, want prior %d", cursor, initial.EventCursor)
	}
	if got := countOutcomeFaultRows(t, storage, "events"); got != eventsBefore+1 {
		t.Fatalf("briefing unknown-event read changed event count = %d, want %d", got, eventsBefore+1)
	}
	replayed, err := storage.ShowManagementBriefing(context.Background(), query)
	if err != nil || replayed.ID != unknown.ID || replayed.ContentSHA256 != unknown.ContentSHA256 ||
		replayed.EventCursor != initial.EventCursor || replayed.UnknownEventSequence != unknownSequence {
		t.Fatalf("ShowManagementBriefing(unknown replay) = %#v, %v; want stable diagnostic %#v", replayed, err, unknown)
	}
}

func TestOutcomeProjectorRestartsAfterCommittedFirstPageBeyondOneThousandEvents(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	fixedTime := time.Date(2032, 3, 4, 5, 6, 7, 0, time.UTC)
	clock := func() time.Time { return fixedTime }
	storage.clock = clock
	fixture.createCommitment(t, "projector-multipage-restart")
	for index := 0; index < 1001; index++ {
		appendOutcomeProjectorFixtureEvent(t, storage, fixture.workspace.ID, "run.progress_reported", "projector-multipage-restart")
	}
	var firstPageCursor, cutoff int64
	if err := storage.db.QueryRow(`SELECT sequence FROM events WHERE workspace_id=? ORDER BY sequence LIMIT 1 OFFSET 999`, fixture.workspace.ID).Scan(&firstPageCursor); err != nil {
		t.Fatalf("read expected first-page cursor = %v", err)
	}
	if err := storage.db.QueryRow(`SELECT MAX(sequence) FROM events WHERE workspace_id=?`, fixture.workspace.ID).Scan(&cutoff); err != nil {
		t.Fatalf("read projector cutoff = %v", err)
	}
	if cutoff <= firstPageCursor {
		t.Fatalf("fixture cutoff %d does not cross first page %d", cutoff, firstPageCursor)
	}
	beforeEvents := countOutcomeFaultRows(t, storage, "events")
	injected := errors.New("injected second outcome projector page")
	page := 0
	storage.mutationHook = func(stage string) error {
		if stage == MutationAfterBriefingCursor {
			page++
			if page == 2 {
				return injected
			}
		}
		return nil
	}
	query := ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID,
		ScopeType:           domain.OwnerCheckpointTask,
		ScopeIdentifier:     fixture.task.Task.ID,
	}
	if result, err := storage.ShowManagementBriefing(context.Background(), query); !errors.Is(err, injected) {
		t.Fatalf("ShowManagementBriefing(second-page fault) = %#v, %v; want injected fault", result, err)
	}
	if page != 2 {
		t.Fatalf("projector pages observed = %d, want second-page injection", page)
	}
	if cursor := outcomeProjectorCursorForFaultTest(t, storage, fixture.workspace.ID); cursor != firstPageCursor {
		t.Fatalf("cursor after second-page rollback = %d, want committed first page %d", cursor, firstPageCursor)
	}
	if count := countOutcomeFaultRows(t, storage, "management_briefings"); count != 0 {
		t.Fatalf("briefings after projector-page fault = %d, want none", count)
	}
	if got := countOutcomeFaultRows(t, storage, "events"); got != beforeEvents {
		t.Fatalf("projector fault changed events = %d, want %d", got, beforeEvents)
	}

	dataDirectory := filepath.Dir(storage.Path())
	if err := storage.Close(); err != nil {
		t.Fatalf("Close(after committed first page) = %v", err)
	}
	reopened, err := Open(context.Background(), dataDirectory, Options{Clock: clock})
	if err != nil {
		t.Fatalf("Open(after committed first page) = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	briefing, err := reopened.ShowManagementBriefing(context.Background(), query)
	if err != nil {
		t.Fatalf("ShowManagementBriefing(restart) = %v", err)
	}
	if !briefing.CaughtUp || briefing.EventCursor != cutoff || briefing.CutoffEventSequence != cutoff || briefing.UnknownEventType != "" {
		t.Fatalf("restarted multipage briefing = %#v; want caught up at %d", briefing, cutoff)
	}
	if got := countOutcomeFaultRows(t, reopened, "events"); got != beforeEvents {
		t.Fatalf("restarted projector changed events = %d, want %d", got, beforeEvents)
	}
}

func appendOutcomeProjectorFixtureEvent(t *testing.T, storage *Store, workspaceID, eventType, key string) int64 {
	t.Helper()
	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin projector fixture event = %v", err)
	}
	defer tx.Rollback()
	sequence, err := appendEvent(context.Background(), tx, workspaceID, "projector_fixture", "projector_fixture", 1,
		eventType, key, storage.nowText(), map[string]any{"fixture": key})
	if err != nil {
		t.Fatalf("append projector fixture event %q = %v", eventType, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit projector fixture event %q = %v", eventType, err)
	}
	return sequence
}
