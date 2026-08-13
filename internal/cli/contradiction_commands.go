package cli

import (
	"context"
	"fmt"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func (a *App) runContradiction(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("contradiction requires a subcommand", "run 'crewfold help contradiction' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, contradictionHelp)
		return ExitOK
	}
	switch args[0] {
	case "report":
		return a.runContradictionReport(ctx, mode, args[1:])
	case "show":
		return a.runContradictionShow(ctx, mode, args[1:])
	case "list":
		return a.runContradictionList(ctx, mode, args[1:])
	case "confirm", "dismiss":
		return a.runContradictionDecision(ctx, mode, args[1:], args[0])
	default:
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("unknown contradiction command %q", args[0]), "run 'crewfold help contradiction' for usage"))
	}
}

func (a *App) runContradictionReport(ctx context.Context, mode outputMode, args []string) int {
	left, remaining, failure := requiredLeadingArgument(args, "left knowledge revision ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	right, optionArgs, failure := requiredLeadingArgument(remaining, "right knowledge revision ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "reason", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	reason, failure := requiredOption(options, "reason")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).ContradictionReport(ctx, localapi.ContradictionReportParams{
		Workspace: workspace, LeftRevision: left, RightRevision: right,
		Reason: reason, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "report knowledge contradiction", err)
	}
	return a.writeContradictionMutation(mode, result)
}

func (a *App) runContradictionShow(ctx context.Context, mode outputMode, args []string) int {
	contradiction, optionArgs, failure := requiredLeadingArgument(args, "contradiction ID")
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
	result, err := a.newClient(socket).ContradictionShow(ctx, workspace, contradiction)
	if err != nil {
		return a.writeClientFailure(mode, "show knowledge contradiction", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write contradiction detail", err))
		}
	} else {
		writeContradictionDetail(a, result.Detail, true)
	}
	return ExitOK
}

func (a *App) runContradictionList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "status", "revision", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	project, failure := requiredOption(options, "project")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit := 50
	if _, supplied := options["limit"]; supplied {
		limit, failure = intOption(options, "limit", 50, 1, 200)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
	}
	result, err := a.newClient(socket).ContradictionList(ctx, localapi.ContradictionListParams{
		Workspace: workspace, Project: project, Status: options["status"],
		Revision: options["revision"], Limit: &limit,
	})
	if err != nil {
		return a.writeClientFailure(mode, "list knowledge contradictions", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write contradiction list", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "contradictions: %d\n", len(result.List.Details))
		for _, detail := range result.List.Details {
			writeContradictionDetail(a, detail, false)
		}
	}
	return ExitOK
}

func (a *App) runContradictionDecision(ctx context.Context, mode outputMode, args []string, action string) int {
	contradiction, optionArgs, failure := requiredLeadingArgument(args, "contradiction ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "expected-state-revision", "note", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	expectedRevision, failure := requiredInt64Option(options, "expected-state-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	params := localapi.ContradictionDecisionParams{
		Workspace: workspace, Contradiction: contradiction, ExpectedStateRevision: expectedRevision,
		Note: options["note"], IdempotencyKey: options["idempotency-key"],
	}
	var result localapi.ContradictionMutationResult
	var err error
	if action == "confirm" {
		result, err = a.newClient(socket).ContradictionConfirm(ctx, params)
	} else {
		result, err = a.newClient(socket).ContradictionDismiss(ctx, params)
	}
	if err != nil {
		return a.writeClientFailure(mode, action+" knowledge contradiction", err)
	}
	return a.writeContradictionMutation(mode, result)
}

func (a *App) writeContradictionMutation(mode outputMode, result localapi.ContradictionMutationResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write contradiction mutation", err))
		}
	} else {
		writeContradictionDetail(a, result.Detail, true)
		fmt.Fprintf(a.stdout, "event sequence: %d\n", result.EventSequence)
	}
	return ExitOK
}

func writeContradictionDetail(a *App, detail domain.KnowledgeContradictionDetail, verbose bool) {
	contradiction := detail.Contradiction
	fmt.Fprintf(a.stdout, "%s\t%s\trevision=%d\tproject=%s\n", contradiction.ID, contradiction.Status, contradiction.StateRevision, contradiction.ProjectID)
	fmt.Fprintf(a.stdout, "  left: %s\t%s\t%s\n", detail.LeftRevision.ID, knowledgeApplicability(detail.LeftRevision), detail.LeftRevision.Title)
	fmt.Fprintf(a.stdout, "  right: %s\t%s\t%s\n", detail.RightRevision.ID, knowledgeApplicability(detail.RightRevision), detail.RightRevision.Title)
	if verbose {
		fmt.Fprintf(a.stdout, "  reported by: %s/%s\n  reason: %s\n  authority checks: %d displayed / %d total\n", contradiction.ReportedByType, contradiction.ReportedBy, contradiction.ReportNote, len(detail.AuthorityChecks), detail.AuthorityCheckCount)
		for _, check := range detail.AuthorityChecks {
			fmt.Fprintf(a.stdout, "    %s\t%s\t%s\t%s\n", check.Action, check.Outcome, check.Reason, check.ID)
		}
	}
}

func knowledgeApplicability(revision domain.KnowledgeRevision) string {
	if revision.TaskScopeID == "" {
		return "project-wide"
	}
	return "task=" + revision.TaskScopeID
}

const contradictionHelp = `Usage:
  crewfold contradiction report <left-krev> <right-krev> --reason <text> --workspace <scope> --socket <path> [--idempotency-key <key>]
  crewfold contradiction show <kcon> --workspace <scope> --socket <path>
  crewfold contradiction list --workspace <scope> --project <project> --socket <path> [--status proposed|open|resolved|dismissed] [--revision <krev>] [--limit <count>]
  crewfold contradiction confirm <kcon> --expected-state-revision <n> --workspace <scope> --socket <path> [--note <text>] [--idempotency-key <key>]
  crewfold contradiction dismiss <kcon> --expected-state-revision <n> --workspace <scope> --socket <path> [--note <text>] [--idempotency-key <key>]

Report a conflict between two exact accepted, current knowledge revisions without
changing either revision's currency. Reports remain proposed until the workspace
owner confirms them. Confirmed open contradictions make both exact revisions
effectively disputed everywhere each revision would otherwise apply. A project-wide
participant is therefore quarantined project-wide even when its conflicting peer is
task-scoped; scope intersection gates reporting, not the quarantine. Dismissal records
a false positive. Lists are bounded to 50 by default and 200 maximum, ordered by
reported_at descending then contradiction ID descending (newest first), with no
continuation cursor. An omitted status returns only active proposed and open records.
Each detail returns the newest at most 200 authority checks plus their exact total.
Neither revision order nor an actor can be selected as authority through this
owner-only local command surface.
`
