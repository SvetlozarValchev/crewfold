package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestEntityInspectionLoadsAndRendersCanonicalTimeline(t *testing.T) {
	const taskID = "task_11111111111111111111111111111111"
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if request.Method != localapi.MethodEventsTimeline {
			t.Fatalf("method = %q, want %q", request.Method, localapi.MethodEventsTimeline)
		}
		var params localapi.EventsTimelineParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params.Workspace != m19TransportWorkspace || params.EntityType != "task" || params.EntityID != taskID {
			t.Fatalf("timeline params = %#v", params)
		}
		events := []domain.Event{
			m19TimelineEvent(12, "task.completed", "task", taskID),
			m19TimelineEvent(7, "task.updated", "task", taskID),
		}
		return m19TimelinePage(12, 2, events, ""), nil
	})

	model := NewModel(Config{}, client)
	model.snapshot.Workspace = domain.Workspace{ID: m19TransportWorkspace, Name: "personal"}
	model.cursors = cursorState{Applied: 12, Candidate: 12, HighWater: 12}
	model.snapshot.Tasks = []domain.TaskDetail{{Task: domain.Task{ID: taskID, Revision: 2, Title: "Build the game", Status: domain.TaskCompleted}}}
	model.routeStack = []routeFrame{{Route: RouteWork}}
	model.selection[RouteWork] = taskID
	model.focus = FocusRecords

	command := model.inspectSelected()
	if command == nil || model.currentFrame().EntityType != "task" || model.currentFrame().EntityID != taskID || model.focus != FocusDetail {
		t.Fatalf("inspect did not enter exact timeline detail: frame=%#v focus=%v command=%v", model.currentFrame(), model.focus, command)
	}
	message := command().(timelineLoadedMsg)
	updated, _ := model.Update(message)
	model = updated.(Model)
	timeline, ok := model.entityTimelines[entityTimelineKey(m19TransportWorkspace, "task", taskID)]
	if !ok || timeline.HighWater != 12 || timeline.Total != 2 || timeline.HasMore || len(timeline.Events) != 2 {
		t.Fatalf("timeline = %#v", timeline)
	}
	detail := strings.Join(model.detailContentLines(model.recordsFor(RouteWork)[0]), "\n")
	for _, want := range []string{"Entity timeline:", "2 of 2 events cached at high-water 12", "#12 task.completed", "#7 task.updated"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail missing %q:\n%s", want, detail)
		}
	}
}

func TestEntityTimelineUsesAtMostThreeBoundedPages(t *testing.T) {
	const taskID = "task_22222222222222222222222222222222"
	var calls atomic.Int32
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		var params localapi.EventsTimelineParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatal(err)
		}
		call := int(calls.Add(1))
		event := m19TimelineEvent(int64(101-call), "task.updated", "task", taskID)
		return m19TimelinePage(100, 100, []domain.Event{event}, fmt.Sprintf("page-%d", call+1)), nil
	})
	message := loadTimelineCmd(context.Background(), make(chan struct{}, 4), client, 4, 2,
		m19TransportWorkspace, "task", taskID)().(timelineLoadedMsg)
	if message.Err != nil {
		t.Fatal(message.Err)
	}
	if got := calls.Load(); got != maxCollectionPages {
		t.Fatalf("timeline calls = %d, want %d", got, maxCollectionPages)
	}
	if len(message.Timeline.Events) != maxCollectionPages || message.Timeline.Total != 100 || !message.Timeline.HasMore {
		t.Fatalf("bounded timeline = %#v", message.Timeline)
	}
}

func TestEventContinuationInvalidCursorRequestsJournalRewind(t *testing.T) {
	var calls atomic.Int32
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if calls.Add(1) == 1 {
			return m19EventPage(12, 2, []domain.Event{m19TransportEvent(11)}, "continuation"), nil
		}
		return nil, &localapi.APIError{Code: "invalid_cursor", Message: "journal was replaced", Retryable: false}
	})
	message := pollEventsCmd(context.Background(), make(chan struct{}, 1), client, m19TransportWorkspace, 3, 10, false, 8)().(eventsPolledMsg)
	if message.Err != nil || !message.Rewind {
		t.Fatalf("continuation result = %#v, want rewind without fatal error", message)
	}
}

func TestTimelineContinuationInvalidCursorRequestsJournalRewind(t *testing.T) {
	const taskID = "task_44444444444444444444444444444444"
	var calls atomic.Int32
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if calls.Add(1) == 1 {
			return m19TimelinePage(12, 2, []domain.Event{m19TimelineEvent(12, "task.updated", "task", taskID)}, "continuation"), nil
		}
		return nil, &localapi.APIError{Code: "invalid_cursor", Message: "journal was replaced", Retryable: false}
	})
	message := loadTimelineCmd(context.Background(), make(chan struct{}, 4), client, 2, 3,
		m19TransportWorkspace, "task", taskID)().(timelineLoadedMsg)
	if message.Err != nil || !message.Rewind {
		t.Fatalf("timeline continuation result = %#v, want rewind without background error", message)
	}
}

func TestSuccessfulActionKeepsAppliedAsEventCoverageLowerBound(t *testing.T) {
	model := NewModel(Config{Workspace: m19TransportWorkspace}, nil)
	model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
	model.modal = modalState{Kind: modalReview}
	updated, command := model.updateActionCompleted(actionCompletedMsg{Kind: actionAllowApproval})
	result := updated.(Model)
	if command == nil {
		t.Fatal("successful action did not start canonical catch-up")
	}
	if result.cursors.Applied != 10 || result.cursors.Candidate != 10 {
		t.Fatalf("action skipped event interval: cursors=%#v", result.cursors)
	}
}

func TestInitialBootstrapFenceEventsSurviveSameScopeRestart(t *testing.T) {
	model := NewModel(Config{Workspace: m19TransportWorkspace}, nil)
	model.loadSnapshot = snapshot{Workspace: domain.Workspace{ID: m19TransportWorkspace, Name: "personal"}}
	model.loadGeneration = 1
	model.loadInFlight = true
	model.loadTarget = 0
	model.loadHighWater = 0
	event := m19TimelineEvent(1, "task.failed", "task", "task_33333333333333333333333333333333")

	updated, command := model.updateFenceEvents(eventsPolledMsg{
		Generation: 1, After: 0, Fence: true, Events: []domain.Event{event}, Candidate: 1, HighWater: 1,
	})
	if command == nil {
		t.Fatal("fence event did not restart canonical loading")
	}
	model = updated.(Model)
	if model.loadGeneration != 2 {
		t.Fatalf("generation = %d, want 2", model.loadGeneration)
	}
	updated, _ = model.updateScopeLoaded(scopeLoadedMsg{
		Generation: 2, Workspace: domain.Workspace{ID: m19TransportWorkspace, Name: "personal"},
		TargetCursor: 1, HighWater: 1,
	})
	model = updated.(Model)
	if len(model.loadSnapshot.Events) != 1 || model.loadSnapshot.Events[0].EventID != event.EventID {
		t.Fatalf("staged first-bootstrap activity was lost: %#v", model.loadSnapshot.Events)
	}
	updated, _ = model.finishCanonicalLoad()
	model = updated.(Model)
	if len(model.snapshot.Events) != 1 || len(model.snapshot.Notifications) != 1 || model.cursors.Applied != 1 {
		t.Fatalf("published first-bootstrap event bundle = events %#v notifications %#v cursors %#v",
			model.snapshot.Events, model.snapshot.Notifications, model.cursors)
	}
}

func TestTimelinePublishesOnlyAtAppliedJournalFence(t *testing.T) {
	const taskID = "task_55555555555555555555555555555555"
	newModel := func() Model {
		model := NewModel(Config{Workspace: m19TransportWorkspace}, nil)
		model.snapshot.Workspace = domain.Workspace{ID: m19TransportWorkspace}
		model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
		model.routeStack = []routeFrame{{Route: RouteWork}, {Route: RouteWork, EntityType: "task", EntityID: taskID}}
		model.timelineEpoch = 2
		model.activeTimelineEpoch = 2
		return model
	}
	for _, test := range []struct {
		name      string
		highWater int64
	}{
		{name: "newer journal state", highWater: 11},
		{name: "journal rewind", highWater: 9},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newModel()
			updated, command := model.updateTimeline(timelineLoadedMsg{
				Generation: model.loadGeneration, Epoch: 2, WorkspaceID: m19TransportWorkspace,
				EntityType: "task", EntityID: taskID, Timeline: entityTimeline{HighWater: test.highWater},
			})
			result := updated.(Model)
			if command == nil || len(result.entityTimelines) != 0 || !result.loadInFlight {
				t.Fatalf("timeline fence mismatch was published: model=%#v command=%v", result, command)
			}
		})
	}
}

func TestTargetDrillFiltersBeforeCombinedScreenBound(t *testing.T) {
	model := NewModel(Config{}, nil)
	targets := make([]string, 400)
	for index := 0; index < 400; index++ {
		model.snapshot.Objectives = append(model.snapshot.Objectives, domain.Objective{ID: fmt.Sprintf("objective_%03d", index)})
		taskID := fmt.Sprintf("task_%03d", index)
		targets[index] = taskID
		model.snapshot.Tasks = append(model.snapshot.Tasks, domain.TaskDetail{Task: domain.Task{ID: taskID, Status: domain.TaskReady}})
	}
	model.snapshot.ObjectiveTotal = 400
	model.snapshot.TaskTotal = 400
	model.routeStack = []routeFrame{{Route: RouteOverview}, {Route: RouteWork, EntityType: "aggregate", EntityID: "ready", TargetIDs: targets}}
	records := model.recordsFor(RouteWork)
	if len(records) != len(targets) {
		t.Fatalf("drill returned %d records, want all %d exact producer IDs", len(records), len(targets))
	}
	for _, item := range records {
		if item.Kind != recordTask {
			t.Fatalf("drill leaked sibling/bound record: %#v", item)
		}
	}
}

func TestTimelineTargetUsesCanonicalEventEntityTypes(t *testing.T) {
	tests := []struct {
		item       record
		entityType string
		entityID   string
	}{
		{item: record{Kind: recordApproval, ID: "approval_1"}, entityType: "approval_request", entityID: "approval_1"},
		{item: record{Kind: recordDrift, ID: "drift_1"}, entityType: "claim_drift", entityID: "drift_1"},
		{item: record{Kind: recordNotification, ID: "notification:event_1", Notification: &notification{EntityType: "run", EntityID: "run_1"}}, entityType: "run", entityID: "run_1"},
		{item: record{Kind: recordEvent, ID: "event_1", Event: &domain.Event{Entity: domain.EventEntity{Type: "check_result", ID: "result_1"}}}, entityType: "check_result", entityID: "result_1"},
	}
	for _, test := range tests {
		entityType, entityID, ok := timelineTarget(test.item)
		if !ok || entityType != test.entityType || entityID != test.entityID {
			t.Fatalf("timelineTarget(%#v) = %q/%q/%t, want %q/%q/true", test.item.Kind, entityType, entityID, ok, test.entityType, test.entityID)
		}
	}
}

func m19TimelinePage(highWater, total int64, events []domain.Event, nextCursor string) localapi.EventsTimelineResult {
	if events == nil {
		events = []domain.Event{}
	}
	return localapi.EventsTimelineResult{
		Schema: localapi.EventsTimelineSchema, Type: "event_timeline", WorkspaceID: m19TransportWorkspace,
		HighWater: highWater, Events: events,
		PageResult: localapi.PageResult{NextCursor: nextCursor, HasMore: nextCursor != "", Total: total},
	}
}

func m19TimelineEvent(sequence int64, eventType, entityType, entityID string) domain.Event {
	event := m19TransportEvent(sequence)
	event.Type = eventType
	event.Entity = domain.EventEntity{Type: entityType, ID: entityID, Revision: 1}
	return event
}
