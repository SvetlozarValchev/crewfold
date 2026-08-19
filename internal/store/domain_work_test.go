package store

import (
	"context"
	"testing"

	"crewfold/internal/domain"
)

func TestM23CoordinatorProposalKeepsTheTeamInertUntilExactAcceptance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	inspection, err := storage.InspectProject(ctx, workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	coordinator := createDomainTestAgent(t, storage, workspace.ID, "coordinator", "owner-defined coordinator")
	membership, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: coordinator.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentDelegationFirst,
		PreferredEntry: true, IdempotencyKey: "attach-work-coordinator", CorrelationID: "attach-work-coordinator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: coordinator.Value.ID,
		Provider: "codex", ThreadID: "coordinator-work-thread", CWD: inspection.Checkouts[0].Path,
	}); err != nil {
		t.Fatal(err)
	}
	grant, err := storage.CreateDomainAgentStaffingGrant(ctx, CreateDomainAgentStaffingGrantCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManagerAgentIdentifier: coordinator.Value.ID,
		ExpectedMembershipRevision: membership.Value.Revision,
		Profiles:                   []domain.DomainAgentStaffingProfile{{Provider: "fake", Runtime: "fake", MaxConcurrency: 2}},
		TaskClasses:                []string{"implementation", "verification"}, MaxDescendants: 2, MaxConcurrency: 2,
		Budget:         domain.Budget{TokenLimit: 200, TimeSeconds: 600},
		IdempotencyKey: "work-grant", CorrelationID: "work-grant",
	})
	if err != nil {
		t.Fatal(err)
	}
	command := SubmitDomainWorkProposalCommand{
		ThreadID: "coordinator-work-thread", StaffingGrantID: grant.Value.ID,
		Summary: "Implement one bounded slice and verify it independently.",
		Content: domain.DomainWorkProposalContent{
			ObjectiveTitle: "Deliver one tested slice", ObjectiveBudget: domain.Budget{TokenLimit: 200, TimeSeconds: 600},
			PrimaryCheckoutID: inspection.Checkouts[0].ID, PrimaryCheckoutRevision: inspection.Checkouts[0].Revision,
			Agents: []domain.DomainWorkProposalAgent{
				{Key: "implementer", Name: "implementer", Role: "implementation", OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentHandsOn, Provider: "fake", Runtime: "fake", MaxConcurrency: 1, TaskClass: "implementation", Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}},
				{Key: "verifier", Name: "verifier", Role: "verification", ParentKey: "implementer", OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentHandsOn, Provider: "fake", Runtime: "fake", MaxConcurrency: 1, TaskClass: "verification", Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}},
			},
			Tasks: []domain.DomainWorkProposalTask{
				{Key: "build", Title: "Build the slice", Description: "Implement the bounded deliverable.", TaskClass: "implementation", Priority: 100, Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}, AssigneeKey: "implementer"},
				{Key: "verify", Title: "Verify the slice", Description: "Independently test and review the deliverable.", TaskClass: "verification", Priority: 200, Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}, AssigneeKey: "verifier", DependsOn: []string{"build"}, DependencyDelivery: map[string]string{"build": domain.DependencyDeliveryHandoffWithEvidence}},
			},
		},
		IdempotencyKey: "submit-domain-work", CorrelationID: "submit-domain-work",
	}
	invalid := command
	invalid.IdempotencyKey = "submit-domain-work-invalid-key"
	invalid.CorrelationID = "submit-domain-work-invalid-key"
	invalid.Content.Tasks = append([]domain.DomainWorkProposalTask(nil), command.Content.Tasks...)
	invalid.Content.Tasks[0].Key = "build_slice"
	if _, err := storage.SubmitDomainWorkProposal(ctx, invalid); ErrorCode(err) != CodeDomainStaffingDenied || err.Error() != `work proposal task key "build_slice" must be a lowercase identifier` {
		t.Fatalf("invalid proposal key error = %v, code = %q", err, ErrorCode(err))
	}
	eventsBefore := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	submitted, err := storage.SubmitDomainWorkProposal(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Value.Status != domain.DomainWorkProposalPending || submitted.Value.SourceAgentID != coordinator.Value.ID || submitted.Value.AsOfEventSequence != eventsBefore[len(eventsBefore)-1].Sequence {
		t.Fatalf("submitted proposal = %#v", submitted)
	}
	if submitted.Value.Content.ReferenceCheckoutIDs == nil || submitted.Value.Content.Tasks[0].DependsOn == nil || submitted.Value.Content.Tasks[0].DependencyDelivery == nil {
		t.Fatalf("proposal emitted nullable collection fields: %#v", submitted.Value.Content)
	}
	if objectives, listErr := storage.ListObjectives(ctx, ListObjectivesQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Limit: 10}); listErr != nil || objectives.Total != 0 {
		t.Fatalf("inert proposal objectives = %#v, %v", objectives, listErr)
	}
	var inertAgentCount int
	if err := storage.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agents WHERE workspace_id=? AND name IN ('implementer','verifier')", workspace.ID).Scan(&inertAgentCount); err != nil || inertAgentCount != 0 {
		t.Fatalf("proposal created agents before acceptance: count=%d error=%v", inertAgentCount, err)
	}
	replayed, err := storage.SubmitDomainWorkProposal(ctx, command)
	if err != nil || replayed.Value.ID != submitted.Value.ID || replayed.EventSequence != submitted.EventSequence {
		t.Fatalf("proposal replay = %#v, %v", replayed, err)
	}
	rejectedCommand := command
	rejectedCommand.IdempotencyKey = "submit-rejected-domain-work"
	rejectedCommand.CorrelationID = "submit-rejected-domain-work"
	rejectedCommand.Summary = "This team must remain inert when rejected."
	rejectedCommand.Content.Agents = []domain.DomainWorkProposalAgent{{Key: "rejected", Name: "rejected-worker", Role: "implementation", OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentHandsOn, Provider: "fake", Runtime: "fake", MaxConcurrency: 1, TaskClass: "implementation", Budget: domain.Budget{TokenLimit: 50, TimeSeconds: 100}}}
	rejectedCommand.Content.Tasks = []domain.DomainWorkProposalTask{{Key: "rejected", Title: "Never publish this task", TaskClass: "implementation", Priority: 1, Budget: domain.Budget{TokenLimit: 50, TimeSeconds: 100}, AssigneeKey: "rejected", DependsOn: []string{}, DependencyDelivery: map[string]string{}}}
	rejected, err := storage.SubmitDomainWorkProposal(ctx, rejectedCommand)
	if err != nil {
		t.Fatal(err)
	}
	rejectedDecision, err := storage.RejectDomainWorkProposal(ctx, DecideDomainWorkProposalCommand{WorkspaceIdentifier: workspace.ID, ProposalID: rejected.Value.ID, ExpectedRevision: 1, DecisionNote: "Do not create this team.", IdempotencyKey: "reject-domain-work", CorrelationID: "reject-domain-work"})
	if err != nil || rejectedDecision.Proposal.Status != domain.DomainWorkProposalRejected || len(rejectedDecision.Effects) != 0 {
		t.Fatalf("rejected proposal = %#v, %v", rejectedDecision, err)
	}
	var rejectedAgentCount int
	if err := storage.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agents WHERE workspace_id=? AND name='rejected-worker'", workspace.ID).Scan(&rejectedAgentCount); err != nil || rejectedAgentCount != 0 {
		t.Fatalf("rejected proposal published an agent: count=%d error=%v", rejectedAgentCount, err)
	}

	staleCommand := rejectedCommand
	staleCommand.IdempotencyKey = "submit-stale-domain-work"
	staleCommand.CorrelationID = "submit-stale-domain-work"
	staleCommand.Summary = "This team must remain atomic when its name becomes stale."
	staleCommand.Content.Agents = []domain.DomainWorkProposalAgent{{Key: "stale", Name: "stale-worker", Role: "implementation", OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentHandsOn, Provider: "fake", Runtime: "fake", MaxConcurrency: 1, TaskClass: "implementation", Budget: domain.Budget{TokenLimit: 50, TimeSeconds: 100}}}
	staleCommand.Content.Tasks = []domain.DomainWorkProposalTask{{Key: "stale", Title: "Do not partially publish this task", TaskClass: "implementation", Priority: 1, Budget: domain.Budget{TokenLimit: 50, TimeSeconds: 100}, AssigneeKey: "stale", DependsOn: []string{}, DependencyDelivery: map[string]string{}}}
	stale, err := storage.SubmitDomainWorkProposal(ctx, staleCommand)
	if err != nil {
		t.Fatal(err)
	}
	_ = createDomainTestAgent(t, storage, workspace.ID, "stale-worker", "conflicting owner-created definition")
	staleDecision, err := storage.AcceptDomainWorkProposal(ctx, DecideDomainWorkProposalCommand{WorkspaceIdentifier: workspace.ID, ProposalID: stale.Value.ID, ExpectedRevision: 1, DecisionNote: "Attempt exact acceptance after a conflicting change.", IdempotencyKey: "accept-stale-domain-work", CorrelationID: "accept-stale-domain-work"})
	if err != nil || staleDecision.Proposal.Status != domain.DomainWorkProposalStale || len(staleDecision.Effects) != 0 {
		t.Fatalf("stale proposal = %#v, %v", staleDecision, err)
	}
	if objectives, listErr := storage.ListObjectives(ctx, ListObjectivesQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Limit: 10}); listErr != nil || objectives.Total != 0 {
		t.Fatalf("stale acceptance partially published graph = %#v, %v", objectives, listErr)
	}

	accepted, err := storage.AcceptDomainWorkProposal(ctx, DecideDomainWorkProposalCommand{
		WorkspaceIdentifier: workspace.ID, ProposalID: submitted.Value.ID, ExpectedRevision: 1,
		DecisionNote: "Accept this exact graph for supervised execution.", IdempotencyKey: "accept-domain-work", CorrelationID: "accept-domain-work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Proposal.Status != domain.DomainWorkProposalAccepted || accepted.Proposal.Revision != 2 || len(accepted.Effects) != 14 {
		t.Fatalf("accepted proposal = %#v", accepted)
	}
	acceptedReplay, err := storage.AcceptDomainWorkProposal(ctx, DecideDomainWorkProposalCommand{
		WorkspaceIdentifier: workspace.ID, ProposalID: submitted.Value.ID, ExpectedRevision: 1,
		DecisionNote: "Accept this exact graph for supervised execution.", IdempotencyKey: "accept-domain-work", CorrelationID: "accept-domain-work",
	})
	if err != nil || acceptedReplay.EventSequence != accepted.EventSequence || len(acceptedReplay.Effects) != len(accepted.Effects) {
		t.Fatalf("accepted proposal replay = %#v, %v", acceptedReplay, err)
	}
	var objectiveCount, taskCount, dependencyCount, intentCount int
	if err := storage.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM objectives WHERE project_id=?", project.ID).Scan(&objectiveCount); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE project_id=?", project.ID).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM task_dependencies WHERE task_id IN (SELECT id FROM tasks WHERE project_id=?)", project.ID).Scan(&dependencyCount); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduling_intents WHERE source_domain_work_proposal_id=? AND status='pending'", submitted.Value.ID).Scan(&intentCount); err != nil {
		t.Fatal(err)
	}
	if objectiveCount != 1 || taskCount != 2 || dependencyCount != 1 || intentCount != 2 {
		t.Fatalf("accepted graph counts objective=%d task=%d dependency=%d intent=%d", objectiveCount, taskCount, dependencyCount, intentCount)
	}
	createdAgents := make(map[string]domain.AgentDefinition, 2)
	for _, name := range []string{"implementer", "verifier"} {
		created, agentErr := queryAgent(ctx, storage.db, workspace.ID, name)
		if agentErr != nil {
			t.Fatalf("accepted agent %s = %v", name, agentErr)
		}
		placed, placeErr := queryDomainAgentMembership(ctx, storage.db, project.ID, created.ID)
		if placeErr != nil || placed.WorkstreamID == "" || placed.Revision != 1 {
			t.Fatalf("accepted placement %s = %#v, %v", name, placed, placeErr)
		}
		createdAgents[name] = created
	}
	verifierMembership, err := queryDomainAgentMembership(ctx, storage.db, project.ID, createdAgents["verifier"].ID)
	if err != nil || verifierMembership.ParentAgentID != createdAgents["implementer"].ID {
		t.Fatalf("accepted nested hierarchy = %#v, %v", verifierMembership, err)
	}
	report, err := storage.VerifyCanonical(ctx, CanonicalVerifyOptions{Full: true})
	if err != nil || !report.Complete || report.Status != "ok" {
		t.Fatalf("canonical verification = %#v, %v", report, err)
	}
	policy, err := storage.ConfigureSupervisorPolicy(ctx, ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: workspace.ID, Enabled: true, AutoSchedule: true,
		Limits:           domain.SupervisorLimits{MaxActiveRuns: 4, MaxStartingRuns: 2, DefaultProjectConcurrency: 4, DefaultProviderConcurrency: 4},
		ExpectedRevision: 1, IdempotencyKey: "m23-work-policy", CorrelationID: "m23-work-policy",
	})
	if err != nil || !policy.Value.AutoSchedule {
		t.Fatalf("ConfigureSupervisorPolicy() = %#v, %v", policy, err)
	}
	firstSweep, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: workspace.ID, Limit: 100, IdempotencyKey: "m23-first-work-sweep", CorrelationID: "m23-first-work-sweep",
	})
	if err != nil || len(firstSweep.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(first) = %#v, %v", firstSweep, err)
	}
	firstRun, err := storage.RunDetail(ctx, workspace.ID, firstSweep.ScheduledRunIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if firstRun.Task.Title != "Build the slice" || firstRun.Run.CheckoutID != inspection.Checkouts[0].ID || firstRun.Checkout.Path != inspection.Checkouts[0].Path {
		t.Fatalf("first scheduled run = %#v", firstRun)
	}
	var verificationRuns int
	if err := storage.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE task_id IN (SELECT id FROM tasks WHERE objective_id=? AND title='Verify the slice')`, objectiveIDFromEffects(accepted.Effects)).Scan(&verificationRuns); err != nil {
		t.Fatal(err)
	}
	if verificationRuns != 0 {
		t.Fatalf("verification runs before delivered dependency = %d, want 0", verificationRuns)
	}
	starting, err := storage.MarkRunStarting(ctx, firstRun.Run.ID, "m23-first-run-starting")
	if err != nil {
		t.Fatal(err)
	}
	active, err := storage.MarkRunStarted(ctx, starting.ID, "m23-runtime", "m23-provider", "m23-first-run-started")
	if err != nil {
		t.Fatal(err)
	}
	completion, err := storage.SubmitRunReport(ctx, CreateRunReportCommand{
		RunID: active.Run.ID, Kind: domain.ObservationCompletion, Message: "built the bounded slice",
		Handoff: "verify the changed slice independently", Evidence: []string{"artifact:implementation"},
		Payload:        map[string]any{"changed_paths": []string{"src/slice.ts"}, "checks": []string{"unit"}},
		IdempotencyKey: "m23-first-run-completion",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ApplyQueuedRunReport(ctx, active.Run.ID, completion.ID, true, nil,
		prepareTestRunLogArchive(t, storage, active.Run.ID), "", "m23-apply-first-run-completion"); err != nil {
		t.Fatal(err)
	}
	secondSweep, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: workspace.ID, Limit: 100, IdempotencyKey: "m23-second-work-sweep", CorrelationID: "m23-second-work-sweep",
	})
	if err != nil || len(secondSweep.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(second) = %#v, %v", secondSweep, err)
	}
	secondRun, err := storage.RunDetail(ctx, workspace.ID, secondSweep.ScheduledRunIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	packet, err := storage.ContextPacket(ctx, workspace.ID, secondRun.Run.ContextPacketID)
	if err != nil {
		t.Fatal(err)
	}
	if secondRun.Task.Title != "Verify the slice" || secondRun.Run.CheckoutID != inspection.Checkouts[0].ID || len(packet.Dependencies) != 1 || packet.Dependencies[0].Output == nil ||
		packet.Dependencies[0].DeliveryRequirement != domain.DependencyDeliveryHandoffWithEvidence || packet.Dependencies[0].Output.RunID != firstRun.Run.ID ||
		packet.Dependencies[0].Output.Handoff != "verify the changed slice independently" {
		t.Fatalf("second scheduled run/context = run %#v packet %#v", secondRun, packet.Dependencies)
	}
}

func objectiveIDFromEffects(effects []domain.DomainWorkProposalEffect) string {
	for _, effect := range effects {
		if effect.EntityType == "objective" {
			return effect.EntityID
		}
	}
	return ""
}
