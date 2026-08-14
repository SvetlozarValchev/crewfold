package daemon

import (
	"context"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestM19RealDaemonInboxBindsCanonicalDeliveryForAgentIDOrName(t *testing.T) {
	config := testConfig(t)
	config.DisableRunWorker = true
	config.DisableCheckWorker = true
	config.DisableCheckWatcher = true
	config.DisableClaimWatcher = true
	config.DisableSupervisor = true
	config.DisableLeaseReconciler = true
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	workspace, err := client.WorkspaceInit(context.Background(), "personal", "inbox-scope-workspace")
	if err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{
		Workspace: workspace.Workspace.ID, Name: "inbox-reader", Role: "reader",
		Provider: "fake", Runtime: "fake", MaxConcurrency: 1, IdempotencyKey: "inbox-scope-agent",
	})
	if err != nil {
		t.Fatalf("AgentCreate() error = %v", err)
	}
	sent, err := client.MessageSend(context.Background(), localapi.MessageSendParams{
		Workspace: workspace.Workspace.ID, RecipientAgent: agent.Agent.ID,
		Kind: domain.MessageInform, Body: "exact canonical delivery",
		ArtifactIDs: []string{}, IdempotencyKey: "inbox-scope-message",
	})
	if err != nil {
		t.Fatalf("MessageSend() error = %v", err)
	}
	for _, identifier := range []string{agent.Agent.ID, agent.Agent.Name} {
		inbox, readErr := client.InboxList(context.Background(), workspace.Workspace.ID, identifier, 20)
		if readErr != nil {
			t.Fatalf("InboxList(agent=%q) error = %v", identifier, readErr)
		}
		if inbox.Agent != agent.Agent.ID || len(inbox.Items) != 1 || inbox.Items[0].Message.ID != sent.Mutation.Message.ID ||
			inbox.Items[0].Message.WorkspaceID != workspace.Workspace.ID || inbox.Items[0].Delivery.MessageID != sent.Mutation.Message.ID ||
			inbox.Items[0].Delivery.RecipientAgentID != agent.Agent.ID || inbox.Items[0].Delivery.RecipientName != agent.Agent.Name {
			t.Fatalf("InboxList(agent=%q) = %#v", identifier, inbox)
		}
	}

	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
