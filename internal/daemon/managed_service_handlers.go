package daemon

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleManagedServiceDefinitionCreate(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceDefinitionCreateParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Arguments == nil || params.Environment == nil {
		return invalidParamsResponse(request, "managed_service.definition.create requires one exact bounded checkout process definition")
	}
	executable := strings.TrimSpace(params.Executable)
	if !filepath.IsAbs(executable) {
		resolved, err := exec.LookPath(executable)
		if err != nil {
			return invalidParamsResponse(request, "managed_service.definition.create executable is not available in the daemon local-process profile")
		}
		executable, err = filepath.Abs(resolved)
		if err != nil {
			return invalidParamsResponse(request, "managed_service.definition.create executable could not be resolved canonically")
		}
	}
	executable = filepath.Clean(executable)
	result, err := s.store.CreateManagedServiceDefinition(context.Background(), store.CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, WorkstreamID: params.Workstream, CheckoutID: params.Checkout,
		Name: params.Name, Description: params.Description, Executable: executable, Arguments: params.Arguments,
		WorkingDirectory: params.WorkingDirectory, Environment: params.Environment, Profile: params.Profile, ProfileRevision: params.ProfileRevision,
		NetworkMode: params.NetworkMode, Health: params.Health, RestartPolicy: params.RestartPolicy, MaximumRestarts: params.MaximumRestarts,
		RestartCooldownMillis: params.RestartCooldownMillis, StopSignal: params.StopSignal, StopGraceMillis: params.StopGraceMillis,
		OutputByteLimit: params.OutputByteLimit, CapacityClass: params.CapacityClass, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceDefinitionMutationResult{
		Schema: localapi.ManagedServiceDefinitionMutationSchema, Type: "managed_service_definition_mutation", Definition: result.Value, EventSequence: result.EventSequence,
	})
}

func (s *server) handleManagedServiceDefinitionRetire(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceDefinitionRetireParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "managed_service.definition.retire requires workspace, definition, exact revision, reason, and idempotency key")
	}
	result, err := s.store.RetireManagedServiceDefinition(context.Background(), store.RetireManagedServiceDefinitionCommand{
		WorkspaceIdentifier: params.Workspace, DefinitionID: params.Definition, ExpectedRevision: params.ExpectedRevision,
		Reason: params.Reason, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceDefinitionMutationResult{
		Schema: localapi.ManagedServiceDefinitionMutationSchema, Type: "managed_service_definition_mutation", Definition: result.Value, EventSequence: result.EventSequence,
	})
}

func (s *server) handleManagedServiceDefinitionShow(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceDefinitionQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || strings.TrimSpace(params.Definition) == "" || params.Project != "" || params.Workstream != "" || params.Checkout != "" || params.Status != "" || params.Limit != 0 {
		return invalidParamsResponse(request, "managed_service.definition.show requires only workspace and definition")
	}
	definition, err := s.store.ManagedServiceDefinition(context.Background(), params.Workspace, params.Definition)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceDefinitionShowResult{Schema: localapi.ManagedServiceDefinitionShowSchema, Type: "managed_service_definition", Definition: definition})
}

func (s *server) handleManagedServiceDefinitionList(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceDefinitionQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Definition != "" || params.Limit < 0 || params.Limit > 200 {
		return invalidParamsResponse(request, "managed_service.definition.list requires workspace and bounded project, workstream, checkout, status, and limit filters")
	}
	definitions, err := s.store.ManagedServiceDefinitions(context.Background(), store.ListManagedServiceDefinitionsQuery{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, WorkstreamID: params.Workstream, CheckoutID: params.Checkout, Status: params.Status, Limit: params.Limit,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceDefinitionListResult{Schema: localapi.ManagedServiceDefinitionListSchema, Type: "managed_service_definition_list", Definitions: definitions})
}

func (s *server) handleManagedServiceStart(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceStartParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "managed_service.start requires workspace, definition, exact revision, and idempotency key")
	}
	result, err := s.store.StartManagedService(context.Background(), store.StartManagedServiceCommand{
		WorkspaceIdentifier: params.Workspace, DefinitionID: params.Definition, ExpectedRevision: params.ExpectedRevision,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceMutationResult{Schema: localapi.ManagedServiceMutationSchema, Type: "managed_service_mutation", Instance: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleManagedServiceAction(request localapi.Request, action string) localapi.Response {
	var params localapi.ManagedServiceActionParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "managed-service control requires workspace, instance, exact revision, and idempotency key")
	}
	result, err := s.store.RequestManagedServiceAction(context.Background(), store.RequestManagedServiceActionCommand{
		WorkspaceIdentifier: params.Workspace, InstanceID: params.Instance, ExpectedRevision: params.ExpectedRevision,
		Action: action, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceMutationResult{Schema: localapi.ManagedServiceMutationSchema, Type: "managed_service_mutation", Instance: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleManagedServiceResolveUnknown(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceResolveUnknownParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "managed_service.resolve_unknown requires workspace, instance, exact revision, explicit runtime-retired confirmation, reason, and idempotency key")
	}
	result, err := s.store.ResolveManagedServiceUnknown(context.Background(), store.ResolveManagedServiceUnknownCommand{
		WorkspaceIdentifier: params.Workspace, InstanceID: params.Instance, ExpectedRevision: params.ExpectedRevision,
		RuntimeRetiredConfirmed: params.RuntimeRetiredConfirmed, Reason: params.Reason,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceMutationResult{Schema: localapi.ManagedServiceMutationSchema, Type: "managed_service_mutation", Instance: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleManagedServiceShow(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || strings.TrimSpace(params.Instance) == "" || params.Project != "" || params.Workstream != "" || params.Checkout != "" || params.Status != "" || params.Limit != 0 {
		return invalidParamsResponse(request, "managed_service.show requires only workspace and instance")
	}
	detail, err := s.store.ManagedServiceDetail(context.Background(), params.Workspace, params.Instance)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceShowResult{Schema: localapi.ManagedServiceShowSchema, Type: "managed_service", Detail: detail})
}

func (s *server) handleManagedServiceList(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Instance != "" || params.Limit < 0 || params.Limit > 200 {
		return invalidParamsResponse(request, "managed_service.list requires workspace and bounded project, workstream, checkout, status, and limit filters")
	}
	instances, err := s.store.ManagedServiceInstances(context.Background(), store.ListManagedServiceInstancesQuery{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, WorkstreamID: params.Workstream, CheckoutID: params.Checkout, Status: params.Status, Limit: params.Limit,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceListResult{Schema: localapi.ManagedServiceListSchema, Type: "managed_service_list", Instances: instances})
}

func (s *server) handleManagedServiceLogs(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceLogsParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "managed_service.logs requires workspace and instance")
	}
	detail, err := s.store.ManagedServiceDetail(context.Background(), params.Workspace, params.Instance)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	logs, err := s.managedServiceLogs(context.Background(), detail)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceLogsResult{Schema: localapi.ManagedServiceLogsSchema, Type: "managed_service_logs", Logs: logs})
}

func (s *server) managedServiceLogs(ctx context.Context, detail domain.ManagedServiceDetail) (domain.ManagedServiceLogs, error) {
	if !s.store.ManagedServiceBindingIsCurrent(detail.Instance) {
		logs, terminalErr := s.store.ManagedServiceTerminalLogs(ctx, detail.Instance.WorkspaceID, detail.Instance.ID)
		if terminalErr != nil {
			return domain.ManagedServiceLogs{}, terminalErr
		}
		return logs, nil
	}
	stdout, stderr, stdoutOmitted, stderrOmitted, err := s.serviceHost.ReadLogs(detail.Instance.ID, managedServiceHostBinding(detail.Instance), detail.Definition.OutputByteLimit)
	if err != nil {
		return domain.ManagedServiceLogs{}, &store.Error{Code: store.CodeManagedServiceLogsUnavailable, Message: "read managed-service live logs", Cause: err}
	}
	stdoutText := execution.RedactTerminalOutput(string(stdout))
	stderrText := execution.RedactTerminalOutput(string(stderr))
	logs := domain.ManagedServiceLogs{
		InstanceID: detail.Instance.ID, State: "live",
		Stdout: domain.CapturedLog{Text: stdoutText, CapturedBytes: int64(len(stdoutText)), OmittedBytes: stdoutOmitted, Truncated: stdoutOmitted > 0},
		Stderr: domain.CapturedLog{Text: stderrText, CapturedBytes: int64(len(stderrText)), OmittedBytes: stderrOmitted, Truncated: stderrOmitted > 0},
	}
	return logs, nil
}

func (s *server) handleManagedServiceGrantCreate(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceGrantCreateParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Actions == nil {
		return invalidParamsResponse(request, "managed_service.grant.create requires an exact definition, active agent membership, action set, instance ceiling, and idempotency key")
	}
	result, err := s.store.CreateManagedServiceGrant(context.Background(), store.CreateManagedServiceGrantCommand{
		WorkspaceIdentifier: params.Workspace, DefinitionID: params.Definition, ExpectedDefinitionRevision: params.ExpectedDefinitionRevision,
		ManagerAgentIdentifier: params.ManagerAgent, ExpectedMembershipRevision: params.ExpectedMembershipRevision,
		Actions: params.Actions, MaximumInstances: params.MaximumInstances, ExpiresAt: params.ExpiresAt,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceGrantMutationResult{
		Schema: localapi.ManagedServiceGrantMutationSchema, Type: "managed_service_grant_mutation", Grant: result.Value, EventSequence: result.EventSequence,
	})
}

func (s *server) handleManagedServiceGrantRevoke(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceGrantRevokeParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "managed_service.grant.revoke requires workspace, grant, exact revision, reason, and idempotency key")
	}
	result, err := s.store.RevokeManagedServiceGrant(context.Background(), store.RevokeManagedServiceGrantCommand{
		WorkspaceIdentifier: params.Workspace, GrantID: params.Grant, ExpectedRevision: params.ExpectedRevision,
		Reason: params.Reason, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceGrantMutationResult{
		Schema: localapi.ManagedServiceGrantMutationSchema, Type: "managed_service_grant_mutation", Grant: result.Value, EventSequence: result.EventSequence,
	})
}

func (s *server) handleManagedServiceGrantList(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceGrantQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Limit < 0 || params.Limit > 200 {
		return invalidParamsResponse(request, "managed_service.grant.list requires workspace and bounded project, manager, definition, status, and limit filters")
	}
	values, err := s.store.ManagedServiceGrants(context.Background(), store.ListManagedServiceGrantsQuery{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, ManagerIdentifier: params.Manager,
		DefinitionID: params.Definition, Status: params.Status, Limit: params.Limit,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceGrantListResult{
		Schema: localapi.ManagedServiceGrantListSchema, Type: "managed_service_grant_list", Grants: values,
	})
}

func (s *server) handleManagedServiceRequestList(request localapi.Request) localapi.Response {
	var params localapi.ManagedServiceRequestQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || params.Limit < 0 || params.Limit > 200 {
		return invalidParamsResponse(request, "managed_service.request.list requires workspace and bounded project, agent, definition, status, and limit filters")
	}
	values, err := s.store.ManagedServiceRequests(context.Background(), store.ListManagedServiceRequestsQuery{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, AgentIdentifier: params.Agent,
		DefinitionID: params.Definition, Status: params.Status, Limit: params.Limit,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceRequestListResult{
		Schema: localapi.ManagedServiceRequestListSchema, Type: "managed_service_request_list", Requests: values,
	})
}

func (s *server) handleManagedServiceRequestDecision(request localapi.Request, accept bool) localapi.Response {
	var params localapi.ManagedServiceRequestDecisionParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "managed-service request decision requires workspace, request, exact revision, reason, and idempotency key")
	}
	result, err := s.store.DecideManagedServiceRequest(context.Background(), store.DecideManagedServiceRequestCommand{
		WorkspaceIdentifier: params.Workspace, RequestID: params.Request, ExpectedRevision: params.ExpectedRevision,
		Accept: accept, Reason: params.Reason, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ManagedServiceRequestMutationResult{
		Schema: localapi.ManagedServiceRequestMutationSchema, Type: "managed_service_request_mutation", Decision: result.Value, EventSequence: result.EventSequence,
	})
}
