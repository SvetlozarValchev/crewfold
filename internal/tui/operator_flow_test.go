package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"crewfold/internal/domain"
)

func TestBriefingClaimInspectionLoadsAndValidatesExactExplanation(t *testing.T) {
	claim := domain.BriefingClaim{
		ID: "bclaim_1", Kind: domain.BriefingClaimRisk, Urgency: "high", Status: "open", Summary: "Exact risk",
		Sources:             []domain.BriefingClaimSource{{EntityType: "run", EntityID: "run_1", Revision: 4, EventSequence: 18}},
		SourceEventSequence: 18,
	}
	model := NewModel(Config{}, nil)
	model.width, model.height = 120, 32
	model.loadGeneration = 7
	model.snapshot.Workspace = domain.Workspace{ID: "ws_1"}
	model.snapshot.Briefing = domain.ManagementBriefing{ID: "brief_1", Claims: []domain.BriefingClaim{claim}}
	model.routeStack = []routeFrame{{Route: RouteBriefing}}
	model.selection[RouteBriefing] = claim.ID
	model.focus = FocusRecords

	command := model.inspectSelected()
	if command == nil || model.activeBriefingExplainEpoch == 0 || model.currentFrame().EntityType != "briefing_claim" || model.focus != FocusDetail {
		t.Fatalf("claim inspection did not start its bound explanation read: frame=%#v focus=%v epoch=%d", model.currentFrame(), model.focus, model.activeBriefingExplainEpoch)
	}
	explanation := domain.BriefingClaimExplanation{
		BriefingID: "brief_1", Claim: claim, EvaluatedAt: "2026-08-14T12:00:00Z",
		Provenance: append([]domain.BriefingClaimSource(nil), claim.Sources...),
		Diagnoses:  []string{"source freshness changed after the pinned cut"},
	}
	updated, next := model.updateBriefingExplanation(briefingExplainLoadedMsg{
		Generation: 7, Epoch: model.activeBriefingExplainEpoch, WorkspaceID: "ws_1", BriefingID: "brief_1", ClaimID: claim.ID,
		Explanation: explanation,
	})
	if next != nil {
		t.Fatal("validated explanation unexpectedly scheduled more I/O")
	}
	result := updated.(Model)
	stored, ok := result.briefingExplanations[briefingExplanationKey("brief_1", claim.ID)]
	if !ok || !reflect.DeepEqual(stored, explanation) {
		t.Fatalf("stored explanation = %#v, want exact %#v", stored, explanation)
	}
	explanation.Diagnoses[0] = "mutated after reducer"
	if stored.Diagnoses[0] == explanation.Diagnoses[0] {
		t.Fatal("reducer did not freeze explanation diagnoses")
	}
	detail := strings.Join(detailLines(result.recordsFor(RouteBriefing)[1]), "\n")
	if !strings.Contains(detail, "Explanation provenance:") || !strings.Contains(detail, "source freshness changed after the pinned cut") {
		t.Fatalf("detail did not render the exact explanation provenance/diagnosis:\n%s", detail)
	}

	mismatch := model
	mismatch.briefingExplanations = make(map[string]domain.BriefingClaimExplanation)
	mismatch.activeBriefingExplainEpoch = 99
	bad := cloneBriefingExplanation(stored)
	bad.Claim.Summary = "different claim"
	badResult, _ := mismatch.updateBriefingExplanation(briefingExplainLoadedMsg{
		Generation: 7, Epoch: 99, WorkspaceID: "ws_1", BriefingID: "brief_1", ClaimID: claim.ID, Explanation: bad,
	})
	rejected := badResult.(Model)
	if rejected.modal.Kind != modalError || len(rejected.briefingExplanations) != 0 {
		t.Fatalf("mismatched explanation was not rejected closed: modal=%v cache=%#v", rejected.modal.Kind, rejected.briefingExplanations)
	}
}

func TestApprovalReviewRequiresAndFreezesExactSupervisorAction(t *testing.T) {
	model, choice, action := approvalReviewFixture()
	message := actionPreparedMsg{
		Generation: model.actionGeneration, CanonicalGeneration: model.loadGeneration, WorkspaceID: model.snapshot.Workspace.ID,
		Choice: choice, SupervisorAction: action, HasSupervisorAction: true, IdempotencyKey: "ui-fixed-key",
	}
	updated, command := model.updateActionPrepared(message)
	if command != nil {
		t.Fatal("prepared review unexpectedly scheduled a mutation")
	}
	result := updated.(Model)
	if result.modal.Kind != modalReview || !result.modal.Review.HasApprovalContext {
		t.Fatalf("valid supervisor action did not open a review: %#v", result.modal)
	}
	if !reflect.DeepEqual(result.modal.Review.Approval, model.snapshot.Approvals[0]) || !reflect.DeepEqual(result.modal.Review.SupervisorAction, action) {
		t.Fatal("review did not freeze the exact approval and supervisor action")
	}
	message.SupervisorAction.Reasons[0] = "mutated after reducer"
	if result.modal.Review.SupervisorAction.Reasons[0] == message.SupervisorAction.Reasons[0] {
		t.Fatal("review shares mutable supervisor reasons with its async message")
	}
	content := strings.Join(result.reviewContentLines(80), "\n")
	for _, want := range []string{
		"Approval revision: 3", "Action revision: 5", "Expected action revision: 5",
		"Condition: blocked", "Response: resume_run", "Workspace ID: " + action.WorkspaceID, "Project ID: " + action.ProjectID,
		"Task ID: " + action.TaskID, "Run ID: " + action.RunID, "Reasons:", "blocked owner intervention",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("approval review content lacks %q:\n%s", want, content)
		}
	}
}

func TestApprovalReviewRejectsEveryStaleOrCrossScopeLink(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.SupervisorAction)
	}{
		{name: "action id", mutate: func(action *domain.SupervisorAction) { action.ID = "saction_other" }},
		{name: "action revision", mutate: func(action *domain.SupervisorAction) { action.Revision++ }},
		{name: "status", mutate: func(action *domain.SupervisorAction) { action.Status = domain.SupervisorActionApplied }},
		{name: "approval link", mutate: func(action *domain.SupervisorAction) { action.ApprovalID = "approval_other" }},
		{name: "workspace", mutate: func(action *domain.SupervisorAction) { action.WorkspaceID = "ws_other" }},
		{name: "project scope", mutate: func(action *domain.SupervisorAction) { action.ProjectID = "project_other" }},
		{name: "condition union", mutate: func(action *domain.SupervisorAction) { action.Condition = "future_condition" }},
		{name: "response union", mutate: func(action *domain.SupervisorAction) { action.Response = "future_response" }},
		{name: "condition hash", mutate: func(action *domain.SupervisorAction) { action.ConditionKey = "missing" }},
		{name: "content hash", mutate: func(action *domain.SupervisorAction) { action.ContentSHA256 = strings.Repeat("G", 64) }},
		{name: "policy revision", mutate: func(action *domain.SupervisorAction) { action.PolicyRevision = 0 }},
		{name: "target revision", mutate: func(action *domain.SupervisorAction) { action.EntityRevision = 0 }},
		{name: "reasons absent", mutate: func(action *domain.SupervisorAction) { action.Reasons = nil }},
		{name: "reason empty", mutate: func(action *domain.SupervisorAction) { action.Reasons = []string{""} }},
		{name: "constraint snapshot absent", mutate: func(action *domain.SupervisorAction) { action.ConstraintSnapshot = nil }},
		{name: "created time absent", mutate: func(action *domain.SupervisorAction) { action.CreatedAt = "" }},
		{name: "run target absent", mutate: func(action *domain.SupervisorAction) { action.RunID = "" }},
		{name: "schedule not approval executable", mutate: func(action *domain.SupervisorAction) { action.Response = domain.SupervisorResponseSchedule }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, choice, action := approvalReviewFixture()
			test.mutate(&action)
			updated, command := model.updateActionPrepared(actionPreparedMsg{
				Generation: model.actionGeneration, CanonicalGeneration: model.loadGeneration, WorkspaceID: model.snapshot.Workspace.ID,
				Choice: choice, SupervisorAction: action, HasSupervisorAction: true, IdempotencyKey: "ui-fixed-key",
			})
			if command != nil {
				t.Fatal("invalid approval action scheduled a command")
			}
			result := updated.(Model)
			if result.modal.Kind != modalError || result.modal.Review.HasApprovalContext {
				t.Fatalf("invalid approval action was confirmable: %#v", result.modal)
			}
		})
	}
}

func TestLongApprovalReviewRequiresDeterministicScrollAtSupportedSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{60, 18}, {80, 24}} {
		model, choice, action := approvalReviewFixture()
		model.width, model.height = size.width, size.height
		for index := 0; index < 30; index++ {
			action.Reasons = append(action.Reasons, strings.Repeat("reason exact ", 8))
		}
		updated, _ := model.updateActionPrepared(actionPreparedMsg{
			Generation: model.actionGeneration, CanonicalGeneration: model.loadGeneration, WorkspaceID: model.snapshot.Workspace.ID,
			Choice: choice, SupervisorAction: action, HasSupervisorAction: true, IdempotencyKey: "ui-fixed-key",
		})
		result := updated.(Model)
		result.actionNote.SetValue("owner reviewed the exact action")
		frame := result.render()
		for _, want := range []string{"Target: approval / " + choice.TargetID, "Approval revision: 3", "Condition: blocked", "Response: resume_run", "Consequence:", "Idempotency key: ui-fixed-key"} {
			if !strings.Contains(frame, want) {
				t.Fatalf("%dx%d first review page lacks %q:\n%s", size.width, size.height, want, frame)
			}
		}
		if !strings.Contains(frame, "confirmation locked") || strings.Contains(frame, "Ctrl+Enter confirm") {
			t.Fatalf("%dx%d overflowing review advertised unsafe confirmation:\n%s", size.width, size.height, frame)
		}
		blocked, command := result.updateModalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}), "ctrl+enter")
		if command != nil || blocked.(Model).modal.Review.Executing {
			t.Fatalf("%dx%d submitted before the full review was inspected", size.width, size.height)
		}
		result.modal.ReviewOffset = result.reviewMaxOffset()
		if !result.reviewFullyViewed() || !strings.Contains(result.footerText(), "Ctrl+Enter confirm") {
			t.Fatalf("%dx%d final review page did not unlock explicit confirmation", size.width, size.height)
		}
		submitted, command := result.updateModalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}), "ctrl+enter")
		if command == nil || !submitted.(Model).modal.Review.Executing {
			t.Fatalf("%dx%d complete review with owner note did not submit", size.width, size.height)
		}
	}
}

func approvalReviewFixture() (Model, actionChoice, domain.SupervisorAction) {
	workspaceID := "ws_" + strings.Repeat("1", 32)
	projectID := "prj_" + strings.Repeat("2", 32)
	objectiveID := "obj_" + strings.Repeat("3", 32)
	taskID := "task_" + strings.Repeat("4", 32)
	runID := "run_" + strings.Repeat("5", 32)
	agentID := "agent_" + strings.Repeat("6", 32)
	intentID := "sintent_" + strings.Repeat("7", 32)
	approvalID := "appr_" + strings.Repeat("8", 32)
	actionID := "saction_" + strings.Repeat("9", 32)
	now := "2026-08-14T12:00:00Z"
	approval := domain.ApprovalRequest{
		ID: approvalID, WorkspaceID: workspaceID, ActionID: actionID, Status: domain.ApprovalPending,
		ExpectedActionRevision: 5, Revision: 3, CreatedAt: now, UpdatedAt: now,
	}
	choice := actionChoice{
		Kind: actionAllowApproval, TargetType: "approval", TargetID: approval.ID, Revision: approval.Revision,
		RequiresNote: true, ApprovalActionID: approval.ActionID, ExpectedActionRevision: approval.ExpectedActionRevision,
	}
	action := domain.SupervisorAction{
		ID: actionID, WorkspaceID: workspaceID, ProjectID: projectID, ObjectiveID: objectiveID, TaskID: taskID, RunID: runID,
		AgentID: agentID, IntentID: intentID, Condition: domain.SupervisorConditionBlocked, ConditionKey: strings.Repeat("b", 64),
		Response: domain.SupervisorResponseResumeRun, Status: domain.SupervisorActionAwaitingApproval, EntityRevision: 8, PolicyRevision: 2,
		AsOfEventSequence: 44, Reasons: []string{"blocked owner intervention"}, ContentSHA256: strings.Repeat("a", 64),
		ConstraintSnapshot: map[string]any{"policy": "exact"}, ApprovalID: approval.ID, Revision: approval.ExpectedActionRevision,
		CreatedAt: now, UpdatedAt: now, CreatedBy: "subsystem:supervisor", UpdatedBy: "subsystem:supervisor",
	}
	model := NewModel(Config{Color: ColorNever}, nil)
	model.width, model.height = 80, 24
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.loadGeneration = 9
	model.actionGeneration = 4
	model.cursors = cursorState{Applied: 44, Candidate: 44, HighWater: 44}
	model.snapshot.Workspace = domain.Workspace{ID: workspaceID}
	model.snapshot.Project = &domain.Project{ID: projectID, WorkspaceID: workspaceID}
	model.snapshot.Approvals = []domain.ApprovalRequest{approval}
	model.modal = modalState{Kind: modalActions, Choices: []actionChoice{choice}}
	model.focus = FocusModal
	return model, choice, action
}
