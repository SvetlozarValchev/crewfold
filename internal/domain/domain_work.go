package domain

const (
	DomainWorkProposalPending  = "pending"
	DomainWorkProposalAccepted = "accepted"
	DomainWorkProposalRejected = "rejected"
	DomainWorkProposalStale    = "stale"
)

type DomainWorkProposalTask struct {
	Key             string   `json:"key"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	TaskClass       string   `json:"task_class"`
	Priority        int      `json:"priority"`
	Budget          Budget   `json:"budget"`
	LaunchProfileID string   `json:"launch_profile_id"`
	DependsOn       []string `json:"depends_on"`
}

type DomainWorkProposalContent struct {
	ObjectiveTitle  string                   `json:"objective_title"`
	ObjectiveBudget Budget                   `json:"objective_budget"`
	Tasks           []DomainWorkProposalTask `json:"tasks"`
}

type DomainWorkProposal struct {
	ID                    string                    `json:"id"`
	WorkspaceID           string                    `json:"workspace_id"`
	ProjectID             string                    `json:"project_id"`
	SourceAgentID         string                    `json:"source_agent_id"`
	SourceThreadID        string                    `json:"source_thread_id"`
	StaffingGrantID       string                    `json:"staffing_grant_id"`
	StaffingGrantRevision int64                     `json:"staffing_grant_revision"`
	Summary               string                    `json:"summary"`
	AsOfEventSequence     int64                     `json:"as_of_event_sequence"`
	Content               DomainWorkProposalContent `json:"content"`
	ContentSHA256         string                    `json:"content_sha256"`
	Status                string                    `json:"status"`
	DecisionNote          string                    `json:"decision_note,omitempty"`
	Revision              int64                     `json:"revision"`
	CreatedAt             string                    `json:"created_at"`
	UpdatedAt             string                    `json:"updated_at"`
	CreatedBy             string                    `json:"created_by"`
	UpdatedBy             string                    `json:"updated_by"`
	DecidedAt             string                    `json:"decided_at,omitempty"`
	DecidedBy             string                    `json:"decided_by,omitempty"`
}

type DomainWorkProposalEffect struct {
	TaskKey       string `json:"task_key,omitempty"`
	EntityType    string `json:"entity_type"`
	EntityID      string `json:"entity_id"`
	EventSequence int64  `json:"event_sequence"`
}

type DomainWorkProposalDecision struct {
	Proposal      DomainWorkProposal         `json:"proposal"`
	Effects       []DomainWorkProposalEffect `json:"effects"`
	EventSequence int64                      `json:"event_sequence"`
}
