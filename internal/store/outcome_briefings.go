package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	maximumBriefingClaims = 128
	maximumBriefingBytes  = 64 * 1024
)

var briefingSectionOrder = []string{
	domain.BriefingSectionRequiredDecisions,
	domain.BriefingSectionContradictions,
	domain.BriefingSectionRisksUnknowns,
	domain.BriefingSectionVerificationGaps,
	domain.BriefingSectionDeviationsUnmet,
	domain.BriefingSectionAcceptedDelivery,
	domain.BriefingSectionRationaleChange,
}

var briefingSectionQuota = map[string]int{
	domain.BriefingSectionRequiredDecisions: 24,
	domain.BriefingSectionContradictions:    16,
	domain.BriefingSectionRisksUnknowns:     24,
	domain.BriefingSectionVerificationGaps:  24,
	domain.BriefingSectionDeviationsUnmet:   16,
	domain.BriefingSectionAcceptedDelivery:  12,
	domain.BriefingSectionRationaleChange:   12,
}

type managementBriefingContent struct {
	Scope                domain.BriefingScope      `json:"scope"`
	EventCursor          int64                     `json:"event_cursor"`
	CutoffEventSequence  int64                     `json:"cutoff_event_sequence"`
	CheckpointID         string                    `json:"checkpoint_id,omitempty"`
	SinceEventSequence   int64                     `json:"since_event_sequence"`
	EvaluatedAt          string                    `json:"evaluated_at"`
	CaughtUp             bool                      `json:"caught_up"`
	UnknownEventType     string                    `json:"unknown_event_type,omitempty"`
	UnknownEventSequence int64                     `json:"unknown_event_sequence,omitempty"`
	Claims               []domain.BriefingClaim    `json:"claims"`
	Omitted              []domain.BriefingOmission `json:"omitted"`
}

type briefingCandidate struct {
	Section     string
	SemanticKey string
	Claim       domain.BriefingClaim
}

type outcomeProjection struct {
	Cursor               int64
	UnknownEventType     string
	UnknownEventSequence int64
}

func (s *Store) ShowManagementBriefing(ctx context.Context, query ShowManagementBriefingQuery) (domain.ManagementBriefing, error) {
	query.WorkspaceIdentifier = strings.TrimSpace(query.WorkspaceIdentifier)
	query.ScopeType = strings.TrimSpace(query.ScopeType)
	query.ScopeIdentifier = strings.TrimSpace(query.ScopeIdentifier)
	query.SinceCheckpointID = strings.TrimSpace(query.SinceCheckpointID)
	if query.WorkspaceIdentifier == "" || !validOutcomeScopeType(query.ScopeType) || query.ScopeIdentifier == "" {
		return domain.ManagementBriefing{}, outcomeError(CodeInvalidManagementBriefing, "briefing requires an exact workspace and task, objective, project, or workspace scope")
	}
	workspace, err := s.Workspace(ctx, query.WorkspaceIdentifier)
	if err != nil {
		return domain.ManagementBriefing{}, err
	}
	scope, err := resolveOutcomeScope(ctx, dbgen.New(s.db), workspace, query.ScopeType, query.ScopeIdentifier)
	if err != nil {
		return domain.ManagementBriefing{}, outcomeError(CodeInvalidManagementBriefing, "briefing scope was not found in the selected workspace")
	}
	sinceSequence := int64(0)
	checkpointID := ""
	if query.SinceCheckpointID != "" {
		checkpoint, checkpointErr := s.OwnerCheckpoint(ctx, workspace.ID, query.SinceCheckpointID)
		if checkpointErr != nil {
			return domain.ManagementBriefing{}, checkpointErr
		}
		if checkpoint.ScopeType != scope.Type || checkpoint.ScopeID != scopeID(scope) {
			return domain.ManagementBriefing{}, outcomeError(CodeInvalidManagementBriefing, "since checkpoint must have the exact briefing scope")
		}
		checkpointID, sinceSequence = checkpoint.ID, checkpoint.EventSequence
	}
	cutoff, err := dbgen.New(s.db).MaxWorkspaceEventSequence(ctx, workspace.ID)
	if err != nil {
		return domain.ManagementBriefing{}, storageFailure("capture briefing event cutoff", err)
	}
	projection, err := s.advanceOutcomeProjector(ctx, workspace.ID, cutoff)
	if err != nil {
		return domain.ManagementBriefing{}, err
	}
	evaluatedAt := s.nowText()
	return s.materializeManagementBriefing(ctx, workspace.ID, scope, checkpointID, sinceSequence, cutoff, projection, evaluatedAt)
}

func (s *Store) advanceOutcomeProjector(ctx context.Context, workspaceID string, cutoff int64) (outcomeProjection, error) {
	for {
		var result outcomeProjection
		done := false
		err := s.withOutcomeMutation(ctx, "outcome projector page", func(tx *sql.Tx) error {
			queries := dbgen.New(tx)
			state, stateErr := queries.GetOutcomeProjectorState(ctx, workspaceID)
			if errors.Is(stateErr, sql.ErrNoRows) {
				if insertErr := queries.InsertOutcomeProjectorState(ctx, dbgen.InsertOutcomeProjectorStateParams{WorkspaceID: workspaceID, UpdatedAt: s.nowText()}); insertErr != nil {
					return storageFailure("initialize outcome projector cursor", insertErr)
				}
				state, stateErr = queries.GetOutcomeProjectorState(ctx, workspaceID)
			}
			if stateErr != nil {
				return storageFailure("read outcome projector cursor", stateErr)
			}
			result.Cursor = state.LastEventSequence
			if state.LastEventSequence >= cutoff {
				done = true
				return nil
			}
			events, listErr := queries.ListOutcomeProjectorEvents(ctx, dbgen.ListOutcomeProjectorEventsParams{WorkspaceID: workspaceID, AfterSequence: state.LastEventSequence, CutoffSequence: cutoff})
			if listErr != nil {
				return storageFailure("read outcome projector page", listErr)
			}
			for _, event := range events {
				if !knownOutcomeProjectorEvent(event.Type) {
					result.UnknownEventType, result.UnknownEventSequence = event.Type, event.Sequence
					done = true
					return nil
				}
			}
			if len(events) == 0 {
				return storageFailure("advance outcome projector", fmt.Errorf("workspace cursor %d cannot reach captured cutoff %d", state.LastEventSequence, cutoff))
			}
			last := events[len(events)-1].Sequence
			changed, updateErr := queries.AdvanceOutcomeProjectorState(ctx, dbgen.AdvanceOutcomeProjectorStateParams{
				LastEventSequence: last, UpdatedAt: s.nowText(), WorkspaceID: workspaceID, ExpectedEventSequence: state.LastEventSequence,
			})
			if updateErr != nil || changed != 1 {
				return storageFailure("advance outcome projector cursor", fmt.Errorf("cursor compare-and-swap failed: %v", updateErr))
			}
			if hookErr := s.runMutationHook(MutationAfterBriefingCursor); hookErr != nil {
				return hookErr
			}
			result.Cursor = last
			done = last >= cutoff
			return nil
		})
		if err != nil {
			return outcomeProjection{}, err
		}
		if done {
			return result, nil
		}
	}
}

func knownOutcomeProjectorEvent(value string) bool { return domain.KnownEventType(value) }

func (s *Store) materializeManagementBriefing(ctx context.Context, workspaceID string, scope domain.BriefingScope, checkpointID string, sinceSequence, cutoff int64, projection outcomeProjection, evaluatedAt string) (domain.ManagementBriefing, error) {
	var result domain.ManagementBriefing
	err := s.withOutcomeMutation(ctx, "management briefing", func(tx *sql.Tx) error {
		queries := dbgen.New(tx)
		candidates, candidateErr := buildBriefingCandidates(ctx, queries, workspaceID, scope, sinceSequence, projection.Cursor, evaluatedAt)
		if candidateErr != nil {
			return candidateErr
		}
		selected, omissions := boundBriefingCandidates(scope, candidates)
		content := managementBriefingContent{
			Scope: scope, EventCursor: projection.Cursor, CutoffEventSequence: cutoff, CheckpointID: checkpointID,
			SinceEventSequence: sinceSequence, EvaluatedAt: evaluatedAt, CaughtUp: projection.Cursor == cutoff && projection.UnknownEventType == "",
			UnknownEventType: projection.UnknownEventType, UnknownEventSequence: projection.UnknownEventSequence,
			Claims: claimsFromCandidates(selected), Omitted: omissions,
		}
		contentJSON, contentSHA, boundedSelected, contentErr := boundedBriefingContent(scope, &content, selected)
		if contentErr != nil {
			return contentErr
		}
		selected = boundedSelected
		if existing, findErr := queries.FindManagementBriefing(ctx, dbgen.FindManagementBriefingParams{
			WorkspaceID: workspaceID, ScopeType: scope.Type, ScopeID: scopeID(scope), EventCursor: projection.Cursor,
			CheckpointID: checkpointID, ContentSha256: contentSHA,
		}); findErr == nil {
			value, readErr := managementBriefingFromRow(ctx, queries, existing)
			if readErr != nil {
				return readErr
			}
			result = value
			return nil
		} else if !errors.Is(findErr, sql.ErrNoRows) {
			return storageFailure("find management briefing", findErr)
		}
		revision, revisionErr := queries.NextManagementBriefingRevision(ctx, dbgen.NextManagementBriefingRevisionParams{WorkspaceID: workspaceID, ScopeType: scope.Type, ScopeID: scopeID(scope)})
		if revisionErr != nil {
			return storageFailure("allocate management briefing revision", revisionErr)
		}
		id, idErr := randomID("briefing_")
		if idErr != nil {
			return storageFailure("generate management briefing id", idErr)
		}
		if insertErr := queries.InsertManagementBriefing(ctx, dbgen.InsertManagementBriefingParams{
			ID: id, Revision: revision, WorkspaceID: workspaceID, ScopeType: scope.Type, ScopeID: scopeID(scope),
			EventCursor: projection.Cursor, CutoffEventSequence: cutoff, CheckpointID: checkpointID, SinceEventSequence: sinceSequence,
			EvaluatedAt: evaluatedAt, CaughtUp: boolInt64(content.CaughtUp), UnknownEventType: content.UnknownEventType,
			UnknownEventSequence: content.UnknownEventSequence, ContentJson: string(contentJSON), ContentSha256: contentSHA,
			ByteSize: int64(len(contentJSON)), CreatedAt: evaluatedAt,
		}); insertErr != nil {
			return storageFailure("insert management briefing", insertErr)
		}
		for index, candidate := range selected {
			claimJSON, marshalErr := json.Marshal(candidate.Claim)
			if marshalErr != nil {
				return storageFailure("encode management briefing claim", marshalErr)
			}
			if insertErr := queries.InsertManagementBriefingClaim(ctx, dbgen.InsertManagementBriefingClaimParams{
				BriefingID: id, Ordinal: int64(index), ClaimID: candidate.Claim.ID, SemanticKey: candidate.SemanticKey, Kind: candidate.Claim.Kind,
				Urgency: candidate.Claim.Urgency, Summary: candidate.Claim.Summary, Status: candidate.Claim.Status,
				ProjectID: candidate.Claim.ProjectID, SourceEventSequence: candidate.Claim.SourceEventSequence, ClaimJson: string(claimJSON),
			}); insertErr != nil {
				return storageFailure("insert management briefing claim", insertErr)
			}
			for sourceIndex, source := range candidate.Claim.Sources {
				if insertErr := queries.InsertManagementBriefingClaimSource(ctx, dbgen.InsertManagementBriefingClaimSourceParams{
					BriefingID: id, ClaimID: candidate.Claim.ID, Ordinal: int64(sourceIndex), EntityType: source.EntityType,
					EntityID: source.EntityID, EntityRevision: source.Revision, ContentSha256: source.ContentSHA256, EventSequence: source.EventSequence,
					EvidenceClass: source.EvidenceClass, EvidenceEffect: source.EvidenceEffect,
					PinnedFreshness: source.PinnedFreshness, CurrentFreshness: source.CurrentFreshness,
				}); insertErr != nil {
					return storageFailure("insert management briefing claim source", insertErr)
				}
			}
		}
		if hookErr := s.runMutationHook(MutationAfterBriefingClaims); hookErr != nil {
			return hookErr
		}
		sourceCount := 0
		for _, candidate := range selected {
			sourceCount += len(candidate.Claim.Sources)
		}
		if insertErr := queries.InsertManagementBriefingReceipt(ctx, dbgen.InsertManagementBriefingReceiptParams{
			BriefingID: id, ClaimCount: int64(len(selected)), SourceCount: int64(sourceCount), SealedAt: evaluatedAt,
		}); insertErr != nil {
			return storageFailure("seal complete management briefing", insertErr)
		}
		if hookErr := s.runMutationHook(MutationAfterBriefingRevision); hookErr != nil {
			return hookErr
		}
		row, readErr := queries.GetManagementBriefing(ctx, dbgen.GetManagementBriefingParams{WorkspaceID: workspaceID, ID: id})
		if readErr != nil {
			return storageFailure("read inserted management briefing", readErr)
		}
		value, readErr := managementBriefingFromRow(ctx, queries, row)
		if readErr != nil {
			return readErr
		}
		result = value
		return nil
	})
	return result, err
}

func buildBriefingCandidates(ctx context.Context, queries *dbgen.Queries, workspaceID string, scope domain.BriefingScope, sinceSequence, cursor int64, evaluatedAt string) ([]briefingCandidate, error) {
	accepted, err := queries.ListAcceptedOutcomeAssessmentClaims(ctx, dbgen.ListAcceptedOutcomeAssessmentClaimsParams{
		WorkspaceID: workspaceID, EventCursor: cursor,
		ProjectID: scope.ProjectID, ObjectiveID: scope.ObjectiveID, TaskID: scope.TaskID,
	})
	if err != nil {
		return nil, storageFailure("list accepted outcome briefing sources", err)
	}
	result := make([]briefingCandidate, 0)
	for _, row := range accepted {
		detail, detailErr := outcomeAssessmentDetail(ctx, queries, workspaceID, row.AssessmentID, evaluatedAt)
		if detailErr != nil {
			return nil, detailErr
		}
		base := []domain.BriefingClaimSource{
			{EntityType: "outcome_assessment", EntityID: row.AssessmentID, Revision: 2, ContentSHA256: row.ContentSha256, EventSequence: row.EventSequence},
			{EntityType: "deliverable_commitment", EntityID: row.CommitmentID, Revision: 1, ContentSHA256: row.CommitmentSha256, EventSequence: row.CommitmentEventSequence},
			{EntityType: "outcome_assessment_acceptance_basis", EntityID: row.AssessmentID, Revision: 1, ContentSHA256: row.AcceptanceBasisSha256, EventSequence: row.EventSequence},
		}
		deliverySources := append([]domain.BriefingClaimSource(nil), base...)
		for _, evidence := range detail.Evidence {
			deliverySources = append(deliverySources, briefingEvidenceSource(evidence))
		}
		for index, attention := range detail.OwnerAttention {
			result = append(result, newBriefingCandidate(scope, domain.BriefingSectionRequiredDecisions, domain.BriefingClaimRequiredDecision, fmt.Sprintf("attention/%d", index), attention.Urgency, attention.Action+": "+attention.Reason, domain.BriefingClaimStatusRequired, row.ProjectID, base))
		}
		for index, followup := range detail.FollowUpTasks {
			taskSource := domain.BriefingClaimSource{EntityType: "task", EntityID: followup.TaskID, Revision: followup.TaskRevision, EventSequence: followup.EventSequence}
			result = append(result, newBriefingCandidate(scope, domain.BriefingSectionRequiredDecisions, domain.BriefingClaimRequiredDecision, fmt.Sprintf("follow-up/%d", index), domain.OutcomeAttentionNext, "Follow-up task requires owner tracking: "+followup.TaskID, domain.BriefingClaimStatusRequired, row.ProjectID, appendSources(base, taskSource)))
		}
		for index, risk := range detail.Risks {
			result = append(result, newBriefingCandidate(scope, domain.BriefingSectionRisksUnknowns, domain.BriefingClaimRisk, fmt.Sprintf("risk/%d", index), urgencyForRisk(risk.Severity), risk.Summary, risk.Severity, row.ProjectID, base))
		}
		for index, unknown := range detail.Unknowns {
			result = append(result, newBriefingCandidate(scope, domain.BriefingSectionRisksUnknowns, domain.BriefingClaimUnknown, fmt.Sprintf("unknown/%d", index), domain.OutcomeAttentionNow, unknown.Summary, domain.BriefingClaimStatusOpen, row.ProjectID, base))
		}
		for index, evidence := range detail.Evidence {
			if evidence.Current && !evidence.Disputed && !evidence.Contradictory {
				continue
			}
			status := domain.BriefingClaimStatusStale
			switch {
			case evidence.Contradictory:
				status = domain.BriefingClaimStatusContradictory
			case evidence.Disputed:
				status = domain.BriefingClaimStatusDisputed
			case strings.Contains(evidence.Diagnosis, "missing"):
				status = domain.BriefingClaimStatusMissing
			}
			sources := appendSources(base, briefingEvidenceSource(evidence))
			summary := evidence.Diagnosis
			if summary == "" {
				summary = "Pinned outcome evidence requires owner verification"
			}
			result = append(result, newBriefingCandidate(scope, domain.BriefingSectionVerificationGaps, domain.BriefingClaimVerificationGap, fmt.Sprintf("evidence/%d", index), domain.OutcomeAttentionNow, summary, status, row.ProjectID, sources))
		}
		for index, decision := range detail.Decisions {
			decisionEvent, decisionEventErr := queries.GetOutcomeJournalEvent(ctx, decision.EventSequence)
			if decisionEventErr != nil || decisionEvent.EntityType != "knowledge_revision" || decisionEvent.EntityID != decision.RevisionID {
				return nil, storageFailure("validate briefing decision provenance", errors.New("decision acceptance event differs from pinned revision"))
			}
			decisionSource := domain.BriefingClaimSource{EntityType: "knowledge_revision", EntityID: decision.RevisionID, Revision: decisionEvent.EntityRevision, ContentSHA256: decision.ContentSHA256, EventSequence: decision.EventSequence}
			if !decision.Current || decision.Disputed {
				status := domain.BriefingClaimStatusStale
				if decision.Disputed {
					status = domain.BriefingClaimStatusDisputed
				}
				result = append(result, newBriefingCandidate(scope, domain.BriefingSectionVerificationGaps, domain.BriefingClaimVerificationGap, fmt.Sprintf("decision-gap/%d", index), domain.OutcomeAttentionNow, "Accepted outcome decision knowledge requires current verification", status, row.ProjectID, appendSources(base, decisionSource)))
			}
			if row.EventSequence > sinceSequence {
				result = append(result, newBriefingCandidate(scope, domain.BriefingSectionRationaleChange, domain.BriefingClaimRationale, fmt.Sprintf("decision/%d", index), domain.OutcomeAttentionLater, "Accepted decision informs the outcome: "+decision.RevisionID, domain.BriefingClaimStatusAccepted, row.ProjectID, appendSources(base, decisionSource)))
			}
		}
		for index, deviation := range detail.Deviations {
			result = append(result, newBriefingCandidate(scope, domain.BriefingSectionDeviationsUnmet, domain.BriefingClaimDeviation, fmt.Sprintf("deviation/%d", index), domain.OutcomeAttentionNext, deviation.Summary, domain.BriefingClaimStatusRecorded, row.ProjectID, base))
		}
		if len(detail.UnmetScope) != 0 {
			result = append(result, newBriefingCandidate(scope, domain.BriefingSectionDeviationsUnmet, domain.BriefingClaimUnmetCommitment, "unmet", domain.OutcomeAttentionNow, row.CommitmentTitle+": "+strings.Join(detail.UnmetScope, "; "), domain.BriefingClaimStatusUnmet, row.ProjectID, base))
		}
		if row.ReviewState == domain.OutcomeAssessmentAccepted {
			result = append(result, newBriefingCandidate(scope, domain.BriefingSectionAcceptedDelivery, domain.BriefingClaimAcceptedDelivery, "delivery", domain.OutcomeAttentionLater, row.CommitmentTitle+": owner accepted outcome as "+row.Conclusion, row.Conclusion, row.ProjectID, deliverySources))
			if row.EventSequence > sinceSequence && row.SupersededAssessmentID != nil && row.SupersededEventSequence != nil && row.SupersededContentSha256 != nil && row.SupersededStateRevision != nil {
				priorSource := domain.BriefingClaimSource{EntityType: "outcome_assessment", EntityID: *row.SupersededAssessmentID, Revision: *row.SupersededStateRevision, ContentSHA256: *row.SupersededContentSha256, EventSequence: *row.SupersededEventSequence}
				result = append(result, newBriefingCandidate(scope, domain.BriefingSectionRationaleChange, domain.BriefingClaimChange, "delivery-revised", domain.OutcomeAttentionNext, row.CommitmentTitle+": owner revised the accepted delivery judgment", domain.BriefingClaimStatusRecorded, row.ProjectID, appendSources(base, priorSource)))
			}
			if row.EventSequence > sinceSequence {
				for index, effect := range detail.Effects {
					result = append(result, newBriefingCandidate(scope, domain.BriefingSectionRationaleChange, domain.BriefingClaimChange, fmt.Sprintf("effect/%d", index), domain.OutcomeAttentionLater, effect.Summary, domain.BriefingClaimStatusRecorded, row.ProjectID, base))
				}
			}
		}
	}

	unassessed, err := queries.ListUnassessedOutcomeCommitments(ctx, dbgen.ListUnassessedOutcomeCommitmentsParams{
		WorkspaceID: workspaceID, EventCursor: cursor,
		ProjectID: scope.ProjectID, ObjectiveID: scope.ObjectiveID, TaskID: scope.TaskID,
	})
	if err != nil {
		return nil, storageFailure("list unassessed outcome commitments", err)
	}
	for _, row := range unassessed {
		if _, readErr := deliverableCommitment(ctx, queries, workspaceID, row.ID); readErr != nil {
			return nil, readErr
		}
		source := domain.BriefingClaimSource{EntityType: "deliverable_commitment", EntityID: row.ID, Revision: 1, ContentSHA256: row.ContentSha256, EventSequence: row.EventSequence}
		result = append(result, newBriefingCandidate(scope, domain.BriefingSectionDeviationsUnmet, domain.BriefingClaimUnmetCommitment, "unassessed", domain.OutcomeAttentionNow, row.Title+": no owner-accepted outcome assessment", domain.BriefingClaimStatusUnmet, row.ProjectID, []domain.BriefingClaimSource{source}))
	}

	contradictions, err := queries.ListOpenOutcomeContradictions(ctx, dbgen.ListOpenOutcomeContradictionsParams{WorkspaceID: workspaceID, EventCursor: &cursor, ProjectID: scope.ProjectID})
	if err != nil {
		return nil, storageFailure("list open briefing contradictions", err)
	}
	for _, row := range contradictions {
		if row.EventSequence == nil {
			return nil, storageFailure("validate briefing contradiction", errors.New("open contradiction has no confirmation event"))
		}
		if scope.Type == domain.OwnerCheckpointTask || scope.Type == domain.OwnerCheckpointObjective {
			relevant, relevanceErr := queries.OutcomeContradictionTouchesScopeDecision(ctx, dbgen.OutcomeContradictionTouchesScopeDecisionParams{
				WorkspaceID: workspaceID, ObjectiveID: scope.ObjectiveID, TaskID: scope.TaskID,
				LeftRevisionID: row.LeftRevisionID, RightRevisionID: row.RightRevisionID,
			})
			if relevanceErr != nil {
				return nil, storageFailure("derive scoped briefing contradiction", relevanceErr)
			}
			if !relevant {
				continue
			}
		}
		summary := strings.TrimSpace(row.ReportNote)
		if summary == "" {
			summary = "Accepted knowledge revisions contradict: " + row.LeftRevisionID + " and " + row.RightRevisionID
		}
		source := domain.BriefingClaimSource{EntityType: "knowledge_contradiction", EntityID: row.ID, Revision: row.StateRevision, EventSequence: *row.EventSequence}
		result = append(result, newBriefingCandidate(scope, domain.BriefingSectionContradictions, domain.BriefingClaimContradiction, "contradiction", domain.OutcomeAttentionNow, summary, domain.BriefingClaimStatusOpen, row.ProjectID, []domain.BriefingClaimSource{source}))
	}
	return result, nil
}

func newBriefingCandidate(scope domain.BriefingScope, section, kind, semantic, urgency, summary, status, projectID string, sources []domain.BriefingClaimSource) briefingCandidate {
	summary = strings.TrimSpace(summary)
	summary = truncateOutcomeText(summary, 2048)
	sources = canonicalBriefingSources(sources)
	maxSequence := int64(0)
	for _, source := range sources {
		if source.EventSequence > maxSequence {
			maxSequence = source.EventSequence
		}
	}
	semanticKey := kind + "/" + semantic
	idContent := struct {
		Scope    domain.BriefingScope         `json:"scope"`
		Semantic string                       `json:"semantic_kind"`
		Status   string                       `json:"status"`
		Sources  []domain.BriefingClaimSource `json:"sources"`
	}{Scope: scope, Semantic: semanticKey, Status: status, Sources: sources}
	encoded, _ := json.Marshal(idContent)
	digest := sha256.Sum256(encoded)
	claim := domain.BriefingClaim{
		ID: "bclaim_" + hex.EncodeToString(digest[:]), Kind: kind, Urgency: urgency, Summary: summary,
		Status: status, ProjectID: projectID, Sources: sources, SourceEventSequence: maxSequence,
	}
	return briefingCandidate{Section: section, SemanticKey: semanticKey, Claim: claim}
}

func briefingClaimID(scope domain.BriefingScope, semanticKey, status string, sources []domain.BriefingClaimSource) string {
	idContent := struct {
		Scope    domain.BriefingScope         `json:"scope"`
		Semantic string                       `json:"semantic_kind"`
		Status   string                       `json:"status"`
		Sources  []domain.BriefingClaimSource `json:"sources"`
	}{Scope: scope, Semantic: semanticKey, Status: status, Sources: sources}
	encoded, _ := json.Marshal(idContent)
	digest := sha256.Sum256(encoded)
	return "bclaim_" + hex.EncodeToString(digest[:])
}

func outcomeBriefingClaimID(scopeJSON, semanticKey, status, sourcesJSON string) string {
	var scope domain.BriefingScope
	var sources []domain.BriefingClaimSource
	if json.Unmarshal([]byte(scopeJSON), &scope) != nil || json.Unmarshal([]byte(sourcesJSON), &sources) != nil {
		return ""
	}
	return briefingClaimID(scope, semanticKey, status, sources)
}

func canonicalBriefingSources(values []domain.BriefingClaimSource) []domain.BriefingClaimSource {
	result := append([]domain.BriefingClaimSource(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].EntityType != result[j].EntityType {
			return result[i].EntityType < result[j].EntityType
		}
		if result[i].EntityID != result[j].EntityID {
			return result[i].EntityID < result[j].EntityID
		}
		if result[i].Revision != result[j].Revision {
			return result[i].Revision < result[j].Revision
		}
		if result[i].EventSequence != result[j].EventSequence {
			return result[i].EventSequence < result[j].EventSequence
		}
		return result[i].ContentSHA256 < result[j].ContentSHA256
	})
	return result
}

func appendSources(base []domain.BriefingClaimSource, extra ...domain.BriefingClaimSource) []domain.BriefingClaimSource {
	result := append([]domain.BriefingClaimSource(nil), base...)
	return append(result, extra...)
}

func briefingEvidenceSource(value domain.OutcomeEvidenceReference) domain.BriefingClaimSource {
	return domain.BriefingClaimSource{
		EntityType: value.SourceType, EntityID: value.SourceID, Revision: value.SourceRevision,
		ContentSHA256: value.SourceSHA256, EventSequence: value.EventSequence,
		EvidenceClass: value.Class, EvidenceEffect: value.Effect,
		PinnedFreshness: value.PinnedFreshness, CurrentFreshness: value.CurrentFreshness,
	}
}

func boundBriefingCandidates(scope domain.BriefingScope, values []briefingCandidate) ([]briefingCandidate, []domain.BriefingOmission) {
	bySection := make(map[string][]briefingCandidate)
	for _, value := range values {
		bySection[value.Section] = append(bySection[value.Section], value)
	}
	for _, section := range briefingSectionOrder {
		bySection[section] = fairBriefingSection(bySection[section], scope.Type == domain.OwnerCheckpointWorkspace)
	}
	selected := make([]briefingCandidate, 0, maximumBriefingClaims)
	remainders := make([]briefingCandidate, 0)
	for _, section := range briefingSectionOrder {
		values := bySection[section]
		count := briefingSectionQuota[section]
		if count > len(values) {
			count = len(values)
		}
		selected = append(selected, values[:count]...)
		remainders = append(remainders, values[count:]...)
	}
	space := maximumBriefingClaims - len(selected)
	if space > len(remainders) {
		space = len(remainders)
	}
	selected = append(selected, remainders[:space]...)
	omitted := make(map[string]int)
	selectedIDs := make(map[string]struct{}, len(selected))
	for _, value := range selected {
		selectedIDs[value.Claim.ID] = struct{}{}
	}
	for _, value := range values {
		if _, ok := selectedIDs[value.Claim.ID]; !ok {
			omitted[value.Section+"\x00"+domain.BriefingOmittedClaimLimit]++
		}
	}
	return selected, briefingOmissions(omitted)
}

func fairBriefingSection(values []briefingCandidate, roundRobin bool) []briefingCandidate {
	less := func(left, right briefingCandidate) bool {
		if urgencyRank(left.Claim.Urgency) != urgencyRank(right.Claim.Urgency) {
			return urgencyRank(left.Claim.Urgency) < urgencyRank(right.Claim.Urgency)
		}
		if left.Claim.SourceEventSequence != right.Claim.SourceEventSequence {
			return left.Claim.SourceEventSequence > right.Claim.SourceEventSequence
		}
		return left.Claim.ID < right.Claim.ID
	}
	if !roundRobin {
		sort.Slice(values, func(i, j int) bool { return less(values[i], values[j]) })
		return values
	}
	byUrgency := map[string][]briefingCandidate{}
	for _, value := range values {
		byUrgency[value.Claim.Urgency] = append(byUrgency[value.Claim.Urgency], value)
	}
	result := make([]briefingCandidate, 0, len(values))
	for _, urgency := range []string{domain.OutcomeAttentionNow, domain.OutcomeAttentionNext, domain.OutcomeAttentionLater} {
		result = append(result, roundRobinBriefingProjects(byUrgency[urgency], less)...)
	}
	return result
}

func roundRobinBriefingProjects(values []briefingCandidate, less func(briefingCandidate, briefingCandidate) bool) []briefingCandidate {
	groups := make(map[string][]briefingCandidate)
	projects := make([]string, 0)
	for _, value := range values {
		key := value.Claim.ProjectID
		if _, exists := groups[key]; !exists {
			projects = append(projects, key)
		}
		groups[key] = append(groups[key], value)
	}
	sort.Strings(projects)
	for _, project := range projects {
		sort.Slice(groups[project], func(i, j int) bool { return less(groups[project][i], groups[project][j]) })
	}
	result := make([]briefingCandidate, 0, len(values))
	for index := 0; len(result) < len(values); index++ {
		for _, project := range projects {
			if index < len(groups[project]) {
				result = append(result, groups[project][index])
			}
		}
	}
	return result
}

func boundedBriefingContent(scope domain.BriefingScope, content *managementBriefingContent, selected []briefingCandidate) ([]byte, string, []briefingCandidate, error) {
	omitted := make(map[string]int)
	for _, value := range content.Omitted {
		omitted[value.Section+"\x00"+value.Reason] += value.Count
	}
	for {
		content.Claims = claimsFromCandidates(selected)
		content.Omitted = briefingOmissions(omitted)
		encoded, hash, err := canonicalContent(*content)
		if err != nil {
			return nil, "", nil, storageFailure("encode management briefing", err)
		}
		if len(encoded) <= maximumBriefingBytes {
			return encoded, hash, selected, nil
		}
		if len(selected) == 0 {
			return nil, "", nil, outcomeError(CodeInvalidManagementBriefing, "briefing metadata exceeds the 64KiB bound")
		}
		removed := selected[len(selected)-1]
		selected = selected[:len(selected)-1]
		omitted[removed.Section+"\x00"+domain.BriefingOmittedByteLimit]++
	}
}

func truncateOutcomeText(value string, maximumBytes int) string {
	if len(value) <= maximumBytes {
		return value
	}
	for len(value) > maximumBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}

func claimsFromCandidates(values []briefingCandidate) []domain.BriefingClaim {
	result := make([]domain.BriefingClaim, 0, len(values))
	for _, value := range values {
		result = append(result, value.Claim)
	}
	return result
}

func briefingOmissions(values map[string]int) []domain.BriefingOmission {
	result := make([]domain.BriefingOmission, 0, len(values))
	for _, section := range briefingSectionOrder {
		for _, reason := range []string{domain.BriefingOmittedClaimLimit, domain.BriefingOmittedByteLimit} {
			if count := values[section+"\x00"+reason]; count != 0 {
				result = append(result, domain.BriefingOmission{Section: section, Reason: reason, Count: count})
			}
		}
	}
	return result
}

func urgencyRank(value string) int {
	if value == domain.OutcomeAttentionNow {
		return 0
	}
	if value == domain.OutcomeAttentionNext {
		return 1
	}
	return 2
}
func urgencyForRisk(value string) string {
	if value == domain.OutcomeRiskCritical || value == domain.OutcomeRiskHigh {
		return domain.OutcomeAttentionNow
	}
	if value == domain.OutcomeRiskMedium {
		return domain.OutcomeAttentionNext
	}
	return domain.OutcomeAttentionLater
}
func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func managementBriefingFromRow(ctx context.Context, queries *dbgen.Queries, row dbgen.ManagementBriefing) (domain.ManagementBriefing, error) {
	var content managementBriefingContent
	if err := json.Unmarshal([]byte(row.ContentJson), &content); err != nil {
		return domain.ManagementBriefing{}, storageFailure("decode management briefing", err)
	}
	encoded, hash, err := canonicalContent(content)
	if err != nil || string(encoded) != row.ContentJson || hash != row.ContentSha256 || int64(len(encoded)) != row.ByteSize || row.ScopeType != content.Scope.Type || row.ScopeID != scopeID(content.Scope) || row.WorkspaceID != content.Scope.WorkspaceID || row.EventCursor != content.EventCursor || row.CutoffEventSequence != content.CutoffEventSequence || row.CheckpointID != content.CheckpointID || row.SinceEventSequence != content.SinceEventSequence || row.EvaluatedAt != content.EvaluatedAt || (row.CaughtUp != 0) != content.CaughtUp || stringValue(row.UnknownEventType) != content.UnknownEventType || int64Value(row.UnknownEventSequence) != content.UnknownEventSequence {
		return domain.ManagementBriefing{}, storageFailure("validate management briefing content", errors.New("briefing columns differ from canonical bounded content"))
	}
	receipt, receiptErr := queries.GetManagementBriefingReceipt(ctx, row.ID)
	if receiptErr != nil || receipt.SealedAt != row.CreatedAt || receipt.ClaimCount != int64(len(content.Claims)) {
		return domain.ManagementBriefing{}, storageFailure("validate complete management briefing receipt", errors.New("briefing completeness receipt is missing or differs from canonical content"))
	}
	claimRows, err := queries.ListManagementBriefingClaims(ctx, row.ID)
	if err != nil || len(claimRows) != len(content.Claims) {
		return domain.ManagementBriefing{}, storageFailure("validate management briefing claims", errors.New("briefing normalized claim count differs from content"))
	}
	actualSourceCount := int64(0)
	for index, stored := range claimRows {
		claim := content.Claims[index]
		claimJSON, _ := json.Marshal(claim)
		if stored.Ordinal != int64(index) || stored.ClaimID != claim.ID || stored.ClaimID != briefingClaimID(content.Scope, stored.SemanticKey, claim.Status, claim.Sources) || stored.Kind != claim.Kind || stored.Urgency != claim.Urgency || stored.Summary != claim.Summary || stored.Status != claim.Status || stringValue(stored.ProjectID) != claim.ProjectID || stored.SourceEventSequence != claim.SourceEventSequence || stored.ClaimJson != string(claimJSON) {
			return domain.ManagementBriefing{}, storageFailure("validate management briefing claim", errors.New("normalized claim differs from canonical briefing content"))
		}
		sourceRows, sourceErr := queries.ListManagementBriefingClaimSources(ctx, dbgen.ListManagementBriefingClaimSourcesParams{BriefingID: row.ID, ClaimID: claim.ID})
		if sourceErr != nil || len(sourceRows) != len(claim.Sources) {
			return domain.ManagementBriefing{}, storageFailure("validate management briefing provenance", errors.New("claim provenance count differs from canonical claim"))
		}
		actualSourceCount += int64(len(sourceRows))
		for sourceIndex, sourceRow := range sourceRows {
			source := claim.Sources[sourceIndex]
			if validateErr := validateBriefingSourceType(source); validateErr != nil {
				return domain.ManagementBriefing{}, validateErr
			}
			if sourceRow.Ordinal != int64(sourceIndex) || sourceRow.EntityType != source.EntityType || sourceRow.EntityID != source.EntityID || sourceRow.EntityRevision != source.Revision || sourceRow.ContentSha256 != source.ContentSHA256 || sourceRow.EventSequence != source.EventSequence || sourceRow.EvidenceClass != source.EvidenceClass || sourceRow.EvidenceEffect != source.EvidenceEffect || sourceRow.PinnedFreshness != source.PinnedFreshness || sourceRow.CurrentFreshness != source.CurrentFreshness {
				return domain.ManagementBriefing{}, storageFailure("validate management briefing provenance", errors.New("normalized provenance differs from canonical claim"))
			}
		}
	}
	if receipt.SourceCount != actualSourceCount {
		return domain.ManagementBriefing{}, storageFailure("validate complete management briefing receipt", errors.New("briefing provenance count differs from completeness receipt"))
	}
	return domain.ManagementBriefing{
		ID: row.ID, Revision: row.Revision, Scope: content.Scope, EventCursor: content.EventCursor, CutoffEventSequence: content.CutoffEventSequence,
		CheckpointID: content.CheckpointID, SinceEventSequence: content.SinceEventSequence, EvaluatedAt: content.EvaluatedAt,
		CaughtUp: content.CaughtUp, UnknownEventType: content.UnknownEventType, UnknownEventSequence: content.UnknownEventSequence,
		Claims: content.Claims, Omitted: content.Omitted, ContentSHA256: row.ContentSha256, ByteSize: int(row.ByteSize),
	}, nil
}

func (s *Store) ExplainManagementBriefingClaim(ctx context.Context, query ExplainManagementBriefingClaimQuery) (domain.BriefingClaimExplanation, error) {
	query.WorkspaceIdentifier = strings.TrimSpace(query.WorkspaceIdentifier)
	query.BriefingID = strings.TrimSpace(query.BriefingID)
	query.ClaimID = strings.TrimSpace(query.ClaimID)
	if query.WorkspaceIdentifier == "" || query.BriefingID == "" || query.ClaimID == "" {
		return domain.BriefingClaimExplanation{}, outcomeError(CodeInvalidManagementBriefing, "briefing explain requires exact briefing and claim ids")
	}
	workspace, err := s.Workspace(ctx, query.WorkspaceIdentifier)
	if err != nil {
		return domain.BriefingClaimExplanation{}, err
	}
	queries := dbgen.New(s.db)
	row, err := queries.GetManagementBriefing(ctx, dbgen.GetManagementBriefingParams{WorkspaceID: workspace.ID, ID: query.BriefingID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BriefingClaimExplanation{}, outcomeError(CodeManagementBriefingNotFound, "management briefing was not found")
	}
	if err != nil {
		return domain.BriefingClaimExplanation{}, storageFailure("read management briefing", err)
	}
	briefing, err := managementBriefingFromRow(ctx, queries, row)
	if err != nil {
		return domain.BriefingClaimExplanation{}, err
	}
	var claim *domain.BriefingClaim
	for index := range briefing.Claims {
		if briefing.Claims[index].ID == query.ClaimID {
			claim = &briefing.Claims[index]
			break
		}
	}
	if claim == nil {
		return domain.BriefingClaimExplanation{}, outcomeError(CodeBriefingClaimNotFound, "briefing claim was not found")
	}
	diagnoses := make([]string, 0, len(claim.Sources))
	for _, source := range claim.Sources {
		diagnosis, diagnosisErr := diagnoseBriefingSource(ctx, queries, workspace.ID, source, s.nowText())
		if diagnosisErr != nil {
			return domain.BriefingClaimExplanation{}, diagnosisErr
		}
		diagnoses = append(diagnoses, diagnosis)
	}
	return domain.BriefingClaimExplanation{BriefingID: briefing.ID, Claim: *claim, EvaluatedAt: s.nowText(), Provenance: append([]domain.BriefingClaimSource(nil), claim.Sources...), Diagnoses: diagnoses}, nil
}

func diagnoseBriefingSource(ctx context.Context, queries *dbgen.Queries, workspaceID string, source domain.BriefingClaimSource, evaluatedAt string) (string, error) {
	event, err := queries.GetOutcomeJournalEvent(ctx, source.EventSequence)
	if errors.Is(err, sql.ErrNoRows) {
		return "source journal event is missing", nil
	}
	if err != nil {
		return "", storageFailure("read briefing source event", err)
	}
	if event.WorkspaceID != workspaceID {
		return "source journal event belongs to another workspace", nil
	}
	if err := validateBriefingSourceType(source); err != nil {
		return "", err
	}
	switch source.EntityType {
	case "outcome_assessment":
		detail, readErr := outcomeAssessmentDetail(ctx, queries, workspaceID, source.EntityID, evaluatedAt)
		if readErr != nil {
			return "source outcome assessment is missing or invalid", nil
		}
		if detail.Assessment.ContentSHA256 != source.ContentSHA256 || detail.Assessment.StateRevision != source.Revision {
			return "source outcome assessment changed from the pinned revision", nil
		}
		if detail.Assessment.ReviewState != domain.OutcomeAssessmentAccepted {
			return "source outcome assessment is no longer current accepted governance", nil
		}
		return "source outcome assessment remains current and accepted", nil
	case "deliverable_commitment":
		value, readErr := deliverableCommitment(ctx, queries, workspaceID, source.EntityID)
		if readErr != nil {
			return "source deliverable commitment is missing or invalid", nil
		}
		if value.ContentSHA256 != source.ContentSHA256 {
			return "source deliverable commitment differs from the pinned content", nil
		}
		return "source deliverable commitment remains exact", nil
	case "outcome_assessment_acceptance_basis":
		basis, readErr := queries.GetOutcomeAssessmentAcceptanceBasis(ctx, source.EntityID)
		if readErr != nil || basis.SourceSha256 != source.ContentSHA256 || basis.EventSequence != source.EventSequence {
			return "owner acceptance basis is missing or differs from the pinned governance fact", nil
		}
		return "owner acceptance basis remains exact", nil
	case "knowledge_contradiction":
		value, readErr := queries.GetKnowledgeContradiction(ctx, dbgen.GetKnowledgeContradictionParams{WorkspaceID: workspaceID, ID: source.EntityID})
		if readErr != nil {
			return "knowledge contradiction is missing", nil
		}
		if value.StateRevision != source.Revision || value.Status != domain.KnowledgeContradictionOpen {
			return "knowledge contradiction is no longer open at the pinned revision", nil
		}
		return "knowledge contradiction remains open", nil
	case "knowledge_revision":
		value, readErr := queries.GetKnowledgeRevision(ctx, dbgen.GetKnowledgeRevisionParams{RevisionID: source.EntityID, WorkspaceID: workspaceID})
		if readErr != nil {
			return "knowledge revision is missing", nil
		}
		if value.ContentHash != source.ContentSHA256 || value.ReviewStatus != domain.KnowledgeReviewAccepted {
			return "knowledge revision no longer matches accepted pinned content", nil
		}
		return "knowledge revision remains accepted at pinned content", nil
	case "task":
		value, readErr := queries.GetOutcomeTaskRevision(ctx, dbgen.GetOutcomeTaskRevisionParams{WorkspaceID: workspaceID, TaskID: source.EntityID})
		if readErr != nil {
			return "follow-up task source is missing", nil
		}
		if value.Revision != source.Revision || value.EventSequence != source.EventSequence {
			return "follow-up task changed from its pinned revision", nil
		}
		return "follow-up task remains at the pinned revision", nil
	case domain.OutcomeEvidenceHandoff:
		value, readErr := queries.GetOutcomeHandoffEvidence(ctx, dbgen.GetOutcomeHandoffEvidenceParams{WorkspaceID: workspaceID, HandoffID: source.EntityID})
		if readErr != nil || value.SourceRevision != source.Revision || value.EventSequence != source.EventSequence {
			return "handoff source is missing or no longer matches pinned provenance", nil
		}
		return "handoff source remains exact", nil
	case domain.OutcomeEvidenceCheckRequirementEvidence:
		value, readErr := queries.GetOutcomeCheckEvidence(ctx, dbgen.GetOutcomeCheckEvidenceParams{WorkspaceID: workspaceID, EvidenceID: source.EntityID})
		if readErr != nil {
			return "mechanical evidence source is missing", nil
		}
		if value.RequirementStatus != domain.CheckRequirementActive || value.CurrentRequirementRevision != value.RequirementRevision || !interfaceBoolean(value.CurrentRequirementResult) {
			return "mechanical evidence is no longer the active latest exact requirement result", nil
		}
		if value.CurrentFreshness != domain.CheckFreshnessFresh {
			return "mechanical evidence is currently " + value.CurrentFreshness, nil
		}
		return "mechanical evidence remains current and fresh", nil
	default:
		return "source type is outside the current briefing provenance union", nil
	}
}

func validateBriefingSourceType(source domain.BriefingClaimSource) error {
	evidence := source.EntityType == domain.OutcomeEvidenceHandoff || source.EntityType == domain.OutcomeEvidenceCheckRequirementEvidence
	if evidence {
		if (source.EvidenceClass != domain.EvidenceAgentSelfReport && source.EvidenceClass != domain.EvidenceMechanicalCheck && source.EvidenceClass != domain.EvidenceIndependentReview) ||
			(source.EvidenceEffect != domain.CheckEvidenceSupports && source.EvidenceEffect != domain.CheckEvidenceContradicts && source.EvidenceEffect != domain.CheckEvidenceInconclusive) ||
			!validOutcomeFreshness(source.PinnedFreshness) || !validOutcomeFreshness(source.CurrentFreshness) {
			return storageFailure("validate briefing evidence provenance", errors.New("evidence source has incomplete derived trust metadata"))
		}
		return nil
	}
	if source.EvidenceClass != "" || source.EvidenceEffect != "" || source.PinnedFreshness != "" || source.CurrentFreshness != "" {
		return storageFailure("validate briefing provenance", errors.New("non-evidence source carries evidence trust metadata"))
	}
	switch source.EntityType {
	case "outcome_assessment", "deliverable_commitment", "outcome_assessment_acceptance_basis", "knowledge_contradiction", "knowledge_revision", "task":
		return nil
	default:
		return storageFailure("validate briefing provenance", errors.New("source type is outside the current closed union"))
	}
}

func validOutcomeFreshness(value string) bool {
	return value == domain.OutcomeEvidenceFresh || value == domain.OutcomeEvidenceStale || value == domain.OutcomeEvidenceUnknown
}
