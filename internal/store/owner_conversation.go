package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
)

const maximumOwnerConversationTurns = 200

type PrepareOwnerTurnCommand struct {
	WorkspaceIdentifier   string
	ProjectIdentifier     string
	ConversationID        string
	Instruction           string
	Kind                  string
	IdempotencyKey        string
	InitiatedBy           string
	TriggerEventSequence  int64
	ExpectedEventSequence int64
	Interpretation        domain.OwnerInterpretation
	Citations             []domain.OwnerCitation
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

type CreateOwnerSchedulingIntentCommand struct {
	WorkspaceIdentifier string
	TurnID              string
	OperationID         string
	TaskID              string
	LaunchProfileID     string
	CorrelationID       string
}

type EditOwnerPlanCommand struct {
	WorkspaceIdentifier string
	TurnID              string
	ExpectedRevision    int64
	ObjectiveTitle      string
	ObjectiveBudget     domain.Budget
	Tasks               []domain.OwnerPlanTask
}

type OwnerConversationPage struct {
	Conversations []domain.OwnerConversation      `json:"conversations"`
	Turns         []domain.OwnerTurnDetail        `json:"turns"`
	Exchanges     []domain.OwnerExecutiveExchange `json:"exchanges"`
	Executive     *domain.OwnerExecutiveBinding   `json:"executive,omitempty"`
	Review        *domain.OwnerManagerReviewJob   `json:"review,omitempty"`
}

type ownerPlanOperation struct {
	Type         string         `json:"type"`
	Payload      map[string]any `json:"payload"`
	PolicyResult string         `json:"policy_result"`
	Status       string         `json:"-"`
}

var ownerPlanKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// OwnerTurnReplay checks the owner instruction idempotency boundary before a
// provider is invoked. A replay therefore never consumes a second model turn.
func (s *Store) OwnerTurnReplay(ctx context.Context, command PrepareOwnerTurnCommand) (domain.OwnerTurnDetail, bool, error) {
	instruction := strings.TrimSpace(command.Instruction)
	kind := strings.TrimSpace(command.Kind)
	key := strings.TrimSpace(command.IdempotencyKey)
	initiatedBy := strings.TrimSpace(command.InitiatedBy)
	if initiatedBy == "" {
		initiatedBy = "owner"
	}
	validOwnerTurn := initiatedBy == "owner" && command.TriggerEventSequence == 0 && (kind == "query" || kind == "plan" || kind == "act")
	validManagerTurn := initiatedBy == "executive" && command.TriggerEventSequence > 0 && kind == "review" && strings.TrimSpace(command.ConversationID) != ""
	if !validManagerText(instruction, 4096) || (!validOwnerTurn && !validManagerTurn) || key == "" || len(key) > 128 {
		return domain.OwnerTurnDetail{}, false, &Error{Code: CodeInvalidOwnerConversation, Message: "owner turn requires an exact origin, bounded instruction, current kind, and idempotency key"}
	}
	requestHash, err := hashCommand("owner.turn", map[string]any{
		"workspace": command.WorkspaceIdentifier, "project": command.ProjectIdentifier,
		"conversation": command.ConversationID, "instruction": instruction, "kind": kind,
		"initiated_by": initiatedBy, "trigger_event_sequence": command.TriggerEventSequence,
	})
	if err != nil {
		return domain.OwnerTurnDetail{}, false, storageFailure("hash owner turn replay", err)
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.OwnerTurnDetail{}, false, storageFailure("begin owner turn replay", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(command.WorkspaceIdentifier))
	if err != nil {
		return domain.OwnerTurnDetail{}, false, err
	}
	project, err := queryProject(ctx, tx, workspace.ID, strings.TrimSpace(command.ProjectIdentifier))
	if err != nil {
		return domain.OwnerTurnDetail{}, false, err
	}
	var turnID, existingHash, existingWorkspace, existingProject string
	err = tx.QueryRowContext(ctx, `SELECT turn.id,turn.request_sha256,conversation.workspace_id,conversation.project_id FROM owner_turns turn JOIN owner_conversations conversation ON conversation.id=turn.conversation_id WHERE turn.idempotency_key=?`, key).Scan(&turnID, &existingHash, &existingWorkspace, &existingProject)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OwnerTurnDetail{}, false, nil
	}
	if err != nil {
		return domain.OwnerTurnDetail{}, false, storageFailure("read owner turn replay", err)
	}
	if existingHash != requestHash || existingWorkspace != workspace.ID || existingProject != project.ID {
		return domain.OwnerTurnDetail{}, false, &Error{Code: CodeIdempotencyConflict, Message: "owner turn idempotency key was used with different content"}
	}
	detail, err := ownerTurnDetailInTransaction(ctx, tx, turnID)
	return detail, err == nil, err
}

func (s *Store) PrepareOwnerTurn(ctx context.Context, command PrepareOwnerTurnCommand) (domain.OwnerTurnDetail, error) {
	if command.Citations == nil {
		command.Citations = []domain.OwnerCitation{}
	}
	instruction := strings.TrimSpace(command.Instruction)
	kind := strings.TrimSpace(command.Kind)
	key := strings.TrimSpace(command.IdempotencyKey)
	initiatedBy := strings.TrimSpace(command.InitiatedBy)
	if initiatedBy == "" {
		initiatedBy = "owner"
	}
	validOwnerTurn := initiatedBy == "owner" && command.TriggerEventSequence == 0 && (kind == "query" || kind == "plan" || kind == "act")
	validManagerReview := initiatedBy == "executive" && kind == "review" && command.TriggerEventSequence > 0 && command.TriggerEventSequence == command.ExpectedEventSequence && command.ConversationID != ""
	if !validManagerText(instruction, 4096) || (!validOwnerTurn && !validManagerReview) || key == "" || len(key) > 128 {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner turn requires a bounded instruction, supported origin/kind, and idempotency key"}
	}
	requestHash, err := hashCommand("owner.turn", map[string]any{
		"workspace": command.WorkspaceIdentifier, "project": command.ProjectIdentifier,
		"conversation": command.ConversationID, "instruction": instruction, "kind": kind,
		"initiated_by": initiatedBy, "trigger_event_sequence": command.TriggerEventSequence,
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
	conversation, err := ownerConversationForCommand(ctx, tx, workspace.ID, project.ID, strings.TrimSpace(command.ConversationID), instruction, initiatedBy)
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
	eventCut := highWater
	if initiatedBy == "executive" {
		eventCut = command.ExpectedEventSequence
		var sourceWorkspace string
		if eventCut < 1 || eventCut > highWater || tx.QueryRowContext(ctx, "SELECT workspace_id FROM events WHERE sequence=?", eventCut).Scan(&sourceWorkspace) != nil || sourceWorkspace != workspace.ID {
			return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner manager review event cut is no longer available in this workspace"}
		}
	} else if command.ExpectedEventSequence != highWater {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "canonical project state changed while the manager was interpreting; retry against the new event cut"}
	}
	plan, answerText, status, interpretation, err := compileOwnerInterpretation(ctx, tx, workspace.ID, project.ID, kind, instruction, eventCut, command.Interpretation, command.Citations)
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	planJSON, planHash, err := canonicalContent(plan)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("seal owner turn plan", err)
	}
	interpretationJSON, _, err := canonicalContent(interpretation)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("seal owner interpretation", err)
	}
	citationsJSON, _, err := canonicalContent(command.Citations)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("seal owner citations", err)
	}
	turnID, err := randomID("turn_")
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("generate owner turn id", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	answer := any(nil)
	if answerText != "" {
		answer = answerText
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO owner_turns(id,conversation_id,ordinal,kind,initiated_by,trigger_event_sequence,instruction,status,as_of_event_sequence,answer,interpretation_json,citations_json,plan_json,plan_sha256,error_code,completed_event_sequence,idempotency_key,request_sha256,revision,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, turnID, conversation.ID, count+1, kind, initiatedBy, nullablePositiveInt(command.TriggerEventSequence), instruction, status, eventCut, answer,
		string(interpretationJSON), string(citationsJSON), string(planJSON), planHash, nil, nil, key, requestHash, 1, now, now); err != nil {
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
	updatedBy := localOwnerActorID
	if initiatedBy == "executive" {
		updatedBy = "subsystem:owner-manager"
	}
	if _, err := tx.ExecContext(ctx, "UPDATE owner_conversations SET revision=revision+1,updated_at=?,updated_by=? WHERE id=?", now, updatedBy, conversation.ID); err != nil {
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

// CreateOwnerSchedulingIntent publishes one supervisor work item from an
// exact executing owner-turn operation. The baseline trigger proves the task
// was created by the same frozen turn and the launch profile is still current.
func (s *Store) CreateOwnerSchedulingIntent(ctx context.Context, command CreateOwnerSchedulingIntentCommand) (MutationResult[domain.SchedulingIntent], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.TurnID = strings.TrimSpace(command.TurnID)
	command.OperationID = strings.TrimSpace(command.OperationID)
	command.TaskID = strings.TrimSpace(command.TaskID)
	command.LaunchProfileID = strings.TrimSpace(command.LaunchProfileID)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.TurnID == "" || command.OperationID == "" || command.TaskID == "" || command.LaunchProfileID == "" || command.CorrelationID == "" || len(command.CorrelationID) > 128 {
		return MutationResult[domain.SchedulingIntent]{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner scheduling intent requires exact turn, operation, task, and launch profile"}
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.SchedulingIntent]{}, storageFailure("begin owner scheduling intent", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.SchedulingIntent]{}, err
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM scheduling_intents WHERE source_owner_operation_id=?`, command.OperationID).Scan(&existingID)
	if err == nil {
		intent, queryErr := querySchedulingIntent(ctx, tx, workspace.ID, existingID)
		if queryErr != nil {
			return MutationResult[domain.SchedulingIntent]{}, queryErr
		}
		var sequence int64
		if queryErr := tx.QueryRowContext(ctx, `SELECT sequence FROM events WHERE workspace_id=? AND entity_type='scheduling_intent' AND entity_id=? AND entity_revision=1`, workspace.ID, intent.ID).Scan(&sequence); queryErr != nil {
			return MutationResult[domain.SchedulingIntent]{}, storageFailure("read owner scheduling intent event", queryErr)
		}
		return MutationResult[domain.SchedulingIntent]{Value: intent, EventSequence: sequence}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MutationResult[domain.SchedulingIntent]{}, storageFailure("read owner scheduling intent replay", err)
	}
	detail, err := ownerTurnDetailInTransaction(ctx, tx, command.TurnID)
	if err != nil {
		return MutationResult[domain.SchedulingIntent]{}, err
	}
	if detail.Conversation.WorkspaceID != workspace.ID || detail.Turn.Status != "executing" {
		return MutationResult[domain.SchedulingIntent]{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner turn is not executing in the selected workspace"}
	}
	operationFound := false
	for _, operation := range detail.Operations {
		if operation.ID == command.OperationID && operation.Type == "schedule_task" && operation.Status == "pending" {
			operationFound = true
			break
		}
	}
	if !operationFound {
		return MutationResult[domain.SchedulingIntent]{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner scheduling operation changed before publication"}
	}
	task, err := queryTask(ctx, tx, workspace.ID, command.TaskID)
	if err != nil {
		return MutationResult[domain.SchedulingIntent]{}, err
	}
	profile, err := queryLaunchProfile(ctx, tx, workspace.ID, command.LaunchProfileID)
	if err != nil {
		return MutationResult[domain.SchedulingIntent]{}, err
	}
	if task.ProjectID != detail.Conversation.ProjectID || profile.ProjectID != task.ProjectID || profile.AgentID == "" || profile.Status != domain.LaunchProfileActive {
		return MutationResult[domain.SchedulingIntent]{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner scheduling target or profile changed"}
	}
	id, err := randomID("sintent_")
	if err != nil {
		return MutationResult[domain.SchedulingIntent]{}, storageFailure("generate owner scheduling intent id", err)
	}
	now := s.nowText()
	intent := domain.SchedulingIntent{ID: id, WorkspaceID: workspace.ID, ProjectID: task.ProjectID, ObjectiveID: task.ObjectiveID, TaskID: task.ID,
		AgentID: profile.AgentID, LaunchProfileID: profile.ID, SourceOwnerTurnID: detail.Turn.ID, SourceOwnerOperationID: command.OperationID,
		Status: domain.SchedulingIntentPending, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scheduling_intents(id,workspace_id,project_id,objective_id,task_id,agent_id,launch_profile_id,source_owner_turn_id,source_owner_operation_id,status,attempts,revision,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,?,?,?,?,?,?,'pending',0,1,?,?,?,?)`,
		intent.ID, intent.WorkspaceID, intent.ProjectID, intent.ObjectiveID, intent.TaskID, intent.AgentID, intent.LaunchProfileID, intent.SourceOwnerTurnID, intent.SourceOwnerOperationID, now, now, localOwnerActorID, localOwnerActorID); err != nil {
		return MutationResult[domain.SchedulingIntent]{}, storageFailure("insert owner scheduling intent", err)
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "scheduling_intent", intent.ID, 1, schedulingIntentCreatedEvent, command.CorrelationID, now, map[string]any{
		"task_id": intent.TaskID, "agent_id": intent.AgentID, "launch_profile_id": intent.LaunchProfileID, "source_owner_turn_id": intent.SourceOwnerTurnID, "source_owner_operation_id": intent.SourceOwnerOperationID,
	})
	if err != nil {
		return MutationResult[domain.SchedulingIntent]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.SchedulingIntent]{}, storageFailure("commit owner scheduling intent", err)
	}
	return MutationResult[domain.SchedulingIntent]{Value: intent, EventSequence: sequence}, nil
}

// EditOwnerPlan replaces the editable values in one still-effect-free plan.
// Operation identities remain stable; the new canonical plan and every payload
// hash are sealed together before the reviewed revision is returned.
func (s *Store) EditOwnerPlan(ctx context.Context, command EditOwnerPlanCommand) (domain.OwnerTurnDetail, error) {
	if command.ExpectedRevision < 1 {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner plan edit requires an exact current revision"}
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
	if detail.Conversation.WorkspaceID != workspace.ID || (detail.Turn.Kind != "plan" && detail.Turn.Kind != "review") || detail.Turn.Status != "planned" || detail.Turn.Revision != command.ExpectedRevision {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner plan changed or is no longer editable; refresh its exact revision"}
	}
	interpretation := detail.Turn.Interpretation
	interpretation.Disposition = "ready"
	interpretation.ObjectiveTitle = command.ObjectiveTitle
	interpretation.ObjectiveBudget = command.ObjectiveBudget
	interpretation.Tasks = append([]domain.OwnerPlanTask(nil), command.Tasks...)
	interpretation.Answer, interpretation.Question = "", ""
	interpretation.Choices = []domain.OwnerChoice{}
	plan, _, status, interpretation, err := compileOwnerInterpretation(ctx, tx, workspace.ID, detail.Conversation.ProjectID, detail.Turn.Kind, detail.Turn.Instruction, detail.Turn.AsOfEventSequence, interpretation, detail.Turn.Citations)
	if err != nil {
		return domain.OwnerTurnDetail{}, err
	}
	if status != "planned" {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerTurnConflict, Message: "edited owner plan did not remain inert"}
	}
	planJSON, planHash, err := canonicalContent(plan)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("seal edited owner plan", err)
	}
	interpretationJSON, _, err := canonicalContent(interpretation)
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("seal edited owner interpretation", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `DELETE FROM owner_turn_operations WHERE turn_id=? AND status='pending'`, detail.Turn.ID); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("replace edited owner operations", err)
	}
	for index, operation := range plan {
		operationID, idErr := randomID("op_")
		if idErr != nil {
			return domain.OwnerTurnDetail{}, storageFailure("generate edited owner operation id", idErr)
		}
		payloadJSON, payloadHash, encodeErr := canonicalContent(operation.Payload)
		if encodeErr != nil {
			return domain.OwnerTurnDetail{}, storageFailure("seal edited owner operation", encodeErr)
		}
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO owner_turn_operations(id,turn_id,ordinal,type,payload_json,payload_sha256,policy_result,status,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,1,?,?)`, operationID, detail.Turn.ID, index+1, operation.Type, string(payloadJSON), payloadHash, operation.PolicyResult, operation.Status, now, now); insertErr != nil {
			return domain.OwnerTurnDetail{}, storageFailure("insert edited owner operation", insertErr)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE owner_turns SET interpretation_json=?,plan_json=?,plan_sha256=?,revision=revision+1,updated_at=? WHERE id=?`, string(interpretationJSON), string(planJSON), planHash, now, detail.Turn.ID); err != nil {
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
	page := OwnerConversationPage{Conversations: []domain.OwnerConversation{}, Turns: []domain.OwnerTurnDetail{}, Exchanges: []domain.OwnerExecutiveExchange{}}
	if executive, found, bindingErr := ownerExecutiveBindingInTransaction(ctx, tx, project.ID); bindingErr != nil {
		return OwnerConversationPage{}, bindingErr
	} else if found {
		page.Executive = &executive
	}
	if review, reviewErr := ownerManagerReviewJobInTransaction(ctx, tx, project.ID); reviewErr == nil {
		page.Review = &review
	} else if !errors.Is(reviewErr, sql.ErrNoRows) {
		return OwnerConversationPage{}, storageFailure("read owner manager review state", reviewErr)
	}
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
		if exchange, exchangeErr := ownerExecutiveExchangeByTurnInTransaction(ctx, tx, id); exchangeErr == nil {
			page.Exchanges = append(page.Exchanges, exchange)
		} else if ErrorCode(exchangeErr) != CodeOwnerConversationNotFound {
			return OwnerConversationPage{}, exchangeErr
		}
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
	if detail.Conversation.WorkspaceID != workspace.ID || (detail.Turn.Kind != "plan" && detail.Turn.Kind != "review") {
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

func ownerConversationForCommand(ctx context.Context, tx *sql.Tx, workspaceID, projectID, conversationID, instruction, initiatedBy string) (domain.OwnerConversation, error) {
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
	createdBy := localOwnerActorID
	if initiatedBy == "executive" {
		createdBy = "subsystem:owner-manager"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO owner_conversations(id,workspace_id,project_id,title,status,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,'open',1,?,?,?,?)`, id, workspaceID, projectID, title, now, now, createdBy, createdBy); err != nil {
		return domain.OwnerConversation{}, storageFailure("insert owner conversation", err)
	}
	return domain.OwnerConversation{ID: id, WorkspaceID: workspaceID, ProjectID: projectID, Title: title, Status: "open", Revision: 1, CreatedAt: now, UpdatedAt: now}, nil
}

func compileOwnerInterpretation(ctx context.Context, tx *sql.Tx, workspaceID, projectID, kind, instruction string, eventCut int64, interpretation domain.OwnerInterpretation, citations []domain.OwnerCitation) ([]ownerPlanOperation, string, string, domain.OwnerInterpretation, error) {
	if len(citations) > 16 || !validOptionalOwnerText(interpretation.Summary, 2048) {
		return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager interpretation is outside its bounded contract"}
	}
	for _, citation := range citations {
		if citation.Ref == "" || len(citation.Ref) > 96 || citation.EntityType == "" || citation.EntityID == "" || citation.EntityRevision < 0 || citation.AsOfEventSequence != eventCut || !validManagerText(citation.Label, 512) {
			return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager citation is not bound to the interpreted event cut"}
		}
	}
	if ownerInstructionRequiresReview(instruction) && interpretation.Disposition == "ready" {
		interpretation = domain.OwnerInterpretation{Disposition: "refuse", Summary: "Crewfold rejected an unsafe operation boundary.", Answer: "This instruction requests a destructive, publication, external, credential, budget, or authority effect that is not part of the owner workbench action grammar.", ObjectiveBudget: domain.Budget{}, Tasks: []domain.OwnerPlanTask{}, Choices: []domain.OwnerChoice{}, CitationRefs: interpretation.CitationRefs}
	}
	switch interpretation.Disposition {
	case "answer":
		if (kind != "query" && kind != "review") || !validManagerText(interpretation.Answer, 8192) || interpretation.Question != "" || len(interpretation.Choices) != 0 || interpretation.ObjectiveTitle != "" || len(interpretation.Tasks) != 0 || interpretation.ObjectiveBudget != (domain.Budget{}) {
			return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager answer contains operations or malformed content"}
		}
		return []ownerPlanOperation{}, interpretation.Answer, "completed", interpretation, nil
	case "clarify":
		if !validManagerText(interpretation.Question, 2048) || interpretation.ObjectiveTitle != "" || len(interpretation.Tasks) != 0 || interpretation.ObjectiveBudget != (domain.Budget{}) {
			return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager clarification must be one bounded effect-free question"}
		}
		if err := validateOwnerChoices(interpretation.Choices); err != nil {
			return nil, "", "", domain.OwnerInterpretation{}, err
		}
		return []ownerPlanOperation{}, interpretation.Question, "completed", interpretation, nil
	case "refuse":
		if !validManagerText(interpretation.Answer, 8192) || interpretation.Question != "" || len(interpretation.Choices) != 0 || interpretation.ObjectiveTitle != "" || len(interpretation.Tasks) != 0 || interpretation.ObjectiveBudget != (domain.Budget{}) {
			return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager refusal must be bounded and effect-free"}
		}
		return []ownerPlanOperation{}, interpretation.Answer, "completed", interpretation, nil
	case "ready":
	default:
		return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager returned an unknown disposition"}
	}
	if kind == "query" || interpretation.Answer != "" || interpretation.Question != "" || len(interpretation.Choices) != 0 || !validTitle(interpretation.ObjectiveTitle) || !validBudget(interpretation.ObjectiveBudget) || interpretation.ObjectiveBudget.TokenLimit > 1_000_000 || interpretation.ObjectiveBudget.CostCents != 0 || interpretation.ObjectiveBudget.TimeSeconds > 86_400 || len(interpretation.Tasks) < 1 || len(interpretation.Tasks) > 8 {
		return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager ready result is not a bounded objective graph"}
	}
	keys := make(map[string]domain.OwnerPlanTask, len(interpretation.Tasks))
	for _, task := range interpretation.Tasks {
		if !ownerPlanKeyPattern.MatchString(task.Key) || !validTitle(task.Title) || len(task.Description) > 4096 || !utf8.ValidString(task.Description) || task.Priority < 0 || task.Priority > 1000 || !validBudget(task.Budget) || task.Budget.TokenLimit > 1_000_000 || task.Budget.CostCents != 0 || task.Budget.TimeSeconds > 86_400 || len(task.DependsOn) > 7 {
			return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager task is outside the typed graph limits"}
		}
		if _, duplicate := keys[task.Key]; duplicate {
			return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager task keys must be unique"}
		}
		profile, err := queryLaunchProfile(ctx, tx, workspaceID, task.LaunchProfileID)
		if err != nil {
			return nil, "", "", domain.OwnerInterpretation{}, err
		}
		if profile.ProjectID != projectID || profile.Status != domain.LaunchProfileActive || profile.ManagerGrantID != "" {
			return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager selected a launch profile outside the project execution set"}
		}
		agent, err := queryAgent(ctx, tx, workspaceID, profile.AgentID)
		if err != nil {
			return nil, "", "", domain.OwnerInterpretation{}, err
		}
		if !agent.Enabled || agent.Revision != profile.AgentRevision || agent.Provider != profile.Provider || agent.Runtime != profile.Runtime {
			return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager selected a stale launch profile"}
		}
		keys[task.Key] = task
	}
	for _, task := range interpretation.Tasks {
		seen := make(map[string]struct{}, len(task.DependsOn))
		for _, dependency := range task.DependsOn {
			if dependency == task.Key {
				return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager task cannot depend on itself"}
			}
			if _, exists := keys[dependency]; !exists {
				return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager dependency references an unknown task key"}
			}
			if _, duplicate := seen[dependency]; duplicate {
				return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager repeated a task dependency"}
			}
			seen[dependency] = struct{}{}
		}
	}
	if ownerPlanHasCycle(keys) {
		return nil, "", "", domain.OwnerInterpretation{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager task dependency graph contains a cycle"}
	}
	plan := make([]ownerPlanOperation, 0, 1+len(interpretation.Tasks)*2+28)
	plan = append(plan, ownerPlanOperation{Type: "create_objective", Payload: map[string]any{"title": interpretation.ObjectiveTitle, "budget": interpretation.ObjectiveBudget}, PolicyResult: "allowed", Status: "pending"})
	for _, task := range interpretation.Tasks {
		plan = append(plan, ownerPlanOperation{Type: "create_task", Payload: map[string]any{"task_key": task.Key, "title": task.Title, "description": task.Description, "priority": task.Priority, "budget": task.Budget, "launch_profile_id": task.LaunchProfileID}, PolicyResult: "allowed", Status: "pending"})
	}
	for _, task := range interpretation.Tasks {
		for _, dependency := range task.DependsOn {
			plan = append(plan, ownerPlanOperation{Type: "add_dependency", Payload: map[string]any{"task_key": task.Key, "depends_on_task_key": dependency}, PolicyResult: "allowed", Status: "pending"})
		}
	}
	for _, task := range interpretation.Tasks {
		plan = append(plan, ownerPlanOperation{Type: "schedule_task", Payload: map[string]any{"task_key": task.Key, "launch_profile_id": task.LaunchProfileID}, PolicyResult: "allowed", Status: "pending"})
	}
	status := "planned"
	if kind == "act" {
		status = "executing"
	}
	return plan, interpretation.Summary, status, interpretation, nil
}

func validOptionalOwnerText(value string, maximum int) bool {
	return value == "" || validManagerText(value, maximum)
}

func validateOwnerChoices(choices []domain.OwnerChoice) error {
	if len(choices) != 0 && (len(choices) < 2 || len(choices) > 4) {
		return &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager clarification choices must contain two to four alternatives"}
	}
	seen := make(map[string]struct{}, len(choices))
	recommended := 0
	for _, choice := range choices {
		if !ownerPlanKeyPattern.MatchString(choice.Key) || !validManagerText(choice.Label, 160) || len(choice.Description) > 512 || !utf8.ValidString(choice.Description) {
			return &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager clarification choice is malformed"}
		}
		if _, duplicate := seen[choice.Key]; duplicate {
			return &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager clarification choice keys must be unique"}
		}
		seen[choice.Key] = struct{}{}
		if choice.Recommended {
			recommended++
		}
	}
	if recommended > 1 {
		return &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager may recommend at most one clarification choice"}
	}
	return nil
}

func ownerPlanHasCycle(tasks map[string]domain.OwnerPlanTask) bool {
	state := make(map[string]uint8, len(tasks))
	var visit func(string) bool
	visit = func(key string) bool {
		if state[key] == 1 {
			return true
		}
		if state[key] == 2 {
			return false
		}
		state[key] = 1
		for _, dependency := range tasks[key].DependsOn {
			if visit(dependency) {
				return true
			}
		}
		state[key] = 2
		return false
	}
	for key := range tasks {
		if visit(key) {
			return true
		}
	}
	return false
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

func ownerTurnDetailInTransaction(ctx context.Context, tx *sql.Tx, turnID string) (domain.OwnerTurnDetail, error) {
	var detail domain.OwnerTurnDetail
	var answer, errorCode sql.NullString
	var trigger, completed sql.NullInt64
	var interpretationJSON, citationsJSON string
	err := tx.QueryRowContext(ctx, `SELECT conversation.id,conversation.workspace_id,conversation.project_id,conversation.title,conversation.status,conversation.revision,conversation.created_at,conversation.updated_at,
 turn.id,turn.conversation_id,turn.ordinal,turn.kind,turn.initiated_by,turn.trigger_event_sequence,turn.instruction,turn.status,turn.as_of_event_sequence,turn.answer,turn.interpretation_json,turn.citations_json,turn.plan_sha256,turn.error_code,turn.revision,turn.created_at,turn.updated_at,turn.completed_event_sequence
FROM owner_turns turn JOIN owner_conversations conversation ON conversation.id=turn.conversation_id WHERE turn.id=?`, turnID).Scan(
		&detail.Conversation.ID, &detail.Conversation.WorkspaceID, &detail.Conversation.ProjectID, &detail.Conversation.Title, &detail.Conversation.Status, &detail.Conversation.Revision, &detail.Conversation.CreatedAt, &detail.Conversation.UpdatedAt,
		&detail.Turn.ID, &detail.Turn.ConversationID, &detail.Turn.Ordinal, &detail.Turn.Kind, &detail.Turn.InitiatedBy, &trigger, &detail.Turn.Instruction, &detail.Turn.Status, &detail.Turn.AsOfEventSequence, &answer, &interpretationJSON, &citationsJSON, &detail.Turn.PlanSHA256, &errorCode, &detail.Turn.Revision, &detail.Turn.CreatedAt, &detail.Turn.UpdatedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OwnerTurnDetail{}, &Error{Code: CodeOwnerConversationNotFound, Message: "owner turn was not found"}
	}
	if err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("read owner turn", err)
	}
	detail.Turn.Answer, detail.Turn.ErrorCode = answer.String, errorCode.String
	if trigger.Valid {
		detail.Turn.TriggerEventSequence = trigger.Int64
	}
	if err := json.Unmarshal([]byte(interpretationJSON), &detail.Turn.Interpretation); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("decode owner interpretation", err)
	}
	if err := json.Unmarshal([]byte(citationsJSON), &detail.Turn.Citations); err != nil {
		return domain.OwnerTurnDetail{}, storageFailure("decode owner citations", err)
	}
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
