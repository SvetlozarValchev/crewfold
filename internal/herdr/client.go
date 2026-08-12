// Package herdr implements the structured local automation boundary between
// Crewfold and an installed Herdr session.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const (
	SupportedSchemaVersion = 1
	SupportedProtocol      = 19
	maximumCommandOutput   = 2 * 1024 * 1024
)

var requiredMethods = []string{
	"pane.close",
	"pane.get",
	"pane.process_info",
	"pane.read",
	"pane.send_input",
	"pane.send_keys",
	"pane.send_text",
	"session.snapshot",
	"workspace.close",
	"workspace.create",
}

// CommandResult is the bounded result of one Herdr CLI command.
type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// CommandRunner makes the real CLI replaceable by recorded protocol fixtures.
type CommandRunner interface {
	Run(context.Context, string, []string, map[string]string) (CommandResult, error)
}

// ExecRunner invokes the installed Herdr CLI without a shell.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, executable string, arguments []string, environment map[string]string) (CommandResult, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = mergedEnvironment(os.Environ(), environment)
	stdout, stderr := &limitedBuffer{limit: maximumCommandOutput}, &limitedBuffer{limit: maximumCommandOutput}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return result, err
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = buffer.buffer.Write(value)
	}
	return written, nil
}

func (buffer *limitedBuffer) Bytes() []byte { return append([]byte(nil), buffer.buffer.Bytes()...) }

// Client uses Herdr's documented CLI wrappers. The CLI performs its own live
// client/server protocol guard before sending session commands.
type Client struct {
	executable string
	session    string
	runner     CommandRunner
}

func NewClient(executable, session string, runner CommandRunner) *Client {
	if strings.TrimSpace(executable) == "" {
		executable = "herdr"
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Client{executable: executable, session: strings.TrimSpace(session), runner: runner}
}

func (client *Client) Executable() string { return client.executable }
func (client *Client) Session() string    { return client.session }

type ProbeCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type ProbeReport struct {
	Schema        string       `json:"schema"`
	Runtime       string       `json:"runtime"`
	Status        string       `json:"status"`
	Binary        string       `json:"binary"`
	Version       string       `json:"version,omitempty"`
	Session       string       `json:"session"`
	SchemaVersion int          `json:"schema_version,omitempty"`
	Protocol      int          `json:"protocol,omitempty"`
	Checks        []ProbeCheck `json:"checks"`
}

const ProbeSchema = "urn:crewfold:schema:runtime:herdr-probe:v1"

func (report ProbeReport) Compatible() bool { return report.Status == "ok" }

func (report ProbeReport) Error() error {
	if report.Compatible() {
		return nil
	}
	for _, check := range report.Checks {
		if check.Status == "failed" {
			return fmt.Errorf("Herdr %s check failed: %s", check.Name, check.Detail)
		}
	}
	return errors.New("Herdr compatibility check failed")
}

// Probe verifies the binary, installed schema, and selected live session.
func (client *Client) Probe(ctx context.Context) ProbeReport {
	report := ProbeReport{
		Schema: ProbeSchema, Runtime: "herdr", Status: "ok", Binary: client.executable,
		Session: client.sessionName(), Checks: make([]ProbeCheck, 0, 3),
	}

	versionResult, err := client.run(ctx, "--version")
	if err != nil {
		report.fail("binary", err.Error())
		return report
	}
	version := strings.TrimSpace(string(versionResult.Stdout))
	if version == "" {
		report.fail("binary", "Herdr returned an empty version")
		return report
	}
	report.Version = version
	report.pass("binary", version)

	schemaResult, err := client.run(ctx, "api", "schema", "--json")
	if err != nil {
		report.fail("schema", err.Error())
		return report
	}
	schemaVersion, protocol, missing, err := inspectSchema(schemaResult.Stdout)
	report.SchemaVersion, report.Protocol = schemaVersion, protocol
	if err != nil {
		report.fail("schema", err.Error())
		return report
	}
	if schemaVersion != SupportedSchemaVersion || protocol != SupportedProtocol || len(missing) != 0 {
		detail := fmt.Sprintf("installed schema_version=%d protocol=%d; Crewfold requires schema_version=%d protocol=%d", schemaVersion, protocol, SupportedSchemaVersion, SupportedProtocol)
		if len(missing) != 0 {
			detail += "; missing methods: " + strings.Join(missing, ", ")
		}
		detail += "; install a compatible Herdr release or update the Crewfold runtime driver"
		report.fail("schema", detail)
		return report
	}
	report.pass("schema", fmt.Sprintf("schema_version=%d protocol=%d", schemaVersion, protocol))

	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		report.fail("session", err.Error())
		return report
	}
	if snapshot.Protocol != protocol {
		report.fail("session", fmt.Sprintf("running server protocol=%d does not match installed CLI protocol=%d; restart the Herdr session", snapshot.Protocol, protocol))
		return report
	}
	report.pass("session", fmt.Sprintf("%s is reachable with %d workspace(s)", report.Session, len(snapshot.Workspaces)))
	return report
}

func (report *ProbeReport) pass(name, detail string) {
	report.Checks = append(report.Checks, ProbeCheck{Name: name, Status: "ok", Detail: detail})
}

func (report *ProbeReport) fail(name, detail string) {
	report.Status = "failed"
	report.Checks = append(report.Checks, ProbeCheck{Name: name, Status: "failed", Detail: detail})
}

func inspectSchema(data []byte) (int, int, []string, error) {
	if len(data) == 0 || len(data) > maximumCommandOutput {
		return 0, 0, nil, errors.New("installed Herdr schema is empty or exceeds 2 MiB")
	}
	var document struct {
		SchemaVersion int                        `json:"schema_version"`
		Protocol      int                        `json:"protocol"`
		Schemas       map[string]json.RawMessage `json:"schemas"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return 0, 0, nil, fmt.Errorf("decode installed API schema: %w", err)
	}
	for _, name := range []string{"request", "success_response", "error_response", "event", "subscription_event"} {
		if len(document.Schemas[name]) == 0 {
			return document.SchemaVersion, document.Protocol, nil, fmt.Errorf("installed API schema omits %q", name)
		}
	}
	var request any
	if err := json.Unmarshal(document.Schemas["request"], &request); err != nil {
		return document.SchemaVersion, document.Protocol, nil, fmt.Errorf("decode request schema: %w", err)
	}
	constants := make(map[string]bool)
	collectSchemaConstants(request, constants)
	missing := make([]string, 0)
	for _, method := range requiredMethods {
		if !constants[method] {
			missing = append(missing, method)
		}
	}
	sort.Strings(missing)
	return document.SchemaVersion, document.Protocol, missing, nil
}

func collectSchemaConstants(value any, constants map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "const" {
				if text, ok := child.(string); ok {
					constants[text] = true
				}
			}
			collectSchemaConstants(child, constants)
		}
	case []any:
		for _, child := range typed {
			collectSchemaConstants(child, constants)
		}
	}
}

type WorkspaceInfo struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type TabInfo struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
}

type PaneInfo struct {
	PaneID      string `json:"pane_id"`
	TerminalID  string `json:"terminal_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	Label       string `json:"label,omitempty"`
}

type Snapshot struct {
	Version    string          `json:"version"`
	Protocol   int             `json:"protocol"`
	Workspaces []WorkspaceInfo `json:"workspaces"`
	Tabs       []TabInfo       `json:"tabs"`
	Panes      []PaneInfo      `json:"panes"`
}

type WorkspaceSurface struct {
	Workspace WorkspaceInfo
	Tab       TabInfo
	Pane      PaneInfo
}

type Process struct {
	PID     int      `json:"pid"`
	Name    string   `json:"name"`
	Argv    []string `json:"argv,omitempty"`
	Cmdline string   `json:"cmdline,omitempty"`
}

type ProcessInfo struct {
	PaneID              string    `json:"pane_id"`
	ForegroundProcesses []Process `json:"foreground_processes"`
}

func (client *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	result, err := client.run(ctx, "api", "snapshot")
	if err != nil {
		return Snapshot{}, err
	}
	var envelope struct {
		Result struct {
			Type     string   `json:"type"`
			Snapshot Snapshot `json:"snapshot"`
		} `json:"result"`
	}
	if err := decodeJSON(result.Stdout, &envelope); err != nil {
		return Snapshot{}, fmt.Errorf("decode Herdr session snapshot: %w", err)
	}
	if envelope.Result.Type != "session_snapshot" || envelope.Result.Snapshot.Protocol == 0 {
		return Snapshot{}, errors.New("Herdr session snapshot has an unexpected result shape")
	}
	return envelope.Result.Snapshot, nil
}

func (client *Client) CreateWorkspace(ctx context.Context, cwd, label string, environment map[string]string) (WorkspaceSurface, error) {
	arguments := []string{"workspace", "create", "--cwd", cwd, "--label", label, "--no-focus"}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		arguments = append(arguments, "--env", name+"="+environment[name])
	}
	result, err := client.run(ctx, arguments...)
	if err != nil {
		return WorkspaceSurface{}, err
	}
	var envelope struct {
		Result struct {
			Type      string        `json:"type"`
			Workspace WorkspaceInfo `json:"workspace"`
			Tab       TabInfo       `json:"tab"`
			RootPane  PaneInfo      `json:"root_pane"`
		} `json:"result"`
	}
	if err := decodeJSON(result.Stdout, &envelope); err != nil {
		return WorkspaceSurface{}, fmt.Errorf("decode Herdr workspace creation: %w", err)
	}
	if envelope.Result.Type != "workspace_created" || envelope.Result.Workspace.WorkspaceID == "" || envelope.Result.Tab.TabID == "" || envelope.Result.RootPane.PaneID == "" || envelope.Result.RootPane.TerminalID == "" {
		return WorkspaceSurface{}, errors.New("Herdr workspace creation has an unexpected result shape")
	}
	return WorkspaceSurface{Workspace: envelope.Result.Workspace, Tab: envelope.Result.Tab, Pane: envelope.Result.RootPane}, nil
}

func (client *Client) ProcessInfo(ctx context.Context, paneID string) (ProcessInfo, error) {
	result, err := client.run(ctx, "pane", "process-info", "--pane", paneID)
	if err != nil {
		return ProcessInfo{}, err
	}
	var envelope struct {
		Result struct {
			Type        string      `json:"type"`
			ProcessInfo ProcessInfo `json:"process_info"`
		} `json:"result"`
	}
	if err := decodeJSON(result.Stdout, &envelope); err != nil {
		return ProcessInfo{}, fmt.Errorf("decode Herdr pane process info: %w", err)
	}
	if envelope.Result.Type != "pane_process_info" || envelope.Result.ProcessInfo.PaneID == "" {
		return ProcessInfo{}, errors.New("Herdr pane process info has an unexpected result shape")
	}
	return envelope.Result.ProcessInfo, nil
}

func (client *Client) RunPane(ctx context.Context, paneID, command string) error {
	_, err := client.run(ctx, "pane", "run", paneID, command)
	return err
}

func (client *Client) SendText(ctx context.Context, paneID, value string) error {
	_, err := client.run(ctx, "pane", "send-text", paneID, value)
	return err
}

func (client *Client) SendKeys(ctx context.Context, paneID string, keys ...string) error {
	arguments := append([]string{"pane", "send-keys", paneID}, keys...)
	_, err := client.run(ctx, arguments...)
	return err
}

func (client *Client) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	arguments := []string{"pane", "read", paneID, "--source", "recent-unwrapped"}
	if lines > 0 {
		arguments = append(arguments, "--lines", strconv.Itoa(lines))
	}
	result, err := client.run(ctx, arguments...)
	if err != nil {
		return "", err
	}
	return string(result.Stdout), nil
}

func (client *Client) ClosePane(ctx context.Context, paneID string) error {
	_, err := client.run(ctx, "pane", "close", paneID)
	return err
}

func (client *Client) CloseWorkspace(ctx context.Context, workspaceID string) error {
	_, err := client.run(ctx, "workspace", "close", workspaceID)
	return err
}

// PaneByTerminal resolves the current public pane ID after arbitrary layout
// moves. Herdr terminal IDs remain stable across those moves.
func PaneByTerminal(snapshot Snapshot, terminalID string) (PaneInfo, bool) {
	for _, pane := range snapshot.Panes {
		if pane.TerminalID == terminalID {
			return pane, true
		}
	}
	return PaneInfo{}, false
}

func (client *Client) sessionName() string {
	if client.session == "" {
		return "default"
	}
	return client.session
}

func (client *Client) run(ctx context.Context, arguments ...string) (CommandResult, error) {
	environment := map[string]string{}
	if client.session != "" {
		environment["HERDR_SESSION"] = client.session
	}
	result, err := client.runner.Run(ctx, client.executable, arguments, environment)
	if err != nil {
		return result, fmt.Errorf("run Herdr %s: %w", strings.Join(arguments, " "), err)
	}
	if result.ExitCode == 0 {
		return result, nil
	}
	return result, commandError(arguments, result)
}

type CommandError struct {
	Code      string
	Message   string
	ExitCode  int
	Arguments []string
}

func (failure *CommandError) Error() string {
	return fmt.Sprintf("Herdr %s failed (%s): %s", strings.Join(failure.Arguments, " "), failure.Code, failure.Message)
}

func commandError(arguments []string, result CommandResult) error {
	body := bytes.TrimSpace(result.Stderr)
	if len(body) == 0 {
		body = bytes.TrimSpace(result.Stdout)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Code != "" {
		return &CommandError{Code: envelope.Error.Code, Message: envelope.Error.Message, ExitCode: result.ExitCode, Arguments: append([]string(nil), arguments...)}
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = fmt.Sprintf("exit status %d", result.ExitCode)
	}
	return &CommandError{Code: "command_failed", Message: message, ExitCode: result.ExitCode, Arguments: append([]string(nil), arguments...)}
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func mergedEnvironment(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		if name, value, ok := strings.Cut(entry, "="); ok {
			values[name] = value
		}
	}
	for name, value := range overrides {
		values[name] = value
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
