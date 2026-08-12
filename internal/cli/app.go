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
	"os/exec"
	"strconv"
	"strings"

	"crewfold/internal/buildinfo"
	"crewfold/internal/daemon"
	"crewfold/internal/herdr"
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
	probeHerdr     func(context.Context, string, string) herdr.ProbeReport
	runInteractive func(context.Context, localapi.RunAttachResult) error
}

type daemonClient interface {
	Status(context.Context) (localapi.StatusResult, error)
	Stop(context.Context) (localapi.StopResult, error)
	DatabaseStatus(context.Context) (localapi.DatabaseStatusResult, error)
	WorkspaceInit(context.Context, string, string) (localapi.WorkspaceInitResult, error)
	WorkspaceShow(context.Context, string) (localapi.WorkspaceShowResult, error)
	ProjectAdd(context.Context, string, string, string, string, string) (localapi.ProjectAddResult, error)
	ProjectInspect(context.Context, string, string) (localapi.ProjectInspectResult, error)
	CheckoutAdd(context.Context, string, string, string, string, string) (localapi.CheckoutAddResult, error)
	CheckoutList(context.Context, string, string) (localapi.CheckoutListResult, error)
	AgentCreate(context.Context, localapi.AgentCreateParams) (localapi.AgentMutationResult, error)
	AgentUpdate(context.Context, localapi.AgentUpdateParams) (localapi.AgentMutationResult, error)
	AgentShow(context.Context, string, string) (localapi.AgentShowResult, error)
	AgentList(context.Context, string) (localapi.AgentListResult, error)
	ObjectiveCreate(context.Context, localapi.ObjectiveCreateParams) (localapi.ObjectiveMutationResult, error)
	ObjectiveUpdate(context.Context, localapi.ObjectiveUpdateParams) (localapi.ObjectiveMutationResult, error)
	ObjectiveShow(context.Context, string, string) (localapi.ObjectiveShowResult, error)
	ObjectiveList(context.Context, string, string) (localapi.ObjectiveListResult, error)
	TaskCreate(context.Context, localapi.TaskCreateParams) (localapi.TaskMutationResult, error)
	TaskUpdate(context.Context, localapi.TaskUpdateParams) (localapi.TaskMutationResult, error)
	TaskShow(context.Context, string, string) (localapi.TaskShowResult, error)
	TaskList(context.Context, string, string, bool) (localapi.TaskListResult, error)
	TaskDepend(context.Context, localapi.TaskDependencyParams) (localapi.TaskMutationResult, error)
	TaskAssign(context.Context, localapi.TaskAssignParams) (localapi.TaskMutationResult, error)
	TaskTransition(context.Context, localapi.TaskTransitionParams) (localapi.TaskMutationResult, error)
	TaskTimeline(context.Context, string, string) (localapi.TaskTimelineResult, error)
	ContextBuild(context.Context, localapi.ContextBuildParams) (localapi.ContextBuildResult, error)
	ContextShow(context.Context, string, string) (localapi.ContextShowResult, error)
	ContextExplain(context.Context, string, string) (localapi.ContextExplainResult, error)
	MessageSend(context.Context, localapi.MessageSendParams) (localapi.MessageSendResult, error)
	InboxList(context.Context, string, string, int) (localapi.InboxListResult, error)
	ThreadShow(context.Context, string, string) (localapi.ThreadShowResult, error)
	RunStart(context.Context, localapi.RunStartParams) (localapi.RunMutationResult, error)
	RunShow(context.Context, string, string) (localapi.RunShowResult, error)
	RunList(context.Context, string, string, string) (localapi.RunListResult, error)
	RunResume(context.Context, localapi.RunResumeParams) (localapi.RunMutationResult, error)
	RunStop(context.Context, localapi.RunStopParams) (localapi.RunMutationResult, error)
	RunLogs(context.Context, string, string, int) (localapi.RunLogsResult, error)
	RunPrompt(context.Context, string, string, string) (localapi.RunControlResult, error)
	RunInterrupt(context.Context, string, string) (localapi.RunControlResult, error)
	RunAttach(context.Context, string, string, bool) (localapi.RunAttachResult, error)
	CoordinationStatus(context.Context, string) (localapi.CoordinationStatusResult, error)
	EventsList(context.Context, int64, int) (localapi.EventsListResult, error)
}

// New constructs a command runner with no process-global output dependencies.
func New(stdout, stderr io.Writer, info buildinfo.Info) *App {
	return &App{
		stdout:         stdout,
		stderr:         stderr,
		info:           info,
		executablePath: os.Executable,
		runDaemon:      daemon.Run,
		probeHerdr: func(ctx context.Context, executable, session string) herdr.ProbeReport {
			return herdr.NewClient(executable, session, nil).Probe(ctx)
		},
		runInteractive: runAttachedProcess,
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
		return a.runDoctor(ctx, mode, args[1:])
	case "daemon":
		return a.runDaemonCommand(ctx, mode, args[1:])
	case "status":
		return a.runStatus(ctx, mode, args[1:])
	case "workspace":
		return a.runWorkspace(ctx, mode, args[1:])
	case "project":
		return a.runProject(ctx, mode, args[1:])
	case "checkout":
		return a.runCheckout(ctx, mode, args[1:])
	case "agent":
		return a.runAgent(ctx, mode, args[1:])
	case "objective":
		return a.runObjective(ctx, mode, args[1:])
	case "task":
		return a.runTask(ctx, mode, args[1:])
	case "context":
		return a.runContext(ctx, mode, args[1:])
	case "message":
		return a.runMessage(ctx, mode, args[1:])
	case "inbox":
		return a.runInbox(ctx, mode, args[1:])
	case "thread":
		return a.runThread(ctx, mode, args[1:])
	case "run":
		return a.runRun(ctx, mode, args[1:])
	case "events":
		return a.runEvents(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{
			exitCode: ExitUsage,
			code:     "unknown_command",
			message:  fmt.Sprintf("unknown command %q", args[0]),
			hint:     "run 'crewfold help' to list available commands",
		})
	}
}

func (a *App) runProject(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("project requires a subcommand", "run 'crewfold help project' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, projectHelp)
		return ExitOK
	}
	switch args[0] {
	case "add":
		return a.runProjectAdd(ctx, mode, args[1:])
	case "inspect":
		return a.runProjectInspect(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_project_command", message: fmt.Sprintf("unknown project command %q", args[0]), hint: "run 'crewfold help project' for usage"})
	}
}

func (a *App) runProjectAdd(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, projectAddHelp)
		return ExitOK
	}
	name, optionArgs, failure := requiredLeadingArgument(args, "project name")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "repo", "mode", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, failure := requiredOption(options, "workspace")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	repositoryPath, failure := requiredOption(options, "repo")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socketPath).ProjectAdd(ctx, workspace, name, repositoryPath, options["mode"], options["idempotency-key"])
	if err != nil {
		return a.writeClientFailure(mode, "register project", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write project registration output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "project: %s (%s)\n", result.Project.Name, result.Project.ID)
		fmt.Fprintf(a.stdout, "repository: %s\n", result.Repository.ID)
		fmt.Fprintf(a.stdout, "checkout: %s (%s)\n", result.Checkout.Path, result.Checkout.ID)
		fmt.Fprintf(a.stdout, "kind: %s\n", result.Checkout.CheckoutKind)
	}
	return ExitOK
}

func (a *App) runProjectInspect(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, projectInspectHelp)
		return ExitOK
	}
	project, optionArgs, failure := requiredLeadingArgument(args, "project name or ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, failure := requiredOption(options, "workspace")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socketPath).ProjectInspect(ctx, workspace, project)
	if err != nil {
		return a.writeClientFailure(mode, "inspect project", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write project inspection output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "project: %s (%s)\n", result.Project.Name, result.Project.ID)
		fmt.Fprintf(a.stdout, "repositories: %d\n", len(result.Repositories))
		for _, checkout := range result.Checkouts {
			fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\tdirty=%t\n", checkout.ID, checkout.Path, checkout.Availability, checkout.Branch, checkout.Dirty)
			if checkout.DiagnosticCode != "" {
				fmt.Fprintf(a.stdout, "  diagnostic: %s: %s\n", checkout.DiagnosticCode, checkout.Diagnostic)
			}
		}
	}
	return ExitOK
}

func (a *App) runCheckout(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("checkout requires a subcommand", "run 'crewfold help checkout' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, checkoutHelp)
		return ExitOK
	}
	switch args[0] {
	case "add":
		return a.runCheckoutAdd(ctx, mode, args[1:])
	case "list":
		return a.runCheckoutList(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_checkout_command", message: fmt.Sprintf("unknown checkout command %q", args[0]), hint: "run 'crewfold help checkout' for usage"})
	}
}

func (a *App) runCheckoutAdd(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, checkoutAddHelp)
		return ExitOK
	}
	if len(args) < 2 || strings.HasPrefix(args[0], "--") || strings.HasPrefix(args[1], "--") {
		return a.writeFailure(mode, usageFailure("checkout add requires a project and repository path", "run 'crewfold help checkout' for usage"))
	}
	project, repositoryPath := args[0], args[1]
	options, failure := parseOptions(args[2:], "workspace", "mode", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, failure := requiredOption(options, "workspace")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socketPath).CheckoutAdd(ctx, workspace, project, repositoryPath, options["mode"], options["idempotency-key"])
	if err != nil {
		return a.writeClientFailure(mode, "register checkout", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write checkout registration output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "checkout: %s (%s)\n", result.Checkout.Path, result.Checkout.ID)
		fmt.Fprintf(a.stdout, "repository: %s\n", result.Repository.ID)
		fmt.Fprintf(a.stdout, "kind: %s\n", result.Checkout.CheckoutKind)
	}
	return ExitOK
}

func (a *App) runCheckoutList(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, checkoutListHelp)
		return ExitOK
	}
	project, optionArgs, failure := requiredLeadingArgument(args, "project name or ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, failure := requiredOption(options, "workspace")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socketPath).CheckoutList(ctx, workspace, project)
	if err != nil {
		return a.writeClientFailure(mode, "list checkouts", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write checkout list output", err))
		}
	} else if len(result.Checkouts) == 0 {
		fmt.Fprintf(a.stdout, "no checkouts registered for %s\n", result.Project.Name)
	} else {
		for _, checkout := range result.Checkouts {
			fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\t%s\n", checkout.ID, checkout.Path, checkout.CheckoutKind, checkout.WriteMode, checkout.Availability)
		}
	}
	return ExitOK
}

func (a *App) runWorkspace(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("workspace requires a subcommand", "run 'crewfold help workspace' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, workspaceHelp)
		return ExitOK
	}
	switch args[0] {
	case "init":
		return a.runWorkspaceInit(ctx, mode, args[1:])
	case "show":
		return a.runWorkspaceShow(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{
			exitCode: ExitUsage,
			code:     "unknown_workspace_command",
			message:  fmt.Sprintf("unknown workspace command %q", args[0]),
			hint:     "run 'crewfold help workspace' for usage",
		})
	}
}

func (a *App) runWorkspaceInit(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, workspaceInitHelp)
		return ExitOK
	}
	name, optionArgs, failure := requiredLeadingArgument(args, "workspace name")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}

	result, err := a.newClient(socketPath).WorkspaceInit(ctx, name, options["idempotency-key"])
	if err != nil {
		return a.writeClientFailure(mode, "initialize workspace", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write workspace initialization output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "workspace: %s (%s)\n", result.Workspace.Name, result.Workspace.ID)
		fmt.Fprintf(a.stdout, "revision: %d\n", result.Workspace.Revision)
		fmt.Fprintf(a.stdout, "event: %s (sequence %d)\n", result.EventID, result.EventSequence)
	}
	return ExitOK
}

func (a *App) runWorkspaceShow(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, workspaceShowHelp)
		return ExitOK
	}
	identifier, optionArgs, failure := requiredLeadingArgument(args, "workspace name or ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}

	result, err := a.newClient(socketPath).WorkspaceShow(ctx, identifier)
	if err != nil {
		return a.writeClientFailure(mode, "show workspace", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write workspace output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "workspace: %s\n", result.Workspace.Name)
		fmt.Fprintf(a.stdout, "id: %s\n", result.Workspace.ID)
		fmt.Fprintf(a.stdout, "revision: %d\n", result.Workspace.Revision)
		fmt.Fprintf(a.stdout, "created: %s\n", result.Workspace.CreatedAt)
		fmt.Fprintf(a.stdout, "updated: %s\n", result.Workspace.UpdatedAt)
	}
	return ExitOK
}

func (a *App) runEvents(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("events requires a subcommand", "run 'crewfold help events' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, eventsHelp)
		return ExitOK
	}
	if args[0] != "list" {
		return a.writeFailure(mode, commandFailure{
			exitCode: ExitUsage,
			code:     "unknown_events_command",
			message:  fmt.Sprintf("unknown events command %q", args[0]),
			hint:     "run 'crewfold help events' for usage",
		})
	}
	if len(args) == 2 && isHelp(args[1]) {
		fmt.Fprint(a.stdout, eventsListHelp)
		return ExitOK
	}
	options, failure := parseOptions(args[1:], "socket", "after", "limit")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	afterValue, failure := requiredOption(options, "after")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	after, parseErr := strconv.ParseInt(afterValue, 10, 64)
	if parseErr != nil || after < 0 {
		return a.writeFailure(mode, usageFailure("--after must be a non-negative integer", "use an event sequence such as --after 0"))
	}
	limit := 0
	if value := options["limit"]; value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 1000 {
			return a.writeFailure(mode, usageFailure("--limit must be an integer from 1 to 1000", "omit --limit to use 100"))
		}
		limit = parsed
	}

	result, err := a.newClient(socketPath).EventsList(ctx, after, limit)
	if err != nil {
		return a.writeClientFailure(mode, "list events", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write event list output", err))
		}
	} else if len(result.Events) == 0 {
		fmt.Fprintf(a.stdout, "no events after sequence %d\n", result.After)
	} else {
		for _, event := range result.Events {
			fmt.Fprintf(a.stdout, "%d\t%s\t%s\trevision %d\n", event.Sequence, event.Type, event.Entity.ID, event.Entity.Revision)
		}
		fmt.Fprintf(a.stdout, "next_after: %d\n", result.NextAfter)
		fmt.Fprintf(a.stdout, "has_more: %t\n", result.HasMore)
	}
	return ExitOK
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
	options, failure := parseOptions(args, "data-dir", "socket", "herdr-binary", "herdr-session")
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
		DataDir:         dataDir,
		SocketPath:      socketPath,
		Version:         a.info,
		Logger:          logger,
		HerdrExecutable: options["herdr-binary"],
		HerdrSession:    options["herdr-session"],
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
	options, failure := parseOptions(args, "socket", "workspace")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}

	client := a.newClient(socketPath)
	if workspace := options["workspace"]; workspace != "" {
		result, err := client.CoordinationStatus(ctx, workspace)
		if err != nil {
			return a.writeClientFailure(mode, "query coordination status", err)
		}
		if mode == outputJSON {
			if err := writeJSON(a.stdout, result); err != nil {
				return a.writeFailure(outputText, internalFailure("write coordination status output", err))
			}
		} else {
			fmt.Fprintf(a.stdout, "Crewfold workspace: %s\n", result.Workspace)
			fmt.Fprintf(a.stdout, "agents: %d registered, %d enabled\n", result.Status.AgentsRegistered, result.Status.AgentsEnabled)
			fmt.Fprintf(a.stdout, "tasks: %d registered, %d ready, %d assigned, %d active, %d blocked, %d completed, %d cancelled\n", result.Status.TasksRegistered, result.Status.TasksReady, result.Status.TasksAssigned, result.Status.TasksActive, result.Status.TasksBlocked, result.Status.TasksCompleted, result.Status.TasksCancelled)
		}
		return ExitOK
	}

	result, err := client.Status(ctx)
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
	case "workspace":
		fmt.Fprint(a.stdout, workspaceHelp)
	case "project":
		fmt.Fprint(a.stdout, projectHelp)
	case "checkout":
		fmt.Fprint(a.stdout, checkoutHelp)
	case "agent":
		fmt.Fprint(a.stdout, agentHelp)
	case "objective":
		fmt.Fprint(a.stdout, objectiveHelp)
	case "task":
		fmt.Fprint(a.stdout, taskHelp)
	case "context":
		fmt.Fprint(a.stdout, contextHelp)
	case "message":
		fmt.Fprint(a.stdout, messageHelp)
	case "inbox":
		fmt.Fprint(a.stdout, inboxHelp)
	case "thread":
		fmt.Fprint(a.stdout, threadHelp)
	case "run":
		fmt.Fprint(a.stdout, runHelp)
	case "events":
		fmt.Fprint(a.stdout, eventsHelp)
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
			hint:     clientFailureHint(apiError.Code),
		})
	}
	return a.writeFailure(mode, commandFailure{
		exitCode: ExitFailure,
		code:     "daemon_unreachable",
		message:  fmt.Sprintf("%s: %v", operation, err),
		hint:     "verify the socket path and start the daemon with 'crewfold daemon run'",
	})
}

func clientFailureHint(code string) string {
	switch code {
	case "workspace_already_exists":
		return "use 'crewfold workspace show <name> --socket <path>' or choose another name"
	case "workspace_not_found":
		return "verify the workspace name or stable ID"
	case "idempotency_conflict":
		return "retry the original command payload or choose a new idempotency key"
	case "invalid_workspace", "invalid_request":
		return "check the command arguments and run the command with --help"
	case "storage_failed":
		return "run 'crewfold doctor --database --socket <path>' and inspect daemon logs"
	default:
		return "run 'crewfold status --socket <path>' to inspect daemon availability"
	}
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

func (a *App) runDoctor(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, doctorHelp)
		return ExitOK
	}
	if len(args) > 0 && args[0] == "--database" {
		return a.runDatabaseDoctor(ctx, mode, args[1:])
	}
	if len(args) > 0 && args[0] == "--runtime" {
		return a.runRuntimeDoctor(ctx, mode, args[1:])
	}
	if len(args) != 1 || args[0] != "--self" {
		return a.writeFailure(mode, usageFailure(
			"doctor requires --self, --database, or --runtime herdr",
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

func (a *App) runRuntimeDoctor(ctx context.Context, mode outputMode, args []string) int {
	runtimeName, optionArgs, failure := requiredLeadingArgument(args, "runtime name")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	if runtimeName != "herdr" {
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("unsupported runtime doctor %q", runtimeName), "use --runtime herdr"))
	}
	options, failure := parseOptions(optionArgs, "herdr-binary", "herdr-session")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	executable := options["herdr-binary"]
	if executable == "" {
		executable = strings.TrimSpace(os.Getenv("CREWFOLD_HERDR_BINARY"))
	}
	if executable == "" {
		executable = "herdr"
	}
	session := options["herdr-session"]
	if session == "" {
		session = strings.TrimSpace(os.Getenv("CREWFOLD_HERDR_SESSION"))
	}
	if session == "" {
		session = strings.TrimSpace(os.Getenv("HERDR_SESSION"))
	}
	report := a.probeHerdr(ctx, executable, session)
	if mode == outputJSON {
		if err := writeJSON(a.stdout, report); err != nil {
			return a.writeFailure(outputText, internalFailure("write Herdr doctor output", err))
		}
	} else {
		fmt.Fprintln(a.stdout, "Crewfold Herdr runtime")
		for _, check := range report.Checks {
			fmt.Fprintf(a.stdout, "  %-12s %s", check.Name+":", check.Status)
			if check.Detail != "" {
				fmt.Fprintf(a.stdout, " — %s", check.Detail)
			}
			fmt.Fprintln(a.stdout)
		}
		fmt.Fprintf(a.stdout, "status: %s\n", report.Status)
	}
	if !report.Compatible() {
		return ExitFailure
	}
	return ExitOK
}

func (a *App) runDatabaseDoctor(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socketPath).DatabaseStatus(ctx)
	if err != nil {
		return a.writeClientFailure(mode, "check database", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write database doctor output", err))
		}
	} else {
		fmt.Fprintln(a.stdout, "Crewfold database")
		fmt.Fprintf(a.stdout, "status: %s\n", result.Status)
		fmt.Fprintf(a.stdout, "schema: %d/%d\n", result.SchemaVersion, result.LatestSchemaVersion)
		fmt.Fprintf(a.stdout, "journal: %s\n", result.JournalMode)
		fmt.Fprintf(a.stdout, "foreign_keys: %t\n", result.ForeignKeys)
		fmt.Fprintf(a.stdout, "integrity: %s\n", result.IntegrityCheck)
	}
	if result.Status != "ok" {
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

func requiredLeadingArgument(args []string, description string) (string, []string, *commandFailure) {
	if len(args) == 0 || strings.HasPrefix(args[0], "--") {
		failure := usageFailure(description+" is required", "run the command with --help for usage")
		return "", nil, &failure
	}
	return args[0], args[1:], nil
}

func daemonFailureHint(code string) string {
	switch code {
	case daemon.CodeDataDirInUse:
		return "stop the daemon using this data directory or select another --data-dir"
	case daemon.CodeSocketInUse:
		return "use 'crewfold status --socket <path>' or select another --socket"
	case daemon.CodeSocketPathOccupied:
		return "inspect the exact socket path; Crewfold will not remove a live or non-socket file"
	case daemon.CodeDatabaseUnavailable:
		return "inspect the data directory and database; Crewfold will not adopt an unrelated SQLite file"
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
  doctor --self  Check this binary (use --database for daemon storage)
  daemon run     Run the local daemon in the foreground
  daemon stop    Ask a running daemon to stop cleanly
  status         Query daemon health through its local socket
  workspace      Initialize or inspect a durable workspace
  project        Register or inspect a project and its source locations
  checkout       Register or list concrete repository directories
  agent          Define provider-neutral agents and roles
  objective      Define project objectives and budgets
  task           Coordinate dependency-aware, leased work
  context        Build and inspect immutable run briefings
  message        Send one bounded durable message to an agent
  inbox          Inspect an agent's durable mailbox
  thread         Inspect a durable agent conversation
  run            Launch and inspect provider-neutral execution
  events         Inspect the durable event journal
  help [command] Show command help

Global options:
  --output text|json  Select human or machine-readable output
  -h, --help          Show help

This build coordinates provider-neutral agents, objectives, tasks, dependencies,
leases, durable mail, deterministic fake runs, and bounded direct fixture
subprocesses. It does not mutate source repositories.
`

const versionHelp = `Usage:
  crewfold version [--output text|json]

Print embedded build metadata and Go runtime/platform information. The command
does not invoke Git or access the network.
`

const doctorHelp = `Usage:
  crewfold doctor --self [--output text|json]
  crewfold doctor --database --socket <path> [--output text|json]
  crewfold doctor --runtime herdr [--herdr-binary <path>] [--herdr-session <name>] [--output text|json]

Run checks for the current executable or query the daemon's SQLite schema,
journal mode, foreign-key enforcement, and integrity status.
`

func runAttachedProcess(ctx context.Context, attachment localapi.RunAttachResult) error {
	if strings.TrimSpace(attachment.Executable) == "" {
		return errors.New("runtime returned an empty attach executable")
	}
	command := exec.CommandContext(ctx, attachment.Executable, attachment.Arguments...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.Env = os.Environ()
	for name, value := range attachment.Environment {
		prefix := name + "="
		filtered := command.Env[:0]
		for _, entry := range command.Env {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		command.Env = append(filtered, prefix+value)
	}
	return command.Run()
}

const daemonHelp = `Usage:
  crewfold daemon run --data-dir <path> --socket <path>
  crewfold daemon stop --socket <path>

Run the local daemon in the foreground or ask it to stop through the local API.
There is no background-service installer. The selected data directory contains the
SQLite coordination database.
`

const daemonRunHelp = `Usage:
  crewfold daemon run --data-dir <path> --socket <path> [--herdr-binary <path>] [--herdr-session <name>] [--output text|json]

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
  crewfold status --workspace <scope> --socket <path> [--output text|json]

Without --workspace, query daemon process health and build metadata. With a
workspace, show the durable coordination projection for agents and tasks.
`

const workspaceHelp = `Usage:
  crewfold workspace init <name> --socket <path> [--idempotency-key <key>]
  crewfold workspace show <name-or-id> --socket <path>

Initialize a durable workspace or inspect it by stable ID or unique name.
`

const workspaceInitHelp = `Usage:
  crewfold workspace init <name> --socket <path> [--idempotency-key <key>] [--output text|json]

Atomically create a workspace projection and workspace.created event. Reusing an
idempotency key with the same name returns the original result.
`

const workspaceShowHelp = `Usage:
  crewfold workspace show <name-or-id> --socket <path> [--output text|json]

Read one durable workspace without mutating it.
`

const projectHelp = `Usage:
  crewfold project add <name> --workspace <name-or-id> --repo <path> --socket <path> [--mode <mode>] [--idempotency-key <key>]
  crewfold project inspect <name-or-id> --workspace <name-or-id> --socket <path>

Register a project from any local Git checkout, or refresh and inspect all of its
registered checkouts. Adjacent clones and linked worktrees are both supported.
`

const projectAddHelp = `Usage:
  crewfold project add <name> --workspace <name-or-id> --repo <path> --socket <path> [--mode exclusive|claimed|shared|read_only] [--idempotency-key <key>]

Registration only reads Git state. The default write mode is exclusive.
`

const projectInspectHelp = `Usage:
  crewfold project inspect <name-or-id> --workspace <name-or-id> --socket <path>

Refresh repository state for every registered checkout, retaining missing paths
as unavailable durable checkouts.
`

const checkoutHelp = `Usage:
  crewfold checkout add <project> <path> --workspace <name-or-id> --socket <path> [--mode <mode>] [--idempotency-key <key>]
  crewfold checkout list <project> --workspace <name-or-id> --socket <path>

A checkout is any concrete Git repository directory. It need not be a Git linked
worktree; adjacent standalone clones are first-class checkouts.
`

const checkoutAddHelp = `Usage:
  crewfold checkout add <project> <path> --workspace <name-or-id> --socket <path> [--mode exclusive|claimed|shared|read_only] [--idempotency-key <key>]
`

const checkoutListHelp = `Usage:
  crewfold checkout list <project> --workspace <name-or-id> --socket <path>
`

const eventsHelp = `Usage:
  crewfold events list --socket <path> --after <sequence> [--limit <count>]

Inspect events in ascending local sequence order.
`

const eventsListHelp = `Usage:
  crewfold events list --socket <path> --after <sequence> [--limit <count>] [--output text|json]

Return events strictly after the supplied resumable cursor. The default limit is
100 and the maximum is 1000.
`

const helpHelp = `Usage:
  crewfold help [command]

Show root help or help for one command.
`
