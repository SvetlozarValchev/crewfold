package store

import (
	"context"
	"testing"

	"crewfold/internal/domain"
)

func TestM22DurableAgentMessagesAreSameDomainScopedDeliveredAndReplaySafe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	sender := createDomainTestAgent(t, storage, workspace.ID, "orchid", "arbitrary sender")
	recipient := createDomainTestAgent(t, storage, workspace.ID, "fern", "arbitrary recipient")
	for index, agent := range []MutationResult[domain.AgentDefinition]{sender, recipient} {
		if _, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
			WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
			PreferredEntry: index == 0, IdempotencyKey: "attach-message-agent-" + agent.Value.ID,
			CorrelationID: "attach-message-agent-" + agent.Value.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: sender.Value.ID,
		Provider: "codex", ThreadID: "sender-private-thread", CWD: "/work/sender",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: recipient.Value.ID,
		Provider: "codex", ThreadID: "recipient-private-thread", CWD: "/work/recipient",
	}); err != nil {
		t.Fatal(err)
	}

	command := SendMessageCommand{
		WorkspaceIdentifier: workspace.ID, SenderDomainThreadID: "sender-private-thread",
		RecipientAgent: recipient.Value.ID, Kind: domain.MessageQuestion, Subject: "Shared boundary",
		Body: "Which format is frozen?", IdempotencyKey: "durable-message-one", CorrelationID: "durable-message-one",
	}
	eventsBefore := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	sent, err := storage.SendMessage(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Value.Message.SenderType != "durable_agent" || sent.Value.Message.SenderID != sender.Value.ID ||
		sent.Value.Message.SenderAgentID != sender.Value.ID || sent.Value.Message.ProjectID != project.ID ||
		sent.Value.Message.TaskID != "" || sent.Value.Recipient.Status != domain.DeliveryQueued {
		t.Fatalf("durable message = %#v", sent)
	}
	replayed, err := storage.SendMessage(ctx, command)
	if err != nil || replayed.Value.Message.ID != sent.Value.Message.ID || replayed.EventSequence != sent.EventSequence {
		t.Fatalf("durable message replay = %#v, %v; want %#v", replayed, err, sent)
	}
	queued, err := storage.DomainAgentInbox(ctx, workspace.ID, project.ID, recipient.Value.ID, 20)
	if err != nil || len(queued) != 1 || queued[0].Delivery.Status != domain.DeliveryQueued {
		t.Fatalf("queued domain inbox = %#v, %v", queued, err)
	}
	delivered, err := storage.DeliverDomainAgentSessionInbox(ctx, "recipient-private-thread", 20)
	if err != nil || len(delivered) != 1 || delivered[0].Delivery.Status != domain.DeliveryDelivered || delivered[0].Delivery.DeliveredRunID != "" {
		t.Fatalf("delivered domain inbox = %#v, %v", delivered, err)
	}
	eventsAfterDelivery := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	if len(eventsAfterDelivery) != len(eventsBefore)+3 {
		t.Fatalf("message send+delivery events = %d -> %d", len(eventsBefore), len(eventsAfterDelivery))
	}
	deliveryEvent := eventsAfterDelivery[len(eventsAfterDelivery)-1]
	if deliveryEvent.Type != messageDeliveredEvent || deliveryEvent.Actor.ActorType != domain.EventActorIntegration || deliveryEvent.Actor.ActorID != recipient.Value.ID {
		t.Fatalf("durable delivery event = %#v", deliveryEvent)
	}
	secondDelivery, err := storage.DeliverDomainAgentSessionInbox(ctx, "recipient-private-thread", 20)
	if err != nil || len(secondDelivery) != 1 || len(testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)) != len(eventsAfterDelivery) {
		t.Fatalf("domain inbox delivery replay = %#v, %v", secondDelivery, err)
	}
	self := command
	self.RecipientAgent = sender.Value.ID
	self.IdempotencyKey, self.CorrelationID = "durable-message-self", "durable-message-self"
	if _, err := storage.SendMessage(ctx, self); ErrorCode(err) != CodeMessageDenied {
		t.Fatalf("self-message error = %v, code %q", err, ErrorCode(err))
	}
	if len(testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)) != len(eventsAfterDelivery) {
		t.Fatal("denied durable self-message appended an event")
	}
}
