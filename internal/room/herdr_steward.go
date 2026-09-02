package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maximumStewardConsoleBytes = 256 * 1024

type StewardRuntimeState struct {
	WorkspaceID string
	PaneID      string
	AgentStatus string
	Output      string
}

type StewardRuntime interface {
	Ensure(context.Context, HostedSteward, Room) (StewardRuntimeState, error)
	Inspect(context.Context, HostedSteward) (StewardRuntimeState, error)
	Prompt(context.Context, HostedSteward, string) error
	Deliver(context.Context, HostedSteward, string) error
	SendKey(context.Context, HostedSteward, string) error
	Stop(context.Context, HostedSteward) error
	Close()
}

type HerdrStewardRuntime struct {
	herdrPath    string
	crewfoldPath string
	socketPath   string
	dataDir      string
	mu           sync.Mutex
	servers      map[string]stewardServerProcess
}

type stewardServerProcess struct {
	command *exec.Cmd
	input   io.WriteCloser
}

func NewHerdrStewardRuntime(socketPath, dataDir string) (*HerdrStewardRuntime, error) {
	herdrPath, err := exec.LookPath("herdr")
	if err != nil {
		herdrPath = "herdr"
	}
	crewfoldPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve Crewfold executable: %w", err)
	}
	return &HerdrStewardRuntime{herdrPath: herdrPath, crewfoldPath: crewfoldPath, socketPath: socketPath, dataDir: dataDir, servers: map[string]stewardServerProcess{}}, nil
}

func (r *HerdrStewardRuntime) Ensure(ctx context.Context, steward HostedSteward, room Room) (StewardRuntimeState, error) {
	if state, err := r.Inspect(ctx, steward); err == nil {
		return state, nil
	}
	if err := r.startServer(ctx, steward); err != nil {
		return StewardRuntimeState{}, err
	}

	pathValue := filepath.Dir(r.crewfoldPath)
	if inherited := os.Getenv("PATH"); inherited != "" {
		pathValue += string(os.PathListSeparator) + inherited
	}
	output, err := r.run(ctx, steward.HerdrSession, "workspace", "create",
		"--cwd", steward.WorkingDirectory,
		"--label", room.Title+" steward",
		"--env", "CREWFOLD_SOCKET="+r.socketPath,
		"--env", "PATH="+pathValue,
		"--no-focus")
	if err != nil {
		return StewardRuntimeState{}, fmt.Errorf("create Herdr steward workspace: %w", err)
	}
	var created struct {
		Result struct {
			RootPane struct {
				PaneID      string `json:"pane_id"`
				WorkspaceID string `json:"workspace_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &created); err != nil || created.Result.RootPane.PaneID == "" {
		return StewardRuntimeState{}, fmt.Errorf("Herdr returned an invalid workspace response: %s", boundedRuntimeError(output))
	}

	var startErr error
	blockedDuringStart := false
	for attempt := 0; attempt < 12; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return StewardRuntimeState{}, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
		agentArguments := append([]string{"agent", "start", steward.AgentName, "--kind", "codex", "--pane", created.Result.RootPane.PaneID, "--timeout", "60000", "--"}, stewardCodexArguments(steward)...)
		_, startErr = r.run(ctx, steward.HerdrSession, agentArguments...)
		if startErr == nil {
			break
		}
		if strings.Contains(startErr.Error(), "agent_not_ready") {
			blockedDuringStart = true
			startErr = nil
			break
		}
		if !strings.Contains(startErr.Error(), "agent_pane_busy") {
			break
		}
	}
	if startErr != nil {
		return StewardRuntimeState{}, fmt.Errorf("start Codex in Herdr: %w", startErr)
	}

	state, err := r.waitForAgent(ctx, steward, 20*time.Second)
	if err != nil {
		return StewardRuntimeState{}, err
	}
	if blockedDuringStart && !(steward.ManagedWorkingDirectory && hasCodexTrustPrompt(state.Output)) {
		return StewardRuntimeState{}, fmt.Errorf("Codex is blocked during startup: %s", boundedRuntimeError([]byte(state.Output)))
	}
	if steward.ManagedWorkingDirectory && hasCodexTrustPrompt(state.Output) {
		if err := r.SendKey(ctx, steward, "enter"); err != nil {
			return StewardRuntimeState{}, fmt.Errorf("accept managed steward workspace: %w", err)
		}
	}
	state, err = r.waitForAgentReady(ctx, steward, 20*time.Second)
	if err != nil {
		return StewardRuntimeState{}, err
	}
	return state, nil
}

func stewardCodexArguments(steward HostedSteward) []string {
	arguments := []string{"--no-alt-screen", "-c", "shell_environment_policy.inherit=all", "-c", "check_for_update_on_startup=false"}
	if steward.ManagedWorkingDirectory {
		arguments = append(arguments, "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust")
	}
	// A completed onboarding turn proves that this steward has a saved Codex
	// thread in its room-owned working directory. Herdr panes are disposable;
	// after a daemon restart, resume that thread instead of silently creating a
	// second personality with an empty transcript.
	if steward.InitializedAt != "" {
		arguments = append(arguments, "resume", "--last")
	}
	return arguments
}

func hasCodexTrustPrompt(output string) bool {
	return strings.Contains(strings.ToLower(output), "do you trust the contents of this directory")
}

func (r *HerdrStewardRuntime) Inspect(ctx context.Context, steward HostedSteward) (StewardRuntimeState, error) {
	output, err := r.run(ctx, steward.HerdrSession, "agent", "get", steward.AgentName)
	if err != nil {
		return StewardRuntimeState{}, err
	}
	var result struct {
		Result struct {
			Agent struct {
				WorkspaceID string `json:"workspace_id"`
				PaneID      string `json:"pane_id"`
				AgentStatus string `json:"agent_status"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &result); err != nil || result.Result.Agent.PaneID == "" {
		return StewardRuntimeState{}, fmt.Errorf("Herdr returned an invalid agent response: %s", boundedRuntimeError(output))
	}
	transcript, readErr := r.run(ctx, steward.HerdrSession, "agent", "read", steward.AgentName, "--source", "recent-unwrapped", "--lines", "500", "--format", "text")
	if readErr != nil {
		return StewardRuntimeState{}, fmt.Errorf("read steward terminal: %w", readErr)
	}
	if len(transcript) > maximumStewardConsoleBytes {
		transcript = transcript[len(transcript)-maximumStewardConsoleBytes:]
	}
	return StewardRuntimeState{WorkspaceID: result.Result.Agent.WorkspaceID, PaneID: result.Result.Agent.PaneID, AgentStatus: result.Result.Agent.AgentStatus, Output: string(transcript)}, nil
}

func (r *HerdrStewardRuntime) Prompt(ctx context.Context, steward HostedSteward, text string) error {
	_, err := r.run(ctx, steward.HerdrSession, "agent", "prompt", steward.AgentName, text, "--wait", "--until", "working", "--timeout", "8000")
	if !needsCodexPasteRecovery() {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}
	state, inspectErr := r.Inspect(ctx, steward)
	if inspectErr != nil || (state.AgentStatus != "idle" && state.AgentStatus != "done") || !hasPendingCodexPaste(state.Output) {
		return err
	}
	if err := r.SendKey(ctx, steward, "enter"); err != nil {
		return fmt.Errorf("submit pasted Codex prompt: %w", err)
	}
	_, err = r.run(ctx, steward.HerdrSession, "agent", "wait", steward.AgentName, "--until", "working", "--timeout", "8000")
	return err
}

func (r *HerdrStewardRuntime) Deliver(ctx context.Context, steward HostedSteward, text string) error {
	_, err := r.run(ctx, steward.HerdrSession, "agent", "prompt", steward.AgentName, text, "--wait", "--until", "idle", "--until", "done", "--timeout", "300000")
	return err
}

func hasPendingCodexPaste(output string) bool {
	if len(output) > 4096 {
		output = output[len(output)-4096:]
	}
	return strings.Contains(output, "[Pasted Content")
}

func (r *HerdrStewardRuntime) SendKey(ctx context.Context, steward HostedSteward, key string) error {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "enter", "esc", "ctrl+c":
	default:
		return errors.New("steward key must be enter, esc, or ctrl+c")
	}
	_, err := r.run(ctx, steward.HerdrSession, "agent", "send-keys", steward.AgentName, key)
	return err
}

func (r *HerdrStewardRuntime) Stop(ctx context.Context, steward HostedSteward) error {
	r.mu.Lock()
	process, exists := r.servers[steward.HerdrSession]
	delete(r.servers, steward.HerdrSession)
	r.mu.Unlock()
	stopCommand := exec.CommandContext(ctx, r.herdrPath, "session", "stop", steward.HerdrSession, "--json")
	stopOutput, stopErr := stopCommand.CombinedOutput()
	if exists {
		_ = process.input.Close()
	}
	if stopErr != nil && !strings.Contains(string(stopOutput), "not_running") && !strings.Contains(string(stopOutput), "not running or cannot be reached") && !strings.Contains(string(stopOutput), "not found") && !strings.Contains(string(stopOutput), "does not exist") {
		return fmt.Errorf("stop Herdr session: %s", boundedRuntimeError(stopOutput))
	}
	command := exec.CommandContext(ctx, r.herdrPath, "session", "delete", steward.HerdrSession)
	output, err := command.CombinedOutput()
	if err != nil && !strings.Contains(string(output), "not found") && !strings.Contains(string(output), "not_found") && !strings.Contains(string(output), "does not exist") {
		return fmt.Errorf("delete Herdr session: %s", boundedRuntimeError(output))
	}
	return nil
}

func (r *HerdrStewardRuntime) Close() {
	r.mu.Lock()
	processes := make([]stewardServerProcess, 0, len(r.servers))
	for _, process := range r.servers {
		processes = append(processes, process)
	}
	r.servers = map[string]stewardServerProcess{}
	r.mu.Unlock()
	for _, process := range processes {
		_ = process.input.Close()
		if process.command.Process != nil {
			_ = process.command.Process.Kill()
		}
	}
}

func (r *HerdrStewardRuntime) startServer(ctx context.Context, steward HostedSteward) error {
	logDirectory := filepath.Join(r.dataDir, "rooms", steward.RoomID)
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(logDirectory, "steward-herdr.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	command := exec.Command(r.herdrPath, "--session", steward.HerdrSession, "server")
	command.Stdout, command.Stderr = logFile, logFile
	input, err := command.StdinPipe()
	if err != nil {
		_ = logFile.Close()
		return fmt.Errorf("open Herdr server input: %w", err)
	}
	if err := command.Start(); err != nil {
		_ = input.Close()
		_ = logFile.Close()
		return fmt.Errorf("start Herdr server: %w", err)
	}
	r.mu.Lock()
	r.servers[steward.HerdrSession] = stewardServerProcess{command: command, input: input}
	r.mu.Unlock()
	go func() {
		_ = command.Wait()
		_ = logFile.Close()
		r.mu.Lock()
		if process, exists := r.servers[steward.HerdrSession]; exists && process.command == command {
			delete(r.servers, steward.HerdrSession)
		}
		r.mu.Unlock()
	}()

	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		output, runErr := r.run(ctx, steward.HerdrSession, "status", "server")
		if runErr == nil && (bytes.Contains(output, []byte(`"running":true`)) || bytes.Contains(output, []byte("status: running"))) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return errors.New("Herdr server did not become ready")
}

func (r *HerdrStewardRuntime) waitForAgent(ctx context.Context, steward HostedSteward, timeout time.Duration) (StewardRuntimeState, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		state, err := r.Inspect(ctx, steward)
		if err == nil {
			return state, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return StewardRuntimeState{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return StewardRuntimeState{}, fmt.Errorf("Codex did not become inspectable: %w", lastErr)
}

func (r *HerdrStewardRuntime) waitForAgentReady(ctx context.Context, steward HostedSteward, timeout time.Duration) (StewardRuntimeState, error) {
	deadline := time.Now().Add(timeout)
	var state StewardRuntimeState
	var lastErr error
	var readySince time.Time
	for time.Now().Before(deadline) {
		state, lastErr = r.Inspect(ctx, steward)
		if lastErr == nil && (state.AgentStatus == "idle" || state.AgentStatus == "done") {
			if readySince.IsZero() {
				readySince = time.Now()
			} else if time.Since(readySince) >= 3*time.Second {
				return state, nil
			}
		} else {
			readySince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return StewardRuntimeState{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return StewardRuntimeState{}, fmt.Errorf("Codex did not become ready: %w", lastErr)
	}
	return StewardRuntimeState{}, fmt.Errorf("Codex did not become ready; last status was %q", state.AgentStatus)
}

func (r *HerdrStewardRuntime) run(ctx context.Context, session string, arguments ...string) ([]byte, error) {
	commandArguments := append([]string{"--session", session}, arguments...)
	command := exec.CommandContext(ctx, r.herdrPath, commandArguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("herdr %s: %s", strings.Join(arguments, " "), boundedRuntimeError(output))
	}
	return output, nil
}

func boundedRuntimeError(output []byte) string {
	value := strings.TrimSpace(strings.ToValidUTF8(string(output), "�"))
	if len(value) > 4096 {
		value = value[len(value)-4096:]
	}
	if value == "" {
		return "command failed without output"
	}
	return value
}
