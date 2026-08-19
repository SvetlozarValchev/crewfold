package domain

const (
	DomainWorkProposalPending  = "pending"
	DomainWorkProposalAccepted = "accepted"
	DomainWorkProposalRejected = "rejected"
	DomainWorkProposalStale    = "stale"
)

type DomainWorkProposalTask struct {
	Key                string            `json:"key"`
	Title              string            `json:"title"`
	Description        string            `json:"description"`
	TaskClass          string            `json:"task_class"`
	Priority           int               `json:"priority"`
	Budget             Budget            `json:"budget"`
	AssigneeKey        string            `json:"assignee_key"`
	DependsOn          []string          `json:"depends_on"`
	DependencyDelivery map[string]string `json:"dependency_delivery"`
}

// DomainWorkProposalAgent is an inert logical team member. Existing agents
// carry exact canonical references; new agents carry their complete proposed
// definition and execution allocation. No new agent exists until acceptance.
type DomainWorkProposalAgent struct {
	Key                        string `json:"key"`
	ExistingAgentID            string `json:"existing_agent_id,omitempty"`
	ExistingMembershipRevision int64  `json:"existing_membership_revision,omitempty"`
	ExistingLaunchProfileID    string `json:"existing_launch_profile_id,omitempty"`
	Name                       string `json:"name,omitempty"`
	Role                       string `json:"role,omitempty"`
	ParentKey                  string `json:"parent_key,omitempty"`
	OperatingCharter           string `json:"operating_charter,omitempty"`
	DelegationPolicy           string `json:"delegation_policy,omitempty"`
	Provider                   string `json:"provider,omitempty"`
	Runtime                    string `json:"runtime,omitempty"`
	MaxConcurrency             int    `json:"max_concurrency,omitempty"`
	TaskClass                  string `json:"task_class,omitempty"`
	Budget                     Budget `json:"budget"`
}

type DomainWorkProposalContent struct {
	ObjectiveTitle          string                    `json:"objective_title"`
	ObjectiveBudget         Budget                    `json:"objective_budget"`
	PrimaryCheckoutID       string                    `json:"primary_checkout_id"`
	PrimaryCheckoutRevision int64                     `json:"primary_checkout_revision"`
	ReferenceCheckoutIDs    []string                  `json:"reference_checkout_ids"`
	Agents                  []DomainWorkProposalAgent `json:"agents"`
	Tasks                   []DomainWorkProposalTask  `json:"tasks"`
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
	AgentKey      string `json:"agent_key,omitempty"`
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
