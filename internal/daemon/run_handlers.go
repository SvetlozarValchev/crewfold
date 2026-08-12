package daemon

import (
	"context"
	"strings"
	"time"

	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleRunStart(request localapi.Request) localapi.Response {
	var params localapi.RunStartParams
	if err := decodeParams(request.Params, &params); err != nil || execution.ValidateScenario(params.Scenario) != nil {
		return invalidParamsResponse(request, "run.start requires workspace, task, runtime, provider, a valid fake scenario, expected_task_revision, and idempotency_key")
	}
	if _, exists := s.runtimes[params.Runtime]; !exists {
		return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: "requested runtime driver is not registered"})
	}
	if _, exists := s.providers[params.Provider]; !exists {
		return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: "requested provider adapter is not registered"})
	}
	result, err := s.store.CreateRun(context.Background(), store.CreateRunCommand{
		WorkspaceIdentifier:  params.Workspace,
		TaskID:               params.Task,
		CheckoutIdentifier:   params.Checkout,
		ContextPacketID:      params.Context,
		Runtime:              params.Runtime,
		Provider:             params.Provider,
		Scenario:             params.Scenario,
		ExpectedTaskRevision: params.ExpectedTaskRevision,
		IdempotencyKey:       params.IdempotencyKey,
		CorrelationID:        request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunMutationResult{Schema: localapi.RunMutationSchema, Type: "run_mutation", Detail: result.Detail, EventSequence: result.EventSequence})
}

func (s *server) handleRunShow(request localapi.Request) localapi.Response {
	var params localapi.RunQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Run) == "" {
		return invalidParamsResponse(request, "run.show requires workspace and run")
	}
	detail, err := s.store.RunDetail(context.Background(), params.Workspace, params.Run)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunShowResult{Schema: localapi.RunShowSchema, Type: "run", Detail: detail})
}

func (s *server) handleRunList(request localapi.Request) localapi.Response {
	var params localapi.RunQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Run) != "" {
		return invalidParamsResponse(request, "run.list requires workspace and accepts optional task and status filters")
	}
	runs, err := s.store.Runs(context.Background(), params.Workspace, params.Task, params.Status)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunListResult{Schema: localapi.RunListSchema, Type: "run_list", Runs: runs})
}

func (s *server) handleRunResume(request localapi.Request) localapi.Response {
	var params localapi.RunResumeParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "run.resume requires workspace, run, expected_revision, and idempotency_key")
	}
	result, err := s.store.ResumeRun(context.Background(), store.ResumeRunCommand{WorkspaceIdentifier: params.Workspace, RunID: params.Run, ExpectedRevision: params.ExpectedRevision, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunMutationResult{Schema: localapi.RunMutationSchema, Type: "run_mutation", Detail: result.Detail, EventSequence: result.EventSequence})
}

func (s *server) handleRunStop(request localapi.Request) localapi.Response {
	var params localapi.RunStopParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "run.stop requires workspace, run, expected_revision, grace_period_millis, and idempotency_key")
	}
	result, err := s.store.RequestRunStop(context.Background(), store.StopRunCommand{
		WorkspaceIdentifier: params.Workspace,
		RunID:               params.Run,
		ExpectedRevision:    params.ExpectedRevision,
		GracePeriodMillis:   params.GracePeriodMillis,
		IdempotencyKey:      params.IdempotencyKey,
		CorrelationID:       request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunMutationResult{Schema: localapi.RunMutationSchema, Type: "run_mutation", Detail: result.Detail, EventSequence: result.EventSequence})
}

func (s *server) handleRunLogs(request localapi.Request) localapi.Response {
	var params localapi.RunLogsParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Run) == "" || params.Tail < 0 || params.Tail > 10000 {
		return invalidParamsResponse(request, "run.logs requires workspace and run and accepts a tail from 0 to 10000")
	}
	detail, err := s.store.RunDetail(context.Background(), params.Workspace, params.Run)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	driver, exists := s.runtimes[detail.Run.Runtime]
	if !exists {
		return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: "run runtime driver is unavailable"})
	}
	if detail.Run.RuntimeHandle == "" {
		return storeErrorResponse(request, &store.Error{Code: store.CodeRunConflict, Message: "run has no runtime handle yet"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	logs, err := driver.Logs(ctx, detail.Run.ID, detail.Run.RuntimeHandle, params.Tail)
	if err != nil {
		return storeErrorResponse(request, &store.Error{Code: store.CodeRuntimeFailed, Message: "read runtime logs", Cause: err})
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunLogsResult{Schema: localapi.RunLogsSchema, Type: "run_logs", Logs: logs})
}

func (s *server) handleTaskTimeline(request localapi.Request) localapi.Response {
	var params localapi.TaskTimelineParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Task) == "" {
		return invalidParamsResponse(request, "task.timeline requires workspace and task")
	}
	timeline, err := s.store.TaskTimeline(context.Background(), params.Workspace, params.Task)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.TaskTimelineResult{Schema: localapi.TaskTimelineSchema, Type: "task_timeline", Timeline: timeline})
}
