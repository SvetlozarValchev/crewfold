package localapi

import (
	"context"
	"fmt"
	"time"
)

// WebBootstrap mints one owner-local, single-use browser bootstrap grant.
func (c *Client) WebBootstrap(ctx context.Context) (WebBootstrapResult, error) {
	var result WebBootstrapResult
	if err := c.callParamsStrict(ctx, MethodWebBootstrap, WebBootstrapParams{}, &result); err != nil {
		return WebBootstrapResult{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, result.ExpiresAt)
	if err != nil || !time.Now().Before(expiresAt) {
		return WebBootstrapResult{}, fmt.Errorf("decode local API result %s: bootstrap grant is already expired", MethodWebBootstrap)
	}
	return result, nil
}
