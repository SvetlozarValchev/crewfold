package daemon

import (
	"context"
	"strings"

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
		CheckoutIdentifier: params.Checkout, ExpectedTaskRevision: params.ExpectedTaskRevision,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
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
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ContextShowResult{Schema: localapi.ContextShowSchema, Type: "context_packet", Packet: packet})
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
