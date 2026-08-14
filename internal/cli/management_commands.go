package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

func (a *App) runManager(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, managerHelp)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	switch args[0] {
	case "grant":
		return a.runManagerGrant(ctx, mode, args[1:])
	case "propose-tasks":
		return a.runManagerInvoke(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown manager command "+args[0], "run 'crewfold help manager' for usage"))
	}
}

func (a *App) runManagerGrant(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("manager grant requires create, revoke, show, or list", "run 'crewfold help manager' for usage"))
	}
	switch args[0] {
	case "create":
		return a.runManagerGrantCreate(ctx, mode, args[1:])
	case "revoke":
		return a.runManagerGrantRevoke(ctx, mode, args[1:])
	case "show":
		return a.runManagerGrantShow(ctx, mode, args[1:])
	case "list":
		return a.runManagerGrantList(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown manager grant command "+args[0], "run 'crewfold help manager' for usage"))
	}
}

func (a *App) runManagerGrantCreate(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "objective", "task", "agent", "expected-task-revision", "expected-agent-revision", "proposal-kinds", "launch-profiles", "claim-kinds", "max-open-proposals", "max-actions", "max-tasks", "max-dependencies", "max-claim-requirements", "token-limit", "cost-cents", "time-seconds", "expires-at", "socket", "idempotency-key")
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
	objective, failure := requiredOption(options, "objective")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	task, failure := requiredOption(options, "task")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	agent, failure := requiredOption(options, "agent")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	taskRevision, failure := requiredInt64Option(options, "expected-task-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	agentRevision, failure := requiredInt64Option(options, "expected-agent-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limits, failure := managerLimitsFromOptions(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	proposalKinds, failure := csvOption(options, "proposal-kinds", true)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	profiles, failure := csvOption(options, "launch-profiles", true)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	claimKinds, failure := csvOption(options, "claim-kinds", false)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).ManagerGrantCreate(ctx, localapi.ManagerGrantCreateParams{Workspace: workspace, Project: project, Objective: objective, Task: task, Agent: agent, ExpectedTaskRevision: taskRevision, ExpectedAgentRevision: agentRevision, ProposalKinds: proposalKinds, LaunchProfileIDs: profiles, AllowedClaimKinds: claimKinds, Limits: limits, ExpiresAt: options["expires-at"], IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "create manager grant", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("manager grant: %s\nstatus: %s\nrevision: %d\n", result.Grant.ID, result.Grant.Status, result.Grant.Revision))
}

func managerLimitsFromOptions(options map[string]string) (domain.ManagerProposalLimits, *commandFailure) {
	read := func(name string, minimum, maximum int64) (int64, *commandFailure) {
		return requiredInt64Option(options, name, minimum, maximum)
	}
	open, f := read("max-open-proposals", 1, 32)
	if f != nil {
		return domain.ManagerProposalLimits{}, f
	}
	actions, f := read("max-actions", 1, 32)
	if f != nil {
		return domain.ManagerProposalLimits{}, f
	}
	tasks, f := read("max-tasks", 1, 16)
	if f != nil {
		return domain.ManagerProposalLimits{}, f
	}
	dependencies, f := read("max-dependencies", 1, 32)
	if f != nil {
		return domain.ManagerProposalLimits{}, f
	}
	claims, f := read("max-claim-requirements", 1, 32)
	if f != nil {
		return domain.ManagerProposalLimits{}, f
	}
	tokens, f := read("token-limit", 0, 1<<62)
	if f != nil {
		return domain.ManagerProposalLimits{}, f
	}
	cost, f := read("cost-cents", 0, 1<<62)
	if f != nil {
		return domain.ManagerProposalLimits{}, f
	}
	seconds, f := read("time-seconds", 0, 1<<62)
	if f != nil {
		return domain.ManagerProposalLimits{}, f
	}
	return domain.ManagerProposalLimits{MaxOpenProposals: int(open), MaxActions: int(actions), MaxTasks: int(tasks), MaxDependencies: int(dependencies), MaxClaimRequirements: int(claims), Budget: domain.Budget{TokenLimit: tokens, CostCents: cost, TimeSeconds: seconds}}, nil
}

func (a *App) runManagerGrantRevoke(ctx context.Context, mode outputMode, args []string) int {
	grant, rest, failure := requiredLeadingArgument(args, "manager grant ID")
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
	result, err := a.newClient(socket).ManagerGrantRevoke(ctx, localapi.ManagerGrantRevokeParams{Workspace: workspace, Grant: grant, ExpectedRevision: revision, Reason: reason, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "revoke manager grant", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("manager grant: %s\nstatus: %s\nrevision: %d\n", result.Grant.ID, result.Grant.Status, result.Grant.Revision))
}

func (a *App) runManagerGrantShow(ctx context.Context, mode outputMode, args []string) int {
	grant, rest, failure := requiredLeadingArgument(args, "manager grant ID")
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
	result, err := a.newClient(socket).ManagerGrantShow(ctx, workspace, grant)
	if err != nil {
		return a.writeClientFailure(mode, "show manager grant", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("manager grant: %s\nstatus: %s\nagent: %s\ntask: %s\nrevision: %d\n", result.Grant.ID, result.Grant.Status, result.Grant.AgentID, result.Grant.TaskID, result.Grant.Revision))
}

func (a *App) runManagerGrantList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "objective", "task", "agent", "status", "limit", "socket")
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
	result, err := a.newClient(socket).ManagerGrantList(ctx, localapi.ManagerGrantQueryParams{Workspace: workspace, Project: options["project"], Objective: options["objective"], Task: options["task"], Agent: options["agent"], Status: options["status"], Limit: int(limit)})
	if err != nil {
		return a.writeClientFailure(mode, "list manager grants", err)
	}
	if mode == outputJSON {
		return a.writeManagementResult(mode, result, "")
	}
	for _, grant := range result.Grants {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\trevision %d\n", grant.ID, grant.Status, grant.TaskID, grant.AgentID, grant.Revision)
	}
	return ExitOK
}

func (a *App) runManagerInvoke(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "objective", "planning-task", "grant", "profile", "expected-task-revision", "expected-grant-revision", "expected-profile-revision", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	objective, failure := requiredOption(options, "objective")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	taskRevision, failure := optionalIntOption(options, "expected-task-revision", 0, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	grantRevision, failure := optionalIntOption(options, "expected-grant-revision", 0, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	profileRevision, failure := optionalIntOption(options, "expected-profile-revision", 0, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).ManagerInvoke(ctx, localapi.ManagerInvokeParams{Workspace: workspace, Objective: objective, PlanningTask: options["planning-task"], Grant: options["grant"], Profile: options["profile"], ExpectedTaskRevision: taskRevision, ExpectedGrantRevision: grantRevision, ExpectedProfileRevision: profileRevision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "invoke manager planning run", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("manager run: %s\ngrant: %s\nprofile: %s\nstatus: %s\n", result.Detail.Run.ID, result.ManagerGrant.ID, result.LaunchProfile.ID, result.Detail.Run.Status))
}

func (a *App) runLaunchProfile(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, launchProfileHelp)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	switch args[0] {
	case "create":
		return a.runLaunchProfileCreate(ctx, mode, args[1:])
	case "retire":
		return a.runLaunchProfileRetire(ctx, mode, args[1:])
	case "show":
		return a.runLaunchProfileShow(ctx, mode, args[1:])
	case "list":
		return a.runLaunchProfileList(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown launch-profile command "+args[0], "run 'crewfold help launch-profile' for usage"))
	}
}

func (a *App) runLaunchProfileCreate(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "agent", "expected-agent-revision", "purpose", "runtime", "provider", "checkout", "scenario", "assignment-lease-seconds", "capability-ttl-seconds", "manager-grant", "socket", "idempotency-key")
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
	runtimeName, failure := requiredOption(options, "runtime")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	provider, failure := requiredOption(options, "provider")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	scenarioPath, failure := requiredOption(options, "scenario")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	agentRevision, failure := requiredInt64Option(options, "expected-agent-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	lease, failure := requiredInt64Option(options, "assignment-lease-seconds", 30, 86400)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	ttl, failure := requiredInt64Option(options, "capability-ttl-seconds", 30, 86400)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	scenario, err := execution.LoadScenario(scenarioPath)
	if err != nil {
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "invalid_scenario", message: err.Error(), hint: "use a valid bounded provider scenario"})
	}
	result, err := a.newClient(socket).LaunchProfileCreate(ctx, localapi.LaunchProfileCreateParams{Workspace: workspace, Project: project, Agent: agent, ExpectedAgentRevision: agentRevision, Purpose: options["purpose"], Runtime: runtimeName, Provider: provider, Checkout: options["checkout"], Scenario: scenario, AssignmentLeaseSeconds: lease, CapabilityTTLSeconds: ttl, ManagerGrant: options["manager-grant"], IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "create launch profile", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("launch profile: %s\nagent: %s\nstatus: %s\nrevision: %d\n", result.Profile.ID, result.Profile.AgentID, result.Profile.Status, result.Profile.Revision))
}

func (a *App) runLaunchProfileRetire(ctx context.Context, mode outputMode, args []string) int {
	profile, rest, failure := requiredLeadingArgument(args, "launch profile ID")
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
	result, err := a.newClient(socket).LaunchProfileRetire(ctx, localapi.LaunchProfileRetireParams{Workspace: workspace, Profile: profile, ExpectedRevision: revision, Reason: reason, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "retire launch profile", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("launch profile: %s\nstatus: %s\nrevision: %d\n", result.Profile.ID, result.Profile.Status, result.Profile.Revision))
}

func (a *App) runLaunchProfileShow(ctx context.Context, mode outputMode, args []string) int {
	profile, rest, failure := requiredLeadingArgument(args, "launch profile ID")
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
	result, err := a.newClient(socket).LaunchProfileShow(ctx, workspace, profile)
	if err != nil {
		return a.writeClientFailure(mode, "show launch profile", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("launch profile: %s\nagent: %s\nruntime/provider: %s/%s\nstatus: %s\nrevision: %d\n", result.Profile.ID, result.Profile.AgentID, result.Profile.Runtime, result.Profile.Provider, result.Profile.Status, result.Profile.Revision))
}

func (a *App) runLaunchProfileList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "agent", "manager-grant", "status", "limit", "socket")
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
	result, err := a.newClient(socket).LaunchProfileList(ctx, localapi.LaunchProfileQueryParams{Workspace: workspace, Project: options["project"], Agent: options["agent"], ManagerGrant: options["manager-grant"], Status: options["status"], Limit: int(limit)})
	if err != nil {
		return a.writeClientFailure(mode, "list launch profiles", err)
	}
	if mode == outputJSON {
		return a.writeManagementResult(mode, result, "")
	}
	for _, profile := range result.Profiles {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s/%s\t%s\trevision %d\n", profile.ID, profile.AgentID, profile.Runtime, profile.Provider, profile.Status, profile.Revision)
	}
	return ExitOK
}

func (a *App) runProposal(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, proposalHelp)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	switch args[0] {
	case "list":
		return a.runProposalList(ctx, mode, args[1:])
	case "inspect":
		return a.runProposalInspect(ctx, mode, args[1:])
	case "accept":
		return a.runProposalDecision(ctx, mode, args[1:], true)
	case "reject":
		return a.runProposalDecision(ctx, mode, args[1:], false)
	default:
		return a.writeFailure(mode, usageFailure("unknown proposal command "+args[0], "run 'crewfold help proposal' for usage"))
	}
}

func (a *App) runProposalList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "objective", "source-run", "grant", "kind", "status", "limit", "socket")
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
	result, err := a.newClient(socket).ProposalList(ctx, localapi.ProposalQueryParams{Workspace: workspace, Project: options["project"], Objective: options["objective"], SourceRun: options["source-run"], Grant: options["grant"], Kind: options["kind"], Status: options["status"], Limit: int(limit)})
	if err != nil {
		return a.writeClientFailure(mode, "list manager proposals", err)
	}
	if mode == outputJSON {
		return a.writeManagementResult(mode, result, "")
	}
	for _, proposal := range result.Proposals {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%d actions\trevision %d\n", proposal.ID, proposal.Kind, proposal.Status, len(proposal.Actions), proposal.Revision)
	}
	return ExitOK
}

func (a *App) runProposalInspect(ctx context.Context, mode outputMode, args []string) int {
	proposal, rest, failure := requiredLeadingArgument(args, "proposal ID")
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
	result, err := a.newClient(socket).ProposalInspect(ctx, workspace, proposal)
	if err != nil {
		return a.writeClientFailure(mode, "inspect manager proposal", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("proposal: %s\nkind: %s\nstatus: %s\nactions: %d\nrevision: %d\nsummary: %s\n", result.Proposal.ID, result.Proposal.Kind, result.Proposal.Status, len(result.Proposal.Actions), result.Proposal.Revision, result.Proposal.Summary))
}

func (a *App) runProposalDecision(ctx context.Context, mode outputMode, args []string, accept bool) int {
	proposal, rest, failure := requiredLeadingArgument(args, "proposal ID")
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
	decisionNote, failure := requiredOption(options, "decision-note")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	params := localapi.ProposalDecisionParams{Workspace: workspace, Proposal: proposal, ExpectedRevision: revision, DecisionNote: decisionNote, IdempotencyKey: options["idempotency-key"]}
	client := a.newClient(socket)
	var result localapi.ProposalMutationResult
	var err error
	verb := "reject"
	if accept {
		verb = "accept"
		result, err = client.ProposalAccept(ctx, params)
	} else {
		result, err = client.ProposalReject(ctx, params)
	}
	if err != nil {
		return a.writeClientFailure(mode, verb+" manager proposal", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("proposal: %s\nstatus: %s\neffects: %d\nrevision: %d\n", result.Proposal.ID, result.Proposal.Status, len(result.Effects), result.Proposal.Revision))
}

func (a *App) runSupervisor(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, supervisorHelp)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	switch args[0] {
	case "policy":
		return a.runSupervisorPolicy(ctx, mode, args[1:])
	case "run":
		return a.runSupervisorRun(ctx, mode, args[1:])
	case "list":
		return a.runSupervisorList(ctx, mode, args[1:])
	case "explain":
		return a.runSupervisorExplain(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure("unknown supervisor command "+args[0], "run 'crewfold help supervisor' for usage"))
	}
}

func (a *App) runSupervisorPolicy(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("supervisor policy requires show or update", "run 'crewfold help supervisor' for usage"))
	}
	if args[0] == "show" {
		options, failure := parseOptions(args[1:], "workspace", "socket")
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		workspace, socket, failure := requiredWorkspaceSocket(options)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		result, err := a.newClient(socket).SupervisorPolicyShow(ctx, workspace)
		if err != nil {
			return a.writeClientFailure(mode, "show supervisor policy", err)
		}
		return a.writeManagementResult(mode, result, fmt.Sprintf("supervisor enabled: %t\nauto_schedule: %t\nauto_retry_limit: %d\nrevision: %d\n", result.Policy.Enabled, result.Policy.AutoSchedule, result.Policy.AutoRetryLimit, result.Policy.Revision))
	}
	if args[0] != "update" {
		return a.writeFailure(mode, usageFailure("supervisor policy requires show or update", "run 'crewfold help supervisor' for usage"))
	}
	options, failure := parseOptions(args[1:], "workspace", "enabled", "auto-schedule", "auto-retry-limit", "retry-cooldown-seconds", "max-active-runs", "max-starting-runs", "default-project-concurrency", "default-provider-concurrency", "project-concurrency-json", "provider-concurrency-json", "expected-revision", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	enabled, failure := boolOption(options, "enabled")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	autoSchedule, failure := boolOption(options, "auto-schedule")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	retryLimit, failure := requiredInt64Option(options, "auto-retry-limit", 0, 3)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	cooldown, failure := requiredInt64Option(options, "retry-cooldown-seconds", 0, 86400)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	maxActive, failure := requiredInt64Option(options, "max-active-runs", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	maxStarting, failure := requiredInt64Option(options, "max-starting-runs", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	defaultProject, failure := requiredInt64Option(options, "default-project-concurrency", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	defaultProvider, failure := requiredInt64Option(options, "default-provider-concurrency", 1, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	expected, failure := optionalIntOption(options, "expected-revision", 0, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	projects, failure := concurrencyMapOption(options, "project-concurrency-json")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	providers, failure := concurrencyMapOption(options, "provider-concurrency-json")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).SupervisorPolicyConfigure(ctx, localapi.SupervisorPolicyConfigureParams{Workspace: workspace, Enabled: enabled, AutoSchedule: autoSchedule, AutoRetryLimit: int(retryLimit), RetryCooldownSeconds: cooldown, ExpectedRevision: expected, IdempotencyKey: options["idempotency-key"], Limits: domain.SupervisorLimits{MaxActiveRuns: int(maxActive), MaxStartingRuns: int(maxStarting), DefaultProjectConcurrency: int(defaultProject), DefaultProviderConcurrency: int(defaultProvider), ProjectConcurrency: projects, ProviderConcurrency: providers}})
	if err != nil {
		return a.writeClientFailure(mode, "update supervisor policy", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("supervisor enabled: %t\nrevision: %d\n", result.Policy.Enabled, result.Policy.Revision))
}

func (a *App) runSupervisorRun(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "limit", "socket", "idempotency-key")
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
	result, err := a.newClient(socket).SupervisorRun(ctx, localapi.SupervisorRunParams{Workspace: workspace, Limit: int(limit), IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "run supervisor", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("supervisor actions: %d\nscheduled runs: %d\nevent_sequence: %d\n", len(result.Actions), len(result.ScheduledRunIDs), result.EventSequence))
}

func (a *App) runSupervisorList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "task", "run", "status", "condition", "limit", "socket")
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
	result, err := a.newClient(socket).SupervisorActionList(ctx, localapi.SupervisorActionQueryParams{Workspace: workspace, Project: options["project"], Task: options["task"], Run: options["run"], Status: options["status"], Condition: options["condition"], Limit: int(limit)})
	if err != nil {
		return a.writeClientFailure(mode, "list supervisor actions", err)
	}
	if mode == outputJSON {
		return a.writeManagementResult(mode, result, "")
	}
	for _, action := range result.Actions {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\trevision %d\n", action.ID, action.Condition, action.Response, action.Status, action.Revision)
	}
	return ExitOK
}

func (a *App) runSupervisorExplain(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "intent", "task", "action", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	if action := options["action"]; action != "" {
		result, err := a.newClient(socket).SupervisorActionShow(ctx, workspace, action)
		if err != nil {
			return a.writeClientFailure(mode, "explain supervisor action", err)
		}
		return a.writeManagementResult(mode, result, fmt.Sprintf("action: %s\ncondition: %s\nresponse: %s\nstatus: %s\nreasons: %s\n", result.Action.ID, result.Action.Condition, result.Action.Response, result.Action.Status, strings.Join(result.Action.Reasons, "; ")))
	}
	limit, failure := optionalIntOption(options, "limit", 0, 100)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).SupervisorExplain(ctx, localapi.SupervisorExplainParams{Workspace: workspace, Intent: options["intent"], Task: options["task"], Limit: int(limit)})
	if err != nil {
		return a.writeClientFailure(mode, "explain supervisor", err)
	}
	if mode == outputJSON {
		return a.writeManagementResult(mode, result, "")
	}
	fmt.Fprintf(a.stdout, "as_of_event_sequence: %d\n", result.Explanation.AsOfEventSequence)
	for _, candidate := range result.Explanation.Candidates {
		fmt.Fprintf(a.stdout, "%s\teligible=%t\t%s\n", candidate.IntentID, candidate.Eligible, strings.Join(candidate.Reasons, "; "))
	}
	return ExitOK
}

func (a *App) runApproval(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, approvalHelp)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	switch args[0] {
	case "list":
		return a.runApprovalList(ctx, mode, args[1:])
	case "inspect":
		return a.runApprovalInspect(ctx, mode, args[1:])
	case "allow":
		return a.runApprovalDecision(ctx, mode, args[1:], true)
	case "deny":
		return a.runApprovalDecision(ctx, mode, args[1:], false)
	default:
		return a.writeFailure(mode, usageFailure("unknown approval command "+args[0], "run 'crewfold help approval' for usage"))
	}
}

func (a *App) runApprovalList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "status", "action", "cursor", "limit", "socket")
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
	result, err := a.newClient(socket).ApprovalList(ctx, localapi.ApprovalListParams{Workspace: workspace, Project: options["project"], Status: options["status"], Action: options["action"], PageParams: localapi.PageParams{Cursor: options["cursor"], Limit: int(limit)}})
	if err != nil {
		return a.writeClientFailure(mode, "list approvals", err)
	}
	if mode == outputJSON {
		return a.writeManagementResult(mode, result, "")
	}
	for _, approval := range result.Approvals {
		fmt.Fprintf(a.stdout, "%s\t%s\t%s\trevision %d\n", approval.ID, approval.Status, approval.ActionID, approval.Revision)
	}
	writePageMetadata(a.stdout, result.PageResult, len(result.Approvals))
	return ExitOK
}

func (a *App) runApprovalInspect(ctx context.Context, mode outputMode, args []string) int {
	approval, rest, failure := requiredLeadingArgument(args, "approval ID")
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
	result, err := a.newClient(socket).ApprovalInspect(ctx, workspace, approval)
	if err != nil {
		return a.writeClientFailure(mode, "inspect approval", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("approval: %s\naction: %s\nstatus: %s\nrevision: %d\n", result.Approval.ID, result.Approval.ActionID, result.Approval.Status, result.Approval.Revision))
}

func (a *App) runApprovalDecision(ctx context.Context, mode outputMode, args []string, allow bool) int {
	approval, rest, failure := requiredLeadingArgument(args, "approval ID")
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
	decisionNote, failure := requiredOption(options, "decision-note")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	params := localapi.ApprovalDecisionParams{Workspace: workspace, Approval: approval, ExpectedRevision: revision, DecisionNote: strings.TrimSpace(decisionNote), IdempotencyKey: options["idempotency-key"]}
	client := a.newClient(socket)
	var result localapi.ApprovalMutationResult
	var err error
	verb := "deny"
	if allow {
		verb = "allow"
		result, err = client.ApprovalAllow(ctx, params)
	} else {
		result, err = client.ApprovalDeny(ctx, params)
	}
	if err != nil {
		return a.writeClientFailure(mode, verb+" approval", err)
	}
	return a.writeManagementResult(mode, result, fmt.Sprintf("approval: %s\nstatus: %s\naction status: %s\n", result.Approval.ID, result.Approval.Status, result.Action.Status))
}

func (a *App) writeManagementResult(mode outputMode, value any, text string) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, value); err != nil {
			return a.writeFailure(outputText, internalFailure("write management output", err))
		}
	} else {
		fmt.Fprint(a.stdout, text)
	}
	return ExitOK
}

func csvOption(options map[string]string, name string, required bool) ([]string, *commandFailure) {
	value := strings.TrimSpace(options[name])
	if value == "" {
		if required {
			f := usageFailure("--"+name+" is required", "supply a comma-separated bounded list")
			return nil, &f
		}
		return []string{}, nil
	}
	parts, seen := strings.Split(value, ","), map[string]bool{}
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			f := usageFailure("--"+name+" contains an empty or duplicate value", "supply unique comma-separated values")
			return nil, &f
		}
		seen[part] = true
		result = append(result, part)
	}
	return result, nil
}

func optionalIntOption(options map[string]string, name string, minimum, maximum int64) (int64, *commandFailure) {
	value := options[name]
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		f := usageFailure(fmt.Sprintf("--%s must be an integer from %d to %d", name, minimum, maximum), "use a bounded integer value")
		return 0, &f
	}
	return parsed, nil
}

func concurrencyMapOption(options map[string]string, name string) (map[string]int, *commandFailure) {
	result := map[string]int{}
	if options[name] == "" {
		return result, nil
	}
	if err := json.Unmarshal([]byte(options[name]), &result); err != nil {
		f := usageFailure("--"+name+" must be a JSON object of positive integer limits", "for example '{\"proj_id\":2}'")
		return nil, &f
	}
	for key, value := range result {
		if strings.TrimSpace(key) == "" || value < 1 || value > 100 {
			f := usageFailure("--"+name+" contains an invalid concurrency entry", "use non-empty keys and values from 1 to 100")
			return nil, &f
		}
	}
	return result, nil
}

const managerHelp = `Usage:
  crewfold manager grant create --workspace <scope> --project <id> --objective <id> --task <planning-task> --agent <id> --expected-task-revision <n> --expected-agent-revision <n> --proposal-kinds <csv> --launch-profiles <csv> --max-open-proposals <n> --max-actions <n> --max-tasks <n> --max-dependencies <n> --max-claim-requirements <n> --token-limit <n> --cost-cents <n> --time-seconds <n> --socket <path>
  crewfold manager grant revoke <grant-id> --workspace <scope> --expected-revision <n> --reason <text> --socket <path>
  crewfold manager grant show <grant-id> --workspace <scope> --socket <path>
  crewfold manager grant list --workspace <scope> --socket <path> [filters]
  crewfold manager propose-tasks --workspace <scope> --objective <id> --socket <path> [--planning-task <id>] [--grant <id>] [--profile <id>]

Manager authority is an exact owner-created grant, never an agent role label. Set up
target launch profiles first, create and assign the planning task to its exact
agent, create a grant allowlisting the targets and binding that assignment, then
create the planning-run profile linked to that grant. Omitted
planning tuple fields resolve only when exactly one current compatible tuple exists.
`

const launchProfileHelp = `Usage:
  crewfold launch-profile create --workspace <scope> --project <id> --agent <id> --expected-agent-revision <n> --runtime <name> --provider <name> --scenario <path> --assignment-lease-seconds <n> --capability-ttl-seconds <n> --socket <path> [--manager-grant <id>]
  crewfold launch-profile retire <profile-id> --workspace <scope> --expected-revision <n> --reason <text> --socket <path>
  crewfold launch-profile show <profile-id> --workspace <scope> --socket <path>
  crewfold launch-profile list --workspace <scope> --socket <path> [filters]

A launch profile is immutable owner-authored scheduling eligibility. Its purpose is
metadata only; runtime, provider, checkout, and scenario can never be model-selected.
`

const proposalHelp = `Usage:
  crewfold proposal list --workspace <scope> --socket <path> [filters]
  crewfold proposal inspect <proposal-id> --workspace <scope> --socket <path>
  crewfold proposal accept <proposal-id> --workspace <scope> --expected-revision <n> --decision-note <text> --socket <path>
  crewfold proposal reject <proposal-id> --workspace <scope> --expected-revision <n> --decision-note <text> --socket <path>

Manager runs can only submit bounded proposals. Only this local-owner surface can
accept or reject them; acceptance atomically revalidates exact current authority.
`

const supervisorHelp = `Usage:
  crewfold supervisor policy show --workspace <scope> --socket <path>
  crewfold supervisor policy update --workspace <scope> --enabled <bool> --auto-schedule <bool> --auto-retry-limit <0..3> --retry-cooldown-seconds <n> --max-active-runs <n> --max-starting-runs <n> --default-project-concurrency <n> --default-provider-concurrency <n> --socket <path>
  crewfold supervisor run --workspace <scope> --socket <path> [--limit <n>]
  crewfold supervisor list --workspace <scope> --socket <path> [filters]
  crewfold supervisor explain --workspace <scope> --socket <path> [--intent <id>|--task <id>|--action <id>]

The supervisor evaluates durable scheduling intents deterministically. Automatic
retry is bounded and cooled down; reassignment always enters the owner approval queue.
`

const approvalHelp = `Usage:
  crewfold approval list --workspace <scope> [--project <project>] [--status <status>] [--action <id>] [--cursor <cursor>] [--limit <1..200>] --socket <path>
  crewfold approval inspect <approval-id> --workspace <scope> --socket <path>
  crewfold approval allow <approval-id> --workspace <scope> --expected-revision <n> --decision-note <text> --socket <path>
  crewfold approval deny <approval-id> --workspace <scope> --expected-revision <n> --decision-note <text> --socket <path>

Approvals are exact revision-bound local-owner decisions over supervisor actions.
`
