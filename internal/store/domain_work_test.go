package store

import (
	"context"
	"sort"
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
	implementationArtifact, err := storage.PublishRunArtifact(ctx, PublishRunArtifactCommand{
		RunID: active.Run.ID, Name: "implementation evidence", MediaType: "text/plain", Content: "the bounded slice is implemented",
		IdempotencyKey: "m24-first-run-artifact",
	})
	if err != nil {
		t.Fatal(err)
	}
	completion, err := storage.SubmitRunReport(ctx, CreateRunReportCommand{
		RunID: active.Run.ID, Kind: domain.ObservationCompletion, Message: "built the bounded slice",
		Handoff: "verify the changed slice independently", Evidence: []string{implementationArtifact.ID, "artifact:unpublished-claim"},
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
	if secondRun.Task.TaskClass != "verification" || packet.Task.TaskClass != "verification" {
		t.Fatalf("verification task class was not preserved: task=%q packet=%q", secondRun.Task.TaskClass, packet.Task.TaskClass)
	}
	verificationStarting, err := storage.MarkRunStarting(ctx, secondRun.Run.ID, "m24-verification-starting")
	if err != nil {
		t.Fatal(err)
	}
	verificationActive, err := storage.MarkRunStarted(ctx, verificationStarting.ID, "m24-runtime", "m24-provider", "m24-verification-started")
	if err != nil {
		t.Fatal(err)
	}
	verificationArtifact, err := storage.PublishRunArtifact(ctx, PublishRunArtifactCommand{
		RunID: verificationActive.Run.ID, Name: "verification evidence", MediaType: "text/plain", Content: "independent verification passed",
		IdempotencyKey: "m24-verification-artifact",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.SubmitRunReport(ctx, CreateRunReportCommand{
		RunID: verificationActive.Run.ID, Kind: domain.ObservationCompletion, Message: "verification passed",
		Handoff: "the slice is ready for owner acceptance", Evidence: []string{verificationArtifact.ID},
		Payload: map[string]any{"checks": []string{"review"}}, IdempotencyKey: "m24-missing-assessment",
	}); ErrorCode(err) != CodeInvalidReport {
		t.Fatalf("verification completion without assessment error = %v (%q)", err, ErrorCode(err))
	}
	passingAssessment, err := storage.SubmitRunReport(ctx, CreateRunReportCommand{
		RunID: verificationActive.Run.ID, Kind: domain.ObservationCompletion, Message: "verification passed",
		Handoff: "the slice is ready for owner acceptance", Assessment: "pass", Evidence: []string{verificationArtifact.ID},
		Payload: map[string]any{"assessment": "pass", "checks": []string{"review"}}, IdempotencyKey: "m24-pass-assessment",
	})
	if err != nil {
		t.Fatal(err)
	}
	assessed, err := storage.ApplyQueuedRunReport(ctx, verificationActive.Run.ID, passingAssessment.ID, true, nil,
		prepareTestRunLogArchive(t, storage, verificationActive.Run.ID), "", "m24-apply-pass-assessment")
	if err != nil {
		t.Fatal(err)
	}
	if assessed.Assessment != "pass" || assessed.Run.Status != domain.RunCompleted || assessed.Task.Status != domain.TaskCompleted {
		t.Fatalf("passing assessment did not complete verification: %#v", assessed)
	}
	objectiveID := objectiveIDFromEffects(accepted.Effects)
	delivery, err := storage.WorkstreamDelivery(ctx, workspace.ID, objectiveID)
	if err != nil {
		t.Fatal(err)
	}
	expectedEvidence := []string{implementationArtifact.ID, verificationArtifact.ID}
	sort.Strings(expectedEvidence)
	if delivery.State != domain.WorkstreamDeliveryVerifiedAwaitingAcceptance || delivery.TaskCount != 2 || delivery.CompletedTasks != 2 || delivery.VerificationTasks != 1 || delivery.PassingVerifications != 1 ||
		len(delivery.Evidence) != 2 || delivery.Evidence[0] != expectedEvidence[0] || delivery.Evidence[1] != expectedEvidence[1] {
		t.Fatalf("verified delivery = %#v", delivery)
	}
	rejectedDelivery, err := storage.RejectWorkstreamDelivery(ctx, DecideWorkstreamDeliveryCommand{
		WorkspaceIdentifier: workspace.ID, ObjectiveID: objectiveID, ExpectedObjectiveRevision: delivery.ObjectiveRevision,
		ExpectedSHA256: delivery.SHA256, Reason: "Owner requested one more product review.",
		IdempotencyKey: "m24-reject-delivery", CorrelationID: "m24-reject-delivery-a",
	})
	if err != nil || rejectedDelivery.Delivery.State != domain.WorkstreamDeliveryRejected {
		t.Fatalf("RejectWorkstreamDelivery() = %#v, %v", rejectedDelivery, err)
	}
	rejectedReplay, err := storage.RejectWorkstreamDelivery(ctx, DecideWorkstreamDeliveryCommand{
		WorkspaceIdentifier: workspace.ID, ObjectiveID: objectiveID, ExpectedObjectiveRevision: delivery.ObjectiveRevision,
		ExpectedSHA256: delivery.SHA256, Reason: "Owner requested one more product review.",
		IdempotencyKey: "m24-reject-delivery", CorrelationID: "m24-reject-delivery-b",
	})
	if err != nil || rejectedReplay.EventSequence != rejectedDelivery.EventSequence {
		t.Fatalf("delivery rejection replay = %#v, %v", rejectedReplay, err)
	}
	eventsBeforeStale := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	if _, err := storage.AcceptWorkstreamDelivery(ctx, DecideWorkstreamDeliveryCommand{
		WorkspaceIdentifier: workspace.ID, ObjectiveID: objectiveID, ExpectedObjectiveRevision: delivery.ObjectiveRevision,
		ExpectedSHA256: delivery.SHA256, IdempotencyKey: "m24-stale-accept", CorrelationID: "m24-stale-accept",
	}); ErrorCode(err) != CodeWorkstreamDeliveryStale {
		t.Fatalf("stale delivery acceptance error = %v (%q)", err, ErrorCode(err))
	}
	if eventsAfter := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000); len(eventsAfter) != len(eventsBeforeStale) {
		t.Fatalf("stale delivery acceptance appended an event: before=%d after=%d", len(eventsBeforeStale), len(eventsAfter))
	}
	acceptedDelivery, err := storage.AcceptWorkstreamDelivery(ctx, DecideWorkstreamDeliveryCommand{
		WorkspaceIdentifier: workspace.ID, ObjectiveID: objectiveID, ExpectedObjectiveRevision: rejectedDelivery.Delivery.ObjectiveRevision,
		ExpectedSHA256: delivery.SHA256, IdempotencyKey: "m24-accept-delivery", CorrelationID: "m24-accept-delivery-a",
	})
	if err != nil || acceptedDelivery.Delivery.State != domain.WorkstreamDeliveryAccepted {
		t.Fatalf("AcceptWorkstreamDelivery() = %#v, %v", acceptedDelivery, err)
	}
	acceptedDeliveryReplay, err := storage.AcceptWorkstreamDelivery(ctx, DecideWorkstreamDeliveryCommand{
		WorkspaceIdentifier: workspace.ID, ObjectiveID: objectiveID, ExpectedObjectiveRevision: rejectedDelivery.Delivery.ObjectiveRevision,
		ExpectedSHA256: delivery.SHA256, IdempotencyKey: "m24-accept-delivery", CorrelationID: "m24-accept-delivery-b",
	})
	if err != nil || acceptedDeliveryReplay.EventSequence != acceptedDelivery.EventSequence {
		t.Fatalf("delivery acceptance replay = %#v, %v", acceptedDeliveryReplay, err)
	}
	objective, err := storage.Objective(ctx, workspace.ID, objectiveID)
	if err != nil || objective.Status != domain.ObjectiveCompleted {
		t.Fatalf("accepted delivery objective = %#v, %v", objective, err)
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

func TestM24ReviewFindingsDeliverToRemediationInsteadOfDeadlockingTheGraph(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	inspection, err := storage.InspectProject(ctx, workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	coordinator := createDomainTestAgent(t, storage, workspace.ID, "review-coordinator", "coordinates review remediation")
	membership, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: coordinator.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentDelegationFirst,
		PreferredEntry: true, IdempotencyKey: "review-route-membership", CorrelationID: "review-route-membership",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: coordinator.Value.ID,
		Provider: "codex", ThreadID: "review-route-thread", CWD: inspection.Checkouts[0].Path,
	}); err != nil {
		t.Fatal(err)
	}
	grant, err := storage.CreateDomainAgentStaffingGrant(ctx, CreateDomainAgentStaffingGrantCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManagerAgentIdentifier: coordinator.Value.ID,
		ExpectedMembershipRevision: membership.Value.Revision,
		Profiles:                   []domain.DomainAgentStaffingProfile{{Provider: "fake", Runtime: "fake", MaxConcurrency: 3}},
		TaskClasses:                []string{"review", "implementation", "verification"}, MaxDescendants: 3, MaxConcurrency: 3,
		Budget: domain.Budget{TokenLimit: 300, TimeSeconds: 900}, IdempotencyKey: "review-route-grant", CorrelationID: "review-route-grant",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := storage.SubmitDomainWorkProposal(ctx, SubmitDomainWorkProposalCommand{
		ThreadID: "review-route-thread", StaffingGrantID: grant.Value.ID,
		Summary: "Review, remediate the exact findings, then verify the candidate.",
		Content: domain.DomainWorkProposalContent{
			ObjectiveTitle: "Close one independent review loop", ObjectiveBudget: domain.Budget{TokenLimit: 300, TimeSeconds: 900},
			PrimaryCheckoutID: inspection.Checkouts[0].ID, PrimaryCheckoutRevision: inspection.Checkouts[0].Revision,
			Agents: []domain.DomainWorkProposalAgent{
				{Key: "reviewer", Name: "route-reviewer", Role: "independent reviewer", OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentHandsOn, Provider: "fake", Runtime: "fake", MaxConcurrency: 1, TaskClass: "review", Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}},
				{Key: "remediator", Name: "route-remediator", Role: "implementation owner", OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentHandsOn, Provider: "fake", Runtime: "fake", MaxConcurrency: 1, TaskClass: "implementation", Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}},
				{Key: "verifier", Name: "route-verifier", Role: "final verifier", OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentHandsOn, Provider: "fake", Runtime: "fake", MaxConcurrency: 1, TaskClass: "verification", Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}},
			},
			Tasks: []domain.DomainWorkProposalTask{
				{Key: "review", Title: "Review the candidate", TaskClass: "review", Priority: 100, Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}, AssigneeKey: "reviewer"},
				{Key: "remediate", Title: "Remediate the review findings", TaskClass: "implementation", Priority: 200, Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}, AssigneeKey: "remediator", DependsOn: []string{"review"}, DependencyDelivery: map[string]string{"review": domain.DependencyDeliveryHandoffWithEvidence}},
				{Key: "verify", Title: "Verify the remediated candidate", TaskClass: "verification", Priority: 300, Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}, AssigneeKey: "verifier", DependsOn: []string{"remediate"}, DependencyDelivery: map[string]string{"remediate": domain.DependencyDeliveryHandoffWithEvidence}},
			},
		},
		IdempotencyKey: "review-route-proposal", CorrelationID: "review-route-proposal",
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := storage.AcceptDomainWorkProposal(ctx, DecideDomainWorkProposalCommand{
		WorkspaceIdentifier: workspace.ID, ProposalID: proposal.Value.ID, ExpectedRevision: proposal.Value.Revision,
		DecisionNote: "Exercise the exact review and remediation graph.", IdempotencyKey: "review-route-accept", CorrelationID: "review-route-accept",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ConfigureSupervisorPolicy(ctx, ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: workspace.ID, Enabled: true, AutoSchedule: true,
		Limits:           domain.SupervisorLimits{MaxActiveRuns: 3, MaxStartingRuns: 2, DefaultProjectConcurrency: 3, DefaultProviderConcurrency: 3},
		ExpectedRevision: 1, IdempotencyKey: "review-route-policy", CorrelationID: "review-route-policy",
	}); err != nil {
		t.Fatal(err)
	}

	startScheduled := func(key, wantTitle string) domain.RunDetail {
		t.Helper()
		sweep, sweepErr := storage.RunSupervisor(ctx, RunSupervisorCommand{WorkspaceIdentifier: workspace.ID, Limit: 20, IdempotencyKey: key + "-sweep", CorrelationID: key + "-sweep"})
		if sweepErr != nil || len(sweep.ScheduledRunIDs) != 1 {
			t.Fatalf("RunSupervisor(%s) = %#v, %v", key, sweep, sweepErr)
		}
		detail, detailErr := storage.RunDetail(ctx, workspace.ID, sweep.ScheduledRunIDs[0])
		if detailErr != nil || detail.Task.Title != wantTitle {
			t.Fatalf("scheduled %s = %#v, %v", key, detail, detailErr)
		}
		starting, startErr := storage.MarkRunStarting(ctx, detail.Run.ID, key+"-starting")
		if startErr != nil {
			t.Fatal(startErr)
		}
		active, activeErr := storage.MarkRunStarted(ctx, starting.ID, key+"-runtime", key+"-provider", key+"-started")
		if activeErr != nil {
			t.Fatal(activeErr)
		}
		return active
	}
	complete := func(key string, active domain.RunDetail, assessment, summary, handoff string) domain.RunDetail {
		t.Helper()
		artifact, artifactErr := storage.PublishRunArtifact(ctx, PublishRunArtifactCommand{
			RunID: active.Run.ID, Name: key + " evidence", MediaType: "text/plain", Content: summary, IdempotencyKey: key + "-artifact",
		})
		if artifactErr != nil {
			t.Fatal(artifactErr)
		}
		payload := map[string]any{"checks": []string{key + " check"}}
		if assessment != "" {
			payload["assessment"] = assessment
		}
		report, reportErr := storage.SubmitRunReport(ctx, CreateRunReportCommand{
			RunID: active.Run.ID, Kind: domain.ObservationCompletion, Message: summary, Handoff: handoff, Assessment: assessment,
			Evidence: []string{artifact.ID}, Payload: payload, IdempotencyKey: key + "-report",
		})
		if reportErr != nil {
			t.Fatal(reportErr)
		}
		result, applyErr := storage.ApplyQueuedRunReport(ctx, active.Run.ID, report.ID, true, nil, prepareTestRunLogArchive(t, storage, active.Run.ID), "", key+"-apply")
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		return result
	}

	review := startScheduled("review-route-review", "Review the candidate")
	reviewed := complete("review-route-review", review, "changes_requested", "two concrete product defects remain", "fix the named defects and preserve this review evidence")
	if reviewed.Run.Status != domain.RunCompleted || reviewed.Task.Status != domain.TaskCompleted || reviewed.Task.AssignmentID != "" || reviewed.Assessment != "changes_requested" {
		t.Fatalf("changes-requested review = %#v", reviewed)
	}
	var completedEvents, changesRequestedEvents int
	if err := storage.db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE entity_id=? AND type='task.completed'`, reviewed.Task.ID).Scan(&completedEvents); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE entity_id=? AND type='task.changes_requested'`, reviewed.Task.ID).Scan(&changesRequestedEvents); err != nil {
		t.Fatal(err)
	}
	if completedEvents != 1 || changesRequestedEvents != 0 {
		t.Fatalf("review terminal events completed=%d changes_requested=%d", completedEvents, changesRequestedEvents)
	}
	remediation := startScheduled("review-route-remediation", "Remediate the review findings")
	packet, err := storage.ContextPacket(ctx, workspace.ID, remediation.Run.ContextPacketID)
	if err != nil || len(packet.Dependencies) != 1 || packet.Dependencies[0].Output == nil || packet.Dependencies[0].Output.Handoff != "fix the named defects and preserve this review evidence" || len(packet.Dependencies[0].Output.EvidenceIDs) != 1 {
		t.Fatalf("remediation packet = %#v, %v", packet.Dependencies, err)
	}
	remediated := complete("review-route-remediation", remediation, "", "fixed both review findings", "independently verify the corrected candidate")
	if remediated.Run.Status != domain.RunCompleted || remediated.Task.Status != domain.TaskCompleted {
		t.Fatalf("remediation completion = %#v", remediated)
	}
	verification := startScheduled("review-route-verification", "Verify the remediated candidate")
	verified := complete("review-route-verification", verification, "pass", "the remediated candidate passes", "ready for exact owner acceptance")
	if verified.Run.Status != domain.RunCompleted || verified.Task.Status != domain.TaskCompleted || verified.Assessment != "pass" {
		t.Fatalf("final verification = %#v", verified)
	}
	delivery, err := storage.WorkstreamDelivery(ctx, workspace.ID, objectiveIDFromEffects(accepted.Effects))
	if err != nil || delivery.State != domain.WorkstreamDeliveryVerifiedAwaitingAcceptance || delivery.TaskCount != 3 || delivery.CompletedTasks != 3 || delivery.PassingVerifications != 1 || len(delivery.Blockers) != 0 {
		t.Fatalf("delivery after routed remediation = %#v, %v", delivery, err)
	}
}
