package localapi

import "crewfold/internal/domain"

const (
	MethodOutcomeCommitmentCreate = "outcome.commitment.create"
	MethodOutcomeCommitmentShow   = "outcome.commitment.show"
	MethodOutcomeCommitmentList   = "outcome.commitment.list"
	MethodOutcomePropose          = "outcome.assessment.propose"
	MethodOutcomeShow             = "outcome.assessment.show"
	MethodOutcomeList             = "outcome.assessment.list"
	MethodOutcomeAccept           = "outcome.assessment.accept"
	MethodOutcomeReject           = "outcome.assessment.reject"
	MethodCheckpointCreate        = "checkpoint.create"
	MethodCheckpointShow          = "checkpoint.show"
	MethodCheckpointList          = "checkpoint.list"
	MethodBriefingShow            = "briefing.show"
	MethodBriefingExplain         = "briefing.explain"

	OutcomeCommitmentMutationSchema = "urn:crewfold:schema:local-api:outcome-commitment-mutation-result:v1"
	OutcomeCommitmentShowSchema     = "urn:crewfold:schema:local-api:outcome-commitment-show-result:v1"
	OutcomeCommitmentListSchema     = "urn:crewfold:schema:local-api:outcome-commitment-list-result:v1"
	OutcomeMutationSchema           = "urn:crewfold:schema:local-api:outcome-mutation-result:v1"
	OutcomeShowSchema               = "urn:crewfold:schema:local-api:outcome-show-result:v1"
	OutcomeListSchema               = "urn:crewfold:schema:local-api:outcome-list-result:v1"
	CheckpointMutationSchema        = "urn:crewfold:schema:local-api:checkpoint-mutation-result:v1"
	CheckpointShowSchema            = "urn:crewfold:schema:local-api:checkpoint-show-result:v1"
	CheckpointListSchema            = "urn:crewfold:schema:local-api:checkpoint-list-result:v1"
	BriefingShowSchema              = "urn:crewfold:schema:local-api:briefing-show-result:v1"
	BriefingExplainSchema           = "urn:crewfold:schema:local-api:briefing-explain-result:v1"
)

type OutcomeCommitmentCreateParams struct {
	Workspace          string   `json:"workspace"`
	Task               string   `json:"task"`
	Key                string   `json:"key"`
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	IdempotencyKey     string   `json:"idempotency_key"`
}

type OutcomeCommitmentQueryParams struct {
	Workspace  string `json:"workspace"`
	Commitment string `json:"commitment,omitempty"`
	Project    string `json:"project,omitempty"`
	Objective  string `json:"objective,omitempty"`
	Task       string `json:"task,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type OutcomeProposeParams struct {
	Workspace         string                        `json:"workspace"`
	Task              string                        `json:"task"`
	Commitment        string                        `json:"commitment"`
	SupersedesOutcome string                        `json:"supersedes_outcome,omitempty"`
	Assessment        domain.OutcomeAssessmentInput `json:"assessment"`
	IdempotencyKey    string                        `json:"idempotency_key"`
}

type OutcomeQueryParams struct {
	Workspace   string `json:"workspace"`
	Outcome     string `json:"outcome,omitempty"`
	Project     string `json:"project,omitempty"`
	Objective   string `json:"objective,omitempty"`
	Task        string `json:"task,omitempty"`
	Commitment  string `json:"commitment,omitempty"`
	ReviewState string `json:"review_state,omitempty"`
	Conclusion  string `json:"conclusion,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type OutcomeDecisionParams struct {
	Workspace             string `json:"workspace"`
	Outcome               string `json:"outcome"`
	ExpectedStateRevision int64  `json:"expected_state_revision"`
	DecisionNote          string `json:"decision_note,omitempty"`
	IdempotencyKey        string `json:"idempotency_key"`
}

type CheckpointCreateParams struct {
	Workspace       string `json:"workspace"`
	ScopeType       string `json:"scope_type"`
	ScopeIdentifier string `json:"scope_identifier"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type CheckpointQueryParams struct {
	Workspace       string `json:"workspace"`
	Checkpoint      string `json:"checkpoint,omitempty"`
	ScopeType       string `json:"scope_type,omitempty"`
	ScopeIdentifier string `json:"scope_identifier,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

type BriefingShowParams struct {
	Workspace       string `json:"workspace"`
	ScopeType       string `json:"scope_type"`
	ScopeIdentifier string `json:"scope_identifier"`
	SinceCheckpoint string `json:"since_checkpoint,omitempty"`
}

type BriefingExplainParams struct {
	Workspace string `json:"workspace"`
	Briefing  string `json:"briefing"`
	Claim     string `json:"claim"`
}

type OutcomeCommitmentMutationResult struct {
	Schema        string                       `json:"schema"`
	Type          string                       `json:"type"`
	Commitment    domain.DeliverableCommitment `json:"commitment"`
	EventSequence int64                        `json:"event_sequence"`
}

type OutcomeCommitmentShowResult struct {
	Schema     string                       `json:"schema"`
	Type       string                       `json:"type"`
	Commitment domain.DeliverableCommitment `json:"commitment"`
}

type OutcomeCommitmentListResult struct {
	Schema      string                         `json:"schema"`
	Type        string                         `json:"type"`
	Commitments []domain.DeliverableCommitment `json:"commitments"`
}

type OutcomeMutationResult struct {
	Schema        string                         `json:"schema"`
	Type          string                         `json:"type"`
	Detail        domain.OutcomeAssessmentDetail `json:"detail"`
	EventSequence int64                          `json:"event_sequence"`
}

type OutcomeShowResult struct {
	Schema string                         `json:"schema"`
	Type   string                         `json:"type"`
	Detail domain.OutcomeAssessmentDetail `json:"detail"`
}

type OutcomeListResult struct {
	Schema   string                           `json:"schema"`
	Type     string                           `json:"type"`
	Outcomes []domain.OutcomeAssessmentDetail `json:"outcomes"`
}

type CheckpointMutationResult struct {
	Schema        string                 `json:"schema"`
	Type          string                 `json:"type"`
	Checkpoint    domain.OwnerCheckpoint `json:"checkpoint"`
	EventSequence int64                  `json:"event_sequence"`
}

type CheckpointShowResult struct {
	Schema     string                 `json:"schema"`
	Type       string                 `json:"type"`
	Checkpoint domain.OwnerCheckpoint `json:"checkpoint"`
}

type CheckpointListResult struct {
	Schema      string                   `json:"schema"`
	Type        string                   `json:"type"`
	Checkpoints []domain.OwnerCheckpoint `json:"checkpoints"`
}

type BriefingShowResult struct {
	Schema   string                    `json:"schema"`
	Type     string                    `json:"type"`
	Briefing domain.ManagementBriefing `json:"briefing"`
}

type BriefingExplainResult struct {
	Schema      string                          `json:"schema"`
	Type        string                          `json:"type"`
	Explanation domain.BriefingClaimExplanation `json:"explanation"`
}
