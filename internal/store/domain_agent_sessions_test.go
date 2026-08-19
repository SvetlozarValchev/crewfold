package store

import (
	"context"
	"encoding/json"
	"testing"

	"crewfold/internal/domain"
)

func TestM22DomainAgentSessionBindingIsPrivateCurrentNodeAndNameNeutral(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	agent := createDomainTestAgent(t, storage, workspace.ID, "orchid", "arbitrary coordinator")
	if _, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		PreferredEntry: true, IdempotencyKey: "attach-orchid-session", CorrelationID: "attach-orchid-session",
	}); err != nil {
		t.Fatal(err)
	}
	eventsBefore := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	command := BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		Provider: "codex", ThreadID: "019-private-thread", CWD: "/work/orchid",
	}
	bound, err := storage.BindDomainAgentSession(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if bound.State != domain.DomainAgentSessionReady || !bound.HasConversation || bound.Provider != "codex" || bound.ThreadID != "019-private-thread" || bound.NodeID == "" {
		t.Fatalf("public binding = %#v", bound)
	}
	replayed, err := storage.BindDomainAgentSession(ctx, command)
	if err != nil || replayed != bound {
		t.Fatalf("BindDomainAgentSession(replay) = %#v, %v; want %#v", replayed, err, bound)
	}
	private, err := storage.DomainAgentSession(ctx, workspace.Name, project.Name, agent.Value.Name)
	if err != nil {
		t.Fatal(err)
	}
	if private.ThreadID != "019-private-thread" || private.NodeID != storage.runtimeNodeID || private.State != domain.DomainAgentSessionReady {
		t.Fatalf("private session = %#v", private)
	}
	scope, err := storage.DomainAgentSessionScopeByThread(ctx, "019-private-thread")
	if err != nil {
		t.Fatal(err)
	}
	if scope.Workspace.ID != workspace.ID || scope.Project.ID != project.ID || scope.Agent.ID != agent.Value.ID || scope.Membership.AgentID != agent.Value.ID || scope.Session.ThreadID != "019-private-thread" {
		t.Fatalf("provider thread scope = %#v", scope)
	}
	if _, err := storage.DomainAgentSessionScopeByThread(ctx, "unknown-private-thread"); ErrorCode(err) != CodeDomainAgentSessionNotFound {
		t.Fatalf("unknown provider thread error = %v, code %q", err, ErrorCode(err))
	}
	toolCommand := DomainAgentToolReceiptCommand{
		ThreadID: "019-private-thread", CallID: "call-context-1", TurnID: "turn-one",
		ToolName: "crewfold_get_domain_context", Arguments: json.RawMessage(`{}`),
	}
	receipt, err := storage.RecordDomainAgentToolReceipt(ctx, toolCommand, map[string]any{
		"success": true, "contentItems": []map[string]string{{"type": "inputText", "text": "exact context"}},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "succeeded" || receipt.ToolName != "crewfold_get_domain_context" || receipt.CallID != "call-context-1" || len(receipt.ResponseSHA256) != 64 {
		t.Fatalf("tool receipt = %#v", receipt)
	}
	replayedReceipt, found, err := storage.ReplayDomainAgentToolReceipt(ctx, toolCommand)
	if err != nil || !found || replayedReceipt.ID != receipt.ID || string(replayedReceipt.Response) != string(receipt.Response) {
		t.Fatalf("tool receipt replay = %#v, %t, %v; want %#v", replayedReceipt, found, err, receipt)
	}
	firstWins, err := storage.RecordDomainAgentToolReceipt(ctx, toolCommand, map[string]any{
		"success": true, "contentItems": []map[string]string{{"type": "inputText", "text": "different late result"}},
	}, true)
	if err != nil || firstWins.ID != receipt.ID || string(firstWins.Response) != string(receipt.Response) {
		t.Fatalf("tool receipt first-write replay = %#v, %v", firstWins, err)
	}
	conflictCommand := toolCommand
	conflictCommand.Arguments = json.RawMessage(`{"unexpected":true}`)
	if _, _, err := storage.ReplayDomainAgentToolReceipt(ctx, conflictCommand); ErrorCode(err) != CodeDomainAgentToolConflict {
		t.Fatalf("divergent tool receipt replay error = %v, code %q", err, ErrorCode(err))
	}
	publicReceipt, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if jsonContainsAnyKey(publicReceipt, "call_id", "turn_id", "request_sha256") {
		t.Fatalf("public tool receipt leaked private provider ids: %s", publicReceipt)
	}
	if _, err := storage.db.ExecContext(ctx, "UPDATE domain_agent_tool_receipts SET status='failed' WHERE id=?", receipt.ID); err == nil {
		t.Fatal("immutable tool receipt accepted an update")
	}
	encoded, err := json.Marshal(private)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || jsonContainsAnyKey(encoded, "thread_id", "node_id", "node_fingerprint") {
		t.Fatalf("public session leaked private binding: %s", encoded)
	}
	if eventsAfter := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000); len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("operational session binding appended a domain event: %d -> %d", len(eventsBefore), len(eventsAfter))
	}
	if _, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		Provider: "codex", ThreadID: "different-thread", CWD: "/work/orchid",
	}); ErrorCode(err) != CodeDomainAgentSessionConflict {
		t.Fatalf("replacement error = %v, code %q", err, ErrorCode(err))
	}
}

func TestM22DomainAgentSessionForeignNodeIsDetachedAndUnboundIsHonest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	first := openTestStore(t, dataDir, Options{
		RuntimeNodeID:          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RuntimeNodeFingerprint: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	workspace, project := initializeWorkTestProject(t, first)
	agent := createDomainTestAgent(t, first, workspace.ID, "plain-name", "plain-role")
	if _, err := first.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		IdempotencyKey: "attach-plain", CorrelationID: "attach-plain",
	}); err != nil {
		t.Fatal(err)
	}
	unbound, err := first.DomainAgentSession(ctx, workspace.ID, project.ID, agent.Value.ID)
	if err != nil || unbound.State != domain.DomainAgentSessionUnbound || unbound.HasConversation {
		t.Fatalf("unbound session = %#v, %v", unbound, err)
	}
	if _, err := first.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		Provider: "codex", ThreadID: "source-thread", CWD: "/work/source",
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, dataDir, Options{
		RuntimeNodeID:          "cccccccccccccccccccccccccccccccc",
		RuntimeNodeFingerprint: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	detached, err := second.DomainAgentSession(ctx, workspace.ID, project.ID, agent.Value.ID)
	if err != nil || detached.State != domain.DomainAgentSessionDetached || !detached.HasConversation || detached.ThreadID != "source-thread" {
		t.Fatalf("foreign session = %#v, %v", detached, err)
	}
	if _, err := second.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		Provider: "codex", ThreadID: "replacement", CWD: "/work/source",
	}); ErrorCode(err) != CodeDomainAgentSessionConflict {
		t.Fatalf("foreign replacement error = %v, code %q", err, ErrorCode(err))
	}
	if _, err := second.DomainAgentSessionScopeByThread(ctx, "source-thread"); ErrorCode(err) != CodeDomainAgentSessionDetached {
		t.Fatalf("foreign provider tool scope error = %v, code %q", err, ErrorCode(err))
	}
}

func TestM22UnavailableProviderSessionReplacementIsExactAndEventFree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	agent := createDomainTestAgent(t, storage, workspace.ID, "orchid", "arbitrary coordinator")
	if _, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		PreferredEntry: true, IdempotencyKey: "attach-orchid-replacement", CorrelationID: "attach-orchid-replacement",
	}); err != nil {
		t.Fatal(err)
	}
	bound, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		Provider: "codex", ThreadID: "missing-provider-thread", CWD: "/work/orchid",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	if _, err := storage.ReplaceDomainAgentSession(ctx, ReplaceDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		ExpectedThreadID: "wrong-expected-thread", Provider: "codex", ThreadID: "replacement-provider-thread", CWD: "/work/orchid",
	}); ErrorCode(err) != CodeDomainAgentSessionConflict {
		t.Fatalf("stale replacement error = %v, code %q", err, ErrorCode(err))
	}
	unchanged, err := storage.DomainAgentSession(ctx, workspace.ID, project.ID, agent.Value.ID)
	if err != nil || unchanged.ThreadID != bound.ThreadID || unchanged.Revision != bound.Revision {
		t.Fatalf("binding after stale replacement = %#v, %v; want %#v", unchanged, err, bound)
	}
	replaced, err := storage.ReplaceDomainAgentSession(ctx, ReplaceDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		ExpectedThreadID: bound.ThreadID, Provider: "codex", ThreadID: "replacement-provider-thread", CWD: "/work/orchid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.ThreadID != "replacement-provider-thread" || replaced.Revision != bound.Revision+1 || replaced.AgentID != bound.AgentID || replaced.ProjectID != bound.ProjectID {
		t.Fatalf("replacement binding = %#v; prior = %#v", replaced, bound)
	}
	if _, err := storage.DomainAgentSessionScopeByThread(ctx, bound.ThreadID); ErrorCode(err) != CodeDomainAgentSessionDetached {
		t.Fatalf("old provider thread scope error = %v, code %q", err, ErrorCode(err))
	}
	if scope, err := storage.DomainAgentSessionScopeByThread(ctx, replaced.ThreadID); err != nil || scope.Agent.ID != agent.Value.ID {
		t.Fatalf("replacement provider scope = %#v, %v", scope, err)
	}
	epochs, err := storage.DomainAgentSessionEpochs(ctx, project.ID, agent.Value.ID)
	if err != nil || len(epochs) != 2 || epochs[0].Epoch != 2 || epochs[0].Status != "current" ||
		epochs[1].Epoch != 1 || epochs[1].Status != "archived" || epochs[1].RotationReason != "continuity_unavailable" {
		t.Fatalf("session epochs = %#v, %v", epochs, err)
	}
	if eventsAfter := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000); len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("operational provider replacement appended a domain event: %d -> %d", len(eventsBefore), len(eventsAfter))
	}
}

func jsonContainsAnyKey(encoded []byte, keys ...string) bool {
	var object map[string]any
	if json.Unmarshal(encoded, &object) != nil {
		return true
	}
	for _, key := range keys {
		if _, ok := object[key]; ok {
			return true
		}
	}
	return false
}
