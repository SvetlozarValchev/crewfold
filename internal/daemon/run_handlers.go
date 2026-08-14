package daemon

import (
	"context"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleRunStart(request localapi.Request) localapi.Response {
	var params localapi.RunStartParams
	if err := decodeParams(request.Params, &params); err != nil || execution.ValidateScenario(params.Scenario) != nil || (params.CheckWatchGrant == "") != (params.ExpectedCheckWatchGrantRevision == 0) || (params.CheckWatchGrant != "" && params.Context != "") {
		return invalidParamsResponse(request, "run.start requires workspace, task, runtime, provider, a valid fake scenario, expected_task_revision, idempotency_key, and an optional exact check-watch grant/revision pair")
	}
	if _, exists := s.runtimes[params.Runtime]; !exists {
		return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: "requested runtime driver is not registered"})
	}
	if _, exists := s.providers[params.Provider]; !exists {
		return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: "requested provider adapter is not registered"})
	}
	result, err := s.store.CreateRun(context.Background(), store.CreateRunCommand{
		WorkspaceIdentifier:             params.Workspace,
		TaskID:                          params.Task,
		CheckoutIdentifier:              params.Checkout,
		ContextPacketID:                 params.Context,
		Runtime:                         params.Runtime,
		Provider:                        params.Provider,
		Scenario:                        params.Scenario,
		ExpectedTaskRevision:            params.ExpectedTaskRevision,
		CheckWatchGrantID:               params.CheckWatchGrant,
		ExpectedCheckWatchGrantRevision: params.ExpectedCheckWatchGrantRevision,
		IdempotencyKey:                  params.IdempotencyKey,
		CorrelationID:                   request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunMutationResult{Schema: localapi.RunMutationSchema, Type: "run_mutation", Detail: result.Detail, EventSequence: result.EventSequence})
}

func (s *server) handleRunShow(request localapi.Request) localapi.Response {
	var params localapi.RunQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.Run, "run_") {
		return invalidParamsResponse(request, "run.show requires workspace and run")
	}
	detail, err := s.store.RunDetail(context.Background(), params.Workspace, params.Run)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunShowResult{Schema: localapi.RunShowSchema, Type: "run", Detail: detail})
}

func (s *server) handleRunList(request localapi.Request) localapi.Response {
	var params localapi.RunListParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" ||
		(params.Task != "" && !validCanonicalEntityID(params.Task, "task_")) {
		return invalidParamsResponse(request, "run.list requires workspace and accepts optional task and status filters")
	}
	page, err := s.store.ListRuns(context.Background(), store.ListRunsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, TaskID: params.Task, Status: params.Status, Cursor: params.Cursor, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	for index := range page.Runs {
		driver, exists := s.runtimes[page.Runs[index].Runtime]
		_, supportsAttach := driver.(execution.RuntimeAttacher)
		page.Runs[index].CanAttach = page.RuntimeHandleBoundIDs[page.Runs[index].ID] && exists && supportsAttach
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunListResult{Schema: localapi.RunListSchema, Type: "run_list", Runs: page.Runs, PageResult: localapi.PageResult{NextCursor: page.NextCursor, HasMore: page.HasMore, Total: page.Total}})
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

func (s *server) handleRunPrompt(request localapi.Request) localapi.Response {
	var params localapi.RunPromptParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Run) == "" || strings.TrimSpace(params.Text) == "" || len(params.Text) > 16*1024 || strings.ContainsRune(params.Text, '\x00') {
		return invalidParamsResponse(request, "run.prompt requires workspace, run, and bounded non-empty text")
	}
	detail, driver, err := s.interactiveRun(params.Workspace, params.Run)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	prompter, supported := driver.(execution.RuntimePrompter)
	if !supported {
		return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: "run runtime does not support prompting"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := prompter.Prompt(ctx, detail.Run.ID, detail.Run.RuntimeHandle, params.Text); err != nil {
		return storeErrorResponse(request, &store.Error{Code: store.CodeRuntimeFailed, Message: "prompt runtime", Cause: err})
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunControlResult{Schema: localapi.RunControlSchema, Type: "run_control", RunID: detail.Run.ID, Runtime: detail.Run.Runtime, Action: "prompt", Status: "delivered"})
}

func (s *server) handleRunInterrupt(request localapi.Request) localapi.Response {
	var params localapi.RunQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.Run, "run_") {
		return invalidParamsResponse(request, "run.interrupt requires workspace and run")
	}
	detail, driver, err := s.interactiveRun(params.Workspace, params.Run)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	interrupter, supported := driver.(execution.RuntimeInterrupter)
	if !supported {
		return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: "run runtime does not support interrupts"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := interrupter.Interrupt(ctx, detail.Run.ID, detail.Run.RuntimeHandle); err != nil {
		return storeErrorResponse(request, &store.Error{Code: store.CodeRuntimeFailed, Message: "interrupt runtime", Cause: err})
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunControlResult{Schema: localapi.RunControlSchema, Type: "run_control", RunID: detail.Run.ID, Runtime: detail.Run.Runtime, Action: "interrupt", Status: "delivered"})
}

func (s *server) handleRunAttach(request localapi.Request) localapi.Response {
	var params localapi.RunAttachParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Run) == "" {
		return invalidParamsResponse(request, "run.attach requires workspace and run")
	}
	detail, driver, err := s.interactiveRun(params.Workspace, params.Run)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	attacher, supported := driver.(execution.RuntimeAttacher)
	if !supported {
		return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: "run runtime does not support interactive attach"})
	}
	spec, err := attacher.Attach(context.Background(), detail.Run.ID, detail.Run.RuntimeHandle)
	if err != nil {
		return storeErrorResponse(request, &store.Error{Code: store.CodeRuntimeFailed, Message: "prepare runtime attach", Cause: err})
	}
	result := localapi.RunAttachResult{
		Schema: localapi.RunAttachSchema, Type: "run_attach", RunID: detail.Run.ID, Runtime: detail.Run.Runtime,
		Executable: spec.Executable, Arguments: append([]string{}, spec.Arguments...), Environment: spec.Environment,
	}
	if err := localapi.ValidateRunAttachResult(result, detail.Run.ID); err != nil {
		return storeErrorResponse(request, &store.Error{Code: store.CodeRuntimeFailed, Message: "runtime returned an invalid attach specification", Cause: err})
	}
	return localapi.MarshalResult(request.ID, request.Protocol, result)
}

func (s *server) interactiveRun(workspace, runID string) (domain.RunDetail, execution.RuntimeDriver, error) {
	detail, err := s.store.RunDetail(context.Background(), workspace, runID)
	if err != nil {
		return domain.RunDetail{}, nil, err
	}
	if detail.Run.RuntimeHandle == "" {
		return domain.RunDetail{}, nil, &store.Error{Code: store.CodeRunConflict, Message: "run has no runtime handle yet"}
	}
	driver, exists := s.runtimes[detail.Run.Runtime]
	if !exists {
		return domain.RunDetail{}, nil, &store.Error{Code: store.CodeAdapterUnavailable, Message: "run runtime driver is unavailable"}
	}
	return detail, driver, nil
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
