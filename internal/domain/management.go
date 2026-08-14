package domain

const (
	ManagerGrantActive  = "active"
	ManagerGrantRevoked = "revoked"
	ManagerGrantExpired = "expired"

	LaunchProfileActive  = "active"
	LaunchProfileRetired = "retired"

	ManagerProposalTaskDecomposition = "task_decomposition"
	ManagerProposalAssignment        = "assignment"
	ManagerProposalReview            = "review"
	ManagerProposalEscalation        = "escalation"

	ManagerProposalPending  = "pending"
	ManagerProposalInvalid  = "invalid"
	ManagerProposalAccepted = "accepted"
	ManagerProposalRejected = "rejected"
	ManagerProposalStale    = "stale"

	ProposalActionCreateTask              = "create_task"
	ProposalActionAddDependency           = "add_dependency"
	ProposalActionDeclareClaimRequirement = "declare_claim_requirement"
	ProposalActionAssignTask              = "assign_task"
	ProposalActionRequestReview           = "request_review"
	ProposalActionRequestAction           = "request_action"

	ProposalResponseResumeRun    = "resume_run"
	ProposalResponseStopRun      = "stop_run"
	ProposalResponseRetryTask    = "retry_task"
	ProposalResponseReassignTask = "reassign_task"

	ProposalIssueError   = "error"
	ProposalIssueWarning = "warning"

	SchedulingIntentPending          = "pending"
	SchedulingIntentDeferred         = "deferred"
	SchedulingIntentAwaitingApproval = "awaiting_approval"
	SchedulingIntentRunRequested     = "run_requested"
	SchedulingIntentSatisfied        = "satisfied"
	SchedulingIntentFailed           = "failed"
	SchedulingIntentCancelled        = "cancelled"

	SupervisorConditionDependencyReady   = "dependency_ready"
	SupervisorConditionBlocked           = "blocked"
	SupervisorConditionStale             = "stale"
	SupervisorConditionFailed            = "failed"
	SupervisorConditionRepeatedFailure   = "repeated_failure"
	SupervisorConditionOverBudget        = "over_budget"
	SupervisorConditionManagerEscalation = "manager_escalation"

	SupervisorResponseSchedule       = "schedule"
	SupervisorResponseResumeRun      = ProposalResponseResumeRun
	SupervisorResponseStopRun        = ProposalResponseStopRun
	SupervisorResponseRetryTask      = ProposalResponseRetryTask
	SupervisorResponseReassignTask   = ProposalResponseReassignTask
	SupervisorResponseRequestOwner   = "request_owner"
	SupervisorActionProposed         = "proposed"
	SupervisorActionAwaitingApproval = "awaiting_approval"
	SupervisorActionApplied          = "applied"
	SupervisorActionDeferred         = "deferred"
	SupervisorActionDismissed        = "dismissed"
	SupervisorActionFailed           = "failed"

	ApprovalPending  = "pending"
	ApprovalGranted  = "granted"
	ApprovalDenied   = "denied"
	ApprovalExpired  = "expired"
	ApprovalConsumed = "consumed"
)

// ManagerProposalLimits are owner-authored caps. A zero budget dimension is
// unlimited; all count limits must be positive and never exceed the hard store
// bounds.
type ManagerProposalLimits struct {
	MaxOpenProposals     int    `json:"max_open_proposals"`
	MaxActions           int    `json:"max_actions"`
	MaxTasks             int    `json:"max_tasks"`
	MaxDependencies      int    `json:"max_dependencies"`
	MaxClaimRequirements int    `json:"max_claim_requirements"`
	Budget               Budget `json:"budget"`
}

type ManagerGrantLaunchProfile struct {
	LaunchProfileID string `json:"launch_profile_id"`
	Revision        int64  `json:"revision"`
	AgentID         string `json:"agent_id"`
	AgentRevision   int64  `json:"agent_revision"`
}

// ManagerGrant is the only authority that lets an agent-originated run submit
// manager proposals. Agent Role remains arbitrary display metadata and is never
// consulted for authority.
type ManagerGrant struct {
	ID                string                      `json:"id"`
	WorkspaceID       string                      `json:"workspace_id"`
	ProjectID         string                      `json:"project_id"`
	ObjectiveID       string                      `json:"objective_id"`
	ObjectiveRevision int64                       `json:"objective_revision"`
	TaskID            string                      `json:"task_id"`
	TaskRevision      int64                       `json:"task_revision"`
	AgentID           string                      `json:"agent_id"`
	AgentRevision     int64                       `json:"agent_revision"`
	ProposalKinds     []string                    `json:"proposal_kinds"`
	LaunchProfiles    []ManagerGrantLaunchProfile `json:"launch_profiles"`
	AllowedClaimKinds []string                    `json:"allowed_claim_kinds"`
	Limits            ManagerProposalLimits       `json:"limits"`
	ContentSHA256     string                      `json:"content_sha256"`
	ExpiresAt         string                      `json:"expires_at,omitempty"`
	Status            string                      `json:"status"`
	Revision          int64                       `json:"revision"`
	CreatedAt         string                      `json:"created_at"`
	UpdatedAt         string                      `json:"updated_at"`
	CreatedBy         string                      `json:"created_by"`
	UpdatedBy         string                      `json:"updated_by"`
}

// LaunchProfile is immutable owner-authored launch content. Retirement changes
// only lifecycle metadata; a manager can cite a profile but cannot alter its
// provider, runtime, checkout, scenario, leases, or capability lifetime.
type LaunchProfile struct {
	ID                     string       `json:"id"`
	WorkspaceID            string       `json:"workspace_id"`
	ProjectID              string       `json:"project_id"`
	AgentID                string       `json:"agent_id"`
	AgentRevision          int64        `json:"agent_revision"`
	Purpose                string       `json:"purpose,omitempty"`
	Runtime                string       `json:"runtime"`
	Provider               string       `json:"provider"`
	CheckoutID             string       `json:"checkout_id,omitempty"`
	Scenario               FakeScenario `json:"scenario"`
	ScenarioSHA256         string       `json:"scenario_sha256"`
	ContentSHA256          string       `json:"content_sha256"`
	AssignmentLeaseSeconds int64        `json:"assignment_lease_seconds"`
	CapabilityTTLSeconds   int64        `json:"capability_ttl_seconds"`
	ManagerGrantID         string       `json:"manager_grant_id,omitempty"`
	Status                 string       `json:"status"`
	Revision               int64        `json:"revision"`
	CreatedAt              string       `json:"created_at"`
	UpdatedAt              string       `json:"updated_at"`
	CreatedBy              string       `json:"created_by"`
	UpdatedBy              string       `json:"updated_by"`
}

type ProposalTaskRef struct {
	TaskID               string `json:"task_id,omitempty"`
	ProposalTaskKey      string `json:"proposal_task_key,omitempty"`
	ExpectedTaskRevision int64  `json:"expected_task_revision,omitempty"`
}

type ProposalCreateTaskAction struct {
	TaskKey         string `json:"task_key"`
	LaunchProfileID string `json:"launch_profile_id"`
	Title           string `json:"title"`
	Description     string `json:"description,omitempty"`
	Priority        int    `json:"priority"`
	Budget          Budget `json:"budget"`
}

type ProposalAddDependencyAction struct {
	Task      ProposalTaskRef `json:"task"`
	DependsOn ProposalTaskRef `json:"depends_on"`
}

type ProposalDeclareClaimRequirementAction struct {
	Task           ProposalTaskRef `json:"task"`
	Kind           string          `json:"kind"`
	Target         string          `json:"target"`
	Mode           string          `json:"mode"`
	ConflictPolicy string          `json:"conflict_policy"`
}

type ProposalAssignTaskAction struct {
	Task            ProposalTaskRef `json:"task"`
	LaunchProfileID string          `json:"launch_profile_id"`
}

type ProposalRequestReviewAction struct {
	Task            ProposalTaskRef `json:"task"`
	LaunchProfileID string          `json:"launch_profile_id"`
	Title           string          `json:"title"`
	Description     string          `json:"description,omitempty"`
	Priority        int             `json:"priority"`
	Budget          Budget          `json:"budget"`
}

type ProposalRequestAction struct {
	Response         string `json:"response"`
	TargetRunID      string `json:"target_run_id,omitempty"`
	TargetTaskID     string `json:"target_task_id,omitempty"`
	LaunchProfileID  string `json:"launch_profile_id,omitempty"`
	Reason           string `json:"reason"`
	ExpectedRevision int64  `json:"expected_revision"`
}

// ManagerProposalAction is a closed tagged union. Exactly one payload pointer
// must be present and it must match Type.
type ManagerProposalAction struct {
	ID                      string                                 `json:"id,omitempty"`
	Ordinal                 int                                    `json:"ordinal"`
	Type                    string                                 `json:"type"`
	CreateTask              *ProposalCreateTaskAction              `json:"create_task,omitempty"`
	AddDependency           *ProposalAddDependencyAction           `json:"add_dependency,omitempty"`
	DeclareClaimRequirement *ProposalDeclareClaimRequirementAction `json:"declare_claim_requirement,omitempty"`
	AssignTask              *ProposalAssignTaskAction              `json:"assign_task,omitempty"`
	RequestReview           *ProposalRequestReviewAction           `json:"request_review,omitempty"`
	RequestAction           *ProposalRequestAction                 `json:"request_action,omitempty"`
}

type ProposalValidationIssue struct {
	Code     string `json:"code"`
	Path     string `json:"path"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

type ManagerProposal struct {
	ID                string                    `json:"id"`
	WorkspaceID       string                    `json:"workspace_id"`
	ProjectID         string                    `json:"project_id"`
	ObjectiveID       string                    `json:"objective_id"`
	ObjectiveRevision int64                     `json:"objective_revision"`
	SourceRunID       string                    `json:"source_run_id"`
	SourceAgentID     string                    `json:"source_agent_id"`
	GrantID           string                    `json:"grant_id"`
	GrantRevision     int64                     `json:"grant_revision"`
	Kind              string                    `json:"kind"`
	Summary           string                    `json:"summary"`
	Status            string                    `json:"status"`
	AsOfEventSequence int64                     `json:"as_of_event_sequence"`
	Actions           []ManagerProposalAction   `json:"actions"`
	ValidationIssues  []ProposalValidationIssue `json:"validation_issues"`
	ContentSHA256     string                    `json:"content_sha256"`
	DecisionNote      string                    `json:"decision_note,omitempty"`
	Revision          int64                     `json:"revision"`
	CreatedAt         string                    `json:"created_at"`
	UpdatedAt         string                    `json:"updated_at"`
	DecidedAt         string                    `json:"decided_at,omitempty"`
	CreatedBy         string                    `json:"created_by"`
	UpdatedBy         string                    `json:"updated_by"`
	DecidedBy         string                    `json:"decided_by,omitempty"`
}

type ManagerProposalEffect struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	ProjectID     string `json:"project_id"`
	ObjectiveID   string `json:"objective_id"`
	ProposalID    string `json:"proposal_id"`
	ActionID      string `json:"action_id"`
	EffectType    string `json:"effect_type"`
	EntityType    string `json:"entity_type"`
	EntityID      string `json:"entity_id"`
	EventSequence int64  `json:"event_sequence"`
	CreatedAt     string `json:"created_at"`
}

type TaskClaimRequirement struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspace_id"`
	ProjectID        string `json:"project_id"`
	ObjectiveID      string `json:"objective_id"`
	TaskID           string `json:"task_id"`
	SourceProposalID string `json:"source_proposal_id"`
	SourceActionID   string `json:"source_action_id"`
	Kind             string `json:"kind"`
	Target           string `json:"target"`
	Mode             string `json:"mode"`
	ConflictPolicy   string `json:"conflict_policy"`
	Revision         int64  `json:"revision"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	CreatedBy        string `json:"created_by"`
	UpdatedBy        string `json:"updated_by"`
}

type SchedulingIntent struct {
	ID                          string `json:"id"`
	WorkspaceID                 string `json:"workspace_id"`
	ProjectID                   string `json:"project_id"`
	ObjectiveID                 string `json:"objective_id"`
	TaskID                      string `json:"task_id"`
	AgentID                     string `json:"agent_id"`
	LaunchProfileID             string `json:"launch_profile_id"`
	SourceProposalID            string `json:"source_proposal_id"`
	SourceActionID              string `json:"source_action_id"`
	SourceCheckRepairProposalID string `json:"source_check_repair_proposal_id,omitempty"`
	SourceOwnerTurnID           string `json:"source_owner_turn_id,omitempty"`
	SourceOwnerOperationID      string `json:"source_owner_operation_id,omitempty"`
	Status                      string `json:"status"`
	Reason                      string `json:"reason,omitempty"`
	AssignmentID                string `json:"assignment_id,omitempty"`
	RunID                       string `json:"run_id,omitempty"`
	SupervisorActionID          string `json:"supervisor_action_id,omitempty"`
	Attempts                    int    `json:"attempts"`
	LastEvaluatedEventSequence  int64  `json:"-"`
	Revision                    int64  `json:"revision"`
	CreatedAt                   string `json:"created_at"`
	UpdatedAt                   string `json:"updated_at"`
	NextAttemptAt               string `json:"next_attempt_at,omitempty"`
	CreatedBy                   string `json:"created_by"`
	UpdatedBy                   string `json:"updated_by"`
}

type SupervisorLimits struct {
	MaxActiveRuns              int            `json:"max_active_runs"`
	MaxStartingRuns            int            `json:"max_starting_runs"`
	DefaultProjectConcurrency  int            `json:"default_project_concurrency"`
	DefaultProviderConcurrency int            `json:"default_provider_concurrency"`
	ProjectConcurrency         map[string]int `json:"project_concurrency"`
	ProviderConcurrency        map[string]int `json:"provider_concurrency"`
}

type SupervisorPolicy struct {
	WorkspaceID          string           `json:"workspace_id"`
	Enabled              bool             `json:"enabled"`
	Limits               SupervisorLimits `json:"limits"`
	AutoSchedule         bool             `json:"auto_schedule"`
	AutoRetryLimit       int              `json:"auto_retry_limit"`
	RetryCooldownSeconds int64            `json:"retry_cooldown_seconds"`
	Revision             int64            `json:"revision"`
	CreatedAt            string           `json:"created_at"`
	UpdatedAt            string           `json:"updated_at"`
	CreatedBy            string           `json:"created_by"`
	UpdatedBy            string           `json:"updated_by"`
}

type SupervisorAction struct {
	ID                 string         `json:"id"`
	WorkspaceID        string         `json:"workspace_id"`
	ProjectID          string         `json:"project_id,omitempty"`
	ObjectiveID        string         `json:"objective_id,omitempty"`
	TaskID             string         `json:"task_id,omitempty"`
	RunID              string         `json:"run_id,omitempty"`
	PriorRunID         string         `json:"prior_run_id,omitempty"`
	SourceProposalID   string         `json:"source_proposal_id,omitempty"`
	SourceActionID     string         `json:"source_action_id,omitempty"`
	AgentID            string         `json:"agent_id,omitempty"`
	IntentID           string         `json:"intent_id,omitempty"`
	Condition          string         `json:"condition"`
	ConditionKey       string         `json:"condition_key"`
	Response           string         `json:"response"`
	Status             string         `json:"status"`
	Decision           string         `json:"decision,omitempty"`
	EntityRevision     int64          `json:"entity_revision"`
	PolicyRevision     int64          `json:"policy_revision"`
	AsOfEventSequence  int64          `json:"as_of_event_sequence"`
	Reasons            []string       `json:"reasons"`
	ConstraintSnapshot map[string]any `json:"constraint_snapshot"`
	ContentSHA256      string         `json:"content_sha256"`
	ApprovalID         string         `json:"approval_id,omitempty"`
	Revision           int64          `json:"revision"`
	CreatedAt          string         `json:"created_at"`
	UpdatedAt          string         `json:"updated_at"`
	AppliedAt          string         `json:"applied_at,omitempty"`
	CreatedBy          string         `json:"created_by"`
	UpdatedBy          string         `json:"updated_by"`
}

type ApprovalRequest struct {
	ID                     string `json:"id"`
	WorkspaceID            string `json:"workspace_id"`
	ProjectID              string `json:"project_id,omitempty"`
	ActionID               string `json:"action_id"`
	Status                 string `json:"status"`
	DecisionNote           string `json:"decision_note,omitempty"`
	DecisionEventSequence  int64  `json:"decision_event_sequence,omitempty"`
	ExpectedActionRevision int64  `json:"expected_action_revision"`
	Revision               int64  `json:"revision"`
	ExpiresAt              string `json:"expires_at,omitempty"`
	CreatedAt              string `json:"created_at"`
	UpdatedAt              string `json:"updated_at"`
	DecidedAt              string `json:"decided_at,omitempty"`
	CreatedBy              string `json:"created_by"`
	UpdatedBy              string `json:"updated_by"`
	DecidedBy              string `json:"decided_by,omitempty"`
}

type SupervisorCandidateExplanation struct {
	IntentID    string         `json:"intent_id"`
	TaskID      string         `json:"task_id"`
	Eligible    bool           `json:"eligible"`
	Reasons     []string       `json:"reasons"`
	Constraints map[string]any `json:"constraints"`
}

type SupervisorExplanation struct {
	WorkspaceID       string                           `json:"workspace_id"`
	Policy            SupervisorPolicy                 `json:"policy"`
	AsOfEventSequence int64                            `json:"as_of_event_sequence"`
	Candidates        []SupervisorCandidateExplanation `json:"candidates"`
}
