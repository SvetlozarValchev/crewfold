package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"gopkg.in/yaml.v3"
)

const maximumOutcomeInputBytes = 64 * 1024

type outcomeDaemonClient interface {
	OutcomeCommitmentCreate(context.Context, localapi.OutcomeCommitmentCreateParams) (localapi.OutcomeCommitmentMutationResult, error)
	OutcomeCommitmentShow(context.Context, string, string) (localapi.OutcomeCommitmentShowResult, error)
	OutcomeCommitmentList(context.Context, localapi.OutcomeCommitmentQueryParams) (localapi.OutcomeCommitmentListResult, error)
	OutcomePropose(context.Context, localapi.OutcomeProposeParams) (localapi.OutcomeMutationResult, error)
	OutcomeShow(context.Context, string, string) (localapi.OutcomeShowResult, error)
	OutcomeList(context.Context, localapi.OutcomeQueryParams) (localapi.OutcomeListResult, error)
	OutcomeAccept(context.Context, localapi.OutcomeDecisionParams) (localapi.OutcomeMutationResult, error)
	OutcomeReject(context.Context, localapi.OutcomeDecisionParams) (localapi.OutcomeMutationResult, error)
	CheckpointCreate(context.Context, localapi.CheckpointCreateParams) (localapi.CheckpointMutationResult, error)
	CheckpointShow(context.Context, string, string) (localapi.CheckpointShowResult, error)
	CheckpointList(context.Context, localapi.CheckpointQueryParams) (localapi.CheckpointListResult, error)
	BriefingShow(context.Context, localapi.BriefingShowParams) (localapi.BriefingShowResult, error)
	BriefingExplain(context.Context, localapi.BriefingExplainParams) (localapi.BriefingExplainResult, error)
}

func (a *App) runOutcome(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, outcomeHelp)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	switch args[0] {
	case "commitment":
		return a.runOutcomeCommitment(ctx, mode, args[1:])
	case "propose":
		return a.runOutcomePropose(ctx, mode, args[1:])
	case "show":
		return a.runOutcomeShow(ctx, mode, args[1:])
	case "list":
		return a.runOutcomeList(ctx, mode, args[1:])
	case "accept":
		return a.runOutcomeDecision(ctx, mode, args[1:], true)
	case "reject":
		return a.runOutcomeDecision(ctx, mode, args[1:], false)
	default:
		return a.writeFailure(mode, usageFailure("unknown outcome command "+args[0], "run 'crewfold help outcome' for usage"))
	}
}

func (a *App) runOutcomeCommitment(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("outcome commitment requires add, show, or list", "run 'crewfold help outcome' for usage"))
	}
	switch args[0] {
	case "add":
		return a.runOutcomeCommitmentAdd(ctx, mode, args[1:])
	case "show":
		return a.runOutcomeCommitmentShow(ctx, mode, args[1:])
	case "list":
		return a.runOutcomeCommitmentList(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown outcome commitment command "+args[0], "run 'crewfold help outcome' for usage"))
	}
}

func (a *App) runOutcomeCommitmentAdd(ctx context.Context, mode outputMode, args []string) int {
	key, rest, failure := requiredLeadingArgument(args, "commitment key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, repeated, failure := parseRepeatedOptions(rest, map[string]bool{"criterion": true}, "workspace", "task", "title", "description", "criterion", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	task, failure := requiredOption(options, "task")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	title, failure := requiredOption(options, "title")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	criteria := repeated["criterion"]
	if len(criteria) == 0 || len(criteria) > 32 {
		return a.writeFailure(mode, usageFailure("outcome commitment requires one to 32 --criterion values", "name each owner-visible acceptance criterion"))
	}
	result, err := a.newClient(socket).OutcomeCommitmentCreate(ctx, localapi.OutcomeCommitmentCreateParams{
		Workspace: workspace, Task: task, Key: key, Title: title, Description: options["description"],
		AcceptanceCriteria: criteria, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "add outcome commitment", err)
	}
	return a.writeOutcomeResult(mode, result, fmt.Sprintf("commitment: %s\nkey: %s\ntask: %s\ncriteria: %d\n", result.Commitment.ID, result.Commitment.Key, result.Commitment.TaskID, len(result.Commitment.AcceptanceCriteria)))
}

func (a *App) runOutcomeCommitmentShow(ctx context.Context, mode outputMode, args []string) int {
	commitment, rest, failure := requiredLeadingArgument(args, "commitment ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).OutcomeCommitmentShow(ctx, workspace, commitment)
	if err != nil {
		return a.writeClientFailure(mode, "show outcome commitment", err)
	}
	return a.writeOutcomeResult(mode, result, fmt.Sprintf("commitment: %s\nkey: %s\ntitle: %s\ntask: %s\ncriteria: %d\n", result.Commitment.ID, result.Commitment.Key, result.Commitment.Title, result.Commitment.TaskID, len(result.Commitment.AcceptanceCriteria)))
}

func (a *App) runOutcomeCommitmentList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "objective", "task", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalIntOption(options, "limit", 0, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).OutcomeCommitmentList(ctx, localapi.OutcomeCommitmentQueryParams{
		Workspace: workspace, Project: options["project"], Objective: options["objective"], Task: options["task"], Limit: int(limit),
	})
	if err != nil {
		return a.writeClientFailure(mode, "list outcome commitments", err)
	}
	if mode == outputJSON {
		return a.writeOutcomeResult(mode, result, "")
	}
	for _, commitment := range result.Commitments {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\n", commitment.ID, commitment.Key, commitment.TaskID, commitment.Title)
	}
	return ExitOK
}

func (a *App) runOutcomePropose(ctx context.Context, mode outputMode, args []string) int {
	path, optionArgs, failure := outcomeProposalPath(args)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "task", "supersedes", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	task, failure := requiredOption(options, "task")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	document, err := readOutcomeProposalDocument(path)
	if err != nil {
		return a.writeFailure(mode, usageFailure("read outcome proposal: "+err.Error(), "use one strict structured JSON or YAML proposal document"))
	}
	result, err := a.newClient(socket).OutcomePropose(ctx, localapi.OutcomeProposeParams{
		Workspace: workspace, Task: task, Commitment: document.Commitment, SupersedesOutcome: options["supersedes"], Assessment: document.Assessment, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "propose outcome assessment", err)
	}
	return a.writeOutcomeResult(mode, result, outcomeDetailText(result.Detail))
}

func (a *App) runOutcomeShow(ctx context.Context, mode outputMode, args []string) int {
	outcome, rest, failure := requiredLeadingArgument(args, "outcome assessment ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).OutcomeShow(ctx, workspace, outcome)
	if err != nil {
		return a.writeClientFailure(mode, "show outcome assessment", err)
	}
	return a.writeOutcomeResult(mode, result, outcomeDetailText(result.Detail))
}

func (a *App) runOutcomeList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "objective", "task", "commitment", "review-state", "conclusion", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalIntOption(options, "limit", 0, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).OutcomeList(ctx, localapi.OutcomeQueryParams{
		Workspace: workspace, Project: options["project"], Objective: options["objective"], Task: options["task"], Commitment: options["commitment"],
		ReviewState: options["review-state"], Conclusion: options["conclusion"], Limit: int(limit),
	})
	if err != nil {
		return a.writeClientFailure(mode, "list outcome assessments", err)
	}
	if mode == outputJSON {
		return a.writeOutcomeResult(mode, result, "")
	}
	for _, detail := range result.Outcomes {
		assessment := detail.Assessment
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\tcommitment %s\trevision %d\n", assessment.ID, assessment.ReviewState, assessment.Conclusion, assessment.CommitmentID, assessment.Revision)
	}
	return ExitOK
}

func (a *App) runOutcomeDecision(ctx context.Context, mode outputMode, args []string, accept bool) int {
	outcome, rest, failure := requiredLeadingArgument(args, "outcome assessment ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "expected-state-revision", "decision-note", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revision, failure := requiredInt64Option(options, "expected-state-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	params := localapi.OutcomeDecisionParams{Workspace: workspace, Outcome: outcome, ExpectedStateRevision: revision, DecisionNote: options["decision-note"], IdempotencyKey: options["idempotency-key"]}
	client := a.newClient(socket)
	var result localapi.OutcomeMutationResult
	var err error
	operation := "reject outcome assessment"
	if accept {
		operation = "accept outcome assessment"
		result, err = client.OutcomeAccept(ctx, params)
	} else {
		result, err = client.OutcomeReject(ctx, params)
	}
	if err != nil {
		return a.writeClientFailure(mode, operation, err)
	}
	return a.writeOutcomeResult(mode, result, outcomeDetailText(result.Detail))
}

func (a *App) runCheckpoint(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, checkpointHelp)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	switch args[0] {
	case "create":
		return a.runCheckpointCreate(ctx, mode, args[1:])
	case "show":
		return a.runCheckpointShow(ctx, mode, args[1:])
	case "list":
		return a.runCheckpointList(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown checkpoint command "+args[0], "run 'crewfold help checkpoint' for usage"))
	}
}

func (a *App) runCheckpointCreate(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "objective", "task", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	scopeType, scopeID, failure := outcomeScope(options, workspace)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).CheckpointCreate(ctx, localapi.CheckpointCreateParams{Workspace: workspace, ScopeType: scopeType, ScopeIdentifier: scopeID, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "create owner checkpoint", err)
	}
	return a.writeOutcomeResult(mode, result, fmt.Sprintf("checkpoint: %s\nscope: %s %s\nevent sequence: %d\n", result.Checkpoint.ID, result.Checkpoint.ScopeType, result.Checkpoint.ScopeID, result.Checkpoint.EventSequence))
}

func (a *App) runCheckpointShow(ctx context.Context, mode outputMode, args []string) int {
	checkpoint, rest, failure := requiredLeadingArgument(args, "checkpoint ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).CheckpointShow(ctx, workspace, checkpoint)
	if err != nil {
		return a.writeClientFailure(mode, "show owner checkpoint", err)
	}
	return a.writeOutcomeResult(mode, result, fmt.Sprintf("checkpoint: %s\nscope: %s %s\nevent sequence: %d\n", result.Checkpoint.ID, result.Checkpoint.ScopeType, result.Checkpoint.ScopeID, result.Checkpoint.EventSequence))
}

func (a *App) runCheckpointList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "objective", "task", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	scopeType, scopeID, failure := outcomeScope(options, workspace)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalIntOption(options, "limit", 0, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).CheckpointList(ctx, localapi.CheckpointQueryParams{Workspace: workspace, ScopeType: scopeType, ScopeIdentifier: scopeID, Limit: int(limit)})
	if err != nil {
		return a.writeClientFailure(mode, "list owner checkpoints", err)
	}
	if mode == outputJSON {
		return a.writeOutcomeResult(mode, result, "")
	}
	for _, checkpoint := range result.Checkpoints {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\tevent %d\n", checkpoint.ID, checkpoint.ScopeType, checkpoint.ScopeID, checkpoint.EventSequence)
	}
	return ExitOK
}

func (a *App) runBriefing(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, briefingHelp)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	switch args[0] {
	case "show":
		return a.runBriefingShow(ctx, mode, args[1:])
	case "explain":
		return a.runBriefingExplain(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown briefing command "+args[0], "run 'crewfold help briefing' for usage"))
	}
}

func (a *App) runBriefingShow(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "objective", "task", "since", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	scopeType, scopeID, failure := outcomeScope(options, workspace)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).BriefingShow(ctx, localapi.BriefingShowParams{Workspace: workspace, ScopeType: scopeType, ScopeIdentifier: scopeID, SinceCheckpoint: options["since"]})
	if err != nil {
		return a.writeClientFailure(mode, "show management briefing", err)
	}
	if mode == outputJSON {
		return a.writeOutcomeResult(mode, result, "")
	}
	briefing := result.Briefing
	fmt.Fprintf(a.stdout, "briefing: %s\nscope: %s\nevent cursor: %d\nclaims: %d\nbytes: %d\n", briefing.ID, briefing.Scope.Type, briefing.EventCursor, len(briefing.Claims), briefing.ByteSize)
	for _, claim := range briefing.Claims {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\n", claim.ID, claim.Kind, claim.Urgency, claim.Summary)
	}
	for _, omitted := range briefing.Omitted {
		fmt.Fprintf(a.stdout, "omitted\t%s\t%s\t%d\n", omitted.Section, omitted.Reason, omitted.Count)
	}
	return ExitOK
}

func (a *App) runBriefingExplain(ctx context.Context, mode outputMode, args []string) int {
	claim, rest, failure := requiredLeadingArgument(args, "briefing claim ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "briefing", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	briefing, failure := requiredOption(options, "briefing")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).BriefingExplain(ctx, localapi.BriefingExplainParams{Workspace: workspace, Briefing: briefing, Claim: claim})
	if err != nil {
		return a.writeClientFailure(mode, "explain management briefing claim", err)
	}
	if mode == outputJSON {
		return a.writeOutcomeResult(mode, result, "")
	}
	explanation := result.Explanation
	fmt.Fprintf(a.stdout, "briefing: %s\nclaim: %s\nkind: %s\nstatus: %s\nsummary: %s\nprovenance: %d\n", explanation.BriefingID, explanation.Claim.ID, explanation.Claim.Kind, explanation.Claim.Status, explanation.Claim.Summary, len(explanation.Provenance))
	for _, source := range explanation.Provenance {
		fmt.Fprintf(a.stdout, "%s\t%s\trevision %d\tevent %d\n", source.EntityType, source.EntityID, source.Revision, source.EventSequence)
	}
	return ExitOK
}

func outcomeScope(options map[string]string, workspace string) (string, string, *commandFailure) {
	type candidate struct {
		kind string
		id   string
	}
	selected := []candidate{}
	for _, value := range []candidate{{domain.OwnerCheckpointProject, options["project"]}, {domain.OwnerCheckpointObjective, options["objective"]}, {domain.OwnerCheckpointTask, options["task"]}} {
		if strings.TrimSpace(value.id) != "" {
			selected = append(selected, value)
		}
	}
	if len(selected) > 1 {
		failure := usageFailure("scope accepts at most one of --project, --objective, or --task", "omit all three for workspace scope")
		return "", "", &failure
	}
	if len(selected) == 0 {
		return domain.OwnerCheckpointWorkspace, workspace, nil
	}
	return selected[0].kind, selected[0].id, nil
}

func outcomeProposalPath(args []string) (string, []string, *commandFailure) {
	path := ""
	options := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if strings.HasPrefix(argument, "--") {
			options = append(options, argument)
			if !strings.Contains(argument, "=") {
				if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
					failure := usageFailure(argument+" requires a value", "run 'crewfold help outcome' for usage")
					return "", nil, &failure
				}
				index++
				options = append(options, args[index])
			}
			continue
		}
		if path != "" {
			failure := usageFailure(fmt.Sprintf("unexpected positional argument %q", argument), "outcome propose accepts exactly one proposal file")
			return "", nil, &failure
		}
		path = argument
	}
	if strings.TrimSpace(path) == "" {
		failure := usageFailure("outcome propose requires a proposal file", "run 'crewfold help outcome' for usage")
		return "", nil, &failure
	}
	return path, options, nil
}

type outcomeProposalDocument struct {
	Commitment string                        `json:"commitment"`
	Assessment domain.OutcomeAssessmentInput `json:"assessment"`
}

func readOutcomeProposalDocument(path string) (outcomeProposalDocument, error) {
	file, err := os.Open(path)
	if err != nil {
		return outcomeProposalDocument{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumOutcomeInputBytes+1))
	if err != nil {
		return outcomeProposalDocument{}, err
	}
	if len(data) == 0 || len(data) > maximumOutcomeInputBytes {
		return outcomeProposalDocument{}, fmt.Errorf("proposal file must contain 1 to %d bytes", maximumOutcomeInputBytes)
	}
	if !utf8.Valid(data) {
		return outcomeProposalDocument{}, errors.New("proposal file is not valid UTF-8")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return outcomeProposalDocument{}, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return outcomeProposalDocument{}, err
		}
		return outcomeProposalDocument{}, errors.New("proposal file contains more than one document")
	}
	value, err := strictYAMLValue(&root)
	if err != nil {
		return outcomeProposalDocument{}, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return outcomeProposalDocument{}, errors.New("proposal document must be an object")
	}
	if err := validateOutcomeProposalShape(object); err != nil {
		return outcomeProposalDocument{}, err
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return outcomeProposalDocument{}, err
	}
	jsonDecoder := json.NewDecoder(bytes.NewReader(encoded))
	jsonDecoder.DisallowUnknownFields()
	var document outcomeProposalDocument
	if err := jsonDecoder.Decode(&document); err != nil {
		return outcomeProposalDocument{}, err
	}
	if strings.TrimSpace(document.Commitment) == "" || strings.TrimSpace(document.Assessment.Conclusion) == "" {
		return outcomeProposalDocument{}, errors.New("proposal requires commitment and assessment.conclusion")
	}
	return document, nil
}

func strictYAMLValue(node *yaml.Node) (any, error) {
	if node == nil {
		return nil, errors.New("proposal document is empty")
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) != 1 {
			return nil, errors.New("proposal document must contain one value")
		}
		return strictYAMLValue(node.Content[0])
	}
	switch node.Kind {
	case yaml.MappingNode:
		result := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || strings.TrimSpace(key.Value) == "" {
				return nil, errors.New("proposal object keys must be non-empty strings")
			}
			if _, duplicate := result[key.Value]; duplicate {
				return nil, fmt.Errorf("duplicate proposal field %q", key.Value)
			}
			value, err := strictYAMLValue(node.Content[index+1])
			if err != nil {
				return nil, err
			}
			result[key.Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := strictYAMLValue(child)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, nil
	case yaml.ScalarNode:
		if node.Tag == "!!str" {
			return node.Value, nil
		}
		return nil, fmt.Errorf("proposal scalar %q must be a string", node.Value)
	case yaml.AliasNode:
		return nil, errors.New("proposal aliases are not allowed")
	default:
		return nil, errors.New("proposal contains an unsupported YAML value")
	}
}

func validateOutcomeProposalShape(document map[string]any) error {
	for _, name := range []string{"commitment", "assessment"} {
		if _, exists := document[name]; !exists {
			return fmt.Errorf("proposal requires %q", name)
		}
	}
	assessment, ok := document["assessment"].(map[string]any)
	if !ok {
		return errors.New("proposal assessment must be an object")
	}
	for _, name := range []string{"conclusion", "delivered_scope", "unmet_scope", "decision_revision_ids", "evidence", "effects", "deviations", "risks", "unknowns", "follow_up_task_ids", "owner_attention"} {
		value, exists := assessment[name]
		if !exists {
			return fmt.Errorf("proposal assessment requires %q", name)
		}
		if name != "conclusion" {
			if _, array := value.([]any); !array {
				return fmt.Errorf("proposal assessment %q must be an array", name)
			}
		}
	}
	return nil
}

func outcomeDetailText(detail domain.OutcomeAssessmentDetail) string {
	assessment := detail.Assessment
	return fmt.Sprintf("outcome: %s\ncommitment: %s\nreview state: %s\nconclusion: %s\nrevision: %d\nstate revision: %d\n", assessment.ID, assessment.CommitmentID, assessment.ReviewState, assessment.Conclusion, assessment.Revision, assessment.StateRevision)
}

func (a *App) writeOutcomeResult(mode outputMode, value any, text string) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, value); err != nil {
			return a.writeFailure(outputText, internalFailure("write outcome output", err))
		}
	} else {
		fmt.Fprint(a.stdout, text)
	}
	return ExitOK
}

const outcomeHelp = `Usage:
  crewfold outcome commitment add <key> --task <task> --title <title> --criterion <text> [--criterion <text> ...] --workspace <scope> --socket <path>
  crewfold outcome commitment show <commitment-id> --workspace <scope> --socket <path>
  crewfold outcome commitment list --workspace <scope> --socket <path> [--project <project>|--objective <objective>|--task <task>]
  crewfold outcome propose --task <task> <outcome.yaml> --workspace <scope> --socket <path> [--supersedes <outcome-id>]
  crewfold outcome show <outcome-id> --workspace <scope> --socket <path>
  crewfold outcome list --workspace <scope> --socket <path> [filters]
  crewfold outcome accept <outcome-id> --expected-state-revision <n> --workspace <scope> --socket <path>
  crewfold outcome reject <outcome-id> --expected-state-revision <n> --workspace <scope> --socket <path>

Commitments are explicit owner promises and must exist before assessment. The
proposal file is one strict JSON or YAML document containing exactly commitment
and assessment. Only the local owner can
propose or decide an outcome. Run completion, handoff, and check results remain
linked evidence and never imply accepted delivery.
`

const checkpointHelp = `Usage:
  crewfold checkpoint create --workspace <scope> --socket <path> [--project <project>|--objective <objective>|--task <task>]
  crewfold checkpoint show <checkpoint-id> --workspace <scope> --socket <path>
  crewfold checkpoint list --workspace <scope> --socket <path> [--project <project>|--objective <objective>|--task <task>]

Omit project, objective, and task for an immutable workspace checkpoint. A
checkpoint freezes one exact event cursor and has no archive lifecycle.
`

const briefingHelp = `Usage:
  crewfold briefing show --workspace <scope> --socket <path> [--project <project>|--objective <objective>|--task <task>] [--since <checkpoint-id>]
  crewfold briefing explain <claim-id> --briefing <briefing-id> --workspace <scope> --socket <path>

Briefings are deterministic structured projections bounded to 128 whole claims
and 64 KiB. Every material claim has exact durable provenance. Structured
briefing data is the complete representation and only the local owner can
create or decide outcome assessments.
`
