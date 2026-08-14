package localapi

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

var managementResultDiscriminators = map[string][2]string{
	MethodManagerGrantCreate:        {ManagerGrantMutationSchema, "manager_grant_mutation"},
	MethodManagerGrantRevoke:        {ManagerGrantMutationSchema, "manager_grant_mutation"},
	MethodManagerGrantShow:          {ManagerGrantShowSchema, "manager_grant"},
	MethodManagerGrantList:          {ManagerGrantListSchema, "manager_grant_list"},
	MethodLaunchProfileCreate:       {LaunchProfileMutationSchema, "launch_profile_mutation"},
	MethodLaunchProfileRetire:       {LaunchProfileMutationSchema, "launch_profile_mutation"},
	MethodLaunchProfileShow:         {LaunchProfileShowSchema, "launch_profile"},
	MethodLaunchProfileList:         {LaunchProfileListSchema, "launch_profile_list"},
	MethodManagerInvoke:             {ManagerInvocationSchema, "manager_invocation"},
	MethodProposalList:              {ProposalListSchema, "manager_proposal_list"},
	MethodProposalInspect:           {ProposalShowSchema, "manager_proposal"},
	MethodProposalAccept:            {ProposalMutationSchema, "manager_proposal_mutation"},
	MethodProposalReject:            {ProposalMutationSchema, "manager_proposal_mutation"},
	MethodSupervisorPolicyShow:      {SupervisorPolicyShowSchema, "supervisor_policy"},
	MethodSupervisorPolicyConfigure: {SupervisorPolicyMutationSchema, "supervisor_policy_mutation"},
	MethodSupervisorRun:             {SupervisorRunSchema, "supervisor_run"},
	MethodSupervisorActionList:      {SupervisorActionListSchema, "supervisor_action_list"},
	MethodSupervisorActionShow:      {SupervisorActionShowSchema, "supervisor_action"},
	MethodSupervisorExplain:         {SupervisorExplanationSchema, "supervisor_explanation"},
	MethodApprovalList:              {ApprovalListSchema, "approval_list"},
	MethodApprovalInspect:           {ApprovalShowSchema, "approval"},
	MethodApprovalAllow:             {ApprovalMutationSchema, "approval_mutation"},
	MethodApprovalDeny:              {ApprovalMutationSchema, "approval_mutation"},
}

func validateManagementResultDiscriminator(method string, result any) error {
	expected, managed := managementResultDiscriminators[method]
	if !managed {
		return nil
	}
	value := reflect.ValueOf(result)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return fmt.Errorf("decode local API result %s: nil result", method)
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return fmt.Errorf("decode local API result %s: result has no discriminator", method)
	}
	schema, kind := value.FieldByName("Schema"), value.FieldByName("Type")
	if !schema.IsValid() || !kind.IsValid() || schema.Kind() != reflect.String || kind.Kind() != reflect.String {
		return fmt.Errorf("decode local API result %s: result has no discriminator", method)
	}
	if schema.String() != expected[0] || kind.String() != expected[1] {
		return fmt.Errorf("decode local API result %s: discriminator is %q/%q, want %q/%q", method, schema.String(), kind.String(), expected[0], expected[1])
	}
	return nil
}

func (c *Client) ManagerGrantCreate(ctx context.Context, params ManagerGrantCreateParams) (ManagerGrantMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ManagerGrantMutationResult
	err := c.callParamsStrict(ctx, MethodManagerGrantCreate, params, &result)
	return result, err
}

func (c *Client) ManagerGrantRevoke(ctx context.Context, params ManagerGrantRevokeParams) (ManagerGrantMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ManagerGrantMutationResult
	err := c.callParamsStrict(ctx, MethodManagerGrantRevoke, params, &result)
	return result, err
}

func (c *Client) ManagerGrantShow(ctx context.Context, workspace, grant string) (ManagerGrantShowResult, error) {
	var result ManagerGrantShowResult
	err := c.callParamsStrict(ctx, MethodManagerGrantShow, ManagerGrantQueryParams{Workspace: workspace, Grant: grant}, &result)
	return result, err
}

func (c *Client) ManagerGrantList(ctx context.Context, params ManagerGrantQueryParams) (ManagerGrantListResult, error) {
	var result ManagerGrantListResult
	err := c.callParamsStrict(ctx, MethodManagerGrantList, params, &result)
	return result, err
}

func (c *Client) LaunchProfileCreate(ctx context.Context, params LaunchProfileCreateParams) (LaunchProfileMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result LaunchProfileMutationResult
	err := c.callParamsStrict(ctx, MethodLaunchProfileCreate, params, &result)
	return result, err
}

func (c *Client) LaunchProfileRetire(ctx context.Context, params LaunchProfileRetireParams) (LaunchProfileMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result LaunchProfileMutationResult
	err := c.callParamsStrict(ctx, MethodLaunchProfileRetire, params, &result)
	return result, err
}

func (c *Client) LaunchProfileShow(ctx context.Context, workspace, profile string) (LaunchProfileShowResult, error) {
	var result LaunchProfileShowResult
	err := c.callParamsStrict(ctx, MethodLaunchProfileShow, LaunchProfileQueryParams{Workspace: workspace, Profile: profile}, &result)
	return result, err
}

func (c *Client) LaunchProfileList(ctx context.Context, params LaunchProfileQueryParams) (LaunchProfileListResult, error) {
	var result LaunchProfileListResult
	err := c.callParamsStrict(ctx, MethodLaunchProfileList, params, &result)
	return result, err
}

func (c *Client) ManagerInvoke(ctx context.Context, params ManagerInvokeParams) (ManagerInvocationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ManagerInvocationResult
	err := c.callParamsStrict(ctx, MethodManagerInvoke, params, &result)
	return result, err
}

func (c *Client) ProposalList(ctx context.Context, params ProposalQueryParams) (ProposalListResult, error) {
	var result ProposalListResult
	err := c.callParamsStrict(ctx, MethodProposalList, params, &result)
	return result, err
}

func (c *Client) ProposalInspect(ctx context.Context, workspace, proposal string) (ProposalShowResult, error) {
	var result ProposalShowResult
	err := c.callParamsStrict(ctx, MethodProposalInspect, ProposalQueryParams{Workspace: workspace, Proposal: proposal}, &result)
	return result, err
}

func (c *Client) ProposalAccept(ctx context.Context, params ProposalDecisionParams) (ProposalMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ProposalMutationResult
	err := c.callParamsStrict(ctx, MethodProposalAccept, params, &result)
	return result, err
}

func (c *Client) ProposalReject(ctx context.Context, params ProposalDecisionParams) (ProposalMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ProposalMutationResult
	err := c.callParamsStrict(ctx, MethodProposalReject, params, &result)
	return result, err
}

func (c *Client) SupervisorPolicyShow(ctx context.Context, workspace string) (SupervisorPolicyShowResult, error) {
	var result SupervisorPolicyShowResult
	err := c.callParamsStrict(ctx, MethodSupervisorPolicyShow, WorkspaceParams{Workspace: workspace}, &result)
	return result, err
}

func (c *Client) SupervisorPolicyConfigure(ctx context.Context, params SupervisorPolicyConfigureParams) (SupervisorPolicyMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result SupervisorPolicyMutationResult
	err := c.callParamsStrict(ctx, MethodSupervisorPolicyConfigure, params, &result)
	return result, err
}

func (c *Client) SupervisorRun(ctx context.Context, params SupervisorRunParams) (SupervisorRunResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result SupervisorRunResult
	err := c.callParamsStrict(ctx, MethodSupervisorRun, params, &result)
	return result, err
}

func (c *Client) SupervisorActionList(ctx context.Context, params SupervisorActionQueryParams) (SupervisorActionListResult, error) {
	var result SupervisorActionListResult
	err := c.callParamsStrict(ctx, MethodSupervisorActionList, params, &result)
	return result, err
}

func (c *Client) SupervisorActionShow(ctx context.Context, workspace, action string) (SupervisorActionShowResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, workspace)
	if err != nil {
		return SupervisorActionShowResult{}, err
	}
	var result SupervisorActionShowResult
	err = c.callParamsStrict(ctx, MethodSupervisorActionShow, SupervisorActionQueryParams{Workspace: workspaceID, Action: action}, &result)
	return result, err
}

func (c *Client) SupervisorExplain(ctx context.Context, params SupervisorExplainParams) (SupervisorExplanationResult, error) {
	var result SupervisorExplanationResult
	err := c.callParamsStrict(ctx, MethodSupervisorExplain, params, &result)
	return result, err
}

func (c *Client) ApprovalList(ctx context.Context, params ApprovalListParams) (ApprovalListResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return ApprovalListResult{}, err
	}
	params.Workspace, params.Project = workspaceID, projectID
	var result ApprovalListResult
	err = c.callParamsStrict(ctx, MethodApprovalList, params, &result)
	return result, err
}

func (c *Client) ApprovalInspect(ctx context.Context, workspace, approval string) (ApprovalShowResult, error) {
	var result ApprovalShowResult
	err := c.callParamsStrict(ctx, MethodApprovalInspect, ApprovalQueryParams{Workspace: workspace, Approval: approval}, &result)
	return result, err
}

func (c *Client) ApprovalAllow(ctx context.Context, params ApprovalDecisionParams) (ApprovalMutationResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, params.Workspace)
	if err != nil {
		return ApprovalMutationResult{}, err
	}
	params.Workspace = workspaceID
	if params.DecisionNote != strings.TrimSpace(params.DecisionNote) {
		return ApprovalMutationResult{}, fmt.Errorf("approval decision note must not have leading or trailing whitespace")
	}
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ApprovalMutationResult
	err = c.callParamsStrict(ctx, MethodApprovalAllow, params, &result)
	return result, err
}

func (c *Client) ApprovalDeny(ctx context.Context, params ApprovalDecisionParams) (ApprovalMutationResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, params.Workspace)
	if err != nil {
		return ApprovalMutationResult{}, err
	}
	params.Workspace = workspaceID
	if params.DecisionNote != strings.TrimSpace(params.DecisionNote) {
		return ApprovalMutationResult{}, fmt.Errorf("approval decision note must not have leading or trailing whitespace")
	}
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ApprovalMutationResult
	err = c.callParamsStrict(ctx, MethodApprovalDeny, params, &result)
	return result, err
}
