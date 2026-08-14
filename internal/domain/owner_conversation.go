package domain

// OwnerConversation is the durable browser-facing command thread for one
// exact workspace/project scope. It records intent; canonical domain effects
// remain in their existing projections and event journal.
type OwnerConversation struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Revision    int64  `json:"revision"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type OwnerTurn struct {
	ID                     string `json:"id"`
	ConversationID         string `json:"conversation_id"`
	Ordinal                int64  `json:"ordinal"`
	Kind                   string `json:"kind"`
	Instruction            string `json:"instruction"`
	Status                 string `json:"status"`
	AsOfEventSequence      int64  `json:"as_of_event_sequence"`
	Answer                 string `json:"answer,omitempty"`
	PlanSHA256             string `json:"plan_sha256"`
	ErrorCode              string `json:"error_code,omitempty"`
	Revision               int64  `json:"revision"`
	CreatedAt              string `json:"created_at"`
	UpdatedAt              string `json:"updated_at"`
	CompletedEventSequence int64  `json:"completed_event_sequence,omitempty"`
}

type OwnerTurnOperation struct {
	ID               string         `json:"id"`
	TurnID           string         `json:"turn_id"`
	Ordinal          int64          `json:"ordinal"`
	Type             string         `json:"type"`
	Payload          map[string]any `json:"payload"`
	PayloadSHA256    string         `json:"payload_sha256"`
	PolicyResult     string         `json:"policy_result"`
	Status           string         `json:"status"`
	ResultEntityType string         `json:"result_entity_type,omitempty"`
	ResultEntityID   string         `json:"result_entity_id,omitempty"`
	EventSequence    int64          `json:"event_sequence,omitempty"`
	Diagnosis        string         `json:"diagnosis,omitempty"`
	Revision         int64          `json:"revision"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
}

type OwnerEffectReceipt struct {
	OperationID    string `json:"operation_id"`
	Method         string `json:"method"`
	IdempotencyKey string `json:"idempotency_key"`
	RequestSHA256  string `json:"request_sha256"`
	ResponseSHA256 string `json:"response_sha256"`
	EventSequence  int64  `json:"event_sequence,omitempty"`
	CommittedAt    string `json:"committed_at"`
}

type OwnerTurnDetail struct {
	Conversation OwnerConversation    `json:"conversation"`
	Turn         OwnerTurn            `json:"turn"`
	Operations   []OwnerTurnOperation `json:"operations"`
	Receipts     []OwnerEffectReceipt `json:"receipts"`
}
