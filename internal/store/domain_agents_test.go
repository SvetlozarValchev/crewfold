package store

import (
	"context"
	"path/filepath"
	"testing"

	"crewfold/internal/domain"
)

const testDomainAgentCharter = "Own the described durable responsibility, keep canonical context current, communicate material boundaries, and escalate authority gaps to the owner."

func TestM22OwnerCreatesDomainAgentAtomicallyAndReplaySafely(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	parent := createDomainTestAgent(t, storage, workspace.ID, "terrain-lead", "workstream lead")
	if _, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: parent.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		PreferredEntry: true, IdempotencyKey: "m22-owner-parent", CorrelationID: "m22-owner-parent",
	}); err != nil {
		t.Fatal(err)
	}
	command := CreateDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Name: "terrain-reviewer",
		Role: "independent review", Provider: "codex", Runtime: "herdr", MaxConcurrency: 2,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		ParentAgentIdentifier: parent.Value.ID, IdempotencyKey: "m22-owner-create", CorrelationID: "m22-owner-create",
	}
	created, err := storage.CreateDomainAgent(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if created.Agent.Definition.Name != command.Name || created.Agent.Membership.ParentAgentID != parent.Value.ID || len(created.EventSequences) != 2 {
		t.Fatalf("CreateDomainAgent() = %#v", created)
	}
	replayed, err := storage.CreateDomainAgent(ctx, command)
	if err != nil || replayed.Agent.Definition.ID != created.Agent.Definition.ID || replayed.Agent.Membership != created.Agent.Membership || len(replayed.EventSequences) != 2 {
		t.Fatalf("CreateDomainAgent(replay) = %#v, %v; want %#v", replayed, err, created)
	}
	eventsBefore := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	if _, err := storage.CreateDomainAgent(ctx, CreateDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Name: command.Name,
		Role: "another label", Provider: "codex", Runtime: "herdr", MaxConcurrency: 1,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		IdempotencyKey: "m22-owner-duplicate", CorrelationID: "m22-owner-duplicate",
	}); ErrorCode(err) != CodeDomainAgentExists {
		t.Fatalf("duplicate create error = %v, code %q", err, ErrorCode(err))
	}
	eventsAfter := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("rejected atomic create appended events: before=%d after=%d", len(eventsBefore), len(eventsAfter))
	}
}

func TestM22DomainAgentTreeIsNeutralDurableAndIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	workstream, err := storage.CreateObjective(ctx, CreateObjectiveCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Title: "Terrain consolidation",
		IdempotencyKey: "m22-workstream", CorrelationID: "m22-workstream",
	})
	if err != nil {
		t.Fatal(err)
	}
	root := createDomainTestAgent(t, storage, workspace.ID, "orchid", "owner-defined-coordinator")
	child := createDomainTestAgent(t, storage, workspace.ID, "red-team", "independent-review")
	unattachedNamedSteward := createDomainTestAgent(t, storage, workspace.ID, "domain-steward", "ordinary-descriptive-role")

	rootCommand := AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: root.Value.Name,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		PreferredEntry: true, IdempotencyKey: "m22-attach-orchid", CorrelationID: "m22-attach-orchid",
	}
	attachedRoot, err := storage.AttachDomainAgent(ctx, rootCommand)
	if err != nil {
		t.Fatal(err)
	}
	replayedRoot, err := storage.AttachDomainAgent(ctx, rootCommand)
	if err != nil || replayedRoot != attachedRoot {
		t.Fatalf("AttachDomainAgent(replay) = %#v, %v; want %#v", replayedRoot, err, attachedRoot)
	}
	attachedChild, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: child.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		ParentAgentIdentifier: root.Value.ID, WorkstreamIdentifier: workstream.Value.ID,
		IdempotencyKey: "m22-attach-red-team", CorrelationID: "m22-attach-red-team",
	})
	if err != nil {
		t.Fatal(err)
	}
	if attachedChild.Value.ParentAgentID != root.Value.ID || attachedChild.Value.WorkstreamID != workstream.Value.ID {
		t.Fatalf("attached child = %#v", attachedChild.Value)
	}

	tree, err := storage.DomainAgentTree(ctx, workspace.Name, project.Name)
	if err != nil {
		t.Fatal(err)
	}
	if tree.ProjectID != project.ID || len(tree.Agents) != 2 {
		t.Fatalf("DomainAgentTree() = %#v", tree)
	}
	if tree.Agents[0].Definition.ID != root.Value.ID || !tree.Agents[0].Membership.PreferredEntry ||
		tree.Agents[1].Definition.ID != child.Value.ID || tree.Agents[1].Membership.ParentAgentID != root.Value.ID {
		t.Fatalf("domain tree order/content = %#v", tree.Agents)
	}
	for _, item := range tree.Agents {
		if item.Definition.ID == unattachedNamedSteward.Value.ID {
			t.Fatalf("agent name %q was treated as implicit domain membership", unattachedNamedSteward.Value.Name)
		}
	}
	report, err := storage.VerifyCanonical(ctx, CanonicalVerifyOptions{Full: true})
	if err != nil || !report.Complete || report.Status != "ok" {
		t.Fatalf("VerifyCanonical() = %#v, %v", report, err)
	}
}

func TestM22DomainAgentHierarchyRejectsCrossScopeCycleAndImplicitCardinality(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, firstProject := initializeWorkTestProject(t, storage)
	secondRegistered, err := storage.RegisterProject(ctx, RegisterProjectCommand{
		WorkspaceIdentifier: workspace.ID, Name: "other-domain", IdempotencyKey: "m22-other-domain",
		CorrelationID: "m22-other-domain", Observation: sourceTestObservation(filepath.Join(t.TempDir(), "other-domain"), "main"),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondProject := secondRegistered.Project
	firstWorkstream, err := storage.CreateObjective(ctx, CreateObjectiveCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: firstProject.ID, Title: "First workstream",
		IdempotencyKey: "m22-first-workstream", CorrelationID: "m22-first-workstream",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondWorkstream, err := storage.CreateObjective(ctx, CreateObjectiveCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: secondProject.ID, Title: "Second workstream",
		IdempotencyKey: "m22-second-workstream", CorrelationID: "m22-second-workstream",
	})
	if err != nil {
		t.Fatal(err)
	}
	root := createDomainTestAgent(t, storage, workspace.ID, "lead-one", "arbitrary")
	child := createDomainTestAgent(t, storage, workspace.ID, "child-one", "arbitrary")
	other := createDomainTestAgent(t, storage, workspace.ID, "lead-two", "arbitrary")
	for _, command := range []AttachDomainAgentCommand{
		{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: firstProject.ID, AgentIdentifier: root.Value.ID, OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive, PreferredEntry: true, IdempotencyKey: "m22-root", CorrelationID: "m22-root"},
		{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: firstProject.ID, AgentIdentifier: child.Value.ID, OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive, ParentAgentIdentifier: root.Value.ID, WorkstreamIdentifier: firstWorkstream.Value.ID, IdempotencyKey: "m22-child", CorrelationID: "m22-child"},
		{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: secondProject.ID, AgentIdentifier: other.Value.ID, OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive, PreferredEntry: true, WorkstreamIdentifier: secondWorkstream.Value.ID, IdempotencyKey: "m22-other", CorrelationID: "m22-other"},
	} {
		if _, err := storage.AttachDomainAgent(ctx, command); err != nil {
			t.Fatal(err)
		}
	}
	eventsBefore := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)

	if _, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: secondProject.ID, AgentIdentifier: root.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		IdempotencyKey: "m22-duplicate-domain", CorrelationID: "m22-duplicate-domain",
	}); ErrorCode(err) != CodeDomainAgentExists {
		t.Fatalf("cross-domain reattachment error = %v, code %q", err, ErrorCode(err))
	}
	if _, err := storage.UpdateDomainAgent(ctx, UpdateDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: firstProject.ID, AgentIdentifier: child.Value.ID,
		ParentAgentIdentifier: stringPointer(other.Value.ID), ExpectedRevision: 1,
		IdempotencyKey: "m22-cross-parent", CorrelationID: "m22-cross-parent",
	}); ErrorCode(err) != CodeDomainAgentNotFound {
		t.Fatalf("cross-domain parent error = %v, code %q", err, ErrorCode(err))
	}
	if _, err := storage.UpdateDomainAgent(ctx, UpdateDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: firstProject.ID, AgentIdentifier: child.Value.ID,
		WorkstreamIdentifier: stringPointer(secondWorkstream.Value.ID), ExpectedRevision: 1,
		IdempotencyKey: "m22-cross-workstream", CorrelationID: "m22-cross-workstream",
	}); ErrorCode(err) != CodeInvalidDomainAgent {
		t.Fatalf("cross-domain workstream error = %v, code %q", err, ErrorCode(err))
	}
	if _, err := storage.UpdateDomainAgent(ctx, UpdateDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: firstProject.ID, AgentIdentifier: root.Value.ID,
		ParentAgentIdentifier: stringPointer(child.Value.ID), ExpectedRevision: 1,
		IdempotencyKey: "m22-cycle", CorrelationID: "m22-cycle",
	}); ErrorCode(err) != CodeDomainAgentCycle {
		t.Fatalf("cycle error = %v, code %q", err, ErrorCode(err))
	}
	preferred := true
	if _, err := storage.UpdateDomainAgent(ctx, UpdateDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: firstProject.ID, AgentIdentifier: child.Value.ID,
		PreferredEntry: &preferred, ExpectedRevision: 1,
		IdempotencyKey: "m22-second-preferred", CorrelationID: "m22-second-preferred",
	}); ErrorCode(err) != CodeInvalidDomainAgent {
		t.Fatalf("second preferred entry error = %v, code %q", err, ErrorCode(err))
	}
	retired := domain.DomainAgentRetired
	if _, err := storage.UpdateDomainAgent(ctx, UpdateDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: firstProject.ID, AgentIdentifier: root.Value.ID,
		Status: &retired, ExpectedRevision: 1,
		IdempotencyKey: "m22-retire-parent", CorrelationID: "m22-retire-parent",
	}); ErrorCode(err) != CodeInvalidDomainAgent {
		t.Fatalf("retire parent error = %v, code %q", err, ErrorCode(err))
	}
	eventsAfter := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("rejected hierarchy changes appended events: before=%d after=%d", len(eventsBefore), len(eventsAfter))
	}
	tree, err := storage.DomainAgentTree(ctx, workspace.ID, firstProject.ID)
	if err != nil || len(tree.Agents) != 2 || tree.Agents[0].Membership.ParentAgentID != "" || tree.Agents[1].Membership.ParentAgentID != root.Value.ID {
		t.Fatalf("tree after rejected mutations = %#v, %v", tree, err)
	}
}

func createDomainTestAgent(t *testing.T, storage *Store, workspaceID, name, role string) MutationResult[domain.AgentDefinition] {
	t.Helper()
	result, err := storage.CreateAgent(context.Background(), CreateAgentCommand{
		WorkspaceIdentifier: workspaceID, Name: name, Role: role, Provider: "codex-subscription", Runtime: "herdr",
		IdempotencyKey: "m22-agent-" + name, CorrelationID: "m22-agent-" + name,
	})
	if err != nil {
		t.Fatalf("CreateAgent(%s) = %v", name, err)
	}
	return result
}

func stringPointer(value string) *string { return &value }
