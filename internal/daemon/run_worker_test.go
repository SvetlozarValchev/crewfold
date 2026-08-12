package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

func TestDirectProcessHelper(t *testing.T) {
	for index, argument := range os.Args {
		switch argument {
		case "crewfold-direct-supervisor-helper":
			os.Exit(execution.RunDirectSupervisor(os.Args[index+1:]))
		case "crewfold-fixture-provider-helper":
			os.Exit(execution.RunFixtureProvider(os.Stdin, os.Stdout, os.Stderr))
		}
	}
}

type rejectingBindingProvider struct{}

func (rejectingBindingProvider) Name() string { return "reject-binding" }

func (rejectingBindingProvider) Prepare(_ context.Context, _ domain.Run, scenario domain.FakeScenario) (execution.LaunchSpec, error) {
	if err := execution.ValidateScenario(scenario); err != nil {
		return execution.LaunchSpec{}, err
	}
	return execution.LaunchSpec{Scenario: scenario}, nil
}

func (rejectingBindingProvider) Bind(context.Context, domain.Run, execution.RuntimeBinding) (execution.ProviderBinding, error) {
	return execution.ProviderBinding{}, errors.New("provider rejected runtime binding")
}

func (rejectingBindingProvider) Next(context.Context, domain.Run, domain.FakeScenario, execution.RuntimeSnapshot) (domain.RunObservation, bool, error) {
	return domain.RunObservation{}, false, errors.New("unreachable provider observation")
}

func TestRunWorkerCompletesBlocksResumesAndRejectsInsufficientEvidence(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	project, agent := initializeRunWorkerAPI(t, client, fixtureRoot)
	unavailableAgent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{Workspace: "personal", Name: "unavailable-runtime", Role: "implementer", Provider: "fake", Runtime: "not-registered", IdempotencyKey: "unavailable-runtime-agent"})
	if err != nil {
		t.Fatalf("AgentCreate(unavailable runtime) error = %v", err)
	}
	unavailableTask := createAssignedRunWorkerTask(t, client, project.Project.ID, unavailableAgent.Agent.ID, "unavailable runtime")
	unavailableScenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "unavailable-runtime", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "must not launch"}}}
	if _, err := client.RunStart(context.Background(), localapi.RunStartParams{Workspace: "personal", Task: unavailableTask.Detail.Task.ID, Runtime: "not-registered", Provider: "fake", Scenario: unavailableScenario, ExpectedTaskRevision: unavailableTask.Detail.Task.Revision, IdempotencyKey: "start-unavailable-runtime"}); localAPIErrorCode(err) != "adapter_unavailable" {
		t.Fatalf("RunStart(unavailable runtime) error = %v, code = %q", err, localAPIErrorCode(err))
	}

	for _, testCase := range []struct {
		name               string
		scenario           domain.FakeScenario
		firstStatus        string
		finalStatus        string
		finalTaskStatus    string
		requiresResume     bool
		expectsHandoff     bool
		expectsAssignment  bool
		expectedStepCursor int
	}{
		{
			name: "successful handoff",
			scenario: domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "successful-handoff", Acceptance: domain.AcceptanceRule{RequiredEvidence: []string{"tests_passed"}}, Steps: []domain.FakeStep{
				{Kind: domain.ObservationProgress, Message: "working"},
				{Kind: domain.ObservationCompletion, Message: "done", Evidence: []string{"tests_passed"}, Handoff: "review the completed work"},
			}},
			firstStatus: domain.RunCompleted, finalStatus: domain.RunCompleted, finalTaskStatus: domain.TaskCompleted, expectsHandoff: true, expectedStepCursor: 2,
		},
		{
			name: "blocked then resumed",
			scenario: domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "blocked-then-resumed", Steps: []domain.FakeStep{
				{Kind: domain.ObservationBlocked, Message: "Which behavior should be preserved?"},
				{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review the selected behavior"},
			}},
			firstStatus: domain.RunBlocked, finalStatus: domain.RunCompleted, finalTaskStatus: domain.TaskCompleted, requiresResume: true, expectsHandoff: true, expectedStepCursor: 2,
		},
		{
			name: "insufficient evidence",
			scenario: domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "insufficient-evidence", Acceptance: domain.AcceptanceRule{RequiredEvidence: []string{"tests_passed", "reviewed"}}, Steps: []domain.FakeStep{
				{Kind: domain.ObservationCompletion, Message: "proposed", Evidence: []string{"tests_passed"}, Handoff: "review remains"},
			}},
			firstStatus: domain.RunReview, finalStatus: domain.RunReview, finalTaskStatus: domain.TaskChangesRequested, expectsAssignment: true, expectedStepCursor: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, testCase.name)
			started, err := client.RunStart(context.Background(), localapi.RunStartParams{Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "fake", Provider: "fake", Scenario: testCase.scenario, ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "start-" + testCase.scenario.Name})
			if err != nil {
				t.Fatalf("RunStart() error = %v", err)
			}
			first := waitForRunStatus(t, client, started.Detail.Run.ID, testCase.firstStatus)
			if testCase.requiresResume {
				resumed, err := client.RunResume(context.Background(), localapi.RunResumeParams{Workspace: "personal", Run: first.Detail.Run.ID, ExpectedRevision: first.Detail.Run.Revision, IdempotencyKey: "resume-" + testCase.scenario.Name})
				if err != nil || resumed.Detail.Run.StepCursor != 1 {
					t.Fatalf("RunResume() = %#v, %v", resumed, err)
				}
			}
			final := waitForRunStatus(t, client, started.Detail.Run.ID, testCase.finalStatus)
			if final.Detail.Task.Status != testCase.finalTaskStatus || final.Detail.Run.StepCursor != testCase.expectedStepCursor {
				t.Fatalf("final run = %#v", final.Detail)
			}
			if (final.Detail.Handoff != nil) != testCase.expectsHandoff {
				t.Fatalf("handoff = %#v, expected present = %t", final.Detail.Handoff, testCase.expectsHandoff)
			}
			if (final.Detail.Task.AssignmentID != "") != testCase.expectsAssignment {
				t.Fatalf("task assignment = %q, expected present = %t", final.Detail.Task.AssignmentID, testCase.expectsAssignment)
			}
		})
	}

	startFailureTask := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "start failure")
	failed, err := client.RunStart(context.Background(), localapi.RunStartParams{Workspace: "personal", Task: startFailureTask.Detail.Task.ID, Runtime: "fake", Provider: "fake", Scenario: domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "start-failure", StartFailure: "fixture refused to start"}, ExpectedTaskRevision: startFailureTask.Detail.Task.Revision, IdempotencyKey: "start-failure"})
	if err != nil {
		t.Fatalf("RunStart(start failure) error = %v", err)
	}
	failedRun := waitForRunStatus(t, client, failed.Detail.Run.ID, domain.RunStartFailed)
	if failedRun.Detail.Task.Status != domain.TaskAssigned || failedRun.Detail.Task.AssignmentID == "" || failedRun.Detail.Run.FailureCode != "runtime_start_failed" {
		t.Fatalf("start failure = %#v", failedRun.Detail)
	}

	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunLaunchReconcilesIdempotentlyAfterDaemonRestart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	runtimeDriver := execution.NewFakeRuntime()
	providerAdapter := execution.FakeProvider{}
	config := testConfig(t)
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"fake": runtimeDriver}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"fake": providerAdapter}
	var barrierReached atomic.Bool
	config.RunWorkerHook = func(stage string, _ domain.Run) error {
		if stage == "after_runtime_launch" && barrierReached.CompareAndSwap(false, true) {
			return errors.New("injected daemon restart after launch")
		}
		return nil
	}

	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	project, agent := initializeRunWorkerAPI(t, client, fixtureRoot)
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "restart launch")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "restart-launch", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review after restart"}}}
	started, err := client.RunStart(context.Background(), localapi.RunStartParams{Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "restart-launch"})
	if err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}
	if !barrierReached.Load() || runtimeDriver.LaunchCount() != 1 {
		t.Fatalf("barrier=%t launch count=%d, want reached and one", barrierReached.Load(), runtimeDriver.LaunchCount())
	}

	config.RunWorkerHook = nil
	second := startTestServer(t, config)
	restarted := localapi.NewClient(config.SocketPath)
	completed := waitForRunStatus(t, restarted, started.Detail.Run.ID, domain.RunCompleted)
	if completed.Detail.Run.RuntimeHandle == "" || completed.Detail.Handoff == nil || runtimeDriver.LaunchCount() != 1 {
		t.Fatalf("completed after restart = %#v; launch count=%d", completed.Detail, runtimeDriver.LaunchCount())
	}
	if _, err := restarted.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
}

func TestProviderBindingFailureStopsKnownRuntimeBeforeReleasingCapacity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	runtimeDriver := execution.NewFakeRuntime()
	providerAdapter := rejectingBindingProvider{}
	config := testConfig(t)
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{runtimeDriver.Name(): runtimeDriver}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{providerAdapter.Name(): providerAdapter}
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "binding-failure-workspace"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	project, err := client.ProjectAdd(context.Background(), "personal", "demo", filepath.Join(fixtureRoot, "world-engine"), domain.WriteModeShared, "binding-failure-project")
	if err != nil {
		t.Fatalf("ProjectAdd() error = %v", err)
	}
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{Workspace: "personal", Name: "binding-reviewer", Role: "reviewer", Provider: providerAdapter.Name(), Runtime: runtimeDriver.Name(), IdempotencyKey: "binding-failure-agent"})
	if err != nil {
		t.Fatalf("AgentCreate() error = %v", err)
	}
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "binding failure")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "binding-failure", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "unreachable"}}}
	started, err := client.RunStart(context.Background(), localapi.RunStartParams{Workspace: "personal", Task: task.Detail.Task.ID, Runtime: runtimeDriver.Name(), Provider: providerAdapter.Name(), Scenario: scenario, ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "start-binding-failure"})
	if err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	failed := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunStartFailed)
	if failed.Detail.Run.RuntimeHandle == "" || failed.Detail.Task.Status != domain.TaskAssigned || failed.Detail.Task.AssignmentID == "" {
		t.Fatalf("binding failure detail = %#v", failed.Detail)
	}
	snapshot, err := runtimeDriver.Inspect(context.Background(), failed.Detail.Run.ID, failed.Detail.Run.RuntimeHandle)
	if err != nil || snapshot.State != execution.RuntimeStateStopped {
		t.Fatalf("runtime state after binding failure = %#v, %v", snapshot, err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRequestedRunStartsWhenWorkerIsEnabledAfterRestart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	config.DisableRunWorker = true
	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	project, agent := initializeRunWorkerAPI(t, client, fixtureRoot)
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "requested restart")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "requested-restart", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review after requested restart"}}}
	started, err := client.RunStart(context.Background(), localapi.RunStartParams{Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "requested-restart"})
	if err != nil || started.Detail.Run.Status != domain.RunRequested {
		t.Fatalf("RunStart() = %#v, %v", started, err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(first) error = %v", err)
	}
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}

	config.DisableRunWorker = false
	second := startTestServer(t, config)
	restarted := localapi.NewClient(config.SocketPath)
	completed := waitForRunStatus(t, restarted, started.Detail.Run.ID, domain.RunCompleted)
	if completed.Detail.Handoff == nil || completed.Detail.Run.StepCursor != 1 {
		t.Fatalf("completed requested run = %#v", completed.Detail)
	}
	if _, err := restarted.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
}

func TestDirectRunWorkerSupervisesCompletionCrashTimeoutOutputAndForcedStop(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	directRuntime := execution.NewDirectRuntime(execution.DirectRuntimeOptions{
		StateRoot:            filepath.Join(config.DataDir, "runtime"),
		SupervisorExecutable: executable,
		SupervisorArguments:  []string{"-test.run=^TestDirectProcessHelper$", "--", "crewfold-direct-supervisor-helper"},
	})
	fixtureProvider := execution.FixtureProvider{
		Executable: executable,
		Arguments:  []string{"-test.run=^TestDirectProcessHelper$", "--", "crewfold-fixture-provider-helper"},
	}
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{directRuntime.Name(): directRuntime}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{fixtureProvider.Name(): fixtureProvider}
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "direct-worker-workspace"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	project, err := client.ProjectAdd(context.Background(), "personal", "demo", filepath.Join(fixtureRoot, "world-engine"), domain.WriteModeShared, "direct-worker-project")
	if err != nil {
		t.Fatalf("ProjectAdd() error = %v", err)
	}
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{Workspace: "personal", Name: "direct-implementer", Role: "implementer", Provider: "fixture", Runtime: "direct", MaxConcurrency: 10, IdempotencyKey: "direct-worker-agent"})
	if err != nil {
		t.Fatalf("AgentCreate() error = %v", err)
	}

	start := func(t *testing.T, name string, scenario domain.FakeScenario) localapi.RunMutationResult {
		t.Helper()
		task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, name)
		result, err := client.RunStart(context.Background(), localapi.RunStartParams{Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "direct", Provider: "fixture", Scenario: scenario, ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "start-direct-" + name})
		if err != nil {
			t.Fatalf("RunStart(%s) error = %v", name, err)
		}
		return result
	}

	t.Run("completion and bounded output", func(t *testing.T) {
		scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "direct-completion", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"}}, Process: domain.FixtureProcess{StdoutNoiseBytes: 200000, StderrNoiseBytes: 200000}}
		started := start(t, "completion", scenario)
		completed := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunCompleted)
		if completed.Detail.Run.RuntimeHandle == "" || completed.Detail.Handoff == nil {
			t.Fatalf("completed direct run = %#v", completed.Detail)
		}
		var logs localapi.RunLogsResult
		waitForCondition(t, 5*time.Second, func() bool {
			current, logsErr := client.RunLogs(context.Background(), "personal", started.Detail.Run.ID, 0)
			if logsErr != nil {
				return false
			}
			logs = current
			return current.Logs.State == execution.RuntimeStateExited
		}, "direct logs to reach exited state")
		if !logs.Logs.Stdout.Truncated || !logs.Logs.Stderr.Truncated || logs.Logs.Stdout.OmittedBytes == 0 || logs.Logs.Stderr.OmittedBytes == 0 || logs.Logs.Stdout.CapturedBytes > 64*1024 || logs.Logs.Stderr.CapturedBytes > 64*1024 {
			t.Fatalf("bounded direct logs = %#v", logs.Logs)
		}
	})

	t.Run("crash", func(t *testing.T) {
		scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "direct-crash", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "started"}}, Process: domain.FixtureProcess{ExitCode: 17}}
		started := start(t, "crash", scenario)
		failed := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunFailed)
		if failed.Detail.Run.FailureCode != "process_exited" {
			t.Fatalf("crashed direct run = %#v", failed.Detail.Run)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "direct-timeout", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting"}}, Process: domain.FixtureProcess{TimeoutMillis: 100, HangAfterSteps: true, IgnoreTermination: true}}
		started := start(t, "timeout", scenario)
		failed := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunFailed)
		if failed.Detail.Run.FailureCode != "runtime_timeout" {
			t.Fatalf("timed out direct run = %#v", failed.Detail.Run)
		}
	})

	t.Run("forced stop", func(t *testing.T) {
		scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "direct-stop", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting", WaitForResume: true}}, Process: domain.FixtureProcess{HangAfterSteps: true, IgnoreTermination: true}}
		started := start(t, "stop", scenario)
		var active localapi.RunShowResult
		waitForCondition(t, 5*time.Second, func() bool {
			current, showErr := client.RunShow(context.Background(), "personal", started.Detail.Run.ID)
			if showErr != nil {
				return false
			}
			active = current
			return current.Detail.Run.Status == domain.RunActive && current.Detail.Run.StepCursor == 1
		}, "direct stop fixture checkpoint")
		if _, err := client.RunStop(context.Background(), localapi.RunStopParams{Workspace: "personal", Run: active.Detail.Run.ID, ExpectedRevision: active.Detail.Run.Revision, GracePeriodMillis: 100, IdempotencyKey: "stop-direct-fixture"}); err != nil {
			t.Fatalf("RunStop() error = %v", err)
		}
		stopped := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunStopped)
		if !stopped.Detail.Run.StopForced || stopped.Detail.Task.Status != domain.TaskAssigned || stopped.Detail.Task.AssignmentID == "" {
			t.Fatalf("stopped direct run = %#v", stopped.Detail)
		}
	})

	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestDirectRunReconcilesAcrossDaemonRestartWhileChildContinues(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	newDirectRuntime := func() *execution.DirectRuntime {
		return execution.NewDirectRuntime(execution.DirectRuntimeOptions{
			StateRoot:            filepath.Join(config.DataDir, "runtime"),
			SupervisorExecutable: executable,
			SupervisorArguments:  []string{"-test.run=^TestDirectProcessHelper$", "--", "crewfold-direct-supervisor-helper"},
		})
	}
	fixtureProvider := execution.FixtureProvider{Executable: executable, Arguments: []string{"-test.run=^TestDirectProcessHelper$", "--", "crewfold-fixture-provider-helper"}}
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"direct": newDirectRuntime()}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"fixture": fixtureProvider}

	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "direct-restart-workspace"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	project, err := client.ProjectAdd(context.Background(), "personal", "demo", filepath.Join(fixtureRoot, "world-engine"), domain.WriteModeShared, "direct-restart-project")
	if err != nil {
		t.Fatalf("ProjectAdd() error = %v", err)
	}
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{Workspace: "personal", Name: "direct-restart-agent", Role: "implementer", Provider: "fixture", Runtime: "direct", IdempotencyKey: "direct-restart-agent"})
	if err != nil {
		t.Fatalf("AgentCreate() error = %v", err)
	}
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "direct restart")
	scenario := domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "direct-restart",
		Steps: []domain.FakeStep{
			{Kind: domain.ObservationProgress, Message: "child is running"},
			{Kind: domain.ObservationCompletion, Message: "done after restart", Handoff: "review after restart"},
		},
		Process: domain.FixtureProcess{StepDelayMillis: 500},
	}
	started, err := client.RunStart(context.Background(), localapi.RunStartParams{Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "direct", Provider: "fixture", Scenario: scenario, ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "start-direct-restart"})
	if err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	var before localapi.RunShowResult
	waitForCondition(t, 5*time.Second, func() bool {
		current, showErr := client.RunShow(context.Background(), "personal", started.Detail.Run.ID)
		if showErr != nil {
			return false
		}
		before = current
		return current.Detail.Run.Status == domain.RunActive && current.Detail.Run.StepCursor == 1
	}, "direct child first report before daemon restart")
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(first) error = %v", err)
	}
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}

	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"direct": newDirectRuntime()}
	second := startTestServer(t, config)
	restarted := localapi.NewClient(config.SocketPath)
	completed := waitForRunStatus(t, restarted, started.Detail.Run.ID, domain.RunCompleted)
	if completed.Detail.Run.RuntimeHandle != before.Detail.Run.RuntimeHandle || completed.Detail.Handoff == nil || completed.Detail.Run.StepCursor != 2 {
		t.Fatalf("completed after direct restart = %#v", completed.Detail)
	}
	logs, err := restarted.RunLogs(context.Background(), "personal", started.Detail.Run.ID, 0)
	if err != nil || logs.Logs.State != execution.RuntimeStateExited {
		t.Fatalf("RunLogs(after restart) = %#v, %v", logs, err)
	}
	if _, err := restarted.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
}

func initializeRunWorkerAPI(t *testing.T, client *localapi.Client, fixtureRoot string) (localapi.ProjectAddResult, localapi.AgentMutationResult) {
	t.Helper()
	if _, err := client.WorkspaceInit(context.Background(), "personal", "run-worker-workspace"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	project, err := client.ProjectAdd(context.Background(), "personal", "demo", filepath.Join(fixtureRoot, "world-engine"), domain.WriteModeShared, "run-worker-project")
	if err != nil {
		t.Fatalf("ProjectAdd() error = %v", err)
	}
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{Workspace: "personal", Name: "implementer", Role: "implementer", Provider: "fake", Runtime: "fake", MaxConcurrency: 10, IdempotencyKey: "run-worker-agent"})
	if err != nil {
		t.Fatalf("AgentCreate() error = %v", err)
	}
	return project, agent
}

func createAssignedRunWorkerTask(t *testing.T, client *localapi.Client, projectID, agentID, name string) localapi.TaskMutationResult {
	t.Helper()
	created, err := client.TaskCreate(context.Background(), localapi.TaskCreateParams{Workspace: "personal", Project: projectID, Title: name, Priority: 100, IdempotencyKey: "task-" + name})
	if err != nil {
		t.Fatalf("TaskCreate() error = %v", err)
	}
	assigned, err := client.TaskAssign(context.Background(), localapi.TaskAssignParams{Workspace: "personal", Task: created.Detail.Task.ID, Agent: agentID, LeaseSeconds: 300, ExpectedRevision: created.Detail.Task.Revision, IdempotencyKey: "assign-" + name})
	if err != nil {
		t.Fatalf("TaskAssign() error = %v", err)
	}
	return assigned
}

func waitForRunStatus(t *testing.T, client *localapi.Client, runID, status string) localapi.RunShowResult {
	t.Helper()
	var result localapi.RunShowResult
	waitForCondition(t, 5*time.Second, func() bool {
		current, err := client.RunShow(context.Background(), "personal", runID)
		if err != nil {
			return false
		}
		result = current
		return current.Detail.Run.Status == status
	}, "run "+runID+" status "+status)
	return result
}
