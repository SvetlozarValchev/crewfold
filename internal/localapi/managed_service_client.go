package localapi

import (
	"context"

	"crewfold/internal/domain"
)

func (c *Client) ManagedServiceDefinitionCreate(ctx context.Context, params ManagedServiceDefinitionCreateParams) (ManagedServiceDefinitionMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	if params.Arguments == nil {
		params.Arguments = []string{}
	}
	if params.Environment == nil {
		params.Environment = []domain.ManagedServiceEnvironmentVariable{}
	}
	var result ManagedServiceDefinitionMutationResult
	err := c.callParamsStrict(ctx, MethodManagedServiceDefinitionCreate, params, &result)
	return result, err
}

func (c *Client) ManagedServiceDefinitionRetire(ctx context.Context, params ManagedServiceDefinitionRetireParams) (ManagedServiceDefinitionMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ManagedServiceDefinitionMutationResult
	err := c.callParamsStrict(ctx, MethodManagedServiceDefinitionRetire, params, &result)
	return result, err
}

func (c *Client) ManagedServiceDefinitionShow(ctx context.Context, workspace, definition string) (ManagedServiceDefinitionShowResult, error) {
	var result ManagedServiceDefinitionShowResult
	err := c.callParamsStrict(ctx, MethodManagedServiceDefinitionShow, ManagedServiceDefinitionQueryParams{Workspace: workspace, Definition: definition}, &result)
	return result, err
}

func (c *Client) ManagedServiceDefinitionList(ctx context.Context, params ManagedServiceDefinitionQueryParams) (ManagedServiceDefinitionListResult, error) {
	var result ManagedServiceDefinitionListResult
	err := c.callParamsStrict(ctx, MethodManagedServiceDefinitionList, params, &result)
	return result, err
}

func (c *Client) ManagedServiceStart(ctx context.Context, params ManagedServiceStartParams) (ManagedServiceMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ManagedServiceMutationResult
	err := c.callParamsStrict(ctx, MethodManagedServiceStart, params, &result)
	return result, err
}

func (c *Client) ManagedServiceShow(ctx context.Context, workspace, instance string) (ManagedServiceShowResult, error) {
	var result ManagedServiceShowResult
	err := c.callParamsStrict(ctx, MethodManagedServiceShow, ManagedServiceQueryParams{Workspace: workspace, Instance: instance}, &result)
	return result, err
}

func (c *Client) ManagedServiceList(ctx context.Context, params ManagedServiceQueryParams) (ManagedServiceListResult, error) {
	var result ManagedServiceListResult
	err := c.callParamsStrict(ctx, MethodManagedServiceList, params, &result)
	return result, err
}

func (c *Client) ManagedServiceStop(ctx context.Context, params ManagedServiceActionParams) (ManagedServiceMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ManagedServiceMutationResult
	err := c.callParamsStrict(ctx, MethodManagedServiceStop, params, &result)
	return result, err
}

func (c *Client) ManagedServiceRestart(ctx context.Context, params ManagedServiceActionParams) (ManagedServiceMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ManagedServiceMutationResult
	err := c.callParamsStrict(ctx, MethodManagedServiceRestart, params, &result)
	return result, err
}

func (c *Client) ManagedServiceResolveUnknown(ctx context.Context, params ManagedServiceResolveUnknownParams) (ManagedServiceMutationResult, error) {
	var result ManagedServiceMutationResult
	err := c.callParamsStrict(ctx, MethodManagedServiceResolveUnknown, params, &result)
	return result, err
}

func (c *Client) ManagedServiceLogs(ctx context.Context, workspace, instance string) (ManagedServiceLogsResult, error) {
	var result ManagedServiceLogsResult
	err := c.callParamsStrict(ctx, MethodManagedServiceLogs, ManagedServiceLogsParams{Workspace: workspace, Instance: instance}, &result)
	return result, err
}

func (c *Client) ManagedServiceGrantCreate(ctx context.Context, params ManagedServiceGrantCreateParams) (ManagedServiceGrantMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	if params.Actions == nil {
		params.Actions = []string{}
	}
	var result ManagedServiceGrantMutationResult
	err := c.callParamsStrict(ctx, MethodManagedServiceGrantCreate, params, &result)
	return result, err
}

func (c *Client) ManagedServiceGrantRevoke(ctx context.Context, params ManagedServiceGrantRevokeParams) (ManagedServiceGrantMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ManagedServiceGrantMutationResult
	err := c.callParamsStrict(ctx, MethodManagedServiceGrantRevoke, params, &result)
	return result, err
}

func (c *Client) ManagedServiceGrantList(ctx context.Context, params ManagedServiceGrantQueryParams) (ManagedServiceGrantListResult, error) {
	var result ManagedServiceGrantListResult
	err := c.callParamsStrict(ctx, MethodManagedServiceGrantList, params, &result)
	return result, err
}

func (c *Client) ManagedServiceRequestList(ctx context.Context, params ManagedServiceRequestQueryParams) (ManagedServiceRequestListResult, error) {
	var result ManagedServiceRequestListResult
	err := c.callParamsStrict(ctx, MethodManagedServiceRequestList, params, &result)
	return result, err
}

func (c *Client) ManagedServiceRequestAccept(ctx context.Context, params ManagedServiceRequestDecisionParams) (ManagedServiceRequestMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ManagedServiceRequestMutationResult
	err := c.callParamsStrict(ctx, MethodManagedServiceRequestAccept, params, &result)
	return result, err
}

func (c *Client) ManagedServiceRequestReject(ctx context.Context, params ManagedServiceRequestDecisionParams) (ManagedServiceRequestMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result ManagedServiceRequestMutationResult
	err := c.callParamsStrict(ctx, MethodManagedServiceRequestReject, params, &result)
	return result, err
}
