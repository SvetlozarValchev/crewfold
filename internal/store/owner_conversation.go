package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
)

const maximumOwnerConversationTurns = 200

type PrepareOwnerTurnCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	ConversationID      string
	Instruction         string
	Kind                string
	IdempotencyKey      string
}

type RecordOwnerOperationCommand struct {
	WorkspaceIdentifier string
	TurnID              string
	OperationID         string
	Method              string
	IdempotencyKey      string
	Request             any
	Response            any
	ResultEntityType    string
	ResultEntityID      string
	EventSequence       int64
}

type EditOwnerPlanCommand struct {
	WorkspaceIdentifier string
	TurnID              string
	ExpectedRevision    int64
	Title               string
	Description         string
	Priority            int
	Budget              domain.Budget
	AgentIdentifier     string
}

type OwnerConversationPage struct {
	Conversations []domain.OwnerConversation `json:"conversations"`
	Turns         []domain.OwnerTurnDetail   `json:"turns"`
}

type ownerPlanOperation struct {
	Type         string         `json:"type"`
	Payload      map[string]any `json:"payload"`
	PolicyResult string         `json:"policy_result"`
	Status       string         `json:"-"`
}

func (s *Store) PrepareOwnerTurn(ctx context.Context, command PrepareOwnerTurnCommand) (domain.OwnerTurnDetail, error) {
	instruction := strings.TrimSpace(command.Instruction)
	kind := strings.TrimSpace(command.Kind)
	key := strings.TrimSpace(command.IdempotencyKey)
	if !validManagerText(instruction, 4096) || (kind != "query" && kind != "plan" && kind != "act") || key == "" || len(key) > 128 {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner turn requires a bounded instruction, query|plan|act kind, and idempotency key"}
	}
	requestHash, err := hashCommand("owner.turn", map[string]any{
		"workspace": command.WorkspaceIdentifier, "project": command.ProjectIdentifier,
		"conversation": command.ConversationID, "instruction": instruction, "kind": kind,
	})
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("hash owner turn", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("begin owner turn", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(command.WorkspaceIdentifier))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	project, err := queryProject(ctx, tx, workspace.ID, strings.TrimSpace(command.ProjectIdentifier))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}

	var existingID, existingHash, existingWorkspace, existingProject string
	err = tx.QueryRowContext(ctx, `SELECT turn.id,turn.request_sha256,conversation.workspace_id,conversation.project_id
FROM owner_turns turn JOIN owner_conversations conversation ON conversation.id=turn.conversation_id WHERE turn.idempotency_key=?`, key).Scan(&existingID, &existingHash, &existingWorkspace, &existingProject)
	if err == nil {
		if existingHash != requestHash || existingWorkspace != workspace.ID || existingProject != project.ID {
			return domain.OwnerTurnDetail{}, &Error{Code: CodeIdempotencyConflict, Message: "owner turn idempotency key was used with different content"}
		}
		return ownerTurnDetailInTransaction(ctx, tx, existingID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.OwnerTurnDetail{}, storageFailure("read owner turn replay", err)
	}
	conversation, err := ownerConversationForCommand(ctx, tx, workspace.ID, project.ID, strings.TrimSpace(command.ConversationID), instruction)
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM owner_turns WHERE conversation_id=?", conversation.ID).Scan(&count); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("count owner turns", err)
	}
	if count >= maximumOwnerConversationTurns {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner conversation reached its bounded turn limit"}
	}
	var highWater int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?", workspace.ID).Scan(&highWater); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("capture owner turn event high-water", err)
	}
	plan, gated := ownerPlan(kind, instruction)
	planJSON, planHash, err := canonicalContent(plan)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("seal owner turn plan", err)
	}
	turnID, err := randomID("turn_")
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("generate owner turn id", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	status := "planned"
	answer := any(nil)
	if kind == "act" {
		status = "executing"
	}
	if gated {
		status = "awaiting_approval"
		answer = "This instruction crosses a destructive, publication, external-communication, credential, network, budget, or authority boundary. Crewfold froze it before every effect; use a narrower local instruction or an exact typed decision path."
	}
	if kind == "query" {
		status = "completed"
		answerText, queryErr := ownerQueryAnswer(ctx, tx, workspace.ID, project.ID)
		if queryErr != nil {
			return domain.OwnerTurnDetail{}, queryErr
		}
		answer = answerText
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO owner_turns(id,conversation_id,ordinal,kind,instruction,status,as_of_event_sequence,answer,plan_json,plan_sha256,error_code,completed_event_sequence,idempotency_key,request_sha256,revision,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, turnID, conversation.ID, count+1, kind, instruction, status, highWater, answer,
		string(planJSON), planHash, nil, nil, key, requestHash, 1, now, now); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("insert owner turn", err)
	}
	for index, operation := range plan {
		operationID, idErr := randomID("op_")
		if idErr != nil {
			return domain.OwnerTurnDetail{}, storageFailure("generate owner operation id", idErr)
		}
		payloadJSON, payloadHash, encodeErr := canonicalContent(operation.Payload)
		if encodeErr != nil {
			return domain.OwnerTurnDetail{}, storageFailure("seal owner operation", encodeErr)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO owner_turn_operations(id,turn_id,ordinal,type,payload_json,payload_sha256,policy_result,status,result_entity_type,result_entity_id,event_sequence,diagnosis,revision,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,NULL,NULL,NULL,NULL,1,?,?)`, operationID, turnID, index+1, operation.Type, string(payloadJSON), payloadHash, operation.PolicyResult, operation.Status, now, now); err != nil {
			return domain.OwnerTurnDetail{}, storageFailure("insert owner operation", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE owner_conversations SET revision=revision+1,updated_at=? WHERE id=?", now, conversation.ID); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("advance owner conversation", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("commit owner turn", err)
	}
	return s.OwnerTurnDetail(ctx, workspace.ID, turnID)
}

func (s *Store) OwnerTurnDetail(ctx context.Context, workspaceIdentifier, turnID string) (domain.OwnerTurnDetail, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("begin owner turn read", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	detail, err := ownerTurnDetailInTransaction(ctx, tx, strings.TrimSpace(turnID))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	if detail.Conversation.WorkspaceID != workspace.ID {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerConversationNotFound, Message: "owner turn was not found in the selected workspace"}
	}
	return detail, nil
}

// EditOwnerPlan replaces the editable values in one still-effect-free plan.
// Operation identities remain stable; the new canonical plan and every payload
// hash are sealed together before the reviewed revision is returned.
func (s *Store) EditOwnerPlan(ctx context.Context, command EditOwnerPlanCommand) (domain.OwnerTurnDetail, error) {
	title := strings.TrimSpace(command.Title)
	description := strings.TrimSpace(command.Description)
	if command.ExpectedRevision < 1 || !validTitle(title) || len(description) > 4096 || command.Priority < 0 || command.Priority > 1000 || !validBudget(command.Budget) || command.Budget.TokenLimit > 1_000_000 || command.Budget.CostCents != 0 || command.Budget.TimeSeconds > 86_400 {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner plan edit requires a title, bounded description, priority from 0 to 1000, at most 1000000 tokens and 86400 seconds, zero paid-cost authority, and an exact revision"}
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("begin owner plan edit", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(command.WorkspaceIdentifier))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	detail, err := ownerTurnDetailInTransaction(ctx, tx, strings.TrimSpace(command.TurnID))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	if detail.Conversation.WorkspaceID != workspace.ID || detail.Turn.Kind != "plan" || detail.Turn.Status != "planned" || detail.Turn.Revision != command.ExpectedRevision || len(detail.Operations) != 4 {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner plan changed or is no longer editable; refresh its exact revision"}
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, strings.TrimSpace(command.AgentIdentifier))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	if !agent.Enabled {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner plan requires an enabled agent"}
	}
	plan := []ownerPlanOperation{
		{Type: "create_objective", Payload: map[string]any{"title": title, "budget": command.Budget}, PolicyResult: "allowed", Status: "pending"},
		{Type: "create_task", Payload: map[string]any{"title": title, "description": description, "priority": command.Priority, "budget": command.Budget}, PolicyResult: "allowed", Status: "pending"},
		{Type: "assign_task", Payload: map[string]any{"agent_id": agent.ID}, PolicyResult: "allowed", Status: "pending"},
		{Type: "start_run", Payload: map[string]any{"agent_id": agent.ID, "launch": "assigned_agent_default"}, PolicyResult: "allowed", Status: "pending"},
	}
	planJSON, planHash, err := canonicalContent(plan)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("seal edited owner plan", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index, operation := range plan {
		payloadJSON, payloadHash, encodeErr := canonicalContent(operation.Payload)
		if encodeErr != nil {
			return domain.OwnerTurnDetail{}, storageFailure("seal edited owner operation", encodeErr)
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE owner_turn_operations SET payload_json=?,payload_sha256=?,policy_result=?,revision=revision+1,updated_at=? WHERE turn_id=? AND ordinal=? AND type=? AND status='pending'`, string(payloadJSON), payloadHash, operation.PolicyResult, now, detail.Turn.ID, index+1, operation.Type)
		if updateErr != nil {
			return domain.OwnerTurnDetail{}, storageFailure("update edited owner operation", updateErr)
		}
		if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
			return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner plan operation changed while it was being edited"}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE owner_turns SET plan_json=?,plan_sha256=?,revision=revision+1,updated_at=? WHERE id=?`, string(planJSON), planHash, now, detail.Turn.ID); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("update edited owner plan", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE owner_conversations SET revision=revision+1,updated_at=? WHERE id=?`, now, detail.Conversation.ID); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("advance edited owner conversation", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("commit owner plan edit", err)
	}
	return s.OwnerTurnDetail(ctx, workspace.ID, detail.Turn.ID)
}

func (s *Store) ListOwnerConversation(ctx context.Context, workspaceIdentifier, projectIdentifier, conversationID string) (OwnerConversationPage, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OwnerConversationPage{}, storageFailure("begin owner conversation read", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return OwnerConversationPage{}, err
	}
	project, err := queryProject(ctx, tx, workspace.ID, strings.TrimSpace(projectIdentifier))
	if err != nil {
		return OwnerConversationPage{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,workspace_id,project_id,title,status,revision,created_at,updated_at
FROM owner_conversations WHERE workspace_id=? AND project_id=? AND (?='' OR id=?) ORDER BY updated_at,id LIMIT 20`, workspace.ID, project.ID, conversationID, conversationID)
	if err != nil {
		return OwnerConversationPage{}, storageFailure("list owner conversations", err)
	}
	defer rows.Close()
	page := OwnerConversationPage{Conversations: []domain.OwnerConversation{}, Turns: []domain.OwnerTurnDetail{}}
	for rows.Next() {
		var value domain.OwnerConversation
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.ProjectID, &value.Title, &value.Status, &value.Revision, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return OwnerConversationPage{}, storageFailure("scan owner conversation", err)
		}
		page.Conversations = append(page.Conversations, value)
	}
	if err := rows.Err(); err != nil {
		return OwnerConversationPage{}, storageFailure("iterate owner conversations", err)
	}
	if len(page.Conversations) == 0 {
		return page, nil
	}
	selected := page.Conversations[len(page.Conversations)-1].ID
	turnRows, err := tx.QueryContext(ctx, "SELECT id FROM owner_turns WHERE conversation_id=? ORDER BY ordinal LIMIT ?", selected, maximumOwnerConversationTurns)
	if err != nil {
		return OwnerConversationPage{}, storageFailure("list owner turn ids", err)
	}
	defer turnRows.Close()
	var ids []string
	for turnRows.Next() {
		var id string
		if err := turnRows.Scan(&id); err != nil {
			return OwnerConversationPage{}, storageFailure("scan owner turn id", err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		detail, err := ownerTurnDetailInTransaction(ctx, tx, id)
		if err != nil {
			return OwnerConversationPage{}, err
		}
		page.Turns = append(page.Turns, detail)
	}
	return page, nil
}

func (s *Store) RecordOwnerOperation(ctx context.Context, command RecordOwnerOperationCommand) (domain.OwnerTurnDetail, error) {
	responseJSON, responseHash, err := canonicalContent(command.Response)
	if err != nil || len(responseJSON) > 32768 {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner operation response is not a bounded canonical object"}
	}
	requestJSON, requestHash, err := canonicalContent(command.Request)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("seal owner operation request", err)
	}
	_ = requestJSON
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("begin owner operation receipt", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(command.WorkspaceIdentifier))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	detail, err := ownerTurnDetailInTransaction(ctx, tx, command.TurnID)
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	if detail.Conversation.WorkspaceID != workspace.ID {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerConversationNotFound, Message: "owner turn was not found in the selected workspace"}
	}
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM owner_turn_operations WHERE id=? AND turn_id=?", command.OperationID, command.TurnID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner operation was not found in the selected turn"}
		}
		return domain.OwnerTurnDetail{}, storageFailure("read owner operation", err)
	}
	if status == "applied" {
		return detail, nil
	}
	if status != "pending" {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner operation is not pending"}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO owner_effect_receipts(operation_id,method,idempotency_key,request_sha256,response_json,response_sha256,event_sequence,committed_at)
VALUES(?,?,?,?,?,?,?,?)`, command.OperationID, command.Method, command.IdempotencyKey, requestHash, string(responseJSON), responseHash, command.EventSequence, now); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("insert owner effect receipt", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE owner_turn_operations SET status='applied',result_entity_type=?,result_entity_id=?,event_sequence=?,revision=revision+1,updated_at=? WHERE id=?`, command.ResultEntityType, command.ResultEntityID, command.EventSequence, now, command.OperationID); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("apply owner operation receipt", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("commit owner effect receipt", err)
	}
	return s.OwnerTurnDetail(ctx, workspace.ID, command.TurnID)
}

func (s *Store) StartOwnerTurnExecution(ctx context.Context, workspaceIdentifier, turnID string) (domain.OwnerTurnDetail, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("begin owner plan execution", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	detail, err := ownerTurnDetailInTransaction(ctx, tx, strings.TrimSpace(turnID))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	if detail.Conversation.WorkspaceID != workspace.ID || detail.Turn.Kind != "plan" {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner plan was not found in the selected workspace"}
	}
	if detail.Turn.Status == "completed" || detail.Turn.Status == "executing" {
		return detail, nil
	}
	if detail.Turn.Status != "planned" {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner plan cannot execute from its current state"}
	}
	var highWater int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?", workspace.ID).Scan(&highWater); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("capture owner plan launch high-water", err)
	}
	if highWater != detail.Turn.AsOfEventSequence {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner plan is stale because canonical project state changed; refresh and review a new plan"}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, "UPDATE owner_turns SET status='executing',revision=revision+1,updated_at=? WHERE id=?", now, turnID); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("start owner plan execution", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("commit owner plan execution", err)
	}
	return s.OwnerTurnDetail(ctx, workspace.ID, turnID)
}

func (s *Store) FinishOwnerTurn(ctx context.Context, workspaceIdentifier, turnID, answer string) (domain.OwnerTurnDetail, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("begin owner turn completion", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	detail, err := ownerTurnDetailInTransaction(ctx, tx, strings.TrimSpace(turnID))
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	if detail.Conversation.WorkspaceID != workspace.ID || detail.Turn.Kind == "query" {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner turn cannot be completed from this scope or state"}
	}
	var pending int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM owner_turn_operations WHERE turn_id=? AND status<>'applied'", turnID).Scan(&pending); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("verify owner turn effects", err)
	}
	if pending != 0 {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner turn still has unapplied operations"}
	}
	if !validManagerText(answer, 8192) {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner turn answer is invalid"}
	}
	var highWater int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?", workspace.ID).Scan(&highWater); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("capture completed owner turn high-water", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, "UPDATE owner_turns SET status='completed',answer=?,completed_event_sequence=?,revision=revision+1,updated_at=? WHERE id=?", answer, highWater, now, turnID); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("complete owner turn", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE owner_conversations SET revision=revision+1,updated_at=? WHERE id=?", now, detail.Conversation.ID); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("advance completed owner conversation", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("commit owner turn completion", err)
	}
	return s.OwnerTurnDetail(ctx, workspace.ID, turnID)
}

func ownerConversationForCommand(ctx context.Context, tx *sql.Tx, workspaceID, projectID, conversationID, instruction string) (domain.OwnerConversation, error) {
	if conversationID != "" {
		var value domain.OwnerConversation
		err := tx.QueryRowContext(ctx, `SELECT id,workspace_id,project_id,title,status,revision,created_at,updated_at FROM owner_conversations WHERE id=?`, conversationID).Scan(
			&value.ID, &value.WorkspaceID, &value.ProjectID, &value.Title, &value.Status, &value.Revision, &value.CreatedAt, &value.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (value.WorkspaceID != workspaceID || value.ProjectID != projectID || value.Status != "open")) {
			return domain.OwnerConversation{}, &Error{Code: CodeOwnerConversationNotFound, Message: "open owner conversation was not found in the selected scope"}
		}
		if err != nil {
			return domain.OwnerConversation{}, storageFailure("read owner conversation", err)
		}
		return value, nil
	}
	id, err := randomID("conv_")
	if err != nil {
		return domain.OwnerConversation{}, storageFailure("generate owner conversation id", err)
	}
	title := instruction
	for len(title) > 160 {
		_, size := utf8.DecodeLastRuneInString(title)
		title = title[:len(title)-size]
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO owner_conversations(id,workspace_id,project_id,title,status,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,'open',1,?,?,'local-owner','local-owner')`, id, workspaceID, projectID, title, now, now); err != nil {
		return domain.OwnerConversation{}, storageFailure("insert owner conversation", err)
	}
	return domain.OwnerConversation{ID: id, WorkspaceID: workspaceID, ProjectID: projectID, Title: title, Status: "open", Revision: 1, CreatedAt: now, UpdatedAt: now}, nil
}

func ownerPlan(kind, instruction string) ([]ownerPlanOperation, bool) {
	if kind == "query" {
		return []ownerPlanOperation{}, false
	}
	gated := ownerInstructionRequiresReview(instruction)
	policy, status := "allowed", "pending"
	if gated {
		policy, status = "gated", "awaiting_approval"
	}
	budget := domain.Budget{}
	return []ownerPlanOperation{
		{Type: "create_objective", Payload: map[string]any{"title": instruction, "budget": budget}, PolicyResult: policy, Status: status},
		{Type: "create_task", Payload: map[string]any{"title": instruction, "description": instruction, "priority": 500, "budget": budget}, PolicyResult: policy, Status: status},
		{Type: "assign_task", Payload: map[string]any{"agent_selection": "first_enabled"}, PolicyResult: policy, Status: status},
		{Type: "start_run", Payload: map[string]any{"launch": "assigned_agent_default"}, PolicyResult: policy, Status: status},
	}, gated
}

func ownerInstructionRequiresReview(instruction string) bool {
	lower := " " + strings.ToLower(instruction) + " "
	for _, phrase := range []string{
		" rm -rf ", " delete ", " wipe ", " erase ", " git reset ", " git clean ",
		" push ", " publish ", " deploy ", " release ", " upload ", " download ",
		" curl ", " wget ", " email ", " send externally ", " post to ",
		" api key ", " credential ", " password ", " secret ",
		" change policy ", " grant access ", " revoke access ", " increase budget ", " spend ",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func ownerQueryAnswer(ctx context.Context, tx *sql.Tx, workspaceID, projectID string) (string, error) {
	var tasks, openTasks, runs, activeRuns, approvals int
	err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM tasks WHERE workspace_id=? AND project_id=?),
 (SELECT count(*) FROM tasks WHERE workspace_id=? AND project_id=? AND status NOT IN ('completed','failed','cancelled')),
 (SELECT count(*) FROM runs WHERE workspace_id=? AND project_id=?),
 (SELECT count(*) FROM runs WHERE workspace_id=? AND project_id=? AND status IN ('requested','starting','active','blocked','stopping','lost')),
	 (SELECT count(*) FROM approval_requests approval JOIN supervisor_actions action ON action.id=approval.action_id WHERE approval.workspace_id=? AND action.project_id=? AND approval.status='pending')`,
		workspaceID, projectID, workspaceID, projectID, workspaceID, projectID, workspaceID, projectID, workspaceID, projectID).Scan(&tasks, &openTasks, &runs, &activeRuns, &approvals)
	if err != nil {
		return "", storageFailure("summarize owner query", err)
	}
	return fmt.Sprintf("This project has %d tasks (%d open), %d runs (%d active or unresolved), and %d pending decisions at the frozen event cut.", tasks, openTasks, runs, activeRuns, approvals), nil
}

func ownerTurnDetailInTransaction(ctx context.Context, tx *sql.Tx, turnID string) (domain.OwnerTurnDetail, error) {
	var detail domain.OwnerTurnDetail
	var answer, errorCode sql.NullString
	var completed sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT conversation.id,conversation.workspace_id,conversation.project_id,conversation.title,conversation.status,conversation.revision,conversation.created_at,conversation.updated_at,
 turn.id,turn.conversation_id,turn.ordinal,turn.kind,turn.instruction,turn.status,turn.as_of_event_sequence,turn.answer,turn.plan_sha256,turn.error_code,turn.revision,turn.created_at,turn.updated_at,turn.completed_event_sequence
FROM owner_turns turn JOIN owner_conversations conversation ON conversation.id=turn.conversation_id WHERE turn.id=?`, turnID).Scan(
		&detail.Conversation.ID, &detail.Conversation.WorkspaceID, &detail.Conversation.ProjectID, &detail.Conversation.Title, &detail.Conversation.Status, &detail.Conversation.Revision, &detail.Conversation.CreatedAt, &detail.Conversation.UpdatedAt,
		&detail.Turn.ID, &detail.Turn.ConversationID, &detail.Turn.Ordinal, &detail.Turn.Kind, &detail.Turn.Instruction, &detail.Turn.Status, &detail.Turn.AsOfEventSequence, &answer, &detail.Turn.PlanSHA256, &errorCode, &detail.Turn.Revision, &detail.Turn.CreatedAt, &detail.Turn.UpdatedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerConversationNotFound, Message: "owner turn was not found"}
	}
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("read owner turn", err)
	}
	detail.Turn.Answer, detail.Turn.ErrorCode = answer.String, errorCode.String
	if completed.Valid {
		detail.Turn.CompletedEventSequence = completed.Int64
	}
	detail.Operations = []domain.OwnerTurnOperation{}
	rows, err := tx.QueryContext(ctx, `SELECT id,turn_id,ordinal,type,payload_json,payload_sha256,policy_result,status,result_entity_type,result_entity_id,event_sequence,diagnosis,revision,created_at,updated_at
FROM owner_turn_operations WHERE turn_id=? ORDER BY ordinal`, turnID)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("read owner operations", err)
	}
	defer rows.Close()
	for rows.Next() {
		var value domain.OwnerTurnOperation
		var payload string
		var entityType, entityID, diagnosis sql.NullString
		var sequence sql.NullInt64
		if err := rows.Scan(&value.ID, &value.TurnID, &value.Ordinal, &value.Type, &payload, &value.PayloadSHA256, &value.PolicyResult, &value.Status, &entityType, &entityID, &sequence, &diagnosis, &value.Revision, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return domain.OwnerTurnDetail{}, storageFailure("scan owner operation", err)
		}
		if err := json.Unmarshal([]byte(payload), &value.Payload); err != nil {
			return domain.OwnerTurnDetail{}, storageFailure("decode owner operation", err)
		}
		value.ResultEntityType, value.ResultEntityID, value.Diagnosis = entityType.String, entityID.String, diagnosis.String
		if sequence.Valid {
			value.EventSequence = sequence.Int64
		}
		detail.Operations = append(detail.Operations, value)
	}
	detail.Receipts = []domain.OwnerEffectReceipt{}
	receipts, err := tx.QueryContext(ctx, `SELECT operation_id,method,idempotency_key,request_sha256,response_sha256,event_sequence,committed_at FROM owner_effect_receipts WHERE operation_id IN (SELECT id FROM owner_turn_operations WHERE turn_id=?) ORDER BY committed_at,operation_id`, turnID)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("read owner receipts", err)
	}
	defer receipts.Close()
	for receipts.Next() {
		var value domain.OwnerEffectReceipt
		var sequence sql.NullInt64
		if err := receipts.Scan(&value.OperationID, &value.Method, &value.IdempotencyKey, &value.RequestSHA256, &value.ResponseSHA256, &sequence, &value.CommittedAt); err != nil {
			return domain.OwnerTurnDetail{}, storageFailure("scan owner receipt", err)
		}
		if sequence.Valid {
			value.EventSequence = sequence.Int64
		}
		detail.Receipts = append(detail.Receipts, value)
	}
	return detail, nil
}

func ownerSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
