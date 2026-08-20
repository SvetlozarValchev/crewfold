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
	"crewfold/internal/store/dbgen"
)

const (
	threadCreatedEvent        = "thread.created"
	messageSentEvent          = "message.sent"
	messageDeliveredEvent     = "message.delivered"
	messageReadEvent          = "message.read"
	messageAcknowledgedEvent  = "message.acknowledged"
	messageWakeSucceeded      = "message.wake_succeeded"
	messageWakeFailed         = "message.wake_failed"
	messageWakeFailedUnknown  = "message.wake_failed_unknown"
	threadParticipantAdded    = "thread.participant_added"
	messageWakeActorID        = "message-wake-worker"
	maximumMessageBytes       = 4096
	maximumMessageArtifacts   = 16
	maximumInboxItems         = 50
	maximumThreadListItems    = 50
	contextInboxItems         = 10
	minimumThreadParticipants = 2
	maximumThreadParticipants = 8
	maximumWakeRecoveryBatch  = 16
)

var supportedMessageKinds = map[string]bool{
	domain.MessageInform: true, domain.MessageQuestion: true, domain.MessageRequest: true,
	domain.MessageReviewRequest: true, domain.MessageHandoff: true,
	domain.MessageDecisionNotice: true, domain.MessageRisk: true,
	domain.MessageConflict: true, domain.MessageApprovalRequest: true,
}

func (s *Store) CreateParticipantThread(ctx context.Context, command CreateParticipantThreadCommand) (ParticipantThreadMutationResult, error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	subject := strings.TrimSpace(command.Subject)
	key, correlationID := strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	inputs := normalizeParticipantInputs(command.Participants)
	if workspaceIdentifier == "" || !validMessageText(subject, 160) || len(inputs) < minimumThreadParticipants || len(inputs) > maximumThreadParticipants || len(inputs) != len(command.Participants) {
		return ParticipantThreadMutationResult{}, &Error{Code: CodeInvalidMessage, Message: "participant thread requires a UTF-8 subject from 1 to 160 bytes and 2 to 8 distinct agent/task bindings"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidMessage); err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return ParticipantThreadMutationResult{}, storageFailure("begin participant thread creation", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	requestHash, err := hashCommand("participant-thread.create", map[string]any{"workspace_id": workspace.ID, "subject": subject, "participants": inputs})
	if err != nil {
		return ParticipantThreadMutationResult{}, storageFailure("hash participant thread creation", err)
	}
	scopedKey := "participant-thread-create:" + localOwnerActorID + ":" + key
	var replay ParticipantThreadMutationResult
	if found, lookupErr := lookupIdempotency(ctx, tx, scopedKey, "participant-thread.create", requestHash, &replay); lookupErr != nil {
		return ParticipantThreadMutationResult{}, lookupErr
	} else if found {
		return replay, nil
	}
	now := s.nowText()
	bindings := make([]domain.ThreadParticipant, 0, len(inputs))
	projects := make(map[string]bool)
	seenAgents := make(map[string]bool, len(inputs))
	seenTasks := make(map[string]bool, len(inputs))
	for index, input := range inputs {
		participant, resolveErr := s.resolveParticipantBinding(ctx, tx, workspace.ID, input, now, index+1)
		if resolveErr != nil {
			return ParticipantThreadMutationResult{}, resolveErr
		}
		if seenAgents[participant.AgentID] || seenTasks[participant.TaskID] {
			return ParticipantThreadMutationResult{}, &Error{Code: CodeInvalidMessage, Message: "participant agents and tasks must each be unique within a thread"}
		}
		seenAgents[participant.AgentID], seenTasks[participant.TaskID] = true, true
		projects[participant.ProjectID] = true
		bindings = append(bindings, participant)
	}
	if len(projects) < 2 {
		return ParticipantThreadMutationResult{}, &Error{Code: CodeInvalidMessage, Message: "participant thread creation must span at least two projects"}
	}
	threadID, err := randomID("thread_")
	if err != nil {
		return ParticipantThreadMutationResult{}, storageFailure("generate participant thread id", err)
	}
	queries := dbgen.New(tx)
	if err := queries.InsertParticipantThread(ctx, dbgen.InsertParticipantThreadParams{ID: threadID, WorkspaceID: workspace.ID, Subject: subject, CreatedAt: now, CreatedBy: localOwnerActorID, InitialParticipantCount: int64(len(bindings))}); err != nil {
		return ParticipantThreadMutationResult{}, storageFailure("insert participant thread", err)
	}
	for index := range bindings {
		bindings[index].ThreadID = threadID
		if err := insertThreadParticipant(ctx, queries, bindings[index]); err != nil {
			return ParticipantThreadMutationResult{}, err
		}
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	collaboration, err := participantThreadInTransaction(ctx, queries, workspace.ID, threadID)
	if err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	sequence, err := appendEventForActor(ctx, tx, workspace.ID, "thread", threadID, collaboration.Thread.Revision, threadCreatedEvent, correlationID, now, localOwnerActorID, localActorType, map[string]any{
		"kind": domain.ThreadKindParticipantBound, "participant_revision": collaboration.ParticipantRevision,
		"participant_count": len(bindings), "participants": participantEventValues(bindings), "subject": subject,
	})
	if err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	result := ParticipantThreadMutationResult{Collaboration: collaboration, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, scopedKey, "participant-thread.create", requestHash, result, now); err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ParticipantThreadMutationResult{}, storageFailure("commit participant thread creation", err)
	}
	return result, nil
}

func (s *Store) InviteThreadParticipant(ctx context.Context, command InviteThreadParticipantCommand) (ParticipantThreadMutationResult, error) {
	workspaceIdentifier, threadID := strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.ThreadID)
	key, correlationID := strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	input := domain.ParticipantBindingInput{AgentIdentifier: strings.TrimSpace(command.Participant.AgentIdentifier), TaskIdentifier: strings.TrimSpace(command.Participant.TaskIdentifier)}
	if workspaceIdentifier == "" || threadID == "" || input.AgentIdentifier == "" || input.TaskIdentifier == "" || command.ExpectedParticipantRevision < 1 {
		return ParticipantThreadMutationResult{}, &Error{Code: CodeInvalidMessage, Message: "participant invitation requires a thread, agent/task binding, and positive expected participant revision"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidMessage); err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return ParticipantThreadMutationResult{}, storageFailure("begin participant invitation", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	requestHash, err := hashCommand("participant-thread.invite", map[string]any{"workspace_id": workspace.ID, "thread_id": threadID, "participant": input, "expected_participant_revision": command.ExpectedParticipantRevision})
	if err != nil {
		return ParticipantThreadMutationResult{}, storageFailure("hash participant invitation", err)
	}
	scopedKey := "participant-thread-invite:" + localOwnerActorID + ":" + key
	var replay ParticipantThreadMutationResult
	if found, lookupErr := lookupIdempotency(ctx, tx, scopedKey, "participant-thread.invite", requestHash, &replay); lookupErr != nil {
		return ParticipantThreadMutationResult{}, lookupErr
	} else if found {
		return replay, nil
	}
	queries := dbgen.New(tx)
	current, err := participantThreadInTransaction(ctx, queries, workspace.ID, threadID)
	if err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	if current.Thread.Status != domain.ThreadOpen {
		return ParticipantThreadMutationResult{}, &Error{Code: CodeMessageDenied, Message: "participant thread is closed"}
	}
	if current.ParticipantRevision != command.ExpectedParticipantRevision {
		return ParticipantThreadMutationResult{}, revisionConflict("participant thread", threadID, command.ExpectedParticipantRevision, current.ParticipantRevision)
	}
	if len(current.Participants) >= maximumThreadParticipants {
		return ParticipantThreadMutationResult{}, &Error{Code: CodeInvalidMessage, Message: "participant thread already has the maximum of 8 bindings"}
	}
	now := s.nowText()
	participant, err := s.resolveParticipantBinding(ctx, tx, workspace.ID, input, now, len(current.Participants)+1)
	if err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	for _, existing := range current.Participants {
		if existing.AgentID == participant.AgentID || existing.TaskID == participant.TaskID {
			return ParticipantThreadMutationResult{}, &Error{Code: CodeInvalidMessage, Message: "participant agent and task must each be new to this thread"}
		}
	}
	participant.ThreadID = threadID
	if err := insertThreadParticipant(ctx, queries, participant); err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	updated, err := participantThreadInTransaction(ctx, queries, workspace.ID, threadID)
	if err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	sequence, err := appendEventForActor(ctx, tx, workspace.ID, "thread", threadID, updated.Thread.Revision, threadParticipantAdded, correlationID, now, localOwnerActorID, localActorType, map[string]any{
		"kind": domain.ThreadKindParticipantBound, "participant_revision": updated.ParticipantRevision, "participant": participantEventValue(participant),
	})
	if err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	result := ParticipantThreadMutationResult{Collaboration: updated, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, scopedKey, "participant-thread.invite", requestHash, result, now); err != nil {
		return ParticipantThreadMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ParticipantThreadMutationResult{}, storageFailure("commit participant invitation", err)
	}
	return result, nil
}

func (s *Store) ParticipantThread(ctx context.Context, workspaceIdentifier, threadID string) (domain.ParticipantThread, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.ParticipantThread{}, err
	}
	return participantThreadInTransaction(ctx, dbgen.New(s.db), workspace.ID, strings.TrimSpace(threadID))
}

func normalizeParticipantInputs(values []domain.ParticipantBindingInput) []domain.ParticipantBindingInput {
	result := make([]domain.ParticipantBindingInput, 0, len(values))
	for _, value := range values {
		agent, task := strings.TrimSpace(value.AgentIdentifier), strings.TrimSpace(value.TaskIdentifier)
		if agent == "" || task == "" || len(agent) > 128 || len(task) > 128 || strings.ContainsAny(agent+task, "\r\n\x00") {
			continue
		}
		result = append(result, domain.ParticipantBindingInput{AgentIdentifier: agent, TaskIdentifier: task})
	}
	return result
}

func (s *Store) resolveParticipantBinding(ctx context.Context, tx *sql.Tx, workspaceID string, input domain.ParticipantBindingInput, now string, ordinal int) (domain.ThreadParticipant, error) {
	agent, err := queryAgent(ctx, tx, workspaceID, input.AgentIdentifier)
	if err != nil {
		return domain.ThreadParticipant{}, &Error{Code: CodeMessageDenied, Message: "participant agent is outside the workspace or unavailable", Cause: err}
	}
	if !agent.Enabled {
		return domain.ThreadParticipant{}, &Error{Code: CodeMessageDenied, Message: "participant agent must be enabled"}
	}
	task, err := queryTask(ctx, tx, workspaceID, input.TaskIdentifier)
	if err != nil {
		return domain.ThreadParticipant{}, &Error{Code: CodeMessageDenied, Message: "participant task is outside the workspace or unavailable", Cause: err}
	}
	assignment, err := dbgen.New(tx).GetEligibleParticipantAssignment(ctx, dbgen.GetEligibleParticipantAssignmentParams{TaskID: task.ID, AgentID: agent.ID, ObservedAt: now})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadParticipant{}, &Error{Code: CodeMessageDenied, Message: "participant task requires an active unexpired assignment to the exact agent"}
	}
	if err != nil {
		return domain.ThreadParticipant{}, storageFailure("resolve eligible participant assignment", err)
	}
	participantID, err := randomID("participant_")
	if err != nil {
		return domain.ThreadParticipant{}, storageFailure("generate thread participant id", err)
	}
	project, err := queryProject(ctx, tx, workspaceID, task.ProjectID)
	if err != nil {
		return domain.ThreadParticipant{}, err
	}
	return domain.ThreadParticipant{ID: participantID, WorkspaceID: workspaceID, AgentID: agent.ID, AgentName: agent.Name, TaskID: task.ID, TaskTitle: task.Title, ProjectID: task.ProjectID, ProjectName: project.Name, AssignmentID: assignment.AssignmentID, AssignmentRevision: assignment.AssignmentRevision, AgentRevision: agent.Revision, TaskRevision: task.Revision, Ordinal: ordinal, Status: domain.ThreadParticipantActive, InvitedAt: now, InvitedBy: localOwnerActorID}, nil
}

func insertThreadParticipant(ctx context.Context, queries *dbgen.Queries, participant domain.ThreadParticipant) error {
	err := queries.InsertThreadParticipant(ctx, dbgen.InsertThreadParticipantParams{ID: participant.ID, ThreadID: participant.ThreadID, WorkspaceID: participant.WorkspaceID, AgentID: participant.AgentID, AgentName: participant.AgentName, TaskID: participant.TaskID, TaskTitle: participant.TaskTitle, ProjectID: participant.ProjectID, ProjectName: participant.ProjectName, AssignmentID: participant.AssignmentID, AssignmentRevision: participant.AssignmentRevision, AgentRevision: participant.AgentRevision, TaskRevision: participant.TaskRevision, Ordinal: int64(participant.Ordinal), InvitedAt: participant.InvitedAt, InvitedBy: participant.InvitedBy})
	if err != nil {
		return storageFailure("insert thread participant", err)
	}
	return nil
}

func participantThreadInTransaction(ctx context.Context, queries *dbgen.Queries, workspaceID, threadID string) (domain.ParticipantThread, error) {
	row, err := queries.GetParticipantThreadState(ctx, dbgen.GetParticipantThreadStateParams{ID: threadID, WorkspaceID: workspaceID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ParticipantThread{}, &Error{Code: CodeMessageNotFound, Message: fmt.Sprintf("participant thread %q was not found", threadID)}
	}
	if err != nil {
		return domain.ParticipantThread{}, storageFailure("query participant thread", err)
	}
	rows, err := queries.ListThreadParticipants(ctx, threadID)
	if err != nil {
		return domain.ParticipantThread{}, storageFailure("list thread participants", err)
	}
	participants := make([]domain.ThreadParticipant, 0, len(rows))
	projects := make(map[string]bool)
	for _, participant := range rows {
		participants = append(participants, threadParticipantFromDB(participant))
		projects[participant.ProjectID] = true
	}
	if row.InitialParticipantCount < minimumThreadParticipants || row.InitialParticipantCount > maximumThreadParticipants || len(participants) < int(row.InitialParticipantCount) || len(participants) > maximumThreadParticipants || row.ParticipantRevision != int64(len(participants))-row.InitialParticipantCount+1 || len(projects) < 2 {
		return domain.ParticipantThread{}, &Error{Code: CodeStorageFailed, Message: "participant thread roster is incomplete or inconsistent"}
	}
	thread := domain.MessageThread{ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, TaskID: row.TaskID, Subject: row.Subject, Status: row.Status, Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy}
	return domain.ParticipantThread{Thread: thread, Kind: row.Kind, ParticipantRevision: row.ParticipantRevision, Participants: participants}, nil
}

func participantEventValues(participants []domain.ThreadParticipant) []map[string]any {
	values := make([]map[string]any, 0, len(participants))
	for _, participant := range participants {
		values = append(values, participantEventValue(participant))
	}
	return values
}

func participantEventValue(participant domain.ThreadParticipant) map[string]any {
	return map[string]any{"participant_id": participant.ID, "agent_id": participant.AgentID, "task_id": participant.TaskID, "project_id": participant.ProjectID, "assignment_id": participant.AssignmentID, "assignment_revision": participant.AssignmentRevision, "agent_revision": participant.AgentRevision, "task_revision": participant.TaskRevision, "ordinal": participant.Ordinal}
}

func threadParticipantFromDB(participant dbgen.ThreadParticipant) domain.ThreadParticipant {
	return domain.ThreadParticipant{ID: participant.ID, ThreadID: participant.ThreadID, WorkspaceID: participant.WorkspaceID, AgentID: participant.AgentID, AgentName: participant.AgentName, TaskID: participant.TaskID, TaskTitle: participant.TaskTitle, ProjectID: participant.ProjectID, ProjectName: participant.ProjectName, AssignmentID: participant.AssignmentID, AssignmentRevision: participant.AssignmentRevision, AgentRevision: participant.AgentRevision, TaskRevision: participant.TaskRevision, Ordinal: int(participant.Ordinal), Status: participant.Status, InvitedAt: participant.InvitedAt, InvitedBy: participant.InvitedBy}
}

func findThreadParticipantBinding(ctx context.Context, tx *sql.Tx, threadID, agentID, projectID, taskID string) (domain.ThreadParticipant, error) {
	row, err := dbgen.New(tx).FindThreadParticipantByBinding(ctx, dbgen.FindThreadParticipantByBindingParams{ThreadID: threadID, AgentID: agentID, TaskID: taskID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadParticipant{}, nil
	}
	if err != nil {
		return domain.ThreadParticipant{}, storageFailure("query exact thread participant", err)
	}
	participant := threadParticipantFromDB(row)
	if participant.ProjectID != projectID || participant.Status != domain.ThreadParticipantActive {
		return domain.ThreadParticipant{}, nil
	}
	return participant, nil
}

func runAuthorizedForMessage(ctx context.Context, tx *sql.Tx, run domain.Run, messageID string) (bool, error) {
	authorized, err := dbgen.New(tx).RunAuthorizedForMessage(ctx, dbgen.RunAuthorizedForMessageParams{RunID: run.ID, MessageID: messageID})
	if err != nil {
		return false, storageFailure("authorize run message binding", err)
	}
	return authorized == 1, nil
}

func (s *Store) SendMessage(ctx context.Context, command SendMessageCommand) (MutationResult[domain.MessageMutation], error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	recipientIdentifier := strings.TrimSpace(command.RecipientAgent)
	senderRunID, senderDomainThreadID, threadID := strings.TrimSpace(command.SenderRunID), strings.TrimSpace(command.SenderDomainThreadID), strings.TrimSpace(command.ThreadID)
	projectIdentifier, taskID := strings.TrimSpace(command.ProjectIdentifier), strings.TrimSpace(command.TaskID)
	kind, subject, body := strings.TrimSpace(command.Kind), strings.TrimSpace(command.Subject), strings.TrimSpace(command.Body)
	replyTo, key, correlationID := strings.TrimSpace(command.ReplyToMessageID), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	artifactIDs := normalizedIdentifiers(command.ArtifactIDs)
	if workspaceIdentifier == "" || recipientIdentifier == "" || !supportedMessageKinds[kind] || !validMessageText(body, maximumMessageBytes) || len(artifactIDs) > maximumMessageArtifacts {
		return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeInvalidMessage, Message: "message requires workspace, one agent recipient, supported kind, and a UTF-8 body from 1 to 4096 bytes"}
	}
	if senderRunID != "" && senderDomainThreadID != "" {
		return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeInvalidMessage, Message: "message must have only one authenticated sender origin"}
	}
	if len(command.ArtifactIDs) != len(artifactIDs) {
		return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeInvalidMessage, Message: "message artifact identifiers must be unique, bounded, and non-empty"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidMessage); err != nil {
		return MutationResult[domain.MessageMutation]{}, err
	}

	tx, err := s.beginTx(ctx, nil)
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
	} else if senderDomainThreadID != "" {
		scope, scopeErr := s.domainAgentSessionScopeInTransaction(ctx, tx, senderDomainThreadID)
		if scopeErr != nil || scope.Workspace.ID != workspace.ID {
			return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "durable-agent sender session is outside the authorized workspace", Cause: scopeErr}
		}
		senderType, senderID, senderAgentID, senderAgentName, actorType = "durable_agent", scope.Agent.ID, scope.Agent.ID, scope.Agent.Name, domain.EventActorIntegration
		projectIdentifier, taskID = scope.Project.ID, ""
	}
	recipient, err := queryAgent(ctx, tx, workspace.ID, recipientIdentifier)
	if err != nil {
		return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "recipient must be one enabled agent in this workspace"}
	}
	if senderAgentID != "" && recipient.ID == senderAgentID {
		return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "an agent cannot message itself"}
	}

	projectID, resolvedTaskID, err := resolveMessageScope(ctx, tx, workspace.ID, projectIdentifier, taskID)
	if err != nil {
		return MutationResult[domain.MessageMutation]{}, err
	}
	if senderDomainThreadID != "" {
		recipientMembership, membershipErr := queryDomainAgentMembership(ctx, tx, projectID, recipient.ID)
		if membershipErr != nil || recipientMembership.Status != domain.DomainAgentActive {
			return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "recipient must be one active durable agent in the sender's domain", Cause: membershipErr}
		}
	}
	request := map[string]any{
		"workspace_id": workspace.ID, "sender_run_id": senderRunID, "sender_domain_thread_id": senderDomainThreadID, "recipient_agent_id": recipient.ID,
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
	threadKind := domain.ThreadKindDirect
	var recipientParticipant domain.ThreadParticipant
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
		sequence, err = appendEventForActor(ctx, tx, workspace.ID, "thread", thread.ID, thread.Revision, threadCreatedEvent, correlationID, now, senderID, actorType, map[string]any{"kind": domain.ThreadKindDirect, "project_id": projectID, "task_id": resolvedTaskID, "subject": subject})
		if err != nil {
			return MutationResult[domain.MessageMutation]{}, err
		}
	} else {
		thread, err = queryMessageThread(ctx, tx, workspace.ID, threadID)
		if err != nil {
			return MutationResult[domain.MessageMutation]{}, err
		}
		if err := tx.QueryRowContext(ctx, "SELECT kind FROM message_threads WHERE id = ? AND workspace_id = ?", thread.ID, workspace.ID).Scan(&threadKind); err != nil {
			return MutationResult[domain.MessageMutation]{}, storageFailure("query message thread kind", err)
		}
		if thread.Status != domain.ThreadOpen || (threadKind == domain.ThreadKindDirect && projectID != "" && thread.ProjectID != "" && projectID != thread.ProjectID) {
			return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "thread is closed or outside the sender scope"}
		}
		if threadKind == domain.ThreadKindParticipantBound {
			if _, rosterErr := participantThreadInTransaction(ctx, dbgen.New(tx), workspace.ID, thread.ID); rosterErr != nil {
				return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "participant thread roster is incomplete or inconsistent", Cause: rosterErr}
			}
			if len(artifactIDs) != 0 {
				return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "participant-bound messages cannot attach artifacts"}
			}
			if senderDomainThreadID != "" {
				return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "durable-agent sessions cannot send as task-bound thread participants"}
			}
			if senderAgentID != "" {
				senderParticipant, participantErr := findThreadParticipantBinding(ctx, tx, thread.ID, senderAgentID, senderRun.ProjectID, senderRun.TaskID)
				if participantErr != nil || senderParticipant.ID == "" {
					return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "sender run must exactly match an active participant agent/project/task binding"}
				}
			} else if projectID != "" || resolvedTaskID != "" {
				return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "owner participant-thread messages cannot claim a project or task origin"}
			}
			matches, participantErr := dbgen.New(tx).ListThreadParticipantsByAgent(ctx, dbgen.ListThreadParticipantsByAgentParams{ThreadID: thread.ID, AgentID: recipient.ID})
			if participantErr != nil {
				return MutationResult[domain.MessageMutation]{}, storageFailure("resolve participant recipient", participantErr)
			}
			if len(matches) != 1 {
				return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "recipient must resolve to exactly one active thread binding"}
			}
			recipientParticipant = threadParticipantFromDB(matches[0])
		} else {
			if senderAgentID != "" {
				senderParticipant, participantErr := messageThreadHasAgent(ctx, tx, thread.ID, senderAgentID)
				if participantErr != nil {
					return MutationResult[domain.MessageMutation]{}, participantErr
				}
				recipientInThread, participantErr := messageThreadHasAgent(ctx, tx, thread.ID, recipient.ID)
				if participantErr != nil {
					return MutationResult[domain.MessageMutation]{}, participantErr
				}
				if !senderParticipant || !recipientInThread {
					return MutationResult[domain.MessageMutation]{}, &Error{Code: CodeMessageDenied, Message: "agent replies are limited to existing thread participants"}
				}
			}
			projectID, resolvedTaskID = thread.ProjectID, thread.TaskID
		}
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
	if _, err := tx.ExecContext(ctx, "INSERT INTO message_recipients(message_id, recipient_agent_id, status, queued_at, recipient_participant_id) VALUES (?, ?, ?, ?, NULLIF(?, ''))", message.ID, recipient.ID, domain.DeliveryQueued, now, recipientParticipant.ID); err != nil {
		return MutationResult[domain.MessageMutation]{}, storageFailure("queue message recipient", err)
	}
	thread.Revision++
	thread.UpdatedAt, thread.UpdatedBy = now, senderID
	if _, err := tx.ExecContext(ctx, "UPDATE message_threads SET revision = ?, updated_at = ?, updated_by = ? WHERE id = ?", thread.Revision, now, senderID, thread.ID); err != nil {
		return MutationResult[domain.MessageMutation]{}, storageFailure("update message thread", err)
	}
	sequence, err = appendEventForActor(ctx, tx, workspace.ID, "message", message.ID, 1, messageSentEvent, correlationID, now, senderID, actorType, map[string]any{
		"thread_id": thread.ID, "thread_kind": threadKind, "recipient_agent_id": recipient.ID,
		"recipient_participant_id": recipientParticipant.ID, "kind": kind,
		"origin_project_id": projectID, "origin_task_id": resolvedTaskID, "artifact_ids": artifactIDs,
	})
	if err != nil {
		return MutationResult[domain.MessageMutation]{}, err
	}
	if senderRunID != "" && projectID != "" {
		if err := enqueueOwnerManagerReview(ctx, tx, workspace.ID, projectID, sequence, now); err != nil {
			return MutationResult[domain.MessageMutation]{}, err
		}
	}
	wakeStatus := domain.WakeNotRequested
	var targetRunID, targetDomainThreadID string
	wakeProjectID := projectID
	if threadKind == domain.ThreadKindParticipantBound {
		wakeProjectID = recipientParticipant.ProjectID
	}
	// A durable agent has one provider identity. Prefer its domain thread even
	// while an accepted task run is active; the daemon will steer that exact
	// turn. Routing to the run's ordinary runtime driver would create a second
	// control plane and cannot decode the durable-thread binding.
	if wakeProjectID != "" {
		err = tx.QueryRowContext(ctx, `SELECT binding.thread_id
FROM domain_agent_session_bindings binding
JOIN domain_agent_memberships membership
  ON membership.project_id=binding.project_id AND membership.agent_id=binding.agent_id
JOIN agents agent ON agent.id=binding.agent_id
WHERE binding.project_id=? AND binding.agent_id=?
  AND membership.status='active' AND agent.enabled=1`, wakeProjectID, recipient.ID).Scan(&targetDomainThreadID)
	} else {
		err = sql.ErrNoRows
	}
	if errors.Is(err, sql.ErrNoRows) && threadKind == domain.ThreadKindParticipantBound {
		err = tx.QueryRowContext(ctx, `SELECT id FROM runs
WHERE workspace_id = ? AND agent_id = ? AND project_id = ? AND task_id = ?
AND status IN ('starting', 'active', 'blocked') ORDER BY created_at DESC, id DESC LIMIT 1`, workspace.ID, recipient.ID, recipientParticipant.ProjectID, recipientParticipant.TaskID).Scan(&targetRunID)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id FROM runs
WHERE workspace_id = ? AND agent_id = ? AND status IN ('starting', 'active', 'blocked')
AND (? = '' OR project_id = ?) ORDER BY created_at DESC, id DESC LIMIT 1`, workspace.ID, recipient.ID, projectID, projectID).Scan(&targetRunID)
	}
	if err == nil {
		wakeID, generateErr := randomID("wake_")
		if generateErr != nil {
			return MutationResult[domain.MessageMutation]{}, storageFailure("generate message wake id", generateErr)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_wake_jobs(id, message_id, recipient_agent_id, target_run_id, target_domain_thread_id, status, attempts, available_at, created_at, updated_at)
VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), 'pending', 0, ?, ?, ?)`, wakeID, message.ID, recipient.ID, targetRunID, targetDomainThreadID, now, now, now); err != nil {
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

// DomainAgentInbox returns only messages explicitly scoped to one durable
// agent's selected domain. A workspace-level inbox is broader and must never be
// copied wholesale into an unrelated provider conversation.
func (s *Store) DomainAgentInbox(ctx context.Context, workspaceIdentifier, projectIdentifier, agentIdentifier string, limit int) ([]domain.InboxItem, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > maximumInboxItems {
		return nil, &Error{Code: CodeInvalidMessage, Message: "domain agent inbox limit must be from 1 to 50"}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageFailure("begin domain agent inbox", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return nil, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, strings.TrimSpace(projectIdentifier))
	if err != nil {
		return nil, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, strings.TrimSpace(agentIdentifier))
	if err != nil {
		return nil, err
	}
	membership, err := queryDomainAgentMembership(ctx, tx, project.ID, agent.ID)
	if err != nil || membership.Status != domain.DomainAgentActive {
		return nil, &Error{Code: CodeMessageDenied, Message: "domain inbox requires one active durable agent membership", Cause: err}
	}
	return queryInboxWithCondition(ctx, tx, agent.ID, limit, " AND m.project_id = ?", project.ID)
}

// DeliverDomainAgentSessionInbox makes one current-node durable session's
// same-domain queue visible to its provider context. Delivery is a durable
// lifecycle fact; repeated context reads do not append duplicate events.
func (s *Store) DeliverDomainAgentSessionInbox(ctx context.Context, threadID string, limit int) ([]domain.InboxItem, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || limit < 1 || limit > maximumInboxItems {
		return nil, &Error{Code: CodeInvalidMessage, Message: "domain session inbox requires one private thread and a limit from 1 to 50"}
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return nil, storageFailure("begin domain session inbox", err)
	}
	defer tx.Rollback()
	scope, err := s.domainAgentSessionScopeInTransaction(ctx, tx, threadID)
	if err != nil {
		return nil, err
	}
	items, err := queryInboxWithCondition(ctx, tx, scope.Agent.ID, limit, " AND m.project_id = ?", scope.Project.ID)
	if err != nil {
		return nil, err
	}
	now := s.nowText()
	for index := range items {
		if items[index].Delivery.Status != domain.DeliveryQueued {
			continue
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE message_recipients
SET status='delivered', delivered_at=?
WHERE message_id=? AND recipient_agent_id=? AND status='queued'`, now, items[index].Message.ID, scope.Agent.ID)
		if updateErr != nil {
			return nil, storageFailure("deliver durable-agent inbox message", updateErr)
		}
		changed, updateErr := result.RowsAffected()
		if updateErr != nil {
			return nil, storageFailure("count durable-agent inbox delivery", updateErr)
		}
		if changed != 1 {
			continue
		}
		items[index].Delivery.Status = domain.DeliveryDelivered
		items[index].Delivery.DeliveredAt = now
		if _, err := appendEventForActor(ctx, tx, scope.Workspace.ID, "message", items[index].Message.ID, 2,
			messageDeliveredEvent, "domain-inbox-"+items[index].Message.ID, now, scope.Agent.ID,
			domain.EventActorIntegration, map[string]any{
				"recipient_agent_id": scope.Agent.ID,
				"delivery_surface":   "durable_agent_session",
			}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, storageFailure("commit domain session inbox", err)
	}
	return items, nil
}

func (s *Store) RunInbox(ctx context.Context, runID string, limit int) ([]domain.InboxItem, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > maximumInboxItems {
		return nil, &Error{Code: CodeInvalidMessage, Message: "inbox limit must be from 1 to 50"}
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return nil, storageFailure("begin run inbox", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", strings.TrimSpace(runID))
	if err != nil || !runCanUseMailbox(run.Status) {
		return nil, &Error{Code: CodeMessageDenied, Message: "run mailbox is not active"}
	}
	items, err := queryInboxWithCondition(ctx, tx, run.AgentID, limit, ` AND (
((SELECT kind FROM message_threads WHERE id = m.thread_id) = 'direct' AND (m.project_id IS NULL OR m.project_id = ?))
OR EXISTS (
    SELECT 1 FROM thread_participants p
    WHERE p.id = r.recipient_participant_id AND p.thread_id = m.thread_id
      AND p.status = 'active' AND p.agent_id = ? AND p.project_id = ? AND p.task_id = ?
      AND (SELECT COUNT(*) FROM thread_participants roster WHERE roster.thread_id = m.thread_id) BETWEEN 2 AND 8
      AND (SELECT COUNT(*) FROM thread_participants roster WHERE roster.thread_id = m.thread_id) >=
          (SELECT initial_participant_count FROM message_threads WHERE id = m.thread_id)
      AND (SELECT COUNT(*) FROM thread_participants roster WHERE roster.thread_id = m.thread_id) =
          (SELECT initial_participant_count + participant_revision - 1 FROM message_threads WHERE id = m.thread_id)
      AND (SELECT COUNT(DISTINCT roster.project_id) FROM thread_participants roster WHERE roster.thread_id = m.thread_id) >= 2
))`, run.ProjectID, run.AgentID, run.ProjectID, run.TaskID)
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
	tx, err := s.beginTx(ctx, nil)
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
	authorized, authErr := runAuthorizedForMessage(ctx, tx, run, messageID)
	if authErr != nil {
		return MutationResult[domain.InboxItem]{}, authErr
	}
	if !authorized {
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

// ListThreads discovers bounded durable coordination threads in a workspace
// or project. Participant-bound threads are included when any frozen
// participant binding belongs to the requested project.
func (s *Store) ListThreads(ctx context.Context, workspaceIdentifier, projectIdentifier string, limit int) ([]domain.ThreadSummary, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return nil, err
	}
	projectID := ""
	if strings.TrimSpace(projectIdentifier) != "" {
		project, projectErr := s.Project(ctx, workspace.ID, strings.TrimSpace(projectIdentifier))
		if projectErr != nil {
			return nil, projectErr
		}
		projectID = project.ID
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > maximumThreadListItems {
		return nil, &Error{Code: CodeInvalidMessage, Message: "thread list limit must be between 1 and 50"}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT th.id, th.workspace_id, COALESCE(th.project_id, ''), COALESCE(th.task_id, ''),
       th.subject, th.status, th.revision, th.created_at, th.updated_at, th.created_by, th.updated_by,
       (SELECT COUNT(*) FROM messages m WHERE m.thread_id = th.id),
       (SELECT COALESCE(group_concat(agent_id, ','), '') FROM (
          SELECT sender_agent_id AS agent_id FROM messages WHERE thread_id = th.id AND sender_agent_id IS NOT NULL
          UNION
          SELECT r.recipient_agent_id AS agent_id FROM message_recipients r JOIN messages m ON m.id = r.message_id WHERE m.thread_id = th.id
          UNION
          SELECT p.agent_id AS agent_id FROM thread_participants p WHERE p.thread_id = th.id
          ORDER BY agent_id LIMIT 100))
FROM message_threads th
WHERE th.workspace_id = ?
  AND (? = '' OR th.project_id = ?
       OR EXISTS (SELECT 1 FROM messages m WHERE m.thread_id = th.id AND m.project_id = ?)
       OR EXISTS (SELECT 1 FROM thread_participants p WHERE p.thread_id = th.id AND p.project_id = ?))
ORDER BY th.updated_at DESC, th.id DESC
LIMIT ?`, workspace.ID, projectID, projectID, projectID, projectID, limit)
	if err != nil {
		return nil, storageFailure("list message threads", err)
	}
	defer rows.Close()
	result := make([]domain.ThreadSummary, 0)
	for rows.Next() {
		var summary domain.ThreadSummary
		var agentIDsText string
		thread := &summary.Thread
		if err := rows.Scan(&thread.ID, &thread.WorkspaceID, &thread.ProjectID, &thread.TaskID, &thread.Subject, &thread.Status, &thread.Revision, &thread.CreatedAt, &thread.UpdatedAt, &thread.CreatedBy, &thread.UpdatedBy, &summary.MessageCount, &agentIDsText); err != nil {
			return nil, storageFailure("scan message thread summary", err)
		}
		summary.AgentIDs = []string{}
		if agentIDsText != "" {
			summary.AgentIDs = strings.Split(agentIDsText, ",")
		}
		result = append(result, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate message thread summaries", err)
	}
	return result, nil
}

const expiredMessageWakeDiagnostic = "wake lease expired before durable completion; external delivery outcome is unknown"

type expiredMessageWake struct {
	ID                   string
	MessageID            string
	RecipientAgentID     string
	TargetRunID          string
	TargetDomainThreadID string
	WorkspaceID          string
}

// settleExpiredMessageWakes makes the at-most-once boundary explicit. Once a
// wake lease has expired, Crewfold cannot know whether the external prompt was
// delivered before the worker disappeared. Such a job is terminal and visible;
// it is never returned to the pending queue.
func settleExpiredMessageWakes(ctx context.Context, tx *sql.Tx, now string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT j.id, j.message_id, j.recipient_agent_id,
COALESCE(j.target_run_id,''), COALESCE(j.target_domain_thread_id,''), m.workspace_id
FROM message_wake_jobs j
JOIN messages m ON m.id = j.message_id
WHERE j.status = 'leased' AND j.lease_expires_at <= ?
ORDER BY j.sequence
LIMIT ?`, now, maximumWakeRecoveryBatch)
	if err != nil {
		return 0, storageFailure("query expired message wakes", err)
	}
	expired := make([]expiredMessageWake, 0, maximumWakeRecoveryBatch)
	for rows.Next() {
		var job expiredMessageWake
		if err := rows.Scan(&job.ID, &job.MessageID, &job.RecipientAgentID, &job.TargetRunID, &job.TargetDomainThreadID, &job.WorkspaceID); err != nil {
			rows.Close()
			return 0, storageFailure("scan expired message wake", err)
		}
		expired = append(expired, job)
	}
	if err := rows.Close(); err != nil {
		return 0, storageFailure("close expired message wake rows", err)
	}
	if err := rows.Err(); err != nil {
		return 0, storageFailure("iterate expired message wakes", err)
	}

	settled := 0
	for _, job := range expired {
		result, err := tx.ExecContext(ctx, `UPDATE message_wake_jobs
SET status = ?, diagnostic = ?, lease_expires_at = NULL, updated_at = ?
WHERE id = ? AND status = 'leased' AND lease_expires_at <= ?`, domain.WakeFailedUnknown, expiredMessageWakeDiagnostic, now, job.ID, now)
		if err != nil {
			return 0, storageFailure("settle expired message wake", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, storageFailure("inspect expired message wake settlement", err)
		}
		if changed == 0 {
			continue
		}
		eventData := map[string]any{"recipient_agent_id": job.RecipientAgentID, "diagnostic": expiredMessageWakeDiagnostic}
		if job.TargetRunID != "" {
			eventData["target_run_id"] = job.TargetRunID
		} else {
			eventData["target_surface"] = "durable_agent_session"
		}
		if _, err := appendEventForActor(ctx, tx, job.WorkspaceID, "message", job.MessageID, 1, messageWakeFailedUnknown, "wake-unknown-"+job.ID, now, messageWakeActorID, "subsystem", eventData); err != nil {
			return 0, err
		}
		settled++
	}
	return settled, nil
}

func (s *Store) ClaimMessageWakeJob(ctx context.Context, lease time.Duration) (domain.MessageWakeJob, bool, error) {
	if lease <= 0 {
		lease = time.Second
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.MessageWakeJob{}, false, storageFailure("begin message wake claim", err)
	}
	defer tx.Rollback()
	now := s.clock().UTC()
	nowText, expires := now.Format(time.RFC3339Nano), now.Add(lease).Format(time.RFC3339Nano)
	if _, err := settleExpiredMessageWakes(ctx, tx, nowText); err != nil {
		return domain.MessageWakeJob{}, false, err
	}
	var job domain.MessageWakeJob
	err = tx.QueryRowContext(ctx, `SELECT id, message_id, recipient_agent_id, COALESCE(target_run_id,''), COALESCE(target_domain_thread_id,''), status, attempts, COALESCE(diagnostic, '')
FROM message_wake_jobs WHERE status = 'pending' AND available_at <= ?
ORDER BY sequence LIMIT 1`, nowText).Scan(&job.ID, &job.MessageID, &job.RecipientAgentID, &job.TargetRunID, &job.TargetDomainThreadID, &job.Status, &job.Attempts, &job.Diagnostic)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return domain.MessageWakeJob{}, false, storageFailure("commit message wake recovery", err)
		}
		return domain.MessageWakeJob{}, false, nil
	}
	if err != nil {
		return domain.MessageWakeJob{}, false, storageFailure("query message wake", err)
	}
	job.Status, job.Attempts = domain.WakeLeased, job.Attempts+1
	if _, err := tx.ExecContext(ctx, "UPDATE message_wake_jobs SET status = 'leased', attempts = ?, lease_expires_at = ?, updated_at = ? WHERE id = ? AND status = 'pending'", job.Attempts, expires, nowText, job.ID); err != nil {
		return domain.MessageWakeJob{}, false, storageFailure("lease message wake", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.MessageWakeJob{}, false, storageFailure("commit message wake claim", err)
	}
	return job, true, nil
}

func (s *Store) SettleMessageWakeJob(ctx context.Context, jobID, outcome, diagnostic string) error {
	outcome = strings.TrimSpace(outcome)
	if outcome != domain.WakeSucceeded && outcome != domain.WakeFailed && outcome != domain.WakeFailedUnknown {
		return &Error{Code: CodeInvalidMessage, Message: "message wake outcome must be succeeded, failed, or failed_unknown"}
	}
	diagnostic = strings.TrimSpace(diagnostic)
	if len(diagnostic) > 1024 {
		maximum := 1024
		for maximum > 0 && !utf8.RuneStart(diagnostic[maximum]) {
			maximum--
		}
		diagnostic = diagnostic[:maximum]
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin message wake completion", err)
	}
	defer tx.Rollback()
	var job domain.MessageWakeJob
	var workspaceID, messageProjectID, threadKind, recipientProjectID, recipientTaskID string
	err = tx.QueryRowContext(ctx, `SELECT w.id, COALESCE(m.project_id, ''), th.kind,
COALESCE(p.project_id, ''), COALESCE(p.task_id, ''),
j.id, j.message_id, j.recipient_agent_id, COALESCE(j.target_run_id,''), COALESCE(j.target_domain_thread_id,''), j.status, j.attempts
FROM message_wake_jobs j
JOIN messages m ON m.id = j.message_id
JOIN message_threads th ON th.id = m.thread_id
JOIN workspaces w ON w.id = m.workspace_id
LEFT JOIN message_recipients mr ON mr.message_id = j.message_id AND mr.recipient_agent_id = j.recipient_agent_id
LEFT JOIN thread_participants p ON p.id = mr.recipient_participant_id
WHERE j.id = ?`, strings.TrimSpace(jobID)).Scan(&workspaceID, &messageProjectID, &threadKind, &recipientProjectID, &recipientTaskID, &job.ID, &job.MessageID, &job.RecipientAgentID, &job.TargetRunID, &job.TargetDomainThreadID, &job.Status, &job.Attempts)
	if err != nil {
		return storageFailure("query message wake completion", err)
	}
	if job.Status == domain.WakeSucceeded || job.Status == domain.WakeFailed || job.Status == domain.WakeFailedUnknown {
		return tx.Commit()
	}
	if job.Status != domain.WakeLeased {
		return &Error{Code: CodeInvalidMessage, Message: "message wake job is not leased"}
	}
	if outcome == domain.WakeSucceeded && job.TargetRunID != "" {
		var targetStatus, targetAgentID, targetProjectID, targetTaskID string
		targetErr := tx.QueryRowContext(ctx, "SELECT status, agent_id, project_id, task_id FROM runs WHERE id = ?", job.TargetRunID).Scan(&targetStatus, &targetAgentID, &targetProjectID, &targetTaskID)
		if targetErr != nil && !errors.Is(targetErr, sql.ErrNoRows) {
			return storageFailure("revalidate message wake target", targetErr)
		}
		directMismatch := threadKind == domain.ThreadKindDirect && messageProjectID != "" && targetProjectID != messageProjectID
		participantMismatch := threadKind == domain.ThreadKindParticipantBound && (recipientProjectID == "" || recipientTaskID == "" || targetProjectID != recipientProjectID || targetTaskID != recipientTaskID)
		if targetErr != nil || targetAgentID != job.RecipientAgentID || !runCanUseMailbox(targetStatus) || directMismatch || participantMismatch {
			outcome = domain.WakeFailed
			if threadKind == domain.ThreadKindParticipantBound {
				diagnostic = "target run is no longer live in the authorized participant binding for wake-up"
			} else {
				diagnostic = "target run is no longer live in the message project for wake-up"
			}
		}
	}
	if outcome == domain.WakeSucceeded && job.TargetDomainThreadID != "" {
		var targetAgentID, targetProjectID, nodeID, nodeFingerprint, membershipStatus string
		var enabled int
		targetErr := tx.QueryRowContext(ctx, `SELECT binding.agent_id,binding.project_id,binding.node_id,binding.node_fingerprint,membership.status,agent.enabled
FROM domain_agent_session_bindings binding
JOIN domain_agent_memberships membership ON membership.project_id=binding.project_id AND membership.agent_id=binding.agent_id
JOIN agents agent ON agent.id=binding.agent_id
WHERE binding.thread_id=?`, job.TargetDomainThreadID).Scan(&targetAgentID, &targetProjectID, &nodeID, &nodeFingerprint, &membershipStatus, &enabled)
		if targetErr != nil || targetAgentID != job.RecipientAgentID || targetProjectID != messageProjectID ||
			nodeID != s.runtimeNodeID || nodeFingerprint != s.runtimeNodeFingerprint ||
			membershipStatus != domain.DomainAgentActive || enabled != 1 {
			outcome = domain.WakeFailed
			diagnostic = "durable-agent wake target is no longer active on the current Crewfold node"
		}
	}
	now := s.nowText()
	eventType := messageWakeFailed
	correlationID := "wake-" + job.ID
	switch outcome {
	case domain.WakeSucceeded:
		eventType = messageWakeSucceeded
	case domain.WakeFailedUnknown:
		eventType = messageWakeFailedUnknown
		correlationID = "wake-unknown-" + job.ID
		if diagnostic == "" {
			diagnostic = expiredMessageWakeDiagnostic
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE message_wake_jobs SET status = ?, diagnostic = NULLIF(?, ''), lease_expires_at = NULL, updated_at = ? WHERE id = ?", outcome, diagnostic, now, job.ID); err != nil {
		return storageFailure("complete message wake", err)
	}
	deliveryChanged := false
	if outcome == domain.WakeSucceeded {
		var deliveryStatus string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM message_recipients WHERE message_id = ? AND recipient_agent_id = ?", job.MessageID, job.RecipientAgentID).Scan(&deliveryStatus); err != nil {
			return storageFailure("query wake delivery", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE message_recipients SET status = CASE WHEN status = 'queued' THEN 'delivered' ELSE status END,
delivered_at = COALESCE(delivered_at, ?), delivered_run_id = CASE WHEN ?<>'' THEN COALESCE(delivered_run_id, ?) ELSE delivered_run_id END WHERE message_id = ? AND recipient_agent_id = ?`, now, job.TargetRunID, job.TargetRunID, job.MessageID, job.RecipientAgentID); err != nil {
			return storageFailure("record wake delivery", err)
		}
		deliveryChanged = deliveryStatus == domain.DeliveryQueued
	}
	eventData := map[string]any{"recipient_agent_id": job.RecipientAgentID, "diagnostic": diagnostic}
	if job.TargetRunID != "" {
		eventData["target_run_id"] = job.TargetRunID
	} else {
		eventData["target_surface"] = "durable_agent_session"
	}
	if _, err := appendEventForActor(ctx, tx, workspaceID, "message", job.MessageID, 1, eventType, correlationID, now, messageWakeActorID, "subsystem", eventData); err != nil {
		return err
	}
	if outcome == domain.WakeSucceeded && deliveryChanged {
		deliveryData := map[string]any{"recipient_agent_id": job.RecipientAgentID}
		if job.TargetRunID != "" {
			deliveryData["run_id"] = job.TargetRunID
		} else {
			deliveryData["delivery_surface"] = "durable_agent_session"
		}
		if _, err := appendEventForActor(ctx, tx, workspaceID, "message", job.MessageID, 2, messageDeliveredEvent, "wake-delivery-"+job.ID, now, messageWakeActorID, "subsystem", deliveryData); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit message wake completion", err)
	}
	return nil
}

// DeferMessageWakeJob returns a leased wake to the pending queue only before
// any external prompt is issued. Durable-agent sessions use this when another
// turn is already active, preventing concurrent provider turns without losing
// the queued message or weakening the at-most-once effect boundary.
func (s *Store) DeferMessageWakeJob(ctx context.Context, jobID string, delay time.Duration, diagnostic string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || delay < 0 || delay > time.Minute {
		return &Error{Code: CodeInvalidMessage, Message: "message wake deferral is invalid"}
	}
	diagnostic = strings.TrimSpace(diagnostic)
	if len(diagnostic) > 1024 {
		maximum := 1024
		for maximum > 0 && !utf8.RuneStart(diagnostic[maximum]) {
			maximum--
		}
		diagnostic = diagnostic[:maximum]
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin message wake deferral", err)
	}
	defer tx.Rollback()
	now := s.clock().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE message_wake_jobs
SET status='pending', available_at=?, lease_expires_at=NULL, diagnostic=?, updated_at=?
WHERE id=? AND status='leased'`, now.Add(delay).Format(time.RFC3339Nano), diagnostic, now.Format(time.RFC3339Nano), jobID)
	if err != nil {
		return storageFailure("defer message wake", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storageFailure("inspect message wake deferral", err)
	}
	if changed != 1 {
		return &Error{Code: CodeInvalidMessage, Message: "message wake job is not leased"}
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit message wake deferral", err)
	}
	return nil
}

func inboxSummaryInTransaction(ctx context.Context, tx *sql.Tx, agentID, projectID, taskID string) (domain.InboxSummary, error) {
	var summary domain.InboxSummary
	const authorizedCondition = ` AND (
((SELECT kind FROM message_threads WHERE id = m.thread_id) = 'direct' AND (m.project_id IS NULL OR m.project_id = ?))
OR EXISTS (
    SELECT 1 FROM thread_participants p
    WHERE p.id = r.recipient_participant_id AND p.thread_id = m.thread_id
      AND p.status = 'active' AND p.agent_id = ? AND p.project_id = ? AND p.task_id = ?
      AND (SELECT COUNT(*) FROM thread_participants roster WHERE roster.thread_id = m.thread_id) BETWEEN 2 AND 8
      AND (SELECT COUNT(*) FROM thread_participants roster WHERE roster.thread_id = m.thread_id) >=
          (SELECT initial_participant_count FROM message_threads WHERE id = m.thread_id)
      AND (SELECT COUNT(*) FROM thread_participants roster WHERE roster.thread_id = m.thread_id) =
          (SELECT initial_participant_count + participant_revision - 1 FROM message_threads WHERE id = m.thread_id)
      AND (SELECT COUNT(DISTINCT roster.project_id) FROM thread_participants roster WHERE roster.thread_id = m.thread_id) >= 2
))`
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_recipients r JOIN messages m ON m.id = r.message_id
WHERE r.recipient_agent_id = ? AND r.status IN ('queued', 'delivered')`+authorizedCondition, agentID, projectID, agentID, projectID, taskID).Scan(&summary.UnseenCount); err != nil {
		return domain.InboxSummary{}, storageFailure("count context inbox", err)
	}
	items, err := queryInboxWithCondition(ctx, tx, agentID, contextInboxItems, " AND r.status IN ('queued', 'delivered')"+authorizedCondition, projectID, agentID, projectID, taskID)
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
