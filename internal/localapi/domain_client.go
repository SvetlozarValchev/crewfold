package localapi

import (
	"context"
	"fmt"
	"time"
)

func (c *Client) DomainAgentCreate(ctx context.Context, params DomainAgentCreateParams) (DomainAgentCreateResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return DomainAgentCreateResult{}, err
	}
	params.Workspace, params.Project = workspaceID, projectID
	if params.ParentAgent != "" {
		params.ParentAgent, err = c.resolveOperatorAgent(ctx, workspaceID, params.ParentAgent)
		if err != nil {
			return DomainAgentCreateResult{}, err
		}
	}
	if params.Workstream != "" && !canonicalObjectiveIDPattern.MatchString(params.Workstream) {
		return DomainAgentCreateResult{}, fmt.Errorf("domain agent workstream must be a canonical objective ID")
	}
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result DomainAgentCreateResult
	if err := c.callParamsStrict(ctx, MethodDomainAgentCreate, params, &result); err != nil {
		return DomainAgentCreateResult{}, err
	}
	return result, nil
}

func (c *Client) DomainAgentAttach(ctx context.Context, params DomainAgentAttachParams) (DomainAgentMutationResult, error) {
	resolved, err := c.resolveDomainAgentAttachParams(ctx, params)
	if err != nil {
		return DomainAgentMutationResult{}, err
	}
	resolved.IdempotencyKey = defaultIdempotencyKey(resolved.IdempotencyKey)
	var result DomainAgentMutationResult
	if err := c.callParamsStrict(ctx, MethodDomainAgentAttach, resolved, &result); err != nil {
		return DomainAgentMutationResult{}, err
	}
	return result, nil
}

func (c *Client) DomainAgentUpdate(ctx context.Context, params DomainAgentUpdateParams) (DomainAgentMutationResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return DomainAgentMutationResult{}, err
	}
	agentID, err := c.resolveOperatorAgent(ctx, workspaceID, params.Agent)
	if err != nil {
		return DomainAgentMutationResult{}, err
	}
	params.Workspace, params.Project, params.Agent = workspaceID, projectID, agentID
	if params.ParentAgent != nil && *params.ParentAgent != "" {
		parentID, resolveErr := c.resolveOperatorAgent(ctx, workspaceID, *params.ParentAgent)
		if resolveErr != nil {
			return DomainAgentMutationResult{}, resolveErr
		}
		params.ParentAgent = &parentID
	}
	if params.Workstream != nil && *params.Workstream != "" && !canonicalObjectiveIDPattern.MatchString(*params.Workstream) {
		return DomainAgentMutationResult{}, fmt.Errorf("domain agent workstream must be a canonical objective ID")
	}
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result DomainAgentMutationResult
	if err := c.callParamsStrict(ctx, MethodDomainAgentUpdate, params, &result); err != nil {
		return DomainAgentMutationResult{}, err
	}
	return result, nil
}

func (c *Client) DomainAgentTree(ctx context.Context, workspace, project string) (DomainAgentTreeResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, workspace, project)
	if err != nil {
		return DomainAgentTreeResult{}, err
	}
	params := DomainAgentTreeParams{Workspace: workspaceID, Project: projectID}
	var result DomainAgentTreeResult
	if err := c.callParamsStrict(ctx, MethodDomainAgentTree, params, &result); err != nil {
		return DomainAgentTreeResult{}, err
	}
	return result, nil
}

func (c *Client) DomainAgentSessionShow(ctx context.Context, workspace, project, agent string) (DomainAgentSessionResult, error) {
	params, err := c.resolveDomainAgentSessionParams(ctx, workspace, project, agent)
	if err != nil {
		return DomainAgentSessionResult{}, err
	}
	var result DomainAgentSessionResult
	if err := c.callParamsStrict(ctx, MethodDomainAgentSessionShow, params, &result); err != nil {
		return DomainAgentSessionResult{}, err
	}
	return result, nil
}

func (c *Client) DomainAgentSessionOpen(ctx context.Context, params DomainAgentSessionOpenParams) (DomainAgentSessionResult, error) {
	resolved, err := c.resolveDomainAgentSessionParams(ctx, params.Workspace, params.Project, params.Agent)
	if err != nil {
		return DomainAgentSessionResult{}, err
	}
	params.Workspace, params.Project, params.Agent = resolved.Workspace, resolved.Project, resolved.Agent
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result DomainAgentSessionResult
	if err := c.callParamsStrictWithTimeout(ctx, 60*time.Second, MethodDomainAgentSessionOpen, params, &result); err != nil {
		return DomainAgentSessionResult{}, err
	}
	return result, nil
}

func (c *Client) DomainAgentSessionSend(ctx context.Context, params DomainAgentSessionSendParams) (DomainAgentSessionResult, error) {
	resolved, err := c.resolveDomainAgentSessionParams(ctx, params.Workspace, params.Project, params.Agent)
	if err != nil {
		return DomainAgentSessionResult{}, err
	}
	params.Workspace, params.Project, params.Agent = resolved.Workspace, resolved.Project, resolved.Agent
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result DomainAgentSessionResult
	if err := c.callParamsStrictWithTimeout(ctx, 60*time.Second, MethodDomainAgentSessionSend, params, &result); err != nil {
		return DomainAgentSessionResult{}, err
	}
	return result, nil
}

func (c *Client) DomainAgentSessionInterrupt(ctx context.Context, params DomainAgentSessionInterruptParams) (DomainAgentSessionResult, error) {
	resolved, err := c.resolveDomainAgentSessionParams(ctx, params.Workspace, params.Project, params.Agent)
	if err != nil {
		return DomainAgentSessionResult{}, err
	}
	params.Workspace, params.Project, params.Agent = resolved.Workspace, resolved.Project, resolved.Agent
	var result DomainAgentSessionResult
	if err := c.callParamsStrict(ctx, MethodDomainAgentSessionInterrupt, params, &result); err != nil {
		return DomainAgentSessionResult{}, err
	}
	return result, nil
}

func (c *Client) DomainStaffingGrantCreate(ctx context.Context, params DomainStaffingGrantCreateParams) (DomainStaffingGrantMutationResult, error) {
	resolved, err := c.resolveDomainAgentSessionParams(ctx, params.Workspace, params.Project, params.ManagerAgent)
	if err != nil {
		return DomainStaffingGrantMutationResult{}, err
	}
	params.Workspace, params.Project, params.ManagerAgent = resolved.Workspace, resolved.Project, resolved.Agent
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result DomainStaffingGrantMutationResult
	if err := c.callParamsStrict(ctx, MethodDomainStaffingGrantCreate, params, &result); err != nil {
		return DomainStaffingGrantMutationResult{}, err
	}
	return result, nil
}

func (c *Client) DomainStaffingGrantList(ctx context.Context, workspace, project, manager string) (DomainStaffingGrantListResult, error) {
	resolved, err := c.resolveDomainAgentSessionParams(ctx, workspace, project, manager)
	if err != nil {
		return DomainStaffingGrantListResult{}, err
	}
	params := DomainStaffingGrantListParams{Workspace: resolved.Workspace, Project: resolved.Project, ManagerAgent: resolved.Agent}
	var result DomainStaffingGrantListResult
	if err := c.callParamsStrict(ctx, MethodDomainStaffingGrantList, params, &result); err != nil {
		return DomainStaffingGrantListResult{}, err
	}
	return result, nil
}

func (c *Client) DomainStaffingGrantRevoke(ctx context.Context, params DomainStaffingGrantRevokeParams) (DomainStaffingGrantMutationResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, params.Workspace)
	if err != nil {
		return DomainStaffingGrantMutationResult{}, err
	}
	params.Workspace, params.IdempotencyKey = workspaceID, defaultIdempotencyKey(params.IdempotencyKey)
	var result DomainStaffingGrantMutationResult
	if err := c.callParamsStrict(ctx, MethodDomainStaffingGrantRevoke, params, &result); err != nil {
		return DomainStaffingGrantMutationResult{}, err
	}
	return result, nil
}

func (c *Client) resolveDomainAgentSessionParams(ctx context.Context, workspace, project, agent string) (DomainAgentSessionParams, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, workspace, project)
	if err != nil {
		return DomainAgentSessionParams{}, err
	}
	agentID, err := c.resolveOperatorAgent(ctx, workspaceID, agent)
	if err != nil {
		return DomainAgentSessionParams{}, err
	}
	return DomainAgentSessionParams{Workspace: workspaceID, Project: projectID, Agent: agentID}, nil
}

func (c *Client) resolveDomainAgentAttachParams(ctx context.Context, params DomainAgentAttachParams) (DomainAgentAttachParams, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return DomainAgentAttachParams{}, err
	}
	agentID, err := c.resolveOperatorAgent(ctx, workspaceID, params.Agent)
	if err != nil {
		return DomainAgentAttachParams{}, err
	}
	params.Workspace, params.Project, params.Agent = workspaceID, projectID, agentID
	if params.ParentAgent != "" {
		params.ParentAgent, err = c.resolveOperatorAgent(ctx, workspaceID, params.ParentAgent)
		if err != nil {
			return DomainAgentAttachParams{}, err
		}
	}
	if params.Workstream != "" && !canonicalObjectiveIDPattern.MatchString(params.Workstream) {
		return DomainAgentAttachParams{}, fmt.Errorf("domain agent workstream must be a canonical objective ID")
	}
	return params, nil
}
