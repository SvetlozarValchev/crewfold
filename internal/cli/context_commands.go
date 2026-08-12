package cli

import (
	"context"
	"fmt"

	"crewfold/internal/localapi"
)

func (a *App) runContext(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("context requires a subcommand", "run 'crewfold help context' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, contextHelp)
		return ExitOK
	}
	switch args[0] {
	case "build":
		return a.runContextBuild(ctx, mode, args[1:])
	case "show":
		return a.runContextShow(ctx, mode, args[1:], false)
	case "explain":
		return a.runContextShow(ctx, mode, args[1:], true)
	default:
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_context_command", message: fmt.Sprintf("unknown context command %q", args[0]), hint: "run 'crewfold help context' for usage"})
	}
}

func (a *App) runContextBuild(ctx context.Context, mode outputMode, args []string) int {
	taskID, optionArgs, failure := requiredLeadingArgument(args, "task ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "agent", "checkout", "expected-task-revision", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	agent, failure := requiredOption(options, "agent")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revision, failure := requiredInt64Option(options, "expected-task-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).ContextBuild(ctx, localapi.ContextBuildParams{
		Workspace: workspace, Task: taskID, Agent: agent, Checkout: options["checkout"],
		ExpectedTaskRevision: revision, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "build context packet", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write context packet", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "context: %s\nhash: %s\nbytes: %d\ntask: %s\nagent: %s\ncheckout: %s\n", result.Packet.ID, result.Packet.ContentHash, result.Packet.ByteSize, result.Packet.TaskID, result.Packet.AgentID, result.Packet.CheckoutID)
	}
	return ExitOK
}

func (a *App) runContextShow(ctx context.Context, mode outputMode, args []string, explain bool) int {
	contextID, optionArgs, failure := requiredLeadingArgument(args, "context packet ID")
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
	if explain {
		result, err := a.newClient(socket).ContextExplain(ctx, workspace, contextID)
		if err != nil {
			return a.writeClientFailure(mode, "explain context packet", err)
		}
		if mode == outputJSON {
			if err := writeJSON(a.stdout, result); err != nil {
				return a.writeFailure(outputText, internalFailure("write context explanation", err))
			}
		} else {
			fmt.Fprintf(a.stdout, "context: %s\nhash: %s\nbytes: %d\nincluded: %d\nexcluded: %d\n", result.Explanation.PacketID, result.Explanation.ContentHash, result.Explanation.ByteSize, len(result.Explanation.Included), len(result.Explanation.Excluded))
		}
		return ExitOK
	}
	result, err := a.newClient(socket).ContextShow(ctx, workspace, contextID)
	if err != nil {
		return a.writeClientFailure(mode, "show context packet", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write context packet", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "context: %s\nhash: %s\nbytes: %d\ntask: %s\nagent: %s\ncheckout: %s\n", result.Packet.ID, result.Packet.ContentHash, result.Packet.ByteSize, result.Packet.TaskID, result.Packet.AgentID, result.Packet.CheckoutID)
	}
	return ExitOK
}

const contextHelp = `Usage:
  crewfold context build <task-id> --workspace <scope> --agent <agent> --expected-task-revision <n> --socket <path> [--checkout <id>]
  crewfold context show <id> --workspace <scope> --socket <path>
  crewfold context explain <id> --workspace <scope> --socket <path>

Context packets are immutable, bounded briefings. A run either binds an explicit
packet with --context or builds and binds one atomically during run start.
`
