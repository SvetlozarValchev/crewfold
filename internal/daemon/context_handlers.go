package daemon

import (
	"context"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleContextBuild(request localapi.Request) localapi.Response {
	var params localapi.ContextBuildParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "context.build requires workspace, task, agent, expected_task_revision, and idempotency_key")
	}
	result, err := s.store.BuildContextPacket(context.Background(), store.BuildContextCommand{
		WorkspaceIdentifier: params.Workspace, TaskID: params.Task, AgentIdentifier: params.Agent,
		CheckoutIdentifier: params.Checkout, KnowledgeRevisionIDs: params.KnowledgeRevisionIDs,
		ExpectedTaskRevision: params.ExpectedTaskRevision,
		IdempotencyKey:       params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ContextBuildResult{Schema: localapi.ContextBuildSchema, Type: "context_packet", Packet: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleContextShow(request localapi.Request) localapi.Response {
	var params localapi.ContextQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Context) == "" {
		return invalidParamsResponse(request, "context.show requires workspace and context")
	}
	packet, err := s.store.ContextPacket(context.Background(), params.Workspace, params.Context)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	schema := localapi.ContextShowSchema
	if packet.Schema == domain.ContextPacketSchemaV5 {
		schema = localapi.ContextShowSchemaV5
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ContextShowResult{Schema: schema, Type: "context_packet", Packet: packet})
}

func (s *server) handleContextExplain(request localapi.Request) localapi.Response {
	var params localapi.ContextQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Context) == "" {
		return invalidParamsResponse(request, "context.explain requires workspace and context")
	}
	explanation, err := s.store.ExplainContextPacket(context.Background(), params.Workspace, params.Context)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ContextExplainResult{Schema: localapi.ContextExplainSchema, Type: "context_explanation", Explanation: explanation})
}

func (s *server) handleContextRefresh(request localapi.Request) localapi.Response {
	var params localapi.ContextRefreshParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Run) == "" || strings.TrimSpace(params.IdempotencyKey) == "" {
		return invalidParamsResponse(request, "context.refresh requires workspace, run, and idempotency_key")
	}
	result, err := s.store.RefreshContext(context.Background(), store.RefreshContextCommand{
		WorkspaceIdentifier: params.Workspace, RunID: params.Run,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ContextRefreshResult{
		Schema: localapi.ContextRefreshSchema, Type: "context_refresh", ContextRefreshResult: result,
	})
}

func (s *server) handleContextDeltaList(request localapi.Request) localapi.Response {
	var params localapi.ContextDeltaListParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Run) == "" || params.AfterSequence == nil || *params.AfterSequence < 0 || params.Limit < 1 || params.Limit > 100 {
		return invalidParamsResponse(request, "context.delta.list requires workspace, run, after_sequence, and a limit from 1 to 100")
	}
	list, err := s.store.ListContextDeltas(context.Background(), store.ListContextDeltasQuery{
		WorkspaceIdentifier: params.Workspace, RunID: params.Run,
		AfterSequence: *params.AfterSequence, Limit: params.Limit,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ContextDeltaListResult{
		Schema: localapi.ContextDeltaListSchema, Type: "context_delta_list", ContextDeltaList: list,
	})
}

func (s *server) handleContextDeltaShow(request localapi.Request) localapi.Response {
	var params localapi.ContextDeltaQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Delta) == "" {
		return invalidParamsResponse(request, "context.delta.show requires workspace and delta")
	}
	delta, err := s.store.ContextDelta(context.Background(), params.Workspace, params.Delta)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ContextDeltaShowResult{
		Schema: localapi.ContextDeltaShowSchema, Type: "context_delta", Delta: delta,
	})
}

func (s *server) handleContextDeltaExplain(request localapi.Request) localapi.Response {
	var params localapi.ContextDeltaQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Delta) == "" {
		return invalidParamsResponse(request, "context.delta.explain requires workspace and delta")
	}
	explanation, err := s.store.ExplainContextDelta(context.Background(), params.Workspace, params.Delta)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ContextDeltaExplainResult{
		Schema: localapi.ContextDeltaExplainSchema, Type: "context_delta_explanation", Explanation: explanation,
	})
}
