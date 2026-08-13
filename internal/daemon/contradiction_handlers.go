package daemon

import (
	"context"
	"strings"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleContradictionReport(request localapi.Request) localapi.Response {
	var params localapi.ContradictionReportParams
	if err := decodeParams(request.Params, &params); err != nil ||
		strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.LeftRevision) == "" ||
		strings.TrimSpace(params.RightRevision) == "" || !validContradictionText(params.Reason, true) ||
		strings.TrimSpace(params.IdempotencyKey) == "" {
		return invalidParamsResponse(request, "contradiction.report requires workspace, two exact knowledge revisions, reason, and idempotency_key")
	}
	result, err := s.store.ReportKnowledgeContradiction(context.Background(), store.ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: params.Workspace,
		LeftRevisionID:      params.LeftRevision,
		RightRevisionID:     params.RightRevision,
		ReportNote:          params.Reason,
		Actor:               store.OwnerKnowledgeActor(),
		IdempotencyKey:      params.IdempotencyKey,
		CorrelationID:       request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalContradictionMutation(request, result)
}

func (s *server) handleContradictionShow(request localapi.Request) localapi.Response {
	var params localapi.ContradictionQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Contradiction) == "" {
		return invalidParamsResponse(request, "contradiction.show requires workspace and contradiction")
	}
	detail, err := s.store.KnowledgeContradictionDetail(context.Background(), params.Workspace, params.Contradiction)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ContradictionShowResult{
		Schema: localapi.ContradictionShowSchema, Type: "knowledge_contradiction_detail", Detail: detail,
	})
}

func (s *server) handleContradictionList(request localapi.Request) localapi.Response {
	var params localapi.ContradictionListParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Project) == "" ||
		(params.Status != "" && !domain.ValidKnowledgeContradictionStatus(params.Status)) ||
		(params.Limit != nil && (*params.Limit < 1 || *params.Limit > 200)) {
		return invalidParamsResponse(request, "contradiction.list requires workspace, project, and an optional limit between 1 and 200")
	}
	limit := 50
	if params.Limit != nil {
		limit = *params.Limit
	}
	details, err := s.store.ListKnowledgeContradictionDetails(context.Background(), store.ListKnowledgeContradictionsQuery{
		WorkspaceIdentifier: params.Workspace,
		ProjectIdentifier:   params.Project,
		Status:              params.Status,
		RevisionID:          params.Revision,
		Limit:               limit,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ContradictionListResult{
		Schema: localapi.ContradictionListSchema, Type: "knowledge_contradiction_list",
		List: domain.KnowledgeContradictionList{Details: details},
	})
}

func (s *server) handleContradictionConfirm(request localapi.Request) localapi.Response {
	var params localapi.ContradictionDecisionParams
	if err := decodeParams(request.Params, &params); err != nil || !validContradictionDecisionParams(params) {
		return invalidParamsResponse(request, "contradiction.confirm requires workspace, contradiction, expected_state_revision, and idempotency_key")
	}
	result, err := s.store.ConfirmKnowledgeContradiction(context.Background(), store.DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier:   params.Workspace,
		ContradictionID:       params.Contradiction,
		ExpectedStateRevision: params.ExpectedStateRevision,
		Note:                  params.Note,
		Actor:                 store.OwnerKnowledgeActor(),
		IdempotencyKey:        params.IdempotencyKey,
		CorrelationID:         request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalContradictionMutation(request, result)
}

func (s *server) handleContradictionDismiss(request localapi.Request) localapi.Response {
	var params localapi.ContradictionDecisionParams
	if err := decodeParams(request.Params, &params); err != nil || !validContradictionDecisionParams(params) {
		return invalidParamsResponse(request, "contradiction.dismiss requires workspace, contradiction, expected_state_revision, and idempotency_key")
	}
	result, err := s.store.DismissKnowledgeContradiction(context.Background(), store.DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier:   params.Workspace,
		ContradictionID:       params.Contradiction,
		ExpectedStateRevision: params.ExpectedStateRevision,
		Note:                  params.Note,
		Actor:                 store.OwnerKnowledgeActor(),
		IdempotencyKey:        params.IdempotencyKey,
		CorrelationID:         request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalContradictionMutation(request, result)
}

func marshalContradictionMutation(request localapi.Request, result store.KnowledgeContradictionMutationResult) localapi.Response {
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ContradictionMutationResult{
		Schema: localapi.ContradictionMutationSchema, Type: "contradiction_mutation",
		Detail: result.Detail, AuthorityCheck: result.AuthorityCheck, EventSequence: result.EventSequence,
	})
}

func validContradictionDecisionParams(params localapi.ContradictionDecisionParams) bool {
	return strings.TrimSpace(params.Workspace) != "" && strings.TrimSpace(params.Contradiction) != "" &&
		params.ExpectedStateRevision >= 1 && validContradictionText(params.Note, false) &&
		strings.TrimSpace(params.IdempotencyKey) != ""
}

func validContradictionText(value string, required bool) bool {
	if !utf8.ValidString(value) || len(value) > 2048 || strings.ContainsRune(value, '\x00') {
		return false
	}
	return !required || strings.TrimSpace(value) != ""
}
