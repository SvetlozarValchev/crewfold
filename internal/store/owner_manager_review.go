package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"crewfold/internal/domain"
)

// enqueueOwnerManagerReview advances the single project review cursor inside
// the same transaction that records a worker-originated canonical event. A
// project is observed only after the owner has opened a workbench conversation;
// CLI-only projects therefore do not unexpectedly consume provider work.
func enqueueOwnerManagerReview(ctx context.Context, tx *sql.Tx, workspaceID, projectID string, eventSequence int64, now string) error {
	if workspaceID == "" || projectID == "" || eventSequence < 1 {
		return &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager review requires an exact project event"}
	}
	var conversationID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM owner_conversations
WHERE workspace_id=? AND project_id=? AND status='open'
ORDER BY updated_at DESC,id DESC LIMIT 1`, workspaceID, projectID).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return storageFailure("resolve owner manager review conversation", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO owner_manager_review_jobs(
project_id,workspace_id,conversation_id,status,requested_event_sequence,reviewed_event_sequence,attempts,available_at,lease_expires_at,last_turn_id,last_error,created_at,updated_at)
VALUES(?,?,?,'pending',?,0,0,?,NULL,NULL,NULL,?,?)
ON CONFLICT(project_id) DO UPDATE SET
 conversation_id=excluded.conversation_id,
 status=CASE WHEN owner_manager_review_jobs.status='leased' THEN 'leased' ELSE 'pending' END,
 requested_event_sequence=MAX(owner_manager_review_jobs.requested_event_sequence,excluded.requested_event_sequence),
 attempts=CASE WHEN owner_manager_review_jobs.status='leased' THEN owner_manager_review_jobs.attempts ELSE 0 END,
 available_at=CASE WHEN owner_manager_review_jobs.status='leased' THEN owner_manager_review_jobs.available_at ELSE excluded.available_at END,
 lease_expires_at=CASE WHEN owner_manager_review_jobs.status='leased' THEN owner_manager_review_jobs.lease_expires_at ELSE NULL END,
 last_error=NULL,
 updated_at=excluded.updated_at
WHERE excluded.requested_event_sequence>owner_manager_review_jobs.reviewed_event_sequence`,
		projectID, workspaceID, conversationID, eventSequence, now, now, now)
	if err != nil {
		return storageFailure("enqueue owner manager review", err)
	}
	return nil
}

func (s *Store) ClaimOwnerManagerReview(ctx context.Context, lease time.Duration) (domain.OwnerManagerReviewJob, bool, error) {
	if lease <= 0 || lease > 10*time.Minute {
		return domain.OwnerManagerReviewJob{}, false, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager review lease is outside its bound"}
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.OwnerManagerReviewJob{}, false, storageFailure("begin owner manager review claim", err)
	}
	defer tx.Rollback()
	now := s.nowText()
	if _, err := tx.ExecContext(ctx, `UPDATE owner_manager_review_jobs
SET status='pending',lease_expires_at=NULL,last_error=NULL,available_at=?,updated_at=?
WHERE status='leased' AND lease_expires_at<=?`, now, now, now); err != nil {
		return domain.OwnerManagerReviewJob{}, false, storageFailure("recover owner manager review lease", err)
	}
	var projectID string
	err = tx.QueryRowContext(ctx, `SELECT project_id FROM owner_manager_review_jobs
WHERE status='pending' AND available_at<=? ORDER BY available_at,project_id LIMIT 1`, now).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return domain.OwnerManagerReviewJob{}, false, storageFailure("finish empty owner manager review claim", err)
		}
		return domain.OwnerManagerReviewJob{}, false, nil
	}
	if err != nil {
		return domain.OwnerManagerReviewJob{}, false, storageFailure("select owner manager review", err)
	}
	expires := s.clock().UTC().Add(lease).Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE owner_manager_review_jobs
SET status='leased',attempts=attempts+1,lease_expires_at=?,updated_at=?
WHERE project_id=? AND status='pending'`, expires, now, projectID)
	if err != nil {
		return domain.OwnerManagerReviewJob{}, false, storageFailure("lease owner manager review", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return domain.OwnerManagerReviewJob{}, false, &Error{Code: CodeOwnerTurnConflict, Message: "owner manager review claim changed concurrently"}
	}
	job, err := ownerManagerReviewJobInTransaction(ctx, tx, projectID)
	if err != nil {
		return domain.OwnerManagerReviewJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.OwnerManagerReviewJob{}, false, storageFailure("commit owner manager review claim", err)
	}
	return job, true, nil
}

// RecoverOwnerManagerReviewLeases is called only after the daemon owns the
// exclusive data-directory lock. No previous manager worker can still commit,
// so an interrupted read-only interpretation is immediately replayable instead
// of waiting for its wall-clock lease to expire.
func (s *Store) RecoverOwnerManagerReviewLeases(ctx context.Context) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin owner manager review recovery", err)
	}
	defer tx.Rollback()
	now := s.nowText()
	if _, err := tx.ExecContext(ctx, `UPDATE owner_manager_review_jobs
SET status='pending',lease_expires_at=NULL,last_error=NULL,available_at=?,updated_at=?
WHERE status='leased'`, now, now); err != nil {
		return storageFailure("recover owner manager review leases", err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit owner manager review recovery", err)
	}
	return nil
}

func (s *Store) CompleteOwnerManagerReview(ctx context.Context, projectID string, reviewedEventSequence int64, turnID string) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin owner manager review completion", err)
	}
	defer tx.Rollback()
	job, err := ownerManagerReviewJobInTransaction(ctx, tx, strings.TrimSpace(projectID))
	if err != nil {
		return err
	}
	if job.Status != "leased" || reviewedEventSequence <= job.ReviewedEventSequence || reviewedEventSequence > job.RequestedEventSequence {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner manager review completion does not match its leased event cut"}
	}
	var turnProjectID, initiatedBy string
	var trigger int64
	err = tx.QueryRowContext(ctx, `SELECT conversation.project_id,turn.initiated_by,turn.trigger_event_sequence
FROM owner_turns turn JOIN owner_conversations conversation ON conversation.id=turn.conversation_id WHERE turn.id=?`, strings.TrimSpace(turnID)).Scan(&turnProjectID, &initiatedBy, &trigger)
	if err != nil || turnProjectID != job.ProjectID || initiatedBy != "executive" || trigger != reviewedEventSequence {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner manager review turn does not seal the leased project event cut", Cause: err}
	}
	now := s.nowText()
	status := "idle"
	if job.RequestedEventSequence > reviewedEventSequence {
		status = "pending"
	}
	_, err = tx.ExecContext(ctx, `UPDATE owner_manager_review_jobs SET
status=?,reviewed_event_sequence=?,attempts=0,available_at=?,lease_expires_at=NULL,last_turn_id=?,last_error=NULL,updated_at=?
WHERE project_id=? AND status='leased'`, status, reviewedEventSequence, now, turnID, now, job.ProjectID)
	if err != nil {
		return storageFailure("complete owner manager review", err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit owner manager review completion", err)
	}
	return nil
}

// DeferOwnerManagerReview returns a claimed cursor to the durable pending state
// without consuming an attempt. Worker facts that arrive while an executive
// exchange is live continue to advance requested_event_sequence on this same
// row, so one later review observes the complete coalesced cut.
func (s *Store) DeferOwnerManagerReview(ctx context.Context, projectID string) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin owner manager review deferral", err)
	}
	defer tx.Rollback()
	now := s.nowText()
	available := s.clock().UTC().Add(time.Second).Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE owner_manager_review_jobs SET
status='pending',attempts=CASE WHEN attempts>0 THEN attempts-1 ELSE 0 END,
available_at=?,lease_expires_at=NULL,last_error=NULL,updated_at=?
WHERE project_id=? AND status='leased'`, available, now, strings.TrimSpace(projectID))
	if err != nil {
		return storageFailure("defer owner manager review", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner manager review changed before deferral"}
	}
	return tx.Commit()
}

// AdvanceOwnerManagerReviewCut binds a leased review to the exact newer
// snapshot captured immediately before provider interpretation. This absorbs
// unrelated canonical events without weakening the event-cut citation fence;
// worker events arriving after this update still advance the same row and
// force one subsequent coalesced pass.
func (s *Store) AdvanceOwnerManagerReviewCut(ctx context.Context, projectID string, eventSequence int64) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin owner manager review cut advance", err)
	}
	defer tx.Rollback()
	job, err := ownerManagerReviewJobInTransaction(ctx, tx, strings.TrimSpace(projectID))
	if err != nil {
		return err
	}
	if job.Status != "leased" || eventSequence < job.RequestedEventSequence {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner manager review snapshot predates its requested event cut"}
	}
	var workspaceID string
	if err := tx.QueryRowContext(ctx, "SELECT workspace_id FROM events WHERE sequence=?", eventSequence).Scan(&workspaceID); err != nil || workspaceID != job.WorkspaceID {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner manager review snapshot is outside its workspace", Cause: err}
	}
	if eventSequence > job.RequestedEventSequence {
		now := s.nowText()
		if _, err := tx.ExecContext(ctx, "UPDATE owner_manager_review_jobs SET requested_event_sequence=?,updated_at=? WHERE project_id=? AND status='leased'", eventSequence, now, job.ProjectID); err != nil {
			return storageFailure("advance owner manager review event cut", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit owner manager review cut advance", err)
	}
	return nil
}

func (s *Store) FailOwnerManagerReview(ctx context.Context, projectID string, claimedEventSequence int64, cause error) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin owner manager review failure", err)
	}
	defer tx.Rollback()
	job, err := ownerManagerReviewJobInTransaction(ctx, tx, strings.TrimSpace(projectID))
	if err != nil {
		return err
	}
	if job.Status != "leased" || claimedEventSequence < 1 || claimedEventSequence > job.RequestedEventSequence {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner manager review failure does not match its lease"}
	}
	status := "failed"
	lastError := managerReviewDiagnostic(cause)
	if job.RequestedEventSequence > claimedEventSequence {
		status, lastError = "pending", ""
	}
	now := s.nowText()
	_, err = tx.ExecContext(ctx, `UPDATE owner_manager_review_jobs SET status=?,attempts=0,available_at=?,lease_expires_at=NULL,last_error=NULLIF(?,''),updated_at=? WHERE project_id=? AND status='leased'`, status, now, lastError, now, job.ProjectID)
	if err != nil {
		return storageFailure("fail owner manager review", err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit owner manager review failure", err)
	}
	return nil
}

func (s *Store) OwnerManagerReview(ctx context.Context, workspaceIdentifier, projectIdentifier string) (domain.OwnerManagerReviewJob, bool, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.OwnerManagerReviewJob{}, false, storageFailure("begin owner manager review read", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.OwnerManagerReviewJob{}, false, err
	}
	project, err := queryProject(ctx, tx, workspace.ID, strings.TrimSpace(projectIdentifier))
	if err != nil {
		return domain.OwnerManagerReviewJob{}, false, err
	}
	job, err := ownerManagerReviewJobInTransaction(ctx, tx, project.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OwnerManagerReviewJob{}, false, nil
	}
	return job, err == nil, err
}

func ownerManagerReviewJobInTransaction(ctx context.Context, tx *sql.Tx, projectID string) (domain.OwnerManagerReviewJob, error) {
	var job domain.OwnerManagerReviewJob
	var lease, turn, diagnostic sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT workspace_id,project_id,conversation_id,status,requested_event_sequence,reviewed_event_sequence,attempts,available_at,lease_expires_at,last_turn_id,last_error,created_at,updated_at
FROM owner_manager_review_jobs WHERE project_id=?`, projectID).Scan(
		&job.WorkspaceID, &job.ProjectID, &job.ConversationID, &job.Status, &job.RequestedEventSequence, &job.ReviewedEventSequence,
		&job.Attempts, &job.AvailableAt, &lease, &turn, &diagnostic, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return domain.OwnerManagerReviewJob{}, err
	}
	job.LeaseExpiresAt, job.LastTurnID, job.LastError = lease.String, turn.String, diagnostic.String
	return job, nil
}

func managerReviewDiagnostic(cause error) string {
	value := "owner manager review failed"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		value = strings.TrimSpace(cause.Error())
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	for len(value) > 2048 {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	if strings.TrimSpace(value) == "" {
		return "owner manager review failed"
	}
	return value
}
