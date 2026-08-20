package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"

	"golang.org/x/sys/unix"
)

const (
	directSupervisorSpecSchema   = "urn:crewfold:direct-supervisor-spec:v1"
	directSupervisorStateSchema  = "urn:crewfold:direct-supervisor-state:v1"
	directStopRequestSchema      = "urn:crewfold:direct-stop-request:v1"
	directDefaultOutputLimit     = int64(64 * 1024)
	directMaximumOutputLimit     = int64(1024 * 1024)
	directDefaultGracePeriod     = 500 * time.Millisecond
	directStartupTimeout         = 3 * time.Second
	directPollInterval           = 10 * time.Millisecond
	directStateStartFailed       = "start_failed"
	directMaximumSpecBytes       = int64(256 * 1024)
	directMaximumStateBytes      = int64(64 * 1024)
	directMaximumDiagnosticBytes = 16 * 1024
	directMaximumStopBytes       = int64(4 * 1024)
	directMaximumStopGrace       = 30 * time.Second
	directSupervisorWorkingDirFD = 3

	DirectRunIDEnvironmentVariable      = "CREWFOLD_RUN_ID"
	DirectCheckRunIDEnvironmentVariable = "CREWFOLD_CHECK_RUN_ID"
)

var (
	directOperationPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	directSpecSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	secretAssignment        = regexp.MustCompile(`(?i)((?:"?authorization"?\s*:\s*"?bearer\s+)|(?:"?(?:token|secret|password|api[_-]?key)"?\s*[:=]\s*"?))([^"\s,}]+)`)
	errDirectSpecExists     = errors.New("direct runtime launch specification already exists")
)

var directInheritedEnvironmentAllowlist = []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "CREWFOLD_MCP_CAPABILITY_FILE", "CREWFOLD_MCP_SOCKET", "LANG", "LC_ALL", "LC_CTYPE", "PATH", "TMPDIR", "TZ"}

// Provider adapters are trusted runtime components. They may add these
// non-secret process settings without allowing arbitrary inherited host values
// through the runtime boundary. In particular, HOME makes login-shell startup
// coherent while package caches remain redirected to a writable sandbox path.
var directProviderEnvironmentAllowlist = []string{"HOME", "NPM_CONFIG_CACHE", "XDG_CACHE_HOME"}

type DirectRuntimeOptions struct {
	NodeID                         string
	StateRoot                      string
	SupervisorExecutable           string
	SupervisorArguments            []string
	InheritedEnvironment           []string
	OutputByteLimit                int64
	StartupTimeout                 time.Duration
	OperationIDEnvironmentVariable string
}

// DirectRuntime launches one durable supervisor per operation. The supervisor is
// deliberately a separate process so output bounds and exit records survive a
// daemon restart.
type DirectRuntime struct {
	nodeID                         string
	root                           string
	supervisorExecutable           string
	supervisorArguments            []string
	inheritedEnvironment           []string
	outputLimit                    int64
	startupTimeout                 time.Duration
	operationIDEnvironmentVariable string
	mu                             sync.Mutex
}

func NewDirectRuntime(options DirectRuntimeOptions) *DirectRuntime {
	outputLimit := options.OutputByteLimit
	if outputLimit <= 0 || outputLimit > directMaximumOutputLimit {
		outputLimit = directDefaultOutputLimit
	}
	startupTimeout := options.StartupTimeout
	if startupTimeout <= 0 {
		startupTimeout = directStartupTimeout
	}
	var inherited []string
	if options.InheritedEnvironment == nil {
		inherited = os.Environ()
	} else {
		inherited = append([]string{}, options.InheritedEnvironment...)
	}
	operationIDEnvironmentVariable := options.OperationIDEnvironmentVariable
	if operationIDEnvironmentVariable == "" {
		operationIDEnvironmentVariable = DirectRunIDEnvironmentVariable
	}
	return &DirectRuntime{
		nodeID:                         strings.TrimSpace(options.NodeID),
		root:                           options.StateRoot,
		supervisorExecutable:           options.SupervisorExecutable,
		supervisorArguments:            append([]string(nil), options.SupervisorArguments...),
		inheritedEnvironment:           inherited,
		outputLimit:                    outputLimit,
		startupTimeout:                 startupTimeout,
		operationIDEnvironmentVariable: operationIDEnvironmentVariable,
	}
}

func (runtime *DirectRuntime) Name() string { return "direct" }

// PrepareLaunch returns the exact effective-spec seal used by Launch without
// creating an operation directory, writing runtime state, or starting a child.
func (runtime *DirectRuntime) PrepareLaunch(_ context.Context, operationID string, placement domain.RunPlacement, launch LaunchSpec) (RuntimeLaunchPreparation, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	spec, err := runtime.prepareEffectiveSpec(operationID, placement, launch)
	if err != nil {
		return RuntimeLaunchPreparation{}, err
	}
	return RuntimeLaunchPreparation{SpecSHA256: spec.SpecSHA256}, nil
}

func (runtime *DirectRuntime) Launch(ctx context.Context, operationID string, placement domain.RunPlacement, launch LaunchSpec) (RuntimeBinding, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	spec, err := runtime.prepareEffectiveSpec(operationID, placement, launch)
	if err != nil {
		return RuntimeBinding{}, err
	}
	workingDirectory := spec.WorkingDirectory
	executable := spec.Executable
	environment := spec.Environment

	runDirectory, err := runtime.prepareRunDirectory(operationID)
	if err != nil {
		return RuntimeBinding{}, &StartError{Message: err.Error()}
	}
	binding := RuntimeBinding{RuntimeHandle: directHandle(runtime.nodeID, operationID)}
	if state, stateErr := readDirectState(runDirectory); stateErr == nil {
		if state.NodeID != runtime.nodeID || state.OperationID != operationID {
			return RuntimeBinding{}, &OutcomeUnknownError{Message: "existing direct runtime state identity is invalid"}
		}
		if err := verifyDirectReplay(directSpecPath(runDirectory), state.SpecSHA256, state.StateSHA256, spec); err != nil {
			return RuntimeBinding{}, &OutcomeUnknownError{Message: err.Error()}
		}
		switch state.Status {
		case RuntimeStateRunning, RuntimeStateExited, RuntimeStateStopped, RuntimeStateTimedOut:
			return binding, nil
		case directStateStartFailed:
			return RuntimeBinding{}, &StartError{Message: state.Diagnostic}
		case RuntimeStateUnknown:
			return RuntimeBinding{}, &OutcomeUnknownError{Message: state.Diagnostic}
		case RuntimeStateStarting:
			return runtime.waitForDirectReplayState(ctx, runDirectory, binding, spec)
		}
	} else if !errors.Is(stateErr, os.ErrNotExist) {
		return RuntimeBinding{}, &OutcomeUnknownError{Message: fmt.Sprintf("read existing direct runtime state: %v", stateErr)}
	}
	if _, specErr := os.Lstat(directSpecPath(runDirectory)); specErr == nil {
		if err := verifyDirectPendingReplay(directSpecPath(runDirectory), spec); err != nil {
			return RuntimeBinding{}, &OutcomeUnknownError{Message: err.Error()}
		}
		return runtime.waitForDirectReplayState(ctx, runDirectory, binding, spec)
	} else if !errors.Is(specErr, os.ErrNotExist) {
		return RuntimeBinding{}, &OutcomeUnknownError{Message: fmt.Sprintf("inspect existing direct runtime launch specification: %v", specErr)}
	}
	workingDirectoryFile, err := openDirectWorkingDirectory(workingDirectory)
	if err != nil {
		return RuntimeBinding{}, &StartError{Message: "assigned checkout directory is unavailable without following symbolic links"}
	}
	defer workingDirectoryFile.Close()
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return RuntimeBinding{}, &StartError{Message: "direct runtime executable is unavailable or not executable"}
	}
	if err := writeDirectSpec(runDirectory, spec); err != nil {
		if errors.Is(err, errDirectSpecExists) {
			if replayErr := verifyDirectPendingReplay(directSpecPath(runDirectory), spec); replayErr != nil {
				return RuntimeBinding{}, &OutcomeUnknownError{Message: replayErr.Error()}
			}
			return runtime.waitForDirectReplayState(ctx, runDirectory, binding, spec)
		}
		return RuntimeBinding{}, &StartError{Message: err.Error()}
	}
	initial := directSupervisorState{Schema: directSupervisorStateSchema, NodeID: runtime.nodeID, OperationID: operationID, Status: RuntimeStateStarting, SpecSHA256: spec.SpecSHA256}
	if err := writeDirectState(runDirectory, initial); err != nil {
		return RuntimeBinding{}, &OutcomeUnknownError{Message: fmt.Sprintf("record direct runtime launch intent after publishing its immutable specification: %v", err)}
	}

	supervisorArguments := append([]string(nil), runtime.supervisorArguments...)
	if len(supervisorArguments) == 0 {
		supervisorArguments = []string{"__direct-supervisor"}
	}
	supervisorArguments = append(supervisorArguments, directSpecPath(runDirectory), strconv.Itoa(directSupervisorWorkingDirFD))
	command := exec.CommandContext(context.WithoutCancel(ctx), runtime.supervisorExecutable, supervisorArguments...)
	command.Env = environment
	command.ExtraFiles = []*os.File{workingDirectoryFile}
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		initial.Status, initial.Diagnostic = directStateStartFailed, "start direct runtime supervisor: "+err.Error()
		_ = writeDirectState(runDirectory, initial)
		return RuntimeBinding{}, &StartError{Message: initial.Diagnostic}
	}
	go func() { _ = command.Wait() }()

	deadline := time.NewTimer(runtime.startupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(directPollInterval)
	defer ticker.Stop()
	for {
		state, stateErr := readDirectState(runDirectory)
		if stateErr == nil {
			if state.NodeID != runtime.nodeID || state.OperationID != operationID {
				return RuntimeBinding{}, &OutcomeUnknownError{Message: "direct runtime supervisor acknowledgement identity is invalid"}
			}
			if replayErr := verifyDirectReplay(directSpecPath(runDirectory), state.SpecSHA256, state.StateSHA256, spec); replayErr != nil {
				return RuntimeBinding{}, &OutcomeUnknownError{Message: "direct runtime supervisor acknowledged an invalid immutable launch specification: " + replayErr.Error()}
			}
			switch state.Status {
			case RuntimeStateRunning, RuntimeStateExited, RuntimeStateStopped, RuntimeStateTimedOut:
				return binding, nil
			case directStateStartFailed:
				return RuntimeBinding{}, &StartError{Message: state.Diagnostic}
			case RuntimeStateUnknown:
				return RuntimeBinding{}, &OutcomeUnknownError{Message: state.Diagnostic}
			}
		} else if !errors.Is(stateErr, os.ErrNotExist) {
			return RuntimeBinding{}, &OutcomeUnknownError{Message: "direct runtime supervisor recorded invalid state: " + stateErr.Error()}
		}
		select {
		case <-ctx.Done():
			return RuntimeBinding{}, &OutcomeUnknownError{Message: "direct runtime launch was cancelled after supervisor creation but before acknowledgement"}
		case <-deadline.C:
			return RuntimeBinding{}, &OutcomeUnknownError{Message: "direct runtime supervisor did not acknowledge launch; child state may be unknown"}
		case <-ticker.C:
		}
	}
}

func (runtime *DirectRuntime) waitForDirectReplayState(ctx context.Context, runDirectory string, binding RuntimeBinding, spec directSupervisorSpec) (RuntimeBinding, error) {
	deadline := time.NewTimer(runtime.startupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(directPollInterval)
	defer ticker.Stop()
	for {
		state, err := readDirectState(runDirectory)
		if err == nil {
			if state.NodeID != runtime.nodeID || state.OperationID != spec.OperationID {
				return RuntimeBinding{}, &OutcomeUnknownError{Message: "direct runtime concurrent launch state identity is invalid"}
			}
			if replayErr := verifyDirectReplay(directSpecPath(runDirectory), state.SpecSHA256, state.StateSHA256, spec); replayErr != nil {
				return RuntimeBinding{}, &OutcomeUnknownError{Message: "direct runtime concurrent launch recorded an invalid immutable specification: " + replayErr.Error()}
			}
			switch state.Status {
			case RuntimeStateRunning, RuntimeStateExited, RuntimeStateStopped, RuntimeStateTimedOut:
				return binding, nil
			case directStateStartFailed:
				return RuntimeBinding{}, &StartError{Message: state.Diagnostic}
			case RuntimeStateUnknown:
				return RuntimeBinding{}, &OutcomeUnknownError{Message: state.Diagnostic}
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return RuntimeBinding{}, &OutcomeUnknownError{Message: "direct runtime concurrent launch recorded invalid state: " + err.Error()}
		}
		select {
		case <-ctx.Done():
			return RuntimeBinding{}, &OutcomeUnknownError{Message: "direct runtime launch specification was published but its concurrent state acknowledgement is unknown"}
		case <-deadline.C:
			return RuntimeBinding{}, &OutcomeUnknownError{Message: "direct runtime launch specification exists without a recoverable state; child outcome is unknown"}
		case <-ticker.C:
		}
	}
}

func (runtime *DirectRuntime) Reconcile(ctx context.Context, operationID, handle string) (RuntimeBinding, error) {
	if err := validateDirectHandle(runtime.nodeID, operationID, handle); err != nil {
		return RuntimeBinding{}, err
	}
	status, err := runtime.InspectStatus(ctx, operationID, handle)
	if err != nil {
		return RuntimeBinding{}, err
	}
	if status.State == RuntimeStateUnknown {
		return RuntimeBinding{}, errors.New(status.Diagnostic)
	}
	return RuntimeBinding{RuntimeHandle: handle}, nil
}

func (runtime *DirectRuntime) Inspect(_ context.Context, operationID, handle string) (RuntimeSnapshot, error) {
	state, err := runtime.inspectState(operationID, handle)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	runDirectory, err := runtime.runDirectory(operationID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	stdout, err := readDirectCapture(filepath.Join(runDirectory, "stdout.log"), state.StdoutCapturedBytes, state.StdoutOmittedBytes, state.StdoutSHA256, 0, directMaximumOutputLimit, directStateHasStableCaptures(state.Status))
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	stderr, err := readDirectCapture(filepath.Join(runDirectory, "stderr.log"), state.StderrCapturedBytes, state.StderrOmittedBytes, state.StderrSHA256, 0, directMaximumOutputLimit, directStateHasStableCaptures(state.Status))
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	status := directStatus(state)
	return RuntimeSnapshot{
		State: status.State, ExitCode: status.ExitCode, ExitKnown: status.ExitKnown,
		CompletionReady: status.CompletionReady,
		Forced:          status.Forced, Diagnostic: status.Diagnostic, Stdout: stdout, Stderr: stderr,
	}, nil
}

// InspectStatus returns lifecycle state without reading or returning the raw
// stdout/stderr capture. Mechanical consumers should pair this with Logs when
// they need bounded, redacted diagnostic text.
func (runtime *DirectRuntime) InspectStatus(_ context.Context, operationID, handle string) (RuntimeStatus, error) {
	state, err := runtime.inspectState(operationID, handle)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return directStatus(state), nil
}

func (runtime *DirectRuntime) inspectState(operationID, handle string) (directSupervisorState, error) {
	if err := validateDirectHandle(runtime.nodeID, operationID, handle); err != nil {
		return directSupervisorState{}, err
	}
	runDirectory, err := runtime.runDirectory(operationID)
	if err != nil {
		return directSupervisorState{}, err
	}
	state, err := readDirectState(runDirectory)
	if err != nil {
		return directSupervisorState{}, fmt.Errorf("read direct runtime state: %w", err)
	}
	if state.NodeID != runtime.nodeID || state.OperationID != operationID || state.Schema != directSupervisorStateSchema {
		return directSupervisorState{}, errors.New("direct runtime state identity is invalid")
	}
	if err := verifyDirectStoredSpec(directSpecPath(runDirectory), state.SpecSHA256, state.StateSHA256, runtime.nodeID, operationID); err != nil {
		return directSupervisorState{}, err
	}
	if (state.Status == RuntimeStateStarting || state.Status == RuntimeStateRunning) && state.SupervisorPID > 0 && !sameProcess(state.SupervisorPID, state.SupervisorStart) {
		time.Sleep(directPollInterval)
		if refreshed, refreshErr := readDirectState(runDirectory); refreshErr == nil {
			if refreshed.NodeID != runtime.nodeID || refreshed.OperationID != operationID || refreshed.Schema != directSupervisorStateSchema {
				return directSupervisorState{}, errors.New("direct runtime state identity changed during inspection")
			}
			state = refreshed
		} else {
			return directSupervisorState{}, fmt.Errorf("refresh direct runtime state after process disappearance: %w", refreshErr)
		}
		if (state.Status == RuntimeStateStarting || state.Status == RuntimeStateRunning) && !sameProcess(state.SupervisorPID, state.SupervisorStart) {
			state.Status = RuntimeStateUnknown
			state.Diagnostic = "direct runtime supervisor disappeared before recording a final process result"
		}
	}
	if err := verifyDirectStoredSpec(directSpecPath(runDirectory), state.SpecSHA256, state.StateSHA256, runtime.nodeID, operationID); err != nil {
		return directSupervisorState{}, err
	}
	if state.Status == RuntimeStateStarting && state.SupervisorPID == 0 {
		if info, statErr := os.Stat(filepath.Join(runDirectory, "state.json")); statErr == nil && time.Since(info.ModTime()) > runtime.startupTimeout {
			state.Status = RuntimeStateUnknown
			state.Diagnostic = "direct runtime launch intent has no acknowledged supervisor identity"
		}
	}
	return state, nil
}

func directStatus(state directSupervisorState) RuntimeStatus {
	return RuntimeStatus{
		State: state.Status, ExitCode: state.ExitCode, ExitKnown: state.ExitKnown,
		CompletionReady: state.Status == RuntimeStateExited && state.ExitKnown,
		Forced:          state.Forced, Diagnostic: state.Diagnostic,
	}
}

func directStateHasStableCaptures(status string) bool {
	switch status {
	case RuntimeStateExited, RuntimeStateStopped, RuntimeStateTimedOut:
		return true
	default:
		return false
	}
}

func (runtime *DirectRuntime) Stop(ctx context.Context, operationID, handle string, spec StopSpec) (StopResult, error) {
	if err := validateDirectHandle(runtime.nodeID, operationID, handle); err != nil {
		return StopResult{}, err
	}
	runDirectory, err := runtime.runDirectory(operationID)
	if err != nil {
		return StopResult{}, err
	}
	state, err := readDirectState(runDirectory)
	if err != nil {
		return StopResult{}, err
	}
	if state.NodeID != runtime.nodeID || state.OperationID != operationID {
		return StopResult{}, errors.New("direct runtime state identity is invalid for stop")
	}
	if err := verifyDirectStoredSpec(directSpecPath(runDirectory), state.SpecSHA256, state.StateSHA256, runtime.nodeID, operationID); err != nil {
		return StopResult{}, err
	}
	if state.Status == RuntimeStateStopped || state.Status == RuntimeStateExited || state.Status == RuntimeStateTimedOut {
		return StopResult{Forced: state.Forced, Diagnostic: state.Diagnostic}, nil
	}
	if state.SupervisorPID <= 0 || !sameProcess(state.SupervisorPID, state.SupervisorStart) {
		time.Sleep(directPollInterval)
		refreshed, refreshErr := readDirectState(runDirectory)
		if refreshErr == nil && refreshed.NodeID == runtime.nodeID && refreshed.OperationID == operationID {
			if specErr := verifyDirectStoredSpec(directSpecPath(runDirectory), refreshed.SpecSHA256, refreshed.StateSHA256, runtime.nodeID, operationID); specErr != nil {
				return StopResult{}, specErr
			}
			if refreshed.Status == RuntimeStateStopped || refreshed.Status == RuntimeStateExited || refreshed.Status == RuntimeStateTimedOut {
				return StopResult{Forced: refreshed.Forced, Diagnostic: refreshed.Diagnostic}, nil
			}
		}
		return StopResult{}, &OutcomeUnknownError{Message: "direct runtime supervisor identity cannot be trusted for stop"}
	}
	grace := spec.GracePeriod
	if grace <= 0 {
		grace = directDefaultGracePeriod
	}
	if grace > directMaximumStopGrace {
		grace = directMaximumStopGrace
	}
	request := directStopRequest{Schema: directStopRequestSchema, GraceMillis: grace.Milliseconds()}
	if err := writeJSONAtomic(filepath.Join(runDirectory, "stop.json"), request); err != nil {
		return StopResult{}, fmt.Errorf("record direct runtime stop request: %w", err)
	}
	if err := syscall.Kill(state.SupervisorPID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return StopResult{}, fmt.Errorf("signal direct runtime supervisor: %w", err)
	}

	deadline := time.NewTimer(grace + 2*time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(directPollInterval)
	defer ticker.Stop()
	for {
		current, stateErr := readDirectState(runDirectory)
		if stateErr == nil && (current.NodeID != runtime.nodeID || current.OperationID != operationID) {
			return StopResult{}, errors.New("direct runtime state identity changed while stopping")
		}
		if stateErr == nil && (current.Status == RuntimeStateStopped || current.Status == RuntimeStateExited || current.Status == RuntimeStateTimedOut) {
			if specErr := verifyDirectStoredSpec(directSpecPath(runDirectory), current.SpecSHA256, current.StateSHA256, runtime.nodeID, operationID); specErr != nil {
				return StopResult{}, specErr
			}
			return StopResult{Forced: current.Forced, Diagnostic: current.Diagnostic}, nil
		}
		if stateErr == nil && current.Status == RuntimeStateUnknown {
			return StopResult{}, &OutcomeUnknownError{Message: current.Diagnostic}
		}
		select {
		case <-ctx.Done():
			return StopResult{}, ctx.Err()
		case <-deadline.C:
			return StopResult{}, &OutcomeUnknownError{Message: "direct runtime stop was not acknowledged; process outcome is unknown"}
		case <-ticker.C:
		}
	}
}

func (runtime *DirectRuntime) Logs(ctx context.Context, operationID, handle string, tail int) (domain.RunLogs, error) {
	_ = ctx
	state, err := runtime.inspectState(operationID, handle)
	if err != nil {
		return domain.RunLogs{}, err
	}
	runDirectory, err := runtime.runDirectory(operationID)
	if err != nil {
		return domain.RunLogs{}, err
	}
	spec, err := readVerifiedDirectStoredSpec(directSpecPath(runDirectory), state.SpecSHA256, state.StateSHA256, runtime.nodeID, operationID)
	if err != nil {
		return domain.RunLogs{}, err
	}
	if state.StdoutCapturedBytes > spec.OutputByteLimit || state.StderrCapturedBytes > spec.OutputByteLimit {
		return domain.RunLogs{}, errors.New("direct runtime output capture exceeds its sealed effective byte limit")
	}
	stdout, err := readDirectCapture(filepath.Join(runDirectory, "stdout.log"), state.StdoutCapturedBytes, state.StdoutOmittedBytes, state.StdoutSHA256, tail, spec.OutputByteLimit, directStateHasStableCaptures(state.Status))
	if err != nil {
		return domain.RunLogs{}, err
	}
	stderr, err := readDirectCapture(filepath.Join(runDirectory, "stderr.log"), state.StderrCapturedBytes, state.StderrOmittedBytes, state.StderrSHA256, tail, spec.OutputByteLimit, directStateHasStableCaptures(state.Status))
	if err != nil {
		return domain.RunLogs{}, err
	}
	stdout = redactAndBoundDirectCapture(stdout, tail, spec.OutputByteLimit)
	stderr = redactAndBoundDirectCapture(stderr, tail, spec.OutputByteLimit)
	return domain.RunLogs{RunID: operationID, State: state.Status, Stdout: stdout, Stderr: stderr}, nil
}

func redactAndBoundDirectCapture(capture domain.CapturedLog, tail int, byteLimit int64) domain.CapturedLog {
	text := strings.ToValidUTF8(capture.Text, "\uFFFD")
	text = tailText(redactDirectOutput(text), tail)
	if byteLimit <= 0 || byteLimit > directMaximumOutputLimit {
		byteLimit = directMaximumOutputLimit
	}
	if int64(len(text)) > byteLimit {
		originalLength := len(text)
		limit := int(byteLimit)
		for limit > 0 && !utf8.ValidString(text[:limit]) {
			limit--
		}
		text = text[:limit]
		responseOmitted := int64(originalLength - limit)
		const maximumInt64 = int64(^uint64(0) >> 1)
		if capture.OmittedBytes > maximumInt64-responseOmitted {
			capture.OmittedBytes = maximumInt64
		} else {
			capture.OmittedBytes += responseOmitted
		}
		capture.Truncated = true
	}
	capture.Text = text
	return capture
}

func (runtime *DirectRuntime) prepareEffectiveSpec(operationID string, placement domain.RunPlacement, launch LaunchSpec) (directSupervisorSpec, error) {
	if err := runtime.validate(); err != nil {
		return directSupervisorSpec{}, err
	}
	if !directOperationPattern.MatchString(operationID) {
		return directSupervisorSpec{}, &StartError{Message: "direct runtime operation ID is invalid"}
	}
	if launch.Command == nil {
		return directSupervisorSpec{}, &StartError{Message: "direct runtime requires a command specification"}
	}
	workingDirectory, err := resolveDirectWorkingDirectory(placement.CheckoutPath)
	if err != nil {
		return directSupervisorSpec{}, &StartError{Message: err.Error()}
	}
	executable, err := filepath.Abs(strings.TrimSpace(launch.Command.Executable))
	if err != nil || !filepath.IsAbs(executable) {
		return directSupervisorSpec{}, &StartError{Message: "direct runtime executable must resolve to an absolute path"}
	}
	environment, err := buildDirectEnvironment(runtime.inheritedEnvironment, launch.Command.Environment, operationID, runtime.operationIDEnvironmentVariable)
	if err != nil {
		return directSupervisorSpec{}, &StartError{Message: err.Error()}
	}
	outputLimit := launch.Command.OutputByteLimit
	if outputLimit <= 0 || outputLimit > runtime.outputLimit {
		outputLimit = runtime.outputLimit
	}
	spec := directSupervisorSpec{
		Schema:             directSupervisorSpecSchema,
		NodeID:             runtime.nodeID,
		OperationID:        operationID,
		Executable:         executable,
		Arguments:          append([]string{}, launch.Command.Arguments...),
		StandardInput:      append([]byte{}, launch.Command.StandardInput...),
		Environment:        environment,
		WorkingDirectory:   workingDirectory,
		OutputByteLimit:    outputLimit,
		TimeoutMillis:      launch.Command.Timeout.Milliseconds(),
		DefaultGraceMillis: directDefaultGracePeriod.Milliseconds(),
	}
	if spec.TimeoutMillis < 0 {
		return directSupervisorSpec{}, &StartError{Message: "direct runtime timeout cannot be negative"}
	}
	if err := sealDirectSpec(&spec); err != nil {
		return directSupervisorSpec{}, &StartError{Message: err.Error()}
	}
	return spec, nil
}

func (runtime *DirectRuntime) validate() error {
	if !validNodeID(runtime.nodeID) || strings.TrimSpace(runtime.root) == "" || strings.TrimSpace(runtime.supervisorExecutable) == "" {
		return &StartError{Message: "direct runtime node identity, state root, and supervisor executable are required"}
	}
	return validateDirectOperationIDEnvironmentVariable(runtime.operationIDEnvironmentVariable)
}

func (runtime *DirectRuntime) prepareRunDirectory(operationID string) (string, error) {
	root, err := filepath.Abs(runtime.root)
	if err != nil {
		return "", fmt.Errorf("resolve direct runtime state root: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create direct runtime state root: %w", err)
	}
	if err := rejectSymlink(root); err != nil {
		return "", err
	}
	runDirectory := filepath.Join(root, operationID)
	if err := os.Mkdir(runDirectory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create direct runtime operation directory: %w", err)
	}
	if err := rejectSymlink(runDirectory); err != nil {
		return "", err
	}
	return runDirectory, nil
}

func (runtime *DirectRuntime) runDirectory(operationID string) (string, error) {
	if !directOperationPattern.MatchString(operationID) {
		return "", errors.New("direct runtime operation ID is invalid")
	}
	root, err := filepath.Abs(runtime.root)
	if err != nil {
		return "", err
	}
	runDirectory := filepath.Join(root, operationID)
	if err := rejectSymlink(runDirectory); err != nil {
		return "", err
	}
	return runDirectory, nil
}

type directSupervisorSpec struct {
	Schema             string   `json:"schema"`
	NodeID             string   `json:"node_id"`
	OperationID        string   `json:"operation_id"`
	Executable         string   `json:"executable"`
	Arguments          []string `json:"arguments"`
	StandardInput      []byte   `json:"standard_input"`
	Environment        []string `json:"environment"`
	WorkingDirectory   string   `json:"working_directory"`
	OutputByteLimit    int64    `json:"output_byte_limit"`
	TimeoutMillis      int64    `json:"timeout_millis"`
	DefaultGraceMillis int64    `json:"default_grace_millis"`
	SpecSHA256         string   `json:"spec_sha256,omitempty"`
}

type directSupervisorState struct {
	Schema              string `json:"schema"`
	NodeID              string `json:"node_id"`
	OperationID         string `json:"operation_id"`
	Status              string `json:"status"`
	SupervisorPID       int    `json:"supervisor_pid,omitempty"`
	SupervisorStart     string `json:"supervisor_start,omitempty"`
	ChildPID            int    `json:"child_pid,omitempty"`
	ChildStart          string `json:"child_start,omitempty"`
	ExitCode            int    `json:"exit_code,omitempty"`
	ExitKnown           bool   `json:"exit_known,omitempty"`
	Forced              bool   `json:"forced,omitempty"`
	Diagnostic          string `json:"diagnostic,omitempty"`
	StdoutCapturedBytes int64  `json:"stdout_captured_bytes,omitempty"`
	StdoutOmittedBytes  int64  `json:"stdout_omitted_bytes,omitempty"`
	StdoutSHA256        string `json:"stdout_sha256,omitempty"`
	StderrCapturedBytes int64  `json:"stderr_captured_bytes,omitempty"`
	StderrOmittedBytes  int64  `json:"stderr_omitted_bytes,omitempty"`
	StderrSHA256        string `json:"stderr_sha256,omitempty"`
	SpecSHA256          string `json:"spec_sha256,omitempty"`
	StateSHA256         string `json:"state_sha256,omitempty"`
}

type directStopRequest struct {
	Schema      string `json:"schema"`
	GraceMillis int64  `json:"grace_millis"`
}

func directHandle(nodeID, operationID string) string { return "direct:" + nodeID + ":" + operationID }

func validateDirectHandle(nodeID, operationID, handle string) error {
	if !validNodeID(nodeID) || !directOperationPattern.MatchString(operationID) || handle != directHandle(nodeID, operationID) {
		return errors.New("direct runtime handle does not match the operation")
	}
	return nil
}

func resolveDirectWorkingDirectory(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("direct runtime requires an assigned checkout directory")
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve assigned checkout directory: %w", err)
	}
	return resolved, nil
}

func validateDirectWorkingDirectoryAvailable(resolved string) error {
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return errors.New("assigned checkout directory is unavailable")
	}
	return nil
}

func validateDirectWorkingDirectory(path string) (string, error) {
	resolved, err := resolveDirectWorkingDirectory(path)
	if err != nil {
		return "", err
	}
	if err := validateDirectWorkingDirectoryAvailable(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// openDirectWorkingDirectory resolves no part of the launch directory through
// a symbolic link. The descriptor pins the selected directory across renames
// and is passed to the supervisor so child startup never resolves the mutable
// pathname after this effect-boundary check.
func openDirectWorkingDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("direct runtime working directory must be an absolute clean path")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root for direct runtime working directory: %w", err)
	}
	for _, component := range strings.Split(strings.Trim(path, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("open direct runtime working directory without following symbolic links: %w", openErr)
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("adopt direct runtime working directory descriptor")
	}
	return file, nil
}

func validateDirectOperationIDEnvironmentVariable(name string) error {
	switch name {
	case DirectRunIDEnvironmentVariable, DirectCheckRunIDEnvironmentVariable:
		return nil
	default:
		return errors.New("direct runtime operation ID environment variable is invalid")
	}
}

func buildDirectEnvironment(inherited []string, overrides map[string]string, operationID string, operationIDEnvironmentVariable ...string) ([]string, error) {
	identityName := DirectRunIDEnvironmentVariable
	if len(operationIDEnvironmentVariable) > 0 {
		identityName = operationIDEnvironmentVariable[0]
	}
	if err := validateDirectOperationIDEnvironmentVariable(identityName); err != nil {
		return nil, err
	}
	inheritedAllowed := make(map[string]struct{}, len(directInheritedEnvironmentAllowlist))
	for _, name := range directInheritedEnvironmentAllowlist {
		inheritedAllowed[name] = struct{}{}
	}
	overrideAllowed := make(map[string]struct{}, len(directInheritedEnvironmentAllowlist)+len(directProviderEnvironmentAllowlist))
	for name := range inheritedAllowed {
		overrideAllowed[name] = struct{}{}
	}
	for _, name := range directProviderEnvironmentAllowlist {
		overrideAllowed[name] = struct{}{}
	}
	values := make(map[string]string, len(overrideAllowed)+1)
	for _, entry := range inherited {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if directEnvironmentNameAllowed(inheritedAllowed, name) {
			values[name] = value
		}
	}
	for name, value := range overrides {
		if !directEnvironmentNameAllowed(overrideAllowed, name) {
			return nil, fmt.Errorf("direct runtime environment variable %q is not allowlisted", name)
		}
		if strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("direct runtime environment variable %q contains a NUL byte", name)
		}
		values[name] = value
	}
	values[identityName] = operationID
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result, nil
}

func directEnvironmentNameAllowed(exact map[string]struct{}, name string) bool {
	_, allowed := exact[name]
	return allowed || strings.HasPrefix(name, "LC_")
}

func directSpecPath(runDirectory string) string { return filepath.Join(runDirectory, "launch.json") }

func writeDirectSpec(runDirectory string, spec directSupervisorSpec) error {
	if err := sealDirectSpec(&spec); err != nil {
		return err
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode direct runtime launch specification: %w", err)
	}
	if err := publishPrivateFileNoReplace(runDirectory, filepath.Base(directSpecPath(runDirectory)), data, nil); errors.Is(err, errAtomicPrivateFileExists) {
		return errDirectSpecExists
	} else if err != nil {
		return fmt.Errorf("publish direct runtime launch specification: %w", err)
	}
	return nil
}

func readDirectSpec(path string) (directSupervisorSpec, error) {
	data, err := readBoundedRegularFile(path, directMaximumSpecBytes)
	if err != nil {
		return directSupervisorSpec{}, err
	}
	if int64(len(data)) > directMaximumSpecBytes {
		return directSupervisorSpec{}, errors.New("direct supervisor specification exceeds 256 KiB")
	}
	var spec directSupervisorSpec
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return directSupervisorSpec{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return directSupervisorSpec{}, err
	}
	if spec.Schema != directSupervisorSpecSchema || !validNodeID(spec.NodeID) || !directOperationPattern.MatchString(spec.OperationID) || !filepath.IsAbs(spec.Executable) || !filepath.IsAbs(spec.WorkingDirectory) || spec.OutputByteLimit <= 0 || spec.OutputByteLimit > directMaximumOutputLimit || spec.TimeoutMillis < 0 || spec.DefaultGraceMillis <= 0 {
		return directSupervisorSpec{}, errors.New("direct supervisor specification is invalid")
	}
	if !directSpecSHA256Pattern.MatchString(spec.SpecSHA256) {
		return directSupervisorSpec{}, errors.New("direct supervisor specification digest is invalid")
	}
	digest, err := directSpecDigest(spec)
	if err != nil {
		return directSupervisorSpec{}, err
	}
	if digest != spec.SpecSHA256 {
		return directSupervisorSpec{}, errors.New("direct supervisor specification digest does not match its canonical contents")
	}
	return spec, nil
}

func sealDirectSpec(spec *directSupervisorSpec) error {
	digest, err := directSpecDigest(*spec)
	if err != nil {
		return fmt.Errorf("digest direct runtime launch specification: %w", err)
	}
	spec.SpecSHA256 = digest
	return nil
}

func directSpecDigest(spec directSupervisorSpec) (string, error) {
	spec.SpecSHA256 = ""
	if spec.Arguments == nil {
		spec.Arguments = []string{}
	}
	if spec.StandardInput == nil {
		spec.StandardInput = []byte{}
	}
	if spec.Environment == nil {
		spec.Environment = []string{}
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:]), nil
}

func verifyDirectReplay(path, stateDigest, stateSeal string, desired directSupervisorSpec) error {
	stored, err := readDirectSpec(path)
	if err != nil {
		return fmt.Errorf("read existing direct runtime launch specification: %w", err)
	}
	if stored.NodeID != desired.NodeID || stored.OperationID != desired.OperationID {
		return errors.New("existing direct runtime launch specification identity is invalid")
	}
	storedDigest, err := directSpecDigest(stored)
	if err != nil {
		return fmt.Errorf("digest existing direct runtime launch specification: %w", err)
	}
	if stateSeal == "" {
		return errors.New("existing direct runtime state is missing its persisted state digest")
	}
	if !directSpecSHA256Pattern.MatchString(stateDigest) || stateDigest != stored.SpecSHA256 || stateDigest != storedDigest {
		return errors.New("existing direct runtime state does not match its launch specification digest")
	}
	desiredDigest, err := directSpecDigest(desired)
	if err != nil {
		return fmt.Errorf("digest requested direct runtime launch specification: %w", err)
	}
	if storedDigest != desiredDigest {
		return errors.New("direct runtime operation ID already belongs to a different launch specification")
	}
	return nil
}

func verifyDirectPendingReplay(path string, desired directSupervisorSpec) error {
	stored, err := readDirectSpec(path)
	if err != nil {
		return fmt.Errorf("read existing direct runtime launch specification: %w", err)
	}
	if stored.NodeID != desired.NodeID || stored.OperationID != desired.OperationID {
		return errors.New("existing direct runtime launch specification identity is invalid")
	}
	storedDigest, err := directSpecDigest(stored)
	if err != nil {
		return fmt.Errorf("digest existing direct runtime launch specification: %w", err)
	}
	desiredDigest, err := directSpecDigest(desired)
	if err != nil {
		return fmt.Errorf("digest requested direct runtime launch specification: %w", err)
	}
	if storedDigest != desiredDigest {
		return errors.New("direct runtime operation ID already belongs to a different launch specification")
	}
	return nil
}

func verifyDirectStoredSpec(path, stateDigest, stateSeal, nodeID, operationID string) error {
	_, err := readVerifiedDirectStoredSpec(path, stateDigest, stateSeal, nodeID, operationID)
	return err
}

func readVerifiedDirectStoredSpec(path, stateDigest, stateSeal, nodeID, operationID string) (directSupervisorSpec, error) {
	stored, err := readDirectSpec(path)
	if err != nil {
		return directSupervisorSpec{}, fmt.Errorf("read direct runtime launch specification: %w", err)
	}
	if stored.NodeID != nodeID || stored.OperationID != operationID {
		return directSupervisorSpec{}, errors.New("direct runtime launch specification identity does not match its state")
	}
	if stateSeal == "" {
		return directSupervisorSpec{}, errors.New("direct runtime state is missing its persisted state digest")
	}
	digest, err := directSpecDigest(stored)
	if err != nil {
		return directSupervisorSpec{}, fmt.Errorf("digest direct runtime launch specification: %w", err)
	}
	if !directSpecSHA256Pattern.MatchString(stateDigest) || stateDigest != stored.SpecSHA256 || stateDigest != digest {
		return directSupervisorSpec{}, errors.New("direct runtime state does not match its launch specification digest")
	}
	return stored, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains trailing content")
		}
		return err
	}
	return nil
}

func readDirectState(runDirectory string) (directSupervisorState, error) {
	data, err := readBoundedRegularFile(filepath.Join(runDirectory, "state.json"), directMaximumStateBytes)
	if err != nil {
		return directSupervisorState{}, err
	}
	if int64(len(data)) > directMaximumStateBytes {
		return directSupervisorState{}, errors.New("direct runtime state exceeds 64 KiB")
	}
	var state directSupervisorState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return directSupervisorState{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return directSupervisorState{}, err
	}
	if err := validateDirectState(state); err != nil {
		return directSupervisorState{}, err
	}
	digest, err := directStateDigest(state)
	if err != nil {
		return directSupervisorState{}, err
	}
	if digest != state.StateSHA256 {
		return directSupervisorState{}, errors.New("direct runtime state digest does not match its canonical contents")
	}
	return state, nil
}

func validateDirectState(state directSupervisorState) error {
	if state.Schema != directSupervisorStateSchema || !validNodeID(state.NodeID) || !directOperationPattern.MatchString(state.OperationID) {
		return errors.New("direct runtime state identity is invalid")
	}
	switch state.Status {
	case RuntimeStateStarting, RuntimeStateRunning, RuntimeStateExited, RuntimeStateStopped, RuntimeStateTimedOut, RuntimeStateUnknown, directStateStartFailed:
	default:
		return errors.New("direct runtime state status is invalid")
	}
	if (state.Status == RuntimeStateExited || state.Status == RuntimeStateStopped || state.Status == RuntimeStateTimedOut) && !state.ExitKnown {
		return errors.New("direct runtime terminal state is missing a known process result")
	}
	if state.Status == RuntimeStateRunning && (state.SupervisorPID <= 0 || state.SupervisorStart == "") {
		return errors.New("direct runtime running state is missing a supervisor identity")
	}
	if state.Status == RuntimeStateStarting || state.Status == directStateStartFailed {
		if state.ChildPID != 0 || state.ChildStart != "" || state.ExitKnown || state.Forced || state.StdoutCapturedBytes != 0 || state.StdoutOmittedBytes != 0 || state.StderrCapturedBytes != 0 || state.StderrOmittedBytes != 0 {
			return errors.New("direct runtime pre-child state contains an impossible process result")
		}
	}
	if (state.Status == directStateStartFailed || state.Status == RuntimeStateUnknown) && strings.TrimSpace(state.Diagnostic) == "" {
		return errors.New("direct runtime failure state is missing a diagnostic")
	}
	if state.SupervisorPID < 0 || state.ChildPID < 0 || state.StdoutCapturedBytes < 0 || state.StdoutOmittedBytes < 0 || state.StderrCapturedBytes < 0 || state.StderrOmittedBytes < 0 || state.StdoutCapturedBytes > directMaximumOutputLimit || state.StderrCapturedBytes > directMaximumOutputLimit {
		return errors.New("direct runtime state counters are invalid")
	}
	if !directSpecSHA256Pattern.MatchString(state.SpecSHA256) {
		return errors.New("direct runtime state launch specification digest is invalid")
	}
	if !directSpecSHA256Pattern.MatchString(state.StateSHA256) {
		return errors.New("direct runtime state digest is invalid")
	}
	if state.StdoutSHA256 != "" && !directSpecSHA256Pattern.MatchString(state.StdoutSHA256) || state.StderrSHA256 != "" && !directSpecSHA256Pattern.MatchString(state.StderrSHA256) {
		return errors.New("direct runtime output digest is invalid")
	}
	if directStateHasStableCaptures(state.Status) && (state.StdoutSHA256 == "" || state.StderrSHA256 == "") {
		return errors.New("direct runtime terminal state is missing output digests")
	}
	if len(state.Diagnostic) > directMaximumDiagnosticBytes || strings.ContainsRune(state.Diagnostic, '\x00') {
		return errors.New("direct runtime state diagnostic is invalid")
	}
	return nil
}

func readBoundedRegularFile(path string, byteLimit int64) ([]byte, error) {
	return readPrivateAtomicFile(path, byteLimit)
}

func writeDirectState(runDirectory string, state directSupervisorState) error {
	digest, err := directStateDigest(state)
	if err != nil {
		return fmt.Errorf("digest direct runtime state: %w", err)
	}
	state.StateSHA256 = digest
	return writeJSONAtomic(filepath.Join(runDirectory, "state.json"), state)
}

func directStateDigest(state directSupervisorState) (string, error) {
	state.StateSHA256 = ""
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:]), nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return replacePrivateFileAtomic(path, data, nil)
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("direct runtime path must be a real directory: %s", path)
	}
	return nil
}

func processStartIdentity(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	closing := bytes.LastIndexByte(data, ')')
	if closing < 0 {
		return ""
	}
	fields := strings.Fields(string(data[closing+1:]))
	if len(fields) <= 19 {
		return ""
	}
	return fields[19]
}

func sameProcess(pid int, startIdentity string) bool {
	if pid <= 0 || startIdentity == "" {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	return processStartIdentity(pid) == startIdentity
}

func readDirectCapture(path string, recordedCaptured, recordedOmitted int64, recordedSHA256 string, tail int, byteLimit int64, stable bool) (domain.CapturedLog, error) {
	if byteLimit <= 0 || byteLimit > directMaximumOutputLimit {
		byteLimit = directMaximumOutputLimit
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		if recordedCaptured > 0 {
			return domain.CapturedLog{}, errors.New("direct runtime output capture is missing recorded bytes")
		}
		return domain.CapturedLog{CapturedBytes: recordedCaptured, OmittedBytes: recordedOmitted, Truncated: recordedOmitted > 0}, nil
	}
	if err != nil {
		return domain.CapturedLog{}, fmt.Errorf("read direct runtime output: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return domain.CapturedLog{}, fmt.Errorf("inspect direct runtime output: %w", err)
	}
	if !info.Mode().IsRegular() {
		return domain.CapturedLog{}, errors.New("direct runtime output capture must be a regular file")
	}
	if info.Size() < recordedCaptured {
		return domain.CapturedLog{}, errors.New("direct runtime output capture is shorter than its recorded byte count")
	}
	if stable && info.Size() != recordedCaptured {
		return domain.CapturedLog{}, errors.New("direct runtime terminal output capture does not match its recorded byte count")
	}
	data, err := io.ReadAll(io.LimitReader(file, byteLimit+1))
	if err != nil {
		return domain.CapturedLog{}, fmt.Errorf("read direct runtime output: %w", err)
	}
	if stable && recordedSHA256 != "" {
		digest := sha256.Sum256(data)
		if fmt.Sprintf("%x", digest[:]) != recordedSHA256 {
			return domain.CapturedLog{}, errors.New("direct runtime terminal output capture digest does not match its recorded contents")
		}
	}
	responseOmitted := int64(0)
	if int64(len(data)) > byteLimit {
		data = data[:byteLimit]
		responseOmitted = 1
		if info.Size() > byteLimit {
			responseOmitted = info.Size() - byteLimit
		}
	}
	captured := int64(len(data))
	if recordedCaptured > captured {
		captured = recordedCaptured
	}
	omitted := recordedOmitted
	if responseOmitted > 0 {
		const maximumInt64 = int64(^uint64(0) >> 1)
		if omitted > maximumInt64-responseOmitted {
			omitted = maximumInt64
		} else {
			omitted += responseOmitted
		}
	}
	return domain.CapturedLog{Text: string(data), CapturedBytes: captured, OmittedBytes: omitted, Truncated: omitted > 0}, nil
}

func tailText(value string, lines int) string {
	if lines <= 0 {
		return value
	}
	parts := strings.Split(value, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n") + "\n"
}

func redactDirectOutput(value string) string {
	return RedactTerminalOutput(value)
}

// RedactTerminalOutput removes the fixed credential-shaped values shared by
// execution, check, and managed-service owner log surfaces.
func RedactTerminalOutput(value string) string {
	return secretAssignment.ReplaceAllString(sanitizeTerminalControl(value), "${1}[REDACTED]")
}

// sanitizeTerminalControl preserves readable lines while removing control
// sequences that could act on a terminal or change the visual ordering of
// browser text. Invalid UTF-8 remains visible as the replacement rune.
func sanitizeTerminalControl(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	const (
		terminalText = iota
		terminalEscape
		terminalCSI
		terminalOSC
		terminalOSCEscape
	)
	state := terminalText
	var builder strings.Builder
	builder.Grow(len(value))
	for _, current := range value {
		switch state {
		case terminalEscape:
			switch current {
			case '[':
				state = terminalCSI
			case ']':
				state = terminalOSC
			default:
				state = terminalText
			}
			continue
		case terminalCSI:
			if current >= 0x40 && current <= 0x7e {
				state = terminalText
			}
			continue
		case terminalOSC:
			switch current {
			case 0x07, 0x9c:
				state = terminalText
			case 0x1b:
				state = terminalOSCEscape
			}
			continue
		case terminalOSCEscape:
			if current == '\\' {
				state = terminalText
			} else {
				state = terminalOSC
			}
			continue
		}
		switch current {
		case 0x1b:
			state = terminalEscape
			continue
		case 0x9b:
			state = terminalCSI
			continue
		case 0x9d:
			state = terminalOSC
			continue
		case '\n', '\t':
			builder.WriteRune(current)
			continue
		case '\r':
			builder.WriteByte('\n')
			continue
		}
		if current <= 0x1f || current == 0x7f || current >= 0x80 && current <= 0x9f || terminalBidiControl(current) {
			continue
		}
		builder.WriteRune(current)
	}
	return builder.String()
}

func terminalBidiControl(current rune) bool {
	switch current {
	case 0x061c, 0x200e, 0x200f, 0x2028, 0x2029, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e,
		0x2066, 0x2067, 0x2068, 0x2069, 0x206a, 0x206b, 0x206c, 0x206d, 0x206e, 0x206f:
		return true
	default:
		return false
	}
}

// RunDirectSupervisor is the hidden process entry point used by DirectRuntime.
func RunDirectSupervisor(arguments []string) int {
	if len(arguments) < 1 || len(arguments) > 2 {
		return 2
	}
	specPath, err := filepath.Abs(arguments[0])
	if err != nil {
		return 2
	}
	if info, err := os.Lstat(specPath); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return 2
	}
	spec, err := readDirectSpec(specPath)
	if err != nil {
		return 2
	}
	runDirectory := filepath.Dir(specPath)
	state := directSupervisorState{
		Schema: directSupervisorStateSchema, NodeID: spec.NodeID, OperationID: spec.OperationID,
		Status: RuntimeStateStarting, SupervisorPID: os.Getpid(), SupervisorStart: processStartIdentity(os.Getpid()), SpecSHA256: spec.SpecSHA256,
	}
	_ = writeDirectState(runDirectory, state)
	workingDirectoryFile, err := directSupervisorWorkingDirectory(arguments, spec.WorkingDirectory)
	if err != nil {
		state.Status, state.Diagnostic = directStateStartFailed, "open pinned direct runtime working directory: "+err.Error()
		_ = writeDirectState(runDirectory, state)
		return 1
	}
	if err := unix.Fchdir(int(workingDirectoryFile.Fd())); err != nil {
		_ = workingDirectoryFile.Close()
		state.Status, state.Diagnostic = directStateStartFailed, "enter pinned direct runtime working directory: "+err.Error()
		_ = writeDirectState(runDirectory, state)
		return 1
	}
	if err := workingDirectoryFile.Close(); err != nil {
		state.Status, state.Diagnostic = directStateStartFailed, "close pinned direct runtime working directory: "+err.Error()
		_ = writeDirectState(runDirectory, state)
		return 1
	}

	stdoutFile, err := openDirectCaptureFile(filepath.Join(runDirectory, "stdout.log"))
	if err != nil {
		state.Status, state.Diagnostic = directStateStartFailed, "open bounded stdout capture: "+err.Error()
		_ = writeDirectState(runDirectory, state)
		return 1
	}
	defer stdoutFile.Close()
	stderrFile, err := openDirectCaptureFile(filepath.Join(runDirectory, "stderr.log"))
	if err != nil {
		state.Status, state.Diagnostic = directStateStartFailed, "open bounded stderr capture: "+err.Error()
		_ = writeDirectState(runDirectory, state)
		return 1
	}
	defer stderrFile.Close()

	command := exec.Command(spec.Executable, spec.Arguments...)
	command.Env = append([]string(nil), spec.Environment...)
	command.Stdin = bytes.NewReader(spec.StandardInput)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdoutHash, stderrHash := sha256.New(), sha256.New()
	stdoutCapture := &boundedCapture{writer: io.MultiWriter(stdoutFile, stdoutHash), limit: spec.OutputByteLimit}
	stderrCapture := &boundedCapture{writer: io.MultiWriter(stderrFile, stderrHash), limit: spec.OutputByteLimit}
	command.Stdout = stdoutCapture
	command.Stderr = stderrCapture

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := command.Start(); err != nil {
		state.Status, state.Diagnostic = directStateStartFailed, "start direct child process: "+err.Error()
		_ = writeDirectState(runDirectory, state)
		return 1
	}
	state.Status, state.ChildPID, state.ChildStart = RuntimeStateRunning, command.Process.Pid, processStartIdentity(command.Process.Pid)
	_ = writeDirectState(runDirectory, state)

	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()

	var timeout <-chan time.Time
	var timeoutTimer *time.Timer
	if spec.TimeoutMillis > 0 {
		timeoutTimer = time.NewTimer(time.Duration(spec.TimeoutMillis) * time.Millisecond)
		timeout = timeoutTimer.C
		defer timeoutTimer.Stop()
	}

	finalStatus, diagnostic, forced := RuntimeStateExited, "process exited", false
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-signals:
		grace := time.Duration(spec.DefaultGraceMillis) * time.Millisecond
		if request, requestErr := readDirectStopRequest(filepath.Join(runDirectory, "stop.json")); requestErr == nil && request.GraceMillis > 0 {
			grace = time.Duration(request.GraceMillis) * time.Millisecond
		}
		var trusted bool
		waitErr, forced, trusted = terminateDirectChild(command.Process.Pid, state.ChildStart, waited, grace)
		finalStatus, diagnostic = RuntimeStateStopped, "process stopped after graceful request"
		if !trusted {
			finalStatus, diagnostic = RuntimeStateUnknown, "direct child identity changed before stop could be proven"
		} else if forced {
			diagnostic = "process ignored graceful stop and was force-killed"
		}
	case <-timeout:
		var trusted bool
		waitErr, forced, trusted = terminateDirectChild(command.Process.Pid, state.ChildStart, waited, directDefaultGracePeriod)
		finalStatus, diagnostic = RuntimeStateTimedOut, "process exceeded its direct runtime timeout"
		if !trusted {
			finalStatus, diagnostic = RuntimeStateUnknown, "direct child identity changed before timeout termination could be proven"
		}
	}
	_ = stdoutFile.Sync()
	_ = stderrFile.Sync()
	state.Status, state.Forced, state.Diagnostic = finalStatus, forced, diagnostic
	state.StdoutCapturedBytes, state.StdoutOmittedBytes = stdoutCapture.captured, stdoutCapture.omitted
	state.StderrCapturedBytes, state.StderrOmittedBytes = stderrCapture.captured, stderrCapture.omitted
	state.StdoutSHA256, state.StderrSHA256 = fmt.Sprintf("%x", stdoutHash.Sum(nil)), fmt.Sprintf("%x", stderrHash.Sum(nil))
	if finalStatus != RuntimeStateUnknown {
		state.ExitCode, state.ExitKnown = directExitCode(command, waitErr)
	}
	_ = writeDirectState(runDirectory, state)
	return 0
}

func directSupervisorWorkingDirectory(arguments []string, path string) (*os.File, error) {
	if len(arguments) == 1 {
		return openDirectWorkingDirectory(path)
	}
	if arguments[1] != strconv.Itoa(directSupervisorWorkingDirFD) {
		return nil, errors.New("direct supervisor working directory descriptor is invalid")
	}
	file := os.NewFile(uintptr(directSupervisorWorkingDirFD), path)
	if file == nil {
		return nil, errors.New("direct supervisor working directory descriptor is unavailable")
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("direct supervisor working directory descriptor is not a directory")
	}
	return file, nil
}

func openDirectCaptureFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("direct runtime output capture must be a regular file")
	}
	return file, nil
}

type boundedCapture struct {
	writer   io.Writer
	limit    int64
	captured int64
	omitted  int64
}

func (capture *boundedCapture) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := capture.limit - capture.captured
	if remaining > 0 {
		writeLength := int64(len(data))
		if writeLength > remaining {
			writeLength = remaining
		}
		written, err := capture.writer.Write(data[:writeLength])
		capture.captured += int64(written)
		if err != nil {
			return written, err
		}
	}
	capture.omitted += int64(originalLength) - minInt64(int64(originalLength), remainingNonnegative(remaining))
	return originalLength, nil
}

func remainingNonnegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func readDirectStopRequest(path string) (directStopRequest, error) {
	data, err := readBoundedRegularFile(path, directMaximumStopBytes)
	if err != nil {
		return directStopRequest{}, err
	}
	if int64(len(data)) > directMaximumStopBytes {
		return directStopRequest{}, errors.New("direct stop request exceeds 4 KiB")
	}
	var request directStopRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return directStopRequest{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return directStopRequest{}, err
	}
	if request.Schema != directStopRequestSchema || request.GraceMillis <= 0 || request.GraceMillis > directMaximumStopGrace.Milliseconds() {
		return directStopRequest{}, errors.New("invalid direct stop request schema")
	}
	return request, nil
}

func terminateDirectChild(pid int, startIdentity string, waited <-chan error, grace time.Duration) (error, bool, bool) {
	select {
	case err := <-waited:
		return err, false, true
	default:
	}
	if !sameProcess(pid, startIdentity) {
		return nil, false, false
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-waited:
		return err, false, true
	case <-timer.C:
		select {
		case err := <-waited:
			return err, false, true
		default:
		}
		if !sameProcess(pid, startIdentity) {
			return nil, false, false
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return <-waited, true, true
	}
}

func directExitCode(command *exec.Cmd, waitErr error) (int, bool) {
	if command.ProcessState != nil {
		return command.ProcessState.ExitCode(), true
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		return exitError.ExitCode(), true
	}
	return 0, waitErr == nil
}
