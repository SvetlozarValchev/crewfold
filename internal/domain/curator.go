package domain

const (
	CuratorRuleAcceptedMeetingResolutionCopy = "accepted_meeting_resolution_copy/v1"
	CuratorActorID                           = "subsystem:curator"

	CuratorEligibilitySafe   = "safe_auto_accept"
	CuratorEligibilityManual = "manual_review"

	CuratorEligibilityReasonAcceptedMeetingResolutionCopy = "accepted_meeting_resolution_copy"
	CuratorEligibilityReasonNotDerived                    = "not_curator_derived"
	CuratorEligibilityReasonDerivationMismatch            = "derivation_mismatch"
	CuratorEligibilityReasonRuleDisabled                  = "rule_disabled"
	CuratorSkipAgendaInvalid                              = "agenda_not_exact_safe_copy"
	CuratorSkipSummaryInvalid                             = "summary_not_exact_safe_copy"
)

// CuratorRule is one immutable configuration revision. The latest revision for
// a workspace and name is effective; history is never rewritten.
type CuratorRule struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	Name          string `json:"name"`
	Revision      int64  `json:"revision"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     string `json:"created_at"`
	CreatedBy     string `json:"created_by"`
	EventSequence int64  `json:"event_sequence"`
}

// CuratorDerivation proves that one canonical proposal was rendered by one
// exact deterministic rule from one frozen structured source revision.
type CuratorDerivation struct {
	ID                  string `json:"id"`
	WorkspaceID         string `json:"workspace_id"`
	ProjectID           string `json:"project_id"`
	RuleID              string `json:"rule_id"`
	RuleName            string `json:"rule_name"`
	RuleRevision        int64  `json:"rule_revision"`
	SourceType          string `json:"source_type"`
	SourceID            string `json:"source_id"`
	SourceRevision      int64  `json:"source_revision"`
	SourceContentHash   string `json:"source_content_hash"`
	KnowledgeRevisionID string `json:"knowledge_revision_id"`
	OutputContentHash   string `json:"output_content_hash"`
	CreatedAt           string `json:"created_at"`
	CreatedBy           string `json:"created_by"`
	EventSequence       int64  `json:"event_sequence"`
}

// CuratorAutoAcceptance is immutable evidence for the rule-specific internal
// acceptance path. The linked authority check remains the knowledge authority
// ledger; this record explains why that narrow path was eligible.
type CuratorAutoAcceptance struct {
	ID                     string         `json:"id"`
	WorkspaceID            string         `json:"workspace_id"`
	ProjectID              string         `json:"project_id"`
	RuleID                 string         `json:"rule_id"`
	RuleName               string         `json:"rule_name"`
	RuleRevision           int64          `json:"rule_revision"`
	DerivationID           string         `json:"derivation_id"`
	KnowledgeRevisionID    string         `json:"knowledge_revision_id"`
	AuthorityCheckID       string         `json:"authority_check_id"`
	KnowledgeEventSequence int64          `json:"knowledge_event_sequence"`
	EventSequence          int64          `json:"event_sequence"`
	CreatedAt              string         `json:"created_at"`
	Actor                  KnowledgeActor `json:"actor"`
}

type CuratorQueueEntry struct {
	Revision          KnowledgeRevision  `json:"revision"`
	Eligibility       string             `json:"eligibility"`
	EligibilityReason string             `json:"eligibility_reason"`
	RuleEnabled       bool               `json:"rule_enabled"`
	Derivation        *CuratorDerivation `json:"derivation,omitempty"`
}

type CuratorQueue struct {
	Entries    []CuratorQueueEntry `json:"entries"`
	Rule       CuratorRule         `json:"rule"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

type CuratorProcess struct {
	CandidatesScanned int64                   `json:"candidates_scanned"`
	Derived           []CuratorDerivation     `json:"derived"`
	Accepted          []CuratorAutoAcceptance `json:"accepted"`
	Skipped           []CuratorSkip           `json:"skipped"`
}

type CuratorSkip struct {
	SourceType     string `json:"source_type"`
	SourceID       string `json:"source_id"`
	SourceRevision int64  `json:"source_revision"`
	Reason         string `json:"reason"`
}
