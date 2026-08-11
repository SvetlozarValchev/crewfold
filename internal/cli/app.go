// Package cli implements Crewfold's human and machine-facing command surface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"crewfold/internal/buildinfo"
	"crewfold/internal/daemon"
	"crewfold/internal/localapi"
)

const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2

	errorSchema  = "urn:crewfold:schema:cli:error-response:v1"
	doctorSchema = "urn:crewfold:schema:cli:doctor-self-response:v1"
)

type outputMode string

const (
	outputText outputMode = "text"
	outputJSON outputMode = "json"
)

// App is a testable Crewfold command runner.
type App struct {
	stdout         io.Writer
	stderr         io.Writer
	info           buildinfo.Info
	executablePath func() (string, error)
	runDaemon      func(context.Context, daemon.Config) error
	newClient      func(string) daemonClient
}

type daemonClient interface {
	Status(context.Context) (localapi.StatusResult, error)
	Stop(context.Context) (localapi.StopResult, error)
}

// New constructs a command runner with no process-global output dependencies.
func New(stdout, stderr io.Writer, info buildinfo.Info) *App {
	return &App{
		stdout:         stdout,
		stderr:         stderr,
		info:           info,
		executablePath: os.Executable,
		runDaemon:      daemon.Run,
		newClient: func(socketPath string) daemonClient {
			return localapi.NewClient(socketPath)
		},
	}
}

// Run executes one command and returns a documented process exit code.
func (a *App) Run(args []string) int {
	return a.RunContext(context.Background(), args)
}

// RunContext executes one command with cancellation for long-running operations.
func (a *App) RunContext(ctx context.Context, args []string) int {
	mode, args, failure := extractOutputMode(args)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}

	if len(args) == 0 {
		a.writeRootHelp()
		return ExitOK
	}

	switch args[0] {
	case "help", "--help", "-h":
		return a.runHelp(args[1:])
	case "version", "--version":
		return a.runVersion(mode, args[1:])
	case "doctor":
		return a.runDoctor(mode, args[1:])
	case "daemon":
		return a.runDaemonCommand(ctx, mode, args[1:])
	case "status":
		return a.runStatus(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{
			exitCode: ExitUsage,
			code:     "unknown_command",
			message:  fmt.Sprintf("unknown command %q", args[0]),
			hint:     "run 'crewfold help' to list available commands",
		})
	}
}

func (a *App) runDaemonCommand(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure(
			"daemon requires a subcommand",
			"run 'crewfold help daemon' for usage",
		))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, daemonHelp)
		return ExitOK
	}

	switch args[0] {
	case "run":
		return a.runDaemonForeground(ctx, mode, args[1:])
	case "stop":
		return a.runDaemonStop(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{
			exitCode: ExitUsage,
			code:     "unknown_daemon_command",
			message:  fmt.Sprintf("unknown daemon command %q", args[0]),
			hint:     "run 'crewfold help daemon' for usage",
		})
	}
}

func (a *App) runDaemonForeground(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, daemonRunHelp)
		return ExitOK
	}
	options, failure := parseOptions(args, "data-dir", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	dataDir, failure := requiredOption(options, "data-dir")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}

	logger := slog.New(slog.NewJSONHandler(a.stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	err := a.runDaemon(ctx, daemon.Config{
		DataDir:    dataDir,
		SocketPath: socketPath,
		Version:    a.info,
		Logger:     logger,
	})
	if err == nil {
		return ExitOK
	}
	return a.writeFailure(mode, commandFailure{
		exitCode: ExitFailure,
		code:     daemon.ErrorCode(err),
		message:  err.Error(),
		hint:     daemonFailureHint(daemon.ErrorCode(err)),
	})
}

func (a *App) runDaemonStop(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, daemonStopHelp)
		return ExitOK
	}
	options, failure := parseOptions(args, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}

	result, err := a.newClient(socketPath).Stop(ctx)
	if err != nil {
		return a.writeClientFailure(mode, "stop daemon", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write daemon stop output", err))
		}
	} else {
		fmt.Fprintln(a.stdout, "daemon stopping")
	}
	return ExitOK
}

func (a *App) runStatus(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, statusHelp)
		return ExitOK
	}
	options, failure := parseOptions(args, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}

	result, err := a.newClient(socketPath).Status(ctx)
	if err != nil {
		return a.writeClientFailure(mode, "query daemon status", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write daemon status output", err))
		}
	} else {
		fmt.Fprintln(a.stdout, "Crewfold daemon")
		fmt.Fprintf(a.stdout, "status: %s\n", result.Status)
		fmt.Fprintf(a.stdout, "protocol: %d\n", result.Protocol)
		fmt.Fprintf(a.stdout, "version: %s\n", result.ServerVersion.Version)
		fmt.Fprintf(a.stdout, "pid: %d\n", result.PID)
		fmt.Fprintf(a.stdout, "started: %s\n", result.StartedAt)
		fmt.Fprintf(a.stdout, "uptime_ms: %d\n", result.UptimeMillis)
	}
	return ExitOK
}

func (a *App) runHelp(args []string) int {
	if len(args) == 0 {
		a.writeRootHelp()
		return ExitOK
	}
	if len(args) > 1 {
		return a.writeFailure(outputText, usageFailure(
			"help accepts at most one command name",
			"run 'crewfold help' to list available commands",
		))
	}

	switch args[0] {
	case "version":
		fmt.Fprint(a.stdout, versionHelp)
	case "doctor":
		fmt.Fprint(a.stdout, doctorHelp)
	case "daemon":
		fmt.Fprint(a.stdout, daemonHelp)
	case "status":
		fmt.Fprint(a.stdout, statusHelp)
	case "help":
		fmt.Fprint(a.stdout, helpHelp)
	default:
		return a.writeFailure(outputText, commandFailure{
			exitCode: ExitUsage,
			code:     "unknown_help_topic",
			message:  fmt.Sprintf("no help topic for %q", args[0]),
			hint:     "run 'crewfold help' to list available commands",
		})
	}
	return ExitOK
}

func (a *App) writeClientFailure(mode outputMode, operation string, err error) int {
	var apiError *localapi.APIError
	if errors.As(err, &apiError) {
		return a.writeFailure(mode, commandFailure{
			exitCode: ExitFailure,
			code:     apiError.Code,
			message:  fmt.Sprintf("%s: %s", operation, apiError.Message),
			hint:     "run 'crewfold status --socket <path>' to inspect daemon availability",
		})
	}
	return a.writeFailure(mode, commandFailure{
		exitCode: ExitFailure,
		code:     "daemon_unreachable",
		message:  fmt.Sprintf("%s: %v", operation, err),
		hint:     "verify the socket path and start the daemon with 'crewfold daemon run'",
	})
}

func (a *App) runVersion(mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, versionHelp)
		return ExitOK
	}
	if len(args) != 0 {
		return a.writeFailure(mode, usageFailure(
			"version does not accept positional arguments",
			"run 'crewfold help version' for usage",
		))
	}

	if mode == outputJSON {
		if err := writeJSON(a.stdout, a.info); err != nil {
			return a.writeFailure(outputText, internalFailure("write version output", err))
		}
		return ExitOK
	}

	fmt.Fprintf(a.stdout, "crewfold %s\n", a.info.Version)
	fmt.Fprintf(a.stdout, "commit: %s\n", a.info.Commit)
	fmt.Fprintf(a.stdout, "built: %s\n", a.info.BuiltAt)
	fmt.Fprintf(a.stdout, "go: %s\n", a.info.GoVersion)
	fmt.Fprintf(a.stdout, "platform: %s\n", a.info.Platform)
	return ExitOK
}

func (a *App) runDoctor(mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, doctorHelp)
		return ExitOK
	}
	if len(args) != 1 || args[0] != "--self" {
		return a.writeFailure(mode, usageFailure(
			"doctor requires --self in this milestone",
			"run 'crewfold help doctor' for usage",
		))
	}

	report := a.selfCheck()
	if mode == outputJSON {
		if err := writeJSON(a.stdout, report); err != nil {
			return a.writeFailure(outputText, internalFailure("write doctor output", err))
		}
	} else {
		fmt.Fprintln(a.stdout, "Crewfold self-check")
		for _, check := range report.Checks {
			fmt.Fprintf(a.stdout, "  %-12s %s", check.Name+":", check.Status)
			if check.Detail != "" {
				fmt.Fprintf(a.stdout, " — %s", check.Detail)
			}
			fmt.Fprintln(a.stdout)
		}
		fmt.Fprintf(a.stdout, "status: %s\n", report.Status)
	}

	if report.Status != "ok" {
		return ExitFailure
	}
	return ExitOK
}

func (a *App) selfCheck() doctorReport {
	checks := make([]doctorCheck, 0, 3)
	status := "ok"

	path, err := a.executablePath()
	if err != nil {
		status = "failed"
		checks = append(checks, doctorCheck{
			Name:   "executable",
			Status: "failed",
			Detail: err.Error(),
		})
	} else if strings.TrimSpace(path) == "" {
		status = "failed"
		checks = append(checks, doctorCheck{
			Name:   "executable",
			Status: "failed",
			Detail: "the operating system returned an empty executable path",
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "executable",
			Status: "ok",
			Detail: "current executable is discoverable",
		})
	}

	if err := a.info.Validate(); err != nil {
		status = "failed"
		checks = append(checks, doctorCheck{
			Name:   "build_info",
			Status: "failed",
			Detail: err.Error(),
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   "build_info",
			Status: "ok",
			Detail: "embedded metadata is internally consistent",
		})
	}

	platformStatus := "ok"
	platformDetail := a.info.Platform
	if strings.TrimSpace(a.info.Platform) == "" {
		status = "failed"
		platformStatus = "failed"
		platformDetail = "platform is empty"
	}
	checks = append(checks, doctorCheck{
		Name:   "platform",
		Status: platformStatus,
		Detail: platformDetail,
	})

	return doctorReport{
		Schema:  doctorSchema,
		Status:  status,
		Checks:  checks,
		Version: a.info,
	}
}

type doctorReport struct {
	Schema  string         `json:"schema"`
	Status  string         `json:"status"`
	Checks  []doctorCheck  `json:"checks"`
	Version buildinfo.Info `json:"version"`
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type commandFailure struct {
	exitCode int
	code     string
	message  string
	hint     string
}

type errorEnvelope struct {
	Schema string    `json:"schema"`
	Error  errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (a *App) writeFailure(mode outputMode, failure commandFailure) int {
	if mode == outputJSON {
		err := writeJSON(a.stderr, errorEnvelope{
			Schema: errorSchema,
			Error: errorBody{
				Code:    failure.code,
				Message: failure.message,
				Hint:    failure.hint,
			},
		})
		if err == nil {
			return failure.exitCode
		}
	}

	fmt.Fprintf(a.stderr, "error: %s\n", failure.message)
	if failure.hint != "" {
		fmt.Fprintf(a.stderr, "hint: %s\n", failure.hint)
	}
	return failure.exitCode
}

func extractOutputMode(args []string) (outputMode, []string, *commandFailure) {
	mode := outputText
	cleaned := make([]string, 0, len(args))
	found := false

	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--output" {
			if found {
				failure := usageFailure("--output may be specified only once", "use --output text or --output json")
				return mode, nil, &failure
			}
			if index+1 >= len(args) {
				failure := usageFailure("--output requires a value", "use --output text or --output json")
				return mode, nil, &failure
			}
			index++
			parsed, failure := parseOutputMode(args[index])
			if failure != nil {
				return mode, nil, failure
			}
			mode = parsed
			found = true
			continue
		}

		if strings.HasPrefix(argument, "--output=") {
			if found {
				failure := usageFailure("--output may be specified only once", "use --output text or --output json")
				return mode, nil, &failure
			}
			parsed, failure := parseOutputMode(strings.TrimPrefix(argument, "--output="))
			if failure != nil {
				return mode, nil, failure
			}
			mode = parsed
			found = true
			continue
		}

		cleaned = append(cleaned, argument)
	}

	return mode, cleaned, nil
}

func parseOutputMode(value string) (outputMode, *commandFailure) {
	switch outputMode(value) {
	case outputText:
		return outputText, nil
	case outputJSON:
		return outputJSON, nil
	default:
		failure := usageFailure(
			fmt.Sprintf("unsupported output format %q", value),
			"use --output text or --output json",
		)
		return outputText, &failure
	}
}

func parseOptions(args []string, allowedNames ...string) (map[string]string, *commandFailure) {
	allowed := make(map[string]struct{}, len(allowedNames))
	for _, name := range allowedNames {
		allowed[name] = struct{}{}
	}

	options := make(map[string]string, len(allowedNames))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if !strings.HasPrefix(argument, "--") {
			failure := usageFailure(
				fmt.Sprintf("unexpected positional argument %q", argument),
				"run the command with --help for usage",
			)
			return nil, &failure
		}

		nameValue := strings.TrimPrefix(argument, "--")
		name, value, hasValue := strings.Cut(nameValue, "=")
		if _, ok := allowed[name]; !ok {
			failure := usageFailure(
				fmt.Sprintf("unknown option --%s", name),
				"run the command with --help for usage",
			)
			return nil, &failure
		}
		if _, duplicate := options[name]; duplicate {
			failure := usageFailure(
				fmt.Sprintf("--%s may be specified only once", name),
				"remove the duplicate option",
			)
			return nil, &failure
		}

		if !hasValue {
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
				failure := usageFailure(
					fmt.Sprintf("--%s requires a value", name),
					"run the command with --help for usage",
				)
				return nil, &failure
			}
			index++
			value = args[index]
		}
		if strings.TrimSpace(value) == "" {
			failure := usageFailure(
				fmt.Sprintf("--%s requires a non-empty value", name),
				"run the command with --help for usage",
			)
			return nil, &failure
		}
		options[name] = value
	}

	return options, nil
}

func requiredOption(options map[string]string, name string) (string, *commandFailure) {
	value, ok := options[name]
	if ok {
		return value, nil
	}
	failure := usageFailure(
		fmt.Sprintf("--%s is required", name),
		"run the command with --help for usage",
	)
	return "", &failure
}

func daemonFailureHint(code string) string {
	switch code {
	case daemon.CodeDataDirInUse:
		return "stop the daemon using this data directory or select another --data-dir"
	case daemon.CodeSocketInUse:
		return "use 'crewfold status --socket <path>' or select another --socket"
	case daemon.CodeSocketPathOccupied:
		return "inspect the exact socket path; Crewfold will not remove a live or non-socket file"
	default:
		return "run 'crewfold doctor --self' and verify --data-dir and --socket"
	}
}

func usageFailure(message, hint string) commandFailure {
	return commandFailure{
		exitCode: ExitUsage,
		code:     "usage_error",
		message:  message,
		hint:     hint,
	}
}

func internalFailure(operation string, err error) commandFailure {
	return commandFailure{
		exitCode: ExitFailure,
		code:     "internal_error",
		message:  fmt.Sprintf("%s: %v", operation, err),
		hint:     "rerun with a writable output stream or report the failure",
	}
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func isHelp(value string) bool {
	return value == "--help" || value == "-h"
}

func (a *App) writeRootHelp() {
	fmt.Fprint(a.stdout, rootHelp)
}

const rootHelp = `Crewfold — the local-first control plane for agent crews

Usage:
  crewfold <command> [options]

Commands:
  version        Print build and platform information
  doctor --self  Run checks that need no daemon or external tools
  daemon run     Run the local daemon in the foreground
  daemon stop    Ask a running daemon to stop cleanly
  status         Query daemon health through its local socket
  help [command] Show command help

Global options:
  --output text|json  Select human or machine-readable output
  -h, --help          Show help

This M1 build keeps all daemon state in memory; it has no database, runtime, or
provider integration.
`

const versionHelp = `Usage:
  crewfold version [--output text|json]

Print embedded build metadata and Go runtime/platform information. The command
does not invoke Git or access the network.
`

const doctorHelp = `Usage:
  crewfold doctor --self [--output text|json]

Run checks for the current executable, embedded build metadata, and platform.
M0 self-checks do not inspect a daemon, database, runtime, or provider.
`

const daemonHelp = `Usage:
  crewfold daemon run --data-dir <path> --socket <path>
  crewfold daemon stop --socket <path>

Run the local daemon in the foreground or ask it to stop through the local API.
M1 has no background-service installer and writes no domain/database state.
`

const daemonRunHelp = `Usage:
  crewfold daemon run --data-dir <path> --socket <path> [--output text|json]

Run the local daemon in the foreground. Logs are newline-delimited JSON on stderr.
The socket is owner-only and the data directory is locked for this process.
`

const daemonStopHelp = `Usage:
  crewfold daemon stop --socket <path> [--output text|json]

Negotiate a protocol with the daemon, request graceful shutdown, and wait for its
acknowledgement.
`

const statusHelp = `Usage:
  crewfold status --socket <path> [--output text|json]

Negotiate a protocol and query the daemon's in-memory health and build metadata.
`

const helpHelp = `Usage:
  crewfold help [command]

Show root help or help for one command.
`
