package domain

const (
	ThreadOpen   = "open"
	ThreadClosed = "closed"

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

	WakeNotRequested = "not_requested"
	WakePending      = "pending"
	WakeLeased       = "leased"
	WakeSucceeded    = "succeeded"
	WakeFailed       = "failed"
)

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
	ID               string `json:"id"`
	MessageID        string `json:"message_id"`
	RecipientAgentID string `json:"recipient_agent_id"`
	TargetRunID      string `json:"target_run_id"`
	Status           string `json:"status"`
	Attempts         int    `json:"attempts"`
	Diagnostic       string `json:"diagnostic,omitempty"`
}
