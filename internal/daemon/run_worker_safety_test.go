package daemon

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

type cancellationProbeRuntime struct {
	launchStarted  chan struct{}
	launchCanceled chan struct{}
	startOnce      sync.Once
	cancelOnce     sync.Once
	deadlineOK     bool
}

func newCancellationProbeRuntime() *cancellationProbeRuntime {
	return &cancellationProbeRuntime{launchStarted: make(chan struct{}), launchCanceled: make(chan struct{})}
}

func (*cancellationProbeRuntime) Name() string { return "cancellation-probe" }

func (runtime *cancellationProbeRuntime) Launch(ctx context.Context, _ string, _ domain.RunPlacement, _ execution.LaunchSpec) (execution.RuntimeBinding, error) {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		runtime.deadlineOK = remaining > 0 && remaining <= runLaunchCallTimeout+100*time.Millisecond
	}
	runtime.startOnce.Do(func() { close(runtime.launchStarted) })
	<-ctx.Done()
	runtime.cancelOnce.Do(func() { close(runtime.launchCanceled) })
	return execution.RuntimeBinding{}, ctx.Err()
}

func (*cancellationProbeRuntime) Reconcile(context.Context, string, string) (execution.RuntimeBinding, error) {
	return execution.RuntimeBinding{}, errors.New("unexpected reconciliation")
}

func (*cancellationProbeRuntime) Inspect(context.Context, string, string) (execution.RuntimeSnapshot, error) {
	return execution.RuntimeSnapshot{}, errors.New("unexpected inspection")
}

func (*cancellationProbeRuntime) Stop(context.Context, string, string, execution.StopSpec) (execution.StopResult, error) {
	return execution.StopResult{}, errors.New("unexpected stop")
}

func (*cancellationProbeRuntime) Logs(context.Context, string, string, int) (domain.RunLogs, error) {
	return domain.RunLogs{}, errors.New("unexpected logs")
}

func TestRunLaunchDeadlineIsCanceledByDaemonShutdown(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	runtimeDriver := newCancellationProbeRuntime()
	config := testConfig(t)
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{runtimeDriver.Name(): runtimeDriver}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"fake": execution.FakeProvider{}}
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "run-cancellation-workspace"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	project, err := client.ProjectAdd(context.Background(), "personal", "demo", fixtureRoot+"/world-engine", domain.WriteModeShared, "run-cancellation-project")
	if err != nil {
		t.Fatalf("ProjectAdd() error = %v", err)
	}
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{
		Workspace: "personal", Name: "cancellation-probe", Role: "implementer",
		Provider: "fake", Runtime: runtimeDriver.Name(), IdempotencyKey: "run-cancellation-agent",
	})
	if err != nil {
		t.Fatalf("AgentCreate() error = %v", err)
	}
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "run cancellation")
	if _, err := client.RunStart(context.Background(), localapi.RunStartParams{
		Workspace: "personal", Task: task.Detail.Task.ID, Runtime: runtimeDriver.Name(), Provider: "fake",
		Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "run-cancellation", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "unreachable"}}},
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "start-run-cancellation",
	}); err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	select {
	case <-runtimeDriver.launchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("run worker did not enter runtime launch")
	}
	if !runtimeDriver.deadlineOK {
		t.Fatal("runtime launch did not receive the bounded worker deadline")
	}

	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	select {
	case <-runtimeDriver.launchCanceled:
	case <-time.After(time.Second):
		t.Fatal("daemon shutdown did not cancel runtime launch")
	}
	select {
	case <-running.done:
		if running.err != nil {
			t.Fatalf("Run() error = %v", running.err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon shutdown waited for the launch deadline")
	}
}
