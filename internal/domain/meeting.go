package domain

const (
	MeetingGatheringPositions = "gathering_positions"
	MeetingFacilitatorPending = "facilitator_pending"
	MeetingAwaitingApproval   = "awaiting_approval"
	MeetingAwaitingReviewer   = "awaiting_reviewer"
	MeetingConcluded          = "concluded"
	MeetingStalled            = "stalled"
	MeetingCancelled          = "cancelled"

	MeetingPolicyOwnerDecision  = "owner_decision"
	MeetingPolicyNamedReviewer  = "named_reviewer"
	MeetingPolicyManagerBounded = "manager_bounded"

	MeetingParticipantPending   = "pending"
	MeetingParticipantSubmitted = "submitted"
	MeetingParticipantMissing   = "missing"

	MeetingProposalProposed = "proposed"
	MeetingProposalAccepted = "accepted"
	MeetingProposalRejected = "rejected"

	MeetingActionSequence      = "sequence"
	MeetingActionSplit         = "split"
	MeetingActionReassign      = "reassign"
	MeetingActionDesignateRole = "designate_role"
	MeetingActionCancel        = "cancel"

	MeetingActionPending = "pending"
	MeetingActionApplied = "applied"
	MeetingActionFailed  = "failed"
)

type MeetingInput struct {
	Overlap       WorkOverlap       `json:"overlap"`
	Claims        []WorkClaim       `json:"claims"`
	Tasks         []Task            `json:"tasks"`
	Agents        []AgentDefinition `json:"agents"`
	EventSequence int64             `json:"event_sequence"`
	FrozenAt      string            `json:"frozen_at"`
}

type Meeting struct {
	ID                 string   `json:"id"`
	WorkspaceID        string   `json:"workspace_id"`
	ProjectID          string   `json:"project_id"`
	OverlapID          string   `json:"overlap_id"`
	Agenda             string   `json:"agenda"`
	FacilitatorAgentID string   `json:"facilitator_agent_id"`
	Policy             string   `json:"policy"`
	ReviewerAgentID    string   `json:"reviewer_agent_id,omitempty"`
	AllowedActions     []string `json:"allowed_actions"`
	Status             string   `json:"status"`
	FrozenInputHash    string   `json:"frozen_input_hash"`
	DeadlineAt         string   `json:"deadline_at"`
	StalledReason      string   `json:"stalled_reason,omitempty"`
	Revision           int64    `json:"revision"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
	CreatedBy          string   `json:"created_by"`
	UpdatedBy          string   `json:"updated_by"`
}

type MeetingParticipant struct {
	MeetingID string `json:"meeting_id"`
	AgentID   string `json:"agent_id"`
	TaskID    string `json:"task_id,omitempty"`
	Ordinal   int    `json:"ordinal"`
	Status    string `json:"status"`
}

type MeetingContribution struct {
	ID          string   `json:"id"`
	MeetingID   string   `json:"meeting_id"`
	AgentID     string   `json:"agent_id"`
	Round       string   `json:"round"`
	Summary     string   `json:"summary"`
	Evidence    []string `json:"evidence"`
	SubmittedAt string   `json:"submitted_at"`
}

type MeetingProposal struct {
	ID           string `json:"id"`
	MeetingID    string `json:"meeting_id"`
	ProposedBy   string `json:"proposed_by"`
	Summary      string `json:"summary"`
	Status       string `json:"status"`
	Revision     int64  `json:"revision"`
	ProposedAt   string `json:"proposed_at"`
	DecidedAt    string `json:"decided_at,omitempty"`
	DecisionNote string `json:"decision_note,omitempty"`
}

type MeetingAction struct {
	ID             string         `json:"id"`
	ProposalID     string         `json:"proposal_id"`
	Ordinal        int            `json:"ordinal"`
	Type           string         `json:"type"`
	Payload        map[string]any `json:"payload"`
	Status         string         `json:"status"`
	ResultEntityID string         `json:"result_entity_id,omitempty"`
	Diagnostic     string         `json:"diagnostic,omitempty"`
	AppliedAt      string         `json:"applied_at,omitempty"`
}

type MeetingDetail struct {
	Meeting       Meeting               `json:"meeting"`
	FrozenInput   MeetingInput          `json:"frozen_input"`
	Participants  []MeetingParticipant  `json:"participants"`
	Contributions []MeetingContribution `json:"contributions"`
	Proposal      *MeetingProposal      `json:"proposal,omitempty"`
	Actions       []MeetingAction       `json:"actions"`
}

type MeetingPositionInput struct {
	AgentID  string   `json:"agent_id"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence,omitempty"`
}

type MeetingActionInput struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

type MeetingProposalInput struct {
	Summary string               `json:"summary"`
	Actions []MeetingActionInput `json:"actions"`
}

type MeetingRunFixture struct {
	Positions           []MeetingPositionInput `json:"positions"`
	Proposal            *MeetingProposalInput  `json:"proposal,omitempty"`
	ReviewerApproved    *bool                  `json:"reviewer_approved,omitempty"`
	ReviewerNote        string                 `json:"reviewer_note,omitempty"`
	PauseAfterPositions bool                   `json:"pause_after_positions,omitempty"`
}

type TaskRole struct {
	TaskID          string `json:"task_id"`
	AgentID         string `json:"agent_id"`
	Role            string `json:"role"`
	SourceMeetingID string `json:"source_meeting_id"`
	CreatedAt       string `json:"created_at"`
	CreatedBy       string `json:"created_by"`
}

func ValidMeetingPolicy(value string) bool {
	return value == MeetingPolicyOwnerDecision || value == MeetingPolicyNamedReviewer || value == MeetingPolicyManagerBounded
}

func ValidMeetingAction(value string) bool {
	return value == MeetingActionSequence || value == MeetingActionSplit || value == MeetingActionReassign || value == MeetingActionDesignateRole || value == MeetingActionCancel
}
