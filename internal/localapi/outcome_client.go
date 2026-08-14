package localapi

import (
	"context"
	"fmt"

	"crewfold/internal/domain"
)

func (c *Client) OutcomeCommitmentCreate(ctx context.Context, params OutcomeCommitmentCreateParams) (OutcomeCommitmentMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	if params.AcceptanceCriteria == nil {
		params.AcceptanceCriteria = []string{}
	}
	var result OutcomeCommitmentMutationResult
	err := c.callParamsStrict(ctx, MethodOutcomeCommitmentCreate, params, &result)
	return result, validateOutcomeResult(err, MethodOutcomeCommitmentCreate, result.Schema, result.Type, OutcomeCommitmentMutationSchema, "outcome_commitment_mutation")
}

func (c *Client) OutcomeCommitmentShow(ctx context.Context, workspace, commitment string) (OutcomeCommitmentShowResult, error) {
	var result OutcomeCommitmentShowResult
	err := c.callParamsStrict(ctx, MethodOutcomeCommitmentShow, OutcomeCommitmentQueryParams{Workspace: workspace, Commitment: commitment}, &result)
	return result, validateOutcomeResult(err, MethodOutcomeCommitmentShow, result.Schema, result.Type, OutcomeCommitmentShowSchema, "outcome_commitment")
}

func (c *Client) OutcomeCommitmentList(ctx context.Context, params OutcomeCommitmentQueryParams) (OutcomeCommitmentListResult, error) {
	var result OutcomeCommitmentListResult
	err := c.callParamsStrict(ctx, MethodOutcomeCommitmentList, params, &result)
	return result, validateOutcomeResult(err, MethodOutcomeCommitmentList, result.Schema, result.Type, OutcomeCommitmentListSchema, "outcome_commitment_list")
}

func (c *Client) OutcomePropose(ctx context.Context, params OutcomeProposeParams) (OutcomeMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	normalizeOutcomeAssessmentInput(&params.Assessment)
	var result OutcomeMutationResult
	err := c.callParamsStrict(ctx, MethodOutcomePropose, params, &result)
	return result, validateOutcomeResult(err, MethodOutcomePropose, result.Schema, result.Type, OutcomeMutationSchema, "outcome_mutation")
}

func (c *Client) OutcomeShow(ctx context.Context, workspace, outcome string) (OutcomeShowResult, error) {
	var result OutcomeShowResult
	err := c.callParamsStrict(ctx, MethodOutcomeShow, OutcomeQueryParams{Workspace: workspace, Outcome: outcome}, &result)
	return result, validateOutcomeResult(err, MethodOutcomeShow, result.Schema, result.Type, OutcomeShowSchema, "outcome")
}

func (c *Client) OutcomeList(ctx context.Context, params OutcomeQueryParams) (OutcomeListResult, error) {
	var result OutcomeListResult
	err := c.callParamsStrict(ctx, MethodOutcomeList, params, &result)
	return result, validateOutcomeResult(err, MethodOutcomeList, result.Schema, result.Type, OutcomeListSchema, "outcome_list")
}

func (c *Client) OutcomeAccept(ctx context.Context, params OutcomeDecisionParams) (OutcomeMutationResult, error) {
	return c.decideOutcome(ctx, MethodOutcomeAccept, params)
}

func (c *Client) OutcomeReject(ctx context.Context, params OutcomeDecisionParams) (OutcomeMutationResult, error) {
	return c.decideOutcome(ctx, MethodOutcomeReject, params)
}

func (c *Client) decideOutcome(ctx context.Context, method string, params OutcomeDecisionParams) (OutcomeMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result OutcomeMutationResult
	err := c.callParamsStrict(ctx, method, params, &result)
	return result, validateOutcomeResult(err, method, result.Schema, result.Type, OutcomeMutationSchema, "outcome_mutation")
}

func (c *Client) CheckpointCreate(ctx context.Context, params CheckpointCreateParams) (CheckpointMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckpointMutationResult
	err := c.callParamsStrict(ctx, MethodCheckpointCreate, params, &result)
	return result, validateOutcomeResult(err, MethodCheckpointCreate, result.Schema, result.Type, CheckpointMutationSchema, "checkpoint_mutation")
}

func (c *Client) CheckpointShow(ctx context.Context, workspace, checkpoint string) (CheckpointShowResult, error) {
	var result CheckpointShowResult
	err := c.callParamsStrict(ctx, MethodCheckpointShow, CheckpointQueryParams{Workspace: workspace, Checkpoint: checkpoint}, &result)
	return result, validateOutcomeResult(err, MethodCheckpointShow, result.Schema, result.Type, CheckpointShowSchema, "checkpoint")
}

func (c *Client) CheckpointList(ctx context.Context, params CheckpointQueryParams) (CheckpointListResult, error) {
	var result CheckpointListResult
	err := c.callParamsStrict(ctx, MethodCheckpointList, params, &result)
	return result, validateOutcomeResult(err, MethodCheckpointList, result.Schema, result.Type, CheckpointListSchema, "checkpoint_list")
}

func (c *Client) BriefingShow(ctx context.Context, params BriefingShowParams) (BriefingShowResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, params.Workspace)
	if err != nil {
		return BriefingShowResult{}, err
	}
	params.Workspace = workspaceID
	switch params.ScopeType {
	case "workspace":
		params.ScopeIdentifier, err = c.resolveOperatorWorkspace(ctx, params.ScopeIdentifier)
	case "project":
		params.ScopeIdentifier, err = c.resolveOperatorProject(ctx, workspaceID, params.ScopeIdentifier)
	}
	if err != nil {
		return BriefingShowResult{}, err
	}
	var result BriefingShowResult
	err = c.callParamsStrict(ctx, MethodBriefingShow, params, &result)
	return result, validateOutcomeResult(err, MethodBriefingShow, result.Schema, result.Type, BriefingShowSchema, "management_briefing")
}

func (c *Client) BriefingExplain(ctx context.Context, params BriefingExplainParams) (BriefingExplainResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, params.Workspace)
	if err != nil {
		return BriefingExplainResult{}, err
	}
	params.Workspace = workspaceID
	var result BriefingExplainResult
	err = c.callParamsStrict(ctx, MethodBriefingExplain, params, &result)
	return result, validateOutcomeResult(err, MethodBriefingExplain, result.Schema, result.Type, BriefingExplainSchema, "briefing_claim_explanation")
}

func normalizeOutcomeAssessmentInput(input *domain.OutcomeAssessmentInput) {
	if input.DeliveredScope == nil {
		input.DeliveredScope = []string{}
	}
	if input.UnmetScope == nil {
		input.UnmetScope = []string{}
	}
	if input.DecisionRevisionIDs == nil {
		input.DecisionRevisionIDs = []string{}
	}
	if input.Evidence == nil {
		input.Evidence = []domain.OutcomeEvidenceInput{}
	}
	if input.Effects == nil {
		input.Effects = []domain.OutcomeEffectInput{}
	}
	if input.Deviations == nil {
		input.Deviations = []domain.OutcomeDeviationInput{}
	}
	if input.Risks == nil {
		input.Risks = []domain.OutcomeRiskInput{}
	}
	if input.Unknowns == nil {
		input.Unknowns = []domain.OutcomeUnknownInput{}
	}
	if input.FollowUpTaskIDs == nil {
		input.FollowUpTaskIDs = []string{}
	}
	if input.OwnerAttention == nil {
		input.OwnerAttention = []domain.OutcomeOwnerAttentionInput{}
	}
}

func validateOutcomeResult(callErr error, method, schema, kind, expectedSchema, expectedKind string) error {
	if callErr != nil {
		return callErr
	}
	if schema != expectedSchema || kind != expectedKind {
		return fmt.Errorf("decode local API result %s: discriminator is %q/%q, want %q/%q", method, schema, kind, expectedSchema, expectedKind)
	}
	return nil
}
