package domain

const (
	CheckDefinitionActive  = "active"
	CheckDefinitionRetired = "retired"

	CheckRequirementActive  = "active"
	CheckRequirementRetired = "retired"

	CheckWatchGrantActive  = "active"
	CheckWatchGrantRevoked = "revoked"
	CheckWatchGrantExpired = "expired"

	CheckWatchOperationRun           = "run"
	CheckWatchOperationInspect       = "inspect"
	CheckWatchOperationProposeRepair = "propose_repair"

	CheckRouteActive  = "active"
	CheckRouteRetired = "retired"
	CheckRoutePass    = "pass"
	CheckRouteNonpass = "nonpass"
	CheckRouteStale   = "stale"

	CheckDutyEvidenceReview = "evidence_review"
	CheckDutyCoordination   = "coordination"
	CheckDutyTaskOwner      = "task_owner"

	CheckRunRequested = "requested"
	CheckRunStarting  = "starting"
	CheckRunRunning   = "running"
	CheckRunFinished  = "finished"

	CheckRunSourceOwner    = "owner"
	CheckRunSourceAgentRun = "agent_run"

	CheckJobPending  = "pending"
	CheckJobLeased   = "leased"
	CheckJobComplete = "complete"

	CheckPreflightWorkingDirectoryInvalid = "working_directory_invalid"
	CheckPreflightAuthorityRevoked        = "authority_revoked"
	CheckPreflightDefinitionRetired       = "definition_retired"
	CheckPreflightRequirementRetired      = "requirement_retired"
	CheckPreflightCheckoutChanged         = "checkout_changed"

	CheckOutcomePassed      = "passed"
	CheckOutcomeFailed      = "failed"
	CheckOutcomeTimedOut    = "timed_out"
	CheckOutcomeStartFailed = "start_failed"
	CheckOutcomeUnknown     = "unknown"

	CheckArtifactStdout     = "stdout"
	CheckArtifactStderr     = "stderr"
	CheckArtifactDiagnostic = "diagnostic"

	CheckFreshnessFresh   = "fresh"
	CheckFreshnessStale   = "stale"
	CheckFreshnessUnknown = "unknown"

	CheckRequirementMissing  = "missing"
	CheckRequirementRunning  = "running"
	CheckRequirementVerified = "verified"
	CheckRequirementFailed   = "failed"
	CheckRequirementStale    = "stale"
	CheckRequirementUnknown  = "unknown"

	EvidenceAgentSelfReport   = "agent_self_report"
	EvidenceMechanicalCheck   = "mechanical_check"
	EvidenceIndependentReview = "independent_review"
	EvidencePolicyAcceptance  = "policy_acceptance"

	CheckEvidenceSupports     = "supports"
	CheckEvidenceContradicts  = "contradicts"
	CheckEvidenceInconclusive = "inconclusive"

	CheckRepairPending  = "pending"
	CheckRepairAccepted = "accepted"
	CheckRepairRejected = "rejected"
	CheckRepairStale    = "stale"
)

// CheckDefinition is an owner-authored immutable direct command. Arguments are
// fixed and ordered. It intentionally has no shell, stdin, environment,
// credential, provider, MCP, agent-role, or launch-profile-purpose field.
type CheckDefinition struct {
	ID               string   `json:"id"`
	WorkspaceID      string   `json:"workspace_id"`
	ProjectID        string   `json:"project_id"`
	Name             string   `json:"name"`
	Executable       string   `json:"executable"`
	Arguments        []string `json:"arguments"`
	WorkingDirectory string   `json:"working_directory"`
	TimeoutMillis    int64    `json:"timeout_millis"`
	OutputByteLimit  int64    `json:"output_byte_limit"`
	ContentRevision  int64    `json:"content_revision"`
	ContentSHA256    string   `json:"content_sha256"`
	Status           string   `json:"status"`
	Revision         int64    `json:"revision"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
	CreatedBy        string   `json:"created_by"`
	UpdatedBy        string   `json:"updated_by"`
}

type TaskCheckRequirement struct {
	ID                        string `json:"id"`
	WorkspaceID               string `json:"workspace_id"`
	ProjectID                 string `json:"project_id"`
	TaskID                    string `json:"task_id"`
	TaskRevisionAtCreation    int64  `json:"task_revision_at_creation"`
	CriterionKey              string `json:"criterion_key"`
	Statement                 string `json:"statement"`
	DefinitionID              string `json:"definition_id"`
	DefinitionContentRevision int64  `json:"definition_content_revision"`
	Status                    string `json:"status"`
	Revision                  int64  `json:"revision"`
	CreatedAt                 string `json:"created_at"`
	UpdatedAt                 string `json:"updated_at"`
	CreatedBy                 string `json:"created_by"`
	UpdatedBy                 string `json:"updated_by"`
}

type CheckWatchGrantDefinition struct {
	DefinitionID     string `json:"definition_id"`
	ContentRevision  int64  `json:"content_revision"`
	DefinitionSHA256 string `json:"definition_sha256"`
}

// CheckWatchGrant is the only agent check-watch authority. AgentDefinition.Role
// and LaunchProfile.Purpose are display metadata and must never be consulted.
type CheckWatchGrant struct {
	ID            string                      `json:"id"`
	WorkspaceID   string                      `json:"workspace_id"`
	ProjectID     string                      `json:"project_id"`
	AgentID       string                      `json:"agent_id"`
	AgentRevision int64                       `json:"agent_revision"`
	Operations    []string                    `json:"operations"`
	Definitions   []CheckWatchGrantDefinition `json:"definitions"`
	MaxPending    int                         `json:"max_pending"`
	MaxInFlight   int                         `json:"max_in_flight"`
	ExpiresAt     string                      `json:"expires_at,omitempty"`
	ContentSHA256 string                      `json:"content_sha256"`
	Status        string                      `json:"status"`
	Revision      int64                       `json:"revision"`
	CreatedAt     string                      `json:"created_at"`
	UpdatedAt     string                      `json:"updated_at"`
	CreatedBy     string                      `json:"created_by"`
	UpdatedBy     string                      `json:"updated_by"`
}

type CheckPolicy struct {
	WorkspaceID                 string `json:"workspace_id"`
	ProjectID                   string `json:"project_id"`
	RepairProposalsEnabled      bool   `json:"repair_proposals_enabled"`
	RepairLaunchProfileID       string `json:"repair_launch_profile_id,omitempty"`
	RepairLaunchProfileRevision int64  `json:"repair_launch_profile_revision,omitempty"`
	MaxOpenRepairProposals      int    `json:"max_open_repair_proposals"`
	Revision                    int64  `json:"revision"`
	CreatedAt                   string `json:"created_at"`
	UpdatedAt                   string `json:"updated_at"`
	CreatedBy                   string `json:"created_by"`
	UpdatedBy                   string `json:"updated_by"`
}

type CheckRoute struct {
	ID                        string `json:"id"`
	WorkspaceID               string `json:"workspace_id"`
	ProjectID                 string `json:"project_id"`
	DefinitionID              string `json:"definition_id,omitempty"`
	DefinitionContentRevision int64  `json:"definition_content_revision,omitempty"`
	Trigger                   string `json:"trigger"`
	Duty                      string `json:"duty"`
	AgentID                   string `json:"agent_id"`
	AgentRevision             int64  `json:"agent_revision"`
	Status                    string `json:"status"`
	Revision                  int64  `json:"revision"`
	CreatedAt                 string `json:"created_at"`
	UpdatedAt                 string `json:"updated_at"`
	CreatedBy                 string `json:"created_by"`
	UpdatedBy                 string `json:"updated_by"`
}

// CheckGitObservation is a typed source fact, never an evidence string. An
// unavailable observation has Available=false and no authoritative HEAD.
type CheckGitObservation struct {
	Available      bool     `json:"available"`
	RepositoryID   string   `json:"repository_id"`
	ObjectFormat   string   `json:"object_format"`
	CheckoutID     string   `json:"checkout_id"`
	Branch         string   `json:"branch,omitempty"`
	HeadCommit     string   `json:"head_commit,omitempty"`
	Dirty          bool     `json:"dirty"`
	DirtyPaths     []string `json:"dirty_paths"`
	ObservedAt     string   `json:"observed_at"`
	DiagnosticCode string   `json:"diagnostic_code,omitempty"`
	Diagnostic     string   `json:"diagnostic,omitempty"`
}

type CheckRunSource struct {
	Type          string `json:"type"`
	ActorID       string `json:"actor_id"`
	AgentID       string `json:"agent_id,omitempty"`
	AgentRevision int64  `json:"agent_revision,omitempty"`
	AgentRunID    string `json:"agent_run_id,omitempty"`
	GrantID       string `json:"grant_id,omitempty"`
	GrantRevision int64  `json:"grant_revision,omitempty"`
}

type CheckRun struct {
	ID                        string         `json:"id"`
	WorkspaceID               string         `json:"workspace_id"`
	ProjectID                 string         `json:"project_id"`
	TaskID                    string         `json:"task_id"`
	TaskRevision              int64          `json:"task_revision"`
	RequirementID             string         `json:"requirement_id"`
	RequirementRevision       int64          `json:"requirement_revision"`
	DefinitionID              string         `json:"definition_id"`
	DefinitionContentRevision int64          `json:"definition_content_revision"`
	DefinitionSHA256          string         `json:"definition_sha256"`
	CheckoutID                string         `json:"checkout_id"`
	CheckoutRevision          int64          `json:"checkout_revision"`
	RepositoryID              string         `json:"repository_id"`
	RepositoryObjectFormat    string         `json:"repository_object_format"`
	CheckoutPath              string         `json:"checkout_path"`
	CheckoutWriteMode         string         `json:"checkout_write_mode"`
	Source                    CheckRunSource `json:"source"`
	SourceMaxInFlight         int            `json:"source_max_in_flight,omitempty"`
	Status                    string         `json:"status"`
	RuntimeHandle             string         `json:"-"`
	RuntimeNodeID             string         `json:"-"`
	RuntimeNodeFingerprint    string         `json:"-"`
	RuntimeOperationID        string         `json:"-"`
	Revision                  int64          `json:"revision"`
	CreatedAt                 string         `json:"created_at"`
	UpdatedAt                 string         `json:"updated_at"`
	StartedAt                 string         `json:"started_at,omitempty"`
	FinishedAt                string         `json:"finished_at,omitempty"`
	CreatedBy                 string         `json:"created_by"`
	UpdatedBy                 string         `json:"updated_by"`
}

type CheckJob struct {
	ID             string `json:"id"`
	CheckRunID     string `json:"check_run_id"`
	Status         string `json:"status"`
	AvailableAt    string `json:"available_at"`
	LeaseExpiresAt string `json:"lease_expires_at,omitempty"`
	Attempts       int    `json:"attempts"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type CheckLaunchReceipt struct {
	ID                         string              `json:"id"`
	CheckRunID                 string              `json:"check_run_id"`
	CheckJobID                 string              `json:"check_job_id"`
	OperationID                string              `json:"operation_id"`
	EffectiveSpecSHA256        string              `json:"effective_spec_sha256"`
	EffectiveWorkingDirectory  string              `json:"effective_working_directory"`
	Launchable                 bool                `json:"launchable"`
	PreflightFailureCode       string              `json:"preflight_failure_code,omitempty"`
	PreflightFailureDiagnostic string              `json:"preflight_failure_diagnostic,omitempty"`
	DefinitionSHA256           string              `json:"definition_sha256"`
	Source                     CheckRunSource      `json:"source"`
	Observation                CheckGitObservation `json:"observation"`
	CreatedAt                  string              `json:"created_at"`
	CreatedBy                  string              `json:"created_by"`
}

type CheckResult struct {
	ID                        string              `json:"id"`
	CheckRunID                string              `json:"check_run_id"`
	RequirementID             string              `json:"requirement_id"`
	RequirementRevision       int64               `json:"requirement_revision"`
	DefinitionID              string              `json:"definition_id"`
	DefinitionContentRevision int64               `json:"definition_content_revision"`
	Outcome                   string              `json:"outcome"`
	ExitCode                  *int                `json:"exit_code,omitempty"`
	Forced                    bool                `json:"forced"`
	DiagnosticCode            string              `json:"diagnostic_code,omitempty"`
	Diagnostic                string              `json:"diagnostic,omitempty"`
	TerminalObservation       CheckGitObservation `json:"terminal_observation"`
	CreatedAt                 string              `json:"created_at"`
	CreatedBy                 string              `json:"created_by"`
}

type CheckArtifact struct {
	ID            string `json:"id"`
	CheckResultID string `json:"check_result_id"`
	Kind          string `json:"kind"`
	ContentSHA256 string `json:"content_sha256"`
	CapturedBytes int64  `json:"captured_bytes"`
	OmittedBytes  int64  `json:"omitted_bytes"`
	Truncated     bool   `json:"truncated"`
	CreatedAt     string `json:"created_at"`
}

type CheckResultFreshness struct {
	ID                string              `json:"id"`
	CheckResultID     string              `json:"check_result_id"`
	Revision          int64               `json:"revision"`
	Status            string              `json:"status"`
	Reason            string              `json:"reason"`
	InitiallyEligible bool                `json:"initially_eligible"`
	EverStale         bool                `json:"ever_stale"`
	Observation       CheckGitObservation `json:"observation"`
	CreatedAt         string              `json:"created_at"`
	CreatedBy         string              `json:"created_by"`
}

type CheckRequirementEvidence struct {
	ID                  string `json:"id"`
	RequirementID       string `json:"requirement_id"`
	RequirementRevision int64  `json:"requirement_revision"`
	CheckResultID       string `json:"check_result_id"`
	FreshnessRevision   int64  `json:"freshness_revision"`
	Class               string `json:"class"`
	Effect              string `json:"effect"`
	CreatedAt           string `json:"created_at"`
	CreatedBy           string `json:"created_by"`
}

type CheckEvidenceBuckets struct {
	AgentSelfReport   []CheckRequirementEvidence `json:"agent_self_report"`
	MechanicalCheck   []CheckRequirementEvidence `json:"mechanical_check"`
	IndependentReview []CheckRequirementEvidence `json:"independent_review"`
	PolicyAcceptance  []CheckRequirementEvidence `json:"policy_acceptance"`
}

type TaskCheckRequirementView struct {
	Requirement      TaskCheckRequirement  `json:"requirement"`
	State            string                `json:"state"`
	LatestCheckRunID string                `json:"latest_check_run_id,omitempty"`
	LatestResult     *CheckResult          `json:"latest_result,omitempty"`
	CurrentFreshness *CheckResultFreshness `json:"current_freshness,omitempty"`
}

type CheckNotificationReceipt struct {
	ID                     string `json:"id"`
	CheckResultID          string `json:"check_result_id"`
	FreshnessRevision      int64  `json:"freshness_revision"`
	RouteID                string `json:"route_id,omitempty"`
	Duty                   string `json:"duty"`
	RecipientAgentID       string `json:"recipient_agent_id"`
	RecipientAgentRevision int64  `json:"recipient_agent_revision"`
	AssignmentID           string `json:"assignment_id,omitempty"`
	AssignmentRevision     int64  `json:"assignment_revision,omitempty"`
	MessageID              string `json:"message_id"`
	CreatedAt              string `json:"created_at"`
}

type CheckRouteFailure struct {
	ID                     string `json:"id"`
	CheckResultID          string `json:"check_result_id"`
	FreshnessRevision      int64  `json:"freshness_revision"`
	RouteID                string `json:"route_id,omitempty"`
	Duty                   string `json:"duty"`
	RecipientAgentID       string `json:"recipient_agent_id,omitempty"`
	RecipientAgentRevision int64  `json:"recipient_agent_revision,omitempty"`
	AssignmentID           string `json:"assignment_id,omitempty"`
	AssignmentRevision     int64  `json:"assignment_revision,omitempty"`
	Code                   string `json:"code"`
	Diagnostic             string `json:"diagnostic"`
	CreatedAt              string `json:"created_at"`
}

type CheckRepairProposal struct {
	ID                          string `json:"id"`
	WorkspaceID                 string `json:"workspace_id"`
	ProjectID                   string `json:"project_id"`
	ObjectiveID                 string `json:"objective_id"`
	ObjectiveRevision           int64  `json:"objective_revision"`
	TaskID                      string `json:"task_id"`
	TaskRevision                int64  `json:"task_revision"`
	RequirementID               string `json:"requirement_id"`
	RequirementRevision         int64  `json:"requirement_revision"`
	CheckResultID               string `json:"check_result_id"`
	FreshnessRevision           int64  `json:"freshness_revision"`
	SourceRepositoryID          string `json:"source_repository_id"`
	SourceCheckoutID            string `json:"source_checkout_id"`
	SourceHeadCommit            string `json:"source_head_commit"`
	PolicyRevision              int64  `json:"policy_revision"`
	RepairLaunchProfileID       string `json:"repair_launch_profile_id"`
	RepairLaunchProfileRevision int64  `json:"repair_launch_profile_revision"`
	SourceRunID                 string `json:"source_run_id"`
	SourceAgentID               string `json:"source_agent_id"`
	SourceAgentRevision         int64  `json:"source_agent_revision"`
	SourceGrantID               string `json:"source_grant_id"`
	SourceGrantRevision         int64  `json:"source_grant_revision"`
	Rationale                   string `json:"rationale"`
	RepairTaskTitle             string `json:"repair_task_title"`
	RepairTaskDescription       string `json:"repair_task_description"`
	RepairTaskPriority          int    `json:"repair_task_priority"`
	RepairTaskBudget            Budget `json:"repair_task_budget"`
	Status                      string `json:"status"`
	Revision                    int64  `json:"revision"`
	CreatedAt                   string `json:"created_at"`
	UpdatedAt                   string `json:"updated_at"`
	CreatedBy                   string `json:"created_by"`
	UpdatedBy                   string `json:"updated_by"`
}

type CheckRunDetail struct {
	Run              CheckRun                   `json:"run"`
	Definition       CheckDefinition            `json:"definition"`
	Requirement      TaskCheckRequirement       `json:"requirement"`
	Job              CheckJob                   `json:"job"`
	LaunchReceipt    *CheckLaunchReceipt        `json:"launch_receipt,omitempty"`
	Result           *CheckResult               `json:"result,omitempty"`
	FreshnessHistory []CheckResultFreshness     `json:"freshness_history"`
	CurrentFreshness *CheckResultFreshness      `json:"current_freshness,omitempty"`
	Artifacts        []CheckArtifact            `json:"artifacts"`
	Evidence         CheckEvidenceBuckets       `json:"evidence"`
	Notifications    []CheckNotificationReceipt `json:"notifications"`
	RouteFailures    []CheckRouteFailure        `json:"route_failures"`
	RepairProposal   *CheckRepairProposal       `json:"repair_proposal,omitempty"`
	RequirementState string                     `json:"requirement_state"`
}

type CheckRunListItem struct {
	Run              CheckRun              `json:"run"`
	Outcome          string                `json:"outcome,omitempty"`
	CurrentFreshness *CheckResultFreshness `json:"current_freshness,omitempty"`
	RequirementState string                `json:"requirement_state"`
}

type CheckWatchReceipt struct {
	ID                   string   `json:"id"`
	WorkspaceID          string   `json:"workspace_id"`
	ProjectID            string   `json:"project_id"`
	FromEventSequence    int64    `json:"from_event_sequence"`
	ThroughEventSequence int64    `json:"through_event_sequence"`
	CutoffEventSequence  int64    `json:"cutoff_event_sequence"`
	CaughtUp             bool     `json:"caught_up"`
	Degraded             bool     `json:"degraded"`
	UnknownEventType     string   `json:"unknown_event_type,omitempty"`
	UnknownEventSequence int64    `json:"unknown_event_sequence,omitempty"`
	ExaminedResultIDs    []string `json:"examined_result_ids"`
	FreshnessAppended    int      `json:"freshness_appended"`
	NotificationsCreated int      `json:"notifications_created"`
	RouteFailuresCreated int      `json:"route_failures_created"`
	RepairsMarkedStale   int      `json:"repairs_marked_stale"`
	NextCursor           string   `json:"next_cursor,omitempty"`
	ContentSHA256        string   `json:"content_sha256"`
	CreatedAt            string   `json:"created_at"`
	CreatedBy            string   `json:"created_by"`
}

type CheckRepairDecision struct {
	ID               string `json:"id"`
	RepairProposalID string `json:"repair_proposal_id"`
	Decision         string `json:"decision"`
	ProposalRevision int64  `json:"proposal_revision"`
	Note             string `json:"note,omitempty"`
	CreatedAt        string `json:"created_at"`
	CreatedBy        string `json:"created_by"`
}

type CheckRepairEffect struct {
	ID                 string `json:"id"`
	RepairProposalID   string `json:"repair_proposal_id"`
	RepairTaskID       string `json:"repair_task_id"`
	SchedulingIntentID string `json:"scheduling_intent_id"`
	CreatedAt          string `json:"created_at"`
}

type CheckRepairDetail struct {
	Proposal CheckRepairProposal  `json:"proposal"`
	Result   CheckResult          `json:"result"`
	Decision *CheckRepairDecision `json:"decision,omitempty"`
	Effect   *CheckRepairEffect   `json:"effect,omitempty"`
}

type CheckCapturedLog struct {
	Kind          string `json:"kind"`
	Content       string `json:"content"`
	CapturedBytes int64  `json:"captured_bytes"`
	OmittedBytes  int64  `json:"omitted_bytes"`
	Truncated     bool   `json:"truncated"`
	ContentSHA256 string `json:"content_sha256"`
}

type CheckRunLogs struct {
	CheckRunID string            `json:"check_run_id"`
	Stdout     *CheckCapturedLog `json:"stdout,omitempty"`
	Stderr     *CheckCapturedLog `json:"stderr,omitempty"`
	Diagnostic *CheckCapturedLog `json:"diagnostic,omitempty"`
}
