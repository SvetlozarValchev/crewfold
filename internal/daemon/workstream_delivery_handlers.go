package daemon

import (
	"context"
	"strings"

	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleWorkstreamDeliveryShow(request localapi.Request) localapi.Response {
	var params localapi.WorkstreamDeliveryQueryParams
	if err := decodeCheckParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Objective) == "" {
		return invalidParamsResponse(request, "workstream.delivery.show requires workspace and objective")
	}
	delivery, err := s.store.WorkstreamDelivery(context.Background(), params.Workspace, params.Objective)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.WorkstreamDeliveryShowResult{Schema: localapi.WorkstreamDeliveryShowSchema, Type: "workstream_delivery", Delivery: delivery})
}

func (s *server) handleWorkstreamDeliveryDecision(request localapi.Request, accept bool) localapi.Response {
	var params localapi.WorkstreamDeliveryDecisionParams
	if err := decodeCheckParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "workstream delivery decision requires exact objective revision, SHA-256, reason where rejecting, and idempotency key")
	}
	command := store.DecideWorkstreamDeliveryCommand{
		WorkspaceIdentifier: params.Workspace, ObjectiveID: params.Objective,
		ExpectedObjectiveRevision: params.ExpectedObjectiveRevision, ExpectedSHA256: params.ExpectedSHA256,
		Reason: params.Reason, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	}
	var result store.WorkstreamDeliveryMutationResult
	var err error
	if accept {
		result, err = s.store.AcceptWorkstreamDelivery(context.Background(), command)
	} else {
		result, err = s.store.RejectWorkstreamDelivery(context.Background(), command)
	}
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.WorkstreamDeliveryMutationResult{Schema: localapi.WorkstreamDeliveryMutationSchema, Type: "workstream_delivery_mutation", Delivery: result.Delivery, EventSequence: result.EventSequence})
}
