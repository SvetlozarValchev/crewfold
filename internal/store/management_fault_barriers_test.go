package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"crewfold/internal/domain"
)

var errInjectedManagementBarrier = errors.New("injected management mutation barrier")

func TestManagerProposalNamedFaultBarriersRollbackAndRetry(t *testing.T) {
	for index, stage := range []string{MutationAfterProposalActions, MutationAfterProposalSubmission} {
		t.Run("submit "+stage, func(t *testing.T) {
			storage, fixture := createManagerGrantAdversarialFixture(t)
			key := fmt.Sprintf("fsub-%d", index)
			runID, cursor := invokeAdversarialManager(t, storage, fixture, key)
			command := singleTaskProposalCommand(fixture, runID, cursor, key)
			storage.mutationHook = failManagementStage(stage)
			if _, err := storage.SubmitManagerProposal(context.Background(), command); !errors.Is(err, errInjectedManagementBarrier) {
				t.Fatalf("SubmitManagerProposal(%s) = %v; want injected fault", stage, err)
			}
			storage.mutationHook = nil
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposals WHERE source_run_id=?`, runID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposal_actions WHERE proposal_id IN (SELECT id FROM manager_proposals WHERE source_run_id=?)`, runID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM events WHERE type='manager.proposal_submitted' AND json_extract(data_json,'$.run_id')=?`, runID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, command.IdempotencyKey)

			retried, err := storage.SubmitManagerProposal(context.Background(), singleTaskProposalCommand(fixture, runID, cursor, key))
			if err != nil || retried.Proposal.Status != domain.ManagerProposalPending {
				t.Fatalf("SubmitManagerProposal(%s retry) = %#v, %v", stage, retried, err)
			}
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM manager_proposal_submissions WHERE proposal_id=?`, retried.Proposal.ID)
		})
	}

	for index, stage := range []string{MutationAfterProposalEffects, MutationAfterProposalDecision} {
		t.Run("accept "+stage, func(t *testing.T) {
			storage, fixture := createManagerGrantAdversarialFixture(t)
			key := fmt.Sprintf("faccept-%d", index)
			runID, cursor := invokeAdversarialManager(t, storage, fixture, key)
			submitted, err := storage.SubmitManagerProposal(context.Background(), singleTaskProposalCommand(fixture, runID, cursor, key))
			if err != nil {
				t.Fatalf("SubmitManagerProposal(%s fixture) = %v", stage, err)
			}
			command := AcceptManagerProposalCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				ManagerProposalID:   submitted.Proposal.ID,
				ExpectedRevision:    submitted.Proposal.Revision,
				DecisionNote:        "exercise named acceptance rollback",
				IdempotencyKey:      "fault-accept-" + stage,
				CorrelationID:       "request-fault-accept-" + stage,
			}
			storage.mutationHook = failManagementStage(stage)
			if _, err := storage.AcceptManagerProposal(context.Background(), command); !errors.Is(err, errInjectedManagementBarrier) {
				t.Fatalf("AcceptManagerProposal(%s) = %v; want injected fault", stage, err)
			}
			storage.mutationHook = nil
			stored, err := storage.ManagerProposal(context.Background(), fixture.workspace.ID, submitted.Proposal.ID)
			if err != nil || stored.Status != domain.ManagerProposalPending || stored.Revision != 1 {
				t.Fatalf("proposal after %s rollback = %#v, %v; want pending revision 1", stage, stored, err)
			}
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=?`, submitted.Proposal.ID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposal_decisions WHERE proposal_id=?`, submitted.Proposal.ID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM scheduling_intents WHERE source_proposal_id=?`, submitted.Proposal.ID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, command.IdempotencyKey)

			retried, err := storage.AcceptManagerProposal(context.Background(), command)
			if err != nil || retried.Proposal.Status != domain.ManagerProposalAccepted || len(retried.Effects) != 2 {
				t.Fatalf("AcceptManagerProposal(%s retry) = %#v, %v", stage, retried, err)
			}
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM manager_proposal_decisions WHERE proposal_id=?`, submitted.Proposal.ID)
		})
	}
}

func TestSupervisorSchedulingNamedFaultBarriersRollbackAndRetry(t *testing.T) {
	for index, stage := range []string{
		MutationAfterSchedulingAuthority,
		MutationAfterSchedulingRun,
		MutationAfterSchedulingAction,
		MutationAfterSchedulingReceipt,
	} {
		t.Run(stage, func(t *testing.T) {
			storage, fixture, proposalID, intentID := acceptedSingleSchedulingIntent(t, fmt.Sprintf("fsched-%d", index))
			command := RunSupervisorCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				Limit:               100,
				IdempotencyKey:      "fault-schedule-scan-" + stage,
				CorrelationID:       "request-fault-schedule-scan-" + stage,
			}
			storage.mutationHook = failManagementStage(stage)
			if _, err := storage.RunSupervisor(context.Background(), command); !errors.Is(err, errInjectedManagementBarrier) {
				t.Fatalf("RunSupervisor(%s) = %v; want injected fault", stage, err)
			}
			storage.mutationHook = nil
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status='pending' AND assignment_id IS NULL AND run_id IS NULL AND supervisor_action_id IS NULL`, intentID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM task_assignments WHERE created_by='subsystem:supervisor' AND task_id=(SELECT task_id FROM scheduling_intents WHERE id=?)`, intentID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM runs WHERE created_by='subsystem:supervisor' AND task_id=(SELECT task_id FROM scheduling_intents WHERE id=?)`, intentID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions WHERE intent_id=?`, intentID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM run_scheduling_receipts WHERE intent_id=?`, intentID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, command.IdempotencyKey)

			retried, err := storage.RunSupervisor(context.Background(), command)
			if err != nil || len(retried.ScheduledRunIDs) != 1 {
				t.Fatalf("RunSupervisor(%s retry) = %#v, %v", stage, retried, err)
			}
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_scheduling_receipts WHERE intent_id=? AND run_id=?`, intentID, retried.ScheduledRunIDs[0])
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM manager_proposal_decisions WHERE proposal_id=?`, proposalID)
		})
	}
}

func TestSupervisorRetryNamedFaultBarriersRollbackAndRetry(t *testing.T) {
	for index, stage := range []string{MutationAfterRetryRun, MutationAfterRetryReceipt} {
		t.Run(stage, func(t *testing.T) {
			storage, fixture, _, _ := acceptedSingleSchedulingIntent(t, fmt.Sprintf("fretry-%d", index))
			first, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				Limit:               100,
				IdempotencyKey:      "fault-retry-initial-" + stage,
				CorrelationID:       "request-fault-retry-initial-" + stage,
			})
			if err != nil || len(first.ScheduledRunIDs) != 1 {
				t.Fatalf("RunSupervisor(%s initial) = %#v, %v", stage, first, err)
			}
			priorRunID := first.ScheduledRunIDs[0]
			if _, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
				WorkspaceIdentifier:  fixture.workspace.ID,
				Enabled:              true,
				AutoSchedule:         false,
				Limits:               domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8},
				AutoRetryLimit:       1,
				RetryCooldownSeconds: 30,
				ExpectedRevision:     2,
				IdempotencyKey:       "fault-retry-policy-" + stage,
				CorrelationID:        "request-fault-retry-policy-" + stage,
			}); err != nil {
				t.Fatalf("ConfigureSupervisorPolicy(%s retry) = %v", stage, err)
			}
			if _, err := storage.FailRunStart(context.Background(), priorRunID, "definite injected-barrier fixture", "request-fault-retry-failure-"+stage); err != nil {
				t.Fatalf("FailRunStart(%s) = %v", stage, err)
			}
			eligibleAt := storage.clock().UTC().Add(31 * time.Second)
			storage.clock = func() time.Time { return eligibleAt }
			command := RunSupervisorCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				Limit:               100,
				IdempotencyKey:      "fault-retry-scan-" + stage,
				CorrelationID:       "request-fault-retry-scan-" + stage,
			}
			storage.mutationHook = failManagementStage(stage)
			if _, err := storage.RunSupervisor(context.Background(), command); !errors.Is(err, errInjectedManagementBarrier) {
				t.Fatalf("RunSupervisor(%s retry fault) = %v; want injected fault", stage, err)
			}
			storage.mutationHook = nil
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE id=? AND status='start_failed'`, priorRunID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM run_retry_receipts WHERE prior_run_id=?`, priorRunID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions WHERE prior_run_id=? AND response='retry_task'`, priorRunID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, command.IdempotencyKey)

			retried, err := storage.RunSupervisor(context.Background(), command)
			if err != nil || len(retried.ScheduledRunIDs) != 1 || retried.ScheduledRunIDs[0] == priorRunID {
				t.Fatalf("RunSupervisor(%s retry recovery) = %#v, %v", stage, retried, err)
			}
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_retry_receipts WHERE prior_run_id=? AND run_id=?`, priorRunID, retried.ScheduledRunIDs[0])
		})
	}
}

func failManagementStage(wanted string) func(string) error {
	return func(observed string) error {
		if observed == wanted {
			return errInjectedManagementBarrier
		}
		return nil
	}
}

func singleTaskProposalCommand(fixture managerGrantAdversarialFixture, runID string, cursor int64, key string) SubmitManagerProposalCommand {
	return SubmitManagerProposalCommand{
		RunID: runID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		Kind: domain.ManagerProposalTaskDecomposition, Summary: "one exact task for named transaction fault coverage",
		AsOfEventSequence: cursor,
		Actions: []domain.ManagerProposalAction{{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "fault-task", LaunchProfileID: fixture.target.ID, Title: "Fault barrier " + key, Priority: 10,
			Budget: domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
		}}},
		IdempotencyKey: key + "-submit", CorrelationID: "request-" + key + "-submit",
	}
}

func acceptedSingleSchedulingIntent(t *testing.T, key string) (*Store, managerGrantAdversarialFixture, string, string) {
	t.Helper()
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{TargetMaxConcurrency: 8, SharedTargetCheckout: true})
	runID, cursor := invokeAdversarialManager(t, storage, fixture, key)
	submitted, err := storage.SubmitManagerProposal(context.Background(), singleTaskProposalCommand(fixture, runID, cursor, key))
	if err != nil || submitted.Proposal.Status != domain.ManagerProposalPending {
		t.Fatalf("SubmitManagerProposal(%s scheduling fixture) = %#v, %v", key, submitted, err)
	}
	accepted, err := storage.AcceptManagerProposal(context.Background(), AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ManagerProposalID:   submitted.Proposal.ID,
		ExpectedRevision:    submitted.Proposal.Revision,
		DecisionNote:        "accept named transaction fault fixture",
		IdempotencyKey:      key + "-accept",
		CorrelationID:       "request-" + key + "-accept",
	})
	if err != nil || accepted.Proposal.Status != domain.ManagerProposalAccepted {
		t.Fatalf("AcceptManagerProposal(%s scheduling fixture) = %#v, %v", key, accepted, err)
	}
	if _, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		Enabled:             true,
		AutoSchedule:        true,
		Limits:              domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8},
		ExpectedRevision:    1,
		IdempotencyKey:      key + "-policy",
		CorrelationID:       "request-" + key + "-policy",
	}); err != nil {
		t.Fatalf("ConfigureSupervisorPolicy(%s scheduling fixture) = %v", key, err)
	}
	var intentID string
	if err := storage.db.QueryRow(`SELECT id FROM scheduling_intents WHERE source_proposal_id=?`, submitted.Proposal.ID).Scan(&intentID); err != nil {
		t.Fatalf("read %s scheduling intent = %v", key, err)
	}
	return storage, fixture, submitted.Proposal.ID, intentID
}
