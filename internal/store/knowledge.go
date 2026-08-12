package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	knowledgeProposedEvent         = "knowledge.proposed"
	knowledgeAcceptedEvent         = "knowledge.accepted"
	knowledgeRejectedEvent         = "knowledge.rejected"
	knowledgeStaleEvent            = "knowledge.marked_stale"
	knowledgeSupersededEvent       = "knowledge.superseded"
	knowledgeAcceptanceDeniedEvent = "knowledge.acceptance_denied"
	knowledgeRejectionDeniedEvent  = "knowledge.rejection_denied"
	knowledgeStaleDeniedEvent      = "knowledge.stale_denied"
	maximumKnowledgeTitleBytes     = 160
	maximumKnowledgeBodyBytes      = 16 * 1024
	maximumKnowledgeDecisionBytes  = 1024
	maximumKnowledgeSources        = 16
)

// OwnerKnowledgeActor is the trusted local owner's canonical identity. Local API
// handlers may inject it; request payloads must not make Actor caller-selectable.
func OwnerKnowledgeActor() domain.KnowledgeActor {
	return domain.KnowledgeActor{ID: localOwnerActorID, Type: localActorType}
}

func (s *Store) ProposeKnowledge(ctx context.Context, command ProposeKnowledgeCommand) (KnowledgeMutationResult, error) {
	normalizeProposeKnowledgeCommand(&command)
	if err := validateKnowledgeProposal(command); err != nil {
		return KnowledgeMutationResult{}, err
	}
	requestHash, err := hashCommand("knowledge.propose", map[string]any{
		"workspace": command.WorkspaceIdentifier, "project": command.ProjectIdentifier,
		"task_scope_id": command.TaskScopeID, "type": command.Type, "title": command.Title,
		"body": command.Body, "confidence": command.Confidence,
		"verification_status": command.VerificationStatus, "freshness_policy": command.FreshnessPolicy,
		"fresh_until": command.FreshUntil, "sources": command.Sources,
		"supersedes_revision_id": command.SupersedesRevisionID, "actor": command.Actor,
	})
	if err != nil {
		return KnowledgeMutationResult{}, storageFailure("hash knowledge proposal", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KnowledgeMutationResult{}, storageFailure("begin knowledge proposal", err)
	}
	defer tx.Rollback()
	idempotencyKey := knowledgeIdempotencyKey(command.Actor, command.IdempotencyKey)
	var replay KnowledgeMutationResult
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "knowledge.propose", requestHash, &replay); err != nil {
		return KnowledgeMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return KnowledgeMutationResult{}, err
	}
	queries := dbgen.New(tx)
	if err := validateKnowledgeActorWorkspace(ctx, queries, command.Actor, workspace.ID); err != nil {
		return KnowledgeMutationResult{}, err
	}
	sources, sourceProjectID, err := resolveKnowledgeSources(ctx, queries, workspace.ID, command.Sources)
	if err != nil {
		return KnowledgeMutationResult{}, err
	}
	projectID := sourceProjectID
	if command.ProjectIdentifier != "" {
		project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
		if err != nil {
			return KnowledgeMutationResult{}, err
		}
		if project.ID != sourceProjectID {
			return KnowledgeMutationResult{}, knowledgeInvalid("explicit project does not match the primary knowledge source")
		}
		projectID = project.ID
	}
	if command.TaskScopeID != "" {
		task, err := queryTask(ctx, tx, workspace.ID, command.TaskScopeID)
		if err != nil {
			return KnowledgeMutationResult{}, err
		}
		if task.ProjectID != projectID {
			return KnowledgeMutationResult{}, knowledgeInvalid("task applicability scope must belong to the knowledge project")
		}
		command.TaskScopeID = task.ID
	}
	itemID, revisionNumber := "", int64(1)
	var predecessor domain.KnowledgeRevision
	if command.SupersedesRevisionID != "" {
		predecessor, err = s.KnowledgeRevisionInTransaction(ctx, tx, workspace.ID, command.SupersedesRevisionID)
		if err != nil {
			return KnowledgeMutationResult{}, err
		}
		if predecessor.ProjectID != projectID || predecessor.TaskScopeID != command.TaskScopeID || predecessor.Type != command.Type {
			return KnowledgeMutationResult{}, knowledgeInvalid("a successor must retain its item's type and applicability scope")
		}
		if predecessor.ReviewStatus != domain.KnowledgeReviewAccepted || predecessor.CurrencyStatus != domain.KnowledgeCurrencyCurrent {
			return KnowledgeMutationResult{}, knowledgeConflict("only a current accepted revision can be superseded")
		}
		if successor, findErr := queries.FindLiveKnowledgeSuccessor(ctx, &predecessor.ID); findErr == nil {
			return KnowledgeMutationResult{}, knowledgeConflict(fmt.Sprintf("revision already has live successor %s", successor))
		} else if !errors.Is(findErr, sql.ErrNoRows) {
			return KnowledgeMutationResult{}, storageFailure("check knowledge successor", findErr)
		}
		itemID = predecessor.ItemID
		revisionNumber, err = queries.MaxKnowledgeRevisionNumber(ctx, itemID)
		if err != nil {
			return KnowledgeMutationResult{}, storageFailure("read knowledge revision number", err)
		}
		revisionNumber++
	} else {
		itemID, err = randomID("know_")
		if err != nil {
			return KnowledgeMutationResult{}, storageFailure("generate knowledge item id", err)
		}
	}
	revisionID, err := randomID("krev_")
	if err != nil {
		return KnowledgeMutationResult{}, storageFailure("generate knowledge revision id", err)
	}
	now := s.nowText()
	if command.FreshnessPolicy == domain.KnowledgeFreshExpiresAt {
		deadline, _ := time.Parse(time.RFC3339Nano, command.FreshUntil)
		proposalTime, _ := time.Parse(time.RFC3339Nano, now)
		if !deadline.After(proposalTime) {
			return KnowledgeMutationResult{}, knowledgeInvalid("fresh_until must be later than the proposal time")
		}
	}
	if command.SupersedesRevisionID == "" {
		if err := queries.InsertKnowledgeItem(ctx, dbgen.InsertKnowledgeItemParams{
			ID: itemID, WorkspaceID: workspace.ID, ProjectID: projectID,
			TaskScopeID: optionalStringPointer(command.TaskScopeID), Type: command.Type,
			CreatedAt: now, CreatedBy: command.Actor.ID, CreatedByType: command.Actor.Type,
		}); err != nil {
			return KnowledgeMutationResult{}, storageFailure("insert knowledge item", err)
		}
	}
	contentHash := knowledgeContentHash(command.Title, command.Body)
	if err := queries.InsertKnowledgeRevision(ctx, dbgen.InsertKnowledgeRevisionParams{
		ID: revisionID, ItemID: itemID, RevisionNumber: revisionNumber,
		Title: command.Title, Body: command.Body, ContentHash: contentHash,
		Confidence: command.Confidence, VerificationStatus: command.VerificationStatus,
		FreshnessPolicy: command.FreshnessPolicy, FreshUntil: optionalStringPointer(command.FreshUntil),
		SupersedesRevisionID: optionalStringPointer(command.SupersedesRevisionID), ProposedAt: now,
		ProposedBy: command.Actor.ID, ProposedByType: command.Actor.Type,
	}); err != nil {
		return KnowledgeMutationResult{}, storageFailure("insert knowledge revision", err)
	}
	for index, source := range sources {
		if err := queries.InsertKnowledgeSource(ctx, dbgen.InsertKnowledgeSourceParams{
			RevisionID: revisionID, Ordinal: int64(index), SourceType: source.Type,
			SourceID: source.ID, SourceRevision: source.Revision, Role: source.Role,
		}); err != nil {
			return KnowledgeMutationResult{}, storageFailure("insert knowledge source", err)
		}
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return KnowledgeMutationResult{}, err
	}
	sequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_revision", revisionID, 1,
		knowledgeProposedEvent, command.CorrelationID, now, command.Actor.ID, command.Actor.Type,
		map[string]any{"item_id": itemID, "project_id": projectID, "task_scope_id": command.TaskScopeID,
			"type": command.Type, "supersedes_revision_id": command.SupersedesRevisionID, "source_count": len(sources)})
	if err != nil {
		return KnowledgeMutationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return KnowledgeMutationResult{}, err
	}
	revision, err := s.KnowledgeRevisionInTransaction(ctx, tx, workspace.ID, revisionID)
	if err != nil {
		return KnowledgeMutationResult{}, err
	}
	result := KnowledgeMutationResult{Revision: revision, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, idempotencyKey, "knowledge.propose", requestHash, result, now); err != nil {
		return KnowledgeMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeMutationResult{}, storageFailure("commit knowledge proposal", err)
	}
	return result, nil
}

func (s *Store) AcceptKnowledge(ctx context.Context, command AcceptKnowledgeCommand) (KnowledgeMutationResult, error) {
	return s.decideKnowledge(ctx, domain.KnowledgeAuthorityAccept, command.WorkspaceIdentifier,
		command.RevisionID, command.ExpectedStateRevision, command.DecisionNote, command.Actor,
		command.IdempotencyKey, command.CorrelationID)
}

func (s *Store) RejectKnowledge(ctx context.Context, command RejectKnowledgeCommand) (KnowledgeMutationResult, error) {
	return s.decideKnowledge(ctx, domain.KnowledgeAuthorityReject, command.WorkspaceIdentifier,
		command.RevisionID, command.ExpectedStateRevision, command.DecisionNote, command.Actor,
		command.IdempotencyKey, command.CorrelationID)
}

func (s *Store) MarkKnowledgeStale(ctx context.Context, command MarkKnowledgeStaleCommand) (KnowledgeMutationResult, error) {
	return s.decideKnowledge(ctx, domain.KnowledgeAuthorityMarkStale, command.WorkspaceIdentifier,
		command.RevisionID, command.ExpectedStateRevision, command.Reason, command.Actor,
		command.IdempotencyKey, command.CorrelationID)
}

func (s *Store) decideKnowledge(ctx context.Context, action, workspaceIdentifier, revisionID string,
	expectedStateRevision int64, note string, actor domain.KnowledgeActor, idempotencyKey, correlationID string,
) (KnowledgeMutationResult, error) {
	workspaceIdentifier, revisionID = strings.TrimSpace(workspaceIdentifier), strings.TrimSpace(revisionID)
	note, idempotencyKey, correlationID = strings.TrimSpace(note), strings.TrimSpace(idempotencyKey), strings.TrimSpace(correlationID)
	actor.ID, actor.Type = strings.TrimSpace(actor.ID), strings.TrimSpace(actor.Type)
	if workspaceIdentifier == "" || revisionID == "" || expectedStateRevision < 1 || !validKnowledgeActor(actor) || !validKnowledgeNote(note, action == domain.KnowledgeAuthorityMarkStale) {
		return KnowledgeMutationResult{}, knowledgeInvalid("knowledge decision requires workspace, revision, expected state revision, trusted actor, and bounded note")
	}
	if err := validateMutationMetadata(idempotencyKey, correlationID, CodeInvalidKnowledge); err != nil {
		return KnowledgeMutationResult{}, err
	}
	commandName := "knowledge." + action
	requestHash, err := hashCommand(commandName, map[string]any{
		"workspace": workspaceIdentifier, "revision_id": revisionID,
		"expected_state_revision": expectedStateRevision, "note": note, "actor": actor,
	})
	if err != nil {
		return KnowledgeMutationResult{}, storageFailure("hash knowledge decision", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KnowledgeMutationResult{}, storageFailure("begin knowledge decision", err)
	}
	defer tx.Rollback()
	queries := dbgen.New(tx)
	checkKey := knowledgeIdempotencyKey(actor, idempotencyKey)
	var replay KnowledgeMutationResult
	if found, err := lookupIdempotency(ctx, tx, checkKey, commandName, requestHash, &replay); err != nil {
		return KnowledgeMutationResult{}, err
	} else if found {
		if replay.AuthorityCheck != nil && replay.AuthorityCheck.Outcome == domain.KnowledgeAuthorityDenied {
			return replay, &Error{Code: CodeKnowledgeDenied, Message: "actor is not authorized to govern canonical knowledge"}
		}
		return replay, nil
	}
	if prior, replayErr := queries.GetKnowledgeAuthorityCheckByKey(ctx, dbgen.GetKnowledgeAuthorityCheckByKeyParams{
		ActorType: actor.Type, ActorID: actor.ID, Action: action, IdempotencyKey: idempotencyKey,
	}); replayErr == nil {
		check := knowledgeAuthorityCheckFromRow(prior)
		if check.RequestHash != requestHash {
			return KnowledgeMutationResult{}, &Error{Code: CodeIdempotencyConflict, Message: "idempotency key was already used for a different knowledge decision"}
		}
		revision, detailErr := s.KnowledgeRevisionInTransaction(ctx, tx, check.WorkspaceID, check.RevisionID)
		if detailErr != nil {
			return KnowledgeMutationResult{}, detailErr
		}
		result := KnowledgeMutationResult{Revision: revision, AuthorityCheck: &check, EventSequence: check.EventSequence}
		if check.Outcome == domain.KnowledgeAuthorityDenied {
			return result, &Error{Code: CodeKnowledgeDenied, Message: "actor is not authorized to govern canonical knowledge"}
		}
		return result, nil
	} else if !errors.Is(replayErr, sql.ErrNoRows) {
		return KnowledgeMutationResult{}, storageFailure("read knowledge authority replay", replayErr)
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return KnowledgeMutationResult{}, err
	}
	revision, err := s.KnowledgeRevisionInTransaction(ctx, tx, workspace.ID, revisionID)
	if err != nil {
		return KnowledgeMutationResult{}, err
	}
	if revision.StateRevision != expectedStateRevision {
		return KnowledgeMutationResult{}, &Error{Code: CodeRevisionConflict, Message: fmt.Sprintf("knowledge state revision is %d, expected %d", revision.StateRevision, expectedStateRevision)}
	}
	if err := validateKnowledgeActorWorkspace(ctx, queries, actor, workspace.ID); err != nil {
		return KnowledgeMutationResult{}, err
	}
	if !knowledgeActorIsOwner(actor) {
		return s.commitKnowledgeDenial(ctx, tx, queries, workspace.ID, revision, action, note,
			actor, idempotencyKey, correlationID, requestHash)
	}
	now := s.nowText()
	var rows int64
	switch action {
	case domain.KnowledgeAuthorityAccept:
		if revision.ReviewStatus != domain.KnowledgeReviewProposed || revision.CurrencyStatus != domain.KnowledgeCurrencyPending {
			return KnowledgeMutationResult{}, knowledgeConflict("only a pending proposal can be accepted")
		}
		if revision.SupersedesRevisionID != "" {
			predecessor, predecessorErr := s.KnowledgeRevisionInTransaction(ctx, tx, workspace.ID, revision.SupersedesRevisionID)
			if predecessorErr != nil {
				return KnowledgeMutationResult{}, predecessorErr
			}
			if predecessor.ItemID != revision.ItemID || predecessor.ReviewStatus != domain.KnowledgeReviewAccepted || predecessor.CurrencyStatus != domain.KnowledgeCurrencyCurrent {
				return KnowledgeMutationResult{}, knowledgeConflict("successor acceptance requires its current accepted predecessor")
			}
			rows, err = queries.SupersedeKnowledgeRevision(ctx, dbgen.SupersedeKnowledgeRevisionParams{ID: predecessor.ID, ExpectedStateRevision: predecessor.StateRevision})
			if err != nil || rows != 1 {
				if err != nil {
					return KnowledgeMutationResult{}, storageFailure("supersede knowledge predecessor", err)
				}
				return KnowledgeMutationResult{}, knowledgeConflict("knowledge predecessor changed before successor acceptance")
			}
			supersededSequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_revision", predecessor.ID, predecessor.StateRevision+1,
				knowledgeSupersededEvent, correlationID, now, actor.ID, actor.Type,
				map[string]any{"successor_revision_id": revision.ID, "item_id": revision.ItemID})
			if err != nil {
				return KnowledgeMutationResult{}, err
			}
			if _, err := insertKnowledgeAuthorityCheck(ctx, queries, workspace.ID, predecessor.ID,
				domain.KnowledgeAuthoritySupersede, actor, domain.KnowledgeAuthorityAllowed,
				domain.KnowledgeAuthorityReasonOwner, note, idempotencyKey, requestHash, supersededSequence, now); err != nil {
				return KnowledgeMutationResult{}, err
			}
		}
		rows, err = queries.AcceptKnowledgeRevision(ctx, dbgen.AcceptKnowledgeRevisionParams{
			AcceptedAt: &now, AcceptedBy: &actor.ID, AcceptedByType: &actor.Type,
			DecisionNote: optionalStringPointer(note), ID: revision.ID, ExpectedStateRevision: expectedStateRevision,
		})
	case domain.KnowledgeAuthorityReject:
		if revision.ReviewStatus != domain.KnowledgeReviewProposed || revision.CurrencyStatus != domain.KnowledgeCurrencyPending {
			return KnowledgeMutationResult{}, knowledgeConflict("only a pending proposal can be rejected")
		}
		rows, err = queries.RejectKnowledgeRevision(ctx, dbgen.RejectKnowledgeRevisionParams{
			RejectedAt: &now, RejectedBy: &actor.ID, RejectedByType: &actor.Type,
			DecisionNote: optionalStringPointer(note), ID: revision.ID, ExpectedStateRevision: expectedStateRevision,
		})
	case domain.KnowledgeAuthorityMarkStale:
		if revision.ReviewStatus != domain.KnowledgeReviewAccepted || revision.CurrencyStatus != domain.KnowledgeCurrencyCurrent {
			return KnowledgeMutationResult{}, knowledgeConflict("only current accepted knowledge can be marked stale")
		}
		rows, err = queries.MarkKnowledgeRevisionStale(ctx, dbgen.MarkKnowledgeRevisionStaleParams{
			StaleAt: &now, StaleBy: &actor.ID, StaleByType: &actor.Type, StaleReason: &note,
			ID: revision.ID, ExpectedStateRevision: expectedStateRevision,
		})
	default:
		return KnowledgeMutationResult{}, knowledgeInvalid("unknown knowledge decision action")
	}
	if err != nil {
		return KnowledgeMutationResult{}, storageFailure("update knowledge state", err)
	}
	if rows != 1 {
		return KnowledgeMutationResult{}, &Error{Code: CodeRevisionConflict, Message: "knowledge state changed before the decision was applied"}
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return KnowledgeMutationResult{}, err
	}
	eventType := map[string]string{
		domain.KnowledgeAuthorityAccept:    knowledgeAcceptedEvent,
		domain.KnowledgeAuthorityReject:    knowledgeRejectedEvent,
		domain.KnowledgeAuthorityMarkStale: knowledgeStaleEvent,
	}[action]
	sequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_revision", revision.ID, revision.StateRevision+1,
		eventType, correlationID, now, actor.ID, actor.Type,
		map[string]any{"item_id": revision.ItemID, "decision_note": note, "supersedes_revision_id": revision.SupersedesRevisionID})
	if err != nil {
		return KnowledgeMutationResult{}, err
	}
	check, err := insertKnowledgeAuthorityCheck(ctx, queries, workspace.ID, revision.ID, action, actor,
		domain.KnowledgeAuthorityAllowed, domain.KnowledgeAuthorityReasonOwner, note,
		idempotencyKey, requestHash, sequence, now)
	if err != nil {
		return KnowledgeMutationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return KnowledgeMutationResult{}, err
	}
	revision, err = s.KnowledgeRevisionInTransaction(ctx, tx, workspace.ID, revision.ID)
	if err != nil {
		return KnowledgeMutationResult{}, err
	}
	result := KnowledgeMutationResult{Revision: revision, AuthorityCheck: &check, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, checkKey, commandName, requestHash, result, now); err != nil {
		return KnowledgeMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeMutationResult{}, storageFailure("commit knowledge decision", err)
	}
	return result, nil
}

func (s *Store) commitKnowledgeDenial(ctx context.Context, tx *sql.Tx, queries *dbgen.Queries,
	workspaceID string, revision domain.KnowledgeRevision, action, note string, actor domain.KnowledgeActor,
	idempotencyKey, correlationID, requestHash string,
) (KnowledgeMutationResult, error) {
	now := s.nowText()
	denialEvent := map[string]string{
		domain.KnowledgeAuthorityAccept:    knowledgeAcceptanceDeniedEvent,
		domain.KnowledgeAuthorityReject:    knowledgeRejectionDeniedEvent,
		domain.KnowledgeAuthorityMarkStale: knowledgeStaleDeniedEvent,
	}[action]
	sequence, err := appendEventForActor(ctx, tx, workspaceID, "knowledge_revision", revision.ID, revision.StateRevision,
		denialEvent, correlationID, now, actor.ID, actor.Type,
		map[string]any{"action": action, "reason": domain.KnowledgeAuthorityReasonNotOwner})
	if err != nil {
		return KnowledgeMutationResult{}, err
	}
	check, err := insertKnowledgeAuthorityCheck(ctx, queries, workspaceID, revision.ID, action, actor,
		domain.KnowledgeAuthorityDenied, domain.KnowledgeAuthorityReasonNotOwner, note,
		idempotencyKey, requestHash, sequence, now)
	if err != nil {
		return KnowledgeMutationResult{}, err
	}
	result := KnowledgeMutationResult{Revision: revision, AuthorityCheck: &check, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, knowledgeIdempotencyKey(actor, idempotencyKey), "knowledge."+action, requestHash, result, now); err != nil {
		return KnowledgeMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeMutationResult{}, storageFailure("commit knowledge authority denial", err)
	}
	return result, &Error{Code: CodeKnowledgeDenied, Message: "actor is not authorized to govern canonical knowledge"}
}

func (s *Store) KnowledgeRevision(ctx context.Context, workspaceIdentifier, revisionID string) (domain.KnowledgeRevision, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.KnowledgeRevision{}, err
	}
	return knowledgeRevision(ctx, dbgen.New(s.db), workspace.ID, strings.TrimSpace(revisionID))
}

func (s *Store) KnowledgeDetail(ctx context.Context, workspaceIdentifier, revisionID string) (domain.KnowledgeDetail, error) {
	revision, err := s.KnowledgeRevision(ctx, workspaceIdentifier, revisionID)
	if err != nil {
		return domain.KnowledgeDetail{}, err
	}
	checks, err := s.ListKnowledgeAuthorityChecks(ctx, workspaceIdentifier, revision.ID)
	if err != nil {
		return domain.KnowledgeDetail{}, err
	}
	return domain.KnowledgeDetail{Revision: revision, AuthorityChecks: checks}, nil
}

// KnowledgeRevisionInTransaction loads an exact immutable-content revision and
// its frozen sources for another store mutation (notably context packet builds).
func (s *Store) KnowledgeRevisionInTransaction(ctx context.Context, tx *sql.Tx, workspaceID, revisionID string) (domain.KnowledgeRevision, error) {
	return knowledgeRevision(ctx, dbgen.New(tx), workspaceID, strings.TrimSpace(revisionID))
}

// KnowledgeCurrentSuccessorIDInTransaction follows accepted supersession links
// to the terminal current revision. It reports metadata only; callers must not
// silently substitute the result for an explicitly requested revision.
func (s *Store) KnowledgeCurrentSuccessorIDInTransaction(ctx context.Context, tx *sql.Tx, workspaceID, revisionID string) (string, error) {
	queries := dbgen.New(tx)
	current, err := knowledgeRevision(ctx, queries, workspaceID, strings.TrimSpace(revisionID))
	if err != nil {
		return "", err
	}
	if current.CurrencyStatus != domain.KnowledgeCurrencySuperseded {
		return "", nil
	}
	for depth := 0; depth < 200; depth++ {
		successorID, err := queries.FindAcceptedKnowledgeSuccessor(ctx, dbgen.FindAcceptedKnowledgeSuccessorParams{
			RevisionID: &current.ID, WorkspaceID: workspaceID,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", storageFailure("find current knowledge successor", err)
		}
		current, err = knowledgeRevision(ctx, queries, workspaceID, successorID)
		if err != nil {
			return "", err
		}
		if current.CurrencyStatus == domain.KnowledgeCurrencyCurrent {
			return current.ID, nil
		}
		if current.CurrencyStatus != domain.KnowledgeCurrencySuperseded {
			return "", nil
		}
	}
	return "", storageFailure("find current knowledge successor", errors.New("knowledge supersession chain exceeds 200 revisions"))
}

func (s *Store) ListKnowledge(ctx context.Context, query ListKnowledgeQuery) ([]domain.KnowledgeRevision, error) {
	query.WorkspaceIdentifier = strings.TrimSpace(query.WorkspaceIdentifier)
	query.ProjectIdentifier = strings.TrimSpace(query.ProjectIdentifier)
	query.TaskScopeID = strings.TrimSpace(query.TaskScopeID)
	query.Type = strings.TrimSpace(query.Type)
	query.ReviewStatus = strings.TrimSpace(query.ReviewStatus)
	query.CurrencyStatus = strings.TrimSpace(query.CurrencyStatus)
	if query.WorkspaceIdentifier == "" || query.ProjectIdentifier == "" || (query.Type != "" && !domain.ValidKnowledgeType(query.Type)) ||
		(query.ReviewStatus != "" && !domain.ValidKnowledgeReviewStatus(query.ReviewStatus)) ||
		(query.CurrencyStatus != "" && !domain.ValidKnowledgeCurrencyStatus(query.CurrencyStatus)) {
		return nil, knowledgeInvalid("knowledge list requires workspace, project, and valid optional filters")
	}
	workspace, err := s.Workspace(ctx, query.WorkspaceIdentifier)
	if err != nil {
		return nil, err
	}
	project, err := queryProject(ctx, s.db, workspace.ID, query.ProjectIdentifier)
	if err != nil {
		return nil, err
	}
	queries := dbgen.New(s.db)
	ids, err := queries.ListKnowledgeRevisionIDs(ctx, dbgen.ListKnowledgeRevisionIDsParams{
		WorkspaceID: workspace.ID, ProjectID: project.ID, TaskScopeID: query.TaskScopeID,
		Type: query.Type, ReviewStatus: query.ReviewStatus, CurrencyStatus: query.CurrencyStatus,
	})
	if err != nil {
		return nil, storageFailure("list knowledge revisions", err)
	}
	result := make([]domain.KnowledgeRevision, 0, len(ids))
	for _, id := range ids {
		revision, err := knowledgeRevision(ctx, queries, workspace.ID, id)
		if err != nil {
			return nil, err
		}
		result = append(result, revision)
	}
	return result, nil
}

func (s *Store) ListKnowledgeAuthorityChecks(ctx context.Context, workspaceIdentifier, revisionID string) ([]domain.KnowledgeAuthorityCheck, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return nil, err
	}
	rows, err := dbgen.New(s.db).ListKnowledgeAuthorityChecks(ctx, dbgen.ListKnowledgeAuthorityChecksParams{
		WorkspaceID: workspace.ID, RevisionID: strings.TrimSpace(revisionID),
	})
	if err != nil {
		return nil, storageFailure("list knowledge authority checks", err)
	}
	result := make([]domain.KnowledgeAuthorityCheck, 0, len(rows))
	for _, row := range rows {
		result = append(result, knowledgeAuthorityCheckFromRow(row))
	}
	return result, nil
}

type knowledgeQuerier interface {
	GetKnowledgeRevision(context.Context, dbgen.GetKnowledgeRevisionParams) (dbgen.GetKnowledgeRevisionRow, error)
	ListKnowledgeRevisionSources(context.Context, string) ([]dbgen.KnowledgeSource, error)
}

func knowledgeRevision(ctx context.Context, queries knowledgeQuerier, workspaceID, revisionID string) (domain.KnowledgeRevision, error) {
	if workspaceID == "" || revisionID == "" {
		return domain.KnowledgeRevision{}, knowledgeInvalid("knowledge revision lookup requires workspace and revision id")
	}
	row, err := queries.GetKnowledgeRevision(ctx, dbgen.GetKnowledgeRevisionParams{RevisionID: revisionID, WorkspaceID: workspaceID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.KnowledgeRevision{}, &Error{Code: CodeKnowledgeNotFound, Message: fmt.Sprintf("knowledge revision %q was not found", revisionID)}
	}
	if err != nil {
		return domain.KnowledgeRevision{}, storageFailure("query knowledge revision", err)
	}
	sourceRows, err := queries.ListKnowledgeRevisionSources(ctx, revisionID)
	if err != nil {
		return domain.KnowledgeRevision{}, storageFailure("list knowledge sources", err)
	}
	revision := domain.KnowledgeRevision{
		ID: row.ID, ItemID: row.ItemID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID,
		TaskScopeID: stringValue(row.TaskScopeID), Type: row.Type, RevisionNumber: row.RevisionNumber,
		StateRevision: row.StateRevision, Title: row.Title, Body: row.Body, ContentHash: row.ContentHash,
		ReviewStatus: row.ReviewStatus, CurrencyStatus: row.CurrencyStatus, Confidence: row.Confidence,
		VerificationStatus: row.VerificationStatus, FreshnessPolicy: row.FreshnessPolicy,
		FreshUntil: stringValue(row.FreshUntil), SupersedesRevisionID: stringValue(row.SupersedesRevisionID),
		ProposedAt: row.ProposedAt, ProposedBy: row.ProposedBy, ProposedByType: row.ProposedByType,
		AcceptedAt: stringValue(row.AcceptedAt), AcceptedBy: stringValue(row.AcceptedBy), AcceptedByType: stringValue(row.AcceptedByType),
		RejectedAt: stringValue(row.RejectedAt), RejectedBy: stringValue(row.RejectedBy), RejectedByType: stringValue(row.RejectedByType),
		StaleAt: stringValue(row.StaleAt), StaleBy: stringValue(row.StaleBy), StaleByType: stringValue(row.StaleByType),
		DecisionNote: stringValue(row.DecisionNote), StaleReason: stringValue(row.StaleReason),
		Sources: make([]domain.KnowledgeSource, 0, len(sourceRows)),
	}
	for _, source := range sourceRows {
		revision.Sources = append(revision.Sources, domain.KnowledgeSource{
			Type: source.SourceType, ID: source.SourceID, Revision: source.SourceRevision,
			Role: source.Role, Ordinal: source.Ordinal,
		})
	}
	return revision, nil
}

type resolvedKnowledgeSource struct {
	domain.KnowledgeSource
	WorkspaceID string
	ProjectID   string
}

func resolveKnowledgeSources(ctx context.Context, queries *dbgen.Queries, workspaceID string, inputs []domain.KnowledgeSourceInput) ([]resolvedKnowledgeSource, string, error) {
	result := make([]resolvedKnowledgeSource, 0, len(inputs))
	primaryProjectID, primaryCount := "", 0
	seen := make(map[string]struct{}, len(inputs))
	for index, input := range inputs {
		key := input.Type + "\x00" + input.ID
		if _, exists := seen[key]; exists {
			return nil, "", knowledgeInvalid("knowledge sources must be distinct")
		}
		seen[key] = struct{}{}
		resolved := resolvedKnowledgeSource{KnowledgeSource: domain.KnowledgeSource{
			Type: input.Type, ID: input.ID, Role: input.Role, Ordinal: int64(index),
		}}
		var err error
		switch input.Type {
		case domain.KnowledgeSourceTask:
			row, queryErr := queries.GetKnowledgeSourceTask(ctx, input.ID)
			if queryErr == nil {
				resolved.WorkspaceID, resolved.ProjectID, resolved.Revision = row.WorkspaceID, row.ProjectID, row.Revision
			}
			err = queryErr
		case domain.KnowledgeSourceMeeting:
			row, queryErr := queries.GetKnowledgeSourceMeeting(ctx, input.ID)
			if queryErr == nil {
				if row.Status != domain.MeetingConcluded {
					return nil, "", knowledgeInvalid("meeting knowledge sources must be concluded")
				}
				resolved.WorkspaceID, resolved.ProjectID, resolved.Revision = row.WorkspaceID, row.ProjectID, row.Revision
			}
			err = queryErr
		case domain.KnowledgeSourceMeetingProposal:
			row, queryErr := queries.GetKnowledgeSourceMeetingProposal(ctx, input.ID)
			if queryErr == nil {
				if row.ProposalStatus != domain.MeetingProposalAccepted || row.MeetingStatus != domain.MeetingConcluded {
					return nil, "", knowledgeInvalid("meeting-proposal knowledge sources must be accepted and concluded")
				}
				resolved.WorkspaceID, resolved.ProjectID, resolved.Revision = row.WorkspaceID, row.ProjectID, row.Revision
			}
			err = queryErr
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", knowledgeInvalid(fmt.Sprintf("knowledge source %s %q was not found", input.Type, input.ID))
		}
		if err != nil {
			return nil, "", storageFailure("resolve knowledge source", err)
		}
		if resolved.WorkspaceID != workspaceID {
			return nil, "", knowledgeInvalid("knowledge source belongs to a different workspace")
		}
		if input.Role == domain.KnowledgeSourcePrimary {
			primaryCount++
			primaryProjectID = resolved.ProjectID
		}
		result = append(result, resolved)
	}
	if primaryCount != 1 {
		return nil, "", knowledgeInvalid("knowledge requires exactly one primary source")
	}
	for _, source := range result {
		if source.ProjectID != primaryProjectID {
			return nil, "", knowledgeInvalid("all knowledge sources must belong to the primary source project")
		}
	}
	return result, primaryProjectID, nil
}

func insertKnowledgeAuthorityCheck(ctx context.Context, queries *dbgen.Queries, workspaceID, revisionID, action string,
	actor domain.KnowledgeActor, outcome, reason, note, idempotencyKey, requestHash string, eventSequence int64, now string,
) (domain.KnowledgeAuthorityCheck, error) {
	id, err := randomID("kauth_")
	if err != nil {
		return domain.KnowledgeAuthorityCheck{}, storageFailure("generate knowledge authority check id", err)
	}
	row := dbgen.InsertKnowledgeAuthorityCheckParams{
		ID: id, WorkspaceID: workspaceID, RevisionID: revisionID, Action: action,
		ActorID: actor.ID, ActorType: actor.Type, Outcome: outcome, Reason: reason,
		Note: optionalStringPointer(note), IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		EventSequence: eventSequence, CreatedAt: now,
	}
	if err := queries.InsertKnowledgeAuthorityCheck(ctx, row); err != nil {
		return domain.KnowledgeAuthorityCheck{}, storageFailure("insert knowledge authority check", err)
	}
	return domain.KnowledgeAuthorityCheck{
		ID: id, WorkspaceID: workspaceID, RevisionID: revisionID, Action: action,
		Actor: actor, Outcome: outcome, Reason: reason, Note: note,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash, EventSequence: eventSequence, CreatedAt: now,
	}, nil
}

func knowledgeAuthorityCheckFromRow(row dbgen.KnowledgeAuthorityCheck) domain.KnowledgeAuthorityCheck {
	return domain.KnowledgeAuthorityCheck{
		ID: row.ID, WorkspaceID: row.WorkspaceID, RevisionID: row.RevisionID, Action: row.Action,
		Actor: domain.KnowledgeActor{ID: row.ActorID, Type: row.ActorType}, Outcome: row.Outcome,
		Reason: row.Reason, Note: stringValue(row.Note), IdempotencyKey: row.IdempotencyKey,
		RequestHash: row.RequestHash, EventSequence: row.EventSequence, CreatedAt: row.CreatedAt,
	}
}

func normalizeProposeKnowledgeCommand(command *ProposeKnowledgeCommand) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.TaskScopeID = strings.TrimSpace(command.TaskScopeID)
	command.Type = strings.TrimSpace(command.Type)
	command.Title = strings.TrimSpace(command.Title)
	command.Body = strings.TrimSpace(command.Body)
	command.Confidence = strings.TrimSpace(command.Confidence)
	command.VerificationStatus = strings.TrimSpace(command.VerificationStatus)
	command.FreshnessPolicy = strings.TrimSpace(command.FreshnessPolicy)
	command.FreshUntil = strings.TrimSpace(command.FreshUntil)
	command.SupersedesRevisionID = strings.TrimSpace(command.SupersedesRevisionID)
	command.Actor.ID = strings.TrimSpace(command.Actor.ID)
	command.Actor.Type = strings.TrimSpace(command.Actor.Type)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	for index := range command.Sources {
		command.Sources[index].Type = strings.TrimSpace(command.Sources[index].Type)
		command.Sources[index].ID = strings.TrimSpace(command.Sources[index].ID)
		command.Sources[index].Role = strings.TrimSpace(command.Sources[index].Role)
	}
}

func validateKnowledgeProposal(command ProposeKnowledgeCommand) error {
	if command.WorkspaceIdentifier == "" || !domain.ValidKnowledgeType(command.Type) || !validKnowledgeActor(command.Actor) ||
		!validKnowledgeText(command.Title, maximumKnowledgeTitleBytes) || !validKnowledgeText(command.Body, maximumKnowledgeBodyBytes) ||
		!domain.ValidKnowledgeConfidence(command.Confidence) || !domain.ValidKnowledgeVerification(command.VerificationStatus) ||
		!domain.ValidKnowledgeFreshnessPolicy(command.FreshnessPolicy) || len(command.Sources) == 0 || len(command.Sources) > maximumKnowledgeSources {
		return knowledgeInvalid("knowledge proposal requires bounded content, a valid type, quality metadata, trusted actor, and one to sixteen sources")
	}
	if command.FreshnessPolicy == domain.KnowledgeFreshUntilSuperseded && command.FreshUntil != "" {
		return knowledgeInvalid("until_superseded freshness cannot set fresh_until")
	}
	if command.FreshnessPolicy == domain.KnowledgeFreshExpiresAt {
		if _, err := time.Parse(time.RFC3339Nano, command.FreshUntil); err != nil {
			return knowledgeInvalid("expires_at freshness requires an RFC3339 fresh_until")
		}
	}
	for _, source := range command.Sources {
		if !domain.ValidKnowledgeSourceType(source.Type) || !domain.ValidKnowledgeSourceRole(source.Role) ||
			!validKnowledgeText(source.ID, 128) {
			return knowledgeInvalid("knowledge sources require a valid bounded type, id, and role")
		}
	}
	return validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidKnowledge)
}

func validKnowledgeText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validKnowledgeNote(value string, required bool) bool {
	if value == "" {
		return !required
	}
	return validKnowledgeText(value, maximumKnowledgeDecisionBytes)
}

func validKnowledgeActor(actor domain.KnowledgeActor) bool {
	return validKnowledgeText(actor.ID, 128) && domain.ValidKnowledgeActorType(actor.Type)
}

func validateKnowledgeActorWorkspace(ctx context.Context, queries *dbgen.Queries, actor domain.KnowledgeActor, workspaceID string) error {
	if actor.Type != domain.KnowledgeActorAgentRun {
		return nil
	}
	runWorkspaceID, err := queries.GetKnowledgeActorRunWorkspace(ctx, actor.ID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && runWorkspaceID != workspaceID) {
		return knowledgeInvalid("agent-run actor does not belong to the knowledge workspace")
	}
	if err != nil {
		return storageFailure("validate knowledge actor run", err)
	}
	return nil
}

func knowledgeActorIsOwner(actor domain.KnowledgeActor) bool {
	return actor.ID == localOwnerActorID && actor.Type == localActorType
}

func knowledgeIdempotencyKey(actor domain.KnowledgeActor, key string) string {
	digest := sha256.Sum256([]byte(actor.Type + "\n" + actor.ID + "\n" + key))
	return "knowledge:" + hex.EncodeToString(digest[:])
}

func knowledgeContentHash(title, body string) string {
	digest := sha256.Sum256([]byte(title + "\n" + body))
	return hex.EncodeToString(digest[:])
}

func knowledgeInvalid(message string) error {
	return &Error{Code: CodeInvalidKnowledge, Message: message}
}

func knowledgeConflict(message string) error {
	return &Error{Code: CodeKnowledgeConflict, Message: message}
}
