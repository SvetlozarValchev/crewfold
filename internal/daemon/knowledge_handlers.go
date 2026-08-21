package daemon

import (
	"context"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleKnowledgePropose(request localapi.Request) localapi.Response {
	var params localapi.KnowledgeProposeParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "knowledge.propose requires workspace, type, title, body, quality metadata, structured sources, and idempotency_key")
	}
	result, err := s.store.ProposeKnowledge(context.Background(), store.ProposeKnowledgeCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, TaskScopeID: params.TaskScopeID,
		Type: params.Type, Title: params.Title, Body: params.Body, Confidence: params.Confidence,
		VerificationStatus: params.VerificationStatus, FreshnessPolicy: params.FreshnessPolicy,
		FreshUntil: params.FreshUntil, Sources: params.Sources, SupersedesRevisionID: params.SupersedesRevisionID,
		Actor: store.OwnerKnowledgeActor(), IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalKnowledgeMutation(request, result)
}

func (s *server) handleKnowledgeShow(request localapi.Request) localapi.Response {
	var params localapi.KnowledgeQueryParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "knowledge.show requires workspace and knowledge_revision")
	}
	detail, err := s.store.KnowledgeDetail(context.Background(), params.Workspace, params.KnowledgeRevision)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.KnowledgeShowResult{
		Schema: localapi.KnowledgeShowSchema, Type: "knowledge_detail",
		Detail: detail,
	})
}

func (s *server) handleKnowledgeList(request localapi.Request) localapi.Response {
	var params localapi.KnowledgeListParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "knowledge.list requires workspace and project")
	}
	revisions, err := s.store.ListKnowledge(context.Background(), store.ListKnowledgeQuery{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, TaskScopeID: params.TaskScopeID,
		Type: params.Type, ReviewStatus: params.ReviewStatus, CurrencyStatus: params.CurrencyStatus,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	presentations, err := s.store.KnowledgePresentations(context.Background(), params.Workspace, revisions)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.KnowledgeListResult{
		Schema: localapi.KnowledgeListSchema, Type: "knowledge_list",
		List: domain.KnowledgeList{Revisions: revisions, Presentations: presentations},
	})
}

func (s *server) handleKnowledgeSearch(request localapi.Request) localapi.Response {
	var params localapi.KnowledgeSearchParams
	if err := decodeParams(request.Params, &params); err != nil || (params.Limit != nil && (*params.Limit < 1 || *params.Limit > 100)) {
		return invalidParamsResponse(request, "knowledge.search requires workspace, project, query, and limit between 1 and 100 when supplied")
	}
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	result, err := s.store.SearchKnowledge(context.Background(), store.SearchKnowledgeQuery{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project,
		TaskIdentifier: params.Task, Type: params.Type, Query: params.Query, Limit: limit,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.KnowledgeSearchResult{
		Schema: localapi.KnowledgeSearchSchema, Type: "knowledge_search", Search: result,
	})
}

func (s *server) handleKnowledgeIndexStatus(request localapi.Request) localapi.Response {
	var params localapi.KnowledgeIndexStatusParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "knowledge.index.status requires workspace")
	}
	status, err := s.store.KnowledgeIndexStatus(context.Background(), params.Workspace)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.KnowledgeIndexStatusResult{
		Schema: localapi.KnowledgeIndexStatusSchema, Type: "knowledge_index_status", Index: status,
	})
}

func (s *server) handleKnowledgeIndexRebuild(request localapi.Request) localapi.Response {
	var params localapi.KnowledgeIndexRebuildParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "knowledge.index.rebuild requires workspace and idempotency_key")
	}
	result, err := s.store.RebuildKnowledgeIndex(context.Background(), store.RebuildKnowledgeIndexCommand{
		WorkspaceIdentifier: params.Workspace, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.KnowledgeIndexRebuildResult{
		Schema: localapi.KnowledgeIndexRebuildSchema, Type: "knowledge_index_rebuild", Index: result.Index,
	})
}

func (s *server) handleKnowledgeAccept(request localapi.Request) localapi.Response {
	var params localapi.KnowledgeDecisionParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "knowledge.accept requires workspace, knowledge_revision, expected_state_revision, and idempotency_key")
	}
	result, err := s.store.AcceptKnowledge(context.Background(), store.AcceptKnowledgeCommand{
		WorkspaceIdentifier: params.Workspace, RevisionID: params.KnowledgeRevision,
		ExpectedStateRevision: params.ExpectedStateRevision, DecisionNote: params.DecisionNote,
		Actor: store.OwnerKnowledgeActor(), IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalKnowledgeMutation(request, result)
}

func (s *server) handleKnowledgeReject(request localapi.Request) localapi.Response {
	var params localapi.KnowledgeDecisionParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "knowledge.reject requires workspace, knowledge_revision, expected_state_revision, and idempotency_key")
	}
	result, err := s.store.RejectKnowledge(context.Background(), store.RejectKnowledgeCommand{
		WorkspaceIdentifier: params.Workspace, RevisionID: params.KnowledgeRevision,
		ExpectedStateRevision: params.ExpectedStateRevision, DecisionNote: params.DecisionNote,
		Actor: store.OwnerKnowledgeActor(), IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalKnowledgeMutation(request, result)
}

func (s *server) handleKnowledgeMarkStale(request localapi.Request) localapi.Response {
	var params localapi.KnowledgeMarkStaleParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "knowledge.mark_stale requires workspace, knowledge_revision, expected_state_revision, reason, and idempotency_key")
	}
	result, err := s.store.MarkKnowledgeStale(context.Background(), store.MarkKnowledgeStaleCommand{
		WorkspaceIdentifier: params.Workspace, RevisionID: params.KnowledgeRevision,
		ExpectedStateRevision: params.ExpectedStateRevision, Reason: params.Reason,
		Actor: store.OwnerKnowledgeActor(), IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalKnowledgeMutation(request, result)
}

func (s *server) handleKnowledgeDispute(request localapi.Request) localapi.Response {
	var params localapi.KnowledgeQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.KnowledgeRevision) == "" {
		return invalidParamsResponse(request, "knowledge.dispute requires workspace and knowledge_revision")
	}
	dispute, err := s.store.KnowledgeRevisionDispute(context.Background(), params.Workspace, params.KnowledgeRevision)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.KnowledgeDisputeResult{
		Schema: localapi.KnowledgeDisputeSchema, Type: "knowledge_revision_dispute", Dispute: dispute,
	})
}

func marshalKnowledgeMutation(request localapi.Request, result store.KnowledgeMutationResult) localapi.Response {
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.KnowledgeMutationResult{
		Schema: localapi.KnowledgeMutationSchema, Type: "knowledge_mutation", Revision: result.Revision,
		AuthorityCheck: result.AuthorityCheck, EventSequence: result.EventSequence,
	})
}
