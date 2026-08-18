package store

import (
	"time"

	"crewfold/internal/domain"
)

const (
	MutationAfterBaselineCatalog                = "after_baseline_catalog"
	MutationAfterBaselineIdentity               = "after_baseline_identity"
	MutationBeforeBaselinePublish               = "before_baseline_publish"
	MutationAfterBaselinePublish                = "after_baseline_publish"
	MutationAfterProjection                     = "after_projection"
	MutationAfterEvent                          = "after_event"
	MutationAfterProposalActions                = "after_proposal_actions"
	MutationAfterProposalSubmission             = "after_proposal_submission"
	MutationAfterProposalEffects                = "after_proposal_effects"
	MutationAfterProposalDecision               = "after_proposal_decision"
	MutationAfterSchedulingAuthority            = "after_scheduling_authority"
	MutationAfterSchedulingRun                  = "after_scheduling_run"
	MutationAfterSchedulingAction               = "after_scheduling_action"
	MutationAfterSchedulingReceipt              = "after_scheduling_receipt"
	MutationAfterRetryRun                       = "after_retry_run"
	MutationAfterRetryReceipt                   = "after_retry_receipt"
	MutationAfterCheckLaunchReceipt             = "after_check_launch_receipt"
	MutationAfterCheckRuntimeBinding            = "after_check_runtime_binding"
	MutationAfterCheckResult                    = "after_check_result"
	MutationAfterCheckFreshness                 = "after_check_freshness"
	MutationAfterCheckRequestProjection         = "after_check_request_projection"
	MutationAfterCheckRequestEvent              = "after_check_request_event"
	MutationAfterCheckRequestJob                = "after_check_request_job"
	MutationAfterCheckRequestIdempotency        = "after_check_request_idempotency"
	MutationAfterCheckLaunchEvent               = "after_check_launch_event"
	MutationAfterCheckArtifact                  = "after_check_artifact"
	MutationAfterCheckEvidence                  = "after_check_evidence"
	MutationAfterCheckNotification              = "after_check_notification"
	MutationAfterCheckMessage                   = "after_check_message"
	MutationAfterCheckResultEvent               = "after_check_result_event"
	MutationAfterCheckRepairProposalProjection  = "after_check_repair_proposal_projection"
	MutationAfterCheckRepairProposalEvent       = "after_check_repair_proposal_event"
	MutationAfterCheckRepairProposalIdempotency = "after_check_repair_proposal_idempotency"
	MutationAfterCheckRepairDecision            = "after_check_repair_decision"
	MutationAfterCheckRepairTask                = "after_check_repair_task"
	MutationAfterCheckRepairIntent              = "after_check_repair_intent"
	MutationAfterCheckRepairEffect              = "after_check_repair_effect"
	MutationAfterCheckRepairEvent               = "after_check_repair_event"
	MutationAfterCheckRepairIdempotency         = "after_check_repair_idempotency"
	MutationAfterOutcomeCommitment              = "after_outcome_commitment"
	MutationAfterOutcomeCommitmentEvent         = "after_outcome_commitment_event"
	MutationAfterOutcomeCommitmentIdempotency   = "after_outcome_commitment_idempotency"
	MutationAfterOutcomeAssessment              = "after_outcome_assessment"
	MutationAfterOutcomeAssessmentChildren      = "after_outcome_assessment_children"
	MutationAfterOutcomeAssessmentEvent         = "after_outcome_assessment_event"
	MutationAfterOutcomeAssessmentIdempotency   = "after_outcome_assessment_idempotency"
	MutationAfterOutcomeGovernanceDecision      = "after_outcome_governance_decision"
	MutationAfterOutcomeGovernanceEvents        = "after_outcome_governance_events"
	MutationAfterOutcomeGovernanceIdempotency   = "after_outcome_governance_idempotency"
	MutationAfterOwnerCheckpoint                = "after_owner_checkpoint"
	MutationAfterOwnerCheckpointEvent           = "after_owner_checkpoint_event"
	MutationAfterOwnerCheckpointIdempotency     = "after_owner_checkpoint_idempotency"
	MutationAfterBriefingClaims                 = "after_briefing_claims"
	MutationAfterBriefingCursor                 = "after_briefing_cursor"
	MutationAfterBriefingRevision               = "after_briefing_revision"
)

type Workspace = domain.Workspace

type Event = domain.Event

const (
	DefaultReadPageLimit   = 50
	MaximumReadPageLimit   = 200
	MaximumEventPageLimit  = 1000
	MaximumReadCursorBytes = 256
)

type ListWorkspacesQuery struct {
	Cursor string
	Limit  int
}

type WorkspacePage struct {
	Workspaces []domain.Workspace
	NextCursor string
	HasMore    bool
	Total      int64
}

type ListProjectsQuery struct {
	WorkspaceIdentifier string
	Cursor              string
	Limit               int
}

type ProjectPage struct {
	Projects   []domain.Project
	NextCursor string
	HasMore    bool
	Total      int64
}

type ListAgentsQuery struct {
	WorkspaceIdentifier string
	Cursor              string
	Limit               int
}

type AgentPage struct {
	Agents     []domain.AgentDefinition
	NextCursor string
	HasMore    bool
	Total      int64
}

type ListObjectivesQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	Cursor              string
	Limit               int
}

type ObjectivePage struct {
	Objectives []domain.Objective
	NextCursor string
	HasMore    bool
	Total      int64
}

type ListTasksQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	ReadyOnly           bool
	Cursor              string
	Limit               int
}

type TaskPage struct {
	Tasks      []domain.TaskDetail
	NextCursor string
	HasMore    bool
	Total      int64
}

type ListRunsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	TaskID              string
	Status              string
	Cursor              string
	Limit               int
}

type RunPage struct {
	Runs                  []domain.RunSummary
	RuntimeHandleBoundIDs map[string]bool
	NextCursor            string
	HasMore               bool
	Total                 int64
}

type ListClaimsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	Status              string
	Cursor              string
	Limit               int
}

type ClaimPage struct {
	Claims     []domain.WorkClaim
	NextCursor string
	HasMore    bool
	Total      int64
}

type ListOverlapsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	Status              string
	Cursor              string
	Limit               int
}

type OverlapPage struct {
	Overlaps   []domain.WorkOverlap
	NextCursor string
	HasMore    bool
	Total      int64
}

type ListClaimDriftsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	Status              string
	Cursor              string
	Limit               int
}

type DriftPage struct {
	Drifts     []domain.ClaimDrift
	NextCursor string
	HasMore    bool
	Total      int64
}

type ListMeetingsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	Status              string
	Cursor              string
	Limit               int
}

type MeetingPage struct {
	Meetings   []domain.Meeting
	NextCursor string
	HasMore    bool
	Total      int64
}

type ListEventsQuery struct {
	WorkspaceIdentifier string
	After               int64
	Cursor              string
	Limit               int
}

type EventTimelineQuery struct {
	WorkspaceIdentifier string
	EntityType          string
	EntityID            string
	Cursor              string
	Limit               int
}

type EventPage struct {
	WorkspaceID string
	HighWater   int64
	Events      []domain.Event
	NextCursor  string
	HasMore     bool
	Total       int64
}

type SearchKnowledgeQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	TaskIdentifier      string
	Type                string
	Query               string
	Limit               int
}

type RebuildKnowledgeIndexCommand struct {
	WorkspaceIdentifier string
	IdempotencyKey      string
	CorrelationID       string
}

type KnowledgeIndexRebuildResult struct {
	Index domain.KnowledgeIndexStatus `json:"index"`
}

type WorkspaceInitResult struct {
	Workspace     Workspace `json:"workspace"`
	EventID       string    `json:"event_id"`
	EventSequence int64     `json:"event_sequence"`
}

type DatabaseHealth struct {
	Status         string `json:"status"`
	SQLiteVersion  string `json:"sqlite_version"`
	SchemaVersion  int    `json:"schema_version"`
	SourceSHA256   string `json:"source_sha256"`
	CatalogSHA256  string `json:"catalog_sha256"`
	JournalMode    string `json:"journal_mode"`
	ForeignKeys    bool   `json:"foreign_keys"`
	IntegrityCheck string `json:"integrity_check"`
}

type BaselineIdentity struct {
	SchemaVersion int    `json:"schema_version"`
	SourceSHA256  string `json:"source_sha256"`
	CatalogSHA256 string `json:"catalog_sha256"`
}

type CanonicalVerifyOptions struct {
	Full bool
}

type CanonicalIntegrityReport struct {
	Status                   string                       `json:"status"`
	Complete                 bool                         `json:"complete"`
	Baseline                 BaselineIdentity             `json:"baseline"`
	PhysicalIntegrity        string                       `json:"physical_integrity"`
	ForeignKeyViolationCount int64                        `json:"foreign_key_violation_count"`
	ForeignKeyViolations     []ForeignKeyViolation        `json:"foreign_key_violations"`
	ApplicationTableCount    int                          `json:"application_table_count"`
	EventHighWater           int64                        `json:"event_high_water"`
	LogicalSHA256            string                       `json:"logical_sha256"`
	Quiescence               QuiescentCut                 `json:"quiescence"`
	QuiescenceBlockers       []QuiescenceBlocker          `json:"quiescence_blockers"`
	SemanticFamilies         []SemanticIntegrityFamily    `json:"semantic_families"`
	DurableQueues            []DurableQueueIntegrity      `json:"durable_queues"`
	DerivedProjections       []DerivedProjectionIntegrity `json:"derived_projections"`
	ArtifactReferences       []ImmutableArtifactReference `json:"artifact_references"`
	Failures                 []CanonicalIntegrityIssue    `json:"failures"`
}

// DurableQueueIntegrity is the bounded, registry-derived partition proof for
// one durable external-work queue. Every row must be in exactly one of the
// declared open or terminal state sets.
type DurableQueueIntegrity struct {
	Name           string   `json:"name"`
	Table          string   `json:"table"`
	RowCount       int64    `json:"row_count"`
	OpenCount      int64    `json:"open_count"`
	TerminalCount  int64    `json:"terminal_count"`
	Status         string   `json:"status"`
	ViolationCount int64    `json:"violation_count"`
	Samples        []string `json:"samples"`
}

type DerivedProjectionIntegrity struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Diagnosis string `json:"diagnosis,omitempty"`
}

type ImmutableArtifactReference struct {
	ContentSHA256 string `json:"content_sha256"`
	ByteSize      int64  `json:"byte_size"`
	Kind          string `json:"kind"`
}

type CanonicalIntegrityIssue struct {
	Check  string `json:"check"`
	Detail string `json:"detail"`
}

type ForeignKeyViolation struct {
	Table       string `json:"table"`
	RowID       *int64 `json:"row_id,omitempty"`
	ParentTable string `json:"parent_table"`
	ForeignKey  int64  `json:"foreign_key"`
}

type SemanticIntegrityFamily struct {
	Name           string                       `json:"name"`
	Tables         []string                     `json:"tables"`
	RowsStreamed   int64                        `json:"rows_streamed"`
	LogicalSHA256  string                       `json:"logical_sha256"`
	Status         string                       `json:"status"`
	ViolationCount int64                        `json:"violation_count"`
	Violations     []SemanticIntegrityViolation `json:"violations"`
	Detail         string                       `json:"detail,omitempty"`
}

type SemanticIntegrityViolation struct {
	Check   string   `json:"check"`
	Count   int64    `json:"count"`
	Samples []string `json:"samples"`
}

type QuiescentCut struct {
	Quiescent      bool             `json:"quiescent"`
	EventHighWater int64            `json:"event_high_water"`
	Counts         QuiescenceCounts `json:"counts"`
	ProofSHA256    string           `json:"proof_sha256"`
}

type QuiescenceCounts struct {
	NonterminalRuns             int64 `json:"nonterminal_runs"`
	UnsettledRunJobs            int64 `json:"unsettled_run_jobs"`
	RuntimeBindings             int64 `json:"runtime_bindings"`
	UnfinishedCheckRuns         int64 `json:"unfinished_check_runs"`
	UnsettledCheckJobs          int64 `json:"unsettled_check_jobs"`
	OpenWakeJobs                int64 `json:"open_wake_jobs"`
	OpenSchedulingIntents       int64 `json:"open_scheduling_intents"`
	OpenSupervisorActions       int64 `json:"open_supervisor_actions"`
	OpenApprovals               int64 `json:"open_approvals"`
	OpenOwnerManagerReviews     int64 `json:"open_owner_manager_reviews"`
	OpenOwnerExecutiveExchanges int64 `json:"open_owner_executive_exchanges"`
}

type QuiescenceBlocker struct {
	Kind     string `json:"kind"`
	EntityID string `json:"entity_id"`
}

type SnapshotMetadata struct {
	Path           string           `json:"path"`
	ByteSize       int64            `json:"byte_size"`
	SHA256         string           `json:"sha256"`
	Baseline       BaselineIdentity `json:"baseline"`
	EventHighWater int64            `json:"event_high_water"`
}

type CreateDeliverableCommitmentCommand struct {
	WorkspaceIdentifier string
	TaskID              string
	Key                 string
	Title               string
	Description         string
	AcceptanceCriteria  []string
	IdempotencyKey      string
	CorrelationID       string
}

type ListDeliverableCommitmentsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	ObjectiveID         string
	TaskID              string
	Limit               int
}

type DeliverableCommitmentMutationResult struct {
	Commitment    domain.DeliverableCommitment `json:"commitment"`
	EventSequence int64                        `json:"event_sequence"`
}

type ProposeOutcomeAssessmentCommand struct {
	WorkspaceIdentifier    string
	TaskID                 string
	CommitmentID           string
	SupersedesAssessmentID string
	Input                  domain.OutcomeAssessmentInput
	IdempotencyKey         string
	CorrelationID          string
}

type ListOutcomeAssessmentsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	ObjectiveID         string
	TaskID              string
	CommitmentID        string
	ReviewState         string
	Conclusion          string
	Limit               int
}

type DecideOutcomeAssessmentCommand struct {
	WorkspaceIdentifier   string
	AssessmentID          string
	ExpectedStateRevision int64
	DecisionNote          string
	IdempotencyKey        string
	CorrelationID         string
}

type OutcomeAssessmentMutationResult struct {
	Detail        domain.OutcomeAssessmentDetail `json:"detail"`
	EventSequence int64                          `json:"event_sequence"`
}

type CreateOwnerCheckpointCommand struct {
	WorkspaceIdentifier string
	ScopeType           string
	ScopeIdentifier     string
	IdempotencyKey      string
	CorrelationID       string
}

type ListOwnerCheckpointsQuery struct {
	WorkspaceIdentifier string
	ScopeType           string
	ScopeIdentifier     string
	Limit               int
}

type OwnerCheckpointMutationResult struct {
	Checkpoint    domain.OwnerCheckpoint `json:"checkpoint"`
	EventSequence int64                  `json:"event_sequence"`
}

type ShowManagementBriefingQuery struct {
	WorkspaceIdentifier string
	ScopeType           string
	ScopeIdentifier     string
	SinceCheckpointID   string
}

type ExplainManagementBriefingClaimQuery struct {
	WorkspaceIdentifier string
	BriefingID          string
	ClaimID             string
}

type InitWorkspaceCommand struct {
	Name           string
	IdempotencyKey string
	CorrelationID  string
}

type RegisterProjectCommand struct {
	WorkspaceIdentifier string
	Name                string
	WriteMode           string
	IdempotencyKey      string
	CorrelationID       string
	Observation         domain.CheckoutObservation
}

type AddCheckoutCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	WriteMode           string
	IdempotencyKey      string
	CorrelationID       string
	Observation         domain.CheckoutObservation
}

type ProjectRegistrationResult struct {
	Project       domain.Project    `json:"project"`
	Repository    domain.Repository `json:"repository"`
	Checkout      domain.Checkout   `json:"checkout"`
	EventSequence int64             `json:"event_sequence"`
}

type CheckoutRegistrationResult struct {
	Repository        domain.Repository `json:"repository"`
	Checkout          domain.Checkout   `json:"checkout"`
	RepositoryCreated bool              `json:"repository_created"`
	EventSequence     int64             `json:"event_sequence"`
}

type ProjectInspection struct {
	Project      domain.Project      `json:"project"`
	Repositories []domain.Repository `json:"repositories"`
	Checkouts    []domain.Checkout   `json:"checkouts"`
}

type CreateAgentCommand struct {
	WorkspaceIdentifier string
	Name                string
	Role                string
	Provider            string
	Runtime             string
	MaxConcurrency      int
	IdempotencyKey      string
	CorrelationID       string
}

type UpdateAgentCommand struct {
	WorkspaceIdentifier string
	AgentIdentifier     string
	Role                *string
	Provider            *string
	Runtime             *string
	Enabled             *bool
	MaxConcurrency      *int
	ExpectedRevision    int64
	IdempotencyKey      string
	CorrelationID       string
}

type AttachDomainAgentCommand struct {
	WorkspaceIdentifier   string
	ProjectIdentifier     string
	AgentIdentifier       string
	ParentAgentIdentifier string
	WorkstreamIdentifier  string
	OperatingCharter      string
	DelegationPolicy      string
	PreferredEntry        bool
	IdempotencyKey        string
	CorrelationID         string
}

type CreateDomainAgentCommand struct {
	WorkspaceIdentifier   string
	ProjectIdentifier     string
	Name                  string
	Role                  string
	Provider              string
	Runtime               string
	MaxConcurrency        int
	ParentAgentIdentifier string
	WorkstreamIdentifier  string
	OperatingCharter      string
	DelegationPolicy      string
	PreferredEntry        bool
	IdempotencyKey        string
	CorrelationID         string
}

type UpdateDomainAgentCommand struct {
	WorkspaceIdentifier   string
	ProjectIdentifier     string
	AgentIdentifier       string
	ParentAgentIdentifier *string
	WorkstreamIdentifier  *string
	OperatingCharter      *string
	DelegationPolicy      *string
	PreferredEntry        *bool
	Status                *string
	ExpectedRevision      int64
	IdempotencyKey        string
	CorrelationID         string
}

type CreateDomainAgentStaffingGrantCommand struct {
	WorkspaceIdentifier        string
	ProjectIdentifier          string
	ManagerAgentIdentifier     string
	ExpectedMembershipRevision int64
	Profiles                   []domain.DomainAgentStaffingProfile
	TaskClasses                []string
	MaxDescendants             int
	MaxConcurrency             int
	Budget                     domain.Budget
	ExpiresAt                  string
	IdempotencyKey             string
	CorrelationID              string
}

type RevokeDomainAgentStaffingGrantCommand struct {
	WorkspaceIdentifier string
	GrantID             string
	ExpectedRevision    int64
	IdempotencyKey      string
	CorrelationID       string
}

type CreateDomainAgentChildCommand struct {
	ThreadID         string
	GrantID          string
	Name             string
	Role             string
	Provider         string
	Runtime          string
	MaxConcurrency   int
	Workstream       string
	OperatingCharter string
	DelegationPolicy string
	TaskClass        string
	Budget           domain.Budget
	IdempotencyKey   string
	CorrelationID    string
}

type CreateObjectiveCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	Title               string
	Budget              domain.Budget
	IdempotencyKey      string
	CorrelationID       string
}

type UpdateObjectiveCommand struct {
	WorkspaceIdentifier string
	ObjectiveID         string
	Title               *string
	Status              *string
	Budget              *domain.Budget
	ExpectedRevision    int64
	IdempotencyKey      string
	CorrelationID       string
}

type CreateTaskCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	ObjectiveID         string
	Title               string
	Description         string
	Priority            int
	Budget              domain.Budget
	IdempotencyKey      string
	CorrelationID       string
}

type AddTaskDependencyCommand struct {
	WorkspaceIdentifier string
	TaskID              string
	DependsOnTaskID     string
	ExpectedRevision    int64
	IdempotencyKey      string
	CorrelationID       string
}

type UpdateTaskCommand struct {
	WorkspaceIdentifier string
	TaskID              string
	Title               *string
	Description         *string
	Priority            *int
	Budget              *domain.Budget
	ExpectedRevision    int64
	IdempotencyKey      string
	CorrelationID       string
}

type AssignTaskCommand struct {
	WorkspaceIdentifier string
	TaskID              string
	AgentIdentifier     string
	LeaseSeconds        int64
	ExpectedRevision    int64
	IdempotencyKey      string
	CorrelationID       string
}

type TransitionTaskCommand struct {
	WorkspaceIdentifier string
	TaskID              string
	Action              string
	Reason              string
	ExpectedRevision    int64
	IdempotencyKey      string
	CorrelationID       string
}

type CreateRunCommand struct {
	WorkspaceIdentifier             string
	TaskID                          string
	CheckoutIdentifier              string
	ContextPacketID                 string
	Runtime                         string
	Provider                        string
	Scenario                        domain.FakeScenario
	ExpectedTaskRevision            int64
	CapabilityTTL                   time.Duration
	CheckWatchGrantID               string
	ExpectedCheckWatchGrantRevision int64
	IdempotencyKey                  string
	CorrelationID                   string
	reviewedPriorRunID              string
	expectedReviewedRunRevision     int64
}

type RetryReviewedRunCommand struct {
	WorkspaceIdentifier  string
	PriorRunID           string
	ExpectedRunRevision  int64
	ExpectedTaskRevision int64
	Scenario             domain.FakeScenario
	IdempotencyKey       string
	CorrelationID        string
}

type ResumeRunCommand struct {
	WorkspaceIdentifier string
	RunID               string
	ExpectedRevision    int64
	IdempotencyKey      string
	CorrelationID       string
}

type StopRunCommand struct {
	WorkspaceIdentifier string
	RunID               string
	ExpectedRevision    int64
	GracePeriodMillis   int64
	IdempotencyKey      string
	CorrelationID       string
}

type RunMutationResult struct {
	Detail        domain.RunDetail `json:"detail"`
	EventSequence int64            `json:"event_sequence"`
}

type BuildContextCommand struct {
	WorkspaceIdentifier  string
	TaskID               string
	AgentIdentifier      string
	CheckoutIdentifier   string
	KnowledgeRevisionIDs []string
	ExpectedTaskRevision int64
	IdempotencyKey       string
	CorrelationID        string
}

type RefreshContextCommand struct {
	WorkspaceIdentifier string
	RunID               string
	IdempotencyKey      string
	CorrelationID       string
}

type ListContextDeltasQuery struct {
	WorkspaceIdentifier string
	RunID               string
	AfterSequence       int64
	Limit               int
}

type AcknowledgeContextDeltaCommand struct {
	RunID            string
	DeltaID          string
	ExpectedSequence int64
	IdempotencyKey   string
}

type CreateRunReportCommand struct {
	RunID          string
	Kind           string
	Message        string
	Evidence       []string
	Handoff        string
	Payload        any
	IdempotencyKey string
}

type PublishRunArtifactCommand struct {
	RunID          string
	Name           string
	MediaType      string
	Content        string
	IdempotencyKey string
}

type SendMessageCommand struct {
	WorkspaceIdentifier  string
	SenderRunID          string
	SenderDomainThreadID string
	RecipientAgent       string
	ThreadID             string
	ProjectIdentifier    string
	TaskID               string
	Kind                 string
	Subject              string
	Body                 string
	ArtifactIDs          []string
	ReplyToMessageID     string
	IdempotencyKey       string
	CorrelationID        string
}

type CreateParticipantThreadCommand struct {
	WorkspaceIdentifier string
	Subject             string
	Participants        []domain.ParticipantBindingInput
	IdempotencyKey      string
	CorrelationID       string
}

type InviteThreadParticipantCommand struct {
	WorkspaceIdentifier         string
	ThreadID                    string
	Participant                 domain.ParticipantBindingInput
	ExpectedParticipantRevision int64
	IdempotencyKey              string
	CorrelationID               string
}

type ParticipantThreadMutationResult struct {
	Collaboration domain.ParticipantThread `json:"collaboration"`
	EventSequence int64                    `json:"event_sequence"`
}

type AddClaimCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	TaskID              string
	CheckoutIdentifier  string
	Kind                string
	Target              string
	Mode                string
	ConflictPolicy      string
	LeaseDuration       time.Duration
	IdempotencyKey      string
	CorrelationID       string
}

type ReleaseClaimCommand struct {
	WorkspaceIdentifier string
	ClaimID             string
	ExpectedRevision    int64
	IdempotencyKey      string
	CorrelationID       string
}

type RecordCheckoutClaimScanCommand struct {
	CheckoutID    string
	WatcherID     string
	HeadCommit    string
	DirtyPaths    []string
	CorrelationID string
}

type ClaimMutationResult struct {
	Claim         domain.WorkClaim     `json:"claim"`
	Overlaps      []domain.WorkOverlap `json:"overlaps"`
	Warnings      []string             `json:"warnings"`
	EventSequence int64                `json:"event_sequence"`
	Replayed      bool                 `json:"-"`
}

type CreateMeetingCommand struct {
	WorkspaceIdentifier string
	OverlapID           string
	ParticipantAgents   []string
	FacilitatorAgent    string
	Policy              string
	ReviewerAgent       string
	AllowedActions      []string
	Timeout             time.Duration
	IdempotencyKey      string
	CorrelationID       string
}

type RunMeetingCommand struct {
	WorkspaceIdentifier string
	MeetingID           string
	ExpectedRevision    int64
	Fixture             domain.MeetingRunFixture
	IdempotencyKey      string
	CorrelationID       string
}

type AcceptMeetingCommand struct {
	WorkspaceIdentifier string
	MeetingID           string
	ExpectedRevision    int64
	DecisionNote        string
	IdempotencyKey      string
	CorrelationID       string
}

type TakeoverMeetingCommand struct {
	WorkspaceIdentifier string
	MeetingID           string
	ExpectedRevision    int64
	Proposal            domain.MeetingProposalInput
	DecisionNote        string
	IdempotencyKey      string
	CorrelationID       string
}

type MeetingMutationResult struct {
	Detail        domain.MeetingDetail `json:"detail"`
	EventSequence int64                `json:"event_sequence"`
}

type ProposeKnowledgeCommand struct {
	WorkspaceIdentifier  string
	ProjectIdentifier    string
	TaskScopeID          string
	Type                 string
	Title                string
	Body                 string
	Confidence           string
	VerificationStatus   string
	FreshnessPolicy      string
	FreshUntil           string
	Sources              []domain.KnowledgeSourceInput
	SupersedesRevisionID string
	Actor                domain.KnowledgeActor
	IdempotencyKey       string
	CorrelationID        string
}

type AcceptKnowledgeCommand struct {
	WorkspaceIdentifier   string
	RevisionID            string
	ExpectedStateRevision int64
	DecisionNote          string
	Actor                 domain.KnowledgeActor
	IdempotencyKey        string
	CorrelationID         string
}

type RejectKnowledgeCommand struct {
	WorkspaceIdentifier   string
	RevisionID            string
	ExpectedStateRevision int64
	DecisionNote          string
	Actor                 domain.KnowledgeActor
	IdempotencyKey        string
	CorrelationID         string
}

type MarkKnowledgeStaleCommand struct {
	WorkspaceIdentifier   string
	RevisionID            string
	ExpectedStateRevision int64
	Reason                string
	Actor                 domain.KnowledgeActor
	IdempotencyKey        string
	CorrelationID         string
}

type ListKnowledgeQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	TaskScopeID         string
	Type                string
	ReviewStatus        string
	CurrencyStatus      string
}

type KnowledgeMutationResult struct {
	Revision       domain.KnowledgeRevision        `json:"revision"`
	AuthorityCheck *domain.KnowledgeAuthorityCheck `json:"authority_check,omitempty"`
	EventSequence  int64                           `json:"event_sequence"`
}

type ReportKnowledgeContradictionCommand struct {
	WorkspaceIdentifier string
	LeftRevisionID      string
	RightRevisionID     string
	ReportNote          string
	Actor               domain.KnowledgeActor
	IdempotencyKey      string
	CorrelationID       string
}

// ReportRunKnowledgeContradictionCommand is the authenticated-run boundary for
// contradiction reports. Workspace, project, task, and actor are deliberately
// absent: the store derives all four from RunID in the report transaction.
type ReportRunKnowledgeContradictionCommand struct {
	RunID           string
	LeftRevisionID  string
	RightRevisionID string
	ReportNote      string
	IdempotencyKey  string
	CorrelationID   string
}

type DecideKnowledgeContradictionCommand struct {
	WorkspaceIdentifier   string
	ContradictionID       string
	ExpectedStateRevision int64
	Note                  string
	Actor                 domain.KnowledgeActor
	IdempotencyKey        string
	CorrelationID         string
}

type ConfirmKnowledgeContradictionCommand = DecideKnowledgeContradictionCommand

type DismissKnowledgeContradictionCommand = DecideKnowledgeContradictionCommand

type ListKnowledgeContradictionsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	Status              string
	RevisionID          string
	Limit               int
}

type KnowledgeContradictionMutationResult struct {
	Detail         domain.KnowledgeContradictionDetail          `json:"detail"`
	AuthorityCheck *domain.KnowledgeContradictionAuthorityCheck `json:"authority_check,omitempty"`
	EventSequence  int64                                        `json:"event_sequence"`
}

type ExportKnowledgeBundleQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
}

type KnowledgeBundleExportResult struct {
	Manifest          domain.PortableKnowledgeBundleManifest `json:"manifest"`
	ManifestJSON      []byte                                 `json:"manifest_json"`
	Markdown          []byte                                 `json:"markdown"`
	AsOfEventSequence int64                                  `json:"as_of_event_sequence"`
}

type ImportKnowledgeBundleCommand struct {
	WorkspaceIdentifier   string
	ProjectIdentifier     string
	ManifestJSON          []byte
	Markdown              []byte
	ExpectedContentSHA256 string
	CreateScope           bool
	Actor                 domain.KnowledgeActor
	IdempotencyKey        string
	CorrelationID         string
}

type KnowledgeBundleImportCreated struct {
	Workspace        bool  `json:"workspace"`
	Project          bool  `json:"project"`
	TaskScopeAnchors int64 `json:"task_scope_anchors"`
}

type KnowledgeBundleImportResult struct {
	Receipt       domain.KnowledgeImportReceipt  `json:"receipt"`
	Counts        domain.PortableKnowledgeCounts `json:"counts"`
	Created       KnowledgeBundleImportCreated   `json:"created"`
	EventSequence int64                          `json:"event_sequence"`
	Replayed      bool                           `json:"replayed"`
}

type CuratorQueueQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	After               string
	Limit               int
}

type ConfigureCuratorRuleCommand struct {
	WorkspaceIdentifier string
	RuleName            string
	Enabled             bool
	ExpectedRevision    int64
	IdempotencyKey      string
	CorrelationID       string
}

type CuratorRuleMutationResult struct {
	Rule          domain.CuratorRule `json:"rule"`
	EventSequence int64              `json:"event_sequence"`
}

type ProcessCuratorCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	ApplySafe           bool
	IdempotencyKey      string
	CorrelationID       string
}

type CuratorProcessMutationResult struct {
	Process       domain.CuratorProcess `json:"process"`
	EventSequence int64                 `json:"event_sequence"`
}

type ClaimWatchTarget struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	CheckoutID  string `json:"checkout_id"`
	Path        string `json:"path"`
}

type RunWork struct {
	Run      domain.Run
	Scenario domain.FakeScenario
}

type MutationResult[T any] struct {
	Value         T     `json:"value"`
	EventSequence int64 `json:"event_sequence"`
}

type CreateManagerGrantCommand struct {
	WorkspaceIdentifier   string
	ProjectIdentifier     string
	ObjectiveID           string
	TaskID                string
	AgentIdentifier       string
	ExpectedTaskRevision  int64
	ExpectedAgentRevision int64
	ProposalKinds         []string
	LaunchProfileIDs      []string
	AllowedClaimKinds     []string
	Limits                domain.ManagerProposalLimits
	ExpiresAt             string
	IdempotencyKey        string
	CorrelationID         string
}

type RevokeManagerGrantCommand struct {
	WorkspaceIdentifier string
	ManagerGrantID      string
	ExpectedRevision    int64
	Reason              string
	IdempotencyKey      string
	CorrelationID       string
}

type ListManagerGrantsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	ObjectiveID         string
	TaskID              string
	AgentIdentifier     string
	Status              string
	Limit               int
}

type CreateLaunchProfileCommand struct {
	WorkspaceIdentifier    string
	ProjectIdentifier      string
	AgentIdentifier        string
	ExpectedAgentRevision  int64
	Purpose                string
	Runtime                string
	Provider               string
	CheckoutIdentifier     string
	Scenario               domain.FakeScenario
	AssignmentLeaseSeconds int64
	CapabilityTTLSeconds   int64
	ManagerGrantID         string
	IdempotencyKey         string
	CorrelationID          string
}

type RetireLaunchProfileCommand struct {
	WorkspaceIdentifier string
	LaunchProfileID     string
	ExpectedRevision    int64
	Reason              string
	IdempotencyKey      string
	CorrelationID       string
}

type ListLaunchProfilesQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	AgentIdentifier     string
	ManagerGrantID      string
	Status              string
	Limit               int
}

type InvokeManagerCommand struct {
	WorkspaceIdentifier     string
	ObjectiveID             string
	TaskID                  string
	ManagerGrantID          string
	LaunchProfileID         string
	ExpectedTaskRevision    int64
	ExpectedGrantRevision   int64
	ExpectedProfileRevision int64
	IdempotencyKey          string
	CorrelationID           string
}

type ManagerInvocationResult struct {
	ManagerGrant  domain.ManagerGrant  `json:"manager_grant"`
	LaunchProfile domain.LaunchProfile `json:"launch_profile"`
	Detail        domain.RunDetail     `json:"detail"`
	EventSequence int64                `json:"event_sequence"`
}

type SubmitManagerProposalCommand struct {
	RunID                 string
	ManagerGrantID        string
	ExpectedGrantRevision int64
	Kind                  string
	Summary               string
	AsOfEventSequence     int64
	Actions               []domain.ManagerProposalAction
	IdempotencyKey        string
	CorrelationID         string
}

type AcceptManagerProposalCommand struct {
	WorkspaceIdentifier string
	ManagerProposalID   string
	ExpectedRevision    int64
	DecisionNote        string
	IdempotencyKey      string
	CorrelationID       string
}

type RejectManagerProposalCommand = AcceptManagerProposalCommand

type ListManagerProposalsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	ObjectiveID         string
	SourceRunID         string
	ManagerGrantID      string
	Kind                string
	Status              string
	Limit               int
}

type ManagerProposalMutationResult struct {
	Proposal      domain.ManagerProposal         `json:"proposal"`
	Effects       []domain.ManagerProposalEffect `json:"effects"`
	EventSequence int64                          `json:"event_sequence"`
}

type ConfigureSupervisorPolicyCommand struct {
	WorkspaceIdentifier  string
	Enabled              bool
	Limits               domain.SupervisorLimits
	AutoSchedule         bool
	AutoRetryLimit       int
	RetryCooldownSeconds int64
	ExpectedRevision     int64
	IdempotencyKey       string
	CorrelationID        string
}

type RunSupervisorCommand struct {
	WorkspaceIdentifier string
	Limit               int
	IdempotencyKey      string
	CorrelationID       string
	// PersistNoop is set by the owner-facing command boundary so an exact
	// successful no-op is replayed forever. The daemon's recurring internal
	// sweep leaves it false to avoid an unbounded receipt stream while idle.
	PersistNoop bool
}

type SupervisorRunResult struct {
	Policy          domain.SupervisorPolicy   `json:"policy"`
	Actions         []domain.SupervisorAction `json:"actions"`
	ScheduledRunIDs []string                  `json:"scheduled_run_ids"`
	EventSequence   int64                     `json:"event_sequence"`
}

type ExplainSupervisorQuery struct {
	WorkspaceIdentifier string
	IntentID            string
	TaskID              string
	Limit               int
}

type ListSupervisorActionsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	TaskID              string
	RunID               string
	Status              string
	Condition           string
	Limit               int
}

type ListApprovalRequestsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	Status              string
	ActionID            string
	Cursor              string
	Limit               int
}

type ApprovalPage struct {
	Approvals  []domain.ApprovalRequest
	NextCursor string
	HasMore    bool
	Total      int64
}

type DecideApprovalCommand struct {
	WorkspaceIdentifier string
	ApprovalRequestID   string
	ExpectedRevision    int64
	DecisionNote        string
	IdempotencyKey      string
	CorrelationID       string
}

type ApprovalMutationResult struct {
	Approval      domain.ApprovalRequest  `json:"approval"`
	Action        domain.SupervisorAction `json:"action"`
	EventSequence int64                   `json:"event_sequence"`
}

type TaskMutationResult struct {
	Detail        domain.TaskDetail `json:"detail"`
	EventSequence int64             `json:"event_sequence"`
}

type CreateCheckDefinitionCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	Name                string
	Executable          string
	Arguments           []string
	WorkingDirectory    string
	TimeoutMillis       int64
	OutputByteLimit     int64
	IdempotencyKey      string
	CorrelationID       string
}

type RetireCheckDefinitionCommand struct {
	WorkspaceIdentifier string
	CheckDefinitionID   string
	ExpectedRevision    int64
	Reason              string
	IdempotencyKey      string
	CorrelationID       string
}

type ListCheckDefinitionsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	Status              string
	Limit               int
}

type CreateTaskCheckRequirementCommand struct {
	WorkspaceIdentifier       string
	TaskID                    string
	CriterionKey              string
	Statement                 string
	CheckDefinitionID         string
	DefinitionContentRevision int64
	ExpectedTaskRevision      int64
	IdempotencyKey            string
	CorrelationID             string
}

type RetireTaskCheckRequirementCommand struct {
	WorkspaceIdentifier string
	RequirementID       string
	ExpectedRevision    int64
	Reason              string
	IdempotencyKey      string
	CorrelationID       string
}

type ListTaskCheckRequirementsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	TaskID              string
	Status              string
	Limit               int
}

type CheckDefinitionRevision struct {
	DefinitionID    string `json:"definition_id"`
	ContentRevision int64  `json:"content_revision"`
}

type CreateCheckWatchGrantCommand struct {
	WorkspaceIdentifier   string
	ProjectIdentifier     string
	AgentIdentifier       string
	ExpectedAgentRevision int64
	Operations            []string
	Definitions           []CheckDefinitionRevision
	MaxPending            int
	MaxInFlight           int
	ExpiresAt             string
	IdempotencyKey        string
	CorrelationID         string
}

type RevokeCheckWatchGrantCommand struct {
	WorkspaceIdentifier string
	CheckWatchGrantID   string
	ExpectedRevision    int64
	Reason              string
	IdempotencyKey      string
	CorrelationID       string
}

type ListCheckWatchGrantsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	AgentIdentifier     string
	Status              string
	Limit               int
}

type CreateCheckRouteCommand struct {
	WorkspaceIdentifier       string
	ProjectIdentifier         string
	CheckDefinitionID         string
	DefinitionContentRevision int64
	Trigger                   string
	Duty                      string
	AgentIdentifier           string
	ExpectedAgentRevision     int64
	IdempotencyKey            string
	CorrelationID             string
}

type RetireCheckRouteCommand struct {
	WorkspaceIdentifier string
	CheckRouteID        string
	ExpectedRevision    int64
	Reason              string
	IdempotencyKey      string
	CorrelationID       string
}

type ListCheckRoutesQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	DefinitionID        string
	Trigger             string
	Duty                string
	Status              string
	Limit               int
}

type ConfigureCheckPolicyCommand struct {
	WorkspaceIdentifier         string
	ProjectIdentifier           string
	RepairProposalsEnabled      bool
	RepairLaunchProfileID       string
	RepairLaunchProfileRevision int64
	MaxOpenRepairProposals      int
	ExpectedRevision            int64
	IdempotencyKey              string
	CorrelationID               string
}

type RequestCheckRunCommand struct {
	WorkspaceIdentifier               string
	TaskID                            string
	RequirementID                     string
	CheckDefinitionIdentifier         string
	CheckoutIdentifier                string
	ExpectedRequirementRevision       int64
	ExpectedDefinitionContentRevision int64
	ExpectedCheckoutRevision          int64
	IdempotencyKey                    string
	CorrelationID                     string
}

type RequestGrantedCheckRunCommand struct {
	SourceRunID           string
	CheckWatchGrantID     string
	ExpectedGrantRevision int64
	RequirementID         string
	IdempotencyKey        string
	CorrelationID         string
}

type ListCheckRunsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	TaskID              string
	RequirementID       string
	DefinitionID        string
	Status              string
	Outcome             string
	Cursor              string
	Limit               int
}

type CheckRunPage struct {
	Runs       []domain.CheckRunListItem
	NextCursor string
	HasMore    bool
	Total      int64
}

type CheckWork struct {
	Run           domain.CheckRun
	Definition    domain.CheckDefinition
	Requirement   domain.TaskCheckRequirement
	Job           domain.CheckJob
	Checkout      domain.Checkout
	Repository    domain.Repository
	LaunchReceipt *domain.CheckLaunchReceipt
}

type MarkCheckStartingCommand struct {
	CheckRunID                 string
	OperationID                string
	EffectiveSpecSHA256        string
	EffectiveWorkingDirectory  string
	Launchable                 bool
	PreflightFailureCode       string
	PreflightFailureDiagnostic string
	Observation                domain.CheckGitObservation
	CorrelationID              string
}

type PreparedCheckArtifact struct {
	Kind          string
	ContentSHA256 string
	CapturedBytes int64
	OmittedBytes  int64
	Truncated     bool
}

type FinishCheckRunCommand struct {
	CheckRunID          string
	Outcome             string
	ExitCode            *int
	Forced              bool
	DiagnosticCode      string
	Diagnostic          string
	TerminalObservation domain.CheckGitObservation
	Artifacts           []PreparedCheckArtifact
	CorrelationID       string
}

type ListGrantedCheckResultsQuery struct {
	SourceRunID string
	After       string
	Limit       int
}

type GrantedCheckResultPage struct {
	Items      []domain.CheckRunListItem `json:"items"`
	NextCursor string                    `json:"next_cursor,omitempty"`
}

type ProposeGrantedCheckRepairCommand struct {
	SourceRunID    string
	CheckResultID  string
	Rationale      string
	IdempotencyKey string
	CorrelationID  string
}

// PrepareCheckWatchCommand identifies one bounded, read-only watch pass. The
// returned preparation contains the exact database facts which the caller may
// observe through Git; it conveys no authority to mutate them.
type PrepareCheckWatchCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	After               string
	Limit               int
}

type ListCheckWatchScopesQuery struct {
	After string
	Limit int
}

type CheckWatchScope struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	ProjectID     string `json:"project_id"`
	ProjectName   string `json:"project_name"`
}

type CheckWatchScopePage struct {
	Items      []CheckWatchScope `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// CheckWatchCandidate is the immutable source identity for one result selected
// by a prepared watch pass. RepositoryFingerprint and CheckoutPath are inputs
// to real Git inspection; Role and Purpose metadata are intentionally absent.
type CheckWatchCandidate struct {
	CheckResultID         string `json:"check_result_id"`
	FreshnessRevision     int64  `json:"freshness_revision"`
	RepositoryID          string `json:"repository_id"`
	RepositoryRevision    int64  `json:"repository_revision"`
	RepositoryFingerprint string `json:"repository_fingerprint"`
	ObjectFormat          string `json:"object_format"`
	CheckoutID            string `json:"checkout_id"`
	CheckoutRevision      int64  `json:"checkout_revision"`
	CheckoutPath          string `json:"checkout_path"`
}

type PreparedCheckWatch struct {
	WorkspaceID          string                `json:"workspace_id"`
	ProjectID            string                `json:"project_id"`
	RequestedAfter       string                `json:"requested_after,omitempty"`
	RequestedLimit       int                   `json:"requested_limit"`
	RequestSHA256        string                `json:"request_sha256"`
	FromEventSequence    int64                 `json:"from_event_sequence"`
	ThroughEventSequence int64                 `json:"through_event_sequence"`
	CutoffEventSequence  int64                 `json:"cutoff_event_sequence"`
	FromResultID         string                `json:"from_result_id,omitempty"`
	ThroughResultID      string                `json:"through_result_id,omitempty"`
	CaughtUp             bool                  `json:"caught_up"`
	Candidates           []CheckWatchCandidate `json:"candidates"`
	PreparationSHA256    string                `json:"preparation_sha256"`
}

type CheckWatchObservation struct {
	CheckResultID     string                     `json:"check_result_id"`
	FreshnessRevision int64                      `json:"freshness_revision"`
	Observation       domain.CheckGitObservation `json:"observation"`
}

type ApplyCheckWatchCommand struct {
	Preparation    PreparedCheckWatch
	Observations   []CheckWatchObservation
	IdempotencyKey string
	CorrelationID  string
	PersistNoop    bool
}

type ListCheckRepairProposalsQuery struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	TaskID              string
	Status              string
	Limit               int
}

type DecideCheckRepairCommand struct {
	WorkspaceIdentifier   string
	CheckRepairProposalID string
	ExpectedRevision      int64
	DecisionNote          string
	IdempotencyKey        string
	CorrelationID         string
}

type Options struct {
	// MutationHook is a deterministic fault barrier used by component tests. The
	// production daemon leaves it nil.
	MutationHook func(stage string) error
	// Clock controls lease/timestamp observation in deterministic tests. Production
	// defaults to time.Now.
	Clock func() time.Time
	// RuntimeNodeID and RuntimeNodeFingerprint bind opaque live handles to the
	// exact daemon installation that owns their operational state. They are
	// optional for offline/read-only use and required by binding mutations.
	RuntimeNodeID          string
	RuntimeNodeFingerprint string
}
