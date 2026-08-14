package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

const m19AttachRunID = "run_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestM19AttachResponseMustMatchFrozenReviewedRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		reviewedRunID string
		result        localapi.RunAttachResult
	}{
		{
			name:   "wrong run",
			result: localapi.RunAttachResult{Schema: localapi.RunAttachSchema, Type: "run_attach", RunID: "run_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Executable: "/bin/true"},
		},
		{
			name:   "wrong discriminator",
			result: localapi.RunAttachResult{Schema: localapi.RunAttachSchema, Type: "run_detail", RunID: m19AttachRunID, Executable: "/bin/true"},
		},
		{
			name:   "schema-invalid result run",
			result: localapi.RunAttachResult{Schema: localapi.RunAttachSchema, Type: "run_attach", RunID: "run_A", Executable: "/bin/true"},
		},
		{
			name:          "schema-invalid reviewed run",
			reviewedRunID: "run_A",
			result:        localapi.RunAttachResult{Schema: localapi.RunAttachSchema, Type: "run_attach", RunID: "run_A", Executable: "/bin/true"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := m19ExecutingReviewModel(actionAttachRun, "")
			if test.reviewedRunID != "" {
				model.modal.Review.Choice.TargetID = test.reviewedRunID
			}
			model.modal.Review.Generation = 7
			model.actionGeneration = 7
			updatedValue, command := model.Update(attachReadyMsg{Generation: 7, Result: test.result})
			if command != nil {
				t.Fatal("mismatched attach result returned an ExecProcess command")
			}
			updated := updatedValue.(Model)
			if updated.activeAttachEpoch != 0 || updated.modal.Kind != modalError || updated.lastError == "" {
				t.Fatalf("mismatched attach result did not fail closed: modal=%#v active=%d lastError=%q", updated.modal, updated.activeAttachEpoch, updated.lastError)
			}
		})
	}
}

func TestM19AttachmentCommandPreservesExactArgvAndSafeEnvironmentWithoutShell(t *testing.T) {
	t.Setenv("M19_ATTACH_INHERITED", "retained")
	t.Setenv("M19_ATTACH_OVERRIDE", "old")
	result := localapi.RunAttachResult{
		Schema: localapi.RunAttachSchema, Type: "run_attach", RunID: m19AttachRunID, Runtime: "herdr",
		Executable: "/usr/bin/herdr",
		Arguments:  []string{"terminal", "attach", "term with spaces", "; touch /tmp/must-not-exist"},
		Environment: map[string]string{
			"M19_ATTACH_OVERRIDE": "new", "M19_ATTACH_EXTRA": "exact value",
		},
	}
	command, err := attachmentCommand(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := append([]string{result.Executable}, result.Arguments...)
	if command.Path != result.Executable || !reflect.DeepEqual(command.Args, wantArgs) {
		t.Fatalf("attach command path/argv = %q/%#v, want %q/%#v", command.Path, command.Args, result.Executable, wantArgs)
	}
	if len(command.Args) >= 2 && (command.Args[0] == "/bin/sh" || command.Args[0] == "sh" || command.Args[1] == "-c") {
		t.Fatalf("attach command introduced a shell: %#v", command.Args)
	}
	values := map[string][]string{}
	for _, entry := range command.Env {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = append(values[name], value)
		}
	}
	for name, want := range map[string]string{
		"M19_ATTACH_INHERITED": "retained", "M19_ATTACH_OVERRIDE": "new", "M19_ATTACH_EXTRA": "exact value",
	} {
		if got := values[name]; !reflect.DeepEqual(got, []string{want}) {
			t.Fatalf("attach environment %s = %#v, want exactly [%q]", name, got, want)
		}
	}

	invalid := []localapi.RunAttachResult{
		{},
		{Executable: "bad\x00path"},
		{Executable: "/bin/true", Arguments: []string{"bad\x00argument"}},
		{Executable: "/bin/true", Environment: map[string]string{"": "value"}},
		{Executable: "/bin/true", Environment: map[string]string{"BAD=NAME": "value"}},
		{Executable: "/bin/true", Environment: map[string]string{"BAD": "value\x00tail"}},
	}
	for index, attachment := range invalid {
		if _, err := attachmentCommand(context.Background(), attachment); err == nil {
			t.Errorf("invalid attachment %d accepted: %#v", index, attachment)
		}
	}

	model := m19ExecutingReviewModel(actionAttachRun, "")
	model.modal.Review.Generation = 7
	model.width, model.height = 80, 24
	result.Environment["CREWFOLD_PRIVATE_SECRET"] = "do-not-render"
	updatedValue, execCommand := model.Update(attachReadyMsg{Generation: 7, Result: result})
	if execCommand == nil {
		t.Fatal("valid exact attach did not return ExecProcess")
	}
	frame := updatedValue.(Model).render()
	if strings.Contains(frame, "do-not-render") || strings.Contains(frame, "CREWFOLD_PRIVATE_SECRET") || strings.Contains(frame, "term with spaces") {
		t.Fatalf("attach environment or opaque terminal handle leaked into render:\n%s", frame)
	}
}

func TestM19CanceledAttachCanNeverLaunchAndSecondControlCQuits(t *testing.T) {
	t.Parallel()
	model := m19ExecutingReviewModel(actionAttachRun, "")
	model.modal.Review.Generation = 7
	model.actionGeneration = 7
	cancelContext, cancel := context.WithCancel(context.Background())
	model.modal.Review.cancel = cancel

	canceledValue, command := model.updateModalKey(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}), "ctrl+c")
	if command != nil {
		t.Fatal("first Ctrl+C returned a command while canceling an executing attach")
	}
	canceled := canceledValue.(Model)
	if !canceled.modal.Review.CancelRequested || !canceled.modal.Review.Executing {
		t.Fatalf("first Ctrl+C review = %#v, want executing cancellation requested", canceled.modal.Review)
	}
	select {
	case <-cancelContext.Done():
	default:
		t.Fatal("first Ctrl+C did not cancel the in-flight attach request context")
	}

	readyValue, command := canceled.Update(attachReadyMsg{
		Generation: 7,
		Result: localapi.RunAttachResult{
			RunID: m19AttachRunID, Executable: "/bin/true",
		},
	})
	if command != nil {
		t.Fatal("a successful response to the canceled attach returned an ExecProcess command")
	}
	ready := readyValue.(Model)
	if ready.activeAttachEpoch != 0 || ready.modal.Kind != modalReview || ready.modal.Review.Executing ||
		!ready.modal.Review.CancelRequested || ready.statusLine == "Attaching to run "+m19AttachRunID {
		t.Fatalf("canceled attach response changed into a launch: modal=%#v active=%d status=%q", ready.modal, ready.activeAttachEpoch, ready.statusLine)
	}

	_, command = ready.updateModalKey(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}), "ctrl+c")
	if command == nil {
		t.Fatal("second Ctrl+C after an attach cancellation did not request quit")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("second Ctrl+C command returned %T, want tea.QuitMsg", command())
	}
}

func TestM19StaleBackgroundErrorsCannotEraseExecutingMutationReview(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(*Model)
		message tea.Msg
	}{
		{
			name: "old attach completion",
			prepare: func(model *Model) {
				model.activeAttachEpoch = 9
			},
			message: attachFinishedMsg{Epoch: 8, Err: errors.New("old attach failed")},
		},
		{
			name: "inactive inbox failure",
			prepare: func(model *Model) {
				model.activeInboxEpoch = 4
			},
			message: inboxLoadedMsg{
				Generation: 1, Epoch: 3, WorkspaceID: "ws_1", AgentID: "agent_old", Err: errors.New("old inbox failed"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := m19ExecutingReviewModel(actionStopRun, "ui-fixed-key")
			model.statusLine = "newer mutation remains active"
			cancelContext, cancel := context.WithCancel(context.Background())
			model.modal.Review.cancel = cancel
			test.prepare(&model)

			updatedValue, command := model.Update(test.message)
			if command != nil {
				t.Fatal("stale background result returned a command")
			}
			updated := updatedValue.(Model)
			if updated.modal.Kind != modalReview || !updated.modal.Review.Executing ||
				updated.modal.Review.IdempotencyKey != "ui-fixed-key" || updated.modal.Review.cancel == nil ||
				updated.statusLine != "newer mutation remains active" {
				t.Fatalf("stale result erased or changed executing review: modal=%#v status=%q", updated.modal, updated.statusLine)
			}
			select {
			case <-cancelContext.Done():
				t.Fatal("stale background result canceled the newer mutation")
			default:
			}
			cancel()
		})
	}
}

func TestM19NewestInboxResultCannotBeOverwrittenByOlderEpoch(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{}, nil)
	model.loadGeneration = 5
	model.snapshot.Workspace = domain.Workspace{ID: "ws_1"}
	model.routeStack = []routeFrame{{Route: RouteCoordination}, {Route: RouteCoordination, EntityType: "agent_inbox", EntityID: "agent_a"}}
	model.focus = FocusRecords
	model.activeInboxEpoch = 2

	newItem := domain.InboxItem{Message: domain.Message{ID: "message_new"}}
	newValue, command := model.Update(inboxLoadedMsg{
		Generation: 5, Epoch: 2, WorkspaceID: "ws_1", AgentID: "agent_a", Items: []domain.InboxItem{newItem},
	})
	if command != nil {
		t.Fatal("new inbox result returned a command")
	}
	newest := newValue.(Model)
	if got := newest.inboxes["agent_a"]; len(got) != 1 || got[0].Message.ID != "message_new" {
		t.Fatalf("new inbox result = %#v", got)
	}

	oldItem := domain.InboxItem{Message: domain.Message{ID: "message_old"}}
	oldValue, command := newest.Update(inboxLoadedMsg{
		Generation: 5, Epoch: 1, WorkspaceID: "ws_1", AgentID: "agent_a", Items: []domain.InboxItem{oldItem},
	})
	if command != nil {
		t.Fatal("old inbox result returned a command")
	}
	final := oldValue.(Model)
	if got := final.inboxes["agent_a"]; len(got) != 1 || got[0].Message.ID != "message_new" {
		t.Fatalf("old inbox result overwrote newest cache: %#v", got)
	}
}

func TestM19MatchingBackgroundCompletionPreservesForegroundModal(t *testing.T) {
	t.Parallel()
	t.Run("inbox success caches beneath help", func(t *testing.T) {
		model := NewModel(Config{}, nil)
		model.loadGeneration = 5
		model.snapshot.Workspace = domain.Workspace{ID: "ws_1"}
		model.routeStack = []routeFrame{{Route: RouteCoordination}, {Route: RouteCoordination, EntityType: "agent_inbox", EntityID: "agent_a"}}
		model.focus, model.modalReturnFocus = FocusModal, FocusRecords
		model.modal = modalState{Kind: modalHelp}
		model.activeInboxEpoch = 2
		updatedValue, command := model.Update(inboxLoadedMsg{
			Generation: 5, Epoch: 2, WorkspaceID: "ws_1", AgentID: "agent_a",
			Items: []domain.InboxItem{{Message: domain.Message{ID: "message_new"}}},
		})
		if command != nil {
			t.Fatal("inbox success returned a command")
		}
		updated := updatedValue.(Model)
		if updated.modal.Kind != modalHelp || updated.focus != FocusModal || updated.activeInboxEpoch != 0 ||
			len(updated.inboxes["agent_a"]) != 1 || updated.inboxes["agent_a"][0].Message.ID != "message_new" {
			t.Fatalf("matching inbox success clobbered overlay or cache: modal=%v focus=%v epoch=%d cache=%#v", updated.modal.Kind, updated.focus, updated.activeInboxEpoch, updated.inboxes)
		}
	})

	t.Run("inbox error diagnoses beneath executing review", func(t *testing.T) {
		model := m19ExecutingReviewModel(actionStopRun, "ui-fixed-key")
		model.routeStack = []routeFrame{{Route: RouteCoordination}, {Route: RouteCoordination, EntityType: "agent_inbox", EntityID: "agent_a"}}
		model.activeInboxEpoch = 2
		cancelContext, cancel := context.WithCancel(context.Background())
		model.modal.Review.cancel = cancel
		updatedValue, command := model.Update(inboxLoadedMsg{
			Generation: model.loadGeneration, Epoch: 2, WorkspaceID: "ws_1", AgentID: "agent_a", Err: errors.New("inbox unavailable"),
		})
		if command != nil {
			t.Fatal("inbox error returned a command")
		}
		updated := updatedValue.(Model)
		if updated.modal.Kind != modalReview || !updated.modal.Review.Executing || updated.modal.Review.IdempotencyKey != "ui-fixed-key" ||
			updated.lastError != "inbox unavailable" || !strings.Contains(updated.statusLine, "inbox unavailable") {
			t.Fatalf("matching inbox error clobbered review or lost diagnosis: modal=%#v last=%q status=%q", updated.modal, updated.lastError, updated.statusLine)
		}
		select {
		case <-cancelContext.Done():
			t.Fatal("inbox error canceled executing review")
		default:
		}
		cancel()
	})

	t.Run("attach error diagnoses beneath executing review and schedules catchup", func(t *testing.T) {
		model := m19ExecutingReviewModel(actionStopRun, "ui-fixed-key")
		model.activeAttachEpoch = 8
		cancelContext, cancel := context.WithCancel(context.Background())
		model.modal.Review.cancel = cancel
		updatedValue, command := model.Update(attachFinishedMsg{Epoch: 8, Err: errors.New("attached child exited 7")})
		if command == nil {
			t.Fatal("matching attach completion did not schedule event catch-up")
		}
		updated := updatedValue.(Model)
		if updated.activeAttachEpoch != 0 || updated.modal.Kind != modalReview || !updated.modal.Review.Executing ||
			updated.modal.Review.IdempotencyKey != "ui-fixed-key" || updated.lastError != "attached child exited 7" ||
			!strings.Contains(updated.statusLine, "attached child exited 7") {
			t.Fatalf("matching attach completion clobbered review or lost diagnosis: modal=%#v active=%d last=%q status=%q", updated.modal, updated.activeAttachEpoch, updated.lastError, updated.statusLine)
		}
		select {
		case <-cancelContext.Done():
			t.Fatal("attach completion canceled executing review")
		default:
		}
		cancel()
	})
}

func TestM19ModalCancellationRestoresInputsAndPriorFocus(t *testing.T) {
	t.Parallel()
	t.Run("filter Ctrl+C restores old value", func(t *testing.T) {
		model := NewModel(Config{}, nil)
		model.focus = FocusModal
		model.modalReturnFocus = FocusDetail
		model.filterText = "new filter"
		model.filter.SetValue("new filter")
		model.filter.Focus()
		model.modal = modalState{Kind: modalFilter, Message: "old filter"}

		updatedValue, command := model.updateModalKey(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}), "ctrl+c")
		if command != nil {
			t.Fatal("filter cancellation returned a command")
		}
		updated := updatedValue.(Model)
		if updated.modal.Kind != modalNone || updated.focus != FocusDetail || updated.filterText != "old filter" ||
			updated.filter.Value() != "old filter" || updated.filter.Focused() {
			t.Fatalf("filter cancellation = modal:%v focus:%v text:%q value:%q focused:%t", updated.modal.Kind, updated.focus, updated.filterText, updated.filter.Value(), updated.filter.Focused())
		}
	})

	for _, test := range []struct {
		name string
		key  string
		msg  tea.KeyPressMsg
	}{
		{name: "review Esc", key: "esc", msg: tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})},
		{name: "review Ctrl+C", key: "ctrl+c", msg: tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})},
	} {
		t.Run(test.name+" blurs owner note", func(t *testing.T) {
			model := NewModel(Config{}, nil)
			model.focus = FocusModal
			model.modalReturnFocus = FocusRecords
			model.actionNote.Focus()
			model.modal = modalState{Kind: modalReview, Review: actionReview{
				Choice: actionChoice{Kind: actionDenyApproval, RequiresNote: true},
			}}
			updatedValue, command := model.updateModalKey(test.msg, test.key)
			if command != nil {
				t.Fatal("unsubmitted review cancellation returned a command")
			}
			updated := updatedValue.(Model)
			if updated.modal.Kind != modalNone || updated.focus != FocusRecords || updated.actionNote.Focused() {
				t.Fatalf("review cancellation = modal:%v focus:%v note-focused:%t", updated.modal.Kind, updated.focus, updated.actionNote.Focused())
			}
		})
	}
}

func TestM19AggregateEnterPushesExactlyOneCompleteDrillRoute(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{}, nil)
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
	model.width, model.height = 120, 32
	model.focus = FocusRecords
	model.snapshot.Tasks = []domain.TaskDetail{
		{Task: domain.Task{ID: "task_ready", Status: domain.TaskReady}},
		{Task: domain.Task{ID: "task_blocked", Status: domain.TaskBlocked}},
	}
	model.selection[RouteOverview] = "summary:tasks"

	updatedValue, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command != nil {
		t.Fatal("aggregate inspection unexpectedly started I/O")
	}
	updated := updatedValue.(Model)
	if len(updated.routeStack) != 2 {
		t.Fatalf("aggregate Enter route depth = %d, want exactly 2: %#v", len(updated.routeStack), updated.routeStack)
	}
	wantFrame := routeFrame{
		Route: RouteWork, EntityType: "aggregate", EntityID: "summary:tasks",
		TargetIDs: []string{"task_ready", "task_blocked"},
	}
	frame := updated.currentFrame()
	if frame.Route != wantFrame.Route || frame.EntityType != wantFrame.EntityType || frame.EntityID != wantFrame.EntityID ||
		!reflect.DeepEqual(frame.TargetIDs, wantFrame.TargetIDs) || updated.focus != FocusRecords {
		t.Fatalf("aggregate drill frame = %#v focus=%v, want %#v/records", frame, updated.focus, wantFrame)
	}
	records := updated.recordsFor(RouteWork)
	ids := make([]string, len(records))
	for index, item := range records {
		ids[index] = item.ID
	}
	if !reflect.DeepEqual(ids, wantFrame.TargetIDs) {
		t.Fatalf("aggregate drill records = %#v, want exact %#v", ids, wantFrame.TargetIDs)
	}
}

func TestM19AmbiguousApprovalReplayUsesOnlyExactFrozenRequestAfterRefresh(t *testing.T) {
	t.Parallel()
	model, choice, action := approvalReviewFixture()
	preparedValue, _ := model.updateActionPrepared(actionPreparedMsg{
		Generation: model.actionGeneration, CanonicalGeneration: model.loadGeneration, WorkspaceID: model.snapshot.Workspace.ID,
		Choice: choice, SupervisorAction: action, HasSupervisorAction: true, IdempotencyKey: "ui-fixed-key",
	})
	prepared := preparedValue.(Model)
	prepared.modal.Review.DecisionNote = "exact frozen owner note"
	prepared.modal.Review.RequestFrozen = true
	prepared.modal.Review.Executing = true
	prepared.modal.ReviewOffset = prepared.reviewMaxOffset()

	ambiguousValue, command := prepared.Update(actionCompletedMsg{
		Generation: prepared.modal.Review.Generation, IdempotencyKey: "ui-fixed-key", Kind: actionAllowApproval,
		Err: &localapi.APIError{Code: "transport_lost", Message: "response lost after write", Retryable: true},
	})
	if command == nil {
		t.Fatal("ambiguous response did not schedule reconnect")
	}
	ambiguous := ambiguousValue.(Model)
	if ambiguous.modal.Kind != modalReview || ambiguous.modal.Review.Executing || !ambiguous.modal.Review.RequestFrozen ||
		ambiguous.modal.Review.IdempotencyKey != "ui-fixed-key" || ambiguous.modal.Review.DecisionNote != "exact frozen owner note" {
		t.Fatalf("ambiguous response did not retain exact frozen review: %#v", ambiguous.modal)
	}

	// A committed first request legitimately removes the pending approval from
	// the refreshed projection. That mutable absence must not rewrite or block
	// the one exact idempotent replay.
	ambiguous.connection = ConnectionLive
	ambiguous.loadInFlight = false
	ambiguous.cursors = cursorState{Applied: 55, Candidate: 55, HighWater: 55}
	ambiguous.snapshot.Approvals = nil
	for route := range ambiguous.dirty {
		ambiguous.dirty[route] = false
	}
	ambiguous.modal.ReviewOffset = ambiguous.reviewMaxOffset()
	frozenBefore := ambiguous.modal.Review
	replayedValue, replayCommand := ambiguous.updateModalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}), "ctrl+enter")
	if replayCommand == nil {
		t.Fatal("live synchronized dashboard blocked exact frozen replay after approval disappeared")
	}
	replayed := replayedValue.(Model)
	if !replayed.modal.Review.Executing || replayed.modal.Review.IdempotencyKey != frozenBefore.IdempotencyKey ||
		replayed.modal.Review.DecisionNote != frozenBefore.DecisionNote || !reflect.DeepEqual(replayed.modal.Review.Approval, frozenBefore.Approval) ||
		!reflect.DeepEqual(replayed.modal.Review.SupervisorAction, frozenBefore.SupervisorAction) {
		t.Fatalf("replay mutated frozen approval request: before=%#v after=%#v", frozenBefore, replayed.modal.Review)
	}
	if replayed.modal.Review.cancel != nil {
		replayed.modal.Review.cancel()
	}
}

func TestM19ScopeChangeInvalidatesFrozenAmbiguousRequest(t *testing.T) {
	t.Parallel()
	model, choice, action := approvalReviewFixture()
	preparedValue, _ := model.updateActionPrepared(actionPreparedMsg{
		Generation: model.actionGeneration, CanonicalGeneration: model.loadGeneration, WorkspaceID: model.snapshot.Workspace.ID,
		Choice: choice, SupervisorAction: action, HasSupervisorAction: true, IdempotencyKey: "ui-fixed-key",
	})
	prepared := preparedValue.(Model)
	prepared.modal.Review.RequestFrozen = true
	prepared.modal.Review.Executing = false
	prepared.modal.Review.DecisionNote = "exact frozen owner note"
	prepared.modal.Review.AmbiguousError = "response lost"
	prepared.loadInFlight = true
	previousGeneration := prepared.actionGeneration

	updatedValue, _ := prepared.updateScopeLoaded(scopeLoadedMsg{
		Generation: prepared.loadGeneration,
		Workspace:  domain.Workspace{ID: "ws_" + strings.Repeat("a", 32), Name: "replacement"},
		Project:    &domain.Project{ID: "prj_" + strings.Repeat("b", 32), WorkspaceID: "ws_" + strings.Repeat("a", 32)},
		HighWater:  60, TargetCursor: 60,
	})
	updated := updatedValue.(Model)
	if updated.modal.Kind != modalError || updated.modal.Review.IdempotencyKey != "" || updated.actionGeneration <= previousGeneration ||
		!strings.Contains(updated.modal.Message, "invalidated") || !strings.Contains(updated.modal.Message, "prior outcome is unknown") {
		t.Fatalf("scope change retained or hid frozen request: modal=%#v action-generation=%d", updated.modal, updated.actionGeneration)
	}
}

func m19ExecutingReviewModel(kind actionKind, idempotencyKey string) Model {
	model := NewModel(Config{Color: ColorNever}, nil)
	model.width, model.height = 80, 24
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.snapshot.Workspace = domain.Workspace{ID: "ws_1"}
	model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
	model.focus = FocusModal
	model.modalReturnFocus = FocusRecords
	targetID := "run_1"
	if kind == actionAttachRun {
		targetID = m19AttachRunID
	}
	model.modal = modalState{Kind: modalReview, Review: actionReview{
		Choice:         actionChoice{Kind: kind, TargetType: "run", TargetID: targetID, Revision: 4},
		IdempotencyKey: idempotencyKey, Generation: 1, Executing: true, RequestFrozen: true,
	}}
	return model
}
