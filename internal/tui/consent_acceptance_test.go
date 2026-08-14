package tui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestM19StopReviewDisplaysAndSendsTheSameFrozenGracePeriod(t *testing.T) {
	const runID = "run_44444444444444444444444444444444"
	captured := make(chan localapi.RunStopParams, 1)
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if request.Method != localapi.MethodRunStop {
			t.Fatalf("method = %q, want %q", request.Method, localapi.MethodRunStop)
		}
		var params localapi.RunStopParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatalf("decode run.stop params: %v", err)
		}
		captured <- params
		return nil, &localapi.APIError{Code: "transport_lost", Message: "response deliberately lost", Retryable: true}
	})

	model := NewModel(Config{Workspace: m19TransportWorkspace, Color: ColorNever}, client)
	model.width, model.height = 80, 24
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.cursors = cursorState{Applied: 12, Candidate: 12, HighWater: 12}
	model.snapshot.Workspace = m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
	model.snapshot.Runs = []domain.RunSummary{
		{ID: runID, WorkspaceID: m19TransportWorkspace, Status: domain.RunActive, Revision: 7},
	}
	model.routeStack = []routeFrame{{Route: RouteWork}}
	model.selection[RouteWork] = runID
	model.focus = FocusRecords

	var choice actionChoice
	for _, candidate := range model.actionsForSelection() {
		if candidate.Kind == actionStopRun {
			choice = candidate
			break
		}
	}
	if choice.Kind != actionStopRun || choice.GracePeriodMillis != 5000 {
		t.Fatalf("canonical stop choice = %#v, want frozen 5000 ms grace", choice)
	}
	model.actionGeneration = 3
	model.modal = modalState{Kind: modalReview, Review: actionReview{
		Choice: choice, IdempotencyKey: "ui-stop-exact", Generation: model.actionGeneration,
	}}
	model.focus = FocusModal
	visibleReview := strings.Join(model.reviewContentLines(model.width), "\n")
	if !strings.Contains(visibleReview, "Grace period: 5000 ms") {
		t.Fatalf("stop review omitted the exact frozen grace period:\n%s", visibleReview)
	}
	model.modal.ReviewOffset = model.reviewMaxOffset()

	updated, command := model.updateModalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}), "ctrl+enter")
	if command == nil {
		t.Fatal("fully viewed exact stop review did not submit")
	}
	frozen := updated.(Model)
	if frozen.modal.Review.Choice.GracePeriodMillis != 5000 || !frozen.modal.Review.RequestFrozen {
		t.Fatalf("submitted stop review was not frozen exactly: %#v", frozen.modal.Review)
	}
	message, ok := command().(actionCompletedMsg)
	if !ok || message.Kind != actionStopRun {
		t.Fatalf("run.stop command result = %#v", message)
	}
	params := <-captured
	want := localapi.RunStopParams{
		Workspace: m19TransportWorkspace, Run: runID, ExpectedRevision: 7,
		GracePeriodMillis: 5000, IdempotencyKey: "ui-stop-exact",
	}
	if !reflect.DeepEqual(params, want) {
		t.Fatalf("run.stop raw params = %#v, want displayed frozen %#v", params, want)
	}
	if frozen.modal.Review.cancel != nil {
		frozen.modal.Review.cancel()
	}
}

func TestM19ApprovalNoteIsCanonicalBeforeFirstFreezeAndExactReplay(t *testing.T) {
	captured := make(chan localapi.ApprovalDecisionParams, 2)
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if request.Method != localapi.MethodApprovalAllow {
			t.Fatalf("method = %q, want %q", request.Method, localapi.MethodApprovalAllow)
		}
		var params localapi.ApprovalDecisionParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatalf("decode approval.allow params: %v", err)
		}
		captured <- params
		return nil, &localapi.APIError{Code: "transport_lost", Message: "response deliberately lost", Retryable: true}
	})

	model, choice, action := approvalReviewFixture()
	model.client = client
	prepared, command := model.updateActionPrepared(actionPreparedMsg{
		Generation: model.actionGeneration, CanonicalGeneration: model.loadGeneration, WorkspaceID: model.snapshot.Workspace.ID,
		Choice: choice, SupervisorAction: action, HasSupervisorAction: true, IdempotencyKey: "ui-note-exact",
	})
	if command != nil {
		t.Fatal("prepared approval review unexpectedly scheduled a mutation")
	}
	model = prepared.(Model)
	model.actionNote.SetValue("  exact owner reason  ")
	beforeFreeze := strings.Join(model.reviewContentLines(model.width), "\n")
	if !strings.Contains(beforeFreeze, "Owner decision note: exact owner reason") ||
		strings.Contains(beforeFreeze, "Owner decision note:   exact owner reason") {
		t.Fatalf("approval review did not display the canonical note before freeze:\n%s", beforeFreeze)
	}
	model.modal.ReviewOffset = model.reviewMaxOffset()

	firstUpdated, firstCommand := model.updateModalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}), "ctrl+enter")
	if firstCommand == nil {
		t.Fatal("canonical approval review did not submit")
	}
	first := firstUpdated.(Model)
	if first.modal.Review.DecisionNote != "exact owner reason" || !first.modal.Review.RequestFrozen {
		t.Fatalf("first approval request did not freeze the canonical note: %#v", first.modal.Review)
	}
	firstMessage, ok := firstCommand().(actionCompletedMsg)
	if !ok || firstMessage.Kind != actionAllowApproval || firstMessage.IdempotencyKey != "ui-note-exact" {
		t.Fatalf("first approval command result = %#v", firstMessage)
	}
	firstParams := <-captured
	if firstParams.DecisionNote != first.modal.Review.DecisionNote {
		t.Fatalf("first raw note %q differs from rendered frozen note %q", firstParams.DecisionNote, first.modal.Review.DecisionNote)
	}

	ambiguousValue, reconnectCommand := first.Update(firstMessage)
	if reconnectCommand == nil {
		t.Fatal("lost first response did not retain an ambiguous replay path")
	}
	ambiguous := ambiguousValue.(Model)
	ambiguous.connection = ConnectionLive
	ambiguous.loadInFlight = false
	ambiguous.cursors = cursorState{Applied: 44, Candidate: 44, HighWater: 44}
	ambiguous.snapshot.Approvals = nil
	for route := range ambiguous.dirty {
		ambiguous.dirty[route] = false
	}
	ambiguous.modal.ReviewOffset = ambiguous.reviewMaxOffset()
	frozenView := strings.Join(ambiguous.reviewContentLines(ambiguous.width), "\n")
	if !strings.Contains(frozenView, "Owner decision note: exact owner reason") {
		t.Fatalf("ambiguous review changed the frozen note:\n%s", frozenView)
	}

	replayedValue, replayCommand := ambiguous.updateModalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}), "ctrl+enter")
	if replayCommand == nil {
		t.Fatal("live synchronized dashboard blocked exact frozen approval replay")
	}
	replayed := replayedValue.(Model)
	replayMessage, ok := replayCommand().(actionCompletedMsg)
	if !ok || replayMessage.Kind != actionAllowApproval || replayMessage.IdempotencyKey != "ui-note-exact" {
		t.Fatalf("approval replay command result = %#v", replayMessage)
	}
	replayParams := <-captured
	if !reflect.DeepEqual(replayParams, firstParams) || replayed.modal.Review.DecisionNote != firstParams.DecisionNote {
		t.Fatalf("approval replay changed frozen request: first=%#v replay=%#v review-note=%q", firstParams, replayParams, replayed.modal.Review.DecisionNote)
	}
	if replayed.modal.Review.cancel != nil {
		replayed.modal.Review.cancel()
	}
}
