package daemon

import (
	"context"

	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleDomainAgentCreate(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentCreateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.create requires exact scope, definition, hierarchy placement, and idempotency_key")
	}
	result, err := s.store.CreateDomainAgent(context.Background(), store.CreateDomainAgentCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, Name: params.Name, Role: params.Role,
		Provider: params.Provider, Runtime: params.Runtime, MaxConcurrency: params.MaxConcurrency,
		ParentAgentIdentifier: params.ParentAgent, WorkstreamIdentifier: params.Workstream,
		PreferredEntry: params.PreferredEntry, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.DomainAgentCreateResult{
		Schema: localapi.DomainAgentCreateSchema, Type: "domain_agent_create", Agent: result.Agent, EventSequences: result.EventSequences,
	})
}

func (s *server) handleDomainAgentAttach(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentAttachParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.attach requires canonical workspace, project, agent, and idempotency_key")
	}
	result, err := s.store.AttachDomainAgent(context.Background(), store.AttachDomainAgentCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, AgentIdentifier: params.Agent,
		ParentAgentIdentifier: params.ParentAgent, WorkstreamIdentifier: params.Workstream,
		PreferredEntry: params.PreferredEntry, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.DomainAgentMutationResult{
		Schema: localapi.DomainAgentMutationSchema, Type: "domain_agent_mutation",
		Membership: result.Value, EventSequence: result.EventSequence,
	})
}

func (s *server) handleDomainAgentUpdate(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentUpdateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.update requires canonical scope, expected_revision, idempotency_key, and a changed field")
	}
	result, err := s.store.UpdateDomainAgent(context.Background(), store.UpdateDomainAgentCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, AgentIdentifier: params.Agent,
		ParentAgentIdentifier: params.ParentAgent, WorkstreamIdentifier: params.Workstream,
		PreferredEntry: params.PreferredEntry, Status: params.Status, ExpectedRevision: params.ExpectedRevision,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.DomainAgentMutationResult{
		Schema: localapi.DomainAgentMutationSchema, Type: "domain_agent_mutation",
		Membership: result.Value, EventSequence: result.EventSequence,
	})
}

func (s *server) handleDomainAgentTree(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentTreeParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.tree requires canonical workspace and project")
	}
	tree, err := s.store.DomainAgentTree(context.Background(), params.Workspace, params.Project)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.DomainAgentTreeResult{
		Schema: localapi.DomainAgentTreeSchema, Type: "domain_agent_tree", ProjectID: tree.ProjectID, Agents: tree.Agents,
	})
}

func (s *server) handleDomainStaffingGrantCreate(request localapi.Request) localapi.Response {
	var params localapi.DomainStaffingGrantCreateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.staffing_grant.create requires exact manager scope, bounded profiles/classes/capacity/budget, expected membership revision, and idempotency_key")
	}
	result, err := s.store.CreateDomainAgentStaffingGrant(context.Background(), store.CreateDomainAgentStaffingGrantCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, ManagerAgentIdentifier: params.ManagerAgent,
		ExpectedMembershipRevision: params.ExpectedMembershipRevision, Profiles: params.Profiles,
		TaskClasses: params.TaskClasses, MaxDescendants: params.MaxDescendants, MaxConcurrency: params.MaxConcurrency,
		Budget: params.Budget, ExpiresAt: params.ExpiresAt, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.DomainStaffingGrantMutationResult{
		Schema: localapi.DomainStaffingGrantMutationSchema, Type: "domain_staffing_grant_mutation",
		Grant: result.Value, EventSequence: result.EventSequence,
	})
}

func (s *server) handleDomainStaffingGrantList(request localapi.Request) localapi.Response {
	var params localapi.DomainStaffingGrantListParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.staffing_grant.list requires canonical workspace, project, and manager agent")
	}
	grants, err := s.store.DomainAgentStaffingGrants(context.Background(), params.Workspace, params.Project, params.ManagerAgent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.DomainStaffingGrantListResult{
		Schema: localapi.DomainStaffingGrantListSchema, Type: "domain_staffing_grant_list", Grants: grants,
	})
}

func (s *server) handleDomainStaffingGrantRevoke(request localapi.Request) localapi.Response {
	var params localapi.DomainStaffingGrantRevokeParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.staffing_grant.revoke requires canonical workspace, grant, expected revision, and idempotency_key")
	}
	result, err := s.store.RevokeDomainAgentStaffingGrant(context.Background(), store.RevokeDomainAgentStaffingGrantCommand{
		WorkspaceIdentifier: params.Workspace, GrantID: params.GrantID, ExpectedRevision: params.ExpectedRevision,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.DomainStaffingGrantMutationResult{
		Schema: localapi.DomainStaffingGrantMutationSchema, Type: "domain_staffing_grant_mutation",
		Grant: result.Value, EventSequence: result.EventSequence,
	})
}
