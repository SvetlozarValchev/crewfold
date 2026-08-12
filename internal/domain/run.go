package domain

const (
	RunRequested   = "requested"
	RunStarting    = "starting"
	RunActive      = "active"
	RunBlocked     = "blocked"
	RunStopping    = "stopping"
	RunStopped     = "stopped"
	RunLost        = "lost"
	RunReview      = "review"
	RunCompleted   = "completed"
	RunStartFailed = "start_failed"
	RunFailed      = "failed"

	ObservationProgress   = "progress"
	ObservationBlocked    = "blocked"
	ObservationCompletion = "completion"
)

type RunPlacement struct {
	TaskID       string   `json:"task_id"`
	AgentID      string   `json:"agent_id"`
	CheckoutID   string   `json:"checkout_id"`
	CheckoutPath string   `json:"checkout_path"`
	WriteMode    string   `json:"write_mode"`
	Runtime      string   `json:"runtime"`
	Provider     string   `json:"provider"`
	Reasons      []string `json:"reasons"`
}

type AcceptanceRule struct {
	RequiredEvidence []string `json:"required_evidence"`
}

type FakeStep struct {
	Kind          string   `json:"kind"`
	Message       string   `json:"message,omitempty"`
	Evidence      []string `json:"evidence,omitempty"`
	Handoff       string   `json:"handoff,omitempty"`
	WaitForResume bool     `json:"wait_for_resume,omitempty"`
}

type FakeScenario struct {
	Schema       string         `json:"schema"`
	Name         string         `json:"name"`
	StartFailure string         `json:"start_failure,omitempty"`
	Acceptance   AcceptanceRule `json:"acceptance"`
	Steps        []FakeStep     `json:"steps"`
	Process      FixtureProcess `json:"process,omitempty"`
}

// FixtureProcess describes deterministic operating-system behavior for the
// provider-free direct-runtime fixture. The fake adapters ignore these controls.
type FixtureProcess struct {
	ExitCode          int  `json:"exit_code,omitempty"`
	StepDelayMillis   int  `json:"step_delay_millis,omitempty"`
	TimeoutMillis     int  `json:"timeout_millis,omitempty"`
	StdoutNoiseBytes  int  `json:"stdout_noise_bytes,omitempty"`
	StderrNoiseBytes  int  `json:"stderr_noise_bytes,omitempty"`
	HangAfterSteps    bool `json:"hang_after_steps,omitempty"`
	IgnoreTermination bool `json:"ignore_termination,omitempty"`
}

type Run struct {
	ID              string       `json:"id"`
	WorkspaceID     string       `json:"workspace_id"`
	ProjectID       string       `json:"project_id"`
	TaskID          string       `json:"task_id"`
	AgentID         string       `json:"agent_id"`
	CheckoutID      string       `json:"checkout_id"`
	Runtime         string       `json:"runtime"`
	Provider        string       `json:"provider"`
	ScenarioName    string       `json:"scenario_name"`
	Status          string       `json:"status"`
	StepCursor      int          `json:"step_cursor"`
	RuntimeHandle   string       `json:"runtime_handle,omitempty"`
	ProviderHandle  string       `json:"provider_handle,omitempty"`
	BlockedQuestion string       `json:"blocked_question,omitempty"`
	ResultSummary   string       `json:"result_summary,omitempty"`
	FailureCode     string       `json:"failure_code,omitempty"`
	FailureMessage  string       `json:"failure_message,omitempty"`
	StopGraceMillis int64        `json:"stop_grace_millis,omitempty"`
	StopForced      bool         `json:"stop_forced,omitempty"`
	Revision        int64        `json:"revision"`
	CreatedAt       string       `json:"created_at"`
	UpdatedAt       string       `json:"updated_at"`
	StartedAt       string       `json:"started_at,omitempty"`
	FinishedAt      string       `json:"finished_at,omitempty"`
	CreatedBy       string       `json:"created_by"`
	UpdatedBy       string       `json:"updated_by"`
	Placement       RunPlacement `json:"placement"`
}

type RunTimelineEntry struct {
	Sequence   int64    `json:"sequence"`
	RunID      string   `json:"run_id"`
	Kind       string   `json:"kind"`
	Message    string   `json:"message,omitempty"`
	Evidence   []string `json:"evidence"`
	RecordedAt string   `json:"recorded_at"`
}

type Handoff struct {
	ID        string   `json:"id"`
	RunID     string   `json:"run_id"`
	TaskID    string   `json:"task_id"`
	Summary   string   `json:"summary"`
	Evidence  []string `json:"evidence"`
	CreatedAt string   `json:"created_at"`
	CreatedBy string   `json:"created_by"`
}

type RunDetail struct {
	Run      Run                `json:"run"`
	Task     Task               `json:"task"`
	Agent    AgentDefinition    `json:"agent"`
	Checkout Checkout           `json:"checkout"`
	Timeline []RunTimelineEntry `json:"timeline"`
	Handoff  *Handoff           `json:"handoff,omitempty"`
}

type TaskTimeline struct {
	Task    Task               `json:"task"`
	Runs    []Run              `json:"runs"`
	Entries []RunTimelineEntry `json:"entries"`
}

type RunObservation struct {
	Kind     string
	Message  string
	Evidence []string
	Handoff  string
	Pause    bool
}

type CapturedLog struct {
	Text          string `json:"text"`
	CapturedBytes int64  `json:"captured_bytes"`
	OmittedBytes  int64  `json:"omitted_bytes"`
	Truncated     bool   `json:"truncated"`
}

type RunLogs struct {
	RunID  string      `json:"run_id"`
	State  string      `json:"state"`
	Stdout CapturedLog `json:"stdout"`
	Stderr CapturedLog `json:"stderr"`
}
