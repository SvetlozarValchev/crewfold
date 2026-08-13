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
	MethodDatabaseStatus        = "database.status"
	MethodWorkspaceInit         = "workspace.init"
	MethodWorkspaceShow         = "workspace.show"
	MethodProjectAdd            = "project.add"
	MethodProjectInspect        = "project.inspect"
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
	MethodKnowledgePropose      = "knowledge.propose"
	MethodKnowledgeShow         = "knowledge.show"
	MethodKnowledgeList         = "knowledge.list"
	MethodKnowledgeSearch       = "knowledge.search"
	MethodKnowledgeIndexStatus  = "knowledge.index.status"
	MethodKnowledgeIndexRebuild = "knowledge.index.rebuild"
	MethodKnowledgeAccept       = "knowledge.accept"
	MethodKnowledgeReject       = "knowledge.reject"
	MethodKnowledgeMarkStale    = "knowledge.mark_stale"
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
	MethodEventsList            = "events.list"

	StatusSchema                    = "urn:crewfold:schema:local-api:status-result:v1"
	StopSchema                      = "urn:crewfold:schema:local-api:stop-result:v1"
	DatabaseStatusSchema            = "urn:crewfold:schema:local-api:database-status-result:v1"
	WorkspaceInitSchema             = "urn:crewfold:schema:local-api:workspace-init-result:v1"
	WorkspaceShowSchema             = "urn:crewfold:schema:local-api:workspace-show-result:v1"
	ProjectAddSchema                = "urn:crewfold:schema:local-api:project-add-result:v1"
	ProjectInspectSchema            = "urn:crewfold:schema:local-api:project-inspect-result:v1"
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
	ContextBuildSchema              = "urn:crewfold:schema:local-api:context-build-result:v3"
	ContextShowSchema               = "urn:crewfold:schema:local-api:context-show-result:v3"
	ContextExplainSchema            = "urn:crewfold:schema:local-api:context-explain-result:v2"
	KnowledgeMutationSchema         = "urn:crewfold:schema:local-api:knowledge-mutation-result:v1"
	KnowledgeShowSchema             = "urn:crewfold:schema:local-api:knowledge-show-result:v1"
	KnowledgeListSchema             = "urn:crewfold:schema:local-api:knowledge-list-result:v1"
	KnowledgeSearchSchema           = "urn:crewfold:schema:local-api:knowledge-search-result:v1"
	KnowledgeIndexStatusSchema      = "urn:crewfold:schema:local-api:knowledge-index-status-result:v1"
	KnowledgeIndexRebuildSchema     = "urn:crewfold:schema:local-api:knowledge-index-rebuild-result:v1"
	MessageSendSchema               = "urn:crewfold:schema:local-api:message-send-result:v1"
	InboxListSchema                 = "urn:crewfold:schema:local-api:inbox-list-result:v1"
	ParticipantThreadMutationSchema = "urn:crewfold:schema:local-api:participant-thread-mutation-result:v1"
	ParticipantThreadSchema         = "urn:crewfold:schema:local-api:participant-thread-result:v1"
	ThreadShowSchema                = "urn:crewfold:schema:local-api:thread-show-result:v1"
	RunMutationSchema               = "urn:crewfold:schema:local-api:run-mutation-result:v1"
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
	EventsListSchema                = "urn:crewfold:schema:local-api:events-list-result:v1"
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

type DatabaseStatusResult struct {
	Schema              string `json:"schema"`
	Type                string `json:"type"`
	Status              string `json:"status"`
	SchemaVersion       int    `json:"schema_version"`
	LatestSchemaVersion int    `json:"latest_schema_version"`
	JournalMode         string `json:"journal_mode"`
	ForeignKeys         bool   `json:"foreign_keys"`
	IntegrityCheck      string `json:"integrity_check"`
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

type ProjectInspectResult struct {
	Schema       string              `json:"schema"`
	Type         string              `json:"type"`
	Project      domain.Project      `json:"project"`
	Repositories []domain.Repository `json:"repositories"`
	Checkouts    []domain.Checkout   `json:"checkouts"`
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
	Agent     string `json:"agent,omitempty"`
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
	Project   string `json:"project,omitempty"`
	Objective string `json:"objective,omitempty"`
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
	Project   string `json:"project,omitempty"`
	Task      string `json:"task,omitempty"`
	ReadyOnly bool   `json:"ready_only,omitempty"`
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

type ClaimQueryParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	Status    string `json:"status,omitempty"`
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
}

type OverlapQueryParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	Overlap   string `json:"overlap,omitempty"`
	Status    string `json:"status,omitempty"`
}

type OverlapListResult struct {
	Schema   string               `json:"schema"`
	Type     string               `json:"type"`
	Overlaps []domain.WorkOverlap `json:"overlaps"`
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

type DriftQueryParams struct {
	Workspace string `json:"workspace"`
	Status    string `json:"status,omitempty"`
}

type DriftListResult struct {
	Schema string              `json:"schema"`
	Type   string              `json:"type"`
	Drifts []domain.ClaimDrift `json:"drifts"`
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
	Workspace            string              `json:"workspace"`
	Task                 string              `json:"task"`
	Checkout             string              `json:"checkout,omitempty"`
	Context              string              `json:"context,omitempty"`
	Runtime              string              `json:"runtime"`
	Provider             string              `json:"provider"`
	Scenario             domain.FakeScenario `json:"scenario"`
	ExpectedTaskRevision int64               `json:"expected_task_revision"`
	IdempotencyKey       string              `json:"idempotency_key"`
}

type RunQueryParams struct {
	Workspace string `json:"workspace"`
	Run       string `json:"run,omitempty"`
	Task      string `json:"task,omitempty"`
	Status    string `json:"status,omitempty"`
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
	Takeover  bool   `json:"takeover,omitempty"`
}

type RunMutationResult struct {
	Schema        string           `json:"schema"`
	Type          string           `json:"type"`
	Detail        domain.RunDetail `json:"detail"`
	EventSequence int64            `json:"event_sequence"`
}

type RunShowResult struct {
	Schema string           `json:"schema"`
	Type   string           `json:"type"`
	Detail domain.RunDetail `json:"detail"`
}

type RunListResult struct {
	Schema string             `json:"schema"`
	Type   string             `json:"type"`
	Runs   []domain.RunDetail `json:"runs"`
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
	After *int64 `json:"after"`
	Limit *int   `json:"limit,omitempty"`
}

type EventsListResult struct {
	Schema    string         `json:"schema"`
	Type      string         `json:"type"`
	After     int64          `json:"after"`
	NextAfter int64          `json:"next_after"`
	HasMore   bool           `json:"has_more"`
	Events    []domain.Event `json:"events"`
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
