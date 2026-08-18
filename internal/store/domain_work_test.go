package store

import (
	"context"
	"testing"

	"crewfold/internal/domain"
)

func TestM22CoordinatorProposalPublishesOneExactAcceptedWorkGraph(t *testing.T) {
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
	createChild := func(name, taskClass, key string) domain.DomainAgentChildCreation {
		created, createErr := storage.CreateDomainAgentChild(ctx, CreateDomainAgentChildCommand{
			ThreadID: "coordinator-work-thread", GrantID: grant.Value.ID, Name: name, Role: taskClass,
			Provider: "fake", Runtime: "fake", MaxConcurrency: 1, OperatingCharter: testDomainAgentCharter,
			DelegationPolicy: domain.DomainAgentHandsOn, TaskClass: taskClass,
			Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}, IdempotencyKey: key, CorrelationID: key,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return created
	}
	implementer := createChild("implementer", "implementation", "create-work-implementer")
	verifier := createChild("verifier", "verification", "create-work-verifier")
	createProfile := func(child domain.DomainAgentChildCreation, purpose, key string) domain.LaunchProfile {
		created, createErr := storage.CreateLaunchProfile(ctx, CreateLaunchProfileCommand{
			WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: child.Agent.ID,
			ExpectedAgentRevision: child.Agent.Revision, Purpose: purpose, Runtime: child.Agent.Runtime, Provider: child.Agent.Provider,
			CheckoutIdentifier: inspection.Checkouts[0].ID, Scenario: managementProgressScenario(key),
			AssignmentLeaseSeconds: 3600, CapabilityTTLSeconds: 3600, IdempotencyKey: key, CorrelationID: key,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return created.Value
	}
	implementationProfile := createProfile(implementer, "implementation", "work-implementation-profile")
	verificationProfile := createProfile(verifier, "verification", "work-verification-profile")

	command := SubmitDomainWorkProposalCommand{
		ThreadID: "coordinator-work-thread", StaffingGrantID: grant.Value.ID,
		Summary: "Implement one bounded slice and verify it independently.",
		Content: domain.DomainWorkProposalContent{
			ObjectiveTitle: "Deliver one tested slice", ObjectiveBudget: domain.Budget{TokenLimit: 200, TimeSeconds: 600},
			Tasks: []domain.DomainWorkProposalTask{
				{Key: "build", Title: "Build the slice", Description: "Implement the bounded deliverable.", TaskClass: "implementation", Priority: 100, Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}, LaunchProfileID: implementationProfile.ID, DependsOn: []string{}},
				{Key: "verify", Title: "Verify the slice", Description: "Independently test and review the deliverable.", TaskClass: "verification", Priority: 200, Budget: domain.Budget{TokenLimit: 100, TimeSeconds: 300}, LaunchProfileID: verificationProfile.ID, DependsOn: []string{"build"}},
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
	if objectives, listErr := storage.ListObjectives(ctx, ListObjectivesQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Limit: 10}); listErr != nil || objectives.Total != 0 {
		t.Fatalf("inert proposal objectives = %#v, %v", objectives, listErr)
	}
	replayed, err := storage.SubmitDomainWorkProposal(ctx, command)
	if err != nil || replayed.Value.ID != submitted.Value.ID || replayed.EventSequence != submitted.EventSequence {
		t.Fatalf("proposal replay = %#v, %v", replayed, err)
	}

	accepted, err := storage.AcceptDomainWorkProposal(ctx, DecideDomainWorkProposalCommand{
		WorkspaceIdentifier: workspace.ID, ProposalID: submitted.Value.ID, ExpectedRevision: 1,
		DecisionNote: "Accept this exact graph for supervised execution.", IdempotencyKey: "accept-domain-work", CorrelationID: "accept-domain-work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Proposal.Status != domain.DomainWorkProposalAccepted || accepted.Proposal.Revision != 2 || len(accepted.Effects) != 6 {
		t.Fatalf("accepted proposal = %#v", accepted)
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
	report, err := storage.VerifyCanonical(ctx, CanonicalVerifyOptions{Full: true})
	if err != nil || !report.Complete || report.Status != "ok" {
		t.Fatalf("canonical verification = %#v, %v", report, err)
	}
}
