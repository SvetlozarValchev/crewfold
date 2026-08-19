package daemon

import (
	"context"
	"strings"

	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleAgentCreate(request localapi.Request) localapi.Response {
	var params localapi.AgentCreateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "agent.create requires workspace, name, role, provider, and idempotency_key")
	}
	result, err := s.store.CreateAgent(context.Background(), store.CreateAgentCommand{WorkspaceIdentifier: params.Workspace, Name: params.Name, Role: params.Role, Provider: params.Provider, Runtime: params.Runtime, MaxConcurrency: params.MaxConcurrency, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.AgentMutationResult{Schema: localapi.AgentMutationSchema, Type: "agent_mutation", Agent: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleAgentUpdate(request localapi.Request) localapi.Response {
	var params localapi.AgentUpdateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "agent.update requires workspace, agent, expected_revision, idempotency_key, and a changed field")
	}
	result, err := s.store.UpdateAgent(context.Background(), store.UpdateAgentCommand{WorkspaceIdentifier: params.Workspace, AgentIdentifier: params.Agent, Role: params.Role, Provider: params.Provider, Runtime: params.Runtime, Enabled: params.Enabled, MaxConcurrency: params.MaxConcurrency, ExpectedRevision: params.ExpectedRevision, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.AgentMutationResult{Schema: localapi.AgentMutationSchema, Type: "agent_mutation", Agent: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleAgentShow(request localapi.Request) localapi.Response {
	var params localapi.AgentQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Agent) == "" {
		return invalidParamsResponse(request, "agent.show requires workspace and agent")
	}
	agent, err := s.store.Agent(context.Background(), params.Workspace, params.Agent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.AgentShowResult{Schema: localapi.AgentShowSchema, Type: "agent", Agent: agent})
}

func (s *server) handleAgentList(request localapi.Request) localapi.Response {
	var params localapi.AgentListParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "agent.list requires workspace")
	}
	page, err := s.store.ListAgents(context.Background(), store.ListAgentsQuery{WorkspaceIdentifier: params.Workspace, Cursor: params.Cursor, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.AgentListResult{Schema: localapi.AgentListSchema, Type: "agent_list", Agents: page.Agents, PageResult: localapi.PageResult{NextCursor: page.NextCursor, HasMore: page.HasMore, Total: page.Total}})
}

func (s *server) handleObjectiveCreate(request localapi.Request) localapi.Response {
	var params localapi.ObjectiveCreateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "objective.create requires workspace, project, title, budget, and idempotency_key")
	}
	result, err := s.store.CreateObjective(context.Background(), store.CreateObjectiveCommand{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, PrimaryCheckoutID: params.PrimaryCheckout, ReferenceCheckoutIDs: params.ReferenceCheckouts, Title: params.Title, Budget: params.Budget, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ObjectiveMutationResult{Schema: localapi.ObjectiveMutationSchema, Type: "objective_mutation", Objective: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleObjectiveUpdate(request localapi.Request) localapi.Response {
	var params localapi.ObjectiveUpdateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "objective.update requires workspace, objective, expected_revision, idempotency_key, and a changed field")
	}
	result, err := s.store.UpdateObjective(context.Background(), store.UpdateObjectiveCommand{WorkspaceIdentifier: params.Workspace, ObjectiveID: params.Objective, Title: params.Title, Status: params.Status, Budget: params.Budget, ExpectedRevision: params.ExpectedRevision, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ObjectiveMutationResult{Schema: localapi.ObjectiveMutationSchema, Type: "objective_mutation", Objective: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleObjectiveShow(request localapi.Request) localapi.Response {
	var params localapi.ObjectiveQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.Objective, "obj_") {
		return invalidParamsResponse(request, "objective.show requires workspace and objective")
	}
	objective, err := s.store.Objective(context.Background(), params.Workspace, params.Objective)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ObjectiveShowResult{Schema: localapi.ObjectiveShowSchema, Type: "objective", Objective: objective})
}

func (s *server) handleObjectiveList(request localapi.Request) localapi.Response {
	var params localapi.ObjectiveListParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "objective.list requires workspace and project")
	}
	page, err := s.store.ListObjectives(context.Background(), store.ListObjectivesQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, Cursor: params.Cursor, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ObjectiveListResult{Schema: localapi.ObjectiveListSchema, Type: "objective_list", Objectives: page.Objectives, PageResult: localapi.PageResult{NextCursor: page.NextCursor, HasMore: page.HasMore, Total: page.Total}})
}

func (s *server) handleTaskCreate(request localapi.Request) localapi.Response {
	var params localapi.TaskCreateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "task.create requires workspace, project, title, priority, budget, and idempotency_key")
	}
	result, err := s.store.CreateTask(context.Background(), store.CreateTaskCommand{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, ObjectiveID: params.Objective, Title: params.Title, Description: params.Description, Priority: params.Priority, Budget: params.Budget, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return taskMutationResponse(request, result)
}

func (s *server) handleTaskUpdate(request localapi.Request) localapi.Response {
	var params localapi.TaskUpdateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "task.update requires workspace, task, expected_revision, idempotency_key, and a changed field")
	}
	result, err := s.store.UpdateTask(context.Background(), store.UpdateTaskCommand{WorkspaceIdentifier: params.Workspace, TaskID: params.Task, Title: params.Title, Description: params.Description, Priority: params.Priority, Budget: params.Budget, ExpectedRevision: params.ExpectedRevision, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return taskMutationResponse(request, result)
}

func (s *server) handleTaskShow(request localapi.Request) localapi.Response {
	var params localapi.TaskQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.Task, "task_") {
		return invalidParamsResponse(request, "task.show requires workspace and task")
	}
	detail, err := s.store.TaskDetail(context.Background(), params.Workspace, params.Task)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.TaskShowResult{Schema: localapi.TaskShowSchema, Type: "task", Detail: detail})
}

func (s *server) handleTaskList(request localapi.Request) localapi.Response {
	var params localapi.TaskListParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "task.list requires workspace and optional project/ready_only")
	}
	page, err := s.store.ListTasks(context.Background(), store.ListTasksQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, ReadyOnly: params.ReadyOnly, Cursor: params.Cursor, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.TaskListResult{Schema: localapi.TaskListSchema, Type: "task_list", Tasks: page.Tasks, PageResult: localapi.PageResult{NextCursor: page.NextCursor, HasMore: page.HasMore, Total: page.Total}})
}

func (s *server) handleTaskDepend(request localapi.Request) localapi.Response {
	var params localapi.TaskDependencyParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "task.dependency.add requires workspace, task, depends_on, expected_revision, and idempotency_key")
	}
	result, err := s.store.AddTaskDependency(context.Background(), store.AddTaskDependencyCommand{WorkspaceIdentifier: params.Workspace, TaskID: params.Task, DependsOnTaskID: params.DependsOn, DeliveryRequirement: params.DeliveryRequirement, ExpectedRevision: params.ExpectedRevision, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return taskMutationResponse(request, result)
}

func (s *server) handleTaskAssign(request localapi.Request) localapi.Response {
	var params localapi.TaskAssignParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "task.assign requires workspace, task, agent, lease_seconds, expected_revision, and idempotency_key")
	}
	result, err := s.store.AssignTask(context.Background(), store.AssignTaskCommand{WorkspaceIdentifier: params.Workspace, TaskID: params.Task, AgentIdentifier: params.Agent, LeaseSeconds: params.LeaseSeconds, ExpectedRevision: params.ExpectedRevision, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return taskMutationResponse(request, result)
}

func (s *server) handleTaskTransition(request localapi.Request) localapi.Response {
	var params localapi.TaskTransitionParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "task.transition requires workspace, task, action, expected_revision, and idempotency_key")
	}
	result, err := s.store.TransitionTask(context.Background(), store.TransitionTaskCommand{WorkspaceIdentifier: params.Workspace, TaskID: params.Task, Action: params.Action, Reason: params.Reason, ExpectedRevision: params.ExpectedRevision, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return taskMutationResponse(request, result)
}

func (s *server) handleCoordinationStatus(request localapi.Request) localapi.Response {
	var params localapi.CoordinationStatusParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "coordination.status requires workspace")
	}
	status, err := s.store.CoordinationStatus(context.Background(), params.Workspace)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CoordinationStatusResult{Schema: localapi.CoordinationStatusSchema, Type: "coordination_status", Workspace: params.Workspace, Status: status})
}

func taskMutationResponse(request localapi.Request, result store.TaskMutationResult) localapi.Response {
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.TaskMutationResult{Schema: localapi.TaskMutationSchema, Type: "task_mutation", Detail: result.Detail, EventSequence: result.EventSequence})
}
