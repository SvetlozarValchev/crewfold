package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestDisablingSupervisorPolicyClosesStartFailureWaitingForRetry(t *testing.T) {
	const key = "policy-disable-after-start-failure"
	storage, fixture, _, intentID := acceptedSingleSchedulingIntent(t, key)
	current, err := storage.SupervisorPolicy(context.Background(), fixture.workspace.ID)
	if err != nil {
		t.Fatalf("SupervisorPolicy(before retry enable) = %v", err)
	}
	retryPolicy, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier:  fixture.workspace.ID,
		Enabled:              true,
		AutoSchedule:         true,
		Limits:               current.Limits,
		AutoRetryLimit:       1,
		RetryCooldownSeconds: 60,
		ExpectedRevision:     current.Revision,
		IdempotencyKey:       key + "-enable-retry",
		CorrelationID:        "request-" + key + "-enable-retry",
	})
	if err != nil {
		t.Fatalf("ConfigureSupervisorPolicy(enable retry) = %v", err)
	}
	runID := scheduleSingleAcceptedIntent(t, storage, fixture.workspace.ID, intentID, key)
	if _, err := storage.MarkRunStarting(context.Background(), runID, "request-"+key+"-starting"); err != nil {
		t.Fatalf("MarkRunStarting() = %v", err)
	}
	failed, err := storage.FailRunStart(context.Background(), runID, "definite failure waiting for policy retry", "request-"+key+"-failed")
	if err != nil || failed.Run.Status != domain.RunStartFailed {
		t.Fatalf("FailRunStart() = %#v, %v", failed, err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status='run_requested'`, intentID)

	disabled, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier:  fixture.workspace.ID,
		Enabled:              false,
		AutoSchedule:         false,
		Limits:               retryPolicy.Value.Limits,
		AutoRetryLimit:       0,
		RetryCooldownSeconds: 0,
		ExpectedRevision:     retryPolicy.Value.Revision,
		IdempotencyKey:       key + "-disable",
		CorrelationID:        "request-" + key + "-disable",
	})
	if err != nil || disabled.Value.Enabled {
		t.Fatalf("ConfigureSupervisorPolicy(disable after failure) = %#v, %v", disabled.Value, err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status='failed' AND reason LIKE '%retry is disabled%'`, intentID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type=? AND json_extract(data_json,'$.run_id')=?`, intentID, schedulingIntentFailedEvent, runID)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM run_retry_receipts WHERE prior_run_id=?`, runID)
}

func TestReenablingRetrySkipsClosedStartFailureIntentAndSchedulesLaterWork(t *testing.T) {
	const key = "policy-reenable-after-closed-start-failure"
	ctx := context.Background()
	storage, fixture, proposalID, intentID := acceptedSingleSchedulingIntent(t, key)
	observed := time.Now().UTC().Add(time.Minute)
	storage.clock = func() time.Time { return observed }

	priorRunID := scheduleSingleAcceptedIntent(t, storage, fixture.workspace.ID, intentID, key)
	if _, err := storage.MarkRunStarting(ctx, priorRunID, "request-"+key+"-starting"); err != nil {
		t.Fatalf("MarkRunStarting() = %v", err)
	}
	failed, err := storage.FailRunStart(ctx, priorRunID, "definite failure with retry disabled", "request-"+key+"-failed")
	if err != nil || failed.Run.Status != domain.RunStartFailed {
		t.Fatalf("FailRunStart() = %#v, %v", failed, err)
	}
	closedBefore, err := querySchedulingIntent(ctx, storage.db, fixture.workspace.ID, intentID)
	if err != nil || closedBefore.Status != domain.SchedulingIntentFailed || closedBefore.RunID != priorRunID {
		t.Fatalf("closed scheduling intent = %#v, %v; want failed intent for %s", closedBefore, err, priorRunID)
	}
	priorBefore, err := storage.RunDetail(ctx, fixture.workspace.ID, priorRunID)
	if err != nil {
		t.Fatalf("RunDetail(prior before re-enable) = %v", err)
	}

	sourceProposal, err := storage.ManagerProposal(ctx, fixture.workspace.ID, proposalID)
	if err != nil {
		t.Fatalf("ManagerProposal(source) = %v", err)
	}
	laterKey := key + "-later"
	laterSubmitted, err := storage.SubmitManagerProposal(ctx, singleTaskProposalCommand(
		fixture, sourceProposal.SourceRunID, sourceProposal.AsOfEventSequence, laterKey,
	))
	if err != nil || laterSubmitted.Proposal.Status != domain.ManagerProposalPending {
		t.Fatalf("SubmitManagerProposal(later work) = %#v, %v", laterSubmitted, err)
	}
	observed = observed.Add(time.Nanosecond)
	laterAccepted, err := storage.AcceptManagerProposal(ctx, AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ManagerProposalID:   laterSubmitted.Proposal.ID,
		ExpectedRevision:    laterSubmitted.Proposal.Revision,
		DecisionNote:        "accept later work after the earlier retry intent closed",
		IdempotencyKey:      laterKey + "-accept",
		CorrelationID:       "request-" + laterKey + "-accept",
	})
	if err != nil || laterAccepted.Proposal.Status != domain.ManagerProposalAccepted {
		t.Fatalf("AcceptManagerProposal(later work) = %#v, %v", laterAccepted, err)
	}
	var laterIntentID string
	if err := storage.db.QueryRow(`SELECT id FROM scheduling_intents WHERE source_proposal_id=?`, laterSubmitted.Proposal.ID).Scan(&laterIntentID); err != nil {
		t.Fatalf("read later scheduling intent = %v", err)
	}

	current, err := storage.SupervisorPolicy(ctx, fixture.workspace.ID)
	if err != nil {
		t.Fatalf("SupervisorPolicy(before re-enable) = %v", err)
	}
	reenabled, err := storage.ConfigureSupervisorPolicy(ctx, ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier:  fixture.workspace.ID,
		Enabled:              true,
		AutoSchedule:         true,
		Limits:               current.Limits,
		AutoRetryLimit:       1,
		RetryCooldownSeconds: 1,
		ExpectedRevision:     current.Revision,
		IdempotencyKey:       key + "-reenable",
		CorrelationID:        "request-" + key + "-reenable",
	})
	if err != nil || reenabled.Value.AutoRetryLimit != 1 {
		t.Fatalf("ConfigureSupervisorPolicy(re-enable retry) = %#v, %v", reenabled.Value, err)
	}
	observed = observed.Add(2 * time.Second)

	scan, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		Limit:               100,
		IdempotencyKey:      key + "-scan",
		CorrelationID:       "request-" + key + "-scan",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(after retry re-enable) = %v", err)
	}
	if len(scan.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(after retry re-enable) = %#v; want later eligible intent scheduled once", scan)
	}
	for _, action := range scan.Actions {
		if action.PriorRunID == priorRunID || action.Response == domain.SupervisorResponseRetryTask && action.IntentID == intentID {
			t.Fatalf("RunSupervisor resurrected closed retry intent: %#v", action)
		}
	}
	var scheduledIntentID string
	if err := storage.db.QueryRow(`SELECT intent_id FROM run_scheduling_receipts WHERE run_id=?`, scan.ScheduledRunIDs[0]).Scan(&scheduledIntentID); err != nil {
		t.Fatalf("read later scheduling receipt = %v", err)
	}
	if scheduledIntentID != laterIntentID {
		t.Fatalf("scheduled intent = %q, want later eligible intent %q", scheduledIntentID, laterIntentID)
	}

	closedAfter, err := querySchedulingIntent(ctx, storage.db, fixture.workspace.ID, intentID)
	if err != nil {
		t.Fatalf("query closed scheduling intent after scan = %v", err)
	}
	if closedAfter != closedBefore {
		t.Fatalf("closed scheduling intent changed after retry re-enable:\n before: %#v\n  after: %#v", closedBefore, closedAfter)
	}
	priorAfter, err := storage.RunDetail(ctx, fixture.workspace.ID, priorRunID)
	if err != nil {
		t.Fatalf("RunDetail(prior after re-enable) = %v", err)
	}
	if priorAfter.Run.Status != priorBefore.Run.Status || priorAfter.Run.Revision != priorBefore.Run.Revision ||
		priorAfter.Run.FailureCode != priorBefore.Run.FailureCode || priorAfter.Run.FailureMessage != priorBefore.Run.FailureMessage ||
		priorAfter.Run.UpdatedAt != priorBefore.Run.UpdatedAt || priorAfter.Run.FinishedAt != priorBefore.Run.FinishedAt {
		t.Fatalf("prior start-failed run changed after retry re-enable:\n before: %#v\n  after: %#v", priorBefore.Run, priorAfter.Run)
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM run_retry_receipts WHERE prior_run_id=?`, priorRunID)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions WHERE prior_run_id=? OR (intent_id=? AND response='retry_task')`, priorRunID, intentID)
}
