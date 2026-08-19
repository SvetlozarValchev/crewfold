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

	ObservationProgress          = "progress"
	ObservationBlocked           = "blocked"
	ObservationCompletion        = "completion"
	ObservationExecutiveResponse = "executive_response"
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
	Schema        string               `json:"schema"`
	Name          string               `json:"name"`
	StartFailure  string               `json:"start_failure,omitempty"`
	Acceptance    AcceptanceRule       `json:"acceptance"`
	Steps         []FakeStep           `json:"steps"`
	Process       FixtureProcess       `json:"process,omitempty"`
	Mailbox       FixtureMailbox       `json:"mailbox,omitempty"`
	Knowledge     FixtureKnowledge     `json:"knowledge,omitempty"`
	Contradiction FixtureContradiction `json:"contradiction,omitempty"`
	ContextDelta  FixtureContextDelta  `json:"context_delta,omitempty"`
	Management    FixtureManagement    `json:"management,omitempty"`
	CheckWatch    FixtureCheckWatch    `json:"check_watch,omitempty"`
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
	CrossRunProbe     bool `json:"cross_run_probe,omitempty"`
	DuplicateReport   bool `json:"duplicate_report,omitempty"`
	PublishArtifact   bool `json:"publish_artifact,omitempty"`
}

type FixtureMailboxMessage struct {
	RecipientAgent string `json:"recipient_agent,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
	Kind           string `json:"kind"`
	Subject        string `json:"subject,omitempty"`
	Body           string `json:"body"`
}

type FixtureMailbox struct {
	Send                    *FixtureMailboxMessage `json:"send,omitempty"`
	WaitForKind             string                 `json:"wait_for_kind,omitempty"`
	Reply                   *FixtureMailboxMessage `json:"reply,omitempty"`
	AcknowledgeReceived     bool                   `json:"acknowledge_received,omitempty"`
	RequireInboxSummary     bool                   `json:"require_inbox_summary,omitempty"`
	DeniedArtifactProbe     bool                   `json:"denied_artifact_probe,omitempty"`
	WaitTimeoutMillis       int                    `json:"wait_timeout_millis,omitempty"`
	DeniedRecipientProbe    string                 `json:"denied_recipient_probe,omitempty"`
	OversizedRecipientProbe string                 `json:"oversized_recipient_probe,omitempty"`
}

// FixtureKnowledge lets the provider-free scoped-MCP fixture exercise the same
// proposal and governance-denial boundary available to real provider runs. The
// fixture deliberately cannot choose its actor, run, project, source,
// applicability scope, or expiry.
type FixtureKnowledge struct {
	Proposal                *FixtureKnowledgeProposal `json:"proposal,omitempty"`
	ProbeReservedAcceptance bool                      `json:"probe_reserved_acceptance,omitempty"`
}

type FixtureKnowledgeProposal struct {
	Type               string `json:"type"`
	Title              string `json:"title"`
	Body               string `json:"body"`
	Confidence         string `json:"confidence"`
	VerificationStatus string `json:"verification_status"`
	FreshnessPolicy    string `json:"freshness_policy"`
}

// FixtureContradiction exercises authenticated reporting and the immutable
// governance boundary without allowing a fixture to choose its actor or scope.
type FixtureContradiction struct {
	Report         *FixtureContradictionReport `json:"report,omitempty"`
	ReportReceived bool                        `json:"report_received,omitempty"`
	ConfirmDenied  bool                        `json:"confirm_denied,omitempty"`
}

type FixtureContradictionReport struct {
	LeftRevision  string `json:"left_revision"`
	RightRevision string `json:"right_revision"`
	Reason        string `json:"reason"`
}

// FixtureContextDelta lets the provider-free scoped-MCP fixture prove live
// delivery and exact-run acknowledgement without a model, terminal prompt, or
// network call. It cannot trigger owner refresh or select another run's scope.
type FixtureContextDelta struct {
	Expectations           []FixtureContextDeltaExpectation `json:"expectations,omitempty"`
	InitialDelayMillis     int                              `json:"initial_delay_millis,omitempty"`
	WaitTimeoutMillis      int                              `json:"wait_timeout_millis,omitempty"`
	DuplicateAcknowledge   bool                             `json:"duplicate_acknowledge,omitempty"`
	ExpectNoPending        bool                             `json:"expect_no_pending,omitempty"`
	DeniedDeltaID          string                           `json:"denied_delta_id,omitempty"`
	DeniedExpectedSequence int64                            `json:"denied_expected_sequence,omitempty"`
}

// FixtureContextDeltaExpectation describes only assertions over a delta the
// owner has already built. Empty entity assertions are ignored.
type FixtureContextDeltaExpectation struct {
	RequiredKinds         []string `json:"required_kinds"`
	MessagePreview        string   `json:"message_preview,omitempty"`
	ParticipantThreadID   string   `json:"participant_thread_id,omitempty"`
	KnowledgeRevisionIDs  []string `json:"knowledge_revision_ids,omitempty"`
	WithdrawalRevisionIDs []string `json:"withdrawal_revision_ids,omitempty"`
	ContradictionID       string   `json:"contradiction_id,omitempty"`
	DependentTaskID       string   `json:"dependent_task_id,omitempty"`
}

// FixtureManagement exercises current-packet proposal authority and its revocation
// boundary without allowing the provider fixture to choose trusted run scope.
type FixtureManagement struct {
	Proposals                  []FixtureManagerProposal `json:"proposals,omitempty"`
	ProbeReservedAcceptance    bool                     `json:"probe_reserved_acceptance,omitempty"`
	ExpectToolsDenied          bool                     `json:"expect_tools_denied,omitempty"`
	ProbeRevokedGrant          bool                     `json:"probe_revoked_grant,omitempty"`
	RevocationProbeDelayMillis int                      `json:"revocation_probe_delay_millis,omitempty"`
}

type FixtureManagerProposal struct {
	Kind    string                  `json:"kind"`
	Summary string                  `json:"summary"`
	Actions []ManagerProposalAction `json:"actions"`
}

// FixtureCheckWatch exercises current-packet check authority without allowing the
// provider fixture to choose trusted workspace, project, agent, grant,
// checkout, command, definition, evidence class, or recipient scope.
type FixtureCheckWatch struct {
	RunRequirementID           string `json:"run_requirement_id,omitempty"`
	ListResults                bool   `json:"list_results,omitempty"`
	InspectCheckRunID          string `json:"inspect_check_run_id,omitempty"`
	ProposeRepairResultID      string `json:"propose_repair_result_id,omitempty"`
	RepairRationale            string `json:"repair_rationale,omitempty"`
	ProbeReservedAcceptance    bool   `json:"probe_reserved_acceptance,omitempty"`
	ExpectToolsDenied          bool   `json:"expect_tools_denied,omitempty"`
	ProbeRevokedGrant          bool   `json:"probe_revoked_grant,omitempty"`
	RevocationProbeDelayMillis int    `json:"revocation_probe_delay_millis,omitempty"`
}

type Run struct {
	ID                     string       `json:"id"`
	WorkspaceID            string       `json:"workspace_id"`
	ProjectID              string       `json:"project_id"`
	TaskID                 string       `json:"task_id"`
	AssignmentID           string       `json:"assignment_id"`
	AgentID                string       `json:"agent_id"`
	CheckoutID             string       `json:"checkout_id"`
	ContextPacketID        string       `json:"context_packet_id,omitempty"`
	Runtime                string       `json:"runtime"`
	Provider               string       `json:"provider"`
	ScenarioName           string       `json:"scenario_name"`
	Status                 string       `json:"status"`
	StepCursor             int          `json:"step_cursor"`
	RuntimeHandle          string       `json:"-"`
	ProviderHandle         string       `json:"-"`
	RuntimeNodeID          string       `json:"-"`
	RuntimeNodeFingerprint string       `json:"-"`
	RuntimeOperationID     string       `json:"-"`
	BlockedQuestion        string       `json:"blocked_question,omitempty"`
	ResultSummary          string       `json:"result_summary,omitempty"`
	FailureCode            string       `json:"failure_code,omitempty"`
	FailureMessage         string       `json:"failure_message,omitempty"`
	StopGraceMillis        int64        `json:"stop_grace_millis,omitempty"`
	StopForced             bool         `json:"stop_forced,omitempty"`
	Revision               int64        `json:"revision"`
	CreatedAt              string       `json:"created_at"`
	UpdatedAt              string       `json:"updated_at"`
	StartedAt              string       `json:"started_at,omitempty"`
	FinishedAt             string       `json:"finished_at,omitempty"`
	CreatedBy              string       `json:"created_by"`
	UpdatedBy              string       `json:"updated_by"`
	Placement              RunPlacement `json:"placement"`
}

// RunSummary is the bounded collection representation used by operator views.
// Full timeline, task, agent, checkout, and handoff records remain available
// through run.show.
type RunSummary struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	ProjectID       string `json:"project_id"`
	TaskID          string `json:"task_id"`
	AgentID         string `json:"agent_id"`
	Runtime         string `json:"runtime"`
	Provider        string `json:"provider"`
	Status          string `json:"status"`
	CanAttach       bool   `json:"can_attach"`
	BlockedQuestion string `json:"blocked_question,omitempty"`
	ResultSummary   string `json:"result_summary,omitempty"`
	FailureCode     string `json:"failure_code,omitempty"`
	Revision        int64  `json:"revision"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	StartedAt       string `json:"started_at,omitempty"`
	FinishedAt      string `json:"finished_at,omitempty"`
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
	Blocker  *RunBlocker        `json:"blocker,omitempty"`
	Handoff  *Handoff           `json:"handoff,omitempty"`
}

// RunBlocker is the structured reason and requested resolution carried by the
// latest applied blocked report. It lets operator surfaces explain what is
// missing without reverse-engineering prose from the run projection.
type RunBlocker struct {
	Reason     string   `json:"reason"`
	Needs      []string `json:"needs"`
	Severity   string   `json:"severity"`
	RelatedIDs []string `json:"related_ids"`
}

type RunLossResolution struct {
	RunID         string `json:"run_id"`
	LostRevision  int64  `json:"lost_revision"`
	Resolution    string `json:"resolution"`
	Note          string `json:"note"`
	EventSequence int64  `json:"event_sequence"`
	ResolvedAt    string `json:"resolved_at"`
	ResolvedBy    string `json:"resolved_by"`
}

type TaskTimeline struct {
	Task    Task               `json:"task"`
	Runs    []Run              `json:"runs"`
	Entries []RunTimelineEntry `json:"entries"`
}

type RunObservation struct {
	Kind                 string
	Message              string
	Evidence             []string
	Handoff              string
	Pause                bool
	LogArchive           *RunLogArchive
	LogUnavailableReason string
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

// ArchivedRunLog identifies one immutable terminal stream after its content
// has been published to the node-local content-addressed artifact store.
type ArchivedRunLog struct {
	Kind          string
	ContentSHA256 string
	CapturedBytes int64
	OmittedBytes  int64
	Truncated     bool
}

// RunLogArchive is the exact stdout/stderr pair committed atomically with a
// trusted terminal run transition.
type RunLogArchive struct {
	RunID  string
	State  string
	Stdout ArchivedRunLog
	Stderr ArchivedRunLog
}
