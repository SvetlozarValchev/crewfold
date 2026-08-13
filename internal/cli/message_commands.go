package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"crewfold/internal/domain"
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
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("thread requires a subcommand", "run 'crewfold help thread' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, threadHelp)
		return ExitOK
	}
	switch args[0] {
	case "create":
		return a.runThreadCreate(ctx, mode, args[1:])
	case "invite":
		return a.runThreadInvite(ctx, mode, args[1:])
	case "participants":
		return a.runThreadParticipants(ctx, mode, args[1:])
	case "show":
		return a.runThreadShow(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_thread_command", message: fmt.Sprintf("unknown thread command %q", args[0]), hint: "run 'crewfold help thread' for usage"})
	}
}

func (a *App) runThreadCreate(ctx context.Context, mode outputMode, args []string) int {
	options, participantValues, failure := parseThreadCreateOptions(args)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	subject, failure := requiredOption(options, "subject")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	if len(participantValues) < 2 || len(participantValues) > 8 {
		return a.writeFailure(mode, usageFailure("--participant must be specified from 2 to 8 times", "use --participant agent=task for each participant"))
	}
	participants := make([]localapi.ThreadParticipantParams, len(participantValues))
	for index, value := range participantValues {
		agent, task, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(agent) == "" || strings.TrimSpace(task) == "" || strings.Contains(task, "=") {
			return a.writeFailure(mode, usageFailure("--participant must use agent=task", "bind each participant agent to exactly one task"))
		}
		participants[index] = localapi.ThreadParticipantParams{Agent: strings.TrimSpace(agent), Task: strings.TrimSpace(task)}
	}
	result, err := a.newClient(socket).ThreadCreate(ctx, localapi.ThreadCreateParams{
		Workspace: workspace, Subject: subject, Participants: participants, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "create participant thread", err)
	}
	return a.writeParticipantThreadMutation(mode, result)
}

func (a *App) runThreadInvite(ctx context.Context, mode outputMode, args []string) int {
	threadID, optionArgs, failure := requiredLeadingArgument(args, "thread ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "agent", "task", "expected-participant-revision", "socket", "idempotency-key")
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
	task, failure := requiredOption(options, "task")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revisionText, failure := requiredOption(options, "expected-participant-revision")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revision, err := strconv.ParseInt(revisionText, 10, 64)
	if err != nil || revision < 1 {
		return a.writeFailure(mode, usageFailure("--expected-participant-revision must be a positive integer", "use the participant_revision returned by the latest thread read"))
	}
	result, err := a.newClient(socket).ThreadInvite(ctx, localapi.ThreadInviteParams{
		Workspace: workspace, Thread: threadID,
		Participant:                 localapi.ThreadParticipantParams{Agent: agent, Task: task},
		ExpectedParticipantRevision: revision, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "invite thread participant", err)
	}
	return a.writeParticipantThreadMutation(mode, result)
}

func (a *App) runThreadParticipants(ctx context.Context, mode outputMode, args []string) int {
	threadID, optionArgs, failure := requiredLeadingArgument(args, "thread ID")
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
	result, err := a.newClient(socket).ThreadParticipants(ctx, workspace, threadID)
	if err != nil {
		return a.writeClientFailure(mode, "list thread participants", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write thread participants", err))
		}
	} else {
		a.writeParticipantThread(result.Collaboration)
	}
	return ExitOK
}

func (a *App) runThreadShow(ctx context.Context, mode outputMode, args []string) int {
	threadID, optionArgs, failure := requiredLeadingArgument(args, "thread ID")
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

func (a *App) writeParticipantThreadMutation(mode outputMode, result localapi.ParticipantThreadMutationResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write participant thread", err))
		}
	} else {
		a.writeParticipantThread(result.Collaboration)
	}
	return ExitOK
}

func (a *App) writeParticipantThread(collaboration domain.ParticipantThread) {
	fmt.Fprintf(a.stdout, "thread: %s\nkind: %s\nsubject: %s\nparticipant_revision: %d\nparticipants: %d\n", collaboration.Thread.ID, collaboration.Kind, collaboration.Thread.Subject, collaboration.ParticipantRevision, len(collaboration.Participants))
	for _, participant := range collaboration.Participants {
		fmt.Fprintf(a.stdout, "%d\t%s\t%s\t%s\t%s\n", participant.Ordinal, participant.ID, participant.AgentName, participant.ProjectName, participant.TaskTitle)
	}
}

func parseThreadCreateOptions(args []string) (map[string]string, []string, *commandFailure) {
	remaining := make([]string, 0, len(args))
	participants := make([]string, 0, 2)
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--participant" {
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
				failure := usageFailure("--participant requires a value", "use --participant agent=task")
				return nil, nil, &failure
			}
			index++
			participants = append(participants, args[index])
			continue
		}
		if strings.HasPrefix(argument, "--participant=") {
			value := strings.TrimPrefix(argument, "--participant=")
			if strings.TrimSpace(value) == "" {
				failure := usageFailure("--participant requires a non-empty value", "use --participant agent=task")
				return nil, nil, &failure
			}
			participants = append(participants, value)
			continue
		}
		remaining = append(remaining, argument)
	}
	options, failure := parseOptions(remaining, "workspace", "subject", "socket", "idempotency-key")
	return options, participants, failure
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
  crewfold thread create --workspace <scope> --subject <text> --participant <agent=task> --participant <agent=task> --socket <path> [--participant <agent=task> ...] [--idempotency-key <key>]
  crewfold thread invite <thread-id> --workspace <scope> --agent <agent> --task <task-id> --expected-participant-revision <n> --socket <path> [--idempotency-key <key>]
  crewfold thread participants <thread-id> --workspace <scope> --socket <path>
  crewfold thread show <thread-id> --workspace <scope> --socket <path>

Create and expand owner-controlled participant threads, list their task-bound
participants, or show ordered messages and per-recipient delivery state.
`
