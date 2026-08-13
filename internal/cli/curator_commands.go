package cli

import (
	"context"
	"fmt"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

const curatorAcceptedMeetingResolutionRuleAlias = "accepted-meeting-resolution-copy"

func (a *App) runCurator(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("curator requires a subcommand", "run 'crewfold help curator' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, curatorHelp)
		return ExitOK
	}
	switch args[0] {
	case "queue":
		return a.runCuratorQueue(ctx, mode, args[1:])
	case "rule":
		return a.runCuratorRule(ctx, mode, args[1:])
	case "process":
		return a.runCuratorProcess(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("unknown curator command %q", args[0]), "run 'crewfold help curator' for usage"))
	}
}

func (a *App) runCuratorQueue(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "after", "limit", "socket")
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
	result, err := a.newClient(socket).CuratorQueue(ctx, localapi.CuratorQueueParams{
		Workspace: workspace,
		Project:   project,
		After:     options["after"],
		Limit:     &limit,
	})
	if err != nil {
		return a.writeClientFailure(mode, "inspect curator queue", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write curator queue", err))
		}
		return ExitOK
	}
	fmt.Fprintf(a.stdout, "rule: %s\nrule enabled: %t\nrule revision: %d\nentries: %d\n",
		curatorRuleDisplayName(result.Queue.Rule.Name), result.Queue.Rule.Enabled,
		result.Queue.Rule.Revision, len(result.Queue.Entries))
	for _, entry := range result.Queue.Entries {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\n", entry.Revision.ID, entry.Eligibility, entry.Revision.ReviewStatus, entry.Revision.Title)
		fmt.Fprintf(a.stdout, "  reason: %s\n", entry.EligibilityReason)
		if entry.Derivation != nil {
			fmt.Fprintf(a.stdout, "  derivation: %s (%s %s@%d)\n", entry.Derivation.ID, entry.Derivation.SourceType, entry.Derivation.SourceID, entry.Derivation.SourceRevision)
		}
	}
	if result.Queue.NextCursor != "" {
		fmt.Fprintf(a.stdout, "next cursor: %s\n", result.Queue.NextCursor)
	}
	return ExitOK
}

func (a *App) runCuratorRule(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("curator rule requires enable or disable", "run 'crewfold help curator' for usage"))
	}
	enabled := false
	switch args[0] {
	case "enable":
		enabled = true
	case "disable":
	case "--help", "-h":
		fmt.Fprint(a.stdout, curatorHelp)
		return ExitOK
	default:
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("unknown curator rule command %q", args[0]), "run 'crewfold help curator' for usage"))
	}
	rule, optionArgs, failure := requiredLeadingArgument(args[1:], "curator rule name")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	if rule != curatorAcceptedMeetingResolutionRuleAlias {
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("unsupported curator rule %q", rule), "use accepted-meeting-resolution-copy"))
	}
	options, failure := parseOptions(optionArgs, "workspace", "expected-revision", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	expectedRevision, failure := requiredInt64Option(options, "expected-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).CuratorRuleConfigure(ctx, localapi.CuratorRuleConfigureParams{
		Workspace:        workspace,
		Rule:             domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled:          &enabled,
		ExpectedRevision: expectedRevision,
		IdempotencyKey:   options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "configure curator rule", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write curator rule", err))
		}
		return ExitOK
	}
	fmt.Fprintf(a.stdout, "rule: %s\nenabled: %t\nrevision: %d\nevent sequence: %d\n", curatorRuleDisplayName(result.Rule.Name), result.Rule.Enabled, result.Rule.Revision, result.EventSequence)
	return ExitOK
}

func (a *App) runCuratorProcess(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(normalizeStandaloneApplySafe(args), "workspace", "project", "apply-safe", "socket", "idempotency-key")
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
	applySafe := false
	if _, supplied := options["apply-safe"]; supplied {
		applySafe, failure = boolOption(options, "apply-safe")
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
	}
	result, err := a.newClient(socket).CuratorProcess(ctx, localapi.CuratorProcessParams{
		Workspace: workspace, Project: project, ApplySafe: applySafe, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "process curator candidates", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write curator processing result", err))
		}
		return ExitOK
	}
	fmt.Fprintf(a.stdout, "candidates scanned: %d\nderived proposals: %d\nautomatically accepted: %d\nskipped: %d\nevent sequence: %d\n",
		result.Process.CandidatesScanned, len(result.Process.Derived), len(result.Process.Accepted), len(result.Process.Skipped), result.EventSequence)
	for _, derivation := range result.Process.Derived {
		fmt.Fprintf(a.stdout, "derived\t%s\t%s\n", derivation.KnowledgeRevisionID, derivation.ID)
	}
	for _, acceptance := range result.Process.Accepted {
		fmt.Fprintf(a.stdout, "accepted\t%s\t%s\n", acceptance.KnowledgeRevisionID, acceptance.AuthorityCheckID)
	}
	for _, skipped := range result.Process.Skipped {
		fmt.Fprintf(a.stdout, "skipped\t%s\t%s@%d\t%s\n", skipped.SourceType, skipped.SourceID, skipped.SourceRevision, skipped.Reason)
	}
	return ExitOK
}

func normalizeStandaloneApplySafe(args []string) []string {
	normalized := make([]string, 0, len(args))
	for _, argument := range args {
		if argument == "--apply-safe" {
			normalized = append(normalized, "--apply-safe=true")
			continue
		}
		normalized = append(normalized, argument)
	}
	return normalized
}

func curatorRuleDisplayName(rule string) string {
	if rule == domain.CuratorRuleAcceptedMeetingResolutionCopy {
		return curatorAcceptedMeetingResolutionRuleAlias
	}
	return strings.TrimSpace(rule)
}

const curatorHelp = `Usage:
  crewfold curator queue --workspace <scope> --project <project> --socket <path> [--after <cursor>] [--limit <count>]
  crewfold curator rule enable accepted-meeting-resolution-copy --workspace <scope> --expected-revision <n> --socket <path> [--idempotency-key <key>]
  crewfold curator rule disable accepted-meeting-resolution-copy --workspace <scope> --expected-revision <n> --socket <path> [--idempotency-key <key>]
  crewfold curator process --workspace <scope> --project <project> --socket <path> [--apply-safe] [--idempotency-key <key>]

Inspect deterministic curator proposals, configure the one bounded automatic
acceptance rule, or process structured accepted meeting resolutions. Processing
derives proposals by default; --apply-safe additionally permits the enabled exact
rule to accept bounded eligible proposals. The curator cannot accept its own
proposal unless the owner both enables that rule and supplies this flag.
Queue cursors are opaque; the default page size is 50 and the maximum is 200.
`
