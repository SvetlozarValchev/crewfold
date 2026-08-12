package domain

const (
	KnowledgeTypeDecision = "decision"
	KnowledgeTypeFinding  = "finding"

	KnowledgeReviewProposed = "proposed"
	KnowledgeReviewAccepted = "accepted"
	KnowledgeReviewRejected = "rejected"

	KnowledgeCurrencyPending    = "pending"
	KnowledgeCurrencyCurrent    = "current"
	KnowledgeCurrencyStale      = "stale"
	KnowledgeCurrencySuperseded = "superseded"

	KnowledgeConfidenceLow    = "low"
	KnowledgeConfidenceMedium = "medium"
	KnowledgeConfidenceHigh   = "high"

	KnowledgeVerificationUnverified = "unverified"
	KnowledgeVerificationSupported  = "supported"
	KnowledgeVerificationVerified   = "verified"

	KnowledgeFreshUntilSuperseded = "until_superseded"
	KnowledgeFreshExpiresAt       = "expires_at"

	KnowledgeSourceTask            = "task"
	KnowledgeSourceMeeting         = "meeting"
	KnowledgeSourceMeetingProposal = "meeting_proposal"
	KnowledgeSourcePrimary         = "primary"
	KnowledgeSourceSupporting      = "supporting"

	KnowledgeActorHuman     = "human"
	KnowledgeActorAgentRun  = "agent_run"
	KnowledgeActorSubsystem = "subsystem"

	KnowledgeAuthorityAccept    = "accept"
	KnowledgeAuthorityReject    = "reject"
	KnowledgeAuthorityMarkStale = "mark_stale"
	KnowledgeAuthoritySupersede = "supersede"
	KnowledgeAuthorityAllowed   = "allowed"
	KnowledgeAuthorityDenied    = "denied"

	KnowledgeAuthorityReasonOwner       = "workspace_owner"
	KnowledgeAuthorityReasonNotOwner    = "actor_not_workspace_owner"
	KnowledgeAuthorityReasonStatePolicy = "state_policy"
)

// KnowledgeActor identifies a trusted internal principal. Transport callers must
// never be allowed to supply this value directly.
type KnowledgeActor struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type KnowledgeItem struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	ProjectID     string `json:"project_id"`
	TaskScopeID   string `json:"task_scope_id,omitempty"`
	Type          string `json:"type"`
	CreatedAt     string `json:"created_at"`
	CreatedBy     string `json:"created_by"`
	CreatedByType string `json:"created_by_type"`
}

type KnowledgeSourceInput struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Role string `json:"role"`
}

type KnowledgeSource struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Role     string `json:"role"`
	Ordinal  int64  `json:"ordinal"`
}

// KnowledgeRevision is a complete, immutable-content snapshot. Governance
// fields may advance while Title, Body, provenance, and quality metadata do not.
type KnowledgeRevision struct {
	ID                   string            `json:"id"`
	ItemID               string            `json:"item_id"`
	WorkspaceID          string            `json:"workspace_id"`
	ProjectID            string            `json:"project_id"`
	TaskScopeID          string            `json:"task_scope_id,omitempty"`
	Type                 string            `json:"type"`
	RevisionNumber       int64             `json:"revision_number"`
	StateRevision        int64             `json:"state_revision"`
	Title                string            `json:"title"`
	Body                 string            `json:"body"`
	ContentHash          string            `json:"content_hash"`
	ReviewStatus         string            `json:"review_status"`
	CurrencyStatus       string            `json:"currency_status"`
	Confidence           string            `json:"confidence"`
	VerificationStatus   string            `json:"verification_status"`
	FreshnessPolicy      string            `json:"freshness_policy"`
	FreshUntil           string            `json:"fresh_until,omitempty"`
	SupersedesRevisionID string            `json:"supersedes_revision_id,omitempty"`
	ProposedAt           string            `json:"proposed_at"`
	ProposedBy           string            `json:"proposed_by"`
	ProposedByType       string            `json:"proposed_by_type"`
	AcceptedAt           string            `json:"accepted_at,omitempty"`
	AcceptedBy           string            `json:"accepted_by,omitempty"`
	AcceptedByType       string            `json:"accepted_by_type,omitempty"`
	RejectedAt           string            `json:"rejected_at,omitempty"`
	RejectedBy           string            `json:"rejected_by,omitempty"`
	RejectedByType       string            `json:"rejected_by_type,omitempty"`
	StaleAt              string            `json:"stale_at,omitempty"`
	StaleBy              string            `json:"stale_by,omitempty"`
	StaleByType          string            `json:"stale_by_type,omitempty"`
	DecisionNote         string            `json:"decision_note,omitempty"`
	StaleReason          string            `json:"stale_reason,omitempty"`
	Sources              []KnowledgeSource `json:"sources"`
}

type KnowledgeAuthorityCheck struct {
	ID             string         `json:"id"`
	WorkspaceID    string         `json:"workspace_id"`
	RevisionID     string         `json:"revision_id"`
	Action         string         `json:"action"`
	Actor          KnowledgeActor `json:"actor"`
	Outcome        string         `json:"outcome"`
	Reason         string         `json:"reason"`
	Note           string         `json:"note,omitempty"`
	IdempotencyKey string         `json:"idempotency_key"`
	RequestHash    string         `json:"request_hash"`
	EventSequence  int64          `json:"event_sequence"`
	CreatedAt      string         `json:"created_at"`
}

type KnowledgeDetail struct {
	Revision        KnowledgeRevision         `json:"revision"`
	AuthorityChecks []KnowledgeAuthorityCheck `json:"authority_checks"`
}

type KnowledgeList struct {
	Revisions []KnowledgeRevision `json:"revisions"`
}

func ValidKnowledgeType(value string) bool {
	return value == KnowledgeTypeDecision || value == KnowledgeTypeFinding
}

func ValidKnowledgeReviewStatus(value string) bool {
	return value == KnowledgeReviewProposed || value == KnowledgeReviewAccepted || value == KnowledgeReviewRejected
}

func ValidKnowledgeCurrencyStatus(value string) bool {
	return value == KnowledgeCurrencyPending || value == KnowledgeCurrencyCurrent || value == KnowledgeCurrencyStale || value == KnowledgeCurrencySuperseded
}

func ValidKnowledgeConfidence(value string) bool {
	return value == KnowledgeConfidenceLow || value == KnowledgeConfidenceMedium || value == KnowledgeConfidenceHigh
}

func ValidKnowledgeVerification(value string) bool {
	return value == KnowledgeVerificationUnverified || value == KnowledgeVerificationSupported || value == KnowledgeVerificationVerified
}

func ValidKnowledgeFreshnessPolicy(value string) bool {
	return value == KnowledgeFreshUntilSuperseded || value == KnowledgeFreshExpiresAt
}

func ValidKnowledgeSourceType(value string) bool {
	return value == KnowledgeSourceTask || value == KnowledgeSourceMeeting || value == KnowledgeSourceMeetingProposal
}

func ValidKnowledgeSourceRole(value string) bool {
	return value == KnowledgeSourcePrimary || value == KnowledgeSourceSupporting
}

func ValidKnowledgeActorType(value string) bool {
	return value == KnowledgeActorHuman || value == KnowledgeActorAgentRun || value == KnowledgeActorSubsystem
}
