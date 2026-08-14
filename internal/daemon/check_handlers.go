package daemon

import (
	"context"
	"encoding/json"
	"strings"

	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleCheckDefinitionCreate(request localapi.Request) localapi.Response {
	var params localapi.CheckDefinitionCreateParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Arguments == nil {
		return invalidParamsResponse(request, "check.definition.create requires workspace, project, name, absolute executable, ordered arguments, working_directory, bounded timeout/output, and idempotency_key")
	}
	result, err := s.store.CreateCheckDefinition(context.Background(), store.CreateCheckDefinitionCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, Name: params.Name,
		Executable: params.Executable, Arguments: params.Arguments, WorkingDirectory: params.WorkingDirectory,
		TimeoutMillis: params.TimeoutMillis, OutputByteLimit: params.OutputByteLimit,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckDefinitionMutationResult{Schema: localapi.CheckDefinitionMutationSchema, Type: "check_definition_mutation", Definition: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleCheckDefinitionRetire(request localapi.Request) localapi.Response {
	var params localapi.CheckDefinitionRetireParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "check.definition.retire requires workspace, definition, expected_revision, reason, and idempotency_key")
	}
	result, err := s.store.RetireCheckDefinition(context.Background(), store.RetireCheckDefinitionCommand{WorkspaceIdentifier: params.Workspace, CheckDefinitionID: params.Definition, ExpectedRevision: params.ExpectedRevision, Reason: params.Reason, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckDefinitionMutationResult{Schema: localapi.CheckDefinitionMutationSchema, Type: "check_definition_mutation", Definition: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleCheckDefinitionShow(request localapi.Request) localapi.Response {
	var params localapi.CheckDefinitionQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Definition) == "" || params.Project != "" || params.Status != "" || params.Limit != 0 {
		return invalidParamsResponse(request, "check.definition.show requires only workspace and definition")
	}
	value, err := s.store.CheckDefinition(context.Background(), params.Workspace, params.Definition)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckDefinitionShowResult{Schema: localapi.CheckDefinitionShowSchema, Type: "check_definition", Definition: value})
}

func (s *server) handleCheckDefinitionList(request localapi.Request) localapi.Response {
	var params localapi.CheckDefinitionQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Definition != "" || params.Limit < 0 || params.Limit > 100 {
		return invalidParamsResponse(request, "check.definition.list requires workspace and bounded project, status, and limit filters")
	}
	values, err := s.store.CheckDefinitions(context.Background(), store.ListCheckDefinitionsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, Status: params.Status, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckDefinitionListResult{Schema: localapi.CheckDefinitionListSchema, Type: "check_definition_list", Definitions: values})
}

func (s *server) handleCheckRequirementCreate(request localapi.Request) localapi.Response {
	var params localapi.CheckRequirementCreateParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "check.requirement.create requires workspace, task, criterion_key, statement, exact definition revision, expected_task_revision, and idempotency_key")
	}
	result, err := s.store.CreateTaskCheckRequirement(context.Background(), store.CreateTaskCheckRequirementCommand{
		WorkspaceIdentifier: params.Workspace, TaskID: params.Task, CriterionKey: params.CriterionKey,
		Statement: params.Statement, CheckDefinitionID: params.Definition, DefinitionContentRevision: params.DefinitionContentRevision,
		ExpectedTaskRevision: params.ExpectedTaskRevision, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRequirementMutationResult{Schema: localapi.CheckRequirementMutationSchema, Type: "check_requirement_mutation", Requirement: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleCheckRequirementRetire(request localapi.Request) localapi.Response {
	var params localapi.CheckRequirementRetireParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "check.requirement.retire requires workspace, requirement, expected_revision, reason, and idempotency_key")
	}
	result, err := s.store.RetireTaskCheckRequirement(context.Background(), store.RetireTaskCheckRequirementCommand{WorkspaceIdentifier: params.Workspace, RequirementID: params.Requirement, ExpectedRevision: params.ExpectedRevision, Reason: params.Reason, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRequirementMutationResult{Schema: localapi.CheckRequirementMutationSchema, Type: "check_requirement_mutation", Requirement: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleCheckRequirementList(request localapi.Request) localapi.Response {
	var params localapi.CheckRequirementQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Limit < 0 || params.Limit > 100 {
		return invalidParamsResponse(request, "check.requirement.list requires workspace and bounded project, task, status, and limit filters")
	}
	values, err := s.store.TaskCheckRequirements(context.Background(), store.ListTaskCheckRequirementsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, TaskID: params.Task, Status: params.Status, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRequirementListResult{Schema: localapi.CheckRequirementListSchema, Type: "check_requirement_list", Requirements: values})
}

func (s *server) handleCheckGrantCreate(request localapi.Request) localapi.Response {
	var params localapi.CheckGrantCreateParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Definitions == nil || params.Operations == nil {
		return invalidParamsResponse(request, "check.grant.create requires workspace, project, exact agent revision, exact definition revisions, closed operations, bounded limits, and idempotency_key")
	}
	definitions := make([]store.CheckDefinitionRevision, 0, len(params.Definitions))
	for _, definition := range params.Definitions {
		definitions = append(definitions, store.CheckDefinitionRevision{DefinitionID: definition.Definition, ContentRevision: definition.ContentRevision})
	}
	result, err := s.store.CreateCheckWatchGrant(context.Background(), store.CreateCheckWatchGrantCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, AgentIdentifier: params.Agent,
		ExpectedAgentRevision: params.ExpectedAgentRevision, Definitions: definitions, Operations: params.Operations,
		MaxPending: params.MaxPending, MaxInFlight: params.MaxInFlight, ExpiresAt: params.ExpiresAt,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckGrantMutationResult{Schema: localapi.CheckGrantMutationSchema, Type: "check_watch_grant_mutation", Grant: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleCheckGrantRevoke(request localapi.Request) localapi.Response {
	var params localapi.CheckGrantRevokeParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "check.grant.revoke requires workspace, grant, expected_revision, reason, and idempotency_key")
	}
	result, err := s.store.RevokeCheckWatchGrant(context.Background(), store.RevokeCheckWatchGrantCommand{WorkspaceIdentifier: params.Workspace, CheckWatchGrantID: params.Grant, ExpectedRevision: params.ExpectedRevision, Reason: params.Reason, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckGrantMutationResult{Schema: localapi.CheckGrantMutationSchema, Type: "check_watch_grant_mutation", Grant: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleCheckGrantShow(request localapi.Request) localapi.Response {
	var params localapi.CheckGrantQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Grant) == "" || params.Project != "" || params.Agent != "" || params.Status != "" || params.Limit != 0 {
		return invalidParamsResponse(request, "check.grant.show requires only workspace and grant")
	}
	value, err := s.store.CheckWatchGrant(context.Background(), params.Workspace, params.Grant)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckGrantShowResult{Schema: localapi.CheckGrantShowSchema, Type: "check_watch_grant", Grant: value})
}

func (s *server) handleCheckGrantList(request localapi.Request) localapi.Response {
	var params localapi.CheckGrantQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Grant != "" || params.Limit < 0 || params.Limit > 100 {
		return invalidParamsResponse(request, "check.grant.list requires workspace and bounded project, agent, status, and limit filters")
	}
	values, err := s.store.CheckWatchGrants(context.Background(), store.ListCheckWatchGrantsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, AgentIdentifier: params.Agent, Status: params.Status, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckGrantListResult{Schema: localapi.CheckGrantListSchema, Type: "check_watch_grant_list", Grants: values})
}

func (s *server) handleCheckRouteCreate(request localapi.Request) localapi.Response {
	var params localapi.CheckRouteCreateParams
	if err := decodeCheckParams(request.Params, &params); err != nil || (params.Definition == "") != (params.DefinitionContentRevision == 0) {
		return invalidParamsResponse(request, "check.route.create requires workspace, project, optional exact definition pair, trigger, duty, exact agent revision, and idempotency_key")
	}
	result, err := s.store.CreateCheckRoute(context.Background(), store.CreateCheckRouteCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project,
		CheckDefinitionID: params.Definition, DefinitionContentRevision: params.DefinitionContentRevision,
		Trigger: params.Trigger, Duty: params.Duty, AgentIdentifier: params.Agent,
		ExpectedAgentRevision: params.ExpectedAgentRevision, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRouteMutationResult{Schema: localapi.CheckRouteMutationSchema, Type: "check_route_mutation", Route: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleCheckRouteRetire(request localapi.Request) localapi.Response {
	var params localapi.CheckRouteRetireParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "check.route.retire requires workspace, route, expected_revision, optional reason, and idempotency_key")
	}
	result, err := s.store.RetireCheckRoute(context.Background(), store.RetireCheckRouteCommand{WorkspaceIdentifier: params.Workspace, CheckRouteID: params.Route, ExpectedRevision: params.ExpectedRevision, Reason: params.Reason, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRouteMutationResult{Schema: localapi.CheckRouteMutationSchema, Type: "check_route_mutation", Route: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleCheckRouteList(request localapi.Request) localapi.Response {
	var params localapi.CheckRouteQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Limit < 0 || params.Limit > 100 {
		return invalidParamsResponse(request, "check.route.list requires workspace and bounded project, definition, trigger, duty, status, and limit filters")
	}
	values, err := s.store.CheckRoutes(context.Background(), store.ListCheckRoutesQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, DefinitionID: params.Definition, Trigger: params.Trigger, Duty: params.Duty, Status: params.Status, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRouteListResult{Schema: localapi.CheckRouteListSchema, Type: "check_route_list", Routes: values})
}

func (s *server) handleCheckPolicyShow(request localapi.Request) localapi.Response {
	var params localapi.CheckPolicyQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "check.policy.show requires workspace and project")
	}
	value, err := s.store.CheckPolicy(context.Background(), params.Workspace, params.Project)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckPolicyShowResult{Schema: localapi.CheckPolicyShowSchema, Type: "check_policy", Policy: value})
}

func (s *server) handleCheckPolicyConfigure(request localapi.Request) localapi.Response {
	var params localapi.CheckPolicyConfigureParams
	err := decodeCheckParams(request.Params, &params)
	profileConfigured := params.RepairProfile != ""
	if err != nil ||
		(params.RepairProfile == "") != (params.RepairProfileRevision == 0) ||
		(profileConfigured && params.RepairProfileRevision < 1) || params.RepairProposalsEnabled != profileConfigured ||
		params.MaxOpenRepairs < 1 || params.MaxOpenRepairs > 32 {
		return invalidParamsResponse(request, "check.policy.configure requires workspace, project, repair policy, exact optional repair profile pair, bounded open limit, expected_revision, and idempotency_key")
	}
	result, err := s.store.ConfigureCheckPolicy(context.Background(), store.ConfigureCheckPolicyCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project,
		RepairProposalsEnabled: params.RepairProposalsEnabled, RepairLaunchProfileID: params.RepairProfile,
		RepairLaunchProfileRevision: params.RepairProfileRevision, MaxOpenRepairProposals: params.MaxOpenRepairs,
		ExpectedRevision: params.ExpectedRevision, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckPolicyMutationResult{Schema: localapi.CheckPolicyMutationSchema, Type: "check_policy_mutation", Policy: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleCheckRun(request localapi.Request) localapi.Response {
	var params localapi.CheckRunParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.ExpectedRequirementRevision < 0 || params.ExpectedDefinitionContentRevision < 0 || params.ExpectedCheckoutRevision < 0 || (params.ExpectedCheckoutRevision != 0 && params.Checkout == "") {
		return invalidParamsResponse(request, "check.run requires workspace, task, definition, optional checkout and positive optimistic revisions, and idempotency_key; checkout revision requires checkout")
	}
	result, err := s.store.RequestCheckRun(context.Background(), store.RequestCheckRunCommand{
		WorkspaceIdentifier: params.Workspace, TaskID: params.Task, CheckDefinitionIdentifier: params.Definition,
		CheckoutIdentifier: params.Checkout, ExpectedRequirementRevision: params.ExpectedRequirementRevision,
		ExpectedDefinitionContentRevision: params.ExpectedDefinitionContentRevision, ExpectedCheckoutRevision: params.ExpectedCheckoutRevision,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRunMutationResult{Schema: localapi.CheckRunMutationSchema, Type: "check_run_mutation", Run: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleCheckList(request localapi.Request) localapi.Response {
	var params localapi.CheckListParams
	if err := decodeCheckParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" ||
		params.Limit < 0 || params.Limit > store.MaximumReadPageLimit ||
		params.Task != "" && !validCanonicalEntityID(params.Task, "task_") ||
		params.Requirement != "" && !validCanonicalEntityID(params.Requirement, "checkreq_") ||
		params.Definition != "" && !validCanonicalEntityID(params.Definition, "checkdef_") ||
		!validCheckListStatus(params.Status) || !validCheckListOutcome(params.Outcome) {
		return invalidParamsResponse(request, "check.list requires workspace and bounded project, task, requirement, definition, status, outcome, and limit filters")
	}
	page, err := s.store.CheckRuns(context.Background(), store.ListCheckRunsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, TaskID: params.Task, RequirementID: params.Requirement, DefinitionID: params.Definition, Status: params.Status, Outcome: params.Outcome, Cursor: params.Cursor, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRunListResult{Schema: localapi.CheckRunListSchema, Type: "check_run_list", Runs: page.Runs, PageResult: localapi.PageResult{NextCursor: page.NextCursor, HasMore: page.HasMore, Total: page.Total}})
}

func (s *server) handleCheckInspect(request localapi.Request) localapi.Response {
	var params localapi.CheckQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.CheckRun, "checkrun_") {
		return invalidParamsResponse(request, "check.inspect requires only workspace and check_run")
	}
	value, err := s.store.CheckRunDetail(context.Background(), params.Workspace, params.CheckRun)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckInspectResult{Schema: localapi.CheckInspectSchema, Type: "check_run_detail", Detail: value})
}

func (s *server) handleCheckLogs(request localapi.Request) localapi.Response {
	var params localapi.CheckLogsParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "check.logs requires only workspace and check_run")
	}
	value, err := s.store.CheckRunLogs(context.Background(), params.Workspace, params.CheckRun)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckLogsResult{Schema: localapi.CheckLogsSchema, Type: "check_run_logs", Logs: value})
}

func (s *server) handleCheckWatch(request localapi.Request) localapi.Response {
	var params localapi.CheckWatchParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Limit < 0 || params.Limit > 100 {
		return invalidParamsResponse(request, "check.watch requires workspace, project, optional opaque cursor, bounded limit, and idempotency_key")
	}
	result, err := s.runPreparedCheckWatch(context.Background(), store.PrepareCheckWatchCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, After: params.Cursor, Limit: params.Limit,
	}, params.IdempotencyKey, request.ID, true)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckWatchResult{Schema: localapi.CheckWatchSchema, Type: "check_watch", Receipt: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleCheckRepairList(request localapi.Request) localapi.Response {
	var params localapi.CheckRepairQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Repair != "" || params.Limit < 0 || params.Limit > 100 {
		return invalidParamsResponse(request, "check.repair.list requires workspace and bounded project, task, status, and limit filters")
	}
	values, err := s.store.CheckRepairProposals(context.Background(), store.ListCheckRepairProposalsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, TaskID: params.Task, Status: params.Status, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRepairListResult{Schema: localapi.CheckRepairListSchema, Type: "check_repair_list", Repairs: values})
}

func (s *server) handleCheckRepairInspect(request localapi.Request) localapi.Response {
	var params localapi.CheckRepairQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Repair) == "" || params.Project != "" || params.Task != "" || params.Status != "" || params.Limit != 0 {
		return invalidParamsResponse(request, "check.repair.inspect requires only workspace and repair")
	}
	value, err := s.store.CheckRepairProposal(context.Background(), params.Workspace, params.Repair)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRepairShowResult{Schema: localapi.CheckRepairShowSchema, Type: "check_repair", Detail: value})
}

func (s *server) handleCheckRepairDecision(request localapi.Request, accept bool) localapi.Response {
	var params localapi.CheckRepairDecisionParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "check repair decision requires workspace, repair, expected_revision, bounded decision_note, and idempotency_key")
	}
	command := store.DecideCheckRepairCommand{WorkspaceIdentifier: params.Workspace, CheckRepairProposalID: params.Repair, ExpectedRevision: params.ExpectedRevision, DecisionNote: params.DecisionNote, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID}
	if accept {
		value, err := s.store.AcceptCheckRepair(context.Background(), command)
		if err != nil {
			return storeErrorResponse(request, err)
		}
		return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRepairMutationResult{Schema: localapi.CheckRepairMutationSchema, Type: "check_repair_mutation", Detail: value.Value, EventSequence: value.EventSequence})
	}
	value, err := s.store.RejectCheckRepair(context.Background(), command)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckRepairMutationResult{Schema: localapi.CheckRepairMutationSchema, Type: "check_repair_mutation", Detail: value.Value, EventSequence: value.EventSequence})
}

func decodeCheckParams(data json.RawMessage, target any) error {
	return decodeParams(data, target)
}
