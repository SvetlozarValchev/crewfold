// Package localapi defines the versioned protocol shared by Crewfold's local
// daemon and clients.
package localapi

import (
	"encoding/json"
	"fmt"

	"crewfold/internal/buildinfo"
	"crewfold/internal/domain"
)

const (
	MinProtocol = 1
	MaxProtocol = 1

	MethodHello                 = "system.hello"
	MethodStatus                = "system.status"
	MethodStop                  = "system.stop"
	MethodWebBootstrap          = "web.bootstrap"
	MethodDatabaseStatus        = "database.status"
	MethodWorkspaceInit         = "workspace.init"
	MethodWorkspaceShow         = "workspace.show"
	MethodWorkspaceList         = "workspace.list"
	MethodProjectAdd            = "project.add"
	MethodProjectShow           = "project.show"
	MethodProjectInspect        = "project.inspect"
	MethodProjectList           = "project.list"
	MethodCheckoutAdd           = "checkout.add"
	MethodCheckoutList          = "checkout.list"
	MethodAgentCreate           = "agent.create"
	MethodAgentUpdate           = "agent.update"
	MethodAgentShow             = "agent.show"
	MethodAgentList             = "agent.list"
	MethodObjectiveCreate       = "objective.create"
	MethodObjectiveUpdate       = "objective.update"
	MethodObjectiveShow         = "objective.show"
	MethodObjectiveList         = "objective.list"
	MethodTaskCreate            = "task.create"
	MethodTaskUpdate            = "task.update"
	MethodTaskShow              = "task.show"
	MethodTaskList              = "task.list"
	MethodTaskDepend            = "task.dependency.add"
	MethodTaskAssign            = "task.assign"
	MethodTaskTransition        = "task.transition"
	MethodTaskTimeline          = "task.timeline"
	MethodContextBuild          = "context.build"
	MethodContextShow           = "context.show"
	MethodContextExplain        = "context.explain"
	MethodContextRefresh        = "context.refresh"
	MethodContextDeltaList      = "context.delta.list"
	MethodContextDeltaShow      = "context.delta.show"
	MethodContextDeltaExplain   = "context.delta.explain"
	MethodKnowledgePropose      = "knowledge.propose"
	MethodKnowledgeShow         = "knowledge.show"
	MethodKnowledgeList         = "knowledge.list"
	MethodKnowledgeSearch       = "knowledge.search"
	MethodKnowledgeIndexStatus  = "knowledge.index.status"
	MethodKnowledgeIndexRebuild = "knowledge.index.rebuild"
	MethodKnowledgeAccept       = "knowledge.accept"
	MethodKnowledgeReject       = "knowledge.reject"
	MethodKnowledgeMarkStale    = "knowledge.mark_stale"
	MethodKnowledgeDispute      = "knowledge.dispute"
	MethodKnowledgeExport       = "knowledge.export"
	MethodKnowledgeImport       = "knowledge.import"
	MethodCuratorQueue          = "curator.queue"
	MethodCuratorRuleConfigure  = "curator.rule.configure"
	MethodCuratorProcess        = "curator.process"
	MethodContradictionReport   = "contradiction.report"
	MethodContradictionShow     = "contradiction.show"
	MethodContradictionList     = "contradiction.list"
	MethodContradictionConfirm  = "contradiction.confirm"
	MethodContradictionDismiss  = "contradiction.dismiss"
	MethodMessageSend           = "message.send"
	MethodInboxList             = "inbox.list"
	MethodThreadCreate          = "thread.create"
	MethodThreadInvite          = "thread.invite"
	MethodThreadParticipants    = "thread.participants.list"
	MethodThreadShow            = "thread.show"
	MethodRunStart              = "run.start"
	MethodRunShow               = "run.show"
	MethodRunList               = "run.list"
	MethodRunResume             = "run.resume"
	MethodRunStop               = "run.stop"
	MethodRunLostResolve        = "run.lost.resolve"
	MethodRunLogs               = "run.logs"
	MethodRunPrompt             = "run.prompt"
	MethodRunInterrupt          = "run.interrupt"
	MethodRunAttach             = "run.attach"
	MethodCoordinationStatus    = "coordination.status"
	MethodClaimAdd              = "claim.add"
	MethodClaimList             = "claim.list"
	MethodClaimRelease          = "claim.release"
	MethodOverlapList           = "overlap.list"
	MethodOverlapInspect        = "overlap.inspect"
	MethodOverlapScan           = "overlap.scan"
	MethodDriftList             = "drift.list"
	MethodMeetingCreate         = "meeting.create"
	MethodMeetingRun            = "meeting.run"
	MethodMeetingInspect        = "meeting.inspect"
	MethodMeetingAccept         = "meeting.accept"
	MethodMeetingTakeover       = "meeting.takeover"
	MethodMeetingList           = "meeting.list"
	MethodEventsList            = "events.list"
	MethodEventsTimeline        = "events.timeline"

	StatusSchema                    = "urn:crewfold:schema:local-api:status-result:v1"
	StopSchema                      = "urn:crewfold:schema:local-api:stop-result:v1"
	WebBootstrapSchema              = "urn:crewfold:schema:local-api:web-bootstrap-result:v1"
	DatabaseStatusSchema            = "urn:crewfold:schema:local-api:database-status-result:v1"
	WorkspaceInitSchema             = "urn:crewfold:schema:local-api:workspace-init-result:v1"
	WorkspaceShowSchema             = "urn:crewfold:schema:local-api:workspace-show-result:v1"
	WorkspaceListSchema             = "urn:crewfold:schema:local-api:workspace-list-result:v1"
	ProjectAddSchema                = "urn:crewfold:schema:local-api:project-add-result:v1"
	ProjectShowSchema               = "urn:crewfold:schema:local-api:project-show-result:v1"
	ProjectInspectSchema            = "urn:crewfold:schema:local-api:project-inspect-result:v1"
	ProjectListSchema               = "urn:crewfold:schema:local-api:project-list-result:v1"
	CheckoutAddSchema               = "urn:crewfold:schema:local-api:checkout-add-result:v1"
	CheckoutListSchema              = "urn:crewfold:schema:local-api:checkout-list-result:v1"
	AgentMutationSchema             = "urn:crewfold:schema:local-api:agent-mutation-result:v1"
	AgentShowSchema                 = "urn:crewfold:schema:local-api:agent-show-result:v1"
	AgentListSchema                 = "urn:crewfold:schema:local-api:agent-list-result:v1"
	ObjectiveMutationSchema         = "urn:crewfold:schema:local-api:objective-mutation-result:v1"
	ObjectiveShowSchema             = "urn:crewfold:schema:local-api:objective-show-result:v1"
	ObjectiveListSchema             = "urn:crewfold:schema:local-api:objective-list-result:v1"
	TaskMutationSchema              = "urn:crewfold:schema:local-api:task-mutation-result:v1"
	TaskShowSchema                  = "urn:crewfold:schema:local-api:task-show-result:v1"
	TaskListSchema                  = "urn:crewfold:schema:local-api:task-list-result:v1"
	TaskTimelineSchema              = "urn:crewfold:schema:local-api:task-timeline-result:v1"
	ContextBuildSchema              = "urn:crewfold:schema:local-api:context-build-result:v1"
	ContextShowSchema               = "urn:crewfold:schema:local-api:context-show-result:v1"
	ContextExplainSchema            = "urn:crewfold:schema:local-api:context-explain-result:v1"
	ContextRefreshSchema            = "urn:crewfold:schema:local-api:context-refresh-result:v1"
	ContextDeltaListSchema          = "urn:crewfold:schema:local-api:context-delta-list-result:v1"
	ContextDeltaShowSchema          = "urn:crewfold:schema:local-api:context-delta-show-result:v1"
	ContextDeltaExplainSchema       = "urn:crewfold:schema:local-api:context-delta-explain-result:v1"
	KnowledgeMutationSchema         = "urn:crewfold:schema:local-api:knowledge-mutation-result:v1"
	KnowledgeShowSchema             = "urn:crewfold:schema:local-api:knowledge-show-result:v1"
	KnowledgeListSchema             = "urn:crewfold:schema:local-api:knowledge-list-result:v1"
	KnowledgeSearchSchema           = "urn:crewfold:schema:local-api:knowledge-search-result:v1"
	KnowledgeIndexStatusSchema      = "urn:crewfold:schema:local-api:knowledge-index-status-result:v1"
	KnowledgeIndexRebuildSchema     = "urn:crewfold:schema:local-api:knowledge-index-rebuild-result:v1"
	KnowledgeDisputeSchema          = "urn:crewfold:schema:local-api:knowledge-dispute-result:v1"
	KnowledgeExportSchema           = "urn:crewfold:schema:local-api:knowledge-export-result:v1"
	KnowledgeImportSchema           = "urn:crewfold:schema:local-api:knowledge-import-result:v1"
	CuratorQueueSchema              = "urn:crewfold:schema:local-api:curator-queue-result:v1"
	CuratorRuleMutationSchema       = "urn:crewfold:schema:local-api:curator-rule-mutation-result:v1"
	CuratorProcessSchema            = "urn:crewfold:schema:local-api:curator-process-result:v1"
	ContradictionMutationSchema     = "urn:crewfold:schema:local-api:contradiction-mutation-result:v1"
	ContradictionShowSchema         = "urn:crewfold:schema:local-api:contradiction-show-result:v1"
	ContradictionListSchema         = "urn:crewfold:schema:local-api:contradiction-list-result:v1"
	MessageSendSchema               = "urn:crewfold:schema:local-api:message-send-result:v1"
	InboxListSchema                 = "urn:crewfold:schema:local-api:inbox-list-result:v1"
	ParticipantThreadMutationSchema = "urn:crewfold:schema:local-api:participant-thread-mutation-result:v1"
	ParticipantThreadSchema         = "urn:crewfold:schema:local-api:participant-thread-result:v1"
	ThreadShowSchema                = "urn:crewfold:schema:local-api:thread-show-result:v1"
	RunMutationSchema               = "urn:crewfold:schema:local-api:run-mutation-result:v1"
	RunLossResolutionSchema         = "urn:crewfold:schema:local-api:run-loss-resolution-result:v1"
	RunShowSchema                   = "urn:crewfold:schema:local-api:run-show-result:v1"
	RunListSchema                   = "urn:crewfold:schema:local-api:run-list-result:v1"
	RunLogsSchema                   = "urn:crewfold:schema:local-api:run-logs-result:v1"
	RunControlSchema                = "urn:crewfold:schema:local-api:run-control-result:v1"
	RunAttachSchema                 = "urn:crewfold:schema:local-api:run-attach-result:v1"
	CoordinationStatusSchema        = "urn:crewfold:schema:local-api:coordination-status-result:v1"
	ClaimMutationSchema             = "urn:crewfold:schema:local-api:claim-mutation-result:v1"
	ClaimListSchema                 = "urn:crewfold:schema:local-api:claim-list-result:v1"
	OverlapListSchema               = "urn:crewfold:schema:local-api:overlap-list-result:v1"
	OverlapInspectSchema            = "urn:crewfold:schema:local-api:overlap-inspect-result:v1"
	OverlapScanSchema               = "urn:crewfold:schema:local-api:overlap-scan-result:v1"
	DriftListSchema                 = "urn:crewfold:schema:local-api:drift-list-result:v1"
	MeetingMutationSchema           = "urn:crewfold:schema:local-api:meeting-mutation-result:v1"
	MeetingInspectSchema            = "urn:crewfold:schema:local-api:meeting-inspect-result:v1"
	MeetingListSchema               = "urn:crewfold:schema:local-api:meeting-list-result:v1"
	EventsListSchema                = "urn:crewfold:schema:local-api:events-list-result:v1"
	EventsTimelineSchema            = "urn:crewfold:schema:local-api:events-timeline-result:v1"
)

// Request is one newline-delimited local API request. Hello requests omit
// Protocol; all other methods use the value selected during hello.
type Request struct {
	ID       string          `json:"id"`
	Protocol int             `json:"protocol,omitempty"`
	Method   string          `json:"method"`
	Params   json.RawMessage `json:"params,omitempty"`
}

// Response is one newline-delimited local API response.
type Response struct {
	ID       string          `json:"id"`
	Protocol int             `json:"protocol,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    *APIError       `json:"error,omitempty"`
}

// APIError is the stable error body returned by the daemon.
type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

type HelloParams struct {
	MinProtocol int `json:"min_protocol"`
	MaxProtocol int `json:"max_protocol"`
}

type HelloResult struct {
	Type             string         `json:"type"`
	SelectedProtocol int            `json:"selected_protocol"`
	ServerMin        int            `json:"server_min_protocol"`
	ServerMax        int            `json:"server_max_protocol"`
	Version          buildinfo.Info `json:"version"`
}

type StatusResult struct {
	Schema          string         `json:"schema"`
	Type            string         `json:"type"`
	Status          string         `json:"status"`
	Protocol        int            `json:"protocol"`
	PID             int            `json:"pid"`
	StartedAt       string         `json:"started_at"`
	UptimeMillis    int64          `json:"uptime_ms"`
	ServerVersion   buildinfo.Info `json:"server_version"`
	ActiveRequests  int            `json:"active_requests"`
	ShutdownPending bool           `json:"shutdown_pending"`
}

type StopResult struct {
	Schema string `json:"schema"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

// WebBootstrapParams is intentionally empty. The daemon binds the minted
// one-time grant to its one current owner-local web listener.
type WebBootstrapParams struct{}

// WebBootstrapResult contains a short-lived URL whose fragment is never sent
// in an HTTP request. The browser exchanges it once for an owner session.
type WebBootstrapResult struct {
	Schema    string `json:"schema"`
	Type      string `json:"type"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

type DatabaseStatusResult struct {
	Schema         string `json:"schema"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	SchemaVersion  int    `json:"schema_version"`
	BaselineSHA256 string `json:"baseline_sha256"`
	CatalogSHA256  string `json:"catalog_sha256"`
	JournalMode    string `json:"journal_mode"`
	ForeignKeys    bool   `json:"foreign_keys"`
	IntegrityCheck string `json:"integrity_check"`
}

type WorkspaceInitParams struct {
	Name           string `json:"name"`
	IdempotencyKey string `json:"idempotency_key"`
}

type WorkspaceInitResult struct {
	Schema        string           `json:"schema"`
	Type          string           `json:"type"`
	Workspace     domain.Workspace `json:"workspace"`
	EventID       string           `json:"event_id"`
	EventSequence int64            `json:"event_sequence"`
}

type WorkspaceShowParams struct {
	Identifier string `json:"identifier"`
}

type WorkspaceShowResult struct {
	Schema    string           `json:"schema"`
	Type      string           `json:"type"`
	Workspace domain.Workspace `json:"workspace"`
}

type PageParams struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type PageResult struct {
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
	Total      int64  `json:"total"`
}

type WorkspaceListParams struct {
	PageParams
}

type WorkspaceListResult struct {
	Schema     string             `json:"schema"`
	Type       string             `json:"type"`
	Workspaces []domain.Workspace `json:"workspaces"`
	PageResult
}

type ProjectAddParams struct {
	Workspace      string `json:"workspace"`
	Name           string `json:"name"`
	RepositoryPath string `json:"repository_path"`
	WriteMode      string `json:"write_mode,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

type ProjectAddResult struct {
	Schema        string            `json:"schema"`
	Type          string            `json:"type"`
	Project       domain.Project    `json:"project"`
	Repository    domain.Repository `json:"repository"`
	Checkout      domain.Checkout   `json:"checkout"`
	EventSequence int64             `json:"event_sequence"`
}

type ProjectInspectParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
}

type ProjectShowParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
}

type ProjectShowResult struct {
	Schema  string         `json:"schema"`
	Type    string         `json:"type"`
	Project domain.Project `json:"project"`
}

type ProjectInspectResult struct {
	Schema       string              `json:"schema"`
	Type         string              `json:"type"`
	Project      domain.Project      `json:"project"`
	Repositories []domain.Repository `json:"repositories"`
	Checkouts    []domain.Checkout   `json:"checkouts"`
}

type ProjectListParams struct {
	Workspace string `json:"workspace"`
	PageParams
}

type ProjectListResult struct {
	Schema   string           `json:"schema"`
	Type     string           `json:"type"`
	Projects []domain.Project `json:"projects"`
	PageResult
}

type CheckoutAddParams struct {
	Workspace      string `json:"workspace"`
	Project        string `json:"project"`
	RepositoryPath string `json:"repository_path"`
	WriteMode      string `json:"write_mode,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

type CheckoutAddResult struct {
	Schema            string            `json:"schema"`
	Type              string            `json:"type"`
	Repository        domain.Repository `json:"repository"`
	Checkout          domain.Checkout   `json:"checkout"`
	RepositoryCreated bool              `json:"repository_created"`
	EventSequence     int64             `json:"event_sequence"`
}

type CheckoutListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
}

type CheckoutListResult struct {
	Schema    string            `json:"schema"`
	Type      string            `json:"type"`
	Project   domain.Project    `json:"project"`
	Checkouts []domain.Checkout `json:"checkouts"`
}

type AgentCreateParams struct {
	Workspace      string `json:"workspace"`
	Name           string `json:"name"`
	Role           string `json:"role"`
	Provider       string `json:"provider"`
	Runtime        string `json:"runtime,omitempty"`
	MaxConcurrency int    `json:"max_concurrency,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

type AgentUpdateParams struct {
	Workspace        string  `json:"workspace"`
	Agent            string  `json:"agent"`
	Role             *string `json:"role,omitempty"`
	Provider         *string `json:"provider,omitempty"`
	Runtime          *string `json:"runtime,omitempty"`
	Enabled          *bool   `json:"enabled,omitempty"`
	MaxConcurrency   *int    `json:"max_concurrency,omitempty"`
	ExpectedRevision int64   `json:"expected_revision"`
	IdempotencyKey   string  `json:"idempotency_key"`
}

type AgentQueryParams struct {
	Workspace string `json:"workspace"`
	Agent     string `json:"agent"`
}

type AgentListParams struct {
	Workspace string `json:"workspace"`
	PageParams
}

type AgentMutationResult struct {
	Schema        string                 `json:"schema"`
	Type          string                 `json:"type"`
	Agent         domain.AgentDefinition `json:"agent"`
	EventSequence int64                  `json:"event_sequence"`
}

type AgentShowResult struct {
	Schema string                 `json:"schema"`
	Type   string                 `json:"type"`
	Agent  domain.AgentDefinition `json:"agent"`
}

type AgentListResult struct {
	Schema string                   `json:"schema"`
	Type   string                   `json:"type"`
	Agents []domain.AgentDefinition `json:"agents"`
	PageResult
}

type ObjectiveCreateParams struct {
	Workspace      string        `json:"workspace"`
	Project        string        `json:"project"`
	Title          string        `json:"title"`
	Budget         domain.Budget `json:"budget"`
	IdempotencyKey string        `json:"idempotency_key"`
}

type ObjectiveUpdateParams struct {
	Workspace        string         `json:"workspace"`
	Objective        string         `json:"objective"`
	Title            *string        `json:"title,omitempty"`
	Status           *string        `json:"status,omitempty"`
	Budget           *domain.Budget `json:"budget,omitempty"`
	ExpectedRevision int64          `json:"expected_revision"`
	IdempotencyKey   string         `json:"idempotency_key"`
}

type ObjectiveQueryParams struct {
	Workspace string `json:"workspace"`
	Objective string `json:"objective"`
}

type ObjectiveListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	PageParams
}

type ObjectiveMutationResult struct {
	Schema        string           `json:"schema"`
	Type          string           `json:"type"`
	Objective     domain.Objective `json:"objective"`
	EventSequence int64            `json:"event_sequence"`
}

type ObjectiveShowResult struct {
	Schema    string           `json:"schema"`
	Type      string           `json:"type"`
	Objective domain.Objective `json:"objective"`
}

type ObjectiveListResult struct {
	Schema     string             `json:"schema"`
	Type       string             `json:"type"`
	Objectives []domain.Objective `json:"objectives"`
	PageResult
}

type TaskCreateParams struct {
	Workspace      string        `json:"workspace"`
	Project        string        `json:"project"`
	Objective      string        `json:"objective,omitempty"`
	Title          string        `json:"title"`
	Description    string        `json:"description,omitempty"`
	Priority       int           `json:"priority"`
	Budget         domain.Budget `json:"budget"`
	IdempotencyKey string        `json:"idempotency_key"`
}

type TaskUpdateParams struct {
	Workspace        string         `json:"workspace"`
	Task             string         `json:"task"`
	Title            *string        `json:"title,omitempty"`
	Description      *string        `json:"description,omitempty"`
	Priority         *int           `json:"priority,omitempty"`
	Budget           *domain.Budget `json:"budget,omitempty"`
	ExpectedRevision int64          `json:"expected_revision"`
	IdempotencyKey   string         `json:"idempotency_key"`
}

type TaskQueryParams struct {
	Workspace string `json:"workspace"`
	Task      string `json:"task"`
}

type TaskListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	ReadyOnly bool   `json:"ready_only,omitempty"`
	PageParams
}

type TaskDependencyParams struct {
	Workspace        string `json:"workspace"`
	Task             string `json:"task"`
	DependsOn        string `json:"depends_on"`
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type TaskAssignParams struct {
	Workspace        string `json:"workspace"`
	Task             string `json:"task"`
	Agent            string `json:"agent"`
	LeaseSeconds     int64  `json:"lease_seconds"`
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type TaskTransitionParams struct {
	Workspace        string `json:"workspace"`
	Task             string `json:"task"`
	Action           string `json:"action"`
	Reason           string `json:"reason,omitempty"`
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type TaskMutationResult struct {
	Schema        string            `json:"schema"`
	Type          string            `json:"type"`
	Detail        domain.TaskDetail `json:"detail"`
	EventSequence int64             `json:"event_sequence"`
}

type TaskShowResult struct {
	Schema string            `json:"schema"`
	Type   string            `json:"type"`
	Detail domain.TaskDetail `json:"detail"`
}

type TaskListResult struct {
	Schema string              `json:"schema"`
	Type   string              `json:"type"`
	Tasks  []domain.TaskDetail `json:"tasks"`
	PageResult
}

type TaskTimelineParams struct {
	Workspace string `json:"workspace"`
	Task      string `json:"task"`
}

type TaskTimelineResult struct {
	Schema   string              `json:"schema"`
	Type     string              `json:"type"`
	Timeline domain.TaskTimeline `json:"timeline"`
}

type ClaimAddParams struct {
	Workspace      string `json:"workspace"`
	Project        string `json:"project"`
	Task           string `json:"task"`
	Checkout       string `json:"checkout,omitempty"`
	Kind           string `json:"kind"`
	Target         string `json:"target"`
	Mode           string `json:"mode,omitempty"`
	ConflictPolicy string `json:"conflict_policy,omitempty"`
	LeaseSeconds   int64  `json:"lease_seconds"`
	IdempotencyKey string `json:"idempotency_key"`
}

type ClaimListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	Status    string `json:"status,omitempty"`
	PageParams
}

type ClaimReleaseParams struct {
	Workspace        string `json:"workspace"`
	Claim            string `json:"claim"`
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type ClaimMutationResult struct {
	Schema        string               `json:"schema"`
	Type          string               `json:"type"`
	Claim         domain.WorkClaim     `json:"claim"`
	Overlaps      []domain.WorkOverlap `json:"overlaps"`
	Warnings      []string             `json:"warnings"`
	EventSequence int64                `json:"event_sequence"`
}

type ClaimListResult struct {
	Schema string             `json:"schema"`
	Type   string             `json:"type"`
	Claims []domain.WorkClaim `json:"claims"`
	PageResult
}

type OverlapInspectParams struct {
	Workspace string `json:"workspace"`
	Overlap   string `json:"overlap"`
}

type OverlapScanParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
}

type OverlapListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	Status    string `json:"status,omitempty"`
	PageParams
}

type OverlapListResult struct {
	Schema   string               `json:"schema"`
	Type     string               `json:"type"`
	Overlaps []domain.WorkOverlap `json:"overlaps"`
	PageResult
}

type OverlapInspectResult struct {
	Schema  string             `json:"schema"`
	Type    string             `json:"type"`
	Overlap domain.WorkOverlap `json:"overlap"`
}

type OverlapScanResult struct {
	Schema string                     `json:"schema"`
	Type   string                     `json:"type"`
	Scans  []domain.CheckoutClaimScan `json:"scans"`
	Issues []string                   `json:"issues"`
}

type DriftListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	Status    string `json:"status,omitempty"`
	PageParams
}

type DriftListResult struct {
	Schema string              `json:"schema"`
	Type   string              `json:"type"`
	Drifts []domain.ClaimDrift `json:"drifts"`
	PageResult
}

type MeetingCreateParams struct {
	Workspace      string   `json:"workspace"`
	Overlap        string   `json:"overlap"`
	Participants   []string `json:"participants"`
	Facilitator    string   `json:"facilitator"`
	Policy         string   `json:"policy,omitempty"`
	Reviewer       string   `json:"reviewer,omitempty"`
	AllowedActions []string `json:"allowed_actions,omitempty"`
	TimeoutSeconds int64    `json:"timeout_seconds"`
	IdempotencyKey string   `json:"idempotency_key"`
}

type MeetingRunParams struct {
	Workspace        string                   `json:"workspace"`
	Meeting          string                   `json:"meeting"`
	ExpectedRevision int64                    `json:"expected_revision"`
	Fixture          domain.MeetingRunFixture `json:"fixture"`
	IdempotencyKey   string                   `json:"idempotency_key"`
}

type MeetingQueryParams struct {
	Workspace string `json:"workspace"`
	Meeting   string `json:"meeting"`
}

type MeetingListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	Status    string `json:"status,omitempty"`
	PageParams
}

type MeetingAcceptParams struct {
	Workspace        string `json:"workspace"`
	Meeting          string `json:"meeting"`
	ExpectedRevision int64  `json:"expected_revision"`
	DecisionNote     string `json:"decision_note,omitempty"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type MeetingTakeoverParams struct {
	Workspace        string                      `json:"workspace"`
	Meeting          string                      `json:"meeting"`
	ExpectedRevision int64                       `json:"expected_revision"`
	Proposal         domain.MeetingProposalInput `json:"proposal"`
	DecisionNote     string                      `json:"decision_note,omitempty"`
	IdempotencyKey   string                      `json:"idempotency_key"`
}

type MeetingMutationResult struct {
	Schema        string               `json:"schema"`
	Type          string               `json:"type"`
	Detail        domain.MeetingDetail `json:"detail"`
	EventSequence int64                `json:"event_sequence"`
}

type MeetingInspectResult struct {
	Schema string               `json:"schema"`
	Type   string               `json:"type"`
	Detail domain.MeetingDetail `json:"detail"`
}

type MeetingListResult struct {
	Schema   string           `json:"schema"`
	Type     string           `json:"type"`
	Meetings []domain.Meeting `json:"meetings"`
	PageResult
}

type ContextBuildParams struct {
	Workspace            string   `json:"workspace"`
	Task                 string   `json:"task"`
	Agent                string   `json:"agent"`
	Checkout             string   `json:"checkout,omitempty"`
	KnowledgeRevisionIDs []string `json:"knowledge_revision_ids"`
	ExpectedTaskRevision int64    `json:"expected_task_revision"`
	IdempotencyKey       string   `json:"idempotency_key"`
}

type ContextQueryParams struct {
	Workspace string `json:"workspace"`
	Context   string `json:"context"`
}

type ContextBuildResult struct {
	Schema        string               `json:"schema"`
	Type          string               `json:"type"`
	Packet        domain.ContextPacket `json:"packet"`
	EventSequence int64                `json:"event_sequence"`
}

type ContextShowResult struct {
	Schema string               `json:"schema"`
	Type   string               `json:"type"`
	Packet domain.ContextPacket `json:"packet"`
}

type ContextExplainResult struct {
	Schema      string                    `json:"schema"`
	Type        string                    `json:"type"`
	Explanation domain.ContextExplanation `json:"explanation"`
}

type ContextRefreshParams struct {
	Workspace      string `json:"workspace"`
	Run            string `json:"run"`
	IdempotencyKey string `json:"idempotency_key"`
}

type ContextDeltaListParams struct {
	Workspace     string `json:"workspace"`
	Run           string `json:"run"`
	AfterSequence *int64 `json:"after_sequence"`
	Limit         int    `json:"limit"`
}

type ContextDeltaQueryParams struct {
	Workspace string `json:"workspace"`
	Delta     string `json:"delta"`
}

// ContextRefreshResult intentionally promotes the stable domain fields. The
// local envelope adds only transport schema/type discriminators.
type ContextRefreshResult struct {
	Schema string `json:"schema"`
	Type   string `json:"type"`
	domain.ContextRefreshResult
}

type ContextDeltaListResult struct {
	Schema string `json:"schema"`
	Type   string `json:"type"`
	domain.ContextDeltaList
}

type ContextDeltaShowResult struct {
	Schema string              `json:"schema"`
	Type   string              `json:"type"`
	Delta  domain.ContextDelta `json:"delta"`
}

type ContextDeltaExplainResult struct {
	Schema      string                         `json:"schema"`
	Type        string                         `json:"type"`
	Explanation domain.ContextDeltaExplanation `json:"explanation"`
}

type KnowledgeProposeParams struct {
	Workspace            string                        `json:"workspace"`
	Project              string                        `json:"project,omitempty"`
	TaskScopeID          string                        `json:"task_scope_id,omitempty"`
	Type                 string                        `json:"type"`
	Title                string                        `json:"title"`
	Body                 string                        `json:"body"`
	Confidence           string                        `json:"confidence"`
	VerificationStatus   string                        `json:"verification_status"`
	FreshnessPolicy      string                        `json:"freshness_policy"`
	FreshUntil           string                        `json:"fresh_until,omitempty"`
	Sources              []domain.KnowledgeSourceInput `json:"sources"`
	SupersedesRevisionID string                        `json:"supersedes_revision_id,omitempty"`
	IdempotencyKey       string                        `json:"idempotency_key"`
}

type KnowledgeQueryParams struct {
	Workspace         string `json:"workspace"`
	KnowledgeRevision string `json:"knowledge_revision"`
}

type KnowledgeListParams struct {
	Workspace      string `json:"workspace"`
	Project        string `json:"project"`
	TaskScopeID    string `json:"task_scope_id,omitempty"`
	Type           string `json:"type,omitempty"`
	ReviewStatus   string `json:"review_status,omitempty"`
	CurrencyStatus string `json:"currency_status,omitempty"`
}

type KnowledgeSearchParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
	Query     string `json:"query"`
	Task      string `json:"task,omitempty"`
	Type      string `json:"type,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

type KnowledgeIndexStatusParams struct {
	Workspace string `json:"workspace"`
}

type KnowledgeIndexRebuildParams struct {
	Workspace      string `json:"workspace"`
	IdempotencyKey string `json:"idempotency_key"`
}

type KnowledgeDecisionParams struct {
	Workspace             string `json:"workspace"`
	KnowledgeRevision     string `json:"knowledge_revision"`
	ExpectedStateRevision int64  `json:"expected_state_revision"`
	DecisionNote          string `json:"decision_note,omitempty"`
	IdempotencyKey        string `json:"idempotency_key"`
}

type KnowledgeMarkStaleParams struct {
	Workspace             string `json:"workspace"`
	KnowledgeRevision     string `json:"knowledge_revision"`
	ExpectedStateRevision int64  `json:"expected_state_revision"`
	Reason                string `json:"reason"`
	IdempotencyKey        string `json:"idempotency_key"`
}

type KnowledgeMutationResult struct {
	Schema         string                          `json:"schema"`
	Type           string                          `json:"type"`
	Revision       domain.KnowledgeRevision        `json:"revision"`
	AuthorityCheck *domain.KnowledgeAuthorityCheck `json:"authority_check,omitempty"`
	EventSequence  int64                           `json:"event_sequence"`
}

type KnowledgeDisputeResult struct {
	Schema  string                          `json:"schema"`
	Type    string                          `json:"type"`
	Dispute domain.KnowledgeRevisionDispute `json:"dispute"`
}

type KnowledgeExportParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
	Directory string `json:"directory"`
}

type KnowledgeImportParams struct {
	Workspace             string `json:"workspace"`
	Project               string `json:"project"`
	Directory             string `json:"directory"`
	ExpectedContentSHA256 string `json:"expected_content_sha256"`
	CreateScope           bool   `json:"create_scope"`
	IdempotencyKey        string `json:"idempotency_key"`
}

type KnowledgeExportResult struct {
	Schema            string                         `json:"schema"`
	Type              string                         `json:"type"`
	Directory         string                         `json:"directory"`
	BundleID          string                         `json:"bundle_id"`
	ContentSHA256     string                         `json:"content_sha256"`
	ManifestSHA256    string                         `json:"manifest_sha256"`
	ManifestBytes     int64                          `json:"manifest_bytes"`
	MarkdownSHA256    string                         `json:"markdown_sha256"`
	MarkdownBytes     int64                          `json:"markdown_bytes"`
	AsOfEventSequence int64                          `json:"as_of_event_sequence"`
	Counts            domain.PortableKnowledgeCounts `json:"counts"`
}

type PortableKnowledgeCreatedCounts struct {
	Workspaces       int64 `json:"workspaces"`
	Projects         int64 `json:"projects"`
	TaskScopeAnchors int64 `json:"task_scope_anchors"`
}

type KnowledgeImportResult struct {
	Schema         string                         `json:"schema"`
	Type           string                         `json:"type"`
	Directory      string                         `json:"directory"`
	BundleID       string                         `json:"bundle_id"`
	ContentSHA256  string                         `json:"content_sha256"`
	ManifestSHA256 string                         `json:"manifest_sha256"`
	ManifestBytes  int64                          `json:"manifest_bytes"`
	MarkdownSHA256 string                         `json:"markdown_sha256"`
	MarkdownBytes  int64                          `json:"markdown_bytes"`
	Counts         domain.PortableKnowledgeCounts `json:"counts"`
	Receipt        domain.KnowledgeImportReceipt  `json:"receipt"`
	Status         string                         `json:"status"`
	Created        PortableKnowledgeCreatedCounts `json:"created"`
	EventSequence  int64                          `json:"event_sequence"`
}

type ContradictionReportParams struct {
	Workspace      string `json:"workspace"`
	LeftRevision   string `json:"left_revision"`
	RightRevision  string `json:"right_revision"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

type ContradictionQueryParams struct {
	Workspace     string `json:"workspace"`
	Contradiction string `json:"contradiction"`
}

type ContradictionListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
	Status    string `json:"status,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

type ContradictionDecisionParams struct {
	Workspace             string `json:"workspace"`
	Contradiction         string `json:"contradiction"`
	ExpectedStateRevision int64  `json:"expected_state_revision"`
	Note                  string `json:"note,omitempty"`
	IdempotencyKey        string `json:"idempotency_key"`
}

type ContradictionMutationResult struct {
	Schema         string                                       `json:"schema"`
	Type           string                                       `json:"type"`
	Detail         domain.KnowledgeContradictionDetail          `json:"detail"`
	AuthorityCheck *domain.KnowledgeContradictionAuthorityCheck `json:"authority_check,omitempty"`
	EventSequence  int64                                        `json:"event_sequence"`
}

type ContradictionShowResult struct {
	Schema string                              `json:"schema"`
	Type   string                              `json:"type"`
	Detail domain.KnowledgeContradictionDetail `json:"detail"`
}

type ContradictionListResult struct {
	Schema string                            `json:"schema"`
	Type   string                            `json:"type"`
	List   domain.KnowledgeContradictionList `json:"list"`
}

type KnowledgeShowResult struct {
	Schema string                 `json:"schema"`
	Type   string                 `json:"type"`
	Detail domain.KnowledgeDetail `json:"detail"`
}

type KnowledgeListResult struct {
	Schema string               `json:"schema"`
	Type   string               `json:"type"`
	List   domain.KnowledgeList `json:"list"`
}

type KnowledgeSearchResult struct {
	Schema string                       `json:"schema"`
	Type   string                       `json:"type"`
	Search domain.KnowledgeSearchResult `json:"search"`
}

type KnowledgeIndexStatusResult struct {
	Schema string                      `json:"schema"`
	Type   string                      `json:"type"`
	Index  domain.KnowledgeIndexStatus `json:"index"`
}

type KnowledgeIndexRebuildResult struct {
	Schema string                      `json:"schema"`
	Type   string                      `json:"type"`
	Index  domain.KnowledgeIndexStatus `json:"index"`
}

type CuratorQueueParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
	After     string `json:"after,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

type CuratorRuleConfigureParams struct {
	Workspace        string `json:"workspace"`
	Rule             string `json:"rule"`
	Enabled          *bool  `json:"enabled"`
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type CuratorProcessParams struct {
	Workspace      string `json:"workspace"`
	Project        string `json:"project"`
	ApplySafe      bool   `json:"apply_safe,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

type CuratorQueueResult struct {
	Schema string              `json:"schema"`
	Type   string              `json:"type"`
	Queue  domain.CuratorQueue `json:"queue"`
}

type CuratorRuleMutationResult struct {
	Schema        string             `json:"schema"`
	Type          string             `json:"type"`
	Rule          domain.CuratorRule `json:"rule"`
	EventSequence int64              `json:"event_sequence"`
}

type CuratorProcessResult struct {
	Schema        string                `json:"schema"`
	Type          string                `json:"type"`
	Process       domain.CuratorProcess `json:"process"`
	EventSequence int64                 `json:"event_sequence"`
}

type MessageSendParams struct {
	Workspace      string   `json:"workspace"`
	RecipientAgent string   `json:"recipient_agent"`
	Thread         string   `json:"thread,omitempty"`
	Project        string   `json:"project,omitempty"`
	Task           string   `json:"task,omitempty"`
	Kind           string   `json:"kind"`
	Subject        string   `json:"subject,omitempty"`
	Body           string   `json:"body"`
	ArtifactIDs    []string `json:"artifact_ids"`
	ReplyToMessage string   `json:"reply_to_message,omitempty"`
	IdempotencyKey string   `json:"idempotency_key"`
}

type MessageSendResult struct {
	Schema        string                 `json:"schema"`
	Type          string                 `json:"type"`
	Mutation      domain.MessageMutation `json:"mutation"`
	EventSequence int64                  `json:"event_sequence"`
}

type InboxListParams struct {
	Workspace string `json:"workspace"`
	Agent     string `json:"agent"`
	Limit     int    `json:"limit,omitempty"`
}

type InboxListResult struct {
	Schema string             `json:"schema"`
	Type   string             `json:"type"`
	Agent  string             `json:"agent"`
	Items  []domain.InboxItem `json:"items"`
}

// ThreadParticipantParams binds one enabled workspace agent to the task whose
// project it represents in a participant thread. Owner identity is deliberately
// absent: the daemon/store authority boundary supplies it.
type ThreadParticipantParams struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
}

type ThreadCreateParams struct {
	Workspace      string                    `json:"workspace"`
	Subject        string                    `json:"subject"`
	Participants   []ThreadParticipantParams `json:"participants"`
	IdempotencyKey string                    `json:"idempotency_key"`
}

type ThreadInviteParams struct {
	Workspace                   string                  `json:"workspace"`
	Thread                      string                  `json:"thread"`
	Participant                 ThreadParticipantParams `json:"participant"`
	ExpectedParticipantRevision int64                   `json:"expected_participant_revision"`
	IdempotencyKey              string                  `json:"idempotency_key"`
}

type ParticipantThreadMutationResult struct {
	Schema        string                   `json:"schema"`
	Type          string                   `json:"type"`
	Collaboration domain.ParticipantThread `json:"collaboration"`
	EventSequence int64                    `json:"event_sequence"`
}

type ParticipantThreadResult struct {
	Schema        string                   `json:"schema"`
	Type          string                   `json:"type"`
	Collaboration domain.ParticipantThread `json:"collaboration"`
}

type ThreadQueryParams struct {
	Workspace string `json:"workspace"`
	Thread    string `json:"thread"`
}

type ThreadShowResult struct {
	Schema string              `json:"schema"`
	Type   string              `json:"type"`
	Detail domain.ThreadDetail `json:"detail"`
}

type RunStartParams struct {
	Workspace                       string              `json:"workspace"`
	Task                            string              `json:"task"`
	Checkout                        string              `json:"checkout,omitempty"`
	Context                         string              `json:"context,omitempty"`
	Runtime                         string              `json:"runtime"`
	Provider                        string              `json:"provider"`
	Scenario                        domain.FakeScenario `json:"scenario"`
	ExpectedTaskRevision            int64               `json:"expected_task_revision"`
	CheckWatchGrant                 string              `json:"check_watch_grant,omitempty"`
	ExpectedCheckWatchGrantRevision int64               `json:"expected_check_watch_grant_revision,omitempty"`
	IdempotencyKey                  string              `json:"idempotency_key"`
}

type RunQueryParams struct {
	Workspace string `json:"workspace"`
	Run       string `json:"run"`
}

type RunListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	Task      string `json:"task,omitempty"`
	Status    string `json:"status,omitempty"`
	PageParams
}

type RunResumeParams struct {
	Workspace        string `json:"workspace"`
	Run              string `json:"run"`
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type RunStopParams struct {
	Workspace         string `json:"workspace"`
	Run               string `json:"run"`
	ExpectedRevision  int64  `json:"expected_revision"`
	GracePeriodMillis int64  `json:"grace_period_millis"`
	IdempotencyKey    string `json:"idempotency_key"`
}

type RunLostResolveParams struct {
	Workspace               string `json:"workspace"`
	Run                     string `json:"run"`
	ExpectedRevision        int64  `json:"expected_revision"`
	Note                    string `json:"note"`
	RuntimeRetiredConfirmed bool   `json:"runtime_retired_confirmed"`
	IdempotencyKey          string `json:"idempotency_key"`
}

type RunLogsParams struct {
	Workspace string `json:"workspace"`
	Run       string `json:"run"`
	Tail      int    `json:"tail"`
}

type RunPromptParams struct {
	Workspace string `json:"workspace"`
	Run       string `json:"run"`
	Text      string `json:"text"`
}

type RunAttachParams struct {
	Workspace string `json:"workspace"`
	Run       string `json:"run"`
}

type RunMutationResult struct {
	Schema        string           `json:"schema"`
	Type          string           `json:"type"`
	Detail        domain.RunDetail `json:"detail"`
	EventSequence int64            `json:"event_sequence"`
}

type RunLossResolutionResult struct {
	Schema        string                   `json:"schema"`
	Type          string                   `json:"type"`
	Detail        domain.RunDetail         `json:"detail"`
	Resolution    domain.RunLossResolution `json:"resolution"`
	EventSequence int64                    `json:"event_sequence"`
}

type RunShowResult struct {
	Schema string           `json:"schema"`
	Type   string           `json:"type"`
	Detail domain.RunDetail `json:"detail"`
}

type RunListResult struct {
	Schema string              `json:"schema"`
	Type   string              `json:"type"`
	Runs   []domain.RunSummary `json:"runs"`
	PageResult
}

type RunLogsResult struct {
	Schema string         `json:"schema"`
	Type   string         `json:"type"`
	Logs   domain.RunLogs `json:"logs"`
}

type RunControlResult struct {
	Schema  string `json:"schema"`
	Type    string `json:"type"`
	RunID   string `json:"run_id"`
	Runtime string `json:"runtime"`
	Action  string `json:"action"`
	Status  string `json:"status"`
}

type RunAttachResult struct {
	Schema      string            `json:"schema"`
	Type        string            `json:"type"`
	RunID       string            `json:"run_id"`
	Runtime     string            `json:"runtime"`
	Executable  string            `json:"executable"`
	Arguments   []string          `json:"arguments"`
	Environment map[string]string `json:"environment,omitempty"`
}

type CoordinationStatusParams struct {
	Workspace string `json:"workspace"`
}

type CoordinationStatusResult struct {
	Schema    string                    `json:"schema"`
	Type      string                    `json:"type"`
	Workspace string                    `json:"workspace"`
	Status    domain.CoordinationStatus `json:"status"`
}

type EventsListParams struct {
	Workspace string `json:"workspace"`
	After     int64  `json:"after"`
	PageParams
}

type EventsListResult struct {
	Schema      string         `json:"schema"`
	Type        string         `json:"type"`
	WorkspaceID string         `json:"workspace_id"`
	HighWater   int64          `json:"high_water"`
	Events      []domain.Event `json:"events"`
	PageResult
}

type EventsTimelineParams struct {
	Workspace  string `json:"workspace"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	PageParams
}

type EventsTimelineResult struct {
	Schema      string         `json:"schema"`
	Type        string         `json:"type"`
	WorkspaceID string         `json:"workspace_id"`
	HighWater   int64          `json:"high_water"`
	Events      []domain.Event `json:"events"`
	PageResult
}

// MarshalResult constructs a response without exposing server-only wire types.
func MarshalResult(id string, protocol int, result any) Response {
	data, err := json.Marshal(result)
	if err != nil {
		return ErrorResponse(id, protocol, &APIError{
			Code:      "internal_error",
			Message:   fmt.Sprintf("encode local API result: %v", err),
			Retryable: false,
		})
	}
	return Response{ID: id, Protocol: protocol, Result: data}
}

func ErrorResponse(id string, protocol int, apiError *APIError) Response {
	return Response{ID: id, Protocol: protocol, Error: apiError}
}
