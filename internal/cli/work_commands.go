package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func (a *App) runAgent(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("agent requires a subcommand", "run 'crewfold help agent' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, agentHelp)
		return ExitOK
	}
	if len(args) == 2 && isHelp(args[1]) {
		fmt.Fprint(a.stdout, agentHelp)
		return ExitOK
	}
	switch args[0] {
	case "create":
		return a.runAgentCreate(ctx, mode, args[1:])
	case "update":
		return a.runAgentUpdate(ctx, mode, args[1:])
	case "show":
		return a.runAgentShow(ctx, mode, args[1:])
	case "list":
		return a.runAgentList(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_agent_command", message: fmt.Sprintf("unknown agent command %q", args[0]), hint: "run 'crewfold help agent' for usage"})
	}
}

func (a *App) runAgentCreate(ctx context.Context, mode outputMode, args []string) int {
	name, optionArgs, failure := requiredLeadingArgument(args, "agent name")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "role", "provider", "runtime", "max-concurrency", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	role, failure := requiredOption(options, "role")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	provider, failure := requiredOption(options, "provider")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	maxConcurrency := 0
	if _, ok := options["max-concurrency"]; ok {
		maxConcurrency, failure = intOption(options, "max-concurrency", 0, 1, 100)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
	}
	result, err := a.newClient(socket).AgentCreate(ctx, localapi.AgentCreateParams{Workspace: workspace, Name: name, Role: role, Provider: provider, Runtime: options["runtime"], MaxConcurrency: maxConcurrency, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "create agent", err)
	}
	return a.writeAgentMutation(mode, result)
}

func (a *App) runAgentUpdate(ctx context.Context, mode outputMode, args []string) int {
	agent, optionArgs, failure := requiredLeadingArgument(args, "agent name or ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "role", "provider", "runtime", "enabled", "max-concurrency", "expected-revision", "socket", "idempotency-key")
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
	params := localapi.AgentUpdateParams{Workspace: workspace, Agent: agent, ExpectedRevision: revision, IdempotencyKey: options["idempotency-key"]}
	params.Role = optionalString(options, "role")
	params.Provider = optionalString(options, "provider")
	params.Runtime = optionalString(options, "runtime")
	if _, ok := options["enabled"]; ok {
		value, parseFailure := boolOption(options, "enabled")
		if parseFailure != nil {
			return a.writeFailure(mode, *parseFailure)
		}
		params.Enabled = &value
	}
	if _, ok := options["max-concurrency"]; ok {
		value, parseFailure := intOption(options, "max-concurrency", 0, 1, 100)
		if parseFailure != nil {
			return a.writeFailure(mode, *parseFailure)
		}
		params.MaxConcurrency = &value
	}
	result, err := a.newClient(socket).AgentUpdate(ctx, params)
	if err != nil {
		return a.writeClientFailure(mode, "update agent", err)
	}
	return a.writeAgentMutation(mode, result)
}

func (a *App) runAgentShow(ctx context.Context, mode outputMode, args []string) int {
	agent, optionArgs, failure := requiredLeadingArgument(args, "agent name or ID")
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
	result, err := a.newClient(socket).AgentShow(ctx, workspace, agent)
	if err != nil {
		return a.writeClientFailure(mode, "show agent", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write agent output", err))
		}
	} else {
		writeAgentText(a, result.Agent)
	}
	return ExitOK
}

func (a *App) runAgentList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).AgentList(ctx, workspace)
	if err != nil {
		return a.writeClientFailure(mode, "list agents", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write agent list", err))
		}
	} else {
		for _, agent := range result.Agents {
			fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\tenabled=%t\n", agent.ID, agent.Name, agent.Role, agent.Provider, agent.Enabled)
		}
	}
	return ExitOK
}

func (a *App) runObjective(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("objective requires a subcommand", "run 'crewfold help objective' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, objectiveHelp)
		return ExitOK
	}
	if len(args) == 2 && isHelp(args[1]) {
		fmt.Fprint(a.stdout, objectiveHelp)
		return ExitOK
	}
	switch args[0] {
	case "create":
		return a.runObjectiveCreate(ctx, mode, args[1:])
	case "update":
		return a.runObjectiveUpdate(ctx, mode, args[1:])
	case "show":
		return a.runObjectiveShow(ctx, mode, args[1:])
	case "list":
		return a.runObjectiveList(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_objective_command", message: fmt.Sprintf("unknown objective command %q", args[0]), hint: "run 'crewfold help objective' for usage"})
	}
}

func (a *App) runObjectiveCreate(ctx context.Context, mode outputMode, args []string) int {
	title, optionArgs, failure := requiredLeadingArgument(args, "objective title")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, workCreateOptions("project")...)
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
	budget, failure := parseBudget(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).ObjectiveCreate(ctx, localapi.ObjectiveCreateParams{Workspace: workspace, Project: project, Title: title, Budget: budget, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "create objective", err)
	}
	return a.writeObjectiveMutation(mode, result)
}

func (a *App) runObjectiveUpdate(ctx context.Context, mode outputMode, args []string) int {
	objective, optionArgs, failure := requiredLeadingArgument(args, "objective ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "title", "status", "budget-tokens", "budget-cents", "budget-seconds", "expected-revision", "socket", "idempotency-key")
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
	params := localapi.ObjectiveUpdateParams{Workspace: workspace, Objective: objective, Title: optionalString(options, "title"), Status: optionalString(options, "status"), ExpectedRevision: revision, IdempotencyKey: options["idempotency-key"]}
	if hasBudgetOption(options) {
		budget, parseFailure := parseBudgetReplacement(options)
		if parseFailure != nil {
			return a.writeFailure(mode, *parseFailure)
		}
		params.Budget = &budget
	}
	result, err := a.newClient(socket).ObjectiveUpdate(ctx, params)
	if err != nil {
		return a.writeClientFailure(mode, "update objective", err)
	}
	return a.writeObjectiveMutation(mode, result)
}

func (a *App) runObjectiveShow(ctx context.Context, mode outputMode, args []string) int {
	objective, optionArgs, failure := requiredLeadingArgument(args, "objective ID")
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
	result, err := a.newClient(socket).ObjectiveShow(ctx, workspace, objective)
	if err != nil {
		return a.writeClientFailure(mode, "show objective", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write objective output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "objective: %s\nid: %s\nstatus: %s\nrevision: %d\n", result.Objective.Title, result.Objective.ID, result.Objective.Status, result.Objective.Revision)
	}
	return ExitOK
}

func (a *App) runObjectiveList(ctx context.Context, mode outputMode, args []string) int {
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
	result, err := a.newClient(socket).ObjectiveList(ctx, workspace, project)
	if err != nil {
		return a.writeClientFailure(mode, "list objectives", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write objective list", err))
		}
	} else {
		for _, objective := range result.Objectives {
			fmt.Fprintf(a.stdout, "%s\t%s\t%s\trevision %d\n", objective.ID, objective.Title, objective.Status, objective.Revision)
		}
	}
	return ExitOK
}

func (a *App) runTask(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("task requires a subcommand", "run 'crewfold help task' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, taskHelp)
		return ExitOK
	}
	if len(args) == 2 && isHelp(args[1]) {
		fmt.Fprint(a.stdout, taskHelp)
		return ExitOK
	}
	switch args[0] {
	case "create":
		return a.runTaskCreate(ctx, mode, args[1:])
	case "update":
		return a.runTaskUpdate(ctx, mode, args[1:])
	case "show":
		return a.runTaskShow(ctx, mode, args[1:])
	case "list":
		return a.runTaskList(ctx, mode, args[1:])
	case "depend":
		return a.runTaskDepend(ctx, mode, args[1:])
	case "assign":
		return a.runTaskAssign(ctx, mode, args[1:])
	case "start", "block", "unblock", "cancel":
		return a.runTaskTransition(ctx, mode, args[0], args[1:])
	default:
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_task_command", message: fmt.Sprintf("unknown task command %q", args[0]), hint: "run 'crewfold help task' for usage"})
	}
}

func (a *App) runTaskCreate(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, workCreateOptions("project", "objective", "title", "description", "priority")...)
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
	title, failure := requiredOption(options, "title")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	priority, failure := intOption(options, "priority", 100, 0, 1000)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	budget, failure := parseBudget(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).TaskCreate(ctx, localapi.TaskCreateParams{Workspace: workspace, Project: project, Objective: options["objective"], Title: title, Description: options["description"], Priority: priority, Budget: budget, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "create task", err)
	}
	return a.writeTaskMutation(mode, result)
}

func (a *App) runTaskUpdate(ctx context.Context, mode outputMode, args []string) int {
	task, optionArgs, failure := requiredLeadingArgument(args, "task ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "title", "description", "priority", "budget-tokens", "budget-cents", "budget-seconds", "expected-revision", "socket", "idempotency-key")
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
	params := localapi.TaskUpdateParams{Workspace: workspace, Task: task, Title: optionalString(options, "title"), Description: optionalString(options, "description"), ExpectedRevision: revision, IdempotencyKey: options["idempotency-key"]}
	if _, ok := options["priority"]; ok {
		priority, parseFailure := intOption(options, "priority", 0, 0, 1000)
		if parseFailure != nil {
			return a.writeFailure(mode, *parseFailure)
		}
		params.Priority = &priority
	}
	if hasBudgetOption(options) {
		budget, parseFailure := parseBudgetReplacement(options)
		if parseFailure != nil {
			return a.writeFailure(mode, *parseFailure)
		}
		params.Budget = &budget
	}
	result, err := a.newClient(socket).TaskUpdate(ctx, params)
	if err != nil {
		return a.writeClientFailure(mode, "update task", err)
	}
	return a.writeTaskMutation(mode, result)
}

func (a *App) runTaskShow(ctx context.Context, mode outputMode, args []string) int {
	task, optionArgs, failure := requiredLeadingArgument(args, "task ID")
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
	result, err := a.newClient(socket).TaskShow(ctx, workspace, task)
	if err != nil {
		return a.writeClientFailure(mode, "show task", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write task output", err))
		}
	} else {
		writeTaskText(a, result.Detail)
	}
	return ExitOK
}

func (a *App) runTaskList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "ready", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	ready := false
	if _, ok := options["ready"]; ok {
		ready, failure = boolOption(options, "ready")
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
	}
	result, err := a.newClient(socket).TaskList(ctx, workspace, options["project"], ready)
	if err != nil {
		return a.writeClientFailure(mode, "list tasks", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write task list", err))
		}
	} else {
		for _, detail := range result.Tasks {
			fmt.Fprintf(a.stdout, "%s\t%s\t%s\tready=%t\t%s\n", detail.Task.ID, detail.Task.Status, detail.Task.Title, detail.Readiness.Ready, detail.Readiness.Reason)
		}
	}
	return ExitOK
}

func (a *App) runTaskDepend(ctx context.Context, mode outputMode, args []string) int {
	task, optionArgs, failure := requiredLeadingArgument(args, "task ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "on", "expected-revision", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	on, failure := requiredOption(options, "on")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revision, failure := requiredInt64Option(options, "expected-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).TaskDepend(ctx, localapi.TaskDependencyParams{Workspace: workspace, Task: task, DependsOn: on, ExpectedRevision: revision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "add task dependency", err)
	}
	return a.writeTaskMutation(mode, result)
}

func (a *App) runTaskAssign(ctx context.Context, mode outputMode, args []string) int {
	if len(args) < 2 || strings.HasPrefix(args[0], "--") || strings.HasPrefix(args[1], "--") {
		return a.writeFailure(mode, usageFailure("task assign requires a task ID and agent", "run 'crewfold help task' for usage"))
	}
	task, agent := args[0], args[1]
	options, failure := parseOptions(args[2:], "workspace", "lease-seconds", "expected-revision", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	lease, failure := requiredInt64Option(options, "lease-seconds", 1, 30*24*60*60)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revision, failure := requiredInt64Option(options, "expected-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).TaskAssign(ctx, localapi.TaskAssignParams{Workspace: workspace, Task: task, Agent: agent, LeaseSeconds: lease, ExpectedRevision: revision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "assign task", err)
	}
	return a.writeTaskMutation(mode, result)
}

func (a *App) runTaskTransition(ctx context.Context, mode outputMode, action string, args []string) int {
	task, optionArgs, failure := requiredLeadingArgument(args, "task ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "reason", "expected-revision", "socket", "idempotency-key")
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
	result, err := a.newClient(socket).TaskTransition(ctx, localapi.TaskTransitionParams{Workspace: workspace, Task: task, Action: action, Reason: options["reason"], ExpectedRevision: revision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, action+" task", err)
	}
	return a.writeTaskMutation(mode, result)
}

func (a *App) writeAgentMutation(mode outputMode, result localapi.AgentMutationResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write agent mutation", err))
		}
	} else {
		writeAgentText(a, result.Agent)
		fmt.Fprintf(a.stdout, "event_sequence: %d\n", result.EventSequence)
	}
	return ExitOK
}

func (a *App) writeObjectiveMutation(mode outputMode, result localapi.ObjectiveMutationResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write objective mutation", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "objective: %s (%s)\nstatus: %s\nrevision: %d\nevent_sequence: %d\n", result.Objective.Title, result.Objective.ID, result.Objective.Status, result.Objective.Revision, result.EventSequence)
	}
	return ExitOK
}

func (a *App) writeTaskMutation(mode outputMode, result localapi.TaskMutationResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write task mutation", err))
		}
	} else {
		writeTaskText(a, result.Detail)
		fmt.Fprintf(a.stdout, "event_sequence: %d\n", result.EventSequence)
	}
	return ExitOK
}

func writeAgentText(a *App, agent domain.AgentDefinition) {
	fmt.Fprintf(a.stdout, "agent: %s (%s)\nrole: %s\nprovider: %s\nruntime: %s\nenabled: %t\nrevision: %d\n", agent.Name, agent.ID, agent.Role, agent.Provider, agent.Runtime, agent.Enabled, agent.Revision)
}

func writeTaskText(a *App, detail domain.TaskDetail) {
	fmt.Fprintf(a.stdout, "task: %s (%s)\nstatus: %s\nrevision: %d\nready: %t\nreason: %s\n", detail.Task.Title, detail.Task.ID, detail.Task.Status, detail.Task.Revision, detail.Readiness.Ready, detail.Readiness.Reason)
	if detail.Assignment != nil {
		fmt.Fprintf(a.stdout, "agent: %s\nlease_expires_at: %s\n", detail.Assignment.AgentID, detail.Assignment.LeaseExpiresAt)
	}
}

func requiredWorkspaceSocket(options map[string]string) (string, string, *commandFailure) {
	workspace, failure := requiredOption(options, "workspace")
	if failure != nil {
		return "", "", failure
	}
	socket, failure := requiredOption(options, "socket")
	if failure != nil {
		return "", "", failure
	}
	return workspace, socket, nil
}

func workCreateOptions(extra ...string) []string {
	return append([]string{"workspace", "budget-tokens", "budget-cents", "budget-seconds", "socket", "idempotency-key"}, extra...)
}

func parseBudget(options map[string]string) (domain.Budget, *commandFailure) {
	tokens, failure := int64Option(options, "budget-tokens", 0, 0, 1<<62)
	if failure != nil {
		return domain.Budget{}, failure
	}
	cents, failure := int64Option(options, "budget-cents", 0, 0, 1<<62)
	if failure != nil {
		return domain.Budget{}, failure
	}
	seconds, failure := int64Option(options, "budget-seconds", 0, 0, 1<<62)
	if failure != nil {
		return domain.Budget{}, failure
	}
	return domain.Budget{TokenLimit: tokens, CostCents: cents, TimeSeconds: seconds}, nil
}

func hasBudgetOption(options map[string]string) bool {
	for _, name := range []string{"budget-tokens", "budget-cents", "budget-seconds"} {
		if _, ok := options[name]; ok {
			return true
		}
	}
	return false
}

func parseBudgetReplacement(options map[string]string) (domain.Budget, *commandFailure) {
	for _, name := range []string{"budget-tokens", "budget-cents", "budget-seconds"} {
		if _, ok := options[name]; !ok {
			failure := usageFailure("budget updates require --budget-tokens, --budget-cents, and --budget-seconds together", "supply all three values, using zero for an unlimited dimension")
			return domain.Budget{}, &failure
		}
	}
	return parseBudget(options)
}

func optionalString(options map[string]string, name string) *string {
	value, ok := options[name]
	if !ok {
		return nil
	}
	return &value
}

func intOption(options map[string]string, name string, defaultValue, minimum, maximum int) (int, *commandFailure) {
	value, ok := options[name]
	if !ok {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		failure := usageFailure(fmt.Sprintf("--%s must be an integer from %d to %d", name, minimum, maximum), "run the command with --help for usage")
		return 0, &failure
	}
	return parsed, nil
}

func int64Option(options map[string]string, name string, defaultValue, minimum, maximum int64) (int64, *commandFailure) {
	value, ok := options[name]
	if !ok {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		failure := usageFailure(fmt.Sprintf("--%s must be an integer from %d to %d", name, minimum, maximum), "run the command with --help for usage")
		return 0, &failure
	}
	return parsed, nil
}

func requiredInt64Option(options map[string]string, name string, minimum, maximum int64) (int64, *commandFailure) {
	if _, ok := options[name]; !ok {
		failure := usageFailure("--"+name+" is required", "run the command with --help for usage")
		return 0, &failure
	}
	return int64Option(options, name, 0, minimum, maximum)
}

func boolOption(options map[string]string, name string) (bool, *commandFailure) {
	parsed, err := strconv.ParseBool(options[name])
	if err != nil {
		failure := usageFailure("--"+name+" must be true or false", "run the command with --help for usage")
		return false, &failure
	}
	return parsed, nil
}

const agentHelp = `Usage:
  crewfold agent create <name> --workspace <scope> --role <role> --provider <provider> --socket <path>
  crewfold agent update <name-or-id> --workspace <scope> --expected-revision <n> [fields] --socket <path>
  crewfold agent show <name-or-id> --workspace <scope> --socket <path>
  crewfold agent list --workspace <scope> --socket <path>

Agent definitions are durable role/configuration records. These commands never
launch a provider or runtime process.
`

const objectiveHelp = `Usage:
  crewfold objective create <title> --workspace <scope> --project <project> --socket <path> [budget options]
  crewfold objective update <id> --workspace <scope> --expected-revision <n> [fields] --socket <path>
  crewfold objective show <id> --workspace <scope> --socket <path>
  crewfold objective list --workspace <scope> --project <project> --socket <path>
`

const taskHelp = `Usage:
  crewfold task create --workspace <scope> --project <project> --title <title> --socket <path> [options]
  crewfold task update <id> --workspace <scope> --expected-revision <n> [fields] --socket <path>
  crewfold task depend <id> --on <dependency-id> --workspace <scope> --expected-revision <n> --socket <path>
  crewfold task assign <id> <agent> --lease-seconds <n> --workspace <scope> --expected-revision <n> --socket <path>
  crewfold task start|block|unblock|cancel <id> --workspace <scope> --expected-revision <n> --socket <path>
  crewfold task show <id> --workspace <scope> --socket <path>
  crewfold task list --workspace <scope> [--project <project>] [--ready true] --socket <path>

Every mutation uses an expected revision and an optional stable idempotency key.
Starting a task changes coordination state only; this command launches no process.
`
