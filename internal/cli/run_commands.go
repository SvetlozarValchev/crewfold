package cli

import (
	"context"
	"fmt"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

func (a *App) runRun(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("run requires a subcommand", "run 'crewfold help run' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, runHelp)
		return ExitOK
	}
	switch args[0] {
	case "start":
		return a.runStart(ctx, mode, args[1:])
	case "show":
		return a.runShow(ctx, mode, args[1:])
	case "list":
		return a.runList(ctx, mode, args[1:])
	case "watch":
		return a.runWatch(ctx, mode, args[1:])
	case "resume":
		return a.runResume(ctx, mode, args[1:])
	case "stop":
		return a.runStop(ctx, mode, args[1:])
	case "logs":
		return a.runLogs(ctx, mode, args[1:])
	case "prompt":
		return a.runPrompt(ctx, mode, args[1:])
	case "interrupt":
		return a.runInterrupt(ctx, mode, args[1:])
	case "attach":
		return a.runAttach(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_run_command", message: fmt.Sprintf("unknown run command %q", args[0]), hint: "run 'crewfold help run' for usage"})
	}
}

func (a *App) runPrompt(ctx context.Context, mode outputMode, args []string) int {
	runID, optionArgs, failure := requiredLeadingArgument(args, "run ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "text", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	value, failure := requiredOption(options, "text")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).RunPrompt(ctx, workspace, runID, value)
	if err != nil {
		return a.writeClientFailure(mode, "prompt run", err)
	}
	return a.writeRunControl(mode, result)
}

func (a *App) runInterrupt(ctx context.Context, mode outputMode, args []string) int {
	runID, optionArgs, failure := requiredLeadingArgument(args, "run ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).RunInterrupt(ctx, workspace, runID)
	if err != nil {
		return a.writeClientFailure(mode, "interrupt run", err)
	}
	return a.writeRunControl(mode, result)
}

func (a *App) runAttach(ctx context.Context, mode outputMode, args []string) int {
	if mode == outputJSON {
		return a.writeFailure(mode, usageFailure("run attach is interactive and does not support JSON output", "remove --output json"))
	}
	runID, optionArgs, failure := requiredLeadingArgument(args, "run ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	takeover := false
	normalized := make([]string, 0, len(optionArgs))
	for _, argument := range optionArgs {
		if argument == "--takeover" {
			if takeover {
				return a.writeFailure(mode, usageFailure("--takeover may be specified only once", "remove the duplicate option"))
			}
			takeover = true
			continue
		}
		normalized = append(normalized, argument)
	}
	options, failure := parseOptions(normalized, "workspace", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	attachment, err := a.newClient(socket).RunAttach(ctx, workspace, runID, takeover)
	if err != nil {
		return a.writeClientFailure(mode, "prepare run attach", err)
	}
	if err := a.runInteractive(ctx, attachment); err != nil {
		return a.writeFailure(mode, commandFailure{exitCode: ExitFailure, code: "attach_failed", message: fmt.Sprintf("attach run: %v", err), hint: "verify the Herdr session is running and retry"})
	}
	return ExitOK
}

func (a *App) writeRunControl(mode outputMode, result localapi.RunControlResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write run control output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "run: %s\nruntime: %s\naction: %s\nstatus: %s\n", result.RunID, result.Runtime, result.Action, result.Status)
	}
	return ExitOK
}

func (a *App) runStop(ctx context.Context, mode outputMode, args []string) int {
	runID, optionArgs, failure := requiredLeadingArgument(args, "run ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	normalized := make([]string, 0, len(optionArgs))
	graceful := false
	for _, argument := range optionArgs {
		if argument == "--graceful" {
			if graceful {
				return a.writeFailure(mode, usageFailure("--graceful may be specified only once", "remove the duplicate option"))
			}
			graceful = true
			continue
		}
		normalized = append(normalized, argument)
	}
	if !graceful {
		return a.writeFailure(mode, usageFailure("run stop requires --graceful", "use --graceful to request termination with a forced fallback"))
	}
	options, failure := parseOptions(normalized, "workspace", "expected-revision", "grace-millis", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revision, failure := requiredInt64Option(options, "expected-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	graceMillis, failure := intOption(options, "grace-millis", 500, 1, 30000)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).RunStop(ctx, localapi.RunStopParams{Workspace: workspace, Run: runID, ExpectedRevision: revision, GracePeriodMillis: int64(graceMillis), IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "stop run", err)
	}
	return a.writeRunMutation(mode, result)
}

func (a *App) runLogs(ctx context.Context, mode outputMode, args []string) int {
	runID, optionArgs, failure := requiredLeadingArgument(args, "run ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "tail", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	tail, failure := intOption(options, "tail", 50, 0, 10000)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).RunLogs(ctx, workspace, runID, tail)
	if err != nil {
		return a.writeClientFailure(mode, "read run logs", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write run logs", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "run: %s\nstate: %s\nstdout: captured=%d omitted=%d\n%s", result.Logs.RunID, result.Logs.State, result.Logs.Stdout.CapturedBytes, result.Logs.Stdout.OmittedBytes, result.Logs.Stdout.Text)
		fmt.Fprintf(a.stdout, "stderr: captured=%d omitted=%d\n%s", result.Logs.Stderr.CapturedBytes, result.Logs.Stderr.OmittedBytes, result.Logs.Stderr.Text)
	}
	return ExitOK
}

func (a *App) runStart(ctx context.Context, mode outputMode, args []string) int {
	task, optionArgs, failure := requiredLeadingArgument(args, "task ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "checkout", "context", "runtime", "provider", "scenario", "expected-task-revision", "check-watch-grant", "expected-check-watch-grant-revision", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	runtimeName, failure := requiredOption(options, "runtime")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	providerName, failure := requiredOption(options, "provider")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	scenarioPath, failure := requiredOption(options, "scenario")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revision, failure := requiredInt64Option(options, "expected-task-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	checkWatchGrant := options["check-watch-grant"]
	grantRevisionText := options["expected-check-watch-grant-revision"]
	if (checkWatchGrant == "") != (grantRevisionText == "") {
		return a.writeFailure(mode, usageFailure("--check-watch-grant and --expected-check-watch-grant-revision must be provided together", "provide both exact grant fields or omit both"))
	}
	if checkWatchGrant != "" && options["context"] != "" {
		return a.writeFailure(mode, usageFailure("--context cannot be combined with --check-watch-grant", "let Crewfold build the exact check-watch context packet from the grant"))
	}
	var grantRevision int64
	if checkWatchGrant != "" {
		grantRevision, failure = requiredInt64Option(options, "expected-check-watch-grant-revision", 1, 1<<62)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
	}
	scenario, err := execution.LoadScenario(scenarioPath)
	if err != nil {
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "invalid_scenario", message: err.Error(), hint: "use a valid bounded fake-run scenario document"})
	}
	result, err := a.newClient(socket).RunStart(ctx, localapi.RunStartParams{Workspace: workspace, Task: task, Checkout: options["checkout"], Context: options["context"], Runtime: runtimeName, Provider: providerName, Scenario: scenario, ExpectedTaskRevision: revision, CheckWatchGrant: checkWatchGrant, ExpectedCheckWatchGrantRevision: grantRevision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "start run", err)
	}
	return a.writeRunMutation(mode, result)
}

func (a *App) runShow(ctx context.Context, mode outputMode, args []string) int {
	runID, optionArgs, failure := requiredLeadingArgument(args, "run ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).RunShow(ctx, workspace, runID)
	if err != nil {
		return a.writeClientFailure(mode, "show run", err)
	}
	return a.writeRunShow(mode, result)
}

func (a *App) runList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "task", "status", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).RunList(ctx, workspace, options["task"], options["status"])
	if err != nil {
		return a.writeClientFailure(mode, "list runs", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write run list", err))
		}
	} else {
		for _, detail := range result.Runs {
			fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\tstep=%d\n", detail.Run.ID, detail.Run.Status, detail.Run.TaskID, detail.Run.AgentID, detail.Run.StepCursor)
		}
	}
	return ExitOK
}

func (a *App) runResume(ctx context.Context, mode outputMode, args []string) int {
	runID, optionArgs, failure := requiredLeadingArgument(args, "run ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "expected-revision", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revision, failure := requiredInt64Option(options, "expected-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).RunResume(ctx, localapi.RunResumeParams{Workspace: workspace, Run: runID, ExpectedRevision: revision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "resume run", err)
	}
	return a.writeRunMutation(mode, result)
}

func (a *App) runWatch(ctx context.Context, mode outputMode, args []string) int {
	runID, optionArgs, failure := requiredLeadingArgument(args, "run ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "wait-seconds", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	waitSeconds, failure := intOption(options, "wait-seconds", 10, 1, 3600)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	deadline := time.NewTimer(time.Duration(waitSeconds) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	client := a.newClient(socket)
	for {
		result, err := client.RunShow(ctx, workspace, runID)
		if err != nil {
			return a.writeClientFailure(mode, "watch run", err)
		}
		if runWatchSettled(result.Detail.Run.Status) {
			return a.writeRunShow(mode, result)
		}
		select {
		case <-ctx.Done():
			return a.writeFailure(mode, commandFailure{exitCode: ExitFailure, code: "watch_cancelled", message: ctx.Err().Error(), hint: "run 'crewfold run show' to inspect the latest state"})
		case <-deadline.C:
			return a.writeFailure(mode, commandFailure{exitCode: ExitFailure, code: "watch_timeout", message: fmt.Sprintf("run %s did not settle within %d seconds", runID, waitSeconds), hint: "increase --wait-seconds or inspect the run directly"})
		case <-ticker.C:
		}
	}
}

func runWatchSettled(status string) bool {
	switch status {
	case domain.RunBlocked, domain.RunStopped, domain.RunLost, domain.RunReview, domain.RunCompleted, domain.RunStartFailed, domain.RunFailed:
		return true
	default:
		return false
	}
}

func (a *App) writeRunMutation(mode outputMode, result localapi.RunMutationResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write run mutation", err))
		}
	} else {
		writeRunText(a, result.Detail)
		fmt.Fprintf(a.stdout, "event_sequence: %d\n", result.EventSequence)
	}
	return ExitOK
}

func (a *App) writeRunShow(mode outputMode, result localapi.RunShowResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write run output", err))
		}
	} else {
		writeRunText(a, result.Detail)
	}
	return ExitOK
}

func writeRunText(a *App, detail domain.RunDetail) {
	fmt.Fprintf(a.stdout, "run: %s\nstatus: %s\nrevision: %d\ntask: %s\nagent: %s\ncheckout: %s\nstep: %d\n", detail.Run.ID, detail.Run.Status, detail.Run.Revision, detail.Run.TaskID, detail.Run.AgentID, detail.Checkout.Path, detail.Run.StepCursor)
	if detail.Run.BlockedQuestion != "" {
		fmt.Fprintf(a.stdout, "blocked: %s\n", detail.Run.BlockedQuestion)
	}
	if detail.Run.ResultSummary != "" {
		fmt.Fprintf(a.stdout, "result: %s\n", detail.Run.ResultSummary)
	}
	if detail.Run.FailureMessage != "" {
		fmt.Fprintf(a.stdout, "failure: %s: %s\n", detail.Run.FailureCode, detail.Run.FailureMessage)
	}
	if detail.Run.Status == domain.RunStopped {
		fmt.Fprintf(a.stdout, "stop_forced: %t\n", detail.Run.StopForced)
	}
	if detail.Handoff != nil {
		fmt.Fprintf(a.stdout, "handoff: %s\n", detail.Handoff.Summary)
	}
}

const runHelp = `Usage:
  crewfold run start <task-id> --workspace <scope> --runtime <runtime> --provider <provider> --scenario <file> --expected-task-revision <n> --socket <path> [--checkout <id>] [--context <id>] [--check-watch-grant <id> --expected-check-watch-grant-revision <n>]
  crewfold run show <id> --workspace <scope> --socket <path>
  crewfold run list --workspace <scope> [--task <id>] [--status <status>] --socket <path>
  crewfold run watch <id> --workspace <scope> --socket <path> [--wait-seconds <n>]
  crewfold run resume <id> --workspace <scope> --expected-revision <n> --socket <path>
  crewfold run stop <id> --graceful --workspace <scope> --expected-revision <n> --socket <path> [--grace-millis <n>]
  crewfold run logs <id> --workspace <scope> --socket <path> [--tail <lines>]
  crewfold run prompt <id> --text <prompt> --workspace <scope> --socket <path>
  crewfold run interrupt <id> --workspace <scope> --socket <path>
  crewfold run attach <id> --workspace <scope> --socket <path> [--takeover]

The fake runtime exercises durable domain behavior without a process. The direct
runtime supervises a bounded local subprocess. The Herdr runtime hosts a fixture
or interactive provider in an isolated persistent pane and supports prompt,
interrupt, attach, logs, reconciliation, and stop.
`
