package domain

const (
	ObjectiveActive    = "active"
	ObjectiveCompleted = "completed"
	ObjectiveCancelled = "cancelled"

	TaskReady     = "ready"
	TaskAssigned  = "assigned"
	TaskActive    = "active"
	TaskBlocked   = "blocked"
	TaskCompleted = "completed"
	TaskCancelled = "cancelled"

	AssignmentActive   = "active"
	AssignmentExpired  = "expired"
	AssignmentReleased = "released"
)

type AgentDefinition struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	Name           string `json:"name"`
	Role           string `json:"role"`
	Provider       string `json:"provider"`
	Runtime        string `json:"runtime"`
	Enabled        bool   `json:"enabled"`
	MaxConcurrency int    `json:"max_concurrency"`
	Revision       int64  `json:"revision"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	CreatedBy      string `json:"created_by"`
	UpdatedBy      string `json:"updated_by"`
}

type Budget struct {
	TokenLimit  int64 `json:"token_limit"`
	CostCents   int64 `json:"cost_cents"`
	TimeSeconds int64 `json:"time_seconds"`
}

type Objective struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Budget      Budget `json:"budget"`
	Revision    int64  `json:"revision"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	CreatedBy   string `json:"created_by"`
	UpdatedBy   string `json:"updated_by"`
}

type Task struct {
	ID                       string `json:"id"`
	WorkspaceID              string `json:"workspace_id"`
	ProjectID                string `json:"project_id"`
	ObjectiveID              string `json:"objective_id,omitempty"`
	Title                    string `json:"title"`
	Description              string `json:"description,omitempty"`
	Status                   string `json:"status"`
	BlockedReason            string `json:"blocked_reason,omitempty"`
	Priority                 int    `json:"priority"`
	Budget                   Budget `json:"budget"`
	AssignmentID             string `json:"assignment_id,omitempty"`
	AssignedAgentID          string `json:"assigned_agent_id,omitempty"`
	AssignmentLeaseExpiresAt string `json:"assignment_lease_expires_at,omitempty"`
	Revision                 int64  `json:"revision"`
	CreatedAt                string `json:"created_at"`
	UpdatedAt                string `json:"updated_at"`
	CreatedBy                string `json:"created_by"`
	UpdatedBy                string `json:"updated_by"`
}

type TaskAssignment struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	AgentID        string `json:"agent_id"`
	Status         string `json:"status"`
	LeaseExpiresAt string `json:"lease_expires_at"`
	Revision       int64  `json:"revision"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	CreatedBy      string `json:"created_by"`
	UpdatedBy      string `json:"updated_by"`
}

type TaskDependency struct {
	TaskID          string `json:"task_id"`
	DependsOnTaskID string `json:"depends_on_task_id"`
	CreatedAt       string `json:"created_at"`
	CreatedBy       string `json:"created_by"`
}

type TaskReadiness struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
}

type TaskDetail struct {
	Task         Task             `json:"task"`
	Dependencies []TaskDependency `json:"dependencies"`
	Assignment   *TaskAssignment  `json:"assignment,omitempty"`
	Readiness    TaskReadiness    `json:"readiness"`
}

type CoordinationStatus struct {
	AgentsRegistered int `json:"agents_registered"`
	AgentsEnabled    int `json:"agents_enabled"`
	TasksRegistered  int `json:"tasks_registered"`
	TasksReady       int `json:"tasks_ready"`
	TasksAssigned    int `json:"tasks_assigned"`
	TasksActive      int `json:"tasks_active"`
	TasksBlocked     int `json:"tasks_blocked"`
	TasksCompleted   int `json:"tasks_completed"`
	TasksCancelled   int `json:"tasks_cancelled"`
}
