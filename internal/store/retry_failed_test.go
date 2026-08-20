package store

import (
	"context"
	"testing"

	"crewfold/internal/domain"
)

func TestOwnerRetryFailedRunPreservesFailureAndReusesExactLaunchProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	inspection, err := storage.InspectProject(ctx, workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	coordinator := createDomainTestAgent(t, storage, workspace.ID, "retry-coordinator", "owner-defined coordinator")
	membership, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: coordinator.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentDelegationFirst,
		PreferredEntry: true, IdempotencyKey: "retry-failed-coordinator", CorrelationID: "retry-failed-coordinator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: coordinator.Value.ID,
		Provider: "codex", ThreadID: "retry-failed-thread", CWD: inspection.Checkouts[0].Path,
	}); err != nil {
		t.Fatal(err)
	}
	grant, err := storage.CreateDomainAgentStaffingGrant(ctx, CreateDomainAgentStaffingGrantCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManagerAgentIdentifier: coordinator.Value.ID,
		ExpectedMembershipRevision: membership.Value.Revision,
		Profiles:                   []domain.DomainAgentStaffingProfile{{Provider: "fake", Runtime: "fake", MaxConcurrency: 1}},
		TaskClasses:                []string{"implementation"}, MaxDescendants: 1, MaxConcurrency: 1,
		Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}, IdempotencyKey: "retry-failed-grant", CorrelationID: "retry-failed-grant",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := storage.SubmitDomainWorkProposal(ctx, SubmitDomainWorkProposalCommand{
		ThreadID: "retry-failed-thread", StaffingGrantID: grant.Value.ID, Summary: "Create one exact retry fixture.",
		Content: domain.DomainWorkProposalContent{
			ObjectiveTitle: "Retry one failed provider attempt", ObjectiveBudget: domain.Budget{TokenLimit: 100, TimeSeconds: 300},
			PrimaryCheckoutID: inspection.Checkouts[0].ID, PrimaryCheckoutRevision: inspection.Checkouts[0].Revision,
			Agents: []domain.DomainWorkProposalAgent{{Key: "worker", Name: "retry-worker", Role: "implementation", OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentHandsOn, Provider: "fake", Runtime: "fake", MaxConcurrency: 1, TaskClass: "implementation", Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}}},
			Tasks:  []domain.DomainWorkProposalTask{{Key: "build", Title: "Build once", TaskClass: "implementation", Priority: 1, Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}, AssigneeKey: "worker"}},
		},
		IdempotencyKey: "retry-failed-proposal", CorrelationID: "retry-failed-proposal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.AcceptDomainWorkProposal(ctx, DecideDomainWorkProposalCommand{
		WorkspaceIdentifier: workspace.ID, ProposalID: proposal.Value.ID, ExpectedRevision: proposal.Value.Revision,
		DecisionNote: "Accept exact retry fixture.", IdempotencyKey: "retry-failed-accept", CorrelationID: "retry-failed-accept",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ConfigureSupervisorPolicy(ctx, ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: workspace.ID, Enabled: true, AutoSchedule: true,
		Limits:           domain.SupervisorLimits{MaxActiveRuns: 2, MaxStartingRuns: 1, DefaultProjectConcurrency: 2, DefaultProviderConcurrency: 2},
		ExpectedRevision: 1, IdempotencyKey: "retry-failed-policy", CorrelationID: "retry-failed-policy",
	}); err != nil {
		t.Fatal(err)
	}
	sweep, err := storage.RunSupervisor(ctx, RunSupervisorCommand{WorkspaceIdentifier: workspace.ID, Limit: 10, IdempotencyKey: "retry-failed-sweep", CorrelationID: "retry-failed-sweep"})
	if err != nil || len(sweep.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor() = %#v, %v", sweep, err)
	}
	prior, err := storage.RunDetail(ctx, workspace.ID, sweep.ScheduledRunIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	starting, err := storage.MarkRunStarting(ctx, prior.Run.ID, "retry-failed-starting")
	if err != nil {
		t.Fatal(err)
	}
	active, err := storage.MarkRunStarted(ctx, starting.ID, "retry-failed-runtime", "retry-failed-provider", "retry-failed-started")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := storage.FailRun(ctx, active.Run.ID, "provider_process_exited", "provider process exited after the account became unavailable", nil, "provider exited before terminal logs were captured", "retry-failed-terminal")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Run.Status != domain.RunFailed || failed.Task.Status != domain.TaskFailed || failed.Task.AssignmentID != "" {
		t.Fatalf("failed attempt = %#v", failed)
	}
	retried, err := storage.RetryFailedRun(ctx, RetryFailedRunCommand{
		WorkspaceIdentifier: workspace.ID, PriorRunID: failed.Run.ID,
		ExpectedRunRevision: failed.Run.Revision, ExpectedTaskRevision: failed.Task.Revision,
		IdempotencyKey: "retry-failed-owner", CorrelationID: "retry-failed-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if retried.Detail.Run.ID == failed.Run.ID || retried.Detail.Run.Status != domain.RunRequested || retried.Detail.Task.Status != domain.TaskAssigned ||
		retried.Detail.Run.AgentID != failed.Run.AgentID || retried.Detail.Run.CheckoutID != failed.Run.CheckoutID || retried.Detail.Run.Runtime != failed.Run.Runtime || retried.Detail.Run.Provider != failed.Run.Provider ||
		retried.Detail.Task.AssignmentID == "" || retried.Detail.Task.AssignmentID == failed.Run.AssignmentID {
		t.Fatalf("retried attempt = %#v", retried.Detail)
	}
	replay, err := storage.RetryFailedRun(ctx, RetryFailedRunCommand{
		WorkspaceIdentifier: workspace.ID, PriorRunID: failed.Run.ID,
		ExpectedRunRevision: failed.Run.Revision, ExpectedTaskRevision: failed.Task.Revision,
		IdempotencyKey: "retry-failed-owner", CorrelationID: "retry-failed-owner",
	})
	if err != nil || replay.Detail.Run.ID != retried.Detail.Run.ID || replay.EventSequence != retried.EventSequence {
		t.Fatalf("retry replay = %#v, %v", replay, err)
	}
	immutablePrior, err := storage.RunDetail(ctx, workspace.ID, failed.Run.ID)
	if err != nil || immutablePrior.Run.Status != domain.RunFailed || immutablePrior.Run.Revision != failed.Run.Revision {
		t.Fatalf("immutable prior = %#v, %v", immutablePrior, err)
	}
	report, err := storage.VerifyCanonical(ctx, CanonicalVerifyOptions{Full: true})
	if err != nil || !report.Complete || report.Status != "ok" {
		t.Fatalf("canonical verification = %#v, %v", report, err)
	}
}
