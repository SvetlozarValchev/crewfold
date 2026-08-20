package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

type managedServiceDaemonClient interface {
	ManagedServiceDefinitionCreate(context.Context, localapi.ManagedServiceDefinitionCreateParams) (localapi.ManagedServiceDefinitionMutationResult, error)
	ManagedServiceDefinitionRetire(context.Context, localapi.ManagedServiceDefinitionRetireParams) (localapi.ManagedServiceDefinitionMutationResult, error)
	ManagedServiceDefinitionShow(context.Context, string, string) (localapi.ManagedServiceDefinitionShowResult, error)
	ManagedServiceDefinitionList(context.Context, localapi.ManagedServiceDefinitionQueryParams) (localapi.ManagedServiceDefinitionListResult, error)
	ManagedServiceStart(context.Context, localapi.ManagedServiceStartParams) (localapi.ManagedServiceMutationResult, error)
	ManagedServiceShow(context.Context, string, string) (localapi.ManagedServiceShowResult, error)
	ManagedServiceList(context.Context, localapi.ManagedServiceQueryParams) (localapi.ManagedServiceListResult, error)
	ManagedServiceStop(context.Context, localapi.ManagedServiceActionParams) (localapi.ManagedServiceMutationResult, error)
	ManagedServiceRestart(context.Context, localapi.ManagedServiceActionParams) (localapi.ManagedServiceMutationResult, error)
	ManagedServiceResolveUnknown(context.Context, localapi.ManagedServiceResolveUnknownParams) (localapi.ManagedServiceMutationResult, error)
	ManagedServiceLogs(context.Context, string, string) (localapi.ManagedServiceLogsResult, error)
	ManagedServiceGrantCreate(context.Context, localapi.ManagedServiceGrantCreateParams) (localapi.ManagedServiceGrantMutationResult, error)
	ManagedServiceGrantRevoke(context.Context, localapi.ManagedServiceGrantRevokeParams) (localapi.ManagedServiceGrantMutationResult, error)
	ManagedServiceGrantList(context.Context, localapi.ManagedServiceGrantQueryParams) (localapi.ManagedServiceGrantListResult, error)
	ManagedServiceRequestList(context.Context, localapi.ManagedServiceRequestQueryParams) (localapi.ManagedServiceRequestListResult, error)
	ManagedServiceRequestAccept(context.Context, localapi.ManagedServiceRequestDecisionParams) (localapi.ManagedServiceRequestMutationResult, error)
	ManagedServiceRequestReject(context.Context, localapi.ManagedServiceRequestDecisionParams) (localapi.ManagedServiceRequestMutationResult, error)
}

func (a *App) managedServiceClient(socket string) (managedServiceDaemonClient, error) {
	client, ok := a.newClient(socket).(managedServiceDaemonClient)
	if !ok {
		return nil, fmt.Errorf("configured daemon client does not support managed processes")
	}
	return client, nil
}

func (a *App) runProcess(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, processHelp)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	switch args[0] {
	case "define":
		return a.runProcessDefine(ctx, mode, args[1:])
	case "retire":
		return a.runProcessRetire(ctx, mode, args[1:])
	case "definitions":
		return a.runProcessDefinitions(ctx, mode, args[1:])
	case "start":
		return a.runProcessStart(ctx, mode, args[1:])
	case "show":
		return a.runProcessShow(ctx, mode, args[1:])
	case "list":
		return a.runProcessList(ctx, mode, args[1:])
	case "stop", "restart":
		return a.runProcessAction(ctx, mode, args[0], args[1:])
	case "resolve-unknown":
		return a.runProcessResolveUnknown(ctx, mode, args[1:])
	case "logs":
		return a.runProcessLogs(ctx, mode, args[1:])
	case "grant":
		return a.runProcessGrant(ctx, mode, args[1:])
	case "revoke-grant":
		return a.runProcessGrantRevoke(ctx, mode, args[1:])
	case "grants":
		return a.runProcessGrants(ctx, mode, args[1:])
	case "requests":
		return a.runProcessRequests(ctx, mode, args[1:])
	case "accept-request", "reject-request":
		return a.runProcessRequestDecision(ctx, mode, args[0], args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown process command "+args[0], "run 'crewfold help process' for usage"))
	}
}

func (a *App) runProcessDefine(ctx context.Context, mode outputMode, args []string) int {
	name, rest, failure := requiredLeadingArgument(args, "process name")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, repeated, failure := parseCheckRepeatedOptions(rest, []string{"arg", "env"},
		"workspace", "project", "workstream", "checkout", "description", "executable", "arg", "env", "working-directory",
		"profile", "profile-revision", "network", "health", "health-host", "health-port", "health-path", "health-interval",
		"health-timeout", "restart", "max-restarts", "restart-cooldown", "stop-grace", "output-byte-limit", "socket", "idempotency-key")
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
	checkout, failure := requiredOption(options, "checkout")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	executable, failure := requiredOption(options, "executable")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	description := options["description"]
	if description == "" {
		description = name
	}
	workingDirectory := options["working-directory"]
	if workingDirectory == "" {
		workingDirectory = "."
	}
	profile := options["profile"]
	if profile == "" {
		profile = "local-process"
	}
	profileRevision, failure := optionalProcessInt64(options, "profile-revision", 1, 1, 1<<31)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	network := options["network"]
	if network == "" {
		network = domain.ManagedServiceNetworkNone
	}
	healthType := options["health"]
	if healthType == "" {
		healthType = domain.ManagedServiceHealthProcess
	}
	healthInterval, failure := optionalProcessDuration(options, "health-interval", time.Second, 100*time.Millisecond, time.Minute)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	healthTimeout, failure := optionalProcessDuration(options, "health-timeout", 500*time.Millisecond, 50*time.Millisecond, 30*time.Second)
	if failure != nil || healthTimeout > healthInterval {
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		return a.writeFailure(mode, usageFailure("--health-timeout cannot exceed --health-interval", "use one bounded readiness probe"))
	}
	healthPort, failure := optionalProcessInt(options, "health-port", 0, 0, 65535)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	restart := options["restart"]
	if restart == "" {
		restart = domain.ManagedServiceRestartNever
	}
	maximumRestarts, failure := optionalProcessInt(options, "max-restarts", 0, 0, 20)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	restartCooldown, failure := optionalProcessDuration(options, "restart-cooldown", 500*time.Millisecond, 0, time.Minute)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	stopGrace, failure := optionalProcessDuration(options, "stop-grace", 5*time.Second, 100*time.Millisecond, time.Minute)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	outputLimit, failure := optionalProcessInt64(options, "output-byte-limit", 256*1024, 4096, 1024*1024)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	environment := make([]domain.ManagedServiceEnvironmentVariable, 0, len(repeated["env"]))
	for _, raw := range repeated["env"] {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || key == "" {
			return a.writeFailure(mode, usageFailure("--env must be NAME=VALUE", "repeat --env for each exact environment override"))
		}
		environment = append(environment, domain.ManagedServiceEnvironmentVariable{Name: key, Value: value})
	}
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "define managed process", err)
	}
	result, err := client.ManagedServiceDefinitionCreate(ctx, localapi.ManagedServiceDefinitionCreateParams{
		Workspace: workspace, Project: project, Workstream: options["workstream"], Checkout: checkout, Name: name, Description: description,
		Executable: executable, Arguments: repeated["arg"], WorkingDirectory: workingDirectory, Environment: environment,
		Profile: profile, ProfileRevision: profileRevision, NetworkMode: network,
		Health:        domain.ManagedServiceHealthCheck{Type: healthType, Host: options["health-host"], Port: healthPort, Path: options["health-path"], IntervalMillis: healthInterval.Milliseconds(), TimeoutMillis: healthTimeout.Milliseconds()},
		RestartPolicy: restart, MaximumRestarts: maximumRestarts, RestartCooldownMillis: restartCooldown.Milliseconds(),
		StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: stopGrace.Milliseconds(), OutputByteLimit: outputLimit,
		CapacityClass: domain.ManagedServiceCapacityLocalDevelop, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "define managed process", err)
	}
	return a.writeProcessResult(mode, result, fmt.Sprintf("process definition: %s\nname: %s\ncheckout: %s\ncommand: %s %s\nrevision: %d\n", result.Definition.ID, result.Definition.Name, result.Definition.CheckoutID, result.Definition.Executable, strings.Join(result.Definition.Arguments, " "), result.Definition.Revision))
}

func (a *App) runProcessRetire(ctx context.Context, mode outputMode, args []string) int {
	definition, rest, failure := requiredLeadingArgument(args, "process definition ID")
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
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "retire managed process", err)
	}
	result, err := client.ManagedServiceDefinitionRetire(ctx, localapi.ManagedServiceDefinitionRetireParams{Workspace: workspace, Definition: definition, ExpectedRevision: revision, Reason: reason, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "retire managed process", err)
	}
	return a.writeProcessResult(mode, result, fmt.Sprintf("process definition %s: %s\n", result.Definition.ID, result.Definition.Status))
}

func (a *App) runProcessDefinitions(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "workstream", "checkout", "status", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalProcessInt(options, "limit", 100, 1, 200)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "list managed process definitions", err)
	}
	result, err := client.ManagedServiceDefinitionList(ctx, localapi.ManagedServiceDefinitionQueryParams{Workspace: workspace, Project: options["project"], Workstream: options["workstream"], Checkout: options["checkout"], Status: options["status"], Limit: limit})
	if err != nil {
		return a.writeClientFailure(mode, "list managed process definitions", err)
	}
	var text strings.Builder
	for _, definition := range result.Definitions {
		fmt.Fprintf(&text, "%s\t%s\t%s\t%s\n", definition.ID, definition.Name, definition.Status, definition.Executable)
	}
	return a.writeProcessResult(mode, result, text.String())
}

func (a *App) runProcessStart(ctx context.Context, mode outputMode, args []string) int {
	definition, rest, failure := requiredLeadingArgument(args, "process definition ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "expected-revision", "socket", "idempotency-key")
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
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "start managed process", err)
	}
	result, err := client.ManagedServiceStart(ctx, localapi.ManagedServiceStartParams{Workspace: workspace, Definition: definition, ExpectedRevision: revision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "start managed process", err)
	}
	return a.writeProcessResult(mode, result, processMutationText(result.Instance))
}

func (a *App) runProcessShow(ctx context.Context, mode outputMode, args []string) int {
	instance, rest, failure := requiredLeadingArgument(args, "process instance ID")
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
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "show managed process", err)
	}
	result, err := client.ManagedServiceShow(ctx, workspace, instance)
	if err != nil {
		return a.writeClientFailure(mode, "show managed process", err)
	}
	return a.writeProcessResult(mode, result, processMutationText(result.Detail.Instance))
}

func (a *App) runProcessList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "workstream", "checkout", "status", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalProcessInt(options, "limit", 100, 1, 200)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "list managed processes", err)
	}
	result, err := client.ManagedServiceList(ctx, localapi.ManagedServiceQueryParams{Workspace: workspace, Project: options["project"], Workstream: options["workstream"], Checkout: options["checkout"], Status: options["status"], Limit: limit})
	if err != nil {
		return a.writeClientFailure(mode, "list managed processes", err)
	}
	var text strings.Builder
	for _, instance := range result.Instances {
		fmt.Fprintf(&text, "%s\t%s\t%s\t%s\n", instance.ID, instance.DefinitionID, instance.Status, instance.HealthStatus)
	}
	return a.writeProcessResult(mode, result, text.String())
}

func (a *App) runProcessAction(ctx context.Context, mode outputMode, action string, args []string) int {
	instance, rest, failure := requiredLeadingArgument(args, "process instance ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "expected-revision", "socket", "idempotency-key")
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
	params := localapi.ManagedServiceActionParams{Workspace: workspace, Instance: instance, ExpectedRevision: revision, IdempotencyKey: options["idempotency-key"]}
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, action+" managed process", err)
	}
	var result localapi.ManagedServiceMutationResult
	err = nil
	if action == "stop" {
		result, err = client.ManagedServiceStop(ctx, params)
	} else {
		result, err = client.ManagedServiceRestart(ctx, params)
	}
	if err != nil {
		return a.writeClientFailure(mode, action+" managed process", err)
	}
	return a.writeProcessResult(mode, result, processMutationText(result.Instance))
}

func (a *App) runProcessResolveUnknown(ctx context.Context, mode outputMode, args []string) int {
	instance, rest, failure := requiredLeadingArgument(args, "process instance ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "expected-revision", "confirm-runtime-retired", "reason", "socket", "idempotency-key")
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
	if options["confirm-runtime-retired"] != "true" {
		return a.writeFailure(mode, usageFailure("--confirm-runtime-retired true is required", "confirm only after the unknown external process has ended"))
	}
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "resolve unknown managed process", err)
	}
	result, err := client.ManagedServiceResolveUnknown(ctx, localapi.ManagedServiceResolveUnknownParams{
		Workspace: workspace, Instance: instance, ExpectedRevision: revision, RuntimeRetiredConfirmed: true,
		Reason: reason, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "resolve unknown managed process", err)
	}
	return a.writeProcessResult(mode, result, processMutationText(result.Instance))
}

func (a *App) runProcessLogs(ctx context.Context, mode outputMode, args []string) int {
	instance, rest, failure := requiredLeadingArgument(args, "process instance ID")
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
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "read managed process logs", err)
	}
	result, err := client.ManagedServiceLogs(ctx, workspace, instance)
	if err != nil {
		return a.writeClientFailure(mode, "read managed process logs", err)
	}
	text := result.Logs.Stdout.Text
	if result.Logs.Stderr.Text != "" {
		text += result.Logs.Stderr.Text
	}
	return a.writeProcessResult(mode, result, text)
}

func (a *App) runProcessGrant(ctx context.Context, mode outputMode, args []string) int {
	definition, rest, failure := requiredLeadingArgument(args, "process definition ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(rest, "workspace", "agent", "expected-definition-revision", "expected-membership-revision", "actions", "max-instances", "expires-at", "socket", "idempotency-key")
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
	definitionRevision, failure := requiredInt64Option(options, "expected-definition-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	membershipRevision, failure := requiredInt64Option(options, "expected-membership-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	actions, failure := csvOption(options, "actions", true)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	maximumInstances, failure := optionalProcessInt(options, "max-instances", 1, 1, 8)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "grant managed process authority", err)
	}
	result, err := client.ManagedServiceGrantCreate(ctx, localapi.ManagedServiceGrantCreateParams{
		Workspace: workspace, Definition: definition, ExpectedDefinitionRevision: definitionRevision,
		ManagerAgent: agent, ExpectedMembershipRevision: membershipRevision, Actions: actions,
		MaximumInstances: maximumInstances, ExpiresAt: options["expires-at"], IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "grant managed process authority", err)
	}
	return a.writeProcessResult(mode, result, fmt.Sprintf("process grant: %s\ndefinition: %s\nagent: %s\nactions: %s\nmax instances: %d\nstatus: %s\nrevision: %d\n", result.Grant.ID, result.Grant.DefinitionID, result.Grant.ManagerAgentID, strings.Join(result.Grant.Actions, ","), result.Grant.MaximumInstances, result.Grant.Status, result.Grant.Revision))
}

func (a *App) runProcessGrantRevoke(ctx context.Context, mode outputMode, args []string) int {
	grant, rest, failure := requiredLeadingArgument(args, "process grant ID")
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
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "revoke managed process authority", err)
	}
	result, err := client.ManagedServiceGrantRevoke(ctx, localapi.ManagedServiceGrantRevokeParams{Workspace: workspace, Grant: grant, ExpectedRevision: revision, Reason: reason, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "revoke managed process authority", err)
	}
	return a.writeProcessResult(mode, result, fmt.Sprintf("process grant %s: %s\nrevision: %d\n", result.Grant.ID, result.Grant.Status, result.Grant.Revision))
}

func (a *App) runProcessGrants(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "agent", "definition", "status", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalProcessInt(options, "limit", 100, 1, 200)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "list managed process grants", err)
	}
	result, err := client.ManagedServiceGrantList(ctx, localapi.ManagedServiceGrantQueryParams{Workspace: workspace, Project: options["project"], Manager: options["agent"], Definition: options["definition"], Status: options["status"], Limit: limit})
	if err != nil {
		return a.writeClientFailure(mode, "list managed process grants", err)
	}
	var output strings.Builder
	for _, grant := range result.Grants {
		fmt.Fprintf(&output, "%s\t%s\t%s\t%s\t%s\n", grant.ID, grant.ManagerAgentID, grant.DefinitionID, grant.Status, strings.Join(grant.Actions, ","))
	}
	return a.writeProcessResult(mode, result, output.String())
}

func (a *App) runProcessRequests(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "agent", "definition", "status", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalProcessInt(options, "limit", 100, 1, 200)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, "list managed process requests", err)
	}
	result, err := client.ManagedServiceRequestList(ctx, localapi.ManagedServiceRequestQueryParams{Workspace: workspace, Project: options["project"], Agent: options["agent"], Definition: options["definition"], Status: options["status"], Limit: limit})
	if err != nil {
		return a.writeClientFailure(mode, "list managed process requests", err)
	}
	var output strings.Builder
	for _, request := range result.Requests {
		fmt.Fprintf(&output, "%s\t%s\tstart\t%s\t%s\n", request.ID, request.AgentID, request.DefinitionID, request.Status)
	}
	return a.writeProcessResult(mode, result, output.String())
}

func (a *App) runProcessRequestDecision(ctx context.Context, mode outputMode, action string, args []string) int {
	request, rest, failure := requiredLeadingArgument(args, "process request ID")
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
	client, err := a.managedServiceClient(socket)
	if err != nil {
		return a.writeClientFailure(mode, action+" managed process request", err)
	}
	params := localapi.ManagedServiceRequestDecisionParams{Workspace: workspace, Request: request, ExpectedRevision: revision, Reason: reason, IdempotencyKey: options["idempotency-key"]}
	var result localapi.ManagedServiceRequestMutationResult
	if action == "accept-request" {
		result, err = client.ManagedServiceRequestAccept(ctx, params)
	} else {
		result, err = client.ManagedServiceRequestReject(ctx, params)
	}
	if err != nil {
		return a.writeClientFailure(mode, action+" managed process request", err)
	}
	return a.writeProcessResult(mode, result, fmt.Sprintf("process request: %s\nstatus: %s\nrevision: %d\n", result.Decision.Request.ID, result.Decision.Request.Status, result.Decision.Request.Revision))
}

func (a *App) writeProcessResult(mode outputMode, value any, text string) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, value); err != nil {
			return a.writeFailure(outputText, internalFailure("write managed process output", err))
		}
	} else {
		fmt.Fprint(a.stdout, text)
	}
	return ExitOK
}

func processMutationText(instance domain.ManagedServiceInstance) string {
	return fmt.Sprintf("process: %s\ndefinition: %s\nstatus: %s\nhealth: %s\nrevision: %d\n", instance.ID, instance.DefinitionID, instance.Status, instance.HealthStatus, instance.Revision)
}

func optionalProcessInt(options map[string]string, name string, fallback, minimum, maximum int) (int, *commandFailure) {
	value := options[name]
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		failure := usageFailure(fmt.Sprintf("--%s must be an integer from %d through %d", name, minimum, maximum), "use one bounded value")
		return 0, &failure
	}
	return parsed, nil
}

func optionalProcessInt64(options map[string]string, name string, fallback, minimum, maximum int64) (int64, *commandFailure) {
	value := options[name]
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		failure := usageFailure(fmt.Sprintf("--%s must be an integer from %d through %d", name, minimum, maximum), "use one bounded value")
		return 0, &failure
	}
	return parsed, nil
}

func optionalProcessDuration(options map[string]string, name string, fallback, minimum, maximum time.Duration) (time.Duration, *commandFailure) {
	value := options[name]
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum || parsed > maximum {
		failure := usageFailure(fmt.Sprintf("--%s must be a duration from %s through %s", name, minimum, maximum), "use one bounded duration such as 1s")
		return 0, &failure
	}
	return parsed, nil
}

const processHelp = `Usage:
  crewfold process define <name> --workspace <scope> --project <id> --checkout <id> --executable <name-or-absolute-path> [--arg=<value> ...] [--env NAME=VALUE ...] [options] --socket <path>
  crewfold process definitions --workspace <scope> [filters] --socket <path>
  crewfold process retire <definition-id> --workspace <scope> --expected-revision <n> --reason <text> --socket <path>
  crewfold process start <definition-id> --workspace <scope> --expected-revision <n> --socket <path>
  crewfold process show <instance-id> --workspace <scope> --socket <path>
  crewfold process list --workspace <scope> [filters] --socket <path>
	crewfold process stop|restart <instance-id> --workspace <scope> --expected-revision <n> --socket <path>
	crewfold process resolve-unknown <instance-id> --workspace <scope> --expected-revision <n> --confirm-runtime-retired true --reason <text> --socket <path>
  crewfold process logs <instance-id> --workspace <scope> --socket <path>
  crewfold process grant <definition-id> --workspace <scope> --agent <id> --expected-definition-revision <n> --expected-membership-revision <n> --actions <csv> [--max-instances <n>] --socket <path>
  crewfold process grants --workspace <scope> [filters] --socket <path>
  crewfold process revoke-grant <grant-id> --workspace <scope> --expected-revision <n> --reason <text> --socket <path>
  crewfold process requests --workspace <scope> [filters] --socket <path>
  crewfold process accept-request|reject-request <request-id> --workspace <scope> --expected-revision <n> --reason <text> --socket <path>

Define and operate one canonical non-interactive local process attached to an
exact checkout and optional workstream. The executable and argv are generic:
development servers, asset watchers, local APIs, cookers, mock dependencies, and
other bounded processes use the same contract. Interactive agent terminals and
one-shot checks remain separate surfaces. Grants authorize exact agents to
inspect or control exact definitions; requests are inert until the owner accepts
or rejects them. Use the equals form for option-shaped argv values, for example
--arg=--host or --arg=-m, so Crewfold never mistakes process argv for its own
CLI options.
`
