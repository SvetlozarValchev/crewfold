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

	MethodHello              = "system.hello"
	MethodStatus             = "system.status"
	MethodStop               = "system.stop"
	MethodDatabaseStatus     = "database.status"
	MethodWorkspaceInit      = "workspace.init"
	MethodWorkspaceShow      = "workspace.show"
	MethodProjectAdd         = "project.add"
	MethodProjectInspect     = "project.inspect"
	MethodCheckoutAdd        = "checkout.add"
	MethodCheckoutList       = "checkout.list"
	MethodAgentCreate        = "agent.create"
	MethodAgentUpdate        = "agent.update"
	MethodAgentShow          = "agent.show"
	MethodAgentList          = "agent.list"
	MethodObjectiveCreate    = "objective.create"
	MethodObjectiveUpdate    = "objective.update"
	MethodObjectiveShow      = "objective.show"
	MethodObjectiveList      = "objective.list"
	MethodTaskCreate         = "task.create"
	MethodTaskUpdate         = "task.update"
	MethodTaskShow           = "task.show"
	MethodTaskList           = "task.list"
	MethodTaskDepend         = "task.dependency.add"
	MethodTaskAssign         = "task.assign"
	MethodTaskTransition     = "task.transition"
	MethodCoordinationStatus = "coordination.status"
	MethodEventsList         = "events.list"

	StatusSchema             = "urn:crewfold:schema:local-api:status-result:v1"
	StopSchema               = "urn:crewfold:schema:local-api:stop-result:v1"
	DatabaseStatusSchema     = "urn:crewfold:schema:local-api:database-status-result:v1"
	WorkspaceInitSchema      = "urn:crewfold:schema:local-api:workspace-init-result:v1"
	WorkspaceShowSchema      = "urn:crewfold:schema:local-api:workspace-show-result:v1"
	ProjectAddSchema         = "urn:crewfold:schema:local-api:project-add-result:v1"
	ProjectInspectSchema     = "urn:crewfold:schema:local-api:project-inspect-result:v1"
	CheckoutAddSchema        = "urn:crewfold:schema:local-api:checkout-add-result:v1"
	CheckoutListSchema       = "urn:crewfold:schema:local-api:checkout-list-result:v1"
	AgentMutationSchema      = "urn:crewfold:schema:local-api:agent-mutation-result:v1"
	AgentShowSchema          = "urn:crewfold:schema:local-api:agent-show-result:v1"
	AgentListSchema          = "urn:crewfold:schema:local-api:agent-list-result:v1"
	ObjectiveMutationSchema  = "urn:crewfold:schema:local-api:objective-mutation-result:v1"
	ObjectiveShowSchema      = "urn:crewfold:schema:local-api:objective-show-result:v1"
	ObjectiveListSchema      = "urn:crewfold:schema:local-api:objective-list-result:v1"
	TaskMutationSchema       = "urn:crewfold:schema:local-api:task-mutation-result:v1"
	TaskShowSchema           = "urn:crewfold:schema:local-api:task-show-result:v1"
	TaskListSchema           = "urn:crewfold:schema:local-api:task-list-result:v1"
	CoordinationStatusSchema = "urn:crewfold:schema:local-api:coordination-status-result:v1"
	EventsListSchema         = "urn:crewfold:schema:local-api:events-list-result:v1"
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
