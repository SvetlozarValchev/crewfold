package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

func TestCheckProcessHelper(t *testing.T) {
	for index, argument := range os.Args {
		if argument != "crewfold-check-process-helper" {
			continue
		}
		if index+1 >= len(os.Args) {
			os.Exit(91)
		}
		switch os.Args[index+1] {
		case "sleep-pass":
			time.Sleep(200 * time.Millisecond)
			_, _ = os.Stdout.WriteString("check passed\n")
		case "invalid-utf8":
			_, _ = os.Stdout.Write([]byte{'o', 'k', ':', 0xff, '\n'})
		case "redaction-expansion":
			// Exactly 1024 raw bytes expand substantially when every one-byte
			// secret is replaced. The runtime must reapply the definition's
			// output cap after redaction so terminal persistence cannot fault.
			_, _ = os.Stdout.WriteString(strings.Repeat("token=x ", 128))
		case "mark-pass":
			if index+2 >= len(os.Args) {
				os.Exit(92)
			}
			file, err := os.OpenFile(os.Args[index+2], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				os.Exit(93)
			}
			_, _ = file.WriteString("launched\n")
			_ = file.Close()
		case "relative-mark-pass":
			if err := os.WriteFile("effect.txt", []byte("launched\n"), 0o600); err != nil {
				os.Exit(95)
			}
		default:
			os.Exit(94)
		}
		os.Exit(0)
	}
}

func TestCheckWorkerDefersRunningChildAndPersistsSafeMechanicalEvidence(t *testing.T) {
	if _, err := os.Stat("../../test/fixtures/git/create.sh"); err != nil {
		t.Skipf("Git fixture is unavailable: %v", err)
	}

	for _, testCase := range []struct {
		name            string
		mode            string
		outputByteLimit int64
	}{
		{name: "running child is polled without stopping daemon", mode: "sleep-pass", outputByteLimit: 4096},
		{name: "invalid UTF-8 output is normalized", mode: "invalid-utf8", outputByteLimit: 4096},
		{name: "secret redaction expansion respects definition cap", mode: "redaction-expansion", outputByteLimit: 1024},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixtureRoot := t.TempDir()
			createGitFixture(t, fixtureRoot)
			config, executable := checkWorkerTestConfig(t)
			running := startTestServer(t, config)
			client := localapi.NewClient(config.SocketPath)
			started := createOwnerCheckRun(t, client, fixtureRoot, executable, ".", []string{
				"-test.run=^TestCheckProcessHelper$", "--", "crewfold-check-process-helper", testCase.mode,
			}, testCase.outputByteLimit)

			var detail domain.CheckRunDetail
			observedDeferred := false
			waitForCondition(t, 8*time.Second, func() bool {
				inspected, err := client.CheckInspect(context.Background(), "personal", started.Run.ID)
				if err != nil {
					return false
				}
				detail = inspected.Detail
				if detail.Run.Status == domain.CheckRunRunning && detail.Job.Status == domain.CheckJobPending {
					observedDeferred = true
				}
				return detail.Run.Status == domain.CheckRunFinished && detail.Result != nil
			}, "direct check result")

			if detail.Result.Outcome != domain.CheckOutcomePassed || detail.CurrentFreshness == nil || detail.CurrentFreshness.Status != domain.CheckFreshnessFresh {
				t.Fatalf("finished check = %#v", detail)
			}
			if len(detail.Evidence.MechanicalCheck) != 1 || detail.Evidence.MechanicalCheck[0].Effect != domain.CheckEvidenceSupports {
				t.Fatalf("mechanical evidence = %#v", detail.Evidence)
			}
			if testCase.mode == "sleep-pass" && !observedDeferred {
				t.Fatal("worker never exposed the running child as a deferred durable job")
			}
			logs, err := client.CheckLogs(context.Background(), "personal", started.Run.ID)
			if err != nil {
				t.Fatalf("CheckLogs() error = %v", err)
			}
			if logs.Logs.Stdout == nil {
				t.Fatal("finished check has no stdout artifact")
			}
			if !utf8.ValidString(logs.Logs.Stdout.Content) {
				t.Fatalf("stdout is not valid UTF-8: %q", logs.Logs.Stdout.Content)
			}
			if testCase.mode == "invalid-utf8" && !strings.Contains(logs.Logs.Stdout.Content, "ok:") {
				t.Fatalf("normalized stdout = %q", logs.Logs.Stdout.Content)
			}
			if testCase.mode == "redaction-expansion" {
				if strings.Contains(logs.Logs.Stdout.Content, "token=x") || !strings.Contains(logs.Logs.Stdout.Content, "[REDACTED]") {
					t.Fatalf("expanded stdout was not redacted: %q", logs.Logs.Stdout.Content)
				}
				if logs.Logs.Stdout.CapturedBytes > testCase.outputByteLimit || !logs.Logs.Stdout.Truncated || logs.Logs.Stdout.OmittedBytes == 0 {
					t.Fatalf("expanded stdout escaped definition cap: %#v", logs.Logs.Stdout)
				}
			}
			if _, err := client.Status(context.Background()); err != nil {
				t.Fatalf("daemon stopped after check completion: %v", err)
			}
			if _, err := client.Stop(context.Background()); err != nil {
				t.Fatalf("Stop() error = %v", err)
			}
			if err := running.wait(); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
		})
	}
}

func TestCheckWorkerRecoversReceiptedLaunchExactlyOnce(t *testing.T) {
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config, executable := checkWorkerTestConfig(t)
	marker := filepath.Join(t.TempDir(), "launches")
	var barrierReached atomic.Bool
	config.CheckWorkerHook = func(stage string, _ domain.CheckRun) error {
		if stage == "after_check_launch_receipt" && barrierReached.CompareAndSwap(false, true) {
			return errors.New("injected restart after immutable check launch receipt")
		}
		return nil
	}

	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	started := createOwnerCheckRun(t, client, fixtureRoot, executable, ".", []string{
		"-test.run=^TestCheckProcessHelper$", "--", "crewfold-check-process-helper", "mark-pass", marker,
	}, 4096)
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}
	if !barrierReached.Load() {
		t.Fatal("check launch-receipt barrier was not reached")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child executed before restart: %v", err)
	}

	config.CheckWorkerHook = nil
	config.CheckRuntimeDriver = newCheckWorkerDirectRuntime(t, config.DataDir, executable)
	second := startTestServer(t, config)
	restarted := localapi.NewClient(config.SocketPath)
	detail := waitForCheckResult(t, restarted, started.Run.ID)
	if detail.Result.Outcome != domain.CheckOutcomePassed {
		t.Fatalf("recovered check = %#v", detail)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("ReadFile(marker) error = %v", err)
	}
	if string(data) != "launched\n" {
		t.Fatalf("launch marker = %q, want exactly one child", data)
	}
	if _, err := restarted.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
}

type mismatchedCheckReconcileRuntime struct {
	base           *execution.DirectRuntime
	reconcileCalls atomic.Int64
	inspectCalls   atomic.Int64
	logCalls       atomic.Int64
}

func (*mismatchedCheckReconcileRuntime) Name() string { return "direct" }

func (runtime *mismatchedCheckReconcileRuntime) PrepareLaunch(ctx context.Context, operationID string, placement domain.RunPlacement, spec execution.LaunchSpec) (execution.RuntimeLaunchPreparation, error) {
	return runtime.base.PrepareLaunch(ctx, operationID, placement, spec)
}

func (runtime *mismatchedCheckReconcileRuntime) Launch(ctx context.Context, operationID string, placement domain.RunPlacement, spec execution.LaunchSpec) (execution.RuntimeBinding, error) {
	return runtime.base.Launch(ctx, operationID, placement, spec)
}

func (runtime *mismatchedCheckReconcileRuntime) Reconcile(ctx context.Context, operationID, handle string) (execution.RuntimeBinding, error) {
	runtime.reconcileCalls.Add(1)
	binding, err := runtime.base.Reconcile(ctx, operationID, handle)
	if err == nil {
		binding.RuntimeHandle += "-different-operation"
	}
	return binding, err
}

func (runtime *mismatchedCheckReconcileRuntime) Inspect(ctx context.Context, operationID, handle string) (execution.RuntimeSnapshot, error) {
	runtime.inspectCalls.Add(1)
	return runtime.base.Inspect(ctx, operationID, handle)
}

func (runtime *mismatchedCheckReconcileRuntime) InspectStatus(ctx context.Context, operationID, handle string) (execution.RuntimeStatus, error) {
	runtime.inspectCalls.Add(1)
	return runtime.base.InspectStatus(ctx, operationID, handle)
}

func (runtime *mismatchedCheckReconcileRuntime) Stop(ctx context.Context, operationID, handle string, spec execution.StopSpec) (execution.StopResult, error) {
	return runtime.base.Stop(ctx, operationID, handle, spec)
}

func (runtime *mismatchedCheckReconcileRuntime) Logs(ctx context.Context, operationID, handle string, tail int) (domain.RunLogs, error) {
	runtime.logCalls.Add(1)
	return runtime.base.Logs(ctx, operationID, handle, tail)
}

func TestCheckWorkerRejectsMismatchedReconciledBindingBeforeFurtherRuntimeContact(t *testing.T) {
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config, executable := checkWorkerTestConfig(t)
	var barrierReached atomic.Bool
	config.CheckWorkerHook = func(stage string, _ domain.CheckRun) error {
		if stage == "after_check_runtime_binding" && barrierReached.CompareAndSwap(false, true) {
			return errors.New("injected restart after exact check runtime binding")
		}
		return nil
	}

	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	started := createOwnerCheckRun(t, client, fixtureRoot, executable, ".", []string{
		"-test.run=^TestCheckProcessHelper$", "--", "crewfold-check-process-helper", "sleep-pass",
	}, 4096)
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}
	if !barrierReached.Load() {
		t.Fatal("check runtime-binding barrier was not reached")
	}

	config.CheckWorkerHook = nil
	runtime := &mismatchedCheckReconcileRuntime{base: newCheckWorkerDirectRuntime(t, config.DataDir, executable)}
	config.CheckRuntimeDriver = runtime
	second := startTestServer(t, config)
	restarted := localapi.NewClient(config.SocketPath)
	detail := waitForCheckResult(t, restarted, started.Run.ID)
	if detail.Result.Outcome != domain.CheckOutcomeUnknown || detail.Result.DiagnosticCode != "runtime_binding_mismatch" {
		t.Fatalf("mismatched reconciled binding result = %#v", detail.Result)
	}
	if runtime.reconcileCalls.Load() != 1 || runtime.inspectCalls.Load() != 0 || runtime.logCalls.Load() != 0 {
		t.Fatalf("runtime contacts after mismatched reconcile: reconcile=%d inspect=%d logs=%d", runtime.reconcileCalls.Load(), runtime.inspectCalls.Load(), runtime.logCalls.Load())
	}
	if _, err := restarted.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
}

func TestCheckWorkerRejectsWorkingDirectorySymlinkSwapAfterLaunchReceipt(t *testing.T) {
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	checkout := filepath.Join(fixtureRoot, "world-engine")
	workingDirectory := filepath.Join(checkout, "check-work")
	movedWorkingDirectory := workingDirectory + "-old"
	outside := t.TempDir()
	if err := os.Mkdir(workingDirectory, 0o700); err != nil {
		t.Fatalf("os.Mkdir(working directory) error = %v", err)
	}

	config, executable := checkWorkerTestConfig(t)
	var barrierReached atomic.Bool
	config.CheckWorkerHook = func(stage string, _ domain.CheckRun) error {
		if stage != "after_check_launch_receipt" || !barrierReached.CompareAndSwap(false, true) {
			return nil
		}
		if err := os.Rename(workingDirectory, movedWorkingDirectory); err != nil {
			return err
		}
		if err := os.Symlink(outside, workingDirectory); err != nil {
			return err
		}
		return errors.New("injected restart after swapping the receipted working directory")
	}

	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	started := createOwnerCheckRun(t, client, fixtureRoot, executable, "check-work", []string{
		"-test.run=^TestCheckProcessHelper$", "--", "crewfold-check-process-helper", "relative-mark-pass",
	}, 4096)
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}
	if !barrierReached.Load() {
		t.Fatal("check launch-receipt swap barrier was not reached")
	}
	if _, err := os.Lstat(filepath.Join(outside, "effect.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside directory received an effect before recovery: %v", err)
	}

	config.CheckWorkerHook = nil
	config.CheckRuntimeDriver = newCheckWorkerDirectRuntime(t, config.DataDir, executable)
	second := startTestServer(t, config)
	restarted := localapi.NewClient(config.SocketPath)
	detail := waitForCheckResult(t, restarted, started.Run.ID)
	if detail.Result.Outcome != domain.CheckOutcomeStartFailed && detail.Result.Outcome != domain.CheckOutcomeUnknown {
		t.Fatalf("recovered swapped working directory result = %#v", detail.Result)
	}
	if _, err := os.Lstat(filepath.Join(outside, "effect.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside directory received an effect during recovery: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(movedWorkingDirectory, "effect.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("check child unexpectedly launched in the moved directory: %v", err)
	}
	if _, err := restarted.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
}

func checkWorkerTestConfig(t *testing.T) (Config, string) {
	t.Helper()
	config := testConfig(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	config.CheckRuntimeDriver = newCheckWorkerDirectRuntime(t, config.DataDir, executable)
	config.DisableRunWorker = true
	config.DisableCheckWatcher = true
	return config, executable
}

func newCheckWorkerDirectRuntime(t *testing.T, dataDir, executable string) *execution.DirectRuntime {
	t.Helper()
	return execution.NewDirectRuntime(execution.DirectRuntimeOptions{
		NodeID:                         daemonTestNodeID,
		StateRoot:                      filepath.Join(dataDir, "check-runtime"),
		SupervisorExecutable:           executable,
		SupervisorArguments:            []string{"-test.run=^TestDirectProcessHelper$", "--", "crewfold-direct-supervisor-helper"},
		InheritedEnvironment:           checkRuntimeEnvironment(os.Environ()),
		OutputByteLimit:                1024 * 1024,
		OperationIDEnvironmentVariable: execution.DirectCheckRunIDEnvironmentVariable,
	})
}

func createOwnerCheckRun(t *testing.T, client *localapi.Client, fixtureRoot, executable, workingDirectory string, arguments []string, outputByteLimit int64) localapi.CheckRunMutationResult {
	t.Helper()
	if _, err := client.WorkspaceInit(context.Background(), "personal", "check-worker-workspace"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	project, err := client.ProjectAdd(context.Background(), "personal", "checks", filepath.Join(fixtureRoot, "world-engine"), domain.WriteModeShared, "check-worker-project")
	if err != nil {
		t.Fatalf("ProjectAdd() error = %v", err)
	}
	task, err := client.TaskCreate(context.Background(), localapi.TaskCreateParams{Workspace: "personal", Project: project.Project.ID, Title: "mechanical check target", Priority: 10, IdempotencyKey: "check-worker-task"})
	if err != nil {
		t.Fatalf("TaskCreate() error = %v", err)
	}
	definition, err := client.CheckDefinitionCreate(context.Background(), localapi.CheckDefinitionCreateParams{
		Workspace: "personal", Project: project.Project.ID, Name: "worker-check", Executable: executable,
		Arguments: arguments, WorkingDirectory: workingDirectory, TimeoutMillis: 5000, OutputByteLimit: outputByteLimit,
		IdempotencyKey: "check-worker-definition",
	})
	if err != nil {
		t.Fatalf("CheckDefinitionCreate() error = %v", err)
	}
	requirement, err := client.CheckRequirementCreate(context.Background(), localapi.CheckRequirementCreateParams{
		Workspace: "personal", Task: task.Detail.Task.ID, CriterionKey: "worker-check", Statement: "the mechanical check passes",
		Definition: definition.Definition.ID, DefinitionContentRevision: definition.Definition.ContentRevision,
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "check-worker-requirement",
	})
	if err != nil {
		t.Fatalf("CheckRequirementCreate() error = %v", err)
	}
	started, err := client.CheckRun(context.Background(), localapi.CheckRunParams{
		Workspace: "personal", Task: task.Detail.Task.ID, Definition: definition.Definition.ID,
		Checkout: project.Checkout.ID, ExpectedCheckoutRevision: project.Checkout.Revision,
		ExpectedRequirementRevision:       requirement.Requirement.Revision,
		ExpectedDefinitionContentRevision: definition.Definition.ContentRevision,
		IdempotencyKey:                    "check-worker-run",
	})
	if err != nil {
		t.Fatalf("CheckRun() error = %v", err)
	}
	return started
}

func waitForCheckResult(t *testing.T, client *localapi.Client, runID string) domain.CheckRunDetail {
	t.Helper()
	var detail domain.CheckRunDetail
	waitForCondition(t, 8*time.Second, func() bool {
		result, err := client.CheckInspect(context.Background(), "personal", runID)
		if err != nil {
			return false
		}
		detail = result.Detail
		return detail.Run.Status == domain.CheckRunFinished && detail.Result != nil
	}, "check run "+runID+" to finish")
	return detail
}
