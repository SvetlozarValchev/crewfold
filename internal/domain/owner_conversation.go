package domain

import "encoding/json"

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
	ID                     string              `json:"id"`
	ConversationID         string              `json:"conversation_id"`
	Ordinal                int64               `json:"ordinal"`
	Kind                   string              `json:"kind"`
	InitiatedBy            string              `json:"initiated_by"`
	TriggerEventSequence   int64               `json:"trigger_event_sequence,omitempty"`
	Instruction            string              `json:"instruction"`
	Status                 string              `json:"status"`
	AsOfEventSequence      int64               `json:"as_of_event_sequence"`
	Answer                 string              `json:"answer,omitempty"`
	PlanSHA256             string              `json:"plan_sha256"`
	ErrorCode              string              `json:"error_code,omitempty"`
	Revision               int64               `json:"revision"`
	CreatedAt              string              `json:"created_at"`
	UpdatedAt              string              `json:"updated_at"`
	CompletedEventSequence int64               `json:"completed_event_sequence,omitempty"`
	Citations              []OwnerCitation     `json:"citations"`
	Interpretation         OwnerInterpretation `json:"interpretation"`
}

// OwnerExecutiveBinding is the visible, owner-authored authority tuple for one
// project's durable executive. Role is presentation only; this exact tuple is
// what permits executive exchanges and manager proposals.
type OwnerExecutiveBinding struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	ProjectID       string `json:"project_id"`
	ObjectiveID     string `json:"objective_id"`
	PlanningTaskID  string `json:"planning_task_id"`
	AgentID         string `json:"agent_id"`
	ManagerGrantID  string `json:"manager_grant_id"`
	LaunchProfileID string `json:"launch_profile_id"`
	Status          string `json:"status"`
	Revision        int64  `json:"revision"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// OwnerExecutiveExchange binds one durable owner/review turn to one frozen
// canonical context cut and, once dispatched, one exact manager run.
type OwnerExecutiveExchange struct {
	ID             string   `json:"id"`
	TurnID         string   `json:"turn_id"`
	BindingID      string   `json:"binding_id"`
	RunID          string   `json:"run_id,omitempty"`
	EventSequence  int64    `json:"event_sequence"`
	Status         string   `json:"status"`
	Attempts       int64    `json:"attempts"`
	AvailableAt    string   `json:"available_at"`
	LeaseExpiresAt string   `json:"lease_expires_at,omitempty"`
	ProposalIDs    []string `json:"proposal_ids"`
	LastError      string   `json:"last_error,omitempty"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

type OwnerExecutiveContext struct {
	Exchange OwnerExecutiveExchange `json:"exchange"`
	Turn     OwnerTurn              `json:"turn"`
	Context  json.RawMessage        `json:"context"`
}

// OwnerManagerReviewJob is the one coalesced, durable manager-review cursor for
// a project with an open owner conversation. Worker reports and agent messages
// advance RequestedEventSequence in their own transaction; one daemon worker
// reviews a frozen cut and advances ReviewedEventSequence exactly once.
type OwnerManagerReviewJob struct {
	WorkspaceID            string `json:"workspace_id"`
	ProjectID              string `json:"project_id"`
	ConversationID         string `json:"conversation_id"`
	Status                 string `json:"status"`
	RequestedEventSequence int64  `json:"requested_event_sequence"`
	ReviewedEventSequence  int64  `json:"reviewed_event_sequence"`
	Attempts               int64  `json:"attempts"`
	AvailableAt            string `json:"available_at"`
	LeaseExpiresAt         string `json:"lease_expires_at,omitempty"`
	LastTurnID             string `json:"last_turn_id,omitempty"`
	LastError              string `json:"last_error,omitempty"`
	CreatedAt              string `json:"created_at"`
	UpdatedAt              string `json:"updated_at"`
}

// OwnerCitation binds a manager answer to one exact canonical record at the
// event cut that was supplied to the interpreter. The model chooses only the
// opaque Ref values it was given; Crewfold resolves and persists the record
// identity, revision, and cut itself.
type OwnerCitation struct {
	Ref               string `json:"ref"`
	EntityType        string `json:"entity_type"`
	EntityID          string `json:"entity_id"`
	EntityRevision    int64  `json:"entity_revision"`
	AsOfEventSequence int64  `json:"as_of_event_sequence"`
	Label             string `json:"label"`
}

// OwnerPlanTask is the deliberately small manager output grammar. It contains
// no executable, command, environment, provider, runtime, or authority text;
// an exact current launch profile is the only way to nominate execution.
type OwnerPlanTask struct {
	Key             string   `json:"key"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	Priority        int      `json:"priority"`
	Budget          Budget   `json:"budget"`
	LaunchProfileID string   `json:"launch_profile_id"`
	DependsOn       []string `json:"depends_on"`
}

// OwnerInterpretation is untrusted provider output until Store validation
// freezes it. Pending is the explicit empty state of a queued/running executive
// exchange; completed interpretations are answer, ready, clarify, or refuse.
// Only ready plan/act interpretations may contain an objective and tasks.
type OwnerInterpretation struct {
	Disposition     string          `json:"disposition"`
	Summary         string          `json:"summary"`
	Answer          string          `json:"answer"`
	Question        string          `json:"question"`
	Choices         []OwnerChoice   `json:"choices"`
	ObjectiveTitle  string          `json:"objective_title"`
	ObjectiveBudget Budget          `json:"objective_budget"`
	Tasks           []OwnerPlanTask `json:"tasks"`
	CitationRefs    []string        `json:"citation_refs"`
}

// OwnerChoice is a typed owner decision offered by the manager. It is inert
// until the owner submits the selected answer as a new visible instruction.
type OwnerChoice struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended"`
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
