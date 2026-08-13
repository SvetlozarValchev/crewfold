package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestOwnerAssignTaskRejectsOpenSchedulingIntent(t *testing.T) {
	for _, status := range []string{domain.SchedulingIntentPending, domain.SchedulingIntentDeferred} {
		t.Run(status, func(t *testing.T) {
			storage, fixture, taskID, intentID, taskRevision, intentRevision := ownerIntentFixture(t, "assign-"+status, status)
			var eventsBefore int
			if err := storage.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventsBefore); err != nil {
				t.Fatalf("count events before owner assignment = %v", err)
			}

			_, err := storage.AssignTask(context.Background(), AssignTaskCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				TaskID:              taskID,
				AgentIdentifier:     fixture.target.AgentID,
				LeaseSeconds:        300,
				ExpectedRevision:    taskRevision,
				IdempotencyKey:      "manual-assign-" + status,
				CorrelationID:       "request-manual-assign-" + status,
			})
			if ErrorCode(err) != CodeAssignmentConflict || !strings.Contains(err.Error(), intentID) {
				t.Fatalf("AssignTask(%s intent) error = %v, code %q; want %q naming %s", status, err, ErrorCode(err), CodeAssignmentConflict, intentID)
			}

			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM task_assignments WHERE task_id=?`, taskID)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM tasks WHERE id=? AND status='ready' AND revision=?`, taskID, taskRevision)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status=? AND revision=?`, intentID, status, intentRevision)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, "manual-assign-"+status)
			assertManagementRowCount(t, storage, eventsBefore, `SELECT COUNT(*) FROM events`)
		})
	}
}

func TestOwnerTaskCancellationClosesPendingOrDeferredSchedulingIntent(t *testing.T) {
	for _, status := range []string{domain.SchedulingIntentPending, domain.SchedulingIntentDeferred} {
		t.Run(status, func(t *testing.T) {
			storage, fixture, taskID, intentID, taskRevision, intentRevision := ownerIntentFixture(t, "cancel-"+status, status)
			correlationID := "request-owner-cancel-" + status
			command := TransitionTaskCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				TaskID:              taskID,
				Action:              "cancel",
				ExpectedRevision:    taskRevision,
				IdempotencyKey:      "owner-cancel-" + status,
				CorrelationID:       correlationID,
			}

			cancelled, err := storage.TransitionTask(context.Background(), command)
			if err != nil {
				t.Fatalf("TransitionTask(cancel %s intent) = %v", status, err)
			}
			if cancelled.Detail.Task.Status != domain.TaskCancelled || cancelled.Detail.Task.Revision != taskRevision+1 || cancelled.Detail.Assignment != nil {
				t.Fatalf("TransitionTask(cancel %s intent) = %#v; want cancelled unassigned revision %d", status, cancelled, taskRevision+1)
			}

			var intentStatus, reason, updatedAt, updatedBy string
			var nextAttemptAt sql.NullString
			var storedIntentRevision int64
			if err := storage.db.QueryRow(`SELECT status,reason,revision,updated_at,next_attempt_at,updated_by FROM scheduling_intents WHERE id=?`, intentID).
				Scan(&intentStatus, &reason, &storedIntentRevision, &updatedAt, &nextAttemptAt, &updatedBy); err != nil {
				t.Fatalf("read owner-cancelled %s intent = %v", status, err)
			}
			if intentStatus != domain.SchedulingIntentCancelled || reason != ownerTaskCancellationIntentReason || storedIntentRevision != intentRevision+1 || nextAttemptAt.Valid || updatedBy != localOwnerActorID {
				t.Fatalf("owner-cancelled %s intent = status %q reason %q revision %d next %#v updated_by %q", status, intentStatus, reason, storedIntentRevision, nextAttemptAt, updatedBy)
			}

			var taskEventSequence, taskEventRevision int64
			var taskOccurredAt, taskActorID, taskActorType, taskData string
			if err := storage.db.QueryRow(`SELECT sequence,entity_revision,occurred_at,actor_id,actor_type,data_json FROM events WHERE entity_type='task' AND entity_id=? AND type='task.cancelled' AND correlation_id=?`, taskID, correlationID).
				Scan(&taskEventSequence, &taskEventRevision, &taskOccurredAt, &taskActorID, &taskActorType, &taskData); err != nil {
				t.Fatalf("read task.cancelled event = %v", err)
			}
			if taskEventSequence != cancelled.EventSequence || taskEventRevision != taskRevision+1 || taskOccurredAt != updatedAt || taskActorID != localOwnerActorID || taskActorType != localActorType {
				t.Fatalf("task.cancelled event = sequence %d revision %d at %q actor %q/%q; result sequence %d intent at %q", taskEventSequence, taskEventRevision, taskOccurredAt, taskActorID, taskActorType, cancelled.EventSequence, updatedAt)
			}

			var intentEventSequence, intentEventRevision int64
			var intentOccurredAt, intentActorID, intentActorType, intentData string
			if err := storage.db.QueryRow(`SELECT sequence,entity_revision,occurred_at,actor_id,actor_type,data_json FROM events WHERE entity_type='scheduling_intent' AND entity_id=? AND type=? AND correlation_id=?`, intentID, schedulingIntentCancelledEvent, correlationID).
				Scan(&intentEventSequence, &intentEventRevision, &intentOccurredAt, &intentActorID, &intentActorType, &intentData); err != nil {
				t.Fatalf("read %s event = %v", schedulingIntentCancelledEvent, err)
			}
			if intentEventSequence <= taskEventSequence || intentEventRevision != intentRevision+1 || intentOccurredAt != updatedAt || intentActorID != localOwnerActorID || intentActorType != localActorType {
				t.Fatalf("%s event = sequence %d revision %d at %q actor %q/%q; want after %d at %q", schedulingIntentCancelledEvent, intentEventSequence, intentEventRevision, intentOccurredAt, intentActorID, intentActorType, taskEventSequence, updatedAt)
			}
			assertOwnerCancellationEventData(t, taskData, "", domain.TaskCancelled, "")
			assertOwnerCancellationEventData(t, intentData, taskID, domain.SchedulingIntentCancelled, ownerTaskCancellationIntentReason)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE entity_type='task' AND entity_id=? AND type='task.cancelled'`, taskID)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE entity_type='scheduling_intent' AND entity_id=? AND type=?`, intentID, schedulingIntentCancelledEvent)

			replayed, err := storage.TransitionTask(context.Background(), command)
			if err != nil || replayed.EventSequence != cancelled.EventSequence || replayed.Detail.Task.Status != domain.TaskCancelled {
				t.Fatalf("TransitionTask(cancel %s replay) = %#v, %v; want original result", status, replayed, err)
			}
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE entity_type='task' AND entity_id=? AND type='task.cancelled'`, taskID)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE entity_type='scheduling_intent' AND entity_id=? AND type=?`, intentID, schedulingIntentCancelledEvent)
		})
	}
}

func TestOwnerTaskCancellationClosesRetryPendingIntentAtExactLatestStartFailure(t *testing.T) {
	const key = "owner-cancel-latest-retry-failure"
	ctx := context.Background()
	storage, fixture, _, intentID := acceptedSingleSchedulingIntent(t, key)
	var latestTimestamp string
	if err := storage.db.QueryRow(`SELECT MAX(occurred_at) FROM events WHERE workspace_id=?`, fixture.workspace.ID).Scan(&latestTimestamp); err != nil {
		t.Fatalf("read latest fixture timestamp = %v", err)
	}
	observed, err := time.Parse(time.RFC3339Nano, latestTimestamp)
	if err != nil {
		t.Fatalf("parse latest fixture timestamp %q = %v", latestTimestamp, err)
	}
	observed = observed.Add(time.Second)
	storage.clock = func() time.Time { return observed }
	tick := func(seconds int) { observed = observed.Add(time.Duration(seconds) * time.Second) }

	policy, err := storage.SupervisorPolicy(ctx, fixture.workspace.ID)
	if err != nil {
		t.Fatalf("SupervisorPolicy(owner cancellation retry fixture) = %v", err)
	}
	configured, err := storage.ConfigureSupervisorPolicy(ctx, ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier:  fixture.workspace.ID,
		Enabled:              true,
		AutoSchedule:         true,
		Limits:               policy.Limits,
		AutoRetryLimit:       2,
		RetryCooldownSeconds: 30,
		ExpectedRevision:     policy.Revision,
		IdempotencyKey:       key + "-enable-retry",
		CorrelationID:        "request-" + key + "-enable-retry",
	})
	if err != nil || configured.Value.AutoRetryLimit != 2 {
		t.Fatalf("ConfigureSupervisorPolicy(owner cancellation retry fixture) = %#v, %v", configured.Value, err)
	}

	tick(1)
	originalRunID := scheduleSingleAcceptedIntent(t, storage, fixture.workspace.ID, intentID, key)
	tick(1)
	if _, err := storage.MarkRunStarting(ctx, originalRunID, "request-"+key+"-original-starting"); err != nil {
		t.Fatalf("MarkRunStarting(original) = %v", err)
	}
	tick(1)
	if _, err := storage.FailRunStart(ctx, originalRunID, "first definite start failure", "request-"+key+"-original-failed"); err != nil {
		t.Fatalf("FailRunStart(original) = %v", err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status='run_requested'`, intentID)

	tick(31)
	retried, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		Limit:               100,
		IdempotencyKey:      key + "-first-retry",
		CorrelationID:       "request-" + key + "-first-retry",
	})
	if err != nil || len(retried.ScheduledRunIDs) != 1 || retried.ScheduledRunIDs[0] == originalRunID {
		t.Fatalf("RunSupervisor(first retry) = %#v, %v", retried, err)
	}
	retryRunID := retried.ScheduledRunIDs[0]
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_retry_receipts WHERE intent_id=? AND prior_run_id=? AND run_id=? AND attempt=1`, intentID, originalRunID, retryRunID)

	tick(1)
	if _, err := storage.MarkRunStarting(ctx, retryRunID, "request-"+key+"-retry-starting"); err != nil {
		t.Fatalf("MarkRunStarting(retry) = %v", err)
	}
	tick(1)
	if _, err := storage.FailRunStart(ctx, retryRunID, "latest definite start failure", "request-"+key+"-retry-failed"); err != nil {
		t.Fatalf("FailRunStart(retry) = %v", err)
	}
	var taskID string
	var taskRevision, intentRevision int64
	if err := storage.db.QueryRow(`SELECT intent.task_id,intent.revision,task.revision
FROM scheduling_intents intent JOIN tasks task ON task.id=intent.task_id
WHERE intent.id=? AND intent.status='run_requested'`, intentID).Scan(&taskID, &intentRevision, &taskRevision); err != nil {
		t.Fatalf("read retry-pending owner cancellation target = %v", err)
	}

	tick(1)
	correlationID := "request-" + key + "-cancel"
	cancelled, err := storage.TransitionTask(ctx, TransitionTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		TaskID:              taskID,
		Action:              "cancel",
		ExpectedRevision:    taskRevision,
		IdempotencyKey:      key + "-cancel",
		CorrelationID:       correlationID,
	})
	if err != nil || cancelled.Detail.Task.Status != domain.TaskCancelled {
		t.Fatalf("TransitionTask(cancel retry-pending intent) = %#v, %v", cancelled, err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents
WHERE id=? AND status='cancelled' AND revision=? AND reason=?`, intentID, intentRevision+1, ownerTaskCancellationIntentReason)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events
WHERE entity_type='scheduling_intent' AND entity_id=? AND entity_revision=?
  AND type='supervisor.intent_cancelled' AND correlation_id=?
  AND actor_id='local-owner' AND actor_type='human'
  AND json_extract(data_json,'$.task_id')=?
  AND json_extract(data_json,'$.run_id')=?
  AND json_extract(data_json,'$.run_status')='start_failed'
  AND json_extract(data_json,'$.status')='cancelled'
  AND json_extract(data_json,'$.reason')=?`, intentID, intentRevision+1, correlationID, taskID, retryRunID, ownerTaskCancellationIntentReason)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM task_assignments
WHERE id=(SELECT assignment_id FROM scheduling_intents WHERE id=?) AND status='released'`, intentID)

	tick(31)
	for scan := 1; scan <= 2; scan++ {
		later, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
			WorkspaceIdentifier: fixture.workspace.ID,
			Limit:               100,
			IdempotencyKey:      fmt.Sprintf("%s-after-cancel-%d", key, scan),
			CorrelationID:       fmt.Sprintf("request-%s-after-cancel-%d", key, scan),
		})
		if err != nil {
			t.Fatalf("RunSupervisor(after owner cancellation %d) = %v", scan, err)
		}
		if len(later.ScheduledRunIDs) != 0 {
			t.Fatalf("RunSupervisor(after owner cancellation %d) scheduled %#v; want no retry", scan, later.ScheduledRunIDs)
		}
		for _, action := range later.Actions {
			if action.Response == domain.SupervisorResponseRetryTask {
				t.Fatalf("RunSupervisor(after owner cancellation %d) retried cancelled intent: %#v", scan, action)
			}
		}
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_retry_receipts WHERE intent_id=?`, intentID)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM run_retry_receipts WHERE prior_run_id=?`, retryRunID)
}

func TestSchedulingIntentTriggerRejectsUnsealedOwnerCancellation(t *testing.T) {
	storage, _, _, intentID, _, intentRevision := ownerIntentFixture(t, "raw-owner-cancel", domain.SchedulingIntentPending)
	_, err := storage.db.Exec(`UPDATE scheduling_intents
SET status='cancelled',reason=?,revision=?,updated_at=?,next_attempt_at=NULL,updated_by='local-owner'
WHERE id=?`, ownerTaskCancellationIntentReason, intentRevision+1, storage.nowText(), intentID)
	if err == nil || !strings.Contains(err.Error(), "invalid scheduling intent lifecycle") {
		t.Fatalf("raw unsealed owner cancellation error = %v; want lifecycle trigger rejection", err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status='pending' AND revision=?`, intentID, intentRevision)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM events WHERE entity_type='scheduling_intent' AND entity_id=? AND type=?`, intentID, schedulingIntentCancelledEvent)
}

func ownerIntentFixture(t *testing.T, key, status string) (*Store, managerGrantAdversarialFixture, string, string, int64, int64) {
	t.Helper()
	storage, fixture, _, intentID := acceptedSingleSchedulingIntent(t, key)
	if status == domain.SchedulingIntentDeferred {
		if _, err := storage.RetireLaunchProfile(context.Background(), RetireLaunchProfileCommand{
			WorkspaceIdentifier: fixture.workspace.ID,
			LaunchProfileID:     fixture.target.ID,
			ExpectedRevision:    fixture.target.Revision,
			Reason:              "force a bounded deferred intent fixture",
			IdempotencyKey:      key + "-retire",
			CorrelationID:       "request-" + key + "-retire",
		}); err != nil {
			t.Fatalf("RetireLaunchProfile(%s fixture) = %v", key, err)
		}
		for attempt := 1; attempt <= 3; attempt++ {
			var observed string
			if err := storage.db.QueryRow(`SELECT status FROM scheduling_intents WHERE id=?`, intentID).Scan(&observed); err != nil {
				t.Fatalf("read %s fixture intent status = %v", key, err)
			}
			if observed == status {
				break
			}
			if observed != domain.SchedulingIntentPending {
				t.Fatalf("%s fixture intent status = %q; want pending or deferred", key, observed)
			}
			if _, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				Limit:               100,
				IdempotencyKey:      fmt.Sprintf("%s-defer-%d", key, attempt),
				CorrelationID:       fmt.Sprintf("request-%s-defer-%d", key, attempt),
			}); err != nil {
				t.Fatalf("RunSupervisor(%s deferred fixture attempt %d) = %v", key, attempt, err)
			}
		}
	}

	var taskID, observedStatus string
	var taskRevision, intentRevision int64
	if err := storage.db.QueryRow(`SELECT intent.task_id,intent.status,intent.revision,task.revision
FROM scheduling_intents intent JOIN tasks task ON task.id=intent.task_id WHERE intent.id=?`, intentID).
		Scan(&taskID, &observedStatus, &intentRevision, &taskRevision); err != nil {
		t.Fatalf("read %s owner intent fixture = %v", key, err)
	}
	if observedStatus != status {
		t.Fatalf("%s owner intent fixture status = %q; want %q", key, observedStatus, status)
	}
	return storage, fixture, taskID, intentID, taskRevision, intentRevision
}

func assertOwnerCancellationEventData(t *testing.T, raw, taskID, status, reason string) {
	t.Helper()
	var data struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("decode owner cancellation event data %q = %v", raw, err)
	}
	if data.Status != status || data.Reason != reason || (taskID != "" && data.TaskID != taskID) {
		t.Fatalf("owner cancellation event data = %#v; want task %q status %q reason %q", data, taskID, status, reason)
	}
}
