package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestDurableMailboxSupportsOfflineDeliveryReplyAcknowledgementAndFailedWake(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	sender := createMessageTestAgent(t, storage, workspace.ID, "sender")
	reviewer := createMessageTestAgent(t, storage, workspace.ID, "reviewer")
	reviewerCheckout, err := storage.AddCheckout(context.Background(), AddCheckoutCommand{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, WriteMode: domain.WriteModeExclusive, IdempotencyKey: "reviewer-message-checkout", CorrelationID: "reviewer-message-checkout-request", Observation: sourceTestObservation(filepath.Join(t.TempDir(), "reviewer-clone"), "reviewer-message")})
	if err != nil {
		t.Fatalf("AddCheckout(reviewer) error = %v", err)
	}
	senderTask := createAssignedMessageTestTask(t, storage, workspace.ID, project.ID, sender.ID, "sender task", "sender")
	reviewerTask := createAssignedMessageTestTask(t, storage, workspace.ID, project.ID, reviewer.ID, "review task", "reviewer")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "mailbox-store", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting"}}}
	senderRun := createRunTest(t, storage, workspace.ID, senderTask, scenario, "sender-mailbox-run")
	starting, err := storage.MarkRunStarting(context.Background(), senderRun.Run.ID, "sender-starting")
	if err != nil {
		t.Fatalf("MarkRunStarting(sender) error = %v", err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), starting.ID, "sender-runtime", "sender-provider", "sender-started"); err != nil {
		t.Fatalf("MarkRunStarted(sender) error = %v", err)
	}

	command := SendMessageCommand{
		WorkspaceIdentifier: workspace.ID, SenderRunID: senderRun.Run.ID, RecipientAgent: reviewer.Name,
		Kind: domain.MessageQuestion, Subject: "Review the contract", Body: "Please verify the public contract.",
		IdempotencyKey: "sender-question", CorrelationID: "sender-question-request",
	}
	sent, err := storage.SendMessage(context.Background(), command)
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	replayed, err := storage.SendMessage(context.Background(), command)
	if err != nil || replayed.Value.Message.ID != sent.Value.Message.ID {
		t.Fatalf("SendMessage(replay) = %#v, %v; want %s", replayed, err, sent.Value.Message.ID)
	}
	changed := command
	changed.Body = "different body"
	if _, err := storage.SendMessage(context.Background(), changed); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("SendMessage(conflict) error = %v, code = %q", err, ErrorCode(err))
	}
	disabled := false
	if _, err := storage.UpdateAgent(context.Background(), UpdateAgentCommand{WorkspaceIdentifier: workspace.ID, AgentIdentifier: reviewer.ID, Enabled: &disabled, ExpectedRevision: reviewer.Revision, IdempotencyKey: "disable-message-reviewer", CorrelationID: "disable-message-reviewer-request"}); err != nil {
		t.Fatalf("UpdateAgent(disable reviewer) error = %v", err)
	}
	replayedAfterDisable, err := storage.SendMessage(context.Background(), command)
	if err != nil || replayedAfterDisable.Value.Message.ID != sent.Value.Message.ID || replayedAfterDisable.EventSequence != sent.EventSequence {
		t.Fatalf("SendMessage(replay after recipient disable) = %#v, %v; want %#v", replayedAfterDisable, err, sent)
	}
	enabled := true
	if _, err := storage.UpdateAgent(context.Background(), UpdateAgentCommand{WorkspaceIdentifier: workspace.ID, AgentIdentifier: reviewer.ID, Enabled: &enabled, ExpectedRevision: reviewer.Revision + 1, IdempotencyKey: "enable-message-reviewer", CorrelationID: "enable-message-reviewer-request"}); err != nil {
		t.Fatalf("UpdateAgent(enable reviewer) error = %v", err)
	}
	offlineInbox, err := storage.Inbox(context.Background(), workspace.ID, reviewer.ID, 20)
	if err != nil || len(offlineInbox) != 1 || offlineInbox[0].Delivery.Status != domain.DeliveryQueued || offlineInbox[0].Delivery.WakeStatus != domain.WakeNotRequested {
		t.Fatalf("Inbox(offline reviewer) = %#v, %v", offlineInbox, err)
	}

	packet, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: reviewerTask.Task.ID, AgentIdentifier: reviewer.ID,
		CheckoutIdentifier: reviewerCheckout.Checkout.ID, ExpectedTaskRevision: reviewerTask.Task.Revision, IdempotencyKey: "reviewer-context", CorrelationID: "reviewer-context-request",
	})
	if err != nil || packet.Value.Inbox.UnseenCount != 1 || len(packet.Value.Inbox.Items) != 1 || packet.Value.Inbox.Items[0].MessageID != sent.Value.Message.ID {
		t.Fatalf("BuildContextPacket(reviewer inbox) = %#v, %v", packet, err)
	}
	reviewerRun, err := storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: reviewerTask.Task.ID, ContextPacketID: packet.Value.ID,
		Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: reviewerTask.Task.Revision,
		IdempotencyKey: "reviewer-mailbox-run", CorrelationID: "reviewer-mailbox-run-request",
	})
	if err != nil {
		t.Fatalf("CreateRun(reviewer) error = %v", err)
	}
	reviewerStarting, _ := storage.MarkRunStarting(context.Background(), reviewerRun.Detail.Run.ID, "reviewer-starting")
	if _, err := storage.MarkRunStarted(context.Background(), reviewerStarting.ID, "reviewer-runtime", "reviewer-provider", "reviewer-started"); err != nil {
		t.Fatalf("MarkRunStarted(reviewer) error = %v", err)
	}
	delivered, err := storage.RunInbox(context.Background(), reviewerRun.Detail.Run.ID, 20)
	if err != nil || len(delivered) != 1 || delivered[0].Delivery.Status != domain.DeliveryDelivered || delivered[0].Delivery.DeliveredRunID != reviewerRun.Detail.Run.ID {
		t.Fatalf("RunInbox(reviewer) = %#v, %v", delivered, err)
	}
	read, err := storage.ReadRunMessage(context.Background(), reviewerRun.Detail.Run.ID, sent.Value.Message.ID, "reviewer-read")
	if err != nil || read.Value.Delivery.Status != domain.DeliveryRead {
		t.Fatalf("ReadRunMessage() = %#v, %v", read, err)
	}
	acknowledged, err := storage.AcknowledgeRunMessage(context.Background(), reviewerRun.Detail.Run.ID, sent.Value.Message.ID, "reviewer-ack")
	if err != nil || acknowledged.Value.Delivery.Status != domain.DeliveryAcknowledged {
		t.Fatalf("AcknowledgeRunMessage() = %#v, %v", acknowledged, err)
	}
	ackReplay, err := storage.AcknowledgeRunMessage(context.Background(), reviewerRun.Detail.Run.ID, sent.Value.Message.ID, "reviewer-ack")
	if err != nil || ackReplay.Value.Delivery.Status != domain.DeliveryAcknowledged || ackReplay.EventSequence != acknowledged.EventSequence {
		t.Fatalf("AcknowledgeRunMessage(replay) = %#v, %v; want %#v", ackReplay, err, acknowledged)
	}

	reply, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: workspace.ID, SenderRunID: reviewerRun.Detail.Run.ID, RecipientAgent: sender.ID,
		ThreadID: sent.Value.Thread.ID, ReplyToMessageID: sent.Value.Message.ID,
		Kind: domain.MessageInform, Body: "The public contract is consistent.",
		IdempotencyKey: "reviewer-reply", CorrelationID: "reviewer-reply-request",
	})
	if err != nil || reply.Value.Recipient.WakeStatus != domain.WakePending {
		t.Fatalf("SendMessage(reply) = %#v, %v", reply, err)
	}
	wake, found, err := storage.ClaimMessageWakeJob(context.Background(), time.Second)
	if err != nil || !found || wake.MessageID != reply.Value.Message.ID || wake.TargetRunID != senderRun.Run.ID {
		t.Fatalf("ClaimMessageWakeJob() = %#v, %t, %v", wake, found, err)
	}
	if err := storage.SettleMessageWakeJob(context.Background(), wake.ID, domain.WakeFailed, "fixture prompt refused"); err != nil {
		t.Fatalf("SettleMessageWakeJob(failed) error = %v", err)
	}
	senderInbox, err := storage.Inbox(context.Background(), workspace.ID, sender.ID, 20)
	if err != nil || len(senderInbox) != 1 || senderInbox[0].Delivery.Status != domain.DeliveryQueued || senderInbox[0].Delivery.WakeStatus != domain.WakeFailed || senderInbox[0].Delivery.WakeDiagnostic != "fixture prompt refused" {
		t.Fatalf("Inbox(sender after failed wake) = %#v, %v", senderInbox, err)
	}
	if _, err := storage.RunInbox(context.Background(), senderRun.Run.ID, 20); err != nil {
		t.Fatalf("RunInbox(sender) error = %v", err)
	}
	if _, err := storage.AcknowledgeRunMessage(context.Background(), senderRun.Run.ID, reply.Value.Message.ID, "sender-ack"); err != nil {
		t.Fatalf("AcknowledgeRunMessage(sender) error = %v", err)
	}
	thread, err := storage.Thread(context.Background(), workspace.ID, sent.Value.Thread.ID)
	if err != nil || len(thread.Messages) != 2 || len(thread.Recipients) != 2 || thread.Recipients[0].Status != domain.DeliveryAcknowledged || thread.Recipients[1].Status != domain.DeliveryAcknowledged || thread.Recipients[1].WakeStatus != domain.WakeFailed {
		t.Fatalf("Thread() = %#v, %v", thread, err)
	}
}

func TestExpiredMessageWakeIsFailedUnknownAndNeverReissued(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, project := initializeWorkTestProject(t, storage)
	recipient := createMessageTestAgent(t, storage, workspace.ID, "unknown-wake-recipient")
	task := createAssignedMessageTestTask(t, storage, workspace.ID, project.ID, recipient.ID, "unknown wake task", "unknown-wake")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "unknown-wake", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting"}}}
	run := createRunTest(t, storage, workspace.ID, task, scenario, "unknown-wake-run")
	starting, err := storage.MarkRunStarting(context.Background(), run.Run.ID, "unknown-wake-starting")
	if err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), starting.ID, "unknown-runtime", "unknown-provider", "unknown-wake-started"); err != nil {
		t.Fatalf("MarkRunStarted() error = %v", err)
	}

	sent, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: workspace.ID,
		ProjectIdentifier:   project.ID,
		RecipientAgent:      recipient.ID,
		Kind:                domain.MessageInform,
		Body:                "This prompt may already have reached the runtime.",
		IdempotencyKey:      "unknown-wake-message",
		CorrelationID:       "unknown-wake-message-request",
	})
	if err != nil || sent.Value.Recipient.WakeStatus != domain.WakePending {
		t.Fatalf("SendMessage() = %#v, %v", sent, err)
	}
	wake, found, err := storage.ClaimMessageWakeJob(context.Background(), time.Second)
	if err != nil || !found || wake.Attempts != 1 {
		t.Fatalf("ClaimMessageWakeJob() = %#v, %t, %v", wake, found, err)
	}

	// This is the kill-after-effect-before-completion boundary. The worker never
	// durably settled its leased operation, so recovery must report uncertainty
	// rather than issue the external prompt again.
	now = now.Add(2 * time.Second)
	if next, found, err := storage.ClaimMessageWakeJob(context.Background(), time.Second); err != nil || found {
		t.Fatalf("ClaimMessageWakeJob(after expiry) = %#v, %t, %v; want no reissue", next, found, err)
	}
	if next, found, err := storage.ClaimMessageWakeJob(context.Background(), time.Second); err != nil || found {
		t.Fatalf("ClaimMessageWakeJob(repeated recovery) = %#v, %t, %v; want terminal", next, found, err)
	}

	var status, diagnostic string
	var attempts, unknownEvents int
	var leaseExpiresAt any
	if err := storage.db.QueryRow(`SELECT status, attempts, COALESCE(diagnostic, ''), lease_expires_at
FROM message_wake_jobs WHERE id = ?`, wake.ID).Scan(&status, &attempts, &diagnostic, &leaseExpiresAt); err != nil {
		t.Fatalf("query recovered wake error = %v", err)
	}
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = ? AND entity_id = ?`, messageWakeFailedUnknown, sent.Value.Message.ID).Scan(&unknownEvents); err != nil {
		t.Fatalf("count failed-unknown events error = %v", err)
	}
	if status != domain.WakeFailedUnknown || attempts != 1 || diagnostic != expiredMessageWakeDiagnostic || leaseExpiresAt != nil || unknownEvents != 1 {
		t.Fatalf("recovered wake status=%q attempts=%d diagnostic=%q lease=%v events=%d", status, attempts, diagnostic, leaseExpiresAt, unknownEvents)
	}
	if err := storage.SettleMessageWakeJob(context.Background(), wake.ID, domain.WakeSucceeded, "late worker completion"); err != nil {
		t.Fatalf("SettleMessageWakeJob(late) error = %v", err)
	}
	if err := storage.db.QueryRow(`SELECT status FROM message_wake_jobs WHERE id = ?`, wake.ID).Scan(&status); err != nil || status != domain.WakeFailedUnknown {
		t.Fatalf("late completion status = %q, %v; want failed_unknown", status, err)
	}
	inbox, err := storage.Inbox(context.Background(), workspace.ID, recipient.ID, 20)
	if err != nil || len(inbox) != 1 || inbox[0].Delivery.Status != domain.DeliveryQueued || inbox[0].Delivery.WakeStatus != domain.WakeFailedUnknown || inbox[0].Delivery.WakeDiagnostic != expiredMessageWakeDiagnostic {
		t.Fatalf("Inbox(after unknown wake) = %#v, %v", inbox, err)
	}
}

func TestMailboxDeniesUnscopedRecipientsAndOversizedBodies(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	sender := createMessageTestAgent(t, storage, workspace.ID, "bounded-sender")
	task := createAssignedMessageTestTask(t, storage, workspace.ID, project.ID, sender.ID, "bounded sender task", "bounded")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "bounded-mailbox", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress}}}
	run := createRunTest(t, storage, workspace.ID, task, scenario, "bounded-sender-run")
	starting, _ := storage.MarkRunStarting(context.Background(), run.Run.ID, "bounded-starting")
	_, _ = storage.MarkRunStarted(context.Background(), starting.ID, "runtime", "provider", "bounded-started")
	base := SendMessageCommand{WorkspaceIdentifier: workspace.ID, SenderRunID: run.Run.ID, RecipientAgent: "owner", Kind: domain.MessageInform, Body: "not allowed", IdempotencyKey: "denied-recipient", CorrelationID: "denied-recipient-request"}
	if _, err := storage.SendMessage(context.Background(), base); ErrorCode(err) != CodeMessageDenied {
		t.Fatalf("SendMessage(owner recipient) error = %v, code = %q", err, ErrorCode(err))
	}
	base.RecipientAgent = sender.ID
	base.Body = strings.Repeat("x", maximumMessageBytes+1)
	base.IdempotencyKey = "oversized-message"
	if _, err := storage.SendMessage(context.Background(), base); ErrorCode(err) != CodeInvalidMessage {
		t.Fatalf("SendMessage(oversized) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestRunMailboxExcludesMessagesScopedToAnotherProject(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	other, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{
		WorkspaceIdentifier: workspace.ID, Name: "other-project", WriteMode: domain.WriteModeShared,
		IdempotencyKey: "other-message-project", CorrelationID: "other-message-project-request",
		Observation: sourceTestObservation(filepath.Join(t.TempDir(), "other-project"), "other-message-project"),
	})
	if err != nil {
		t.Fatalf("RegisterProject(other) error = %v", err)
	}
	reviewer := createMessageTestAgent(t, storage, workspace.ID, "scoped-reviewer")
	task := createAssignedMessageTestTask(t, storage, workspace.ID, project.ID, reviewer.ID, "scoped review task", "scoped-reviewer")
	if _, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: workspace.ID, RecipientAgent: reviewer.ID, ProjectIdentifier: other.Project.ID,
		Kind: domain.MessageInform, Body: "This belongs to another project.", IdempotencyKey: "other-project-message", CorrelationID: "other-project-message-request",
	}); err != nil {
		t.Fatalf("SendMessage(other project) error = %v", err)
	}
	packet, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, AgentIdentifier: reviewer.ID,
		ExpectedTaskRevision: task.Task.Revision, IdempotencyKey: "scoped-reviewer-context", CorrelationID: "scoped-reviewer-context-request",
	})
	if err != nil || packet.Value.Inbox.UnseenCount != 0 || len(packet.Value.Inbox.Items) != 0 {
		t.Fatalf("BuildContextPacket(cross-project inbox) = %#v, %v", packet, err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "cross-project-mailbox", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress}}}
	reviewerRun, err := storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, ContextPacketID: packet.Value.ID,
		Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: task.Task.Revision,
		IdempotencyKey: "scoped-reviewer-run", CorrelationID: "scoped-reviewer-run-request",
	})
	if err != nil {
		t.Fatalf("CreateRun(scoped reviewer) error = %v", err)
	}
	starting, _ := storage.MarkRunStarting(context.Background(), reviewerRun.Detail.Run.ID, "scoped-reviewer-starting")
	if _, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime", "provider", "scoped-reviewer-started"); err != nil {
		t.Fatalf("MarkRunStarted(scoped reviewer) error = %v", err)
	}
	crossProject, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: workspace.ID, RecipientAgent: reviewer.ID, ProjectIdentifier: other.Project.ID,
		Kind: domain.MessageInform, Body: "Do not wake the active run in another project.", IdempotencyKey: "cross-project-wake-message", CorrelationID: "cross-project-wake-message-request",
	})
	if err != nil || crossProject.Value.Recipient.WakeStatus != domain.WakeNotRequested {
		t.Fatalf("SendMessage(cross-project wake) = %#v, %v", crossProject, err)
	}
	if wake, found, err := storage.ClaimMessageWakeJob(context.Background(), time.Second); err != nil || found {
		t.Fatalf("ClaimMessageWakeJob(cross-project) = %#v, %t, %v; want no job", wake, found, err)
	}

	// Revalidation also protects recovery from an old or corrupt queue entry that
	// points scoped mail at the recipient's run in another project.
	now := storage.nowText()
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO message_wake_jobs(id, message_id, recipient_agent_id, target_run_id, status, attempts, available_at, created_at, updated_at)
VALUES (?, ?, ?, ?, 'pending', 0, ?, ?, ?)`, "wake_cross_project_regression", crossProject.Value.Message.ID, reviewer.ID, reviewerRun.Detail.Run.ID, now, now, now); err != nil {
		t.Fatalf("insert cross-project wake fixture error = %v", err)
	}
	wake, found, err := storage.ClaimMessageWakeJob(context.Background(), time.Second)
	if err != nil || !found {
		t.Fatalf("ClaimMessageWakeJob(injected cross-project) = %#v, %t, %v", wake, found, err)
	}
	if err := storage.SettleMessageWakeJob(context.Background(), wake.ID, domain.WakeSucceeded, ""); err != nil {
		t.Fatalf("SettleMessageWakeJob(cross-project) error = %v", err)
	}
	ownerInbox, err := storage.Inbox(context.Background(), workspace.ID, reviewer.ID, 20)
	if err != nil || len(ownerInbox) != 2 || ownerInbox[1].Delivery.Status != domain.DeliveryQueued || ownerInbox[1].Delivery.WakeStatus != domain.WakeFailed || !strings.Contains(ownerInbox[1].Delivery.WakeDiagnostic, "message project") {
		t.Fatalf("Inbox(owner inspection) = %#v, %v", ownerInbox, err)
	}
}

func createMessageTestAgent(t *testing.T, storage *Store, workspaceID, name string) domain.AgentDefinition {
	t.Helper()
	created, err := storage.CreateAgent(context.Background(), CreateAgentCommand{WorkspaceIdentifier: workspaceID, Name: name, Role: name, Provider: "fake", Runtime: "fake", IdempotencyKey: "message-agent-" + name, CorrelationID: "message-agent-request-" + name})
	if err != nil {
		t.Fatalf("CreateAgent(%s) error = %v", name, err)
	}
	return created.Value
}

func createAssignedMessageTestTask(t *testing.T, storage *Store, workspaceID, projectID, agentID, title, key string) domain.TaskDetail {
	t.Helper()
	task := createWorkTestTask(t, storage, workspaceID, projectID, title, "message-task-"+key)
	assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspaceID, TaskID: task.Task.ID, AgentIdentifier: agentID, LeaseSeconds: 600, ExpectedRevision: task.Task.Revision, IdempotencyKey: "message-assign-" + key, CorrelationID: "message-assign-request-" + key})
	if err != nil {
		t.Fatalf("AssignTask(%s) error = %v", title, err)
	}
	return assigned.Detail
}
