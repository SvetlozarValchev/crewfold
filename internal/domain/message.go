package domain

const (
	ThreadOpen   = "open"
	ThreadClosed = "closed"

	ThreadKindDirect           = "direct"
	ThreadKindParticipantBound = "participant_bound"
	ThreadParticipantActive    = "active"

	MessageInform          = "inform"
	MessageQuestion        = "question"
	MessageRequest         = "request"
	MessageReviewRequest   = "review_request"
	MessageHandoff         = "handoff"
	MessageDecisionNotice  = "decision_notice"
	MessageRisk            = "risk"
	MessageConflict        = "conflict"
	MessageApprovalRequest = "approval_request"

	DeliveryQueued       = "queued"
	DeliveryDelivered    = "delivered"
	DeliveryRead         = "read"
	DeliveryAcknowledged = "acknowledged"

	WakeNotRequested  = "not_requested"
	WakePending       = "pending"
	WakeLeased        = "leased"
	WakeSucceeded     = "succeeded"
	WakeFailed        = "failed"
	WakeFailedUnknown = "failed_unknown"
)

// ParticipantBindingInput identifies the exact agent/task assignment that an
// owner wants to bind into a participant-bound collaboration thread. Names are
// accepted at the boundary, then resolved to immutable canonical identifiers.
type ParticipantBindingInput struct {
	AgentIdentifier string `json:"agent_identifier"`
	TaskIdentifier  string `json:"task_identifier"`
}

// ThreadParticipant is an audit-frozen binding. The agent, task, project, and
// assignment identifiers and the invitation-time agent/task revisions never
// change, even after the underlying assignment ends.
type ThreadParticipant struct {
	ID                 string `json:"id"`
	ThreadID           string `json:"thread_id"`
	WorkspaceID        string `json:"workspace_id"`
	AgentID            string `json:"agent_id"`
	AgentName          string `json:"agent_name"`
	TaskID             string `json:"task_id"`
	TaskTitle          string `json:"task_title"`
	ProjectID          string `json:"project_id"`
	ProjectName        string `json:"project_name"`
	AssignmentID       string `json:"assignment_id"`
	AssignmentRevision int64  `json:"assignment_revision"`
	AgentRevision      int64  `json:"agent_revision"`
	TaskRevision       int64  `json:"task_revision"`
	Ordinal            int    `json:"ordinal"`
	Status             string `json:"status"`
	InvitedAt          string `json:"invited_at"`
	InvitedBy          string `json:"invited_by"`
}

// ParticipantThread adds collaboration-specific state without changing the
// stable MessageThread wire shape used by direct mail.
type ParticipantThread struct {
	Thread              MessageThread       `json:"thread"`
	Kind                string              `json:"kind"`
	ParticipantRevision int64               `json:"participant_revision"`
	Participants        []ThreadParticipant `json:"participants"`
}

type MessageThread struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	Subject     string `json:"subject"`
	Status      string `json:"status"`
	Revision    int64  `json:"revision"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	CreatedBy   string `json:"created_by"`
	UpdatedBy   string `json:"updated_by"`
}

type Message struct {
	ID               string   `json:"id"`
	WorkspaceID      string   `json:"workspace_id"`
	ThreadID         string   `json:"thread_id"`
	ProjectID        string   `json:"project_id,omitempty"`
	TaskID           string   `json:"task_id,omitempty"`
	SenderType       string   `json:"sender_type"`
	SenderID         string   `json:"sender_id"`
	SenderAgentID    string   `json:"sender_agent_id,omitempty"`
	SenderAgentName  string   `json:"sender_agent_name,omitempty"`
	SenderRunID      string   `json:"sender_run_id,omitempty"`
	Kind             string   `json:"kind"`
	Body             string   `json:"body"`
	ArtifactIDs      []string `json:"artifact_ids"`
	ReplyToMessageID string   `json:"reply_to_message_id,omitempty"`
	CreatedAt        string   `json:"created_at"`
}

type MessageDelivery struct {
	MessageID        string `json:"message_id"`
	RecipientAgentID string `json:"recipient_agent_id"`
	RecipientName    string `json:"recipient_name"`
	Status           string `json:"status"`
	QueuedAt         string `json:"queued_at"`
	DeliveredAt      string `json:"delivered_at,omitempty"`
	ReadAt           string `json:"read_at,omitempty"`
	AcknowledgedAt   string `json:"acknowledged_at,omitempty"`
	DeliveredRunID   string `json:"delivered_run_id,omitempty"`
	WakeStatus       string `json:"wake_status"`
	WakeDiagnostic   string `json:"wake_diagnostic,omitempty"`
}

type InboxItem struct {
	Message  Message         `json:"message"`
	Delivery MessageDelivery `json:"delivery"`
}

type MessageMutation struct {
	Thread    MessageThread   `json:"thread"`
	Message   Message         `json:"message"`
	Recipient MessageDelivery `json:"recipient"`
}

type ThreadDetail struct {
	Thread     MessageThread     `json:"thread"`
	Messages   []Message         `json:"messages"`
	Recipients []MessageDelivery `json:"recipients"`
}

// ThreadSummary is the bounded discovery shape used before an owner opens the
// full message history. A thread is coordination state; it is never presented
// as an accepted knowledge revision.
type ThreadSummary struct {
	Thread       MessageThread `json:"thread"`
	MessageCount int           `json:"message_count"`
	AgentIDs     []string      `json:"agent_ids"`
}

type InboxSummaryItem struct {
	MessageID       string `json:"message_id"`
	ThreadID        string `json:"thread_id"`
	Kind            string `json:"kind"`
	SenderAgentID   string `json:"sender_agent_id,omitempty"`
	SenderAgentName string `json:"sender_agent_name,omitempty"`
	BodyPreview     string `json:"body_preview"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
}

type InboxSummary struct {
	UnseenCount int                `json:"unseen_count"`
	Items       []InboxSummaryItem `json:"items"`
}

type MessageWakeJob struct {
	ID                   string `json:"id"`
	MessageID            string `json:"message_id"`
	RecipientAgentID     string `json:"recipient_agent_id"`
	TargetRunID          string `json:"target_run_id,omitempty"`
	TargetDomainThreadID string `json:"-"`
	Status               string `json:"status"`
	Attempts             int    `json:"attempts"`
	Diagnostic           string `json:"diagnostic,omitempty"`
}
