package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/herdr"
	"crewfold/internal/localapi"
	"crewfold/internal/recovery"
	"crewfold/internal/store"
)

const daemonTestNodeID = "33333333333333333333333333333333"

func TestDirectProcessHelper(t *testing.T) {
	for index, argument := range os.Args {
		switch argument {
		case "crewfold-direct-supervisor-helper":
			os.Exit(execution.RunDirectSupervisor(os.Args[index+1:]))
		case "crewfold-fixture-provider-helper":
			os.Exit(execution.RunFixtureProvider(os.Stdin, os.Stdout, os.Stderr))
		case "crewfold-terminal-output-helper":
			_, _ = os.Stdout.WriteString("API_TOKEN=top-secret\n")
			_, _ = os.Stdout.WriteString(strings.Repeat("x", 72*1024) + "\n")
			_, _ = os.Stderr.WriteString("Authorization: Bearer stderr-secret\n")
			os.Exit(0)
		case "crewfold-herdr-pane-supervisor-helper":
			arguments := append([]string(nil), os.Args[index+1:]...)
			if len(arguments) > 0 && arguments[0] == "__herdr-pane-supervisor" {
				arguments = arguments[1:]
			}
			os.Exit(execution.RunHerdrPaneSupervisor(arguments))
		}
	}
}

type terminalOutputProvider struct {
	executable string
}

func (terminalOutputProvider) Name() string { return "terminal-output" }

func (provider terminalOutputProvider) Prepare(_ context.Context, _ domain.Run, scenario domain.FakeScenario) (execution.LaunchSpec, error) {
	if err := execution.ValidateScenario(scenario); err != nil {
		return execution.LaunchSpec{}, err
	}
	return execution.LaunchSpec{Scenario: scenario, Command: &execution.CommandSpec{
		Executable: provider.executable,
		Arguments:  []string{"-test.run=^TestDirectProcessHelper$", "--", "crewfold-terminal-output-helper"},
		// The runtime may retain more than the archive contract. The Store must
		// still commit at most its exact 64 KiB immutable terminal bound.
		OutputByteLimit: 96 * 1024,
	}}, nil
}

func (terminalOutputProvider) Bind(_ context.Context, run domain.Run, binding execution.RuntimeBinding) (execution.ProviderBinding, error) {
	if binding.RuntimeHandle == "" {
		return execution.ProviderBinding{}, errors.New("terminal output provider requires runtime binding")
	}
	return execution.ProviderBinding{ProviderHandle: "terminal-output:" + run.ID}, nil
}

func (terminalOutputProvider) Next(_ context.Context, run domain.Run, _ domain.FakeScenario, snapshot execution.RuntimeSnapshot) (domain.RunObservation, bool, error) {
	if !snapshot.CompletionReady || run.StepCursor != 0 {
		return domain.RunObservation{}, false, nil
	}
	return domain.RunObservation{Kind: domain.ObservationCompletion, Message: "terminal output archived", Handoff: "review immutable logs"}, true, nil
}

type daemonHerdrRunner struct {
	mu        sync.Mutex
	stateRoot string
	label     string
	surface   bool
	command   *exec.Cmd
	stdout    lockedBuffer
	stderr    lockedBuffer
}

func newDaemonHerdrRunner(stateRoot string) *daemonHerdrRunner {
	return &daemonHerdrRunner{stateRoot: stateRoot}
}

func (runner *daemonHerdrRunner) Run(_ context.Context, _ string, arguments []string, _ map[string]string) (herdr.CommandResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	key := strings.Join(arguments, " ")
	switch key {
	case "--version":
		return herdr.CommandResult{Stdout: []byte("herdr 0.8.0\n")}, nil
	case "api schema --json":
		data, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "protocol", "herdr", "schema-compatible.json"))
		return herdr.CommandResult{Stdout: data}, err
	case "api snapshot":
		return herdr.CommandResult{Stdout: runner.snapshotLocked()}, nil
	}
	if len(arguments) >= 2 && arguments[0] == "workspace" && arguments[1] == "create" {
		for index := 2; index+1 < len(arguments); index++ {
			if arguments[index] == "--label" {
				runner.label = arguments[index+1]
				break
			}
		}
		runner.surface = true
		return herdr.CommandResult{Stdout: runner.workspaceCreatedLocked()}, nil
	}
	if len(arguments) == 4 && arguments[0] == "pane" && arguments[1] == "run" {
		command := exec.Command("/bin/sh", "-c", arguments[3])
		command.Stdout, command.Stderr = &runner.stdout, &runner.stderr
		if err := command.Start(); err != nil {
			return herdr.CommandResult{}, err
		}
		runner.command = command
		go func() { _ = command.Wait() }()
		return herdr.CommandResult{}, nil
	}
	if len(arguments) == 4 && arguments[0] == "pane" && arguments[1] == "process-info" && arguments[2] == "--pane" {
		supervisorPID, childPID := runner.statePIDsLocked()
		payload, _ := json.Marshal(map[string]any{"id": "cli:request", "result": map[string]any{
			"type": "pane_process_info", "process_info": map[string]any{"pane_id": "w1:p1", "foreground_processes": []map[string]any{{"pid": supervisorPID, "name": "crewfold"}, {"pid": childPID, "name": "provider"}}},
		}})
		return herdr.CommandResult{Stdout: payload}, nil
	}
	if len(arguments) >= 4 && arguments[0] == "pane" && arguments[1] == "read" {
		return herdr.CommandResult{Stdout: []byte(runner.stdout.String() + runner.stderr.String())}, nil
	}
	if len(arguments) == 3 && arguments[0] == "pane" && arguments[1] == "close" {
		runner.surface = false
		return herdr.CommandResult{}, nil
	}
	if len(arguments) >= 4 && arguments[0] == "pane" && (arguments[1] == "send-keys" || arguments[1] == "send-text") {
		return herdr.CommandResult{}, nil
	}
	return herdr.CommandResult{ExitCode: 1, Stderr: []byte(`{"error":{"code":"unexpected_command","message":"` + key + `"}}`)}, nil
}

func (runner *daemonHerdrRunner) snapshotLocked() []byte {
	workspaces, tabs, panes := []map[string]any{}, []map[string]any{}, []map[string]any{}
	if runner.surface {
		workspaces = append(workspaces, map[string]any{"workspace_id": "w1", "label": runner.label})
		tabs = append(tabs, map[string]any{"tab_id": "w1:t1", "workspace_id": "w1"})
		panes = append(panes, map[string]any{"pane_id": "w1:p1", "terminal_id": "term-runtime", "workspace_id": "w1", "tab_id": "w1:t1"})
	}
	payload, _ := json.Marshal(map[string]any{"id": "cli:api:snapshot", "result": map[string]any{
		"type": "session_snapshot", "snapshot": map[string]any{"version": "0.8.0", "protocol": 19, "workspaces": workspaces, "tabs": tabs, "panes": panes, "layouts": []any{}, "agents": []any{}},
	}})
	return payload
}

func (runner *daemonHerdrRunner) workspaceCreatedLocked() []byte {
	payload, _ := json.Marshal(map[string]any{"id": "cli:request", "result": map[string]any{
		"type": "workspace_created", "workspace": map[string]any{"workspace_id": "w1", "label": runner.label},
		"tab":       map[string]any{"tab_id": "w1:t1", "workspace_id": "w1"},
		"root_pane": map[string]any{"pane_id": "w1:p1", "terminal_id": "term-runtime", "workspace_id": "w1", "tab_id": "w1:t1"},
	}})
	return payload
}

func (runner *daemonHerdrRunner) statePIDsLocked() (int, int) {
	entries, _ := os.ReadDir(runner.stateRoot)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(runner.stateRoot, entry.Name(), "state.json"))
		if err != nil {
			continue
		}
		var state struct {
			SupervisorPID int `json:"supervisor_pid"`
			ChildPID      int `json:"child_pid"`
		}
		if json.Unmarshal(data, &state) == nil {
			return state.SupervisorPID, state.ChildPID
		}
	}
	return 1, 1
}

func (runner *daemonHerdrRunner) shutdown() {
	runner.mu.Lock()
	command := runner.command
	runner.mu.Unlock()
	if command != nil && command.Process != nil {
		_ = command.Process.Signal(os.Interrupt)
	}
}

func writeDaemonHerdrSupervisorWrapper(t *testing.T, executable string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crewfold-herdr-test-wrapper")
	quotedExecutable := "'" + strings.ReplaceAll(executable, "'", "'\\''") + "'"
	content := "#!/bin/sh\nexec " + quotedExecutable + " -test.run='^TestDirectProcessHelper$' -- crewfold-herdr-pane-supervisor-helper \"$@\"\n"
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("write Herdr supervisor wrapper: %v", err)
	}
	return path
}

func TestHerdrTerminalArchiveSurvivesRestartAndBackupActivationWithoutPaneState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	stateRoot := filepath.Join(config.DataDir, "runtime", "herdr")
	runner := newDaemonHerdrRunner(stateRoot)
	wrapper := writeDaemonHerdrSupervisorWrapper(t, executable)
	newRuntime := func(root string, commandRunner herdr.CommandRunner) *execution.HerdrRuntime {
		return execution.NewHerdrRuntime(execution.HerdrRuntimeOptions{
			NodeID: daemonTestNodeID, StateRoot: root, HerdrExecutable: "/fixture/herdr",
			CrewfoldExecutable: wrapper, CommandRunner: commandRunner,
			StartupTimeout: 3 * time.Second, PollInterval: 5 * time.Millisecond,
		})
	}
	provider := terminalOutputProvider{executable: executable}
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"herdr": newRuntime(stateRoot, runner)}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{provider.Name(): provider}
	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	project, _ := initializeRunWorkerAPI(t, client, fixtureRoot)
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{
		Workspace: "personal", Name: "herdr-terminal-archive", Role: "implementer", Provider: provider.Name(), Runtime: "herdr",
		IdempotencyKey: "herdr-terminal-archive-agent",
	})
	if err != nil {
		t.Fatalf("AgentCreate() error = %v", err)
	}
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "herdr terminal archive")
	started, err := client.RunStart(context.Background(), localapi.RunStartParams{
		Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "herdr", Provider: provider.Name(),
		Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "herdr-terminal-archive", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"}}},
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "herdr-terminal-archive-run",
	})
	if err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	completed := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunCompleted)
	if completed.Detail.Handoff == nil {
		t.Fatalf("completed Herdr run has no handoff: %#v", completed.Detail)
	}
	archived, err := client.RunLogs(context.Background(), "personal", started.Detail.Run.ID, 0)
	if err != nil {
		t.Fatalf("RunLogs(Herdr terminal) error = %v", err)
	}
	if archived.Logs.State != execution.RuntimeStateExited || archived.Logs.Stdout.CapturedBytes > 64*1024 ||
		archived.Logs.Stdout.CapturedBytes != int64(len(archived.Logs.Stdout.Text)) || !archived.Logs.Stdout.Truncated || archived.Logs.Stdout.OmittedBytes == 0 ||
		strings.Contains(archived.Logs.Stdout.Text, "top-secret") || strings.Contains(archived.Logs.Stdout.Text, "stderr-secret") || !strings.Contains(archived.Logs.Stdout.Text, "[REDACTED]") {
		t.Fatalf("bounded/redacted Herdr archive = %#v", archived.Logs)
	}
	runner.shutdown()
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(first Herdr daemon) error = %v", err)
	}
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first Herdr daemon) error = %v", err)
	}

	restartRunner := newDaemonHerdrRunner(stateRoot)
	restartConfig := config
	restartConfig.RuntimeDrivers = map[string]execution.RuntimeDriver{"herdr": newRuntime(stateRoot, restartRunner)}
	second := startTestServer(t, restartConfig)
	restarted := localapi.NewClient(restartConfig.SocketPath)
	restartedLogs, err := restarted.RunLogs(context.Background(), "personal", started.Detail.Run.ID, 0)
	if err != nil || !reflect.DeepEqual(restartedLogs, archived) {
		t.Fatalf("RunLogs(after Herdr daemon restart) = %#v, %v; want exact %#v", restartedLogs, err, archived)
	}
	bundlePath := filepath.Join(t.TempDir(), "herdr-terminal-bundle")
	if _, err := restarted.BackupCreate(context.Background(), localapi.BackupCreateParams{TargetPath: bundlePath, IdempotencyKey: "herdr-terminal-bundle"}); err != nil {
		t.Fatalf("BackupCreate(Herdr terminal archive) error = %v", err)
	}
	if _, err := restarted.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second Herdr daemon) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second Herdr daemon) error = %v", err)
	}

	restoredDataDir := filepath.Join(t.TempDir(), "restored-herdr-terminal")
	if _, err := recovery.RestorePending(context.Background(), bundlePath, restoredDataDir); err != nil {
		t.Fatalf("RestorePending(Herdr terminal archive) error = %v", err)
	}
	if _, err := recovery.Activate(context.Background(), restoredDataDir, true); err != nil {
		t.Fatalf("Activate(Herdr terminal archive) error = %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(restoredDataDir, "runtime")); err != nil || len(entries) != 0 {
		t.Fatalf("restored Herdr backup retained source pane/runtime state: entries=%v error=%v", entries, err)
	}
	restoredStateRoot := filepath.Join(restoredDataDir, "runtime", "herdr")
	restoredConfig := config
	restoredConfig.DataDir = restoredDataDir
	restoredConfig.SocketPath = filepath.Join(t.TempDir(), "restored-herdr.sock")
	restoredConfig.RuntimeDrivers = map[string]execution.RuntimeDriver{"herdr": newRuntime(restoredStateRoot, newDaemonHerdrRunner(restoredStateRoot))}
	restored := startTestServer(t, restoredConfig)
	restoredClient := localapi.NewClient(restoredConfig.SocketPath)
	restoredLogs, err := restoredClient.RunLogs(context.Background(), "personal", started.Detail.Run.ID, 0)
	if err != nil || !reflect.DeepEqual(restoredLogs, archived) {
		t.Fatalf("RunLogs(after Herdr backup activation) = %#v, %v; want exact %#v", restoredLogs, err, archived)
	}
	if _, err := restoredClient.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(restored Herdr daemon) error = %v", err)
	}
	if err := restored.wait(); err != nil {
		t.Fatalf("Run(restored Herdr daemon) error = %v", err)
	}
}

func TestDirectTerminalArchiveIsExactBoundedAndRedactedAfterRestart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	newRuntime := func() *execution.DirectRuntime {
		return execution.NewDirectRuntime(execution.DirectRuntimeOptions{
			NodeID: daemonTestNodeID, StateRoot: filepath.Join(config.DataDir, "runtime"), SupervisorExecutable: executable,
			SupervisorArguments: []string{"-test.run=^TestDirectProcessHelper$", "--", "crewfold-direct-supervisor-helper"},
		})
	}
	provider := terminalOutputProvider{executable: executable}
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"direct": newRuntime()}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{provider.Name(): provider}
	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	project, _ := initializeRunWorkerAPI(t, client, fixtureRoot)
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{
		Workspace: "personal", Name: "direct-terminal-archive", Role: "implementer", Provider: provider.Name(), Runtime: "direct",
		IdempotencyKey: "direct-terminal-archive-agent",
	})
	if err != nil {
		t.Fatalf("AgentCreate() error = %v", err)
	}
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "direct terminal redacted archive")
	started, err := client.RunStart(context.Background(), localapi.RunStartParams{
		Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "direct", Provider: provider.Name(),
		Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "direct-terminal-redacted", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"}}},
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "direct-terminal-redacted-run",
	})
	if err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunCompleted)
	archived, err := client.RunLogs(context.Background(), "personal", started.Detail.Run.ID, 0)
	if err != nil {
		t.Fatalf("RunLogs(direct terminal) error = %v", err)
	}
	if archived.Logs.State != execution.RuntimeStateExited || archived.Logs.Stdout.CapturedBytes > 64*1024 ||
		archived.Logs.Stdout.CapturedBytes != int64(len(archived.Logs.Stdout.Text)) || !archived.Logs.Stdout.Truncated || archived.Logs.Stdout.OmittedBytes == 0 ||
		strings.Contains(archived.Logs.Stdout.Text, "top-secret") || !strings.Contains(archived.Logs.Stdout.Text, "[REDACTED]") ||
		strings.Contains(archived.Logs.Stderr.Text, "stderr-secret") || !strings.Contains(archived.Logs.Stderr.Text, "[REDACTED]") {
		t.Fatalf("bounded/redacted direct archive = %#v", archived.Logs)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(first direct archive daemon) error = %v", err)
	}
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first direct archive daemon) error = %v", err)
	}
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"direct": newRuntime()}
	second := startTestServer(t, config)
	restarted := localapi.NewClient(config.SocketPath)
	restartedLogs, err := restarted.RunLogs(context.Background(), "personal", started.Detail.Run.ID, 0)
	if err != nil || !reflect.DeepEqual(restartedLogs, archived) {
		t.Fatalf("RunLogs(after direct terminal restart) = %#v, %v; want exact %#v", restartedLogs, err, archived)
	}
	if _, err := restarted.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second direct archive daemon) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second direct archive daemon) error = %v", err)
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

type uncertainObservationProvider struct{}

func (uncertainObservationProvider) Name() string { return "uncertain-observation" }

func (uncertainObservationProvider) Prepare(ctx context.Context, run domain.Run, scenario domain.FakeScenario) (execution.LaunchSpec, error) {
	return (execution.FakeProvider{}).Prepare(ctx, run, scenario)
}

func (uncertainObservationProvider) Bind(_ context.Context, run domain.Run, binding execution.RuntimeBinding) (execution.ProviderBinding, error) {
	if binding.RuntimeHandle == "" {
		return execution.ProviderBinding{}, errors.New("missing runtime binding")
	}
	return execution.ProviderBinding{ProviderHandle: "uncertain-provider:" + run.ID}, nil
}

func (uncertainObservationProvider) Next(context.Context, domain.Run, domain.FakeScenario, execution.RuntimeSnapshot) (domain.RunObservation, bool, error) {
	return domain.RunObservation{}, false, errors.New("provider lost the terminal observation")
}

type stopLogOrderRuntime struct {
	base           *execution.FakeRuntime
	stopped        atomic.Bool
	logsBeforeStop atomic.Bool
}

type refusingForeignRuntime struct {
	contacts atomic.Int64
}

func (*refusingForeignRuntime) Name() string { return "fake" }

func (runtime *refusingForeignRuntime) contacted() error {
	runtime.contacts.Add(1)
	return errors.New("foreign runtime must not be contacted")
}

func (runtime *refusingForeignRuntime) Launch(context.Context, string, domain.RunPlacement, execution.LaunchSpec) (execution.RuntimeBinding, error) {
	return execution.RuntimeBinding{}, runtime.contacted()
}

func (runtime *refusingForeignRuntime) Reconcile(context.Context, string, string) (execution.RuntimeBinding, error) {
	return execution.RuntimeBinding{}, runtime.contacted()
}

func (runtime *refusingForeignRuntime) Inspect(context.Context, string, string) (execution.RuntimeSnapshot, error) {
	return execution.RuntimeSnapshot{}, runtime.contacted()
}

func (runtime *refusingForeignRuntime) Stop(context.Context, string, string, execution.StopSpec) (execution.StopResult, error) {
	return execution.StopResult{}, runtime.contacted()
}

func (runtime *refusingForeignRuntime) Logs(context.Context, string, string, int) (domain.RunLogs, error) {
	return domain.RunLogs{}, runtime.contacted()
}

func (runtime *refusingForeignRuntime) Prompt(context.Context, string, string, string) error {
	return runtime.contacted()
}

func (runtime *refusingForeignRuntime) Interrupt(context.Context, string, string) error {
	return runtime.contacted()
}

func (runtime *refusingForeignRuntime) Attach(context.Context, string, string) (execution.AttachSpec, error) {
	return execution.AttachSpec{}, runtime.contacted()
}

func TestForeignNodeBindingRejectsEveryControlAndStopBeforeRuntimeContact(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"fake": execution.NewFakeRuntime()}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"fake": execution.FakeProvider{}}
	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	project, _ := initializeRunWorkerAPI(t, client, fixtureRoot)
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{
		Workspace: "personal", Name: "foreign-binding", Role: "implementer", Provider: "fake", Runtime: "fake",
		IdempotencyKey: "foreign-binding-agent",
	})
	if err != nil {
		t.Fatalf("AgentCreate() error = %v", err)
	}
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "foreign node runtime binding")
	started, err := client.RunStart(context.Background(), localapi.RunStartParams{
		Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "fake", Provider: "fake",
		Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "foreign-binding", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting", WaitForResume: true}}},
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "foreign-binding-run",
	})
	if err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	active := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunActive)
	first.cancel()
	if err := first.wait(); err != nil {
		t.Fatalf("first daemon shutdown error = %v", err)
	}
	for _, name := range []string{"node.id", "node.key"} {
		if err := os.Remove(filepath.Join(config.DataDir, name)); err != nil {
			t.Fatalf("remove old %s for foreign-node fixture: %v", name, err)
		}
	}

	foreignRuntime := &refusingForeignRuntime{}
	foreignConfig := config
	foreignConfig.DisableRunWorker = true
	foreignConfig.RuntimeDrivers = map[string]execution.RuntimeDriver{"fake": foreignRuntime}
	foreign := startTestServer(t, foreignConfig)
	foreignClient := localapi.NewClient(foreignConfig.SocketPath)
	foreignRun, err := foreignClient.RunShow(context.Background(), "personal", active.Detail.Run.ID)
	if err != nil || foreignRun.Detail.Run.Status != domain.RunActive {
		t.Fatalf("RunShow(foreign node) = %#v, %v", foreignRun, err)
	}
	for name, call := range map[string]func() error{
		"logs": func() error {
			_, err := foreignClient.RunLogs(context.Background(), "personal", active.Detail.Run.ID, 0)
			return err
		},
		"prompt": func() error {
			_, err := foreignClient.RunPrompt(context.Background(), "personal", active.Detail.Run.ID, "must not deliver")
			return err
		},
		"interrupt": func() error {
			_, err := foreignClient.RunInterrupt(context.Background(), "personal", active.Detail.Run.ID)
			return err
		},
		"attach": func() error {
			_, err := foreignClient.RunAttach(context.Background(), "personal", active.Detail.Run.ID)
			return err
		},
	} {
		if err := call(); localAPIErrorCode(err) != store.CodeRuntimeBindingUnavailable {
			t.Fatalf("%s(foreign node) error = %v, code = %q", name, err, localAPIErrorCode(err))
		}
	}
	if contacts := foreignRuntime.contacts.Load(); contacts != 0 {
		t.Fatalf("foreign interactive controls contacted runtime %d times", contacts)
	}
	if _, err := foreignClient.RunResume(context.Background(), localapi.RunResumeParams{
		Workspace: "personal", Run: active.Detail.Run.ID, ExpectedRevision: foreignRun.Detail.Run.Revision,
		IdempotencyKey: "foreign-binding-resume",
	}); localAPIErrorCode(err) != store.CodeRuntimeBindingUnavailable {
		t.Fatalf("RunResume(foreign binding) error = %v, code = %q; want %q", err, localAPIErrorCode(err), store.CodeRuntimeBindingUnavailable)
	}
	beforeEvents, err := foreignClient.EventsTimeline(context.Background(), localapi.EventsTimelineParams{
		Workspace: "personal", EntityType: "run", EntityID: active.Detail.Run.ID, PageParams: localapi.PageParams{Limit: 100},
	})
	if err != nil {
		t.Fatalf("EventsTimeline(before foreign stop) error = %v", err)
	}
	beforeTaskTimeline, err := foreignClient.TaskTimeline(context.Background(), "personal", active.Detail.Task.ID)
	if err != nil {
		t.Fatalf("TaskTimeline(before foreign stop) error = %v", err)
	}
	if _, err := foreignClient.RunStop(context.Background(), localapi.RunStopParams{
		Workspace: "personal", Run: active.Detail.Run.ID, ExpectedRevision: foreignRun.Detail.Run.Revision,
		GracePeriodMillis: 100, IdempotencyKey: "foreign-binding-stop",
	}); localAPIErrorCode(err) != store.CodeRuntimeBindingUnavailable {
		t.Fatalf("RunStop(foreign binding) error = %v, code = %q; want %q", err, localAPIErrorCode(err), store.CodeRuntimeBindingUnavailable)
	}
	afterRun, err := foreignClient.RunShow(context.Background(), "personal", active.Detail.Run.ID)
	if err != nil {
		t.Fatalf("RunShow(after foreign stop refusal) error = %v", err)
	}
	afterEvents, err := foreignClient.EventsTimeline(context.Background(), localapi.EventsTimelineParams{
		Workspace: "personal", EntityType: "run", EntityID: active.Detail.Run.ID, PageParams: localapi.PageParams{Limit: 100},
	})
	if err != nil {
		t.Fatalf("EventsTimeline(after foreign stop refusal) error = %v", err)
	}
	afterTaskTimeline, err := foreignClient.TaskTimeline(context.Background(), "personal", active.Detail.Task.ID)
	if err != nil {
		t.Fatalf("TaskTimeline(after foreign stop refusal) error = %v", err)
	}
	if !reflect.DeepEqual(afterRun, foreignRun) || !reflect.DeepEqual(afterEvents.Events, beforeEvents.Events) || !reflect.DeepEqual(afterTaskTimeline, beforeTaskTimeline) {
		t.Fatalf("foreign stop refusal changed public state/events: run_before=%#v run_after=%#v events_before=%#v events_after=%#v task_before=%#v task_after=%#v",
			foreignRun, afterRun, beforeEvents.Events, afterEvents.Events, beforeTaskTimeline, afterTaskTimeline)
	}
	if _, err := foreignClient.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(foreign daemon) error = %v", err)
	}
	if err := foreign.wait(); err != nil {
		t.Fatalf("foreign daemon shutdown error = %v", err)
	}

	reconcileConfig := config
	reconcileConfig.RuntimeDrivers = map[string]execution.RuntimeDriver{"fake": foreignRuntime}
	reconciler := startTestServer(t, reconcileConfig)
	reconcileClient := localapi.NewClient(reconcileConfig.SocketPath)
	lost := waitForRunStatus(t, reconcileClient, active.Detail.Run.ID, domain.RunLost)
	if lost.Detail.Run.RuntimeHandle != "" {
		// Public projections never reveal handles; the assertion documents that
		// loss remains an owner-resolution state rather than an attach surface.
		t.Fatalf("lost public run leaked runtime handle: %#v", lost.Detail.Run)
	}
	if contacts := foreignRuntime.contacts.Load(); contacts != 0 {
		t.Fatalf("foreign stop/reconciliation contacted runtime %d times", contacts)
	}
	if _, err := reconcileClient.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(reconciler) error = %v", err)
	}
	if err := reconciler.wait(); err != nil {
		t.Fatalf("reconciler shutdown error = %v", err)
	}
}

func newStopLogOrderRuntime() *stopLogOrderRuntime {
	return &stopLogOrderRuntime{base: execution.NewFakeRuntime()}
}

func (*stopLogOrderRuntime) Name() string { return "stop-log-order" }

func (runtime *stopLogOrderRuntime) Launch(ctx context.Context, operationID string, placement domain.RunPlacement, spec execution.LaunchSpec) (execution.RuntimeBinding, error) {
	return runtime.base.Launch(ctx, operationID, placement, spec)
}

func (runtime *stopLogOrderRuntime) Reconcile(ctx context.Context, operationID, handle string) (execution.RuntimeBinding, error) {
	return runtime.base.Reconcile(ctx, operationID, handle)
}

func (runtime *stopLogOrderRuntime) Inspect(ctx context.Context, operationID, handle string) (execution.RuntimeSnapshot, error) {
	return runtime.base.Inspect(ctx, operationID, handle)
}

func (runtime *stopLogOrderRuntime) Stop(ctx context.Context, operationID, handle string, spec execution.StopSpec) (execution.StopResult, error) {
	result, err := runtime.base.Stop(ctx, operationID, handle, spec)
	if err == nil {
		runtime.stopped.Store(true)
	}
	return result, err
}

func (runtime *stopLogOrderRuntime) Logs(context.Context, string, string, int) (domain.RunLogs, error) {
	if !runtime.stopped.Load() {
		runtime.logsBeforeStop.Store(true)
		return domain.RunLogs{}, errors.New("logs requested before definitive stop")
	}
	return domain.RunLogs{}, errors.New("terminal log capture unavailable after definitive stop")
}

func TestRunStopCapturesOnlyAfterDefinitiveStopAndRecordsUnavailable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	runtimeDriver := newStopLogOrderRuntime()
	fakeRuntime := execution.NewFakeRuntime()
	config := testConfig(t)
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{runtimeDriver.Name(): runtimeDriver, fakeRuntime.Name(): fakeRuntime}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"fake": execution.FakeProvider{}}
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	project, _ := initializeRunWorkerAPI(t, client, fixtureRoot)
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{
		Workspace: "personal", Name: "stop-order", Role: "arbitrary-display", Provider: "fake", Runtime: runtimeDriver.Name(),
		IdempotencyKey: "stop-order-agent",
	})
	if err != nil {
		t.Fatalf("AgentCreate() error = %v", err)
	}
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "stop log ordering")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "stop-log-order", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "running"}}}
	started, err := client.RunStart(context.Background(), localapi.RunStartParams{
		Workspace: "personal", Task: task.Detail.Task.ID, Runtime: runtimeDriver.Name(), Provider: "fake", Scenario: scenario,
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "stop-log-order-start",
	})
	if err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	active := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunActive)
	if _, err := client.RunStop(context.Background(), localapi.RunStopParams{
		Workspace: "personal", Run: active.Detail.Run.ID, ExpectedRevision: active.Detail.Run.Revision,
		GracePeriodMillis: 100, IdempotencyKey: "stop-log-order-stop",
	}); err != nil {
		t.Fatalf("RunStop() error = %v", err)
	}
	stopped := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunStopped)
	if runtimeDriver.logsBeforeStop.Load() || stopped.Detail.Run.Status != domain.RunStopped {
		t.Fatalf("stop/log ordering leaked preterminal capture: before=%t detail=%#v", runtimeDriver.logsBeforeStop.Load(), stopped.Detail)
	}
	if _, err := client.RunLogs(context.Background(), "personal", started.Detail.Run.ID, 0); err == nil || !strings.Contains(err.Error(), store.CodeRunLogsUnavailable) || !strings.Contains(err.Error(), "after definitive stop") {
		t.Fatalf("RunLogs(unavailable after stop) error = %v", err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunLostResolutionReplaysAcrossDistinctRequestsAndRejectsSemanticConflict(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"fake": execution.NewFakeRuntime()}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{
		"fake":                  execution.FakeProvider{},
		"uncertain-observation": uncertainObservationProvider{},
	}
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	project, _ := initializeRunWorkerAPI(t, client, fixtureRoot)
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{
		Workspace: "personal", Name: "uncertain-observer", Role: "implementer", Provider: "uncertain-observation", Runtime: "fake",
		MaxConcurrency: 1, IdempotencyKey: "uncertain-observation-agent",
	})
	if err != nil {
		t.Fatalf("AgentCreate(uncertain observation) error = %v", err)
	}
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "owner resolves lost runtime")
	started, err := client.RunStart(context.Background(), localapi.RunStartParams{
		Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "fake", Provider: "uncertain-observation",
		Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "owner-resolves-lost", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "unobservable"}}},
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "start-owner-resolves-lost",
	})
	if err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	lost := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunLost)
	if lost.Detail.Task.Status != domain.TaskBlocked || lost.Detail.Task.AssignmentID == "" || lost.Detail.Run.FinishedAt != "" {
		t.Fatalf("lost run = %#v", lost.Detail)
	}
	listed, err := client.RunList(context.Background(), localapi.RunListParams{Workspace: "personal", Task: task.Detail.Task.ID, PageParams: localapi.PageParams{Limit: 20}})
	if err != nil || len(listed.Runs) != 1 || listed.Runs[0].ID != lost.Detail.Run.ID || listed.Runs[0].CanAttach {
		t.Fatalf("RunList(lost) = %#v, %v; lost binding must not advertise attach", listed, err)
	}
	if _, err := client.RunPrompt(context.Background(), "personal", lost.Detail.Run.ID, "do not deliver"); localAPIErrorCode(err) != store.CodeRunConflict {
		t.Fatalf("RunPrompt(lost) error = %v, code = %q", err, localAPIErrorCode(err))
	}
	if _, err := client.RunInterrupt(context.Background(), "personal", lost.Detail.Run.ID); localAPIErrorCode(err) != store.CodeRunConflict {
		t.Fatalf("RunInterrupt(lost) error = %v, code = %q", err, localAPIErrorCode(err))
	}
	if _, err := client.RunAttach(context.Background(), "personal", lost.Detail.Run.ID); localAPIErrorCode(err) != store.CodeRunConflict {
		t.Fatalf("RunAttach(lost) error = %v, code = %q", err, localAPIErrorCode(err))
	}

	params := localapi.RunLostResolveParams{
		Workspace: "personal", Run: lost.Detail.Run.ID, ExpectedRevision: lost.Detail.Run.Revision,
		Note: "owner independently verified the runtime was retired", RuntimeRetiredConfirmed: true,
		IdempotencyKey: "resolve-owner-lost-runtime",
	}
	resolved, err := client.RunLostResolve(context.Background(), params)
	if err != nil {
		t.Fatalf("RunLostResolve() error = %v", err)
	}
	replayed, err := client.RunLostResolve(context.Background(), params)
	if err != nil {
		t.Fatalf("RunLostResolve(distinct transport request replay) error = %v", err)
	}
	if replayed.EventSequence != resolved.EventSequence || replayed.Detail.Run.ID != resolved.Detail.Run.ID ||
		replayed.Detail.Run.Revision != resolved.Detail.Run.Revision || replayed.Resolution != resolved.Resolution {
		t.Fatalf("RunLostResolve(replay) = %#v; want exact %#v", replayed, resolved)
	}
	if resolved.Detail.Run.Status != domain.RunFailed || resolved.Detail.Run.FailureCode != "runtime_retired_by_owner" ||
		resolved.Detail.Task.Status != domain.TaskBlocked || resolved.Detail.Task.AssignmentID != "" ||
		resolved.Resolution.Resolution != "owner_confirmed_effects_ended" || resolved.Resolution.EventSequence != resolved.EventSequence {
		t.Fatalf("resolved run = %#v", resolved)
	}
	changed := params
	changed.Note = "different semantic retirement assertion"
	if _, err := client.RunLostResolve(context.Background(), changed); localAPIErrorCode(err) != store.CodeIdempotencyConflict {
		t.Fatalf("RunLostResolve(changed semantic replay) error = %v, code = %q", err, localAPIErrorCode(err))
	}
	timeline, err := client.EventsTimeline(context.Background(), localapi.EventsTimelineParams{
		Workspace: "personal", EntityType: "run", EntityID: lost.Detail.Run.ID, PageParams: localapi.PageParams{Limit: 100},
	})
	if err != nil {
		t.Fatalf("EventsTimeline() error = %v", err)
	}
	resolutionEvents := 0
	for _, event := range timeline.Events {
		if event.Type == "run.lost_resolved" {
			resolutionEvents++
		}
	}
	if resolutionEvents != 1 {
		t.Fatalf("run.lost_resolved event count = %d, want 1", resolutionEvents)
	}

	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
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
	if completed.Detail.Run.RuntimeHandle != "" || completed.Detail.Run.ProviderHandle != "" || completed.Detail.Handoff == nil || runtimeDriver.LaunchCount() != 1 {
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
	if failed.Detail.Run.RuntimeHandle != "" || failed.Detail.Run.ProviderHandle != "" || failed.Detail.Task.Status != domain.TaskAssigned || failed.Detail.Task.AssignmentID == "" {
		t.Fatalf("binding failure detail = %#v", failed.Detail)
	}
	snapshot, err := runtimeDriver.Inspect(context.Background(), failed.Detail.Run.ID, "fake-runtime:"+failed.Detail.Run.ID)
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

func TestSupervisorOriginRunCommittedBeforeWorkerLaunchesExactlyOnceAfterRestart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	testCases := []struct {
		name            string
		crashStage      string
		ownerBlockStage string
	}{
		{name: "requested run survives restart"},
		{name: "starting without handle survives crash", crashStage: "after_run_starting"},
		{name: "starting with handle reconciles after crash", crashStage: "after_runtime_launch"},
		{name: "owner block is rejected for requested reservation", ownerBlockStage: domain.RunRequested},
		{name: "owner block is rejected across starting launch gate", ownerBlockStage: domain.RunStarting},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {

			fixtureRoot := t.TempDir()
			createGitFixture(t, fixtureRoot)
			runtimeDriver := execution.NewFakeRuntime()
			config := testConfig(t)
			config.RuntimeDrivers = map[string]execution.RuntimeDriver{"fake": runtimeDriver}
			config.ProviderAdapters = map[string]execution.ProviderAdapter{"fake": execution.FakeProvider{}}
			config.DisableRunWorker = true
			config.DisableSupervisor = true

			first := startTestServer(t, config)
			client := localapi.NewClient(config.SocketPath)
			if _, err := client.WorkspaceInit(context.Background(), "personal", "supervisor-recovery-workspace"); err != nil {
				t.Fatalf("WorkspaceInit() error = %v", err)
			}
			project, err := client.ProjectAdd(context.Background(), "personal", "demo", filepath.Join(fixtureRoot, "world-engine"), domain.WriteModeShared, "supervisor-recovery-project")
			if err != nil {
				t.Fatalf("ProjectAdd() error = %v", err)
			}
			manager, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{
				Workspace: "personal", Name: "recovery-manager", Role: "nebula surveyor", Provider: "fake", Runtime: "fake", MaxConcurrency: 1,
				IdempotencyKey: "supervisor-recovery-manager",
			})
			if err != nil {
				t.Fatalf("AgentCreate(manager) = %v", err)
			}
			target, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{
				Workspace: "personal", Name: "recovery-target", Role: "nebula surveyor", Provider: "fake", Runtime: "fake", MaxConcurrency: 1,
				IdempotencyKey: "supervisor-recovery-target",
			})
			if err != nil {
				t.Fatalf("AgentCreate(target) = %v", err)
			}
			objective, err := client.ObjectiveCreate(context.Background(), localapi.ObjectiveCreateParams{
				Workspace: "personal", Project: project.Project.ID, Title: "Recover supervisor-origin launch",
				Budget: domain.Budget{TokenLimit: 1000, CostCents: 100, TimeSeconds: 600}, IdempotencyKey: "supervisor-recovery-objective",
			})
			if err != nil {
				t.Fatalf("ObjectiveCreate() = %v", err)
			}
			planning, err := client.TaskCreate(context.Background(), localapi.TaskCreateParams{
				Workspace: "personal", Project: project.Project.ID, Objective: objective.Objective.ID, Title: "Plan recovered work", Priority: 100,
				Budget: domain.Budget{TokenLimit: 100, CostCents: 10, TimeSeconds: 60}, IdempotencyKey: "supervisor-recovery-planning-task",
			})
			if err != nil {
				t.Fatalf("TaskCreate(planning) = %v", err)
			}
			planning, err = client.TaskAssign(context.Background(), localapi.TaskAssignParams{
				Workspace: "personal", Task: planning.Detail.Task.ID, Agent: manager.Agent.ID, LeaseSeconds: 900,
				ExpectedRevision: planning.Detail.Task.Revision, IdempotencyKey: "supervisor-recovery-planning-assign",
			})
			if err != nil {
				t.Fatalf("TaskAssign(planning) = %v", err)
			}
			targetScenario := domain.FakeScenario{
				Schema: execution.FakeScenarioSchema, Name: "supervisor-recovery-target",
				Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "recovered exactly once", Handoff: "inspect recovered launch"}},
			}
			targetProfile, err := client.LaunchProfileCreate(context.Background(), localapi.LaunchProfileCreateParams{
				Workspace: "personal", Project: project.Project.ID, Agent: target.Agent.ID, ExpectedAgentRevision: target.Agent.Revision,
				Purpose: "recovery target metadata", Runtime: "fake", Provider: "fake", Scenario: targetScenario,
				AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900, IdempotencyKey: "supervisor-recovery-target-profile",
			})
			if err != nil {
				t.Fatalf("LaunchProfileCreate(target) = %v", err)
			}
			grant, err := client.ManagerGrantCreate(context.Background(), localapi.ManagerGrantCreateParams{
				Workspace: "personal", Project: project.Project.ID, Objective: objective.Objective.ID, Task: planning.Detail.Task.ID,
				Agent: manager.Agent.ID, ExpectedTaskRevision: planning.Detail.Task.Revision, ExpectedAgentRevision: manager.Agent.Revision,
				ProposalKinds: []string{domain.ManagerProposalTaskDecomposition}, LaunchProfileIDs: []string{targetProfile.Profile.ID},
				AllowedClaimKinds: []string{}, Limits: domain.ManagerProposalLimits{
					MaxOpenProposals: 2, MaxActions: 4, MaxTasks: 2, MaxDependencies: 2, MaxClaimRequirements: 1,
					Budget: domain.Budget{TokenLimit: 500, CostCents: 50, TimeSeconds: 300},
				}, IdempotencyKey: "supervisor-recovery-grant",
			})
			if err != nil {
				t.Fatalf("ManagerGrantCreate() = %v", err)
			}
			managerScenario := domain.FakeScenario{
				Schema: execution.FakeScenarioSchema, Name: "supervisor-recovery-manager",
				Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "proposal recorded", Handoff: "owner accepted exact proposal"}},
			}
			managerProfile, err := client.LaunchProfileCreate(context.Background(), localapi.LaunchProfileCreateParams{
				Workspace: "personal", Project: project.Project.ID, Agent: manager.Agent.ID, ExpectedAgentRevision: manager.Agent.Revision,
				Purpose: "recovery planning metadata", Runtime: "fake", Provider: "fake", Scenario: managerScenario,
				AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900, ManagerGrant: grant.Grant.ID,
				IdempotencyKey: "supervisor-recovery-manager-profile",
			})
			if err != nil {
				t.Fatalf("LaunchProfileCreate(manager) = %v", err)
			}
			if _, err := client.Stop(context.Background()); err != nil {
				t.Fatalf("Stop(first) = %v", err)
			}
			if err := first.wait(); err != nil {
				t.Fatalf("Run(first) = %v", err)
			}

			nodeID, err := execution.LoadOrCreateNodeID(config.DataDir)
			if err != nil {
				t.Fatalf("LoadOrCreateNodeID(recovery setup) = %v", err)
			}
			nodeKey, err := execution.LoadOrCreateNodeKey(config.DataDir)
			if err != nil {
				t.Fatalf("LoadOrCreateNodeKey(recovery setup) = %v", err)
			}
			nodeFingerprint, err := execution.NodeFingerprint(nodeID, nodeKey)
			if err != nil {
				t.Fatalf("NodeFingerprint(recovery setup) = %v", err)
			}
			storage, err := store.Open(context.Background(), config.DataDir, store.Options{
				RuntimeNodeID:          nodeID,
				RuntimeNodeFingerprint: nodeFingerprint,
			})
			if err != nil {
				t.Fatalf("store.Open(recovery setup) = %v", err)
			}
			invoked, err := storage.InvokeManager(context.Background(), store.InvokeManagerCommand{
				WorkspaceIdentifier: "personal", ObjectiveID: objective.Objective.ID, TaskID: planning.Detail.Task.ID,
				ManagerGrantID: grant.Grant.ID, LaunchProfileID: managerProfile.Profile.ID,
				ExpectedTaskRevision: planning.Detail.Task.Revision, ExpectedGrantRevision: grant.Grant.Revision,
				ExpectedProfileRevision: managerProfile.Profile.Revision,
				IdempotencyKey:          "supervisor-recovery-invoke", CorrelationID: "request-supervisor-recovery-invoke",
			})
			if err != nil {
				_ = storage.Close()
				t.Fatalf("InvokeManager() = %v", err)
			}
			if _, err := storage.MarkRunStarting(context.Background(), invoked.Detail.Run.ID, "request-supervisor-recovery-manager-starting"); err != nil {
				_ = storage.Close()
				t.Fatalf("MarkRunStarting(manager) = %v", err)
			}
			packet, err := storage.ContextPacket(context.Background(), "personal", invoked.Detail.Run.ContextPacketID)
			if err != nil {
				_ = storage.Close()
				t.Fatalf("ContextPacket(manager) = %v", err)
			}
			submitted, err := storage.SubmitManagerProposal(context.Background(), store.SubmitManagerProposalCommand{
				RunID: invoked.Detail.Run.ID, ManagerGrantID: grant.Grant.ID, ExpectedGrantRevision: grant.Grant.Revision,
				Kind: domain.ManagerProposalTaskDecomposition, Summary: "Create one exact recovery task.", AsOfEventSequence: packet.AsOfEventSequence,
				Actions: []domain.ManagerProposalAction{{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
					TaskKey: "recover", LaunchProfileID: targetProfile.Profile.ID, Title: "Launch after supervisor restart", Priority: 100,
					Budget: domain.Budget{TokenLimit: 100, CostCents: 10, TimeSeconds: 60},
				}}}, IdempotencyKey: "supervisor-recovery-submit", CorrelationID: "request-supervisor-recovery-submit",
			})
			if err != nil {
				_ = storage.Close()
				t.Fatalf("SubmitManagerProposal() = %v", err)
			}
			accepted, err := storage.AcceptManagerProposal(context.Background(), store.AcceptManagerProposalCommand{
				WorkspaceIdentifier: "personal", ManagerProposalID: submitted.Proposal.ID, ExpectedRevision: submitted.Proposal.Revision,
				DecisionNote: "Accept exact recovery task.", IdempotencyKey: "supervisor-recovery-accept", CorrelationID: "request-supervisor-recovery-accept",
			})
			if err != nil || len(accepted.Effects) == 0 {
				_ = storage.Close()
				t.Fatalf("AcceptManagerProposal() = %#v, %v", accepted, err)
			}
			if _, err := storage.MarkRunStarted(context.Background(), invoked.Detail.Run.ID, "manager-runtime", "manager-provider", "request-supervisor-recovery-manager-started"); err != nil {
				_ = storage.Close()
				t.Fatalf("MarkRunStarted(manager) = %v", err)
			}
			managerLogs, err := storage.PrepareRunLogArchive(context.Background(), invoked.Detail.Run.ID, domain.RunLogs{
				RunID: invoked.Detail.Run.ID, State: execution.RuntimeStateExited,
			})
			if err != nil {
				_ = storage.Close()
				t.Fatalf("PrepareRunLogArchive(manager) = %v", err)
			}
			if _, err := storage.ApplyRunObservation(context.Background(), invoked.Detail.Run.ID, domain.RunObservation{
				Kind: domain.ObservationCompletion, Message: "proposal recorded", Handoff: "owner accepted exact proposal", LogArchive: &managerLogs,
			}, true, nil, "request-supervisor-recovery-manager-completed"); err != nil {
				_ = storage.Close()
				t.Fatalf("ApplyRunObservation(manager completion) = %v", err)
			}
			if _, err := storage.ConfigureSupervisorPolicy(context.Background(), store.ConfigureSupervisorPolicyCommand{
				WorkspaceIdentifier: "personal", Enabled: true, AutoSchedule: true,
				Limits:           domain.SupervisorLimits{MaxActiveRuns: 4, MaxStartingRuns: 2, DefaultProjectConcurrency: 2, DefaultProviderConcurrency: 2},
				ExpectedRevision: 1, IdempotencyKey: "supervisor-recovery-policy", CorrelationID: "request-supervisor-recovery-policy",
			}); err != nil {
				_ = storage.Close()
				t.Fatalf("ConfigureSupervisorPolicy() = %v", err)
			}
			scan, err := storage.RunSupervisor(context.Background(), store.RunSupervisorCommand{
				WorkspaceIdentifier: "personal", Limit: 100, IdempotencyKey: "supervisor-recovery-scan", CorrelationID: "request-supervisor-recovery-scan",
			})
			if err != nil || len(scan.ScheduledRunIDs) != 1 {
				_ = storage.Close()
				t.Fatalf("RunSupervisor() = %#v, %v", scan, err)
			}
			scheduledRunID := scan.ScheduledRunIDs[0]
			requested, err := storage.RunDetail(context.Background(), "personal", scheduledRunID)
			if err != nil || requested.Run.Status != domain.RunRequested {
				_ = storage.Close()
				t.Fatalf("supervisor-origin run before worker = %#v, %v", requested, err)
			}
			if len(scan.Actions) != 1 || scan.Actions[0].RunID != scheduledRunID {
				_ = storage.Close()
				t.Fatalf("supervisor action/run linkage before worker = %#v", scan)
			}
			if testCase.ownerBlockStage == domain.RunRequested {
				_, blockErr := storage.TransitionTask(context.Background(), store.TransitionTaskCommand{
					WorkspaceIdentifier: "personal", TaskID: requested.Task.ID, ExpectedRevision: requested.Task.Revision,
					Action: "block", Reason: "owner attempted to block reserved task before external launch",
					IdempotencyKey: "supervisor-recovery-owner-block", CorrelationID: "request-supervisor-recovery-owner-block",
				})
				unchanged, detailErr := storage.RunDetail(context.Background(), "personal", scheduledRunID)
				if store.ErrorCode(blockErr) != store.CodeRunConflict || detailErr != nil || unchanged.Run.Status != domain.RunRequested || unchanged.Task.Status != domain.TaskAssigned || runtimeDriver.LaunchCount() != 0 {
					_ = storage.Close()
					t.Fatalf("TransitionTask(block requested reservation) error=%v; run=%#v, detail error=%v, launch count=%d",
						blockErr, unchanged, detailErr, runtimeDriver.LaunchCount())
				}
			}
			if err := storage.Close(); err != nil {
				t.Fatalf("close recovery setup store = %v", err)
			}

			config.DisableRunWorker = false
			if testCase.ownerBlockStage == domain.RunStarting {
				var barrierReached atomic.Bool
				startingReached := make(chan struct{})
				releaseLaunch := make(chan struct{})
				config.RunWorkerHook = func(stage string, _ domain.Run) error {
					if stage == "after_run_starting" && barrierReached.CompareAndSwap(false, true) {
						close(startingReached)
						<-releaseLaunch
					}
					return nil
				}
				gatedWorker := startTestServer(t, config)
				gatedClient := localapi.NewClient(config.SocketPath)
				select {
				case <-startingReached:
				case <-time.After(3 * time.Second):
					close(releaseLaunch)
					t.Fatal("worker did not reach the starting-to-launch gate")
				}
				_, blockErr := gatedClient.TaskTransition(context.Background(), localapi.TaskTransitionParams{
					Workspace: "personal", Task: requested.Task.ID, ExpectedRevision: requested.Task.Revision,
					Action: "block", Reason: "owner attempted to block during external launch handoff",
					IdempotencyKey: "supervisor-recovery-owner-block-starting",
				})
				unchanged, detailErr := gatedClient.TaskShow(context.Background(), "personal", requested.Task.ID)
				launchCountAtDecision := runtimeDriver.LaunchCount()
				close(releaseLaunch)
				if localAPIErrorCode(blockErr) != store.CodeRunConflict || detailErr != nil || unchanged.Detail.Task.Status != domain.TaskAssigned || launchCountAtDecision != 0 {
					t.Fatalf("TaskTransition(block at starting gate) error=%v; task=%#v, detail error=%v, launch count before release=%d",
						blockErr, unchanged, detailErr, launchCountAtDecision)
				}
				completed := waitForRunStatus(t, gatedClient, scheduledRunID, domain.RunCompleted)
				if completed.Detail.Run.StepCursor != 1 || runtimeDriver.LaunchCount() != 1 {
					t.Fatalf("run after rejected starting block = %#v; launch count=%d", completed.Detail, runtimeDriver.LaunchCount())
				}
				if _, err := gatedClient.Stop(context.Background()); err != nil {
					t.Fatalf("Stop(gated worker) = %v", err)
				}
				if err := gatedWorker.wait(); err != nil {
					t.Fatalf("Run(gated worker) = %v", err)
				}
				return
			}
			if testCase.crashStage != "" {
				var barrierReached atomic.Bool
				releaseCrash := make(chan struct{})
				config.RunWorkerHook = func(stage string, _ domain.Run) error {
					if stage == testCase.crashStage && barrierReached.CompareAndSwap(false, true) {
						// Let the server publish readiness before stopping it. Without this
						// gate the deliberately fast fake worker can win the test harness's
						// readiness probe, making the crash assertion timing-dependent.
						<-releaseCrash
						return errors.New("injected supervisor-origin prelaunch crash")
					}
					return nil
				}
				crashedWorker := startTestServer(t, config)
				close(releaseCrash)
				if err := crashedWorker.wait(); err != nil {
					t.Fatalf("Run(crashed worker at %s) = %v", testCase.crashStage, err)
				}
				if !barrierReached.Load() {
					t.Fatalf("worker did not reach crash stage %s", testCase.crashStage)
				}
				crashedStore, err := store.Open(context.Background(), config.DataDir, store.Options{})
				if err != nil {
					t.Fatalf("store.Open(after %s crash) = %v", testCase.crashStage, err)
				}
				crashed, err := crashedStore.RunDetail(context.Background(), "personal", scheduledRunID)
				if err != nil {
					_ = crashedStore.Close()
					t.Fatalf("RunDetail(after %s crash) = %v", testCase.crashStage, err)
				}
				if closeErr := crashedStore.Close(); closeErr != nil {
					t.Fatalf("close store after %s crash = %v", testCase.crashStage, closeErr)
				}
				hasHandle := crashed.Run.RuntimeHandle != ""
				wantHandle := testCase.crashStage == "after_runtime_launch"
				wantLaunchCount := 0
				if wantHandle {
					wantLaunchCount = 1
				}
				if crashed.Run.Status != domain.RunStarting || hasHandle != wantHandle || runtimeDriver.LaunchCount() != wantLaunchCount {
					t.Fatalf("supervisor-origin crash state at %s = %#v; launch count=%d, want starting handle=%t count=%d",
						testCase.crashStage, crashed.Run, runtimeDriver.LaunchCount(), wantHandle, wantLaunchCount)
				}
				config.RunWorkerHook = nil
			}
			second := startTestServer(t, config)
			restarted := localapi.NewClient(config.SocketPath)
			completed := waitForRunStatus(t, restarted, scheduledRunID, domain.RunCompleted)
			if completed.Detail.Run.ID != scheduledRunID || completed.Detail.Run.StepCursor != 1 || completed.Detail.Handoff == nil || runtimeDriver.LaunchCount() != 1 {
				t.Fatalf("supervisor-origin run after restart = %#v; launch count=%d", completed.Detail, runtimeDriver.LaunchCount())
			}
			action, err := restarted.SupervisorActionShow(context.Background(), "personal", scan.Actions[0].ID)
			if err != nil || action.Action.RunID != scheduledRunID || action.Action.ID != scan.Actions[0].ID {
				t.Fatalf("SupervisorActionShow(after restart) = %#v, %v", action, err)
			}
			if _, err := restarted.Stop(context.Background()); err != nil {
				t.Fatalf("Stop(second) = %v", err)
			}
			if err := second.wait(); err != nil {
				t.Fatalf("Run(second) = %v", err)
			}
		})
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
		NodeID:               daemonTestNodeID,
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
		if completed.Detail.Run.RuntimeHandle != "" || completed.Detail.Run.ProviderHandle != "" || completed.Detail.Handoff == nil {
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
		assertM19RunWorkerFailureProvenance(t, config.DataDir, failed.Detail)
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

func assertM19RunWorkerFailureProvenance(t *testing.T, dataDir string, detail domain.RunDetail) {
	t.Helper()
	database, err := sql.Open("sqlite3", filepath.Join(dataDir, "crewfold.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var runUpdatedBy, taskUpdatedBy, assignmentStatus, assignmentUpdatedBy string
	if err := database.QueryRow(`SELECT updated_by FROM runs WHERE id = ?`, detail.Run.ID).Scan(&runUpdatedBy); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT updated_by FROM tasks WHERE id = ?`, detail.Task.ID).Scan(&taskUpdatedBy); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT status, updated_by FROM task_assignments WHERE id = ?`, detail.Run.AssignmentID).Scan(&assignmentStatus, &assignmentUpdatedBy); err != nil {
		t.Fatal(err)
	}
	if runUpdatedBy != "subsystem:run-worker" || taskUpdatedBy != "subsystem:run-worker" || assignmentStatus != "released" || assignmentUpdatedBy != "subsystem:run-worker" {
		t.Fatalf("real worker failure projections = run:%q task:%q assignment:%q/%q, want subsystem:run-worker and released assignment",
			runUpdatedBy, taskUpdatedBy, assignmentStatus, assignmentUpdatedBy)
	}
	wantEvents := map[string]int{
		"run.starting": 1,
		"run.started":  1,
		"task.started": 1,
		"run.failed":   1,
		"task.failed":  1,
	}
	rows, err := database.Query(`
SELECT type, actor_id, actor_type
FROM events
WHERE (entity_id = ? AND type IN ('run.starting','run.started','run.failed'))
   OR (entity_id = ? AND type IN ('task.started','task.failed'))
ORDER BY sequence`, detail.Run.ID, detail.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[string]int)
	for rows.Next() {
		var eventType, actorID, actorType string
		if err := rows.Scan(&eventType, &actorID, &actorType); err != nil {
			t.Fatal(err)
		}
		if actorID != "subsystem:run-worker" || actorType != domain.EventActorSubsystem {
			t.Fatalf("real worker event %q actor = %q/%q, want subsystem:run-worker/subsystem", eventType, actorID, actorType)
		}
		got[eventType]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for eventType, count := range wantEvents {
		if got[eventType] != count {
			t.Errorf("real worker event %q count = %d, want %d (all=%#v)", eventType, got[eventType], count, got)
		}
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
			NodeID:               daemonTestNodeID,
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
	completed := waitForRunStatusWithin(t, restarted, started.Detail.Run.ID, domain.RunCompleted, 15*time.Second)
	if completed.Detail.Run.RuntimeHandle != before.Detail.Run.RuntimeHandle || completed.Detail.Handoff == nil || completed.Detail.Run.StepCursor != 2 {
		t.Fatalf("completed after direct restart = %#v", completed.Detail)
	}
	if _, err := restarted.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}

	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"direct": newDirectRuntime()}
	third := startTestServer(t, config)
	restartedAfterTerminal := localapi.NewClient(config.SocketPath)
	logs, err := restartedAfterTerminal.RunLogs(context.Background(), "personal", started.Detail.Run.ID, 0)
	if err != nil || logs.Logs.State != execution.RuntimeStateExited {
		t.Fatalf("RunLogs(after terminal restart) = %#v, %v", logs, err)
	}
	bundlePath := filepath.Join(t.TempDir(), "direct-terminal-bundle")
	if _, err := restartedAfterTerminal.BackupCreate(context.Background(), localapi.BackupCreateParams{TargetPath: bundlePath, IdempotencyKey: "direct-terminal-bundle"}); err != nil {
		t.Fatalf("BackupCreate(direct terminal archive) error = %v", err)
	}
	if _, err := restartedAfterTerminal.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(third) error = %v", err)
	}
	if err := third.wait(); err != nil {
		t.Fatalf("Run(third) error = %v", err)
	}
	restoredDataDir := filepath.Join(t.TempDir(), "restored-direct-terminal")
	if _, err := recovery.RestorePending(context.Background(), bundlePath, restoredDataDir); err != nil {
		t.Fatalf("RestorePending(direct terminal archive) error = %v", err)
	}
	if _, err := recovery.Activate(context.Background(), restoredDataDir, true); err != nil {
		t.Fatalf("Activate(direct terminal archive) error = %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(restoredDataDir, "runtime")); err != nil || len(entries) != 0 {
		t.Fatalf("restored backup retained source runtime state: entries=%v error=%v", entries, err)
	}
	restoredConfig := config
	restoredConfig.DataDir = restoredDataDir
	restoredConfig.SocketPath = filepath.Join(t.TempDir(), "restored-direct.sock")
	restoredConfig.RuntimeDrivers = map[string]execution.RuntimeDriver{"direct": execution.NewDirectRuntime(execution.DirectRuntimeOptions{
		NodeID: daemonTestNodeID, StateRoot: filepath.Join(restoredDataDir, "runtime"), SupervisorExecutable: executable,
		SupervisorArguments: []string{"-test.run=^TestDirectProcessHelper$", "--", "crewfold-direct-supervisor-helper"},
	})}
	restored := startTestServer(t, restoredConfig)
	restoredClient := localapi.NewClient(restoredConfig.SocketPath)
	restoredLogs, err := restoredClient.RunLogs(context.Background(), "personal", started.Detail.Run.ID, 0)
	if err != nil || !reflect.DeepEqual(restoredLogs, logs) {
		t.Fatalf("RunLogs(after direct backup activation) = %#v, %v; want exact %#v", restoredLogs, err, logs)
	}
	if _, err := restoredClient.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(restored direct daemon) error = %v", err)
	}
	if err := restored.wait(); err != nil {
		t.Fatalf("Run(restored direct daemon) error = %v", err)
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
	return waitForRunStatusWithin(t, client, runID, status, 5*time.Second)
}

func waitForRunStatusWithin(t *testing.T, client *localapi.Client, runID, status string, timeout time.Duration) localapi.RunShowResult {
	t.Helper()
	var result localapi.RunShowResult
	var lastErr error
	deadline := time.Now().Add(timeout)
	for {
		current, err := client.RunShow(context.Background(), "personal", runID)
		if err == nil {
			result = current
			if current.Detail.Run.Status == status {
				return result
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for run %s status %s; last detail=%#v last error=%v", runID, status, result.Detail, lastErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
