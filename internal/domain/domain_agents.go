package domain

import "encoding/json"

const (
	DomainAgentActive  = "active"
	DomainAgentRetired = "retired"

	DomainAgentHandsOn         = "hands_on"
	DomainAgentAdaptive        = "adaptive"
	DomainAgentDelegationFirst = "delegation_first"

	DomainStaffingGrantActive  = "active"
	DomainStaffingGrantRevoked = "revoked"
	DomainStaffingGrantExpired = "expired"
)

// DomainAgentStaffingProfile is one exact provider/runtime envelope an owner
// permits a durable manager to use for children. Names and roles are not part
// of this authority.
type DomainAgentStaffingProfile struct {
	Provider       string `json:"provider"`
	Runtime        string `json:"runtime"`
	MaxConcurrency int    `json:"max_concurrency"`
}

// DomainAgentStaffingGrant is owner-authored authority for bounded durable
// child creation. Parentage remains presentation; this grant is the effect
// authority checked by the daemon and Store.
type DomainAgentStaffingGrant struct {
	ID                        string                       `json:"id"`
	ProjectID                 string                       `json:"project_id"`
	ManagerAgentID            string                       `json:"manager_agent_id"`
	ManagerMembershipRevision int64                        `json:"manager_membership_revision"`
	Profiles                  []DomainAgentStaffingProfile `json:"profiles"`
	TaskClasses               []string                     `json:"task_classes"`
	MaxDescendants            int                          `json:"max_descendants"`
	MaxConcurrency            int                          `json:"max_concurrency"`
	Budget                    Budget                       `json:"budget"`
	ExpiresAt                 string                       `json:"expires_at,omitempty"`
	Status                    string                       `json:"status"`
	Revision                  int64                        `json:"revision"`
	CreatedAt                 string                       `json:"created_at"`
	UpdatedAt                 string                       `json:"updated_at"`
	CreatedBy                 string                       `json:"created_by"`
	UpdatedBy                 string                       `json:"updated_by"`
}

type DomainAgentStaffingAllocation struct {
	ID            string `json:"id"`
	GrantID       string `json:"grant_id"`
	ProjectID     string `json:"project_id"`
	ParentAgentID string `json:"parent_agent_id"`
	ChildAgentID  string `json:"child_agent_id"`
	Provider      string `json:"provider"`
	Runtime       string `json:"runtime"`
	TaskClass     string `json:"task_class"`
	Budget        Budget `json:"budget"`
	EventSequence int64  `json:"event_sequence"`
	CreatedAt     string `json:"created_at"`
	CreatedBy     string `json:"created_by"`
}

type DomainAgentChildCreation struct {
	Agent          AgentDefinition               `json:"agent"`
	Membership     DomainAgentMembership         `json:"membership"`
	Grant          DomainAgentStaffingGrant      `json:"grant"`
	Allocation     DomainAgentStaffingAllocation `json:"allocation"`
	EventSequences []int64                       `json:"event_sequences"`
}

// DomainAgentMembership places one durable agent definition in one domain's
// owner-visible attention tree. Parentage and PreferredEntry are presentation
// and routing metadata only; they never grant authority.
type DomainAgentMembership struct {
	ProjectID        string `json:"project_id"`
	AgentID          string `json:"agent_id"`
	ParentAgentID    string `json:"parent_agent_id,omitempty"`
	WorkstreamID     string `json:"workstream_id,omitempty"`
	OperatingCharter string `json:"operating_charter"`
	DelegationPolicy string `json:"delegation_policy"`
	PreferredEntry   bool   `json:"preferred_entry"`
	Status           string `json:"status"`
	Revision         int64  `json:"revision"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	CreatedBy        string `json:"created_by"`
	UpdatedBy        string `json:"updated_by"`
}

// DomainAgent joins the durable definition with its domain-local position.
// The result remains flat so every consumer sees exact canonical parent IDs;
// presentation clients may render the proven-acyclic relation as a tree.
type DomainAgent struct {
	Definition AgentDefinition       `json:"definition"`
	Membership DomainAgentMembership `json:"membership"`
}

// DomainAgentCreation is the atomic owner-authorized creation of a durable
// definition and its place in one domain hierarchy.
type DomainAgentCreation struct {
	Agent          DomainAgent `json:"agent"`
	EventSequences []int64     `json:"event_sequences"`
}

type DomainAgentTree struct {
	ProjectID string        `json:"project_id"`
	Agents    []DomainAgent `json:"agents"`
}

const (
	DomainAgentSessionUnbound  = "unbound"
	DomainAgentSessionReady    = "ready"
	DomainAgentSessionDetached = "detached"
)

// DomainAgentSession describes the owner-visible state of one durable
// provider conversation. Provider thread and node identifiers are deliberately
// private operational bindings; they never become the agent's domain identity
// or cross the public JSON boundary.
type DomainAgentSession struct {
	ProjectID       string `json:"project_id"`
	AgentID         string `json:"agent_id"`
	Provider        string `json:"provider,omitempty"`
	State           string `json:"state"`
	CWD             string `json:"cwd,omitempty"`
	HasConversation bool   `json:"has_conversation"`
	Revision        int64  `json:"revision"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`

	ThreadID        string `json:"-"`
	NodeID          string `json:"-"`
	NodeFingerprint string `json:"-"`
}

type DomainAgentSessionItem struct {
	ID             string                            `json:"id"`
	Type           string                            `json:"type"`
	Text           string                            `json:"text,omitempty"`
	Command        string                            `json:"command,omitempty"`
	Status         string                            `json:"status,omitempty"`
	CWD            string                            `json:"cwd,omitempty"`
	ProcessID      string                            `json:"process_id,omitempty"`
	ExitCode       *int                              `json:"exit_code,omitempty"`
	DurationMillis int64                             `json:"duration_ms,omitempty"`
	CommandActions []DomainAgentSessionCommandAction `json:"command_actions,omitempty"`
	Changes        []DomainAgentSessionFileChange    `json:"changes,omitempty"`
}

// DomainAgentSessionCommandAction is app-server's safe, best-effort command
// classification. It lets owner surfaces distinguish repository exploration
// from an opaque shell invocation without exposing environment variables or
// inventing intent from command text.
type DomainAgentSessionCommandAction struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Name    string `json:"name,omitempty"`
	Path    string `json:"path,omitempty"`
	Query   string `json:"query,omitempty"`
}

// DomainAgentSessionFileChange preserves app-server's bounded observable patch
// projection. Crewfold still treats provider file output as noncanonical until
// a real assigned execution run records its own evidence.
type DomainAgentSessionFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Diff string `json:"diff,omitempty"`
}

type DomainAgentSessionTurn struct {
	ID     string                   `json:"id"`
	Status string                   `json:"status"`
	Items  []DomainAgentSessionItem `json:"items"`
}

type DomainAgentSessionView struct {
	Session      DomainAgentSession       `json:"session"`
	ThreadStatus string                   `json:"thread_status"`
	Turns        []DomainAgentSessionTurn `json:"turns"`
}

// DomainAgentToolReceipt is Crewfold's durable record of one provider-originated
// structured tool exchange. Provider call/turn identifiers and node bindings
// remain private; the owner-facing result exposes the exact tool, status, and
// bounded Crewfold response.
type DomainAgentToolReceipt struct {
	ID              string          `json:"id"`
	ProjectID       string          `json:"project_id"`
	AgentID         string          `json:"agent_id"`
	SessionRevision int64           `json:"session_revision"`
	ToolName        string          `json:"tool_name"`
	Status          string          `json:"status"`
	ResponseSHA256  string          `json:"response_sha256"`
	Response        json.RawMessage `json:"response"`
	CreatedAt       string          `json:"created_at"`

	CallID        string `json:"-"`
	TurnID        string `json:"-"`
	RequestSHA256 string `json:"-"`
}

// DomainAgentSessionScope is a private daemon/store join used to authorize a
// provider-originated structured tool call. It is never a public API result.
type DomainAgentSessionScope struct {
	Workspace  Workspace             `json:"-"`
	Project    Project               `json:"-"`
	Agent      AgentDefinition       `json:"-"`
	Membership DomainAgentMembership `json:"-"`
	Session    DomainAgentSession    `json:"-"`
}
