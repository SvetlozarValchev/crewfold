package domain

const (
	ManagedServiceDefinitionActive  = "active"
	ManagedServiceDefinitionRetired = "retired"

	ManagedServiceNetworkNone     = "none"
	ManagedServiceNetworkLoopback = "loopback"

	ManagedServiceHealthProcess = "process"
	ManagedServiceHealthTCP     = "tcp"
	ManagedServiceHealthHTTP    = "http"

	ManagedServiceRestartNever     = "never"
	ManagedServiceRestartOnFailure = "on_failure"
	ManagedServiceRestartOnDaemon  = "on_daemon_restart"

	ManagedServiceRequested = "requested"
	ManagedServiceStarting  = "starting"
	ManagedServiceHealthy   = "healthy"
	ManagedServiceDegraded  = "degraded"
	ManagedServiceStopping  = "stopping"
	ManagedServiceStopped   = "stopped"
	ManagedServiceFailed    = "failed"
	ManagedServiceUnknown   = "unknown"

	ManagedServiceDesiredRunning = "running"
	ManagedServiceDesiredStopped = "stopped"

	ManagedServiceHealthPending   = "pending"
	ManagedServiceHealthHealthy   = "healthy"
	ManagedServiceHealthUnhealthy = "unhealthy"
	ManagedServiceHealthUnknown   = "unknown"

	ManagedServiceJobStart   = "start"
	ManagedServiceJobStop    = "stop"
	ManagedServiceJobRestart = "restart"
	ManagedServiceJobProbe   = "probe"

	ManagedServiceJobPending       = "pending"
	ManagedServiceJobLeased        = "leased"
	ManagedServiceJobComplete      = "complete"
	ManagedServiceJobFailedUnknown = "failed_unknown"

	ManagedServiceSourceOwner   = "owner"
	ManagedServiceSourceAgent   = "agent"
	ManagedServiceSourceRequest = "agent_request"

	ManagedServiceActionInspect  = "inspect"
	ManagedServiceActionLogs     = "logs"
	ManagedServiceActionStart    = "start"
	ManagedServiceActionStop     = "stop"
	ManagedServiceActionRestart  = "restart"
	ManagedServiceActionDelegate = "delegate"

	ManagedServiceGrantActive  = "active"
	ManagedServiceGrantRevoked = "revoked"

	ManagedServiceRequestPending  = "pending"
	ManagedServiceRequestAccepted = "accepted"
	ManagedServiceRequestRejected = "rejected"

	ManagedServiceStopSignalTerm       = "term"
	ManagedServiceCapacityLocalDevelop = "local_development"

	ManagedServiceLogStdout = "stdout"
	ManagedServiceLogStderr = "stderr"
)

// ManagedServiceEnvironmentVariable is an exact, ordered environment override.
// The daemon starts from its deliberately small service environment and applies
// only these recorded values; it never snapshots the owner's ambient process.
type ManagedServiceEnvironmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ManagedServiceHealthCheck describes one bounded local liveness/readiness
// probe. Process checks need no endpoint; TCP and HTTP checks bind an explicit
// host and port, and HTTP additionally binds a path.
type ManagedServiceHealthCheck struct {
	Type           string `json:"type"`
	Host           string `json:"host,omitempty"`
	Port           int    `json:"port,omitempty"`
	Path           string `json:"path,omitempty"`
	IntervalMillis int64  `json:"interval_millis"`
	TimeoutMillis  int64  `json:"timeout_millis"`
}

// ManagedServiceDefinition is the immutable launch contract for a local
// workstream service. A definition is configuration, not evidence that an OS
// process exists. Runtime authority lives in ManagedServiceRuntimeBinding.
type ManagedServiceDefinition struct {
	ID                    string                              `json:"id"`
	WorkspaceID           string                              `json:"workspace_id"`
	ProjectID             string                              `json:"project_id"`
	WorkstreamID          string                              `json:"workstream_id,omitempty"`
	CheckoutID            string                              `json:"checkout_id"`
	Name                  string                              `json:"name"`
	Description           string                              `json:"description"`
	Executable            string                              `json:"executable"`
	Arguments             []string                            `json:"arguments"`
	WorkingDirectory      string                              `json:"working_directory"`
	Environment           []ManagedServiceEnvironmentVariable `json:"environment"`
	Profile               string                              `json:"profile"`
	ProfileRevision       int64                               `json:"profile_revision"`
	NetworkMode           string                              `json:"network_mode"`
	Health                ManagedServiceHealthCheck           `json:"health"`
	RestartPolicy         string                              `json:"restart_policy"`
	MaximumRestarts       int                                 `json:"maximum_restarts"`
	RestartCooldownMillis int64                               `json:"restart_cooldown_millis"`
	StopSignal            string                              `json:"stop_signal"`
	StopGraceMillis       int64                               `json:"stop_grace_millis"`
	OutputByteLimit       int64                               `json:"output_byte_limit"`
	CapacityClass         string                              `json:"capacity_class"`
	ContentRevision       int64                               `json:"content_revision"`
	ContentSHA256         string                              `json:"content_sha256"`
	Status                string                              `json:"status"`
	Revision              int64                               `json:"revision"`
	CreatedAt             string                              `json:"created_at"`
	UpdatedAt             string                              `json:"updated_at"`
	CreatedBy             string                              `json:"created_by"`
	UpdatedBy             string                              `json:"updated_by"`
}

type ManagedServiceSource struct {
	Type          string `json:"type"`
	ActorID       string `json:"actor_id"`
	AgentID       string `json:"agent_id,omitempty"`
	AgentRevision int64  `json:"agent_revision,omitempty"`
	ThreadID      string `json:"thread_id,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	GrantID       string `json:"grant_id,omitempty"`
	GrantRevision int64  `json:"grant_revision,omitempty"`
}

// ManagedServiceGrant is explicit durable authority for one current agent to
// operate one exact service definition. A delegated grant can only narrow its
// parent's actions, instance ceiling, and expiry.
type ManagedServiceGrant struct {
	ID                        string   `json:"id"`
	WorkspaceID               string   `json:"workspace_id"`
	ProjectID                 string   `json:"project_id"`
	DefinitionID              string   `json:"definition_id"`
	DefinitionRevision        int64    `json:"definition_revision"`
	ManagerAgentID            string   `json:"manager_agent_id"`
	ManagerMembershipRevision int64    `json:"manager_membership_revision"`
	ParentGrantID             string   `json:"parent_grant_id,omitempty"`
	Actions                   []string `json:"actions"`
	MaximumInstances          int      `json:"maximum_instances"`
	ExpiresAt                 string   `json:"expires_at,omitempty"`
	Status                    string   `json:"status"`
	Revision                  int64    `json:"revision"`
	CreatedAt                 string   `json:"created_at"`
	UpdatedAt                 string   `json:"updated_at"`
	CreatedBy                 string   `json:"created_by"`
	UpdatedBy                 string   `json:"updated_by"`
}

// ManagedServiceRequest is an inert agent request for one exact service
// action. Only owner acceptance can apply it when no direct grant exists.
type ManagedServiceRequest struct {
	ID                      string `json:"id"`
	WorkspaceID             string `json:"workspace_id"`
	ProjectID               string `json:"project_id"`
	DefinitionID            string `json:"definition_id"`
	DefinitionRevision      int64  `json:"definition_revision"`
	AgentID                 string `json:"agent_id"`
	AgentMembershipRevision int64  `json:"agent_membership_revision"`
	ThreadID                string `json:"thread_id"`
	Summary                 string `json:"summary"`
	Status                  string `json:"status"`
	Revision                int64  `json:"revision"`
	CreatedAt               string `json:"created_at"`
	UpdatedAt               string `json:"updated_at"`
	DecidedAt               string `json:"decided_at,omitempty"`
	DecisionReason          string `json:"decision_reason,omitempty"`
}

type ManagedServiceRequestDecision struct {
	Request  ManagedServiceRequest   `json:"request"`
	Instance *ManagedServiceInstance `json:"instance,omitempty"`
}

// ManagedServiceInstance is canonical lifecycle state. PID/process-group and
// log-file handles are deliberately internal node bindings and never public.
type ManagedServiceInstance struct {
	ID                      string               `json:"id"`
	WorkspaceID             string               `json:"workspace_id"`
	ProjectID               string               `json:"project_id"`
	WorkstreamID            string               `json:"workstream_id,omitempty"`
	CheckoutID              string               `json:"checkout_id"`
	DefinitionID            string               `json:"definition_id"`
	DefinitionRevision      int64                `json:"definition_revision"`
	DefinitionContentSHA256 string               `json:"definition_content_sha256"`
	Source                  ManagedServiceSource `json:"source"`
	Status                  string               `json:"status"`
	DesiredState            string               `json:"desired_state"`
	HealthStatus            string               `json:"health_status"`
	RestartCount            int                  `json:"restart_count"`
	ExitCode                *int                 `json:"exit_code,omitempty"`
	DiagnosticCode          string               `json:"diagnostic_code,omitempty"`
	Diagnostic              string               `json:"diagnostic,omitempty"`
	Revision                int64                `json:"revision"`
	CreatedAt               string               `json:"created_at"`
	UpdatedAt               string               `json:"updated_at"`
	StartedAt               string               `json:"started_at,omitempty"`
	HealthyAt               string               `json:"healthy_at,omitempty"`
	FinishedAt              string               `json:"finished_at,omitempty"`
	RuntimeNodeID           string               `json:"-"`
	RuntimeNodeFingerprint  string               `json:"-"`
	RuntimeOperationID      string               `json:"-"`
	RuntimePID              int                  `json:"-"`
	RuntimeProcessGroupID   int                  `json:"-"`
	RuntimeStartTicks       uint64               `json:"-"`
	RuntimeStdoutPath       string               `json:"-"`
	RuntimeStderrPath       string               `json:"-"`
}

type ManagedServiceJob struct {
	ID             string `json:"id"`
	InstanceID     string `json:"instance_id"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	AvailableAt    string `json:"available_at"`
	LeaseExpiresAt string `json:"lease_expires_at,omitempty"`
	Attempts       int    `json:"attempts"`
	Diagnostic     string `json:"diagnostic,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type ManagedServiceLogArtifact struct {
	ID            string `json:"id"`
	InstanceID    string `json:"instance_id"`
	Kind          string `json:"kind"`
	ContentSHA256 string `json:"content_sha256"`
	CapturedBytes int64  `json:"captured_bytes"`
	OmittedBytes  int64  `json:"omitted_bytes"`
	Truncated     bool   `json:"truncated"`
	CreatedAt     string `json:"created_at"`
}

type ManagedServiceDetail struct {
	Definition ManagedServiceDefinition    `json:"definition"`
	Instance   ManagedServiceInstance      `json:"instance"`
	Jobs       []ManagedServiceJob         `json:"jobs"`
	Logs       []ManagedServiceLogArtifact `json:"logs"`
}

type ManagedServiceLogs struct {
	InstanceID string      `json:"instance_id"`
	State      string      `json:"state"`
	Stdout     CapturedLog `json:"stdout"`
	Stderr     CapturedLog `json:"stderr"`
}

// ManagedServiceLogArchive is a prepared immutable stdout/stderr pair. The
// files are published before the terminal database transaction and only become
// canonical when that transaction records their typed references.
type ManagedServiceLogArchive struct {
	InstanceID string         `json:"instance_id"`
	Stdout     ArchivedRunLog `json:"stdout"`
	Stderr     ArchivedRunLog `json:"stderr"`
}
