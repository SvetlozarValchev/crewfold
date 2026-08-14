package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"crewfold/internal/domain"
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
		if !domain.KnownEventType(eventType) {
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

// The M17 check watcher crosses the same workspace journal as the supervisor.
// Keeping a separately named classifier makes its raw cursor proof explicit
// while preserving one closed fact vocabulary for a given schema binary.
func knownCheckWatchJournalEvent(value string) bool {
	return domain.KnownEventType(value)
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
