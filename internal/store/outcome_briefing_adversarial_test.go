package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
)

func TestManagementBriefingUnchangedSemanticStateDoesNotGrowWithClock(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	now := time.Date(2037, 1, 2, 3, 4, 5, 0, time.UTC)
	storage.clock = func() time.Time { return now }
	fixture.createCommitment(t, "briefing-semantic-reuse")
	query := ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID,
		ScopeType:           domain.OwnerCheckpointTask,
		ScopeIdentifier:     fixture.task.Task.ID,
	}
	first, err := storage.ShowManagementBriefing(context.Background(), query)
	if err != nil {
		t.Fatalf("ShowManagementBriefing(first semantic snapshot) = %v", err)
	}
	if first.EvaluatedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("first evaluated_at = %q, want %q", first.EvaluatedAt, now.Format(time.RFC3339Nano))
	}
	var contentJSON string
	if err := storage.db.QueryRow(`SELECT content_json FROM management_briefings WHERE id=?`, first.ID).Scan(&contentJSON); err != nil {
		t.Fatalf("read first briefing content = %v", err)
	}
	if strings.Contains(contentJSON, `"evaluated_at"`) {
		t.Fatalf("semantic briefing content contains observation time: %s", contentJSON)
	}
	settled := map[string]int{}
	for _, table := range []string{"management_briefings", "management_briefing_claims", "management_briefing_claim_sources", "management_briefing_receipts", "events"} {
		settled[table] = countOutcomeFaultRows(t, storage, table)
	}
	for iteration := 1; iteration <= 100; iteration++ {
		now = now.Add(24 * time.Hour)
		replayed, replayErr := storage.ShowManagementBriefing(context.Background(), query)
		if replayErr != nil {
			t.Fatalf("ShowManagementBriefing(clock-only replay %d) = %v", iteration, replayErr)
		}
		if !reflect.DeepEqual(replayed, first) {
			t.Fatalf("clock-only replay %d changed immutable briefing\nfirst=%#v\nreplayed=%#v", iteration, first, replayed)
		}
	}
	for table, want := range settled {
		if got := countOutcomeFaultRows(t, storage, table); got != want {
			t.Fatalf("clock-only briefing reads grew %s: got %d, want %d", table, got, want)
		}
	}
}

func TestManagementBriefingByteLimitPersistsExactlyTheBoundedWholeClaims(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	for assessmentIndex := 0; assessmentIndex < 4; assessmentIndex++ {
		key := fmt.Sprintf("briefing-byte-pressure-%d", assessmentIndex)
		commitment := fixture.createCommitment(t, key)
		input := fullyBoundedUnknownOutcomeInput()
		for riskIndex := 0; riskIndex < 20; riskIndex++ {
			input.Risks = append(input.Risks, domain.OutcomeRiskInput{
				Severity: domain.OutcomeRiskCritical,
				Summary:  fmt.Sprintf("risk-%02d-%02d: ", assessmentIndex, riskIndex) + strings.Repeat("界", 620),
			})
		}
		fixture.acceptOutcomeInput(t, commitment.Commitment.ID, input, key)
	}

	briefing, err := storage.ShowManagementBriefing(context.Background(), ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID,
		ScopeType:           domain.OwnerCheckpointTask,
		ScopeIdentifier:     fixture.task.Task.ID,
	})
	if err != nil {
		t.Fatalf("ShowManagementBriefing(byte pressure) = %v", err)
	}
	if briefing.ByteSize < 1 || briefing.ByteSize > maximumBriefingBytes || len(briefing.Claims) > maximumBriefingClaims {
		t.Fatalf("bounded briefing = byte_size %d claims %d", briefing.ByteSize, len(briefing.Claims))
	}
	byteOmissions := 0
	for _, omission := range briefing.Omitted {
		if omission.Reason == domain.BriefingOmittedByteLimit {
			byteOmissions += omission.Count
		}
	}
	if byteOmissions == 0 {
		t.Fatalf("byte-pressure briefing omissions = %#v; want deterministic byte-limit omissions", briefing.Omitted)
	}
	var storedClaims, storedSources, contentBytes int
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM management_briefing_claims WHERE briefing_id=?`, briefing.ID).Scan(&storedClaims); err != nil {
		t.Fatalf("count persisted bounded claims = %v", err)
	}
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM management_briefing_claim_sources WHERE briefing_id=?`, briefing.ID).Scan(&storedSources); err != nil {
		t.Fatalf("count persisted bounded sources = %v", err)
	}
	if err := storage.db.QueryRow(`SELECT length(CAST(content_json AS BLOB)) FROM management_briefings WHERE id=?`, briefing.ID).Scan(&contentBytes); err != nil {
		t.Fatalf("read persisted briefing bytes = %v", err)
	}
	expectedSources := 0
	for _, claim := range briefing.Claims {
		expectedSources += len(claim.Sources)
	}
	if storedClaims != len(briefing.Claims) || storedSources != expectedSources || contentBytes != briefing.ByteSize {
		t.Fatalf("persisted bounded rows = claims %d/%d sources %d/%d bytes %d/%d", storedClaims, len(briefing.Claims), storedSources, expectedSources, contentBytes, briefing.ByteSize)
	}
	if _, err := storage.ExplainManagementBriefingClaim(context.Background(), ExplainManagementBriefingClaimQuery{
		WorkspaceIdentifier: fixture.workspace.ID, BriefingID: briefing.ID, ClaimID: briefing.Claims[0].ID,
	}); err != nil {
		t.Fatalf("ExplainManagementBriefingClaim(bounded persisted claim) = %v", err)
	}
}

func TestManagementBriefingSinceCheckpointRetainsOlderUnresolvedCurrentFacts(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	acceptedCommitment := fixture.createCommitment(t, "since-current-accepted")
	input := fullyBoundedUnknownOutcomeInput()
	input.Risks = []domain.OutcomeRiskInput{{Severity: domain.OutcomeRiskHigh, Summary: "older risk remains unresolved"}}
	fixture.acceptOutcomeInput(t, acceptedCommitment.Commitment.ID, input, "since-current-accepted")
	fixture.createCommitment(t, "since-current-unassessed")

	left := acceptedContradictionKnowledge(t, storage, fixture.workspace.ID, fixture.task.Task.ID,
		"since-current-left", "older left", "left claim", fixture.task.Task.ID)
	right := acceptedContradictionKnowledge(t, storage, fixture.workspace.ID, fixture.task.Task.ID,
		"since-current-right", "older right", "right claim", fixture.task.Task.ID)
	contradiction := openContradiction(t, storage, fixture.workspace.ID, left.Revision.ID, right.Revision.ID, "since-current")

	checkpoint, err := storage.CreateOwnerCheckpoint(context.Background(), CreateOwnerCheckpointCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointTask,
		ScopeIdentifier: fixture.task.Task.ID, IdempotencyKey: "since-current-checkpoint",
		CorrelationID: "request-since-current-checkpoint",
	})
	if err != nil {
		t.Fatalf("CreateOwnerCheckpoint() = %v", err)
	}
	briefing, err := storage.ShowManagementBriefing(context.Background(), ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointTask,
		ScopeIdentifier: fixture.task.Task.ID, SinceCheckpointID: checkpoint.Checkpoint.ID,
	})
	if err != nil {
		t.Fatalf("ShowManagementBriefing(since current facts) = %v", err)
	}
	wantKinds := map[string]bool{
		domain.BriefingClaimRisk:            false,
		domain.BriefingClaimUnknown:         false,
		domain.BriefingClaimUnmetCommitment: false,
		domain.BriefingClaimContradiction:   false,
	}
	for _, claim := range briefing.Claims {
		if _, wanted := wantKinds[claim.Kind]; wanted {
			wantKinds[claim.Kind] = true
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Errorf("since-checkpoint briefing omitted older unresolved current %s; claims=%#v", kind, briefing.Claims)
		}
	}
	if !briefingHasSource(briefing, "knowledge_contradiction", contradiction.ID) {
		t.Fatalf("since-checkpoint contradiction lacks exact source %s: %#v", contradiction.ID, briefing.Claims)
	}
}

func TestManagementBriefingSuccessorHasOneChangeWithPriorAndSuccessorProvenance(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "briefing-successor")
	first := fixture.acceptUnknown(t, commitment.Commitment.ID, "briefing-successor-first")
	checkpoint, err := storage.CreateOwnerCheckpoint(context.Background(), CreateOwnerCheckpointCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointProject,
		ScopeIdentifier: fixture.project.ID, IdempotencyKey: "briefing-successor-checkpoint",
		CorrelationID: "request-briefing-successor-checkpoint",
	})
	if err != nil {
		t.Fatalf("CreateOwnerCheckpoint() = %v", err)
	}
	secondProposal := fixture.proposeUnknown(t, commitment.Commitment.ID, first.Detail.Assessment.ID, "briefing-successor-second")
	second, err := storage.AcceptOutcomeAssessment(context.Background(), DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, AssessmentID: secondProposal.Detail.Assessment.ID,
		ExpectedStateRevision: secondProposal.Detail.Assessment.StateRevision, DecisionNote: "accept revised exact judgment",
		IdempotencyKey: "briefing-successor-second-accept", CorrelationID: "request-briefing-successor-second-accept",
	})
	if err != nil {
		t.Fatalf("AcceptOutcomeAssessment(successor) = %v", err)
	}
	briefing, err := storage.ShowManagementBriefing(context.Background(), ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointProject,
		ScopeIdentifier: fixture.project.ID, SinceCheckpointID: checkpoint.Checkpoint.ID,
	})
	if err != nil {
		t.Fatalf("ShowManagementBriefing(successor) = %v", err)
	}
	matching := 0
	for _, claim := range briefing.Claims {
		if claim.Kind != domain.BriefingClaimChange {
			continue
		}
		hasPrior, hasSuccessor := false, false
		for _, source := range claim.Sources {
			if source.EntityType != "outcome_assessment" {
				continue
			}
			hasPrior = hasPrior || source.EntityID == first.Detail.Assessment.ID
			hasSuccessor = hasSuccessor || source.EntityID == second.Detail.Assessment.ID
		}
		if hasPrior && hasSuccessor {
			matching++
		}
	}
	if matching != 1 {
		t.Fatalf("successor briefing has %d revision-change claims with exact prior/successor provenance; want 1: %#v", matching, briefing.Claims)
	}
}

func TestWorkspaceBriefingRoundRobinStaysInsideUrgencyBand(t *testing.T) {
	values := []briefingCandidateSeed{
		briefingOrderingCandidate("a-later-1", "prj_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", domain.OutcomeAttentionLater, 10),
		briefingOrderingCandidate("a-later-2", "prj_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", domain.OutcomeAttentionLater, 9),
		briefingOrderingCandidate("b-now-1", "prj_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", domain.OutcomeAttentionNow, 8),
		briefingOrderingCandidate("b-now-2", "prj_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", domain.OutcomeAttentionNow, 7),
		briefingOrderingCandidate("c-now-1", "prj_cccccccccccccccccccccccccccccccc", domain.OutcomeAttentionNow, 6),
		briefingOrderingCandidate("c-now-2", "prj_cccccccccccccccccccccccccccccccc", domain.OutcomeAttentionNow, 5),
	}
	ordered := fairBriefingSeedSection(values, true)
	wantIDs := []string{"b-now-1", "c-now-1", "b-now-2", "c-now-2", "a-later-1", "a-later-2"}
	if len(ordered) != len(wantIDs) {
		t.Fatalf("fair section length = %d, want %d", len(ordered), len(wantIDs))
	}
	for index, want := range wantIDs {
		if ordered[index].SourceID != want {
			t.Fatalf("fair section order[%d] = %s, want %s; full=%#v", index, ordered[index].SourceID, want, ordered)
		}
	}
}

func TestBriefingComposedMultibyteSummaryRemainsValidAndByteBounded(t *testing.T) {
	summary := "owner attention: " + strings.Repeat("界", 900)
	candidate := newBriefingCandidate(domain.BriefingScope{Type: domain.OwnerCheckpointWorkspace, WorkspaceID: "ws_11111111111111111111111111111111"},
		domain.BriefingSectionRequiredDecisions, domain.BriefingClaimRequiredDecision, "multibyte",
		domain.OutcomeAttentionNow, summary, domain.BriefingClaimStatusRequired, "", []domain.BriefingClaimSource{{
			EntityType: "task", EntityID: "task_22222222222222222222222222222222", Revision: 1,
			ContentSHA256: strings.Repeat("a", 64), EventSequence: 1,
		}})
	if !utf8.ValidString(candidate.Claim.Summary) || len(candidate.Claim.Summary) > 2048 || candidate.Claim.Summary == "" {
		t.Fatalf("composed multibyte summary = valid %t bytes %d value %q", utf8.ValidString(candidate.Claim.Summary), len(candidate.Claim.Summary), candidate.Claim.Summary)
	}
	encoded, err := json.Marshal(candidate.Claim)
	if err != nil || !json.Valid(encoded) {
		t.Fatalf("marshal bounded multibyte claim = %s, %v", encoded, err)
	}
}

func TestManagementBriefingSurfacesFollowUpTaskAsExactProvenance(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	followUp, err := storage.CreateTask(context.Background(), CreateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		ObjectiveID: fixture.objective.ID, Title: "Exact follow-up action",
		IdempotencyKey: "briefing-follow-up-task", CorrelationID: "request-briefing-follow-up-task",
	})
	if err != nil {
		t.Fatalf("CreateTask(follow up) = %v", err)
	}
	commitment := fixture.createCommitment(t, "briefing-follow-up")
	input := fullyBoundedUnknownOutcomeInput()
	input.FollowUpTaskIDs = []string{followUp.Detail.Task.ID}
	fixture.acceptOutcomeInput(t, commitment.Commitment.ID, input, "briefing-follow-up")
	briefing, err := storage.ShowManagementBriefing(context.Background(), ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointTask,
		ScopeIdentifier: fixture.task.Task.ID,
	})
	if err != nil {
		t.Fatalf("ShowManagementBriefing(follow up) = %v", err)
	}
	if !briefingHasSource(briefing, "task", followUp.Detail.Task.ID) {
		t.Fatalf("briefing omitted exact follow-up task provenance %s: %#v", followUp.Detail.Task.ID, briefing.Claims)
	}
}

func TestNarrowBriefingExcludesUnrelatedSameProjectContradiction(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	unrelated, err := storage.CreateTask(context.Background(), CreateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		ObjectiveID: fixture.objective.ID, Title: "Unrelated contradiction task",
		IdempotencyKey: "briefing-unrelated-contradiction-task", CorrelationID: "request-briefing-unrelated-contradiction-task",
	})
	if err != nil {
		t.Fatalf("CreateTask(unrelated contradiction) = %v", err)
	}
	relatedLeft := acceptedContradictionKnowledge(t, storage, fixture.workspace.ID, fixture.task.Task.ID,
		"briefing-related-left", "related left", "related left", fixture.task.Task.ID)
	relatedRight := acceptedContradictionKnowledge(t, storage, fixture.workspace.ID, fixture.task.Task.ID,
		"briefing-related-right", "related right", "related right", fixture.task.Task.ID)
	related := openContradiction(t, storage, fixture.workspace.ID, relatedLeft.Revision.ID, relatedRight.Revision.ID, "briefing-related")
	unrelatedLeft := acceptedContradictionKnowledge(t, storage, fixture.workspace.ID, unrelated.Detail.Task.ID,
		"briefing-unrelated-left", "unrelated left", "unrelated left", unrelated.Detail.Task.ID)
	unrelatedRight := acceptedContradictionKnowledge(t, storage, fixture.workspace.ID, unrelated.Detail.Task.ID,
		"briefing-unrelated-right", "unrelated right", "unrelated right", unrelated.Detail.Task.ID)
	unrelatedConflict := openContradiction(t, storage, fixture.workspace.ID, unrelatedLeft.Revision.ID, unrelatedRight.Revision.ID, "briefing-unrelated")

	briefing, err := storage.ShowManagementBriefing(context.Background(), ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointTask,
		ScopeIdentifier: fixture.task.Task.ID,
	})
	if err != nil {
		t.Fatalf("ShowManagementBriefing(narrow contradictions) = %v", err)
	}
	if !briefingHasSource(briefing, "knowledge_contradiction", related.ID) {
		t.Fatalf("task briefing omitted its exact contradiction %s: %#v", related.ID, briefing.Claims)
	}
	if briefingHasSource(briefing, "knowledge_contradiction", unrelatedConflict.ID) {
		t.Fatalf("task briefing leaked unrelated same-project contradiction %s: %#v", unrelatedConflict.ID, briefing.Claims)
	}
}

func (fixture outcomeAdversarialFixture) acceptOutcomeInput(t *testing.T, commitmentID string, input domain.OutcomeAssessmentInput, key string) OutcomeAssessmentMutationResult {
	t.Helper()
	proposed, err := fixture.storage.ProposeOutcomeAssessment(context.Background(), ProposeOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.task.Task.ID, CommitmentID: commitmentID,
		Input: input, IdempotencyKey: key + "-propose-custom", CorrelationID: "request-" + key + "-propose-custom",
	})
	if err != nil {
		t.Fatalf("ProposeOutcomeAssessment(%s) = %v", key, err)
	}
	accepted, err := fixture.storage.AcceptOutcomeAssessment(context.Background(), DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, AssessmentID: proposed.Detail.Assessment.ID,
		ExpectedStateRevision: proposed.Detail.Assessment.StateRevision, DecisionNote: "accept exact structured judgment",
		IdempotencyKey: key + "-accept-custom", CorrelationID: "request-" + key + "-accept-custom",
	})
	if err != nil {
		t.Fatalf("AcceptOutcomeAssessment(%s) = %v", key, err)
	}
	return accepted
}

func briefingHasSource(briefing domain.ManagementBriefing, entityType, entityID string) bool {
	for _, claim := range briefing.Claims {
		for _, source := range claim.Sources {
			if source.EntityType == entityType && source.EntityID == entityID {
				return true
			}
		}
	}
	return false
}

func briefingOrderingCandidate(id, projectID, urgency string, eventSequence int64) briefingCandidateSeed {
	return briefingCandidateSeed{
		Section: domain.BriefingSectionRisksUnknowns, Urgency: urgency, ProjectID: projectID,
		SourceEventSequence: eventSequence, SourceKind: briefingSourceAcceptedAssessment,
		SourceID: id, Variant: "risk", Ordinal: 0,
	}
}
