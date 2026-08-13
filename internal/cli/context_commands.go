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
	case "refresh":
		return a.runContextRefresh(ctx, mode, args[1:])
	case "delta":
		return a.runContextDelta(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_context_command", message: fmt.Sprintf("unknown context command %q", args[0]), hint: "run 'crewfold help context' for usage"})
	}
}

func (a *App) runContextRefresh(ctx context.Context, mode outputMode, args []string) int {
	runID, optionArgs, failure := requiredLeadingArgument(args, "run ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).ContextRefresh(ctx, localapi.ContextRefreshParams{
		Workspace: workspace, Run: runID, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "refresh run context", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write context refresh", err))
		}
		return ExitOK
	}
	fmt.Fprintf(a.stdout, "status: %s\nrun: %s\ncontext: %s\nscanned: (%d, %d]\n",
		result.Status, result.RunID, result.ContextPacketID,
		result.ScannedFromEventSequence, result.ScannedThroughEventSequence)
	if result.Delta != nil {
		fmt.Fprintf(a.stdout, "delta: %s\nsequence: %d\nevents: (%d, %d]\nchanges: %d\nbytes: %d\n",
			result.Delta.ID, result.Delta.Sequence, result.Delta.FromEventSequence,
			result.Delta.ThroughEventSequence, len(result.Delta.Changes), result.Delta.ByteSize)
	}
	if result.EventSequence > 0 {
		fmt.Fprintf(a.stdout, "event sequence: %d\n", result.EventSequence)
	}
	if result.RebaseReason != "" {
		fmt.Fprintf(a.stdout, "rebase reason: %s\naction: stop this run with a durable handoff and start a replacement run with a fresh context packet\n", result.RebaseReason)
	}
	return ExitOK
}

func (a *App) runContextDelta(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("context delta requires a subcommand", "run 'crewfold help context' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, contextHelp)
		return ExitOK
	}
	switch args[0] {
	case "list":
		return a.runContextDeltaList(ctx, mode, args[1:])
	case "show":
		return a.runContextDeltaQuery(ctx, mode, args[1:], false)
	case "explain":
		return a.runContextDeltaQuery(ctx, mode, args[1:], true)
	default:
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_context_delta_command", message: fmt.Sprintf("unknown context delta command %q", args[0]), hint: "run 'crewfold help context' for usage"})
	}
}

func (a *App) runContextDeltaList(ctx context.Context, mode outputMode, args []string) int {
	runID, optionArgs, failure := requiredLeadingArgument(args, "run ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "socket", "after-sequence", "limit")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	after, failure := int64Option(options, "after-sequence", 0, 0, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := intOption(options, "limit", 20, 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).ContextDeltaList(ctx, localapi.ContextDeltaListParams{
		Workspace: workspace, Run: runID, AfterSequence: &after, Limit: limit,
	})
	if err != nil {
		return a.writeClientFailure(mode, "list context deltas", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write context delta list", err))
		}
		return ExitOK
	}
	for _, delta := range result.Deltas {
		fmt.Fprintf(a.stdout, "%s\tsequence=%d\tevents=(%d,%d]\tchanges=%d\tbytes=%d\n",
			delta.ID, delta.Sequence, delta.FromEventSequence, delta.ThroughEventSequence, len(delta.Changes), delta.ByteSize)
	}
	if result.HasMore {
		fmt.Fprintf(a.stdout, "next sequence: %d\n", result.NextSequence)
	}
	return ExitOK
}

func (a *App) runContextDeltaQuery(ctx context.Context, mode outputMode, args []string, explain bool) int {
	deltaID, optionArgs, failure := requiredLeadingArgument(args, "context delta ID")
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
		result, err := a.newClient(socket).ContextDeltaExplain(ctx, workspace, deltaID)
		if err != nil {
			return a.writeClientFailure(mode, "explain context delta", err)
		}
		if mode == outputJSON {
			if err := writeJSON(a.stdout, result); err != nil {
				return a.writeFailure(outputText, internalFailure("write context delta explanation", err))
			}
		} else {
			fmt.Fprintf(a.stdout, "delta: %s\nrun: %s\ncontext: %s\nsequence: %d\nevents: (%d, %d]\nchanges: %d\nincluded: %d\nexcluded: %d\nhash: %s\nbytes: %d\n",
				result.Explanation.DeltaID, result.Explanation.RunID, result.Explanation.ContextPacketID,
				result.Explanation.Sequence, result.Explanation.FromEventSequence,
				result.Explanation.ThroughEventSequence, len(result.Explanation.ChangeKinds),
				len(result.Explanation.Included), len(result.Explanation.Excluded),
				result.Explanation.ContentHash, result.Explanation.ByteSize)
		}
		return ExitOK
	}
	result, err := a.newClient(socket).ContextDeltaShow(ctx, workspace, deltaID)
	if err != nil {
		return a.writeClientFailure(mode, "show context delta", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write context delta", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "delta: %s\nrun: %s\ncontext: %s\nsequence: %d\nevents: (%d, %d]\nchanges: %d\nhash: %s\nbytes: %d\n",
			result.Delta.ID, result.Delta.RunID, result.Delta.ContextPacketID, result.Delta.Sequence,
			result.Delta.FromEventSequence, result.Delta.ThroughEventSequence, len(result.Delta.Changes),
			result.Delta.ContentHash, result.Delta.ByteSize)
	}
	return ExitOK
}

func (a *App) runContextBuild(ctx context.Context, mode outputMode, args []string) int {
	taskID, optionArgs, failure := requiredLeadingArgument(args, "task ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, repeated, failure := parseRepeatedOptions(optionArgs, map[string]bool{"include": true}, "workspace", "agent", "checkout", "include", "expected-task-revision", "socket", "idempotency-key")
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
		KnowledgeRevisionIDs: repeated["include"], ExpectedTaskRevision: revision,
		IdempotencyKey: options["idempotency-key"],
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
  crewfold context build <task-id> --workspace <scope> --agent <agent> --expected-task-revision <n> --socket <path> [--checkout <id>] [--include <knowledge-revision> ...]
  crewfold context show <id> --workspace <scope> --socket <path>
  crewfold context explain <id> --workspace <scope> --socket <path>
  crewfold context refresh <run-id> --workspace <scope> --socket <path>
  crewfold context delta list <run-id> --workspace <scope> --socket <path> [--after-sequence <n>] [--limit <n>]
  crewfold context delta show <delta-id> --workspace <scope> --socket <path>
  crewfold context delta explain <delta-id> --workspace <scope> --socket <path>

Context packets are immutable, bounded briefings. A run either binds an explicit
packet with --context or builds and binds one atomically during run start. The
owner explicitly builds bounded live deltas; only the bound run can acknowledge
one through its scoped MCP capability.
`
