package daemon

import (
	"context"
	"reflect"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestManagementLocalAPIReplayIgnoresTransportCorrelationID(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "management-replay-workspace"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	params := localapi.SupervisorPolicyConfigureParams{
		Workspace: "personal", Enabled: false, AutoSchedule: false, AutoRetryLimit: 0,
		RetryCooldownSeconds: 0, ExpectedRevision: 1, IdempotencyKey: "management-policy-replay",
		Limits: domain.SupervisorLimits{
			MaxActiveRuns: 2, MaxStartingRuns: 1, DefaultProjectConcurrency: 1, DefaultProviderConcurrency: 1,
			ProjectConcurrency: map[string]int{}, ProviderConcurrency: map[string]int{},
		},
	}
	first, err := client.SupervisorPolicyConfigure(context.Background(), params)
	if err != nil {
		t.Fatalf("SupervisorPolicyConfigure(first) error = %v", err)
	}
	// Client calls generate fresh JSON-RPC request IDs. The request ID becomes
	// the correlation ID at the daemon seam and must not alter mutation identity.
	replayed, err := client.SupervisorPolicyConfigure(context.Background(), params)
	if err != nil {
		t.Fatalf("SupervisorPolicyConfigure(replay) error = %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("replayed policy result = %#v, want exact first result %#v", replayed, first)
	}

	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
