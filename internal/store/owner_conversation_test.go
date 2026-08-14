package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"crewfold/internal/domain"
)

func TestM21OwnerQueryAndPlanAreDurableExactAndEffectFree(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{Name: "personal", IdempotencyKey: "owner-conversation-workspace", CorrelationID: "owner-conversation-workspace"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := storage.CreateAgent(context.Background(), CreateAgentCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, Name: "builder", Role: "implementation", Provider: "fixture-mcp", Runtime: "direct", MaxConcurrency: 1,
		IdempotencyKey: "owner-conversation-agent", CorrelationID: "owner-conversation-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, Name: "world-engine", WriteMode: domain.WriteModeShared,
		IdempotencyKey: "owner-conversation-project", CorrelationID: "owner-conversation-project",
		Observation: sourceTestObservation(filepath.Join(t.TempDir(), "world-engine"), "main"),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := testWorkspaceEvents(t, storage, workspace.Workspace.ID, 0, 100)
	queryCommand := PrepareOwnerTurnCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, ProjectIdentifier: project.Project.ID,
		Instruction: "What is happening?", Kind: "query", IdempotencyKey: "owner-query-one",
	}
	query, err := storage.PrepareOwnerTurn(context.Background(), queryCommand)
	if err != nil {
		t.Fatal(err)
	}
	if query.Turn.Status != "completed" || query.Turn.Answer == "" || len(query.Operations) != 0 || query.Turn.AsOfEventSequence != before[len(before)-1].Sequence {
		t.Fatalf("query turn = %#v", query)
	}
	if after := testWorkspaceEvents(t, storage, workspace.Workspace.ID, 0, 100); !reflect.DeepEqual(after, before) {
		t.Fatalf("query-only owner turn changed the domain journal:\nbefore=%#v\nafter=%#v", before, after)
	}
	replay, err := storage.PrepareOwnerTurn(context.Background(), queryCommand)
	if err != nil || !reflect.DeepEqual(replay, query) {
		t.Fatalf("query replay = %#v, %v; want %#v", replay, err, query)
	}
	queryCommand.Instruction = "Something else"
	if _, err := storage.PrepareOwnerTurn(context.Background(), queryCommand); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("changed owner query error = %v", err)
	}

	plan, err := storage.PrepareOwnerTurn(context.Background(), PrepareOwnerTurnCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, ProjectIdentifier: project.Project.ID, ConversationID: query.Conversation.ID,
		Instruction: "Build the first playable loop", Kind: "plan", IdempotencyKey: "owner-plan-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Turn.Status != "planned" || len(plan.Operations) != 4 || len(plan.Receipts) != 0 {
		t.Fatalf("plan turn = %#v", plan)
	}
	for index, operation := range plan.Operations {
		if operation.Ordinal != int64(index+1) || operation.Status != "pending" || operation.PolicyResult != "allowed" || len(operation.PayloadSHA256) != 64 {
			t.Fatalf("plan operation %d = %#v", index, operation)
		}
	}
	planEvents := testWorkspaceEvents(t, storage, workspace.Workspace.ID, 0, 100)
	edited, err := storage.EditOwnerPlan(context.Background(), EditOwnerPlanCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, TurnID: plan.Turn.ID, ExpectedRevision: plan.Turn.Revision,
		Title: "Playable world loop", Description: "Build a deterministic first loop", Priority: 700,
		Budget: domain.Budget{TokenLimit: 12000, CostCents: 0, TimeSeconds: 1800}, AgentIdentifier: agent.Value.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Turn.Revision != plan.Turn.Revision+1 || edited.Operations[1].Payload["title"] != "Playable world loop" || edited.Operations[2].Payload["agent_id"] != agent.Value.ID {
		t.Fatalf("edited owner plan = %#v", edited)
	}
	if afterEdit := testWorkspaceEvents(t, storage, workspace.Workspace.ID, 0, 100); !reflect.DeepEqual(afterEdit, planEvents) {
		t.Fatalf("owner plan edit changed the domain journal:\nbefore=%#v\nafter=%#v", planEvents, afterEdit)
	}
	if _, err := storage.EditOwnerPlan(context.Background(), EditOwnerPlanCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, TurnID: plan.Turn.ID, ExpectedRevision: edited.Turn.Revision,
		Title: "Paid plan", Priority: 1, Budget: domain.Budget{CostCents: 1}, AgentIdentifier: agent.Value.ID,
	}); ErrorCode(err) != CodeInvalidOwnerConversation {
		t.Fatalf("paid owner plan edit error = %v", err)
	}
	if _, err := storage.EditOwnerPlan(context.Background(), EditOwnerPlanCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, TurnID: plan.Turn.ID, ExpectedRevision: plan.Turn.Revision,
		Title: "Stale edit", Priority: 1, AgentIdentifier: agent.Value.ID,
	}); ErrorCode(err) != CodeOwnerTurnConflict {
		t.Fatalf("stale owner plan edit error = %v", err)
	}
	plan = edited
	if _, err := storage.CreateTask(context.Background(), CreateTaskCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, ProjectIdentifier: project.Project.ID,
		Title: "A canonical change after plan review", Priority: 100,
		IdempotencyKey: "owner-plan-stale-task", CorrelationID: "owner-plan-stale-task",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.StartOwnerTurnExecution(context.Background(), workspace.Workspace.ID, plan.Turn.ID); ErrorCode(err) != CodeOwnerTurnConflict {
		t.Fatalf("stale owner plan execution error = %v, want %q", err, CodeOwnerTurnConflict)
	}
	stillPlanned, err := storage.OwnerTurnDetail(context.Background(), workspace.Workspace.ID, plan.Turn.ID)
	if err != nil || stillPlanned.Turn.Status != "planned" || len(stillPlanned.Receipts) != 0 {
		t.Fatalf("stale owner plan changed = %#v, %v", stillPlanned, err)
	}
	gatedBefore := testWorkspaceEvents(t, storage, workspace.Workspace.ID, 0, 100)
	gated, err := storage.PrepareOwnerTurn(context.Background(), PrepareOwnerTurnCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, ProjectIdentifier: project.Project.ID, ConversationID: query.Conversation.ID,
		Instruction: "Delete the repository and push a release", Kind: "act", IdempotencyKey: "owner-gated-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gated.Turn.Status != "awaiting_approval" || gated.Turn.Answer == "" || len(gated.Operations) != 4 || len(gated.Receipts) != 0 {
		t.Fatalf("gated turn = %#v", gated)
	}
	for index, operation := range gated.Operations {
		if operation.PolicyResult != "gated" || operation.Status != "awaiting_approval" {
			t.Fatalf("gated operation %d = %#v", index, operation)
		}
	}
	if gatedAfter := testWorkspaceEvents(t, storage, workspace.Workspace.ID, 0, 100); !reflect.DeepEqual(gatedAfter, gatedBefore) {
		t.Fatalf("gated owner turn changed the domain journal:\nbefore=%#v\nafter=%#v", gatedBefore, gatedAfter)
	}
	page, err := storage.ListOwnerConversation(context.Background(), workspace.Workspace.ID, project.Project.ID, query.Conversation.ID)
	if err != nil || len(page.Conversations) != 1 || len(page.Turns) != 3 {
		t.Fatalf("owner conversation page = %#v, %v", page, err)
	}
	report, err := storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
	if err != nil || report.Status != "ok" {
		t.Fatalf("VerifyCanonical() = %#v, %v", report, err)
	}
}
