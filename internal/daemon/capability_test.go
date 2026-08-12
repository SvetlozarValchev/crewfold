package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/mcp"
)

func TestRunCapabilityFilesArePrivateDeterministicAndAuthenticated(t *testing.T) {
	dataDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "crewfold.sock")
	manager, err := newRunCapabilityManager(dataDir, socketPath)
	if err != nil {
		t.Fatalf("newRunCapabilityManager() error = %v", err)
	}
	const runID = "run_0123456789abcdef0123456789abcdef"
	access, err := manager.PrepareRunCapability(context.Background(), runID)
	if err != nil {
		t.Fatalf("PrepareRunCapability() error = %v", err)
	}
	if access.SocketPath != socketPath || !strings.HasSuffix(access.CapabilityFile, runID+".token") {
		t.Fatalf("capability access = %#v", access)
	}
	info, err := os.Lstat(access.CapabilityFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("capability file info = %#v, %v", info, err)
	}
	keyInfo, err := os.Lstat(filepath.Join(dataDir, "node.key"))
	if err != nil || !keyInfo.Mode().IsRegular() || keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("node key info = %#v, %v", keyInfo, err)
	}
	tokenBytes, err := os.ReadFile(access.CapabilityFile)
	if err != nil {
		t.Fatalf("os.ReadFile(capability) error = %v", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if authenticated, err := manager.authenticate(token); err != nil || authenticated != runID {
		t.Fatalf("authenticate(valid) = %q, %v", authenticated, err)
	}
	if _, err := manager.authenticate(token + "x"); err == nil {
		t.Fatal("authenticate(tampered) unexpectedly succeeded")
	}
	restarted, err := newRunCapabilityManager(dataDir, socketPath)
	if err != nil || restarted.token(runID) != token {
		t.Fatalf("restarted capability token = %q, %v; want stable token", restarted.token(runID), err)
	}
	replayed, err := restarted.PrepareRunCapability(context.Background(), runID)
	if err != nil || replayed != access {
		t.Fatalf("PrepareRunCapability(replay) = %#v, %v; want %#v", replayed, err, access)
	}
}

func TestMCPRejectsCrossRunAndStoppedCapability(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	running := startTestServer(t, config)
	owner := localapi.NewClient(config.SocketPath)
	project, agent := initializeRunWorkerAPI(t, owner, fixtureRoot)
	task := createAssignedRunWorkerTask(t, owner, project.Project.ID, agent.Agent.ID, "scoped MCP stop")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "scoped-mcp-stop", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting for stop", WaitForResume: true}}}
	started, err := owner.RunStart(context.Background(), localapi.RunStartParams{Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "scoped-mcp-stop-run"})
	if err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	active := waitForRunStatus(t, owner, started.Detail.Run.ID, domain.RunActive)
	waitForCondition(t, 5*time.Second, func() bool {
		current, showErr := owner.RunShow(context.Background(), "personal", active.Detail.Run.ID)
		if showErr == nil && current.Detail.Run.StepCursor == 1 {
			active = current
			return true
		}
		return false
	}, "paused scoped MCP run")

	manager, err := newRunCapabilityManager(config.DataDir, config.SocketPath)
	if err != nil {
		t.Fatalf("newRunCapabilityManager() error = %v", err)
	}
	client, err := mcp.Dial(context.Background(), config.SocketPath, manager.token(active.Detail.Run.ID))
	if err != nil {
		t.Fatalf("mcp.Dial() error = %v", err)
	}
	defer client.Close()
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if result, err := client.CallTool(context.Background(), toolStatus, map[string]any{}); err != nil || result.IsError {
		t.Fatalf("CallTool(status while active) = %#v, %v", result, err)
	}
	_, err = client.ReadResource(context.Background(), "crewfold://runs/run_ffffffffffffffffffffffffffffffff/briefing")
	var crossRunError *mcp.RPCError
	if !errors.As(err, &crossRunError) || crossRunError.Data == nil || crossRunError.Data.Code != "out_of_scope" {
		t.Fatalf("ReadResource(cross-run) error = %#v, want out_of_scope", err)
	}
	if _, err := owner.RunStop(context.Background(), localapi.RunStopParams{Workspace: "personal", Run: active.Detail.Run.ID, ExpectedRevision: active.Detail.Run.Revision, GracePeriodMillis: 100, IdempotencyKey: "stop-scoped-mcp-run"}); err != nil {
		t.Fatalf("RunStop() error = %v", err)
	}
	waitForRunStatus(t, owner, active.Detail.Run.ID, domain.RunStopped)
	_, err = client.CallTool(context.Background(), toolStatus, map[string]any{})
	var stoppedError *mcp.RPCError
	if !errors.As(err, &stoppedError) || stoppedError.Data == nil || stoppedError.Data.Code != "denied_by_policy" {
		t.Fatalf("CallTool(stopped capability) error = %#v, want denied_by_policy", err)
	}
	events, err := owner.EventsList(context.Background(), 0, 200)
	if err != nil {
		t.Fatalf("EventsList() error = %v", err)
	}
	denied := 0
	for _, event := range events.Events {
		if event.Type == "run.tool_denied" && event.Entity.ID == active.Detail.Run.ID {
			denied++
		}
	}
	if denied < 2 {
		t.Fatalf("run.tool_denied events = %d, want cross-run and stopped denials", denied)
	}
	if _, err := owner.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
