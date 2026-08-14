package execution

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/herdr"
)

const (
	herdrHandleSchema          = "urn:crewfold:schema:runtime:herdr-handle:v1"
	herdrSupervisorSpecSchema  = "urn:crewfold:schema:runtime:herdr-pane-supervisor-spec:v1"
	herdrSupervisorStateSchema = "urn:crewfold:schema:runtime:herdr-pane-supervisor-state:v1"
	herdrDispatchSchema        = "urn:crewfold:schema:runtime:herdr-dispatch-intent:v1"
	herdrHandlePrefix          = "herdr:v1:"
	herdrSupervisorStartFailed = "start_failed"
	herdrDefaultStartupTimeout = 4 * time.Second
	herdrDefaultPollInterval   = 20 * time.Millisecond
	herdrSurfaceCheckInterval  = 250 * time.Millisecond
)

var herdrManagedEnvironment = []string{
	"HERDR_ENV", "HERDR_PANE_ID", "HERDR_SOCKET_PATH", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID",
}

type HerdrRuntimeOptions struct {
	NodeID               string
	StateRoot            string
	HerdrExecutable      string
	CrewfoldExecutable   string
	Session              string
	InheritedEnvironment []string
	CommandRunner        herdr.CommandRunner
	StartupTimeout       time.Duration
	PollInterval         time.Duration
}

// HerdrRuntime owns an isolated Herdr workspace per Crewfold run. Its handle is
// anchored to Herdr's terminal ID, so visual pane moves never change run
// identity.
type HerdrRuntime struct {
	nodeID               string
	root                 string
	crewfoldExecutable   string
	inheritedEnvironment []string
	client               *herdr.Client
	startupTimeout       time.Duration
	pollInterval         time.Duration
	mu                   sync.Mutex
	compatibilityChecked bool
	lastSurfaceCheck     map[string]time.Time
}

func NewHerdrRuntime(options HerdrRuntimeOptions) *HerdrRuntime {
	startupTimeout := options.StartupTimeout
	if startupTimeout <= 0 {
		startupTimeout = herdrDefaultStartupTimeout
	}
	pollInterval := options.PollInterval
	if pollInterval <= 0 {
		pollInterval = herdrDefaultPollInterval
	}
	inherited := append([]string(nil), options.InheritedEnvironment...)
	if inherited == nil {
		inherited = os.Environ()
	}
	return &HerdrRuntime{
		nodeID: strings.TrimSpace(options.NodeID), root: options.StateRoot, crewfoldExecutable: options.CrewfoldExecutable,
		inheritedEnvironment: inherited,
		client:               herdr.NewClient(options.HerdrExecutable, options.Session, options.CommandRunner),
		startupTimeout:       startupTimeout, pollInterval: pollInterval,
		lastSurfaceCheck: make(map[string]time.Time),
	}
}

func (runtime *HerdrRuntime) Name() string { return "herdr" }

func (runtime *HerdrRuntime) Probe(ctx context.Context) herdr.ProbeReport {
	return runtime.client.Probe(ctx)
}

func (runtime *HerdrRuntime) Launch(ctx context.Context, operationID string, placement domain.RunPlacement, launch LaunchSpec) (RuntimeBinding, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if err := runtime.validate(operationID, placement, launch); err != nil {
		return RuntimeBinding{}, &StartError{Message: err.Error()}
	}
	if err := runtime.ensureCompatible(ctx); err != nil {
		return RuntimeBinding{}, &StartError{Message: err.Error()}
	}
	runDirectory, err := runtime.prepareRunDirectory(operationID)
	if err != nil {
		return RuntimeBinding{}, &StartError{Message: err.Error()}
	}
	if err := runtime.ensureSupervisorSpec(runDirectory, operationID, placement, *launch.Command); err != nil {
		return RuntimeBinding{}, &StartError{Message: err.Error()}
	}

	handle, found, err := runtime.recoverSurface(ctx, operationID, runDirectory)
	if err != nil {
		return RuntimeBinding{}, &StartError{Message: err.Error()}
	}
	if !found {
		surface, createErr := runtime.client.CreateWorkspace(ctx, placement.CheckoutPath, herdrWorkspaceLabel(runtime.nodeID, operationID), map[string]string{"CREWFOLD_NODE_ID": runtime.nodeID, "CREWFOLD_RUN_ID": operationID})
		if createErr != nil {
			return RuntimeBinding{}, &StartError{Message: "create Herdr workspace: " + createErr.Error()}
		}
		handle = herdrRuntimeHandle{
			Schema: herdrHandleSchema, NodeID: runtime.nodeID, OperationID: operationID, Session: runtime.client.Session(),
			WorkspaceID: surface.Workspace.WorkspaceID, TabID: surface.Tab.TabID,
			PaneID: surface.Pane.PaneID, TerminalID: surface.Pane.TerminalID,
		}
		if err := writeJSONAtomic(filepath.Join(runDirectory, "surface.json"), handle); err != nil {
			return RuntimeBinding{}, &OutcomeUnknownError{Message: "Herdr workspace was created but its stable mapping could not be recorded: " + err.Error()}
		}
	}
	encoded, err := encodeHerdrHandle(handle)
	if err != nil {
		return RuntimeBinding{}, &OutcomeUnknownError{Message: err.Error()}
	}
	if state, stateErr := readHerdrSupervisorState(runDirectory); stateErr == nil {
		if err := validateHerdrStateBinding(state, runtime.nodeID, operationID); err != nil {
			return RuntimeBinding{}, &OutcomeUnknownError{Message: err.Error()}
		}
		if state.Status == herdrSupervisorStartFailed {
			return RuntimeBinding{}, &StartError{Message: state.Diagnostic}
		}
		return RuntimeBinding{RuntimeHandle: encoded}, nil
	} else if !errors.Is(stateErr, os.ErrNotExist) {
		return RuntimeBinding{}, &OutcomeUnknownError{Message: "read Herdr pane supervisor state: " + stateErr.Error()}
	}
	if _, err := os.Stat(filepath.Join(runDirectory, "dispatch.json")); err == nil {
		return RuntimeBinding{}, &OutcomeUnknownError{Message: "Herdr command dispatch was previously attempted but no supervisor acknowledgement exists"}
	} else if !errors.Is(err, os.ErrNotExist) {
		return RuntimeBinding{}, &OutcomeUnknownError{Message: "inspect Herdr dispatch intent: " + err.Error()}
	}
	intent := herdrDispatchIntent{Schema: herdrDispatchSchema, NodeID: runtime.nodeID, OperationID: operationID, TerminalID: handle.TerminalID}
	if err := writeJSONAtomic(filepath.Join(runDirectory, "dispatch.json"), intent); err != nil {
		return RuntimeBinding{}, &StartError{Message: "record Herdr command dispatch intent: " + err.Error()}
	}
	command := shellJoin(runtime.crewfoldExecutable, "__herdr-pane-supervisor", filepath.Join(runDirectory, "launch.json"))
	if err := runtime.client.RunPane(ctx, handle.PaneID, command); err != nil {
		return RuntimeBinding{}, &OutcomeUnknownError{Message: "Herdr command dispatch outcome is unknown: " + err.Error()}
	}

	deadline := time.NewTimer(runtime.startupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(runtime.pollInterval)
	defer ticker.Stop()
	for {
		state, stateErr := readHerdrSupervisorState(runDirectory)
		if stateErr == nil {
			if err := validateHerdrStateBinding(state, runtime.nodeID, operationID); err != nil {
				return RuntimeBinding{}, &OutcomeUnknownError{Message: err.Error()}
			}
			switch state.Status {
			case RuntimeStateStarting, RuntimeStateRunning, RuntimeStateExited, RuntimeStateStopped, RuntimeStateTimedOut:
				return RuntimeBinding{RuntimeHandle: encoded}, nil
			case herdrSupervisorStartFailed:
				_ = runtime.client.ClosePane(context.WithoutCancel(ctx), handle.PaneID)
				return RuntimeBinding{}, &StartError{Message: state.Diagnostic}
			}
		} else if !errors.Is(stateErr, os.ErrNotExist) {
			return RuntimeBinding{}, &OutcomeUnknownError{Message: "read Herdr supervisor acknowledgement: " + stateErr.Error()}
		}
		select {
		case <-ctx.Done():
			return RuntimeBinding{}, &OutcomeUnknownError{Message: "Herdr launch was cancelled after command dispatch"}
		case <-deadline.C:
			return RuntimeBinding{}, &OutcomeUnknownError{Message: "Herdr pane supervisor did not acknowledge launch before the compatibility timeout"}
		case <-ticker.C:
		}
	}
}

func (runtime *HerdrRuntime) Reconcile(ctx context.Context, operationID, rawHandle string) (RuntimeBinding, error) {
	handle, err := decodeHerdrHandle(runtime.nodeID, operationID, rawHandle)
	if err != nil {
		return RuntimeBinding{}, err
	}
	state, err := readHerdrSupervisorState(filepath.Join(runtime.root, operationID))
	if err != nil || validateHerdrStateBinding(state, runtime.nodeID, operationID) != nil {
		return RuntimeBinding{}, errors.New("Herdr supervisor state does not match the current node binding")
	}
	if err := runtime.ensureCompatible(ctx); err != nil {
		return RuntimeBinding{}, runtimeUnavailable(err)
	}
	pane, err := runtime.resolvePane(ctx, handle.TerminalID)
	if err != nil {
		return RuntimeBinding{}, err
	}
	handle.WorkspaceID, handle.TabID, handle.PaneID = pane.WorkspaceID, pane.TabID, pane.PaneID
	encoded, err := encodeHerdrHandle(handle)
	if err != nil {
		return RuntimeBinding{}, err
	}
	return RuntimeBinding{RuntimeHandle: encoded}, nil
}

func (runtime *HerdrRuntime) Inspect(ctx context.Context, operationID, rawHandle string) (RuntimeSnapshot, error) {
	handle, err := decodeHerdrHandle(runtime.nodeID, operationID, rawHandle)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	runDirectory := filepath.Join(runtime.root, operationID)
	state, err := readHerdrSupervisorState(runDirectory)
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("read Herdr pane supervisor state: %w", err)
	}
	if err := validateHerdrStateBinding(state, runtime.nodeID, operationID); err != nil {
		return RuntimeSnapshot{}, err
	}
	terminal := state.Status == RuntimeStateExited || state.Status == RuntimeStateStopped || state.Status == RuntimeStateTimedOut || state.Status == herdrSupervisorStartFailed
	var resolvedPane herdr.PaneInfo
	if terminal || runtime.surfaceCheckDue(operationID) {
		pane, resolveErr := runtime.resolvePane(ctx, handle.TerminalID)
		if resolveErr != nil {
			return RuntimeSnapshot{}, resolveErr
		}
		runtime.markSurfaceChecked(operationID)
		if !terminal {
			processes, processErr := runtime.client.ProcessInfo(ctx, pane.PaneID)
			if processErr != nil {
				return RuntimeSnapshot{}, runtimeControlError("inspect Herdr pane process", processErr)
			}
			if !herdrProcessMatchesState(processes, state) {
				refreshed, refreshErr := readHerdrSupervisorState(runDirectory)
				if refreshErr == nil && validateHerdrStateBinding(refreshed, runtime.nodeID, operationID) == nil {
					state = refreshed
					terminal = state.Status == RuntimeStateExited || state.Status == RuntimeStateStopped || state.Status == RuntimeStateTimedOut || state.Status == herdrSupervisorStartFailed
				}
				if !terminal {
					return RuntimeSnapshot{}, errors.New("Herdr pane exists but its recorded supervisor or child is no longer a foreground process")
				}
			}
		}
		resolvedPane = pane
	}
	snapshot := herdrSnapshot(state)
	if terminal {
		text, readErr := runtime.client.ReadPane(ctx, resolvedPane.PaneID, 500)
		if readErr != nil {
			return RuntimeSnapshot{}, runtimeControlError("read terminal Herdr provider output", readErr)
		}
		text = redactDirectOutput(text)
		snapshot.Stdout = domain.CapturedLog{Text: text, CapturedBytes: int64(len(text))}
	}
	return snapshot, nil
}

func (runtime *HerdrRuntime) Stop(ctx context.Context, operationID, rawHandle string, spec StopSpec) (StopResult, error) {
	handle, err := decodeHerdrHandle(runtime.nodeID, operationID, rawHandle)
	if err != nil {
		return StopResult{}, err
	}
	runDirectory := filepath.Join(runtime.root, operationID)
	state, stateErr := readHerdrSupervisorState(runDirectory)
	if stateErr != nil {
		return StopResult{}, stateErr
	}
	if err := validateHerdrStateBinding(state, runtime.nodeID, operationID); err != nil {
		return StopResult{}, err
	}
	pane, err := runtime.resolvePane(ctx, handle.TerminalID)
	if err != nil {
		return StopResult{}, err
	}
	if state.Status == RuntimeStateRunning || state.Status == RuntimeStateStarting {
		if err := runtime.client.SendKeys(ctx, pane.PaneID, "ctrl+c"); err != nil {
			return StopResult{}, runtimeControlError("interrupt Herdr pane", err)
		}
		grace := spec.GracePeriod
		if grace <= 0 {
			grace = 500 * time.Millisecond
		}
		deadline := time.NewTimer(grace)
		ticker := time.NewTicker(runtime.pollInterval)
		defer deadline.Stop()
		defer ticker.Stop()
		for state.Status == RuntimeStateRunning || state.Status == RuntimeStateStarting {
			select {
			case <-ctx.Done():
				return StopResult{}, &OutcomeUnknownError{Message: "Herdr stop was cancelled after interrupt delivery"}
			case <-deadline.C:
				state.Status = RuntimeStateUnknown
			case <-ticker.C:
				if refreshed, refreshErr := readHerdrSupervisorState(runDirectory); refreshErr == nil {
					if err := validateHerdrStateBinding(refreshed, runtime.nodeID, operationID); err != nil {
						return StopResult{}, err
					}
					state = refreshed
				}
			}
		}
	}
	forced := state.Status == RuntimeStateUnknown
	if snapshot, snapshotErr := runtime.client.Snapshot(ctx); snapshotErr != nil {
		return StopResult{}, &OutcomeUnknownError{Message: "verify Herdr pane before closure: " + snapshotErr.Error()}
	} else if _, exists := herdr.PaneByTerminal(snapshot, handle.TerminalID); !exists {
		diagnostic := "Herdr pane stopped and closed"
		if forced {
			diagnostic = "Herdr pane closed after its stop grace period expired"
		}
		return StopResult{Forced: forced, Diagnostic: diagnostic}, nil
	}
	if err := runtime.client.ClosePane(ctx, pane.PaneID); err != nil {
		return StopResult{}, &OutcomeUnknownError{Message: "close Herdr pane after stop: " + err.Error()}
	}
	if snapshot, snapshotErr := runtime.client.Snapshot(ctx); snapshotErr != nil {
		return StopResult{}, &OutcomeUnknownError{Message: "verify Herdr pane closure: " + snapshotErr.Error()}
	} else if _, exists := herdr.PaneByTerminal(snapshot, handle.TerminalID); exists {
		return StopResult{}, &OutcomeUnknownError{Message: "Herdr acknowledged pane close but the terminal remains present"}
	}
	diagnostic := "Herdr pane stopped and closed"
	if forced {
		diagnostic = "Herdr pane did not settle during the grace period and was closed"
	}
	return StopResult{Forced: forced, Diagnostic: diagnostic}, nil
}

func (runtime *HerdrRuntime) Logs(ctx context.Context, operationID, rawHandle string, tail int) (domain.RunLogs, error) {
	handle, err := decodeHerdrHandle(runtime.nodeID, operationID, rawHandle)
	if err != nil {
		return domain.RunLogs{}, err
	}
	state, err := readHerdrSupervisorState(filepath.Join(runtime.root, operationID))
	if err != nil {
		return domain.RunLogs{}, err
	}
	if err := validateHerdrStateBinding(state, runtime.nodeID, operationID); err != nil {
		return domain.RunLogs{}, err
	}
	pane, err := runtime.resolvePane(ctx, handle.TerminalID)
	if err != nil {
		return domain.RunLogs{}, err
	}
	text, err := runtime.client.ReadPane(ctx, pane.PaneID, tail)
	if err != nil {
		return domain.RunLogs{}, runtimeControlError("read Herdr pane", err)
	}
	text = redactDirectOutput(text)
	return domain.RunLogs{RunID: operationID, State: state.Status, Stdout: domain.CapturedLog{Text: text, CapturedBytes: int64(len(text))}}, nil
}

func (runtime *HerdrRuntime) Prompt(ctx context.Context, operationID, rawHandle, prompt string) error {
	if strings.TrimSpace(prompt) == "" || strings.ContainsRune(prompt, '\x00') || len(prompt) > 16*1024 {
		return errors.New("Herdr prompt must be non-empty, valid terminal text no larger than 16 KiB")
	}
	handle, err := decodeHerdrHandle(runtime.nodeID, operationID, rawHandle)
	if err != nil {
		return err
	}
	state, err := readHerdrSupervisorState(filepath.Join(runtime.root, operationID))
	if err != nil || validateHerdrStateBinding(state, runtime.nodeID, operationID) != nil || (state.Status != RuntimeStateRunning && state.Status != RuntimeStateStarting) {
		return errors.New("Herdr pane process is not accepting prompts")
	}
	pane, err := runtime.resolvePane(ctx, handle.TerminalID)
	if err != nil {
		return err
	}
	if err := runtime.client.SendText(ctx, pane.PaneID, prompt); err != nil {
		return runtimeControlError("send Herdr prompt text", err)
	}
	if err := runtime.client.SendKeys(ctx, pane.PaneID, "enter"); err != nil {
		return runtimeControlError("submit Herdr prompt", err)
	}
	return nil
}

func (runtime *HerdrRuntime) Interrupt(ctx context.Context, operationID, rawHandle string) error {
	handle, err := decodeHerdrHandle(runtime.nodeID, operationID, rawHandle)
	if err != nil {
		return err
	}
	state, err := readHerdrSupervisorState(filepath.Join(runtime.root, operationID))
	if err != nil || validateHerdrStateBinding(state, runtime.nodeID, operationID) != nil || (state.Status != RuntimeStateRunning && state.Status != RuntimeStateStarting) {
		return errors.New("Herdr pane process is not accepting interrupts")
	}
	pane, err := runtime.resolvePane(ctx, handle.TerminalID)
	if err != nil {
		return err
	}
	if err := runtime.client.SendKeys(ctx, pane.PaneID, "ctrl+c"); err != nil {
		return runtimeControlError("interrupt Herdr pane", err)
	}
	return nil
}

func (runtime *HerdrRuntime) Attach(_ context.Context, operationID, rawHandle string) (AttachSpec, error) {
	handle, err := decodeHerdrHandle(runtime.nodeID, operationID, rawHandle)
	if err != nil {
		return AttachSpec{}, err
	}
	state, err := readHerdrSupervisorState(filepath.Join(runtime.root, operationID))
	if err != nil || validateHerdrStateBinding(state, runtime.nodeID, operationID) != nil || (state.Status != RuntimeStateRunning && state.Status != RuntimeStateStarting) {
		return AttachSpec{}, errors.New("Herdr pane is not live on the current node")
	}
	arguments := []string{"terminal", "attach", handle.TerminalID}
	environment := map[string]string{}
	if runtime.client.Session() != "" {
		environment["HERDR_SESSION"] = runtime.client.Session()
	}
	return AttachSpec{Executable: runtime.client.Executable(), Arguments: arguments, Environment: environment}, nil
}

type herdrRuntimeHandle struct {
	Schema      string `json:"schema"`
	NodeID      string `json:"node_id"`
	OperationID string `json:"operation_id"`
	Session     string `json:"session,omitempty"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	PaneID      string `json:"pane_id"`
	TerminalID  string `json:"terminal_id"`
}

type herdrSupervisorSpec struct {
	Schema           string   `json:"schema"`
	NodeID           string   `json:"node_id"`
	OperationID      string   `json:"operation_id"`
	Executable       string   `json:"executable"`
	Arguments        []string `json:"arguments"`
	StandardInput    []byte   `json:"standard_input,omitempty"`
	Environment      []string `json:"environment"`
	WorkingDirectory string   `json:"working_directory"`
	TimeoutMillis    int64    `json:"timeout_millis,omitempty"`
	HoldAfterExit    bool     `json:"hold_after_exit,omitempty"`
}

type herdrSupervisorState struct {
	Schema          string `json:"schema"`
	NodeID          string `json:"node_id"`
	OperationID     string `json:"operation_id"`
	Status          string `json:"status"`
	SupervisorPID   int    `json:"supervisor_pid,omitempty"`
	SupervisorStart string `json:"supervisor_start,omitempty"`
	ChildPID        int    `json:"child_pid,omitempty"`
	ChildStart      string `json:"child_start,omitempty"`
	ExitCode        int    `json:"exit_code,omitempty"`
	ExitKnown       bool   `json:"exit_known,omitempty"`
	Forced          bool   `json:"forced,omitempty"`
	Diagnostic      string `json:"diagnostic,omitempty"`
}

type herdrDispatchIntent struct {
	Schema      string `json:"schema"`
	NodeID      string `json:"node_id"`
	OperationID string `json:"operation_id"`
	TerminalID  string `json:"terminal_id"`
}

func (runtime *HerdrRuntime) validate(operationID string, placement domain.RunPlacement, launch LaunchSpec) error {
	if !validNodeID(runtime.nodeID) || !directOperationPattern.MatchString(operationID) {
		return errors.New("Herdr runtime operation ID is invalid")
	}
	if launch.Command == nil {
		return errors.New("Herdr runtime requires a command specification")
	}
	if _, err := validateDirectWorkingDirectory(placement.CheckoutPath); err != nil {
		return errors.New(strings.NewReplacer("direct runtime", "Herdr runtime").Replace(err.Error()))
	}
	if strings.TrimSpace(runtime.root) == "" || strings.TrimSpace(runtime.crewfoldExecutable) == "" {
		return errors.New("Herdr runtime state root and Crewfold executable are required")
	}
	if _, err := filepath.Abs(runtime.root); err != nil {
		return fmt.Errorf("resolve Herdr runtime state root: %w", err)
	}
	crewfoldExecutable, err := filepath.Abs(runtime.crewfoldExecutable)
	if err != nil {
		return fmt.Errorf("resolve Crewfold executable: %w", err)
	}
	if info, err := os.Stat(crewfoldExecutable); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("Crewfold executable for the Herdr pane supervisor is unavailable")
	}
	runtime.crewfoldExecutable = crewfoldExecutable
	return nil
}

func (runtime *HerdrRuntime) ensureCompatible(ctx context.Context) error {
	if runtime.compatibilityChecked {
		return nil
	}
	report := runtime.client.Probe(ctx)
	if err := report.Error(); err != nil {
		return err
	}
	runtime.compatibilityChecked = true
	return nil
}

func (runtime *HerdrRuntime) prepareRunDirectory(operationID string) (string, error) {
	root, err := filepath.Abs(runtime.root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create Herdr runtime root: %w", err)
	}
	if err := rejectHerdrSymlink(root); err != nil {
		return "", err
	}
	runDirectory := filepath.Join(root, operationID)
	if err := os.Mkdir(runDirectory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create Herdr run state: %w", err)
	}
	if err := rejectHerdrSymlink(runDirectory); err != nil {
		return "", err
	}
	return runDirectory, nil
}

func rejectHerdrSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("Herdr runtime path must be a real directory: %s", path)
	}
	return nil
}

func (runtime *HerdrRuntime) ensureSupervisorSpec(runDirectory, operationID string, placement domain.RunPlacement, command CommandSpec) error {
	path := filepath.Join(runDirectory, "launch.json")
	if existing, err := readHerdrSupervisorSpec(path); err == nil {
		if existing.NodeID != runtime.nodeID || existing.OperationID != operationID || existing.Executable != command.Executable || !equalStrings(existing.Arguments, command.Arguments) {
			return errors.New("existing Herdr launch specification does not match the requested operation")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read existing Herdr launch specification: %w", err)
	}
	executable, err := filepath.Abs(strings.TrimSpace(command.Executable))
	if err != nil || !filepath.IsAbs(executable) {
		return errors.New("Herdr child executable must resolve to an absolute path")
	}
	if info, err := os.Stat(executable); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("Herdr child executable is unavailable or not executable")
	}
	environment, err := buildDirectEnvironment(runtime.inheritedEnvironment, command.Environment, operationID)
	if err != nil {
		return errors.New(strings.NewReplacer("direct runtime", "Herdr runtime").Replace(err.Error()))
	}
	workingDirectory, err := validateDirectWorkingDirectory(placement.CheckoutPath)
	if err != nil {
		return err
	}
	spec := herdrSupervisorSpec{
		Schema: herdrSupervisorSpecSchema, NodeID: runtime.nodeID, OperationID: operationID, Executable: executable,
		Arguments: append([]string(nil), command.Arguments...), StandardInput: append([]byte(nil), command.StandardInput...),
		Environment: environment, WorkingDirectory: workingDirectory, TimeoutMillis: command.Timeout.Milliseconds(), HoldAfterExit: true,
	}
	if spec.TimeoutMillis < 0 {
		return errors.New("Herdr child timeout cannot be negative")
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (runtime *HerdrRuntime) recoverSurface(ctx context.Context, operationID, runDirectory string) (herdrRuntimeHandle, bool, error) {
	if handle, err := readHerdrHandleFile(filepath.Join(runDirectory, "surface.json")); err == nil {
		if handle.NodeID != runtime.nodeID || handle.OperationID != operationID {
			return herdrRuntimeHandle{}, false, errors.New("recorded Herdr surface belongs to another operation")
		}
		pane, resolveErr := runtime.resolvePane(ctx, handle.TerminalID)
		if resolveErr != nil {
			return herdrRuntimeHandle{}, false, resolveErr
		}
		handle.WorkspaceID, handle.TabID, handle.PaneID = pane.WorkspaceID, pane.TabID, pane.PaneID
		return handle, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return herdrRuntimeHandle{}, false, err
	}
	snapshot, err := runtime.client.Snapshot(ctx)
	if err != nil {
		return herdrRuntimeHandle{}, false, runtimeControlError("discover existing Herdr workspace", err)
	}
	workspaceIDs := make(map[string]bool)
	for _, workspace := range snapshot.Workspaces {
		if workspace.Label == herdrWorkspaceLabel(runtime.nodeID, operationID) {
			workspaceIDs[workspace.WorkspaceID] = true
		}
	}
	panes := make([]herdr.PaneInfo, 0)
	for _, pane := range snapshot.Panes {
		if workspaceIDs[pane.WorkspaceID] {
			panes = append(panes, pane)
		}
	}
	if len(panes) == 0 && len(workspaceIDs) == 0 {
		return herdrRuntimeHandle{}, false, nil
	}
	if len(workspaceIDs) != 1 || len(panes) != 1 {
		return herdrRuntimeHandle{}, false, errors.New("Herdr recovery found an ambiguous Crewfold-labelled surface")
	}
	pane := panes[0]
	handle := herdrRuntimeHandle{Schema: herdrHandleSchema, NodeID: runtime.nodeID, OperationID: operationID, Session: runtime.client.Session(), WorkspaceID: pane.WorkspaceID, TabID: pane.TabID, PaneID: pane.PaneID, TerminalID: pane.TerminalID}
	if err := writeJSONAtomic(filepath.Join(runDirectory, "surface.json"), handle); err != nil {
		return herdrRuntimeHandle{}, false, err
	}
	return handle, true, nil
}

func (runtime *HerdrRuntime) resolvePane(ctx context.Context, terminalID string) (herdr.PaneInfo, error) {
	snapshot, err := runtime.client.Snapshot(ctx)
	if err != nil {
		return herdr.PaneInfo{}, runtimeControlError("read Herdr session snapshot", err)
	}
	pane, found := herdr.PaneByTerminal(snapshot, terminalID)
	if !found {
		return herdr.PaneInfo{}, fmt.Errorf("Herdr terminal %s is no longer present; pane closure is not completion evidence", terminalID)
	}
	return pane, nil
}

func runtimeControlError(operation string, err error) error {
	var commandError *herdr.CommandError
	if errors.As(err, &commandError) && (commandError.Code == "server_not_running" || commandError.Code == "connection_failed") {
		return &RuntimeUnavailableError{Message: operation + ": " + err.Error()}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func runtimeUnavailable(err error) error {
	var unavailable *RuntimeUnavailableError
	if errors.As(err, &unavailable) {
		return err
	}
	return &RuntimeUnavailableError{Message: err.Error()}
}

func (runtime *HerdrRuntime) surfaceCheckDue(operationID string) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return time.Since(runtime.lastSurfaceCheck[operationID]) >= herdrSurfaceCheckInterval
}

func (runtime *HerdrRuntime) markSurfaceChecked(operationID string) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.lastSurfaceCheck[operationID] = time.Now()
}

func herdrSnapshot(state herdrSupervisorState) RuntimeSnapshot {
	status := state.Status
	if status == herdrSupervisorStartFailed {
		status = RuntimeStateExited
	}
	return RuntimeSnapshot{
		State: status, ExitCode: state.ExitCode, ExitKnown: state.ExitKnown,
		CompletionReady: state.Status == RuntimeStateExited,
		Forced:          state.Forced, Diagnostic: state.Diagnostic,
	}
}

func herdrProcessMatchesState(processes herdr.ProcessInfo, state herdrSupervisorState) bool {
	for _, process := range processes.ForegroundProcesses {
		if process.PID > 0 && (process.PID == state.SupervisorPID || process.PID == state.ChildPID) {
			return true
		}
	}
	return false
}

func validateHerdrStateBinding(state herdrSupervisorState, nodeID, operationID string) error {
	if state.NodeID != nodeID || state.OperationID != operationID {
		return errors.New("Herdr supervisor state does not match the current node binding")
	}
	return nil
}

func encodeHerdrHandle(handle herdrRuntimeHandle) (string, error) {
	data, err := json.Marshal(handle)
	if err != nil {
		return "", err
	}
	return herdrHandlePrefix + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeHerdrHandle(nodeID, operationID, raw string) (herdrRuntimeHandle, error) {
	if !strings.HasPrefix(raw, herdrHandlePrefix) {
		return herdrRuntimeHandle{}, errors.New("Herdr runtime handle has an unsupported encoding")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, herdrHandlePrefix))
	if err != nil || len(data) > 8*1024 {
		return herdrRuntimeHandle{}, errors.New("Herdr runtime handle is invalid")
	}
	var handle herdrRuntimeHandle
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&handle); err != nil || handle.Schema != herdrHandleSchema || handle.NodeID != nodeID || !validNodeID(handle.NodeID) || handle.OperationID != operationID || handle.WorkspaceID == "" || handle.TabID == "" || handle.PaneID == "" || handle.TerminalID == "" {
		return herdrRuntimeHandle{}, errors.New("Herdr runtime handle does not match the operation")
	}
	return handle, nil
}

func readHerdrHandleFile(path string) (herdrRuntimeHandle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return herdrRuntimeHandle{}, err
	}
	var handle herdrRuntimeHandle
	if err := json.Unmarshal(data, &handle); err != nil || handle.Schema != herdrHandleSchema || !validNodeID(handle.NodeID) {
		return herdrRuntimeHandle{}, errors.New("recorded Herdr surface is invalid")
	}
	return handle, nil
}

func readHerdrSupervisorSpec(path string) (herdrSupervisorSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return herdrSupervisorSpec{}, err
	}
	if len(data) > 256*1024 {
		return herdrSupervisorSpec{}, errors.New("Herdr supervisor specification exceeds 256 KiB")
	}
	var spec herdrSupervisorSpec
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return herdrSupervisorSpec{}, err
	}
	if spec.Schema != herdrSupervisorSpecSchema || !validNodeID(spec.NodeID) || !directOperationPattern.MatchString(spec.OperationID) || !filepath.IsAbs(spec.Executable) || !filepath.IsAbs(spec.WorkingDirectory) || spec.TimeoutMillis < 0 {
		return herdrSupervisorSpec{}, errors.New("Herdr supervisor specification is invalid")
	}
	return spec, nil
}

func readHerdrSupervisorState(runDirectory string) (herdrSupervisorState, error) {
	data, err := os.ReadFile(filepath.Join(runDirectory, "state.json"))
	if err != nil {
		return herdrSupervisorState{}, err
	}
	var state herdrSupervisorState
	if err := json.Unmarshal(data, &state); err != nil {
		return herdrSupervisorState{}, err
	}
	if state.Schema != herdrSupervisorStateSchema || !validNodeID(state.NodeID) || !directOperationPattern.MatchString(state.OperationID) {
		return herdrSupervisorState{}, errors.New("Herdr supervisor state is invalid")
	}
	return state, nil
}

func herdrWorkspaceLabel(nodeID, operationID string) string {
	return "crewfold-" + nodeID + "-" + operationID
}

func shellJoin(arguments ...string) string {
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quoted = append(quoted, "'"+strings.ReplaceAll(argument, "'", "'\"'\"'")+"'")
	}
	return "exec " + strings.Join(quoted, " ")
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// RunHerdrPaneSupervisor is the hidden process entry point launched inside a
// Herdr pane. It preserves the pane PTY while recording a durable child result.
func RunHerdrPaneSupervisor(arguments []string) int {
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
	spec, err := readHerdrSupervisorSpec(specPath)
	if err != nil {
		return 2
	}
	runDirectory := filepath.Dir(specPath)
	state := herdrSupervisorState{
		Schema: herdrSupervisorStateSchema, NodeID: spec.NodeID, OperationID: spec.OperationID, Status: RuntimeStateStarting,
		SupervisorPID: os.Getpid(), SupervisorStart: processStartIdentity(os.Getpid()),
	}
	if err := writeJSONAtomic(filepath.Join(runDirectory, "state.json"), state); err != nil {
		return 1
	}

	command := exec.Command(spec.Executable, spec.Arguments...)
	command.Dir = spec.WorkingDirectory
	command.Env = supervisorChildEnvironment(spec.Environment, os.Environ())
	if len(spec.StandardInput) != 0 {
		command.Stdin = bytes.NewReader(spec.StandardInput)
	} else {
		command.Stdin = os.Stdin
	}
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		state.Status, state.Diagnostic = herdrSupervisorStartFailed, "start Herdr pane child: "+err.Error()
		_ = writeJSONAtomic(filepath.Join(runDirectory, "state.json"), state)
		return 1
	}
	state.Status, state.ChildPID, state.ChildStart = RuntimeStateRunning, command.Process.Pid, processStartIdentity(command.Process.Pid)
	if err := writeJSONAtomic(filepath.Join(runDirectory, "state.json"), state); err != nil {
		_ = command.Process.Kill()
		return 1
	}

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	var waitErr error
	timedOut, interrupted := false, false
	var timeout <-chan time.Time
	var timer *time.Timer
	if spec.TimeoutMillis > 0 {
		timer = time.NewTimer(time.Duration(spec.TimeoutMillis) * time.Millisecond)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case waitErr = <-waited:
	case received := <-signals:
		interrupted = true
		_ = command.Process.Signal(received)
		waitErr = <-waited
	case <-timeout:
		timedOut = true
		_ = command.Process.Kill()
		waitErr = <-waited
	}

	state.ExitCode, state.ExitKnown = exitResult(waitErr)
	switch {
	case timedOut:
		state.Status, state.Forced, state.Diagnostic = RuntimeStateTimedOut, true, "Herdr pane child exceeded its configured timeout"
	case interrupted:
		state.Status, state.Diagnostic = RuntimeStateStopped, "Herdr pane child was interrupted"
	default:
		state.Status = RuntimeStateExited
		if waitErr != nil {
			state.Diagnostic = "Herdr pane child exited: " + waitErr.Error()
		}
	}
	_ = writeJSONAtomic(filepath.Join(runDirectory, "state.json"), state)
	if spec.HoldAfterExit && !interrupted {
		// Keep the terminal alive after the provider settles. Real multiplexers may
		// close a pane when its root process exits, racing Crewfold's consumption of
		// the already-durable MCP report and eliminating post-run attach evidence.
		// A pane close/SIGHUP still terminates this process normally; ctrl+c exits it
		// through the subscribed signal channel.
		<-signals
	}
	if state.ExitKnown {
		return state.ExitCode
	}
	return 1
}

func supervisorChildEnvironment(specEnvironment, paneEnvironment []string) []string {
	values := make(map[string]string, len(specEnvironment)+len(herdrManagedEnvironment))
	for _, entry := range specEnvironment {
		if name, value, ok := strings.Cut(entry, "="); ok {
			values[name] = value
		}
	}
	managed := make(map[string]bool, len(herdrManagedEnvironment))
	for _, name := range herdrManagedEnvironment {
		managed[name] = true
	}
	for _, entry := range paneEnvironment {
		if name, value, ok := strings.Cut(entry, "="); ok && managed[name] {
			values[name] = value
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

func exitResult(err error) (int, bool) {
	if err == nil {
		return 0, true
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), true
	}
	return 0, false
}
