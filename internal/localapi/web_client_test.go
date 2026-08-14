package localapi

import (
	"context"
	"strings"
	"testing"
)

func TestM21WebBootstrapClientRequiresCurrentStrictResult(t *testing.T) {
	t.Parallel()
	exact := WebBootstrapResult{
		Schema:    WebBootstrapSchema,
		Type:      "web_bootstrap",
		URL:       "http://127.0.0.1:43121/#bootstrap=" + strings.Repeat("a", 64),
		ExpiresAt: "2099-08-14T20:00:00Z",
	}
	call := func(client *Client) error {
		_, err := client.WebBootstrap(context.Background())
		return err
	}
	if err := capturePortableResultError(t, MethodWebBootstrap, call, exact); err != nil {
		t.Fatalf("WebBootstrap(exact) error = %v", err)
	}
	wrongHost := exact
	wrongHost.URL = "http://localhost:43121/#bootstrap=" + strings.Repeat("a", 64)
	if err := capturePortableResultError(t, MethodWebBootstrap, call, wrongHost); err == nil {
		t.Fatal("WebBootstrap accepted a noncanonical host")
	}
	wrongToken := exact
	wrongToken.URL = "http://127.0.0.1:43121/#bootstrap=secret"
	if err := capturePortableResultError(t, MethodWebBootstrap, call, wrongToken); err == nil {
		t.Fatal("WebBootstrap accepted a malformed bootstrap token")
	}
	invalidPort := exact
	invalidPort.URL = "http://127.0.0.1:65536/#bootstrap=" + strings.Repeat("a", 64)
	if err := capturePortableResultError(t, MethodWebBootstrap, call, invalidPort); err == nil {
		t.Fatal("WebBootstrap accepted an out-of-range loopback port")
	}
	expired := exact
	expired.ExpiresAt = "2000-01-01T00:00:00Z"
	if err := capturePortableResultError(t, MethodWebBootstrap, call, expired); err == nil {
		t.Fatal("WebBootstrap accepted an expired grant")
	}
}
