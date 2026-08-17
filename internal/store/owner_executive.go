package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"crewfold/internal/domain"
)

const ownerExecutiveExchangeLease = 2 * time.Minute

const ownerExecutiveReviewBusyMessage = "project executive is still processing an earlier exchange"

type CreateOwnerExecutiveBindingCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	ObjectiveID         string
	PlanningTaskID      string
	AgentID             string
	ManagerGrantID      string
	LaunchProfileID     string
}

// ReconfigureOwnerExecutiveCommand atomically moves the durable project
// executive to a newly-created exact grant/profile pair. The new authority must
// already exist and remain inert until this mutation commits. The prior pair is
// retired in the same transaction, so no exchange can observe a mixed crew plan.
type ReconfigureOwnerExecutiveCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	ExpectedRevision    int64
	ManagerGrantID      string
	LaunchProfileID     string
	ConfigurationHash   string
	Reason              string
	IdempotencyKey      string
	CorrelationID       string
}

type RequestOwnerExecutiveTurnCommand struct {
	WorkspaceIdentifier  string
	ProjectIdentifier    string
	ConversationID       string
	Instruction          string
	IdempotencyKey       string
	Kind                 string
	InitiatedBy          string
	TriggerEventSequence int64
	Snapshot             OwnerInterpretationSnapshot
}

type OwnerExecutiveTurnResult struct {
	Detail   domain.OwnerTurnDetail        `json:"detail"`
	Exchange domain.OwnerExecutiveExchange `json:"exchange"`
}

type RespondOwnerExecutiveCommand struct {
	RunID          string
	ResponseKind   string
	Summary        string
	Answer         string
	Question       string
	Choices        []domain.OwnerChoice
	CitationRefs   []string
	ProposalIDs    []string
	IdempotencyKey string
}

func (s *Store) CreateOwnerExecutiveBinding(ctx context.Context, command CreateOwnerExecutiveBindingCommand) (domain.OwnerExecutiveBinding, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.ObjectiveID = strings.TrimSpace(command.ObjectiveID)
	command.PlanningTaskID = strings.TrimSpace(command.PlanningTaskID)
	command.AgentID = strings.TrimSpace(command.AgentID)
	command.ManagerGrantID = strings.TrimSpace(command.ManagerGrantID)
	command.LaunchProfileID = strings.TrimSpace(command.LaunchProfileID)
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" || command.ObjectiveID == "" || command.PlanningTaskID == "" || command.AgentID == "" || command.ManagerGrantID == "" || command.LaunchProfileID == "" {
		return domain.OwnerExecutiveBinding{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner executive binding requires one exact authority tuple"}
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.OwnerExecutiveBinding{}, storageFailure("begin owner executive binding", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return domain.OwnerExecutiveBinding{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return domain.OwnerExecutiveBinding{}, err
	}
	if existing, found, readErr := ownerExecutiveBindingInTransaction(ctx, tx, project.ID); readErr != nil {
		return domain.OwnerExecutiveBinding{}, readErr
	} else if found {
		if existing.WorkspaceID != workspace.ID || existing.ObjectiveID != command.ObjectiveID || existing.PlanningTaskID != command.PlanningTaskID || existing.AgentID != command.AgentID || existing.ManagerGrantID != command.ManagerGrantID || existing.LaunchProfileID != command.LaunchProfileID || existing.Status != "active" {
			return domain.OwnerExecutiveBinding{}, &Error{Code: CodeOwnerTurnConflict, Message: "project already has a different owner executive binding"}
		}
		return existing, nil
	}
	id, err := randomID("exec_")
	if err != nil {
		return domain.OwnerExecutiveBinding{}, storageFailure("generate owner executive binding id", err)
	}
	now := s.nowText()
	value := domain.OwnerExecutiveBinding{ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, ObjectiveID: command.ObjectiveID, PlanningTaskID: command.PlanningTaskID, AgentID: command.AgentID, ManagerGrantID: command.ManagerGrantID, LaunchProfileID: command.LaunchProfileID, Status: "active", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO owner_executive_bindings(id,workspace_id,project_id,objective_id,planning_task_id,agent_id,manager_grant_id,launch_profile_id,status,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,?,?,?,?,?,?,'active',1,?,?,'local-owner','local-owner')`, value.ID, value.WorkspaceID, value.ProjectID, value.ObjectiveID, value.PlanningTaskID, value.AgentID, value.ManagerGrantID, value.LaunchProfileID, now, now); err != nil {
		return domain.OwnerExecutiveBinding{}, storageFailure("insert owner executive binding", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.OwnerExecutiveBinding{}, storageFailure("commit owner executive binding", err)
	}
	return value, nil
}

func (s *Store) ReconfigureOwnerExecutive(ctx context.Context, command ReconfigureOwnerExecutiveCommand) (MutationResult[domain.OwnerExecutiveBinding], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.ManagerGrantID = strings.TrimSpace(command.ManagerGrantID)
	command.LaunchProfileID = strings.TrimSpace(command.LaunchProfileID)
	command.ConfigurationHash = strings.TrimSpace(command.ConfigurationHash)
	command.Reason = strings.TrimSpace(command.Reason)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" || command.ExpectedRevision < 1 || command.ManagerGrantID == "" || command.LaunchProfileID == "" || len(command.ConfigurationHash) != 64 || strings.Trim(command.ConfigurationHash, "0123456789abcdef") != "" || !validDecisionNote(command.Reason) {
		return MutationResult[domain.OwnerExecutiveBinding]{}, &Error{Code: CodeInvalidOwnerConversation, Message: "executive reconfiguration requires exact scope, binding revision, replacement grant/profile, and reason"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidOwnerConversation); err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	requestHash, err := hashCommand("owner.executive.reconfigure", map[string]any{
		"workspace": command.WorkspaceIdentifier, "project": command.ProjectIdentifier,
		"expected_revision": command.ExpectedRevision, "configuration_hash": command.ConfigurationHash,
	})
	if err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, storageFailure("hash owner executive reconfiguration", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, storageFailure("begin owner executive reconfiguration", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.OwnerExecutiveBinding]
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "owner.executive.reconfigure", requestHash, &replay); err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	binding, found, err := ownerExecutiveBindingInTransaction(ctx, tx, project.ID)
	if err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	if !found || binding.Status != "active" {
		return MutationResult[domain.OwnerExecutiveBinding]{}, &Error{Code: CodeOwnerConversationNotFound, Message: "project executive is not configured"}
	}
	if binding.Revision != command.ExpectedRevision {
		return MutationResult[domain.OwnerExecutiveBinding]{}, revisionConflict("owner executive binding", binding.ID, command.ExpectedRevision, binding.Revision)
	}
	if binding.ManagerGrantID == command.ManagerGrantID || binding.LaunchProfileID == command.LaunchProfileID {
		return MutationResult[domain.OwnerExecutiveBinding]{}, &Error{Code: CodeInvalidOwnerConversation, Message: "executive reconfiguration requires a new exact grant and management launch profile"}
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM owner_executive_exchanges exchange
WHERE exchange.binding_id=? AND exchange.status IN ('pending','leased','running')
) OR EXISTS(
SELECT 1 FROM runs run WHERE run.task_id=? AND run.status IN ('requested','starting','active','blocked','stopping','lost')
)`, binding.ID, binding.PlanningTaskID).Scan(&busy); err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, storageFailure("check owner executive reconfiguration activity", err)
	}
	if busy != 0 {
		return MutationResult[domain.OwnerExecutiveBinding]{}, &Error{Code: CodeOwnerTurnConflict, Message: "project executive authority cannot change while an executive exchange is live"}
	}
	grant, err := queryManagerGrant(ctx, tx, workspace.ID, command.ManagerGrantID)
	if err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	profile, err := queryLaunchProfile(ctx, tx, workspace.ID, command.LaunchProfileID)
	if err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	if grant.Status != domain.ManagerGrantActive || grant.ProjectID != binding.ProjectID || grant.ObjectiveID != binding.ObjectiveID || grant.TaskID != binding.PlanningTaskID || grant.AgentID != binding.AgentID ||
		profile.Status != domain.LaunchProfileActive || profile.ProjectID != binding.ProjectID || profile.AgentID != binding.AgentID || profile.ManagerGrantID != grant.ID {
		return MutationResult[domain.OwnerExecutiveBinding]{}, &Error{Code: CodeInvalidOwnerConversation, Message: "replacement executive grant/profile does not match the current project authority tuple"}
	}
	priorGrant, err := queryManagerGrant(ctx, tx, workspace.ID, binding.ManagerGrantID)
	if err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	priorProfile, err := queryLaunchProfile(ctx, tx, workspace.ID, binding.LaunchProfileID)
	if err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	if priorGrant.Status != domain.ManagerGrantActive || priorProfile.Status != domain.LaunchProfileActive {
		return MutationResult[domain.OwnerExecutiveBinding]{}, &Error{Code: CodeOwnerTurnConflict, Message: "current executive authority was already retired"}
	}
	now := s.nowText()
	binding.ManagerGrantID = grant.ID
	binding.LaunchProfileID = profile.ID
	binding.Revision++
	binding.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `UPDATE owner_executive_bindings SET manager_grant_id=?,launch_profile_id=?,revision=?,updated_at=?,updated_by='local-owner' WHERE id=?`, binding.ManagerGrantID, binding.LaunchProfileID, binding.Revision, now, binding.ID); err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, storageFailure("replace owner executive binding", err)
	}
	priorGrant.Status, priorGrant.Revision, priorGrant.UpdatedAt, priorGrant.UpdatedBy = domain.ManagerGrantRevoked, priorGrant.Revision+1, now, localOwnerActorID
	if _, err := tx.ExecContext(ctx, `UPDATE manager_grants SET status='revoked',revision=?,updated_at=?,updated_by=? WHERE id=?`, priorGrant.Revision, now, localOwnerActorID, priorGrant.ID); err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, storageFailure("retire prior executive grant", err)
	}
	priorProfile.Status, priorProfile.Revision, priorProfile.UpdatedAt, priorProfile.UpdatedBy = domain.LaunchProfileRetired, priorProfile.Revision+1, now, localOwnerActorID
	if _, err := tx.ExecContext(ctx, `UPDATE launch_profiles SET status='retired',revision=?,updated_at=?,updated_by=? WHERE id=?`, priorProfile.Revision, now, localOwnerActorID, priorProfile.ID); err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, storageFailure("retire prior executive profile", err)
	}
	if _, err := appendEvent(ctx, tx, workspace.ID, "manager_grant", priorGrant.ID, priorGrant.Revision, managerGrantRevokedEvent, command.CorrelationID, now, map[string]any{"reason": command.Reason}); err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	if _, err := appendEvent(ctx, tx, workspace.ID, "launch_profile", priorProfile.ID, priorProfile.Revision, launchProfileRetiredEvent, command.CorrelationID, now, map[string]any{"reason": command.Reason}); err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "owner_executive_binding", binding.ID, binding.Revision, ownerExecutiveReconfiguredEvent, command.CorrelationID, now, map[string]any{
		"project_id": binding.ProjectID, "manager_grant_id": binding.ManagerGrantID, "launch_profile_id": binding.LaunchProfileID, "reason": command.Reason,
	})
	if err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	result := MutationResult[domain.OwnerExecutiveBinding]{Value: binding, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "owner.executive.reconfigure", requestHash, result, now); err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.OwnerExecutiveBinding]{}, storageFailure("commit owner executive reconfiguration", err)
	}
	return result, nil
}

func (s *Store) OwnerExecutiveBinding(ctx context.Context, workspaceIdentifier, projectIdentifier string) (domain.OwnerExecutiveBinding, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.OwnerExecutiveBinding{}, storageFailure("begin owner executive binding read", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.OwnerExecutiveBinding{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, strings.TrimSpace(projectIdentifier))
	if err != nil {
		return domain.OwnerExecutiveBinding{}, err
	}
	value, found, err := ownerExecutiveBindingInTransaction(ctx, tx, project.ID)
	if err != nil {
		return domain.OwnerExecutiveBinding{}, err
	}
	if !found {
		return domain.OwnerExecutiveBinding{}, &Error{Code: CodeOwnerConversationNotFound, Message: "project executive is not configured"}
	}
	return value, nil
}

// EnsureImplementationWorkerCanDisable proves that removing a worker cannot
// strand accepted work or detach a live runtime. The owner must first let the
// existing work finish or explicitly replan it.
func (s *Store) EnsureImplementationWorkerCanDisable(ctx context.Context, workspaceIdentifier, projectIdentifier, agentIdentifier string) error {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return storageFailure("begin worker disable check", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, strings.TrimSpace(projectIdentifier))
	if err != nil {
		return err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, strings.TrimSpace(agentIdentifier))
	if err != nil {
		return err
	}
	if !agent.Enabled {
		return &Error{Code: CodeInvalidAgent, Message: "implementation worker is already disabled"}
	}
	var executive int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owner_executive_bindings WHERE project_id=? AND agent_id=? AND status='active')`, project.ID, agent.ID).Scan(&executive); err != nil {
		return storageFailure("check worker executive authority", err)
	}
	if executive != 0 {
		return &Error{Code: CodeInvalidAgent, Message: "the project executive cannot be removed as an implementation worker"}
	}
	var retained int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM task_assignments assignment JOIN tasks task ON task.id=assignment.task_id
WHERE assignment.agent_id=? AND assignment.status='active' AND task.project_id=?
) OR EXISTS(
SELECT 1 FROM runs run WHERE run.agent_id=? AND run.project_id=?
 AND run.status IN ('requested','starting','active','blocked','stopping','lost')
)`, agent.ID, project.ID, agent.ID, project.ID).Scan(&retained); err != nil {
		return storageFailure("check worker retained work", err)
	}
	if retained != 0 {
		return &Error{Code: CodeInvalidAgent, Message: "implementation worker still owns accepted or live work; finish or replan that work before disabling it"}
	}
	return nil
}

func (s *Store) OwnerExecutiveBindingForExchange(ctx context.Context, exchangeID string) (domain.OwnerExecutiveBinding, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.OwnerExecutiveBinding{}, storageFailure("begin owner executive exchange authority read", err)
	}
	defer tx.Rollback()
	var bindingID string
	if err := tx.QueryRowContext(ctx, `SELECT binding_id FROM owner_executive_exchanges WHERE id=?`, strings.TrimSpace(exchangeID)).Scan(&bindingID); err != nil {
		return domain.OwnerExecutiveBinding{}, &Error{Code: CodeOwnerConversationNotFound, Message: "owner executive exchange authority was not found", Cause: err}
	}
	value, found, err := ownerExecutiveBindingByIDInTransaction(ctx, tx, bindingID)
	if err != nil {
		return domain.OwnerExecutiveBinding{}, err
	}
	if !found || value.Status != "active" {
		return domain.OwnerExecutiveBinding{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner executive binding is not active"}
	}
	return value, nil
}

func ownerExecutiveBindingInTransaction(ctx context.Context, tx *sql.Tx, projectID string) (domain.OwnerExecutiveBinding, bool, error) {
	var value domain.OwnerExecutiveBinding
	err := tx.QueryRowContext(ctx, `SELECT id,workspace_id,project_id,objective_id,planning_task_id,agent_id,manager_grant_id,launch_profile_id,status,revision,created_at,updated_at FROM owner_executive_bindings WHERE project_id=?`, projectID).Scan(&value.ID, &value.WorkspaceID, &value.ProjectID, &value.ObjectiveID, &value.PlanningTaskID, &value.AgentID, &value.ManagerGrantID, &value.LaunchProfileID, &value.Status, &value.Revision, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OwnerExecutiveBinding{}, false, nil
	}
	if err != nil {
		return domain.OwnerExecutiveBinding{}, false, storageFailure("read owner executive binding", err)
	}
	return value, true, nil
}

func ownerExecutiveReviewEnabledInTransaction(ctx context.Context, tx *sql.Tx, projectID string) (bool, error) {
	var enabled int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM owner_executive_bindings binding
JOIN owner_conversations conversation ON conversation.project_id=binding.project_id
WHERE binding.project_id=? AND binding.status='active' AND conversation.status='open'
)`, projectID).Scan(&enabled)
	if err != nil {
		return false, storageFailure("check owner executive review route", err)
	}
	return enabled == 1, nil
}

func (s *Store) RequestOwnerExecutiveTurn(ctx context.Context, command RequestOwnerExecutiveTurnCommand) (OwnerExecutiveTurnResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.ConversationID = strings.TrimSpace(command.ConversationID)
	command.Instruction = strings.TrimSpace(command.Instruction)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.Kind = strings.TrimSpace(command.Kind)
	command.InitiatedBy = strings.TrimSpace(command.InitiatedBy)
	if command.Kind == "" {
		command.Kind = "instruction"
	}
	if command.InitiatedBy == "" {
		command.InitiatedBy = "owner"
	}
	validOwner := command.Kind == "instruction" && command.InitiatedBy == "owner" && command.TriggerEventSequence == 0
	validReview := command.Kind == "review" && command.InitiatedBy == "executive" && command.TriggerEventSequence > 0 && command.ConversationID != ""
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" || !validManagerText(command.Instruction, 4096) || command.IdempotencyKey == "" || len(command.IdempotencyKey) > 128 || (!validOwner && !validReview) {
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner executive turn requires exact scope, origin, instruction, and idempotency"}
	}
	if command.Snapshot.WorkspaceID == "" || command.Snapshot.ProjectID == "" || command.Snapshot.EventSequence < 0 || len(command.Snapshot.CanonicalContext) == 0 || len(command.Snapshot.CanonicalContext) > 256*1024 || !json.Valid(command.Snapshot.CanonicalContext) {
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner executive turn requires one frozen canonical context"}
	}
	requestHash, err := hashCommand("owner.executive.turn", map[string]any{"workspace": command.WorkspaceIdentifier, "project": command.ProjectIdentifier, "conversation": command.ConversationID, "instruction": command.Instruction, "kind": command.Kind, "initiated_by": command.InitiatedBy, "trigger_event_sequence": command.TriggerEventSequence})
	if err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("hash owner executive turn", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("begin owner executive turn", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return OwnerExecutiveTurnResult{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return OwnerExecutiveTurnResult{}, err
	}
	// Replay is resolved before comparing a newly-read snapshot cut. Dispatching
	// the original exchange can itself append run events; that must not turn an
	// exact HTTP retry into a false state-change conflict or a second model turn.
	if existing, found, replayErr := ownerExecutiveTurnReplayInTransaction(ctx, tx, command.IdempotencyKey, requestHash, workspace.ID, project.ID); replayErr != nil {
		return OwnerExecutiveTurnResult{}, replayErr
	} else if found {
		return existing, nil
	}
	if command.Snapshot.WorkspaceID != workspace.ID || command.Snapshot.ProjectID != project.ID {
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner executive context belongs to a different project"}
	}
	var highWater int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?`, workspace.ID).Scan(&highWater); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("read owner executive event cut", err)
	}
	if highWater != command.Snapshot.EventSequence || validReview && command.TriggerEventSequence > highWater {
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeOwnerTurnConflict, Message: "canonical project state changed before the executive turn was frozen"}
	}
	binding, found, err := ownerExecutiveBindingInTransaction(ctx, tx, project.ID)
	if err != nil {
		return OwnerExecutiveTurnResult{}, err
	}
	if !found || binding.Status != "active" {
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeOwnerConversationNotFound, Message: "project executive is not configured"}
	}
	if validReview {
		var busy int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM owner_executive_exchanges exchange
WHERE exchange.binding_id=? AND exchange.status IN ('pending','leased','running')
) OR EXISTS(
SELECT 1 FROM runs run
WHERE run.task_id=? AND run.status IN ('requested','starting','active','blocked','stopping','lost')
)`, binding.ID, binding.PlanningTaskID).Scan(&busy); err != nil {
			return OwnerExecutiveTurnResult{}, storageFailure("check owner executive review serialization", err)
		}
		if busy != 0 {
			return OwnerExecutiveTurnResult{}, &Error{Code: CodeOwnerTurnConflict, Message: ownerExecutiveReviewBusyMessage}
		}
	}
	conversation, err := ownerConversationForCommand(ctx, tx, workspace.ID, project.ID, command.ConversationID, command.Instruction, "owner")
	if err != nil {
		return OwnerExecutiveTurnResult{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM owner_turns WHERE conversation_id=?`, conversation.ID).Scan(&count); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("count owner executive turns", err)
	}
	if count >= maximumOwnerConversationTurns {
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner conversation reached its bounded turn limit"}
	}
	turnID, err := randomID("turn_")
	if err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("generate owner executive turn id", err)
	}
	exchangeID, err := randomID("execx_")
	if err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("generate owner executive exchange id", err)
	}
	emptyPlanJSON, emptyPlanHash, _ := canonicalContent([]ownerPlanOperation{})
	emptyInterpretationJSON, _, _ := canonicalContent(domain.OwnerInterpretation{
		Disposition: "pending", Choices: []domain.OwnerChoice{}, Tasks: []domain.OwnerPlanTask{}, CitationRefs: []string{},
	})
	emptyCitationsJSON, _, _ := canonicalContent([]domain.OwnerCitation{})
	citations := make([]domain.OwnerCitation, 0, len(command.Snapshot.Citations))
	for _, citation := range command.Snapshot.Citations {
		citations = append(citations, citation)
	}
	sort.Slice(citations, func(i, j int) bool { return citations[i].Ref < citations[j].Ref })
	allCitationsJSON, _, err := canonicalContent(citations)
	if err != nil || len(allCitationsJSON) > 131072 {
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner executive citation namespace exceeds its bound", Cause: err}
	}
	now := s.nowText()
	var trigger any
	if command.TriggerEventSequence > 0 {
		trigger = command.TriggerEventSequence
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO owner_turns(id,conversation_id,ordinal,kind,initiated_by,trigger_event_sequence,instruction,status,as_of_event_sequence,answer,interpretation_json,citations_json,plan_json,plan_sha256,error_code,completed_event_sequence,idempotency_key,request_sha256,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'queued',?,NULL,?,?,?, ?,NULL,NULL,?,?,1,?,?)`, turnID, conversation.ID, count+1, command.Kind, command.InitiatedBy, trigger, command.Instruction, highWater, string(emptyInterpretationJSON), string(emptyCitationsJSON), string(emptyPlanJSON), emptyPlanHash, command.IdempotencyKey, requestHash, now, now); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("insert owner executive turn", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO owner_executive_exchanges(id,turn_id,binding_id,run_id,event_sequence,context_json,citations_json,proposal_ids_json,status,attempts,available_at,lease_expires_at,last_error,created_at,updated_at) VALUES(?,?,?,NULL,?,?,?,'[]','pending',0,?,NULL,NULL,?,?)`, exchangeID, turnID, binding.ID, highWater, string(command.Snapshot.CanonicalContext), string(allCitationsJSON), now, now, now); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("insert owner executive exchange", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE owner_conversations SET revision=revision+1,updated_at=?,updated_by=? WHERE id=?`, now, map[bool]string{true: "subsystem:owner-manager", false: localOwnerActorID}[validReview], conversation.ID); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("advance owner executive conversation", err)
	}
	if err := tx.Commit(); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("commit owner executive turn", err)
	}
	return s.OwnerExecutiveTurn(ctx, workspace.ID, turnID)
}

// OwnerExecutiveReviewTemporarilyBusy identifies a coalescing boundary, not a
// failed interpretation. The durable manager-review cursor must remain pending
// until the earlier short-lived executive exchange has finished.
func OwnerExecutiveReviewTemporarilyBusy(err error) bool {
	var typed *Error
	return ErrorCode(err) == CodeOwnerTurnConflict && errors.As(err, &typed) && typed.Message == ownerExecutiveReviewBusyMessage
}

func ownerExecutiveTurnReplayInTransaction(ctx context.Context, tx *sql.Tx, key, requestHash, workspaceID, projectID string) (OwnerExecutiveTurnResult, bool, error) {
	var turnID, existingHash, existingWorkspace, existingProject string
	err := tx.QueryRowContext(ctx, `SELECT turn.id,turn.request_sha256,conversation.workspace_id,conversation.project_id FROM owner_turns turn JOIN owner_conversations conversation ON conversation.id=turn.conversation_id WHERE turn.idempotency_key=?`, key).Scan(&turnID, &existingHash, &existingWorkspace, &existingProject)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerExecutiveTurnResult{}, false, nil
	}
	if err != nil {
		return OwnerExecutiveTurnResult{}, false, storageFailure("read owner executive turn replay", err)
	}
	if existingHash != requestHash || existingWorkspace != workspaceID || existingProject != projectID {
		return OwnerExecutiveTurnResult{}, false, &Error{Code: CodeIdempotencyConflict, Message: "owner executive turn idempotency key was used with different content"}
	}
	detail, err := ownerTurnDetailInTransaction(ctx, tx, turnID)
	if err != nil {
		return OwnerExecutiveTurnResult{}, false, err
	}
	exchange, err := ownerExecutiveExchangeByTurnInTransaction(ctx, tx, turnID)
	return OwnerExecutiveTurnResult{Detail: detail, Exchange: exchange}, err == nil, err
}

func (s *Store) OwnerExecutiveTurn(ctx context.Context, workspaceIdentifier, turnID string) (OwnerExecutiveTurnResult, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("begin owner executive turn read", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return OwnerExecutiveTurnResult{}, err
	}
	detail, err := ownerTurnDetailInTransaction(ctx, tx, strings.TrimSpace(turnID))
	if err != nil {
		return OwnerExecutiveTurnResult{}, err
	}
	if detail.Conversation.WorkspaceID != workspace.ID {
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeOwnerConversationNotFound, Message: "owner executive turn is outside the selected workspace"}
	}
	exchange, err := ownerExecutiveExchangeByTurnInTransaction(ctx, tx, detail.Turn.ID)
	return OwnerExecutiveTurnResult{Detail: detail, Exchange: exchange}, err
}

func (s *Store) ClaimOwnerExecutiveExchange(ctx context.Context) (domain.OwnerExecutiveExchange, bool, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.OwnerExecutiveExchange{}, false, storageFailure("begin owner executive claim", err)
	}
	defer tx.Rollback()
	now := s.nowText()
	if _, err := tx.ExecContext(ctx, `UPDATE owner_executive_exchanges SET status='pending',lease_expires_at=NULL,available_at=?,updated_at=? WHERE status='leased' AND lease_expires_at<=?`, now, now, now); err != nil {
		return domain.OwnerExecutiveExchange{}, false, storageFailure("recover owner executive exchange leases", err)
	}
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM owner_executive_exchanges WHERE status='pending' AND available_at<=? ORDER BY available_at,id LIMIT 1`, now).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		if commitErr := tx.Commit(); commitErr != nil {
			return domain.OwnerExecutiveExchange{}, false, storageFailure("finish empty owner executive claim", commitErr)
		}
		return domain.OwnerExecutiveExchange{}, false, nil
	} else if err != nil {
		return domain.OwnerExecutiveExchange{}, false, storageFailure("select owner executive exchange", err)
	}
	expires := s.clock().UTC().Add(ownerExecutiveExchangeLease).Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE owner_executive_exchanges SET status='leased',attempts=attempts+1,lease_expires_at=?,updated_at=? WHERE id=? AND status='pending'`, expires, now, id)
	if err != nil {
		return domain.OwnerExecutiveExchange{}, false, storageFailure("lease owner executive exchange", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return domain.OwnerExecutiveExchange{}, false, &Error{Code: CodeOwnerTurnConflict, Message: "owner executive exchange changed while being claimed"}
	}
	exchange, err := ownerExecutiveExchangeInTransaction(ctx, tx, id)
	if err != nil {
		return domain.OwnerExecutiveExchange{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.OwnerExecutiveExchange{}, false, storageFailure("commit owner executive claim", err)
	}
	return exchange, true, nil
}

func (s *Store) DispatchOwnerExecutiveExchange(ctx context.Context, exchangeID, runID string) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin owner executive dispatch", err)
	}
	defer tx.Rollback()
	exchange, err := ownerExecutiveExchangeInTransaction(ctx, tx, strings.TrimSpace(exchangeID))
	if err != nil {
		return err
	}
	if exchange.Status == "running" && exchange.RunID == strings.TrimSpace(runID) {
		return nil
	}
	if exchange.Status != "leased" || strings.TrimSpace(runID) == "" {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner executive dispatch does not match its lease"}
	}
	var taskID, agentID, grantID string
	if err := tx.QueryRowContext(ctx, `SELECT run.task_id,run.agent_id,packet.manager_grant_id FROM runs run JOIN run_context_bindings binding ON binding.run_id=run.id JOIN context_packets packet_row ON packet_row.id=binding.context_packet_id JOIN (SELECT id,json_extract(packet_json,'$.management_grant.grant_id') manager_grant_id FROM context_packets) packet ON packet.id=packet_row.id WHERE run.id=?`, strings.TrimSpace(runID)).Scan(&taskID, &agentID, &grantID); err != nil {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner executive run does not carry manager authority", Cause: err}
	}
	binding, found, err := ownerExecutiveBindingByIDInTransaction(ctx, tx, exchange.BindingID)
	if err != nil || !found {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner executive binding is unavailable", Cause: err}
	}
	if taskID != binding.PlanningTaskID || agentID != binding.AgentID || grantID != binding.ManagerGrantID {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner executive run differs from its exact binding"}
	}
	now := s.nowText()
	if _, err := tx.ExecContext(ctx, `UPDATE owner_executive_exchanges SET run_id=?,status='running',attempts=0,lease_expires_at=NULL,last_error=NULL,updated_at=? WHERE id=? AND status='leased'`, runID, now, exchange.ID); err != nil {
		return storageFailure("bind owner executive run", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE owner_turns SET status='running',revision=revision+1,updated_at=? WHERE id=? AND status='queued'`, now, exchange.TurnID); err != nil {
		return storageFailure("start owner executive turn", err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit owner executive dispatch", err)
	}
	return nil
}

func (s *Store) RetryOwnerExecutiveExchange(ctx context.Context, exchangeID string, cause error) error {
	message := managerReviewDiagnostic(cause)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin owner executive retry", err)
	}
	defer tx.Rollback()
	now := s.nowText()
	available := s.clock().UTC().Add(time.Second).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE owner_executive_exchanges SET status='pending',available_at=?,lease_expires_at=NULL,last_error=?,updated_at=? WHERE id=? AND status='leased'`, available, message, now, strings.TrimSpace(exchangeID)); err != nil {
		return storageFailure("retry owner executive exchange", err)
	}
	return tx.Commit()
}

// DeferOwnerExecutiveExchange returns an otherwise-valid exchange to the
// durable queue without consuming the claim attempt. This is used when the
// one project-executive assignment is still occupied by its previous
// short-lived session or a bounded admission ceiling.
func (s *Store) DeferOwnerExecutiveExchange(ctx context.Context, exchangeID string, cause error) error {
	message := managerReviewDiagnostic(cause)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin owner executive deferral", err)
	}
	defer tx.Rollback()
	now := s.nowText()
	available := s.clock().UTC().Add(time.Second).Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE owner_executive_exchanges SET status='pending',attempts=CASE WHEN attempts>0 THEN attempts-1 ELSE 0 END,available_at=?,lease_expires_at=NULL,last_error=?,updated_at=? WHERE id=? AND status='leased'`, available, message, now, strings.TrimSpace(exchangeID))
	if err != nil {
		return storageFailure("defer owner executive exchange", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner executive exchange changed while being deferred"}
	}
	return tx.Commit()
}

func (s *Store) FailOwnerExecutiveExchange(ctx context.Context, exchangeID string, cause error) error {
	message := managerReviewDiagnostic(cause)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin owner executive failure", err)
	}
	defer tx.Rollback()
	exchange, err := ownerExecutiveExchangeInTransaction(ctx, tx, strings.TrimSpace(exchangeID))
	if err != nil {
		return err
	}
	if exchange.Status == "failed" {
		return nil
	}
	if exchange.Status == "responded" {
		return &Error{Code: CodeOwnerTurnConflict, Message: "responded owner executive exchange cannot fail"}
	}
	now := s.nowText()
	if _, err := tx.ExecContext(ctx, `UPDATE owner_executive_exchanges SET status='failed',lease_expires_at=NULL,last_error=?,updated_at=? WHERE id=?`, message, now, exchange.ID); err != nil {
		return storageFailure("fail owner executive exchange", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE owner_turns SET status='failed',error_code='executive_unavailable',revision=revision+1,updated_at=? WHERE id=? AND status IN ('queued','running')`, now, exchange.TurnID); err != nil {
		return storageFailure("fail owner executive turn", err)
	}
	return tx.Commit()
}

func (s *Store) RecoverOwnerExecutiveExchangeLeases(ctx context.Context) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin owner executive recovery", err)
	}
	defer tx.Rollback()
	now := s.nowText()
	if _, err := tx.ExecContext(ctx, `UPDATE owner_executive_exchanges SET status='pending',lease_expires_at=NULL,available_at=?,updated_at=? WHERE status='leased'`, now, now); err != nil {
		return storageFailure("recover owner executive exchange leases", err)
	}
	return tx.Commit()
}

func (s *Store) OwnerExecutiveContext(ctx context.Context, runID string) (domain.OwnerExecutiveContext, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.OwnerExecutiveContext{}, storageFailure("begin owner executive context", err)
	}
	defer tx.Rollback()
	exchange, contextJSON, err := ownerExecutiveExchangeByRunInTransaction(ctx, tx, strings.TrimSpace(runID))
	if err != nil {
		return domain.OwnerExecutiveContext{}, err
	}
	detail, err := ownerTurnDetailInTransaction(ctx, tx, exchange.TurnID)
	if err != nil {
		return domain.OwnerExecutiveContext{}, err
	}
	return domain.OwnerExecutiveContext{Exchange: exchange, Turn: detail.Turn, Context: json.RawMessage(contextJSON)}, nil
}

func (s *Store) RespondOwnerExecutive(ctx context.Context, command RespondOwnerExecutiveCommand) (OwnerExecutiveTurnResult, error) {
	command.RunID = strings.TrimSpace(command.RunID)
	command.ResponseKind = strings.TrimSpace(command.ResponseKind)
	command.Summary = strings.TrimSpace(command.Summary)
	command.Answer = strings.TrimSpace(command.Answer)
	command.Question = strings.TrimSpace(command.Question)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	if command.RunID == "" || command.IdempotencyKey == "" || len(command.IdempotencyKey) > 128 || !validOptionalOwnerText(command.Summary, 2048) || len(command.CitationRefs) > 16 || len(command.ProposalIDs) > 32 {
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner executive response exceeds its bounded contract"}
	}
	citationRefs := make([]string, len(command.CitationRefs))
	copy(citationRefs, command.CitationRefs)
	interpretation := domain.OwnerInterpretation{Summary: command.Summary, Choices: []domain.OwnerChoice{}, Tasks: []domain.OwnerPlanTask{}, CitationRefs: citationRefs}
	switch command.ResponseKind {
	case "answer", "update", "proposal":
		if !validOwnerExecutiveResponseText(command.Answer, 8192) || command.Question != "" || len(command.Choices) != 0 {
			return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner executive answer is malformed"}
		}
		interpretation.Disposition, interpretation.Answer = "answer", command.Answer
	case "decision":
		if !validOwnerExecutiveDecision(command.Question, command.Answer, command.Choices) {
			return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner executive decision is malformed"}
		}
		interpretation.Disposition, interpretation.Question, interpretation.Choices = "clarify", command.Question, append([]domain.OwnerChoice(nil), command.Choices...)
	case "refusal":
		if !validOwnerExecutiveResponseText(command.Answer, 8192) || command.Question != "" || len(command.Choices) != 0 {
			return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner executive refusal is malformed"}
		}
		interpretation.Disposition, interpretation.Answer = "refuse", command.Answer
	default:
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner executive response kind is unsupported"}
	}
	requestHash, err := hashCommand("owner.executive.respond", command)
	if err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("hash owner executive response", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("begin owner executive response", err)
	}
	defer tx.Rollback()
	exchange, _, err := ownerExecutiveExchangeByRunInTransaction(ctx, tx, command.RunID)
	if err != nil {
		return OwnerExecutiveTurnResult{}, err
	}
	if exchange.Status == "responded" {
		var existingHash string
		if err := tx.QueryRowContext(ctx, `SELECT request_hash FROM run_reports WHERE run_id=? AND idempotency_key=?`, command.RunID, command.IdempotencyKey).Scan(&existingHash); err == nil && existingHash == requestHash {
			detail, detailErr := ownerTurnDetailInTransaction(ctx, tx, exchange.TurnID)
			return OwnerExecutiveTurnResult{Detail: detail, Exchange: exchange}, detailErr
		}
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner executive exchange already has its response"}
	}
	if exchange.Status != "running" {
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeOwnerTurnConflict, Message: "owner executive response requires its live exchange"}
	}
	var runStatus, workspaceID string
	var currentRunRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT status,workspace_id,revision FROM runs WHERE id=?`, command.RunID).Scan(&runStatus, &workspaceID, &currentRunRevision); err != nil || runStatus != domain.RunActive {
		return OwnerExecutiveTurnResult{}, &Error{Code: CodeRunConflict, Message: "owner executive response requires its active run", Cause: err}
	}
	var citationJSON string
	if err := tx.QueryRowContext(ctx, `SELECT citations_json FROM owner_executive_exchanges WHERE id=?`, exchange.ID).Scan(&citationJSON); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("read owner executive citations", err)
	}
	var available []domain.OwnerCitation
	if err := json.Unmarshal([]byte(citationJSON), &available); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("decode owner executive citations", err)
	}
	byRef := make(map[string]domain.OwnerCitation, len(available))
	for _, value := range available {
		byRef[value.Ref] = value
	}
	selected := make([]domain.OwnerCitation, 0, len(command.CitationRefs))
	seenRefs := map[string]bool{}
	for _, ref := range command.CitationRefs {
		if seenRefs[ref] {
			return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner executive repeated a citation"}
		}
		value, ok := byRef[ref]
		if !ok {
			return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: fmt.Sprintf("owner executive cited unknown ref %q", ref)}
		}
		seenRefs[ref] = true
		selected = append(selected, value)
	}
	proposalIDs, err := canonicalIDs(command.ProposalIDs, 0, 32, "owner executive proposals", CodeInvalidOwnerConversation)
	if err != nil {
		return OwnerExecutiveTurnResult{}, err
	}
	for _, proposalID := range proposalIDs {
		var sourceRun string
		if err := tx.QueryRowContext(ctx, `SELECT source_run_id FROM manager_proposals WHERE id=?`, proposalID).Scan(&sourceRun); err != nil || sourceRun != command.RunID {
			return OwnerExecutiveTurnResult{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner executive response linked a proposal outside its run", Cause: err}
		}
	}
	interpretationJSON, _, _ := canonicalContent(interpretation)
	selectedJSON, _, _ := canonicalContent(selected)
	proposalJSON, _, _ := canonicalContent(proposalIDs)
	answer := interpretation.Answer
	if interpretation.Disposition == "clarify" {
		answer = interpretation.Question
	}
	now := s.nowText()
	if _, err := tx.ExecContext(ctx, `UPDATE owner_turns SET status='completed',answer=?,interpretation_json=?,citations_json=?,revision=revision+1,updated_at=? WHERE id=? AND status='running'`, answer, string(interpretationJSON), string(selectedJSON), now, exchange.TurnID); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("complete owner executive turn", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE owner_executive_exchanges SET status='responded',proposal_ids_json=?,last_error=NULL,updated_at=? WHERE id=? AND status='running'`, string(proposalJSON), now, exchange.ID); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("complete owner executive exchange", err)
	}
	reportID, err := randomID("report_")
	if err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("generate owner executive report", err)
	}
	payloadJSON, _, _ := canonicalContent(map[string]any{"response_kind": command.ResponseKind, "proposal_ids": proposalIDs})
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_reports(id,run_id,kind,message,evidence_json,handoff,payload_json,idempotency_key,request_hash,status,created_at) VALUES(?,?,?,?,'[]',NULL,?,?,?,'pending',?)`, reportID, command.RunID, domain.ObservationExecutiveResponse, command.Summary, string(payloadJSON), command.IdempotencyKey, requestHash, now); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("insert owner executive response report", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_jobs SET status='pending',available_at=?,lease_expires_at=NULL,updated_at=? WHERE run_id=? AND status='complete'`, now, now, command.RunID); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("wake owner executive run", err)
	}
	if _, err := appendEventForActor(ctx, tx, workspaceID, "run", command.RunID, currentRunRevision, runReportReceivedEvent, "report-"+reportID, now, command.RunID, domain.EventActorAgentRun, map[string]any{"report_id": reportID, "kind": domain.ObservationExecutiveResponse}); err != nil {
		return OwnerExecutiveTurnResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return OwnerExecutiveTurnResult{}, storageFailure("commit owner executive response", err)
	}
	return s.OwnerExecutiveTurn(ctx, workspaceID, exchange.TurnID)
}

// Executive answers are durable human-readable conversation, not compact
// manager identifiers. Preserve bounded paragraph breaks while rejecting NUL
// and malformed UTF-8 at the Store boundary.
func validOwnerExecutiveResponseText(value string, maximum int) bool {
	return validMessageText(value, maximum)
}

func validOwnerExecutiveDecision(question, answer string, choices []domain.OwnerChoice) bool {
	return validManagerText(question, 2048) && answer == "" && len(choices) >= 2 && validateOwnerChoices(choices) == nil
}

func failOwnerExecutiveRunInTransaction(ctx context.Context, tx *sql.Tx, runID, message, now string) (bool, error) {
	var exchangeID, turnID, status string
	err := tx.QueryRowContext(ctx, `SELECT id,turn_id,status FROM owner_executive_exchanges WHERE run_id=?`, runID).Scan(&exchangeID, &turnID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storageFailure("read owner executive run exchange", err)
	}
	// A response is already durable owner-visible truth. A later runtime teardown
	// may fail the run, but must not retract or rewrite that response.
	if status == "responded" {
		return true, nil
	}
	message = managerReviewDiagnostic(errors.New(message))
	result, err := tx.ExecContext(ctx, `UPDATE owner_executive_exchanges SET status='failed',lease_expires_at=NULL,last_error=?,updated_at=? WHERE id=? AND status IN ('leased','running')`, message, now, exchangeID)
	if err != nil {
		return true, storageFailure("fail owner executive run exchange", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return true, &Error{Code: CodeOwnerTurnConflict, Message: "owner executive exchange changed before its run failed"}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE owner_turns SET status='failed',error_code='executive_unavailable',revision=revision+1,updated_at=? WHERE id=? AND status IN ('queued','running')`, now, turnID); err != nil {
		return true, storageFailure("fail owner executive run turn", err)
	}
	return true, nil
}

func (s *Store) IsOwnerExecutiveRun(ctx context.Context, runID string) bool {
	var count int
	return s.db.QueryRowContext(ctx, `SELECT count(*) FROM owner_executive_exchanges WHERE run_id=?`, strings.TrimSpace(runID)).Scan(&count) == nil && count == 1
}

func ownerExecutiveExchangeInTransaction(ctx context.Context, tx *sql.Tx, id string) (domain.OwnerExecutiveExchange, error) {
	var value domain.OwnerExecutiveExchange
	var runID, lease, lastError sql.NullString
	var proposalJSON string
	err := tx.QueryRowContext(ctx, `SELECT id,turn_id,binding_id,run_id,event_sequence,proposal_ids_json,status,attempts,available_at,lease_expires_at,last_error,created_at,updated_at FROM owner_executive_exchanges WHERE id=?`, id).Scan(&value.ID, &value.TurnID, &value.BindingID, &runID, &value.EventSequence, &proposalJSON, &value.Status, &value.Attempts, &value.AvailableAt, &lease, &lastError, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OwnerExecutiveExchange{}, &Error{Code: CodeOwnerConversationNotFound, Message: "owner executive exchange was not found"}
	}
	if err != nil {
		return domain.OwnerExecutiveExchange{}, storageFailure("read owner executive exchange", err)
	}
	value.RunID, value.LeaseExpiresAt, value.LastError = runID.String, lease.String, lastError.String
	if err := json.Unmarshal([]byte(proposalJSON), &value.ProposalIDs); err != nil {
		return domain.OwnerExecutiveExchange{}, storageFailure("decode owner executive proposal links", err)
	}
	return value, nil
}

func ownerExecutiveExchangeByTurnInTransaction(ctx context.Context, tx *sql.Tx, turnID string) (domain.OwnerExecutiveExchange, error) {
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM owner_executive_exchanges WHERE turn_id=?`, turnID).Scan(&id); err != nil {
		return domain.OwnerExecutiveExchange{}, &Error{Code: CodeOwnerConversationNotFound, Message: "owner turn is not an executive exchange", Cause: err}
	}
	return ownerExecutiveExchangeInTransaction(ctx, tx, id)
}

func ownerExecutiveExchangeByRunInTransaction(ctx context.Context, tx *sql.Tx, runID string) (domain.OwnerExecutiveExchange, string, error) {
	var id, contextJSON string
	if err := tx.QueryRowContext(ctx, `SELECT id,context_json FROM owner_executive_exchanges WHERE run_id=?`, runID).Scan(&id, &contextJSON); err != nil {
		return domain.OwnerExecutiveExchange{}, "", &Error{Code: CodeOwnerConversationNotFound, Message: "run is not an owner executive exchange", Cause: err}
	}
	value, err := ownerExecutiveExchangeInTransaction(ctx, tx, id)
	return value, contextJSON, err
}

func ownerExecutiveBindingByIDInTransaction(ctx context.Context, tx *sql.Tx, id string) (domain.OwnerExecutiveBinding, bool, error) {
	var value domain.OwnerExecutiveBinding
	err := tx.QueryRowContext(ctx, `SELECT id,workspace_id,project_id,objective_id,planning_task_id,agent_id,manager_grant_id,launch_profile_id,status,revision,created_at,updated_at FROM owner_executive_bindings WHERE id=?`, id).Scan(&value.ID, &value.WorkspaceID, &value.ProjectID, &value.ObjectiveID, &value.PlanningTaskID, &value.AgentID, &value.ManagerGrantID, &value.LaunchProfileID, &value.Status, &value.Revision, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OwnerExecutiveBinding{}, false, nil
	}
	if err != nil {
		return domain.OwnerExecutiveBinding{}, false, storageFailure("read owner executive binding", err)
	}
	return value, true, nil
}
