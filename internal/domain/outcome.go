package domain

const (
	OutcomeAssessmentProposed   = "proposed"
	OutcomeAssessmentAccepted   = "accepted"
	OutcomeAssessmentRejected   = "rejected"
	OutcomeAssessmentSuperseded = "superseded"

	OutcomeAchieved    = "achieved"
	OutcomePartial     = "partial"
	OutcomeNotAchieved = "not_achieved"
	OutcomeUnknown     = "unknown"

	OutcomeEvidenceHandoff                  = "handoff"
	OutcomeEvidenceCheckRequirementEvidence = "check_requirement_evidence"

	OutcomeEvidenceFresh   = "fresh"
	OutcomeEvidenceStale   = "stale"
	OutcomeEvidenceUnknown = "unknown"

	OutcomeEffectCompatibility = "compatibility"
	OutcomeEffectStability     = "stability"
	OutcomeEffectPositive      = "positive"
	OutcomeEffectNeutral       = "neutral"
	OutcomeEffectNegative      = "negative"
	OutcomeEffectUncertain     = "uncertain"

	OutcomeDeviationScopeChange   = "scope_change"
	OutcomeDeviationDuplicateWork = "duplicate_work"

	OutcomeRiskLow      = "low"
	OutcomeRiskMedium   = "medium"
	OutcomeRiskHigh     = "high"
	OutcomeRiskCritical = "critical"

	OutcomeAttentionNow   = "now"
	OutcomeAttentionNext  = "next"
	OutcomeAttentionLater = "later"

	OwnerCheckpointTask      = "task"
	OwnerCheckpointObjective = "objective"
	OwnerCheckpointProject   = "project"
	OwnerCheckpointWorkspace = "workspace"

	BriefingClaimRequiredDecision = "required_decision"
	BriefingClaimContradiction    = "contradiction"
	BriefingClaimRisk             = "risk"
	BriefingClaimUnknown          = "unknown"
	BriefingClaimVerificationGap  = "verification_gap"
	BriefingClaimDeviation        = "deviation"
	BriefingClaimUnmetCommitment  = "unmet_commitment"
	BriefingClaimAcceptedDelivery = "accepted_delivery"
	BriefingClaimRationale        = "rationale"
	BriefingClaimChange           = "change"

	BriefingOmittedClaimLimit = "claim_limit"
	BriefingOmittedByteLimit  = "byte_limit"

	BriefingSectionRequiredDecisions = "required_decisions"
	BriefingSectionContradictions    = "contradictions"
	BriefingSectionRisksUnknowns     = "risks_unknowns"
	BriefingSectionVerificationGaps  = "verification_gaps"
	BriefingSectionDeviationsUnmet   = "deviations_unmet"
	BriefingSectionAcceptedDelivery  = "accepted_delivery"
	BriefingSectionRationaleChange   = "rationale_change"

	BriefingClaimStatusRequired      = "required"
	BriefingClaimStatusOpen          = "open"
	BriefingClaimStatusMissing       = "missing"
	BriefingClaimStatusStale         = "stale"
	BriefingClaimStatusDisputed      = "disputed"
	BriefingClaimStatusContradictory = "contradictory"
	BriefingClaimStatusRecorded      = "recorded"
	BriefingClaimStatusUnmet         = "unmet"
	BriefingClaimStatusAccepted      = "accepted"
)

type DeliverableCommitment struct {
	ID                 string   `json:"id"`
	WorkspaceID        string   `json:"workspace_id"`
	ProjectID          string   `json:"project_id"`
	ObjectiveID        string   `json:"objective_id"`
	TaskID             string   `json:"task_id"`
	Key                string   `json:"key"`
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	ContentSHA256      string   `json:"content_sha256"`
	CreatedAt          string   `json:"created_at"`
	CreatedBy          string   `json:"created_by"`
}

// OutcomeAssessmentInput is owner-authored structured judgment. Evidence and
// decision inputs identify exact durable records; their authority, class,
// freshness, dispute state, hashes, revisions, and event cursors are derived by
// the Store and cannot be selected by a caller.
type OutcomeAssessmentInput struct {
	Conclusion          string                       `json:"conclusion"`
	DeliveredScope      []string                     `json:"delivered_scope"`
	UnmetScope          []string                     `json:"unmet_scope"`
	DecisionRevisionIDs []string                     `json:"decision_revision_ids"`
	Evidence            []OutcomeEvidenceInput       `json:"evidence"`
	Effects             []OutcomeEffectInput         `json:"effects"`
	Deviations          []OutcomeDeviationInput      `json:"deviations"`
	Risks               []OutcomeRiskInput           `json:"risks"`
	Unknowns            []OutcomeUnknownInput        `json:"unknowns"`
	FollowUpTaskIDs     []string                     `json:"follow_up_task_ids"`
	OwnerAttention      []OutcomeOwnerAttentionInput `json:"owner_attention"`
}

type OutcomeEvidenceInput struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
}

type OutcomeEffectInput struct {
	Kind      string `json:"kind"`
	Direction string `json:"direction"`
	Summary   string `json:"summary"`
}

type OutcomeDeviationInput struct {
	Kind          string `json:"kind"`
	Summary       string `json:"summary"`
	RelatedTaskID string `json:"related_task_id,omitempty"`
}

type OutcomeRiskInput struct {
	Severity   string `json:"severity"`
	Summary    string `json:"summary"`
	Mitigation string `json:"mitigation,omitempty"`
}

type OutcomeUnknownInput struct {
	Summary string `json:"summary"`
}

type OutcomeOwnerAttentionInput struct {
	Urgency string `json:"urgency"`
	Action  string `json:"action"`
	Reason  string `json:"reason"`
}

type OutcomeAssessment struct {
	ID                     string `json:"id"`
	WorkspaceID            string `json:"workspace_id"`
	ProjectID              string `json:"project_id"`
	ObjectiveID            string `json:"objective_id"`
	TaskID                 string `json:"task_id"`
	CommitmentID           string `json:"commitment_id"`
	Revision               int64  `json:"revision"`
	StateRevision          int64  `json:"state_revision"`
	ReviewState            string `json:"review_state"`
	Conclusion             string `json:"conclusion"`
	ContentSHA256          string `json:"content_sha256"`
	SupersedesAssessmentID string `json:"supersedes_assessment_id,omitempty"`
	ProposedAt             string `json:"proposed_at"`
	ProposedBy             string `json:"proposed_by"`
	DecidedAt              string `json:"decided_at,omitempty"`
	DecidedBy              string `json:"decided_by,omitempty"`
	DecisionNote           string `json:"decision_note,omitempty"`
}

type OutcomeDecisionReference struct {
	RevisionID    string `json:"revision_id"`
	ContentSHA256 string `json:"content_sha256"`
	EventSequence int64  `json:"event_sequence"`
	Current       bool   `json:"current"`
	Disputed      bool   `json:"disputed"`
}

type OutcomeEvidenceReference struct {
	SourceType       string `json:"source_type"`
	SourceID         string `json:"source_id"`
	SourceRevision   int64  `json:"source_revision"`
	SourceSHA256     string `json:"source_sha256"`
	EventSequence    int64  `json:"event_sequence"`
	Class            string `json:"class"`
	Effect           string `json:"effect"`
	PinnedFreshness  string `json:"pinned_freshness"`
	CurrentFreshness string `json:"current_freshness"`
	Current          bool   `json:"current"`
	Disputed         bool   `json:"disputed"`
	Contradictory    bool   `json:"contradictory"`
	Diagnosis        string `json:"diagnosis,omitempty"`
}

type OutcomeEffect struct {
	Ordinal   int64  `json:"ordinal"`
	Kind      string `json:"kind"`
	Direction string `json:"direction"`
	Summary   string `json:"summary"`
}

type OutcomeDeviation struct {
	Ordinal             int64  `json:"ordinal"`
	Kind                string `json:"kind"`
	Summary             string `json:"summary"`
	RelatedTaskID       string `json:"related_task_id,omitempty"`
	RelatedTaskRevision int64  `json:"related_task_revision,omitempty"`
}

type OutcomeRisk struct {
	Ordinal    int64  `json:"ordinal"`
	Severity   string `json:"severity"`
	Summary    string `json:"summary"`
	Mitigation string `json:"mitigation,omitempty"`
}

type OutcomeUnknownRecord struct {
	Ordinal int64  `json:"ordinal"`
	Summary string `json:"summary"`
}

type OutcomeFollowUpTask struct {
	Ordinal       int64  `json:"ordinal"`
	TaskID        string `json:"task_id"`
	TaskRevision  int64  `json:"task_revision"`
	EventSequence int64  `json:"event_sequence"`
}

type OutcomeOwnerAttention struct {
	Ordinal int64  `json:"ordinal"`
	Urgency string `json:"urgency"`
	Action  string `json:"action"`
	Reason  string `json:"reason"`
}

type OutcomeAssessmentDetail struct {
	Assessment     OutcomeAssessment          `json:"assessment"`
	Commitment     DeliverableCommitment      `json:"commitment"`
	DeliveredScope []string                   `json:"delivered_scope"`
	UnmetScope     []string                   `json:"unmet_scope"`
	Decisions      []OutcomeDecisionReference `json:"decisions"`
	Evidence       []OutcomeEvidenceReference `json:"evidence"`
	Effects        []OutcomeEffect            `json:"effects"`
	Deviations     []OutcomeDeviation         `json:"deviations"`
	Risks          []OutcomeRisk              `json:"risks"`
	Unknowns       []OutcomeUnknownRecord     `json:"unknowns"`
	FollowUpTasks  []OutcomeFollowUpTask      `json:"follow_up_tasks"`
	OwnerAttention []OutcomeOwnerAttention    `json:"owner_attention"`
}

type OwnerCheckpoint struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	ScopeType     string `json:"scope_type"`
	ScopeID       string `json:"scope_id"`
	EventSequence int64  `json:"event_sequence"`
	CreatedAt     string `json:"created_at"`
	CreatedBy     string `json:"created_by"`
}

type BriefingScope struct {
	Type        string `json:"type"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id,omitempty"`
	ObjectiveID string `json:"objective_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
}

type BriefingClaimSource struct {
	EntityType       string `json:"entity_type"`
	EntityID         string `json:"entity_id"`
	Revision         int64  `json:"revision"`
	ContentSHA256    string `json:"content_sha256,omitempty"`
	EventSequence    int64  `json:"event_sequence"`
	EvidenceClass    string `json:"evidence_class,omitempty"`
	EvidenceEffect   string `json:"evidence_effect,omitempty"`
	PinnedFreshness  string `json:"pinned_freshness,omitempty"`
	CurrentFreshness string `json:"current_freshness,omitempty"`
}

type BriefingClaim struct {
	ID                  string                `json:"id"`
	Kind                string                `json:"kind"`
	Urgency             string                `json:"urgency"`
	Summary             string                `json:"summary"`
	Status              string                `json:"status"`
	ProjectID           string                `json:"project_id,omitempty"`
	Sources             []BriefingClaimSource `json:"sources"`
	SourceEventSequence int64                 `json:"source_event_sequence"`
}

type BriefingOmission struct {
	Section string `json:"section"`
	Reason  string `json:"reason"`
	Count   int    `json:"count"`
}

type ManagementBriefing struct {
	ID                   string             `json:"id"`
	Revision             int64              `json:"revision"`
	Scope                BriefingScope      `json:"scope"`
	EventCursor          int64              `json:"event_cursor"`
	CutoffEventSequence  int64              `json:"cutoff_event_sequence"`
	CheckpointID         string             `json:"checkpoint_id,omitempty"`
	SinceEventSequence   int64              `json:"since_event_sequence"`
	EvaluatedAt          string             `json:"evaluated_at"`
	CaughtUp             bool               `json:"caught_up"`
	UnknownEventType     string             `json:"unknown_event_type,omitempty"`
	UnknownEventSequence int64              `json:"unknown_event_sequence,omitempty"`
	Claims               []BriefingClaim    `json:"claims"`
	Omitted              []BriefingOmission `json:"omitted"`
	ContentSHA256        string             `json:"content_sha256"`
	ByteSize             int                `json:"byte_size"`
}

type BriefingClaimExplanation struct {
	BriefingID  string                `json:"briefing_id"`
	Claim       BriefingClaim         `json:"claim"`
	EvaluatedAt string                `json:"evaluated_at"`
	Provenance  []BriefingClaimSource `json:"provenance"`
	Diagnoses   []string              `json:"diagnoses"`
}
