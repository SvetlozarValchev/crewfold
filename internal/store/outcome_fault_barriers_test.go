package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
)

var outcomeFaultTables = []string{
	"deliverable_commitments",
	"outcome_commitment_receipts",
	"outcome_assessments",
	"outcome_assessment_decision_refs",
	"outcome_assessment_evidence_refs",
	"outcome_assessment_effects",
	"outcome_assessment_deviations",
	"outcome_assessment_risks",
	"outcome_assessment_unknowns",
	"outcome_assessment_follow_up_tasks",
	"outcome_assessment_owner_attention",
	"outcome_assessment_submissions",
	"outcome_assessment_governance",
	"outcome_assessment_acceptance_basis",
	"owner_checkpoints",
	"outcome_projector_state",
	"management_briefings",
	"management_briefing_claims",
	"management_briefing_claim_sources",
	"management_briefing_receipts",
	"events",
	"idempotency_keys",
}

func outcomeFaultRowCounts(t *testing.T, storage *Store) []int {
	t.Helper()
	counts := make([]int, len(outcomeFaultTables))
	for index, table := range outcomeFaultTables {
		if err := storage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&counts[index]); err != nil {
			t.Fatalf("count %s = %v", table, err)
		}
	}
	return counts
}

func injectOneOutcomeFault(storage *Store, stage string, injected error) {
	storage.mutationHook = func(observed string) error {
		if observed == stage {
			return injected
		}
		return nil
	}
}

func reopenOutcomeFaultStore(t *testing.T, storage *Store, clock func() time.Time) *Store {
	t.Helper()
	dataDirectory := filepath.Dir(storage.Path())
	if err := storage.Close(); err != nil {
		t.Fatalf("Close(before fault retry) = %v", err)
	}
	reopened, err := Open(context.Background(), dataDirectory, Options{Clock: clock})
	if err != nil {
		t.Fatalf("Open(after fault) = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	return reopened
}

func TestOutcomeCommitmentFaultBarriersRollbackReopenRetryAndReplay(t *testing.T) {
	for _, stage := range []string{
		MutationAfterOutcomeCommitment,
		MutationAfterOutcomeCommitmentEvent,
		MutationAfterOutcomeCommitmentIdempotency,
	} {
		t.Run(stage, func(t *testing.T) {
			storage, fixture := newOutcomeAdversarialFixture(t, false)
			before := outcomeFaultRowCounts(t, storage)
			command := CreateDeliverableCommitmentCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				TaskID:              fixture.task.Task.ID,
				Key:                 "fault-barrier-commitment",
				Title:               "Fault-barrier deliverable",
				AcceptanceCriteria:  []string{"one complete durable graph survives"},
				IdempotencyKey:      "fault-barrier-commitment-create",
				CorrelationID:       "request-fault-barrier-commitment-create",
			}
			injected := errors.New("injected " + stage)
			injectOneOutcomeFault(storage, stage, injected)
			if result, err := storage.CreateDeliverableCommitment(context.Background(), command); !errors.Is(err, injected) {
				t.Fatalf("CreateDeliverableCommitment(fault) = %#v, %v; want injected fault", result, err)
			}
			if after := outcomeFaultRowCounts(t, storage); !reflect.DeepEqual(after, before) {
				t.Fatalf("commitment fault left partial rows\n before=%v\n  after=%v", before, after)
			}

			storage = reopenOutcomeFaultStore(t, storage, nil)
			first, err := storage.CreateDeliverableCommitment(context.Background(), command)
			if err != nil {
				t.Fatalf("CreateDeliverableCommitment(retry) = %v", err)
			}
			settled := outcomeFaultRowCounts(t, storage)
			second, err := storage.CreateDeliverableCommitment(context.Background(), command)
			if err != nil || !reflect.DeepEqual(second, first) {
				t.Fatalf("CreateDeliverableCommitment(replay) = %#v, %v; want exact %#v", second, err, first)
			}
			if replayed := outcomeFaultRowCounts(t, storage); !reflect.DeepEqual(replayed, settled) {
				t.Fatalf("commitment replay added rows\nsettled=%v\nreplayed=%v", settled, replayed)
			}
		})
	}
}

func TestOutcomeProposalFaultBarriersRollbackReopenRetryAndReplay(t *testing.T) {
	for _, stage := range []string{
		MutationAfterOutcomeAssessment,
		MutationAfterOutcomeAssessmentChildren,
		MutationAfterOutcomeAssessmentEvent,
		MutationAfterOutcomeAssessmentIdempotency,
	} {
		t.Run(stage, func(t *testing.T) {
			storage, fixture := newOutcomeAdversarialFixture(t, false)
			commitment := fixture.createCommitment(t, "fault-barrier-proposal")
			before := outcomeFaultRowCounts(t, storage)
			command := ProposeOutcomeAssessmentCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				TaskID:              fixture.task.Task.ID,
				CommitmentID:        commitment.Commitment.ID,
				Input:               fullyBoundedUnknownOutcomeInput(),
				IdempotencyKey:      "fault-barrier-assessment-propose",
				CorrelationID:       "request-fault-barrier-assessment-propose",
			}
			injected := errors.New("injected " + stage)
			injectOneOutcomeFault(storage, stage, injected)
			if result, err := storage.ProposeOutcomeAssessment(context.Background(), command); !errors.Is(err, injected) {
				t.Fatalf("ProposeOutcomeAssessment(fault) = %#v, %v; want injected fault", result, err)
			}
			if after := outcomeFaultRowCounts(t, storage); !reflect.DeepEqual(after, before) {
				t.Fatalf("proposal fault left partial rows\n before=%v\n  after=%v", before, after)
			}

			storage = reopenOutcomeFaultStore(t, storage, nil)
			first, err := storage.ProposeOutcomeAssessment(context.Background(), command)
			if err != nil {
				t.Fatalf("ProposeOutcomeAssessment(retry) = %v", err)
			}
			settled := outcomeFaultRowCounts(t, storage)
			second, err := storage.ProposeOutcomeAssessment(context.Background(), command)
			if err != nil || !reflect.DeepEqual(second, first) {
				t.Fatalf("ProposeOutcomeAssessment(replay) = %#v, %v; want exact %#v", second, err, first)
			}
			if replayed := outcomeFaultRowCounts(t, storage); !reflect.DeepEqual(replayed, settled) {
				t.Fatalf("proposal replay added rows\nsettled=%v\nreplayed=%v", settled, replayed)
			}
		})
	}
}

func TestOutcomeGovernanceFaultBarriersRollbackReopenRetryAndReplay(t *testing.T) {
	for _, stage := range []string{
		MutationAfterOutcomeGovernanceDecision,
		MutationAfterOutcomeGovernanceEvents,
		MutationAfterOutcomeGovernanceIdempotency,
	} {
		t.Run(stage, func(t *testing.T) {
			storage, fixture := newOutcomeAdversarialFixture(t, false)
			commitment := fixture.createCommitment(t, "fault-barrier-governance")
			proposed := fixture.proposeUnknown(t, commitment.Commitment.ID, "", "fault-barrier-governance")
			before := outcomeFaultRowCounts(t, storage)
			command := DecideOutcomeAssessmentCommand{
				WorkspaceIdentifier:   fixture.workspace.ID,
				AssessmentID:          proposed.Detail.Assessment.ID,
				ExpectedStateRevision: proposed.Detail.Assessment.StateRevision,
				DecisionNote:          "accept after the transaction is complete",
				IdempotencyKey:        "fault-barrier-governance-accept",
				CorrelationID:         "request-fault-barrier-governance-accept",
			}
			injected := errors.New("injected " + stage)
			injectOneOutcomeFault(storage, stage, injected)
			if result, err := storage.AcceptOutcomeAssessment(context.Background(), command); !errors.Is(err, injected) {
				t.Fatalf("AcceptOutcomeAssessment(fault) = %#v, %v; want injected fault", result, err)
			}
			if after := outcomeFaultRowCounts(t, storage); !reflect.DeepEqual(after, before) {
				t.Fatalf("governance fault left partial rows\n before=%v\n  after=%v", before, after)
			}
			rolledBack, err := storage.OutcomeAssessment(context.Background(), fixture.workspace.ID, proposed.Detail.Assessment.ID)
			if err != nil || rolledBack.Assessment.ReviewState != domain.OutcomeAssessmentProposed || rolledBack.Assessment.StateRevision != proposed.Detail.Assessment.StateRevision {
				t.Fatalf("OutcomeAssessment(after governance rollback) = %#v, %v; want original proposed state", rolledBack, err)
			}

			storage = reopenOutcomeFaultStore(t, storage, nil)
			first, err := storage.AcceptOutcomeAssessment(context.Background(), command)
			if err != nil {
				t.Fatalf("AcceptOutcomeAssessment(retry) = %v", err)
			}
			settled := outcomeFaultRowCounts(t, storage)
			second, err := storage.AcceptOutcomeAssessment(context.Background(), command)
			if err != nil || !reflect.DeepEqual(second, first) {
				t.Fatalf("AcceptOutcomeAssessment(replay) = %#v, %v; want exact %#v", second, err, first)
			}
			if replayed := outcomeFaultRowCounts(t, storage); !reflect.DeepEqual(replayed, settled) {
				t.Fatalf("governance replay added rows\nsettled=%v\nreplayed=%v", settled, replayed)
			}
		})
	}
}

func TestOwnerCheckpointFaultBarriersRollbackReopenRetryAndReplay(t *testing.T) {
	for _, stage := range []string{
		MutationAfterOwnerCheckpointEvent,
		MutationAfterOwnerCheckpoint,
		MutationAfterOwnerCheckpointIdempotency,
	} {
		t.Run(stage, func(t *testing.T) {
			storage, fixture := newOutcomeAdversarialFixture(t, false)
			before := outcomeFaultRowCounts(t, storage)
			command := CreateOwnerCheckpointCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				ScopeType:           domain.OwnerCheckpointTask,
				ScopeIdentifier:     fixture.task.Task.ID,
				IdempotencyKey:      "fault-barrier-checkpoint-create",
				CorrelationID:       "request-fault-barrier-checkpoint-create",
			}
			injected := errors.New("injected " + stage)
			injectOneOutcomeFault(storage, stage, injected)
			if result, err := storage.CreateOwnerCheckpoint(context.Background(), command); !errors.Is(err, injected) {
				t.Fatalf("CreateOwnerCheckpoint(fault) = %#v, %v; want injected fault", result, err)
			}
			if after := outcomeFaultRowCounts(t, storage); !reflect.DeepEqual(after, before) {
				t.Fatalf("checkpoint fault left partial rows\n before=%v\n  after=%v", before, after)
			}

			storage = reopenOutcomeFaultStore(t, storage, nil)
			first, err := storage.CreateOwnerCheckpoint(context.Background(), command)
			if err != nil {
				t.Fatalf("CreateOwnerCheckpoint(retry) = %v", err)
			}
			settled := outcomeFaultRowCounts(t, storage)
			second, err := storage.CreateOwnerCheckpoint(context.Background(), command)
			if err != nil || !reflect.DeepEqual(second, first) {
				t.Fatalf("CreateOwnerCheckpoint(replay) = %#v, %v; want exact %#v", second, err, first)
			}
			if replayed := outcomeFaultRowCounts(t, storage); !reflect.DeepEqual(replayed, settled) {
				t.Fatalf("checkpoint replay added rows\nsettled=%v\nreplayed=%v", settled, replayed)
			}
		})
	}
}

func TestManagementBriefingFaultBarriersRollbackReopenRetryAndStableReplay(t *testing.T) {
	for _, stage := range []string{
		MutationAfterBriefingCursor,
		MutationAfterBriefingClaims,
		MutationAfterBriefingRevision,
	} {
		t.Run(stage, func(t *testing.T) {
			storage, fixture := newOutcomeAdversarialFixture(t, false)
			fixedTime := time.Date(2030, 4, 5, 6, 7, 8, 0, time.UTC)
			clock := func() time.Time { return fixedTime }
			storage.clock = clock
			fixture.createCommitment(t, "fault-barrier-briefing")
			beforeEvents := countOutcomeFaultRows(t, storage, "events")
			beforeIdempotency := countOutcomeFaultRows(t, storage, "idempotency_keys")
			query := ShowManagementBriefingQuery{
				WorkspaceIdentifier: fixture.workspace.ID,
				ScopeType:           domain.OwnerCheckpointTask,
				ScopeIdentifier:     fixture.task.Task.ID,
			}
			injected := errors.New("injected " + stage)
			injectOneOutcomeFault(storage, stage, injected)
			if result, err := storage.ShowManagementBriefing(context.Background(), query); !errors.Is(err, injected) {
				t.Fatalf("ShowManagementBriefing(fault) = %#v, %v; want injected fault", result, err)
			}
			for _, table := range []string{"management_briefings", "management_briefing_claims", "management_briefing_claim_sources", "management_briefing_receipts"} {
				if count := countOutcomeFaultRows(t, storage, table); count != 0 {
					t.Fatalf("%s count after %s = %d, want rolled back", table, stage, count)
				}
			}
			if got := countOutcomeFaultRows(t, storage, "events"); got != beforeEvents {
				t.Fatalf("briefing fault appended events: got %d, want %d", got, beforeEvents)
			}
			if got := countOutcomeFaultRows(t, storage, "idempotency_keys"); got != beforeIdempotency {
				t.Fatalf("briefing fault changed idempotency records: got %d, want %d", got, beforeIdempotency)
			}
			if stage == MutationAfterBriefingCursor {
				if cursor := outcomeProjectorCursorForFaultTest(t, storage, fixture.workspace.ID); cursor != 0 {
					t.Fatalf("projector cursor after cursor barrier rollback = %d, want 0", cursor)
				}
			}

			storage = reopenOutcomeFaultStore(t, storage, clock)
			first, err := storage.ShowManagementBriefing(context.Background(), query)
			if err != nil {
				t.Fatalf("ShowManagementBriefing(retry) = %v", err)
			}
			settled := outcomeFaultRowCounts(t, storage)
			second, err := storage.ShowManagementBriefing(context.Background(), query)
			if err != nil || !reflect.DeepEqual(second, first) {
				t.Fatalf("ShowManagementBriefing(stable replay) = %#v, %v; want exact %#v", second, err, first)
			}
			if replayed := outcomeFaultRowCounts(t, storage); !reflect.DeepEqual(replayed, settled) {
				t.Fatalf("briefing replay added rows\nsettled=%v\nreplayed=%v", settled, replayed)
			}
			if len(first.Claims) == 0 || !first.CaughtUp || first.EventCursor != first.CutoffEventSequence {
				t.Fatalf("briefing retry did not materialize one complete current snapshot: %#v", first)
			}
			if got := countOutcomeFaultRows(t, storage, "events"); got != beforeEvents {
				t.Fatalf("successful briefing appended events: got %d, want %d", got, beforeEvents)
			}
		})
	}
}

func countOutcomeFaultRows(t *testing.T, storage *Store, table string) int {
	t.Helper()
	var count int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count %s = %v", table, err)
	}
	return count
}

func outcomeProjectorCursorForFaultTest(t *testing.T, storage *Store, workspaceID string) int64 {
	t.Helper()
	var cursor int64
	err := storage.db.QueryRow(`SELECT last_event_sequence FROM outcome_projector_state WHERE workspace_id=?`, workspaceID).Scan(&cursor)
	if err != nil {
		// A cursor-page failure is also allowed to roll back initialization of the
		// workspace state row. Both representations mean no durable advancement.
		return 0
	}
	return cursor
}
