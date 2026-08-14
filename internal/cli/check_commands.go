package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"crewfold/internal/localapi"
)

type checkDaemonClient interface {
	CheckDefinitionCreate(context.Context, localapi.CheckDefinitionCreateParams) (localapi.CheckDefinitionMutationResult, error)
	CheckDefinitionRetire(context.Context, localapi.CheckDefinitionRetireParams) (localapi.CheckDefinitionMutationResult, error)
	CheckDefinitionShow(context.Context, string, string) (localapi.CheckDefinitionShowResult, error)
	CheckDefinitionList(context.Context, localapi.CheckDefinitionQueryParams) (localapi.CheckDefinitionListResult, error)
	CheckRequirementCreate(context.Context, localapi.CheckRequirementCreateParams) (localapi.CheckRequirementMutationResult, error)
	CheckRequirementRetire(context.Context, localapi.CheckRequirementRetireParams) (localapi.CheckRequirementMutationResult, error)
	CheckRequirementList(context.Context, localapi.CheckRequirementQueryParams) (localapi.CheckRequirementListResult, error)
	CheckGrantCreate(context.Context, localapi.CheckGrantCreateParams) (localapi.CheckGrantMutationResult, error)
	CheckGrantRevoke(context.Context, localapi.CheckGrantRevokeParams) (localapi.CheckGrantMutationResult, error)
	CheckGrantShow(context.Context, string, string) (localapi.CheckGrantShowResult, error)
	CheckGrantList(context.Context, localapi.CheckGrantQueryParams) (localapi.CheckGrantListResult, error)
	CheckRouteCreate(context.Context, localapi.CheckRouteCreateParams) (localapi.CheckRouteMutationResult, error)
	CheckRouteRetire(context.Context, localapi.CheckRouteRetireParams) (localapi.CheckRouteMutationResult, error)
	CheckRouteList(context.Context, localapi.CheckRouteQueryParams) (localapi.CheckRouteListResult, error)
	CheckPolicyShow(context.Context, string, string) (localapi.CheckPolicyShowResult, error)
	CheckPolicyConfigure(context.Context, localapi.CheckPolicyConfigureParams) (localapi.CheckPolicyMutationResult, error)
	CheckRun(context.Context, localapi.CheckRunParams) (localapi.CheckRunMutationResult, error)
	CheckList(context.Context, localapi.CheckListParams) (localapi.CheckRunListResult, error)
	CheckInspect(context.Context, string, string) (localapi.CheckInspectResult, error)
	CheckLogs(context.Context, string, string) (localapi.CheckLogsResult, error)
	CheckWatch(context.Context, localapi.CheckWatchParams) (localapi.CheckWatchResult, error)
	CheckRepairList(context.Context, localapi.CheckRepairQueryParams) (localapi.CheckRepairListResult, error)
	CheckRepairInspect(context.Context, string, string) (localapi.CheckRepairShowResult, error)
	CheckRepairAccept(context.Context, localapi.CheckRepairDecisionParams) (localapi.CheckRepairMutationResult, error)
	CheckRepairReject(context.Context, localapi.CheckRepairDecisionParams) (localapi.CheckRepairMutationResult, error)
}

func (a *App) runCheck(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, checkHelp)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	switch args[0] {
	case "definition":
		return a.runCheckDefinition(ctx, mode, args[1:])
	case "requirement":
		return a.runCheckRequirement(ctx, mode, args[1:])
	case "grant":
		return a.runCheckGrant(ctx, mode, args[1:])
	case "route":
		return a.runCheckRoute(ctx, mode, args[1:])
	case "policy":
		return a.runCheckPolicy(ctx, mode, args[1:])
	case "run":
		return a.runCheckRun(ctx, mode, args[1:])
	case "list":
		return a.runCheckList(ctx, mode, args[1:])
	case "inspect":
		return a.runCheckInspect(ctx, mode, args[1:])
	case "logs":
		return a.runCheckLogs(ctx, mode, args[1:])
	case "watch":
		return a.runCheckWatch(ctx, mode, args[1:])
	case "repair":
		return a.runCheckRepair(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown check command "+args[0], "run 'crewfold help check' for usage"))
	}
}

func (a *App) checkClient(socket string) checkDaemonClient {
	return a.newClient(socket)
}

func (a *App) writeCheckResult(mode outputMode, value any, text string) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, value); err != nil {
			return a.writeFailure(outputText, internalFailure("write check output", err))
		}
	} else {
		fmt.Fprint(a.stdout, text)
	}
	return ExitOK
}

func (a *App) runCheckDefinition(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("check definition requires create, retire, show, or list", "run 'crewfold help check' for usage"))
	}
	switch args[0] {
	case "create":
		return a.runCheckDefinitionCreate(ctx, mode, args[1:])
	case "retire":
		return a.runCheckDefinitionRetire(ctx, mode, args[1:])
	case "show":
		return a.runCheckDefinitionShow(ctx, mode, args[1:])
	case "list":
		return a.runCheckDefinitionList(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown check definition command "+args[0], "run 'crewfold help check' for usage"))
	}
}

func (a *App) runCheckDefinitionCreate(ctx context.Context, mode outputMode, args []string) int {
	name, rest, failure := requiredLeadingArgument(args, "check definition name")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, repeated, failure := parseCheckRepeatedOptions(rest, []string{"arg"}, "workspace", "project", "executable", "arg", "working-directory", "timeout", "output-byte-limit", "socket", "idempotency-key")
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
	executable, failure := requiredOption(options, "executable")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workingDirectory, failure := requiredOption(options, "working-directory")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	timeoutText, failure := requiredOption(options, "timeout")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	timeout, err := time.ParseDuration(timeoutText)
	if err != nil || timeout < 100*time.Millisecond || timeout > time.Hour {
		return a.writeFailure(mode, usageFailure("--timeout must be a duration from 100ms through 1h", "for example --timeout 10m"))
	}
	outputLimit, failure := requiredInt64Option(options, "output-byte-limit", 1024, 1024*1024)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	if len(repeated["arg"]) > 64 {
		return a.writeFailure(mode, usageFailure("--arg may be specified at most 64 times", "use one fixed bounded argument vector"))
	}
	arguments := repeated["arg"]
	if arguments == nil {
		arguments = []string{}
	}
	client := a.checkClient(socket)
	result, err := client.CheckDefinitionCreate(ctx, localapi.CheckDefinitionCreateParams{Workspace: workspace, Project: project, Name: name, Executable: executable, Arguments: arguments, WorkingDirectory: workingDirectory, TimeoutMillis: timeout.Milliseconds(), OutputByteLimit: outputLimit, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "create check definition", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check definition: %s\nname: %s\nstatus: %s\ncontent revision: %d\nrevision: %d\n", result.Definition.ID, result.Definition.Name, result.Definition.Status, result.Definition.ContentRevision, result.Definition.Revision))
}

func (a *App) runCheckDefinitionRetire(ctx context.Context, mode outputMode, args []string) int {
	definition, rest, failure := requiredLeadingArgument(args, "check definition ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "expected-revision", "reason", "socket", "idempotency-key")
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
	reason, failure := requiredOption(options, "reason")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckDefinitionRetire(ctx, localapi.CheckDefinitionRetireParams{Workspace: workspace, Definition: definition, ExpectedRevision: revision, Reason: reason, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "retire check definition", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check definition: %s\nstatus: %s\nrevision: %d\n", result.Definition.ID, result.Definition.Status, result.Definition.Revision))
}

func (a *App) runCheckDefinitionShow(ctx context.Context, mode outputMode, args []string) int {
	definition, rest, failure := requiredLeadingArgument(args, "check definition ID or name")
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
	client := a.checkClient(socket)
	result, err := client.CheckDefinitionShow(ctx, workspace, definition)
	if err != nil {
		return a.writeClientFailure(mode, "show check definition", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check definition: %s\nname: %s\ncommand: %s %s\nstatus: %s\ncontent revision: %d\nrevision: %d\n", result.Definition.ID, result.Definition.Name, result.Definition.Executable, strings.Join(result.Definition.Arguments, " "), result.Definition.Status, result.Definition.ContentRevision, result.Definition.Revision))
}

func (a *App) runCheckDefinitionList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "status", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalIntOption(options, "limit", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckDefinitionList(ctx, localapi.CheckDefinitionQueryParams{Workspace: workspace, Project: options["project"], Status: options["status"], Limit: int(limit)})
	if err != nil {
		return a.writeClientFailure(mode, "list check definitions", err)
	}
	if mode == outputJSON {
		return a.writeCheckResult(mode, result, "")
	}
	for _, definition := range result.Definitions {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\tcontent %d\trevision %d\n", definition.ID, definition.Name, definition.Status, definition.ContentRevision, definition.Revision)
	}
	return ExitOK
}

func (a *App) runCheckRequirement(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("check requirement requires create, retire, or list", "run 'crewfold help check' for usage"))
	}
	switch args[0] {
	case "create":
		return a.runCheckRequirementCreate(ctx, mode, args[1:])
	case "retire":
		return a.runCheckRequirementRetire(ctx, mode, args[1:])
	case "list":
		return a.runCheckRequirementList(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown check requirement command "+args[0], "run 'crewfold help check' for usage"))
	}
}

func (a *App) runCheckRequirementCreate(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "task", "criterion", "statement", "definition", "definition-content-revision", "expected-task-revision", "socket", "idempotency-key")
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
	criterion, failure := requiredOption(options, "criterion")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	statement, failure := requiredOption(options, "statement")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	definition, failure := requiredOption(options, "definition")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	definitionRevision, failure := requiredInt64Option(options, "definition-content-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	taskRevision, failure := requiredInt64Option(options, "expected-task-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckRequirementCreate(ctx, localapi.CheckRequirementCreateParams{Workspace: workspace, Task: task, CriterionKey: criterion, Statement: statement, Definition: definition, DefinitionContentRevision: definitionRevision, ExpectedTaskRevision: taskRevision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "create check requirement", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check requirement: %s\ncriterion: %s\nstatus: %s\nrevision: %d\n", result.Requirement.ID, result.Requirement.CriterionKey, result.Requirement.Status, result.Requirement.Revision))
}

func (a *App) runCheckRequirementRetire(ctx context.Context, mode outputMode, args []string) int {
	requirement, rest, failure := requiredLeadingArgument(args, "check requirement ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "expected-revision", "reason", "socket", "idempotency-key")
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
	reason, failure := requiredOption(options, "reason")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckRequirementRetire(ctx, localapi.CheckRequirementRetireParams{Workspace: workspace, Requirement: requirement, ExpectedRevision: revision, Reason: reason, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "retire check requirement", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check requirement: %s\nstatus: %s\nrevision: %d\n", result.Requirement.ID, result.Requirement.Status, result.Requirement.Revision))
}

func (a *App) runCheckRequirementList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "task", "status", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalIntOption(options, "limit", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckRequirementList(ctx, localapi.CheckRequirementQueryParams{Workspace: workspace, Project: options["project"], Task: options["task"], Status: options["status"], Limit: int(limit)})
	if err != nil {
		return a.writeClientFailure(mode, "list check requirements", err)
	}
	if mode == outputJSON {
		return a.writeCheckResult(mode, result, "")
	}
	for _, view := range result.Requirements {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\trevision %d\n", view.Requirement.ID, view.Requirement.CriterionKey, view.Requirement.Status, view.State, view.Requirement.Revision)
	}
	return ExitOK
}

func (a *App) runCheckGrant(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("check grant requires create, revoke, show, or list", "run 'crewfold help check' for usage"))
	}
	switch args[0] {
	case "create":
		return a.runCheckGrantCreate(ctx, mode, args[1:])
	case "revoke":
		return a.runCheckGrantRevoke(ctx, mode, args[1:])
	case "show":
		return a.runCheckGrantShow(ctx, mode, args[1:])
	case "list":
		return a.runCheckGrantList(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown check grant command "+args[0], "run 'crewfold help check' for usage"))
	}
}

func (a *App) runCheckGrantCreate(ctx context.Context, mode outputMode, args []string) int {
	options, repeated, failure := parseCheckRepeatedOptions(args, []string{"definition", "operation"}, "workspace", "project", "agent", "expected-agent-revision", "definition", "operation", "max-pending", "max-in-flight", "expires-at", "socket", "idempotency-key")
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
	agent, failure := requiredOption(options, "agent")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	agentRevision, failure := requiredInt64Option(options, "expected-agent-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	if len(repeated["definition"]) == 0 || len(repeated["definition"]) > 64 {
		return a.writeFailure(mode, usageFailure("check grant create requires 1 to 64 --definition NAME@REV values", "repeat --definition for every exact allowed definition revision"))
	}
	definitions := make([]localapi.CheckDefinitionContentRevisionParam, 0, len(repeated["definition"]))
	seenDefinitions := map[string]bool{}
	for _, frozen := range repeated["definition"] {
		definition, revision, failure := parseDefinitionRevision(frozen)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		key := definition + "@" + strconv.FormatInt(revision, 10)
		if seenDefinitions[key] {
			return a.writeFailure(mode, usageFailure("duplicate --definition "+key, "supply each exact definition revision once"))
		}
		seenDefinitions[key] = true
		definitions = append(definitions, localapi.CheckDefinitionContentRevisionParam{Definition: definition, ContentRevision: revision})
	}
	operations := repeated["operation"]
	if len(operations) == 0 || len(operations) > 3 {
		return a.writeFailure(mode, usageFailure("check grant create requires 1 to 3 --operation values", "use run, inspect, or propose_repair"))
	}
	seenOperations := map[string]bool{}
	for _, operation := range operations {
		if operation != "run" && operation != "inspect" && operation != "propose_repair" {
			return a.writeFailure(mode, usageFailure("unsupported check grant operation "+operation, "use run, inspect, or propose_repair"))
		}
		if seenOperations[operation] {
			return a.writeFailure(mode, usageFailure("duplicate --operation "+operation, "supply each operation once"))
		}
		seenOperations[operation] = true
	}
	maxPending, failure := requiredInt64Option(options, "max-pending", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	maxInFlight, failure := requiredInt64Option(options, "max-in-flight", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckGrantCreate(ctx, localapi.CheckGrantCreateParams{Workspace: workspace, Project: project, Agent: agent, ExpectedAgentRevision: agentRevision, Definitions: definitions, Operations: operations, MaxPending: int(maxPending), MaxInFlight: int(maxInFlight), ExpiresAt: options["expires-at"], IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "create check-watch grant", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check-watch grant: %s\nagent: %s\nstatus: %s\nrevision: %d\n", result.Grant.ID, result.Grant.AgentID, result.Grant.Status, result.Grant.Revision))
}

func (a *App) runCheckGrantRevoke(ctx context.Context, mode outputMode, args []string) int {
	grant, rest, failure := requiredLeadingArgument(args, "check-watch grant ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "expected-revision", "reason", "socket", "idempotency-key")
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
	reason, failure := requiredOption(options, "reason")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckGrantRevoke(ctx, localapi.CheckGrantRevokeParams{Workspace: workspace, Grant: grant, ExpectedRevision: revision, Reason: reason, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "revoke check-watch grant", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check-watch grant: %s\nstatus: %s\nrevision: %d\n", result.Grant.ID, result.Grant.Status, result.Grant.Revision))
}

func (a *App) runCheckGrantShow(ctx context.Context, mode outputMode, args []string) int {
	grant, rest, failure := requiredLeadingArgument(args, "check-watch grant ID")
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
	client := a.checkClient(socket)
	result, err := client.CheckGrantShow(ctx, workspace, grant)
	if err != nil {
		return a.writeClientFailure(mode, "show check-watch grant", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check-watch grant: %s\nagent: %s\noperations: %s\nstatus: %s\nrevision: %d\n", result.Grant.ID, result.Grant.AgentID, strings.Join(result.Grant.Operations, ","), result.Grant.Status, result.Grant.Revision))
}

func (a *App) runCheckGrantList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "agent", "status", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalIntOption(options, "limit", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckGrantList(ctx, localapi.CheckGrantQueryParams{Workspace: workspace, Project: options["project"], Agent: options["agent"], Status: options["status"], Limit: int(limit)})
	if err != nil {
		return a.writeClientFailure(mode, "list check-watch grants", err)
	}
	if mode == outputJSON {
		return a.writeCheckResult(mode, result, "")
	}
	for _, grant := range result.Grants {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\trevision %d\n", grant.ID, grant.AgentID, strings.Join(grant.Operations, ","), grant.Status, grant.Revision)
	}
	return ExitOK
}

func (a *App) runCheckRoute(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("check route requires create, retire, or list", "run 'crewfold help check' for usage"))
	}
	switch args[0] {
	case "create":
		return a.runCheckRouteCreate(ctx, mode, args[1:])
	case "retire":
		return a.runCheckRouteRetire(ctx, mode, args[1:])
	case "list":
		return a.runCheckRouteList(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown check route command "+args[0], "run 'crewfold help check' for usage"))
	}
}

func (a *App) runCheckRouteCreate(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "definition", "definition-content-revision", "trigger", "duty", "agent", "expected-agent-revision", "socket", "idempotency-key")
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
	trigger, failure := requiredEnumOption(options, "trigger", "pass", "nonpass", "stale")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	duty, failure := requiredEnumOption(options, "duty", "evidence_review", "coordination")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	agent, failure := requiredOption(options, "agent")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	agentRevision, failure := requiredInt64Option(options, "expected-agent-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	definition := options["definition"]
	definitionRevisionText := options["definition-content-revision"]
	if (definition == "") != (definitionRevisionText == "") {
		return a.writeFailure(mode, usageFailure("--definition and --definition-content-revision must be provided together", "provide both exact definition fields or omit both"))
	}
	var definitionRevision int64
	if definition != "" {
		definitionRevision, failure = requiredInt64Option(options, "definition-content-revision", 1, 1<<62)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
	}
	client := a.checkClient(socket)
	result, err := client.CheckRouteCreate(ctx, localapi.CheckRouteCreateParams{Workspace: workspace, Project: project, Definition: definition, DefinitionContentRevision: definitionRevision, Trigger: trigger, Duty: duty, Agent: agent, ExpectedAgentRevision: agentRevision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "create check route", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check route: %s\ntrigger: %s\nduty: %s\nagent: %s\nstatus: %s\nrevision: %d\n", result.Route.ID, result.Route.Trigger, result.Route.Duty, result.Route.AgentID, result.Route.Status, result.Route.Revision))
}

func (a *App) runCheckRouteRetire(ctx context.Context, mode outputMode, args []string) int {
	route, rest, failure := requiredLeadingArgument(args, "check route ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "expected-revision", "reason", "socket", "idempotency-key")
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
	client := a.checkClient(socket)
	result, err := client.CheckRouteRetire(ctx, localapi.CheckRouteRetireParams{Workspace: workspace, Route: route, ExpectedRevision: revision, Reason: options["reason"], IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "retire check route", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check route: %s\nstatus: %s\nrevision: %d\n", result.Route.ID, result.Route.Status, result.Route.Revision))
}

func (a *App) runCheckRouteList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "definition", "trigger", "duty", "status", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalIntOption(options, "limit", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckRouteList(ctx, localapi.CheckRouteQueryParams{Workspace: workspace, Project: options["project"], Definition: options["definition"], Trigger: options["trigger"], Duty: options["duty"], Status: options["status"], Limit: int(limit)})
	if err != nil {
		return a.writeClientFailure(mode, "list check routes", err)
	}
	if mode == outputJSON {
		return a.writeCheckResult(mode, result, "")
	}
	for _, route := range result.Routes {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\t%s\trevision %d\n", route.ID, route.Trigger, route.Duty, route.AgentID, route.Status, route.Revision)
	}
	return ExitOK
}

func (a *App) runCheckPolicy(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("check policy requires show or configure", "run 'crewfold help check' for usage"))
	}
	switch args[0] {
	case "show":
		return a.runCheckPolicyShow(ctx, mode, args[1:])
	case "configure":
		return a.runCheckPolicyConfigure(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown check policy command "+args[0], "run 'crewfold help check' for usage"))
	}
}

func (a *App) runCheckPolicyShow(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "socket")
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
	client := a.checkClient(socket)
	result, err := client.CheckPolicyShow(ctx, workspace, project)
	if err != nil {
		return a.writeClientFailure(mode, "show check policy", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("repair proposals enabled: %t\nrepair profile: %s@%d\nmax open repairs: %d\nrevision: %d\n", result.Policy.RepairProposalsEnabled, result.Policy.RepairLaunchProfileID, result.Policy.RepairLaunchProfileRevision, result.Policy.MaxOpenRepairProposals, result.Policy.Revision))
}

func (a *App) runCheckPolicyConfigure(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "repair-proposals", "repair-profile", "repair-profile-revision", "max-open-repairs", "expected-revision", "socket", "idempotency-key")
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
	repairMode, failure := requiredEnumOption(options, "repair-proposals", "enabled", "disabled")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	repairProfile := options["repair-profile"]
	repairProfileRevisionText := options["repair-profile-revision"]
	if (repairProfile == "") != (repairProfileRevisionText == "") {
		return a.writeFailure(mode, usageFailure("--repair-profile and --repair-profile-revision must be provided together", "provide both exact repair profile fields or omit both"))
	}
	if repairMode == "enabled" && repairProfile == "" {
		return a.writeFailure(mode, usageFailure("enabled repair proposals require an exact repair profile revision", "provide --repair-profile and --repair-profile-revision"))
	}
	if repairMode == "disabled" && repairProfile != "" {
		return a.writeFailure(mode, usageFailure("disabled repair proposals cannot name a repair profile", "omit both repair profile fields"))
	}
	var repairProfileRevision int64
	if repairProfile != "" {
		repairProfileRevision, failure = requiredInt64Option(options, "repair-profile-revision", 1, 1<<62)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
	}
	maxOpen, failure := requiredInt64Option(options, "max-open-repairs", 1, 32)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revision, failure := requiredInt64Option(options, "expected-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckPolicyConfigure(ctx, localapi.CheckPolicyConfigureParams{Workspace: workspace, Project: project, RepairProposalsEnabled: repairMode == "enabled", RepairProfile: repairProfile, RepairProfileRevision: repairProfileRevision, MaxOpenRepairs: int(maxOpen), ExpectedRevision: revision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "configure check policy", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("repair proposals enabled: %t\nmax open repairs: %d\nrevision: %d\n", result.Policy.RepairProposalsEnabled, result.Policy.MaxOpenRepairProposals, result.Policy.Revision))
}

func (a *App) runCheckRun(ctx context.Context, mode outputMode, args []string) int {
	definition, rest, failure := requiredLeadingArgument(args, "check definition name or ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "task", "checkout", "expected-requirement-revision", "expected-definition-content-revision", "expected-checkout-revision", "socket", "idempotency-key")
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
	requirementRevision, failure := optionalIntOption(options, "expected-requirement-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	definitionRevision, failure := optionalIntOption(options, "expected-definition-content-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	checkoutRevision, failure := optionalIntOption(options, "expected-checkout-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	if checkoutRevision != 0 && options["checkout"] == "" {
		return a.writeFailure(mode, usageFailure("--expected-checkout-revision requires --checkout", "provide the exact checkout or omit its optimistic revision"))
	}
	client := a.checkClient(socket)
	result, err := client.CheckRun(ctx, localapi.CheckRunParams{Workspace: workspace, Task: task, Definition: definition, Checkout: options["checkout"], ExpectedRequirementRevision: requirementRevision, ExpectedDefinitionContentRevision: definitionRevision, ExpectedCheckoutRevision: checkoutRevision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "request check run", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check run: %s\nstatus: %s\nrequirement: %s\ndefinition: %s@%d\ncheckout: %s\nrevision: %d\n", result.Run.ID, result.Run.Status, result.Run.RequirementID, result.Run.DefinitionID, result.Run.DefinitionContentRevision, result.Run.CheckoutID, result.Run.Revision))
}

func (a *App) runCheckList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "task", "requirement", "definition", "status", "outcome", "cursor", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalIntOption(options, "limit", 1, 200)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckList(ctx, localapi.CheckListParams{Workspace: workspace, Project: options["project"], Task: options["task"], Requirement: options["requirement"], Definition: options["definition"], Status: options["status"], Outcome: options["outcome"], PageParams: localapi.PageParams{Cursor: options["cursor"], Limit: int(limit)}})
	if err != nil {
		return a.writeClientFailure(mode, "list check runs", err)
	}
	if mode == outputJSON {
		return a.writeCheckResult(mode, result, "")
	}
	for _, item := range result.Runs {
		freshness := ""
		if item.CurrentFreshness != nil {
			freshness = item.CurrentFreshness.Status
		}
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\t%s\trevision %d\n", item.Run.ID, item.Run.Status, item.Outcome, freshness, item.RequirementState, item.Run.Revision)
	}
	writePageMetadata(a.stdout, result.PageResult, len(result.Runs))
	return ExitOK
}

func (a *App) runCheckInspect(ctx context.Context, mode outputMode, args []string) int {
	checkRun, rest, failure := requiredLeadingArgument(args, "check run ID")
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
	client := a.checkClient(socket)
	result, err := client.CheckInspect(ctx, workspace, checkRun)
	if err != nil {
		return a.writeClientFailure(mode, "inspect check run", err)
	}
	outcome, freshness := "", ""
	if result.Detail.Result != nil {
		outcome = result.Detail.Result.Outcome
	}
	if result.Detail.CurrentFreshness != nil {
		freshness = result.Detail.CurrentFreshness.Status
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("check run: %s\nstatus: %s\noutcome: %s\nfreshness: %s\nrequirement state: %s\nartifacts: %d\nroute failures: %d\n", result.Detail.Run.ID, result.Detail.Run.Status, outcome, freshness, result.Detail.RequirementState, len(result.Detail.Artifacts), len(result.Detail.RouteFailures)))
}

func (a *App) runCheckLogs(ctx context.Context, mode outputMode, args []string) int {
	checkRun, rest, failure := requiredLeadingArgument(args, "check run ID")
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
	client := a.checkClient(socket)
	result, err := client.CheckLogs(ctx, workspace, checkRun)
	if err != nil {
		return a.writeClientFailure(mode, "read check logs", err)
	}
	if mode == outputJSON {
		return a.writeCheckResult(mode, result, "")
	}
	fmt.Fprintf(a.stdout, "check run: %s\n", result.Logs.CheckRunID)
	if result.Logs.Stdout != nil {
		fmt.Fprintf(a.stdout, "stdout: captured=%d omitted=%d\n%s", result.Logs.Stdout.CapturedBytes, result.Logs.Stdout.OmittedBytes, result.Logs.Stdout.Content)
	}
	if result.Logs.Stderr != nil {
		fmt.Fprintf(a.stdout, "stderr: captured=%d omitted=%d\n%s", result.Logs.Stderr.CapturedBytes, result.Logs.Stderr.OmittedBytes, result.Logs.Stderr.Content)
	}
	if result.Logs.Diagnostic != nil {
		fmt.Fprintf(a.stdout, "diagnostic: captured=%d omitted=%d\n%s", result.Logs.Diagnostic.CapturedBytes, result.Logs.Diagnostic.OmittedBytes, result.Logs.Diagnostic.Content)
	}
	return ExitOK
}

func (a *App) runCheckWatch(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "cursor", "limit", "socket", "idempotency-key")
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
	limit, failure := optionalIntOption(options, "limit", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckWatch(ctx, localapi.CheckWatchParams{Workspace: workspace, Project: project, Cursor: options["cursor"], Limit: int(limit), IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "run check watch pass", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("watch receipt: %s\nexamined: %d\nfreshness appended: %d\nnotifications: %d\nroute failures: %d\nrepairs stale: %d\nnext cursor: %s\n", result.Receipt.ID, len(result.Receipt.ExaminedResultIDs), result.Receipt.FreshnessAppended, result.Receipt.NotificationsCreated, result.Receipt.RouteFailuresCreated, result.Receipt.RepairsMarkedStale, result.Receipt.NextCursor))
}

func (a *App) runCheckRepair(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("check repair requires list, inspect, accept, or reject", "run 'crewfold help check' for usage"))
	}
	switch args[0] {
	case "list":
		return a.runCheckRepairList(ctx, mode, args[1:])
	case "inspect":
		return a.runCheckRepairInspect(ctx, mode, args[1:])
	case "accept":
		return a.runCheckRepairDecision(ctx, mode, args[1:], true)
	case "reject":
		return a.runCheckRepairDecision(ctx, mode, args[1:], false)
	default:
		return a.writeFailure(mode, usageFailure("unknown check repair command "+args[0], "run 'crewfold help check' for usage"))
	}
}

func (a *App) runCheckRepairList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "task", "status", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalIntOption(options, "limit", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.checkClient(socket)
	result, err := client.CheckRepairList(ctx, localapi.CheckRepairQueryParams{Workspace: workspace, Project: options["project"], Task: options["task"], Status: options["status"], Limit: int(limit)})
	if err != nil {
		return a.writeClientFailure(mode, "list check repair proposals", err)
	}
	if mode == outputJSON {
		return a.writeCheckResult(mode, result, "")
	}
	for _, repair := range result.Repairs {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\trevision %d\n", repair.Proposal.ID, repair.Proposal.TaskID, repair.Result.Outcome, repair.Proposal.Status, repair.Proposal.Revision)
	}
	return ExitOK
}

func (a *App) runCheckRepairInspect(ctx context.Context, mode outputMode, args []string) int {
	repair, rest, failure := requiredLeadingArgument(args, "check repair proposal ID")
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
	client := a.checkClient(socket)
	result, err := client.CheckRepairInspect(ctx, workspace, repair)
	if err != nil {
		return a.writeClientFailure(mode, "inspect check repair proposal", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("repair proposal: %s\nstatus: %s\nresult: %s (%s)\npolicy revision: %d\nrevision: %d\nrationale: %s\n", result.Detail.Proposal.ID, result.Detail.Proposal.Status, result.Detail.Result.ID, result.Detail.Result.Outcome, result.Detail.Proposal.PolicyRevision, result.Detail.Proposal.Revision, result.Detail.Proposal.Rationale))
}

func (a *App) runCheckRepairDecision(ctx context.Context, mode outputMode, args []string, accept bool) int {
	repair, rest, failure := requiredLeadingArgument(args, "check repair proposal ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "expected-revision", "decision-note", "socket", "idempotency-key")
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
	client := a.checkClient(socket)
	params := localapi.CheckRepairDecisionParams{Workspace: workspace, Repair: repair, ExpectedRevision: revision, DecisionNote: options["decision-note"], IdempotencyKey: options["idempotency-key"]}
	verb := "reject"
	var result localapi.CheckRepairMutationResult
	var err error
	if accept {
		verb = "accept"
		result, err = client.CheckRepairAccept(ctx, params)
	} else {
		result, err = client.CheckRepairReject(ctx, params)
	}
	if err != nil {
		return a.writeClientFailure(mode, verb+" check repair proposal", err)
	}
	return a.writeCheckResult(mode, result, fmt.Sprintf("repair proposal: %s\nstatus: %s\nrevision: %d\n", result.Detail.Proposal.ID, result.Detail.Proposal.Status, result.Detail.Proposal.Revision))
}

func parseCheckRepeatedOptions(args []string, repeatNames []string, allowedNames ...string) (map[string]string, map[string][]string, *commandFailure) {
	allowed := make(map[string]bool, len(allowedNames))
	for _, name := range allowedNames {
		allowed[name] = true
	}
	repeatedName := make(map[string]bool, len(repeatNames))
	for _, name := range repeatNames {
		repeatedName[name] = true
	}
	options := make(map[string]string, len(allowedNames))
	repeated := make(map[string][]string, len(repeatNames))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if !strings.HasPrefix(argument, "--") {
			failure := usageFailure(fmt.Sprintf("unexpected positional argument %q", argument), "run the command with --help for usage")
			return nil, nil, &failure
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(argument, "--"), "=")
		if !allowed[name] {
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
		if strings.TrimSpace(value) == "" && name != "arg" {
			failure := usageFailure(fmt.Sprintf("--%s requires a non-empty value", name), "run the command with --help for usage")
			return nil, nil, &failure
		}
		if repeatedName[name] {
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

func parseDefinitionRevision(value string) (string, int64, *commandFailure) {
	separator := strings.LastIndex(value, "@")
	if separator < 1 || separator == len(value)-1 {
		failure := usageFailure("--definition must be NAME@REV", "bind one exact active definition content revision")
		return "", 0, &failure
	}
	revision, err := strconv.ParseInt(value[separator+1:], 10, 64)
	if err != nil || revision < 1 {
		failure := usageFailure("--definition revision must be a positive integer", "for example --definition unit@1")
		return "", 0, &failure
	}
	return value[:separator], revision, nil
}

func requiredEnumOption(options map[string]string, name string, allowed ...string) (string, *commandFailure) {
	value, failure := requiredOption(options, name)
	if failure != nil {
		return "", failure
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	failureValue := usageFailure("--"+name+" must be one of "+strings.Join(allowed, ", "), "use one closed supported value")
	return "", &failureValue
}

const checkHelp = `Usage:
  crewfold check definition create <name> --workspace <scope> --project <id> --executable <absolute-path> [--arg <value> ...] --working-directory <relative-path> --timeout <duration> --output-byte-limit <bytes> --socket <path>
  crewfold check definition retire <id> --workspace <scope> --expected-revision <n> --reason <text> --socket <path>
  crewfold check definition show <id-or-name> --workspace <scope> --socket <path>
  crewfold check definition list --workspace <scope> --socket <path> [filters]
  crewfold check requirement create --workspace <scope> --task <id> --criterion <key> --statement <text> --definition <id-or-name> --definition-content-revision <n> --expected-task-revision <n> --socket <path>
  crewfold check requirement retire <id> --workspace <scope> --expected-revision <n> --reason <text> --socket <path>
  crewfold check requirement list --workspace <scope> --socket <path> [filters]
  crewfold check grant create --workspace <scope> --project <id> --agent <id> --expected-agent-revision <n> --definition <name-or-id>@<revision> ... --operation <run|inspect|propose_repair> ... --max-pending <n> --max-in-flight <n> --socket <path>
  crewfold check grant revoke <id> --workspace <scope> --expected-revision <n> --reason <text> --socket <path>
  crewfold check grant show <id> --workspace <scope> --socket <path>
  crewfold check grant list --workspace <scope> --socket <path> [filters]
  crewfold check route create --workspace <scope> --project <id> [--definition <id-or-name> --definition-content-revision <n>] --trigger <pass|nonpass|stale> --duty <evidence_review|coordination> --agent <id> --expected-agent-revision <n> --socket <path>
  crewfold check route retire <id> --workspace <scope> --expected-revision <n> --socket <path>
  crewfold check route list --workspace <scope> --socket <path> [filters]
  crewfold check policy show --workspace <scope> --project <id> --socket <path>
  crewfold check policy configure --workspace <scope> --project <id> --repair-proposals <enabled|disabled> [--repair-profile <id> --repair-profile-revision <n>] --max-open-repairs <n> --expected-revision <n> --socket <path>
  crewfold check run <definition> --task <id> --workspace <scope> [--checkout <id>] [--expected-requirement-revision <n>] [--expected-definition-content-revision <n>] [--expected-checkout-revision <n>] --socket <path>
  crewfold check list --workspace <scope> [filters] [--cursor <cursor>] [--limit <1..200>] --socket <path>
  crewfold check inspect <check-run-id> --workspace <scope> --socket <path>
  crewfold check logs <check-run-id> --workspace <scope> --socket <path>
  crewfold check watch --workspace <scope> --project <id> --socket <path> [--cursor <cursor>] [--limit <1..100>]
  crewfold check repair list --workspace <scope> --socket <path> [filters]
  crewfold check repair inspect <id> --workspace <scope> --socket <path>
  crewfold check repair accept <id> --workspace <scope> --expected-revision <n> [--decision-note <text>] --socket <path>
  crewfold check repair reject <id> --workspace <scope> --expected-revision <n> [--decision-note <text>] --socket <path>

Definitions are owner-authored fixed executable/argv allowlist entries. They have
no shell string, stdin, environment, credentials, provider, MCP, role, or purpose.
Check evidence satisfies only its named criterion and never completes tasks,
pushes, merges, deploys, or selects integration order.
`
