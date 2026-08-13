package daemon

import (
	"context"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleCuratorQueue(request localapi.Request) localapi.Response {
	var params localapi.CuratorQueueParams
	if err := decodeParams(request.Params, &params); err != nil ||
		strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Project) == "" ||
		(params.Limit != nil && (*params.Limit < 1 || *params.Limit > 200)) {
		return invalidParamsResponse(request, "curator.queue requires workspace, project, an optional opaque after cursor, and an optional limit between 1 and 200")
	}
	limit := 50
	if params.Limit != nil {
		limit = *params.Limit
	}
	queue, err := s.store.QueueCuratorRevisions(context.Background(), store.CuratorQueueQuery{
		WorkspaceIdentifier: params.Workspace,
		ProjectIdentifier:   params.Project,
		After:               params.After,
		Limit:               limit,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CuratorQueueResult{
		Schema: localapi.CuratorQueueSchema,
		Type:   "curator_queue",
		Queue:  queue,
	})
}

func (s *server) handleCuratorRuleConfigure(request localapi.Request) localapi.Response {
	var params localapi.CuratorRuleConfigureParams
	if err := decodeParams(request.Params, &params); err != nil ||
		strings.TrimSpace(params.Workspace) == "" || params.Rule != domain.CuratorRuleAcceptedMeetingResolutionCopy ||
		params.Enabled == nil || params.ExpectedRevision < 1 || strings.TrimSpace(params.IdempotencyKey) == "" {
		return invalidParamsResponse(request, "curator.rule.configure requires workspace, the supported rule, enabled, expected_revision at least one, and idempotency_key")
	}
	result, err := s.store.ConfigureCuratorRule(context.Background(), store.ConfigureCuratorRuleCommand{
		WorkspaceIdentifier: params.Workspace,
		RuleName:            params.Rule,
		Enabled:             *params.Enabled,
		ExpectedRevision:    params.ExpectedRevision,
		IdempotencyKey:      params.IdempotencyKey,
		CorrelationID:       request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CuratorRuleMutationResult{
		Schema:        localapi.CuratorRuleMutationSchema,
		Type:          "curator_rule_mutation",
		Rule:          result.Rule,
		EventSequence: result.EventSequence,
	})
}

func (s *server) handleCuratorProcess(request localapi.Request) localapi.Response {
	var params localapi.CuratorProcessParams
	if err := decodeParams(request.Params, &params); err != nil ||
		strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Project) == "" ||
		strings.TrimSpace(params.IdempotencyKey) == "" {
		return invalidParamsResponse(request, "curator.process requires workspace, project, an optional apply_safe Boolean, and idempotency_key")
	}
	result, err := s.store.ProcessCurator(context.Background(), store.ProcessCuratorCommand{
		WorkspaceIdentifier: params.Workspace,
		ProjectIdentifier:   params.Project,
		ApplySafe:           params.ApplySafe,
		IdempotencyKey:      params.IdempotencyKey,
		CorrelationID:       request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CuratorProcessResult{
		Schema:        localapi.CuratorProcessSchema,
		Type:          "curator_process",
		Process:       result.Process,
		EventSequence: result.EventSequence,
	})
}
