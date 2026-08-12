package localapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"crewfold/internal/domain"
)

const defaultTimeout = 2 * time.Second

type Client struct {
	socketPath string
	timeout    time.Duration
}

func NewClient(socketPath string) *Client {
	return &Client{socketPath: socketPath, timeout: defaultTimeout}
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
	params, err := json.Marshal(WorkspaceShowParams{Identifier: identifier})
	if err != nil {
		return WorkspaceShowResult{}, fmt.Errorf("marshal workspace query: %w", err)
	}
	var result WorkspaceShowResult
	if err := c.call(ctx, MethodWorkspaceShow, params, &result); err != nil {
		return WorkspaceShowResult{}, err
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
	var result AgentShowResult
	if err := c.callParams(ctx, MethodAgentShow, AgentQueryParams{Workspace: workspace, Agent: agent}, &result); err != nil {
		return AgentShowResult{}, err
	}
	return result, nil
}

func (c *Client) AgentList(ctx context.Context, workspace string) (AgentListResult, error) {
	var result AgentListResult
	if err := c.callParams(ctx, MethodAgentList, AgentQueryParams{Workspace: workspace}, &result); err != nil {
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

func (c *Client) ObjectiveList(ctx context.Context, workspace, project string) (ObjectiveListResult, error) {
	var result ObjectiveListResult
	if err := c.callParams(ctx, MethodObjectiveList, ObjectiveQueryParams{Workspace: workspace, Project: project}, &result); err != nil {
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

func (c *Client) TaskList(ctx context.Context, workspace, project string, readyOnly bool) (TaskListResult, error) {
	var result TaskListResult
	if err := c.callParams(ctx, MethodTaskList, TaskQueryParams{Workspace: workspace, Project: project, ReadyOnly: readyOnly}, &result); err != nil {
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
	if err := c.callParams(ctx, MethodContextShow, ContextQueryParams{Workspace: workspace, Context: contextID}, &result); err != nil {
		return ContextShowResult{}, err
	}
	return result, nil
}

func (c *Client) ContextExplain(ctx context.Context, workspace, contextID string) (ContextExplainResult, error) {
	var result ContextExplainResult
	if err := c.callParams(ctx, MethodContextExplain, ContextQueryParams{Workspace: workspace, Context: contextID}, &result); err != nil {
		return ContextExplainResult{}, err
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
	var result InboxListResult
	if err := c.callParams(ctx, MethodInboxList, InboxListParams{Workspace: workspace, Agent: agent, Limit: limit}, &result); err != nil {
		return InboxListResult{}, err
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

func (c *Client) RunList(ctx context.Context, workspace, task, status string) (RunListResult, error) {
	var result RunListResult
	if err := c.callParams(ctx, MethodRunList, RunQueryParams{Workspace: workspace, Task: task, Status: status}, &result); err != nil {
		return RunListResult{}, err
	}
	return result, nil
}

func (c *Client) RunResume(ctx context.Context, paramsValue RunResumeParams) (RunMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result RunMutationResult
	if err := c.callParams(ctx, MethodRunResume, paramsValue, &result); err != nil {
		return RunMutationResult{}, err
	}
	return result, nil
}

func (c *Client) RunStop(ctx context.Context, paramsValue RunStopParams) (RunMutationResult, error) {
	paramsValue.IdempotencyKey = defaultIdempotencyKey(paramsValue.IdempotencyKey)
	var result RunMutationResult
	if err := c.callParams(ctx, MethodRunStop, paramsValue, &result); err != nil {
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

func (c *Client) RunAttach(ctx context.Context, workspace, run string, takeover bool) (RunAttachResult, error) {
	var result RunAttachResult
	if err := c.callParams(ctx, MethodRunAttach, RunAttachParams{Workspace: workspace, Run: run, Takeover: takeover}, &result); err != nil {
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

func (c *Client) ClaimList(ctx context.Context, workspace, project, status string) (ClaimListResult, error) {
	var result ClaimListResult
	if err := c.callParams(ctx, MethodClaimList, ClaimQueryParams{Workspace: workspace, Project: project, Status: status}, &result); err != nil {
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

func (c *Client) OverlapList(ctx context.Context, workspace, project, status string) (OverlapListResult, error) {
	var result OverlapListResult
	if err := c.callParams(ctx, MethodOverlapList, OverlapQueryParams{Workspace: workspace, Project: project, Status: status}, &result); err != nil {
		return OverlapListResult{}, err
	}
	return result, nil
}

func (c *Client) OverlapInspect(ctx context.Context, workspace, overlap string) (OverlapInspectResult, error) {
	var result OverlapInspectResult
	if err := c.callParams(ctx, MethodOverlapInspect, OverlapQueryParams{Workspace: workspace, Overlap: overlap}, &result); err != nil {
		return OverlapInspectResult{}, err
	}
	return result, nil
}

func (c *Client) OverlapScan(ctx context.Context, workspace, project string) (OverlapScanResult, error) {
	var result OverlapScanResult
	if err := c.callParams(ctx, MethodOverlapScan, OverlapQueryParams{Workspace: workspace, Project: project}, &result); err != nil {
		return OverlapScanResult{}, err
	}
	return result, nil
}

func (c *Client) DriftList(ctx context.Context, workspace, status string) (DriftListResult, error) {
	var result DriftListResult
	if err := c.callParams(ctx, MethodDriftList, DriftQueryParams{Workspace: workspace, Status: status}, &result); err != nil {
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

func (c *Client) EventsList(ctx context.Context, after int64, limit int) (EventsListResult, error) {
	paramsValue := EventsListParams{After: &after}
	if limit != 0 {
		paramsValue.Limit = &limit
	}
	params, err := json.Marshal(paramsValue)
	if err != nil {
		return EventsListResult{}, fmt.Errorf("marshal event query: %w", err)
	}
	var result EventsListResult
	if err := c.call(ctx, MethodEventsList, params, &result); err != nil {
		return EventsListResult{}, err
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
	if err := roundTrip(connection, Request{
		ID:     requestID(),
		Method: MethodHello,
		Params: params,
	}, &result); err != nil {
		return HelloResult{}, err
	}
	if result.SelectedProtocol < MinProtocol || result.SelectedProtocol > MaxProtocol {
		return HelloResult{}, fmt.Errorf("daemon selected unsupported protocol %d", result.SelectedProtocol)
	}
	return result, nil
}

func roundTrip(connection net.Conn, request Request, result any) error {
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return fmt.Errorf("write local API request %s: %w", request.ID, err)
	}

	reader := bufio.NewReader(connection)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read local API response %s: %w", request.ID, err)
	}

	var response Response
	if err := json.Unmarshal(line, &response); err != nil {
		return fmt.Errorf("decode local API response %s: %w", request.ID, err)
	}
	if response.ID != request.ID {
		return fmt.Errorf("local API response id %q does not match request %q", response.ID, request.ID)
	}
	if response.Error != nil {
		return response.Error
	}
	if len(response.Result) == 0 {
		return errors.New("local API response has neither result nor error")
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		return fmt.Errorf("decode local API result %s: %w", request.ID, err)
	}
	return nil
}

func requestID() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return "req-" + hex.EncodeToString(random[:])
}
