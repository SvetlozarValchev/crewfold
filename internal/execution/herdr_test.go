package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/herdr"
)

type fixtureHerdrRunner struct {
	mu                 sync.Mutex
	root               string
	surface            bool
	closed             bool
	paneID             string
	workspace          string
	tab                string
	terminal           string
	calls              []string
	incompatibleSchema bool
}

func newFixtureHerdrRunner(root string) *fixtureHerdrRunner {
	return &fixtureHerdrRunner{root: root, paneID: "w1:p1", workspace: "w1", tab: "w1:t1", terminal: "term-runtime"}
}

func (runner *fixtureHerdrRunner) Run(_ context.Context, _ string, arguments []string, _ map[string]string) (herdr.CommandResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	key := strings.Join(arguments, " ")
	runner.calls = append(runner.calls, key)
	if len(arguments) >= 2 && arguments[0] == "workspace" && arguments[1] == "create" {
		runner.surface, runner.closed = true, false
		return herdr.CommandResult{Stdout: runner.workspaceCreated()}, nil
	}
	switch key {
	case "--version":
		return herdr.CommandResult{Stdout: []byte("herdr 0.8.0\n")}, nil
	case "api schema --json":
		schema := "schema-compatible.json"
		if runner.incompatibleSchema {
			schema = "schema-incompatible.json"
		}
		data, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "protocol", "herdr", schema))
		return herdr.CommandResult{Stdout: data}, err
	case "api snapshot":
		return herdr.CommandResult{Stdout: runner.snapshot()}, nil
	case "pane process-info --pane " + runner.paneID:
		return herdr.CommandResult{Stdout: []byte(`{"id":"cli:request","result":{"type":"pane_process_info","process_info":{"pane_id":"` + runner.paneID + `","foreground_processes":[{"pid":101,"name":"fixture"}]}}}`)}, nil
	case "pane read " + runner.paneID + " --source recent-unwrapped --lines 25":
		return herdr.CommandResult{Stdout: []byte("fixture output\n")}, nil
	case "pane send-text " + runner.paneID + " wake now", "pane send-keys " + runner.paneID + " enter", "pane send-keys " + runner.paneID + " ctrl+c":
		return herdr.CommandResult{}, nil
	case "pane close " + runner.paneID:
		runner.closed, runner.surface = true, false
		return herdr.CommandResult{}, nil
	}
	if len(arguments) >= 4 && arguments[0] == "pane" && arguments[1] == "run" {
		state := herdrSupervisorState{Schema: herdrSupervisorStateSchema, OperationID: "run_fixture", Status: RuntimeStateRunning, SupervisorPID: 100, ChildPID: 101}
		if err := writeJSONAtomic(filepath.Join(runner.root, "state.json"), state); err != nil {
			return herdr.CommandResult{}, err
		}
		return herdr.CommandResult{}, nil
	}
	return herdr.CommandResult{Stderr: []byte(`{"error":{"code":"unexpected_command","message":"` + key + `"}}`), ExitCode: 1}, nil
}

func (runner *fixtureHerdrRunner) snapshot() []byte {
	if !runner.surface || runner.closed {
		return []byte(`{"id":"cli:api:snapshot","result":{"type":"session_snapshot","snapshot":{"version":"0.8.0","protocol":19,"workspaces":[],"tabs":[],"panes":[],"layouts":[],"agents":[]}}}`)
	}
	return []byte(`{"id":"cli:api:snapshot","result":{"type":"session_snapshot","snapshot":{"version":"0.8.0","protocol":19,"workspaces":[{"workspace_id":"` + runner.workspace + `","label":"crewfold-run_fixture"}],"tabs":[{"tab_id":"` + runner.tab + `","workspace_id":"` + runner.workspace + `"}],"panes":[{"pane_id":"` + runner.paneID + `","terminal_id":"` + runner.terminal + `","workspace_id":"` + runner.workspace + `","tab_id":"` + runner.tab + `"}],"layouts":[],"agents":[]}}}`)
}

func (runner *fixtureHerdrRunner) workspaceCreated() []byte {
	return []byte(`{"id":"cli:request","result":{"type":"workspace_created","workspace":{"workspace_id":"` + runner.workspace + `","label":"crewfold-run_fixture"},"tab":{"tab_id":"` + runner.tab + `","workspace_id":"` + runner.workspace + `"},"root_pane":{"pane_id":"` + runner.paneID + `","terminal_id":"` + runner.terminal + `","workspace_id":"` + runner.workspace + `","tab_id":"` + runner.tab + `"}}}`)
}

func TestHerdrRuntimeLaunchReconcileMovePromptAndClosedPane(t *testing.T) {
	checkout := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "herdr")
	runDirectory := filepath.Join(stateRoot, "run_fixture")
	runner := newFixtureHerdrRunner(runDirectory)
	runtime := NewHerdrRuntime(HerdrRuntimeOptions{
		StateRoot: stateRoot, HerdrExecutable: "/fixture/herdr", CrewfoldExecutable: os.Args[0],
		CommandRunner: runner, StartupTimeout: time.Second,
	})
	binding, err := runtime.Launch(context.Background(), "run_fixture", domain.RunPlacement{CheckoutPath: checkout}, LaunchSpec{Command: &CommandSpec{Executable: "/bin/sh", Arguments: []string{"-c", "exit 0"}}})
	if err != nil || !strings.HasPrefix(binding.RuntimeHandle, herdrHandlePrefix) {
		t.Fatalf("Launch() = %#v, %v", binding, err)
	}
	snapshot, err := runtime.Inspect(context.Background(), "run_fixture", binding.RuntimeHandle)
	if err != nil || snapshot.State != RuntimeStateRunning || snapshot.CompletionReady {
		t.Fatalf("Inspect(running) = %#v, %v", snapshot, err)
	}

	runner.mu.Lock()
	runner.workspace, runner.tab, runner.paneID = "w2", "w2:t3", "w2:p7"
	runner.mu.Unlock()
	restarted := NewHerdrRuntime(HerdrRuntimeOptions{
		StateRoot: stateRoot, HerdrExecutable: "/fixture/herdr", CrewfoldExecutable: os.Args[0], CommandRunner: runner,
	})
	reconciled, err := restarted.Reconcile(context.Background(), "run_fixture", binding.RuntimeHandle)
	if err != nil {
		t.Fatalf("Reconcile(moved) error = %v", err)
	}
	movedHandle, err := decodeHerdrHandle("run_fixture", reconciled.RuntimeHandle)
	if err != nil || movedHandle.TerminalID != "term-runtime" || movedHandle.PaneID != "w2:p7" {
		t.Fatalf("moved handle = %#v, %v", movedHandle, err)
	}
	if err := restarted.Prompt(context.Background(), "run_fixture", binding.RuntimeHandle, "wake now"); err != nil {
		t.Fatalf("Prompt(after move) error = %v", err)
	}
	attachment, err := restarted.Attach(context.Background(), "run_fixture", binding.RuntimeHandle, true)
	if err != nil || strings.Join(attachment.Arguments, " ") != "terminal attach term-runtime --takeover" {
		t.Fatalf("Attach() = %#v, %v", attachment, err)
	}

	if err := writeJSONAtomic(filepath.Join(runDirectory, "state.json"), herdrSupervisorState{Schema: herdrSupervisorStateSchema, OperationID: "run_fixture", Status: RuntimeStateExited, ExitKnown: true}); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.closed, runner.surface = true, false
	runner.mu.Unlock()
	closedSnapshot, closedErr := restarted.Inspect(context.Background(), "run_fixture", binding.RuntimeHandle)
	if closedErr == nil || closedSnapshot.CompletionReady {
		t.Fatalf("Inspect(closed pane) = %#v, %v; closed pane must not complete", closedSnapshot, closedErr)
	}
}

func TestHerdrRuntimeRejectsIncompatibleSchemaBeforeCreatingSurface(t *testing.T) {
	checkout := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "herdr")
	runner := newFixtureHerdrRunner(filepath.Join(stateRoot, "run_fixture"))
	runner.incompatibleSchema = true
	runtime := NewHerdrRuntime(HerdrRuntimeOptions{StateRoot: stateRoot, HerdrExecutable: "herdr", CrewfoldExecutable: os.Args[0], CommandRunner: runner})
	_, err := runtime.Launch(context.Background(), "run_fixture", domain.RunPlacement{CheckoutPath: checkout}, LaunchSpec{Command: &CommandSpec{Executable: "/bin/sh", Arguments: []string{"-c", "exit 0"}}})
	var startError *StartError
	if !errors.As(err, &startError) || !strings.Contains(err.Error(), "install a compatible Herdr release") {
		t.Fatalf("Launch(incompatible schema) error = %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.surface {
		t.Fatal("incompatible launch created a Herdr surface")
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "workspace create") {
			t.Fatalf("incompatible launch called %q", call)
		}
	}
}

func TestHerdrRuntimeOnlyCompletesAfterProviderExitWhilePaneExists(t *testing.T) {
	stateRoot := t.TempDir()
	runDirectory := filepath.Join(stateRoot, "run_fixture")
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := newFixtureHerdrRunner(runDirectory)
	runner.surface = true
	handle := herdrRuntimeHandle{Schema: herdrHandleSchema, OperationID: "run_fixture", WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1", TerminalID: "term-runtime"}
	rawHandle, _ := encodeHerdrHandle(handle)
	if err := writeJSONAtomic(filepath.Join(runDirectory, "state.json"), herdrSupervisorState{Schema: herdrSupervisorStateSchema, OperationID: "run_fixture", Status: RuntimeStateExited, ExitKnown: true, ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	runtime := NewHerdrRuntime(HerdrRuntimeOptions{StateRoot: stateRoot, HerdrExecutable: "herdr", CrewfoldExecutable: os.Args[0], CommandRunner: runner})
	snapshot, err := runtime.Inspect(context.Background(), "run_fixture", rawHandle)
	if err != nil || !snapshot.CompletionReady || snapshot.State != RuntimeStateExited {
		t.Fatalf("Inspect(exited) = %#v, %v", snapshot, err)
	}
	logs, err := runtime.Logs(context.Background(), "run_fixture", rawHandle, 25)
	if err != nil || logs.Stdout.Text != "fixture output\n" {
		t.Fatalf("Logs() = %#v, %v", logs, err)
	}
}

func TestHerdrRuntimeStopClosesOnlyItsStableTerminalAfterGraceExpires(t *testing.T) {
	stateRoot := t.TempDir()
	runDirectory := filepath.Join(stateRoot, "run_fixture")
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := newFixtureHerdrRunner(runDirectory)
	runner.surface = true
	handle := herdrRuntimeHandle{Schema: herdrHandleSchema, OperationID: "run_fixture", WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1", TerminalID: "term-runtime"}
	rawHandle, _ := encodeHerdrHandle(handle)
	if err := writeJSONAtomic(filepath.Join(runDirectory, "state.json"), herdrSupervisorState{Schema: herdrSupervisorStateSchema, OperationID: "run_fixture", Status: RuntimeStateRunning}); err != nil {
		t.Fatal(err)
	}
	runtime := NewHerdrRuntime(HerdrRuntimeOptions{StateRoot: stateRoot, HerdrExecutable: "herdr", CrewfoldExecutable: os.Args[0], CommandRunner: runner, PollInterval: time.Millisecond})
	result, err := runtime.Stop(context.Background(), "run_fixture", rawHandle, StopSpec{GracePeriod: time.Millisecond})
	if err != nil || !result.Forced || !strings.Contains(result.Diagnostic, "closed") {
		t.Fatalf("Stop() = %#v, %v", result, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.surface || !runner.closed {
		t.Fatalf("fixture surface after stop = surface:%t closed:%t", runner.surface, runner.closed)
	}
}

func TestHerdrRuntimeClassifiesServerRestartAsRetryableUnavailable(t *testing.T) {
	stateRoot := t.TempDir()
	runDirectory := filepath.Join(stateRoot, "run_fixture")
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	handle := herdrRuntimeHandle{Schema: herdrHandleSchema, OperationID: "run_fixture", WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1", TerminalID: "term-runtime"}
	rawHandle, _ := encodeHerdrHandle(handle)
	if err := writeJSONAtomic(filepath.Join(runDirectory, "state.json"), herdrSupervisorState{Schema: herdrSupervisorStateSchema, OperationID: "run_fixture", Status: RuntimeStateRunning}); err != nil {
		t.Fatal(err)
	}
	runner := &singleHerdrResultRunner{result: herdr.CommandResult{ExitCode: 1, Stderr: []byte(`{"error":{"code":"server_not_running","message":"session is restarting"}}`)}}
	runtime := NewHerdrRuntime(HerdrRuntimeOptions{StateRoot: stateRoot, HerdrExecutable: "herdr", CrewfoldExecutable: os.Args[0], CommandRunner: runner})
	_, err := runtime.Inspect(context.Background(), "run_fixture", rawHandle)
	var unavailable *RuntimeUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Inspect(server restart) error = %v", err)
	}
}

type singleHerdrResultRunner struct {
	result herdr.CommandResult
}

func (runner *singleHerdrResultRunner) Run(context.Context, string, []string, map[string]string) (herdr.CommandResult, error) {
	return runner.result, nil
}

func TestHerdrPaneSupervisorPersistsExitWithoutClaimingProviderCompletion(t *testing.T) {
	runDirectory := t.TempDir()
	spec := herdrSupervisorSpec{
		Schema: herdrSupervisorSpecSchema, OperationID: "run_supervisor", Executable: "/bin/sh",
		Arguments: []string{"-c", "exit 7"}, Environment: []string{"PATH=/usr/bin:/bin"}, WorkingDirectory: runDirectory,
	}
	if err := writeJSONAtomic(filepath.Join(runDirectory, "launch.json"), spec); err != nil {
		t.Fatal(err)
	}
	if code := RunHerdrPaneSupervisor([]string{filepath.Join(runDirectory, "launch.json")}); code != 7 {
		t.Fatalf("RunHerdrPaneSupervisor() code = %d", code)
	}
	state, err := readHerdrSupervisorState(runDirectory)
	if err != nil || state.Status != RuntimeStateExited || !state.ExitKnown || state.ExitCode != 7 {
		t.Fatalf("supervisor state = %#v, %v", state, err)
	}
}

func TestHerdrRuntimeLaunchSpecKeepsSettledTerminalAttachable(t *testing.T) {
	checkout := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "herdr")
	runDirectory := filepath.Join(stateRoot, "run_fixture")
	runner := newFixtureHerdrRunner(runDirectory)
	runtime := NewHerdrRuntime(HerdrRuntimeOptions{StateRoot: stateRoot, HerdrExecutable: "herdr", CrewfoldExecutable: os.Args[0], CommandRunner: runner})
	if _, err := runtime.Launch(context.Background(), "run_fixture", domain.RunPlacement{CheckoutPath: checkout}, LaunchSpec{Command: &CommandSpec{Executable: "/bin/sh", Arguments: []string{"-c", "exit 0"}}}); err != nil {
		t.Fatal(err)
	}
	spec, err := readHerdrSupervisorSpec(filepath.Join(runDirectory, "launch.json"))
	if err != nil || !spec.HoldAfterExit {
		t.Fatalf("launch spec = %#v, %v", spec, err)
	}
}

func TestHerdrHandleRejectsCrossRunReuse(t *testing.T) {
	raw, err := encodeHerdrHandle(herdrRuntimeHandle{Schema: herdrHandleSchema, OperationID: "run_one", WorkspaceID: "w1", TabID: "t1", PaneID: "p1", TerminalID: "term"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeHerdrHandle("run_two", raw); err == nil {
		t.Fatal("decodeHerdrHandle(cross-run) error = nil")
	}
}

func TestFixtureTerminalProviderUsesDistinctProviderIdentity(t *testing.T) {
	provider := NewFixtureTerminalProvider(nil)
	if provider.Name() != "fixture-terminal" {
		t.Fatalf("Name() = %q", provider.Name())
	}
	binding, err := provider.Bind(context.Background(), domain.Run{ID: "run_fixture"}, RuntimeBinding{RuntimeHandle: "handle"})
	if err != nil || binding.ProviderHandle != "fixture-terminal-provider:run_fixture" {
		t.Fatalf("Bind() = %#v, %v", binding, err)
	}
}

func TestHerdrFixtureResponsesAreStrictJSON(t *testing.T) {
	for _, data := range [][]byte{newFixtureHerdrRunner("/tmp").snapshot(), newFixtureHerdrRunner("/tmp").workspaceCreated()} {
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
	}
}
