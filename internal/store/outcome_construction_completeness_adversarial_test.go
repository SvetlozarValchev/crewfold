package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestOutcomeAuthenticatedConstructionRejectsOmittedDeclaredChild(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "construction-child-completeness")
	input := fullyBoundedUnknownOutcomeInput()
	input.Unknowns = []domain.OutcomeUnknownInput{
		{Summary: "first exact unknown"},
		{Summary: "second exact unknown must not be omitted"},
	}
	assessmentID := "outassess_" + strings.Repeat("c", 32)
	content := outcomeAssessmentContent{
		WorkspaceID: fixture.workspace.ID, ProjectID: fixture.project.ID, ObjectiveID: fixture.objective.ID,
		TaskID: fixture.task.Task.ID, CommitmentID: commitment.Commitment.ID, Revision: 1, Input: input,
	}
	contentJSON, contentSHA, err := canonicalContent(content)
	if err != nil {
		t.Fatalf("canonicalContent(incomplete child fixture) = %v", err)
	}
	deliveredJSON, _ := json.Marshal(input.DeliveredScope)
	unmetJSON, _ := json.Marshal(input.UnmetScope)
	wantRollback := errors.New("rollback rejected incomplete assessment graph")
	acceptedIncomplete := errors.New("incomplete assessment graph was sealed")
	err = storage.withOutcomeMutation(context.Background(), "incomplete assessment construction", func(tx *sql.Tx) error {
		now := storage.nowText()
		if _, insertErr := tx.ExecContext(context.Background(), `INSERT INTO outcome_assessments(
id,workspace_id,project_id,objective_id,task_id,commitment_id,revision,state_revision,
review_state,conclusion,delivered_scope_json,unmet_scope_json,content_json,content_sha256,
supersedes_assessment_id,proposed_at,proposed_by,decided_at,decided_by,decision_note
) VALUES(?,?,?,?,?,?,1,1,'proposed',?,?,?,?,?,NULL,?,'local-owner',NULL,NULL,NULL)`,
			assessmentID, fixture.workspace.ID, fixture.project.ID, fixture.objective.ID, fixture.task.Task.ID,
			commitment.Commitment.ID, input.Conclusion, string(deliveredJSON), string(unmetJSON), string(contentJSON), contentSHA, now); insertErr != nil {
			return fmt.Errorf("insert incomplete assessment parent: %w", insertErr)
		}
		// Deliberately normalize only the first of the two unknowns declared in
		// the canonical parent JSON. Every row that does exist is individually
		// valid, so only the terminal submission seal can reject this omission.
		if _, insertErr := tx.ExecContext(context.Background(), `INSERT INTO outcome_assessment_unknowns(assessment_id,ordinal,summary) VALUES(?,0,?)`, assessmentID, input.Unknowns[0].Summary); insertErr != nil {
			return fmt.Errorf("insert valid first normalized child: %w", insertErr)
		}
		sequence, eventErr := appendEvent(context.Background(), tx, fixture.workspace.ID, "outcome_assessment", assessmentID, 1,
			outcomeAssessmentProposedEvent, "incomplete-assessment-construction", now, map[string]any{
				"commitment_id": commitment.Commitment.ID, "task_id": fixture.task.Task.ID, "assessment_revision": int64(1),
				"conclusion": input.Conclusion, "supersedes_assessment_id": "", "content_sha256": contentSHA,
			})
		if eventErr != nil {
			return fmt.Errorf("append incomplete assessment proposal event: %w", eventErr)
		}
		if _, insertErr := tx.ExecContext(context.Background(), `INSERT INTO outcome_assessment_submissions(assessment_id,event_sequence,child_count,submitted_at) VALUES(?,?,1,?)`, assessmentID, sequence, now); insertErr == nil {
			return acceptedIncomplete
		}
		return wantRollback
	})
	if errors.Is(err, acceptedIncomplete) {
		t.Fatal("authenticated construction sealed an assessment missing a child declared in canonical JSON")
	}
	if !errors.Is(err, wantRollback) {
		t.Fatalf("incomplete assessment construction = %v, want terminal seal rejection", err)
	}

	result, err := storage.ProposeOutcomeAssessment(context.Background(), ProposeOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.task.Task.ID,
		CommitmentID: commitment.Commitment.ID, Input: input,
		IdempotencyKey: "construction-child-completeness-control", CorrelationID: "request-construction-child-completeness-control",
	})
	if err != nil || len(result.Detail.Unknowns) != 2 {
		t.Fatalf("ProposeOutcomeAssessment(complete Store control) = %#v, %v", result, err)
	}
}

func TestSupersededOutcomeFactsAreHistoryNotCurrentBriefingClaims(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "superseded-facts-currentness")
	priorInput := fullyBoundedUnknownOutcomeInput()
	priorInput.Unknowns = []domain.OutcomeUnknownInput{{Summary: "obsolete uncertainty replaced by successor"}}
	prior := fixture.acceptOutcomeInput(t, commitment.Commitment.ID, priorInput, "superseded-facts-prior")

	currentInput := fullyBoundedUnknownOutcomeInput()
	currentInput.Unknowns = []domain.OutcomeUnknownInput{{Summary: "current successor uncertainty"}}
	proposed, err := storage.ProposeOutcomeAssessment(context.Background(), ProposeOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.task.Task.ID,
		CommitmentID: commitment.Commitment.ID, SupersedesAssessmentID: prior.Detail.Assessment.ID,
		Input: currentInput, IdempotencyKey: "superseded-facts-successor-propose", CorrelationID: "request-superseded-facts-successor-propose",
	})
	if err != nil {
		t.Fatalf("ProposeOutcomeAssessment(successor) = %v", err)
	}
	current, err := storage.AcceptOutcomeAssessment(context.Background(), DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, AssessmentID: proposed.Detail.Assessment.ID,
		ExpectedStateRevision: proposed.Detail.Assessment.StateRevision,
		DecisionNote:          "replace prior uncertainty with the successor judgment",
		IdempotencyKey:        "superseded-facts-successor-accept", CorrelationID: "request-superseded-facts-successor-accept",
	})
	if err != nil {
		t.Fatalf("AcceptOutcomeAssessment(successor) = %v", err)
	}
	briefing, err := storage.ShowManagementBriefing(context.Background(), ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointTask, ScopeIdentifier: fixture.task.Task.ID,
	})
	if err != nil {
		t.Fatalf("ShowManagementBriefing(successor currentness) = %v", err)
	}
	currentUnknowns, obsoleteUnknowns, successorChanges := 0, 0, 0
	for _, claim := range briefing.Claims {
		if claim.Kind == domain.BriefingClaimUnknown {
			if claim.Summary == currentInput.Unknowns[0].Summary {
				currentUnknowns++
			}
			if claim.Summary == priorInput.Unknowns[0].Summary {
				obsoleteUnknowns++
			}
		}
		if claim.Kind == domain.BriefingClaimChange && briefingClaimHasEntity(claim, "outcome_assessment", prior.Detail.Assessment.ID) && briefingClaimHasEntity(claim, "outcome_assessment", current.Detail.Assessment.ID) {
			successorChanges++
		}
	}
	if currentUnknowns != 1 || obsoleteUnknowns != 0 || successorChanges != 1 {
		t.Fatalf("successor briefing currentness = current unknown %d, obsolete unknown %d, history changes %d; claims=%#v", currentUnknowns, obsoleteUnknowns, successorChanges, briefing.Claims)
	}
}

func TestRejectingOutcomeSuccessorLeavesCurrentAcceptedAssessmentUnchanged(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "rejected-successor-governance")
	prior := fixture.acceptUnknown(t, commitment.Commitment.ID, "rejected-successor-prior")
	successor := fixture.proposeUnknown(t, commitment.Commitment.ID, prior.Detail.Assessment.ID, "rejected-successor")

	rejected, err := storage.RejectOutcomeAssessment(context.Background(), DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, AssessmentID: successor.Detail.Assessment.ID,
		ExpectedStateRevision: successor.Detail.Assessment.StateRevision,
		DecisionNote:          "retain the existing accepted judgment",
		IdempotencyKey:        "rejected-successor-reject", CorrelationID: "request-rejected-successor-reject",
	})
	if err != nil || rejected.Detail.Assessment.ReviewState != domain.OutcomeAssessmentRejected {
		t.Fatalf("RejectOutcomeAssessment(successor) = %#v, %v", rejected, err)
	}
	current, err := storage.OutcomeAssessment(context.Background(), fixture.workspace.ID, prior.Detail.Assessment.ID)
	if err != nil || current.Assessment.ReviewState != domain.OutcomeAssessmentAccepted {
		t.Fatalf("OutcomeAssessment(prior after rejected successor) = %#v, %v", current, err)
	}
	var supersessionRows int
	if err := storage.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE type='outcome.assessment_superseded' AND entity_id=?`, prior.Detail.Assessment.ID).Scan(&supersessionRows); err != nil || supersessionRows != 0 {
		t.Fatalf("supersession events after rejected successor = %d, %v; want 0", supersessionRows, err)
	}
}

func TestOutcomeAuthenticatedGovernanceRejectsUnsupportedAcceptanceAndForgedEvent(t *testing.T) {
	for _, test := range []struct {
		name        string
		input       domain.OutcomeAssessmentInput
		mutateEvent func(map[string]any)
	}{
		{
			name: "unsupported achieved acceptance",
			input: domain.OutcomeAssessmentInput{
				Conclusion: domain.OutcomeAchieved, DeliveredScope: []string{"declared delivery"}, UnmetScope: []string{},
				DecisionRevisionIDs: []string{}, Evidence: []domain.OutcomeEvidenceInput{}, Effects: []domain.OutcomeEffectInput{},
				Deviations: []domain.OutcomeDeviationInput{}, Risks: []domain.OutcomeRiskInput{}, Unknowns: []domain.OutcomeUnknownInput{},
				FollowUpTaskIDs: []string{}, OwnerAttention: []domain.OutcomeOwnerAttentionInput{},
			},
		},
		{
			name:  "forged decision event payload",
			input: fullyBoundedUnknownOutcomeInput(),
			mutateEvent: func(data map[string]any) {
				data["decision_note"] = "payload differs from immutable governance state"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, fixture := newOutcomeAdversarialFixture(t, false)
			commitment := fixture.createCommitment(t, "governance-seal-"+strings.ReplaceAll(test.name, " ", "-"))
			proposed, err := storage.ProposeOutcomeAssessment(context.Background(), ProposeOutcomeAssessmentCommand{
				WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.task.Task.ID,
				CommitmentID: commitment.Commitment.ID, Input: test.input,
				IdempotencyKey: "governance-seal-propose-" + strings.ReplaceAll(test.name, " ", "-"),
				CorrelationID:  "request-governance-seal-propose-" + strings.ReplaceAll(test.name, " ", "-"),
			})
			if err != nil {
				t.Fatalf("ProposeOutcomeAssessment() = %v", err)
			}
			wantRollback := errors.New("rollback rejected governance construction")
			acceptedForgery := errors.New("forged governance graph was sealed")
			err = storage.withOutcomeMutation(context.Background(), "forged governance construction", func(tx *sql.Tx) error {
				now := storage.nowText()
				decisionNote := "exact owner governance note"
				if _, updateErr := tx.ExecContext(context.Background(), `UPDATE outcome_assessments
SET review_state='accepted',state_revision=2,decided_at=?,decided_by='local-owner',decision_note=?
WHERE id=? AND review_state='proposed' AND state_revision=1`, now, decisionNote, proposed.Detail.Assessment.ID); updateErr != nil {
					return fmt.Errorf("update forged governance projection: %w", updateErr)
				}
				eventData := map[string]any{
					"commitment_id": commitment.Commitment.ID, "assessment_revision": proposed.Detail.Assessment.Revision,
					"conclusion": proposed.Detail.Assessment.Conclusion, "decision_note": decisionNote,
				}
				if test.mutateEvent != nil {
					test.mutateEvent(eventData)
				}
				sequence, eventErr := appendEvent(context.Background(), tx, fixture.workspace.ID, "outcome_assessment", proposed.Detail.Assessment.ID, 2,
					outcomeAssessmentAcceptedEvent, "forged-governance-construction", now, eventData)
				if eventErr != nil {
					return fmt.Errorf("append forged governance event: %w", eventErr)
				}
				basisSHA, hashErr := hashCommand("outcome.policy_acceptance", map[string]any{
					"assessment_id": proposed.Detail.Assessment.ID, "content_sha256": proposed.Detail.Assessment.ContentSHA256,
					"event_sequence": sequence, "state_revision": int64(2),
				})
				if hashErr != nil {
					return fmt.Errorf("hash forged governance basis: %w", hashErr)
				}
				if _, basisErr := tx.ExecContext(context.Background(), `INSERT INTO outcome_assessment_acceptance_basis(assessment_id,event_sequence,source_sha256,created_at,created_by) VALUES(?,?,?,?,'local-owner')`, proposed.Detail.Assessment.ID, sequence, basisSHA, now); basisErr != nil {
					return fmt.Errorf("insert otherwise exact acceptance basis: %w", basisErr)
				}
				if _, governanceErr := tx.ExecContext(context.Background(), `INSERT INTO outcome_assessment_governance(assessment_id,decision,decision_event_sequence,superseded_assessment_id,superseded_event_sequence,decided_at) VALUES(?,'accepted',?,NULL,NULL,?)`, proposed.Detail.Assessment.ID, sequence, now); governanceErr == nil {
					return acceptedForgery
				}
				return wantRollback
			})
			if errors.Is(err, acceptedForgery) {
				t.Fatalf("authenticated construction sealed %s", test.name)
			}
			if !errors.Is(err, wantRollback) {
				t.Fatalf("forged governance construction = %v, want terminal receipt rejection", err)
			}
		})
	}
}

func TestOutcomeAuthenticatedGovernanceRequiresAcceptanceBasis(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "governance-basis-required")
	proposed := fixture.proposeUnknown(t, commitment.Commitment.ID, "", "governance-basis-required")
	wantRollback := errors.New("rollback rejected acceptance without basis")
	acceptedWithoutBasis := errors.New("acceptance without basis was sealed")
	err := storage.withOutcomeMutation(context.Background(), "acceptance missing owner basis", func(tx *sql.Tx) error {
		now := storage.nowText()
		decisionNote := "owner note without the required durable basis"
		if _, updateErr := tx.ExecContext(context.Background(), `UPDATE outcome_assessments
SET review_state='accepted',state_revision=2,decided_at=?,decided_by='local-owner',decision_note=?
WHERE id=? AND review_state='proposed' AND state_revision=1`, now, decisionNote, proposed.Detail.Assessment.ID); updateErr != nil {
			return fmt.Errorf("update basis-less acceptance projection: %w", updateErr)
		}
		sequence, eventErr := appendEvent(context.Background(), tx, fixture.workspace.ID, "outcome_assessment", proposed.Detail.Assessment.ID, 2,
			outcomeAssessmentAcceptedEvent, "basis-less-acceptance", now, map[string]any{
				"commitment_id": commitment.Commitment.ID, "assessment_revision": proposed.Detail.Assessment.Revision,
				"conclusion": proposed.Detail.Assessment.Conclusion, "decision_note": decisionNote,
			})
		if eventErr != nil {
			return fmt.Errorf("append basis-less acceptance event: %w", eventErr)
		}
		// The parent and decision event are exact, but the mandatory owner-policy
		// acceptance basis row is deliberately absent.
		if _, governanceErr := tx.ExecContext(context.Background(), `INSERT INTO outcome_assessment_governance(
assessment_id,decision,decision_event_sequence,superseded_assessment_id,superseded_event_sequence,decided_at
) VALUES(?,'accepted',?,NULL,NULL,?)`, proposed.Detail.Assessment.ID, sequence, now); governanceErr == nil {
			return acceptedWithoutBasis
		}
		return wantRollback
	})
	if errors.Is(err, acceptedWithoutBasis) {
		t.Fatal("authenticated construction sealed an accepted assessment without its owner acceptance basis")
	}
	if !errors.Is(err, wantRollback) {
		t.Fatalf("basis-less acceptance construction = %v, want governance receipt rejection", err)
	}
	if _, err := storage.AcceptOutcomeAssessment(context.Background(), DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, AssessmentID: proposed.Detail.Assessment.ID,
		ExpectedStateRevision: 1, DecisionNote: "owner accepts with an exact durable basis",
		IdempotencyKey: "governance-basis-required-control", CorrelationID: "request-governance-basis-required-control",
	}); err != nil {
		t.Fatalf("AcceptOutcomeAssessment(complete basis control) = %v", err)
	}
}

func TestOutcomeAuthenticatedGovernanceRequiresSuccessorSupersessionPair(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "governance-successor-pair-required")
	prior := fixture.acceptUnknown(t, commitment.Commitment.ID, "governance-successor-pair-prior")
	proposed := fixture.proposeUnknown(t, commitment.Commitment.ID, prior.Detail.Assessment.ID, "governance-successor-pair-next")
	wantRollback := errors.New("rollback rejected successor without supersession pair")
	acceptedWithoutPair := errors.New("successor without supersession pair was sealed")
	err := storage.withOutcomeMutation(context.Background(), "successor acceptance missing supersession pair", func(tx *sql.Tx) error {
		now := storage.nowText()
		decisionNote := "accept successor while maliciously omitting its supersession pair"
		if _, updateErr := tx.ExecContext(context.Background(), `UPDATE outcome_assessments
SET review_state='superseded',state_revision=state_revision+1
WHERE id=? AND review_state='accepted'`, prior.Detail.Assessment.ID); updateErr != nil {
			return fmt.Errorf("supersede prior for pair-less successor: %w", updateErr)
		}
		if _, eventErr := appendEvent(context.Background(), tx, fixture.workspace.ID, "outcome_assessment", prior.Detail.Assessment.ID, 3,
			outcomeAssessmentSupersededEvent, "pair-less-successor", now, map[string]any{
				"successor_assessment_id": proposed.Detail.Assessment.ID, "commitment_id": commitment.Commitment.ID,
			}); eventErr != nil {
			return fmt.Errorf("append otherwise exact supersession event: %w", eventErr)
		}
		if _, updateErr := tx.ExecContext(context.Background(), `UPDATE outcome_assessments
SET review_state='accepted',state_revision=2,decided_at=?,decided_by='local-owner',decision_note=?
WHERE id=? AND review_state='proposed' AND state_revision=1`, now, decisionNote, proposed.Detail.Assessment.ID); updateErr != nil {
			return fmt.Errorf("update pair-less successor projection: %w", updateErr)
		}
		sequence, eventErr := appendEvent(context.Background(), tx, fixture.workspace.ID, "outcome_assessment", proposed.Detail.Assessment.ID, 2,
			outcomeAssessmentAcceptedEvent, "pair-less-successor", now, map[string]any{
				"commitment_id": commitment.Commitment.ID, "assessment_revision": proposed.Detail.Assessment.Revision,
				"conclusion": proposed.Detail.Assessment.Conclusion, "decision_note": decisionNote,
			})
		if eventErr != nil {
			return fmt.Errorf("append pair-less successor acceptance event: %w", eventErr)
		}
		basisSHA, hashErr := hashCommand("outcome.policy_acceptance", map[string]any{
			"assessment_id": proposed.Detail.Assessment.ID, "content_sha256": proposed.Detail.Assessment.ContentSHA256,
			"event_sequence": sequence, "state_revision": int64(2),
		})
		if hashErr != nil {
			return fmt.Errorf("hash pair-less successor basis: %w", hashErr)
		}
		if _, basisErr := tx.ExecContext(context.Background(), `INSERT INTO outcome_assessment_acceptance_basis(
assessment_id,event_sequence,source_sha256,created_at,created_by
) VALUES(?,?,?,?,'local-owner')`, proposed.Detail.Assessment.ID, sequence, basisSHA, now); basisErr != nil {
			return fmt.Errorf("insert otherwise exact successor basis: %w", basisErr)
		}
		// The assessment canonically names the prior accepted assessment, but
		// both governance supersession columns are deliberately omitted.
		if _, governanceErr := tx.ExecContext(context.Background(), `INSERT INTO outcome_assessment_governance(
assessment_id,decision,decision_event_sequence,superseded_assessment_id,superseded_event_sequence,decided_at
) VALUES(?,'accepted',?,NULL,NULL,?)`, proposed.Detail.Assessment.ID, sequence, now); governanceErr == nil {
			return acceptedWithoutPair
		}
		return wantRollback
	})
	if errors.Is(err, acceptedWithoutPair) {
		t.Fatal("authenticated construction sealed an accepted successor without its supersession pair")
	}
	if !errors.Is(err, wantRollback) {
		t.Fatalf("pair-less successor construction = %v, want governance receipt rejection", err)
	}
	accepted, err := storage.AcceptOutcomeAssessment(context.Background(), DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, AssessmentID: proposed.Detail.Assessment.ID,
		ExpectedStateRevision: 1, DecisionNote: "accept successor with its exact supersession pair",
		IdempotencyKey: "governance-successor-pair-control", CorrelationID: "request-governance-successor-pair-control",
	})
	if err != nil || accepted.Detail.Assessment.SupersedesAssessmentID != prior.Detail.Assessment.ID {
		t.Fatalf("AcceptOutcomeAssessment(complete successor control) = %#v, %v", accepted, err)
	}
}

func TestRejectingProposedSuccessorLeavesPriorCurrentAndReplaysExactly(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "reject-proposed-successor")
	prior := fixture.acceptUnknown(t, commitment.Commitment.ID, "reject-proposed-successor-prior")
	proposed := fixture.proposeUnknown(t, commitment.Commitment.ID, prior.Detail.Assessment.ID, "reject-proposed-successor-next")
	command := DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, AssessmentID: proposed.Detail.Assessment.ID,
		ExpectedStateRevision: proposed.Detail.Assessment.StateRevision, DecisionNote: "reject this successor without changing current truth",
		IdempotencyKey: "reject-proposed-successor-decision", CorrelationID: "request-reject-proposed-successor-decision",
	}
	before := adversarialOutcomeEventCount(t, storage, fixture.workspace.ID)
	rejected, err := storage.RejectOutcomeAssessment(context.Background(), command)
	if err != nil {
		t.Fatalf("RejectOutcomeAssessment(successor) = %v", err)
	}
	if rejected.Detail.Assessment.ReviewState != domain.OutcomeAssessmentRejected || rejected.Detail.Assessment.SupersedesAssessmentID != prior.Detail.Assessment.ID {
		t.Fatalf("rejected successor state = %#v", rejected.Detail.Assessment)
	}
	if after := adversarialOutcomeEventCount(t, storage, fixture.workspace.ID); after != before+1 {
		t.Fatalf("reject successor outcome event count = %d, want %d", after, before+1)
	}
	currentPrior, err := storage.OutcomeAssessment(context.Background(), fixture.workspace.ID, prior.Detail.Assessment.ID)
	if err != nil || currentPrior.Assessment.ReviewState != domain.OutcomeAssessmentAccepted || currentPrior.Assessment.StateRevision != 2 {
		t.Fatalf("prior after successor rejection = %#v, %v; want unchanged accepted revision 2", currentPrior.Assessment, err)
	}
	var decision string
	var supersededID, supersededSequence sql.NullString
	if err := storage.db.QueryRowContext(context.Background(), `SELECT decision,superseded_assessment_id,CAST(superseded_event_sequence AS TEXT)
FROM outcome_assessment_governance WHERE assessment_id=?`, proposed.Detail.Assessment.ID).Scan(&decision, &supersededID, &supersededSequence); err != nil {
		t.Fatalf("read rejected successor governance = %v", err)
	}
	if decision != domain.OutcomeAssessmentRejected || supersededID.Valid || supersededSequence.Valid {
		t.Fatalf("rejected successor governance = decision %q, superseded id %#v, sequence %#v; want no supersession pair", decision, supersededID, supersededSequence)
	}
	var supersededEffects int
	if err := storage.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events
WHERE workspace_id=? AND entity_type='outcome_assessment' AND entity_id=? AND type=?`,
		fixture.workspace.ID, prior.Detail.Assessment.ID, outcomeAssessmentSupersededEvent).Scan(&supersededEffects); err != nil {
		t.Fatalf("count rejected-successor supersession effects = %v", err)
	}
	if supersededEffects != 0 {
		t.Fatalf("rejecting a proposed successor emitted %d supersession effects, want 0", supersededEffects)
	}
	accepted, err := storage.OutcomeAssessments(context.Background(), ListOutcomeAssessmentsQuery{
		WorkspaceIdentifier: fixture.workspace.ID, CommitmentID: commitment.Commitment.ID,
		ReviewState: domain.OutcomeAssessmentAccepted, Limit: 100,
	})
	if err != nil || len(accepted) != 1 || accepted[0].Assessment.ID != prior.Detail.Assessment.ID {
		t.Fatalf("accepted outcomes after successor rejection = %#v, %v; want only prior", accepted, err)
	}
	replay, err := storage.RejectOutcomeAssessment(context.Background(), command)
	if err != nil {
		t.Fatalf("RejectOutcomeAssessment(exact replay) = %v", err)
	}
	if !reflect.DeepEqual(replay, rejected) {
		t.Fatalf("rejected successor replay differs:\nfirst=%#v\nreplay=%#v", rejected, replay)
	}
	if afterReplay := adversarialOutcomeEventCount(t, storage, fixture.workspace.ID); afterReplay != before+1 {
		t.Fatalf("rejected successor replay appended an effect: event count %d, want %d", afterReplay, before+1)
	}
}

func adversarialOutcomeEventCount(t *testing.T, storage *Store, workspaceID string) int {
	t.Helper()
	var count int
	if err := storage.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE workspace_id=? AND type LIKE 'outcome.%'`, workspaceID).Scan(&count); err != nil {
		t.Fatalf("count outcome events = %v", err)
	}
	return count
}

func TestManagementBriefingAuthenticatedConstructionRejectsOmittedDeclaredClaim(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	first := fixture.createCommitment(t, "briefing-completeness-first")
	second := fixture.createCommitment(t, "briefing-completeness-second")
	scope := adversarialTaskBriefingScope(fixture)
	firstCandidate := newBriefingCandidate(scope, domain.BriefingSectionDeviationsUnmet, domain.BriefingClaimUnmetCommitment,
		"unassessed", domain.OutcomeAttentionNow, first.Commitment.Title+": no owner-accepted outcome assessment",
		domain.BriefingClaimStatusUnmet, fixture.project.ID, []domain.BriefingClaimSource{{
			EntityType: "deliverable_commitment", EntityID: first.Commitment.ID, Revision: 1,
			ContentSHA256: first.Commitment.ContentSHA256, EventSequence: first.EventSequence,
		}})
	secondCandidate := newBriefingCandidate(scope, domain.BriefingSectionDeviationsUnmet, domain.BriefingClaimUnmetCommitment,
		"unassessed", domain.OutcomeAttentionNow, second.Commitment.Title+": no owner-accepted outcome assessment",
		domain.BriefingClaimStatusUnmet, fixture.project.ID, []domain.BriefingClaimSource{{
			EntityType: "deliverable_commitment", EntityID: second.Commitment.ID, Revision: 1,
			ContentSHA256: second.Commitment.ContentSHA256, EventSequence: second.EventSequence,
		}})
	content, contentJSON, contentSHA := adversarialCanonicalBriefingContent(t, storage, fixture.workspace.ID, scope,
		[]briefingCandidate{firstCandidate, secondCandidate})

	wantRollback := errors.New("rollback rejected incomplete briefing graph")
	acceptedIncomplete := errors.New("incomplete briefing graph was sealed")
	err := storage.withOutcomeMutation(context.Background(), "incomplete management briefing construction", func(tx *sql.Tx) error {
		briefingID := "briefing_" + strings.Repeat("d", 32)
		if insertErr := adversarialInsertBriefingParent(context.Background(), tx, briefingID, content, contentJSON, contentSHA); insertErr != nil {
			return fmt.Errorf("insert incomplete briefing parent: %w", insertErr)
		}
		if insertErr := adversarialInsertBriefingClaim(context.Background(), tx, briefingID, 0, firstCandidate); insertErr != nil {
			return fmt.Errorf("insert complete first briefing claim: %w", insertErr)
		}
		if insertErr := adversarialInsertBriefingSource(context.Background(), tx, briefingID, firstCandidate.Claim.ID, 0, firstCandidate.Claim.Sources[0]); insertErr != nil {
			return fmt.Errorf("insert complete first briefing provenance: %w", insertErr)
		}
		// The canonical parent declares two claims, but only the first complete
		// claim graph exists. Lowering the receipt counts to the actual rows must
		// not turn that omitted suffix into an authenticated briefing.
		if _, insertErr := tx.ExecContext(context.Background(), `INSERT INTO management_briefing_receipts(briefing_id,claim_count,source_count,sealed_at) VALUES(?,1,1,?)`, briefingID, content.EvaluatedAt); insertErr == nil {
			return acceptedIncomplete
		}
		return wantRollback
	})
	if errors.Is(err, acceptedIncomplete) {
		t.Fatal("authenticated construction sealed a briefing missing a claim declared in canonical JSON")
	}
	if !errors.Is(err, wantRollback) {
		t.Fatalf("incomplete briefing construction = %v, want terminal receipt rejection", err)
	}

	briefing, err := storage.ShowManagementBriefing(context.Background(), ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointTask, ScopeIdentifier: fixture.task.Task.ID,
	})
	if err != nil || len(briefing.Claims) < 2 {
		t.Fatalf("ShowManagementBriefing(complete Store control) = %#v, %v", briefing, err)
	}
}

func TestManagementBriefingAuthenticatedConstructionRejectsForgedProvenanceEvent(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "briefing-forged-provenance-event")
	var unrelatedSequence int64
	if err := storage.db.QueryRowContext(context.Background(), `SELECT sequence FROM events WHERE workspace_id=? AND sequence<>? ORDER BY sequence LIMIT 1`, fixture.workspace.ID, commitment.EventSequence).Scan(&unrelatedSequence); err != nil {
		t.Fatalf("find unrelated source event = %v", err)
	}
	scope := adversarialTaskBriefingScope(fixture)
	forgedCandidate := newBriefingCandidate(scope, domain.BriefingSectionDeviationsUnmet, domain.BriefingClaimUnmetCommitment,
		"unassessed", domain.OutcomeAttentionNow, commitment.Commitment.Title+": no owner-accepted outcome assessment",
		domain.BriefingClaimStatusUnmet, fixture.project.ID, []domain.BriefingClaimSource{{
			EntityType: "deliverable_commitment", EntityID: commitment.Commitment.ID, Revision: 1,
			ContentSHA256: commitment.Commitment.ContentSHA256, EventSequence: unrelatedSequence,
		}})
	content, contentJSON, contentSHA := adversarialCanonicalBriefingContent(t, storage, fixture.workspace.ID, scope, []briefingCandidate{forgedCandidate})
	wantRollback := errors.New("rollback rejected forged briefing provenance")
	acceptedForgery := errors.New("forged briefing provenance was sealed")
	err := storage.withOutcomeMutation(context.Background(), "forged management briefing provenance", func(tx *sql.Tx) error {
		briefingID := "briefing_" + strings.Repeat("e", 32)
		if insertErr := adversarialInsertBriefingParent(context.Background(), tx, briefingID, content, contentJSON, contentSHA); insertErr != nil {
			return fmt.Errorf("insert forged-provenance briefing parent: %w", insertErr)
		}
		if insertErr := adversarialInsertBriefingClaim(context.Background(), tx, briefingID, 0, forgedCandidate); insertErr != nil {
			return fmt.Errorf("insert forged-provenance briefing claim: %w", insertErr)
		}
		// The claim JSON and normalized row agree with each other, but the event
		// belongs to a different entity. Provenance must be derived from the
		// referenced durable record rather than accepted as a caller assertion.
		if insertErr := adversarialInsertBriefingSource(context.Background(), tx, briefingID, forgedCandidate.Claim.ID, 0, forgedCandidate.Claim.Sources[0]); insertErr == nil {
			return acceptedForgery
		}
		return wantRollback
	})
	if errors.Is(err, acceptedForgery) {
		t.Fatal("authenticated construction sealed briefing provenance pointing at an unrelated event")
	}
	if !errors.Is(err, wantRollback) {
		t.Fatalf("forged briefing provenance construction = %v, want semantic source-event rejection", err)
	}
}

func adversarialTaskBriefingScope(fixture outcomeAdversarialFixture) domain.BriefingScope {
	return domain.BriefingScope{
		Type: domain.OwnerCheckpointTask, WorkspaceID: fixture.workspace.ID, ProjectID: fixture.project.ID,
		ObjectiveID: fixture.objective.ID, TaskID: fixture.task.Task.ID,
	}
}

func adversarialCanonicalBriefingContent(t *testing.T, storage *Store, workspaceID string, scope domain.BriefingScope, candidates []briefingCandidate) (managementBriefingContent, []byte, string) {
	t.Helper()
	var cutoff int64
	if err := storage.db.QueryRowContext(context.Background(), `SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?`, workspaceID).Scan(&cutoff); err != nil {
		t.Fatalf("capture adversarial briefing cutoff = %v", err)
	}
	projection, err := storage.advanceOutcomeProjector(context.Background(), workspaceID, cutoff)
	if err != nil {
		t.Fatalf("advance adversarial briefing projector = %v", err)
	}
	content := managementBriefingContent{
		Scope: scope, EventCursor: projection.Cursor, CutoffEventSequence: cutoff, SinceEventSequence: 0,
		EvaluatedAt: storage.nowText(), CaughtUp: true, Claims: claimsFromCandidates(candidates), Omitted: []domain.BriefingOmission{},
	}
	contentJSON, contentSHA, err := canonicalContent(content)
	if err != nil {
		t.Fatalf("canonicalContent(adversarial briefing) = %v", err)
	}
	return content, contentJSON, contentSHA
}

func adversarialInsertBriefingParent(ctx context.Context, tx *sql.Tx, briefingID string, content managementBriefingContent, contentJSON []byte, contentSHA string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO management_briefings(
id,revision,workspace_id,scope_type,scope_id,event_cursor,cutoff_event_sequence,checkpoint_id,since_event_sequence,
evaluated_at,caught_up,unknown_event_type,unknown_event_sequence,content_json,content_sha256,byte_size,created_at
) VALUES(?,1,?,?,?,?,?, '',0,?,1,NULL,NULL,?,?,?,?)`,
		briefingID, content.Scope.WorkspaceID, content.Scope.Type, scopeID(content.Scope), content.EventCursor,
		content.CutoffEventSequence, content.EvaluatedAt, string(contentJSON), contentSHA, len(contentJSON), content.EvaluatedAt)
	return err
}

func adversarialInsertBriefingClaim(ctx context.Context, tx *sql.Tx, briefingID string, ordinal int64, candidate briefingCandidate) error {
	claimJSON, err := json.Marshal(candidate.Claim)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO management_briefing_claims(
briefing_id,ordinal,claim_id,semantic_key,kind,urgency,summary,status,project_id,source_event_sequence,claim_json
) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, briefingID, ordinal, candidate.Claim.ID, candidate.SemanticKey, candidate.Claim.Kind,
		candidate.Claim.Urgency, candidate.Claim.Summary, candidate.Claim.Status, candidate.Claim.ProjectID,
		candidate.Claim.SourceEventSequence, string(claimJSON))
	return err
}

func adversarialInsertBriefingSource(ctx context.Context, tx *sql.Tx, briefingID, claimID string, ordinal int64, source domain.BriefingClaimSource) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO management_briefing_claim_sources(
briefing_id,claim_id,ordinal,entity_type,entity_id,entity_revision,content_sha256,event_sequence,
evidence_class,evidence_effect,pinned_freshness,current_freshness
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, briefingID, claimID, ordinal, source.EntityType, source.EntityID, source.Revision,
		source.ContentSHA256, source.EventSequence, source.EvidenceClass, source.EvidenceEffect, source.PinnedFreshness, source.CurrentFreshness)
	return err
}

func briefingClaimHasEntity(claim domain.BriefingClaim, entityType, entityID string) bool {
	for _, source := range claim.Sources {
		if source.EntityType == entityType && source.EntityID == entityID {
			return true
		}
	}
	return false
}
