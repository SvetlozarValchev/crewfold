package localapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"crewfold/internal/domain"
)

const (
	defaultTimeout           = 2 * time.Second
	portableKnowledgeTimeout = 2 * time.Minute
	maximumResponseBytes     = 16 * 1024 * 1024
)

// ErrProtocolMismatch identifies a fatal local API negotiation failure.
var ErrProtocolMismatch = errors.New("local API protocol mismatch")

// ProtocolMismatchError records a protocol selected outside the client's
// supported range. It unwraps to ErrProtocolMismatch for errors.Is callers.
type ProtocolMismatchError struct {
	Selected  int
	ClientMin int
	ClientMax int
}

func (e *ProtocolMismatchError) Error() string {
	return fmt.Sprintf("daemon selected unsupported protocol %d; client supports %d through %d", e.Selected, e.ClientMin, e.ClientMax)
}

func (e *ProtocolMismatchError) Unwrap() error { return ErrProtocolMismatch }

type Client struct {
	socketPath string
	timeout    time.Duration
}

func NewClient(socketPath string) *Client {
	return &Client{socketPath: socketPath, timeout: defaultTimeout}
}

// WithTimeout returns a concrete client clone with the requested per-call
// connection deadline. It does not mutate a client that may be shared by the
// event poller and slower canonical refreshes.
func (c *Client) WithTimeout(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{socketPath: c.socketPath, timeout: timeout}
}

func (c *Client) Hello(ctx context.Context) (HelloResult, error) {
	connection, cancel, err := c.dial(ctx)
	if err != nil {
		return HelloResult{}, err
	}
	defer cancel()
	defer connection.Close()

	return negotiate(connection)
}

func (c *Client) Status(ctx context.Context) (StatusResult, error) {
	connection, cancel, err := c.dial(ctx)
	if err != nil {
		return StatusResult{}, err
	}
	defer cancel()
	defer connection.Close()

	hello, err := negotiate(connection)
	if err != nil {
		return StatusResult{}, err
	}

	var result StatusResult
	if err := roundTrip(connection, Request{
		ID:       requestID(),
		Protocol: hello.SelectedProtocol,
		Method:   MethodStatus,
	}, &result); err != nil {
		return StatusResult{}, err
	}
	return result, nil
}

func (c *Client) Stop(ctx context.Context) (StopResult, error) {
	connection, cancel, err := c.dial(ctx)
	if err != nil {
		return StopResult{}, err
	}
	defer cancel()
	defer connection.Close()

	hello, err := negotiate(connection)
	if err != nil {
		return StopResult{}, err
	}

	var result StopResult
	if err := roundTrip(connection, Request{
		ID:       requestID(),
		Protocol: hello.SelectedProtocol,
		Method:   MethodStop,
	}, &result); err != nil {
		return StopResult{}, err
	}
	return result, nil
}

func (c *Client) DatabaseStatus(ctx context.Context) (DatabaseStatusResult, error) {
	var result DatabaseStatusResult
	if err := c.call(ctx, MethodDatabaseStatus, nil, &result); err != nil {
		return DatabaseStatusResult{}, err
	}
	return result, nil
}

func (c *Client) WorkspaceInit(ctx context.Context, name, idempotencyKey string) (WorkspaceInitResult, error) {
	if idempotencyKey == "" {
		idempotencyKey = "idem-" + requestID()
	}
	params, err := json.Marshal(WorkspaceInitParams{Name: name, IdempotencyKey: idempotencyKey})
	if err != nil {
		return WorkspaceInitResult{}, fmt.Errorf("marshal workspace initialization: %w", err)
	}
	var result WorkspaceInitResult
	if err := c.call(ctx, MethodWorkspaceInit, params, &result); err != nil {
		return WorkspaceInitResult{}, err
	}
	return result, nil
}

func (c *Client) WorkspaceShow(ctx context.Context, identifier string) (WorkspaceShowResult, error) {
	var result WorkspaceShowResult
	if err := c.callParamsStrict(ctx, MethodWorkspaceShow, WorkspaceShowParams{Identifier: identifier}, &result); err != nil {
		return WorkspaceShowResult{}, err
	}
	return result, nil
}

func (c *Client) WorkspaceList(ctx context.Context, params WorkspaceListParams) (WorkspaceListResult, error) {
	var result WorkspaceListResult
	if err := c.callParamsStrict(ctx, MethodWorkspaceList, params, &result); err != nil {
		return WorkspaceListResult{}, err
	}
	return result, nil
}

func (c *Client) ProjectAdd(ctx context.Context, workspace, name, repositoryPath, writeMode, idempotencyKey string) (ProjectAddResult, error) {
	if idempotencyKey == "" {
		idempotencyKey = "idem-" + requestID()
	}
	params, err := json.Marshal(ProjectAddParams{Workspace: workspace, Name: name, RepositoryPath: repositoryPath, WriteMode: writeMode, IdempotencyKey: idempotencyKey})
	if err != nil {
		return ProjectAddResult{}, fmt.Errorf("marshal project registration: %w", err)
	}
	var result ProjectAddResult
	if err := c.call(ctx, MethodProjectAdd, params, &result); err != nil {
		return ProjectAddResult{}, err
	}
	return result, nil
}

func (c *Client) ProjectInspect(ctx context.Context, workspace, project string) (ProjectInspectResult, error) {
	params, err := json.Marshal(ProjectInspectParams{Workspace: workspace, Project: project})
	if err != nil {
		return ProjectInspectResult{}, fmt.Errorf("marshal project inspection: %w", err)
	}
	var result ProjectInspectResult
	if err := c.call(ctx, MethodProjectInspect, params, &result); err != nil {
		return ProjectInspectResult{}, err
	}
	return result, nil
}

func (c *Client) ProjectShow(ctx context.Context, workspace, project string) (ProjectShowResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, workspace)
	if err != nil {
		return ProjectShowResult{}, err
	}
	var result ProjectShowResult
	if err := c.callParamsStrict(ctx, MethodProjectShow, ProjectShowParams{Workspace: workspaceID, Project: project}, &result); err != nil {
		return ProjectShowResult{}, err
	}
	return result, nil
}

func (c *Client) ProjectList(ctx context.Context, params ProjectListParams) (ProjectListResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, params.Workspace)
	if err != nil {
		return ProjectListResult{}, err
	}
	params.Workspace = workspaceID
	var result ProjectListResult
	if err := c.callParamsStrict(ctx, MethodProjectList, params, &result); err != nil {
		return ProjectListResult{}, err
	}
	return result, nil
}

func (c *Client) CheckoutAdd(ctx context.Context, workspace, project, repositoryPath, writeMode, idempotencyKey string) (CheckoutAddResult, error) {
	if idempotencyKey == "" {
		idempotencyKey = "idem-" + requestID()
	}
	params, err := json.Marshal(CheckoutAddParams{Workspace: workspace, Project: project, RepositoryPath: repositoryPath, WriteMode: writeMode, IdempotencyKey: idempotencyKey})
	if err != nil {
		return CheckoutAddResult{}, fmt.Errorf("marshal checkout registration: %w", err)
	}
	var result CheckoutAddResult
	if err := c.call(ctx, MethodCheckoutAdd, params, &result); err != nil {
		return CheckoutAddResult{}, err
	}
	return result, nil
}

func (c *Client) CheckoutList(ctx context.Context, workspace, project string) (CheckoutListResult, error) {
	params, err := json.Marshal(CheckoutListParams{Workspace: workspace, Project: project})
	if err != nil {
		return CheckoutListResult{}, fmt.Errorf("marshal checkout query: %w", err)
	}
	var result CheckoutListResult
	if err := c.call(ctx, MethodCheckoutList, params, &result); err != nil {
		return CheckoutListResult{}, err
	}
	return result, nil
}

func (c *Client) AgentCreate(ctx context.Context, paramsValue AgentCreateParams) (AgentMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result AgentMutationResult
	if err := c.callParams(ctx, MethodAgentCreate, paramsValue, &result); err != nil {
		return AgentMutationResult{}, err
	}
	return result, nil
}

func (c *Client) AgentUpdate(ctx context.Context, paramsValue AgentUpdateParams) (AgentMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result AgentMutationResult
	if err := c.callParams(ctx, MethodAgentUpdate, paramsValue, &result); err != nil {
		return AgentMutationResult{}, err
	}
	return result, nil
}

func (c *Client) AgentShow(ctx context.Context, workspace, agent string) (AgentShowResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, workspace)
	if err != nil {
		return AgentShowResult{}, err
	}
	var result AgentShowResult
	if err := c.callParamsStrict(ctx, MethodAgentShow, AgentQueryParams{Workspace: workspaceID, Agent: agent}, &result); err != nil {
		return AgentShowResult{}, err
	}
	return result, nil
}

func (c *Client) AgentList(ctx context.Context, params AgentListParams) (AgentListResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, params.Workspace)
	if err != nil {
		return AgentListResult{}, err
	}
	params.Workspace = workspaceID
	var result AgentListResult
	if err := c.callParamsStrict(ctx, MethodAgentList, params, &result); err != nil {
		return AgentListResult{}, err
	}
	return result, nil
}

func (c *Client) ObjectiveCreate(ctx context.Context, paramsValue ObjectiveCreateParams) (ObjectiveMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result ObjectiveMutationResult
	if err := c.callParams(ctx, MethodObjectiveCreate, paramsValue, &result); err != nil {
		return ObjectiveMutationResult{}, err
	}
	return result, nil
}

func (c *Client) ObjectiveUpdate(ctx context.Context, paramsValue ObjectiveUpdateParams) (ObjectiveMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result ObjectiveMutationResult
	if err := c.callParams(ctx, MethodObjectiveUpdate, paramsValue, &result); err != nil {
		return ObjectiveMutationResult{}, err
	}
	return result, nil
}

func (c *Client) ObjectiveShow(ctx context.Context, workspace, objective string) (ObjectiveShowResult, error) {
	var result ObjectiveShowResult
	if err := c.callParams(ctx, MethodObjectiveShow, ObjectiveQueryParams{Workspace: workspace, Objective: objective}, &result); err != nil {
		return ObjectiveShowResult{}, err
	}
	return result, nil
}

func (c *Client) ObjectiveList(ctx context.Context, params ObjectiveListParams) (ObjectiveListResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return ObjectiveListResult{}, err
	}
	params.Workspace, params.Project = workspaceID, projectID
	var result ObjectiveListResult
	if err := c.callParamsStrict(ctx, MethodObjectiveList, params, &result); err != nil {
		return ObjectiveListResult{}, err
	}
	return result, nil
}

func (c *Client) TaskCreate(ctx context.Context, paramsValue TaskCreateParams) (TaskMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result TaskMutationResult
	if err := c.callParams(ctx, MethodTaskCreate, paramsValue, &result); err != nil {
		return TaskMutationResult{}, err
	}
	return result, nil
}

func (c *Client) TaskUpdate(ctx context.Context, paramsValue TaskUpdateParams) (TaskMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result TaskMutationResult
	if err := c.callParams(ctx, MethodTaskUpdate, paramsValue, &result); err != nil {
		return TaskMutationResult{}, err
	}
	return result, nil
}

func (c *Client) TaskShow(ctx context.Context, workspace, task string) (TaskShowResult, error) {
	var result TaskShowResult
	if err := c.callParams(ctx, MethodTaskShow, TaskQueryParams{Workspace: workspace, Task: task}, &result); err != nil {
		return TaskShowResult{}, err
	}
	return result, nil
}

func (c *Client) TaskList(ctx context.Context, params TaskListParams) (TaskListResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return TaskListResult{}, err
	}
	params.Workspace, params.Project = workspaceID, projectID
	var result TaskListResult
	if err := c.callParamsStrict(ctx, MethodTaskList, params, &result); err != nil {
		return TaskListResult{}, err
	}
	return result, nil
}

func (c *Client) TaskDepend(ctx context.Context, paramsValue TaskDependencyParams) (TaskMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result TaskMutationResult
	if err := c.callParams(ctx, MethodTaskDepend, paramsValue, &result); err != nil {
		return TaskMutationResult{}, err
	}
	return result, nil
}

func (c *Client) TaskAssign(ctx context.Context, paramsValue TaskAssignParams) (TaskMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result TaskMutationResult
	if err := c.callParams(ctx, MethodTaskAssign, paramsValue, &result); err != nil {
		return TaskMutationResult{}, err
	}
	return result, nil
}

func (c *Client) TaskTransition(ctx context.Context, paramsValue TaskTransitionParams) (TaskMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result TaskMutationResult
	if err := c.callParams(ctx, MethodTaskTransition, paramsValue, &result); err != nil {
		return TaskMutationResult{}, err
	}
	return result, nil
}

func (c *Client) TaskTimeline(ctx context.Context, workspace, task string) (TaskTimelineResult, error) {
	var result TaskTimelineResult
	if err := c.callParams(ctx, MethodTaskTimeline, TaskTimelineParams{Workspace: workspace, Task: task}, &result); err != nil {
		return TaskTimelineResult{}, err
	}
	return result, nil
}

func (c *Client) ContextBuild(ctx context.Context, paramsValue ContextBuildParams) (ContextBuildResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	if paramsValue.KnowledgeRevisionIDs == nil {
		paramsValue.KnowledgeRevisionIDs = []string{}
	}
	var result ContextBuildResult
	if err := c.callParams(ctx, MethodContextBuild, paramsValue, &result); err != nil {
		return ContextBuildResult{}, err
	}
	return result, nil
}

func (c *Client) ContextShow(ctx context.Context, workspace, contextID string) (ContextShowResult, error) {
	var result ContextShowResult
	if err := c.callParamsStrict(ctx, MethodContextShow, ContextQueryParams{Workspace: workspace, Context: contextID}, &result); err != nil {
		return ContextShowResult{}, err
	}
	if err := validateContextShowResult(result); err != nil {
		return ContextShowResult{}, err
	}
	return result, nil
}

func validateContextShowResult(result ContextShowResult) error {
	if result.Schema != ContextShowSchema || result.Type != "context_packet" || result.Packet.Schema != domain.ContextPacketSchema {
		return fmt.Errorf("context.show returned unexpected result schema %q or type %q for packet schema %q", result.Schema, result.Type, result.Packet.Schema)
	}
	return nil
}

func (c *Client) ContextExplain(ctx context.Context, workspace, contextID string) (ContextExplainResult, error) {
	var result ContextExplainResult
	if err := c.callParamsStrict(ctx, MethodContextExplain, ContextQueryParams{Workspace: workspace, Context: contextID}, &result); err != nil {
		return ContextExplainResult{}, err
	}
	if result.Schema != ContextExplainSchema || result.Type != "context_explanation" {
		return ContextExplainResult{}, fmt.Errorf("context.explain returned unexpected result schema %q or type %q", result.Schema, result.Type)
	}
	return result, nil
}

func (c *Client) ContextRefresh(ctx context.Context, params ContextRefreshParams) (ContextRefreshResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ContextRefreshResult
	if err := c.callParamsStrict(ctx, MethodContextRefresh, params, &result); err != nil {
		return ContextRefreshResult{}, err
	}
	if result.Schema != ContextRefreshSchema || result.Type != "context_refresh" {
		return ContextRefreshResult{}, fmt.Errorf("context.refresh returned unexpected result schema %q or type %q", result.Schema, result.Type)
	}
	return result, nil
}

func (c *Client) ContextDeltaList(ctx context.Context, params ContextDeltaListParams) (ContextDeltaListResult, error) {
	var result ContextDeltaListResult
	if err := c.callParamsStrict(ctx, MethodContextDeltaList, params, &result); err != nil {
		return ContextDeltaListResult{}, err
	}
	if result.Schema != ContextDeltaListSchema || result.Type != "context_delta_list" {
		return ContextDeltaListResult{}, fmt.Errorf("context.delta.list returned unexpected result schema %q or type %q", result.Schema, result.Type)
	}
	return result, nil
}

func (c *Client) ContextDeltaShow(ctx context.Context, workspace, deltaID string) (ContextDeltaShowResult, error) {
	var result ContextDeltaShowResult
	if err := c.callParamsStrict(ctx, MethodContextDeltaShow, ContextDeltaQueryParams{Workspace: workspace, Delta: deltaID}, &result); err != nil {
		return ContextDeltaShowResult{}, err
	}
	if result.Schema != ContextDeltaShowSchema || result.Type != "context_delta" {
		return ContextDeltaShowResult{}, fmt.Errorf("context.delta.show returned unexpected result schema %q or type %q", result.Schema, result.Type)
	}
	return result, nil
}

func (c *Client) ContextDeltaExplain(ctx context.Context, workspace, deltaID string) (ContextDeltaExplainResult, error) {
	var result ContextDeltaExplainResult
	if err := c.callParamsStrict(ctx, MethodContextDeltaExplain, ContextDeltaQueryParams{Workspace: workspace, Delta: deltaID}, &result); err != nil {
		return ContextDeltaExplainResult{}, err
	}
	if result.Schema != ContextDeltaExplainSchema || result.Type != "context_delta_explanation" {
		return ContextDeltaExplainResult{}, fmt.Errorf("context.delta.explain returned unexpected result schema %q or type %q", result.Schema, result.Type)
	}
	return result, nil
}

func (c *Client) KnowledgePropose(ctx context.Context, params KnowledgeProposeParams) (KnowledgeMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	if params.Sources == nil {
		params.Sources = []domain.KnowledgeSourceInput{}
	}
	var result KnowledgeMutationResult
	if err := c.callParams(ctx, MethodKnowledgePropose, params, &result); err != nil {
		return KnowledgeMutationResult{}, err
	}
	return result, nil
}

func (c *Client) KnowledgeShow(ctx context.Context, workspace, revision string) (KnowledgeShowResult, error) {
	var result KnowledgeShowResult
	if err := c.callParams(ctx, MethodKnowledgeShow, KnowledgeQueryParams{Workspace: workspace, KnowledgeRevision: revision}, &result); err != nil {
		return KnowledgeShowResult{}, err
	}
	return result, nil
}

func (c *Client) KnowledgeList(ctx context.Context, params KnowledgeListParams) (KnowledgeListResult, error) {
	var result KnowledgeListResult
	if err := c.callParams(ctx, MethodKnowledgeList, params, &result); err != nil {
		return KnowledgeListResult{}, err
	}
	return result, nil
}

func (c *Client) KnowledgeSearch(ctx context.Context, params KnowledgeSearchParams) (KnowledgeSearchResult, error) {
	if params.Limit == nil {
		limit := 20
		params.Limit = &limit
	}
	var result KnowledgeSearchResult
	if err := c.callParams(ctx, MethodKnowledgeSearch, params, &result); err != nil {
		return KnowledgeSearchResult{}, err
	}
	return result, nil
}

func (c *Client) KnowledgeIndexStatus(ctx context.Context, workspace string) (KnowledgeIndexStatusResult, error) {
	var result KnowledgeIndexStatusResult
	if err := c.callParams(ctx, MethodKnowledgeIndexStatus, KnowledgeIndexStatusParams{Workspace: workspace}, &result); err != nil {
		return KnowledgeIndexStatusResult{}, err
	}
	return result, nil
}

func (c *Client) KnowledgeIndexRebuild(ctx context.Context, params KnowledgeIndexRebuildParams) (KnowledgeIndexRebuildResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result KnowledgeIndexRebuildResult
	if err := c.callParams(ctx, MethodKnowledgeIndexRebuild, params, &result); err != nil {
		return KnowledgeIndexRebuildResult{}, err
	}
	return result, nil
}

func (c *Client) KnowledgeAccept(ctx context.Context, params KnowledgeDecisionParams) (KnowledgeMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result KnowledgeMutationResult
	if err := c.callParams(ctx, MethodKnowledgeAccept, params, &result); err != nil {
		return KnowledgeMutationResult{}, err
	}
	return result, nil
}

func (c *Client) KnowledgeReject(ctx context.Context, params KnowledgeDecisionParams) (KnowledgeMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result KnowledgeMutationResult
	if err := c.callParams(ctx, MethodKnowledgeReject, params, &result); err != nil {
		return KnowledgeMutationResult{}, err
	}
	return result, nil
}

func (c *Client) KnowledgeMarkStale(ctx context.Context, params KnowledgeMarkStaleParams) (KnowledgeMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result KnowledgeMutationResult
	if err := c.callParams(ctx, MethodKnowledgeMarkStale, params, &result); err != nil {
		return KnowledgeMutationResult{}, err
	}
	return result, nil
}

func (c *Client) KnowledgeDispute(ctx context.Context, workspace, revision string) (KnowledgeDisputeResult, error) {
	var result KnowledgeDisputeResult
	if err := c.callParams(ctx, MethodKnowledgeDispute, KnowledgeQueryParams{
		Workspace: workspace, KnowledgeRevision: revision,
	}, &result); err != nil {
		return KnowledgeDisputeResult{}, err
	}
	return result, nil
}

func (c *Client) KnowledgeExport(ctx context.Context, params KnowledgeExportParams) (KnowledgeExportResult, error) {
	var result KnowledgeExportResult
	if err := c.callParamsStrictWithTimeout(ctx, portableKnowledgeTimeout, MethodKnowledgeExport, params, &result); err != nil {
		return KnowledgeExportResult{}, err
	}
	if result.Schema != KnowledgeExportSchema || result.Type != "knowledge_export" {
		return KnowledgeExportResult{}, fmt.Errorf("knowledge.export returned unexpected result schema %q or type %q", result.Schema, result.Type)
	}
	return result, nil
}

func (c *Client) KnowledgeImport(ctx context.Context, params KnowledgeImportParams) (KnowledgeImportResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result KnowledgeImportResult
	if err := c.callParamsStrictWithTimeout(ctx, portableKnowledgeTimeout, MethodKnowledgeImport, params, &result); err != nil {
		return KnowledgeImportResult{}, err
	}
	if result.Schema != KnowledgeImportSchema || result.Type != "knowledge_import" {
		return KnowledgeImportResult{}, fmt.Errorf("knowledge.import returned unexpected result schema %q or type %q", result.Schema, result.Type)
	}
	return result, nil
}

func (c *Client) CuratorQueue(ctx context.Context, params CuratorQueueParams) (CuratorQueueResult, error) {
	if params.Limit == nil {
		limit := 50
		params.Limit = &limit
	}
	var result CuratorQueueResult
	if err := c.callParams(ctx, MethodCuratorQueue, params, &result); err != nil {
		return CuratorQueueResult{}, err
	}
	return result, nil
}

func (c *Client) CuratorRuleConfigure(ctx context.Context, params CuratorRuleConfigureParams) (CuratorRuleMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CuratorRuleMutationResult
	if err := c.callParams(ctx, MethodCuratorRuleConfigure, params, &result); err != nil {
		return CuratorRuleMutationResult{}, err
	}
	return result, nil
}

func (c *Client) CuratorProcess(ctx context.Context, params CuratorProcessParams) (CuratorProcessResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CuratorProcessResult
	if err := c.callParams(ctx, MethodCuratorProcess, params, &result); err != nil {
		return CuratorProcessResult{}, err
	}
	return result, nil
}

func (c *Client) ContradictionReport(ctx context.Context, params ContradictionReportParams) (ContradictionMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ContradictionMutationResult
	if err := c.callParams(ctx, MethodContradictionReport, params, &result); err != nil {
		return ContradictionMutationResult{}, err
	}
	return result, nil
}

func (c *Client) ContradictionShow(ctx context.Context, workspace, contradiction string) (ContradictionShowResult, error) {
	var result ContradictionShowResult
	if err := c.callParams(ctx, MethodContradictionShow, ContradictionQueryParams{
		Workspace: workspace, Contradiction: contradiction,
	}, &result); err != nil {
		return ContradictionShowResult{}, err
	}
	return result, nil
}

func (c *Client) ContradictionList(ctx context.Context, params ContradictionListParams) (ContradictionListResult, error) {
	if params.Limit == nil {
		limit := 50
		params.Limit = &limit
	}
	var result ContradictionListResult
	if err := c.callParams(ctx, MethodContradictionList, params, &result); err != nil {
		return ContradictionListResult{}, err
	}
	return result, nil
}

func (c *Client) ContradictionConfirm(ctx context.Context, params ContradictionDecisionParams) (ContradictionMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ContradictionMutationResult
	if err := c.callParams(ctx, MethodContradictionConfirm, params, &result); err != nil {
		return ContradictionMutationResult{}, err
	}
	return result, nil
}

func (c *Client) ContradictionDismiss(ctx context.Context, params ContradictionDecisionParams) (ContradictionMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ContradictionMutationResult
	if err := c.callParams(ctx, MethodContradictionDismiss, params, &result); err != nil {
		return ContradictionMutationResult{}, err
	}
	return result, nil
}

func (c *Client) MessageSend(ctx context.Context, paramsValue MessageSendParams) (MessageSendResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	if paramsValue.ArtifactIDs == nil {
		paramsValue.ArtifactIDs = []string{}
	}
	var result MessageSendResult
	if err := c.callParams(ctx, MethodMessageSend, paramsValue, &result); err != nil {
		return MessageSendResult{}, err
	}
	return result, nil
}

func (c *Client) InboxList(ctx context.Context, workspace, agent string, limit int) (InboxListResult, error) {
	effectiveLimit := limit
	if effectiveLimit == 0 {
		effectiveLimit = 20
	}
	if effectiveLimit < 1 || effectiveLimit > 50 {
		return InboxListResult{}, fmt.Errorf("inbox.list limit must be from 1 to 50")
	}
	workspaceID, err := c.resolveOperatorWorkspace(ctx, workspace)
	if err != nil {
		return InboxListResult{}, err
	}
	agentID, err := c.resolveOperatorAgent(ctx, workspaceID, agent)
	if err != nil {
		return InboxListResult{}, err
	}
	var result InboxListResult
	if err := c.callParamsStrict(ctx, MethodInboxList, InboxListParams{Workspace: workspaceID, Agent: agentID, Limit: limit}, &result); err != nil {
		return InboxListResult{}, err
	}
	if result.Schema != InboxListSchema || result.Type != "inbox" || !canonicalAgentIDPattern.MatchString(result.Agent) ||
		result.Agent != agentID || result.Items == nil || len(result.Items) > effectiveLimit {
		return InboxListResult{}, fmt.Errorf("decode local API result %s: result violates the requested inbox scope or bound", MethodInboxList)
	}
	for _, item := range result.Items {
		if item.Message.WorkspaceID != workspaceID ||
			item.Delivery.RecipientAgentID != result.Agent || item.Delivery.MessageID != item.Message.ID {
			return InboxListResult{}, fmt.Errorf("decode local API result %s: inbox item violates message, recipient, or workspace scope", MethodInboxList)
		}
	}
	return result, nil
}

func (c *Client) ThreadCreate(ctx context.Context, paramsValue ThreadCreateParams) (ParticipantThreadMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result ParticipantThreadMutationResult
	if err := c.callParams(ctx, MethodThreadCreate, paramsValue, &result); err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	return result, nil
}

func (c *Client) ThreadInvite(ctx context.Context, paramsValue ThreadInviteParams) (ParticipantThreadMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result ParticipantThreadMutationResult
	if err := c.callParams(ctx, MethodThreadInvite, paramsValue, &result); err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	return result, nil
}

func (c *Client) ThreadParticipants(ctx context.Context, workspace, thread string) (ParticipantThreadResult, error) {
	var result ParticipantThreadResult
	if err := c.callParams(ctx, MethodThreadParticipants, ThreadQueryParams{Workspace: workspace, Thread: thread}, &result); err != nil {
		return ParticipantThreadResult{}, err
	}
	return result, nil
}

func (c *Client) ThreadShow(ctx context.Context, workspace, thread string) (ThreadShowResult, error) {
	var result ThreadShowResult
	if err := c.callParams(ctx, MethodThreadShow, ThreadQueryParams{Workspace: workspace, Thread: thread}, &result); err != nil {
		return ThreadShowResult{}, err
	}
	return result, nil
}

func (c *Client) RunStart(ctx context.Context, paramsValue RunStartParams) (RunMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result RunMutationResult
	if err := c.callParams(ctx, MethodRunStart, paramsValue, &result); err != nil {
		return RunMutationResult{}, err
	}
	return result, nil
}

func (c *Client) RunShow(ctx context.Context, workspace, run string) (RunShowResult, error) {
	var result RunShowResult
	if err := c.callParams(ctx, MethodRunShow, RunQueryParams{Workspace: workspace, Run: run}, &result); err != nil {
		return RunShowResult{}, err
	}
	return result, nil
}

func (c *Client) RunList(ctx context.Context, params RunListParams) (RunListResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return RunListResult{}, err
	}
	params.Workspace, params.Project = workspaceID, projectID
	var result RunListResult
	if err := c.callParamsStrict(ctx, MethodRunList, params, &result); err != nil {
		return RunListResult{}, err
	}
	return result, nil
}

func (c *Client) RunResume(ctx context.Context, paramsValue RunResumeParams) (RunMutationResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, paramsValue.Workspace)
	if err != nil {
		return RunMutationResult{}, err
	}
	paramsValue.Workspace = workspaceID
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result RunMutationResult
	if err := c.callParamsStrict(ctx, MethodRunResume, paramsValue, &result); err != nil {
		return RunMutationResult{}, err
	}
	return result, nil
}

func (c *Client) RunStop(ctx context.Context, paramsValue RunStopParams) (RunMutationResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, paramsValue.Workspace)
	if err != nil {
		return RunMutationResult{}, err
	}
	paramsValue.Workspace = workspaceID
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result RunMutationResult
	if err := c.callParamsStrict(ctx, MethodRunStop, paramsValue, &result); err != nil {
		return RunMutationResult{}, err
	}
	return result, nil
}

func (c *Client) RunLogs(ctx context.Context, workspace, run string, tail int) (RunLogsResult, error) {
	var result RunLogsResult
	if err := c.callParams(ctx, MethodRunLogs, RunLogsParams{Workspace: workspace, Run: run, Tail: tail}, &result); err != nil {
		return RunLogsResult{}, err
	}
	return result, nil
}

func (c *Client) RunPrompt(ctx context.Context, workspace, run, text string) (RunControlResult, error) {
	var result RunControlResult
	if err := c.callParams(ctx, MethodRunPrompt, RunPromptParams{Workspace: workspace, Run: run, Text: text}, &result); err != nil {
		return RunControlResult{}, err
	}
	return result, nil
}

func (c *Client) RunInterrupt(ctx context.Context, workspace, run string) (RunControlResult, error) {
	var result RunControlResult
	if err := c.callParams(ctx, MethodRunInterrupt, RunQueryParams{Workspace: workspace, Run: run}, &result); err != nil {
		return RunControlResult{}, err
	}
	return result, nil
}

func (c *Client) RunAttach(ctx context.Context, workspace, run string) (RunAttachResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, workspace)
	if err != nil {
		return RunAttachResult{}, err
	}
	var result RunAttachResult
	if err := c.callParamsStrict(ctx, MethodRunAttach, RunAttachParams{Workspace: workspaceID, Run: run}, &result); err != nil {
		return RunAttachResult{}, err
	}
	if err := ValidateRunAttachResult(result, run); err != nil {
		return RunAttachResult{}, err
	}
	return result, nil
}

func (c *Client) CoordinationStatus(ctx context.Context, workspace string) (CoordinationStatusResult, error) {
	var result CoordinationStatusResult
	if err := c.callParams(ctx, MethodCoordinationStatus, CoordinationStatusParams{Workspace: workspace}, &result); err != nil {
		return CoordinationStatusResult{}, err
	}
	return result, nil
}

func (c *Client) ClaimAdd(ctx context.Context, params ClaimAddParams) (ClaimMutationResult, error) {
	var result ClaimMutationResult
	if err := c.callParams(ctx, MethodClaimAdd, params, &result); err != nil {
		return ClaimMutationResult{}, err
	}
	return result, nil
}

func (c *Client) ClaimList(ctx context.Context, params ClaimListParams) (ClaimListResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return ClaimListResult{}, err
	}
	params.Workspace, params.Project = workspaceID, projectID
	var result ClaimListResult
	if err := c.callParamsStrict(ctx, MethodClaimList, params, &result); err != nil {
		return ClaimListResult{}, err
	}
	return result, nil
}

func (c *Client) ClaimRelease(ctx context.Context, params ClaimReleaseParams) (ClaimMutationResult, error) {
	var result ClaimMutationResult
	if err := c.callParams(ctx, MethodClaimRelease, params, &result); err != nil {
		return ClaimMutationResult{}, err
	}
	return result, nil
}

func (c *Client) OverlapList(ctx context.Context, params OverlapListParams) (OverlapListResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return OverlapListResult{}, err
	}
	params.Workspace, params.Project = workspaceID, projectID
	var result OverlapListResult
	if err := c.callParamsStrict(ctx, MethodOverlapList, params, &result); err != nil {
		return OverlapListResult{}, err
	}
	return result, nil
}

func (c *Client) OverlapInspect(ctx context.Context, workspace, overlap string) (OverlapInspectResult, error) {
	var result OverlapInspectResult
	if err := c.callParams(ctx, MethodOverlapInspect, OverlapInspectParams{Workspace: workspace, Overlap: overlap}, &result); err != nil {
		return OverlapInspectResult{}, err
	}
	return result, nil
}

func (c *Client) OverlapScan(ctx context.Context, workspace, project string) (OverlapScanResult, error) {
	var result OverlapScanResult
	if err := c.callParams(ctx, MethodOverlapScan, OverlapScanParams{Workspace: workspace, Project: project}, &result); err != nil {
		return OverlapScanResult{}, err
	}
	return result, nil
}

func (c *Client) DriftList(ctx context.Context, params DriftListParams) (DriftListResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return DriftListResult{}, err
	}
	params.Workspace, params.Project = workspaceID, projectID
	var result DriftListResult
	if err := c.callParamsStrict(ctx, MethodDriftList, params, &result); err != nil {
		return DriftListResult{}, err
	}
	return result, nil
}

func (c *Client) MeetingCreate(ctx context.Context, params MeetingCreateParams) (MeetingMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result MeetingMutationResult
	if err := c.callParams(ctx, MethodMeetingCreate, params, &result); err != nil {
		return MeetingMutationResult{}, err
	}
	return result, nil
}

func (c *Client) MeetingRun(ctx context.Context, params MeetingRunParams) (MeetingMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result MeetingMutationResult
	if err := c.callParams(ctx, MethodMeetingRun, params, &result); err != nil {
		return MeetingMutationResult{}, err
	}
	return result, nil
}

func (c *Client) MeetingInspect(ctx context.Context, workspace, meeting string) (MeetingInspectResult, error) {
	var result MeetingInspectResult
	if err := c.callParams(ctx, MethodMeetingInspect, MeetingQueryParams{Workspace: workspace, Meeting: meeting}, &result); err != nil {
		return MeetingInspectResult{}, err
	}
	return result, nil
}

func (c *Client) MeetingList(ctx context.Context, params MeetingListParams) (MeetingListResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return MeetingListResult{}, err
	}
	params.Workspace, params.Project = workspaceID, projectID
	var result MeetingListResult
	if err := c.callParamsStrict(ctx, MethodMeetingList, params, &result); err != nil {
		return MeetingListResult{}, err
	}
	return result, nil
}

func (c *Client) MeetingAccept(ctx context.Context, params MeetingAcceptParams) (MeetingMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result MeetingMutationResult
	if err := c.callParams(ctx, MethodMeetingAccept, params, &result); err != nil {
		return MeetingMutationResult{}, err
	}
	return result, nil
}

func (c *Client) MeetingTakeover(ctx context.Context, params MeetingTakeoverParams) (MeetingMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result MeetingMutationResult
	if err := c.callParams(ctx, MethodMeetingTakeover, params, &result); err != nil {
		return MeetingMutationResult{}, err
	}
	return result, nil
}

func (c *Client) EventsList(ctx context.Context, params EventsListParams) (EventsListResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, params.Workspace)
	if err != nil {
		return EventsListResult{}, err
	}
	params.Workspace = workspaceID
	var result EventsListResult
	if err := c.callParamsStrict(ctx, MethodEventsList, params, &result); err != nil {
		return EventsListResult{}, err
	}
	return result, nil
}

func (c *Client) EventsTimeline(ctx context.Context, params EventsTimelineParams) (EventsTimelineResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, params.Workspace)
	if err != nil {
		return EventsTimelineResult{}, err
	}
	params.Workspace = workspaceID
	var result EventsTimelineResult
	if err := c.callParamsStrict(ctx, MethodEventsTimeline, params, &result); err != nil {
		return EventsTimelineResult{}, err
	}
	return result, nil
}

func (c *Client) call(ctx context.Context, method string, params json.RawMessage, result any) error {
	connection, cancel, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	defer connection.Close()

	hello, err := negotiate(connection)
	if err != nil {
		return err
	}
	return roundTrip(connection, Request{
		ID:       requestID(),
		Protocol: hello.SelectedProtocol,
		Method:   method,
		Params:   params,
	}, result)
}

func (c *Client) callParams(ctx context.Context, method string, paramsValue, result any) error {
	params, err := json.Marshal(paramsValue)
	if err != nil {
		return fmt.Errorf("marshal %s parameters: %w", method, err)
	}
	return c.call(ctx, method, params, result)
}

func (c *Client) callParamsStrict(ctx context.Context, method string, paramsValue, result any) error {
	params, err := json.Marshal(paramsValue)
	if err != nil {
		return fmt.Errorf("marshal %s parameters: %w", method, err)
	}
	if err := c.callStrict(ctx, method, params, result); err != nil {
		return err
	}
	if err := validateManagementResultDiscriminator(method, result); err != nil {
		return err
	}
	if err := validateCheckResultDiscriminator(method, result); err != nil {
		return err
	}
	return validateOperatorReadResult(method, paramsValue, result)
}

func (c *Client) callParamsStrictWithTimeout(ctx context.Context, timeout time.Duration, method string, paramsValue, result any) error {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	params, err := json.Marshal(paramsValue)
	if err != nil {
		return fmt.Errorf("marshal %s parameters: %w", method, err)
	}
	return c.callStrict(ctx, method, params, result)
}

func (c *Client) callStrict(ctx context.Context, method string, params json.RawMessage, result any) error {
	connection, cancel, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	defer connection.Close()
	hello, err := negotiate(connection)
	if err != nil {
		return err
	}
	return roundTripStrict(connection, Request{
		ID: requestID(), Protocol: hello.SelectedProtocol, Method: method, Params: params,
	}, result)
}

func defaultIdempotencyKey(value string) string {
	if value == "" {
		return "idem-" + requestID()
	}
	return value
}

func (c *Client) dial(ctx context.Context) (net.Conn, context.CancelFunc, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socketPath)
		if err != nil {
			cancel()
			return nil, func() {}, fmt.Errorf("connect to Crewfold daemon at %s: %w", c.socketPath, err)
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = connection.SetDeadline(deadline)
		}
		return connection, cancel, nil
	}

	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return nil, func() {}, fmt.Errorf("connect to Crewfold daemon at %s: %w", c.socketPath, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	return connection, func() {}, nil
}

func negotiate(connection net.Conn) (HelloResult, error) {
	params, err := json.Marshal(HelloParams{MinProtocol: MinProtocol, MaxProtocol: MaxProtocol})
	if err != nil {
		return HelloResult{}, fmt.Errorf("marshal protocol negotiation: %w", err)
	}

	var result HelloResult
	if err := roundTripStrict(connection, Request{
		ID:     requestID(),
		Method: MethodHello,
		Params: params,
	}, &result); err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Code == "protocol_mismatch" {
			return HelloResult{}, fmt.Errorf("%w: %s", ErrProtocolMismatch, apiError.Message)
		}
		return HelloResult{}, err
	}
	if result.SelectedProtocol < MinProtocol || result.SelectedProtocol > MaxProtocol {
		return HelloResult{}, &ProtocolMismatchError{Selected: result.SelectedProtocol, ClientMin: MinProtocol, ClientMax: MaxProtocol}
	}
	if result.Type != "hello" || result.ServerMin < 1 || result.ServerMax < result.ServerMin || result.SelectedProtocol < result.ServerMin || result.SelectedProtocol > result.ServerMax {
		return HelloResult{}, errors.New("daemon returned a malformed protocol negotiation result")
	}
	if result.ServerMax < MinProtocol || result.ServerMin > MaxProtocol {
		return HelloResult{}, fmt.Errorf("%w: daemon supports %d through %d", ErrProtocolMismatch, result.ServerMin, result.ServerMax)
	}
	return result, nil
}

func roundTrip(connection net.Conn, request Request, result any) error {
	return roundTripDecoded(connection, request, result, false)
}

func roundTripStrict(connection net.Conn, request Request, result any) error {
	return roundTripDecoded(connection, request, result, true)
}

func roundTripDecoded(connection net.Conn, request Request, result any, strict bool) error {
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return fmt.Errorf("write local API request %s: %w", request.ID, err)
	}

	reader := bufio.NewReader(io.LimitReader(connection, maximumResponseBytes+1))
	line, err := reader.ReadBytes('\n')
	if len(line) > maximumResponseBytes {
		return fmt.Errorf("read local API response %s: response exceeds %d bytes", request.ID, maximumResponseBytes)
	}
	if err != nil {
		return fmt.Errorf("read local API response %s: %w", request.ID, err)
	}

	var response Response
	if strict {
		if err := rejectDuplicateJSONFields(line); err != nil {
			return fmt.Errorf("decode local API response %s: %w", request.ID, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&response); err != nil {
			return fmt.Errorf("decode local API response %s: %w", request.ID, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				return fmt.Errorf("decode local API response %s: response contains more than one value", request.ID)
			}
			return fmt.Errorf("decode local API response %s: %w", request.ID, err)
		}
	} else if err := json.Unmarshal(line, &response); err != nil {
		return fmt.Errorf("decode local API response %s: %w", request.ID, err)
	}
	if response.ID != request.ID {
		return fmt.Errorf("local API response id %q does not match request %q", response.ID, request.ID)
	}
	hasResult, hasError := len(response.Result) != 0, response.Error != nil
	if hasResult == hasError {
		return errors.New("local API response must contain exactly one of result or error")
	}
	if request.Method == MethodHello {
		if hasError && response.Protocol != 0 {
			return fmt.Errorf("%w: hello error response declared protocol %d", ErrProtocolMismatch, response.Protocol)
		}
	} else if response.Protocol != request.Protocol {
		return fmt.Errorf("%w: response protocol %d does not match negotiated protocol %d", ErrProtocolMismatch, response.Protocol, request.Protocol)
	}
	if response.Error != nil {
		return response.Error
	}
	if strict {
		if err := rejectDuplicateJSONFields(response.Result); err != nil {
			return fmt.Errorf("decode local API result %s: %w", request.ID, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(response.Result))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(result); err != nil {
			return fmt.Errorf("decode local API result %s: %w", request.ID, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				return fmt.Errorf("decode local API result %s: result contains more than one value", request.ID)
			}
			return fmt.Errorf("decode local API result %s: %w", request.ID, err)
		}
		if err := validateStrictOperatorResultWire(request.Method, response.Result); err != nil {
			return fmt.Errorf("decode local API result %s: %w", request.ID, err)
		}
		return validateHelloResultProtocol(request, response, result)
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		return fmt.Errorf("decode local API result %s: %w", request.ID, err)
	}
	return validateHelloResultProtocol(request, response, result)
}

func validateHelloResultProtocol(request Request, response Response, result any) error {
	if request.Method != MethodHello {
		return nil
	}
	hello, ok := result.(*HelloResult)
	if !ok || response.Protocol != hello.SelectedProtocol {
		return fmt.Errorf("%w: hello response protocol %d differs from selected protocol", ErrProtocolMismatch, response.Protocol)
	}
	return nil
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanUniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains more than one value")
		}
		return err
	}
	return nil
}

func scanUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := scanUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

func requestID() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return "req-" + hex.EncodeToString(random[:])
}
