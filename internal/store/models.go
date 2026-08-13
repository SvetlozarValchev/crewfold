package store

import (
	"time"

	"crewfold/internal/domain"
)

const (
	LatestSchemaVersion = 17

	MutationAfterProjection          = "after_projection"
	MutationAfterEvent               = "after_event"
	MutationAfterProposalActions     = "after_proposal_actions"
	MutationAfterProposalSubmission  = "after_proposal_submission"
	MutationAfterProposalEffects     = "after_proposal_effects"
	MutationAfterProposalDecision    = "after_proposal_decision"
	MutationAfterSchedulingAuthority = "after_scheduling_authority"
	MutationAfterSchedulingRun       = "after_scheduling_run"
	MutationAfterSchedulingAction    = "after_scheduling_action"
	MutationAfterSchedulingReceipt   = "after_scheduling_receipt"
	MutationAfterRetryRun            = "after_retry_run"
	MutationAfterRetryReceipt        = "after_retry_receipt"
)

type Workspace = domain.Workspace

type Event = domain.Event

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
	Status              string `json:"status"`
	SchemaVersion       int    `json:"schema_version"`
	LatestSchemaVersion int    `json:"latest_schema_version"`
	JournalMode         string `json:"journal_mode"`
	ForeignKeys         bool   `json:"foreign_keys"`
	IntegrityCheck      string `json:"integrity_check"`
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
	WorkspaceIdentifier  string
	TaskID               string
	CheckoutIdentifier   string
	ContextPacketID      string
	Runtime              string
	Provider             string
	Scenario             domain.FakeScenario
	ExpectedTaskRevision int64
	CapabilityTTL        time.Duration
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
	WorkspaceIdentifier string
	SenderRunID         string
	RecipientAgent      string
	ThreadID            string
	ProjectIdentifier   string
	TaskID              string
	Kind                string
	Subject             string
	Body                string
	ArtifactIDs         []string
	ReplyToMessageID    string
	IdempotencyKey      string
	CorrelationID       string
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
	Status              string
	ActionID            string
	Limit               int
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

type Options struct {
	// MutationHook is a deterministic fault barrier used by component tests. The
	// production daemon leaves it nil.
	MutationHook func(stage string) error
	// Clock controls lease/timestamp observation in deterministic tests. Production
	// defaults to time.Now.
	Clock func() time.Time
}
