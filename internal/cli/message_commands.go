package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"crewfold/internal/localapi"
)

func (a *App) runMessage(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("message requires a subcommand", "run 'crewfold help message' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, messageHelp)
		return ExitOK
	}
	if args[0] != "send" {
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_message_command", message: fmt.Sprintf("unknown message command %q", args[0]), hint: "run 'crewfold help message' for usage"})
	}
	recipient, optionArgs, failure := requiredLeadingArgument(args[1:], "recipient agent")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "kind", "body", "subject", "thread", "project", "task", "artifact-ids", "reply-to", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	kind, failure := requiredOption(options, "kind")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	body, failure := requiredOption(options, "body")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	artifacts := splitBoundedList(options["artifact-ids"])
	result, err := a.newClient(socket).MessageSend(ctx, localapi.MessageSendParams{
		Workspace: workspace, RecipientAgent: recipient, Thread: options["thread"], Project: options["project"], Task: options["task"],
		Kind: kind, Subject: options["subject"], Body: body, ArtifactIDs: artifacts, ReplyToMessage: options["reply-to"], IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "send message", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write message result", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "message: %s\nthread: %s\nrecipient: %s\nstatus: %s\nwake: %s\n", result.Mutation.Message.ID, result.Mutation.Thread.ID, result.Mutation.Recipient.RecipientName, result.Mutation.Recipient.Status, result.Mutation.Recipient.WakeStatus)
	}
	return ExitOK
}

func (a *App) runInbox(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, inboxHelp)
		return ExitOK
	}
	options, failure := parseOptions(args, "workspace", "agent", "limit", "socket")
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
	limit := 20
	if options["limit"] != "" {
		parsed, err := strconv.Atoi(options["limit"])
		if err != nil || parsed < 1 || parsed > 50 {
			return a.writeFailure(mode, usageFailure("--limit must be from 1 to 50", "run 'crewfold help inbox' for usage"))
		}
		limit = parsed
	}
	result, err := a.newClient(socket).InboxList(ctx, workspace, agent, limit)
	if err != nil {
		return a.writeClientFailure(mode, "list inbox", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write inbox", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "agent: %s\nmessages: %d\n", result.Agent, len(result.Items))
		for _, item := range result.Items {
			fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\t%s\n", item.Message.ID, item.Message.Kind, item.Message.SenderAgentName, item.Delivery.Status, item.Message.Body)
		}
	}
	return ExitOK
}

func (a *App) runThread(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, threadHelp)
		return ExitOK
	}
	if len(args) == 0 || args[0] != "show" {
		return a.writeFailure(mode, usageFailure("thread requires the show subcommand", "run 'crewfold help thread' for usage"))
	}
	threadID, optionArgs, failure := requiredLeadingArgument(args[1:], "thread ID")
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
	result, err := a.newClient(socket).ThreadShow(ctx, workspace, threadID)
	if err != nil {
		return a.writeClientFailure(mode, "show thread", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write thread", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "thread: %s\nsubject: %s\nmessages: %d\n", result.Detail.Thread.ID, result.Detail.Thread.Subject, len(result.Detail.Messages))
		for _, message := range result.Detail.Messages {
			fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\n", message.ID, message.Kind, message.SenderAgentName, message.Body)
		}
	}
	return ExitOK
}

func splitBoundedList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

const messageHelp = `Usage:
  crewfold message send <agent> --workspace <scope> --kind <kind> --body <text> --socket <path> [--subject <text>] [--thread <id>] [--project <id>] [--task <id>] [--artifact-ids <id,...>] [--reply-to <id>] [--idempotency-key <key>]

Send one bounded durable message. The recipient is one registered agent; this
command cannot broadcast or address a real person.
`

const inboxHelp = `Usage:
  crewfold inbox --workspace <scope> --agent <agent> --socket <path> [--limit <1..50>]

Inspect durable delivery state without marking messages read or acknowledged.
`

const threadHelp = `Usage:
  crewfold thread show <thread-id> --workspace <scope> --socket <path>

Show ordered messages and per-recipient delivery, acknowledgement, and wake state.
`
