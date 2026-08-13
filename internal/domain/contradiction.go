package domain

const (
	KnowledgeContradictionProposed  = "proposed"
	KnowledgeContradictionOpen      = "open"
	KnowledgeContradictionResolved  = "resolved"
	KnowledgeContradictionDismissed = "dismissed"

	KnowledgeContradictionAuthorityConfirm = "confirm"
	KnowledgeContradictionAuthorityDismiss = "dismiss"
)

// KnowledgeContradiction preserves a reported conflict between two exact,
// immutable knowledge revisions. Its status is deliberately independent of the
// review and currency axes on either knowledge revision.
type KnowledgeContradiction struct {
	ID                           string `json:"id"`
	WorkspaceID                  string `json:"workspace_id"`
	ProjectID                    string `json:"project_id"`
	LeftRevisionID               string `json:"left_revision_id"`
	RightRevisionID              string `json:"right_revision_id"`
	Status                       string `json:"status"`
	StateRevision                int64  `json:"state_revision"`
	ReportNote                   string `json:"report_note"`
	ReportedAt                   string `json:"reported_at"`
	ReportedBy                   string `json:"reported_by"`
	ReportedByType               string `json:"reported_by_type"`
	ConfirmedAt                  string `json:"confirmed_at,omitempty"`
	ConfirmedBy                  string `json:"confirmed_by,omitempty"`
	ConfirmedByType              string `json:"confirmed_by_type,omitempty"`
	ConfirmNote                  string `json:"confirm_note,omitempty"`
	ConfirmEventSequence         int64  `json:"confirm_event_sequence,omitempty"`
	DismissedAt                  string `json:"dismissed_at,omitempty"`
	DismissedBy                  string `json:"dismissed_by,omitempty"`
	DismissedByType              string `json:"dismissed_by_type,omitempty"`
	DismissNote                  string `json:"dismiss_note,omitempty"`
	DismissEventSequence         int64  `json:"dismiss_event_sequence,omitempty"`
	ResolutionReason             string `json:"resolution_reason,omitempty"`
	ResolvedAt                   string `json:"resolved_at,omitempty"`
	ResolvedBy                   string `json:"resolved_by,omitempty"`
	ResolvedByType               string `json:"resolved_by_type,omitempty"`
	ResolutionNote               string `json:"resolution_note,omitempty"`
	ResolutionEventSequence      int64  `json:"resolution_event_sequence,omitempty"`
	ResolutionCauseEventSequence int64  `json:"resolution_cause_event_sequence,omitempty"`
}

// KnowledgeContradictionAuthorityCheck is durable evidence for every owner
// governance attempt, including denied attempts.
type KnowledgeContradictionAuthorityCheck struct {
	ID              string         `json:"id"`
	WorkspaceID     string         `json:"workspace_id"`
	ContradictionID string         `json:"contradiction_id"`
	Action          string         `json:"action"`
	Actor           KnowledgeActor `json:"actor"`
	Outcome         string         `json:"outcome"`
	Reason          string         `json:"reason"`
	Note            string         `json:"note,omitempty"`
	IdempotencyKey  string         `json:"idempotency_key"`
	RequestHash     string         `json:"request_hash"`
	EventSequence   int64          `json:"event_sequence"`
	CreatedAt       string         `json:"created_at"`
}

// KnowledgeContradictionDetail carries the exact participant snapshots and
// authority history needed to explain why a revision is currently disputed.
type KnowledgeContradictionDetail struct {
	Contradiction       KnowledgeContradiction                 `json:"contradiction"`
	LeftRevision        KnowledgeRevision                      `json:"left_revision"`
	RightRevision       KnowledgeRevision                      `json:"right_revision"`
	AuthorityCheckCount int64                                  `json:"authority_check_count"`
	AuthorityChecks     []KnowledgeContradictionAuthorityCheck `json:"authority_checks"`
}

type KnowledgeContradictionList struct {
	Details []KnowledgeContradictionDetail `json:"details"`
}

// KnowledgeRevisionDispute is the derived effective-dispute state for an exact
// knowledge revision. A revision is disputed while at least one confirmed open
// contradiction references it.
type KnowledgeRevisionDispute struct {
	RevisionID             string   `json:"revision_id"`
	Disputed               bool     `json:"disputed"`
	OpenContradictionCount int64    `json:"open_contradiction_count"`
	OpenContradictionIDs   []string `json:"open_contradiction_ids"`
}

func ValidKnowledgeContradictionStatus(value string) bool {
	return value == KnowledgeContradictionProposed || value == KnowledgeContradictionOpen ||
		value == KnowledgeContradictionResolved || value == KnowledgeContradictionDismissed
}

func ValidKnowledgeContradictionAuthorityAction(value string) bool {
	return value == KnowledgeContradictionAuthorityConfirm || value == KnowledgeContradictionAuthorityDismiss
}
