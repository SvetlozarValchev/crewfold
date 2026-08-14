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
	SourceKind  string
	SourceID    string
	Variant     string
	Ordinal     int64
}

type briefingCandidateSeed struct {
	Section             string
	Urgency             string
	ProjectID           string
	SourceEventSequence int64
	SourceKind          string
	SourceID            string
	Variant             string
	Ordinal             int64
	SourceRevision      int64
	ContentSHA256       string
	Summary             string
}

type briefingCandidateSeedRow struct {
	Seed         briefingCandidateSeed
	SectionTotal int64
}

type acceptedBriefingSource struct {
	AssessmentID            string
	StateRevision           int64
	ReviewState             string
	Conclusion              string
	ContentSHA256           string
	ProjectID               string
	ObjectiveID             string
	TaskID                  string
	CommitmentID            string
	CommitmentTitle         string
	CommitmentSHA256        string
	CommitmentEventSequence int64
	EventSequence           int64
	AcceptanceBasisSHA256   string
	SupersededAssessmentID  *string
	SupersededEventSequence *int64
	SupersededContentSHA256 *string
	SupersededStateRevision *int64
}

const (
	briefingSourceAcceptedAssessment   = "accepted_assessment"
	briefingSourceUnassessedCommitment = "unassessed_commitment"
	briefingSourceOpenContradiction    = "open_contradiction"
)

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
		selected, omissions, candidateErr := buildBriefingCandidates(ctx, queries, workspaceID, scope, sinceSequence, projection.Cursor, evaluatedAt)
		if candidateErr != nil {
			return candidateErr
		}
		content := managementBriefingContent{
			Scope: scope, EventCursor: projection.Cursor, CutoffEventSequence: cutoff, CheckpointID: checkpointID,
			SinceEventSequence: sinceSequence, CaughtUp: projection.Cursor == cutoff && projection.UnknownEventType == "",
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

func buildBriefingCandidates(ctx context.Context, queries *dbgen.Queries, workspaceID string, scope domain.BriefingScope, sinceSequence, cursor int64, evaluatedAt string) ([]briefingCandidate, []domain.BriefingOmission, error) {
	rows := make([]briefingCandidateSeedRow, 0, maximumBriefingClaims)
	staticRows, err := queries.ListStaticManagementBriefingCandidateSeeds(ctx, dbgen.ListStaticManagementBriefingCandidateSeedsParams{
		SinceSequence: sinceSequence, WorkspaceID: workspaceID, EventCursor: cursor,
		ProjectID: scope.ProjectID, ObjectiveID: scope.ObjectiveID, TaskID: scope.TaskID,
	})
	if err != nil {
		return nil, nil, storageFailure("list static management briefing seeds", err)
	}
	for _, row := range staticRows {
		rows = append(rows, briefingCandidateSeedRow{Seed: briefingCandidateSeed{
			Section: row.Section, Urgency: row.Urgency, ProjectID: row.ProjectID,
			SourceEventSequence: row.SourceEventSequence, SourceKind: row.SourceKind, SourceID: row.SourceID,
			Variant: row.Variant, Ordinal: row.ChildOrdinal, SourceRevision: row.SourceRevision,
			ContentSHA256: row.ContentSha256, Summary: row.Summary,
		}, SectionTotal: row.SectionTotal})
	}
	decisionRows, err := queries.ListDecisionManagementBriefingCandidateSeeds(ctx, dbgen.ListDecisionManagementBriefingCandidateSeedsParams{
		SinceSequence: sinceSequence, WorkspaceID: workspaceID, EventCursor: cursor,
		ProjectID: scope.ProjectID, ObjectiveID: scope.ObjectiveID, TaskID: scope.TaskID, EvaluatedAt: evaluatedAt,
	})
	if err != nil {
		return nil, nil, storageFailure("list decision management briefing seeds", err)
	}
	decisionBatch := make([]briefingCandidateSeedRow, 0, len(decisionRows))
	for _, row := range decisionRows {
		decisionBatch = append(decisionBatch, briefingCandidateSeedRow{Seed: briefingCandidateSeed{
			Section: row.Section, Urgency: row.Urgency, ProjectID: row.ProjectID,
			SourceEventSequence: row.SourceEventSequence, SourceKind: row.SourceKind, SourceID: row.SourceID,
			Variant: row.Variant, Ordinal: row.ChildOrdinal, SourceRevision: row.SourceRevision,
			ContentSHA256: row.ContentSha256, Summary: row.Summary,
		}, SectionTotal: row.SectionTotal})
	}
	evidenceRows, err := queries.ListEvidenceManagementBriefingCandidateSeeds(ctx, dbgen.ListEvidenceManagementBriefingCandidateSeedsParams{
		WorkspaceID: workspaceID, EventCursor: cursor,
		ProjectID: scope.ProjectID, ObjectiveID: scope.ObjectiveID, TaskID: scope.TaskID,
	})
	if err != nil {
		return nil, nil, storageFailure("list evidence management briefing seeds", err)
	}
	evidenceBatch := make([]briefingCandidateSeedRow, 0, len(evidenceRows))
	for _, row := range evidenceRows {
		evidenceBatch = append(evidenceBatch, briefingCandidateSeedRow{Seed: briefingCandidateSeed{
			Section: row.Section, Urgency: row.Urgency, ProjectID: row.ProjectID,
			SourceEventSequence: row.SourceEventSequence, SourceKind: row.SourceKind, SourceID: row.SourceID,
			Variant: row.Variant, Ordinal: row.ChildOrdinal, SourceRevision: row.SourceRevision,
			ContentSHA256: row.ContentSha256, Summary: row.Summary,
		}, SectionTotal: row.SectionTotal})
	}
	unassessedRows, err := queries.ListUnassessedManagementBriefingCandidateSeeds(ctx, dbgen.ListUnassessedManagementBriefingCandidateSeedsParams{
		WorkspaceID: workspaceID, EventCursor: cursor,
		ProjectID: scope.ProjectID, ObjectiveID: scope.ObjectiveID, TaskID: scope.TaskID,
	})
	if err != nil {
		return nil, nil, storageFailure("list unassessed management briefing seeds", err)
	}
	unassessedBatch := make([]briefingCandidateSeedRow, 0, len(unassessedRows))
	for _, row := range unassessedRows {
		unassessedBatch = append(unassessedBatch, briefingCandidateSeedRow{Seed: briefingCandidateSeed{
			Section: row.Section, Urgency: row.Urgency, ProjectID: row.ProjectID,
			SourceEventSequence: row.SourceEventSequence, SourceKind: row.SourceKind, SourceID: row.SourceID,
			Variant: row.Variant, Ordinal: row.ChildOrdinal, SourceRevision: row.SourceRevision,
			ContentSHA256: row.ContentSha256, Summary: row.Summary,
		}, SectionTotal: row.SectionTotal})
	}
	contradictionRows, err := queries.ListContradictionManagementBriefingCandidateSeeds(ctx, dbgen.ListContradictionManagementBriefingCandidateSeedsParams{
		WorkspaceID: workspaceID, EventCursor: cursor, ProjectID: scope.ProjectID,
		ScopeType: scope.Type, TaskID: scope.TaskID, ObjectiveID: scope.ObjectiveID,
	})
	if err != nil {
		return nil, nil, storageFailure("list contradiction management briefing seeds", err)
	}
	contradictionBatch := make([]briefingCandidateSeedRow, 0, len(contradictionRows))
	for _, row := range contradictionRows {
		contradictionBatch = append(contradictionBatch, briefingCandidateSeedRow{Seed: briefingCandidateSeed{
			Section: row.Section, Urgency: row.Urgency, ProjectID: row.ProjectID,
			SourceEventSequence: row.SourceEventSequence, SourceKind: row.SourceKind, SourceID: row.SourceID,
			Variant: row.Variant, Ordinal: row.ChildOrdinal, SourceRevision: row.SourceRevision,
			ContentSHA256: row.ContentSha256, Summary: row.Summary,
		}, SectionTotal: row.SectionTotal})
	}

	all := make([]briefingCandidateSeed, 0, len(rows)+len(decisionBatch)+len(evidenceBatch)+len(unassessedBatch)+len(contradictionBatch))
	totals := make(map[string]int64)
	for name, batch := range map[string][]briefingCandidateSeedRow{
		"static": rows, "decision": decisionBatch, "evidence": evidenceBatch,
		"unassessed": unassessedBatch, "contradiction": contradictionBatch,
	} {
		if err := mergeBriefingSeedBatch(name, len(batch), batch, totals); err != nil {
			return nil, nil, err
		}
		for _, row := range batch {
			all = append(all, row.Seed)
		}
	}
	selectedSeeds, omissions, err := boundBriefingCandidateSeeds(scope, all, totals)
	if err != nil {
		return nil, nil, err
	}
	selected := make([]briefingCandidate, 0, len(selectedSeeds))
	acceptedCache := make(map[string]map[string]briefingCandidate)
	for _, seed := range selectedSeeds {
		candidate, expandErr := expandBriefingCandidateSeed(ctx, queries, workspaceID, scope, cursor, evaluatedAt, seed, acceptedCache)
		if expandErr != nil {
			return nil, nil, expandErr
		}
		if candidate.Section != seed.Section || candidate.Claim.Urgency != seed.Urgency ||
			candidate.Claim.ProjectID != seed.ProjectID || candidate.Claim.SourceEventSequence != seed.SourceEventSequence ||
			candidate.SourceKind != seed.SourceKind || candidate.SourceID != seed.SourceID ||
			candidate.Variant != seed.Variant || candidate.Ordinal != seed.Ordinal {
			return nil, nil, storageFailure("validate management briefing seed expansion", errors.New("selected source seed did not expand one-to-one"))
		}
		selected = append(selected, candidate)
	}
	return selected, omissions, nil
}

func mergeBriefingSeedBatch(name string, rowCount int, rows []briefingCandidateSeedRow, totals map[string]int64) error {
	if rowCount != len(rows) || len(rows) > maximumBriefingClaims*len(briefingSectionOrder) {
		return storageFailure("validate bounded management briefing seeds", fmt.Errorf("%s seed query returned %d rows", name, len(rows)))
	}
	sectionTotals := make(map[string]int64)
	sectionRows := make(map[string]int64)
	for _, row := range rows {
		if err := validateBriefingCandidateSeed(row.Seed); err != nil {
			return err
		}
		if row.SectionTotal <= 0 {
			return storageFailure("validate management briefing seed counts", errors.New("seed query returned a non-positive section total"))
		}
		if prior, exists := sectionTotals[row.Seed.Section]; exists && prior != row.SectionTotal {
			return storageFailure("validate management briefing seed counts", errors.New("seed query returned inconsistent section totals"))
		}
		sectionTotals[row.Seed.Section] = row.SectionTotal
		sectionRows[row.Seed.Section]++
	}
	if totals == nil {
		return nil
	}
	for section, count := range sectionTotals {
		if count < sectionRows[section] {
			return storageFailure("validate management briefing seed counts", errors.New("seed query returned more rows than its exact section total"))
		}
		totals[section] += count
	}
	return nil
}

func validateBriefingCandidateSeed(seed briefingCandidateSeed) error {
	validUrgency := seed.Urgency == domain.OutcomeAttentionNow || seed.Urgency == domain.OutcomeAttentionNext || seed.Urgency == domain.OutcomeAttentionLater
	if _, ok := briefingSectionQuota[seed.Section]; !ok || !validUrgency ||
		seed.ProjectID == "" || seed.SourceEventSequence <= 0 || seed.SourceID == "" ||
		seed.SourceRevision <= 0 || seed.Ordinal < -1 || seed.Ordinal > 31 {
		return storageFailure("validate management briefing seed", errors.New("seed fields are outside the closed bounded contract"))
	}
	valid := false
	switch seed.SourceKind {
	case briefingSourceAcceptedAssessment:
		valid = seed.ContentSHA256 != "" && map[string]string{
			"attention":        domain.BriefingSectionRequiredDecisions,
			"follow_up":        domain.BriefingSectionRequiredDecisions,
			"risk":             domain.BriefingSectionRisksUnknowns,
			"unknown":          domain.BriefingSectionRisksUnknowns,
			"evidence_gap":     domain.BriefingSectionVerificationGaps,
			"decision_gap":     domain.BriefingSectionVerificationGaps,
			"deviation":        domain.BriefingSectionDeviationsUnmet,
			"unmet":            domain.BriefingSectionDeviationsUnmet,
			"delivery":         domain.BriefingSectionAcceptedDelivery,
			"delivery_revised": domain.BriefingSectionRationaleChange,
			"effect":           domain.BriefingSectionRationaleChange,
			"decision":         domain.BriefingSectionRationaleChange,
		}[seed.Variant] == seed.Section
	case briefingSourceUnassessedCommitment:
		valid = seed.Variant == "unassessed" && seed.Ordinal == -1 && seed.SourceRevision == 1 &&
			seed.ContentSHA256 != "" && seed.Section == domain.BriefingSectionDeviationsUnmet
	case briefingSourceOpenContradiction:
		valid = seed.Variant == "contradiction" && seed.Ordinal == -1 && seed.ContentSHA256 == "" &&
			seed.Section == domain.BriefingSectionContradictions
	}
	if !valid {
		return storageFailure("validate management briefing seed", errors.New("seed source kind and variant are outside the closed union"))
	}
	return nil
}

func boundBriefingCandidateSeeds(scope domain.BriefingScope, seeds []briefingCandidateSeed, totals map[string]int64) ([]briefingCandidateSeed, []domain.BriefingOmission, error) {
	seen := make(map[string]struct{}, len(seeds))
	bySection := make(map[string][]briefingCandidateSeed)
	for _, seed := range seeds {
		key := briefingSeedKey(seed)
		if _, duplicate := seen[key]; duplicate {
			return nil, nil, storageFailure("validate management briefing seeds", errors.New("duplicate semantic seed"))
		}
		seen[key] = struct{}{}
		bySection[seed.Section] = append(bySection[seed.Section], seed)
	}
	for _, section := range briefingSectionOrder {
		bySection[section] = fairBriefingSeedSection(bySection[section], scope.Type == domain.OwnerCheckpointWorkspace)
	}
	selected := make([]briefingCandidateSeed, 0, maximumBriefingClaims)
	remainders := make([]briefingCandidateSeed, 0)
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
	selectedBySection := make(map[string]int64)
	for _, seed := range selected {
		selectedBySection[seed.Section]++
	}
	omitted := make(map[string]int)
	for _, section := range briefingSectionOrder {
		if totals[section] < selectedBySection[section] {
			return nil, nil, storageFailure("validate management briefing omissions", errors.New("selected seeds exceed the exact candidate total"))
		}
		if count := totals[section] - selectedBySection[section]; count != 0 {
			omitted[section+"\x00"+domain.BriefingOmittedClaimLimit] = int(count)
		}
	}
	return selected, briefingOmissions(omitted), nil
}

func fairBriefingSeedSection(values []briefingCandidateSeed, roundRobin bool) []briefingCandidateSeed {
	less := func(left, right briefingCandidateSeed) bool {
		if urgencyRank(left.Urgency) != urgencyRank(right.Urgency) {
			return urgencyRank(left.Urgency) < urgencyRank(right.Urgency)
		}
		if left.SourceEventSequence != right.SourceEventSequence {
			return left.SourceEventSequence > right.SourceEventSequence
		}
		return briefingSeedKey(left) < briefingSeedKey(right)
	}
	if !roundRobin {
		sort.Slice(values, func(i, j int) bool { return less(values[i], values[j]) })
		return values
	}
	byUrgency := map[string][]briefingCandidateSeed{}
	for _, value := range values {
		byUrgency[value.Urgency] = append(byUrgency[value.Urgency], value)
	}
	result := make([]briefingCandidateSeed, 0, len(values))
	for _, urgency := range []string{domain.OutcomeAttentionNow, domain.OutcomeAttentionNext, domain.OutcomeAttentionLater} {
		groups := make(map[string][]briefingCandidateSeed)
		projects := make([]string, 0)
		for _, value := range byUrgency[urgency] {
			if _, exists := groups[value.ProjectID]; !exists {
				projects = append(projects, value.ProjectID)
			}
			groups[value.ProjectID] = append(groups[value.ProjectID], value)
		}
		sort.Strings(projects)
		for _, project := range projects {
			sort.Slice(groups[project], func(i, j int) bool { return less(groups[project][i], groups[project][j]) })
		}
		for index := 0; len(result) < len(values); index++ {
			added := false
			for _, project := range projects {
				if index < len(groups[project]) {
					result = append(result, groups[project][index])
					added = true
				}
			}
			if !added {
				break
			}
		}
	}
	return result
}

func briefingSeedKey(seed briefingCandidateSeed) string {
	return seed.SourceKind + "\x00" + seed.SourceID + "\x00" + seed.Variant + fmt.Sprintf("\x00%02d", seed.Ordinal)
}

func expandBriefingCandidateSeed(ctx context.Context, queries *dbgen.Queries, workspaceID string, scope domain.BriefingScope, cursor int64, evaluatedAt string, seed briefingCandidateSeed, acceptedCache map[string]map[string]briefingCandidate) (briefingCandidate, error) {
	switch seed.SourceKind {
	case briefingSourceAcceptedAssessment:
		candidates, exists := acceptedCache[seed.SourceID]
		if !exists {
			row, err := queries.GetAcceptedOutcomeAssessmentClaimSource(ctx, dbgen.GetAcceptedOutcomeAssessmentClaimSourceParams{
				WorkspaceID: workspaceID, AssessmentID: seed.SourceID, EventCursor: cursor,
			})
			if err != nil {
				return briefingCandidate{}, storageFailure("read selected accepted outcome briefing source", err)
			}
			source := acceptedBriefingSource{
				AssessmentID: row.AssessmentID, StateRevision: row.StateRevision, ReviewState: row.ReviewState,
				Conclusion: row.Conclusion, ContentSHA256: row.ContentSha256, ProjectID: row.ProjectID,
				ObjectiveID: row.ObjectiveID, TaskID: row.TaskID, CommitmentID: row.CommitmentID,
				CommitmentTitle: row.CommitmentTitle, CommitmentSHA256: row.CommitmentSha256,
				CommitmentEventSequence: row.CommitmentEventSequence, EventSequence: row.EventSequence,
				AcceptanceBasisSHA256:  row.AcceptanceBasisSha256,
				SupersededAssessmentID: row.SupersededAssessmentID, SupersededEventSequence: row.SupersededEventSequence,
				SupersededContentSHA256: row.SupersededContentSha256, SupersededStateRevision: row.SupersededStateRevision,
			}
			detail, detailErr := outcomeAssessmentDetail(ctx, queries, workspaceID, seed.SourceID, evaluatedAt)
			if detailErr != nil {
				return briefingCandidate{}, detailErr
			}
			if detail.Assessment.ID != source.AssessmentID || detail.Assessment.StateRevision != source.StateRevision ||
				detail.Assessment.ReviewState != source.ReviewState || detail.Assessment.Conclusion != source.Conclusion ||
				detail.Assessment.ContentSHA256 != source.ContentSHA256 || detail.Assessment.ProjectID != source.ProjectID ||
				detail.Assessment.ObjectiveID != source.ObjectiveID || detail.Assessment.TaskID != source.TaskID ||
				detail.Commitment.ID != source.CommitmentID || detail.Commitment.Title != source.CommitmentTitle ||
				detail.Commitment.ContentSHA256 != source.CommitmentSHA256 {
				return briefingCandidate{}, storageFailure("validate selected accepted outcome briefing source", errors.New("source head differs from authenticated assessment detail"))
			}
			generated, generateErr := acceptedAssessmentBriefingCandidates(ctx, queries, scope, source, detail)
			if generateErr != nil {
				return briefingCandidate{}, generateErr
			}
			candidates = make(map[string]briefingCandidate, len(generated))
			for _, candidate := range generated {
				key := candidate.Variant + fmt.Sprintf("/%d", candidate.Ordinal)
				if _, duplicate := candidates[key]; duplicate {
					return briefingCandidate{}, storageFailure("validate accepted outcome briefing expansion", errors.New("duplicate candidate variant"))
				}
				candidates[key] = candidate
			}
			acceptedCache[seed.SourceID] = candidates
		}
		if seed.SourceRevision <= 0 || seed.ContentSHA256 == "" {
			return briefingCandidate{}, storageFailure("validate selected accepted outcome briefing seed", errors.New("accepted source seal is incomplete"))
		}
		candidate, exists := candidates[seed.Variant+fmt.Sprintf("/%d", seed.Ordinal)]
		if !exists {
			return briefingCandidate{}, storageFailure("validate selected accepted outcome briefing seed", errors.New("selected seed has no exact authenticated claim"))
		}
		sealed := false
		for _, source := range candidate.Claim.Sources {
			if source.EntityType == "outcome_assessment" && source.EntityID == seed.SourceID &&
				source.Revision == seed.SourceRevision && source.ContentSHA256 == seed.ContentSHA256 {
				sealed = true
				break
			}
		}
		if !sealed {
			return briefingCandidate{}, storageFailure("validate selected accepted outcome briefing seed", errors.New("seed seal differs from authenticated assessment source"))
		}
		return candidate, nil
	case briefingSourceUnassessedCommitment:
		commitment, err := deliverableCommitment(ctx, queries, workspaceID, seed.SourceID)
		if err != nil {
			return briefingCandidate{}, err
		}
		if commitment.ProjectID != seed.ProjectID || commitment.ContentSHA256 != seed.ContentSHA256 ||
			commitment.Title != seed.Summary || seed.SourceRevision != 1 {
			return briefingCandidate{}, storageFailure("validate selected unassessed commitment seed", errors.New("seed differs from authenticated commitment"))
		}
		source := domain.BriefingClaimSource{
			EntityType: "deliverable_commitment", EntityID: commitment.ID, Revision: 1,
			ContentSHA256: commitment.ContentSHA256, EventSequence: seed.SourceEventSequence,
		}
		candidate := newBriefingCandidate(scope, domain.BriefingSectionDeviationsUnmet,
			domain.BriefingClaimUnmetCommitment, "unassessed", domain.OutcomeAttentionNow,
			commitment.Title+": no owner-accepted outcome assessment", domain.BriefingClaimStatusUnmet,
			commitment.ProjectID, []domain.BriefingClaimSource{source})
		return tagBriefingCandidate(candidate, seed.SourceKind, seed.SourceID, seed.Variant, seed.Ordinal), nil
	case briefingSourceOpenContradiction:
		source := domain.BriefingClaimSource{
			EntityType: "knowledge_contradiction", EntityID: seed.SourceID,
			Revision: seed.SourceRevision, EventSequence: seed.SourceEventSequence,
		}
		candidate := newBriefingCandidate(scope, domain.BriefingSectionContradictions,
			domain.BriefingClaimContradiction, "contradiction", domain.OutcomeAttentionNow,
			seed.Summary, domain.BriefingClaimStatusOpen, seed.ProjectID, []domain.BriefingClaimSource{source})
		return tagBriefingCandidate(candidate, seed.SourceKind, seed.SourceID, seed.Variant, seed.Ordinal), nil
	default:
		return briefingCandidate{}, storageFailure("expand management briefing seed", errors.New("unknown source kind"))
	}
}

func acceptedAssessmentBriefingCandidates(ctx context.Context, queries *dbgen.Queries, scope domain.BriefingScope, row acceptedBriefingSource, detail domain.OutcomeAssessmentDetail) ([]briefingCandidate, error) {
	base := []domain.BriefingClaimSource{
		{EntityType: "outcome_assessment", EntityID: row.AssessmentID, Revision: row.StateRevision, ContentSHA256: row.ContentSHA256, EventSequence: row.EventSequence},
		{EntityType: "deliverable_commitment", EntityID: row.CommitmentID, Revision: 1, ContentSHA256: row.CommitmentSHA256, EventSequence: row.CommitmentEventSequence},
		{EntityType: "outcome_assessment_acceptance_basis", EntityID: row.AssessmentID, Revision: 1, ContentSHA256: row.AcceptanceBasisSHA256, EventSequence: row.EventSequence},
	}
	result := make([]briefingCandidate, 0)
	add := func(candidate briefingCandidate, variant string, ordinal int64) {
		result = append(result, tagBriefingCandidate(candidate, briefingSourceAcceptedAssessment, row.AssessmentID, variant, ordinal))
	}
	deliverySources := append([]domain.BriefingClaimSource(nil), base...)
	for _, evidence := range detail.Evidence {
		deliverySources = append(deliverySources, briefingEvidenceSource(evidence))
	}
	for index, attention := range detail.OwnerAttention {
		add(newBriefingCandidate(scope, domain.BriefingSectionRequiredDecisions, domain.BriefingClaimRequiredDecision,
			fmt.Sprintf("attention/%d", index), attention.Urgency, attention.Action+": "+attention.Reason,
			domain.BriefingClaimStatusRequired, row.ProjectID, base), "attention", int64(index))
	}
	for index, followup := range detail.FollowUpTasks {
		taskSource := domain.BriefingClaimSource{EntityType: "task", EntityID: followup.TaskID, Revision: followup.TaskRevision, EventSequence: followup.EventSequence}
		add(newBriefingCandidate(scope, domain.BriefingSectionRequiredDecisions, domain.BriefingClaimRequiredDecision,
			fmt.Sprintf("follow-up/%d", index), domain.OutcomeAttentionNext,
			"Follow-up task requires owner tracking: "+followup.TaskID, domain.BriefingClaimStatusRequired,
			row.ProjectID, appendSources(base, taskSource)), "follow_up", int64(index))
	}
	for index, risk := range detail.Risks {
		add(newBriefingCandidate(scope, domain.BriefingSectionRisksUnknowns, domain.BriefingClaimRisk,
			fmt.Sprintf("risk/%d", index), urgencyForRisk(risk.Severity), risk.Summary, risk.Severity,
			row.ProjectID, base), "risk", int64(index))
	}
	for index, unknown := range detail.Unknowns {
		add(newBriefingCandidate(scope, domain.BriefingSectionRisksUnknowns, domain.BriefingClaimUnknown,
			fmt.Sprintf("unknown/%d", index), domain.OutcomeAttentionNow, unknown.Summary,
			domain.BriefingClaimStatusOpen, row.ProjectID, base), "unknown", int64(index))
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
		summary := evidence.Diagnosis
		if summary == "" {
			summary = "Pinned outcome evidence requires owner verification"
		}
		add(newBriefingCandidate(scope, domain.BriefingSectionVerificationGaps,
			domain.BriefingClaimVerificationGap, fmt.Sprintf("evidence/%d", index),
			domain.OutcomeAttentionNow, summary, status, row.ProjectID,
			appendSources(base, briefingEvidenceSource(evidence))), "evidence_gap", int64(index))
	}
	for index, decision := range detail.Decisions {
		decisionEvent, err := queries.GetOutcomeJournalEvent(ctx, decision.EventSequence)
		if err != nil || decisionEvent.EntityType != "knowledge_revision" || decisionEvent.EntityID != decision.RevisionID {
			return nil, storageFailure("validate briefing decision provenance", errors.New("decision acceptance event differs from pinned revision"))
		}
		decisionSource := domain.BriefingClaimSource{
			EntityType: "knowledge_revision", EntityID: decision.RevisionID, Revision: decisionEvent.EntityRevision,
			ContentSHA256: decision.ContentSHA256, EventSequence: decision.EventSequence,
		}
		if !decision.Current || decision.Disputed {
			status := domain.BriefingClaimStatusStale
			if decision.Disputed {
				status = domain.BriefingClaimStatusDisputed
			}
			add(newBriefingCandidate(scope, domain.BriefingSectionVerificationGaps,
				domain.BriefingClaimVerificationGap, fmt.Sprintf("decision-gap/%d", index),
				domain.OutcomeAttentionNow, "Accepted outcome decision knowledge requires current verification",
				status, row.ProjectID, appendSources(base, decisionSource)), "decision_gap", int64(index))
		}
		add(newBriefingCandidate(scope, domain.BriefingSectionRationaleChange,
			domain.BriefingClaimRationale, fmt.Sprintf("decision/%d", index),
			domain.OutcomeAttentionLater, "Accepted decision informs the outcome: "+decision.RevisionID,
			domain.BriefingClaimStatusAccepted, row.ProjectID, appendSources(base, decisionSource)), "decision", int64(index))
	}
	for index, deviation := range detail.Deviations {
		add(newBriefingCandidate(scope, domain.BriefingSectionDeviationsUnmet,
			domain.BriefingClaimDeviation, fmt.Sprintf("deviation/%d", index),
			domain.OutcomeAttentionNext, deviation.Summary, domain.BriefingClaimStatusRecorded,
			row.ProjectID, base), "deviation", int64(index))
	}
	if len(detail.UnmetScope) != 0 {
		add(newBriefingCandidate(scope, domain.BriefingSectionDeviationsUnmet,
			domain.BriefingClaimUnmetCommitment, "unmet", domain.OutcomeAttentionNow,
			row.CommitmentTitle+": "+strings.Join(detail.UnmetScope, "; "),
			domain.BriefingClaimStatusUnmet, row.ProjectID, base), "unmet", -1)
	}
	add(newBriefingCandidate(scope, domain.BriefingSectionAcceptedDelivery,
		domain.BriefingClaimAcceptedDelivery, "delivery", domain.OutcomeAttentionLater,
		row.CommitmentTitle+": owner accepted outcome as "+row.Conclusion, row.Conclusion,
		row.ProjectID, deliverySources), "delivery", -1)
	if row.SupersededAssessmentID != nil && row.SupersededEventSequence != nil &&
		row.SupersededContentSHA256 != nil && row.SupersededStateRevision != nil {
		priorSource := domain.BriefingClaimSource{
			EntityType: "outcome_assessment", EntityID: *row.SupersededAssessmentID,
			Revision: *row.SupersededStateRevision, ContentSHA256: *row.SupersededContentSHA256,
			EventSequence: *row.SupersededEventSequence,
		}
		add(newBriefingCandidate(scope, domain.BriefingSectionRationaleChange,
			domain.BriefingClaimChange, "delivery-revised", domain.OutcomeAttentionNext,
			row.CommitmentTitle+": owner revised the accepted delivery judgment",
			domain.BriefingClaimStatusRecorded, row.ProjectID, appendSources(base, priorSource)),
			"delivery_revised", -1)
	}
	for index, effect := range detail.Effects {
		add(newBriefingCandidate(scope, domain.BriefingSectionRationaleChange,
			domain.BriefingClaimChange, fmt.Sprintf("effect/%d", index), domain.OutcomeAttentionLater,
			effect.Summary, domain.BriefingClaimStatusRecorded, row.ProjectID, base), "effect", int64(index))
	}
	return result, nil
}

func tagBriefingCandidate(candidate briefingCandidate, sourceKind, sourceID, variant string, ordinal int64) briefingCandidate {
	candidate.SourceKind = sourceKind
	candidate.SourceID = sourceID
	candidate.Variant = variant
	candidate.Ordinal = ordinal
	return candidate
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
	if err != nil || string(encoded) != row.ContentJson || hash != row.ContentSha256 || int64(len(encoded)) != row.ByteSize || row.ScopeType != content.Scope.Type || row.ScopeID != scopeID(content.Scope) || row.WorkspaceID != content.Scope.WorkspaceID || row.EventCursor != content.EventCursor || row.CutoffEventSequence != content.CutoffEventSequence || row.CheckpointID != content.CheckpointID || row.SinceEventSequence != content.SinceEventSequence || (row.CaughtUp != 0) != content.CaughtUp || stringValue(row.UnknownEventType) != content.UnknownEventType || int64Value(row.UnknownEventSequence) != content.UnknownEventSequence {
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
	allSourceRows, err := queries.ListManagementBriefingClaimSourcesForBriefing(ctx, row.ID)
	if err != nil {
		return domain.ManagementBriefing{}, storageFailure("read management briefing provenance", err)
	}
	sourcesByClaim := make(map[string][]dbgen.ManagementBriefingClaimSource)
	for _, sourceRow := range allSourceRows {
		sourcesByClaim[sourceRow.ClaimID] = append(sourcesByClaim[sourceRow.ClaimID], sourceRow)
	}
	actualSourceCount := int64(0)
	for index, stored := range claimRows {
		claim := content.Claims[index]
		claimJSON, _ := json.Marshal(claim)
		if stored.Ordinal != int64(index) || stored.ClaimID != claim.ID || stored.ClaimID != briefingClaimID(content.Scope, stored.SemanticKey, claim.Status, claim.Sources) || stored.Kind != claim.Kind || stored.Urgency != claim.Urgency || stored.Summary != claim.Summary || stored.Status != claim.Status || stringValue(stored.ProjectID) != claim.ProjectID || stored.SourceEventSequence != claim.SourceEventSequence || stored.ClaimJson != string(claimJSON) {
			return domain.ManagementBriefing{}, storageFailure("validate management briefing claim", errors.New("normalized claim differs from canonical briefing content"))
		}
		sourceRows := sourcesByClaim[claim.ID]
		delete(sourcesByClaim, claim.ID)
		if len(sourceRows) != len(claim.Sources) {
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
	if len(sourcesByClaim) != 0 || receipt.SourceCount != actualSourceCount || actualSourceCount != int64(len(allSourceRows)) {
		return domain.ManagementBriefing{}, storageFailure("validate complete management briefing receipt", errors.New("briefing provenance count differs from completeness receipt"))
	}
	return domain.ManagementBriefing{
		ID: row.ID, Revision: row.Revision, Scope: content.Scope, EventCursor: content.EventCursor, CutoffEventSequence: content.CutoffEventSequence,
		CheckpointID: content.CheckpointID, SinceEventSequence: content.SinceEventSequence, EvaluatedAt: row.EvaluatedAt,
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
