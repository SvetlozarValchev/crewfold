package execution

import (
	"bytes"
	"context"
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

	"crewfold/internal/domain"
)

const (
	directSupervisorSpecSchema  = "urn:crewfold:direct-supervisor-spec:v1"
	directSupervisorStateSchema = "urn:crewfold:direct-supervisor-state:v1"
	directStopRequestSchema     = "urn:crewfold:direct-stop-request:v1"
	directDefaultOutputLimit    = int64(64 * 1024)
	directMaximumOutputLimit    = int64(1024 * 1024)
	directDefaultGracePeriod    = 500 * time.Millisecond
	directStartupTimeout        = 3 * time.Second
	directPollInterval          = 10 * time.Millisecond
	directStateStartFailed      = "start_failed"
)

var (
	directOperationPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	secretAssignment       = regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+|(?:token|secret|password|api[_-]?key)\s*[:=]\s*)([^\s]+)`)
)

var directEnvironmentAllowlist = []string{"LANG", "LC_ALL", "LC_CTYPE", "PATH", "TMPDIR", "TZ"}

type DirectRuntimeOptions struct {
	StateRoot            string
	SupervisorExecutable string
	SupervisorArguments  []string
	InheritedEnvironment []string
	OutputByteLimit      int64
	StartupTimeout       time.Duration
}

// DirectRuntime launches one durable supervisor per operation. The supervisor is
// deliberately a separate process so output bounds and exit records survive a
// daemon restart.
type DirectRuntime struct {
	root                 string
	supervisorExecutable string
	supervisorArguments  []string
	inheritedEnvironment []string
	outputLimit          int64
	startupTimeout       time.Duration
	mu                   sync.Mutex
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
	inherited := append([]string(nil), options.InheritedEnvironment...)
	if inherited == nil {
		inherited = os.Environ()
	}
	return &DirectRuntime{
		root:                 options.StateRoot,
		supervisorExecutable: options.SupervisorExecutable,
		supervisorArguments:  append([]string(nil), options.SupervisorArguments...),
		inheritedEnvironment: inherited,
		outputLimit:          outputLimit,
		startupTimeout:       startupTimeout,
	}
}

func (runtime *DirectRuntime) Name() string { return "direct" }

func (runtime *DirectRuntime) Launch(ctx context.Context, operationID string, placement domain.RunPlacement, launch LaunchSpec) (RuntimeBinding, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	if err := runtime.validate(); err != nil {
		return RuntimeBinding{}, err
	}
	if !directOperationPattern.MatchString(operationID) {
		return RuntimeBinding{}, &StartError{Message: "direct runtime operation ID is invalid"}
	}
	if launch.Command == nil {
		return RuntimeBinding{}, &StartError{Message: "direct runtime requires a command specification"}
	}
	workingDirectory, err := validateDirectWorkingDirectory(placement.CheckoutPath)
	if err != nil {
		return RuntimeBinding{}, &StartError{Message: err.Error()}
	}
	executable, err := filepath.Abs(strings.TrimSpace(launch.Command.Executable))
	if err != nil || !filepath.IsAbs(executable) {
		return RuntimeBinding{}, &StartError{Message: "direct runtime executable must resolve to an absolute path"}
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return RuntimeBinding{}, &StartError{Message: "direct runtime executable is unavailable or not executable"}
	}
	environment, err := buildDirectEnvironment(runtime.inheritedEnvironment, launch.Command.Environment, operationID)
	if err != nil {
		return RuntimeBinding{}, &StartError{Message: err.Error()}
	}

	runDirectory, err := runtime.prepareRunDirectory(operationID)
	if err != nil {
		return RuntimeBinding{}, &StartError{Message: err.Error()}
	}
	binding := RuntimeBinding{RuntimeHandle: directHandle(operationID)}
	if state, stateErr := readDirectState(runDirectory); stateErr == nil {
		if state.Status == directStateStartFailed {
			return RuntimeBinding{}, &StartError{Message: state.Diagnostic}
		}
		return binding, nil
	} else if !errors.Is(stateErr, os.ErrNotExist) {
		return RuntimeBinding{}, &StartError{Message: fmt.Sprintf("read existing direct runtime state: %v", stateErr)}
	}

	outputLimit := launch.Command.OutputByteLimit
	if outputLimit <= 0 || outputLimit > runtime.outputLimit {
		outputLimit = runtime.outputLimit
	}
	spec := directSupervisorSpec{
		Schema:             directSupervisorSpecSchema,
		OperationID:        operationID,
		Executable:         executable,
		Arguments:          append([]string(nil), launch.Command.Arguments...),
		StandardInput:      append([]byte(nil), launch.Command.StandardInput...),
		Environment:        environment,
		WorkingDirectory:   workingDirectory,
		OutputByteLimit:    outputLimit,
		TimeoutMillis:      launch.Command.Timeout.Milliseconds(),
		DefaultGraceMillis: directDefaultGracePeriod.Milliseconds(),
	}
	if spec.TimeoutMillis < 0 {
		return RuntimeBinding{}, &StartError{Message: "direct runtime timeout cannot be negative"}
	}
	if err := writeDirectSpec(runDirectory, spec); err != nil {
		return RuntimeBinding{}, &StartError{Message: err.Error()}
	}
	initial := directSupervisorState{Schema: directSupervisorStateSchema, OperationID: operationID, Status: RuntimeStateStarting}
	if err := writeDirectState(runDirectory, initial); err != nil {
		return RuntimeBinding{}, &StartError{Message: fmt.Sprintf("record direct runtime launch intent: %v", err)}
	}

	supervisorArguments := append([]string(nil), runtime.supervisorArguments...)
	if len(supervisorArguments) == 0 {
		supervisorArguments = []string{"__direct-supervisor"}
	}
	supervisorArguments = append(supervisorArguments, directSpecPath(runDirectory))
	command := exec.CommandContext(context.WithoutCancel(ctx), runtime.supervisorExecutable, supervisorArguments...)
	command.Env = environment
	command.Dir = workingDirectory
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
			switch state.Status {
			case RuntimeStateRunning, RuntimeStateExited, RuntimeStateStopped, RuntimeStateTimedOut:
				return binding, nil
			case directStateStartFailed:
				return RuntimeBinding{}, &StartError{Message: state.Diagnostic}
			}
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

func (runtime *DirectRuntime) Reconcile(ctx context.Context, operationID, handle string) (RuntimeBinding, error) {
	if err := validateDirectHandle(operationID, handle); err != nil {
		return RuntimeBinding{}, err
	}
	snapshot, err := runtime.Inspect(ctx, operationID, handle)
	if err != nil {
		return RuntimeBinding{}, err
	}
	if snapshot.State == RuntimeStateUnknown {
		return RuntimeBinding{}, errors.New(snapshot.Diagnostic)
	}
	return RuntimeBinding{RuntimeHandle: handle}, nil
}

func (runtime *DirectRuntime) Inspect(_ context.Context, operationID, handle string) (RuntimeSnapshot, error) {
	if err := validateDirectHandle(operationID, handle); err != nil {
		return RuntimeSnapshot{}, err
	}
	runDirectory, err := runtime.runDirectory(operationID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	state, err := readDirectState(runDirectory)
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("read direct runtime state: %w", err)
	}
	if state.OperationID != operationID || state.Schema != directSupervisorStateSchema {
		return RuntimeSnapshot{}, errors.New("direct runtime state identity is invalid")
	}
	if (state.Status == RuntimeStateStarting || state.Status == RuntimeStateRunning) && state.SupervisorPID > 0 && !sameProcess(state.SupervisorPID, state.SupervisorStart) {
		time.Sleep(directPollInterval)
		if refreshed, refreshErr := readDirectState(runDirectory); refreshErr == nil {
			state = refreshed
		}
		if (state.Status == RuntimeStateStarting || state.Status == RuntimeStateRunning) && !sameProcess(state.SupervisorPID, state.SupervisorStart) {
			state.Status = RuntimeStateUnknown
			state.Diagnostic = "direct runtime supervisor disappeared before recording a final process result"
		}
	}
	if state.Status == RuntimeStateStarting && state.SupervisorPID == 0 {
		if info, statErr := os.Stat(filepath.Join(runDirectory, "state.json")); statErr == nil && time.Since(info.ModTime()) > runtime.startupTimeout {
			state.Status = RuntimeStateUnknown
			state.Diagnostic = "direct runtime launch intent has no acknowledged supervisor identity"
		}
	}
	stdout, err := readDirectCapture(filepath.Join(runDirectory, "stdout.log"), state.StdoutCapturedBytes, state.StdoutOmittedBytes, 0)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	stderr, err := readDirectCapture(filepath.Join(runDirectory, "stderr.log"), state.StderrCapturedBytes, state.StderrOmittedBytes, 0)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	return RuntimeSnapshot{
		State: state.Status, ExitCode: state.ExitCode, ExitKnown: state.ExitKnown,
		CompletionReady: state.Status == RuntimeStateExited,
		Forced:          state.Forced, Diagnostic: state.Diagnostic, Stdout: stdout, Stderr: stderr,
	}, nil
}

func (runtime *DirectRuntime) Stop(ctx context.Context, operationID, handle string, spec StopSpec) (StopResult, error) {
	if err := validateDirectHandle(operationID, handle); err != nil {
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
	if state.Status == RuntimeStateStopped || state.Status == RuntimeStateExited || state.Status == RuntimeStateTimedOut {
		return StopResult{Forced: state.Forced, Diagnostic: state.Diagnostic}, nil
	}
	if state.SupervisorPID <= 0 || !sameProcess(state.SupervisorPID, state.SupervisorStart) {
		return StopResult{}, &OutcomeUnknownError{Message: "direct runtime supervisor identity cannot be trusted for stop"}
	}
	grace := spec.GracePeriod
	if grace <= 0 {
		grace = directDefaultGracePeriod
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
		if stateErr == nil && (current.Status == RuntimeStateStopped || current.Status == RuntimeStateExited || current.Status == RuntimeStateTimedOut) {
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
	snapshot, err := runtime.Inspect(ctx, operationID, handle)
	if err != nil {
		return domain.RunLogs{}, err
	}
	stdout, stderr := snapshot.Stdout, snapshot.Stderr
	stdout.Text = redactDirectOutput(tailText(stdout.Text, tail))
	stderr.Text = redactDirectOutput(tailText(stderr.Text, tail))
	return domain.RunLogs{RunID: operationID, State: snapshot.State, Stdout: stdout, Stderr: stderr}, nil
}

func (runtime *DirectRuntime) validate() error {
	if strings.TrimSpace(runtime.root) == "" || strings.TrimSpace(runtime.supervisorExecutable) == "" {
		return &StartError{Message: "direct runtime state root and supervisor executable are required"}
	}
	return nil
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
	OperationID        string   `json:"operation_id"`
	Executable         string   `json:"executable"`
	Arguments          []string `json:"arguments"`
	StandardInput      []byte   `json:"standard_input"`
	Environment        []string `json:"environment"`
	WorkingDirectory   string   `json:"working_directory"`
	OutputByteLimit    int64    `json:"output_byte_limit"`
	TimeoutMillis      int64    `json:"timeout_millis"`
	DefaultGraceMillis int64    `json:"default_grace_millis"`
}

type directSupervisorState struct {
	Schema              string `json:"schema"`
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
	StderrCapturedBytes int64  `json:"stderr_captured_bytes,omitempty"`
	StderrOmittedBytes  int64  `json:"stderr_omitted_bytes,omitempty"`
}

type directStopRequest struct {
	Schema      string `json:"schema"`
	GraceMillis int64  `json:"grace_millis"`
}

func directHandle(operationID string) string { return "direct:" + operationID }

func validateDirectHandle(operationID, handle string) error {
	if !directOperationPattern.MatchString(operationID) || handle != directHandle(operationID) {
		return errors.New("direct runtime handle does not match the operation")
	}
	return nil
}

func validateDirectWorkingDirectory(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("direct runtime requires an assigned checkout directory")
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve assigned checkout directory: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("assigned checkout directory is unavailable")
	}
	return resolved, nil
}

func buildDirectEnvironment(inherited []string, overrides map[string]string, operationID string) ([]string, error) {
	allowed := make(map[string]struct{}, len(directEnvironmentAllowlist))
	for _, name := range directEnvironmentAllowlist {
		allowed[name] = struct{}{}
	}
	values := make(map[string]string, len(allowed)+1)
	for _, entry := range inherited {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, accepted := allowed[name]; accepted {
			values[name] = value
		}
	}
	for name, value := range overrides {
		if _, accepted := allowed[name]; !accepted {
			return nil, fmt.Errorf("direct runtime environment variable %q is not allowlisted", name)
		}
		if strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("direct runtime environment variable %q contains a NUL byte", name)
		}
		values[name] = value
	}
	values["CREWFOLD_RUN_ID"] = operationID
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

func directSpecPath(runDirectory string) string { return filepath.Join(runDirectory, "launch.json") }

func writeDirectSpec(runDirectory string, spec directSupervisorSpec) error {
	data, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode direct runtime launch specification: %w", err)
	}
	path := directSpecPath(runDirectory)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return errors.New("direct runtime launch specification exists without a recoverable state; refusing duplicate launch")
	}
	if err != nil {
		return fmt.Errorf("create direct runtime launch specification: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write direct runtime launch specification: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close direct runtime launch specification: %w", err)
	}
	return nil
}

func readDirectSpec(path string) (directSupervisorSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return directSupervisorSpec{}, err
	}
	if len(data) > 256*1024 {
		return directSupervisorSpec{}, errors.New("direct supervisor specification exceeds 256 KiB")
	}
	var spec directSupervisorSpec
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return directSupervisorSpec{}, err
	}
	if spec.Schema != directSupervisorSpecSchema || !directOperationPattern.MatchString(spec.OperationID) || !filepath.IsAbs(spec.Executable) || !filepath.IsAbs(spec.WorkingDirectory) || spec.OutputByteLimit <= 0 || spec.OutputByteLimit > directMaximumOutputLimit || spec.TimeoutMillis < 0 || spec.DefaultGraceMillis <= 0 {
		return directSupervisorSpec{}, errors.New("direct supervisor specification is invalid")
	}
	return spec, nil
}

func readDirectState(runDirectory string) (directSupervisorState, error) {
	data, err := os.ReadFile(filepath.Join(runDirectory, "state.json"))
	if err != nil {
		return directSupervisorState{}, err
	}
	var state directSupervisorState
	if err := json.Unmarshal(data, &state); err != nil {
		return directSupervisorState{}, err
	}
	return state, nil
}

func writeDirectState(runDirectory string, state directSupervisorState) error {
	return writeJSONAtomic(filepath.Join(runDirectory, "state.json"), state)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".crewfold-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
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

func readDirectCapture(path string, recordedCaptured, recordedOmitted int64, tail int) (domain.CapturedLog, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data = nil
	} else if err != nil {
		return domain.CapturedLog{}, fmt.Errorf("read direct runtime output: %w", err)
	}
	captured := int64(len(data))
	if recordedCaptured > captured {
		captured = recordedCaptured
	}
	text := tailText(string(data), tail)
	return domain.CapturedLog{Text: text, CapturedBytes: captured, OmittedBytes: recordedOmitted, Truncated: recordedOmitted > 0}, nil
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
	return secretAssignment.ReplaceAllString(value, "${1}[REDACTED]")
}

// RunDirectSupervisor is the hidden process entry point used by DirectRuntime.
func RunDirectSupervisor(arguments []string) int {
	if len(arguments) != 1 {
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
		Schema: directSupervisorStateSchema, OperationID: spec.OperationID,
		Status: RuntimeStateStarting, SupervisorPID: os.Getpid(), SupervisorStart: processStartIdentity(os.Getpid()),
	}
	_ = writeDirectState(runDirectory, state)

	stdoutFile, err := os.OpenFile(filepath.Join(runDirectory, "stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		state.Status, state.Diagnostic = directStateStartFailed, "open bounded stdout capture: "+err.Error()
		_ = writeDirectState(runDirectory, state)
		return 1
	}
	defer stdoutFile.Close()
	stderrFile, err := os.OpenFile(filepath.Join(runDirectory, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		state.Status, state.Diagnostic = directStateStartFailed, "open bounded stderr capture: "+err.Error()
		_ = writeDirectState(runDirectory, state)
		return 1
	}
	defer stderrFile.Close()

	command := exec.Command(spec.Executable, spec.Arguments...)
	command.Dir = spec.WorkingDirectory
	command.Env = append([]string(nil), spec.Environment...)
	command.Stdin = bytes.NewReader(spec.StandardInput)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdoutCapture := &boundedCapture{writer: stdoutFile, limit: spec.OutputByteLimit}
	stderrCapture := &boundedCapture{writer: stderrFile, limit: spec.OutputByteLimit}
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
	if finalStatus != RuntimeStateUnknown {
		state.ExitCode, state.ExitKnown = directExitCode(command, waitErr)
	}
	_ = writeDirectState(runDirectory, state)
	return 0
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
	data, err := os.ReadFile(path)
	if err != nil {
		return directStopRequest{}, err
	}
	var request directStopRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return directStopRequest{}, err
	}
	if request.Schema != directStopRequestSchema {
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
