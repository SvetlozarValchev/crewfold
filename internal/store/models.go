package store

import (
	"time"

	"crewfold/internal/domain"
)

const (
	LatestSchemaVersion = 10

	MutationAfterProjection = "after_projection"
	MutationAfterEvent      = "after_event"
)

type Workspace = domain.Workspace

type Event = domain.Event

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
