package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
)

const (
	threadCreatedEvent       = "thread.created"
	messageSentEvent         = "message.sent"
	messageDeliveredEvent    = "message.delivered"
	messageReadEvent         = "message.read"
	messageAcknowledgedEvent = "message.acknowledged"
	messageWakeSucceeded     = "message.wake_succeeded"
	messageWakeFailed        = "message.wake_failed"
	messageWakeActorID       = "message-wake-worker"
	maximumMessageBytes      = 4096
	maximumMessageArtifacts  = 16
	maximumInboxItems        = 50
	contextInboxItems        = 10
)

var supportedMessageKinds = map[string]bool{
	domain.MessageInform: true, domain.MessageQuestion: true, domain.MessageRequest: true,
	domain.MessageReviewRequest: true, domain.MessageHandoff: true,
	domain.MessageDecisionNotice: true, domain.MessageRisk: true,
	domain.MessageConflict: true, domain.MessageApprovalRequest: true,
}

func (s *Store) SendMessage(ctx context.Context, command SendMessageCommand) (MutationResult[domain.MessageMutation], error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	recipientIdentifier := strings.TrimSpace(command.RecipientAgent)
	senderRunID, threadID := strings.TrimSpace(command.SenderRunID), strings.TrimSpace(command.ThreadID)
	projectIdentifier, taskID := strings.TrimSpace(command.ProjectIdentifier), strings.TrimSpace(command.TaskID)
	kind, subject, body := strings.TrimSpace(command.Kind), strings.TrimSpace(command.Subject), strings.TrimSpace(command.Body)
	replyTo, key, correlationID := strings.TrimSpace(command.ReplyToMessageID), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	artifactIDs := normalizedIdentifiers(command.ArtifactIDs)
	if workspaceIdentifier == "" || recipientIdentifier == "" || !supportedMessageKinds[kind] || !validMessageText(body, maximumMessageBytes) || len(artifactIDs) > maximumMessageArtifacts {
		return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeInvalidMessage, Message: "message requires workspace, one agent recipient, supported kind, and a UTF-8 body from 1 to 4096 bytes"}
	}
	if len(command.ArtifactIDs) != len(artifactIDs) {
		return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeInvalidMessage, Message: "message artifact identifiers must be unique, bounded, and non-empty"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidMessage); err != nil {
		return MutationResult[domain.MessageMutation]{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.MessageMutation]{}, storageFailure("begin message send", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return MutationResult[domain.MessageMutation]{}, err
	}

	senderType, senderID, senderAgentID, senderAgentName := "owner", localOwnerActorID, "", ""
	actorType := localActorType
	var senderRun domain.Run
	if senderRunID != "" {
		senderRun, err = queryRun(ctx, tx, workspace.ID, senderRunID)
		if err != nil {
			return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "message sender run is outside the authorized workspace"}
		}
		if !runCanUseMailbox(senderRun.Status) {
			return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "message sender run is not live"}
		}
		sender, senderErr := queryAgent(ctx, tx, workspace.ID, senderRun.AgentID)
		if senderErr != nil {
			return MutationResult[domain.MessageMutation]{}, senderErr
		}
		senderType, senderID, senderAgentID, senderAgentName, actorType = "agent_run", senderRun.ID, sender.ID, sender.Name, "agent_run"
		projectIdentifier, taskID = senderRun.ProjectID, senderRun.TaskID
	}
	recipient, err := queryAgent(ctx, tx, workspace.ID, recipientIdentifier)
	if err != nil {
		return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "recipient must be one enabled agent in this workspace"}
	}
	if senderAgentID != "" && recipient.ID == senderAgentID {
		return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "an agent run cannot message itself"}
	}

	projectID, resolvedTaskID, err := resolveMessageScope(ctx, tx, workspace.ID, projectIdentifier, taskID)
	if err != nil {
		return MutationResult[domain.MessageMutation]{}, err
	}
	request := map[string]any{
		"workspace_id": workspace.ID, "sender_run_id": senderRunID, "recipient_agent_id": recipient.ID,
		"thread_id": threadID, "project_id": projectID, "task_id": resolvedTaskID, "kind": kind,
		"subject": subject, "body": body, "artifact_ids": artifactIDs, "reply_to_message_id": replyTo,
	}
	requestHash, err := hashCommand("message.send", request)
	if err != nil {
		return MutationResult[domain.MessageMutation]{}, storageFailure("hash message send", err)
	}
	scopedKey := "message-send:" + senderID + ":" + key
	var replay MutationResult[domain.MessageMutation]
	if found, lookupErr := lookupIdempotency(ctx, tx, scopedKey, "message.send", requestHash, &replay); lookupErr != nil {
		return MutationResult[domain.MessageMutation]{}, lookupErr
	} else if found {
		return replay, nil
	}
	if !recipient.Enabled {
		return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "recipient must be one enabled agent in this workspace"}
	}

	now := s.nowText()
	var thread domain.MessageThread
	var sequence int64
	if threadID == "" {
		if subject == "" {
			subject = messagePreview(body, 80)
		}
		if !validMessageText(subject, 160) {
			return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeInvalidMessage, Message: "a new thread subject must contain 1 to 160 UTF-8 bytes"}
		}
		generatedID, generateErr := randomID("thread_")
		if generateErr != nil {
			return MutationResult[domain.MessageMutation]{}, storageFailure("generate thread id", generateErr)
		}
		thread = domain.MessageThread{ID: generatedID, WorkspaceID: workspace.ID, ProjectID: projectID, TaskID: resolvedTaskID, Subject: subject, Status: domain.ThreadOpen, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: senderID, UpdatedBy: senderID}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_threads(id, workspace_id, project_id, task_id, subject, status, revision, created_at, updated_at, created_by, updated_by)
VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, 1, ?, ?, ?, ?)`, thread.ID, thread.WorkspaceID, thread.ProjectID, thread.TaskID, thread.Subject, thread.Status, now, now, senderID, senderID); err != nil {
			return MutationResult[domain.MessageMutation]{}, storageFailure("insert message thread", err)
		}
		sequence, err = appendEventForActor(ctx, tx, workspace.ID, "thread", thread.ID, thread.Revision, threadCreatedEvent, correlationID, now, senderID, actorType, map[string]any{"project_id": projectID, "task_id": resolvedTaskID, "subject": subject})
		if err != nil {
			return MutationResult[domain.MessageMutation]{}, err
		}
	} else {
		thread, err = queryMessageThread(ctx, tx, workspace.ID, threadID)
		if err != nil {
			return MutationResult[domain.MessageMutation]{}, err
		}
		if thread.Status != domain.ThreadOpen || (projectID != "" && thread.ProjectID != "" && projectID != thread.ProjectID) {
			return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "thread is closed or outside the sender scope"}
		}
		if senderAgentID != "" {
			senderParticipant, participantErr := messageThreadHasAgent(ctx, tx, thread.ID, senderAgentID)
			if participantErr != nil {
				return MutationResult[domain.MessageMutation]{}, participantErr
			}
			recipientParticipant, participantErr := messageThreadHasAgent(ctx, tx, thread.ID, recipient.ID)
			if participantErr != nil {
				return MutationResult[domain.MessageMutation]{}, participantErr
			}
			if !senderParticipant || !recipientParticipant {
				return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "agent replies are limited to existing thread participants"}
			}
		}
		projectID, resolvedTaskID = thread.ProjectID, thread.TaskID
	}
	if replyTo != "" {
		var replyThread string
		if err := tx.QueryRowContext(ctx, "SELECT thread_id FROM messages WHERE id = ? AND workspace_id = ?", replyTo, workspace.ID).Scan(&replyThread); err != nil || replyThread != thread.ID {
			return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeInvalidMessage, Message: "reply target must belong to the selected thread"}
		}
	}
	if err := validateMessageArtifacts(ctx, tx, senderRunID, artifactIDs); err != nil {
		return MutationResult[domain.MessageMutation]{}, err
	}

	messageID, err := randomID("msg_")
	if err != nil {
		return MutationResult[domain.MessageMutation]{}, storageFailure("generate message id", err)
	}
	artifactsJSON, _ := json.Marshal(artifactIDs)
	message := domain.Message{ID: messageID, WorkspaceID: workspace.ID, ThreadID: thread.ID, ProjectID: projectID, TaskID: resolvedTaskID, SenderType: senderType, SenderID: senderID, SenderAgentID: senderAgentID, SenderAgentName: senderAgentName, SenderRunID: senderRunID, Kind: kind, Body: body, ArtifactIDs: artifactIDs, ReplyToMessageID: replyTo, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, workspace_id, thread_id, project_id, task_id, sender_type, sender_id, sender_agent_id, sender_run_id, kind, body, artifact_ids_json, reply_to_message_id, created_at)
VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?)`, message.ID, message.WorkspaceID, message.ThreadID, message.ProjectID, message.TaskID, message.SenderType, message.SenderID, message.SenderAgentID, message.SenderRunID, message.Kind, message.Body, string(artifactsJSON), message.ReplyToMessageID, message.CreatedAt); err != nil {
		return MutationResult[domain.MessageMutation]{}, storageFailure("insert message", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO message_recipients(message_id, recipient_agent_id, status, queued_at) VALUES (?, ?, ?, ?)", message.ID, recipient.ID, domain.DeliveryQueued, now); err != nil {
		return MutationResult[domain.MessageMutation]{}, storageFailure("queue message recipient", err)
	}
	thread.Revision++
	thread.UpdatedAt, thread.UpdatedBy = now, senderID
	if _, err := tx.ExecContext(ctx, "UPDATE message_threads SET revision = ?, updated_at = ?, updated_by = ? WHERE id = ?", thread.Revision, now, senderID, thread.ID); err != nil {
		return MutationResult[domain.MessageMutation]{}, storageFailure("update message thread", err)
	}
	sequence, err = appendEventForActor(ctx, tx, workspace.ID, "message", message.ID, 1, messageSentEvent, correlationID, now, senderID, actorType, map[string]any{"thread_id": thread.ID, "recipient_agent_id": recipient.ID, "kind": kind, "task_id": resolvedTaskID, "artifact_ids": artifactIDs})
	if err != nil {
		return MutationResult[domain.MessageMutation]{}, err
	}
	wakeStatus := domain.WakeNotRequested
	var targetRunID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM runs
WHERE workspace_id = ? AND agent_id = ? AND status IN ('starting', 'active', 'blocked')
AND (? = '' OR project_id = ?) ORDER BY created_at DESC, id DESC LIMIT 1`, workspace.ID, recipient.ID, projectID, projectID).Scan(&targetRunID)
	if err == nil {
		wakeID, generateErr := randomID("wake_")
		if generateErr != nil {
			return MutationResult[domain.MessageMutation]{}, storageFailure("generate message wake id", generateErr)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_wake_jobs(id, message_id, recipient_agent_id, target_run_id, status, attempts, available_at, created_at, updated_at)
VALUES (?, ?, ?, ?, 'pending', 0, ?, ?, ?)`, wakeID, message.ID, recipient.ID, targetRunID, now, now, now); err != nil {
			return MutationResult[domain.MessageMutation]{}, storageFailure("enqueue message wake", err)
		}
		wakeStatus = domain.WakePending
	} else if !errors.Is(err, sql.ErrNoRows) {
		return MutationResult[domain.MessageMutation]{}, storageFailure("find recipient run for wake", err)
	}
	recipientDelivery := domain.MessageDelivery{MessageID: message.ID, RecipientAgentID: recipient.ID, RecipientName: recipient.Name, Status: domain.DeliveryQueued, QueuedAt: now, WakeStatus: wakeStatus}
	result := MutationResult[domain.MessageMutation]{Value: domain.MessageMutation{Thread: thread, Message: message, Recipient: recipientDelivery}, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, scopedKey, "message.send", requestHash, result, now); err != nil {
		return MutationResult[domain.MessageMutation]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.MessageMutation]{}, storageFailure("commit message send", err)
	}
	return result, nil
}

func (s *Store) Inbox(ctx context.Context, workspaceIdentifier, agentIdentifier string, limit int) ([]domain.InboxItem, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > maximumInboxItems {
		return nil, &Error{Code: CodeInvalidMessage, Message: "inbox limit must be from 1 to 50"}
	}
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return nil, err
	}
	agent, err := queryAgent(ctx, s.db, workspace.ID, strings.TrimSpace(agentIdentifier))
	if err != nil {
		return nil, err
	}
	return queryInbox(ctx, s.db, agent.ID, limit)
}

func (s *Store) RunInbox(ctx context.Context, runID string, limit int) ([]domain.InboxItem, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > maximumInboxItems {
		return nil, &Error{Code: CodeInvalidMessage, Message: "inbox limit must be from 1 to 50"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageFailure("begin run inbox", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", strings.TrimSpace(runID))
	if err != nil || !runCanUseMailbox(run.Status) {
		return nil, &Error{Code: CodeMessageDenied, Message: "run mailbox is not active"}
	}
	items, err := queryInboxWithCondition(ctx, tx, run.AgentID, limit, " AND (m.project_id IS NULL OR m.project_id = ?)", run.ProjectID)
	if err != nil {
		return nil, err
	}
	now := s.nowText()
	for index := range items {
		if items[index].Delivery.Status != domain.DeliveryQueued {
			continue
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE message_recipients SET status = 'delivered', delivered_at = ?, delivered_run_id = ?
WHERE message_id = ? AND recipient_agent_id = ? AND status = 'queued'`, now, run.ID, items[index].Message.ID, run.AgentID)
		if updateErr != nil {
			return nil, storageFailure("deliver inbox message", updateErr)
		}
		changed, _ := result.RowsAffected()
		if changed == 1 {
			items[index].Delivery.Status, items[index].Delivery.DeliveredAt, items[index].Delivery.DeliveredRunID = domain.DeliveryDelivered, now, run.ID
			if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "message", items[index].Message.ID, 2, messageDeliveredEvent, "inbox-"+run.ID, now, run.ID, "agent_run", map[string]any{"recipient_agent_id": run.AgentID, "run_id": run.ID}); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, storageFailure("commit run inbox", err)
	}
	return items, nil
}

func (s *Store) ReadRunMessage(ctx context.Context, runID, messageID, key string) (MutationResult[domain.InboxItem], error) {
	return s.transitionRunMessage(ctx, runID, messageID, key, domain.DeliveryRead)
}

func (s *Store) AcknowledgeRunMessage(ctx context.Context, runID, messageID, key string) (MutationResult[domain.InboxItem], error) {
	return s.transitionRunMessage(ctx, runID, messageID, key, domain.DeliveryAcknowledged)
}

func (s *Store) transitionRunMessage(ctx context.Context, runID, messageID, key, target string) (MutationResult[domain.InboxItem], error) {
	runID, messageID, key = strings.TrimSpace(runID), strings.TrimSpace(messageID), strings.TrimSpace(key)
	if runID == "" || messageID == "" || key == "" || len(key) > 128 || (target != domain.DeliveryRead && target != domain.DeliveryAcknowledged) {
		return MutationResult[domain.InboxItem]{}, &Error{Code: CodeInvalidMessage, Message: "message transition requires run, message, and bounded idempotency key"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.InboxItem]{}, storageFailure("begin message transition", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", runID)
	if err != nil || !runCanUseMailbox(run.Status) {
		return MutationResult[domain.InboxItem]{}, &Error{Code: CodeMessageDenied, Message: "run mailbox is not active"}
	}
	requestHash, _ := hashCommand("message."+target, map[string]string{"run_id": run.ID, "message_id": messageID})
	scopedKey := "message-" + target + ":" + run.ID + ":" + key
	var replay MutationResult[domain.InboxItem]
	if found, lookupErr := lookupIdempotency(ctx, tx, scopedKey, "message."+target, requestHash, &replay); lookupErr != nil {
		return MutationResult[domain.InboxItem]{}, lookupErr
	} else if found {
		return replay, nil
	}
	item, err := queryInboxItem(ctx, tx, messageID, run.AgentID)
	if err != nil {
		return MutationResult[domain.InboxItem]{}, &Error{Code: CodeMessageNotFound, Message: "message is outside this run mailbox"}
	}
	if item.Message.ProjectID != "" && item.Message.ProjectID != run.ProjectID {
		return MutationResult[domain.InboxItem]{}, &Error{Code: CodeMessageNotFound, Message: "message is outside this run project scope"}
	}
	now := s.nowText()
	sequence := int64(0)
	if item.Delivery.Status == domain.DeliveryQueued {
		if _, err := tx.ExecContext(ctx, "UPDATE message_recipients SET status = 'delivered', delivered_at = ?, delivered_run_id = ? WHERE message_id = ? AND recipient_agent_id = ?", now, run.ID, messageID, run.AgentID); err != nil {
			return MutationResult[domain.InboxItem]{}, storageFailure("deliver message before transition", err)
		}
		sequence, err = appendEventForActor(ctx, tx, run.WorkspaceID, "message", messageID, 2, messageDeliveredEvent, "message-deliver-"+messageID, now, run.ID, "agent_run", map[string]any{"recipient_agent_id": run.AgentID, "run_id": run.ID})
		if err != nil {
			return MutationResult[domain.InboxItem]{}, err
		}
		item.Delivery.Status, item.Delivery.DeliveredAt, item.Delivery.DeliveredRunID = domain.DeliveryDelivered, now, run.ID
	}
	if target == domain.DeliveryRead && item.Delivery.Status == domain.DeliveryDelivered {
		if _, err := tx.ExecContext(ctx, "UPDATE message_recipients SET status = 'read', read_at = ? WHERE message_id = ? AND recipient_agent_id = ?", now, messageID, run.AgentID); err != nil {
			return MutationResult[domain.InboxItem]{}, storageFailure("mark message read", err)
		}
		sequence, err = appendEventForActor(ctx, tx, run.WorkspaceID, "message", messageID, 3, messageReadEvent, "message-read-"+messageID, now, run.ID, "agent_run", map[string]any{"recipient_agent_id": run.AgentID, "run_id": run.ID})
		item.Delivery.Status, item.Delivery.ReadAt = domain.DeliveryRead, now
	}
	if target == domain.DeliveryAcknowledged && item.Delivery.Status != domain.DeliveryAcknowledged {
		if item.Delivery.ReadAt == "" {
			item.Delivery.ReadAt = now
		}
		if _, err := tx.ExecContext(ctx, "UPDATE message_recipients SET status = 'acknowledged', read_at = COALESCE(read_at, ?), acknowledged_at = ? WHERE message_id = ? AND recipient_agent_id = ?", now, now, messageID, run.AgentID); err != nil {
			return MutationResult[domain.InboxItem]{}, storageFailure("acknowledge message", err)
		}
		sequence, err = appendEventForActor(ctx, tx, run.WorkspaceID, "message", messageID, 4, messageAcknowledgedEvent, "message-ack-"+messageID, now, run.ID, "agent_run", map[string]any{"recipient_agent_id": run.AgentID, "run_id": run.ID})
		item.Delivery.Status, item.Delivery.AcknowledgedAt = domain.DeliveryAcknowledged, now
	}
	result := MutationResult[domain.InboxItem]{Value: item, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, scopedKey, "message."+target, requestHash, result, now); err != nil {
		return MutationResult[domain.InboxItem]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.InboxItem]{}, storageFailure("commit message transition", err)
	}
	return result, nil
}

func (s *Store) Thread(ctx context.Context, workspaceIdentifier, threadID string) (domain.ThreadDetail, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.ThreadDetail{}, err
	}
	thread, err := queryMessageThread(ctx, s.db, workspace.ID, strings.TrimSpace(threadID))
	if err != nil {
		return domain.ThreadDetail{}, err
	}
	messages, err := queryThreadMessages(ctx, s.db, thread.ID)
	if err != nil {
		return domain.ThreadDetail{}, err
	}
	recipients, err := queryThreadRecipients(ctx, s.db, thread.ID)
	if err != nil {
		return domain.ThreadDetail{}, err
	}
	return domain.ThreadDetail{Thread: thread, Messages: messages, Recipients: recipients}, nil
}

func (s *Store) ClaimMessageWakeJob(ctx context.Context, lease time.Duration) (domain.MessageWakeJob, bool, error) {
	if lease <= 0 {
		lease = time.Second
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MessageWakeJob{}, false, storageFailure("begin message wake claim", err)
	}
	defer tx.Rollback()
	now, expires := s.clock().UTC(), s.clock().UTC().Add(lease)
	var job domain.MessageWakeJob
	err = tx.QueryRowContext(ctx, `SELECT id, message_id, recipient_agent_id, target_run_id, status, attempts, COALESCE(diagnostic, '')
FROM message_wake_jobs WHERE (status = 'pending' AND available_at <= ?) OR (status = 'leased' AND lease_expires_at <= ?)
ORDER BY sequence LIMIT 1`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)).Scan(&job.ID, &job.MessageID, &job.RecipientAgentID, &job.TargetRunID, &job.Status, &job.Attempts, &job.Diagnostic)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MessageWakeJob{}, false, nil
	}
	if err != nil {
		return domain.MessageWakeJob{}, false, storageFailure("query message wake", err)
	}
	job.Status, job.Attempts = domain.WakeLeased, job.Attempts+1
	if _, err := tx.ExecContext(ctx, "UPDATE message_wake_jobs SET status = 'leased', attempts = ?, lease_expires_at = ?, updated_at = ? WHERE id = ?", job.Attempts, expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), job.ID); err != nil {
		return domain.MessageWakeJob{}, false, storageFailure("lease message wake", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.MessageWakeJob{}, false, storageFailure("commit message wake claim", err)
	}
	return job, true, nil
}

func (s *Store) CompleteMessageWakeJob(ctx context.Context, jobID string, succeeded bool, diagnostic string) error {
	diagnostic = strings.TrimSpace(diagnostic)
	if len(diagnostic) > 1024 {
		maximum := 1024
		for maximum > 0 && !utf8.RuneStart(diagnostic[maximum]) {
			maximum--
		}
		diagnostic = diagnostic[:maximum]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin message wake completion", err)
	}
	defer tx.Rollback()
	var job domain.MessageWakeJob
	var workspaceID, messageProjectID string
	err = tx.QueryRowContext(ctx, `SELECT w.id, COALESCE(m.project_id, ''), j.id, j.message_id, j.recipient_agent_id, j.target_run_id, j.status, j.attempts
FROM message_wake_jobs j JOIN messages m ON m.id = j.message_id JOIN workspaces w ON w.id = m.workspace_id WHERE j.id = ?`, strings.TrimSpace(jobID)).Scan(&workspaceID, &messageProjectID, &job.ID, &job.MessageID, &job.RecipientAgentID, &job.TargetRunID, &job.Status, &job.Attempts)
	if err != nil {
		return storageFailure("query message wake completion", err)
	}
	if job.Status == domain.WakeSucceeded || job.Status == domain.WakeFailed {
		return tx.Commit()
	}
	if job.Status != domain.WakeLeased {
		return &Error{Code: CodeInvalidMessage, Message: "message wake job is not leased"}
	}
	if succeeded {
		var targetStatus, targetAgentID, targetProjectID string
		targetErr := tx.QueryRowContext(ctx, "SELECT status, agent_id, project_id FROM runs WHERE id = ?", job.TargetRunID).Scan(&targetStatus, &targetAgentID, &targetProjectID)
		if targetErr != nil && !errors.Is(targetErr, sql.ErrNoRows) {
			return storageFailure("revalidate message wake target", targetErr)
		}
		if targetErr != nil || targetAgentID != job.RecipientAgentID || !runCanUseMailbox(targetStatus) || (messageProjectID != "" && targetProjectID != messageProjectID) {
			succeeded = false
			diagnostic = "target run is no longer live in the message project for wake-up"
		}
	}
	now := s.nowText()
	status, eventType := domain.WakeFailed, messageWakeFailed
	if succeeded {
		status, eventType = domain.WakeSucceeded, messageWakeSucceeded
	}
	if _, err := tx.ExecContext(ctx, "UPDATE message_wake_jobs SET status = ?, diagnostic = NULLIF(?, ''), lease_expires_at = NULL, updated_at = ? WHERE id = ?", status, diagnostic, now, job.ID); err != nil {
		return storageFailure("complete message wake", err)
	}
	deliveryChanged := false
	if succeeded {
		var deliveryStatus string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM message_recipients WHERE message_id = ? AND recipient_agent_id = ?", job.MessageID, job.RecipientAgentID).Scan(&deliveryStatus); err != nil {
			return storageFailure("query wake delivery", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE message_recipients SET status = CASE WHEN status = 'queued' THEN 'delivered' ELSE status END,
delivered_at = COALESCE(delivered_at, ?), delivered_run_id = COALESCE(delivered_run_id, ?) WHERE message_id = ? AND recipient_agent_id = ?`, now, job.TargetRunID, job.MessageID, job.RecipientAgentID); err != nil {
			return storageFailure("record wake delivery", err)
		}
		deliveryChanged = deliveryStatus == domain.DeliveryQueued
	}
	if _, err := appendEventForActor(ctx, tx, workspaceID, "message", job.MessageID, 1, eventType, "wake-"+job.ID, now, messageWakeActorID, "subsystem", map[string]any{"recipient_agent_id": job.RecipientAgentID, "target_run_id": job.TargetRunID, "diagnostic": diagnostic}); err != nil {
		return err
	}
	if succeeded && deliveryChanged {
		if _, err := appendEventForActor(ctx, tx, workspaceID, "message", job.MessageID, 2, messageDeliveredEvent, "wake-delivery-"+job.ID, now, messageWakeActorID, "subsystem", map[string]any{"recipient_agent_id": job.RecipientAgentID, "run_id": job.TargetRunID}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit message wake completion", err)
	}
	return nil
}

func inboxSummaryInTransaction(ctx context.Context, tx *sql.Tx, agentID, projectID string) (domain.InboxSummary, error) {
	var summary domain.InboxSummary
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_recipients r JOIN messages m ON m.id = r.message_id
WHERE r.recipient_agent_id = ? AND r.status IN ('queued', 'delivered') AND (m.project_id IS NULL OR m.project_id = ?)`, agentID, projectID).Scan(&summary.UnseenCount); err != nil {
		return domain.InboxSummary{}, storageFailure("count context inbox", err)
	}
	items, err := queryInboxWithCondition(ctx, tx, agentID, contextInboxItems, " AND r.status IN ('queued', 'delivered') AND (m.project_id IS NULL OR m.project_id = ?)", projectID)
	if err != nil {
		return domain.InboxSummary{}, err
	}
	summary.Items = make([]domain.InboxSummaryItem, 0, len(items))
	for _, item := range items {
		if item.Delivery.Status != domain.DeliveryQueued && item.Delivery.Status != domain.DeliveryDelivered {
			continue
		}
		summary.Items = append(summary.Items, domain.InboxSummaryItem{MessageID: item.Message.ID, ThreadID: item.Message.ThreadID, Kind: item.Message.Kind, SenderAgentID: item.Message.SenderAgentID, SenderAgentName: item.Message.SenderAgentName, BodyPreview: messagePreview(item.Message.Body, 160), Status: item.Delivery.Status, CreatedAt: item.Message.CreatedAt})
	}
	return summary, nil
}

type messageQueryContext interface {
	queryRower
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryInbox(ctx context.Context, database messageQueryContext, agentID string, limit int) ([]domain.InboxItem, error) {
	return queryInboxWithCondition(ctx, database, agentID, limit, "")
}

func queryInboxWithCondition(ctx context.Context, database messageQueryContext, agentID string, limit int, condition string, conditionArgs ...any) ([]domain.InboxItem, error) {
	query := `SELECT m.id, m.workspace_id, m.thread_id, COALESCE(m.project_id, ''), COALESCE(m.task_id, ''),
m.sender_type, m.sender_id, COALESCE(m.sender_agent_id, ''), COALESCE(a.name, ''), COALESCE(m.sender_run_id, ''), m.kind, m.body,
m.artifact_ids_json, COALESCE(m.reply_to_message_id, ''), m.created_at, r.recipient_agent_id, ra.name, r.status, r.queued_at,
COALESCE(r.delivered_at, ''), COALESCE(r.read_at, ''), COALESCE(r.acknowledged_at, ''), COALESCE(r.delivered_run_id, ''),
COALESCE((SELECT j.status FROM message_wake_jobs j WHERE j.message_id = m.id AND j.recipient_agent_id = r.recipient_agent_id ORDER BY j.sequence DESC LIMIT 1), 'not_requested'),
COALESCE((SELECT j.diagnostic FROM message_wake_jobs j WHERE j.message_id = m.id AND j.recipient_agent_id = r.recipient_agent_id ORDER BY j.sequence DESC LIMIT 1), '')
FROM message_recipients r JOIN messages m ON m.id = r.message_id
JOIN agents ra ON ra.id = r.recipient_agent_id LEFT JOIN agents a ON a.id = m.sender_agent_id
WHERE r.recipient_agent_id = ?` + condition + ` ORDER BY m.created_at, m.id LIMIT ?`
	arguments := make([]any, 0, len(conditionArgs)+2)
	arguments = append(arguments, agentID)
	arguments = append(arguments, conditionArgs...)
	arguments = append(arguments, limit)
	rows, err := database.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, storageFailure("query inbox", err)
	}
	defer rows.Close()
	result := make([]domain.InboxItem, 0)
	for rows.Next() {
		item, scanErr := scanInboxItem(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func queryInboxItem(ctx context.Context, database queryRower, messageID, agentID string) (domain.InboxItem, error) {
	row := database.QueryRowContext(ctx, `SELECT m.id, m.workspace_id, m.thread_id, COALESCE(m.project_id, ''), COALESCE(m.task_id, ''),
m.sender_type, m.sender_id, COALESCE(m.sender_agent_id, ''), COALESCE(a.name, ''), COALESCE(m.sender_run_id, ''), m.kind, m.body,
m.artifact_ids_json, COALESCE(m.reply_to_message_id, ''), m.created_at, r.recipient_agent_id, ra.name, r.status, r.queued_at,
COALESCE(r.delivered_at, ''), COALESCE(r.read_at, ''), COALESCE(r.acknowledged_at, ''), COALESCE(r.delivered_run_id, ''),
COALESCE((SELECT j.status FROM message_wake_jobs j WHERE j.message_id = m.id AND j.recipient_agent_id = r.recipient_agent_id ORDER BY j.sequence DESC LIMIT 1), 'not_requested'),
COALESCE((SELECT j.diagnostic FROM message_wake_jobs j WHERE j.message_id = m.id AND j.recipient_agent_id = r.recipient_agent_id ORDER BY j.sequence DESC LIMIT 1), '')
FROM message_recipients r JOIN messages m ON m.id = r.message_id
JOIN agents ra ON ra.id = r.recipient_agent_id LEFT JOIN agents a ON a.id = m.sender_agent_id
WHERE m.id = ? AND r.recipient_agent_id = ?`, messageID, agentID)
	return scanInboxItem(row)
}

func scanInboxItem(scanner rowScanner) (domain.InboxItem, error) {
	var item domain.InboxItem
	var artifactsJSON string
	err := scanner.Scan(&item.Message.ID, &item.Message.WorkspaceID, &item.Message.ThreadID, &item.Message.ProjectID, &item.Message.TaskID,
		&item.Message.SenderType, &item.Message.SenderID, &item.Message.SenderAgentID, &item.Message.SenderAgentName, &item.Message.SenderRunID,
		&item.Message.Kind, &item.Message.Body, &artifactsJSON, &item.Message.ReplyToMessageID, &item.Message.CreatedAt,
		&item.Delivery.RecipientAgentID, &item.Delivery.RecipientName, &item.Delivery.Status, &item.Delivery.QueuedAt,
		&item.Delivery.DeliveredAt, &item.Delivery.ReadAt, &item.Delivery.AcknowledgedAt, &item.Delivery.DeliveredRunID,
		&item.Delivery.WakeStatus, &item.Delivery.WakeDiagnostic)
	if err != nil {
		return domain.InboxItem{}, err
	}
	item.Delivery.MessageID = item.Message.ID
	if err := json.Unmarshal([]byte(artifactsJSON), &item.Message.ArtifactIDs); err != nil {
		return domain.InboxItem{}, storageFailure("decode message artifacts", err)
	}
	return item, nil
}

func queryMessageThread(ctx context.Context, database queryRower, workspaceID, threadID string) (domain.MessageThread, error) {
	var thread domain.MessageThread
	err := database.QueryRowContext(ctx, `SELECT id, workspace_id, COALESCE(project_id, ''), COALESCE(task_id, ''), subject, status, revision, created_at, updated_at, created_by, updated_by
FROM message_threads WHERE id = ? AND workspace_id = ?`, threadID, workspaceID).Scan(&thread.ID, &thread.WorkspaceID, &thread.ProjectID, &thread.TaskID, &thread.Subject, &thread.Status, &thread.Revision, &thread.CreatedAt, &thread.UpdatedAt, &thread.CreatedBy, &thread.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MessageThread{}, &Error{Code: CodeMessageNotFound, Message: fmt.Sprintf("thread %q was not found", threadID)}
	}
	if err != nil {
		return domain.MessageThread{}, storageFailure("query message thread", err)
	}
	return thread, nil
}

func queryThreadMessages(ctx context.Context, database messageQueryContext, threadID string) ([]domain.Message, error) {
	rows, err := database.QueryContext(ctx, `SELECT m.id, m.workspace_id, m.thread_id, COALESCE(m.project_id, ''), COALESCE(m.task_id, ''), m.sender_type, m.sender_id,
COALESCE(m.sender_agent_id, ''), COALESCE(a.name, ''), COALESCE(m.sender_run_id, ''), m.kind, m.body, m.artifact_ids_json, COALESCE(m.reply_to_message_id, ''), m.created_at
FROM messages m LEFT JOIN agents a ON a.id = m.sender_agent_id WHERE m.thread_id = ? ORDER BY m.created_at, m.id`, threadID)
	if err != nil {
		return nil, storageFailure("query thread messages", err)
	}
	defer rows.Close()
	result := make([]domain.Message, 0)
	for rows.Next() {
		var message domain.Message
		var artifactsJSON string
		if err := rows.Scan(&message.ID, &message.WorkspaceID, &message.ThreadID, &message.ProjectID, &message.TaskID, &message.SenderType, &message.SenderID, &message.SenderAgentID, &message.SenderAgentName, &message.SenderRunID, &message.Kind, &message.Body, &artifactsJSON, &message.ReplyToMessageID, &message.CreatedAt); err != nil {
			return nil, storageFailure("scan thread message", err)
		}
		if err := json.Unmarshal([]byte(artifactsJSON), &message.ArtifactIDs); err != nil {
			return nil, storageFailure("decode thread message artifacts", err)
		}
		result = append(result, message)
	}
	return result, rows.Err()
}

func queryThreadRecipients(ctx context.Context, database messageQueryContext, threadID string) ([]domain.MessageDelivery, error) {
	rows, err := database.QueryContext(ctx, `SELECT r.message_id, r.recipient_agent_id, a.name, r.status, r.queued_at,
COALESCE(r.delivered_at, ''), COALESCE(r.read_at, ''), COALESCE(r.acknowledged_at, ''), COALESCE(r.delivered_run_id, ''),
COALESCE((SELECT j.status FROM message_wake_jobs j WHERE j.message_id = r.message_id AND j.recipient_agent_id = r.recipient_agent_id ORDER BY j.sequence DESC LIMIT 1), 'not_requested'),
COALESCE((SELECT j.diagnostic FROM message_wake_jobs j WHERE j.message_id = r.message_id AND j.recipient_agent_id = r.recipient_agent_id ORDER BY j.sequence DESC LIMIT 1), '')
FROM message_recipients r JOIN messages m ON m.id = r.message_id JOIN agents a ON a.id = r.recipient_agent_id
WHERE m.thread_id = ? ORDER BY m.created_at, m.id, a.name`, threadID)
	if err != nil {
		return nil, storageFailure("query thread recipients", err)
	}
	defer rows.Close()
	result := make([]domain.MessageDelivery, 0)
	for rows.Next() {
		var delivery domain.MessageDelivery
		if err := rows.Scan(&delivery.MessageID, &delivery.RecipientAgentID, &delivery.RecipientName, &delivery.Status, &delivery.QueuedAt, &delivery.DeliveredAt, &delivery.ReadAt, &delivery.AcknowledgedAt, &delivery.DeliveredRunID, &delivery.WakeStatus, &delivery.WakeDiagnostic); err != nil {
			return nil, storageFailure("scan thread recipient", err)
		}
		result = append(result, delivery)
	}
	return result, rows.Err()
}

func resolveMessageScope(ctx context.Context, database queryRower, workspaceID, projectIdentifier, taskID string) (string, string, error) {
	projectID := ""
	if taskID != "" {
		task, err := queryTask(ctx, database, workspaceID, taskID)
		if err != nil {
			return "", "", err
		}
		projectID = task.ProjectID
	}
	if projectIdentifier != "" {
		project, err := queryProject(ctx, database, workspaceID, projectIdentifier)
		if err != nil {
			return "", "", err
		}
		if projectID != "" && project.ID != projectID {
			return "", "", &Error{Code: CodeInvalidMessage, Message: "message task and project scope differ"}
		}
		projectID = project.ID
	}
	return projectID, taskID, nil
}

func validateMessageArtifacts(ctx context.Context, database queryRower, senderRunID string, artifactIDs []string) error {
	if len(artifactIDs) == 0 {
		return nil
	}
	if senderRunID == "" {
		return &Error{Code: CodeMessageDenied, Message: "owner messages cannot attach run-scoped artifacts through this command"}
	}
	for _, artifactID := range artifactIDs {
		var count int
		if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM run_artifacts WHERE id = ? AND run_id = ?", artifactID, senderRunID).Scan(&count); err != nil {
			return storageFailure("validate message artifact", err)
		}
		if count != 1 {
			return &Error{Code: CodeMessageDenied, Message: "message artifacts must belong to the authenticated sender run"}
		}
	}
	return nil
}

func messageThreadHasAgent(ctx context.Context, database queryRower, threadID, agentID string) (bool, error) {
	var count int
	err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
SELECT sender_agent_id AS agent_id FROM messages WHERE thread_id = ? AND sender_agent_id IS NOT NULL
UNION SELECT r.recipient_agent_id AS agent_id FROM message_recipients r JOIN messages m ON m.id = r.message_id WHERE m.thread_id = ?
) WHERE agent_id = ?`, threadID, threadID, agentID).Scan(&count)
	if err != nil {
		return false, storageFailure("query thread participant", err)
	}
	return count > 0, nil
}

func runCanUseMailbox(status string) bool {
	return status == domain.RunStarting || status == domain.RunActive || status == domain.RunBlocked
}

func normalizedIdentifiers(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func validMessageText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maximum && !strings.ContainsRune(value, '\x00')
}

func messagePreview(value string, maximum int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.RuneStart(value[maximum]) {
		maximum--
	}
	return strings.TrimSpace(value[:maximum]) + "…"
}
