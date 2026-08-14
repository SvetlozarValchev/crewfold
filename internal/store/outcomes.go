package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	outcomeCommitmentCreatedEvent    = "outcome.commitment_created"
	outcomeAssessmentProposedEvent   = "outcome.assessment_proposed"
	outcomeAssessmentAcceptedEvent   = "outcome.assessment_accepted"
	outcomeAssessmentRejectedEvent   = "outcome.assessment_rejected"
	outcomeAssessmentSupersededEvent = "outcome.assessment_superseded"
	ownerCheckpointCreatedEvent      = "owner_checkpoint.created"

	maximumOutcomeChildren = 256
)

type deliverableCommitmentContent struct {
	WorkspaceID        string   `json:"workspace_id"`
	ProjectID          string   `json:"project_id"`
	ObjectiveID        string   `json:"objective_id"`
	TaskID             string   `json:"task_id"`
	Key                string   `json:"key"`
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

type outcomeAssessmentContent struct {
	WorkspaceID            string                        `json:"workspace_id"`
	ProjectID              string                        `json:"project_id"`
	ObjectiveID            string                        `json:"objective_id"`
	TaskID                 string                        `json:"task_id"`
	CommitmentID           string                        `json:"commitment_id"`
	Revision               int64                         `json:"revision"`
	SupersedesAssessmentID string                        `json:"supersedes_assessment_id,omitempty"`
	Input                  domain.OutcomeAssessmentInput `json:"assessment"`
}

type resolvedOutcomeDecision struct {
	RevisionID    string
	ContentSHA256 string
	EventSequence int64
}

type resolvedOutcomeEvidence struct {
	SourceType     string
	SourceID       string
	SourceRevision int64
	SourceSHA256   string
	EventSequence  int64
	Class          string
	Effect         string
	Freshness      string
}

func (s *Store) withOutcomeMutation(ctx context.Context, operation string, fn func(*sql.Tx) error) error {
	s.outcomeMutationMu.Lock()
	defer s.outcomeMutationMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin "+operation, err)
	}
	defer tx.Rollback()
	if !s.outcomeMutationSealActive.CompareAndSwap(false, true) {
		return storageFailure("enter authenticated "+operation, errors.New("outcome mutation seal invariant failed"))
	}
	sealed := true
	defer func() {
		if sealed {
			s.outcomeMutationSealActive.Store(false)
		}
	}()
	err = fn(tx)
	// Clear the construction seal while the transaction still exclusively owns
	// the single SQLite connection. Commit cannot run triggers; raw SQL cannot
	// acquire the connection until commit or rollback releases it.
	s.outcomeMutationSealActive.Store(false)
	sealed = false
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit "+operation, err)
	}
	return nil
}

func (s *Store) CreateDeliverableCommitment(ctx context.Context, command CreateDeliverableCommitmentCommand) (DeliverableCommitmentMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.TaskID = strings.TrimSpace(command.TaskID)
	command.Key = strings.TrimSpace(command.Key)
	command.Title = strings.TrimSpace(command.Title)
	command.Description = strings.TrimSpace(command.Description)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	criteria, err := normalizeOutcomeStrings(command.AcceptanceCriteria, 1, 32, 2048, "acceptance criteria")
	if err != nil || command.WorkspaceIdentifier == "" || command.TaskID == "" || !validOutcomeText(command.Key, 128, false) ||
		!validOutcomeText(command.Title, 256, false) || !validOutcomeText(command.Description, 4096, true) {
		return DeliverableCommitmentMutationResult{}, outcomeError(CodeInvalidOutcomeCommitment, "commitment requires an exact task, bounded key/title/description, and one to 32 acceptance criteria")
	}
	command.AcceptanceCriteria = criteria
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidOutcomeCommitment); err != nil {
		return DeliverableCommitmentMutationResult{}, err
	}
	requestHash, err := hashCommand("outcome.commitment.create", map[string]any{
		"workspace": command.WorkspaceIdentifier, "task_id": command.TaskID, "key": command.Key,
		"title": command.Title, "description": command.Description, "acceptance_criteria": criteria,
	})
	if err != nil {
		return DeliverableCommitmentMutationResult{}, storageFailure("hash outcome commitment", err)
	}

	var result DeliverableCommitmentMutationResult
	err = s.withOutcomeMutation(ctx, "outcome commitment", func(tx *sql.Tx) error {
		if found, lookupErr := lookupIdempotency(ctx, tx, command.IdempotencyKey, "outcome.commitment.create", requestHash, &result); lookupErr != nil {
			return lookupErr
		} else if found {
			return nil
		}
		workspace, workspaceErr := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
		if workspaceErr != nil {
			return workspaceErr
		}
		queries := dbgen.New(tx)
		scope, scopeErr := queries.GetOutcomeCommitmentScope(ctx, dbgen.GetOutcomeCommitmentScopeParams{WorkspaceID: workspace.ID, TaskID: command.TaskID})
		if errors.Is(scopeErr, sql.ErrNoRows) || scope.ObjectiveID == nil {
			return outcomeError(CodeInvalidOutcomeCommitment, "commitment task must belong to an objective in the workspace")
		}
		if scopeErr != nil {
			return storageFailure("resolve outcome commitment scope", scopeErr)
		}
		if scope.ObjectiveStatus != domain.ObjectiveActive || (scope.TaskStatus != domain.TaskReady && scope.TaskStatus != domain.TaskAssigned) {
			return outcomeError(CodeOutcomeCommitmentConflict, "commitments must be recorded before work starts on an active objective")
		}
		content := deliverableCommitmentContent{
			WorkspaceID: workspace.ID, ProjectID: scope.ProjectID, ObjectiveID: *scope.ObjectiveID, TaskID: command.TaskID,
			Key: command.Key, Title: command.Title, Description: command.Description, AcceptanceCriteria: criteria,
		}
		contentJSON, contentSHA, contentErr := canonicalContent(content)
		if contentErr != nil {
			return storageFailure("encode outcome commitment", contentErr)
		}
		criteriaJSON, _ := json.Marshal(criteria)
		id, idErr := randomID("outcommit_")
		if idErr != nil {
			return storageFailure("generate outcome commitment id", idErr)
		}
		now := s.nowText()
		if insertErr := queries.InsertDeliverableCommitment(ctx, dbgen.InsertDeliverableCommitmentParams{
			ID: id, WorkspaceID: workspace.ID, ProjectID: scope.ProjectID, ObjectiveID: *scope.ObjectiveID,
			TaskID: command.TaskID, CommitmentKey: command.Key, Title: command.Title, Description: command.Description,
			AcceptanceCriteriaJson: string(criteriaJSON), ContentJson: string(contentJSON), ContentSha256: contentSHA, CreatedAt: now,
		}); insertErr != nil {
			if strings.Contains(insertErr.Error(), "UNIQUE") {
				return outcomeError(CodeOutcomeCommitmentConflict, "task already has a commitment with that key")
			}
			return storageFailure("insert outcome commitment", insertErr)
		}
		if hookErr := s.runMutationHook(MutationAfterOutcomeCommitment); hookErr != nil {
			return hookErr
		}
		sequence, eventErr := appendEvent(ctx, tx, workspace.ID, "deliverable_commitment", id, 1, outcomeCommitmentCreatedEvent, command.CorrelationID, now, map[string]any{
			"project_id": scope.ProjectID, "objective_id": *scope.ObjectiveID, "task_id": command.TaskID,
			"key": command.Key, "content_sha256": contentSHA,
		})
		if eventErr != nil {
			return eventErr
		}
		if receiptErr := queries.InsertOutcomeCommitmentReceipt(ctx, dbgen.InsertOutcomeCommitmentReceiptParams{CommitmentID: id, EventSequence: sequence, CreatedAt: now}); receiptErr != nil {
			return storageFailure("seal outcome commitment event", receiptErr)
		}
		if hookErr := s.runMutationHook(MutationAfterOutcomeCommitmentEvent); hookErr != nil {
			return hookErr
		}
		result = DeliverableCommitmentMutationResult{Commitment: domain.DeliverableCommitment{
			ID: id, WorkspaceID: workspace.ID, ProjectID: scope.ProjectID, ObjectiveID: *scope.ObjectiveID, TaskID: command.TaskID,
			Key: command.Key, Title: command.Title, Description: command.Description, AcceptanceCriteria: criteria,
			ContentSHA256: contentSHA, CreatedAt: now, CreatedBy: localOwnerActorID,
		}, EventSequence: sequence}
		if recordErr := recordIdempotency(ctx, tx, command.IdempotencyKey, "outcome.commitment.create", requestHash, result, now); recordErr != nil {
			return recordErr
		}
		if hookErr := s.runMutationHook(MutationAfterOutcomeCommitmentIdempotency); hookErr != nil {
			return hookErr
		}
		return nil
	})
	return result, err
}

func (s *Store) DeliverableCommitment(ctx context.Context, workspaceIdentifier, commitmentID string) (domain.DeliverableCommitment, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.DeliverableCommitment{}, err
	}
	return deliverableCommitment(ctx, dbgen.New(s.db), workspace.ID, strings.TrimSpace(commitmentID))
}

func (s *Store) DeliverableCommitments(ctx context.Context, query ListDeliverableCommitmentsQuery) ([]domain.DeliverableCommitment, error) {
	query.WorkspaceIdentifier = strings.TrimSpace(query.WorkspaceIdentifier)
	query.ProjectIdentifier = strings.TrimSpace(query.ProjectIdentifier)
	query.ObjectiveID = strings.TrimSpace(query.ObjectiveID)
	query.TaskID = strings.TrimSpace(query.TaskID)
	if query.Limit == 0 {
		query.Limit = 100
	}
	if query.WorkspaceIdentifier == "" || query.Limit < 1 || query.Limit > 100 {
		return nil, outcomeError(CodeInvalidOutcomeCommitment, "commitment list requires a workspace and limit from one to 100")
	}
	workspace, err := s.Workspace(ctx, query.WorkspaceIdentifier)
	if err != nil {
		return nil, err
	}
	projectID := ""
	if query.ProjectIdentifier != "" {
		project, projectErr := queryProject(ctx, s.db, workspace.ID, query.ProjectIdentifier)
		if projectErr != nil {
			return nil, projectErr
		}
		projectID = project.ID
	}
	rows, err := dbgen.New(s.db).ListDeliverableCommitments(ctx, dbgen.ListDeliverableCommitmentsParams{
		WorkspaceID: workspace.ID, ProjectID: projectID, ObjectiveID: query.ObjectiveID, TaskID: query.TaskID, ResultLimit: int64(query.Limit),
	})
	if err != nil {
		return nil, storageFailure("list outcome commitments", err)
	}
	result := make([]domain.DeliverableCommitment, 0, len(rows))
	for _, row := range rows {
		value, convertErr := deliverableCommitment(ctx, dbgen.New(s.db), workspace.ID, row.ID)
		if convertErr != nil {
			return nil, convertErr
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *Store) ProposeOutcomeAssessment(ctx context.Context, command ProposeOutcomeAssessmentCommand) (OutcomeAssessmentMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.TaskID = strings.TrimSpace(command.TaskID)
	command.CommitmentID = strings.TrimSpace(command.CommitmentID)
	command.SupersedesAssessmentID = strings.TrimSpace(command.SupersedesAssessmentID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.TaskID == "" || command.CommitmentID == "" {
		return OutcomeAssessmentMutationResult{}, outcomeError(CodeInvalidOutcomeAssessment, "assessment proposal requires exact workspace, task, and commitment")
	}
	normalized, err := normalizeOutcomeAssessmentInput(command.Input)
	if err != nil {
		return OutcomeAssessmentMutationResult{}, err
	}
	command.Input = normalized
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidOutcomeAssessment); err != nil {
		return OutcomeAssessmentMutationResult{}, err
	}
	requestHash, err := hashCommand("outcome.assessment.propose", map[string]any{
		"workspace": command.WorkspaceIdentifier, "task_id": command.TaskID, "commitment_id": command.CommitmentID,
		"supersedes_assessment_id": command.SupersedesAssessmentID, "assessment": command.Input,
	})
	if err != nil {
		return OutcomeAssessmentMutationResult{}, storageFailure("hash outcome assessment", err)
	}

	var result OutcomeAssessmentMutationResult
	err = s.withOutcomeMutation(ctx, "outcome assessment proposal", func(tx *sql.Tx) error {
		if found, lookupErr := lookupIdempotency(ctx, tx, command.IdempotencyKey, "outcome.assessment.propose", requestHash, &result); lookupErr != nil {
			return lookupErr
		} else if found {
			return nil
		}
		workspace, workspaceErr := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
		if workspaceErr != nil {
			return workspaceErr
		}
		queries := dbgen.New(tx)
		commitment, commitmentErr := deliverableCommitment(ctx, queries, workspace.ID, command.CommitmentID)
		if commitmentErr != nil {
			return commitmentErr
		}
		if commitment.TaskID != command.TaskID {
			return outcomeError(CodeInvalidOutcomeAssessment, "assessment task differs from its commitment")
		}
		currentAccepted, acceptedErr := queries.GetCurrentAcceptedOutcomeAssessmentID(ctx, commitment.ID)
		if acceptedErr != nil && !errors.Is(acceptedErr, sql.ErrNoRows) {
			return storageFailure("read current accepted assessment", acceptedErr)
		}
		if (currentAccepted == "" && command.SupersedesAssessmentID != "") || (currentAccepted != "" && command.SupersedesAssessmentID != currentAccepted) {
			return outcomeError(CodeOutcomeAssessmentConflict, "assessment successor must cite the exact current accepted assessment")
		}
		var proposedCount int64
		ids, listErr := queries.ListOutcomeAssessmentIDs(ctx, dbgen.ListOutcomeAssessmentIDsParams{WorkspaceID: workspace.ID, CommitmentID: commitment.ID, ReviewState: domain.OutcomeAssessmentProposed, ResultLimit: 1})
		if listErr != nil {
			return storageFailure("check pending outcome assessment", listErr)
		}
		proposedCount = int64(len(ids))
		if proposedCount != 0 {
			return outcomeError(CodeOutcomeAssessmentConflict, "commitment already has a proposed assessment")
		}
		revision, revisionErr := queries.NextOutcomeAssessmentRevision(ctx, commitment.ID)
		if revisionErr != nil {
			return storageFailure("allocate outcome assessment revision", revisionErr)
		}
		decisions, decisionErr := resolveOutcomeDecisions(ctx, queries, commitment, normalized.DecisionRevisionIDs)
		if decisionErr != nil {
			return decisionErr
		}
		evidence, evidenceErr := resolveOutcomeEvidence(ctx, queries, commitment, normalized.Evidence)
		if evidenceErr != nil {
			return evidenceErr
		}
		followups, followupErr := resolveOutcomeFollowUps(ctx, queries, commitment, normalized.FollowUpTaskIDs)
		if followupErr != nil {
			return followupErr
		}
		deviations, deviationErr := resolveOutcomeDeviations(ctx, queries, commitment, normalized.Deviations)
		if deviationErr != nil {
			return deviationErr
		}
		id, idErr := randomID("outassess_")
		if idErr != nil {
			return storageFailure("generate outcome assessment id", idErr)
		}
		content := outcomeAssessmentContent{
			WorkspaceID: workspace.ID, ProjectID: commitment.ProjectID, ObjectiveID: commitment.ObjectiveID, TaskID: commitment.TaskID,
			CommitmentID: commitment.ID, Revision: revision, SupersedesAssessmentID: command.SupersedesAssessmentID, Input: normalized,
		}
		contentJSON, contentSHA, contentErr := canonicalContent(content)
		if contentErr != nil {
			return storageFailure("encode outcome assessment", contentErr)
		}
		deliveredJSON, _ := json.Marshal(normalized.DeliveredScope)
		unmetJSON, _ := json.Marshal(normalized.UnmetScope)
		now := s.nowText()
		if insertErr := queries.InsertOutcomeAssessment(ctx, dbgen.InsertOutcomeAssessmentParams{
			ID: id, WorkspaceID: workspace.ID, ProjectID: commitment.ProjectID, ObjectiveID: commitment.ObjectiveID,
			TaskID: commitment.TaskID, CommitmentID: commitment.ID, Revision: revision, Conclusion: normalized.Conclusion,
			DeliveredScopeJson: string(deliveredJSON), UnmetScopeJson: string(unmetJSON), ContentJson: string(contentJSON), ContentSha256: contentSHA,
			SupersedesAssessmentID: command.SupersedesAssessmentID, ProposedAt: now,
		}); insertErr != nil {
			return storageFailure("insert outcome assessment", insertErr)
		}
		if hookErr := s.runMutationHook(MutationAfterOutcomeAssessment); hookErr != nil {
			return hookErr
		}
		if childErr := insertOutcomeAssessmentChildren(ctx, queries, id, normalized, decisions, evidence, followups, deviations); childErr != nil {
			return childErr
		}
		if hookErr := s.runMutationHook(MutationAfterOutcomeAssessmentChildren); hookErr != nil {
			return hookErr
		}
		sequence, eventErr := appendEvent(ctx, tx, workspace.ID, "outcome_assessment", id, 1, outcomeAssessmentProposedEvent, command.CorrelationID, now, map[string]any{
			"commitment_id": commitment.ID, "task_id": commitment.TaskID, "assessment_revision": revision,
			"conclusion": normalized.Conclusion, "supersedes_assessment_id": command.SupersedesAssessmentID, "content_sha256": contentSHA,
		})
		if eventErr != nil {
			return eventErr
		}
		childCount := len(decisions) + len(evidence) + len(normalized.Effects) + len(deviations) + len(normalized.Risks) + len(normalized.Unknowns) + len(followups) + len(normalized.OwnerAttention)
		if receiptErr := queries.InsertOutcomeAssessmentSubmission(ctx, dbgen.InsertOutcomeAssessmentSubmissionParams{AssessmentID: id, EventSequence: sequence, ChildCount: int64(childCount), SubmittedAt: now}); receiptErr != nil {
			return storageFailure("seal outcome assessment proposal", receiptErr)
		}
		if hookErr := s.runMutationHook(MutationAfterOutcomeAssessmentEvent); hookErr != nil {
			return hookErr
		}
		detail, detailErr := outcomeAssessmentDetail(ctx, queries, workspace.ID, id, now)
		if detailErr != nil {
			return detailErr
		}
		result = OutcomeAssessmentMutationResult{Detail: detail, EventSequence: sequence}
		if recordErr := recordIdempotency(ctx, tx, command.IdempotencyKey, "outcome.assessment.propose", requestHash, result, now); recordErr != nil {
			return recordErr
		}
		if hookErr := s.runMutationHook(MutationAfterOutcomeAssessmentIdempotency); hookErr != nil {
			return hookErr
		}
		return nil
	})
	return result, err
}

func (s *Store) OutcomeAssessment(ctx context.Context, workspaceIdentifier, assessmentID string) (domain.OutcomeAssessmentDetail, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.OutcomeAssessmentDetail{}, err
	}
	return outcomeAssessmentDetail(ctx, dbgen.New(s.db), workspace.ID, strings.TrimSpace(assessmentID), s.nowText())
}

func (s *Store) OutcomeAssessments(ctx context.Context, query ListOutcomeAssessmentsQuery) ([]domain.OutcomeAssessmentDetail, error) {
	query.WorkspaceIdentifier = strings.TrimSpace(query.WorkspaceIdentifier)
	query.ProjectIdentifier = strings.TrimSpace(query.ProjectIdentifier)
	query.ObjectiveID = strings.TrimSpace(query.ObjectiveID)
	query.TaskID = strings.TrimSpace(query.TaskID)
	query.CommitmentID = strings.TrimSpace(query.CommitmentID)
	query.ReviewState = strings.TrimSpace(query.ReviewState)
	query.Conclusion = strings.TrimSpace(query.Conclusion)
	if query.Limit == 0 {
		query.Limit = 100
	}
	if query.WorkspaceIdentifier == "" || query.Limit < 1 || query.Limit > 100 ||
		(query.ReviewState != "" && !validOutcomeReviewState(query.ReviewState)) || (query.Conclusion != "" && !validOutcomeConclusion(query.Conclusion)) {
		return nil, outcomeError(CodeInvalidOutcomeAssessment, "assessment list filters or limit are invalid")
	}
	workspace, err := s.Workspace(ctx, query.WorkspaceIdentifier)
	if err != nil {
		return nil, err
	}
	projectID := ""
	if query.ProjectIdentifier != "" {
		project, projectErr := queryProject(ctx, s.db, workspace.ID, query.ProjectIdentifier)
		if projectErr != nil {
			return nil, projectErr
		}
		projectID = project.ID
	}
	queries := dbgen.New(s.db)
	ids, err := queries.ListOutcomeAssessmentIDs(ctx, dbgen.ListOutcomeAssessmentIDsParams{
		WorkspaceID: workspace.ID, ProjectID: projectID, ObjectiveID: query.ObjectiveID, TaskID: query.TaskID,
		CommitmentID: query.CommitmentID, ReviewState: query.ReviewState, Conclusion: query.Conclusion, ResultLimit: int64(query.Limit),
	})
	if err != nil {
		return nil, storageFailure("list outcome assessments", err)
	}
	result := make([]domain.OutcomeAssessmentDetail, 0, len(ids))
	evaluatedAt := s.nowText()
	for _, id := range ids {
		detail, detailErr := outcomeAssessmentDetail(ctx, queries, workspace.ID, id, evaluatedAt)
		if detailErr != nil {
			return nil, detailErr
		}
		result = append(result, detail)
	}
	return result, nil
}

func (s *Store) AcceptOutcomeAssessment(ctx context.Context, command DecideOutcomeAssessmentCommand) (OutcomeAssessmentMutationResult, error) {
	return s.decideOutcomeAssessment(ctx, command, true)
}

func (s *Store) RejectOutcomeAssessment(ctx context.Context, command DecideOutcomeAssessmentCommand) (OutcomeAssessmentMutationResult, error) {
	return s.decideOutcomeAssessment(ctx, command, false)
}

func (s *Store) decideOutcomeAssessment(ctx context.Context, command DecideOutcomeAssessmentCommand, accept bool) (OutcomeAssessmentMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.AssessmentID = strings.TrimSpace(command.AssessmentID)
	command.DecisionNote = strings.TrimSpace(command.DecisionNote)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.AssessmentID == "" || command.ExpectedStateRevision < 1 || !validOutcomeText(command.DecisionNote, 4096, true) {
		return OutcomeAssessmentMutationResult{}, outcomeError(CodeInvalidOutcomeAssessment, "assessment decision requires an exact proposed state revision and bounded note")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidOutcomeAssessment); err != nil {
		return OutcomeAssessmentMutationResult{}, err
	}
	operation := "outcome.assessment.reject"
	decision := domain.OutcomeAssessmentRejected
	eventType := outcomeAssessmentRejectedEvent
	if accept {
		operation, decision, eventType = "outcome.assessment.accept", domain.OutcomeAssessmentAccepted, outcomeAssessmentAcceptedEvent
	}
	requestHash, err := hashCommand(operation, map[string]any{
		"workspace": command.WorkspaceIdentifier, "assessment_id": command.AssessmentID,
		"expected_state_revision": command.ExpectedStateRevision, "decision_note": command.DecisionNote,
	})
	if err != nil {
		return OutcomeAssessmentMutationResult{}, storageFailure("hash outcome assessment decision", err)
	}
	var result OutcomeAssessmentMutationResult
	err = s.withOutcomeMutation(ctx, "outcome assessment decision", func(tx *sql.Tx) error {
		if found, lookupErr := lookupIdempotency(ctx, tx, command.IdempotencyKey, operation, requestHash, &result); lookupErr != nil {
			return lookupErr
		} else if found {
			return nil
		}
		workspace, workspaceErr := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
		if workspaceErr != nil {
			return workspaceErr
		}
		queries := dbgen.New(tx)
		current, currentErr := outcomeAssessmentDetail(ctx, queries, workspace.ID, command.AssessmentID, s.nowText())
		if currentErr != nil {
			return currentErr
		}
		if current.Assessment.ReviewState != domain.OutcomeAssessmentProposed || current.Assessment.StateRevision != command.ExpectedStateRevision {
			return outcomeError(CodeOutcomeAssessmentConflict, "assessment is no longer the expected proposed revision")
		}
		if accept && (current.Assessment.Conclusion == domain.OutcomeAchieved || current.Assessment.Conclusion == domain.OutcomePartial) {
			supports := false
			for _, evidence := range current.Evidence {
				if evidence.Effect == domain.CheckEvidenceSupports {
					supports = true
					break
				}
			}
			if !supports {
				return outcomeError(CodeOutcomeAssessmentConflict, "achieved or partial assessment requires supporting evidence")
			}
		}
		now := s.nowText()
		var supersededID string
		var supersededSequence int64
		if accept && current.Assessment.SupersedesAssessmentID != "" {
			supersededID = current.Assessment.SupersedesAssessmentID
			changed, supersedeErr := queries.SupersedeOutcomeAssessmentProjection(ctx, dbgen.SupersedeOutcomeAssessmentProjectionParams{WorkspaceID: workspace.ID, ID: supersededID})
			if supersedeErr != nil || changed != 1 {
				return outcomeError(CodeOutcomeAssessmentConflict, "current accepted assessment changed before successor acceptance")
			}
			prior, priorErr := queries.GetOutcomeAssessment(ctx, dbgen.GetOutcomeAssessmentParams{WorkspaceID: workspace.ID, ID: supersededID})
			if priorErr != nil {
				return storageFailure("read superseded outcome assessment", priorErr)
			}
			supersededSequence, priorErr = appendEvent(ctx, tx, workspace.ID, "outcome_assessment", supersededID, prior.StateRevision, outcomeAssessmentSupersededEvent, command.CorrelationID, now, map[string]any{
				"successor_assessment_id": current.Assessment.ID, "commitment_id": current.Assessment.CommitmentID,
			})
			if priorErr != nil {
				return priorErr
			}
		}
		var changed int64
		if accept {
			changed, err = queries.AcceptOutcomeAssessmentProjection(ctx, dbgen.AcceptOutcomeAssessmentProjectionParams{
				DecidedAt: &now, DecisionNote: command.DecisionNote, WorkspaceID: workspace.ID, ID: current.Assessment.ID,
				ExpectedStateRevision: command.ExpectedStateRevision,
			})
		} else {
			changed, err = queries.RejectOutcomeAssessmentProjection(ctx, dbgen.RejectOutcomeAssessmentProjectionParams{
				DecidedAt: &now, DecisionNote: command.DecisionNote, WorkspaceID: workspace.ID, ID: current.Assessment.ID,
				ExpectedStateRevision: command.ExpectedStateRevision,
			})
		}
		if err != nil || changed != 1 {
			return outcomeError(CodeOutcomeAssessmentConflict, "assessment decision lost its proposed state")
		}
		if hookErr := s.runMutationHook(MutationAfterOutcomeGovernanceDecision); hookErr != nil {
			return hookErr
		}
		sequence, eventErr := appendEvent(ctx, tx, workspace.ID, "outcome_assessment", current.Assessment.ID, command.ExpectedStateRevision+1, eventType, command.CorrelationID, now, map[string]any{
			"commitment_id": current.Assessment.CommitmentID, "assessment_revision": current.Assessment.Revision,
			"conclusion": current.Assessment.Conclusion, "decision_note": command.DecisionNote,
		})
		if eventErr != nil {
			return eventErr
		}
		if accept {
			basisSHA, hashErr := hashCommand("outcome.policy_acceptance", map[string]any{
				"assessment_id": current.Assessment.ID, "content_sha256": current.Assessment.ContentSHA256,
				"event_sequence": sequence, "state_revision": command.ExpectedStateRevision + 1,
			})
			if hashErr != nil {
				return storageFailure("hash outcome acceptance basis", hashErr)
			}
			if basisErr := queries.InsertOutcomeAssessmentAcceptanceBasis(ctx, dbgen.InsertOutcomeAssessmentAcceptanceBasisParams{AssessmentID: current.Assessment.ID, EventSequence: sequence, SourceSha256: basisSHA, CreatedAt: now}); basisErr != nil {
				return storageFailure("insert outcome acceptance basis", basisErr)
			}
		}
		if governanceErr := queries.InsertOutcomeAssessmentGovernance(ctx, dbgen.InsertOutcomeAssessmentGovernanceParams{
			AssessmentID: current.Assessment.ID, Decision: decision, DecisionEventSequence: sequence,
			SupersededAssessmentID: supersededID, SupersededEventSequence: supersededSequence, DecidedAt: now,
		}); governanceErr != nil {
			return storageFailure("insert outcome assessment governance", governanceErr)
		}
		if hookErr := s.runMutationHook(MutationAfterOutcomeGovernanceEvents); hookErr != nil {
			return hookErr
		}
		detail, detailErr := outcomeAssessmentDetail(ctx, queries, workspace.ID, current.Assessment.ID, now)
		if detailErr != nil {
			return detailErr
		}
		result = OutcomeAssessmentMutationResult{Detail: detail, EventSequence: sequence}
		if recordErr := recordIdempotency(ctx, tx, command.IdempotencyKey, operation, requestHash, result, now); recordErr != nil {
			return recordErr
		}
		if hookErr := s.runMutationHook(MutationAfterOutcomeGovernanceIdempotency); hookErr != nil {
			return hookErr
		}
		return nil
	})
	return result, err
}

func deliverableCommitment(ctx context.Context, queries *dbgen.Queries, workspaceID, commitmentID string) (domain.DeliverableCommitment, error) {
	if commitmentID == "" {
		return domain.DeliverableCommitment{}, outcomeError(CodeInvalidOutcomeCommitment, "commitment id is required")
	}
	row, err := queries.GetDeliverableCommitment(ctx, dbgen.GetDeliverableCommitmentParams{WorkspaceID: workspaceID, ID: commitmentID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DeliverableCommitment{}, outcomeError(CodeOutcomeCommitmentNotFound, fmt.Sprintf("outcome commitment %q was not found", commitmentID))
	}
	if err != nil {
		return domain.DeliverableCommitment{}, storageFailure("read outcome commitment", err)
	}
	value, err := domainDeliverableCommitment(row)
	if err != nil {
		return domain.DeliverableCommitment{}, err
	}
	scope, err := queries.GetOutcomeCommitmentScope(ctx, dbgen.GetOutcomeCommitmentScopeParams{WorkspaceID: workspaceID, TaskID: value.TaskID})
	if err != nil || scope.ObjectiveID == nil || scope.WorkspaceID != value.WorkspaceID || scope.ProjectID != value.ProjectID || *scope.ObjectiveID != value.ObjectiveID {
		return domain.DeliverableCommitment{}, storageFailure("validate outcome commitment scope", errors.New("commitment scope differs from current task/objective ownership"))
	}
	receipt, err := queries.GetOutcomeCommitmentReceipt(ctx, value.ID)
	if err != nil || receipt.CreatedAt != value.CreatedAt {
		return domain.DeliverableCommitment{}, storageFailure("validate outcome commitment receipt", errors.New("commitment receipt is missing or inconsistent"))
	}
	event, err := queries.GetOutcomeJournalEvent(ctx, receipt.EventSequence)
	expectedData, _ := json.Marshal(map[string]any{
		"project_id": value.ProjectID, "objective_id": value.ObjectiveID, "task_id": value.TaskID,
		"key": value.Key, "content_sha256": value.ContentSHA256,
	})
	if err != nil || event.WorkspaceID != value.WorkspaceID || event.EntityType != "deliverable_commitment" || event.EntityID != value.ID || event.EntityRevision != 1 || event.Type != outcomeCommitmentCreatedEvent || event.OccurredAt != value.CreatedAt || event.RecordedAt != value.CreatedAt || event.ActorID != localOwnerActorID || event.ActorType != localActorType || event.DataJson != string(expectedData) {
		return domain.DeliverableCommitment{}, storageFailure("validate outcome commitment event", errors.New("commitment journal fact differs from its receipt"))
	}
	return value, nil
}

func domainDeliverableCommitment(row dbgen.DeliverableCommitment) (domain.DeliverableCommitment, error) {
	var criteria []string
	if err := json.Unmarshal([]byte(row.AcceptanceCriteriaJson), &criteria); err != nil {
		return domain.DeliverableCommitment{}, storageFailure("decode outcome commitment criteria", err)
	}
	value := domain.DeliverableCommitment{
		ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, ObjectiveID: row.ObjectiveID,
		TaskID: row.TaskID, Key: row.CommitmentKey, Title: row.Title, Description: row.Description,
		AcceptanceCriteria: criteria, ContentSHA256: row.ContentSha256, CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy,
	}
	content := deliverableCommitmentContent{
		WorkspaceID: value.WorkspaceID, ProjectID: value.ProjectID, ObjectiveID: value.ObjectiveID, TaskID: value.TaskID,
		Key: value.Key, Title: value.Title, Description: value.Description, AcceptanceCriteria: value.AcceptanceCriteria,
	}
	contentJSON, contentSHA, err := canonicalContent(content)
	criteriaNormalized, normalizeErr := normalizeOutcomeStrings(criteria, 1, 32, 2048, "acceptance criteria")
	if err != nil || normalizeErr != nil || !equalStrings(criteriaNormalized, criteria) || contentSHA != value.ContentSHA256 || string(contentJSON) != row.ContentJson || !canonicalTimestamp(value.CreatedAt) || value.CreatedBy != localOwnerActorID {
		return domain.DeliverableCommitment{}, storageFailure("validate outcome commitment", errors.New("commitment content differs from its canonical seal"))
	}
	return value, nil
}

func outcomeAssessmentDetail(ctx context.Context, queries *dbgen.Queries, workspaceID, assessmentID, evaluatedAt string) (domain.OutcomeAssessmentDetail, error) {
	row, err := queries.GetOutcomeAssessment(ctx, dbgen.GetOutcomeAssessmentParams{WorkspaceID: workspaceID, ID: assessmentID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OutcomeAssessmentDetail{}, outcomeError(CodeOutcomeAssessmentNotFound, fmt.Sprintf("outcome assessment %q was not found", assessmentID))
	}
	if err != nil {
		return domain.OutcomeAssessmentDetail{}, storageFailure("read outcome assessment", err)
	}
	commitment, err := deliverableCommitment(ctx, queries, workspaceID, row.CommitmentID)
	if err != nil {
		return domain.OutcomeAssessmentDetail{}, err
	}
	if row.WorkspaceID != commitment.WorkspaceID || row.ProjectID != commitment.ProjectID || row.ObjectiveID != commitment.ObjectiveID || row.TaskID != commitment.TaskID {
		return domain.OutcomeAssessmentDetail{}, storageFailure("validate outcome assessment scope", errors.New("assessment scope differs from its commitment"))
	}
	detail := domain.OutcomeAssessmentDetail{
		Assessment: domain.OutcomeAssessment{
			ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, ObjectiveID: row.ObjectiveID, TaskID: row.TaskID,
			CommitmentID: row.CommitmentID, Revision: row.Revision, StateRevision: row.StateRevision, ReviewState: row.ReviewState,
			Conclusion: row.Conclusion, ContentSHA256: row.ContentSha256, SupersedesAssessmentID: stringValue(row.SupersedesAssessmentID),
			ProposedAt: row.ProposedAt, ProposedBy: row.ProposedBy, DecidedAt: stringValue(row.DecidedAt),
			DecidedBy: stringValue(row.DecidedBy), DecisionNote: stringValue(row.DecisionNote),
		},
		Commitment: commitment, DeliveredScope: []string{}, UnmetScope: []string{}, Decisions: []domain.OutcomeDecisionReference{},
		Evidence: []domain.OutcomeEvidenceReference{}, Effects: []domain.OutcomeEffect{}, Deviations: []domain.OutcomeDeviation{},
		Risks: []domain.OutcomeRisk{}, Unknowns: []domain.OutcomeUnknownRecord{}, FollowUpTasks: []domain.OutcomeFollowUpTask{},
		OwnerAttention: []domain.OutcomeOwnerAttention{},
	}
	if err := json.Unmarshal([]byte(row.DeliveredScopeJson), &detail.DeliveredScope); err != nil {
		return domain.OutcomeAssessmentDetail{}, storageFailure("decode delivered outcome scope", err)
	}
	if err := json.Unmarshal([]byte(row.UnmetScopeJson), &detail.UnmetScope); err != nil {
		return domain.OutcomeAssessmentDetail{}, storageFailure("decode unmet outcome scope", err)
	}
	decisionRows, err := queries.ListOutcomeAssessmentDecisionRefs(ctx, assessmentID)
	if err != nil {
		return domain.OutcomeAssessmentDetail{}, storageFailure("list outcome decisions", err)
	}
	for index, ref := range decisionRows {
		if ref.Ordinal != int64(index) {
			return domain.OutcomeAssessmentDetail{}, storageFailure("validate outcome decision ordinals", errors.New("outcome decision ordinals are not contiguous"))
		}
		current, disputed := false, false
		decision, decisionErr := queries.GetAcceptedDecisionForOutcome(ctx, dbgen.GetAcceptedDecisionForOutcomeParams{WorkspaceID: workspaceID, ProjectID: row.ProjectID, TaskID: &row.TaskID, RevisionID: ref.RevisionID})
		if decisionErr == nil {
			current = decision.ContentHash == ref.ContentSha256 && decision.CurrencyStatus == domain.KnowledgeCurrencyCurrent &&
				(decision.FreshnessPolicy != domain.KnowledgeFreshExpiresAt || decision.FreshUntil != nil && timestampAfter(*decision.FreshUntil, evaluatedAt))
			disputed, decisionErr = queries.OutcomeDecisionHasOpenContradiction(ctx, dbgen.OutcomeDecisionHasOpenContradictionParams{WorkspaceID: workspaceID, RevisionID: ref.RevisionID})
			if decisionErr != nil {
				return domain.OutcomeAssessmentDetail{}, storageFailure("derive outcome decision dispute state", decisionErr)
			}
		} else if !errors.Is(decisionErr, sql.ErrNoRows) {
			return domain.OutcomeAssessmentDetail{}, storageFailure("read current outcome decision", decisionErr)
		}
		detail.Decisions = append(detail.Decisions, domain.OutcomeDecisionReference{RevisionID: ref.RevisionID, ContentSHA256: ref.ContentSha256, EventSequence: ref.EventSequence, Current: current, Disputed: disputed})
	}
	evidenceRows, err := queries.ListOutcomeAssessmentEvidenceRefs(ctx, assessmentID)
	if err != nil {
		return domain.OutcomeAssessmentDetail{}, storageFailure("list outcome evidence", err)
	}
	for index, ref := range evidenceRows {
		if ref.Ordinal != int64(index) {
			return domain.OutcomeAssessmentDetail{}, storageFailure("validate outcome evidence ordinals", errors.New("outcome evidence ordinals are not contiguous"))
		}
		value, evidenceErr := currentOutcomeEvidence(ctx, queries, workspaceID, row.TaskID, ref)
		if evidenceErr != nil {
			return domain.OutcomeAssessmentDetail{}, evidenceErr
		}
		detail.Evidence = append(detail.Evidence, value)
	}
	effectRows, err := queries.ListOutcomeAssessmentEffects(ctx, assessmentID)
	if err != nil {
		return domain.OutcomeAssessmentDetail{}, storageFailure("list outcome effects", err)
	}
	for _, value := range effectRows {
		detail.Effects = append(detail.Effects, domain.OutcomeEffect{Ordinal: value.Ordinal, Kind: value.Kind, Direction: value.Direction, Summary: value.Summary})
	}
	deviationRows, err := queries.ListOutcomeAssessmentDeviations(ctx, assessmentID)
	if err != nil {
		return domain.OutcomeAssessmentDetail{}, storageFailure("list outcome deviations", err)
	}
	for _, value := range deviationRows {
		detail.Deviations = append(detail.Deviations, domain.OutcomeDeviation{Ordinal: value.Ordinal, Kind: value.Kind, Summary: value.Summary, RelatedTaskID: stringValue(value.RelatedTaskID), RelatedTaskRevision: int64Value(value.RelatedTaskRevision)})
	}
	riskRows, err := queries.ListOutcomeAssessmentRisks(ctx, assessmentID)
	if err != nil {
		return domain.OutcomeAssessmentDetail{}, storageFailure("list outcome risks", err)
	}
	for _, value := range riskRows {
		detail.Risks = append(detail.Risks, domain.OutcomeRisk{Ordinal: value.Ordinal, Severity: value.Severity, Summary: value.Summary, Mitigation: value.Mitigation})
	}
	unknownRows, err := queries.ListOutcomeAssessmentUnknowns(ctx, assessmentID)
	if err != nil {
		return domain.OutcomeAssessmentDetail{}, storageFailure("list outcome unknowns", err)
	}
	for _, value := range unknownRows {
		detail.Unknowns = append(detail.Unknowns, domain.OutcomeUnknownRecord{Ordinal: value.Ordinal, Summary: value.Summary})
	}
	followupRows, err := queries.ListOutcomeAssessmentFollowUpTasks(ctx, assessmentID)
	if err != nil {
		return domain.OutcomeAssessmentDetail{}, storageFailure("list outcome follow-up tasks", err)
	}
	for _, value := range followupRows {
		detail.FollowUpTasks = append(detail.FollowUpTasks, domain.OutcomeFollowUpTask{Ordinal: value.Ordinal, TaskID: value.TaskID, TaskRevision: value.TaskRevision, EventSequence: value.EventSequence})
	}
	attentionRows, err := queries.ListOutcomeAssessmentOwnerAttention(ctx, assessmentID)
	if err != nil {
		return domain.OutcomeAssessmentDetail{}, storageFailure("list outcome owner attention", err)
	}
	for _, value := range attentionRows {
		detail.OwnerAttention = append(detail.OwnerAttention, domain.OutcomeOwnerAttention{Ordinal: value.Ordinal, Urgency: value.Urgency, Action: value.Action, Reason: value.Reason})
	}
	if err := validateOutcomeAssessmentRead(ctx, queries, row, detail); err != nil {
		return domain.OutcomeAssessmentDetail{}, err
	}
	return detail, nil
}

func resolveOutcomeDecisions(ctx context.Context, queries *dbgen.Queries, commitment domain.DeliverableCommitment, ids []string) ([]resolvedOutcomeDecision, error) {
	result := make([]resolvedOutcomeDecision, 0, len(ids))
	for _, id := range ids {
		row, err := queries.GetAcceptedDecisionForOutcome(ctx, dbgen.GetAcceptedDecisionForOutcomeParams{WorkspaceID: commitment.WorkspaceID, ProjectID: commitment.ProjectID, TaskID: &commitment.TaskID, RevisionID: id})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, outcomeError(CodeInvalidOutcomeAssessment, fmt.Sprintf("decision revision %s is not an accepted decision in the commitment project", id))
		}
		if err != nil {
			return nil, storageFailure("resolve outcome decision", err)
		}
		result = append(result, resolvedOutcomeDecision{RevisionID: id, ContentSHA256: row.ContentHash, EventSequence: row.EventSequence})
	}
	return result, nil
}

func resolveOutcomeEvidence(ctx context.Context, queries *dbgen.Queries, commitment domain.DeliverableCommitment, inputs []domain.OutcomeEvidenceInput) ([]resolvedOutcomeEvidence, error) {
	result := make([]resolvedOutcomeEvidence, 0, len(inputs))
	for _, input := range inputs {
		switch input.SourceType {
		case domain.OutcomeEvidenceHandoff:
			row, err := queries.GetOutcomeHandoffEvidence(ctx, dbgen.GetOutcomeHandoffEvidenceParams{WorkspaceID: commitment.WorkspaceID, HandoffID: input.SourceID})
			if errors.Is(err, sql.ErrNoRows) {
				return nil, outcomeError(CodeInvalidOutcomeAssessment, fmt.Sprintf("handoff evidence %s was not found", input.SourceID))
			}
			if err != nil {
				return nil, storageFailure("resolve handoff outcome evidence", err)
			}
			class := domain.EvidenceAgentSelfReport
			if row.TaskID != commitment.TaskID {
				independent, independentErr := queries.OutcomeHandoffIsIndependentReview(ctx, dbgen.OutcomeHandoffIsIndependentReviewParams{HandoffID: row.ID, WorkspaceID: commitment.WorkspaceID, AssessedTaskID: commitment.TaskID})
				if independentErr != nil {
					return nil, storageFailure("resolve independent review provenance", independentErr)
				}
				if !independent {
					return nil, outcomeError(CodeInvalidOutcomeAssessment, fmt.Sprintf("handoff %s is unrelated to the assessed task", input.SourceID))
				}
				class = domain.EvidenceIndependentReview
			}
			_, hash, hashErr := canonicalContent(map[string]any{
				"id": row.ID, "run_id": row.RunID, "task_id": row.TaskID, "summary": row.Summary,
				"evidence_json": json.RawMessage(row.EvidenceJson), "created_at": row.CreatedAt,
			})
			if hashErr != nil {
				return nil, storageFailure("hash handoff outcome evidence", hashErr)
			}
			result = append(result, resolvedOutcomeEvidence{SourceType: input.SourceType, SourceID: row.ID, SourceRevision: row.SourceRevision, SourceSHA256: hash, EventSequence: row.EventSequence, Class: class, Effect: domain.CheckEvidenceSupports, Freshness: domain.OutcomeEvidenceFresh})
		case domain.OutcomeEvidenceCheckRequirementEvidence:
			row, err := queries.GetOutcomeCheckEvidence(ctx, dbgen.GetOutcomeCheckEvidenceParams{WorkspaceID: commitment.WorkspaceID, EvidenceID: input.SourceID})
			if errors.Is(err, sql.ErrNoRows) || err == nil && (row.ProjectID != commitment.ProjectID || row.TaskID != commitment.TaskID) {
				return nil, outcomeError(CodeInvalidOutcomeAssessment, fmt.Sprintf("check evidence %s is outside the assessed task", input.SourceID))
			}
			if err != nil {
				return nil, storageFailure("resolve mechanical outcome evidence", err)
			}
			_, hash, hashErr := canonicalContent(map[string]any{
				"id": row.ID, "requirement_id": row.RequirementID, "requirement_revision": row.RequirementRevision,
				"check_result_id": row.CheckResultID, "freshness_revision": row.FreshnessRevision,
				"class": row.Class, "effect": row.Effect, "pinned_freshness": row.PinnedFreshness,
			})
			if hashErr != nil {
				return nil, storageFailure("hash mechanical outcome evidence", hashErr)
			}
			result = append(result, resolvedOutcomeEvidence{SourceType: input.SourceType, SourceID: row.ID, SourceRevision: row.FreshnessRevision, SourceSHA256: hash, EventSequence: row.EventSequence, Class: domain.EvidenceMechanicalCheck, Effect: row.Effect, Freshness: row.PinnedFreshness})
		default:
			return nil, outcomeError(CodeInvalidOutcomeAssessment, fmt.Sprintf("unsupported assessment evidence source type %q", input.SourceType))
		}
	}
	return result, nil
}

func resolveOutcomeFollowUps(ctx context.Context, queries *dbgen.Queries, commitment domain.DeliverableCommitment, ids []string) ([]domain.OutcomeFollowUpTask, error) {
	result := make([]domain.OutcomeFollowUpTask, 0, len(ids))
	for index, id := range ids {
		row, err := queries.GetOutcomeTaskRevision(ctx, dbgen.GetOutcomeTaskRevisionParams{WorkspaceID: commitment.WorkspaceID, TaskID: id})
		if errors.Is(err, sql.ErrNoRows) || err == nil && row.ProjectID != commitment.ProjectID {
			return nil, outcomeError(CodeInvalidOutcomeAssessment, fmt.Sprintf("follow-up task %s is outside the commitment project", id))
		}
		if err != nil {
			return nil, storageFailure("resolve outcome follow-up task", err)
		}
		result = append(result, domain.OutcomeFollowUpTask{Ordinal: int64(index), TaskID: id, TaskRevision: row.Revision, EventSequence: row.EventSequence})
	}
	return result, nil
}

func resolveOutcomeDeviations(ctx context.Context, queries *dbgen.Queries, commitment domain.DeliverableCommitment, inputs []domain.OutcomeDeviationInput) ([]domain.OutcomeDeviation, error) {
	result := make([]domain.OutcomeDeviation, 0, len(inputs))
	for index, input := range inputs {
		value := domain.OutcomeDeviation{Ordinal: int64(index), Kind: input.Kind, Summary: input.Summary, RelatedTaskID: input.RelatedTaskID}
		if input.RelatedTaskID != "" {
			row, err := queries.GetOutcomeTaskRevision(ctx, dbgen.GetOutcomeTaskRevisionParams{WorkspaceID: commitment.WorkspaceID, TaskID: input.RelatedTaskID})
			if errors.Is(err, sql.ErrNoRows) || err == nil && row.ProjectID != commitment.ProjectID {
				return nil, outcomeError(CodeInvalidOutcomeAssessment, fmt.Sprintf("deviation task %s is outside the commitment project", input.RelatedTaskID))
			}
			if err != nil {
				return nil, storageFailure("resolve outcome deviation task", err)
			}
			value.RelatedTaskRevision = row.Revision
		}
		result = append(result, value)
	}
	return result, nil
}

func insertOutcomeAssessmentChildren(ctx context.Context, queries *dbgen.Queries, assessmentID string, input domain.OutcomeAssessmentInput, decisions []resolvedOutcomeDecision, evidence []resolvedOutcomeEvidence, followups []domain.OutcomeFollowUpTask, deviations []domain.OutcomeDeviation) error {
	for index, value := range decisions {
		if err := queries.InsertOutcomeAssessmentDecisionRef(ctx, dbgen.InsertOutcomeAssessmentDecisionRefParams{AssessmentID: assessmentID, Ordinal: int64(index), RevisionID: value.RevisionID, ContentSha256: value.ContentSHA256, EventSequence: value.EventSequence}); err != nil {
			return storageFailure("insert outcome decision reference", err)
		}
	}
	for index, value := range evidence {
		if err := queries.InsertOutcomeAssessmentEvidenceRef(ctx, dbgen.InsertOutcomeAssessmentEvidenceRefParams{AssessmentID: assessmentID, Ordinal: int64(index), SourceType: value.SourceType, SourceID: value.SourceID, SourceRevision: value.SourceRevision, SourceSha256: value.SourceSHA256, EventSequence: value.EventSequence, Class: value.Class, Effect: value.Effect, PinnedFreshness: value.Freshness}); err != nil {
			return storageFailure("insert outcome evidence reference", err)
		}
	}
	for index, value := range input.Effects {
		if err := queries.InsertOutcomeAssessmentEffect(ctx, dbgen.InsertOutcomeAssessmentEffectParams{AssessmentID: assessmentID, Ordinal: int64(index), Kind: value.Kind, Direction: value.Direction, Summary: value.Summary}); err != nil {
			return storageFailure("insert outcome effect", err)
		}
	}
	for _, value := range deviations {
		if err := queries.InsertOutcomeAssessmentDeviation(ctx, dbgen.InsertOutcomeAssessmentDeviationParams{AssessmentID: assessmentID, Ordinal: value.Ordinal, Kind: value.Kind, Summary: value.Summary, RelatedTaskID: value.RelatedTaskID, RelatedTaskRevision: value.RelatedTaskRevision}); err != nil {
			return storageFailure("insert outcome deviation", err)
		}
	}
	for index, value := range input.Risks {
		if err := queries.InsertOutcomeAssessmentRisk(ctx, dbgen.InsertOutcomeAssessmentRiskParams{AssessmentID: assessmentID, Ordinal: int64(index), Severity: value.Severity, Summary: value.Summary, Mitigation: value.Mitigation}); err != nil {
			return storageFailure("insert outcome risk", err)
		}
	}
	for index, value := range input.Unknowns {
		if err := queries.InsertOutcomeAssessmentUnknown(ctx, dbgen.InsertOutcomeAssessmentUnknownParams{AssessmentID: assessmentID, Ordinal: int64(index), Summary: value.Summary}); err != nil {
			return storageFailure("insert outcome unknown", err)
		}
	}
	for _, value := range followups {
		if err := queries.InsertOutcomeAssessmentFollowUpTask(ctx, dbgen.InsertOutcomeAssessmentFollowUpTaskParams{AssessmentID: assessmentID, Ordinal: value.Ordinal, TaskID: value.TaskID, TaskRevision: value.TaskRevision, EventSequence: value.EventSequence}); err != nil {
			return storageFailure("insert outcome follow-up task", err)
		}
	}
	for index, value := range input.OwnerAttention {
		if err := queries.InsertOutcomeAssessmentOwnerAttention(ctx, dbgen.InsertOutcomeAssessmentOwnerAttentionParams{AssessmentID: assessmentID, Ordinal: int64(index), Urgency: value.Urgency, Action: value.Action, Reason: value.Reason}); err != nil {
			return storageFailure("insert outcome owner attention", err)
		}
	}
	return nil
}

func currentOutcomeEvidence(ctx context.Context, queries *dbgen.Queries, workspaceID, assessedTaskID string, ref dbgen.OutcomeAssessmentEvidenceRef) (domain.OutcomeEvidenceReference, error) {
	value := domain.OutcomeEvidenceReference{SourceType: ref.SourceType, SourceID: ref.SourceID, SourceRevision: ref.SourceRevision, SourceSHA256: ref.SourceSha256, EventSequence: ref.EventSequence, Class: ref.Class, Effect: ref.Effect, PinnedFreshness: ref.PinnedFreshness, CurrentFreshness: domain.OutcomeEvidenceUnknown}
	switch ref.SourceType {
	case domain.OutcomeEvidenceHandoff:
		row, err := queries.GetOutcomeHandoffEvidence(ctx, dbgen.GetOutcomeHandoffEvidenceParams{WorkspaceID: workspaceID, HandoffID: ref.SourceID})
		if errors.Is(err, sql.ErrNoRows) {
			value.Diagnosis = "pinned handoff evidence is missing"
			return value, nil
		}
		if err != nil {
			return domain.OutcomeEvidenceReference{}, storageFailure("read current handoff outcome evidence", err)
		}
		_, hash, hashErr := canonicalContent(map[string]any{"id": row.ID, "run_id": row.RunID, "task_id": row.TaskID, "summary": row.Summary, "evidence_json": json.RawMessage(row.EvidenceJson), "created_at": row.CreatedAt})
		if hashErr != nil {
			return domain.OutcomeEvidenceReference{}, storageFailure("hash current handoff outcome evidence", hashErr)
		}
		value.CurrentFreshness = domain.OutcomeEvidenceFresh
		value.Current = hash == ref.SourceSha256 && row.SourceRevision == ref.SourceRevision && row.EventSequence == ref.EventSequence && row.RunStatus == domain.RunCompleted
		if !value.Current {
			value.Diagnosis = "handoff no longer matches its pinned revision, event, or completed run"
		}
	case domain.OutcomeEvidenceCheckRequirementEvidence:
		row, err := queries.GetOutcomeCheckEvidence(ctx, dbgen.GetOutcomeCheckEvidenceParams{WorkspaceID: workspaceID, EvidenceID: ref.SourceID})
		if errors.Is(err, sql.ErrNoRows) {
			value.Diagnosis = "pinned mechanical evidence is missing"
			return value, nil
		}
		if err != nil {
			return domain.OutcomeEvidenceReference{}, storageFailure("read current mechanical outcome evidence", err)
		}
		_, hash, hashErr := canonicalContent(map[string]any{"id": row.ID, "requirement_id": row.RequirementID, "requirement_revision": row.RequirementRevision, "check_result_id": row.CheckResultID, "freshness_revision": row.FreshnessRevision, "class": row.Class, "effect": row.Effect, "pinned_freshness": row.PinnedFreshness})
		if hashErr != nil {
			return domain.OutcomeEvidenceReference{}, storageFailure("hash current mechanical outcome evidence", hashErr)
		}
		value.CurrentFreshness = row.CurrentFreshness
		latest := interfaceBoolean(row.CurrentRequirementResult)
		value.Contradictory = row.Effect == domain.CheckEvidenceContradicts && row.RequirementStatus == domain.CheckRequirementActive &&
			row.CurrentRequirementRevision == row.RequirementRevision && latest && row.CurrentFreshness == domain.CheckFreshnessFresh
		value.Current = row.TaskID == assessedTaskID && hash == ref.SourceSha256 && row.EventSequence == ref.EventSequence &&
			row.RequirementStatus == domain.CheckRequirementActive && row.CurrentRequirementRevision == row.RequirementRevision && latest &&
			row.CurrentFreshnessRevision == ref.SourceRevision && row.CurrentFreshness == domain.CheckFreshnessFresh
		switch {
		case row.TaskID != assessedTaskID:
			value.Diagnosis = "mechanical evidence is outside the assessed task"
		case row.RequirementStatus != domain.CheckRequirementActive:
			value.Diagnosis = "mechanical evidence requirement is retired"
		case row.CurrentRequirementRevision != row.RequirementRevision:
			value.Diagnosis = "mechanical evidence requirement revision was replaced"
		case !latest:
			value.Diagnosis = "mechanical evidence is not the latest exact requirement result"
		case row.CurrentFreshness != domain.CheckFreshnessFresh:
			value.Diagnosis = "mechanical evidence is " + row.CurrentFreshness
		case row.CurrentFreshnessRevision != ref.SourceRevision:
			value.Diagnosis = "mechanical evidence freshness revision changed"
		}
	default:
		return domain.OutcomeEvidenceReference{}, storageFailure("validate outcome evidence type", errors.New("stored outcome evidence type is outside the closed union"))
	}
	if !value.Current && value.Diagnosis == "" {
		value.Diagnosis = "evidence no longer matches its pinned current source"
	}
	return value, nil
}

func validateOutcomeAssessmentRead(ctx context.Context, queries *dbgen.Queries, row dbgen.OutcomeAssessment, detail domain.OutcomeAssessmentDetail) error {
	if row.Revision < 1 || row.StateRevision < 1 || !validOutcomeConclusion(row.Conclusion) || !validOutcomeReviewState(row.ReviewState) || row.ProposedBy != localOwnerActorID {
		return storageFailure("validate outcome assessment lifecycle", errors.New("assessment lifecycle fields are invalid"))
	}
	input := domain.OutcomeAssessmentInput{
		Conclusion: row.Conclusion, DeliveredScope: detail.DeliveredScope, UnmetScope: detail.UnmetScope,
		DecisionRevisionIDs: make([]string, 0, len(detail.Decisions)), Evidence: make([]domain.OutcomeEvidenceInput, 0, len(detail.Evidence)),
		Effects: make([]domain.OutcomeEffectInput, 0, len(detail.Effects)), Deviations: make([]domain.OutcomeDeviationInput, 0, len(detail.Deviations)),
		Risks: make([]domain.OutcomeRiskInput, 0, len(detail.Risks)), Unknowns: make([]domain.OutcomeUnknownInput, 0, len(detail.Unknowns)),
		FollowUpTaskIDs: make([]string, 0, len(detail.FollowUpTasks)), OwnerAttention: make([]domain.OutcomeOwnerAttentionInput, 0, len(detail.OwnerAttention)),
	}
	for _, value := range detail.Decisions {
		input.DecisionRevisionIDs = append(input.DecisionRevisionIDs, value.RevisionID)
	}
	for _, value := range detail.Evidence {
		input.Evidence = append(input.Evidence, domain.OutcomeEvidenceInput{SourceType: value.SourceType, SourceID: value.SourceID})
	}
	for index, value := range detail.Effects {
		if value.Ordinal != int64(index) {
			return storageFailure("validate outcome effect ordinals", errors.New("outcome effect ordinals are not contiguous"))
		}
		input.Effects = append(input.Effects, domain.OutcomeEffectInput{Kind: value.Kind, Direction: value.Direction, Summary: value.Summary})
	}
	for index, value := range detail.Deviations {
		if value.Ordinal != int64(index) {
			return storageFailure("validate outcome deviation ordinals", errors.New("outcome deviation ordinals are not contiguous"))
		}
		input.Deviations = append(input.Deviations, domain.OutcomeDeviationInput{Kind: value.Kind, Summary: value.Summary, RelatedTaskID: value.RelatedTaskID})
	}
	for index, value := range detail.Risks {
		if value.Ordinal != int64(index) {
			return storageFailure("validate outcome risk ordinals", errors.New("outcome risk ordinals are not contiguous"))
		}
		input.Risks = append(input.Risks, domain.OutcomeRiskInput{Severity: value.Severity, Summary: value.Summary, Mitigation: value.Mitigation})
	}
	for index, value := range detail.Unknowns {
		if value.Ordinal != int64(index) {
			return storageFailure("validate outcome unknown ordinals", errors.New("outcome unknown ordinals are not contiguous"))
		}
		input.Unknowns = append(input.Unknowns, domain.OutcomeUnknownInput{Summary: value.Summary})
	}
	for index, value := range detail.FollowUpTasks {
		if value.Ordinal != int64(index) {
			return storageFailure("validate outcome follow-up ordinals", errors.New("outcome follow-up ordinals are not contiguous"))
		}
		input.FollowUpTaskIDs = append(input.FollowUpTaskIDs, value.TaskID)
	}
	for index, value := range detail.OwnerAttention {
		if value.Ordinal != int64(index) {
			return storageFailure("validate outcome attention ordinals", errors.New("outcome attention ordinals are not contiguous"))
		}
		input.OwnerAttention = append(input.OwnerAttention, domain.OutcomeOwnerAttentionInput{Urgency: value.Urgency, Action: value.Action, Reason: value.Reason})
	}
	content := outcomeAssessmentContent{
		WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, ObjectiveID: row.ObjectiveID, TaskID: row.TaskID,
		CommitmentID: row.CommitmentID, Revision: row.Revision, SupersedesAssessmentID: stringValue(row.SupersedesAssessmentID), Input: input,
	}
	contentJSON, contentSHA, err := canonicalContent(content)
	if err != nil || string(contentJSON) != row.ContentJson || contentSHA != row.ContentSha256 {
		return storageFailure("validate outcome assessment content", errors.New("assessment normalized children differ from its canonical content seal"))
	}
	submission, err := queries.GetOutcomeAssessmentSubmission(ctx, row.ID)
	childCount := len(detail.Decisions) + len(detail.Evidence) + len(detail.Effects) + len(detail.Deviations) + len(detail.Risks) + len(detail.Unknowns) + len(detail.FollowUpTasks) + len(detail.OwnerAttention)
	if err != nil || submission.ChildCount != int64(childCount) || submission.SubmittedAt != row.ProposedAt {
		return storageFailure("validate outcome assessment submission", errors.New("assessment submission receipt is missing or differs from normalized children"))
	}
	event, err := queries.GetOutcomeJournalEvent(ctx, submission.EventSequence)
	expectedProposalData, _ := json.Marshal(map[string]any{
		"commitment_id": row.CommitmentID, "task_id": row.TaskID, "assessment_revision": row.Revision,
		"conclusion": row.Conclusion, "supersedes_assessment_id": stringValue(row.SupersedesAssessmentID), "content_sha256": row.ContentSha256,
	})
	if err != nil || event.WorkspaceID != row.WorkspaceID || event.EntityType != "outcome_assessment" || event.EntityID != row.ID || event.EntityRevision != 1 || event.Type != outcomeAssessmentProposedEvent || event.OccurredAt != row.ProposedAt || event.RecordedAt != row.ProposedAt || event.ActorID != localOwnerActorID || event.ActorType != localActorType || event.DataJson != string(expectedProposalData) {
		return storageFailure("validate outcome assessment proposal event", errors.New("assessment proposal journal fact differs from its receipt"))
	}
	governance, governanceErr := queries.GetOutcomeAssessmentGovernance(ctx, row.ID)
	if row.ReviewState == domain.OutcomeAssessmentProposed {
		if !errors.Is(governanceErr, sql.ErrNoRows) || row.StateRevision != 1 || row.DecidedAt != nil || row.DecidedBy != nil {
			return storageFailure("validate proposed outcome assessment", errors.New("proposed assessment has governance state"))
		}
		return nil
	}
	if row.ReviewState == domain.OutcomeAssessmentSuperseded {
		// Its original accepted governance is retained when a successor supersedes it.
		if governanceErr != nil || governance.Decision != domain.OutcomeAssessmentAccepted || row.StateRevision < 3 {
			return storageFailure("validate superseded outcome assessment", errors.New("superseded assessment lacks accepted governance"))
		}
	} else if governanceErr != nil || governance.Decision != row.ReviewState || row.StateRevision != 2 {
		return storageFailure("validate decided outcome assessment", errors.New("assessment governance differs from review state"))
	}
	if row.DecidedAt == nil || row.DecidedBy == nil || *row.DecidedAt != governance.DecidedAt || *row.DecidedBy != localOwnerActorID {
		return storageFailure("validate outcome assessment decision actor", errors.New("assessment decision actor/time differs from governance receipt"))
	}
	decisionEvent, err := queries.GetOutcomeJournalEvent(ctx, governance.DecisionEventSequence)
	wantEvent := outcomeAssessmentAcceptedEvent
	if governance.Decision == domain.OutcomeAssessmentRejected {
		wantEvent = outcomeAssessmentRejectedEvent
	}
	if err != nil || decisionEvent.WorkspaceID != row.WorkspaceID || decisionEvent.EntityType != "outcome_assessment" || decisionEvent.EntityID != row.ID || decisionEvent.EntityRevision != 2 || decisionEvent.Type != wantEvent || decisionEvent.OccurredAt != governance.DecidedAt || decisionEvent.ActorID != localOwnerActorID {
		return storageFailure("validate outcome assessment decision event", errors.New("assessment governance event differs from its receipt"))
	}
	expectedDecisionData, marshalErr := json.Marshal(map[string]any{
		"commitment_id": row.CommitmentID, "assessment_revision": row.Revision,
		"conclusion": row.Conclusion, "decision_note": stringValue(row.DecisionNote),
	})
	if marshalErr != nil || decisionEvent.RecordedAt != governance.DecidedAt || decisionEvent.ActorType != localActorType || decisionEvent.DataJson != string(expectedDecisionData) {
		return storageFailure("validate outcome assessment decision event", errors.New("assessment governance event payload differs from its canonical decision"))
	}
	if governance.Decision == domain.OutcomeAssessmentAccepted {
		if row.Conclusion == domain.OutcomeAchieved || row.Conclusion == domain.OutcomePartial {
			supports := false
			for _, evidence := range detail.Evidence {
				if evidence.Effect == domain.CheckEvidenceSupports {
					supports = true
					break
				}
			}
			if !supports {
				return storageFailure("validate accepted outcome evidence", errors.New("accepted achieved or partial assessment lacks supporting evidence"))
			}
		}
		basis, basisErr := queries.GetOutcomeAssessmentAcceptanceBasis(ctx, row.ID)
		expectedBasisSHA, hashErr := hashCommand("outcome.policy_acceptance", map[string]any{
			"assessment_id": row.ID, "content_sha256": row.ContentSha256,
			"event_sequence": governance.DecisionEventSequence, "state_revision": int64(2),
		})
		if hashErr != nil || basisErr != nil || basis.EventSequence != governance.DecisionEventSequence || basis.SourceSha256 != expectedBasisSHA || basis.CreatedAt != governance.DecidedAt || basis.CreatedBy != localOwnerActorID {
			return storageFailure("validate outcome assessment acceptance basis", errors.New("accepted assessment has no exact derived acceptance basis"))
		}
	} else if _, basisErr := queries.GetOutcomeAssessmentAcceptanceBasis(ctx, row.ID); !errors.Is(basisErr, sql.ErrNoRows) {
		return storageFailure("validate rejected outcome assessment basis", errors.New("rejected assessment unexpectedly has acceptance basis"))
	}
	if governance.Decision == domain.OutcomeAssessmentAccepted && row.SupersedesAssessmentID != nil {
		if governance.SupersededAssessmentID == nil || *governance.SupersededAssessmentID != *row.SupersedesAssessmentID || governance.SupersededEventSequence == nil {
			return storageFailure("validate outcome assessment supersession receipt", errors.New("accepted successor does not cite its exact superseded assessment and event"))
		}
		prior, priorErr := queries.GetOutcomeAssessment(ctx, dbgen.GetOutcomeAssessmentParams{WorkspaceID: row.WorkspaceID, ID: *row.SupersedesAssessmentID})
		supersededEvent, eventErr := queries.GetOutcomeJournalEvent(ctx, *governance.SupersededEventSequence)
		expectedData, _ := json.Marshal(map[string]any{"successor_assessment_id": row.ID, "commitment_id": row.CommitmentID})
		if priorErr != nil || prior.CommitmentID != row.CommitmentID || prior.ReviewState != domain.OutcomeAssessmentSuperseded || eventErr != nil || supersededEvent.WorkspaceID != row.WorkspaceID || supersededEvent.EntityType != "outcome_assessment" || supersededEvent.EntityID != prior.ID || supersededEvent.EntityRevision != prior.StateRevision || supersededEvent.Type != outcomeAssessmentSupersededEvent || supersededEvent.OccurredAt != governance.DecidedAt || supersededEvent.DataJson != string(expectedData) {
			return storageFailure("validate outcome assessment supersession event", errors.New("successor governance differs from the superseded assessment event"))
		}
	} else if governance.SupersededAssessmentID != nil || governance.SupersededEventSequence != nil {
		return storageFailure("validate outcome assessment supersession receipt", errors.New("non-successor governance unexpectedly records supersession"))
	}
	if row.ReviewState == domain.OutcomeAssessmentSuperseded {
		priorID := row.ID
		successor, successorErr := queries.GetOutcomeAssessmentSuccessorGovernance(ctx, &priorID)
		if successorErr != nil || successor.CommitmentID != row.CommitmentID || (successor.ReviewState != domain.OutcomeAssessmentAccepted && successor.ReviewState != domain.OutcomeAssessmentSuperseded) || successor.SupersededEventSequence == nil {
			return storageFailure("validate superseded outcome successor", errors.New("superseded assessment has no exact accepted successor governance"))
		}
		supersededEvent, eventErr := queries.GetOutcomeJournalEvent(ctx, *successor.SupersededEventSequence)
		expectedData, _ := json.Marshal(map[string]any{"successor_assessment_id": successor.SuccessorAssessmentID, "commitment_id": row.CommitmentID})
		if eventErr != nil || supersededEvent.EntityID != row.ID || supersededEvent.EntityRevision != row.StateRevision || supersededEvent.Type != outcomeAssessmentSupersededEvent || supersededEvent.OccurredAt != successor.DecidedAt || supersededEvent.DataJson != string(expectedData) {
			return storageFailure("validate superseded outcome successor event", errors.New("superseded assessment event does not identify its accepted successor"))
		}
	}
	return nil
}

func interfaceBoolean(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case int64:
		return typed != 0
	case []byte:
		return string(typed) == "1"
	case string:
		return typed == "1"
	default:
		return false
	}
}

func normalizeOutcomeAssessmentInput(input domain.OutcomeAssessmentInput) (domain.OutcomeAssessmentInput, error) {
	if input.Evidence == nil {
		input.Evidence = []domain.OutcomeEvidenceInput{}
	}
	if input.Effects == nil {
		input.Effects = []domain.OutcomeEffectInput{}
	}
	if input.Deviations == nil {
		input.Deviations = []domain.OutcomeDeviationInput{}
	}
	if input.Risks == nil {
		input.Risks = []domain.OutcomeRiskInput{}
	}
	if input.Unknowns == nil {
		input.Unknowns = []domain.OutcomeUnknownInput{}
	}
	if input.OwnerAttention == nil {
		input.OwnerAttention = []domain.OutcomeOwnerAttentionInput{}
	}
	input.Conclusion = strings.TrimSpace(input.Conclusion)
	if !validOutcomeConclusion(input.Conclusion) {
		return input, outcomeError(CodeInvalidOutcomeAssessment, "assessment conclusion is invalid")
	}
	var err error
	if input.DeliveredScope, err = normalizeOutcomeStrings(input.DeliveredScope, 0, 32, 2048, "delivered scope"); err != nil {
		return input, err
	}
	if input.UnmetScope, err = normalizeOutcomeStrings(input.UnmetScope, 0, 32, 2048, "unmet scope"); err != nil {
		return input, err
	}
	if input.DecisionRevisionIDs, err = normalizeOutcomeIDs(input.DecisionRevisionIDs, 32, "decision revisions"); err != nil {
		return input, err
	}
	if input.FollowUpTaskIDs, err = normalizeOutcomeIDs(input.FollowUpTaskIDs, 32, "follow-up tasks"); err != nil {
		return input, err
	}
	if len(input.Evidence) > 32 || len(input.Effects) > 32 || len(input.Deviations) > 32 || len(input.Risks) > 32 || len(input.Unknowns) > 32 || len(input.OwnerAttention) > 32 {
		return input, outcomeError(CodeInvalidOutcomeAssessment, "each assessment child collection is limited to 32 records")
	}
	seenEvidence := map[string]struct{}{}
	for index := range input.Evidence {
		input.Evidence[index].SourceType = strings.TrimSpace(input.Evidence[index].SourceType)
		input.Evidence[index].SourceID = strings.TrimSpace(input.Evidence[index].SourceID)
		if (input.Evidence[index].SourceType != domain.OutcomeEvidenceHandoff && input.Evidence[index].SourceType != domain.OutcomeEvidenceCheckRequirementEvidence) || !validOutcomeText(input.Evidence[index].SourceID, 128, false) {
			return input, outcomeError(CodeInvalidOutcomeAssessment, "assessment evidence requires an exact handoff or check_requirement_evidence source")
		}
		key := input.Evidence[index].SourceType + "\x00" + input.Evidence[index].SourceID
		if _, exists := seenEvidence[key]; exists {
			return input, outcomeError(CodeInvalidOutcomeAssessment, "assessment evidence sources must be distinct")
		}
		seenEvidence[key] = struct{}{}
	}
	sort.Slice(input.Evidence, func(i, j int) bool {
		if input.Evidence[i].SourceType != input.Evidence[j].SourceType {
			return input.Evidence[i].SourceType < input.Evidence[j].SourceType
		}
		return input.Evidence[i].SourceID < input.Evidence[j].SourceID
	})
	for index := range input.Effects {
		value := &input.Effects[index]
		value.Kind, value.Direction, value.Summary = strings.TrimSpace(value.Kind), strings.TrimSpace(value.Direction), strings.TrimSpace(value.Summary)
		if (value.Kind != domain.OutcomeEffectCompatibility && value.Kind != domain.OutcomeEffectStability) || !validOutcomeEffectDirection(value.Direction) || !validOutcomeText(value.Summary, 2048, false) {
			return input, outcomeError(CodeInvalidOutcomeAssessment, "assessment effect is invalid")
		}
	}
	for index := range input.Deviations {
		value := &input.Deviations[index]
		value.Kind, value.Summary, value.RelatedTaskID = strings.TrimSpace(value.Kind), strings.TrimSpace(value.Summary), strings.TrimSpace(value.RelatedTaskID)
		if (value.Kind != domain.OutcomeDeviationScopeChange && value.Kind != domain.OutcomeDeviationDuplicateWork) || !validOutcomeText(value.Summary, 2048, false) || !validOutcomeText(value.RelatedTaskID, 128, true) {
			return input, outcomeError(CodeInvalidOutcomeAssessment, "assessment deviation is invalid")
		}
	}
	for index := range input.Risks {
		value := &input.Risks[index]
		value.Severity, value.Summary, value.Mitigation = strings.TrimSpace(value.Severity), strings.TrimSpace(value.Summary), strings.TrimSpace(value.Mitigation)
		if !validOutcomeRiskSeverity(value.Severity) || !validOutcomeText(value.Summary, 2048, false) || !validOutcomeText(value.Mitigation, 2048, true) {
			return input, outcomeError(CodeInvalidOutcomeAssessment, "assessment risk is invalid")
		}
	}
	for index := range input.Unknowns {
		input.Unknowns[index].Summary = strings.TrimSpace(input.Unknowns[index].Summary)
		if !validOutcomeText(input.Unknowns[index].Summary, 2048, false) {
			return input, outcomeError(CodeInvalidOutcomeAssessment, "assessment unknown is invalid")
		}
	}
	for index := range input.OwnerAttention {
		value := &input.OwnerAttention[index]
		value.Urgency, value.Action, value.Reason = strings.TrimSpace(value.Urgency), strings.TrimSpace(value.Action), strings.TrimSpace(value.Reason)
		if !validOutcomeUrgency(value.Urgency) || !validOutcomeText(value.Action, 2048, false) || !validOutcomeText(value.Reason, 2048, false) {
			return input, outcomeError(CodeInvalidOutcomeAssessment, "assessment owner attention is invalid")
		}
	}
	children := len(input.DecisionRevisionIDs) + len(input.Evidence) + len(input.Effects) + len(input.Deviations) + len(input.Risks) + len(input.Unknowns) + len(input.FollowUpTaskIDs) + len(input.OwnerAttention)
	if children > maximumOutcomeChildren {
		return input, outcomeError(CodeInvalidOutcomeAssessment, "assessment has more than 256 child records")
	}
	switch input.Conclusion {
	case domain.OutcomeAchieved:
		if len(input.DeliveredScope) == 0 || len(input.UnmetScope) != 0 {
			return input, outcomeError(CodeInvalidOutcomeAssessment, "achieved requires delivered scope and no unmet scope")
		}
	case domain.OutcomePartial:
		if len(input.DeliveredScope) == 0 || len(input.UnmetScope) == 0 {
			return input, outcomeError(CodeInvalidOutcomeAssessment, "partial requires both delivered and unmet scope")
		}
	case domain.OutcomeNotAchieved:
		if len(input.DeliveredScope) != 0 || len(input.UnmetScope) == 0 {
			return input, outcomeError(CodeInvalidOutcomeAssessment, "not_achieved requires unmet scope and no delivered scope")
		}
	case domain.OutcomeUnknown:
		if len(input.Unknowns) == 0 {
			return input, outcomeError(CodeInvalidOutcomeAssessment, "unknown conclusion requires at least one explicit unknown")
		}
	}
	return input, nil
}

func normalizeOutcomeStrings(values []string, minimum, maximum, byteLimit int, label string) ([]string, error) {
	if len(values) < minimum || len(values) > maximum {
		return nil, outcomeError(CodeInvalidOutcomeAssessment, fmt.Sprintf("%s must contain %d to %d values", label, minimum, maximum))
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if !validOutcomeText(value, byteLimit, false) {
			return nil, outcomeError(CodeInvalidOutcomeAssessment, fmt.Sprintf("%s contains invalid text", label))
		}
		if _, exists := seen[value]; exists {
			return nil, outcomeError(CodeInvalidOutcomeAssessment, fmt.Sprintf("%s must be distinct", label))
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeOutcomeIDs(values []string, maximum int, label string) ([]string, error) {
	return normalizeOutcomeStrings(values, 0, maximum, 128, label)
}

func validOutcomeText(value string, maximumBytes int, empty bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') && len(value) <= maximumBytes && (empty || value != "")
}

func validOutcomeConclusion(value string) bool {
	return value == domain.OutcomeAchieved || value == domain.OutcomePartial || value == domain.OutcomeNotAchieved || value == domain.OutcomeUnknown
}

func validOutcomeReviewState(value string) bool {
	return value == domain.OutcomeAssessmentProposed || value == domain.OutcomeAssessmentAccepted || value == domain.OutcomeAssessmentRejected || value == domain.OutcomeAssessmentSuperseded
}

func validOutcomeEffectDirection(value string) bool {
	return value == domain.OutcomeEffectPositive || value == domain.OutcomeEffectNeutral || value == domain.OutcomeEffectNegative || value == domain.OutcomeEffectUncertain
}

func validOutcomeRiskSeverity(value string) bool {
	return value == domain.OutcomeRiskLow || value == domain.OutcomeRiskMedium || value == domain.OutcomeRiskHigh || value == domain.OutcomeRiskCritical
}

func validOutcomeUrgency(value string) bool {
	return value == domain.OutcomeAttentionNow || value == domain.OutcomeAttentionNext || value == domain.OutcomeAttentionLater
}

func outcomeError(code, message string) error { return &Error{Code: code, Message: message} }
