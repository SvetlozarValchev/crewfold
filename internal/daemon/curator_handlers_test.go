package daemon

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestCuratorLocalAPIMapsOwnerQueueRuleAndBoundedProcessing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	project, _ := initializeRunWorkerAPI(t, client, fixtureRoot)

	queued, err := client.CuratorQueue(context.Background(), localapi.CuratorQueueParams{
		Workspace: "personal", Project: project.Project.ID,
	})
	if err != nil || queued.Schema != localapi.CuratorQueueSchema || queued.Type != "curator_queue" ||
		queued.Queue.Rule.Name != domain.CuratorRuleAcceptedMeetingResolutionCopy || queued.Queue.Rule.Revision != 1 || queued.Queue.Rule.Enabled ||
		queued.Queue.Entries == nil || len(queued.Queue.Entries) != 0 {
		t.Fatalf("CuratorQueue() = %#v, %v", queued, err)
	}
	enabled := true
	configured, err := client.CuratorRuleConfigure(context.Background(), localapi.CuratorRuleConfigureParams{
		Workspace: "personal", Rule: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: &enabled, ExpectedRevision: 1, IdempotencyKey: "api-enable-curator-rule",
	})
	if err != nil || configured.Schema != localapi.CuratorRuleMutationSchema || !configured.Rule.Enabled || configured.Rule.Revision != 2 || configured.EventSequence < 1 {
		t.Fatalf("CuratorRuleConfigure() = %#v, %v", configured, err)
	}
	disabled := false
	if _, err := client.CuratorRuleConfigure(context.Background(), localapi.CuratorRuleConfigureParams{
		Workspace: "personal", Rule: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: &disabled, ExpectedRevision: 1, IdempotencyKey: "api-stale-curator-rule",
	}); localAPIErrorCode(err) != "revision_conflict" {
		t.Fatalf("stale CuratorRuleConfigure() error = %v, code=%q", err, localAPIErrorCode(err))
	} else {
		var apiError *localapi.APIError
		if !errors.As(err, &apiError) || apiError.Retryable {
			t.Fatalf("stale CuratorRuleConfigure() API error = %#v", apiError)
		}
	}
	if _, err := client.CuratorQueue(context.Background(), localapi.CuratorQueueParams{
		Workspace: "personal", Project: project.Project.ID, After: "not-a-curator-cursor",
	}); localAPIErrorCode(err) != "invalid_knowledge" {
		t.Fatalf("invalid CuratorQueue cursor error = %v, code=%q", err, localAPIErrorCode(err))
	}
	deriveOnly, err := client.CuratorProcess(context.Background(), localapi.CuratorProcessParams{
		Workspace: "personal", Project: project.Project.ID,
		IdempotencyKey: "api-process-empty-curator-derive-only",
	})
	if err != nil || deriveOnly.Process.CandidatesScanned != 0 || len(deriveOnly.Process.Accepted) != 0 {
		t.Fatalf("CuratorProcess(derive only) = %#v, %v", deriveOnly, err)
	}
	processed, err := client.CuratorProcess(context.Background(), localapi.CuratorProcessParams{
		Workspace: "personal", Project: project.Project.ID, ApplySafe: true,
		IdempotencyKey: "api-process-empty-curator",
	})
	if err != nil || processed.Schema != localapi.CuratorProcessSchema || processed.Process.CandidatesScanned != 0 || processed.Process.Derived == nil || processed.Process.Accepted == nil || processed.Process.Skipped == nil {
		t.Fatalf("CuratorProcess() = %#v, %v", processed, err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestCuratorHandlersRejectUnknownMissingAndUnboundedParameters(t *testing.T) {
	running := startTestServer(t, testConfig(t))
	tests := []struct {
		name   string
		method string
		params map[string]any
	}{
		{name: "queue unknown", method: localapi.MethodCuratorQueue, params: map[string]any{"workspace": "personal", "project": "engine", "actor": "local-owner"}},
		{name: "queue limit zero", method: localapi.MethodCuratorQueue, params: map[string]any{"workspace": "personal", "project": "engine", "limit": 0}},
		{name: "queue over limit", method: localapi.MethodCuratorQueue, params: map[string]any{"workspace": "personal", "project": "engine", "limit": 201}},
		{name: "rule missing enabled", method: localapi.MethodCuratorRuleConfigure, params: map[string]any{"workspace": "personal", "rule": domain.CuratorRuleAcceptedMeetingResolutionCopy, "expected_revision": 1, "idempotency_key": "missing-enabled"}},
		{name: "rule zero revision", method: localapi.MethodCuratorRuleConfigure, params: map[string]any{"workspace": "personal", "rule": domain.CuratorRuleAcceptedMeetingResolutionCopy, "enabled": true, "expected_revision": 0, "idempotency_key": "zero-revision"}},
		{name: "rule unsupported", method: localapi.MethodCuratorRuleConfigure, params: map[string]any{"workspace": "personal", "rule": "generic_model_summary/v1", "enabled": true, "expected_revision": 1, "idempotency_key": "other-rule"}},
		{name: "process missing idempotency", method: localapi.MethodCuratorProcess, params: map[string]any{"workspace": "personal", "project": "engine", "apply_safe": false}},
		{name: "process authority injection", method: localapi.MethodCuratorProcess, params: map[string]any{"workspace": "personal", "project": "engine", "apply_safe": true, "idempotency_key": "actor", "actor": map[string]any{"id": "subsystem:curator", "type": "subsystem"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := rawLocalAPIRequest(t, running.config.SocketPath, test.method, test.params)
			if response.Error == nil || response.Error.Code != "invalid_request" || response.Error.Retryable {
				t.Fatalf("%s error = %#v, want non-retryable invalid_request", test.method, response.Error)
			}
		})
	}
}
