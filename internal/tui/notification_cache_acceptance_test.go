package tui

import (
	"encoding/json"
	"testing"

	"crewfold/internal/domain"
)

func TestM19InitialBootstrapFenceKeepsNotificationOlderThanActivityCap(t *testing.T) {
	workspace := domain.Workspace{ID: m19TransportWorkspace, Name: "personal", Revision: 1}
	events := m19NotificationBeforeActivityBurst(1)
	model := NewModel(Config{Workspace: workspace.ID, Color: ColorNever}, nil)

	updated, _ := model.Update(scopeLoadedMsg{
		Generation: model.loadGeneration, Workspace: workspace, TargetCursor: 0, HighWater: 0,
	})
	model = updated.(Model)
	model, _ = m19CompleteCanonicalSections(t, model)
	updated, restart := model.Update(eventsPolledMsg{
		Generation: model.loadGeneration, After: 0, Fence: true,
		Events: events, Candidate: events[len(events)-1].Sequence, HighWater: events[len(events)-1].Sequence,
	})
	model = updated.(Model)
	if restart == nil || len(model.loadSnapshot.Events) != maxActivityEvents || len(model.loadSnapshot.Notifications) != 1 {
		t.Fatalf("bootstrap fence staging = events:%d notifications:%d command:%v",
			len(model.loadSnapshot.Events), len(model.loadSnapshot.Notifications), restart)
	}
	m19AssertActivityCacheHasNoData(t, model.loadSnapshot.Events)

	updated, _ = model.Update(scopeLoadedMsg{
		Generation: model.loadGeneration, Workspace: workspace,
		TargetCursor: events[len(events)-1].Sequence, HighWater: events[len(events)-1].Sequence,
	})
	model = updated.(Model)
	model, _ = m19CompleteCanonicalSections(t, model)
	updated, _ = model.Update(fenceLoadedMsg{Generation: model.loadGeneration, HighWater: events[len(events)-1].Sequence})
	model = updated.(Model)
	m19AssertPublishedNotificationBurst(t, model, events)
}

func TestM19SameScopeFenceKeepsNotificationOlderThanActivityCap(t *testing.T) {
	workspace := domain.Workspace{ID: m19TransportWorkspace, Name: "personal", Revision: 1}
	events := m19NotificationBeforeActivityBurst(11)
	model := NewModel(Config{Workspace: workspace.ID, Color: ColorNever}, nil)
	model.snapshot.Workspace = workspace
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
	model.restartCanonicalLoad(10)

	updated, _ := model.Update(scopeLoadedMsg{
		Generation: model.loadGeneration, Workspace: workspace, TargetCursor: 10, HighWater: 10,
	})
	model = updated.(Model)
	model, _ = m19CompleteCanonicalSections(t, model)
	updated, restart := model.Update(eventsPolledMsg{
		Generation: model.loadGeneration, After: 10, Fence: true,
		Events: events, Candidate: events[len(events)-1].Sequence, HighWater: events[len(events)-1].Sequence,
	})
	model = updated.(Model)
	if restart == nil || len(model.loadSnapshot.Events) != maxActivityEvents || len(model.loadSnapshot.Notifications) != 1 {
		t.Fatalf("same-scope fence staging = events:%d notifications:%d command:%v",
			len(model.loadSnapshot.Events), len(model.loadSnapshot.Notifications), restart)
	}
	m19AssertActivityCacheHasNoData(t, model.loadSnapshot.Events)

	updated, _ = model.Update(scopeLoadedMsg{
		Generation: model.loadGeneration, Workspace: workspace,
		TargetCursor: events[len(events)-1].Sequence, HighWater: events[len(events)-1].Sequence,
	})
	model = updated.(Model)
	model, _ = m19CompleteCanonicalSections(t, model)
	updated, _ = model.Update(fenceLoadedMsg{Generation: model.loadGeneration, HighWater: events[len(events)-1].Sequence})
	model = updated.(Model)
	m19AssertPublishedNotificationBurst(t, model, events)
}

func TestM19LivePollActivityCacheStripsOpaqueEventData(t *testing.T) {
	workspace := domain.Workspace{ID: m19TransportWorkspace, Name: "personal", Revision: 1}
	event := m19TransportEvent(11)
	event.Data = json.RawMessage(`{"opaque":"must not be retained"}`)
	model := NewModel(Config{Workspace: workspace.ID, Color: ColorNever}, nil)
	model.snapshot.Workspace = workspace
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
	model.pollInFlight = true
	model.pollActiveEpoch = 4

	updated, command := model.Update(eventsPolledMsg{
		Generation: model.loadGeneration, After: 10, PollEpoch: 4,
		Events: []domain.Event{event}, Candidate: 11, HighWater: 11,
	})
	model = updated.(Model)
	if command == nil || len(model.snapshot.Events) != 1 {
		t.Fatalf("live event did not stage bounded refresh: events=%#v command=%v", model.snapshot.Events, command)
	}
	got := model.snapshot.Events[0]
	if got.EventID != event.EventID || got.Sequence != event.Sequence || got.Type != event.Type || got.Entity != event.Entity {
		t.Fatalf("activity metadata changed while dropping data: got=%#v want=%#v", got, event)
	}
	m19AssertActivityCacheHasNoData(t, model.snapshot.Events)
}

func m19NotificationBeforeActivityBurst(firstSequence int64) []domain.Event {
	events := make([]domain.Event, maxActivityEvents+2)
	events[0] = m19TimelineEvent(firstSequence, "approval.requested", "approval_request", "approval_1")
	events[0].Data = json.RawMessage(`{"opaque":"tracked notification payload"}`)
	for index := 1; index < len(events); index++ {
		events[index] = m19TimelineEvent(firstSequence+int64(index), "task.updated", "task", "task_1")
		events[index].Data = json.RawMessage(`{"opaque":"ordinary activity payload"}`)
	}
	return events
}

func m19AssertPublishedNotificationBurst(t *testing.T, model Model, events []domain.Event) {
	t.Helper()
	lastSequence := events[len(events)-1].Sequence
	if model.connection != ConnectionLive || model.loadInFlight || model.cursors.Applied != lastSequence {
		t.Fatalf("fenced burst state = connection:%v load:%t cursors:%#v", model.connection, model.loadInFlight, model.cursors)
	}
	if len(model.snapshot.Events) != maxActivityEvents || model.snapshot.Events[0].Sequence != lastSequence ||
		model.snapshot.Events[len(model.snapshot.Events)-1].Sequence != events[2].Sequence {
		t.Fatalf("bounded activity sequences = len:%d first:%d last:%d",
			len(model.snapshot.Events), model.snapshot.Events[0].Sequence, model.snapshot.Events[len(model.snapshot.Events)-1].Sequence)
	}
	if len(model.snapshot.Notifications) != 1 || model.snapshot.Notifications[0].EventSequence != events[0].Sequence ||
		model.snapshot.Notifications[0].EventType != "approval.requested" {
		t.Fatalf("independent notification cache lost the pre-cap event: %#v", model.snapshot.Notifications)
	}
	m19AssertActivityCacheHasNoData(t, model.snapshot.Events)
}

func m19AssertActivityCacheHasNoData(t *testing.T, events []domain.Event) {
	t.Helper()
	for _, event := range events {
		if len(event.Data) != 0 {
			t.Fatalf("activity cache retained opaque data for event %s: %s", event.EventID, event.Data)
		}
	}
}
