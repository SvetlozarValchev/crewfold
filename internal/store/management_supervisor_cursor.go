package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const maximumSupervisorJournalEvents = 1000

// supervisorJournalInspection is the authority watermark for one supervisor
// pass. Automation may inspect projections only when CaughtUp is true. A
// cursor-only page is deliberately effect-free and lets a large, completely
// understood backlog converge without ever acting across an unclassified
// journal interval.
type supervisorJournalInspection struct {
	From     int64
	Through  int64
	Cutoff   int64
	CaughtUp bool
}

func inspectSupervisorJournal(ctx context.Context, tx *sql.Tx, workspaceID string) (supervisorJournalInspection, error) {
	var result supervisorJournalInspection
	if err := tx.QueryRowContext(ctx, `SELECT last_event_sequence FROM supervisor_state WHERE workspace_id=?`, workspaceID).Scan(&result.From); err != nil {
		return result, storageFailure("read supervisor journal cursor", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?`, workspaceID).Scan(&result.Cutoff); err != nil {
		return result, storageFailure("read supervisor journal cutoff", err)
	}
	if result.From > result.Cutoff {
		return result, storageFailure("validate supervisor journal cursor", fmt.Errorf("cursor %d exceeds workspace cutoff %d", result.From, result.Cutoff))
	}
	result.Through = result.From
	if result.From >= result.Cutoff {
		result.CaughtUp = true
		return result, nil
	}

	rows, err := tx.QueryContext(ctx, `SELECT sequence,type FROM events
WHERE workspace_id=? AND sequence>? AND sequence<=?
ORDER BY sequence LIMIT ?`, workspaceID, result.From, result.Cutoff, maximumSupervisorJournalEvents)
	if err != nil {
		return result, storageFailure("inspect supervisor journal", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int64
		var eventType string
		if err := rows.Scan(&sequence, &eventType); err != nil {
			return result, storageFailure("scan supervisor journal", err)
		}
		if !knownSupervisorJournalEvent(eventType) {
			return result, &Error{
				Code:    CodeUnsupportedSupervisorEvent,
				Message: fmt.Sprintf("supervisor stopped before unsupported workspace event %q at sequence %d", eventType, sequence),
			}
		}
		result.Through = sequence
	}
	if err := rows.Err(); err != nil {
		return result, storageFailure("iterate supervisor journal", err)
	}
	result.CaughtUp = result.Through == result.Cutoff
	return result, nil
}

// This is intentionally a closed union. A binary that does not understand a
// newly introduced workspace fact must not silently advance automation beyond
// it. Command names and test-only types do not belong here: these are only the
// immutable event facts emitted by supported Store paths through schema 17.
func knownSupervisorJournalEvent(value string) bool {
	switch value {
	case
		"workspace.created",
		"project.registered", "repository.registered", "checkout.registered", "checkout.git_observed",
		"agent.created", "agent.updated", "objective.created", "objective.updated",
		"task.created", "task.updated", "task.dependency_added", "task.assigned", "task.assignment_expired",
		"task.started", "task.blocked", "task.readied", "task.cancelled", "task.completion_proposed",
		"task.changes_requested", "task.completed", "task.failed", "task.handoff_recorded", "task.run_stopped",
		"task.reassigned", "task.role_designated", "task.claim_requirement_created",
		"run.requested", "run.starting", "run.started", "run.runtime_observed", "run.progress_reported",
		"run.blocked", "run.completion_proposed", "run.completed", "run.start_failed", "run.failed",
		"run.resumed", "run.stop_requested", "run.stopped", "run.lost", "run.report_received",
		"run.artifact_published", "run.tool_called", "run.tool_denied",
		"claim.added", "claim.released", "claim.expired", "claim.drift_opened", "claim.drift_resolved",
		"overlap.opened", "overlap.resolved",
		"thread.created", "thread.participant_added", "message.sent", "message.delivered", "message.read",
		"message.acknowledged", "message.wake_succeeded", "message.wake_failed",
		"meeting.created", "meeting.positions_collected", "meeting.resolution_proposed", "meeting.stalled",
		"meeting.concluded", "meeting.human_takeover",
		"knowledge.proposed", "knowledge.accepted", "knowledge.rejected", "knowledge.marked_stale",
		"knowledge.superseded", "knowledge.acceptance_denied", "knowledge.rejection_denied",
		"knowledge.stale_denied", "knowledge.imported", "knowledge.import_completed",
		"contradiction.detected", "contradiction.confirmed", "contradiction.dismissed",
		"contradiction.resolved", "contradiction.confirm_denied", "contradiction.dismiss_denied",
		"contradiction.imported",
		"curator.rule_configured", "curator.derived", "curator.auto_accepted",
		"context.packet_built", "context_delta.built", "context_delta.acknowledged",
		"context_delta.rebase_required",
		"manager.grant_created", "manager.grant_revoked", "manager.launch_profile_created",
		"manager.launch_profile_retired", "manager.proposal_submitted", "manager.proposal_accepted",
		"manager.proposal_rejected", "manager.proposal_stale",
		"supervisor.policy_configured", "supervisor.intent_created", "supervisor.intent_satisfied",
		"supervisor.intent_failed", "supervisor.intent_cancelled", "supervisor.action_recorded",
		"supervisor.action_applied", "supervisor.scan_completed",
		"approval.requested", "approval.granted", "approval.denied", "approval.consumed", "approval.expired":
		return true
	default:
		return false
	}
}

func advanceSupervisorJournalCursor(ctx context.Context, tx *sql.Tx, workspaceID string, through int64, now string) error {
	var previous string
	if err := tx.QueryRowContext(ctx, `SELECT updated_at FROM supervisor_state WHERE workspace_id=?`, workspaceID).Scan(&previous); err != nil {
		return storageFailure("read supervisor journal cursor timestamp", err)
	}
	previousTime, previousErr := time.Parse(time.RFC3339Nano, previous)
	nowTime, nowErr := time.Parse(time.RFC3339Nano, now)
	if previousErr != nil || nowErr != nil {
		return storageFailure("normalize supervisor journal cursor timestamp", fmt.Errorf("previous=%q (%v), now=%q (%v)", previous, previousErr, now, nowErr))
	}
	if !nowTime.After(previousTime) {
		now = previousTime.Add(time.Nanosecond).UTC().Format(time.RFC3339Nano)
	}
	result, err := tx.ExecContext(ctx, `UPDATE supervisor_state
SET last_event_sequence=?,revision=revision+1,updated_at=?
WHERE workspace_id=? AND last_event_sequence<?`, through, now, workspaceID, through)
	if err != nil {
		return storageFailure("advance supervisor journal cursor", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storageFailure("read supervisor journal cursor update", err)
	}
	if changed != 1 {
		return storageFailure("advance supervisor journal cursor", fmt.Errorf("cursor was not advanced to %d", through))
	}
	return nil
}
