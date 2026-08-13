package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	contradictionDetectedEvent      = "contradiction.detected"
	contradictionConfirmedEvent     = "contradiction.confirmed"
	contradictionDismissedEvent     = "contradiction.dismissed"
	contradictionResolvedEvent      = "contradiction.resolved"
	contradictionConfirmDeniedEvent = "contradiction.confirm_denied"
	contradictionDismissDeniedEvent = "contradiction.dismiss_denied"

	ContradictionResolutionParticipantStale      = "participant_stale"
	ContradictionResolutionParticipantSuperseded = "participant_superseded"

	maximumContradictionNoteBytes = 2048
)

func (s *Store) ReportKnowledgeContradiction(ctx context.Context, command ReportKnowledgeContradictionCommand) (KnowledgeContradictionMutationResult, error) {
	normalizeReportKnowledgeContradictionCommand(&command)
	if command.WorkspaceIdentifier == "" || !validKnowledgeActor(command.Actor) ||
		(command.Actor.Type != domain.KnowledgeActorAgentRun && !knowledgeActorIsOwner(command.Actor)) {
		return KnowledgeContradictionMutationResult{}, knowledgeInvalid("contradiction report requires workspace, two distinct exact revision IDs, a bounded reason, and an owner or agent-run actor")
	}
	return s.reportKnowledgeContradiction(ctx, command, "")
}

// ReportRunKnowledgeContradiction derives all authority and applicability scope
// from one exact live run inside the same transaction that records the report.
// MCP callers cannot select a workspace, project, task, or actor through this
// boundary.
func (s *Store) ReportRunKnowledgeContradiction(ctx context.Context, command ReportRunKnowledgeContradictionCommand) (KnowledgeContradictionMutationResult, error) {
	command.RunID = strings.TrimSpace(command.RunID)
	command.LeftRevisionID = strings.TrimSpace(command.LeftRevisionID)
	command.RightRevisionID = strings.TrimSpace(command.RightRevisionID)
	command.ReportNote = strings.TrimSpace(command.ReportNote)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.RunID == "" {
		return KnowledgeContradictionMutationResult{}, knowledgeInvalid("run contradiction report requires an authenticated run")
	}
	return s.reportKnowledgeContradiction(ctx, ReportKnowledgeContradictionCommand{
		LeftRevisionID: command.LeftRevisionID, RightRevisionID: command.RightRevisionID,
		ReportNote: command.ReportNote, IdempotencyKey: command.IdempotencyKey,
		CorrelationID: command.CorrelationID,
	}, command.RunID)
}

func (s *Store) reportKnowledgeContradiction(ctx context.Context, command ReportKnowledgeContradictionCommand, reporterRunID string) (KnowledgeContradictionMutationResult, error) {
	if !validContextKnowledgeRevisionID(command.LeftRevisionID) ||
		!validContextKnowledgeRevisionID(command.RightRevisionID) || command.LeftRevisionID == command.RightRevisionID ||
		!validKnowledgeText(command.ReportNote, maximumContradictionNoteBytes) {
		return KnowledgeContradictionMutationResult{}, knowledgeInvalid("contradiction report requires two distinct exact revision IDs and a bounded reason")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidKnowledge); err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, storageFailure("begin contradiction report", err)
	}
	defer tx.Rollback()
	reporterRunStatus := ""
	if reporterRunID != "" {
		run, runErr := queryRun(ctx, tx, "", reporterRunID)
		if runErr != nil {
			return KnowledgeContradictionMutationResult{}, runErr
		}
		command.WorkspaceIdentifier = run.WorkspaceID
		command.Actor = domain.KnowledgeActor{ID: run.ID, Type: domain.KnowledgeActorAgentRun}
		reporterRunStatus = run.Status
	}
	leftID, rightID := canonicalContradictionPair(command.LeftRevisionID, command.RightRevisionID)
	requestHash, err := hashCommand("contradiction.report", map[string]any{
		"workspace": command.WorkspaceIdentifier, "left_revision_id": leftID,
		"right_revision_id": rightID, "report_note": command.ReportNote, "actor": command.Actor,
	})
	if err != nil {
		return KnowledgeContradictionMutationResult{}, storageFailure("hash contradiction report", err)
	}
	key := contradictionIdempotencyKey(command.Actor, command.IdempotencyKey)
	var replay KnowledgeContradictionMutationResult
	if found, replayErr := lookupIdempotency(ctx, tx, key, "contradiction.report", requestHash, &replay); replayErr != nil {
		return KnowledgeContradictionMutationResult{}, replayErr
	} else if found {
		return replay, nil
	}
	if reporterRunID != "" && reporterRunStatus != domain.RunStarting &&
		reporterRunStatus != domain.RunActive && reporterRunStatus != domain.RunBlocked {
		return KnowledgeContradictionMutationResult{}, &Error{Code: CodeRunConflict, Message: "contradiction reports require a live authenticated run"}
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	queries := dbgen.New(tx)
	if err := validateKnowledgeActorWorkspace(ctx, queries, command.Actor, workspace.ID); err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	left, right, err := s.validateKnowledgeContradictionPairInTransaction(ctx, tx, workspace.ID, leftID, rightID)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	if command.Actor.Type == domain.KnowledgeActorAgentRun {
		runScope, scopeErr := queries.GetContradictionReporterRunScope(ctx, command.Actor.ID)
		if errors.Is(scopeErr, sql.ErrNoRows) {
			return KnowledgeContradictionMutationResult{}, knowledgeInvalid("contradiction reporter run was not found")
		}
		if scopeErr != nil {
			return KnowledgeContradictionMutationResult{}, storageFailure("read contradiction reporter scope", scopeErr)
		}
		if runScope.Status != domain.RunStarting && runScope.Status != domain.RunActive && runScope.Status != domain.RunBlocked {
			return KnowledgeContradictionMutationResult{}, &Error{Code: CodeRunConflict, Message: "contradiction reports require a live authenticated run"}
		}
		if runScope.WorkspaceID != workspace.ID || runScope.ProjectID != left.ProjectID ||
			(left.TaskScopeID != "" && left.TaskScopeID != runScope.TaskID) ||
			(right.TaskScopeID != "" && right.TaskScopeID != runScope.TaskID) {
			return KnowledgeContradictionMutationResult{}, knowledgeInvalid("agent-run contradiction reports require both revisions to apply to the reporter's exact project and task")
		}
	}
	if existing, existingErr := queries.GetKnowledgeContradictionByPair(ctx, dbgen.GetKnowledgeContradictionByPairParams{
		WorkspaceID: workspace.ID, LeftRevisionID: left.ID, RightRevisionID: right.ID,
	}); existingErr == nil {
		return KnowledgeContradictionMutationResult{}, contradictionConflict(fmt.Sprintf("exact revision pair already has contradiction %s", existing.ID))
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return KnowledgeContradictionMutationResult{}, storageFailure("check existing contradiction pair", existingErr)
	}
	id, err := randomID("kcon_")
	if err != nil {
		return KnowledgeContradictionMutationResult{}, storageFailure("generate contradiction id", err)
	}
	now := s.nowText()
	sequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_contradiction", id, 1,
		contradictionDetectedEvent, command.CorrelationID, now, command.Actor.ID, command.Actor.Type,
		contradictionLifecycleEventData(left.ProjectID, left.ID, right.ID, left.TaskScopeID, right.TaskScopeID,
			domain.KnowledgeContradictionProposed, 1))
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	if err := queries.InsertKnowledgeContradiction(ctx, dbgen.InsertKnowledgeContradictionParams{
		ID: id, WorkspaceID: workspace.ID, ProjectID: left.ProjectID,
		LeftRevisionID: left.ID, RightRevisionID: right.ID, ReportNote: command.ReportNote,
		ReportedAt: now, ReportedBy: command.Actor.ID, ReportedByType: command.Actor.Type,
		DetectedEventSequence: sequence,
	}); err != nil {
		return KnowledgeContradictionMutationResult{}, storageFailure("insert contradiction", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	detail, err := s.knowledgeContradictionDetailInTransaction(ctx, tx, workspace.ID, id)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	result := KnowledgeContradictionMutationResult{Detail: detail, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "contradiction.report", requestHash, result, now); err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeContradictionMutationResult{}, storageFailure("commit contradiction report", err)
	}
	return result, nil
}

func (s *Store) ConfirmKnowledgeContradiction(ctx context.Context, command DecideKnowledgeContradictionCommand) (KnowledgeContradictionMutationResult, error) {
	return s.decideKnowledgeContradiction(ctx, domain.KnowledgeContradictionAuthorityConfirm, command)
}

func (s *Store) DismissKnowledgeContradiction(ctx context.Context, command DecideKnowledgeContradictionCommand) (KnowledgeContradictionMutationResult, error) {
	return s.decideKnowledgeContradiction(ctx, domain.KnowledgeContradictionAuthorityDismiss, command)
}

func (s *Store) decideKnowledgeContradiction(ctx context.Context, action string, command DecideKnowledgeContradictionCommand) (KnowledgeContradictionMutationResult, error) {
	normalizeDecideKnowledgeContradictionCommand(&command)
	if command.WorkspaceIdentifier == "" || !validKnowledgeContradictionID(command.ContradictionID) ||
		command.ExpectedStateRevision < 1 || !validKnowledgeActor(command.Actor) ||
		(!knowledgeActorIsOwner(command.Actor) && command.Actor.Type != domain.KnowledgeActorAgentRun) ||
		(command.Note != "" && !validKnowledgeText(command.Note, maximumContradictionNoteBytes)) {
		return KnowledgeContradictionMutationResult{}, knowledgeInvalid("contradiction decision requires workspace, contradiction, expected state revision, trusted actor, and an optional bounded note")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidKnowledge); err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	commandName := "contradiction." + action
	requestHash, err := hashCommand(commandName, map[string]any{
		"workspace": command.WorkspaceIdentifier, "contradiction_id": command.ContradictionID,
		"expected_state_revision": command.ExpectedStateRevision, "note": command.Note, "actor": command.Actor,
	})
	if err != nil {
		return KnowledgeContradictionMutationResult{}, storageFailure("hash contradiction decision", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, storageFailure("begin contradiction decision", err)
	}
	defer tx.Rollback()
	key := contradictionIdempotencyKey(command.Actor, command.IdempotencyKey)
	var replay KnowledgeContradictionMutationResult
	if found, replayErr := lookupIdempotency(ctx, tx, key, commandName, requestHash, &replay); replayErr != nil {
		return KnowledgeContradictionMutationResult{}, replayErr
	} else if found {
		if replay.AuthorityCheck != nil && replay.AuthorityCheck.Outcome == domain.KnowledgeAuthorityDenied {
			return replay, &Error{Code: CodeContradictionDenied, Message: "actor is not authorized to govern knowledge contradictions"}
		}
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	queries := dbgen.New(tx)
	contradictionRow, err := queries.GetKnowledgeContradiction(ctx, dbgen.GetKnowledgeContradictionParams{WorkspaceID: workspace.ID, ID: command.ContradictionID})
	if errors.Is(err, sql.ErrNoRows) {
		return KnowledgeContradictionMutationResult{}, contradictionNotFound(command.ContradictionID)
	}
	if err != nil {
		return KnowledgeContradictionMutationResult{}, storageFailure("read contradiction", err)
	}
	contradiction := knowledgeContradictionFromRow(contradictionRow)
	if err := validateKnowledgeActorWorkspace(ctx, queries, command.Actor, workspace.ID); err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	if !knowledgeActorIsOwner(command.Actor) {
		return s.commitKnowledgeContradictionDenial(ctx, tx, queries, contradiction, action, command, requestHash, key, commandName)
	}
	if contradiction.StateRevision != command.ExpectedStateRevision {
		return KnowledgeContradictionMutationResult{}, &Error{Code: CodeRevisionConflict, Message: fmt.Sprintf("contradiction state revision is %d, expected %d", contradiction.StateRevision, command.ExpectedStateRevision)}
	}
	if action == domain.KnowledgeContradictionAuthorityConfirm {
		if contradiction.Status != domain.KnowledgeContradictionProposed {
			return KnowledgeContradictionMutationResult{}, contradictionConflict("only a proposed contradiction can be confirmed")
		}
		if _, _, err := s.validateKnowledgeContradictionPairInTransaction(ctx, tx, workspace.ID, contradiction.LeftRevisionID, contradiction.RightRevisionID); err != nil {
			if ErrorCode(err) == CodeKnowledgeConflict {
				return KnowledgeContradictionMutationResult{}, contradictionConflict("contradiction participants are no longer eligible for confirmation")
			}
			return KnowledgeContradictionMutationResult{}, err
		}
	} else if contradiction.Status != domain.KnowledgeContradictionProposed && contradiction.Status != domain.KnowledgeContradictionOpen {
		return KnowledgeContradictionMutationResult{}, contradictionConflict("only a proposed or open contradiction can be dismissed")
	}
	now := s.nowText()
	leftRevision, err := s.KnowledgeRevisionInTransaction(ctx, tx, workspace.ID, contradiction.LeftRevisionID)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	rightRevision, err := s.KnowledgeRevisionInTransaction(ctx, tx, workspace.ID, contradiction.RightRevisionID)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	eventType := contradictionConfirmedEvent
	if action == domain.KnowledgeContradictionAuthorityDismiss {
		eventType = contradictionDismissedEvent
	}
	sequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_contradiction", contradiction.ID,
		contradiction.StateRevision+1, eventType, command.CorrelationID, now, command.Actor.ID, command.Actor.Type,
		contradictionDecisionEventData(contradiction, leftRevision.TaskScopeID, rightRevision.TaskScopeID, action, command.Note))
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	check, err := insertKnowledgeContradictionAuthorityCheck(ctx, queries, workspace.ID, contradiction.ID,
		action, command.Actor, domain.KnowledgeAuthorityAllowed, domain.KnowledgeAuthorityReasonOwner,
		command.Note, command.IdempotencyKey, requestHash, sequence, now)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	var rows int64
	if action == domain.KnowledgeContradictionAuthorityConfirm {
		rows, err = queries.ConfirmKnowledgeContradiction(ctx, dbgen.ConfirmKnowledgeContradictionParams{
			ConfirmedAt: &now, ConfirmedBy: &command.Actor.ID, ConfirmedByType: &command.Actor.Type,
			ConfirmNote: optionalStringPointer(command.Note), ConfirmEventSequence: &sequence, ID: contradiction.ID,
			ExpectedStateRevision: command.ExpectedStateRevision,
		})
	} else {
		rows, err = queries.DismissKnowledgeContradiction(ctx, dbgen.DismissKnowledgeContradictionParams{
			DismissedAt: &now, DismissedBy: &command.Actor.ID, DismissedByType: &command.Actor.Type,
			DismissNote: optionalStringPointer(command.Note), DismissEventSequence: &sequence, ID: contradiction.ID,
			ExpectedStateRevision: command.ExpectedStateRevision,
		})
	}
	if err != nil {
		return KnowledgeContradictionMutationResult{}, storageFailure("update contradiction state", err)
	}
	if rows != 1 {
		return KnowledgeContradictionMutationResult{}, &Error{Code: CodeRevisionConflict, Message: "contradiction state changed before the decision was applied"}
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	detail, err := s.knowledgeContradictionDetailInTransaction(ctx, tx, workspace.ID, contradiction.ID)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	result := KnowledgeContradictionMutationResult{Detail: detail, AuthorityCheck: &check, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, commandName, requestHash, result, now); err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeContradictionMutationResult{}, storageFailure("commit contradiction decision", err)
	}
	return result, nil
}

func (s *Store) commitKnowledgeContradictionDenial(ctx context.Context, tx *sql.Tx, queries *dbgen.Queries,
	contradiction domain.KnowledgeContradiction, action string, command DecideKnowledgeContradictionCommand,
	requestHash, key, commandName string,
) (KnowledgeContradictionMutationResult, error) {
	now := s.nowText()
	eventType := contradictionConfirmDeniedEvent
	if action == domain.KnowledgeContradictionAuthorityDismiss {
		eventType = contradictionDismissDeniedEvent
	}
	sequence, err := appendEventForActor(ctx, tx, contradiction.WorkspaceID, "knowledge_contradiction",
		contradiction.ID, contradiction.StateRevision, eventType, command.CorrelationID, now,
		command.Actor.ID, command.Actor.Type, map[string]any{"action": action, "reason": domain.KnowledgeAuthorityReasonNotOwner})
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	check, err := insertKnowledgeContradictionAuthorityCheck(ctx, queries, contradiction.WorkspaceID,
		contradiction.ID, action, command.Actor, domain.KnowledgeAuthorityDenied,
		domain.KnowledgeAuthorityReasonNotOwner, command.Note, command.IdempotencyKey, requestHash, sequence, now)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	detail, err := s.knowledgeContradictionDetailInTransaction(ctx, tx, contradiction.WorkspaceID, contradiction.ID)
	if err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	result := KnowledgeContradictionMutationResult{Detail: detail, AuthorityCheck: &check, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, commandName, requestHash, result, now); err != nil {
		return KnowledgeContradictionMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeContradictionMutationResult{}, storageFailure("commit contradiction authority denial", err)
	}
	return result, &Error{Code: CodeContradictionDenied, Message: "actor is not authorized to govern knowledge contradictions"}
}

func (s *Store) KnowledgeContradictionDetail(ctx context.Context, workspaceIdentifier, contradictionID string) (domain.KnowledgeContradictionDetail, error) {
	workspaceIdentifier, contradictionID = strings.TrimSpace(workspaceIdentifier), strings.TrimSpace(contradictionID)
	if workspaceIdentifier == "" || !validKnowledgeContradictionID(contradictionID) {
		return domain.KnowledgeContradictionDetail{}, knowledgeInvalid("contradiction detail requires workspace and contradiction ID")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.KnowledgeContradictionDetail{}, storageFailure("begin contradiction detail snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return domain.KnowledgeContradictionDetail{}, err
	}
	detail, err := s.knowledgeContradictionDetailInTransaction(ctx, tx, workspace.ID, contradictionID)
	if err != nil {
		return domain.KnowledgeContradictionDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.KnowledgeContradictionDetail{}, storageFailure("commit contradiction detail snapshot", err)
	}
	return detail, nil
}

func (s *Store) ListKnowledgeContradictionDetails(ctx context.Context, query ListKnowledgeContradictionsQuery) ([]domain.KnowledgeContradictionDetail, error) {
	normalizeListKnowledgeContradictionsQuery(&query)
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.WorkspaceIdentifier == "" || query.ProjectIdentifier == "" || query.Limit < 1 || query.Limit > 200 ||
		(query.Status != "" && !domain.ValidKnowledgeContradictionStatus(query.Status)) ||
		(query.RevisionID != "" && !validContextKnowledgeRevisionID(query.RevisionID)) {
		return nil, knowledgeInvalid("contradiction list requires workspace, valid optional filters, and limit from 1 to 200")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageFailure("begin contradiction list snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, query.WorkspaceIdentifier)
	if err != nil {
		return nil, err
	}
	project, projectErr := projectInTransaction(ctx, tx, workspace.ID, query.ProjectIdentifier)
	if projectErr != nil {
		return nil, projectErr
	}
	projectID := project.ID
	if query.RevisionID != "" {
		revision, revisionErr := s.KnowledgeRevisionInTransaction(ctx, tx, workspace.ID, query.RevisionID)
		if revisionErr != nil {
			return nil, revisionErr
		}
		if revision.ProjectID != projectID {
			return nil, knowledgeInvalid("contradiction list revision must belong to the selected project")
		}
	}
	ids, err := dbgen.New(tx).ListKnowledgeContradictionIDs(ctx, dbgen.ListKnowledgeContradictionIDsParams{
		WorkspaceID: workspace.ID, ProjectID: projectID, Status: query.Status,
		RevisionID: query.RevisionID, ResultLimit: int64(query.Limit),
	})
	if err != nil {
		return nil, storageFailure("list contradictions", err)
	}
	result := make([]domain.KnowledgeContradictionDetail, 0, len(ids))
	for _, id := range ids {
		detail, detailErr := s.knowledgeContradictionDetailInTransaction(ctx, tx, workspace.ID, id)
		if detailErr != nil {
			return nil, detailErr
		}
		result = append(result, detail)
	}
	if err := tx.Commit(); err != nil {
		return nil, storageFailure("commit contradiction list snapshot", err)
	}
	return result, nil
}

func (s *Store) ListKnowledgeContradictions(ctx context.Context, query ListKnowledgeContradictionsQuery) ([]domain.KnowledgeContradiction, error) {
	details, err := s.ListKnowledgeContradictionDetails(ctx, query)
	if err != nil {
		return nil, err
	}
	result := make([]domain.KnowledgeContradiction, 0, len(details))
	for _, detail := range details {
		result = append(result, detail.Contradiction)
	}
	return result, nil
}

func (s *Store) KnowledgeRevisionDispute(ctx context.Context, workspaceIdentifier, revisionID string) (domain.KnowledgeRevisionDispute, error) {
	workspaceIdentifier, revisionID = strings.TrimSpace(workspaceIdentifier), strings.TrimSpace(revisionID)
	if workspaceIdentifier == "" || !validContextKnowledgeRevisionID(revisionID) {
		return domain.KnowledgeRevisionDispute{}, knowledgeInvalid("knowledge dispute lookup requires workspace and exact revision ID")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.KnowledgeRevisionDispute{}, storageFailure("begin knowledge dispute snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return domain.KnowledgeRevisionDispute{}, err
	}
	if _, err := s.KnowledgeRevisionInTransaction(ctx, tx, workspace.ID, revisionID); err != nil {
		return domain.KnowledgeRevisionDispute{}, err
	}
	dispute, err := s.KnowledgeRevisionDisputeInTransaction(ctx, tx, workspace.ID, revisionID)
	if err != nil {
		return domain.KnowledgeRevisionDispute{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.KnowledgeRevisionDispute{}, storageFailure("commit knowledge dispute snapshot", err)
	}
	return dispute, nil
}

// KnowledgeRevisionDisputeInTransaction derives effective dispute state from
// open contradiction records. No second currency axis is written to knowledge.
func (s *Store) KnowledgeRevisionDisputeInTransaction(ctx context.Context, tx *sql.Tx, workspaceID, revisionID string) (domain.KnowledgeRevisionDispute, error) {
	queries := dbgen.New(tx)
	rows, err := queries.ListOpenKnowledgeContradictionsForRevision(ctx, dbgen.ListOpenKnowledgeContradictionsForRevisionParams{
		WorkspaceID: workspaceID, RevisionID: revisionID,
	})
	if err != nil {
		return domain.KnowledgeRevisionDispute{}, storageFailure("list open knowledge contradictions", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	count, err := queries.CountOpenKnowledgeContradictionsForRevision(ctx, dbgen.CountOpenKnowledgeContradictionsForRevisionParams{
		WorkspaceID: workspaceID, RevisionID: revisionID,
	})
	if err != nil {
		return domain.KnowledgeRevisionDispute{}, storageFailure("count open knowledge contradictions", err)
	}
	return domain.KnowledgeRevisionDispute{RevisionID: revisionID, Disputed: count != 0, OpenContradictionCount: count, OpenContradictionIDs: ids}, nil
}

// AssertKnowledgeRevisionsUndisputedInTransaction is the canonical hard gate
// for exact context authority. It reports every open conflict in stable ID order.
func (s *Store) AssertKnowledgeRevisionsUndisputedInTransaction(ctx context.Context, tx *sql.Tx, workspaceID string, revisionIDs []string) error {
	if len(revisionIDs) == 0 {
		return nil
	}
	if len(revisionIDs) > maximumContextKnowledgeItems {
		return storageFailure("check context knowledge contradictions", errors.New("eligible revision set exceeds context knowledge bound"))
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(revisionIDs)), ",")
	arguments := make([]any, 0, 1+len(revisionIDs)*2)
	arguments = append(arguments, workspaceID)
	for _, id := range revisionIDs {
		arguments = append(arguments, id)
	}
	for _, id := range revisionIDs {
		arguments = append(arguments, id)
	}
	predicate := fmt.Sprintf("workspace_id=? AND status='open' AND (left_revision_id IN (%s) OR right_revision_id IN (%s))", placeholders, placeholders)
	var count int64
	if err := tx.QueryRowContext(ctx, "SELECT CAST(COUNT(DISTINCT id) AS INTEGER) FROM knowledge_contradictions WHERE "+predicate, arguments...).Scan(&count); err != nil {
		return storageFailure("count context knowledge contradictions", err)
	}
	if count == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT id FROM knowledge_contradictions WHERE "+predicate+" ORDER BY id LIMIT 16", arguments...)
	if err != nil {
		return storageFailure("list context knowledge contradictions", err)
	}
	defer rows.Close()
	ids := make([]string, 0, 16)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return storageFailure("scan context knowledge contradiction", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return storageFailure("iterate context knowledge contradictions", err)
	}
	suffix := ""
	if count > int64(len(ids)) {
		suffix = fmt.Sprintf(" (+%d more)", count-int64(len(ids)))
	}
	return knowledgeConflict("requested knowledge is disputed by open contradictions " + strings.Join(ids, ", ") + suffix)
}

func (s *Store) knowledgeContradictionDetailInTransaction(ctx context.Context, tx *sql.Tx, workspaceID, contradictionID string) (domain.KnowledgeContradictionDetail, error) {
	queries := dbgen.New(tx)
	row, err := queries.GetKnowledgeContradiction(ctx, dbgen.GetKnowledgeContradictionParams{WorkspaceID: workspaceID, ID: contradictionID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.KnowledgeContradictionDetail{}, contradictionNotFound(contradictionID)
	}
	if err != nil {
		return domain.KnowledgeContradictionDetail{}, storageFailure("read contradiction", err)
	}
	contradiction := knowledgeContradictionFromRow(row)
	left, err := knowledgeRevision(ctx, queries, workspaceID, contradiction.LeftRevisionID)
	if err != nil {
		return domain.KnowledgeContradictionDetail{}, err
	}
	right, err := knowledgeRevision(ctx, queries, workspaceID, contradiction.RightRevisionID)
	if err != nil {
		return domain.KnowledgeContradictionDetail{}, err
	}
	checkRows, err := queries.ListKnowledgeContradictionAuthorityChecks(ctx, dbgen.ListKnowledgeContradictionAuthorityChecksParams{
		WorkspaceID: workspaceID, ContradictionID: contradictionID,
	})
	if err != nil {
		return domain.KnowledgeContradictionDetail{}, storageFailure("list contradiction authority checks", err)
	}
	checks := make([]domain.KnowledgeContradictionAuthorityCheck, 0, len(checkRows))
	for _, check := range checkRows {
		checks = append(checks, knowledgeContradictionAuthorityCheckFromRow(check))
	}
	checkCount, err := queries.CountKnowledgeContradictionAuthorityChecks(ctx, dbgen.CountKnowledgeContradictionAuthorityChecksParams{
		WorkspaceID: workspaceID, ContradictionID: contradictionID,
	})
	if err != nil {
		return domain.KnowledgeContradictionDetail{}, storageFailure("count contradiction authority checks", err)
	}
	return domain.KnowledgeContradictionDetail{Contradiction: contradiction, LeftRevision: left, RightRevision: right,
		AuthorityCheckCount: checkCount, AuthorityChecks: checks}, nil
}

func (s *Store) validateKnowledgeContradictionPairInTransaction(ctx context.Context, tx *sql.Tx, workspaceID, leftID, rightID string) (domain.KnowledgeRevision, domain.KnowledgeRevision, error) {
	left, err := s.KnowledgeRevisionInTransaction(ctx, tx, workspaceID, leftID)
	if err != nil {
		return domain.KnowledgeRevision{}, domain.KnowledgeRevision{}, err
	}
	right, err := s.KnowledgeRevisionInTransaction(ctx, tx, workspaceID, rightID)
	if err != nil {
		return domain.KnowledgeRevision{}, domain.KnowledgeRevision{}, err
	}
	if left.ProjectID != right.ProjectID {
		return domain.KnowledgeRevision{}, domain.KnowledgeRevision{}, knowledgeConflict("contradiction participants must belong to the same project")
	}
	if left.ItemID == right.ItemID {
		return domain.KnowledgeRevision{}, domain.KnowledgeRevision{}, knowledgeConflict("contradiction participants must be revisions of different knowledge items")
	}
	if left.ReviewStatus != domain.KnowledgeReviewAccepted || left.CurrencyStatus != domain.KnowledgeCurrencyCurrent ||
		right.ReviewStatus != domain.KnowledgeReviewAccepted || right.CurrencyStatus != domain.KnowledgeCurrencyCurrent {
		return domain.KnowledgeRevision{}, domain.KnowledgeRevision{}, knowledgeConflict("contradiction participants must both be accepted and current")
	}
	if left.TaskScopeID != "" && right.TaskScopeID != "" && left.TaskScopeID != right.TaskScopeID {
		return domain.KnowledgeRevision{}, domain.KnowledgeRevision{}, knowledgeConflict("contradiction participant applicability scopes do not intersect")
	}
	return left, right, nil
}

func (s *Store) resolveOpenKnowledgeContradictionsInTransaction(ctx context.Context, tx *sql.Tx,
	workspaceID, revisionID, currencyStatus string, causeEventSequence int64,
	actor domain.KnowledgeActor, correlationID, now string,
) (int64, error) {
	reason, statusWord := "", ""
	switch currencyStatus {
	case domain.KnowledgeCurrencyStale:
		reason, statusWord = ContradictionResolutionParticipantStale, "stale"
	case domain.KnowledgeCurrencySuperseded:
		reason, statusWord = ContradictionResolutionParticipantSuperseded, "superseded"
	default:
		return 0, storageFailure("resolve knowledge contradictions", fmt.Errorf("unsupported terminal currency %q", currencyStatus))
	}
	queries := dbgen.New(tx)
	rows, err := queries.ListAllOpenKnowledgeContradictionsForRevision(ctx, dbgen.ListAllOpenKnowledgeContradictionsForRevisionParams{
		WorkspaceID: workspaceID, RevisionID: revisionID,
	})
	if err != nil {
		return 0, storageFailure("list contradictions to resolve", err)
	}
	lastSequence := int64(0)
	for _, row := range rows {
		left, leftErr := s.KnowledgeRevisionInTransaction(ctx, tx, workspaceID, row.LeftRevisionID)
		if leftErr != nil {
			return 0, leftErr
		}
		right, rightErr := s.KnowledgeRevisionInTransaction(ctx, tx, workspaceID, row.RightRevisionID)
		if rightErr != nil {
			return 0, rightErr
		}
		note := fmt.Sprintf("knowledge revision %s became %s", revisionID, statusWord)
		eventData := contradictionLifecycleEventData(row.ProjectID, row.LeftRevisionID, row.RightRevisionID,
			left.TaskScopeID, right.TaskScopeID, domain.KnowledgeContradictionResolved, row.StateRevision+1)
		eventData["participant_revision_id"] = revisionID
		eventData["resolution_reason"] = reason
		eventData["cause_event_sequence"] = causeEventSequence
		sequence, eventErr := appendEventForActor(ctx, tx, workspaceID, "knowledge_contradiction", row.ID,
			row.StateRevision+1, contradictionResolvedEvent, correlationID, now, actor.ID, actor.Type,
			eventData)
		if eventErr != nil {
			return 0, eventErr
		}
		updated, updateErr := queries.ResolveKnowledgeContradiction(ctx, dbgen.ResolveKnowledgeContradictionParams{
			ResolutionReason: &reason, ResolvedAt: &now, ResolvedBy: &actor.ID, ResolvedByType: &actor.Type,
			ResolutionNote: &note, ResolutionEventSequence: &sequence,
			ResolutionCauseEventSequence: &causeEventSequence, ID: row.ID, ExpectedStateRevision: row.StateRevision,
		})
		if updateErr != nil {
			return 0, storageFailure("resolve knowledge contradiction", updateErr)
		}
		if updated != 1 {
			return 0, &Error{Code: CodeRevisionConflict, Message: fmt.Sprintf("contradiction %s changed before automatic resolution", row.ID)}
		}
		lastSequence = sequence
	}
	return lastSequence, nil
}

func insertKnowledgeContradictionAuthorityCheck(ctx context.Context, queries *dbgen.Queries,
	workspaceID, contradictionID, action string, actor domain.KnowledgeActor, outcome, reason, note,
	idempotencyKey, requestHash string, eventSequence int64, now string,
) (domain.KnowledgeContradictionAuthorityCheck, error) {
	id, err := randomID("kcauth_")
	if err != nil {
		return domain.KnowledgeContradictionAuthorityCheck{}, storageFailure("generate contradiction authority check id", err)
	}
	if err := queries.InsertKnowledgeContradictionAuthorityCheck(ctx, dbgen.InsertKnowledgeContradictionAuthorityCheckParams{
		ID: id, WorkspaceID: workspaceID, ContradictionID: contradictionID, Action: action,
		ActorID: actor.ID, ActorType: actor.Type, Outcome: outcome, Reason: reason,
		Note: optionalStringPointer(note), IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		EventSequence: eventSequence, CreatedAt: now,
	}); err != nil {
		return domain.KnowledgeContradictionAuthorityCheck{}, storageFailure("insert contradiction authority check", err)
	}
	return domain.KnowledgeContradictionAuthorityCheck{
		ID: id, WorkspaceID: workspaceID, ContradictionID: contradictionID, Action: action,
		Actor: actor, Outcome: outcome, Reason: reason, Note: note, IdempotencyKey: idempotencyKey,
		RequestHash: requestHash, EventSequence: eventSequence, CreatedAt: now,
	}, nil
}

func knowledgeContradictionFromRow(row dbgen.KnowledgeContradiction) domain.KnowledgeContradiction {
	return domain.KnowledgeContradiction{
		ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID,
		LeftRevisionID: row.LeftRevisionID, RightRevisionID: row.RightRevisionID,
		Status: row.Status, StateRevision: row.StateRevision, ReportNote: row.ReportNote,
		ReportedAt: row.ReportedAt, ReportedBy: row.ReportedBy, ReportedByType: row.ReportedByType,
		ConfirmedAt: stringValue(row.ConfirmedAt), ConfirmedBy: stringValue(row.ConfirmedBy),
		ConfirmedByType: stringValue(row.ConfirmedByType), ConfirmNote: stringValue(row.ConfirmNote),
		ConfirmEventSequence: optionalInt64Value(row.ConfirmEventSequence),
		DismissedAt:          stringValue(row.DismissedAt), DismissedBy: stringValue(row.DismissedBy),
		DismissedByType: stringValue(row.DismissedByType), DismissNote: stringValue(row.DismissNote),
		DismissEventSequence: optionalInt64Value(row.DismissEventSequence),
		ResolutionReason:     stringValue(row.ResolutionReason), ResolvedAt: stringValue(row.ResolvedAt),
		ResolvedBy: stringValue(row.ResolvedBy), ResolvedByType: stringValue(row.ResolvedByType),
		ResolutionNote: stringValue(row.ResolutionNote), ResolutionEventSequence: optionalInt64Value(row.ResolutionEventSequence),
		ResolutionCauseEventSequence: optionalInt64Value(row.ResolutionCauseEventSequence),
	}
}

func optionalInt64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func knowledgeContradictionAuthorityCheckFromRow(row dbgen.KnowledgeContradictionAuthorityCheck) domain.KnowledgeContradictionAuthorityCheck {
	return domain.KnowledgeContradictionAuthorityCheck{
		ID: row.ID, WorkspaceID: row.WorkspaceID, ContradictionID: row.ContradictionID,
		Action: row.Action, Actor: domain.KnowledgeActor{ID: row.ActorID, Type: row.ActorType},
		Outcome: row.Outcome, Reason: row.Reason, Note: stringValue(row.Note),
		IdempotencyKey: row.IdempotencyKey, RequestHash: row.RequestHash,
		EventSequence: row.EventSequence, CreatedAt: row.CreatedAt,
	}
}

func normalizeReportKnowledgeContradictionCommand(command *ReportKnowledgeContradictionCommand) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.LeftRevisionID = strings.TrimSpace(command.LeftRevisionID)
	command.RightRevisionID = strings.TrimSpace(command.RightRevisionID)
	command.ReportNote = strings.TrimSpace(command.ReportNote)
	command.Actor.ID = strings.TrimSpace(command.Actor.ID)
	command.Actor.Type = strings.TrimSpace(command.Actor.Type)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
}

func normalizeDecideKnowledgeContradictionCommand(command *DecideKnowledgeContradictionCommand) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ContradictionID = strings.TrimSpace(command.ContradictionID)
	command.Note = strings.TrimSpace(command.Note)
	command.Actor.ID = strings.TrimSpace(command.Actor.ID)
	command.Actor.Type = strings.TrimSpace(command.Actor.Type)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
}

func normalizeListKnowledgeContradictionsQuery(query *ListKnowledgeContradictionsQuery) {
	query.WorkspaceIdentifier = strings.TrimSpace(query.WorkspaceIdentifier)
	query.ProjectIdentifier = strings.TrimSpace(query.ProjectIdentifier)
	query.Status = strings.TrimSpace(query.Status)
	query.RevisionID = strings.TrimSpace(query.RevisionID)
}

func canonicalContradictionPair(left, right string) (string, string) {
	if left < right {
		return left, right
	}
	return right, left
}

func contradictionLifecycleEventData(projectID, leftID, rightID, leftTaskScopeID, rightTaskScopeID,
	status string, stateRevision int64,
) map[string]any {
	return map[string]any{
		"project_id": projectID, "left_revision_id": leftID, "right_revision_id": rightID,
		"left_task_scope_id": leftTaskScopeID, "right_task_scope_id": rightTaskScopeID,
		"status": status, "state_revision": stateRevision,
	}
}

func contradictionDecisionEventData(contradiction domain.KnowledgeContradiction, leftTaskScopeID,
	rightTaskScopeID, action, note string,
) map[string]any {
	status := domain.KnowledgeContradictionOpen
	if action == domain.KnowledgeContradictionAuthorityDismiss {
		status = domain.KnowledgeContradictionDismissed
	}
	data := contradictionLifecycleEventData(contradiction.ProjectID, contradiction.LeftRevisionID,
		contradiction.RightRevisionID, leftTaskScopeID, rightTaskScopeID, status, contradiction.StateRevision+1)
	data["note"] = note
	return data
}

func validKnowledgeContradictionID(value string) bool {
	if len(value) != len("kcon_")+32 || !strings.HasPrefix(value, "kcon_") {
		return false
	}
	for _, character := range value[len("kcon_"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func contradictionIdempotencyKey(actor domain.KnowledgeActor, key string) string {
	digest := sha256.Sum256([]byte(actor.Type + "\n" + actor.ID + "\n" + key))
	return "contradiction:" + hex.EncodeToString(digest[:])
}

func contradictionNotFound(id string) error {
	return &Error{Code: CodeContradictionNotFound, Message: fmt.Sprintf("knowledge contradiction %q was not found", id)}
}

func contradictionConflict(message string) error {
	return &Error{Code: CodeContradictionConflict, Message: message}
}
