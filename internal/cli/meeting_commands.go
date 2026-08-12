package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func (a *App) runMeeting(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("meeting requires a subcommand", "run 'crewfold help meeting' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, meetingHelp)
		return ExitOK
	}
	switch args[0] {
	case "create":
		return a.runMeetingCreate(ctx, mode, args[1:])
	case "run":
		return a.runMeetingRun(ctx, mode, args[1:])
	case "inspect":
		return a.runMeetingInspect(ctx, mode, args[1:])
	case "accept":
		return a.runMeetingAccept(ctx, mode, args[1:])
	case "takeover":
		return a.runMeetingTakeover(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("unknown meeting command %q", args[0]), "run 'crewfold help meeting' for usage"))
	}
}

func (a *App) runMeetingCreate(ctx context.Context, mode outputMode, args []string) int {
	options, repeated, failure := parseRepeatedOptions(args, map[string]bool{"participant": true, "allow-action": true}, "from-overlap", "participant", "facilitator", "policy", "reviewer", "allow-action", "timeout", "workspace", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	overlap, failure := requiredOption(options, "from-overlap")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	facilitator, failure := requiredOption(options, "facilitator")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	participants := repeated["participant"]
	if len(participants) < 2 || len(participants) > 3 {
		return a.writeFailure(mode, usageFailure("meeting create requires --participant two or three times", "run 'crewfold help meeting' for usage"))
	}
	timeout := 30 * time.Minute
	if options["timeout"] != "" {
		parsed, err := time.ParseDuration(options["timeout"])
		if err != nil || parsed < time.Second || parsed > 7*24*time.Hour {
			return a.writeFailure(mode, usageFailure("--timeout must be a duration from 1s through 168h", "run 'crewfold help meeting' for usage"))
		}
		timeout = parsed
	}
	result, err := a.newClient(socket).MeetingCreate(ctx, localapi.MeetingCreateParams{
		Workspace: workspace, Overlap: overlap, Participants: participants, Facilitator: facilitator,
		Policy: options["policy"], Reviewer: options["reviewer"], AllowedActions: repeated["allow-action"],
		TimeoutSeconds: int64(timeout / time.Second), IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "create meeting", err)
	}
	return a.writeMeetingMutation(mode, result)
}

func (a *App) runMeetingRun(ctx context.Context, mode outputMode, args []string) int {
	meeting, optionArgs, failure := requiredLeadingArgument(args, "meeting ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "fixture", "expected-revision", "socket", "idempotency-key")
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
	fixturePath, failure := requiredOption(options, "fixture")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	var fixture domain.MeetingRunFixture
	if err := readStrictJSONFile(fixturePath, &fixture); err != nil {
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("read meeting fixture: %v", err), "provide a valid meeting fixture JSON file"))
	}
	result, err := a.newClient(socket).MeetingRun(ctx, localapi.MeetingRunParams{Workspace: workspace, Meeting: meeting, ExpectedRevision: revision, Fixture: fixture, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "run meeting", err)
	}
	return a.writeMeetingMutation(mode, result)
}

func (a *App) runMeetingInspect(ctx context.Context, mode outputMode, args []string) int {
	meeting, optionArgs, failure := requiredLeadingArgument(args, "meeting ID")
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
	result, err := a.newClient(socket).MeetingInspect(ctx, workspace, meeting)
	if err != nil {
		return a.writeClientFailure(mode, "inspect meeting", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write meeting inspection", err))
		}
	} else {
		writeMeetingDetail(a, result.Detail)
	}
	return ExitOK
}

func (a *App) runMeetingAccept(ctx context.Context, mode outputMode, args []string) int {
	meeting, optionArgs, failure := requiredLeadingArgument(args, "meeting ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "expected-revision", "note", "socket", "idempotency-key")
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
	result, err := a.newClient(socket).MeetingAccept(ctx, localapi.MeetingAcceptParams{Workspace: workspace, Meeting: meeting, ExpectedRevision: revision, DecisionNote: options["note"], IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "accept meeting", err)
	}
	return a.writeMeetingMutation(mode, result)
}

func (a *App) runMeetingTakeover(ctx context.Context, mode outputMode, args []string) int {
	meeting, optionArgs, failure := requiredLeadingArgument(args, "meeting ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "proposal", "expected-revision", "note", "socket", "idempotency-key")
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
	proposalPath, failure := requiredOption(options, "proposal")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	var proposal domain.MeetingProposalInput
	if err := readStrictJSONFile(proposalPath, &proposal); err != nil {
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("read takeover proposal: %v", err), "provide a valid meeting proposal JSON file"))
	}
	result, err := a.newClient(socket).MeetingTakeover(ctx, localapi.MeetingTakeoverParams{Workspace: workspace, Meeting: meeting, ExpectedRevision: revision, Proposal: proposal, DecisionNote: options["note"], IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "take over meeting", err)
	}
	return a.writeMeetingMutation(mode, result)
}

func (a *App) writeMeetingMutation(mode outputMode, result localapi.MeetingMutationResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write meeting mutation", err))
		}
	} else {
		writeMeetingDetail(a, result.Detail)
	}
	return ExitOK
}

func writeMeetingDetail(a *App, detail domain.MeetingDetail) {
	fmt.Fprintf(a.stdout, "meeting: %s\nstatus: %s\nrevision: %d\npolicy: %s\ninput: %s\n", detail.Meeting.ID, detail.Meeting.Status, detail.Meeting.Revision, detail.Meeting.Policy, detail.Meeting.FrozenInputHash)
	if detail.Meeting.StalledReason != "" {
		fmt.Fprintf(a.stdout, "stalled: %s\n", detail.Meeting.StalledReason)
	}
	for _, participant := range detail.Participants {
		fmt.Fprintf(a.stdout, "participant: %s status=%s task=%s\n", participant.AgentID, participant.Status, participant.TaskID)
	}
	if detail.Proposal != nil {
		fmt.Fprintf(a.stdout, "proposal: %s status=%s actions=%d\n", detail.Proposal.ID, detail.Proposal.Status, len(detail.Actions))
	}
}

func parseRepeatedOptions(args []string, repeatable map[string]bool, allowedNames ...string) (map[string]string, map[string][]string, *commandFailure) {
	allowed := make(map[string]struct{}, len(allowedNames))
	for _, name := range allowedNames {
		allowed[name] = struct{}{}
	}
	options := make(map[string]string)
	repeated := make(map[string][]string)
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if !strings.HasPrefix(argument, "--") {
			failure := usageFailure(fmt.Sprintf("unexpected positional argument %q", argument), "run the command with --help for usage")
			return nil, nil, &failure
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(argument, "--"), "=")
		if _, ok := allowed[name]; !ok {
			failure := usageFailure(fmt.Sprintf("unknown option --%s", name), "run the command with --help for usage")
			return nil, nil, &failure
		}
		if !hasValue {
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
				failure := usageFailure(fmt.Sprintf("--%s requires a value", name), "run the command with --help for usage")
				return nil, nil, &failure
			}
			index++
			value = args[index]
		}
		value = strings.TrimSpace(value)
		if value == "" {
			failure := usageFailure(fmt.Sprintf("--%s requires a non-empty value", name), "run the command with --help for usage")
			return nil, nil, &failure
		}
		if repeatable[name] {
			repeated[name] = append(repeated[name], value)
			continue
		}
		if _, duplicate := options[name]; duplicate {
			failure := usageFailure(fmt.Sprintf("--%s may be specified only once", name), "remove the duplicate option")
			return nil, nil, &failure
		}
		options[name] = value
	}
	return options, repeated, nil
}

func readStrictJSONFile(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return fmt.Errorf("file contains more than one JSON value")
	}
	return nil
}

const meetingHelp = `Usage:
  crewfold meeting create --from-overlap <id> --participant <agent> --participant <agent> --facilitator <agent> --workspace <scope> --socket <path> [options]
  crewfold meeting run <meeting-id> --fixture <path> --expected-revision <n> --workspace <scope> --socket <path>
  crewfold meeting inspect <meeting-id> --workspace <scope> --socket <path>
  crewfold meeting accept <meeting-id> --expected-revision <n> --workspace <scope> --socket <path> [--note <text>]
  crewfold meeting takeover <meeting-id> --proposal <path> --expected-revision <n> --workspace <scope> --socket <path> [--note <text>]

Create accepts two or three repeated --participant options. Policies are
owner_decision (default), named_reviewer (requires --reviewer), and
manager_bounded (requires one or more repeated --allow-action options).
Allowed action names are sequence, split, reassign, designate_role, and cancel.
Run fixtures contain independent participant positions followed by one typed
facilitator proposal. Owner-policy proposals never mutate work until accepted.
`
