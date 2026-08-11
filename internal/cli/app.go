// Package cli implements Crewfold's human and machine-facing command surface.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"crewfold/internal/buildinfo"
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
}

// New constructs a command runner with no process-global output dependencies.
func New(stdout, stderr io.Writer, info buildinfo.Info) *App {
	return &App{
		stdout:         stdout,
		stderr:         stderr,
		info:           info,
		executablePath: os.Executable,
	}
}

// Run executes one command and returns a documented process exit code.
func (a *App) Run(args []string) int {
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
	default:
		return a.writeFailure(mode, commandFailure{
			exitCode: ExitUsage,
			code:     "unknown_command",
			message:  fmt.Sprintf("unknown command %q", args[0]),
			hint:     "run 'crewfold help' to list available commands",
		})
	}
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
  help [command] Show command help

Global options:
  --output text|json  Select human or machine-readable output
  -h, --help          Show help

This M0 build does not include a daemon, database, runtime, or provider.
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

const helpHelp = `Usage:
  crewfold help [command]

Show root help or help for one command.
`
