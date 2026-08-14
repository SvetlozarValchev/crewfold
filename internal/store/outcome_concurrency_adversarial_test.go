package store

import (
	"context"
	"sync"
	"testing"

	"crewfold/internal/domain"
)

func TestConcurrentOutcomeAcceptRejectHasOneGovernanceWinnerAndStableConflict(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "concurrent-decision-commitment")
	proposed := fixture.proposeUnknown(t, commitment.Commitment.ID, "", "concurrent-decision")

	entered, release := make(chan struct{}), make(chan struct{})
	var blockFirst sync.Once
	storage.mutationHook = func(stage string) error {
		if stage == MutationAfterOutcomeGovernanceDecision {
			blockFirst.Do(func() {
				close(entered)
				<-release
			})
		}
		return nil
	}

	commands := []struct {
		accept bool
		key    string
	}{
		{accept: true, key: "concurrent-decision-accept"},
		{accept: false, key: "concurrent-decision-reject"},
	}
	errorsByCall := make([]error, len(commands))
	results := make([]OutcomeAssessmentMutationResult, len(commands))
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		results[0], errorsByCall[0] = storage.AcceptOutcomeAssessment(context.Background(), DecideOutcomeAssessmentCommand{
			WorkspaceIdentifier: fixture.workspace.ID, AssessmentID: proposed.Detail.Assessment.ID,
			ExpectedStateRevision: proposed.Detail.Assessment.StateRevision, DecisionNote: "accept exact proposal",
			IdempotencyKey: commands[0].key, CorrelationID: "request-" + commands[0].key,
		})
	}()
	<-entered
	secondStarted := make(chan struct{})
	wait.Add(1)
	go func() {
		defer wait.Done()
		close(secondStarted)
		results[1], errorsByCall[1] = storage.RejectOutcomeAssessment(context.Background(), DecideOutcomeAssessmentCommand{
			WorkspaceIdentifier: fixture.workspace.ID, AssessmentID: proposed.Detail.Assessment.ID,
			ExpectedStateRevision: proposed.Detail.Assessment.StateRevision, DecisionNote: "reject exact proposal",
			IdempotencyKey: commands[1].key, CorrelationID: "request-" + commands[1].key,
		})
	}()
	<-secondStarted
	close(release)
	wait.Wait()
	storage.mutationHook = nil

	successes, conflicts := 0, 0
	loser := -1
	for index, err := range errorsByCall {
		if err == nil {
			successes++
			continue
		}
		switch ErrorCode(err) {
		case CodeOutcomeAssessmentConflict:
			conflicts++
			loser = index
		default:
			t.Fatalf("concurrent outcome decision %d = %#v, %v, code %q; want success or stable domain conflict", index, results[index], err, ErrorCode(err))
		}
	}
	if successes != 1 || conflicts != 1 || loser < 0 {
		t.Fatalf("concurrent decisions: results=%#v errors=%v; want one success and one conflict", results, errorsByCall)
	}

	var governanceCount, terminalEventCount, acceptanceBasisCount int
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM outcome_assessment_governance WHERE assessment_id=?`, proposed.Detail.Assessment.ID).Scan(&governanceCount); err != nil {
		t.Fatalf("count governance rows = %v", err)
	}
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM events WHERE entity_type='outcome_assessment' AND entity_id=?
AND type IN ('outcome.assessment_accepted','outcome.assessment_rejected')`, proposed.Detail.Assessment.ID).Scan(&terminalEventCount); err != nil {
		t.Fatalf("count decision events = %v", err)
	}
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM outcome_assessment_acceptance_basis WHERE assessment_id=?`, proposed.Detail.Assessment.ID).Scan(&acceptanceBasisCount); err != nil {
		t.Fatalf("count acceptance basis rows = %v", err)
	}
	if governanceCount != 1 || terminalEventCount != 1 || acceptanceBasisCount > 1 {
		t.Fatalf("decision receipt graph = governance %d events %d basis %d; want one unsplit governance effect", governanceCount, terminalEventCount, acceptanceBasisCount)
	}
	if _, err := storage.OutcomeAssessment(context.Background(), fixture.workspace.ID, proposed.Detail.Assessment.ID); err != nil {
		t.Fatalf("OutcomeAssessment(after concurrent decision) = %v", err)
	}

	loserCommand := DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, AssessmentID: proposed.Detail.Assessment.ID,
		ExpectedStateRevision: proposed.Detail.Assessment.StateRevision,
		IdempotencyKey:        commands[loser].key, CorrelationID: "request-" + commands[loser].key,
	}
	if commands[loser].accept {
		_, errorsByCall[loser] = storage.AcceptOutcomeAssessment(context.Background(), loserCommand)
	} else {
		_, errorsByCall[loser] = storage.RejectOutcomeAssessment(context.Background(), loserCommand)
	}
	if ErrorCode(errorsByCall[loser]) != CodeOutcomeAssessmentConflict {
		t.Fatalf("losing decision replay = %v, code %q; want stable %q", errorsByCall[loser], ErrorCode(errorsByCall[loser]), CodeOutcomeAssessmentConflict)
	}
}

func TestConcurrentOutcomeSuccessorProposalsHaveOneProposalAndNoOrphans(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	commitment := fixture.createCommitment(t, "concurrent-successor-commitment")
	accepted := fixture.acceptUnknown(t, commitment.Commitment.ID, "concurrent-successor-first")

	entered, release := make(chan struct{}), make(chan struct{})
	var blockFirst sync.Once
	storage.mutationHook = func(stage string) error {
		if stage == MutationAfterOutcomeAssessment {
			blockFirst.Do(func() {
				close(entered)
				<-release
			})
		}
		return nil
	}

	errorsByCall := make([]error, 2)
	results := make([]OutcomeAssessmentMutationResult, 2)
	propose := func(index int) {
		results[index], errorsByCall[index] = storage.ProposeOutcomeAssessment(context.Background(), ProposeOutcomeAssessmentCommand{
			WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.task.Task.ID,
			CommitmentID: commitment.Commitment.ID, SupersedesAssessmentID: accepted.Detail.Assessment.ID,
			Input: fullyBoundedUnknownOutcomeInput(), IdempotencyKey: "concurrent-successor-propose-" + string(rune('a'+index)),
			CorrelationID: "request-concurrent-successor-propose-" + string(rune('a'+index)),
		})
	}
	var wait sync.WaitGroup
	wait.Add(1)
	go func() { defer wait.Done(); propose(0) }()
	<-entered
	secondStarted := make(chan struct{})
	wait.Add(1)
	go func() { defer wait.Done(); close(secondStarted); propose(1) }()
	<-secondStarted
	close(release)
	wait.Wait()
	storage.mutationHook = nil

	successes, conflicts := 0, 0
	for index, err := range errorsByCall {
		if err == nil {
			successes++
			continue
		}
		switch ErrorCode(err) {
		case CodeOutcomeAssessmentConflict:
			conflicts++
		default:
			t.Fatalf("concurrent successor proposal %d = %#v, %v, code %q; want success or stable domain conflict", index, results[index], err, ErrorCode(err))
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successor proposals: results=%#v errors=%v; want one success and one conflict", results, errorsByCall)
	}

	var proposedCount, submissionCount, proposalEventCount int
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM outcome_assessments WHERE commitment_id=? AND review_state='proposed'`, commitment.Commitment.ID).Scan(&proposedCount); err != nil {
		t.Fatalf("count proposed successors = %v", err)
	}
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM outcome_assessment_submissions submission
JOIN outcome_assessments assessment ON assessment.id=submission.assessment_id
WHERE assessment.commitment_id=? AND assessment.review_state='proposed'`, commitment.Commitment.ID).Scan(&submissionCount); err != nil {
		t.Fatalf("count successor submissions = %v", err)
	}
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM events event JOIN outcome_assessments assessment ON assessment.id=event.entity_id
WHERE assessment.commitment_id=? AND assessment.review_state='proposed' AND event.type='outcome.assessment_proposed'`, commitment.Commitment.ID).Scan(&proposalEventCount); err != nil {
		t.Fatalf("count successor proposal events = %v", err)
	}
	if proposedCount != 1 || submissionCount != 1 || proposalEventCount != 1 {
		t.Fatalf("successor proposal receipt graph = assessments %d submissions %d events %d; want one complete proposal", proposedCount, submissionCount, proposalEventCount)
	}
	current, err := storage.OutcomeAssessment(context.Background(), fixture.workspace.ID, accepted.Detail.Assessment.ID)
	if err != nil || current.Assessment.ReviewState != domain.OutcomeAssessmentAccepted {
		t.Fatalf("current accepted assessment changed during successor race: %#v, %v", current, err)
	}
}
