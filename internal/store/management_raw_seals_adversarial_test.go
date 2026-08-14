package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"crewfold/internal/domain"
)

// A proposal decision is the immutable receipt for the complete accepted
// effect set. Once it exists, raw SQL must not be able to append another
// purported effect that was neither counted nor hashed by that receipt.
func TestManagerProposalRejectsRawEffectAppendAfterDecision(t *testing.T) {
	storage, fixture := createManagerGrantAdversarialFixture(t)
	acceptAdversarialSchedulingPair(t, storage, fixture, "raw-post-decision-effect", "")

	var proposalID, actionID, objectiveID, entityType, entityID string
	var sealedEffectCount int
	if err := storage.db.QueryRow(`
SELECT proposal.id, action.id, proposal.objective_id, decision.effect_count,
       donor_effect.entity_type, donor_effect.entity_id
FROM manager_proposals proposal
JOIN manager_proposal_actions action ON action.proposal_id=proposal.id AND action.ordinal=0
JOIN manager_proposal_actions donor_action ON donor_action.proposal_id=proposal.id AND donor_action.ordinal=1
JOIN manager_proposal_effects donor_effect ON donor_effect.proposal_id=proposal.id
  AND donor_effect.action_id=donor_action.id AND donor_effect.effect_type='created'
  AND donor_effect.entity_type='task'
JOIN manager_proposal_decisions decision ON decision.proposal_id=proposal.id
WHERE proposal.workspace_id=? AND proposal.status='accepted'
ORDER BY proposal.created_at DESC LIMIT 1`, fixture.workspace.ID).Scan(
		&proposalID, &actionID, &objectiveID, &sealedEffectCount, &entityType, &entityID,
	); err != nil {
		t.Fatalf("read accepted proposal decision fixture = %v", err)
	}
	// Create a fresh otherwise-authorized event so rejection cannot be caused by
	// the effect table's unique event_sequence constraint. The decision seal is
	// the only remaining barrier under test.
	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin fresh post-decision effect event = %v", err)
	}
	occurredAt := storage.nowText()
	eventSequence, err := appendEvent(context.Background(), tx, fixture.workspace.ID, entityType, entityID, 1,
		taskCreated, "raw-post-decision-effect-event", occurredAt, map[string]any{"fixture": "post-decision append"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("append fresh post-decision effect event = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit fresh post-decision effect event = %v", err)
	}

	_, err = storage.db.Exec(`INSERT INTO manager_proposal_effects(
id,workspace_id,project_id,objective_id,proposal_id,action_id,effect_type,entity_type,entity_id,event_sequence,created_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"mpeff_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", fixture.workspace.ID, fixture.project.ID, objectiveID,
		proposalID, actionID, "created", entityType, entityID,
		eventSequence, occurredAt,
	)
	if err == nil {
		t.Fatal("raw manager proposal effect append after the sealed decision unexpectedly succeeded")
	}
	assertManagementRowCount(t, storage, sealedEffectCount,
		`SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=?`, proposalID)
}

// A valid content hash proves only that the row is self-consistent. An
// unreceipted raw row is quarantined: it is invisible and cannot occupy the
// unique condition authority carried only by an authenticated receipt.
func TestSupervisorRejectsRawSelfHashedActionWithoutReceipt(t *testing.T) {
	storage, workspace, blocked := createBlockedRunWithSupervisorPolicy(t)
	policy, err := storage.SupervisorPolicy(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("SupervisorPolicy(raw action fixture) = %v", err)
	}
	var cursor int64
	if err := storage.db.QueryRow(`SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?`, workspace.ID).Scan(&cursor); err != nil {
		t.Fatalf("read raw action cursor = %v", err)
	}
	now := storage.clock().UTC().Format(time.RFC3339Nano)
	action, err := newSupervisorAction(policy, cursor, domain.SupervisorConditionBlocked,
		domain.SupervisorResponseResumeRun, domain.SupervisorActionAwaitingApproval,
		blocked.Task, &blocked.Run, nil, []string{"forged self-consistent action"}, map[string]any{"source": "raw-sql"}, now)
	if err != nil {
		t.Fatalf("newSupervisorAction(raw action fixture) = %v", err)
	}
	action.ApprovalID = "appr_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	reasonsJSON, err := json.Marshal(action.Reasons)
	if err != nil {
		t.Fatalf("marshal raw action reasons = %v", err)
	}
	constraintsJSON, err := json.Marshal(action.ConstraintSnapshot)
	if err != nil {
		t.Fatalf("marshal raw action constraints = %v", err)
	}

	_, err = storage.db.Exec(`INSERT INTO supervisor_actions(
id,workspace_id,project_id,objective_id,task_id,run_id,prior_run_id,source_proposal_id,source_action_id,
agent_id,intent_id,condition,condition_key,response,status,decision,entity_revision,policy_revision,
as_of_event_sequence,reasons_json,constraint_snapshot_json,content_sha256,approval_id,revision,
created_at,updated_at,applied_at,created_by,updated_by
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		action.ID, action.WorkspaceID, nullStringForRawSeal(action.ProjectID), nullStringForRawSeal(action.ObjectiveID), nullStringForRawSeal(action.TaskID), nullStringForRawSeal(action.RunID),
		nil, nil, nil, action.AgentID, nil, action.Condition, action.ConditionKey, action.Response, action.Status,
		nil, action.EntityRevision, action.PolicyRevision, action.AsOfEventSequence, string(reasonsJSON),
		string(constraintsJSON), action.ContentSHA256, action.ApprovalID, 1, action.CreatedAt, action.UpdatedAt,
		nil, action.CreatedBy, action.UpdatedBy,
	)
	if err != nil {
		t.Fatalf("insert quarantined raw self-hashed supervisor action = %v", err)
	}
	if _, err := storage.SupervisorAction(context.Background(), workspace.ID, action.ID); ErrorCode(err) != CodeSupervisorActionNotFound {
		t.Fatalf("SupervisorAction(unreceipted raw row) = %v, code %q; want not found", err, ErrorCode(err))
	}
	listed, err := storage.SupervisorActions(context.Background(), ListSupervisorActionsQuery{WorkspaceIdentifier: workspace.ID, Limit: 100})
	if err != nil || len(listed) != 0 {
		t.Fatalf("SupervisorActions(unreceipted raw row) = %#v, %v; want invisible", listed, err)
	}

	// Even a matching raw event cannot mint the connection-authorized seal.
	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin raw action event = %v", err)
	}
	eventSequence, err := appendEventForActor(context.Background(), tx, workspace.ID, "supervisor_action", action.ID, 1,
		supervisorActionRecordedEvent, "raw-action-event", action.CreatedAt, "subsystem:supervisor", "subsystem",
		map[string]any{"condition": action.Condition, "response": action.Response, "approval_id": action.ApprovalID})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("append raw matching action event = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit raw matching action event = %v", err)
	}
	if _, err := storage.db.Exec(`INSERT INTO supervisor_action_receipts(action_id,workspace_id,condition_key,event_sequence,recorded_status,recorded_at) VALUES (?,?,?,?,?,?)`,
		action.ID, workspace.ID, action.ConditionKey, eventSequence, action.Status, action.CreatedAt); err == nil {
		t.Fatal("raw action event plus receipt unexpectedly minted supervisor authority")
	}

	// The quarantined row does not poison the canonical condition key. A normal
	// scan must still materialize exactly one visible owner decision.
	result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: workspace.ID, Limit: 100,
		IdempotencyKey: "canonical-after-raw-action", CorrelationID: "request-canonical-after-raw-action",
	})
	if err != nil || len(result.Actions) != 1 || result.Actions[0].ConditionKey != action.ConditionKey {
		t.Fatalf("RunSupervisor(after quarantined raw action) = %#v, %v; want canonical action", result, err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM supervisor_action_receipts WHERE condition_key=?`, action.ConditionKey)
}

func nullStringForRawSeal(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// Terminal runs and completed jobs are an append-only execution fact. Raw SQL
// must not be able to roll them back to the original requested/pending tuple,
// for which the original run.requested event and scheduling receipt still
// exist, and thereby make the worker launch the same run twice.
func TestSupervisorRejectsRawTerminalRunRollbackAndRelaunch(t *testing.T) {
	storage, fixture := createManagerGrantAdversarialFixture(t)
	acceptAdversarialSchedulingPair(t, storage, fixture, "raw-terminal-run-relaunch", "")
	configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 8, MaxStartingRuns: 2, DefaultProjectConcurrency: 4, DefaultProviderConcurrency: 4,
	}, "raw-terminal-run-relaunch")
	scan, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "raw-terminal-run-relaunch-scan", CorrelationID: "request-raw-terminal-run-relaunch-scan",
	})
	if err != nil || len(scan.ScheduledRunIDs) == 0 {
		t.Fatalf("RunSupervisor(raw terminal fixture) = %#v, %v", scan, err)
	}
	runID := scan.ScheduledRunIDs[0]
	if _, err := storage.MarkRunStarting(context.Background(), runID, "request-raw-terminal-starting"); err != nil {
		t.Fatalf("MarkRunStarting(raw terminal fixture) = %v", err)
	}
	if _, err := storage.FailRunStart(context.Background(), runID, "definite provider start failure", "request-raw-terminal-failed"); err != nil {
		t.Fatalf("FailRunStart(raw terminal fixture) = %v", err)
	}

	_, resetErr := storage.db.Exec(`UPDATE runs SET
status='requested', step_cursor=0, runtime_handle=NULL, provider_handle=NULL,
blocked_question=NULL, result_summary=NULL, failure_code=NULL, failure_message=NULL,
stop_grace_millis=0, stop_forced=0, revision=1, updated_at=created_at,
started_at=NULL, finished_at=NULL, updated_by=created_by
WHERE id=?`, runID)
	if resetErr != nil {
		return
	}
	_, jobResetErr := storage.db.Exec(`UPDATE run_jobs SET
status='pending', available_at=(SELECT created_at FROM runs WHERE id=run_jobs.run_id),
lease_expires_at=NULL, attempts=0,
updated_at=(SELECT created_at FROM runs WHERE id=run_jobs.run_id)
WHERE run_id=?`, runID)
	if jobResetErr != nil {
		t.Fatalf("terminal run rollback succeeded before job rollback was rejected: %v", jobResetErr)
	}
	work, found, claimErr := storage.ClaimRunLaunchJob(context.Background(), 30*time.Second)
	if claimErr != nil {
		t.Fatalf("ClaimRunLaunchJob after raw terminal rollback = %v", claimErr)
	}
	if found && work.Run.ID == runID {
		t.Fatalf("raw terminal run/job rollback relaunched run %s using its original receipt and event", runID)
	}
	if !found {
		t.Fatalf("raw terminal run/job rollback succeeded for %s; worker did not reject the corrupted pending job", runID)
	}
	t.Fatalf("raw terminal run rollback succeeded for %s; worker claimed unrelated run %s", runID, work.Run.ID)
}
