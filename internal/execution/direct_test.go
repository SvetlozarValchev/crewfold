package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
)

var _ RuntimeStatusInspector = (*DirectRuntime)(nil)
var _ RuntimeLaunchPreparer = (*DirectRuntime)(nil)

const directTestNodeID = "11111111111111111111111111111111"

func newTestDirectRuntime(options DirectRuntimeOptions) *DirectRuntime {
	options.NodeID = directTestNodeID
	return NewDirectRuntime(options)
}

func TestDirectSupervisorHelper(t *testing.T) {
	for index, argument := range os.Args {
		if argument == "crewfold-direct-supervisor-test-helper" {
			os.Exit(RunDirectSupervisor(os.Args[index+1:]))
		}
	}
}

func TestDirectEnvironmentIsAllowlistedAndRejectsSecretOverrides(t *testing.T) {
	t.Parallel()

	environment, err := buildDirectEnvironment([]string{
		"PATH=/usr/bin", "LANG=C", "LC_MESSAGES=C", "HOME=/private/home", "API_TOKEN=must-not-pass",
	}, map[string]string{"LANG": "en_US.UTF-8"}, "run_test")
	if err != nil {
		t.Fatalf("buildDirectEnvironment() error = %v", err)
	}
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "LANG=en_US.UTF-8") || !strings.Contains(joined, "LC_MESSAGES=C") || !strings.Contains(joined, "CREWFOLD_RUN_ID=run_test") {
		t.Fatalf("allowlisted environment = %q", joined)
	}
	if strings.Contains(joined, "HOME=") || strings.Contains(joined, "API_TOKEN") || strings.Contains(joined, "must-not-pass") {
		t.Fatalf("environment leaked a non-allowlisted value: %q", joined)
	}
	managed, err := buildDirectEnvironment(nil, map[string]string{
		"HOME": "/home/owner", "NPM_CONFIG_CACHE": "/tmp/crewfold/npm", "XDG_CACHE_HOME": "/tmp/crewfold/xdg",
	}, "run_managed")
	if err != nil {
		t.Fatalf("buildDirectEnvironment(managed provider values) error = %v", err)
	}
	managedJoined := strings.Join(managed, "\n")
	for _, wanted := range []string{"HOME=/home/owner", "NPM_CONFIG_CACHE=/tmp/crewfold/npm", "XDG_CACHE_HOME=/tmp/crewfold/xdg"} {
		if !strings.Contains(managedJoined, wanted) {
			t.Fatalf("managed environment omits %q: %q", wanted, managedJoined)
		}
	}
	if _, err := buildDirectEnvironment(nil, map[string]string{"PASSWORD": "not-allowed"}, "run_test"); err == nil {
		t.Fatal("secret override error = nil, want allowlist rejection")
	}
}

func TestDirectCheckIdentityEnvironmentIsExplicitAndClosed(t *testing.T) {
	t.Parallel()

	environment, err := buildDirectEnvironment([]string{"PATH=/usr/bin"}, nil, "check_123", DirectCheckRunIDEnvironmentVariable)
	if err != nil {
		t.Fatalf("buildDirectEnvironment(check) error = %v", err)
	}
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, DirectCheckRunIDEnvironmentVariable+"=check_123") || strings.Contains(joined, DirectRunIDEnvironmentVariable+"=") {
		t.Fatalf("check environment = %q", joined)
	}
	if _, err := buildDirectEnvironment(nil, nil, "check_123", "CREWFOLD_ARBITRARY_ID"); err == nil {
		t.Fatal("buildDirectEnvironment(arbitrary identity name) error = nil")
	}
}

func TestDirectPrepareLaunchIsSideEffectFreeAndMatchesPersistedSpec(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	stateRoot := filepath.Join(t.TempDir(), "check-runtime")
	runtime := newTestDirectRuntime(DirectRuntimeOptions{
		StateRoot: stateRoot, SupervisorExecutable: executable,
		SupervisorArguments:            []string{"-test.run=^TestDirectSupervisorHelper$", "--", "crewfold-direct-supervisor-test-helper"},
		InheritedEnvironment:           []string{"PATH=/usr/bin", "LANG=C"},
		OperationIDEnvironmentVariable: DirectCheckRunIDEnvironmentVariable,
	})
	const operationID = "check_prepared_launch"
	placement := domain.RunPlacement{CheckoutPath: t.TempDir()}
	launch := LaunchSpec{Command: &CommandSpec{
		Executable: "/bin/true", Arguments: []string{"prepared"}, Timeout: time.Second, OutputByteLimit: 4096,
	}}
	prepared, err := runtime.PrepareLaunch(context.Background(), operationID, placement, launch)
	if err != nil || !directSpecSHA256Pattern.MatchString(prepared.SpecSHA256) {
		t.Fatalf("PrepareLaunch() = %#v, %v", prepared, err)
	}
	if _, err := os.Lstat(stateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PrepareLaunch() created runtime state: %v", err)
	}
	if _, err := runtime.Launch(context.Background(), operationID, placement, launch); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	stored, err := readDirectSpec(directSpecPath(filepath.Join(stateRoot, operationID)))
	if err != nil {
		t.Fatalf("readDirectSpec() error = %v", err)
	}
	if stored.SpecSHA256 != prepared.SpecSHA256 {
		t.Fatalf("persisted digest = %s, prepared = %s", stored.SpecSHA256, prepared.SpecSHA256)
	}
	waitForDirectCompletion(t, runtime, operationID, directHandle(directTestNodeID, operationID))
}

func TestDirectLaunchRejectsPostPreparationWorkingDirectorySymlinkSwap(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	checkout := t.TempDir()
	workingDirectory := filepath.Join(checkout, "sub")
	outside := t.TempDir()
	if err := os.Mkdir(workingDirectory, 0o700); err != nil {
		t.Fatalf("os.Mkdir(working directory) error = %v", err)
	}
	runtime := newTestDirectRuntime(DirectRuntimeOptions{
		StateRoot: filepath.Join(t.TempDir(), "check-runtime"), SupervisorExecutable: executable,
		SupervisorArguments:  []string{"-test.run=^TestDirectSupervisorHelper$", "--", "crewfold-direct-supervisor-test-helper"},
		InheritedEnvironment: []string{"PATH=/usr/bin"},
	})
	const operationID = "check_working_directory_swap"
	placement := domain.RunPlacement{CheckoutPath: workingDirectory}
	launch := LaunchSpec{Command: &CommandSpec{Executable: "/bin/sh", Arguments: []string{"-c", "printf escaped > effect.txt"}, Timeout: time.Second}}
	if _, err := runtime.PrepareLaunch(context.Background(), operationID, placement, launch); err != nil {
		t.Fatalf("PrepareLaunch() error = %v", err)
	}
	if err := os.Rename(workingDirectory, workingDirectory+"-old"); err != nil {
		t.Fatalf("os.Rename(working directory) error = %v", err)
	}
	if err := os.Symlink(outside, workingDirectory); err != nil {
		t.Fatalf("os.Symlink(outside working directory) error = %v", err)
	}
	_, err = runtime.Launch(context.Background(), operationID, placement, launch)
	var startError *StartError
	if !errors.As(err, &startError) {
		t.Fatalf("Launch(swapped working directory) error = %v, want StartError", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "effect.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("swapped outside directory received a launch effect: %v", err)
	}
}

func TestDirectSupervisorUsesPinnedWorkingDirectoryAfterPathSwap(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	checkout := t.TempDir()
	workingDirectory := filepath.Join(checkout, "sub")
	movedWorkingDirectory := workingDirectory + "-old"
	outside := t.TempDir()
	if err := os.Mkdir(workingDirectory, 0o700); err != nil {
		t.Fatalf("os.Mkdir(working directory) error = %v", err)
	}
	pinned, err := openDirectWorkingDirectory(workingDirectory)
	if err != nil {
		t.Fatalf("openDirectWorkingDirectory() error = %v", err)
	}
	defer pinned.Close()
	if err := os.Rename(workingDirectory, movedWorkingDirectory); err != nil {
		t.Fatalf("os.Rename(working directory) error = %v", err)
	}
	if err := os.Symlink(outside, workingDirectory); err != nil {
		t.Fatalf("os.Symlink(outside working directory) error = %v", err)
	}
	runDirectory := t.TempDir()
	spec := directSupervisorSpec{
		Schema: directSupervisorSpecSchema, NodeID: directTestNodeID, OperationID: "check_pinned_working_directory", Executable: "/bin/sh",
		Arguments: []string{"-c", "printf pinned > effect.txt"}, StandardInput: []byte{}, Environment: []string{"PATH=/usr/bin"}, WorkingDirectory: workingDirectory,
		OutputByteLimit: directDefaultOutputLimit, DefaultGraceMillis: directDefaultGracePeriod.Milliseconds(),
	}
	if err := writeDirectSpec(runDirectory, spec); err != nil {
		t.Fatalf("writeDirectSpec() error = %v", err)
	}
	command := exec.Command(executable, "-test.run=^TestDirectSupervisorHelper$", "--", "crewfold-direct-supervisor-test-helper", directSpecPath(runDirectory), strconv.Itoa(directSupervisorWorkingDirFD))
	command.ExtraFiles = []*os.File{pinned}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("direct supervisor error = %v, output = %q", err, output)
	}
	if _, err := os.Lstat(filepath.Join(outside, "effect.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("swapped outside directory received a launch effect: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(movedWorkingDirectory, "effect.txt"))
	if err != nil || string(contents) != "pinned" {
		t.Fatalf("pinned working directory effect = %q, %v", contents, err)
	}
}

func TestBoundedCaptureReportsOmittedBytes(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	capture := &boundedCapture{writer: buffer, limit: 5}
	if written, err := capture.Write([]byte("abcdefghij")); err != nil || written != 10 {
		t.Fatalf("Write(first) = %d, %v", written, err)
	}
	if written, err := capture.Write([]byte("zz")); err != nil || written != 2 {
		t.Fatalf("Write(second) = %d, %v", written, err)
	}
	if buffer.String() != "abcde" || capture.captured != 5 || capture.omitted != 7 {
		t.Fatalf("capture = text %q captured %d omitted %d", buffer.String(), capture.captured, capture.omitted)
	}
}

func TestDirectLogRedactionPreservesDiagnosisWithoutSecretValue(t *testing.T) {
	t.Parallel()

	redacted := redactDirectOutput("API_TOKEN=top-secret\nAuthorization: Bearer credential\npassword: hunter2\n{\"token\":\"json-secret\"}\n")
	for _, secret := range []string{"top-secret", "credential", "hunter2", "json-secret"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted output %q contains %q", redacted, secret)
		}
	}
	if strings.Count(redacted, "[REDACTED]") != 4 {
		t.Fatalf("redacted output = %q", redacted)
	}
}

func TestFixtureProviderRunsScenarioAsStructuredProcessReports(t *testing.T) {
	t.Parallel()

	scenario := domain.FakeScenario{
		Schema: FakeScenarioSchema,
		Name:   "direct-reports",
		Steps: []domain.FakeStep{
			{Kind: domain.ObservationProgress, Message: "working"},
			{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"},
		},
		Process: domain.FixtureProcess{StdoutNoiseBytes: 16, StderrNoiseBytes: 8},
	}
	input := &bytes.Buffer{}
	if err := jsonEncode(input, scenario); err != nil {
		t.Fatalf("encode scenario: %v", err)
	}
	output, diagnostics := &bytes.Buffer{}, &bytes.Buffer{}
	if code := RunFixtureProvider(input, output, diagnostics); code != 0 {
		t.Fatalf("RunFixtureProvider() code = %d, stderr = %q", code, diagnostics.String())
	}
	reports, err := decodeFixtureReports(output.String())
	if err != nil || len(reports) != 2 || reports[1].Kind != domain.ObservationCompletion {
		t.Fatalf("decodeFixtureReports() = %#v, %v", reports, err)
	}
	provider := FixtureProvider{Executable: "/fixture", Arguments: []string{"worker"}}
	spec, err := provider.Prepare(context.Background(), domain.Run{}, scenario)
	if err != nil || spec.Command == nil || spec.Command.Executable != "/fixture" || spec.Command.OutputByteLimit == 0 {
		t.Fatalf("Prepare() = %#v, %v", spec, err)
	}
	observation, found, err := provider.Next(context.Background(), domain.Run{StepCursor: 1}, scenario, RuntimeSnapshot{Stdout: domain.CapturedLog{Text: output.String()}})
	if err != nil || !found || observation.Kind != domain.ObservationCompletion || observation.Handoff != "review" {
		t.Fatalf("Next() = %#v, %t, %v", observation, found, err)
	}
}

func TestFixtureProviderMapsStartFailureBeforeLaunchingAProcess(t *testing.T) {
	t.Parallel()

	provider := NewFixtureProvider()
	_, err := provider.Prepare(context.Background(), domain.Run{}, domain.FakeScenario{
		Schema: FakeScenarioSchema, Name: "refused", StartFailure: "fixture refused",
	})
	var startError *StartError
	if !errors.As(err, &startError) || startError.Error() != "fixture refused" {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestFixtureTimeoutIsCarriedIntoDirectCommand(t *testing.T) {
	t.Parallel()

	provider := FixtureProvider{Executable: "/fixture"}
	spec, err := provider.Prepare(context.Background(), domain.Run{}, domain.FakeScenario{
		Schema: FakeScenarioSchema, Name: "timeout", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress}},
		Process: domain.FixtureProcess{TimeoutMillis: 250},
	})
	if err != nil || spec.Command.Timeout != 250*time.Millisecond {
		t.Fatalf("Prepare() timeout = %v, error = %v", spec.Command.Timeout, err)
	}
}

func TestDirectInspectionReportsUnknownWhenRecordedSupervisorDisappears(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runID := "run_missing_supervisor"
	runDirectory := filepath.Join(root, runID)
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	storedSpec := directSupervisorSpec{
		Schema: directSupervisorSpecSchema, NodeID: directTestNodeID, OperationID: runID, Executable: "/bin/true", Arguments: []string{}, StandardInput: []byte{}, Environment: []string{},
		WorkingDirectory: root, OutputByteLimit: directDefaultOutputLimit, DefaultGraceMillis: directDefaultGracePeriod.Milliseconds(),
	}
	if err := writeDirectSpec(runDirectory, storedSpec); err != nil {
		t.Fatalf("writeDirectSpec() error = %v", err)
	}
	storedSpec, err := readDirectSpec(directSpecPath(runDirectory))
	if err != nil {
		t.Fatalf("readDirectSpec() error = %v", err)
	}
	if err := writeDirectState(runDirectory, directSupervisorState{
		Schema:          directSupervisorStateSchema,
		NodeID:          directTestNodeID,
		OperationID:     runID,
		Status:          RuntimeStateRunning,
		SupervisorPID:   2147483647,
		SupervisorStart: "unreachable",
		SpecSHA256:      storedSpec.SpecSHA256,
	}); err != nil {
		t.Fatalf("writeDirectState() error = %v", err)
	}
	runtime := newTestDirectRuntime(DirectRuntimeOptions{StateRoot: root})
	_, stopErr := runtime.Stop(context.Background(), runID, directHandle(directTestNodeID, runID), StopSpec{GracePeriod: time.Millisecond})
	var unknownError *OutcomeUnknownError
	if !errors.As(stopErr, &unknownError) {
		t.Fatalf("Stop() error = %v, want OutcomeUnknownError", stopErr)
	}
	snapshot, err := runtime.Inspect(context.Background(), runID, directHandle(directTestNodeID, runID))
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if snapshot.State != RuntimeStateUnknown || snapshot.CompletionReady || !strings.Contains(snapshot.Diagnostic, "disappeared") {
		t.Fatalf("Inspect() snapshot = %#v", snapshot)
	}
}

func TestDirectStopRejectsMismatchedStateBeforeSignallingItsRecordedProcess(t *testing.T) {
	t.Parallel()

	process := exec.Command("/bin/sleep", "30")
	if err := process.Start(); err != nil {
		t.Fatalf("process.Start() error = %v", err)
	}
	defer func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	}()
	startIdentity := processStartIdentity(process.Process.Pid)
	if startIdentity == "" {
		t.Fatal("process start identity is empty")
	}

	root := t.TempDir()
	const operationID = "operation_stop_identity"
	runDirectory := filepath.Join(root, operationID)
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatalf("os.Mkdir(run directory) error = %v", err)
	}
	if err := writeDirectState(runDirectory, directSupervisorState{
		Schema: directSupervisorStateSchema, OperationID: "different_operation", Status: RuntimeStateRunning,
		SupervisorPID: process.Process.Pid, SupervisorStart: startIdentity, SpecSHA256: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatalf("writeDirectState() error = %v", err)
	}
	runtime := newTestDirectRuntime(DirectRuntimeOptions{StateRoot: root})
	if _, err := runtime.Stop(context.Background(), operationID, directHandle(directTestNodeID, operationID), StopSpec{}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("Stop(mismatched state) error = %v", err)
	}
	time.Sleep(2 * directPollInterval)
	if err := process.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("recorded process was signalled despite mismatched operation identity: %v", err)
	}
}

func TestDirectLaunchReplaysAcrossFreshRuntimeInstanceWithoutAnotherSupervisor(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	const operationID = "operation_fresh_restart"
	options := DirectRuntimeOptions{
		StateRoot:            t.TempDir(),
		SupervisorExecutable: executable,
		SupervisorArguments:  []string{"-test.run=^TestDirectSupervisorHelper$", "--", "crewfold-direct-supervisor-test-helper"},
		InheritedEnvironment: []string{"PATH=/usr/bin", "LANG=C"},
	}
	placement := domain.RunPlacement{CheckoutPath: t.TempDir()}
	launch := LaunchSpec{Command: &CommandSpec{Executable: "/bin/true", Arguments: []string{"fixture"}, Timeout: time.Second}}
	first, err := newTestDirectRuntime(options).Launch(context.Background(), operationID, placement, launch)
	if err != nil || first.RuntimeHandle != directHandle(directTestNodeID, operationID) {
		t.Fatalf("Launch(fresh) = %#v, %v", first, err)
	}

	restartedOptions := options
	restartedOptions.SupervisorExecutable = filepath.Join(t.TempDir(), "must-not-start-another-supervisor")
	second, err := newTestDirectRuntime(restartedOptions).Launch(context.Background(), operationID, placement, launch)
	if err != nil || second != first {
		t.Fatalf("Launch(restarted replay) = %#v, %v; first = %#v", second, err, first)
	}
	launch.Command.Arguments = []string{"changed"}
	if _, err := newTestDirectRuntime(restartedOptions).Launch(context.Background(), operationID, placement, launch); err == nil || !strings.Contains(err.Error(), "different launch specification") {
		t.Fatalf("Launch(restarted mismatch) error = %v", err)
	}
	waitForDirectCompletion(t, newTestDirectRuntime(options), operationID, first.RuntimeHandle)
}

func TestDirectLaunchReplayDoesNotRequireTheOriginalExecutableOrCheckoutToRemainAvailable(t *testing.T) {
	t.Parallel()

	const operationID = "operation_unavailable_restart"
	options, placement, launch := directReplayFixture(t, operationID, RuntimeStateExited)
	runDirectory := filepath.Join(options.StateRoot, operationID)
	executable := filepath.Join(t.TempDir(), "fixture-executable")
	binary, err := os.ReadFile("/bin/true")
	if err != nil {
		t.Fatalf("os.ReadFile(/bin/true) error = %v", err)
	}
	if err := os.WriteFile(executable, binary, 0o700); err != nil {
		t.Fatalf("os.WriteFile(executable) error = %v", err)
	}
	stored, err := readDirectSpec(directSpecPath(runDirectory))
	if err != nil {
		t.Fatalf("readDirectSpec() error = %v", err)
	}
	stored.Executable = executable
	if err := sealDirectSpec(&stored); err != nil {
		t.Fatalf("sealDirectSpec() error = %v", err)
	}
	writeTestJSON(t, directSpecPath(runDirectory), stored)
	state, err := readDirectState(runDirectory)
	if err != nil {
		t.Fatalf("readDirectState() error = %v", err)
	}
	state.SpecSHA256 = stored.SpecSHA256
	if err := writeDirectState(runDirectory, state); err != nil {
		t.Fatalf("writeDirectState() error = %v", err)
	}
	launch.Command.Executable = executable
	if err := os.Remove(executable); err != nil {
		t.Fatalf("os.Remove(executable) error = %v", err)
	}
	if err := os.Remove(placement.CheckoutPath); err != nil {
		t.Fatalf("os.Remove(checkout) error = %v", err)
	}
	binding, err := newTestDirectRuntime(options).Launch(context.Background(), operationID, placement, launch)
	if err != nil || binding.RuntimeHandle != directHandle(directTestNodeID, operationID) {
		t.Fatalf("Launch(unavailable exact replay) = %#v, %v", binding, err)
	}
}

func TestDirectLaunchReplayRequiresTheExactCanonicalEffectiveSpecification(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		mutate func(*DirectRuntimeOptions, *domain.RunPlacement, *LaunchSpec)
	}{
		{name: "executable", mutate: func(_ *DirectRuntimeOptions, _ *domain.RunPlacement, launch *LaunchSpec) {
			launch.Command.Executable = "/bin/sh"
		}},
		{name: "arguments", mutate: func(_ *DirectRuntimeOptions, _ *domain.RunPlacement, launch *LaunchSpec) {
			launch.Command.Arguments = []string{"changed"}
		}},
		{name: "standard input", mutate: func(_ *DirectRuntimeOptions, _ *domain.RunPlacement, launch *LaunchSpec) {
			launch.Command.StandardInput = []byte("changed")
		}},
		{name: "environment override", mutate: func(_ *DirectRuntimeOptions, _ *domain.RunPlacement, launch *LaunchSpec) {
			launch.Command.Environment = map[string]string{"LANG": "changed"}
		}},
		{name: "inherited environment", mutate: func(options *DirectRuntimeOptions, _ *domain.RunPlacement, _ *LaunchSpec) {
			options.InheritedEnvironment = []string{"PATH=/changed", "LANG=C"}
		}},
		{name: "working directory", mutate: func(_ *DirectRuntimeOptions, placement *domain.RunPlacement, _ *LaunchSpec) {
			placement.CheckoutPath = t.TempDir()
		}},
		{name: "output limit", mutate: func(_ *DirectRuntimeOptions, _ *domain.RunPlacement, launch *LaunchSpec) {
			launch.Command.OutputByteLimit = 2048
		}},
		{name: "timeout", mutate: func(_ *DirectRuntimeOptions, _ *domain.RunPlacement, launch *LaunchSpec) {
			launch.Command.Timeout = 2 * time.Second
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			operationID := "operation_" + strings.ReplaceAll(testCase.name, " ", "_")
			options, placement, launch := directReplayFixture(t, operationID, RuntimeStateExited)

			first := newTestDirectRuntime(options)
			binding, err := first.Launch(context.Background(), operationID, placement, launch)
			if err != nil || binding.RuntimeHandle != directHandle(directTestNodeID, operationID) {
				t.Fatalf("Launch(existing) = %#v, %v", binding, err)
			}
			restarted := newTestDirectRuntime(options)
			binding, err = restarted.Launch(context.Background(), operationID, placement, launch)
			if err != nil || binding.RuntimeHandle != directHandle(directTestNodeID, operationID) {
				t.Fatalf("Launch(after restart) = %#v, %v", binding, err)
			}

			testCase.mutate(&options, &placement, &launch)
			_, err = newTestDirectRuntime(options).Launch(context.Background(), operationID, placement, launch)
			var unknownError *OutcomeUnknownError
			if !errors.As(err, &unknownError) || !strings.Contains(err.Error(), "different launch specification") {
				t.Fatalf("Launch(changed %s) error = %v, want immutable-spec rejection", testCase.name, err)
			}
		})
	}
}

func TestDirectLaunchReplayValidatesSpecBeforeReturningRecordedStartFailure(t *testing.T) {
	t.Parallel()

	const operationID = "operation_start_failed"
	options, placement, launch := directReplayFixture(t, operationID, directStateStartFailed)
	_, err := newTestDirectRuntime(options).Launch(context.Background(), operationID, placement, launch)
	var startError *StartError
	if !errors.As(err, &startError) || err.Error() != "recorded fixture failure" {
		t.Fatalf("Launch(same failed spec) error = %v", err)
	}
	launch.Command.Arguments = []string{"changed"}
	_, err = newTestDirectRuntime(options).Launch(context.Background(), operationID, placement, launch)
	var unknownError *OutcomeUnknownError
	if !errors.As(err, &unknownError) || !strings.Contains(err.Error(), "different launch specification") || strings.Contains(err.Error(), "recorded fixture failure") {
		t.Fatalf("Launch(changed failed spec) error = %v, want spec rejection before old diagnostic", err)
	}
}

func TestDirectLaunchWaitsForAnExactConcurrentLaunchState(t *testing.T) {
	t.Parallel()

	const operationID = "operation_concurrent"
	options, placement, launch := directReplayFixture(t, operationID, RuntimeStateExited)
	runDirectory := filepath.Join(options.StateRoot, operationID)
	if err := os.Remove(filepath.Join(runDirectory, "state.json")); err != nil {
		t.Fatalf("os.Remove(state) error = %v", err)
	}

	result := make(chan struct {
		binding RuntimeBinding
		err     error
	}, 1)
	go func() {
		binding, err := newTestDirectRuntime(options).Launch(context.Background(), operationID, placement, launch)
		result <- struct {
			binding RuntimeBinding
			err     error
		}{binding: binding, err: err}
	}()
	time.Sleep(4 * directPollInterval)
	spec, err := readDirectSpec(directSpecPath(runDirectory))
	if err != nil {
		t.Fatalf("readDirectSpec() error = %v", err)
	}
	if err := writeDirectState(runDirectory, directSupervisorState{
		Schema: directSupervisorStateSchema, NodeID: directTestNodeID, OperationID: operationID, Status: RuntimeStateRunning,
		SupervisorPID: os.Getpid(), SupervisorStart: processStartIdentity(os.Getpid()), SpecSHA256: spec.SpecSHA256,
	}); err != nil {
		t.Fatalf("writeDirectState() error = %v", err)
	}
	select {
	case replay := <-result:
		if replay.err != nil || replay.binding.RuntimeHandle != directHandle(directTestNodeID, operationID) {
			t.Fatalf("Launch(concurrent replay) = %#v, %v", replay.binding, replay.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Launch(concurrent replay) did not observe the exact state")
	}
}

func TestDirectRuntimeRejectsBindingsFromAnotherNodeBeforeStateAccess(t *testing.T) {
	t.Parallel()

	const operationID = "operation_node_bound"
	const otherNodeID = "22222222222222222222222222222222"
	runtime := NewDirectRuntime(DirectRuntimeOptions{
		NodeID: otherNodeID, StateRoot: filepath.Join(t.TempDir(), "absent-runtime-root"), SupervisorExecutable: "/bin/true",
	})
	if _, err := runtime.InspectStatus(context.Background(), operationID, directHandle(directTestNodeID, operationID)); err == nil || !strings.Contains(err.Error(), "handle does not match") {
		t.Fatalf("InspectStatus(foreign node handle) error = %v", err)
	}
	if _, err := runtime.Stop(context.Background(), operationID, directHandle(directTestNodeID, operationID), StopSpec{}); err == nil || !strings.Contains(err.Error(), "handle does not match") {
		t.Fatalf("Stop(foreign node handle) error = %v", err)
	}
}

func TestDirectRuntimeRejectsCorruptSealedSpecAndState(t *testing.T) {
	t.Parallel()

	t.Run("tampered spec", func(t *testing.T) {
		t.Parallel()
		const operationID = "operation_tampered_spec"
		options, placement, launch := directReplayFixture(t, operationID, RuntimeStateExited)
		path := directSpecPath(filepath.Join(options.StateRoot, operationID))
		spec, err := readDirectSpec(path)
		if err != nil {
			t.Fatalf("readDirectSpec() error = %v", err)
		}
		spec.Arguments = []string{"tampered"}
		writeTestJSON(t, path, spec)
		runtime := newTestDirectRuntime(options)
		if _, err := runtime.Launch(context.Background(), operationID, placement, launch); err == nil || !strings.Contains(err.Error(), "digest") {
			t.Fatalf("Launch(tampered spec) error = %v", err)
		}
		if _, err := runtime.InspectStatus(context.Background(), operationID, directHandle(directTestNodeID, operationID)); err == nil || !strings.Contains(err.Error(), "digest") {
			t.Fatalf("InspectStatus(tampered spec) error = %v", err)
		}
	})

	t.Run("removed spec seal", func(t *testing.T) {
		t.Parallel()
		const operationID = "operation_removed_seal"
		options, placement, launch := directReplayFixture(t, operationID, RuntimeStateExited)
		path := directSpecPath(filepath.Join(options.StateRoot, operationID))
		spec, err := readDirectSpec(path)
		if err != nil {
			t.Fatalf("readDirectSpec() error = %v", err)
		}
		spec.SpecSHA256 = ""
		writeTestJSON(t, path, spec)
		runtime := newTestDirectRuntime(options)
		if _, err := runtime.Launch(context.Background(), operationID, placement, launch); err == nil || !strings.Contains(err.Error(), "digest is invalid") {
			t.Fatalf("Launch(spec without seal) error = %v", err)
		}
		if _, err := runtime.InspectStatus(context.Background(), operationID, directHandle(directTestNodeID, operationID)); err == nil || !strings.Contains(err.Error(), "digest is invalid") {
			t.Fatalf("InspectStatus(spec without seal) error = %v", err)
		}
	})

	t.Run("removed state seal", func(t *testing.T) {
		t.Parallel()
		const operationID = "operation_removed_state_seal"
		options, placement, launch := directReplayFixture(t, operationID, RuntimeStateExited)
		runDirectory := filepath.Join(options.StateRoot, operationID)
		state, err := readDirectState(runDirectory)
		if err != nil {
			t.Fatalf("readDirectState() error = %v", err)
		}
		state.StateSHA256 = ""
		writeTestJSON(t, filepath.Join(runDirectory, "state.json"), state)
		runtime := newTestDirectRuntime(options)
		if _, err := runtime.Launch(context.Background(), operationID, placement, launch); err == nil || !strings.Contains(err.Error(), "state digest is invalid") {
			t.Fatalf("Launch(state without seal) error = %v", err)
		}
		if _, err := runtime.InspectStatus(context.Background(), operationID, directHandle(directTestNodeID, operationID)); err == nil || !strings.Contains(err.Error(), "state digest is invalid") {
			t.Fatalf("InspectStatus(state without seal) error = %v", err)
		}
	})

	t.Run("invalid state", func(t *testing.T) {
		t.Parallel()
		const operationID = "operation_corrupt_state"
		options, placement, launch := directReplayFixture(t, operationID, RuntimeStateExited)
		statePath := filepath.Join(options.StateRoot, operationID, "state.json")
		if err := os.WriteFile(statePath, []byte(`{"schema":"urn:crewfold:direct-supervisor-state:v1","node_id":"11111111111111111111111111111111","operation_id":"operation_corrupt_state","status":"invented"}`), 0o600); err != nil {
			t.Fatalf("os.WriteFile(state) error = %v", err)
		}
		runtime := newTestDirectRuntime(options)
		if _, err := runtime.Launch(context.Background(), operationID, placement, launch); err == nil || !strings.Contains(err.Error(), "status is invalid") {
			t.Fatalf("Launch(corrupt state) error = %v", err)
		}
		if _, err := runtime.InspectStatus(context.Background(), operationID, directHandle(directTestNodeID, operationID)); err == nil || !strings.Contains(err.Error(), "status is invalid") {
			t.Fatalf("InspectStatus(corrupt state) error = %v", err)
		}
	})

	t.Run("exited without process result", func(t *testing.T) {
		t.Parallel()
		const operationID = "operation_incomplete_exit"
		options, placement, launch := directReplayFixture(t, operationID, RuntimeStateExited)
		statePath := filepath.Join(options.StateRoot, operationID, "state.json")
		state, err := readDirectState(filepath.Join(options.StateRoot, operationID))
		if err != nil {
			t.Fatalf("readDirectState() error = %v", err)
		}
		state.ExitKnown = false
		writeTestJSON(t, statePath, state)
		runtime := newTestDirectRuntime(options)
		if _, err := runtime.Launch(context.Background(), operationID, placement, launch); err == nil || !strings.Contains(err.Error(), "known process result") {
			t.Fatalf("Launch(incomplete exit) error = %v", err)
		}
		if _, err := runtime.InspectStatus(context.Background(), operationID, directHandle(directTestNodeID, operationID)); err == nil || !strings.Contains(err.Error(), "known process result") {
			t.Fatalf("InspectStatus(incomplete exit) error = %v", err)
		}
	})

	t.Run("start failure with child evidence", func(t *testing.T) {
		t.Parallel()
		const operationID = "operation_corrupt_start_failure"
		options, placement, launch := directReplayFixture(t, operationID, directStateStartFailed)
		runDirectory := filepath.Join(options.StateRoot, operationID)
		state, err := readDirectState(runDirectory)
		if err != nil {
			t.Fatalf("readDirectState() error = %v", err)
		}
		state.ChildPID, state.ChildStart = 1234, "forged"
		if err := writeDirectState(runDirectory, state); err != nil {
			t.Fatalf("writeDirectState() error = %v", err)
		}
		_, err = newTestDirectRuntime(options).Launch(context.Background(), operationID, placement, launch)
		var unknownError *OutcomeUnknownError
		if !errors.As(err, &unknownError) || !strings.Contains(err.Error(), "impossible process result") {
			t.Fatalf("Launch(corrupt start failure) error = %v", err)
		}
	})
}

func TestDirectStatusOmitsOutputAndLogsAreBoundedAndRedacted(t *testing.T) {
	t.Parallel()

	const operationID = "operation_safe_output"
	options, _, launch := directReplayFixture(t, operationID, RuntimeStateExited)
	runDirectory := filepath.Join(options.StateRoot, operationID)
	secret := "top-secret-credential"
	// The raw child capture fits the operation's deliberately small seal, while
	// replacing many one-byte secret values expands the safe representation well
	// past that same seal. Logs must apply the effective operation limit again
	// after redaction rather than falling back to the driver's global maximum.
	raw := "API_TOKEN=" + secret + "\n"
	raw += strings.Repeat("token=x ", int((launch.Command.OutputByteLimit-int64(len(raw)))/8)+1)
	raw = raw[:launch.Command.OutputByteLimit]
	if err := os.WriteFile(filepath.Join(runDirectory, "stdout.log"), []byte(raw), 0o600); err != nil {
		t.Fatalf("os.WriteFile(stdout) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, "stderr.log"), []byte("Authorization: Bearer "+secret+"\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(stderr) error = %v", err)
	}
	updateDirectTestCaptureState(t, runDirectory, int64(len(raw)), 128, int64(len("Authorization: Bearer "+secret+"\n")), 0)
	runtime := newTestDirectRuntime(options)
	status, err := runtime.InspectStatus(context.Background(), operationID, directHandle(directTestNodeID, operationID))
	if err != nil || status.State != RuntimeStateExited || !status.ExitKnown || !status.CompletionReady {
		t.Fatalf("InspectStatus() = %#v, %v", status, err)
	}
	logs, err := runtime.Logs(context.Background(), operationID, directHandle(directTestNodeID, operationID), 0)
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	if len(logs.Stdout.Text) > int(launch.Command.OutputByteLimit) || !logs.Stdout.Truncated || logs.Stdout.OmittedBytes <= 128 {
		t.Fatalf("bounded stdout = text bytes %d, metadata %#v", len(logs.Stdout.Text), logs.Stdout)
	}
	if strings.Contains(logs.Stdout.Text, secret) || strings.Contains(logs.Stderr.Text, secret) || !strings.Contains(logs.Stdout.Text, "[REDACTED]") || !strings.Contains(logs.Stderr.Text, "[REDACTED]") {
		t.Fatalf("redacted logs = stdout %q stderr %q", logs.Stdout.Text[:min(len(logs.Stdout.Text), 80)], logs.Stderr.Text)
	}
}

func TestDirectLogsRedactBeforeTailingAndRemainBoundedAfterExpansion(t *testing.T) {
	t.Parallel()

	const operationID = "operation_tail_redaction"
	options, _, _ := directReplayFixture(t, operationID, RuntimeStateExited)
	runDirectory := filepath.Join(options.StateRoot, operationID)
	secret := "multiline-secret"
	if err := os.WriteFile(filepath.Join(runDirectory, "stdout.log"), []byte("TOKEN=\n"+secret+"\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(stdout) error = %v", err)
	}
	updateDirectTestCaptureState(t, runDirectory, int64(len("TOKEN=\n"+secret+"\n")), 0, 0, 0)
	logs, err := newTestDirectRuntime(options).Logs(context.Background(), operationID, directHandle(directTestNodeID, operationID), 1)
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	if strings.Contains(logs.Stdout.Text, secret) || len(logs.Stdout.Text) > int(directMaximumOutputLimit) {
		t.Fatalf("Logs(tail) leaked or exceeded bound: bytes=%d contains secret=%t", len(logs.Stdout.Text), strings.Contains(logs.Stdout.Text, secret))
	}
	const effectiveOutputLimit = 1024
	expanding := strings.Repeat("token=x ", effectiveOutputLimit/8)
	if err := os.WriteFile(filepath.Join(runDirectory, "stdout.log"), []byte(expanding), 0o600); err != nil {
		t.Fatalf("os.WriteFile(expanding stdout) error = %v", err)
	}
	updateDirectTestCaptureState(t, runDirectory, int64(len(expanding)), 0, 0, 0)
	all, err := newTestDirectRuntime(options).Logs(context.Background(), operationID, directHandle(directTestNodeID, operationID), 0)
	if err != nil {
		t.Fatalf("Logs(all) error = %v", err)
	}
	if len(all.Stdout.Text) > effectiveOutputLimit || !all.Stdout.Truncated || all.Stdout.OmittedBytes == 0 {
		t.Fatalf("Logs(all) expansion bound = bytes %d metadata %#v", len(all.Stdout.Text), all.Stdout)
	}
}

func TestDirectLogsNormalizeInvalidUTF8FromChildWithoutChangingRawAccounting(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	const operationID = "check_invalid_utf8_output"
	options := DirectRuntimeOptions{
		StateRoot: t.TempDir(), SupervisorExecutable: executable,
		SupervisorArguments:  []string{"-test.run=^TestDirectSupervisorHelper$", "--", "crewfold-direct-supervisor-test-helper"},
		InheritedEnvironment: []string{"PATH=/usr/bin"},
	}
	placement := domain.RunPlacement{CheckoutPath: t.TempDir()}
	launch := LaunchSpec{Command: &CommandSpec{
		Executable: "/bin/sh",
		Arguments:  []string{"-c", `printf '\377\n'`},
		Timeout:    time.Second,
	}}
	runtime := newTestDirectRuntime(options)
	binding, err := runtime.Launch(context.Background(), operationID, placement, launch)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	waitForDirectCompletion(t, runtime, operationID, binding.RuntimeHandle)
	logs, err := runtime.Logs(context.Background(), operationID, binding.RuntimeHandle, 0)
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	if !utf8.ValidString(logs.Stdout.Text) || !strings.ContainsRune(logs.Stdout.Text, utf8.RuneError) {
		t.Fatalf("normalized stdout = %q, want valid UTF-8 replacement", logs.Stdout.Text)
	}
	if logs.Stdout.CapturedBytes != 2 || logs.Stdout.OmittedBytes != 0 || logs.Stdout.Truncated {
		t.Fatalf("raw stdout accounting changed during normalization: %#v", logs.Stdout)
	}
}

func TestDirectTerminalLogsRejectAppendedAndSameSizeReplacement(t *testing.T) {
	t.Parallel()

	const operationID = "operation_output_integrity"
	options, _, _ := directReplayFixture(t, operationID, RuntimeStateExited)
	runDirectory := filepath.Join(options.StateRoot, operationID)
	stdoutPath := filepath.Join(runDirectory, "stdout.log")
	if err := os.WriteFile(stdoutPath, []byte("trusted"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(stdout) error = %v", err)
	}
	updateDirectTestCaptureState(t, runDirectory, 7, 0, 0, 0)
	runtime := newTestDirectRuntime(options)
	file, err := os.OpenFile(stdoutPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("os.OpenFile(stdout append) error = %v", err)
	}
	if _, err := file.WriteString("forged"); err != nil {
		t.Fatalf("WriteString(append) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(append) error = %v", err)
	}
	if _, err := runtime.Logs(context.Background(), operationID, directHandle(directTestNodeID, operationID), 0); err == nil || !strings.Contains(err.Error(), "byte count") {
		t.Fatalf("Logs(appended terminal output) error = %v", err)
	}
	if err := os.WriteFile(stdoutPath, []byte("forged!"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(replacement) error = %v", err)
	}
	if _, err := runtime.Logs(context.Background(), operationID, directHandle(directTestNodeID, operationID), 0); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("Logs(replaced terminal output) error = %v", err)
	}
}

func TestDirectSupervisorRefusesPreexistingOutputSymlink(t *testing.T) {
	t.Parallel()

	runDirectory, checkout := t.TempDir(), t.TempDir()
	target := filepath.Join(t.TempDir(), "must-not-truncate")
	if err := os.WriteFile(target, []byte("preserve me"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(target) error = %v", err)
	}
	if err := os.Symlink(target, filepath.Join(runDirectory, "stdout.log")); err != nil {
		t.Fatalf("os.Symlink(stdout) error = %v", err)
	}
	spec := directSupervisorSpec{
		Schema: directSupervisorSpecSchema, NodeID: directTestNodeID, OperationID: "operation_symlink_capture", Executable: "/bin/true",
		Arguments: []string{}, StandardInput: []byte{}, Environment: []string{"PATH=/usr/bin"}, WorkingDirectory: checkout,
		OutputByteLimit: directDefaultOutputLimit, DefaultGraceMillis: directDefaultGracePeriod.Milliseconds(),
	}
	if err := writeDirectSpec(runDirectory, spec); err != nil {
		t.Fatalf("writeDirectSpec() error = %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	command := exec.Command(executable, "-test.run=^TestDirectSupervisorHelper$", "--", "crewfold-direct-supervisor-test-helper", directSpecPath(runDirectory))
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("RunDirectSupervisor() error = %v, output = %q, want exit 1", err, output)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "preserve me" {
		t.Fatalf("symlink target = %q, %v", contents, err)
	}
}

func TestDirectStopRequestReadIsStrictBoundedAndNoFollow(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "stop.json")
	for name, contents := range map[string]string{
		"unknown":  `{"schema":"urn:crewfold:direct-stop-request:v1","grace_millis":10,"extra":true}`,
		"trailing": `{"schema":"urn:crewfold:direct-stop-request:v1","grace_millis":10} {}`,
		"grace":    fmt.Sprintf(`{"schema":"urn:crewfold:direct-stop-request:v1","grace_millis":%d}`, directMaximumStopGrace.Milliseconds()+1),
		"oversize": strings.Repeat("x", int(directMaximumStopBytes)+1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("os.WriteFile(stop) error = %v", err)
			}
			if _, err := readDirectStopRequest(path); err == nil {
				t.Fatal("readDirectStopRequest() error = nil")
			}
		})
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("os.Remove(stop) error = %v", err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte(`{"schema":"urn:crewfold:direct-stop-request:v1","grace_millis":10}`), 0o600); err != nil {
		t.Fatalf("os.WriteFile(target) error = %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("os.Symlink(stop) error = %v", err)
	}
	if _, err := readDirectStopRequest(path); err == nil {
		t.Fatal("readDirectStopRequest(symlink) error = nil")
	}
}

func directReplayFixture(t *testing.T, operationID, status string) (DirectRuntimeOptions, domain.RunPlacement, LaunchSpec) {
	t.Helper()
	root, checkout := t.TempDir(), t.TempDir()
	options := DirectRuntimeOptions{
		NodeID: directTestNodeID, StateRoot: root, SupervisorExecutable: "/bin/true",
		InheritedEnvironment: []string{"PATH=/usr/bin", "LANG=C", "HOME=/must-not-pass"},
		OutputByteLimit:      4096,
	}
	placement := domain.RunPlacement{CheckoutPath: checkout}
	launch := LaunchSpec{Command: &CommandSpec{
		Executable: "/bin/true", Arguments: []string{"fixture"}, StandardInput: []byte("input"),
		Environment: map[string]string{"LANG": "C"}, Timeout: 1500 * time.Millisecond, OutputByteLimit: 1024,
	}}
	environment, err := buildDirectEnvironment(options.InheritedEnvironment, launch.Command.Environment, operationID)
	if err != nil {
		t.Fatalf("buildDirectEnvironment() error = %v", err)
	}
	spec := directSupervisorSpec{
		Schema: directSupervisorSpecSchema, NodeID: directTestNodeID, OperationID: operationID, Executable: launch.Command.Executable,
		Arguments: append([]string{}, launch.Command.Arguments...), StandardInput: append([]byte{}, launch.Command.StandardInput...), Environment: environment,
		WorkingDirectory: checkout, OutputByteLimit: launch.Command.OutputByteLimit, TimeoutMillis: launch.Command.Timeout.Milliseconds(), DefaultGraceMillis: directDefaultGracePeriod.Milliseconds(),
	}
	if err := sealDirectSpec(&spec); err != nil {
		t.Fatalf("sealDirectSpec() error = %v", err)
	}
	runDirectory := filepath.Join(root, operationID)
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatalf("os.Mkdir(run directory) error = %v", err)
	}
	if err := writeDirectSpec(runDirectory, spec); err != nil {
		t.Fatalf("writeDirectSpec() error = %v", err)
	}
	storedSpec, err := readDirectSpec(directSpecPath(runDirectory))
	if err != nil {
		t.Fatalf("readDirectSpec() error = %v", err)
	}
	stateDigest := storedSpec.SpecSHA256
	state := directSupervisorState{
		Schema: directSupervisorStateSchema, NodeID: directTestNodeID, OperationID: operationID, Status: status,
		ExitCode: 0, ExitKnown: status == RuntimeStateExited, Diagnostic: "recorded fixture failure", SpecSHA256: stateDigest,
	}
	if directStateHasStableCaptures(status) && stateDigest != "" {
		emptyDigest := sha256.Sum256(nil)
		state.StdoutSHA256, state.StderrSHA256 = fmt.Sprintf("%x", emptyDigest[:]), fmt.Sprintf("%x", emptyDigest[:])
	}
	if err := writeDirectState(runDirectory, state); err != nil {
		t.Fatalf("writeDirectState() error = %v", err)
	}
	return options, placement, launch
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%T) error = %v", value, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("os.WriteFile(%s) error = %v", filepath.Base(path), err)
	}
}

func updateDirectTestCaptureState(t *testing.T, runDirectory string, stdoutCaptured, stdoutOmitted, stderrCaptured, stderrOmitted int64) {
	t.Helper()
	state, err := readDirectState(runDirectory)
	if err != nil {
		t.Fatalf("readDirectState(capture metadata) error = %v", err)
	}
	state.StdoutCapturedBytes, state.StdoutOmittedBytes = stdoutCaptured, stdoutOmitted
	state.StderrCapturedBytes, state.StderrOmittedBytes = stderrCaptured, stderrOmitted
	state.StdoutSHA256 = directTestFileSHA256(t, filepath.Join(runDirectory, "stdout.log"))
	state.StderrSHA256 = directTestFileSHA256(t, filepath.Join(runDirectory, "stderr.log"))
	if err := writeDirectState(runDirectory, state); err != nil {
		t.Fatalf("writeDirectState(capture metadata) error = %v", err)
	}
}

func directTestFileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data = nil
	} else if err != nil {
		t.Fatalf("os.ReadFile(%s) error = %v", filepath.Base(path), err)
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:])
}

func waitForDirectCompletion(t *testing.T, runtime *DirectRuntime, operationID, handle string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := runtime.InspectStatus(context.Background(), operationID, handle)
		if err == nil && status.CompletionReady {
			return
		}
		time.Sleep(directPollInterval)
	}
	t.Fatalf("direct operation %s did not reach a stable terminal state", operationID)
}

func jsonEncode(buffer *bytes.Buffer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = buffer.Write(data)
	return err
}
