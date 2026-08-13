package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func (a *App) runKnowledge(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("knowledge requires a subcommand", "run 'crewfold help knowledge' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, knowledgeHelp)
		return ExitOK
	}
	switch args[0] {
	case "propose":
		return a.runKnowledgePropose(ctx, mode, args[1:])
	case "show":
		return a.runKnowledgeShow(ctx, mode, args[1:])
	case "list":
		return a.runKnowledgeList(ctx, mode, args[1:])
	case "search":
		return a.runKnowledgeSearch(ctx, mode, args[1:])
	case "index":
		return a.runKnowledgeIndex(ctx, mode, args[1:])
	case "accept":
		return a.runKnowledgeDecision(ctx, mode, args[1:], "accept")
	case "reject":
		return a.runKnowledgeDecision(ctx, mode, args[1:], "reject")
	case "mark-stale":
		return a.runKnowledgeMarkStale(ctx, mode, args[1:])
	case "dispute":
		return a.runKnowledgeDispute(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("unknown knowledge command %q", args[0]), "run 'crewfold help knowledge' for usage"))
	}
}

func (a *App) runKnowledgeSearch(ctx context.Context, mode outputMode, args []string) int {
	query, optionArgs, failure := requiredKnowledgeSearchQuery(args)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "project", "task", "type", "limit", "socket")
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
	limit := 20
	if options["limit"] != "" {
		parsed, parseFailure := requiredInt64Option(options, "limit", 1, 100)
		if parseFailure != nil {
			return a.writeFailure(mode, *parseFailure)
		}
		limit = int(parsed)
	}
	result, err := a.newClient(socket).KnowledgeSearch(ctx, localapi.KnowledgeSearchParams{
		Workspace: workspace, Project: project, Query: query, Task: options["task"], Type: options["type"], Limit: &limit,
	})
	if err != nil {
		return a.writeClientFailure(mode, "search knowledge", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write knowledge search", err))
		}
	} else {
		writeKnowledgeSearch(a, result.Search)
	}
	return ExitOK
}

func requiredKnowledgeSearchQuery(args []string) (string, []string, *commandFailure) {
	if len(args) > 0 && args[0] == "--" {
		if len(args) < 2 {
			failure := usageFailure("search query is required after --", "run 'crewfold help knowledge' for usage")
			return "", nil, &failure
		}
		return args[1], args[2:], nil
	}
	return requiredLeadingArgument(args, "search query")
}

func (a *App) runKnowledgeIndex(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("knowledge index requires status or rebuild", "run 'crewfold help knowledge' for usage"))
	}
	switch args[0] {
	case "status":
		options, failure := parseOptions(args[1:], "workspace", "socket")
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		workspace, socket, failure := requiredWorkspaceSocket(options)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		result, err := a.newClient(socket).KnowledgeIndexStatus(ctx, workspace)
		if err != nil {
			return a.writeClientFailure(mode, "inspect knowledge index", err)
		}
		return a.writeKnowledgeIndexStatus(mode, result)
	case "rebuild":
		options, failure := parseOptions(args[1:], "workspace", "socket", "idempotency-key")
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		workspace, socket, failure := requiredWorkspaceSocket(options)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		result, err := a.newClient(socket).KnowledgeIndexRebuild(ctx, localapi.KnowledgeIndexRebuildParams{
			Workspace: workspace, IdempotencyKey: options["idempotency-key"],
		})
		if err != nil {
			return a.writeClientFailure(mode, "rebuild knowledge index", err)
		}
		if mode == outputJSON {
			if err := writeJSON(a.stdout, result); err != nil {
				return a.writeFailure(outputText, internalFailure("write knowledge index rebuild", err))
			}
		} else {
			writeKnowledgeIndex(a, result.Index)
		}
		return ExitOK
	default:
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("unknown knowledge index command %q", args[0]), "run 'crewfold help knowledge' for usage"))
	}
}

func writeKnowledgeSearch(a *App, search domain.KnowledgeSearchResult) {
	fmt.Fprintf(a.stdout, "query: %s\nevaluated: %s\ncanonical event sequence: %d\nrank policy: %s\n",
		search.NormalizedQuery, search.EvaluatedAt, search.CanonicalEventSequence, search.RankPolicy)
	writeKnowledgeIndex(a, search.Index)
	fmt.Fprintf(a.stdout, "matches: %d\n", len(search.Matches))
	for _, match := range search.Matches {
		fmt.Fprintf(a.stdout, "%d\t%s\tbm25=%g\t%s\n", match.Ordinal, match.Revision.ID, match.Explanation.Text.BM25, match.Revision.Title)
		fmt.Fprintf(a.stdout, "  scope: %s; authority: %s; freshness: %s; provenance: %s\n",
			match.Explanation.Scope.Reason, match.Explanation.Authority.Reason,
			match.Explanation.Freshness.Reason, match.Explanation.Provenance.Reason)
	}
}

func writeKnowledgeIndex(a *App, index domain.KnowledgeIndexStatus) {
	fmt.Fprintf(a.stdout, "index: %s\ngeneration: %d\nsource event sequence: %d\nsource count: %d\n",
		index.Status, index.Generation, index.SourceEventSequence, index.SourceCount)
	if index.BuiltAt != "" {
		fmt.Fprintf(a.stdout, "built: %s\n", index.BuiltAt)
	}
	if index.SourceDigest != "" {
		fmt.Fprintf(a.stdout, "source digest: %s\n", index.SourceDigest)
	}
	if index.Diagnosis != "" {
		fmt.Fprintf(a.stdout, "diagnosis: %s\n", index.Diagnosis)
	}
}

func (a *App) writeKnowledgeIndexStatus(mode outputMode, result localapi.KnowledgeIndexStatusResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write knowledge index status", err))
		}
	} else {
		writeKnowledgeIndex(a, result.Index)
	}
	return ExitOK
}

func (a *App) runKnowledgePropose(ctx context.Context, mode outputMode, args []string) int {
	path, optionArgs, failure := splitKnowledgeProposalArguments(args)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, repeated, failure := parseRepeatedOptions(optionArgs, map[string]bool{
		"supporting-task": true, "supporting-meeting": true, "supporting-meeting-proposal": true,
	}, "workspace", "project", "task-scope", "type", "from-task", "from-meeting", "from-meeting-proposal",
		"supporting-task", "supporting-meeting", "supporting-meeting-proposal", "confidence", "verification",
		"freshness", "fresh-until", "supersedes", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	knowledgeType, failure := requiredOption(options, "type")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	sources, err := knowledgeSourcesFromOptions(options, repeated)
	if err != nil {
		return a.writeFailure(mode, usageFailure(err.Error(), "choose exactly one primary structured source"))
	}
	title, body, err := readKnowledgeMarkdown(path)
	if err != nil {
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("read knowledge proposal: %v", err), "provide UTF-8 Markdown beginning with one '# ' title"))
	}
	confidence := options["confidence"]
	if confidence == "" {
		confidence = domain.KnowledgeConfidenceMedium
	}
	verification := options["verification"]
	if verification == "" {
		verification = domain.KnowledgeVerificationSupported
	}
	freshness := options["freshness"]
	if freshness == "" {
		freshness = domain.KnowledgeFreshUntilSuperseded
		if options["fresh-until"] != "" {
			freshness = domain.KnowledgeFreshExpiresAt
		}
	}
	result, err := a.newClient(socket).KnowledgePropose(ctx, localapi.KnowledgeProposeParams{
		Workspace: workspace, Project: options["project"], TaskScopeID: options["task-scope"], Type: knowledgeType,
		Title: title, Body: body, Confidence: confidence, VerificationStatus: verification,
		FreshnessPolicy: freshness, FreshUntil: options["fresh-until"], Sources: sources,
		SupersedesRevisionID: options["supersedes"], IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "propose knowledge", err)
	}
	return a.writeKnowledgeMutation(mode, result)
}

func (a *App) runKnowledgeShow(ctx context.Context, mode outputMode, args []string) int {
	revision, optionArgs, failure := requiredLeadingArgument(args, "knowledge revision ID")
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
	result, err := a.newClient(socket).KnowledgeShow(ctx, workspace, revision)
	if err != nil {
		return a.writeClientFailure(mode, "show knowledge", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write knowledge detail", err))
		}
	} else {
		writeKnowledgeRevision(a, result.Detail.Revision)
		fmt.Fprintf(a.stdout, "authority checks: %d\n", len(result.Detail.AuthorityChecks))
	}
	return ExitOK
}

func (a *App) runKnowledgeDispute(ctx context.Context, mode outputMode, args []string) int {
	revision, optionArgs, failure := requiredLeadingArgument(args, "knowledge revision ID")
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
	result, err := a.newClient(socket).KnowledgeDispute(ctx, workspace, revision)
	if err != nil {
		return a.writeClientFailure(mode, "show knowledge dispute", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write knowledge dispute", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "revision: %s\ndisputed: %t\nopen contradictions: %d displayed / %d total\n", result.Dispute.RevisionID, result.Dispute.Disputed, len(result.Dispute.OpenContradictionIDs), result.Dispute.OpenContradictionCount)
		for _, contradiction := range result.Dispute.OpenContradictionIDs {
			fmt.Fprintf(a.stdout, "%s\n", contradiction)
		}
	}
	return ExitOK
}

func (a *App) runKnowledgeList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "task-scope", "type", "review-status", "currency-status", "socket")
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
	result, err := a.newClient(socket).KnowledgeList(ctx, localapi.KnowledgeListParams{
		Workspace: workspace, Project: project, TaskScopeID: options["task-scope"], Type: options["type"],
		ReviewStatus: options["review-status"], CurrencyStatus: options["currency-status"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "list knowledge", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write knowledge list", err))
		}
	} else {
		for _, revision := range result.List.Revisions {
			fmt.Fprintf(a.stdout, "%s\t%s/%s\t%s\t%s\n", revision.ID, revision.ReviewStatus, revision.CurrencyStatus, revision.Type, revision.Title)
		}
	}
	return ExitOK
}

func (a *App) runKnowledgeDecision(ctx context.Context, mode outputMode, args []string, action string) int {
	revision, optionArgs, failure := requiredLeadingArgument(args, "knowledge revision ID")
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
	expected, failure := requiredInt64Option(options, "expected-state-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	params := localapi.KnowledgeDecisionParams{Workspace: workspace, KnowledgeRevision: revision, ExpectedStateRevision: expected, DecisionNote: options["note"], IdempotencyKey: options["idempotency-key"]}
	var result localapi.KnowledgeMutationResult
	var err error
	if action == "accept" {
		result, err = a.newClient(socket).KnowledgeAccept(ctx, params)
	} else {
		result, err = a.newClient(socket).KnowledgeReject(ctx, params)
	}
	if err != nil {
		return a.writeClientFailure(mode, action+" knowledge", err)
	}
	return a.writeKnowledgeMutation(mode, result)
}

func (a *App) runKnowledgeMarkStale(ctx context.Context, mode outputMode, args []string) int {
	revision, optionArgs, failure := requiredLeadingArgument(args, "knowledge revision ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "expected-state-revision", "reason", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	expected, failure := requiredInt64Option(options, "expected-state-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	reason, failure := requiredOption(options, "reason")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).KnowledgeMarkStale(ctx, localapi.KnowledgeMarkStaleParams{
		Workspace: workspace, KnowledgeRevision: revision, ExpectedStateRevision: expected,
		Reason: reason, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "mark knowledge stale", err)
	}
	return a.writeKnowledgeMutation(mode, result)
}

func (a *App) writeKnowledgeMutation(mode outputMode, result localapi.KnowledgeMutationResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write knowledge mutation", err))
		}
	} else {
		writeKnowledgeRevision(a, result.Revision)
		if result.AuthorityCheck != nil {
			fmt.Fprintf(a.stdout, "authority: %s (%s)\n", result.AuthorityCheck.Outcome, result.AuthorityCheck.Reason)
		}
	}
	return ExitOK
}

func writeKnowledgeRevision(a *App, revision domain.KnowledgeRevision) {
	fmt.Fprintf(a.stdout, "knowledge: %s\nitem: %s\ntype: %s\nstate: %s/%s\nstate revision: %d\ntitle: %s\n", revision.ID, revision.ItemID, revision.Type, revision.ReviewStatus, revision.CurrencyStatus, revision.StateRevision, revision.Title)
}

func knowledgeSourcesFromOptions(options map[string]string, repeated map[string][]string) ([]domain.KnowledgeSourceInput, error) {
	primary := make([]domain.KnowledgeSourceInput, 0, 1)
	for _, candidate := range []struct {
		option     string
		sourceType string
	}{{"from-task", domain.KnowledgeSourceTask}, {"from-meeting", domain.KnowledgeSourceMeeting}, {"from-meeting-proposal", domain.KnowledgeSourceMeetingProposal}} {
		if value := options[candidate.option]; value != "" {
			primary = append(primary, domain.KnowledgeSourceInput{Type: candidate.sourceType, ID: value, Role: domain.KnowledgeSourcePrimary})
		}
	}
	if len(primary) != 1 {
		return nil, errors.New("knowledge propose requires exactly one of --from-task, --from-meeting, or --from-meeting-proposal")
	}
	result := primary
	for _, candidate := range []struct {
		option     string
		sourceType string
	}{{"supporting-task", domain.KnowledgeSourceTask}, {"supporting-meeting", domain.KnowledgeSourceMeeting}, {"supporting-meeting-proposal", domain.KnowledgeSourceMeetingProposal}} {
		for _, value := range repeated[candidate.option] {
			result = append(result, domain.KnowledgeSourceInput{Type: candidate.sourceType, ID: value, Role: domain.KnowledgeSourceSupporting})
		}
	}
	return result, nil
}

func splitKnowledgeProposalArguments(args []string) (string, []string, *commandFailure) {
	path := ""
	options := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if strings.HasPrefix(argument, "--") {
			options = append(options, argument)
			if !strings.Contains(argument, "=") {
				if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
					failure := usageFailure(fmt.Sprintf("%s requires a value", argument), "run 'crewfold help knowledge' for usage")
					return "", nil, &failure
				}
				index++
				options = append(options, args[index])
			}
			continue
		}
		if path != "" {
			failure := usageFailure(fmt.Sprintf("unexpected positional argument %q", argument), "knowledge propose accepts exactly one Markdown file")
			return "", nil, &failure
		}
		path = argument
	}
	if strings.TrimSpace(path) == "" {
		failure := usageFailure("knowledge propose requires a Markdown file", "run 'crewfold help knowledge' for usage")
		return "", nil, &failure
	}
	return path, options, nil
}

func readKnowledgeMarkdown(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	if len(data) > 20*1024 {
		return "", "", errors.New("proposal file exceeds 20 KiB")
	}
	if !utf8.Valid(data) {
		return "", "", errors.New("proposal file is not valid UTF-8")
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lineEnd := strings.IndexByte(text, '\n')
	if lineEnd < 0 {
		lineEnd = len(text)
	}
	firstLine := strings.TrimSuffix(text[:lineEnd], "\r")
	if !strings.HasPrefix(firstLine, "# ") {
		return "", "", errors.New("first line must be a '# ' Markdown title")
	}
	title := strings.TrimSpace(strings.TrimPrefix(firstLine, "# "))
	body := ""
	if lineEnd < len(text) {
		body = strings.TrimSpace(text[lineEnd+1:])
	}
	if title == "" || body == "" {
		return "", "", errors.New("proposal requires a non-empty title and body")
	}
	return title, body, nil
}

const knowledgeHelp = `Usage:
  crewfold knowledge propose --type <decision|finding> (--from-task <task>|--from-meeting <meeting>|--from-meeting-proposal <proposal>) <file.md> --workspace <scope> --socket <path> [options]
  crewfold knowledge show <revision> --workspace <scope> --socket <path>
  crewfold knowledge list --project <project> --workspace <scope> --socket <path> [options]
  crewfold knowledge search <query> --project <project> --workspace <scope> --socket <path> [--task <task>] [--type <decision|finding>] [--limit <n>]
  crewfold knowledge search -- <query-starting-with--> --project <project> --workspace <scope> --socket <path> [options]
  crewfold knowledge index status --workspace <scope> --socket <path>
  crewfold knowledge index rebuild --workspace <scope> --socket <path> [--idempotency-key <key>]
  crewfold knowledge accept <revision> --expected-state-revision <n> --workspace <scope> --socket <path> [--note <text>]
  crewfold knowledge reject <revision> --expected-state-revision <n> --workspace <scope> --socket <path> [--note <text>]
  crewfold knowledge mark-stale <revision> --expected-state-revision <n> --reason <text> --workspace <scope> --socket <path>
  crewfold knowledge dispute <revision> --workspace <scope> --socket <path>

Proposal Markdown must begin with one '# ' title followed by a concise body. A
task or meeting source records provenance; applicability defaults to that source's
project and can be narrowed with --task-scope. Use --supersedes with a proposed
successor; accepting it atomically preserves and supersedes the prior revision.
Acceptance, rejection, and staleness are owner-authorized local operations.
Dispute inspection derives effective state from confirmed open contradiction
records; it does not add a currency value or mutate the knowledge revision. An open
contradiction quarantines each exact participant everywhere it would otherwise apply.
The dispute read returns the first at most 200 contradiction IDs in ascending lexical
order together with the exact total count.
Use the -- separator before a literal search query that begins with --.
`
