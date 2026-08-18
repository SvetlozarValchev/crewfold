package daemon

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

const daemonTestDomainCharter = "Own the described responsibility, communicate material boundaries, delegate when useful, and escalate missing authority to the owner."

func TestM22DomainAgentTreeCrossesStrictLocalAPIWithoutRoleAuthority(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	root := t.TempDir()
	createGitFixture(t, root)
	config := testConfig(t)
	startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	ctx := context.Background()
	workspace, err := client.WorkspaceInit(ctx, "personal", "m22-domain-workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.ProjectAdd(ctx, workspace.Workspace.ID, "world-engine", filepath.Join(root, "world-engine"), domain.WriteModeExclusive, "m22-domain-project")
	if err != nil {
		t.Fatal(err)
	}
	workstream, err := client.ObjectiveCreate(ctx, localapi.ObjectiveCreateParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Title: "Terrain consolidation", IdempotencyKey: "m22-domain-workstream",
	})
	if err != nil {
		t.Fatal(err)
	}
	rootAgent, err := client.AgentCreate(ctx, localapi.AgentCreateParams{
		Workspace: workspace.Workspace.Name, Name: "terrain-lead", Role: "owner-defined-coordinator",
		Provider: "codex-subscription", Runtime: "herdr", IdempotencyKey: "m22-domain-root-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DomainAgentAttach(ctx, localapi.DomainAgentAttachParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: rootAgent.Agent.Name,
		OperatingCharter: daemonTestDomainCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		PreferredEntry: true, IdempotencyKey: "m22-domain-attach-root",
	}); err != nil {
		t.Fatal(err)
	}
	childAgent, err := client.DomainAgentCreate(ctx, localapi.DomainAgentCreateParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Name: "terrain-reviewer", Role: "independent-review",
		Provider: "codex-subscription", Runtime: "herdr", MaxConcurrency: 1, ParentAgent: rootAgent.Agent.Name,
		OperatingCharter: daemonTestDomainCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		Workstream: workstream.Objective.ID, IdempotencyKey: "m22-domain-create-child",
	})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := client.DomainAgentTree(ctx, workspace.Workspace.Name, project.Project.Name)
	if err != nil {
		t.Fatal(err)
	}
	if tree.ProjectID != project.Project.ID || len(tree.Agents) != 2 ||
		tree.Agents[0].Definition.ID != rootAgent.Agent.ID || tree.Agents[1].Definition.ID != childAgent.Agent.Definition.ID || tree.Agents[1].Membership.ParentAgentID != rootAgent.Agent.ID {
		t.Fatalf("DomainAgentTree() = %#v", tree)
	}
}
