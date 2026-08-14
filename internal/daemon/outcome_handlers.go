package daemon

import (
	"context"
	"encoding/json"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleOutcomeCommitmentCreate(request localapi.Request) localapi.Response {
	var params localapi.OutcomeCommitmentCreateParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil || params.AcceptanceCriteria == nil {
		return invalidParamsResponse(request, "outcome.commitment.create requires workspace, task, key, title, acceptance_criteria, and idempotency_key")
	}
	result, err := s.store.CreateDeliverableCommitment(context.Background(), store.CreateDeliverableCommitmentCommand{
		WorkspaceIdentifier: params.Workspace,
		TaskID:              params.Task,
		Key:                 params.Key,
		Title:               params.Title,
		Description:         params.Description,
		AcceptanceCriteria:  params.AcceptanceCriteria,
		IdempotencyKey:      params.IdempotencyKey,
		CorrelationID:       request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.OutcomeCommitmentMutationResult{
		Schema: localapi.OutcomeCommitmentMutationSchema, Type: "outcome_commitment_mutation",
		Commitment: result.Commitment, EventSequence: result.EventSequence,
	})
}

func (s *server) handleOutcomeCommitmentShow(request localapi.Request) localapi.Response {
	var params localapi.OutcomeCommitmentQueryParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Commitment) == "" || params.Project != "" || params.Objective != "" || params.Task != "" || params.Limit != 0 {
		return invalidParamsResponse(request, "outcome.commitment.show requires only workspace and commitment")
	}
	value, err := s.store.DeliverableCommitment(context.Background(), params.Workspace, params.Commitment)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.OutcomeCommitmentShowResult{
		Schema: localapi.OutcomeCommitmentShowSchema, Type: "outcome_commitment", Commitment: value,
	})
}

func (s *server) handleOutcomeCommitmentList(request localapi.Request) localapi.Response {
	var params localapi.OutcomeCommitmentQueryParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || params.Commitment != "" || params.Limit < 0 || params.Limit > 100 {
		return invalidParamsResponse(request, "outcome.commitment.list requires workspace and bounded project, objective, task, and limit filters")
	}
	values, err := s.store.DeliverableCommitments(context.Background(), store.ListDeliverableCommitmentsQuery{
		WorkspaceIdentifier: params.Workspace,
		ProjectIdentifier:   params.Project,
		ObjectiveID:         params.Objective,
		TaskID:              params.Task,
		Limit:               params.Limit,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.OutcomeCommitmentListResult{
		Schema: localapi.OutcomeCommitmentListSchema, Type: "outcome_commitment_list", Commitments: values,
	})
}

func (s *server) handleOutcomePropose(request localapi.Request) localapi.Response {
	var params localapi.OutcomeProposeParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil || !completeOutcomeAssessmentInput(params.Assessment) {
		return invalidParamsResponse(request, "outcome.assessment.propose requires workspace, task, commitment, structured assessment, and idempotency_key")
	}
	result, err := s.store.ProposeOutcomeAssessment(context.Background(), store.ProposeOutcomeAssessmentCommand{
		WorkspaceIdentifier:    params.Workspace,
		TaskID:                 params.Task,
		CommitmentID:           params.Commitment,
		SupersedesAssessmentID: params.SupersedesOutcome,
		Input:                  params.Assessment,
		IdempotencyKey:         params.IdempotencyKey,
		CorrelationID:          request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.OutcomeMutationResult{
		Schema: localapi.OutcomeMutationSchema, Type: "outcome_mutation", Detail: result.Detail, EventSequence: result.EventSequence,
	})
}

func (s *server) handleOutcomeShow(request localapi.Request) localapi.Response {
	var params localapi.OutcomeQueryParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Outcome) == "" || params.Project != "" || params.Objective != "" || params.Task != "" || params.Commitment != "" || params.ReviewState != "" || params.Conclusion != "" || params.Limit != 0 {
		return invalidParamsResponse(request, "outcome.assessment.show requires only workspace and outcome")
	}
	value, err := s.store.OutcomeAssessment(context.Background(), params.Workspace, params.Outcome)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.OutcomeShowResult{
		Schema: localapi.OutcomeShowSchema, Type: "outcome", Detail: value,
	})
}

func (s *server) handleOutcomeList(request localapi.Request) localapi.Response {
	var params localapi.OutcomeQueryParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || params.Outcome != "" || params.Limit < 0 || params.Limit > 100 {
		return invalidParamsResponse(request, "outcome.assessment.list requires workspace and bounded project, objective, task, commitment, review_state, conclusion, and limit filters")
	}
	values, err := s.store.OutcomeAssessments(context.Background(), store.ListOutcomeAssessmentsQuery{
		WorkspaceIdentifier: params.Workspace,
		ProjectIdentifier:   params.Project,
		ObjectiveID:         params.Objective,
		TaskID:              params.Task,
		CommitmentID:        params.Commitment,
		ReviewState:         params.ReviewState,
		Conclusion:          params.Conclusion,
		Limit:               params.Limit,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.OutcomeListResult{
		Schema: localapi.OutcomeListSchema, Type: "outcome_list", Outcomes: values,
	})
}

func (s *server) handleOutcomeDecision(request localapi.Request, accept bool) localapi.Response {
	var params localapi.OutcomeDecisionParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "outcome decision requires workspace, outcome, expected_state_revision, optional decision_note, and idempotency_key")
	}
	command := store.DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier:   params.Workspace,
		AssessmentID:          params.Outcome,
		ExpectedStateRevision: params.ExpectedStateRevision,
		DecisionNote:          params.DecisionNote,
		IdempotencyKey:        params.IdempotencyKey,
		CorrelationID:         request.ID,
	}
	var result store.OutcomeAssessmentMutationResult
	var err error
	if accept {
		result, err = s.store.AcceptOutcomeAssessment(context.Background(), command)
	} else {
		result, err = s.store.RejectOutcomeAssessment(context.Background(), command)
	}
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.OutcomeMutationResult{
		Schema: localapi.OutcomeMutationSchema, Type: "outcome_mutation", Detail: result.Detail, EventSequence: result.EventSequence,
	})
}

func (s *server) handleCheckpointCreate(request localapi.Request) localapi.Response {
	var params localapi.CheckpointCreateParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "checkpoint.create requires workspace, scope_type, scope_identifier, and idempotency_key")
	}
	result, err := s.store.CreateOwnerCheckpoint(context.Background(), store.CreateOwnerCheckpointCommand{
		WorkspaceIdentifier: params.Workspace,
		ScopeType:           params.ScopeType,
		ScopeIdentifier:     params.ScopeIdentifier,
		IdempotencyKey:      params.IdempotencyKey,
		CorrelationID:       request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckpointMutationResult{
		Schema: localapi.CheckpointMutationSchema, Type: "checkpoint_mutation", Checkpoint: result.Checkpoint, EventSequence: result.EventSequence,
	})
}

func (s *server) handleCheckpointShow(request localapi.Request) localapi.Response {
	var params localapi.CheckpointQueryParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Checkpoint) == "" || params.ScopeType != "" || params.ScopeIdentifier != "" || params.Limit != 0 {
		return invalidParamsResponse(request, "checkpoint.show requires only workspace and checkpoint")
	}
	value, err := s.store.OwnerCheckpoint(context.Background(), params.Workspace, params.Checkpoint)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckpointShowResult{
		Schema: localapi.CheckpointShowSchema, Type: "checkpoint", Checkpoint: value,
	})
}

func (s *server) handleCheckpointList(request localapi.Request) localapi.Response {
	var params localapi.CheckpointQueryParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || params.Checkpoint != "" || params.Limit < 0 || params.Limit > 100 {
		return invalidParamsResponse(request, "checkpoint.list requires workspace and bounded scope and limit filters")
	}
	values, err := s.store.OwnerCheckpoints(context.Background(), store.ListOwnerCheckpointsQuery{
		WorkspaceIdentifier: params.Workspace,
		ScopeType:           params.ScopeType,
		ScopeIdentifier:     params.ScopeIdentifier,
		Limit:               params.Limit,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckpointListResult{
		Schema: localapi.CheckpointListSchema, Type: "checkpoint_list", Checkpoints: values,
	})
}

func (s *server) handleBriefingShow(request localapi.Request) localapi.Response {
	var params localapi.BriefingShowParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "briefing.show requires workspace, scope_type, scope_identifier, and optional since_checkpoint")
	}
	value, err := s.store.ShowManagementBriefing(context.Background(), store.ShowManagementBriefingQuery{
		WorkspaceIdentifier: params.Workspace,
		ScopeType:           params.ScopeType,
		ScopeIdentifier:     params.ScopeIdentifier,
		SinceCheckpointID:   params.SinceCheckpoint,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.BriefingShowResult{
		Schema: localapi.BriefingShowSchema, Type: "management_briefing", Briefing: value,
	})
}

func (s *server) handleBriefingExplain(request localapi.Request) localapi.Response {
	var params localapi.BriefingExplainParams
	if err := decodeOutcomeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "briefing.explain requires workspace, briefing, and claim")
	}
	value, err := s.store.ExplainManagementBriefingClaim(context.Background(), store.ExplainManagementBriefingClaimQuery{
		WorkspaceIdentifier: params.Workspace,
		BriefingID:          params.Briefing,
		ClaimID:             params.Claim,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.BriefingExplainResult{
		Schema: localapi.BriefingExplainSchema, Type: "briefing_claim_explanation", Explanation: value,
	})
}

func decodeOutcomeParams(data json.RawMessage, target any) error {
	return decodeParams(data, target)
}

func completeOutcomeAssessmentInput(input domain.OutcomeAssessmentInput) bool {
	return input.DeliveredScope != nil &&
		input.UnmetScope != nil &&
		input.DecisionRevisionIDs != nil &&
		input.Evidence != nil &&
		input.Effects != nil &&
		input.Deviations != nil &&
		input.Risks != nil &&
		input.Unknowns != nil &&
		input.FollowUpTaskIDs != nil &&
		input.OwnerAttention != nil
}
