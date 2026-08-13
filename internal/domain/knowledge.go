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

const (
	KnowledgeSearchRankPolicy     = "knowledge_search_v1"
	KnowledgeIndexOK              = "ok"
	KnowledgeIndexDegraded        = "degraded"
	KnowledgeIndexMissing         = "missing"
	KnowledgeIndexCorrupt         = "corrupt"
	KnowledgeIndexOutOfDate       = "out_of_date"
	KnowledgeIndexContentMismatch = "content_mismatch"
	KnowledgeIndexUnsupported     = "unsupported"
)

type KnowledgeIndexStatus struct {
	Status              string `json:"status"`
	Generation          int64  `json:"generation"`
	BuiltAt             string `json:"built_at,omitempty"`
	SourceEventSequence int64  `json:"source_event_sequence"`
	SourceCount         int64  `json:"source_count"`
	SourceDigest        string `json:"source_digest,omitempty"`
	Diagnosis           string `json:"diagnosis,omitempty"`
}

type KnowledgeSearchScopeExplanation struct {
	Rank   int    `json:"rank"`
	Reason string `json:"reason"`
}

type KnowledgeSearchAuthorityExplanation struct {
	ReviewStatus   string `json:"review_status"`
	CurrencyStatus string `json:"currency_status"`
	AcceptedByType string `json:"accepted_by_type"`
	Reason         string `json:"reason"`
}

type KnowledgeSearchFreshnessExplanation struct {
	Class       int    `json:"class"`
	FreshUntil  string `json:"fresh_until,omitempty"`
	EvaluatedAt string `json:"evaluated_at"`
	Reason      string `json:"reason"`
}

type KnowledgeSearchProvenanceExplanation struct {
	Rank             int      `json:"rank"`
	Reason           string   `json:"reason"`
	MatchedSourceIDs []string `json:"matched_source_ids"`
}

type KnowledgeSearchQualityExplanation struct {
	Confidence         string `json:"confidence"`
	ConfidenceRank     int    `json:"confidence_rank"`
	VerificationStatus string `json:"verification_status"`
	VerificationRank   int    `json:"verification_rank"`
}

type KnowledgeSearchTextExplanation struct {
	BM25        float64 `json:"bm25"`
	TitleWeight float64 `json:"title_weight"`
	BodyWeight  float64 `json:"body_weight"`
}

type KnowledgeSearchTieBreaker struct {
	AcceptedAt string `json:"accepted_at"`
	RevisionID string `json:"revision_id"`
}

type KnowledgeSearchExplanation struct {
	Scope      KnowledgeSearchScopeExplanation      `json:"scope"`
	Authority  KnowledgeSearchAuthorityExplanation  `json:"authority"`
	Freshness  KnowledgeSearchFreshnessExplanation  `json:"freshness"`
	Provenance KnowledgeSearchProvenanceExplanation `json:"provenance"`
	Quality    KnowledgeSearchQualityExplanation    `json:"quality"`
	Text       KnowledgeSearchTextExplanation       `json:"text"`
	TieBreaker KnowledgeSearchTieBreaker            `json:"tie_breaker"`
}

type KnowledgeSearchMatch struct {
	Ordinal     int64                      `json:"ordinal"`
	Revision    KnowledgeRevision          `json:"revision"`
	Explanation KnowledgeSearchExplanation `json:"explanation"`
}

type KnowledgeSearchResult struct {
	NormalizedQuery        string                 `json:"normalized_query"`
	EvaluatedAt            string                 `json:"evaluated_at"`
	CanonicalEventSequence int64                  `json:"canonical_event_sequence"`
	RankPolicy             string                 `json:"rank_policy"`
	Index                  KnowledgeIndexStatus   `json:"index"`
	Matches                []KnowledgeSearchMatch `json:"matches"`
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
