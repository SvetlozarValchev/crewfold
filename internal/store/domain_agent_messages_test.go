package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
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
			OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
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
		sent.Value.Message.TaskID != "" || sent.Value.Recipient.Status != domain.DeliveryQueued || sent.Value.Recipient.WakeStatus != domain.WakePending {
		t.Fatalf("durable message = %#v", sent)
	}
	replayed, err := storage.SendMessage(ctx, command)
	if err != nil || replayed.Value.Message.ID != sent.Value.Message.ID || replayed.EventSequence != sent.EventSequence {
		t.Fatalf("durable message replay = %#v, %v; want %#v", replayed, err, sent)
	}
	wake, found, err := storage.ClaimMessageWakeJob(ctx, time.Minute)
	if err != nil || !found || wake.TargetRunID != "" || wake.TargetDomainThreadID != "recipient-private-thread" || wake.RecipientAgentID != recipient.Value.ID {
		t.Fatalf("durable session wake = %#v, %t, %v", wake, found, err)
	}
	if err := storage.SettleMessageWakeJob(ctx, wake.ID, domain.WakeSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	queued, err := storage.DomainAgentInbox(ctx, workspace.ID, project.ID, recipient.Value.ID, 20)
	if err != nil || len(queued) != 1 || queued[0].Delivery.Status != domain.DeliveryDelivered {
		t.Fatalf("woken domain inbox = %#v, %v", queued, err)
	}
	delivered, err := storage.DeliverDomainAgentSessionInbox(ctx, "recipient-private-thread", 20)
	if err != nil || len(delivered) != 1 || delivered[0].Delivery.Status != domain.DeliveryDelivered || delivered[0].Delivery.DeliveredRunID != "" {
		t.Fatalf("delivered domain inbox = %#v, %v", delivered, err)
	}
	eventsAfterDelivery := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	if len(eventsAfterDelivery) != len(eventsBefore)+4 {
		t.Fatalf("message send+delivery events = %d -> %d", len(eventsBefore), len(eventsAfterDelivery))
	}
	deliveryEvent := eventsAfterDelivery[len(eventsAfterDelivery)-1]
	if deliveryEvent.Type != messageDeliveredEvent || deliveryEvent.Actor.ActorType != domain.EventActorSubsystem || deliveryEvent.Actor.ActorID != messageWakeActorID {
		t.Fatalf("durable delivery event = %#v", deliveryEvent)
	}
	secondDelivery, err := storage.DeliverDomainAgentSessionInbox(ctx, "recipient-private-thread", 20)
	if err != nil || len(secondDelivery) != 1 || len(testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)) != len(eventsAfterDelivery) {
		t.Fatalf("domain inbox delivery replay = %#v, %v", secondDelivery, err)
	}
	acknowledged, err := storage.AcknowledgeDomainAgentSessionMessage(ctx, "recipient-private-thread", sent.Value.Message.ID, "recipient-domain-ack")
	if err != nil || acknowledged.Value.Delivery.Status != domain.DeliveryAcknowledged || acknowledged.Value.Delivery.AcknowledgedAt == "" {
		t.Fatalf("AcknowledgeDomainAgentSessionMessage() = %#v, %v", acknowledged, err)
	}
	ackReplay, err := storage.AcknowledgeDomainAgentSessionMessage(ctx, "recipient-private-thread", sent.Value.Message.ID, "recipient-domain-ack")
	if err != nil || ackReplay.Value.Delivery.Status != domain.DeliveryAcknowledged || ackReplay.EventSequence != acknowledged.EventSequence {
		t.Fatalf("AcknowledgeDomainAgentSessionMessage(replay) = %#v, %v; want %#v", ackReplay, err, acknowledged)
	}
	eventsAfterAcknowledgement := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	if len(eventsAfterAcknowledgement) != len(eventsAfterDelivery)+1 {
		t.Fatalf("message acknowledgement event count = %d -> %d", len(eventsAfterDelivery), len(eventsAfterAcknowledgement))
	}
	self := command
	self.RecipientAgent = sender.Value.ID
	self.IdempotencyKey, self.CorrelationID = "durable-message-self", "durable-message-self"
	if _, err := storage.SendMessage(ctx, self); ErrorCode(err) != CodeMessageDenied {
		t.Fatalf("self-message error = %v, code %q", err, ErrorCode(err))
	}
	if len(testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)) != len(eventsAfterAcknowledgement) {
		t.Fatal("denied durable self-message appended an event")
	}
}

func TestM22DurableAgentReplyContinuesTaskScopedDirectThreadWithoutImpersonatingTheRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	sender := createDomainTestAgent(t, storage, workspace.ID, "critic", "task sender")
	recipient := createDomainTestAgent(t, storage, workspace.ID, "vision", "durable recipient")
	for index, agent := range []MutationResult[domain.AgentDefinition]{sender, recipient} {
		if _, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
			WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
			OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
			PreferredEntry: index == 0, IdempotencyKey: "attach-task-thread-agent-" + agent.Value.ID,
			CorrelationID: "attach-task-thread-agent-" + agent.Value.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for name, agent := range map[string]MutationResult[domain.AgentDefinition]{"critic-thread": sender, "vision-thread": recipient} {
		if _, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
			WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
			Provider: "codex", ThreadID: name, CWD: "/work/" + agent.Value.Name,
		}); err != nil {
			t.Fatal(err)
		}
	}
	task := createAssignedMessageTestTask(t, storage, workspace.ID, project.ID, sender.Value.ID, "challenge the north star", "task-thread-critic")
	runResult, err := storage.CreateRun(ctx, CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, Runtime: "herdr", Provider: "codex-subscription",
		Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "task-thread", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting for evidence"}}},
		ExpectedTaskRevision: task.Task.Revision, IdempotencyKey: "task-thread-run", CorrelationID: "task-thread-run",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	run := runResult.Detail
	starting, err := storage.MarkRunStarting(ctx, run.Run.ID, "task-thread-starting")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkRunStarted(ctx, starting.ID, "task-thread-runtime", "task-thread-provider", "task-thread-started"); err != nil {
		t.Fatal(err)
	}
	request, err := storage.SendMessage(ctx, SendMessageCommand{
		WorkspaceIdentifier: workspace.ID, SenderRunID: run.Run.ID, RecipientAgent: recipient.Value.ID,
		Kind: domain.MessageReviewRequest, Subject: "Need north-star evidence", Body: "Send the exact synthesis artifacts.",
		IdempotencyKey: "task-thread-request", CorrelationID: "task-thread-request",
	})
	if err != nil || request.Value.Thread.TaskID != task.Task.ID || request.Value.Message.TaskID != task.Task.ID {
		t.Fatalf("SendMessage(task request) = %#v, %v", request, err)
	}
	if _, err := storage.DeliverDomainAgentSessionInbox(ctx, "vision-thread", 20); err != nil {
		t.Fatal(err)
	}
	reply, err := storage.SendMessage(ctx, SendMessageCommand{
		WorkspaceIdentifier: workspace.ID, SenderDomainThreadID: "vision-thread", RecipientAgent: sender.Value.ID,
		ThreadID: request.Value.Thread.ID, ReplyToMessageID: request.Value.Message.ID,
		Kind: domain.MessageInform, Body: "The synthesis artifacts are available in the recorded handoff.",
		IdempotencyKey: "task-thread-reply", CorrelationID: "task-thread-reply",
	})
	if err != nil {
		t.Fatalf("SendMessage(durable reply) error = %v", err)
	}
	if reply.Value.Thread.TaskID != task.Task.ID || reply.Value.Message.TaskID != "" || reply.Value.Message.SenderType != "durable_agent" {
		t.Fatalf("durable task-thread reply = %#v", reply)
	}
	recipientInbox, err := storage.DomainAgentInbox(ctx, workspace.ID, project.ID, recipient.Value.ID, 20)
	if err != nil || len(recipientInbox) != 1 || recipientInbox[0].Delivery.Status != domain.DeliveryAcknowledged {
		t.Fatalf("recipient inbox after reply = %#v, %v", recipientInbox, err)
	}
	detail, err := storage.Thread(ctx, workspace.ID, request.Value.Thread.ID)
	if err != nil || len(detail.Messages) != 2 || detail.Messages[1].ReplyToMessageID != request.Value.Message.ID {
		t.Fatalf("task-scoped durable conversation = %#v, %v", detail, err)
	}
}
