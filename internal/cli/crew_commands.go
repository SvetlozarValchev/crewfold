package cli

import (
	"context"
	"fmt"

	"crewfold/internal/localapi"
)

func (a *App) runCrew(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("crew requires a subcommand", "run 'crewfold help crew' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, crewHelp)
		return ExitOK
	}
	switch args[0] {
	case "add":
		return a.runCrewAdd(ctx, mode, args[1:])
	case "disable":
		return a.runCrewDisable(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_crew_command", message: fmt.Sprintf("unknown crew command %q", args[0]), hint: "run 'crewfold help crew' for usage"})
	}
}

func (a *App) runCrewAdd(ctx context.Context, mode outputMode, args []string) int {
	name, optionArgs, failure := requiredLeadingArgument(args, "worker name")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "project", "provider", "runtime", "max-concurrency", "expected-binding-revision", "socket", "idempotency-key")
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
	provider, failure := requiredOption(options, "provider")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	runtime, failure := requiredOption(options, "runtime")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	maxConcurrency, failure := intOption(options, "max-concurrency", 0, 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revision, failure := requiredInt64Option(options, "expected-binding-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	key, failure := requiredOption(options, "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.newClient(socket)
	workspaceID, projectID, code := a.resolveCrewScope(ctx, mode, client, workspace, project)
	if code != ExitOK {
		return code
	}
	result, err := client.OwnerCrewConfigure(ctx, localapi.OwnerCrewConfigureParams{
		Workspace: workspaceID, Project: projectID, Action: "add", ExpectedBindingRevision: revision,
		Name: name, Provider: provider, Runtime: runtime, MaxConcurrency: maxConcurrency, IdempotencyKey: key,
	})
	if err != nil {
		return a.writeClientFailure(mode, "add implementation worker", err)
	}
	return a.writeCrewMutation(mode, result)
}

func (a *App) runCrewDisable(ctx context.Context, mode outputMode, args []string) int {
	agent, optionArgs, failure := requiredLeadingArgument(args, "worker agent ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "project", "expected-binding-revision", "socket", "idempotency-key")
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
	revision, failure := requiredInt64Option(options, "expected-binding-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	key, failure := requiredOption(options, "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client := a.newClient(socket)
	workspaceID, projectID, code := a.resolveCrewScope(ctx, mode, client, workspace, project)
	if code != ExitOK {
		return code
	}
	result, err := client.OwnerCrewConfigure(ctx, localapi.OwnerCrewConfigureParams{
		Workspace: workspaceID, Project: projectID, Action: "disable", ExpectedBindingRevision: revision,
		Agent: agent, IdempotencyKey: key,
	})
	if err != nil {
		return a.writeClientFailure(mode, "disable implementation worker", err)
	}
	return a.writeCrewMutation(mode, result)
}

func (a *App) resolveCrewScope(ctx context.Context, mode outputMode, client daemonClient, workspace, project string) (string, string, int) {
	resolvedWorkspace, err := client.WorkspaceShow(ctx, workspace)
	if err != nil {
		return "", "", a.writeClientFailure(mode, "resolve crew workspace", err)
	}
	resolvedProject, err := client.ProjectInspect(ctx, resolvedWorkspace.Workspace.ID, project)
	if err != nil {
		return "", "", a.writeClientFailure(mode, "resolve crew project", err)
	}
	return resolvedWorkspace.Workspace.ID, resolvedProject.Project.ID, ExitOK
}

func (a *App) writeCrewMutation(mode outputMode, result localapi.OwnerCrewMutationResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write crew mutation", err))
		}
		return ExitOK
	}
	fmt.Fprintf(a.stdout, "crew_action: %s\nagent: %s (%s)\nenabled: %t\nbinding_revision: %d\nauthorized_workers: %d\nevent_sequence: %d\n", result.Action, result.Agent.Name, result.Agent.ID, result.Agent.Enabled, result.Binding.Revision, len(result.WorkerProfiles), result.EventSequence)
	return ExitOK
}

const crewHelp = `Usage:
  crewfold crew add <worker-name> --workspace <scope> --project <project> --provider <provider> --runtime <runtime> --max-concurrency <1..100> --expected-binding-revision <n> --idempotency-key <key> --socket <path>
  crewfold crew disable <worker-agent-id> --workspace <scope> --project <project> --expected-binding-revision <n> --idempotency-key <key> --socket <path>

Change the implementation workers authorized by a workbench project's exact
executive binding. Adding authorizes future typed proposals but starts no work.
Disabling first proves that the worker retains no accepted or live work. The
final implementation worker cannot be disabled.
`
