package tui

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestM19SuccessfulMutationPollsAndClassifiesTheWholeEventInterval(t *testing.T) {
	workspace := m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
	events := []domain.Event{
		m19TimelineEvent(11, "approval.granted", "approval_request", "approval_1"),
		m19TimelineEvent(12, "task.blocked", "task", "task_1"),
		m19TimelineEvent(13, "approval.consumed", "approval_request", "approval_1"),
	}
	var fullIntervalCalls atomic.Int64
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		switch request.Method {
		case localapi.MethodWorkspaceShow:
			return localapi.WorkspaceShowResult{
				Schema: localapi.WorkspaceShowSchema, Type: "workspace", Workspace: workspace,
			}, nil
		case localapi.MethodEventsList:
			var params localapi.EventsListParams
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatalf("decode event request: %v", err)
			}
			switch {
			case params.After == 10 && params.Limit == 1:
				return m19EventPage(13, 3, events[:1], "head-continuation"), nil
			case params.After == 10 && params.Limit == eventPageSize:
				fullIntervalCalls.Add(1)
				return m19EventPage(13, 3, events, ""), nil
			case params.After == 13 && params.Limit == 1:
				return m19EventPage(13, 0, []domain.Event{}, ""), nil
			default:
				t.Fatalf("unexpected event request: %#v", params)
				return nil, nil
			}
		default:
			t.Fatalf("unexpected mutation catch-up method %q", request.Method)
			return nil, nil
		}
	})

	model := NewModel(Config{Workspace: workspace.ID, Color: ColorNever}, client)
	model.snapshot.Workspace = workspace
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
	model.actionGeneration = 7
	model.modal = modalState{Kind: modalReview, Review: actionReview{
		Choice:         actionChoice{Kind: actionAllowApproval, TargetType: "approval", TargetID: "approval_1", Revision: 2},
		IdempotencyKey: "ui-multi-event", Generation: 7, Executing: true, RequestFrozen: true,
	}}
	model.focus = FocusModal

	updated, scopeCommand := model.Update(actionCompletedMsg{
		Generation: 7, IdempotencyKey: "ui-multi-event", Kind: actionAllowApproval,
	})
	model = updated.(Model)
	if scopeCommand == nil || model.cursors.Applied != 10 || model.cursors.Candidate != 10 || !model.loadInFlight {
		t.Fatalf("successful mutation skipped its event interval: cursors=%#v load=%t command=%v", model.cursors, model.loadInFlight, scopeCommand)
	}

	model = m19ApplyScopeMessage(t, model, scopeCommand)
	model, fenceCommand := m19CompleteCanonicalSections(t, model)
	if model.cursors.Applied != 10 || fenceCommand == nil {
		t.Fatalf("canonical sections advanced before their event fence: cursors=%#v command=%v", model.cursors, fenceCommand)
	}
	fenceMessage := fenceCommand().(fenceLoadedMsg)
	updated, pollCommand := model.Update(fenceMessage)
	model = updated.(Model)
	if pollCommand == nil || model.connection != ConnectionSyncing || model.cursors.Applied != 10 {
		t.Fatalf("advanced head did not require event catch-up: connection=%v cursors=%#v command=%v", model.connection, model.cursors, pollCommand)
	}

	pollMessage := pollCommand().(eventsPolledMsg)
	if pollMessage.Err != nil || len(pollMessage.Events) != len(events) || pollMessage.After != 10 || pollMessage.Candidate != 13 {
		t.Fatalf("full mutation interval poll = %#v", pollMessage)
	}
	updated, nextScopeCommand := model.Update(pollMessage)
	model = updated.(Model)
	if nextScopeCommand == nil || model.cursors.Applied != 10 || len(model.loadSnapshot.Events) != 3 || len(model.loadSnapshot.Notifications) != 1 {
		t.Fatalf("polled interval was not staged behind canonical refresh: cursors=%#v events=%d notifications=%d command=%v",
			model.cursors, len(model.loadSnapshot.Events), len(model.loadSnapshot.Notifications), nextScopeCommand)
	}
	if model.loadSnapshot.Notifications[0].EventSequence != 12 {
		t.Fatalf("interleaved tracked notification = %#v, want event 12", model.loadSnapshot.Notifications)
	}

	model = m19ApplyScopeMessage(t, model, nextScopeCommand)
	model, fenceCommand = m19CompleteCanonicalSections(t, model)
	if model.cursors.Applied != 10 {
		t.Fatalf("second canonical batch advanced before its final fence: %#v", model.cursors)
	}
	fenceMessage = fenceCommand().(fenceLoadedMsg)
	updated, _ = model.Update(fenceMessage)
	model = updated.(Model)
	if model.connection != ConnectionLive || model.loadInFlight || model.cursors != (cursorState{Applied: 13, Candidate: 13, HighWater: 13}) {
		t.Fatalf("fenced mutation catch-up = connection:%v load:%t cursors:%#v", model.connection, model.loadInFlight, model.cursors)
	}
	if got := fullIntervalCalls.Load(); got != 1 {
		t.Fatalf("full event interval calls = %d, want exactly 1", got)
	}
	wantSequences := []int64{13, 12, 11}
	if len(model.snapshot.Events) != len(wantSequences) {
		t.Fatalf("published activity length = %d, want %d", len(model.snapshot.Events), len(wantSequences))
	}
	for index, want := range wantSequences {
		if model.snapshot.Events[index].Sequence != want {
			t.Fatalf("published activity sequences = %#v, want %#v", m19EventSequences(model.snapshot.Events), wantSequences)
		}
	}
	if len(model.snapshot.Notifications) != 1 || model.snapshot.Notifications[0].EventSequence != 12 {
		t.Fatalf("published notifications = %#v, want only tracked event 12", model.snapshot.Notifications)
	}
}

func TestM19FirstBootstrapEventRemainsStagedUntilCanonicalFence(t *testing.T) {
	workspace := m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
	event := m19TimelineEvent(1, "task.failed", "task", "task_1")
	model := NewModel(Config{Workspace: workspace.ID, Color: ColorNever}, nil)
	model.loadSnapshot = snapshot{Workspace: workspace}
	model.loadTarget = 0
	model.loadHighWater = 0

	updated, scopeCommand := model.Update(eventsPolledMsg{
		Generation: model.loadGeneration, After: 0, Fence: true,
		Events: []domain.Event{event}, Candidate: 1, HighWater: 1,
	})
	model = updated.(Model)
	if scopeCommand == nil || model.loadGeneration != 2 || model.cursors.Applied != 0 || len(model.snapshot.Events) != 0 || len(model.loadSnapshot.Events) != 1 {
		t.Fatalf("first-bootstrap event staging = generation:%d cursors:%#v published:%d staged:%d command:%v",
			model.loadGeneration, model.cursors, len(model.snapshot.Events), len(model.loadSnapshot.Events), scopeCommand)
	}

	updated, _ = model.Update(scopeLoadedMsg{
		Generation: model.loadGeneration, Workspace: workspace, TargetCursor: 1, HighWater: 1,
	})
	model = updated.(Model)
	if len(model.loadSnapshot.Events) != 1 || model.loadSnapshot.Events[0].EventID != event.EventID || len(model.snapshot.Events) != 0 {
		t.Fatalf("same-scope bootstrap restart lost or prematurely published activity: staged=%#v published=%#v", model.loadSnapshot.Events, model.snapshot.Events)
	}
	model, fenceCommand := m19CompleteCanonicalSections(t, model)
	if fenceCommand == nil || model.cursors.Applied != 0 || len(model.snapshot.Events) != 0 {
		t.Fatalf("bootstrap sections published before final fence: cursors=%#v published=%#v command=%v", model.cursors, model.snapshot.Events, fenceCommand)
	}
	updated, _ = model.Update(fenceLoadedMsg{Generation: model.loadGeneration, HighWater: 1})
	model = updated.(Model)
	if model.connection != ConnectionLive || model.cursors.Applied != 1 || len(model.snapshot.Events) != 1 ||
		model.snapshot.Events[0].EventID != event.EventID || len(model.snapshot.Notifications) != 1 {
		t.Fatalf("fenced first-bootstrap activity = connection:%v cursors:%#v events:%#v notifications:%#v",
			model.connection, model.cursors, model.snapshot.Events, model.snapshot.Notifications)
	}
}

func TestM19ContinuationInvalidCursorDiscardsTheWholePublishedCache(t *testing.T) {
	var calls atomic.Int64
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if calls.Add(1) == 1 {
			return m19EventPage(12, 2, []domain.Event{m19TransportEvent(11)}, "continuation"), nil
		}
		return nil, &localapi.APIError{Code: "invalid_cursor", Message: "journal was replaced", Retryable: false}
	})
	model := NewModel(Config{Workspace: m19TransportWorkspace, Color: ColorNever}, client)
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.snapshot.Workspace = domain.Workspace{ID: m19TransportWorkspace, Name: "personal"}
	model.snapshot.Tasks = []domain.TaskDetail{{Task: domain.Task{ID: "task_cached", WorkspaceID: m19TransportWorkspace, Title: "cached"}}}
	model.snapshot.Events = []domain.Event{m19TransportEvent(10)}
	model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
	model.pollInFlight = true
	model.pollActiveEpoch = 4

	message := pollEventsCmd(context.Background(), model.eventSlot, client, m19TransportWorkspace,
		model.loadGeneration, 10, false, 4)().(eventsPolledMsg)
	if message.Err != nil || !message.Rewind || len(message.Events) != 0 || message.Candidate != 0 {
		t.Fatalf("continuation rewind transport result = %#v", message)
	}
	updated, command := model.Update(message)
	model = updated.(Model)
	if command == nil || model.connection != ConnectionSyncing || !model.loadInFlight || model.cursors != (cursorState{}) ||
		model.snapshot.Workspace.ID != "" || len(model.snapshot.Tasks) != 0 || len(model.snapshot.Events) != 0 ||
		len(model.routeStack) != 1 || model.currentRoute() != RouteOverview || model.connection == ConnectionFatal {
		t.Fatalf("continuation rewind retained or fatally labeled cache: connection=%v load=%t cursors=%#v snapshot=%#v route=%#v command=%v",
			model.connection, model.loadInFlight, model.cursors, model.snapshot, model.routeStack, command)
	}
}

func TestM19TimelinePublishesOnlyAtExactAppliedFence(t *testing.T) {
	const taskID = "task_99999999999999999999999999999999"
	newModel := func() Model {
		model := NewModel(Config{Workspace: m19TransportWorkspace, Color: ColorNever}, nil)
		model.connection = ConnectionLive
		model.loadInFlight = false
		model.snapshot.Workspace = domain.Workspace{ID: m19TransportWorkspace, Name: "personal"}
		model.snapshot.Tasks = []domain.TaskDetail{{Task: domain.Task{ID: taskID, WorkspaceID: m19TransportWorkspace, Title: "timeline target"}}}
		model.snapshot.Events = []domain.Event{m19TransportEvent(10)}
		model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
		model.routeStack = []routeFrame{{Route: RouteWork}, {Route: RouteWork, EntityType: "task", EntityID: taskID}}
		model.focus = FocusDetail
		model.timelineEpoch = 5
		model.activeTimelineEpoch = 5
		return model
	}

	t.Run("exact fence publishes", func(t *testing.T) {
		model := newModel()
		event := m19TimelineEvent(10, "task.updated", "task", taskID)
		updated, command := model.Update(timelineLoadedMsg{
			Generation: model.loadGeneration, Epoch: 5, WorkspaceID: m19TransportWorkspace,
			EntityType: "task", EntityID: taskID,
			Timeline: entityTimeline{HighWater: 10, Events: []domain.Event{event}, Total: 1},
		})
		model = updated.(Model)
		timeline, found := model.entityTimelines[entityTimelineKey(m19TransportWorkspace, "task", taskID)]
		if command != nil || !found || timeline.HighWater != 10 || len(timeline.Events) != 1 || model.loadInFlight {
			t.Fatalf("exact-fence timeline = found:%t timeline:%#v load:%t command:%v", found, timeline, model.loadInFlight, command)
		}
	})

	t.Run("newer fence catches up then reloads", func(t *testing.T) {
		model := newModel()
		updated, command := model.Update(timelineLoadedMsg{
			Generation: model.loadGeneration, Epoch: 5, WorkspaceID: m19TransportWorkspace,
			EntityType: "task", EntityID: taskID, Timeline: entityTimeline{HighWater: 11},
		})
		model = updated.(Model)
		if command == nil || model.connection != ConnectionSyncing || !model.loadInFlight || model.cursors.Applied != 10 || len(model.entityTimelines) != 0 {
			t.Fatalf("newer timeline was published instead of triggering catch-up: connection=%v load=%t cursors=%#v timelines=%#v command=%v",
				model.connection, model.loadInFlight, model.cursors, model.entityTimelines, command)
		}

		updated, _ = model.Update(scopeLoadedMsg{
			Generation: model.loadGeneration, Workspace: model.snapshot.Workspace, TargetCursor: 10, HighWater: 11,
		})
		model = updated.(Model)
		model, _ = m19CompleteCanonicalSections(t, model)
		updated, pollCommand := model.Update(fenceLoadedMsg{Generation: model.loadGeneration, HighWater: 11})
		model = updated.(Model)
		if pollCommand == nil || model.connection != ConnectionSyncing || model.cursors.Applied != 10 || model.activeTimelineEpoch != 0 {
			t.Fatalf("newer timeline fence bypassed journal catch-up: connection=%v cursors=%#v timeline-epoch=%d command=%v",
				model.connection, model.cursors, model.activeTimelineEpoch, pollCommand)
		}

		updated, nextScope := model.Update(eventsPolledMsg{
			Generation: model.loadGeneration, After: 10, Fence: true,
			Events: []domain.Event{m19TimelineEvent(11, "task.updated", "task", taskID)}, Candidate: 11, HighWater: 11,
		})
		model = updated.(Model)
		if nextScope == nil || model.cursors.Applied != 10 {
			t.Fatalf("newer timeline event interval applied before refresh: cursors=%#v command=%v", model.cursors, nextScope)
		}
		updated, _ = model.Update(scopeLoadedMsg{
			Generation: model.loadGeneration, Workspace: model.snapshot.Workspace, TargetCursor: 11, HighWater: 11,
		})
		model = updated.(Model)
		model, _ = m19CompleteCanonicalSections(t, model)
		updated, reloadCommand := model.Update(fenceLoadedMsg{Generation: model.loadGeneration, HighWater: 11})
		model = updated.(Model)
		if reloadCommand == nil || model.connection != ConnectionLive || model.cursors.Applied != 11 || model.activeTimelineEpoch == 0 || len(model.entityTimelines) != 0 {
			t.Fatalf("timeline was not reloaded only after applied fence: connection=%v cursors=%#v active-epoch=%d timelines=%#v command=%v",
				model.connection, model.cursors, model.activeTimelineEpoch, model.entityTimelines, reloadCommand)
		}
	})

	for _, test := range []struct {
		name    string
		message timelineLoadedMsg
		fatal   bool
	}{
		{
			name: "older fence is a rewind",
			message: timelineLoadedMsg{Epoch: 5, WorkspaceID: m19TransportWorkspace, EntityType: "task", EntityID: taskID,
				Timeline: entityTimeline{HighWater: 9}},
		},
		{
			name: "continuation invalid cursor is a rewind",
			message: timelineLoadedMsg{Epoch: 5, WorkspaceID: m19TransportWorkspace, EntityType: "task", EntityID: taskID,
				Rewind: true},
		},
		{
			name: "unsupported event is fatal",
			message: timelineLoadedMsg{Epoch: 5, WorkspaceID: m19TransportWorkspace, EntityType: "task", EntityID: taskID,
				Err: &localapi.APIError{Code: "unsupported_operator_event", Message: "unsupported event", Retryable: false}},
			fatal: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newModel()
			test.message.Generation = model.loadGeneration
			updated, command := model.Update(test.message)
			model = updated.(Model)
			if model.snapshot.Workspace.ID != "" || len(model.snapshot.Tasks) != 0 || len(model.snapshot.Events) != 0 || model.cursors != (cursorState{}) {
				t.Fatalf("unsafe timeline result retained canonical cache: connection=%v cursors=%#v snapshot=%#v", model.connection, model.cursors, model.snapshot)
			}
			if test.fatal {
				if command != nil || model.connection != ConnectionFatal || model.loadInFlight {
					t.Fatalf("unsupported timeline event result = connection:%v load:%t command:%v", model.connection, model.loadInFlight, command)
				}
			} else if command == nil || model.connection != ConnectionSyncing || !model.loadInFlight {
				t.Fatalf("timeline rewind did not start full bootstrap: connection=%v load=%t command=%v", model.connection, model.loadInFlight, command)
			}
		})
	}
}

func TestM19AttachReturnSynchronizesAccumulatedEventsBeforeLiveState(t *testing.T) {
	workspace := m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
	events := []domain.Event{
		m19TimelineEvent(11, "run.blocked", "run", "run_1"),
		m19TimelineEvent(12, "run.progress_reported", "run", "run_1"),
	}
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		switch request.Method {
		case localapi.MethodWorkspaceShow:
			return localapi.WorkspaceShowResult{Schema: localapi.WorkspaceShowSchema, Type: "workspace", Workspace: workspace}, nil
		case localapi.MethodEventsList:
			var params localapi.EventsListParams
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			switch {
			case params.After == 10 && params.Limit == 1:
				return m19EventPage(12, 2, events[:1], "head-continuation"), nil
			case params.After == 10 && params.Limit == eventPageSize:
				return m19EventPage(12, 2, events, ""), nil
			case params.After == 12 && params.Limit == 1:
				return m19EventPage(12, 0, []domain.Event{}, ""), nil
			default:
				t.Fatalf("unexpected attach-return event request: %#v", params)
				return nil, nil
			}
		default:
			t.Fatalf("unexpected attach-return method %q", request.Method)
			return nil, nil
		}
	})

	model := NewModel(Config{Workspace: workspace.ID, Color: ColorNever}, client)
	model.snapshot.Workspace = workspace
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
	model.activeAttachEpoch = 6
	for _, route := range routes {
		model.dirty[route] = false
	}

	updated, scopeCommand := model.Update(attachFinishedMsg{Epoch: 6})
	model = updated.(Model)
	if scopeCommand == nil || model.connection != ConnectionSyncing || !model.loadInFlight || model.actionsReady() || model.cursors.Applied != 10 {
		t.Fatalf("attach return did not immediately disable actions and synchronize: connection=%v load=%t actions=%t cursors=%#v command=%v",
			model.connection, model.loadInFlight, model.actionsReady(), model.cursors, scopeCommand)
	}

	model = m19ApplyScopeMessage(t, model, scopeCommand)
	model, fenceCommand := m19CompleteCanonicalSections(t, model)
	fenceMessage := fenceCommand().(fenceLoadedMsg)
	updated, pollCommand := model.Update(fenceMessage)
	model = updated.(Model)
	if pollCommand == nil || model.connection != ConnectionSyncing || model.actionsReady() || model.cursors.Applied != 10 {
		t.Fatalf("attach-return head bypassed catch-up: connection=%v actions=%t cursors=%#v command=%v",
			model.connection, model.actionsReady(), model.cursors, pollCommand)
	}
	pollMessage := pollCommand().(eventsPolledMsg)
	updated, nextScope := model.Update(pollMessage)
	model = updated.(Model)
	if nextScope == nil || model.cursors.Applied != 10 || len(model.loadSnapshot.Events) != 2 {
		t.Fatalf("attach-return events applied before refresh: cursors=%#v staged=%#v command=%v", model.cursors, model.loadSnapshot.Events, nextScope)
	}

	model = m19ApplyScopeMessage(t, model, nextScope)
	model, fenceCommand = m19CompleteCanonicalSections(t, model)
	fenceMessage = fenceCommand().(fenceLoadedMsg)
	updated, _ = model.Update(fenceMessage)
	model = updated.(Model)
	if model.connection != ConnectionLive || model.loadInFlight || model.cursors.Applied != 12 || len(model.snapshot.Events) != 2 || len(model.snapshot.Notifications) != 1 {
		t.Fatalf("attach-return fenced state = connection:%v load:%t cursors:%#v events:%#v notifications:%#v",
			model.connection, model.loadInFlight, model.cursors, model.snapshot.Events, model.snapshot.Notifications)
	}
}

func m19ApplyScopeMessage(t *testing.T, model Model, command tea.Cmd) Model {
	t.Helper()
	message, ok := command().(scopeLoadedMsg)
	if !ok || message.Err != nil || message.Rewind || len(message.WorkspaceChoices) != 0 {
		t.Fatalf("scope command result = %#v", message)
	}
	updated, sectionCommand := model.Update(message)
	model = updated.(Model)
	if sectionCommand == nil || !model.loadInFlight || model.connection != ConnectionSyncing {
		t.Fatalf("scope result did not start canonical sections: connection=%v load=%t command=%v", model.connection, model.loadInFlight, sectionCommand)
	}
	return model
}

func m19CompleteCanonicalSections(t *testing.T, model Model) (Model, tea.Cmd) {
	t.Helper()
	applied := model.cursors.Applied
	var command tea.Cmd
	for index, section := range canonicalSections {
		if model.loadStates[section] != sectionLoading {
			t.Fatalf("canonical section %d/%d (%v) state = %v, want loading", index+1, len(canonicalSections), section, model.loadStates[section])
		}
		updated, next := model.Update(sectionLoadedMsg{Generation: model.loadGeneration, Section: section})
		model = updated.(Model)
		command = next
		if model.cursors.Applied != applied {
			t.Fatalf("canonical section %d/%d advanced Applied from %d to %d before the final fence",
				index+1, len(canonicalSections), applied, model.cursors.Applied)
		}
	}
	if command == nil {
		t.Fatal("last canonical section did not schedule the final event fence")
	}
	return model, command
}

func m19EventSequences(events []domain.Event) []int64 {
	result := make([]int64, len(events))
	for index, event := range events {
		result[index] = event.Sequence
	}
	return result
}
