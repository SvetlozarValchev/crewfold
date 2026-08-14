package daemon

import (
	"context"
	"strings"

	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleManagerGrantCreate(request localapi.Request) localapi.Response {
	var params localapi.ManagerGrantCreateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "manager.grant.create requires exact workspace, project, objective, task, agent revisions, proposal kinds, launch profiles, limits, and idempotency_key")
	}
	result, err := s.store.CreateManagerGrant(context.Background(), store.CreateManagerGrantCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, ObjectiveID: params.Objective,
		TaskID: params.Task, AgentIdentifier: params.Agent, ExpectedTaskRevision: params.ExpectedTaskRevision,
		ExpectedAgentRevision: params.ExpectedAgentRevision, ProposalKinds: params.ProposalKinds,
		LaunchProfileIDs: params.LaunchProfileIDs, AllowedClaimKinds: params.AllowedClaimKinds,
		Limits: params.Limits, ExpiresAt: params.ExpiresAt, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagerGrantMutationResult{Schema: localapi.ManagerGrantMutationSchema, Type: "manager_grant_mutation", Grant: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleManagerGrantRevoke(request localapi.Request) localapi.Response {
	var params localapi.ManagerGrantRevokeParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "manager.grant.revoke requires workspace, grant, expected_revision, reason, and idempotency_key")
	}
	result, err := s.store.RevokeManagerGrant(context.Background(), store.RevokeManagerGrantCommand{WorkspaceIdentifier: params.Workspace, ManagerGrantID: params.Grant, ExpectedRevision: params.ExpectedRevision, Reason: params.Reason, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagerGrantMutationResult{Schema: localapi.ManagerGrantMutationSchema, Type: "manager_grant_mutation", Grant: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleManagerGrantShow(request localapi.Request) localapi.Response {
	var params localapi.ManagerGrantQueryParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "manager.grant.show requires workspace and grant")
	}
	grant, err := s.store.ManagerGrant(context.Background(), params.Workspace, params.Grant)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagerGrantShowResult{Schema: localapi.ManagerGrantShowSchema, Type: "manager_grant", Grant: grant})
}

func (s *server) handleManagerGrantList(request localapi.Request) localapi.Response {
	var params localapi.ManagerGrantQueryParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "manager.grant.list requires workspace and bounded filters")
	}
	values, err := s.store.ManagerGrants(context.Background(), store.ListManagerGrantsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, ObjectiveID: params.Objective, TaskID: params.Task, AgentIdentifier: params.Agent, Status: params.Status, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagerGrantListResult{Schema: localapi.ManagerGrantListSchema, Type: "manager_grant_list", Grants: values})
}

func (s *server) handleLaunchProfileCreate(request localapi.Request) localapi.Response {
	var params localapi.LaunchProfileCreateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "launch_profile.create requires workspace, project, exact agent revision, runtime, provider, scenario, lease/TTL, and idempotency_key")
	}
	result, err := s.store.CreateLaunchProfile(context.Background(), store.CreateLaunchProfileCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, AgentIdentifier: params.Agent,
		ExpectedAgentRevision: params.ExpectedAgentRevision, Purpose: params.Purpose, Runtime: params.Runtime,
		Provider: params.Provider, CheckoutIdentifier: params.Checkout, Scenario: params.Scenario,
		AssignmentLeaseSeconds: params.AssignmentLeaseSeconds, CapabilityTTLSeconds: params.CapabilityTTLSeconds,
		ManagerGrantID: params.ManagerGrant, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.LaunchProfileMutationResult{Schema: localapi.LaunchProfileMutationSchema, Type: "launch_profile_mutation", Profile: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleLaunchProfileRetire(request localapi.Request) localapi.Response {
	var params localapi.LaunchProfileRetireParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "launch_profile.retire requires workspace, profile, expected_revision, reason, and idempotency_key")
	}
	result, err := s.store.RetireLaunchProfile(context.Background(), store.RetireLaunchProfileCommand{WorkspaceIdentifier: params.Workspace, LaunchProfileID: params.Profile, ExpectedRevision: params.ExpectedRevision, Reason: params.Reason, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.LaunchProfileMutationResult{Schema: localapi.LaunchProfileMutationSchema, Type: "launch_profile_mutation", Profile: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleLaunchProfileShow(request localapi.Request) localapi.Response {
	var params localapi.LaunchProfileQueryParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "launch_profile.show requires workspace and profile")
	}
	profile, err := s.store.LaunchProfile(context.Background(), params.Workspace, params.Profile)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.LaunchProfileShowResult{Schema: localapi.LaunchProfileShowSchema, Type: "launch_profile", Profile: profile})
}

func (s *server) handleLaunchProfileList(request localapi.Request) localapi.Response {
	var params localapi.LaunchProfileQueryParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "launch_profile.list requires workspace and bounded filters")
	}
	values, err := s.store.LaunchProfiles(context.Background(), store.ListLaunchProfilesQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, AgentIdentifier: params.Agent, ManagerGrantID: params.ManagerGrant, Status: params.Status, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.LaunchProfileListResult{Schema: localapi.LaunchProfileListSchema, Type: "launch_profile_list", Profiles: values})
}

func (s *server) handleManagerInvoke(request localapi.Request) localapi.Response {
	var params localapi.ManagerInvokeParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "manager.invoke requires workspace, objective, optional exact planning tuple, and idempotency_key")
	}
	result, err := s.store.InvokeManager(context.Background(), store.InvokeManagerCommand{
		WorkspaceIdentifier: params.Workspace, ObjectiveID: params.Objective, TaskID: params.PlanningTask,
		ManagerGrantID: params.Grant, LaunchProfileID: params.Profile, ExpectedTaskRevision: params.ExpectedTaskRevision,
		ExpectedGrantRevision: params.ExpectedGrantRevision, ExpectedProfileRevision: params.ExpectedProfileRevision,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagerInvocationResult{Schema: localapi.ManagerInvocationSchema, Type: "manager_invocation", ManagerGrant: result.ManagerGrant, LaunchProfile: result.LaunchProfile, Detail: result.Detail, EventSequence: result.EventSequence})
}

func (s *server) handleProposalList(request localapi.Request) localapi.Response {
	var params localapi.ProposalQueryParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "proposal.list requires workspace and bounded filters")
	}
	values, err := s.store.ManagerProposals(context.Background(), store.ListManagerProposalsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, ObjectiveID: params.Objective, SourceRunID: params.SourceRun, ManagerGrantID: params.Grant, Kind: params.Kind, Status: params.Status, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ProposalListResult{Schema: localapi.ProposalListSchema, Type: "manager_proposal_list", Proposals: values})
}

func (s *server) handleProposalInspect(request localapi.Request) localapi.Response {
	var params localapi.ProposalQueryParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "proposal.inspect requires workspace and proposal")
	}
	value, err := s.store.ManagerProposal(context.Background(), params.Workspace, params.Proposal)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ProposalShowResult{Schema: localapi.ProposalShowSchema, Type: "manager_proposal", Proposal: value})
}

func (s *server) handleProposalDecision(request localapi.Request, accept bool) localapi.Response {
	var params localapi.ProposalDecisionParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "proposal decision requires workspace, proposal, expected_revision, and idempotency_key")
	}
	command := store.AcceptManagerProposalCommand{WorkspaceIdentifier: params.Workspace, ManagerProposalID: params.Proposal, ExpectedRevision: params.ExpectedRevision, DecisionNote: params.DecisionNote, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID}
	var result store.ManagerProposalMutationResult
	var err error
	if accept {
		result, err = s.store.AcceptManagerProposal(context.Background(), command)
	} else {
		result, err = s.store.RejectManagerProposal(context.Background(), command)
	}
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ProposalMutationResult{Schema: localapi.ProposalMutationSchema, Type: "manager_proposal_mutation", Proposal: result.Proposal, Effects: result.Effects, EventSequence: result.EventSequence})
}

func (s *server) handleSupervisorPolicyShow(request localapi.Request) localapi.Response {
	var params localapi.WorkspaceParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "supervisor.policy.show requires workspace")
	}
	policy, err := s.store.SupervisorPolicy(context.Background(), params.Workspace)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.SupervisorPolicyShowResult{Schema: localapi.SupervisorPolicyShowSchema, Type: "supervisor_policy", Policy: policy})
}

func (s *server) handleSupervisorPolicyConfigure(request localapi.Request) localapi.Response {
	var params localapi.SupervisorPolicyConfigureParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "supervisor.policy.configure requires workspace, bounded policy, and idempotency_key")
	}
	result, err := s.store.ConfigureSupervisorPolicy(context.Background(), store.ConfigureSupervisorPolicyCommand{WorkspaceIdentifier: params.Workspace, Enabled: params.Enabled, Limits: params.Limits, AutoSchedule: params.AutoSchedule, AutoRetryLimit: params.AutoRetryLimit, RetryCooldownSeconds: params.RetryCooldownSeconds, ExpectedRevision: params.ExpectedRevision, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.SupervisorPolicyMutationResult{Schema: localapi.SupervisorPolicyMutationSchema, Type: "supervisor_policy_mutation", Policy: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleSupervisorRun(request localapi.Request) localapi.Response {
	var params localapi.SupervisorRunParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "supervisor.run requires workspace, bounded limit, and idempotency_key")
	}
	result, err := s.store.RunSupervisor(context.Background(), store.RunSupervisorCommand{WorkspaceIdentifier: params.Workspace, Limit: params.Limit, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID, PersistNoop: true})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.SupervisorRunResult{Schema: localapi.SupervisorRunSchema, Type: "supervisor_run", Policy: result.Policy, Actions: result.Actions, ScheduledRunIDs: result.ScheduledRunIDs, EventSequence: result.EventSequence})
}

func (s *server) handleSupervisorActionList(request localapi.Request) localapi.Response {
	var params localapi.SupervisorActionQueryParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "supervisor.action.list requires workspace and bounded filters")
	}
	values, err := s.store.SupervisorActions(context.Background(), store.ListSupervisorActionsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, TaskID: params.Task, RunID: params.Run, Status: params.Status, Condition: params.Condition, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.SupervisorActionListResult{Schema: localapi.SupervisorActionListSchema, Type: "supervisor_action_list", Actions: values})
}

func (s *server) handleSupervisorActionShow(request localapi.Request) localapi.Response {
	var params localapi.SupervisorActionQueryParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "supervisor.action.show requires workspace and action")
	}
	value, err := s.store.SupervisorAction(context.Background(), params.Workspace, params.Action)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.SupervisorActionShowResult{Schema: localapi.SupervisorActionShowSchema, Type: "supervisor_action", Action: value})
}

func (s *server) handleSupervisorExplain(request localapi.Request) localapi.Response {
	var params localapi.SupervisorExplainParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "supervisor.explain requires workspace and an optional exact intent or task")
	}
	value, err := s.store.ExplainSupervisor(context.Background(), store.ExplainSupervisorQuery{WorkspaceIdentifier: params.Workspace, IntentID: params.Intent, TaskID: params.Task, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.SupervisorExplanationResult{Schema: localapi.SupervisorExplanationSchema, Type: "supervisor_explanation", Explanation: value})
}

func (s *server) handleApprovalList(request localapi.Request) localapi.Response {
	var params localapi.ApprovalListParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" ||
		!validApprovalListStatus(params.Status) || params.Action != "" && !validCanonicalEntityID(params.Action, "saction_") {
		return invalidParamsResponse(request, "approval.list requires workspace and bounded filters")
	}
	page, err := s.store.ApprovalRequests(context.Background(), store.ListApprovalRequestsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, Status: params.Status, ActionID: params.Action, Cursor: params.Cursor, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ApprovalListResult{Schema: localapi.ApprovalListSchema, Type: "approval_list", Approvals: page.Approvals, PageResult: localapi.PageResult{NextCursor: page.NextCursor, HasMore: page.HasMore, Total: page.Total}})
}

func (s *server) handleApprovalInspect(request localapi.Request) localapi.Response {
	var params localapi.ApprovalQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.Approval, "appr_") {
		return invalidParamsResponse(request, "approval.inspect requires workspace and approval")
	}
	value, err := s.store.ApprovalRequest(context.Background(), params.Workspace, params.Approval)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ApprovalShowResult{Schema: localapi.ApprovalShowSchema, Type: "approval", Approval: value})
}

func (s *server) handleApprovalDecision(request localapi.Request, allow bool) localapi.Response {
	var params localapi.ApprovalDecisionParams
	if err := decodeParams(request.Params, &params); err != nil || params.DecisionNote != strings.TrimSpace(params.DecisionNote) {
		return invalidParamsResponse(request, "approval decision requires workspace, approval, expected_revision, and idempotency_key")
	}
	command := store.DecideApprovalCommand{WorkspaceIdentifier: params.Workspace, ApprovalRequestID: params.Approval, ExpectedRevision: params.ExpectedRevision, DecisionNote: params.DecisionNote, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID}
	var result store.ApprovalMutationResult
	var err error
	if allow {
		result, err = s.store.AllowApproval(context.Background(), command)
	} else {
		result, err = s.store.DenyApproval(context.Background(), command)
	}
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ApprovalMutationResult{Schema: localapi.ApprovalMutationSchema, Type: "approval_mutation", Approval: result.Approval, Action: result.Action, EventSequence: result.EventSequence})
}
