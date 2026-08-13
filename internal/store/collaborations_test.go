package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestParticipantThreadFreezesEligibleCrossProjectBindingsAndInvites(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	storage := openTestStore(t, dataDir, Options{})
	fixture := createCollaborationFixture(t, storage)
	command := CreateParticipantThreadCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		Subject:             "Coordinate the client and simulation contracts",
		Participants: []domain.ParticipantBindingInput{
			{AgentIdentifier: fixture.clientAgent.Name, TaskIdentifier: fixture.clientTask.Task.ID},
			{AgentIdentifier: fixture.engineAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID},
		},
		IdempotencyKey: "create-cross-project-collaboration", CorrelationID: "create-cross-project-collaboration-request",
	}
	created, err := storage.CreateParticipantThread(context.Background(), command)
	if err != nil {
		t.Fatalf("CreateParticipantThread() error = %v", err)
	}
	if created.Collaboration.Kind != domain.ThreadKindParticipantBound || created.Collaboration.ParticipantRevision != 1 || len(created.Collaboration.Participants) != 2 {
		t.Fatalf("CreateParticipantThread() = %#v", created)
	}
	replayed, err := storage.CreateParticipantThread(context.Background(), command)
	if err != nil || !reflect.DeepEqual(replayed, created) {
		t.Fatalf("CreateParticipantThread(replay) = %#v, %v; want %#v", replayed, err, created)
	}
	changed := command
	changed.Subject = "A changed subject"
	if _, err := storage.CreateParticipantThread(context.Background(), changed); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("CreateParticipantThread(conflict) error = %v, code = %q", err, ErrorCode(err))
	}
	for _, participant := range created.Collaboration.Participants {
		if !strings.HasPrefix(participant.ID, "participant_") || len(participant.ID) != len("participant_")+32 || participant.AgentName == "" || participant.TaskTitle == "" || participant.ProjectName == "" || participant.AssignmentRevision < 1 || participant.AgentRevision < 1 || participant.TaskRevision < 1 {
			t.Fatalf("participant is not audit-frozen: %#v", participant)
		}
	}

	invited, err := storage.InviteThreadParticipant(context.Background(), InviteThreadParticipantCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ThreadID: created.Collaboration.Thread.ID,
		Participant:                 domain.ParticipantBindingInput{AgentIdentifier: fixture.opsAgent.ID, TaskIdentifier: fixture.opsTask.Task.ID},
		ExpectedParticipantRevision: 1, IdempotencyKey: "invite-ops", CorrelationID: "invite-ops-request",
	})
	if err != nil || invited.Collaboration.ParticipantRevision != 2 || invited.Collaboration.Thread.Revision != 2 || len(invited.Collaboration.Participants) != 3 {
		t.Fatalf("InviteThreadParticipant() = %#v, %v", invited, err)
	}
	if _, err := storage.InviteThreadParticipant(context.Background(), InviteThreadParticipantCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ThreadID: created.Collaboration.Thread.ID,
		Participant:                 domain.ParticipantBindingInput{AgentIdentifier: fixture.extraAgent.ID, TaskIdentifier: fixture.extraTask.Task.ID},
		ExpectedParticipantRevision: 1, IdempotencyKey: "stale-invite", CorrelationID: "stale-invite-request",
	}); ErrorCode(err) != CodeRevisionConflict {
		t.Fatalf("InviteThreadParticipant(stale) error = %v, code = %q", err, ErrorCode(err))
	}

	newTitle := "Client integration contract v2"
	updatedTask, err := storage.UpdateTask(context.Background(), UpdateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.clientTask.Task.ID, Title: &newTitle,
		ExpectedRevision: fixture.clientTask.Task.Revision, IdempotencyKey: "rename-bound-task", CorrelationID: "rename-bound-task-request",
	})
	if err != nil {
		t.Fatalf("UpdateTask(bound) error = %v", err)
	}
	if _, err := storage.TransitionTask(context.Background(), TransitionTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.clientTask.Task.ID, Action: "cancel", Reason: "freeze audit fixture",
		ExpectedRevision: updatedTask.Detail.Task.Revision, IdempotencyKey: "cancel-bound-task", CorrelationID: "cancel-bound-task-request",
	}); err != nil {
		t.Fatalf("TransitionTask(cancel bound) error = %v", err)
	}
	afterMutation, err := storage.ParticipantThread(context.Background(), fixture.workspace.ID, created.Collaboration.Thread.ID)
	if err != nil || afterMutation.Participants[0].TaskTitle != created.Collaboration.Participants[0].TaskTitle || afterMutation.Participants[0].AssignmentRevision != created.Collaboration.Participants[0].AssignmentRevision {
		t.Fatalf("ParticipantThread(after source mutation) = %#v, %v", afterMutation, err)
	}

	if _, err := storage.db.ExecContext(context.Background(), "UPDATE thread_participants SET task_title = 'tampered' WHERE id = ?", afterMutation.Participants[0].ID); err == nil {
		t.Fatal("immutable participant update unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), "DELETE FROM thread_participants WHERE id = ?", afterMutation.Participants[0].ID); err == nil {
		t.Fatal("participant removal unexpectedly succeeded")
	}

	if err := storage.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	restarted, err := Open(context.Background(), dataDir, Options{})
	if err != nil {
		t.Fatalf("Open(restart) error = %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	afterRestart, err := restarted.ParticipantThread(context.Background(), fixture.workspace.ID, created.Collaboration.Thread.ID)
	if err != nil || !reflect.DeepEqual(afterRestart, afterMutation) {
		t.Fatalf("ParticipantThread(after restart) = %#v, %v; want %#v", afterRestart, err, afterMutation)
	}
}

func TestParticipantThreadRequiresDistinctProjectsAgentsTasksAndLiveAssignments(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	fixture := createCollaborationFixture(t, storage)
	tests := []struct {
		name         string
		participants []domain.ParticipantBindingInput
	}{
		{name: "same project", participants: []domain.ParticipantBindingInput{{AgentIdentifier: fixture.clientAgent.ID, TaskIdentifier: fixture.clientTask.Task.ID}, {AgentIdentifier: fixture.extraAgent.ID, TaskIdentifier: fixture.extraTask.Task.ID}}},
		{name: "duplicate agent", participants: []domain.ParticipantBindingInput{{AgentIdentifier: fixture.engineAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID}, {AgentIdentifier: fixture.engineAgent.ID, TaskIdentifier: fixture.engineSecondTask.Task.ID}}},
		{name: "duplicate task", participants: []domain.ParticipantBindingInput{{AgentIdentifier: fixture.engineAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID}, {AgentIdentifier: fixture.clientAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID}}},
		{name: "wrong assignment", participants: []domain.ParticipantBindingInput{{AgentIdentifier: fixture.clientAgent.ID, TaskIdentifier: fixture.clientTask.Task.ID}, {AgentIdentifier: fixture.opsAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID}}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := storage.CreateParticipantThread(context.Background(), CreateParticipantThreadCommand{WorkspaceIdentifier: fixture.workspace.ID, Subject: "Invalid roster", Participants: test.participants, IdempotencyKey: "invalid-roster-" + string(rune('a'+index)), CorrelationID: "invalid-roster-request-" + string(rune('a'+index))})
			if ErrorCode(err) != CodeInvalidMessage && ErrorCode(err) != CodeMessageDenied {
				t.Fatalf("CreateParticipantThread() error = %v, code = %q", err, ErrorCode(err))
			}
		})
	}
}

func TestParticipantThreadInitialRosterOfThreeAndEightStartsAtRevisionOne(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	fixture := createCollaborationFixture(t, storage)
	bindings := []domain.ParticipantBindingInput{
		{AgentIdentifier: fixture.clientAgent.ID, TaskIdentifier: fixture.clientTask.Task.ID},
		{AgentIdentifier: fixture.engineAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID},
		{AgentIdentifier: fixture.opsAgent.ID, TaskIdentifier: fixture.opsTask.Task.ID},
		{AgentIdentifier: fixture.extraAgent.ID, TaskIdentifier: fixture.extraTask.Task.ID},
	}
	for index := 5; index <= 8; index++ {
		name := "roster-agent-" + string(rune('a'+index))
		agentResult, err := storage.CreateAgent(context.Background(), CreateAgentCommand{WorkspaceIdentifier: fixture.workspace.ID, Name: name, Role: "initial roster participant", Provider: "fake", Runtime: "fake", IdempotencyKey: "initial-roster-agent-" + name, CorrelationID: "initial-roster-agent-request-" + name})
		if err != nil {
			t.Fatalf("CreateAgent(%s) error = %v", name, err)
		}
		project := fixture.engineProject
		if index%2 == 0 {
			project = fixture.clientProject
		}
		task := createWorkTestTask(t, storage, fixture.workspace.ID, project.ID, "Initial roster task "+name, "initial-roster-task-"+name)
		assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: fixture.workspace.ID, TaskID: task.Task.ID, AgentIdentifier: agentResult.Value.ID, LeaseSeconds: 3600, ExpectedRevision: task.Task.Revision, IdempotencyKey: "initial-roster-assign-" + name, CorrelationID: "initial-roster-assign-request-" + name})
		if err != nil {
			t.Fatalf("AssignTask(%s) error = %v", name, err)
		}
		bindings = append(bindings, domain.ParticipantBindingInput{AgentIdentifier: agentResult.Value.ID, TaskIdentifier: assigned.Detail.Task.ID})
	}
	for _, count := range []int{3, 8} {
		created, err := storage.CreateParticipantThread(context.Background(), CreateParticipantThreadCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Subject: "Initial roster cardinality",
			Participants: bindings[:count], IdempotencyKey: "initial-roster-create-" + string(rune('a'+count)), CorrelationID: "initial-roster-create-request-" + string(rune('a'+count)),
		})
		if err != nil {
			t.Fatalf("CreateParticipantThread(%d participants) error = %v", count, err)
		}
		if created.Collaboration.ParticipantRevision != 1 || created.Collaboration.Thread.Revision != 1 || len(created.Collaboration.Participants) != count {
			t.Fatalf("CreateParticipantThread(%d) = participant revision %d, thread revision %d, count %d; want 1, 1, %d", count, created.Collaboration.ParticipantRevision, created.Collaboration.Thread.Revision, len(created.Collaboration.Participants), count)
		}
		var initialCount int
		if err := storage.db.QueryRowContext(context.Background(), "SELECT initial_participant_count FROM message_threads WHERE id = ?", created.Collaboration.Thread.ID).Scan(&initialCount); err != nil || initialCount != count {
			t.Fatalf("initial participant count for %d-member thread = %d, %v", count, initialCount, err)
		}
	}
}

func TestParticipantThreadMessagesAuthorizeExactCrossProjectRunBindings(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	fixture := createCollaborationFixture(t, storage)
	created, err := storage.CreateParticipantThread(context.Background(), CreateParticipantThreadCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Subject: "Cross-project runtime contract",
		Participants:   []domain.ParticipantBindingInput{{AgentIdentifier: fixture.clientAgent.ID, TaskIdentifier: fixture.clientTask.Task.ID}, {AgentIdentifier: fixture.engineAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID}},
		IdempotencyKey: "runtime-collaboration", CorrelationID: "runtime-collaboration-request",
	})
	if err != nil {
		t.Fatalf("CreateParticipantThread() error = %v", err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "cross-project-runtime", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting"}}}
	if _, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: fixture.workspace.ID, RecipientAgent: fixture.engineAgent.ID, ThreadID: created.Collaboration.Thread.ID,
		ProjectIdentifier: fixture.clientProject.ID, Kind: domain.MessageInform, Body: "Spoofed owner origin",
		IdempotencyKey: "owner-spoof", CorrelationID: "owner-spoof-request",
	}); ErrorCode(err) != CodeMessageDenied {
		t.Fatalf("SendMessage(owner scoped origin) error = %v, code = %q", err, ErrorCode(err))
	}

	ownerMessage, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: fixture.workspace.ID, RecipientAgent: fixture.engineAgent.ID, ThreadID: created.Collaboration.Thread.ID,
		Kind: domain.MessageInform, Body: "The client needs deterministic rewind semantics.",
		IdempotencyKey: "owner-cross-project-message", CorrelationID: "owner-cross-project-message-request",
	})
	if err != nil || ownerMessage.Value.Message.ProjectID != "" || ownerMessage.Value.Message.TaskID != "" || ownerMessage.Value.Recipient.WakeStatus != domain.WakeNotRequested {
		t.Fatalf("SendMessage(owner participant thread) = %#v, %v", ownerMessage, err)
	}
	wrongPacket, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.engineSecondTask.Task.ID, AgentIdentifier: fixture.engineAgent.ID, ExpectedTaskRevision: fixture.engineSecondTask.Task.Revision, IdempotencyKey: "wrong-task-context", CorrelationID: "wrong-task-context-request"})
	if err != nil || wrongPacket.Value.Inbox.UnseenCount != 0 {
		t.Fatalf("BuildContextPacket(wrong task inbox) = %#v, %v", wrongPacket, err)
	}
	wrongRun := startCollaborationRun(t, storage, fixture.workspace.ID, fixture.engineSecondTask, scenario, "wrong-engine-task-run")
	if _, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: fixture.workspace.ID, SenderRunID: wrongRun.ID, RecipientAgent: fixture.clientAgent.ID,
		ThreadID: created.Collaboration.Thread.ID, Kind: domain.MessageQuestion, Body: "Can an unbound task send?",
		IdempotencyKey: "wrong-task-send", CorrelationID: "wrong-task-send-request",
	}); ErrorCode(err) != CodeMessageDenied {
		t.Fatalf("SendMessage(wrong task) error = %v, code = %q", err, ErrorCode(err))
	}
	wrongInbox, err := storage.RunInbox(context.Background(), wrongRun.ID, 20)
	if err != nil || len(wrongInbox) != 0 {
		t.Fatalf("RunInbox(wrong task) = %#v, %v", wrongInbox, err)
	}

	boundRun := startCollaborationRun(t, storage, fixture.workspace.ID, fixture.engineTask, scenario, "bound-engine-task-run")
	boundInbox, err := storage.RunInbox(context.Background(), boundRun.ID, 20)
	if err != nil || len(boundInbox) != 1 || boundInbox[0].Message.ID != ownerMessage.Value.Message.ID || boundInbox[0].Delivery.Status != domain.DeliveryDelivered {
		t.Fatalf("RunInbox(bound task) = %#v, %v", boundInbox, err)
	}
	if _, err := storage.AcknowledgeRunMessage(context.Background(), wrongRun.ID, ownerMessage.Value.Message.ID, "wrong-task-ack"); ErrorCode(err) != CodeMessageNotFound {
		t.Fatalf("AcknowledgeRunMessage(wrong task) error = %v, code = %q", err, ErrorCode(err))
	}
	if acknowledged, err := storage.AcknowledgeRunMessage(context.Background(), boundRun.ID, ownerMessage.Value.Message.ID, "bound-task-ack"); err != nil || acknowledged.Value.Delivery.Status != domain.DeliveryAcknowledged {
		t.Fatalf("AcknowledgeRunMessage(bound task) = %#v, %v", acknowledged, err)
	}

	clientRun := startCollaborationRun(t, storage, fixture.workspace.ID, fixture.clientTask, scenario, "bound-client-task-run")
	artifact, err := storage.PublishRunArtifact(context.Background(), PublishRunArtifactCommand{RunID: clientRun.ID, Name: "client-contract.txt", MediaType: "text/plain", Content: "owned by the bound sender", IdempotencyKey: "participant-artifact"})
	if err != nil {
		t.Fatalf("PublishRunArtifact() error = %v", err)
	}
	if _, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: fixture.workspace.ID, SenderRunID: clientRun.ID, RecipientAgent: fixture.engineAgent.ID,
		ThreadID: created.Collaboration.Thread.ID, Kind: domain.MessageInform, Body: "Attempt owned artifact attachment.", ArtifactIDs: []string{artifact.ID},
		IdempotencyKey: "participant-artifact-send", CorrelationID: "participant-artifact-send-request",
	}); ErrorCode(err) != CodeMessageDenied {
		t.Fatalf("SendMessage(participant artifact) error = %v, code = %q", err, ErrorCode(err))
	}
	response, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: fixture.workspace.ID, SenderRunID: clientRun.ID, RecipientAgent: fixture.engineAgent.ID,
		ThreadID: created.Collaboration.Thread.ID, Kind: domain.MessageRequest, Body: "Please expose the rewind invariant.",
		IdempotencyKey: "bound-agent-message", CorrelationID: "bound-agent-message-request",
	})
	if err != nil || response.Value.Message.ProjectID != fixture.clientProject.ID || response.Value.Message.TaskID != fixture.clientTask.Task.ID || response.Value.Recipient.WakeStatus != domain.WakePending {
		t.Fatalf("SendMessage(bound cross-project agent) = %#v, %v", response, err)
	}
	var recipientCount int
	if err := storage.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM message_recipients WHERE message_id = ?", response.Value.Message.ID).Scan(&recipientCount); err != nil || recipientCount != 1 {
		t.Fatalf("message recipient count = %d, %v; want 1", recipientCount, err)
	}
}

func TestParticipantThreadCreationRollsBackProjectionEventAndIdempotency(t *testing.T) {
	t.Parallel()
	fail := false
	storage := openTestStore(t, t.TempDir(), Options{MutationHook: func(stage string) error {
		if fail && stage == MutationAfterProjection {
			return errors.New("fixture interruption")
		}
		return nil
	}})
	fixture := createCollaborationFixture(t, storage)
	var beforeThreads, beforeParticipants, beforeEvents, beforeKeys int
	for query, destination := range map[string]*int{
		"SELECT COUNT(*) FROM message_threads": &beforeThreads, "SELECT COUNT(*) FROM thread_participants": &beforeParticipants,
		"SELECT COUNT(*) FROM events": &beforeEvents, "SELECT COUNT(*) FROM idempotency_keys": &beforeKeys,
	} {
		if err := storage.db.QueryRowContext(context.Background(), query).Scan(destination); err != nil {
			t.Fatalf("count before rollback: %v", err)
		}
	}
	fail = true
	_, err := storage.CreateParticipantThread(context.Background(), CreateParticipantThreadCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Subject: "Rollback collaboration",
		Participants:   []domain.ParticipantBindingInput{{AgentIdentifier: fixture.clientAgent.ID, TaskIdentifier: fixture.clientTask.Task.ID}, {AgentIdentifier: fixture.engineAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID}},
		IdempotencyKey: "rollback-collaboration", CorrelationID: "rollback-collaboration-request",
	})
	if ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("CreateParticipantThread(interrupted) error = %v, code = %q", err, ErrorCode(err))
	}
	for query, expected := range map[string]int{
		"SELECT COUNT(*) FROM message_threads": beforeThreads, "SELECT COUNT(*) FROM thread_participants": beforeParticipants,
		"SELECT COUNT(*) FROM events": beforeEvents, "SELECT COUNT(*) FROM idempotency_keys": beforeKeys,
	} {
		var actual int
		if err := storage.db.QueryRowContext(context.Background(), query).Scan(&actual); err != nil || actual != expected {
			t.Fatalf("rollback count for %q = %d, %v; want %d", query, actual, err, expected)
		}
	}
}

func TestParticipantRosterRevisionIsMaintainedByTheDatabase(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	fixture := createCollaborationFixture(t, storage)
	created, err := storage.CreateParticipantThread(context.Background(), CreateParticipantThreadCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Subject: "Database-coupled roster",
		Participants: []domain.ParticipantBindingInput{
			{AgentIdentifier: fixture.clientAgent.ID, TaskIdentifier: fixture.clientTask.Task.ID},
			{AgentIdentifier: fixture.engineAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID},
		},
		IdempotencyKey: "database-coupled-roster", CorrelationID: "database-coupled-roster-request",
	})
	if err != nil {
		t.Fatalf("CreateParticipantThread() error = %v", err)
	}
	threadID := created.Collaboration.Thread.ID
	if _, err := storage.db.ExecContext(context.Background(), "UPDATE message_threads SET participant_revision = participant_revision + 1 WHERE id = ?", threadID); err == nil {
		t.Fatal("standalone participant revision increment unexpectedly succeeded")
	}
	otherWorkspaceID := "ws_integrity_scope_target"
	now := storage.nowText()
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO workspaces(id, name, revision, created_at, updated_at, created_by, updated_by)
VALUES (?, 'integrity-scope-target', 1, ?, ?, 'local-owner', 'local-owner')`, otherWorkspaceID, now, now); err != nil {
		t.Fatalf("insert scope target workspace: %v", err)
	}
	if _, err := storage.db.ExecContext(context.Background(), "UPDATE message_threads SET workspace_id = ? WHERE id = ?", otherWorkspaceID, threadID); err == nil {
		t.Fatal("participant thread workspace move unexpectedly succeeded")
	}

	ops := directParticipant(fixture.workspace, fixture.opsProject, fixture.opsAgent, fixture.opsTask, threadID, "participant_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 3, storage.nowText())
	if err := insertDirectParticipant(context.Background(), storage.db, ops); err != nil {
		t.Fatalf("insert direct eligible participant error = %v", err)
	}
	var participantRevision, threadRevision, participantCount int64
	if err := storage.db.QueryRowContext(context.Background(), `SELECT participant_revision, revision,
(SELECT COUNT(*) FROM thread_participants WHERE thread_id = message_threads.id)
FROM message_threads WHERE id = ?`, threadID).Scan(&participantRevision, &threadRevision, &participantCount); err != nil {
		t.Fatalf("query coupled roster revision: %v", err)
	}
	if participantRevision != 2 || threadRevision != 2 || participantCount != 3 {
		t.Fatalf("coupled roster = participant revision %d, thread revision %d, count %d; want 2, 2, 3", participantRevision, threadRevision, participantCount)
	}
	if _, err := storage.db.ExecContext(context.Background(), "UPDATE message_threads SET participant_revision = participant_revision + 1 WHERE id = ?", threadID); err == nil {
		t.Fatal("post-invite standalone participant revision increment unexpectedly succeeded")
	}

	extra := directParticipant(fixture.workspace, fixture.clientProject, fixture.extraAgent, fixture.extraTask, threadID, "participant_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 4, storage.nowText())
	tamperedName := extra
	tamperedName.AgentName = "spoofed-agent-name"
	if err := insertDirectParticipant(context.Background(), storage.db, tamperedName); err == nil {
		t.Fatal("participant with spoofed frozen label unexpectedly succeeded")
	}
	tamperedAssignment := extra
	tamperedAssignment.AssignmentRevision++
	if err := insertDirectParticipant(context.Background(), storage.db, tamperedAssignment); err == nil {
		t.Fatal("participant with spoofed assignment revision unexpectedly succeeded")
	}

	incompleteThreadID := "thread_incomplete_roster_fixture"
	now = storage.nowText()
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO message_threads(
id, workspace_id, project_id, task_id, subject, status, revision, created_at, updated_at,
created_by, updated_by, kind, participant_revision,
initial_participant_count
) VALUES (?, ?, NULL, NULL, 'Incomplete roster', 'open', 1, ?, ?, 'local-owner', 'local-owner', 'participant_bound', 1, 2)`, incompleteThreadID, fixture.workspace.ID, now, now); err != nil {
		t.Fatalf("insert incomplete participant thread: %v", err)
	}
	incomplete := directParticipant(fixture.workspace, fixture.clientProject, fixture.extraAgent, fixture.extraTask, incompleteThreadID, "participant_cccccccccccccccccccccccccccccccc", 1, now)
	if err := insertDirectParticipant(context.Background(), storage.db, incomplete); err != nil {
		t.Fatalf("insert first incomplete participant: %v", err)
	}
	if _, err := storage.ParticipantThread(context.Background(), fixture.workspace.ID, incompleteThreadID); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("ParticipantThread(incomplete) error = %v, code = %q", err, ErrorCode(err))
	}
	if _, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ThreadID: incompleteThreadID, RecipientAgent: fixture.extraAgent.ID,
		Kind: domain.MessageInform, Body: "An incomplete roster must not carry messages.",
		IdempotencyKey: "incomplete-roster-message", CorrelationID: "incomplete-roster-message-request",
	}); ErrorCode(err) != CodeMessageDenied {
		t.Fatalf("SendMessage(incomplete roster) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestParticipantMessageDatabaseIntegrityRejectsDirectSQLBypasses(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	fixture := createCollaborationFixture(t, storage)
	created, err := storage.CreateParticipantThread(context.Background(), CreateParticipantThreadCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Subject: "Direct SQL integrity",
		Participants: []domain.ParticipantBindingInput{
			{AgentIdentifier: fixture.clientAgent.ID, TaskIdentifier: fixture.clientTask.Task.ID},
			{AgentIdentifier: fixture.engineAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID},
		},
		IdempotencyKey: "direct-sql-integrity", CorrelationID: "direct-sql-integrity-request",
	})
	if err != nil {
		t.Fatalf("CreateParticipantThread() error = %v", err)
	}
	threadID := created.Collaboration.Thread.ID
	participants := created.Collaboration.Participants
	clientParticipant, engineParticipant := participants[0], participants[1]
	if clientParticipant.AgentID != fixture.clientAgent.ID {
		clientParticipant, engineParticipant = participants[1], participants[0]
	}

	foreignKeyFound := false
	rows, err := storage.db.QueryContext(context.Background(), "PRAGMA foreign_key_list(message_recipients)")
	if err != nil {
		t.Fatalf("foreign_key_list(message_recipients): %v", err)
	}
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			_ = rows.Close()
			t.Fatalf("scan message recipient foreign key: %v", err)
		}
		if table == "thread_participants" && from == "recipient_participant_id" && to == "id" {
			foreignKeyFound = true
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close foreign key rows: %v", err)
	}
	if !foreignKeyFound {
		t.Fatal("message_recipients.recipient_participant_id has no thread_participants foreign key")
	}

	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "direct-sql-integrity", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting"}}}
	clientRun := startCollaborationRun(t, storage, fixture.workspace.ID, fixture.clientTask, scenario, "integrity-client-run")
	engineRun := startCollaborationRun(t, storage, fixture.workspace.ID, fixture.engineTask, scenario, "integrity-engine-run")
	now := storage.nowText()
	insertMessage := func(id, senderType, senderID, senderAgentID, senderRunID, projectID, taskID, artifacts string) error {
		_, err := storage.db.ExecContext(context.Background(), `INSERT INTO messages(
id, workspace_id, thread_id, project_id, task_id, sender_type, sender_id, sender_agent_id,
sender_run_id, kind, body, artifact_ids_json, reply_to_message_id, created_at
) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''),
'inform', 'direct SQL integrity probe', ?, NULL, ?)`, id, fixture.workspace.ID, threadID, projectID, taskID, senderType, senderID, senderAgentID, senderRunID, artifacts, now)
		return err
	}
	if err := insertMessage("msg_scalar_artifact", "owner", localOwnerActorID, "", "", "", "", `{}`); err == nil {
		t.Fatal("participant message with non-array artifact JSON unexpectedly succeeded")
	}
	if err := insertMessage("msg_owner_spoof", "owner", localOwnerActorID, "", "", fixture.clientProject.ID, fixture.clientTask.Task.ID, `[]`); err == nil {
		t.Fatal("participant owner message with spoofed origin unexpectedly succeeded")
	}
	otherWorkspaceID := "ws_message_integrity_target"
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO workspaces(id, name, revision, created_at, updated_at, created_by, updated_by)
VALUES (?, 'message-integrity-target', 1, ?, ?, 'local-owner', 'local-owner')`, otherWorkspaceID, now, now); err != nil {
		t.Fatalf("insert message integrity target workspace: %v", err)
	}
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO messages(
id, workspace_id, thread_id, project_id, task_id, sender_type, sender_id, sender_agent_id,
sender_run_id, kind, body, artifact_ids_json, reply_to_message_id, created_at
) VALUES ('msg_cross_workspace_owner', ?, ?, NULL, NULL, 'owner', 'local-owner', NULL, NULL,
'inform', 'cross workspace owner probe', '[]', NULL, ?)`, otherWorkspaceID, threadID, now); err == nil {
		t.Fatal("participant message with a different workspace unexpectedly succeeded")
	}
	if err := insertMessage("msg_agent_spoof", "agent_run", clientRun.ID, fixture.clientAgent.ID, clientRun.ID, fixture.engineProject.ID, fixture.engineTask.Task.ID, `[]`); err == nil {
		t.Fatal("participant agent message with mismatched run origin unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO message_threads(
id, workspace_id, project_id, task_id, subject, status, revision, created_at, updated_at,
created_by, updated_by, kind, participant_revision, initial_participant_count
) VALUES ('thread_forged_owner_audit', ?, NULL, NULL, 'Forged owner audit', 'open', 1, ?, ?,
'forged-owner', 'forged-owner', 'participant_bound', 1, 2)`, fixture.workspace.ID, now, now); err == nil {
		t.Fatal("participant thread with forged owner audit fields unexpectedly succeeded")
	}
	forgedInvite := directParticipant(fixture.workspace, fixture.opsProject, fixture.opsAgent, fixture.opsTask, threadID, "participant_dddddddddddddddddddddddddddddddd", 3, now)
	forgedInvite.InvitedBy = "forged-owner"
	if err := insertDirectParticipant(context.Background(), storage.db, forgedInvite); err == nil {
		t.Fatal("participant with forged inviter unexpectedly succeeded")
	}

	ownerToEngine, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ThreadID: threadID, RecipientAgent: fixture.engineAgent.ID,
		Kind: domain.MessageInform, Body: "First immutable owner message.",
		IdempotencyKey: "integrity-owner-engine", CorrelationID: "integrity-owner-engine-request",
	})
	if err != nil {
		t.Fatalf("SendMessage(owner to engine) error = %v", err)
	}
	ownerToClient, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ThreadID: threadID, RecipientAgent: fixture.clientAgent.ID,
		Kind: domain.MessageInform, Body: "Second immutable owner message.",
		IdempotencyKey: "integrity-owner-client", CorrelationID: "integrity-owner-client-request",
	})
	if err != nil {
		t.Fatalf("SendMessage(owner to client) error = %v", err)
	}
	if _, err := storage.db.ExecContext(context.Background(), "UPDATE messages SET body = 'tampered' WHERE id = ?", ownerToEngine.Value.Message.ID); err == nil {
		t.Fatal("message update unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), "DELETE FROM messages WHERE id = ?", ownerToEngine.Value.Message.ID); err == nil {
		t.Fatal("message delete unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), `UPDATE message_threads SET status = 'closed' WHERE id = ?`, threadID); err != nil {
		t.Fatalf("close participant thread fixture: %v", err)
	}
	if err := insertMessage("msg_closed_thread_integrity", "owner", localOwnerActorID, "", "", "", "", `[]`); err == nil {
		t.Fatal("participant message into a closed thread unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), `UPDATE message_threads SET status = 'open' WHERE id = ?`, threadID); err != nil {
		t.Fatalf("reopen participant thread fixture: %v", err)
	}
	directMessage, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: fixture.workspace.ID, RecipientAgent: fixture.engineAgent.ID,
		ProjectIdentifier: fixture.engineProject.ID, Kind: domain.MessageInform, Body: "Direct reply target.",
		IdempotencyKey: "integrity-direct-reply-target", CorrelationID: "integrity-direct-reply-target-request",
	})
	if err != nil {
		t.Fatalf("SendMessage(direct reply target) error = %v", err)
	}
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO messages(
id, workspace_id, thread_id, project_id, task_id, sender_type, sender_id, sender_agent_id,
sender_run_id, kind, body, artifact_ids_json, reply_to_message_id, created_at
) VALUES ('msg_cross_thread_reply', ?, ?, NULL, NULL, 'owner', 'local-owner', NULL, NULL,
'inform', 'cross thread reply probe', '[]', ?, ?)`, fixture.workspace.ID, threadID, directMessage.Value.Message.ID, now); err == nil {
		t.Fatal("participant message with cross-thread reply target unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), `UPDATE message_recipients SET message_id = ?
WHERE message_id = ? AND recipient_agent_id = ?`, ownerToClient.Value.Message.ID, ownerToEngine.Value.Message.ID, fixture.engineAgent.ID); err == nil {
		t.Fatal("message recipient move to another message unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), `DELETE FROM message_recipients
WHERE message_id = ? AND recipient_agent_id = ?`, ownerToEngine.Value.Message.ID, fixture.engineAgent.ID); err == nil {
		t.Fatal("message recipient delete unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO message_recipients(
message_id, recipient_agent_id, status, queued_at, recipient_participant_id
) VALUES (?, ?, 'queued', ?, ?)`, ownerToEngine.Value.Message.ID, fixture.clientAgent.ID, now, clientParticipant.ID); err == nil {
		t.Fatal("second participant recipient unexpectedly succeeded")
	}

	selfMessageID := "msg_self_recipient_integrity"
	if err := insertMessage(selfMessageID, "agent_run", clientRun.ID, fixture.clientAgent.ID, clientRun.ID, fixture.clientProject.ID, fixture.clientTask.Task.ID, `[]`); err != nil {
		t.Fatalf("insert valid raw participant message: %v", err)
	}
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO message_recipients(
message_id, recipient_agent_id, status, queued_at, recipient_participant_id
) VALUES (?, ?, 'queued', ?, ?)`, selfMessageID, fixture.clientAgent.ID, now, clientParticipant.ID); err == nil {
		t.Fatal("self recipient unexpectedly succeeded")
	}

	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO message_wake_jobs(
id, message_id, recipient_agent_id, target_run_id, status, attempts, available_at, created_at, updated_at
) VALUES ('wake_wrong_binding_integrity', ?, ?, ?, 'pending', 0, ?, ?, ?)`, ownerToEngine.Value.Message.ID, fixture.engineAgent.ID, clientRun.ID, now, now, now); err == nil {
		t.Fatal("wake targeting a run outside the recipient binding unexpectedly succeeded")
	}
	if engineRun.ID == "" || engineParticipant.ID == "" {
		t.Fatal("integrity fixture did not create the bound engine run/participant")
	}
}

type collaborationFixture struct {
	workspace                                      domain.Workspace
	clientProject, engineProject, opsProject       domain.Project
	clientAgent, engineAgent, opsAgent, extraAgent domain.AgentDefinition
	clientTask, engineTask, engineSecondTask       domain.TaskDetail
	opsTask, extraTask                             domain.TaskDetail
}

func createCollaborationFixture(t *testing.T, storage *Store) collaborationFixture {
	t.Helper()
	workspace, clientProject := initializeWorkTestProject(t, storage)
	register := func(name string) domain.Project {
		result, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{WorkspaceIdentifier: workspace.ID, Name: name, WriteMode: domain.WriteModeShared, IdempotencyKey: "collaboration-project-" + name, CorrelationID: "collaboration-project-request-" + name, Observation: sourceTestObservation(filepath.Join(t.TempDir(), name), name)})
		if err != nil {
			t.Fatalf("RegisterProject(%s) error = %v", name, err)
		}
		return result.Project
	}
	engineProject, opsProject := register("engine-sim-offline"), register("operations")
	createAgent := func(name string, concurrency int) domain.AgentDefinition {
		result, err := storage.CreateAgent(context.Background(), CreateAgentCommand{WorkspaceIdentifier: workspace.ID, Name: name, Role: name, Provider: "fake", Runtime: "fake", MaxConcurrency: concurrency, IdempotencyKey: "collaboration-agent-" + name, CorrelationID: "collaboration-agent-request-" + name})
		if err != nil {
			t.Fatalf("CreateAgent(%s) error = %v", name, err)
		}
		return result.Value
	}
	clientAgent, engineAgent := createAgent("plugandrev-agent", 2), createAgent("engine-agent", 3)
	opsAgent, extraAgent := createAgent("ops-agent", 2), createAgent("extra-agent", 2)
	assign := func(project domain.Project, agent domain.AgentDefinition, title, key string) domain.TaskDetail {
		task := createWorkTestTask(t, storage, workspace.ID, project.ID, title, "collaboration-task-"+key)
		assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, AgentIdentifier: agent.ID, LeaseSeconds: 3600, ExpectedRevision: task.Task.Revision, IdempotencyKey: "collaboration-assign-" + key, CorrelationID: "collaboration-assign-request-" + key})
		if err != nil {
			t.Fatalf("AssignTask(%s) error = %v", title, err)
		}
		return assigned.Detail
	}
	return collaborationFixture{
		workspace: workspace, clientProject: clientProject, engineProject: engineProject, opsProject: opsProject,
		clientAgent: clientAgent, engineAgent: engineAgent, opsAgent: opsAgent, extraAgent: extraAgent,
		clientTask:       assign(clientProject, clientAgent, "Integrate engine simulation", "client"),
		engineTask:       assign(engineProject, engineAgent, "Define simulation contract", "engine"),
		engineSecondTask: assign(opsProject, engineAgent, "Observe simulation rollout", "engine-second"),
		opsTask:          assign(opsProject, opsAgent, "Coordinate release order", "ops"),
		extraTask:        assign(clientProject, extraAgent, "Review client integration", "extra"),
	}
}

func startCollaborationRun(t *testing.T, storage *Store, workspaceID string, task domain.TaskDetail, scenario domain.FakeScenario, key string) domain.Run {
	t.Helper()
	detail := createRunTest(t, storage, workspaceID, task, scenario, key)
	starting, err := storage.MarkRunStarting(context.Background(), detail.Run.ID, key+"-starting")
	if err != nil {
		t.Fatalf("MarkRunStarting(%s) error = %v", key, err)
	}
	started, err := storage.MarkRunStarted(context.Background(), starting.ID, key+"-runtime", key+"-provider", key+"-started")
	if err != nil {
		t.Fatalf("MarkRunStarted(%s) error = %v", key, err)
	}
	return started.Run
}

func directParticipant(workspace domain.Workspace, project domain.Project, agent domain.AgentDefinition, task domain.TaskDetail, threadID, participantID string, ordinal int, invitedAt string) domain.ThreadParticipant {
	return domain.ThreadParticipant{
		ID: participantID, ThreadID: threadID, WorkspaceID: workspace.ID,
		AgentID: agent.ID, AgentName: agent.Name, AgentRevision: agent.Revision,
		TaskID: task.Task.ID, TaskTitle: task.Task.Title, TaskRevision: task.Task.Revision,
		ProjectID: project.ID, ProjectName: project.Name,
		AssignmentID: task.Assignment.ID, AssignmentRevision: task.Assignment.Revision,
		Ordinal: ordinal, Status: domain.ThreadParticipantActive, InvitedAt: invitedAt, InvitedBy: localOwnerActorID,
	}
}

func insertDirectParticipant(ctx context.Context, database *sql.DB, participant domain.ThreadParticipant) error {
	_, err := database.ExecContext(ctx, `INSERT INTO thread_participants(
id, thread_id, workspace_id, agent_id, agent_name, task_id, task_title, project_id,
project_name, assignment_id, assignment_revision, agent_revision, task_revision,
ordinal, status, invited_at, invited_by
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		participant.ID, participant.ThreadID, participant.WorkspaceID, participant.AgentID, participant.AgentName,
		participant.TaskID, participant.TaskTitle, participant.ProjectID, participant.ProjectName,
		participant.AssignmentID, participant.AssignmentRevision, participant.AgentRevision, participant.TaskRevision,
		participant.Ordinal, participant.Status, participant.InvitedAt, participant.InvitedBy)
	return err
}
