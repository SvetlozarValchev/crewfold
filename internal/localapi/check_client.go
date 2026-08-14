package localapi

import (
	"context"
	"fmt"
	"reflect"
)

var checkResultDiscriminators = map[string][2]string{
	MethodCheckDefinitionCreate:  {CheckDefinitionMutationSchema, "check_definition_mutation"},
	MethodCheckDefinitionRetire:  {CheckDefinitionMutationSchema, "check_definition_mutation"},
	MethodCheckDefinitionShow:    {CheckDefinitionShowSchema, "check_definition"},
	MethodCheckDefinitionList:    {CheckDefinitionListSchema, "check_definition_list"},
	MethodCheckRequirementCreate: {CheckRequirementMutationSchema, "check_requirement_mutation"},
	MethodCheckRequirementRetire: {CheckRequirementMutationSchema, "check_requirement_mutation"},
	MethodCheckRequirementList:   {CheckRequirementListSchema, "check_requirement_list"},
	MethodCheckGrantCreate:       {CheckGrantMutationSchema, "check_watch_grant_mutation"},
	MethodCheckGrantRevoke:       {CheckGrantMutationSchema, "check_watch_grant_mutation"},
	MethodCheckGrantShow:         {CheckGrantShowSchema, "check_watch_grant"},
	MethodCheckGrantList:         {CheckGrantListSchema, "check_watch_grant_list"},
	MethodCheckRouteCreate:       {CheckRouteMutationSchema, "check_route_mutation"},
	MethodCheckRouteRetire:       {CheckRouteMutationSchema, "check_route_mutation"},
	MethodCheckRouteList:         {CheckRouteListSchema, "check_route_list"},
	MethodCheckPolicyShow:        {CheckPolicyShowSchema, "check_policy"},
	MethodCheckPolicyConfigure:   {CheckPolicyMutationSchema, "check_policy_mutation"},
	MethodCheckRun:               {CheckRunMutationSchema, "check_run_mutation"},
	MethodCheckList:              {CheckRunListSchema, "check_run_list"},
	MethodCheckInspect:           {CheckInspectSchema, "check_run_detail"},
	MethodCheckLogs:              {CheckLogsSchema, "check_run_logs"},
	MethodCheckWatch:             {CheckWatchSchema, "check_watch"},
	MethodCheckRepairList:        {CheckRepairListSchema, "check_repair_list"},
	MethodCheckRepairInspect:     {CheckRepairShowSchema, "check_repair"},
	MethodCheckRepairAccept:      {CheckRepairMutationSchema, "check_repair_mutation"},
	MethodCheckRepairReject:      {CheckRepairMutationSchema, "check_repair_mutation"},
}

func validateCheckResultDiscriminator(method string, result any) error {
	expected, managed := checkResultDiscriminators[method]
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

func (c *Client) CheckDefinitionCreate(ctx context.Context, params CheckDefinitionCreateParams) (CheckDefinitionMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	if params.Arguments == nil {
		params.Arguments = []string{}
	}
	var result CheckDefinitionMutationResult
	err := c.callParamsStrict(ctx, MethodCheckDefinitionCreate, params, &result)
	return result, err
}

func (c *Client) CheckDefinitionRetire(ctx context.Context, params CheckDefinitionRetireParams) (CheckDefinitionMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckDefinitionMutationResult
	err := c.callParamsStrict(ctx, MethodCheckDefinitionRetire, params, &result)
	return result, err
}

func (c *Client) CheckDefinitionShow(ctx context.Context, workspace, definition string) (CheckDefinitionShowResult, error) {
	var result CheckDefinitionShowResult
	err := c.callParamsStrict(ctx, MethodCheckDefinitionShow, CheckDefinitionQueryParams{Workspace: workspace, Definition: definition}, &result)
	return result, err
}

func (c *Client) CheckDefinitionList(ctx context.Context, params CheckDefinitionQueryParams) (CheckDefinitionListResult, error) {
	var result CheckDefinitionListResult
	err := c.callParamsStrict(ctx, MethodCheckDefinitionList, params, &result)
	return result, err
}

func (c *Client) CheckRequirementCreate(ctx context.Context, params CheckRequirementCreateParams) (CheckRequirementMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckRequirementMutationResult
	err := c.callParamsStrict(ctx, MethodCheckRequirementCreate, params, &result)
	return result, err
}

func (c *Client) CheckRequirementRetire(ctx context.Context, params CheckRequirementRetireParams) (CheckRequirementMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckRequirementMutationResult
	err := c.callParamsStrict(ctx, MethodCheckRequirementRetire, params, &result)
	return result, err
}

func (c *Client) CheckRequirementList(ctx context.Context, params CheckRequirementQueryParams) (CheckRequirementListResult, error) {
	var result CheckRequirementListResult
	err := c.callParamsStrict(ctx, MethodCheckRequirementList, params, &result)
	return result, err
}

func (c *Client) CheckGrantCreate(ctx context.Context, params CheckGrantCreateParams) (CheckGrantMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckGrantMutationResult
	err := c.callParamsStrict(ctx, MethodCheckGrantCreate, params, &result)
	return result, err
}

func (c *Client) CheckGrantRevoke(ctx context.Context, params CheckGrantRevokeParams) (CheckGrantMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckGrantMutationResult
	err := c.callParamsStrict(ctx, MethodCheckGrantRevoke, params, &result)
	return result, err
}

func (c *Client) CheckGrantShow(ctx context.Context, workspace, grant string) (CheckGrantShowResult, error) {
	var result CheckGrantShowResult
	err := c.callParamsStrict(ctx, MethodCheckGrantShow, CheckGrantQueryParams{Workspace: workspace, Grant: grant}, &result)
	return result, err
}

func (c *Client) CheckGrantList(ctx context.Context, params CheckGrantQueryParams) (CheckGrantListResult, error) {
	var result CheckGrantListResult
	err := c.callParamsStrict(ctx, MethodCheckGrantList, params, &result)
	return result, err
}

func (c *Client) CheckRouteCreate(ctx context.Context, params CheckRouteCreateParams) (CheckRouteMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckRouteMutationResult
	err := c.callParamsStrict(ctx, MethodCheckRouteCreate, params, &result)
	return result, err
}

func (c *Client) CheckRouteRetire(ctx context.Context, params CheckRouteRetireParams) (CheckRouteMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckRouteMutationResult
	err := c.callParamsStrict(ctx, MethodCheckRouteRetire, params, &result)
	return result, err
}

func (c *Client) CheckRouteList(ctx context.Context, params CheckRouteQueryParams) (CheckRouteListResult, error) {
	var result CheckRouteListResult
	err := c.callParamsStrict(ctx, MethodCheckRouteList, params, &result)
	return result, err
}

func (c *Client) CheckPolicyShow(ctx context.Context, workspace, project string) (CheckPolicyShowResult, error) {
	var result CheckPolicyShowResult
	err := c.callParamsStrict(ctx, MethodCheckPolicyShow, CheckPolicyQueryParams{Workspace: workspace, Project: project}, &result)
	return result, err
}

func (c *Client) CheckPolicyConfigure(ctx context.Context, params CheckPolicyConfigureParams) (CheckPolicyMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckPolicyMutationResult
	err := c.callParamsStrict(ctx, MethodCheckPolicyConfigure, params, &result)
	return result, err
}

func (c *Client) CheckRun(ctx context.Context, params CheckRunParams) (CheckRunMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckRunMutationResult
	err := c.callParamsStrict(ctx, MethodCheckRun, params, &result)
	return result, err
}

func (c *Client) CheckList(ctx context.Context, params CheckListParams) (CheckRunListResult, error) {
	workspaceID, projectID, err := c.resolveOperatorScope(ctx, params.Workspace, params.Project)
	if err != nil {
		return CheckRunListResult{}, err
	}
	params.Workspace, params.Project = workspaceID, projectID
	var result CheckRunListResult
	err = c.callParamsStrict(ctx, MethodCheckList, params, &result)
	return result, err
}

func (c *Client) CheckInspect(ctx context.Context, workspace, checkRun string) (CheckInspectResult, error) {
	var result CheckInspectResult
	err := c.callParamsStrict(ctx, MethodCheckInspect, CheckQueryParams{Workspace: workspace, CheckRun: checkRun}, &result)
	return result, err
}

func (c *Client) CheckLogs(ctx context.Context, workspace, checkRun string) (CheckLogsResult, error) {
	var result CheckLogsResult
	err := c.callParamsStrict(ctx, MethodCheckLogs, CheckLogsParams{Workspace: workspace, CheckRun: checkRun}, &result)
	return result, err
}

func (c *Client) CheckWatch(ctx context.Context, params CheckWatchParams) (CheckWatchResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckWatchResult
	err := c.callParamsStrict(ctx, MethodCheckWatch, params, &result)
	return result, err
}

func (c *Client) CheckRepairList(ctx context.Context, params CheckRepairQueryParams) (CheckRepairListResult, error) {
	var result CheckRepairListResult
	err := c.callParamsStrict(ctx, MethodCheckRepairList, params, &result)
	return result, err
}

func (c *Client) CheckRepairInspect(ctx context.Context, workspace, repair string) (CheckRepairShowResult, error) {
	var result CheckRepairShowResult
	err := c.callParamsStrict(ctx, MethodCheckRepairInspect, CheckRepairQueryParams{Workspace: workspace, Repair: repair}, &result)
	return result, err
}

func (c *Client) CheckRepairAccept(ctx context.Context, params CheckRepairDecisionParams) (CheckRepairMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckRepairMutationResult
	err := c.callParamsStrict(ctx, MethodCheckRepairAccept, params, &result)
	return result, err
}

func (c *Client) CheckRepairReject(ctx context.Context, params CheckRepairDecisionParams) (CheckRepairMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result CheckRepairMutationResult
	err := c.callParamsStrict(ctx, MethodCheckRepairReject, params, &result)
	return result, err
}
