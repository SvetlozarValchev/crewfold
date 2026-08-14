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
	"crewfold/internal/store/dbgen"
)

func TestOutcomeRawSQLRejectsUnauthenticatedInsertUpdateAndDelete(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "raw-unauthenticated")

	attacks := []struct {
		name      string
		statement string
		arguments []any
	}{
		{
			name:      "insert projector state",
			statement: `INSERT INTO outcome_projector_state(workspace_id,last_event_sequence,revision,updated_at) VALUES(?,0,1,?)`,
			arguments: []any{fixture.workspace.ID, storage.nowText()},
		},
		{
			name:      "update immutable commitment",
			statement: `UPDATE deliverable_commitments SET title=title WHERE id=?`,
			arguments: []any{commitment.Commitment.ID},
		},
		{
			name:      "delete immutable commitment",
			statement: `DELETE FROM deliverable_commitments WHERE id=?`,
			arguments: []any{commitment.Commitment.ID},
		},
	}
	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			if _, err := storage.db.ExecContext(context.Background(), attack.statement, attack.arguments...); err == nil {
				t.Fatalf("unauthenticated raw SQL attack succeeded: %s", attack.statement)
			}
		})
	}

	read, err := storage.DeliverableCommitment(context.Background(), fixture.workspace.ID, commitment.Commitment.ID)
	if err != nil || !reflect.DeepEqual(read, commitment.Commitment) {
		t.Fatalf("DeliverableCommitment(after raw attacks) = %#v, %v; want %#v", read, err, commitment.Commitment)
	}
	var projectorRows int
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM outcome_projector_state WHERE workspace_id=?`, fixture.workspace.ID).Scan(&projectorRows); err != nil || projectorRows != 0 {
		t.Fatalf("projector rows after unauthenticated insert = %d, %v; want 0", projectorRows, err)
	}
}

func TestOutcomeRawSQLAuthenticatedConstructionRejectsForgedEvidenceSourceClassAndHash(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, true)
	commitment := fixture.createCommitment(t, "raw-forged-evidence")
	handoffID := fixture.completeRun(t)
	input := domain.OutcomeAssessmentInput{
		Conclusion:          domain.OutcomeAchieved,
		DeliveredScope:      []string{"the promised deliverable"},
		UnmetScope:          []string{},
		DecisionRevisionIDs: []string{},
		Evidence: []domain.OutcomeEvidenceInput{{
			SourceType: domain.OutcomeEvidenceHandoff,
			SourceID:   handoffID,
		}},
		Effects:         []domain.OutcomeEffectInput{},
		Deviations:      []domain.OutcomeDeviationInput{},
		Risks:           []domain.OutcomeRiskInput{},
		Unknowns:        []domain.OutcomeUnknownInput{},
		FollowUpTaskIDs: []string{},
		OwnerAttention:  []domain.OutcomeOwnerAttentionInput{},
	}
	rawAssessmentID := "outassess_" + strings.Repeat("a", 32)
	content := outcomeAssessmentContent{
		WorkspaceID: fixture.workspace.ID, ProjectID: fixture.project.ID, ObjectiveID: fixture.objective.ID,
		TaskID: fixture.task.Task.ID, CommitmentID: commitment.Commitment.ID, Revision: 1, Input: input,
	}
	contentJSON, contentSHA, err := canonicalContent(content)
	if err != nil {
		t.Fatalf("canonicalContent(raw assessment) = %v", err)
	}
	deliveredJSON, _ := json.Marshal(input.DeliveredScope)
	unmetJSON, _ := json.Marshal(input.UnmetScope)
	rollback := errors.New("rollback authenticated raw evidence fixture")
	err = storage.withOutcomeMutation(context.Background(), "raw evidence adversarial fixture", func(tx *sql.Tx) error {
		now := storage.nowText()
		if _, insertErr := tx.ExecContext(context.Background(), `INSERT INTO outcome_assessments(
id,workspace_id,project_id,objective_id,task_id,commitment_id,revision,state_revision,
review_state,conclusion,delivered_scope_json,unmet_scope_json,content_json,content_sha256,
supersedes_assessment_id,proposed_at,proposed_by,decided_at,decided_by,decision_note
) VALUES(?,?,?,?,?,?,1,1,'proposed',?,?,?,?,?,NULL,?,'local-owner',NULL,NULL,NULL)`,
			rawAssessmentID, fixture.workspace.ID, fixture.project.ID, fixture.objective.ID, fixture.task.Task.ID,
			commitment.Commitment.ID, input.Conclusion, string(deliveredJSON), string(unmetJSON), string(contentJSON), contentSHA, now); insertErr != nil {
			return fmt.Errorf("insert valid raw assessment parent: %w", insertErr)
		}
		handoff, readErr := dbgen.New(tx).GetOutcomeHandoffEvidence(context.Background(), dbgen.GetOutcomeHandoffEvidenceParams{
			WorkspaceID: fixture.workspace.ID, HandoffID: handoffID,
		})
		if readErr != nil {
			return fmt.Errorf("read raw evidence fixture: %w", readErr)
		}
		_, sourceSHA, hashErr := canonicalContent(map[string]any{
			"id": handoff.ID, "run_id": handoff.RunID, "task_id": handoff.TaskID, "summary": handoff.Summary,
			"evidence_json": json.RawMessage(handoff.EvidenceJson), "created_at": handoff.CreatedAt,
		})
		if hashErr != nil {
			return fmt.Errorf("hash raw evidence fixture: %w", hashErr)
		}
		insertEvidence := func(sourceID, hash, class string) error {
			_, insertErr := tx.ExecContext(context.Background(), `INSERT INTO outcome_assessment_evidence_refs(
assessment_id,ordinal,source_type,source_id,source_revision,source_sha256,event_sequence,class,effect,pinned_freshness
) VALUES(?,0,'handoff',?,?,?,?,?,'supports','fresh')`,
				rawAssessmentID, sourceID, handoff.SourceRevision, hash, handoff.EventSequence, class)
			return insertErr
		}
		for _, attack := range []struct {
			name, sourceID, hash, class string
		}{
			{"source", "handoff_" + strings.Repeat("f", 32), sourceSHA, domain.EvidenceAgentSelfReport},
			{"class", handoffID, sourceSHA, domain.EvidenceIndependentReview},
			{"hash", handoffID, strings.Repeat("e", 64), domain.EvidenceAgentSelfReport},
		} {
			if insertErr := insertEvidence(attack.sourceID, attack.hash, attack.class); insertErr == nil {
				return fmt.Errorf("authenticated raw forged evidence %s was accepted", attack.name)
			}
		}
		if insertErr := insertEvidence(handoffID, sourceSHA, domain.EvidenceAgentSelfReport); insertErr != nil {
			return fmt.Errorf("valid authenticated raw evidence control was rejected: %w", insertErr)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("authenticated raw evidence matrix = %v, want controlled rollback", err)
	}

	result, err := storage.ProposeOutcomeAssessment(context.Background(), ProposeOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.task.Task.ID, CommitmentID: commitment.Commitment.ID,
		Input: input, IdempotencyKey: "raw-forged-evidence-store-control", CorrelationID: "request-raw-forged-evidence-store-control",
	})
	if err != nil || len(result.Detail.Evidence) != 1 || result.Detail.Evidence[0].Class != domain.EvidenceAgentSelfReport {
		t.Fatalf("ProposeOutcomeAssessment(valid Store control) = %#v, %v", result, err)
	}
}

func TestOutcomeRawSQLAuthenticatedConstructionRejectsForgedScopeCursorAndClaimProvenance(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "raw-forged-projection")
	fixture.acceptUnknown(t, commitment.Commitment.ID, "raw-forged-projection")
	briefing, err := storage.ShowManagementBriefing(context.Background(), ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointTask, ScopeIdentifier: fixture.task.Task.ID,
	})
	if err != nil || len(briefing.Claims) == 0 {
		t.Fatalf("ShowManagementBriefing(valid control) = %#v, %v", briefing, err)
	}
	other, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name: "raw-other", IdempotencyKey: "raw-other-workspace", CorrelationID: "request-raw-other-workspace",
	})
	if err != nil {
		t.Fatalf("InitWorkspace(other) = %v", err)
	}
	claim := briefing.Claims[0]
	rollback := errors.New("rollback authenticated raw projection fixture")
	err = storage.withOutcomeMutation(context.Background(), "raw projection adversarial fixture", func(tx *sql.Tx) error {
		checkpointID := "outcpnt_" + strings.Repeat("c", 32)
		now := storage.nowText()
		checkpointSequence, appendErr := appendEvent(context.Background(), tx, other.Workspace.ID, "owner_checkpoint", checkpointID, 1,
			ownerCheckpointCreatedEvent, "raw-cross-workspace-checkpoint", now,
			map[string]any{"scope_type": domain.OwnerCheckpointTask, "scope_id": fixture.task.Task.ID})
		if appendErr != nil {
			return fmt.Errorf("append cross-workspace checkpoint fixture event: %w", appendErr)
		}
		if _, insertErr := tx.ExecContext(context.Background(), `INSERT INTO owner_checkpoints(
id,workspace_id,scope_type,scope_id,event_sequence,created_at,created_by
) VALUES(?,?,'task',?,?,?,'local-owner')`, checkpointID, other.Workspace.ID, fixture.task.Task.ID, checkpointSequence, now); insertErr == nil {
			return errors.New("authenticated raw cross-workspace checkpoint scope was accepted")
		}

		unknownSequence, appendErr := appendEvent(context.Background(), tx, fixture.workspace.ID, "raw_fixture", "raw_fixture", 1,
			"future.outcome_fact", "raw-forged-projector-cursor", now, map[string]any{"fixture": true})
		if appendErr != nil {
			return fmt.Errorf("append unknown cursor fixture event: %w", appendErr)
		}
		if _, updateErr := tx.ExecContext(context.Background(), `UPDATE outcome_projector_state
SET last_event_sequence=?,revision=revision+1,updated_at=? WHERE workspace_id=?`, unknownSequence, now, fixture.workspace.ID); updateErr == nil {
			return errors.New("authenticated raw cursor crossed an unknown event")
		}

		if _, insertErr := tx.ExecContext(context.Background(), `INSERT INTO management_briefing_claim_sources(
briefing_id,claim_id,ordinal,entity_type,entity_id,entity_revision,content_sha256,event_sequence,
evidence_class,evidence_effect,pinned_freshness,current_freshness
) VALUES(?,?,?,?,?,1,?,?,'','','','')`,
			briefing.ID, claim.ID, len(claim.Sources), "deliverable_commitment", commitment.Commitment.ID,
			commitment.Commitment.ContentSHA256, commitment.EventSequence); insertErr == nil {
			return errors.New("authenticated raw unlisted briefing claim provenance was accepted")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("authenticated raw scope/cursor/provenance matrix = %v, want controlled rollback", err)
	}

	var cursor int64
	if err := storage.db.QueryRow(`SELECT last_event_sequence FROM outcome_projector_state WHERE workspace_id=?`, fixture.workspace.ID).Scan(&cursor); err != nil || cursor != briefing.EventCursor {
		t.Fatalf("projector cursor after forged advance = %d, %v; want %d", cursor, err, briefing.EventCursor)
	}
	explanation, err := storage.ExplainManagementBriefingClaim(context.Background(), ExplainManagementBriefingClaimQuery{
		WorkspaceIdentifier: fixture.workspace.ID, BriefingID: briefing.ID, ClaimID: claim.ID,
	})
	if err != nil || explanation.Claim.ID != claim.ID || len(explanation.Provenance) != len(claim.Sources) {
		t.Fatalf("ExplainManagementBriefingClaim(after raw attacks) = %#v, %v", explanation, err)
	}
}
