package localapi

import "context"

func (c *Client) WorkstreamDeliveryShow(ctx context.Context, workspace, objective string) (WorkstreamDeliveryShowResult, error) {
	var result WorkstreamDeliveryShowResult
	err := c.callParamsStrict(ctx, MethodWorkstreamDeliveryShow, WorkstreamDeliveryQueryParams{Workspace: workspace, Objective: objective}, &result)
	return result, err
}

func (c *Client) WorkstreamDeliveryAccept(ctx context.Context, params WorkstreamDeliveryDecisionParams) (WorkstreamDeliveryMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result WorkstreamDeliveryMutationResult
	err := c.callParamsStrict(ctx, MethodWorkstreamDeliveryAccept, params, &result)
	return result, err
}

func (c *Client) WorkstreamDeliveryReject(ctx context.Context, params WorkstreamDeliveryDecisionParams) (WorkstreamDeliveryMutationResult, error) {
	params.IdempotencyKey = defaultIdempotencyKey(params.IdempotencyKey)
	var result WorkstreamDeliveryMutationResult
	err := c.callParamsStrict(ctx, MethodWorkstreamDeliveryReject, params, &result)
	return result, err
}
