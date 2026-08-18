package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestM22OwnerStaffingGrantBoundsTypedDurableChildCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	manager := createDomainTestAgent(t, storage, workspace.ID, "terrain-lead", "owner-defined lead")
	attached, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: manager.Value.ID,
		PreferredEntry: true, IdempotencyKey: "attach-staffing-manager", CorrelationID: "attach-staffing-manager",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: manager.Value.ID,
		Provider: "codex", ThreadID: "terrain-manager-thread", CWD: "/work/terrain",
	}); err != nil {
		t.Fatal(err)
	}
	expiresAt := storage.clock().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	grantCommand := CreateDomainAgentStaffingGrantCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManagerAgentIdentifier: manager.Value.ID,
		ExpectedMembershipRevision: attached.Value.Revision,
		Profiles:                   []domain.DomainAgentStaffingProfile{{Provider: "codex-subscription", Runtime: "herdr", MaxConcurrency: 1}},
		TaskClasses:                []string{"implementer", "reviewer"}, MaxDescendants: 2, MaxConcurrency: 2,
		Budget: domain.Budget{TokenLimit: 20, CostCents: 40, TimeSeconds: 60}, ExpiresAt: expiresAt,
		IdempotencyKey: "staffing-grant-one", CorrelationID: "staffing-grant-one",
	}
	grant, err := storage.CreateDomainAgentStaffingGrant(ctx, grantCommand)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Value.ManagerAgentID != manager.Value.ID || grant.Value.ManagerMembershipRevision != attached.Value.Revision ||
		grant.Value.Status != domain.DomainStaffingGrantActive || len(grant.Value.Profiles) != 1 || len(grant.Value.TaskClasses) != 2 {
		t.Fatalf("staffing grant = %#v", grant)
	}
	grantReplay, err := storage.CreateDomainAgentStaffingGrant(ctx, grantCommand)
	if err != nil || grantReplay.Value.ID != grant.Value.ID || grantReplay.EventSequence != grant.EventSequence {
		t.Fatalf("staffing grant replay = %#v, %v", grantReplay, err)
	}

	createChild := func(name, taskClass, key string, budget domain.Budget) (domain.DomainAgentChildCreation, error) {
		return storage.CreateDomainAgentChild(ctx, CreateDomainAgentChildCommand{
			ThreadID: "terrain-manager-thread", GrantID: grant.Value.ID, Name: name,
			Role: "arbitrary " + taskClass, Provider: "codex-subscription", Runtime: "herdr", MaxConcurrency: 1,
			TaskClass: taskClass, Budget: budget, IdempotencyKey: key, CorrelationID: key,
		})
	}
	eventsBefore := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	first, err := createChild("terrain-implementer", "implementer", "child-one", domain.Budget{TokenLimit: 10, CostCents: 20, TimeSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if first.Membership.ParentAgentID != manager.Value.ID || first.Agent.Name != "terrain-implementer" ||
		first.Allocation.TaskClass != "implementer" || first.Allocation.GrantID != grant.Value.ID || len(first.EventSequences) != 3 {
		t.Fatalf("first durable child = %#v", first)
	}
	firstReplay, err := createChild("terrain-implementer", "implementer", "child-one", domain.Budget{TokenLimit: 10, CostCents: 20, TimeSeconds: 30})
	if err != nil || firstReplay.Agent.ID != first.Agent.ID || firstReplay.Allocation.ID != first.Allocation.ID {
		t.Fatalf("durable child replay = %#v, %v", firstReplay, err)
	}
	eventsAfterFirst := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	if len(eventsAfterFirst) != len(eventsBefore)+3 {
		t.Fatalf("durable child events = %d -> %d", len(eventsBefore), len(eventsAfterFirst))
	}
	wrongProfile := CreateDomainAgentChildCommand{
		ThreadID: "terrain-manager-thread", GrantID: grant.Value.ID, Name: "wrong-provider", Role: "reviewer",
		Provider: "other", Runtime: "herdr", MaxConcurrency: 1, TaskClass: "reviewer",
		Budget: domain.Budget{TokenLimit: 1}, IdempotencyKey: "child-wrong-profile", CorrelationID: "child-wrong-profile",
	}
	if _, err := storage.CreateDomainAgentChild(ctx, wrongProfile); ErrorCode(err) != CodeDomainStaffingDenied {
		t.Fatalf("wrong profile error = %v, code %q", err, ErrorCode(err))
	}
	second, err := createChild("terrain-reviewer", "reviewer", "child-two", domain.Budget{TokenLimit: 10, CostCents: 20, TimeSeconds: 30})
	if err != nil || second.Membership.ParentAgentID != manager.Value.ID {
		t.Fatalf("second durable child = %#v, %v", second, err)
	}
	if _, err := createChild("terrain-third", "reviewer", "child-three", domain.Budget{}); ErrorCode(err) != CodeDomainStaffingCapacity {
		t.Fatalf("over-capacity child error = %v, code %q", err, ErrorCode(err))
	}
	if got := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000); len(got) != len(eventsAfterFirst)+3 {
		t.Fatal("denied durable child appended a domain event")
	}
	preferred := false
	updatedManager, err := storage.UpdateDomainAgent(ctx, UpdateDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: manager.Value.ID,
		PreferredEntry: &preferred, ExpectedRevision: attached.Value.Revision,
		IdempotencyKey: "move-staffing-manager", CorrelationID: "move-staffing-manager",
	})
	if err != nil || updatedManager.Value.Revision != attached.Value.Revision+1 {
		t.Fatalf("update staffing manager = %#v, %v", updatedManager, err)
	}
	if _, err := createChild("stale-child", "reviewer", "child-stale", domain.Budget{}); ErrorCode(err) != CodeDomainStaffingDenied {
		t.Fatalf("stale staffing grant error = %v, code %q", err, ErrorCode(err))
	}
	revoked, err := storage.RevokeDomainAgentStaffingGrant(ctx, RevokeDomainAgentStaffingGrantCommand{
		WorkspaceIdentifier: workspace.ID, GrantID: grant.Value.ID, ExpectedRevision: grant.Value.Revision,
		IdempotencyKey: "revoke-staffing-grant", CorrelationID: "revoke-staffing-grant",
	})
	if err != nil || revoked.Value.Status != domain.DomainStaffingGrantRevoked || revoked.Value.Revision != 2 {
		t.Fatalf("revoke staffing grant = %#v, %v", revoked, err)
	}
	listed, err := storage.DomainAgentStaffingGrants(ctx, workspace.ID, project.ID, manager.Value.ID)
	if err != nil || len(listed) != 1 || listed[0].Status != domain.DomainStaffingGrantRevoked {
		t.Fatalf("listed staffing grants = %#v, %v", listed, err)
	}
	report, err := storage.VerifyCanonical(ctx, CanonicalVerifyOptions{Full: true})
	if err != nil || !report.Complete || report.Status != "ok" {
		t.Fatalf("canonical verification after durable staffing = %#v, %v", report, err)
	}
}

func TestM22ExpiredStaffingGrantIsVisibleAndCannotCreateAChild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, project := initializeWorkTestProject(t, storage)
	manager := createDomainTestAgent(t, storage, workspace.ID, "orchid", "arbitrary coordinator")
	attached, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: manager.Value.ID,
		PreferredEntry: true, IdempotencyKey: "attach-expiring-manager", CorrelationID: "attach-expiring-manager",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: manager.Value.ID,
		Provider: "codex", ThreadID: "expiring-manager-thread", CWD: "/work/orchid",
	}); err != nil {
		t.Fatal(err)
	}
	grant, err := storage.CreateDomainAgentStaffingGrant(ctx, CreateDomainAgentStaffingGrantCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManagerAgentIdentifier: manager.Value.ID,
		ExpectedMembershipRevision: attached.Value.Revision,
		Profiles:                   []domain.DomainAgentStaffingProfile{{Provider: "codex-subscription", Runtime: "herdr", MaxConcurrency: 1}},
		TaskClasses:                []string{"review"}, MaxDescendants: 1, MaxConcurrency: 1,
		Budget: domain.Budget{TokenLimit: 10, TimeSeconds: 30}, ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
		IdempotencyKey: "expiring-staffing-grant", CorrelationID: "expiring-staffing-grant",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	now = now.Add(2 * time.Minute)
	if _, err := storage.CreateDomainAgentChild(ctx, CreateDomainAgentChildCommand{
		ThreadID: "expiring-manager-thread", GrantID: grant.Value.ID, Name: "late-reviewer", Role: "review",
		Provider: "codex-subscription", Runtime: "herdr", MaxConcurrency: 1, TaskClass: "review",
		Budget: domain.Budget{TokenLimit: 1, TimeSeconds: 1}, IdempotencyKey: "late-child", CorrelationID: "late-child",
	}); ErrorCode(err) != CodeDomainStaffingDenied {
		t.Fatalf("expired child error = %v, code %q", err, ErrorCode(err))
	}
	listed, err := storage.DomainAgentStaffingGrants(ctx, workspace.ID, project.ID, manager.Value.ID)
	if err != nil || len(listed) != 1 || listed[0].Status != domain.DomainStaffingGrantExpired {
		t.Fatalf("expired grant list = %#v, %v", listed, err)
	}
	if eventsAfter := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000); len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("expired child request appended events: %d -> %d", len(eventsBefore), len(eventsAfter))
	}
}
