package localapi

import (
	"context"
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestM19RunMutationResponsesBindMethodOutcomeAndRevision(t *testing.T) {
	t.Parallel()
	const expectedRevision int64 = 7
	workspaceID := "ws_" + strings.Repeat("1", 32)
	runID := "run_" + strings.Repeat("2", 32)
	tests := []struct {
		name     string
		method   string
		status   string
		revision int64
		grace    int64
		valid    bool
	}{
		{name: "resume exact", method: MethodRunResume, status: domain.RunActive, revision: expectedRevision + 1, valid: true},
		{name: "resume swapped stopping outcome", method: MethodRunResume, status: domain.RunStopping, revision: expectedRevision + 1, grace: 250},
		{name: "resume wrong revision", method: MethodRunResume, status: domain.RunActive, revision: expectedRevision + 2},
		{name: "stop exact", method: MethodRunStop, status: domain.RunStopping, revision: expectedRevision + 1, grace: 250, valid: true},
		{name: "stop swapped active outcome", method: MethodRunStop, status: domain.RunActive, revision: expectedRevision + 1},
		{name: "stop wrong grace", method: MethodRunStop, status: domain.RunStopping, revision: expectedRevision + 1, grace: 251},
		{name: "stop wrong revision", method: MethodRunStop, status: domain.RunStopping, revision: expectedRevision + 2, grace: 250},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := RunMutationResult{
				Schema: RunMutationSchema, Type: "run_mutation",
				Detail:        m19MutationRunDetail(workspaceID, runID, test.status, test.revision, test.grace),
				EventSequence: 12,
			}
			call := func(client *Client) error {
				if test.method == MethodRunResume {
					_, err := client.RunResume(context.Background(), RunResumeParams{
						Workspace: workspaceID, Run: runID, ExpectedRevision: expectedRevision, IdempotencyKey: "resume-exact",
					})
					return err
				}
				_, err := client.RunStop(context.Background(), RunStopParams{
					Workspace: workspaceID, Run: runID, ExpectedRevision: expectedRevision,
					GracePeriodMillis: 250, IdempotencyKey: "stop-exact",
				})
				return err
			}
			err := capturePortableResultError(t, test.method, call, result)
			if test.valid && err != nil {
				t.Fatalf("%s rejected exact governed outcome: %v", test.method, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("%s accepted swapped or stale governed outcome: %#v", test.method, result.Detail.Run)
			}
		})
	}
}

func TestM19ApprovalMutationResponsesBindMethodOutcomeRevisionAndDecision(t *testing.T) {
	t.Parallel()
	const expectedRevision int64 = 3
	workspaceID := "ws_" + strings.Repeat("1", 32)
	projectID := "prj_" + strings.Repeat("2", 32)
	approvalID := "appr_" + strings.Repeat("3", 32)
	tests := []struct {
		name             string
		method           string
		approvalStatus   string
		actionStatus     string
		approvalRevision int64
		actionRevision   int64
		note             string
		valid            bool
	}{
		{
			name: "allow exact", method: MethodApprovalAllow,
			approvalStatus: domain.ApprovalConsumed, actionStatus: domain.SupervisorActionApplied,
			approvalRevision: expectedRevision + 2, actionRevision: 6, note: "allow exact action", valid: true,
		},
		{
			name: "allow swapped denied outcome", method: MethodApprovalAllow,
			approvalStatus: domain.ApprovalDenied, actionStatus: domain.SupervisorActionDismissed,
			approvalRevision: expectedRevision + 1, actionRevision: 6, note: "allow exact action",
		},
		{
			name: "allow wrong approval revision", method: MethodApprovalAllow,
			approvalStatus: domain.ApprovalConsumed, actionStatus: domain.SupervisorActionApplied,
			approvalRevision: expectedRevision + 1, actionRevision: 6, note: "allow exact action",
		},
		{
			name: "deny exact", method: MethodApprovalDeny,
			approvalStatus: domain.ApprovalDenied, actionStatus: domain.SupervisorActionDismissed,
			approvalRevision: expectedRevision + 1, actionRevision: 6, note: "deny exact action", valid: true,
		},
		{
			name: "deny swapped applied outcome", method: MethodApprovalDeny,
			approvalStatus: domain.ApprovalConsumed, actionStatus: domain.SupervisorActionApplied,
			approvalRevision: expectedRevision + 2, actionRevision: 6, note: "deny exact action",
		},
		{
			name: "deny wrong action revision", method: MethodApprovalDeny,
			approvalStatus: domain.ApprovalDenied, actionStatus: domain.SupervisorActionDismissed,
			approvalRevision: expectedRevision + 1, actionRevision: 7, note: "deny exact action",
		},
		{
			name: "deny changed decision note", method: MethodApprovalDeny,
			approvalStatus: domain.ApprovalDenied, actionStatus: domain.SupervisorActionDismissed,
			approvalRevision: expectedRevision + 1, actionRevision: 6, note: "different note",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestNote := "allow exact action"
			if test.method == MethodApprovalDeny {
				requestNote = "deny exact action"
			}
			result := m19MutationApprovalResult(
				workspaceID, projectID, approvalID, test.approvalStatus, test.actionStatus,
				test.approvalRevision, test.actionRevision, test.note,
			)
			call := func(client *Client) error {
				params := ApprovalDecisionParams{
					Workspace: workspaceID, Approval: approvalID, ExpectedRevision: expectedRevision,
					DecisionNote: requestNote, IdempotencyKey: "approval-exact",
				}
				if test.method == MethodApprovalAllow {
					_, err := client.ApprovalAllow(context.Background(), params)
					return err
				}
				_, err := client.ApprovalDeny(context.Background(), params)
				return err
			}
			err := capturePortableResultError(t, test.method, call, result)
			if test.valid && err != nil {
				t.Fatalf("%s rejected exact governed outcome: %v", test.method, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("%s accepted swapped or stale governed outcome: approval=%#v action=%#v", test.method, result.Approval, result.Action)
			}
		})
	}
}

func m19MutationRunDetail(workspaceID, runID, status string, revision, grace int64) domain.RunDetail {
	projectID := "prj_" + strings.Repeat("3", 32)
	taskID := "task_" + strings.Repeat("4", 32)
	agentID := "agent_" + strings.Repeat("5", 32)
	checkoutID := "co_" + strings.Repeat("6", 32)
	assignmentID := "asg_" + strings.Repeat("7", 32)
	now := "2026-08-14T12:00:00Z"
	return domain.RunDetail{
		Run: domain.Run{
			ID: runID, WorkspaceID: workspaceID, ProjectID: projectID, TaskID: taskID,
			AssignmentID: assignmentID, AgentID: agentID, CheckoutID: checkoutID,
			Runtime: "fake", Provider: "fake", ScenarioName: "operator-test", Status: status,
			StopGraceMillis: grace, Revision: revision, CreatedAt: now, UpdatedAt: now,
			CreatedBy: "local-owner", UpdatedBy: "local-owner",
			Placement: domain.RunPlacement{
				TaskID: taskID, AgentID: agentID, CheckoutID: checkoutID, CheckoutPath: "/tmp/operator-test",
				WriteMode: "exclusive", Runtime: "fake", Provider: "fake", Reasons: []string{"exact test placement"},
			},
		},
		Task: domain.Task{
			ID: taskID, WorkspaceID: workspaceID, ProjectID: projectID, Title: "Operator action task",
			TaskClass: "implementation", Status: domain.TaskActive, Budget: domain.Budget{}, Revision: 2,
			CreatedAt: now, UpdatedAt: now, CreatedBy: "local-owner", UpdatedBy: "local-owner",
		},
		Agent: domain.AgentDefinition{
			ID: agentID, WorkspaceID: workspaceID, Name: "operator-agent", Role: "developer",
			Provider: "fake", Runtime: "fake", Enabled: true, MaxConcurrency: 1, Revision: 1,
			CreatedAt: now, UpdatedAt: now, CreatedBy: "local-owner", UpdatedBy: "local-owner",
		},
		Checkout: domain.Checkout{
			ID: checkoutID, ProjectID: projectID, RepositoryID: "repo_" + strings.Repeat("8", 32),
			Path: "/tmp/operator-test", WriteMode: "exclusive", Revision: 1,
			Availability: "available", CheckoutKind: "standalone", DirtyPaths: []string{}, ObservedAt: now,
			CreatedAt: now, UpdatedAt: now, CreatedBy: "local-owner", UpdatedBy: "local-owner",
		},
		Timeline: []domain.RunTimelineEntry{},
	}
}

func m19MutationApprovalResult(workspaceID, projectID, approvalID, approvalStatus, actionStatus string, approvalRevision, actionRevision int64, note string) ApprovalMutationResult {
	actionID := "saction_" + strings.Repeat("4", 32)
	now := "2026-08-14T12:00:00Z"
	return ApprovalMutationResult{
		Schema: ApprovalMutationSchema, Type: "approval_mutation", EventSequence: 23,
		Approval: domain.ApprovalRequest{
			ID: approvalID, WorkspaceID: workspaceID, ProjectID: projectID, ActionID: actionID,
			Status: approvalStatus, DecisionNote: note, DecisionEventSequence: 21,
			ExpectedActionRevision: 5, Revision: approvalRevision,
			CreatedAt: now, UpdatedAt: now, DecidedAt: now,
			CreatedBy: "subsystem:supervisor", UpdatedBy: "local-owner", DecidedBy: "local-owner",
		},
		Action: domain.SupervisorAction{
			ID: actionID, WorkspaceID: workspaceID, ProjectID: projectID,
			Condition: domain.SupervisorConditionBlocked, ConditionKey: strings.Repeat("a", 64),
			Response: domain.SupervisorResponseRequestOwner, Status: actionStatus, Decision: note,
			EntityRevision: 4, PolicyRevision: 1, AsOfEventSequence: 20,
			Reasons: []string{"exact owner decision required"}, ConstraintSnapshot: map[string]any{},
			ContentSHA256: strings.Repeat("b", 64), ApprovalID: approvalID, Revision: actionRevision,
			CreatedAt: now, UpdatedAt: now, CreatedBy: "subsystem:supervisor", UpdatedBy: "local-owner",
		},
	}
}
