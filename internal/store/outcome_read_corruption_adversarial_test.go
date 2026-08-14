package store

import (
	"context"
	"path/filepath"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

// These tests remove one storage trigger only after constructing an entirely
// valid fact. That models damaged or externally modified storage and verifies
// that every public read revalidates the complete receipt graph rather than
// trusting rows merely because they can be decoded.

func TestOutcomeCommitmentListFailsClosedWhenReceiptIsMissing(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "commitment-list-receipt")

	if _, err := storage.db.Exec(`DROP TRIGGER outcome_commitment_receipt_reject_delete`); err != nil {
		t.Fatalf("drop commitment receipt delete seal = %v", err)
	}
	if _, err := storage.db.Exec(`DELETE FROM outcome_commitment_receipts WHERE commitment_id=?`, commitment.Commitment.ID); err != nil {
		t.Fatalf("delete commitment receipt = %v", err)
	}
	if _, err := storage.DeliverableCommitment(context.Background(), fixture.workspace.ID, commitment.Commitment.ID); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("DeliverableCommitment(unreceipted) = %v, code %q; want %q", err, ErrorCode(err), CodeStorageFailed)
	}
	if values, err := storage.DeliverableCommitments(context.Background(), ListDeliverableCommitmentsQuery{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.task.Task.ID, Limit: 100,
	}); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("DeliverableCommitments(unreceipted) = %#v, %v, code %q; want fail closed", values, err, ErrorCode(err))
	}
}

func TestOutcomeAssessmentReadFailsClosedOnEvidenceOrdinalGap(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, true)
	commitment := fixture.createCommitment(t, "evidence-ordinal-commitment")
	handoffID := fixture.completeRun(t)
	proposed, err := storage.ProposeOutcomeAssessment(context.Background(), ProposeOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		TaskID:              fixture.task.Task.ID,
		CommitmentID:        commitment.Commitment.ID,
		Input: domain.OutcomeAssessmentInput{
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
		},
		IdempotencyKey: "evidence-ordinal-proposal",
		CorrelationID:  "request-evidence-ordinal-proposal",
	})
	if err != nil {
		t.Fatalf("ProposeOutcomeAssessment() = %v", err)
	}

	if _, err := storage.db.Exec(`DROP TRIGGER outcome_evidence_ref_immutable_update`); err != nil {
		t.Fatalf("drop evidence update seal = %v", err)
	}
	if _, err := storage.db.Exec(`UPDATE outcome_assessment_evidence_refs SET ordinal=1 WHERE assessment_id=?`, proposed.Detail.Assessment.ID); err != nil {
		t.Fatalf("create evidence ordinal gap = %v", err)
	}
	if value, err := storage.OutcomeAssessment(context.Background(), fixture.workspace.ID, proposed.Detail.Assessment.ID); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("OutcomeAssessment(evidence ordinal gap) = %#v, %v, code %q; want fail closed", value, err, ErrorCode(err))
	}
}

func TestAcceptedOutcomeReadFailsClosedOnAcceptanceBasisHashTamper(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "acceptance-basis-commitment")
	accepted := fixture.acceptUnknown(t, commitment.Commitment.ID, "acceptance-basis")

	if _, err := storage.db.Exec(`DROP TRIGGER outcome_acceptance_basis_immutable_update`); err != nil {
		t.Fatalf("drop acceptance-basis update seal = %v", err)
	}
	if _, err := storage.db.Exec(`UPDATE outcome_assessment_acceptance_basis SET source_sha256=? WHERE assessment_id=?`,
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", accepted.Detail.Assessment.ID); err != nil {
		t.Fatalf("tamper acceptance-basis hash = %v", err)
	}
	if value, err := storage.OutcomeAssessment(context.Background(), fixture.workspace.ID, accepted.Detail.Assessment.ID); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("OutcomeAssessment(tampered acceptance basis) = %#v, %v, code %q; want fail closed", value, err, ErrorCode(err))
	}
}

func TestAcceptedSuccessorReadFailsClosedOnSupersessionReceiptTamper(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "successor-receipt-commitment")
	first := fixture.acceptUnknown(t, commitment.Commitment.ID, "successor-receipt-first")
	secondProposal := fixture.proposeUnknown(t, commitment.Commitment.ID, first.Detail.Assessment.ID, "successor-receipt-second")
	second, err := storage.AcceptOutcomeAssessment(context.Background(), DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier:   fixture.workspace.ID,
		AssessmentID:          secondProposal.Detail.Assessment.ID,
		ExpectedStateRevision: secondProposal.Detail.Assessment.StateRevision,
		DecisionNote:          "accept the exact successor",
		IdempotencyKey:        "successor-receipt-second-accept",
		CorrelationID:         "request-successor-receipt-second-accept",
	})
	if err != nil {
		t.Fatalf("AcceptOutcomeAssessment(successor) = %v", err)
	}

	if _, err := storage.db.Exec(`DROP TRIGGER outcome_governance_immutable_update`); err != nil {
		t.Fatalf("drop governance update seal = %v", err)
	}
	if _, err := storage.db.Exec(`UPDATE outcome_assessment_governance
SET superseded_assessment_id=NULL,superseded_event_sequence=NULL WHERE assessment_id=?`, second.Detail.Assessment.ID); err != nil {
		t.Fatalf("tamper successor governance receipt = %v", err)
	}
	if value, err := storage.OutcomeAssessment(context.Background(), fixture.workspace.ID, second.Detail.Assessment.ID); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("OutcomeAssessment(tampered successor governance) = %#v, %v, code %q; want fail closed", value, err, ErrorCode(err))
	}
}

func TestOwnerCheckpointShowAndListFailClosedOnEventSubstitution(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	checkpoint, err := storage.CreateOwnerCheckpoint(context.Background(), CreateOwnerCheckpointCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ScopeType:           domain.OwnerCheckpointTask,
		ScopeIdentifier:     fixture.task.Task.ID,
		IdempotencyKey:      "checkpoint-event-substitution",
		CorrelationID:       "request-checkpoint-event-substitution",
	})
	if err != nil {
		t.Fatalf("CreateOwnerCheckpoint() = %v", err)
	}
	var unrelatedEventSequence int64
	if err := storage.db.QueryRow(`SELECT sequence FROM events WHERE workspace_id=? AND type='project.registered' LIMIT 1`, fixture.workspace.ID).Scan(&unrelatedEventSequence); err != nil {
		t.Fatalf("read unrelated workspace event = %v", err)
	}
	if unrelatedEventSequence == checkpoint.EventSequence {
		t.Fatal("checkpoint fixture selected its own event as the unrelated event")
	}

	if _, err := storage.db.Exec(`DROP TRIGGER owner_checkpoint_reject_update`); err != nil {
		t.Fatalf("drop checkpoint update seal = %v", err)
	}
	if _, err := storage.db.Exec(`UPDATE owner_checkpoints SET event_sequence=? WHERE id=?`, unrelatedEventSequence, checkpoint.Checkpoint.ID); err != nil {
		t.Fatalf("substitute checkpoint event = %v", err)
	}
	if value, err := storage.OwnerCheckpoint(context.Background(), fixture.workspace.ID, checkpoint.Checkpoint.ID); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("OwnerCheckpoint(substituted event) = %#v, %v, code %q; want fail closed", value, err, ErrorCode(err))
	}
	if values, err := storage.OwnerCheckpoints(context.Background(), ListOwnerCheckpointsQuery{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointTask,
		ScopeIdentifier: fixture.task.Task.ID, Limit: 100,
	}); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("OwnerCheckpoints(substituted event) = %#v, %v, code %q; want fail closed", values, err, ErrorCode(err))
	}
}

type outcomeAdversarialFixture struct {
	storage   *Store
	workspace domain.Workspace
	project   domain.Project
	objective domain.Objective
	task      domain.TaskDetail
	agent     domain.AgentDefinition
	checkout  domain.Checkout
}

func newOutcomeAdversarialFixture(t *testing.T, runnable bool) (*Store, outcomeAdversarialFixture) {
	t.Helper()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	objectiveResult, err := storage.CreateObjective(context.Background(), CreateObjectiveCommand{
		WorkspaceIdentifier: workspace.ID,
		ProjectIdentifier:   project.ID,
		Title:               "Outcome adversarial objective",
		Budget:              domain.Budget{TokenLimit: 10000, CostCents: 1000, TimeSeconds: 3600},
		IdempotencyKey:      "outcome-adversarial-objective",
		CorrelationID:       "request-outcome-adversarial-objective",
	})
	if err != nil {
		t.Fatalf("CreateObjective() = %v", err)
	}
	taskResult, err := storage.CreateTask(context.Background(), CreateTaskCommand{
		WorkspaceIdentifier: workspace.ID,
		ProjectIdentifier:   project.ID,
		ObjectiveID:         objectiveResult.Value.ID,
		Title:               "Deliver one exact outcome",
		Budget:              domain.Budget{TokenLimit: 1000, CostCents: 100, TimeSeconds: 600},
		IdempotencyKey:      "outcome-adversarial-task",
		CorrelationID:       "request-outcome-adversarial-task",
	})
	if err != nil {
		t.Fatalf("CreateTask() = %v", err)
	}
	fixture := outcomeAdversarialFixture{
		storage: storage, workspace: workspace, project: project,
		objective: objectiveResult.Value, task: taskResult.Detail,
	}
	if !runnable {
		return storage, fixture
	}
	agentResult, err := storage.CreateAgent(context.Background(), CreateAgentCommand{
		WorkspaceIdentifier: workspace.ID, Name: "outcome-adversarial-agent", Role: "arbitrary metadata",
		Provider: "fake", Runtime: "fake", MaxConcurrency: 1,
		IdempotencyKey: "outcome-adversarial-agent", CorrelationID: "request-outcome-adversarial-agent",
	})
	if err != nil {
		t.Fatalf("CreateAgent() = %v", err)
	}
	checkoutResult, err := storage.AddCheckout(context.Background(), AddCheckoutCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, WriteMode: domain.WriteModeExclusive,
		IdempotencyKey: "outcome-adversarial-checkout", CorrelationID: "request-outcome-adversarial-checkout",
		Observation: sourceTestObservation(filepath.Join(t.TempDir(), "outcome-adversarial-checkout"), "outcome-adversarial"),
	})
	if err != nil {
		t.Fatalf("AddCheckout() = %v", err)
	}
	assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: taskResult.Detail.Task.ID, AgentIdentifier: agentResult.Value.ID,
		LeaseSeconds: 300, ExpectedRevision: taskResult.Detail.Task.Revision,
		IdempotencyKey: "outcome-adversarial-assignment", CorrelationID: "request-outcome-adversarial-assignment",
	})
	if err != nil {
		t.Fatalf("AssignTask() = %v", err)
	}
	fixture.task, fixture.agent, fixture.checkout = assigned.Detail, agentResult.Value, checkoutResult.Checkout
	return storage, fixture
}

func (fixture outcomeAdversarialFixture) createCommitment(t *testing.T, key string) DeliverableCommitmentMutationResult {
	t.Helper()
	result, err := fixture.storage.CreateDeliverableCommitment(context.Background(), CreateDeliverableCommitmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		TaskID:              fixture.task.Task.ID,
		Key:                 key,
		Title:               "One exact promised deliverable",
		AcceptanceCriteria:  []string{"the owner can inspect the exact durable result"},
		IdempotencyKey:      key + "-create",
		CorrelationID:       "request-" + key + "-create",
	})
	if err != nil {
		t.Fatalf("CreateDeliverableCommitment(%s) = %v", key, err)
	}
	return result
}

func (fixture outcomeAdversarialFixture) completeRun(t *testing.T) string {
	t.Helper()
	scenario := domain.FakeScenario{
		Schema: execution.FakeScenarioSchema,
		Name:   "outcome-adversarial-completion",
		Steps: []domain.FakeStep{{
			Kind: domain.ObservationCompletion, Message: "completed exact work",
			Evidence: []string{"self reported evidence"}, Handoff: "inspect the exact completed work",
		}},
	}
	created, err := fixture.storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.task.Task.ID, CheckoutIdentifier: fixture.checkout.ID,
		Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: fixture.task.Task.Revision,
		IdempotencyKey: "outcome-adversarial-run", CorrelationID: "request-outcome-adversarial-run",
	})
	if err != nil {
		t.Fatalf("CreateRun() = %v", err)
	}
	starting, err := fixture.storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "outcome-adversarial-starting")
	if err != nil {
		t.Fatalf("MarkRunStarting() = %v", err)
	}
	if _, err := fixture.storage.MarkRunStarted(context.Background(), starting.ID, "runtime-outcome-adversarial", "provider-outcome-adversarial", "outcome-adversarial-started"); err != nil {
		t.Fatalf("MarkRunStarted() = %v", err)
	}
	completed, err := fixture.storage.ApplyRunObservation(context.Background(), starting.ID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "completed exact work",
		Evidence: []string{"self reported evidence"}, Handoff: "inspect the exact completed work",
	}, true, nil, "outcome-adversarial-completed")
	if err != nil || completed.Handoff == nil {
		t.Fatalf("ApplyRunObservation() = %#v, %v", completed, err)
	}
	return completed.Handoff.ID
}

func (fixture outcomeAdversarialFixture) proposeUnknown(t *testing.T, commitmentID, supersedesID, key string) OutcomeAssessmentMutationResult {
	t.Helper()
	result, err := fixture.storage.ProposeOutcomeAssessment(context.Background(), ProposeOutcomeAssessmentCommand{
		WorkspaceIdentifier:    fixture.workspace.ID,
		TaskID:                 fixture.task.Task.ID,
		CommitmentID:           commitmentID,
		SupersedesAssessmentID: supersedesID,
		Input:                  fullyBoundedUnknownOutcomeInput(),
		IdempotencyKey:         key + "-propose",
		CorrelationID:          "request-" + key + "-propose",
	})
	if err != nil {
		t.Fatalf("ProposeOutcomeAssessment(%s) = %v", key, err)
	}
	return result
}

func fullyBoundedUnknownOutcomeInput() domain.OutcomeAssessmentInput {
	return domain.OutcomeAssessmentInput{
		Conclusion:          domain.OutcomeUnknown,
		DeliveredScope:      []string{},
		UnmetScope:          []string{},
		DecisionRevisionIDs: []string{},
		Evidence:            []domain.OutcomeEvidenceInput{},
		Effects:             []domain.OutcomeEffectInput{},
		Deviations:          []domain.OutcomeDeviationInput{},
		Risks:               []domain.OutcomeRiskInput{},
		Unknowns:            []domain.OutcomeUnknownInput{{Summary: "the exact delivered effect remains unknown"}},
		FollowUpTaskIDs:     []string{},
		OwnerAttention:      []domain.OutcomeOwnerAttentionInput{},
	}
}

func (fixture outcomeAdversarialFixture) acceptUnknown(t *testing.T, commitmentID, key string) OutcomeAssessmentMutationResult {
	t.Helper()
	proposed := fixture.proposeUnknown(t, commitmentID, "", key)
	result, err := fixture.storage.AcceptOutcomeAssessment(context.Background(), DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier:   fixture.workspace.ID,
		AssessmentID:          proposed.Detail.Assessment.ID,
		ExpectedStateRevision: proposed.Detail.Assessment.StateRevision,
		DecisionNote:          "accept the explicit unknown",
		IdempotencyKey:        key + "-accept",
		CorrelationID:         "request-" + key + "-accept",
	})
	if err != nil {
		t.Fatalf("AcceptOutcomeAssessment(%s) = %v", key, err)
	}
	return result
}
