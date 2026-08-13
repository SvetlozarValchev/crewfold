package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	curatorRuleConfiguredEvent = "curator.rule_configured"
	curatorDerivedEvent        = "curator.derived"
	curatorAutoAcceptedEvent   = "curator.auto_accepted"
	curatorSourceType          = domain.KnowledgeSourceMeetingProposal
	maximumCuratorCandidates   = 100
	maximumCuratorAcceptances  = 10
	maximumCuratorSummaryBytes = 2 * 1024
	maximumCuratorCursorBytes  = 512
)

var curatorActor = domain.KnowledgeActor{ID: domain.CuratorActorID, Type: domain.KnowledgeActorSubsystem}

type curatorQueueCursor struct {
	Version     int    `json:"v"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	ProposedAt  string `json:"proposed_at"`
	RevisionID  string `json:"revision_id"`
}

func (s *Store) QueueCuratorRevisions(ctx context.Context, query CuratorQueueQuery) (domain.CuratorQueue, error) {
	query.WorkspaceIdentifier = strings.TrimSpace(query.WorkspaceIdentifier)
	query.ProjectIdentifier = strings.TrimSpace(query.ProjectIdentifier)
	query.After = strings.TrimSpace(query.After)
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.WorkspaceIdentifier == "" || query.ProjectIdentifier == "" || query.Limit < 1 || query.Limit > 200 {
		return domain.CuratorQueue{}, knowledgeInvalid("curator queue requires workspace, project, and limit from 1 to 200")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.CuratorQueue{}, storageFailure("begin curator queue snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, query.WorkspaceIdentifier)
	if err != nil {
		return domain.CuratorQueue{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, query.ProjectIdentifier)
	if err != nil {
		return domain.CuratorQueue{}, err
	}
	cursor, err := decodeCuratorQueueCursor(query.After, workspace.ID, project.ID)
	if err != nil {
		return domain.CuratorQueue{}, err
	}
	queries := dbgen.New(tx)
	ruleRow, err := queries.GetEffectiveCuratorRule(ctx, dbgen.GetEffectiveCuratorRuleParams{
		WorkspaceID: workspace.ID, Name: domain.CuratorRuleAcceptedMeetingResolutionCopy,
	})
	if err != nil {
		return domain.CuratorQueue{}, storageFailure("read effective curator rule", err)
	}
	ids, err := queries.ListCuratorQueueRevisionIDs(ctx, dbgen.ListCuratorQueueRevisionIDsParams{
		WorkspaceID: workspace.ID, ProjectID: project.ID, AfterProposedAt: cursor.ProposedAt,
		AfterRevisionID: cursor.RevisionID, ResultLimit: int64(query.Limit + 1),
	})
	if err != nil {
		return domain.CuratorQueue{}, storageFailure("list curator queue", err)
	}
	hasMore := len(ids) > query.Limit
	if hasMore {
		ids = ids[:query.Limit]
	}
	queue := domain.CuratorQueue{Entries: make([]domain.CuratorQueueEntry, 0, len(ids)), Rule: curatorRuleFromRow(ruleRow)}
	for _, id := range ids {
		revision, loadErr := knowledgeRevision(ctx, queries, workspace.ID, id)
		if loadErr != nil {
			return domain.CuratorQueue{}, loadErr
		}
		entry, entryErr := curatorQueueEntry(ctx, queries, revision, ruleRow.Enabled != 0)
		if entryErr != nil {
			return domain.CuratorQueue{}, entryErr
		}
		queue.Entries = append(queue.Entries, entry)
	}
	if hasMore && len(queue.Entries) > 0 {
		last := queue.Entries[len(queue.Entries)-1].Revision
		queue.NextCursor, err = encodeCuratorQueueCursor(curatorQueueCursor{
			Version: 1, WorkspaceID: workspace.ID, ProjectID: project.ID,
			ProposedAt: last.ProposedAt, RevisionID: last.ID,
		})
		if err != nil {
			return domain.CuratorQueue{}, storageFailure("encode curator queue cursor", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.CuratorQueue{}, storageFailure("commit curator queue snapshot", err)
	}
	return queue, nil
}

func (s *Store) ConfigureCuratorRule(ctx context.Context, command ConfigureCuratorRuleCommand) (CuratorRuleMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.RuleName = strings.TrimSpace(command.RuleName)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.RuleName != domain.CuratorRuleAcceptedMeetingResolutionCopy || command.ExpectedRevision < 1 {
		return CuratorRuleMutationResult{}, knowledgeInvalid("curator rule configuration requires workspace, the supported rule, and expected revision")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidKnowledge); err != nil {
		return CuratorRuleMutationResult{}, err
	}
	requestHash, err := hashCommand("curator.rule.configure", map[string]any{
		"workspace": command.WorkspaceIdentifier, "rule_name": command.RuleName,
		"enabled": command.Enabled, "expected_revision": command.ExpectedRevision,
	})
	if err != nil {
		return CuratorRuleMutationResult{}, storageFailure("hash curator rule configuration", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CuratorRuleMutationResult{}, storageFailure("begin curator rule configuration", err)
	}
	defer tx.Rollback()
	var replay CuratorRuleMutationResult
	if found, replayErr := lookupIdempotency(ctx, tx, command.IdempotencyKey, "curator.rule.configure", requestHash, &replay); replayErr != nil {
		return CuratorRuleMutationResult{}, replayErr
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return CuratorRuleMutationResult{}, err
	}
	queries := dbgen.New(tx)
	currentRow, err := queries.GetEffectiveCuratorRule(ctx, dbgen.GetEffectiveCuratorRuleParams{WorkspaceID: workspace.ID, Name: command.RuleName})
	if err != nil {
		return CuratorRuleMutationResult{}, storageFailure("read current curator rule", err)
	}
	if currentRow.Revision != command.ExpectedRevision {
		return CuratorRuleMutationResult{}, &Error{Code: CodeRevisionConflict, Message: fmt.Sprintf("curator rule revision is %d, expected %d", currentRow.Revision, command.ExpectedRevision)}
	}
	ruleID, err := randomID("crule_")
	if err != nil {
		return CuratorRuleMutationResult{}, storageFailure("generate curator rule id", err)
	}
	now := s.nowText()
	updated := domain.CuratorRule{ID: ruleID, WorkspaceID: workspace.ID, Name: command.RuleName,
		Revision: currentRow.Revision + 1, Enabled: command.Enabled, CreatedAt: now, CreatedBy: localOwnerActorID}
	sequence, err := appendEventForActor(ctx, tx, workspace.ID, "curator_rule", ruleID, updated.Revision,
		curatorRuleConfiguredEvent, command.CorrelationID, now, localOwnerActorID, localActorType,
		map[string]any{"name": updated.Name, "enabled": updated.Enabled, "previous_revision": currentRow.Revision})
	if err != nil {
		return CuratorRuleMutationResult{}, err
	}
	updated.EventSequence = sequence
	if err := queries.InsertCuratorRule(ctx, dbgen.InsertCuratorRuleParams{
		ID: updated.ID, WorkspaceID: updated.WorkspaceID, Name: updated.Name, Revision: updated.Revision,
		Enabled: boolInteger(updated.Enabled), CreatedAt: updated.CreatedAt, CreatedBy: updated.CreatedBy, EventSequence: sequence,
	}); err != nil {
		return CuratorRuleMutationResult{}, storageFailure("insert curator rule revision", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return CuratorRuleMutationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return CuratorRuleMutationResult{}, err
	}
	result := CuratorRuleMutationResult{Rule: updated, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "curator.rule.configure", requestHash, result, now); err != nil {
		return CuratorRuleMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CuratorRuleMutationResult{}, storageFailure("commit curator rule configuration", err)
	}
	return result, nil
}

func (s *Store) ProcessCurator(ctx context.Context, command ProcessCuratorCommand) (CuratorProcessMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" {
		return CuratorProcessMutationResult{}, knowledgeInvalid("curator process requires workspace and project")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidKnowledge); err != nil {
		return CuratorProcessMutationResult{}, err
	}
	requestHash, err := hashCommand("curator.process", map[string]any{
		"workspace": command.WorkspaceIdentifier, "project": command.ProjectIdentifier, "apply_safe": command.ApplySafe,
	})
	if err != nil {
		return CuratorProcessMutationResult{}, storageFailure("hash curator process", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CuratorProcessMutationResult{}, storageFailure("begin curator process", err)
	}
	defer tx.Rollback()
	var replay CuratorProcessMutationResult
	if found, replayErr := lookupIdempotency(ctx, tx, command.IdempotencyKey, "curator.process", requestHash, &replay); replayErr != nil {
		return CuratorProcessMutationResult{}, replayErr
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return CuratorProcessMutationResult{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return CuratorProcessMutationResult{}, err
	}
	queries := dbgen.New(tx)
	ruleRow, err := queries.GetEffectiveCuratorRule(ctx, dbgen.GetEffectiveCuratorRuleParams{WorkspaceID: workspace.ID, Name: domain.CuratorRuleAcceptedMeetingResolutionCopy})
	if err != nil {
		return CuratorProcessMutationResult{}, storageFailure("read curator rule", err)
	}
	rule := curatorRuleFromRow(ruleRow)
	startingEventSequence, err := queries.MaxEventSequence(ctx)
	if err != nil {
		return CuratorProcessMutationResult{}, storageFailure("freeze curator candidate cursor", err)
	}
	process := domain.CuratorProcess{Derived: []domain.CuratorDerivation{}, Accepted: []domain.CuratorAutoAcceptance{}, Skipped: []domain.CuratorSkip{}}
	lastSequence := int64(0)
	remainingBudget := maximumCuratorCandidates
	if command.ApplySafe && rule.Enabled {
		ids, listErr := queries.ListCuratorEligibleRevisionIDs(ctx, dbgen.ListCuratorEligibleRevisionIDsParams{
			WorkspaceID: workspace.ID, ProjectID: project.ID, RuleName: rule.Name,
			MaxEventSequence: startingEventSequence, ResultLimit: maximumCuratorCandidates,
		})
		if listErr != nil {
			return CuratorProcessMutationResult{}, storageFailure("scan curator candidates", listErr)
		}
		for _, id := range ids {
			if process.CandidatesScanned >= maximumCuratorCandidates || len(process.Accepted) >= maximumCuratorAcceptances {
				break
			}
			process.CandidatesScanned++
			remainingBudget--
			revision, loadErr := knowledgeRevision(ctx, queries, workspace.ID, id)
			if loadErr != nil {
				return CuratorProcessMutationResult{}, loadErr
			}
			derivationRow, deriveErr := queries.GetCuratorDerivationByKnowledge(ctx, dbgen.GetCuratorDerivationByKnowledgeParams{WorkspaceID: workspace.ID, KnowledgeRevisionID: id})
			if errors.Is(deriveErr, sql.ErrNoRows) {
				return CuratorProcessMutationResult{}, storageFailure("eligible curator revision has no derivation", deriveErr)
			}
			if deriveErr != nil {
				return CuratorProcessMutationResult{}, storageFailure("read curator derivation", deriveErr)
			}
			derivation := curatorDerivationFromRow(derivationRow)
			eligible, eligibilityErr := curatorDerivationMatches(ctx, queries, revision, derivation)
			if eligibilityErr != nil {
				return CuratorProcessMutationResult{}, eligibilityErr
			}
			if !eligible {
				continue
			}
			auto, sequence, acceptErr := s.autoAcceptCuratorRevision(ctx, tx, queries, workspace.ID, project.ID, rule,
				revision, derivation, command.CorrelationID, command.IdempotencyKey)
			if acceptErr != nil {
				return CuratorProcessMutationResult{}, acceptErr
			}
			process.Accepted = append(process.Accepted, auto)
			lastSequence = sequence
		}
	}

	candidates, err := queries.ListCuratorMeetingProposalCandidates(ctx, dbgen.ListCuratorMeetingProposalCandidatesParams{
		WorkspaceID: workspace.ID, ProjectID: project.ID, RuleName: rule.Name, CandidateLimit: int64(remainingBudget),
	})
	if err != nil {
		return CuratorProcessMutationResult{}, storageFailure("discover curator candidates", err)
	}
	for _, candidate := range candidates {
		if process.CandidatesScanned >= maximumCuratorCandidates {
			break
		}
		process.CandidatesScanned++
		if reason := curatorSourceSkipReason(candidate.Agenda, candidate.Summary); reason != "" {
			process.Skipped = append(process.Skipped, domain.CuratorSkip{SourceType: curatorSourceType,
				SourceID: candidate.ProposalID, SourceRevision: candidate.ProposalRevision, Reason: reason})
			continue
		}
		derivation, sequence, deriveErr := s.deriveMeetingResolution(ctx, tx, queries, workspace.ID, project.ID, rule, candidate, command.CorrelationID)
		if deriveErr != nil {
			return CuratorProcessMutationResult{}, deriveErr
		}
		process.Derived = append(process.Derived, derivation)
		lastSequence = sequence
		if command.ApplySafe && rule.Enabled && len(process.Accepted) < maximumCuratorAcceptances {
			revision, loadErr := knowledgeRevision(ctx, queries, workspace.ID, derivation.KnowledgeRevisionID)
			if loadErr != nil {
				return CuratorProcessMutationResult{}, loadErr
			}
			eligible, eligibilityErr := curatorDerivationMatches(ctx, queries, revision, derivation)
			if eligibilityErr != nil {
				return CuratorProcessMutationResult{}, eligibilityErr
			}
			if eligible {
				auto, acceptSequence, acceptErr := s.autoAcceptCuratorRevision(ctx, tx, queries, workspace.ID, project.ID, rule,
					revision, derivation, command.CorrelationID, command.IdempotencyKey)
				if acceptErr != nil {
					return CuratorProcessMutationResult{}, acceptErr
				}
				process.Accepted = append(process.Accepted, auto)
				lastSequence = acceptSequence
			}
		}
	}

	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return CuratorProcessMutationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return CuratorProcessMutationResult{}, err
	}
	result := CuratorProcessMutationResult{Process: process, EventSequence: lastSequence}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "curator.process", requestHash, result, s.nowText()); err != nil {
		return CuratorProcessMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CuratorProcessMutationResult{}, storageFailure("commit curator process", err)
	}
	s.refreshKnowledgeIndexAfterCanonicalMutation(ctx)
	return result, nil
}

func (s *Store) deriveMeetingResolution(ctx context.Context, tx *sql.Tx, queries *dbgen.Queries,
	workspaceID, projectID string, rule domain.CuratorRule, source dbgen.ListCuratorMeetingProposalCandidatesRow, correlationID string,
) (domain.CuratorDerivation, int64, error) {
	itemID, err := randomID("know_")
	if err != nil {
		return domain.CuratorDerivation{}, 0, storageFailure("generate curator knowledge item id", err)
	}
	revisionID, err := randomID("krev_")
	if err != nil {
		return domain.CuratorDerivation{}, 0, storageFailure("generate curator knowledge revision id", err)
	}
	derivationID, err := randomID("cder_")
	if err != nil {
		return domain.CuratorDerivation{}, 0, storageFailure("generate curator derivation id", err)
	}
	now := s.nowText()
	outputHash := knowledgeContentHash(source.Agenda, source.Summary)
	sourceHash := curatorSourceContentHash(source.MeetingID, source.ProposalID, source.ProposalRevision,
		source.Agenda, source.Summary, source.ProposalStatus, source.MeetingStatus)
	if err := queries.InsertKnowledgeItem(ctx, dbgen.InsertKnowledgeItemParams{
		ID: itemID, WorkspaceID: workspaceID, ProjectID: projectID, TaskScopeID: nil,
		Type: domain.KnowledgeTypeDecision, CreatedAt: now, CreatedBy: curatorActor.ID, CreatedByType: curatorActor.Type,
	}); err != nil {
		return domain.CuratorDerivation{}, 0, storageFailure("insert curator knowledge item", err)
	}
	if err := queries.InsertKnowledgeRevision(ctx, dbgen.InsertKnowledgeRevisionParams{
		ID: revisionID, ItemID: itemID, RevisionNumber: 1, Title: source.Agenda, Body: source.Summary,
		ContentHash: outputHash, Confidence: domain.KnowledgeConfidenceMedium,
		VerificationStatus: domain.KnowledgeVerificationSupported, FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		FreshUntil: nil, SupersedesRevisionID: nil, ProposedAt: now, ProposedBy: curatorActor.ID, ProposedByType: curatorActor.Type,
	}); err != nil {
		return domain.CuratorDerivation{}, 0, storageFailure("insert curator knowledge revision", err)
	}
	if err := queries.InsertKnowledgeSource(ctx, dbgen.InsertKnowledgeSourceParams{
		RevisionID: revisionID, Ordinal: 0, SourceType: curatorSourceType, SourceID: source.ProposalID,
		SourceRevision: source.ProposalRevision, Role: domain.KnowledgeSourcePrimary,
	}); err != nil {
		return domain.CuratorDerivation{}, 0, storageFailure("insert curator knowledge source", err)
	}
	proposalSequence, err := appendEventForActor(ctx, tx, workspaceID, "knowledge_revision", revisionID, 1,
		knowledgeProposedEvent, correlationID, now, curatorActor.ID, curatorActor.Type,
		map[string]any{"item_id": itemID, "project_id": projectID, "task_scope_id": "", "type": domain.KnowledgeTypeDecision,
			"supersedes_revision_id": "", "source_count": 1, "curator_rule": rule.Name})
	if err != nil {
		return domain.CuratorDerivation{}, 0, err
	}
	sequence, err := appendEventForActor(ctx, tx, workspaceID, "curator_derivation", derivationID, 1,
		curatorDerivedEvent, correlationID, now, curatorActor.ID, curatorActor.Type,
		map[string]any{"rule_name": rule.Name, "rule_revision": rule.Revision, "source_type": curatorSourceType,
			"source_id": source.ProposalID, "source_revision": source.ProposalRevision,
			"source_content_hash": sourceHash, "knowledge_revision_id": revisionID,
			"output_content_hash": outputHash, "knowledge_event_sequence": proposalSequence})
	if err != nil {
		return domain.CuratorDerivation{}, 0, err
	}
	derivation := domain.CuratorDerivation{ID: derivationID, WorkspaceID: workspaceID, ProjectID: projectID,
		RuleID: rule.ID, RuleName: rule.Name, RuleRevision: rule.Revision, SourceType: curatorSourceType,
		SourceID: source.ProposalID, SourceRevision: source.ProposalRevision, SourceContentHash: sourceHash,
		KnowledgeRevisionID: revisionID, OutputContentHash: outputHash, CreatedAt: now,
		CreatedBy: curatorActor.ID, EventSequence: sequence}
	if err := queries.InsertCuratorDerivation(ctx, dbgen.InsertCuratorDerivationParams{
		ID: derivation.ID, WorkspaceID: derivation.WorkspaceID, ProjectID: derivation.ProjectID,
		RuleID: derivation.RuleID, RuleName: derivation.RuleName, RuleRevision: derivation.RuleRevision,
		SourceType: derivation.SourceType, SourceID: derivation.SourceID, SourceRevision: derivation.SourceRevision,
		SourceContentHash: derivation.SourceContentHash, KnowledgeRevisionID: derivation.KnowledgeRevisionID,
		OutputContentHash: derivation.OutputContentHash, CreatedAt: derivation.CreatedAt,
		CreatedBy: derivation.CreatedBy, EventSequence: derivation.EventSequence,
	}); err != nil {
		return domain.CuratorDerivation{}, 0, storageFailure("insert curator derivation", err)
	}
	return derivation, sequence, nil
}

func (s *Store) autoAcceptCuratorRevision(ctx context.Context, tx *sql.Tx, queries *dbgen.Queries,
	workspaceID, projectID string, rule domain.CuratorRule, revision domain.KnowledgeRevision,
	derivation domain.CuratorDerivation, correlationID, processKey string,
) (domain.CuratorAutoAcceptance, int64, error) {
	now := s.nowText()
	note := "accepted by bounded deterministic curator rule " + rule.Name
	rows, err := queries.AcceptKnowledgeRevision(ctx, dbgen.AcceptKnowledgeRevisionParams{
		AcceptedAt: &now, AcceptedBy: &curatorActor.ID, AcceptedByType: &curatorActor.Type,
		DecisionNote: &note, ID: revision.ID, ExpectedStateRevision: revision.StateRevision,
	})
	if err != nil {
		return domain.CuratorAutoAcceptance{}, 0, storageFailure("auto-accept curator knowledge", err)
	}
	if rows != 1 {
		return domain.CuratorAutoAcceptance{}, 0, knowledgeConflict("curator proposal changed before auto-acceptance")
	}
	knowledgeSequence, err := appendEventForActor(ctx, tx, workspaceID, "knowledge_revision", revision.ID,
		revision.StateRevision+1, knowledgeAcceptedEvent, correlationID, now, curatorActor.ID, curatorActor.Type,
		map[string]any{"item_id": revision.ItemID, "decision_note": note,
			"supersedes_revision_id": "", "curator_rule": rule.Name})
	if err != nil {
		return domain.CuratorAutoAcceptance{}, 0, err
	}
	authorityKey := curatorAuthorityIdempotencyKey(processKey, revision.ID)
	requestHash, err := hashCommand("curator.auto_accept", map[string]any{
		"workspace": workspaceID, "project": projectID, "rule_id": rule.ID, "rule_revision": rule.Revision,
		"derivation_id": derivation.ID, "revision_id": revision.ID,
	})
	if err != nil {
		return domain.CuratorAutoAcceptance{}, 0, storageFailure("hash curator auto acceptance", err)
	}
	check, err := insertKnowledgeAuthorityCheck(ctx, queries, workspaceID, revision.ID,
		domain.KnowledgeAuthorityAccept, curatorActor, domain.KnowledgeAuthorityAllowed,
		domain.KnowledgeAuthorityReasonStatePolicy, note, authorityKey, requestHash, knowledgeSequence, now)
	if err != nil {
		return domain.CuratorAutoAcceptance{}, 0, err
	}
	autoID, err := randomID("cauto_")
	if err != nil {
		return domain.CuratorAutoAcceptance{}, 0, storageFailure("generate curator auto acceptance id", err)
	}
	sequence, err := appendEventForActor(ctx, tx, workspaceID, "curator_auto_acceptance", autoID, 1,
		curatorAutoAcceptedEvent, correlationID, now, curatorActor.ID, curatorActor.Type,
		map[string]any{"rule_name": rule.Name, "rule_revision": rule.Revision, "derivation_id": derivation.ID,
			"knowledge_revision_id": revision.ID, "authority_check_id": check.ID,
			"knowledge_event_sequence": knowledgeSequence})
	if err != nil {
		return domain.CuratorAutoAcceptance{}, 0, err
	}
	auto := domain.CuratorAutoAcceptance{ID: autoID, WorkspaceID: workspaceID, ProjectID: projectID,
		RuleID: rule.ID, RuleName: rule.Name, RuleRevision: rule.Revision, DerivationID: derivation.ID,
		KnowledgeRevisionID: revision.ID, AuthorityCheckID: check.ID, KnowledgeEventSequence: knowledgeSequence,
		EventSequence: sequence, CreatedAt: now, Actor: curatorActor}
	if err := queries.InsertCuratorAutoAcceptance(ctx, dbgen.InsertCuratorAutoAcceptanceParams{
		ID: auto.ID, WorkspaceID: auto.WorkspaceID, ProjectID: auto.ProjectID, RuleID: auto.RuleID,
		RuleName: auto.RuleName, RuleRevision: auto.RuleRevision, DerivationID: auto.DerivationID,
		KnowledgeRevisionID: auto.KnowledgeRevisionID, AuthorityCheckID: auto.AuthorityCheckID,
		KnowledgeEventSequence: auto.KnowledgeEventSequence, EventSequence: auto.EventSequence,
		CreatedAt: auto.CreatedAt, ActorID: auto.Actor.ID, ActorType: auto.Actor.Type,
	}); err != nil {
		return domain.CuratorAutoAcceptance{}, 0, storageFailure("insert curator auto acceptance", err)
	}
	return auto, sequence, nil
}

func curatorQueueEntry(ctx context.Context, queries *dbgen.Queries, revision domain.KnowledgeRevision, ruleEnabled bool) (domain.CuratorQueueEntry, error) {
	entry := domain.CuratorQueueEntry{Revision: revision, Eligibility: domain.CuratorEligibilityManual,
		EligibilityReason: domain.CuratorEligibilityReasonNotDerived, RuleEnabled: ruleEnabled}
	row, err := queries.GetCuratorDerivationByKnowledge(ctx, dbgen.GetCuratorDerivationByKnowledgeParams{
		WorkspaceID: revision.WorkspaceID, KnowledgeRevisionID: revision.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return entry, nil
	}
	if err != nil {
		return domain.CuratorQueueEntry{}, storageFailure("read curator derivation", err)
	}
	derivation := curatorDerivationFromRow(row)
	entry.Derivation = &derivation
	matches, err := curatorDerivationMatches(ctx, queries, revision, derivation)
	if err != nil {
		return domain.CuratorQueueEntry{}, err
	}
	if !matches {
		entry.EligibilityReason = domain.CuratorEligibilityReasonDerivationMismatch
		return entry, nil
	}
	if !ruleEnabled {
		entry.EligibilityReason = domain.CuratorEligibilityReasonRuleDisabled
		return entry, nil
	}
	entry.Eligibility = domain.CuratorEligibilitySafe
	entry.EligibilityReason = domain.CuratorEligibilityReasonAcceptedMeetingResolutionCopy
	return entry, nil
}

func curatorDerivationMatches(ctx context.Context, queries *dbgen.Queries, revision domain.KnowledgeRevision, derivation domain.CuratorDerivation) (bool, error) {
	if derivation.RuleName != domain.CuratorRuleAcceptedMeetingResolutionCopy || derivation.SourceType != curatorSourceType ||
		derivation.KnowledgeRevisionID != revision.ID || revision.ProjectID != derivation.ProjectID ||
		revision.TaskScopeID != "" || revision.Type != domain.KnowledgeTypeDecision ||
		revision.ReviewStatus != domain.KnowledgeReviewProposed || revision.CurrencyStatus != domain.KnowledgeCurrencyPending ||
		revision.Confidence != domain.KnowledgeConfidenceMedium || revision.VerificationStatus != domain.KnowledgeVerificationSupported ||
		revision.FreshnessPolicy != domain.KnowledgeFreshUntilSuperseded || revision.FreshUntil != "" ||
		revision.SupersedesRevisionID != "" || revision.ProposedBy != curatorActor.ID || revision.ProposedByType != curatorActor.Type ||
		len(revision.Sources) != 1 {
		return false, nil
	}
	source := revision.Sources[0]
	if source.Type != curatorSourceType || source.ID != derivation.SourceID || source.Revision != derivation.SourceRevision ||
		source.Role != domain.KnowledgeSourcePrimary || source.Ordinal != 0 {
		return false, nil
	}
	row, err := queries.GetCuratorMeetingProposalSource(ctx, derivation.SourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storageFailure("revalidate curator source", err)
	}
	if row.WorkspaceID != revision.WorkspaceID || row.ProjectID != revision.ProjectID ||
		row.ProposalRevision != derivation.SourceRevision || row.ProposalStatus != domain.MeetingProposalAccepted ||
		row.MeetingStatus != domain.MeetingConcluded || curatorSourceSkipReason(row.Agenda, row.Summary) != "" ||
		revision.Title != row.Agenda || revision.Body != row.Summary ||
		derivation.SourceContentHash != curatorSourceContentHash(row.MeetingID, row.ProposalID, row.ProposalRevision,
			row.Agenda, row.Summary, row.ProposalStatus, row.MeetingStatus) ||
		derivation.OutputContentHash != knowledgeContentHash(row.Agenda, row.Summary) ||
		revision.ContentHash != derivation.OutputContentHash {
		return false, nil
	}
	return true, nil
}

func curatorRuleFromRow(row dbgen.CuratorRule) domain.CuratorRule {
	return domain.CuratorRule{ID: row.ID, WorkspaceID: row.WorkspaceID, Name: row.Name,
		Revision: row.Revision, Enabled: row.Enabled != 0, CreatedAt: row.CreatedAt,
		CreatedBy: row.CreatedBy, EventSequence: row.EventSequence}
}

func curatorDerivationFromRow(row dbgen.CuratorDerivation) domain.CuratorDerivation {
	return domain.CuratorDerivation{ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID,
		RuleID: row.RuleID, RuleName: row.RuleName, RuleRevision: row.RuleRevision,
		SourceType: row.SourceType, SourceID: row.SourceID, SourceRevision: row.SourceRevision,
		SourceContentHash: row.SourceContentHash, KnowledgeRevisionID: row.KnowledgeRevisionID,
		OutputContentHash: row.OutputContentHash, CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy,
		EventSequence: row.EventSequence}
}

func curatorSourceSkipReason(agenda, summary string) string {
	if !validCuratorCopyText(agenda, maximumKnowledgeTitleBytes) {
		return domain.CuratorSkipAgendaInvalid
	}
	if !validCuratorCopyText(summary, maximumCuratorSummaryBytes) {
		return domain.CuratorSkipSummaryInvalid
	}
	return ""
}

func validCuratorCopyText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func curatorSourceContentHash(meetingID, proposalID string, proposalRevision int64, agenda, summary, proposalStatus, meetingStatus string) string {
	value := strings.Join([]string{meetingID, proposalID, fmt.Sprint(proposalRevision), agenda, summary, proposalStatus, meetingStatus}, "\n")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func curatorAuthorityIdempotencyKey(processKey, revisionID string) string {
	digest := sha256.Sum256([]byte(processKey + "\n" + revisionID))
	return "curator:" + hex.EncodeToString(digest[:])
}

func encodeCuratorQueueCursor(cursor curatorQueueCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeCuratorQueueCursor(encoded, workspaceID, projectID string) (curatorQueueCursor, error) {
	if encoded == "" {
		return curatorQueueCursor{}, nil
	}
	if len(encoded) > maximumCuratorCursorBytes {
		return curatorQueueCursor{}, knowledgeInvalid("curator queue cursor is invalid")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return curatorQueueCursor{}, knowledgeInvalid("curator queue cursor is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var cursor curatorQueueCursor
	if err := decoder.Decode(&cursor); err != nil {
		return curatorQueueCursor{}, knowledgeInvalid("curator queue cursor is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return curatorQueueCursor{}, knowledgeInvalid("curator queue cursor is invalid")
	}
	if cursor.Version != 1 || cursor.WorkspaceID != workspaceID || cursor.ProjectID != projectID ||
		!validCuratorRevisionID(cursor.RevisionID) {
		return curatorQueueCursor{}, knowledgeInvalid("curator queue cursor does not match the requested scope")
	}
	if parsed, parseErr := time.Parse(time.RFC3339Nano, cursor.ProposedAt); parseErr != nil || parsed.Format(time.RFC3339Nano) != cursor.ProposedAt {
		return curatorQueueCursor{}, knowledgeInvalid("curator queue cursor is invalid")
	}
	return cursor, nil
}

func validCuratorRevisionID(value string) bool {
	if len(value) != 37 || !strings.HasPrefix(value, "krev_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "krev_"))
	return err == nil && value == strings.ToLower(value)
}

func boolInteger(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
