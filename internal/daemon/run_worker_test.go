package daemon

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

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
