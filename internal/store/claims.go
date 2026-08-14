package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"crewfold/internal/domain"
)

const (
	claimAddedEvent     = "claim.added"
	claimReleasedEvent  = "claim.released"
	claimExpiredEvent   = "claim.expired"
	overlapOpenedEvent  = "overlap.opened"
	overlapClosedEvent  = "overlap.resolved"
	driftOpenedEvent    = "claim.drift_opened"
	driftClosedEvent    = "claim.drift_resolved"
	maximumClaimLease   = 30 * 24 * time.Hour
	leaseActorID        = "subsystem:lease"
	claimWatcherActorID = "subsystem:claim-watcher"
)

func canonicalAddClaimCommand(command AddClaimCommand) (AddClaimCommand, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.TaskID = strings.TrimSpace(command.TaskID)
	command.CheckoutIdentifier = strings.TrimSpace(command.CheckoutIdentifier)
	command.Kind = strings.TrimSpace(command.Kind)
	command.Mode = strings.TrimSpace(command.Mode)
	command.ConflictPolicy = strings.TrimSpace(command.ConflictPolicy)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.Mode == "" {
		command.Mode = domain.ClaimModeExclusive
	}
	if command.ConflictPolicy == "" {
		command.ConflictPolicy = domain.ClaimPolicyNotify
	}
	target, err := domain.NormalizeClaimTarget(command.Kind, command.Target)
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" || command.TaskID == "" || err != nil || !domain.ValidClaimMode(command.Mode) || !domain.ValidClaimPolicy(command.ConflictPolicy) {
		return AddClaimCommand{}, &Error{Code: CodeInvalidClaim, Message: "claim requires workspace, project, task, a valid kind/target, mode, and conflict policy"}
	}
	command.Target = target
	if command.LeaseDuration < time.Second || command.LeaseDuration > maximumClaimLease {
		return AddClaimCommand{}, &Error{Code: CodeInvalidClaim, Message: "claim lease must be between one second and 30 days"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidClaim); err != nil {
		return AddClaimCommand{}, err
	}
	return command, nil
}

func claimAddRequestHash(command AddClaimCommand) (string, error) {
	return hashCommand("claim.add", map[string]any{
		"workspace": command.WorkspaceIdentifier, "project": command.ProjectIdentifier, "task": command.TaskID,
		"checkout": command.CheckoutIdentifier, "kind": command.Kind, "target": command.Target, "mode": command.Mode,
		"conflict_policy": command.ConflictPolicy, "lease_duration": command.LeaseDuration,
	})
}

// ReplayAddClaim performs only canonical request and receipt validation. The
// daemon calls it before observing Git so an exact lost-response replay cannot
// append an unrelated checkout observation ahead of the frozen claim receipt.
func (s *Store) ReplayAddClaim(ctx context.Context, command AddClaimCommand) (ClaimMutationResult, bool, error) {
	command, err := canonicalAddClaimCommand(command)
	if err != nil {
		return ClaimMutationResult{}, false, err
	}
	requestHash, err := claimAddRequestHash(command)
	if err != nil {
		return ClaimMutationResult{}, false, storageFailure("hash claim addition", err)
	}
	var replay ClaimMutationResult
	found, err := s.lookupIdempotencyBeforeEffects(ctx, command.IdempotencyKey, "claim.add", requestHash, &replay)
	if found {
		replay.Replayed = true
	}
	return replay, found, err
}

func (s *Store) AddClaim(ctx context.Context, command AddClaimCommand) (ClaimMutationResult, error) {
	command, err := canonicalAddClaimCommand(command)
	if err != nil {
		return ClaimMutationResult{}, err
	}
	requestHash, err := claimAddRequestHash(command)
	if err != nil {
		return ClaimMutationResult{}, storageFailure("hash claim addition", err)
	}
	var replay ClaimMutationResult
	if found, err := s.lookupIdempotencyBeforeEffects(ctx, command.IdempotencyKey, "claim.add", requestHash, &replay); err != nil {
		return ClaimMutationResult{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	if _, err := s.ReconcileExpiredClaims(ctx, command.WorkspaceIdentifier, derivedCorrelationID(command.CorrelationID, "lease")); err != nil {
		return ClaimMutationResult{}, err
	}

	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return ClaimMutationResult{}, storageFailure("begin claim addition", err)
	}
	defer tx.Rollback()
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "claim.add", requestHash, &replay); err != nil {
		return ClaimMutationResult{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return ClaimMutationResult{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return ClaimMutationResult{}, err
	}
	task, err := queryTask(ctx, tx, workspace.ID, command.TaskID)
	if err != nil {
		return ClaimMutationResult{}, err
	}
	if task.ProjectID != project.ID {
		return ClaimMutationResult{}, &Error{Code: CodeInvalidClaim, Message: "claim task is outside the requested project"}
	}
	checkout, err := selectClaimCheckout(ctx, tx, project.ID, command.CheckoutIdentifier, command.Kind == domain.ClaimKindPath)
	if err != nil {
		return ClaimMutationResult{}, err
	}
	baseline := []string{}
	checkoutID := ""
	warnings := []string{}
	if checkout != nil {
		checkoutID = checkout.ID
		baseline = append(baseline, checkout.DirtyPaths...)
		if checkout.WriteMode == domain.WriteModeShared {
			warnings = append(warnings, fmt.Sprintf("checkout %s uses shared write mode; claims coordinate intent but do not provide filesystem isolation", checkout.ID))
		}
	}
	now := s.nowText()
	claimID, err := randomID("claim_")
	if err != nil {
		return ClaimMutationResult{}, storageFailure("generate claim id", err)
	}
	claim := domain.WorkClaim{
		ID: claimID, WorkspaceID: workspace.ID, ProjectID: project.ID, TaskID: task.ID, CheckoutID: checkoutID,
		Kind: command.Kind, Target: command.Target, Mode: command.Mode, ConflictPolicy: command.ConflictPolicy,
		Status: domain.ClaimActive, BaselinePaths: baseline,
		LeaseExpiresAt: s.clock().UTC().Add(command.LeaseDuration).Format(time.RFC3339Nano), Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
	}
	existing, err := activeClaimsForProjectKind(ctx, tx, project.ID, command.Kind)
	if err != nil {
		return ClaimMutationResult{}, err
	}
	type pendingOverlap struct {
		other    domain.WorkClaim
		witness  string
		severity string
		response string
	}
	pending := make([]pendingOverlap, 0)
	for _, other := range existing {
		if other.TaskID == claim.TaskID {
			continue
		}
		witness, intersects := domain.ClaimScopesOverlap(claim.Kind, claim.Target, other.Target)
		if !intersects {
			continue
		}
		response := domain.ClaimPolicyResponse(claim.ConflictPolicy, other.ConflictPolicy)
		if response == domain.ClaimPolicyDenyNew {
			return ClaimMutationResult{}, &Error{Code: CodeClaimConflict, Message: fmt.Sprintf("claim intersects active claim %s at %s and deterministic policy denies the new claim", other.ID, witness)}
		}
		pending = append(pending, pendingOverlap{other: other, witness: witness, severity: domain.ClaimOverlapSeverity(claim.Mode, other.Mode), response: response})
	}
	baselineJSON, err := json.Marshal(claim.BaselinePaths)
	if err != nil {
		return ClaimMutationResult{}, storageFailure("encode claim baseline", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO work_claims(id, workspace_id, project_id, task_id, checkout_id, kind, target, mode,
    conflict_policy, status, baseline_paths_json, lease_expires_at, revision,
    created_at, updated_at, created_by, updated_by)
VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		claim.ID, claim.WorkspaceID, claim.ProjectID, claim.TaskID, claim.CheckoutID, claim.Kind, claim.Target,
		claim.Mode, claim.ConflictPolicy, claim.Status, string(baselineJSON), claim.LeaseExpiresAt,
		claim.CreatedAt, claim.UpdatedAt, claim.CreatedBy, claim.UpdatedBy); err != nil {
		return ClaimMutationResult{}, storageFailure("insert claim projection", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return ClaimMutationResult{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "claim", claim.ID, claim.Revision, claimAddedEvent, command.CorrelationID, now, claim)
	if err != nil {
		return ClaimMutationResult{}, err
	}
	overlaps := make([]domain.WorkOverlap, 0, len(pending))
	for _, candidate := range pending {
		overlap, eventSequence, err := insertOverlap(ctx, tx, claim, candidate.other, candidate.witness, candidate.severity, candidate.response, command.CorrelationID, now)
		if err != nil {
			return ClaimMutationResult{}, err
		}
		sequence = eventSequence
		overlaps = append(overlaps, overlap)
	}
	result := ClaimMutationResult{Claim: claim, Overlaps: overlaps, Warnings: warnings, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return ClaimMutationResult{}, err
	}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "claim.add", requestHash, result, now); err != nil {
		return ClaimMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClaimMutationResult{}, storageFailure("commit claim addition", err)
	}
	return result, nil
}

func (s *Store) ReleaseClaim(ctx context.Context, command ReleaseClaimCommand) (ClaimMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ClaimID = strings.TrimSpace(command.ClaimID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.ClaimID == "" || command.ExpectedRevision < 1 {
		return ClaimMutationResult{}, &Error{Code: CodeInvalidClaim, Message: "claim release requires workspace, claim id, and expected revision"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidClaim); err != nil {
		return ClaimMutationResult{}, err
	}
	requestHash, err := hashCommand("claim.release", map[string]any{
		"workspace": command.WorkspaceIdentifier, "claim": command.ClaimID, "expected_revision": command.ExpectedRevision,
	})
	if err != nil {
		return ClaimMutationResult{}, storageFailure("hash claim release", err)
	}
	var replay ClaimMutationResult
	if found, err := s.lookupIdempotencyBeforeEffects(ctx, command.IdempotencyKey, "claim.release", requestHash, &replay); err != nil {
		return ClaimMutationResult{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	if _, err := s.ReconcileExpiredClaims(ctx, command.WorkspaceIdentifier, derivedCorrelationID(command.CorrelationID, "lease")); err != nil {
		return ClaimMutationResult{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return ClaimMutationResult{}, storageFailure("begin claim release", err)
	}
	defer tx.Rollback()
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "claim.release", requestHash, &replay); err != nil {
		return ClaimMutationResult{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return ClaimMutationResult{}, err
	}
	claim, err := queryClaim(ctx, tx, workspace.ID, command.ClaimID)
	if err != nil {
		return ClaimMutationResult{}, err
	}
	if claim.Revision != command.ExpectedRevision {
		return ClaimMutationResult{}, revisionConflict("claim", claim.ID, command.ExpectedRevision, claim.Revision)
	}
	if claim.Status != domain.ClaimActive {
		return ClaimMutationResult{}, &Error{Code: CodeClaimConflict, Message: fmt.Sprintf("claim %s is already %s", claim.ID, claim.Status)}
	}
	now := s.nowText()
	claim.Status = domain.ClaimReleased
	claim.Revision++
	claim.UpdatedAt = now
	claim.UpdatedBy = localOwnerActorID
	if _, err := tx.ExecContext(ctx, "UPDATE work_claims SET status = ?, revision = ?, updated_at = ?, updated_by = ? WHERE id = ?", claim.Status, claim.Revision, now, claim.UpdatedBy, claim.ID); err != nil {
		return ClaimMutationResult{}, storageFailure("release claim projection", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return ClaimMutationResult{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "claim", claim.ID, claim.Revision, claimReleasedEvent, command.CorrelationID, now, map[string]any{"status": claim.Status})
	if err != nil {
		return ClaimMutationResult{}, err
	}
	resolved, sequence, err := resolveClaimOverlaps(ctx, tx, workspace.ID, claim.ID, "claim released", command.CorrelationID, now, sequence)
	if err != nil {
		return ClaimMutationResult{}, err
	}
	result := ClaimMutationResult{Claim: claim, Overlaps: resolved, Warnings: []string{}, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "claim.release", requestHash, result, now); err != nil {
		return ClaimMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClaimMutationResult{}, storageFailure("commit claim release", err)
	}
	return result, nil
}

func (s *Store) ReconcileExpiredClaims(ctx context.Context, workspaceIdentifier, correlationID string) (int, error) {
	return s.reconcileExpiredClaims(ctx, workspaceIdentifier, correlationID, 0)
}

// ReconcileExpiredClaimsBatch advances at most limit elapsed claims. Repeated
// calls drain the stable lease-expiry/id order as committed claims cease to be
// active candidates.
func (s *Store) ReconcileExpiredClaimsBatch(ctx context.Context, workspaceIdentifier, correlationID string, limit int) (int, error) {
	if limit < 1 || limit > MaximumLeaseReconciliationBatchLimit {
		return 0, &Error{Code: CodeInvalidClaim, Message: fmt.Sprintf("claim reconciliation limit must be between 1 and %d", MaximumLeaseReconciliationBatchLimit)}
	}
	return s.reconcileExpiredClaims(ctx, workspaceIdentifier, correlationID, limit)
}

func (s *Store) reconcileExpiredClaims(ctx context.Context, workspaceIdentifier, correlationID string, limit int) (int, error) {
	workspaceIdentifier = strings.TrimSpace(workspaceIdentifier)
	correlationID = strings.TrimSpace(correlationID)
	if workspaceIdentifier == "" || correlationID == "" || len(correlationID) > 128 {
		return 0, &Error{Code: CodeInvalidClaim, Message: "claim reconciliation requires workspace and correlation id"}
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return 0, storageFailure("begin claim lease reconciliation", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return 0, err
	}
	now := s.nowText()
	count, err := expireClaimsInTransactionLimit(ctx, tx, workspace.ID, now, correlationID, limit)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, storageFailure("commit claim lease reconciliation", err)
	}
	return count, nil
}

func expireClaimsInTransaction(ctx context.Context, tx *sql.Tx, workspaceID, now, correlationID string) (int, error) {
	return expireClaimsInTransactionLimit(ctx, tx, workspaceID, now, correlationID, 0)
}

func expireClaimsInTransactionLimit(ctx context.Context, tx *sql.Tx, workspaceID, now, correlationID string, limit int) (int, error) {
	query := claimSelect + ` WHERE workspace_id = ? AND status = 'active'
AND crewfold_timestamp_key(lease_expires_at) <= crewfold_timestamp_key(?)
AND NOT EXISTS (
    SELECT 1 FROM runs run
    WHERE run.task_id = work_claims.task_id
      AND run.status IN ('requested','starting','active','blocked','stopping','lost')
)
ORDER BY crewfold_timestamp_key(lease_expires_at), id`
	arguments := []any{workspaceID, now}
	if limit > 0 {
		query += "\nLIMIT ?"
		arguments = append(arguments, limit)
	}
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return 0, storageFailure("list expired claims", err)
	}
	claims, err := scanClaims(rows)
	if err != nil {
		return 0, err
	}
	var sequence int64
	for _, claim := range claims {
		claim.Status = domain.ClaimExpired
		claim.Revision++
		if _, err := tx.ExecContext(ctx, "UPDATE work_claims SET status = 'expired', revision = ?, updated_at = ?, updated_by = ? WHERE id = ? AND status = 'active'", claim.Revision, now, leaseActorID, claim.ID); err != nil {
			return 0, storageFailure("expire claim", err)
		}
		sequence, err = appendEventForActor(ctx, tx, workspaceID, "claim", claim.ID, claim.Revision, claimExpiredEvent, correlationID, now, leaseActorID, domain.EventActorSubsystem, map[string]any{"status": claim.Status, "lease_expires_at": claim.LeaseExpiresAt})
		if err != nil {
			return 0, err
		}
		if _, sequence, err = resolveClaimOverlapsForActor(ctx, tx, workspaceID, claim.ID, "claim lease expired", correlationID, now, sequence, leaseActorID, domain.EventActorSubsystem); err != nil {
			return 0, err
		}
	}
	return len(claims), nil
}

func (s *Store) Overlap(ctx context.Context, workspaceIdentifier, overlapID string) (domain.WorkOverlap, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.WorkOverlap{}, err
	}
	return queryOverlap(ctx, s.db, workspace.ID, strings.TrimSpace(overlapID))
}

func insertOverlap(ctx context.Context, tx *sql.Tx, left, right domain.WorkClaim, witness, severity, response, correlationID, now string) (domain.WorkOverlap, int64, error) {
	return insertOverlapForActor(ctx, tx, left, right, witness, severity, response, correlationID, now, localOwnerActorID, "human")
}

func insertOverlapForActor(ctx context.Context, tx *sql.Tx, left, right domain.WorkClaim, witness, severity, response, correlationID, now, actorID, actorType string) (domain.WorkOverlap, int64, error) {
	low, high := left, right
	if high.ID < low.ID {
		low, high = high, low
	}
	id, err := randomID("overlap_")
	if err != nil {
		return domain.WorkOverlap{}, 0, storageFailure("generate overlap id", err)
	}
	overlap := domain.WorkOverlap{
		ID: id, WorkspaceID: left.WorkspaceID, ProjectID: left.ProjectID,
		ClaimIDs: []string{low.ID, high.ID}, TaskIDs: []string{low.TaskID, high.TaskID}, Kind: left.Kind,
		Witness: witness, Severity: severity, PolicyResponse: response,
		SchedulingPaused:   response == domain.ClaimPolicyPauseScheduling,
		ResolutionRequired: response == domain.ClaimPolicyRequestResolution || response == domain.ClaimPolicyPauseScheduling,
		Status:             domain.OverlapOpen, Explanation: domain.ClaimExplanation(left, right, witness, severity, response), DetectedAt: now, Revision: 1,
	}
	explanationJSON, err := json.Marshal(overlap.Explanation)
	if err != nil {
		return domain.WorkOverlap{}, 0, storageFailure("encode overlap explanation", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO work_overlaps(id, workspace_id, project_id, claim_low_id, claim_high_id, task_low_id, task_high_id,
    kind, witness, severity, policy_response, scheduling_paused, resolution_required, status,
    explanation_json, detected_at, revision)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?, 1)`,
		overlap.ID, overlap.WorkspaceID, overlap.ProjectID, low.ID, high.ID, low.TaskID, high.TaskID,
		overlap.Kind, overlap.Witness, overlap.Severity, overlap.PolicyResponse, overlap.SchedulingPaused,
		overlap.ResolutionRequired, string(explanationJSON), overlap.DetectedAt); err != nil {
		return domain.WorkOverlap{}, 0, storageFailure("insert overlap projection", err)
	}
	if overlap.SchedulingPaused {
		for _, taskID := range overlap.TaskIDs {
			if _, err := tx.ExecContext(ctx, "INSERT INTO task_coordination_holds(overlap_id, task_id, reason, created_at) VALUES (?, ?, ?, ?)", overlap.ID, taskID, "open work overlap "+overlap.ID, now); err != nil {
				return domain.WorkOverlap{}, 0, storageFailure("insert task coordination hold", err)
			}
		}
	}
	sequence, err := appendEventForActor(ctx, tx, overlap.WorkspaceID, "overlap", overlap.ID, overlap.Revision, overlapOpenedEvent, correlationID, now, actorID, actorType, overlap)
	return overlap, sequence, err
}

func resolveClaimOverlaps(ctx context.Context, tx *sql.Tx, workspaceID, claimID, reason, correlationID, now string, sequence int64) ([]domain.WorkOverlap, int64, error) {
	return resolveClaimOverlapsForActor(ctx, tx, workspaceID, claimID, reason, correlationID, now, sequence, localOwnerActorID, domain.EventActorHuman)
}

func resolveClaimOverlapsForActor(ctx context.Context, tx *sql.Tx, workspaceID, claimID, reason, correlationID, now string, sequence int64, actorID, actorType string) ([]domain.WorkOverlap, int64, error) {
	rows, err := tx.QueryContext(ctx, overlapSelect+" WHERE workspace_id = ? AND status = 'open' AND (claim_low_id = ? OR claim_high_id = ?) ORDER BY id", workspaceID, claimID, claimID)
	if err != nil {
		return nil, sequence, storageFailure("list claim overlaps", err)
	}
	overlaps, err := scanOverlaps(rows)
	if err != nil {
		return nil, sequence, err
	}
	for index := range overlaps {
		overlap := &overlaps[index]
		overlap.Status = domain.OverlapResolved
		overlap.ResolvedAt = now
		overlap.ResolutionReason = reason
		overlap.Revision++
		if _, err := tx.ExecContext(ctx, "UPDATE work_overlaps SET status = 'resolved', resolved_at = ?, resolution_reason = ?, revision = ? WHERE id = ?", now, reason, overlap.Revision, overlap.ID); err != nil {
			return nil, sequence, storageFailure("resolve overlap", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM task_coordination_holds WHERE overlap_id = ?", overlap.ID); err != nil {
			return nil, sequence, storageFailure("release task coordination holds", err)
		}
		sequence, err = appendEventForActor(ctx, tx, workspaceID, "overlap", overlap.ID, overlap.Revision, overlapClosedEvent, correlationID, now, actorID, actorType, map[string]any{"status": overlap.Status, "reason": reason})
		if err != nil {
			return nil, sequence, err
		}
	}
	return overlaps, sequence, nil
}

func (s *Store) RecordCheckoutClaimScan(ctx context.Context, command RecordCheckoutClaimScanCommand) (domain.CheckoutClaimScan, error) {
	command.CheckoutID = strings.TrimSpace(command.CheckoutID)
	command.WatcherID = strings.TrimSpace(command.WatcherID)
	command.HeadCommit = strings.TrimSpace(command.HeadCommit)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	dirtyPaths, dirtyPathsJSON, pathErr := encodeDirtyPaths(command.DirtyPaths)
	if command.CheckoutID == "" || command.WatcherID == "" || len(command.WatcherID) > 128 || command.HeadCommit == "" || command.CorrelationID == "" || len(command.CorrelationID) > 128 || pathErr != nil {
		return domain.CheckoutClaimScan{}, &Error{Code: CodeInvalidClaimScan, Message: "claim scan requires checkout, watcher, HEAD, correlation id, and valid repository-relative dirty paths"}
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.CheckoutClaimScan{}, storageFailure("begin claim scan", err)
	}
	defer tx.Rollback()
	var workspaceID, projectID, previousWatcher, previousObserved, currentHead, currentDirtyJSON string
	var checkoutRevision int64
	err = tx.QueryRowContext(ctx, `
SELECT p.workspace_id, c.project_id, c.revision, COALESCE(c.head_commit, ''), c.dirty_paths_json,
       COALESCE(s.watcher_id, ''), COALESCE(s.observed_at, '')
FROM checkouts c JOIN projects p ON p.id = c.project_id
LEFT JOIN checkout_claim_scans s ON s.checkout_id = c.id
WHERE c.id = ?`, command.CheckoutID).Scan(&workspaceID, &projectID, &checkoutRevision, &currentHead, &currentDirtyJSON, &previousWatcher, &previousObserved)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CheckoutClaimScan{}, &Error{Code: CodeInvalidClaimScan, Message: fmt.Sprintf("checkout %q was not found", command.CheckoutID)}
	}
	if err != nil {
		return domain.CheckoutClaimScan{}, storageFailure("query claim scan checkout", err)
	}
	now := s.nowText()
	observationGap := previousWatcher != "" && previousWatcher != command.WatcherID
	if currentHead != command.HeadCommit || currentDirtyJSON != dirtyPathsJSON {
		checkoutRevision++
		if _, err := tx.ExecContext(ctx, `
UPDATE checkouts SET head_commit = ?, dirty = ?, dirty_paths_json = ?, revision = ?, observed_at = ?, updated_at = ?, updated_by = ? WHERE id = ?`,
			command.HeadCommit, len(dirtyPaths) != 0, dirtyPathsJSON, checkoutRevision, now, now, claimWatcherActorID, command.CheckoutID); err != nil {
			return domain.CheckoutClaimScan{}, storageFailure("update claim scan checkout", err)
		}
		if _, err := appendEventForActor(ctx, tx, workspaceID, "checkout", command.CheckoutID, checkoutRevision, checkoutObserved, command.CorrelationID, now, claimWatcherActorID, domain.EventActorSubsystem, map[string]any{"head_commit": command.HeadCommit, "dirty_paths": dirtyPaths, "claim_scan": true}); err != nil {
			return domain.CheckoutClaimScan{}, err
		}
	}
	claims, err := activeClaimsForCheckout(ctx, tx, command.CheckoutID)
	if err != nil {
		return domain.CheckoutClaimScan{}, err
	}
	type taskCoverage struct {
		claims   []domain.WorkClaim
		baseline map[string]bool
	}
	coverage := make(map[string]*taskCoverage)
	for _, claim := range claims {
		item := coverage[claim.TaskID]
		if item == nil {
			item = &taskCoverage{baseline: make(map[string]bool)}
			coverage[claim.TaskID] = item
		}
		item.claims = append(item.claims, claim)
		for _, baseline := range claim.BaselinePaths {
			item.baseline[baseline] = true
		}
	}
	desired := make(map[string]domain.WorkClaim)
	for taskID, item := range coverage {
		sort.Slice(item.claims, func(i, j int) bool { return item.claims[i].ID < item.claims[j].ID })
		for _, dirtyPath := range dirtyPaths {
			if item.baseline[dirtyPath] {
				continue
			}
			covered := false
			for _, claim := range item.claims {
				if domain.ClaimPathMatches(claim.Target, dirtyPath) {
					covered = true
					break
				}
			}
			if !covered {
				desired[taskID+"\x00"+dirtyPath] = item.claims[0]
			}
		}
	}
	opened, resolved := 0, 0
	rows, err := tx.QueryContext(ctx, driftSelect+" WHERE checkout_id = ? AND status = 'open' ORDER BY id", command.CheckoutID)
	if err != nil {
		return domain.CheckoutClaimScan{}, storageFailure("list open claim drift", err)
	}
	openDrifts, err := scanDrifts(rows)
	if err != nil {
		return domain.CheckoutClaimScan{}, err
	}
	for _, drift := range openDrifts {
		key := drift.TaskID + "\x00" + drift.Path
		if _, keep := desired[key]; keep {
			delete(desired, key)
			if _, err := tx.ExecContext(ctx, "UPDATE claim_drifts SET head_commit = ?, observation_gap = observation_gap OR ?, last_observed_at = ?, revision = revision + 1 WHERE id = ?", command.HeadCommit, observationGap, now, drift.ID); err != nil {
				return domain.CheckoutClaimScan{}, storageFailure("refresh claim drift", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE claim_drifts SET status = 'resolved', resolved_at = ?, last_observed_at = ?, revision = revision + 1 WHERE id = ?", now, now, drift.ID); err != nil {
			return domain.CheckoutClaimScan{}, storageFailure("resolve claim drift", err)
		}
		resolved++
		if _, err := appendEventForActor(ctx, tx, workspaceID, "claim_drift", drift.ID, drift.Revision+1, driftClosedEvent, command.CorrelationID, now, claimWatcherActorID, domain.EventActorSubsystem, map[string]any{"path": drift.Path, "status": domain.DriftResolved}); err != nil {
			return domain.CheckoutClaimScan{}, err
		}
	}
	keys := make([]string, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		claim := desired[key]
		path := strings.SplitN(key, "\x00", 2)[1]
		var driftID string
		var revision int64
		err := tx.QueryRowContext(ctx, "SELECT id, revision FROM claim_drifts WHERE task_id = ? AND checkout_id = ? AND path = ?", claim.TaskID, command.CheckoutID, path).Scan(&driftID, &revision)
		if errors.Is(err, sql.ErrNoRows) {
			driftID, err = randomID("drift_")
			if err != nil {
				return domain.CheckoutClaimScan{}, storageFailure("generate claim drift id", err)
			}
			revision = 1
			_, err = tx.ExecContext(ctx, `
INSERT INTO claim_drifts(id, workspace_id, project_id, claim_id, task_id, checkout_id, path, head_commit,
    observation_gap, status, first_observed_at, last_observed_at, revision)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?, 1)`, driftID, workspaceID, projectID, claim.ID, claim.TaskID, command.CheckoutID, path, command.HeadCommit, observationGap, now, now)
		} else if err == nil {
			revision++
			_, err = tx.ExecContext(ctx, `
UPDATE claim_drifts SET claim_id = ?, head_commit = ?, observation_gap = observation_gap OR ?, status = 'open',
    first_observed_at = ?, last_observed_at = ?, resolved_at = NULL, revision = ? WHERE id = ?`, claim.ID, command.HeadCommit, observationGap, now, now, revision, driftID)
		}
		if err != nil {
			return domain.CheckoutClaimScan{}, storageFailure("open claim drift", err)
		}
		opened++
		if _, err := appendEventForActor(ctx, tx, workspaceID, "claim_drift", driftID, revision, driftOpenedEvent, command.CorrelationID, now, claimWatcherActorID, domain.EventActorSubsystem, map[string]any{"claim_id": claim.ID, "task_id": claim.TaskID, "checkout_id": command.CheckoutID, "path": path, "observation_gap": observationGap}); err != nil {
			return domain.CheckoutClaimScan{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO checkout_claim_scans(checkout_id, watcher_id, head_commit, dirty_paths_json, observed_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(checkout_id) DO UPDATE SET watcher_id = excluded.watcher_id, head_commit = excluded.head_commit,
    dirty_paths_json = excluded.dirty_paths_json, observed_at = excluded.observed_at`, command.CheckoutID, command.WatcherID, command.HeadCommit, dirtyPathsJSON, now); err != nil {
		return domain.CheckoutClaimScan{}, storageFailure("record checkout claim scan", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.CheckoutClaimScan{}, storageFailure("commit checkout claim scan", err)
	}
	return domain.CheckoutClaimScan{CheckoutID: command.CheckoutID, ProjectID: projectID, HeadCommit: command.HeadCommit, DirtyPaths: dirtyPaths, ObservedAt: now, PreviousObservedAt: previousObserved, ObservationGap: observationGap, DriftsOpened: opened, DriftsResolved: resolved}, nil
}

func (s *Store) ClaimWatchTargets(ctx context.Context) ([]ClaimWatchTarget, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT c.workspace_id, c.project_id, c.checkout_id, checkout.path
FROM work_claims c JOIN checkouts checkout ON checkout.id = c.checkout_id
WHERE c.status = 'active' AND c.kind = 'path' AND checkout.availability = 'available'
ORDER BY c.workspace_id, c.project_id, c.checkout_id`)
	if err != nil {
		return nil, storageFailure("list claim watch targets", err)
	}
	defer rows.Close()
	result := make([]ClaimWatchTarget, 0)
	for rows.Next() {
		var target ClaimWatchTarget
		if err := rows.Scan(&target.WorkspaceID, &target.ProjectID, &target.CheckoutID, &target.Path); err != nil {
			return nil, storageFailure("scan claim watch target", err)
		}
		result = append(result, target)
	}
	return result, rows.Err()
}

func selectClaimCheckout(ctx context.Context, tx *sql.Tx, projectID, identifier string, required bool) (*domain.Checkout, error) {
	query := `
SELECT id, project_id, repository_id, path, write_mode, revision, availability, checkout_kind,
       COALESCE(branch, ''), COALESCE(head_commit, ''), dirty, dirty_paths_json,
       COALESCE(git_dir, ''), COALESCE(git_common_dir, ''), observed_at,
       COALESCE(diagnostic_code, ''), COALESCE(diagnostic, ''), created_at, updated_at, created_by, updated_by
FROM checkouts WHERE project_id = ? AND availability = 'available' AND write_mode <> 'read_only'`
	arguments := []any{projectID}
	if identifier != "" {
		query += " AND (id = ? OR path = ?)"
		arguments = append(arguments, identifier, identifier)
	}
	query += " ORDER BY path, id"
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, storageFailure("select claim checkout", err)
	}
	defer rows.Close()
	values := make([]domain.Checkout, 0, 2)
	for rows.Next() {
		var checkout domain.Checkout
		var dirtyJSON string
		if err := rows.Scan(&checkout.ID, &checkout.ProjectID, &checkout.RepositoryID, &checkout.Path, &checkout.WriteMode, &checkout.Revision, &checkout.Availability, &checkout.CheckoutKind, &checkout.Branch, &checkout.HeadCommit, &checkout.Dirty, &dirtyJSON, &checkout.GitDir, &checkout.GitCommonDir, &checkout.ObservedAt, &checkout.DiagnosticCode, &checkout.Diagnostic, &checkout.CreatedAt, &checkout.UpdatedAt, &checkout.CreatedBy, &checkout.UpdatedBy); err != nil {
			return nil, storageFailure("scan claim checkout", err)
		}
		if err := json.Unmarshal([]byte(dirtyJSON), &checkout.DirtyPaths); err != nil {
			return nil, storageFailure("decode claim checkout dirty paths", err)
		}
		values = append(values, checkout)
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate claim checkouts", err)
	}
	if identifier != "" {
		if len(values) != 1 {
			return nil, &Error{Code: CodeInvalidClaim, Message: fmt.Sprintf("checkout %q is unavailable, read-only, or outside the project", identifier)}
		}
		return &values[0], nil
	}
	if !required {
		return nil, nil
	}
	if len(values) == 0 {
		return nil, &Error{Code: CodeInvalidClaim, Message: "path claim requires an available writable checkout"}
	}
	if len(values) > 1 {
		return nil, &Error{Code: CodeInvalidClaim, Message: "path claim requires --checkout when the project has multiple writable checkouts"}
	}
	return &values[0], nil
}

func activeClaimsForProjectKind(ctx context.Context, database queryContext, projectID, kind string) ([]domain.WorkClaim, error) {
	rows, err := database.QueryContext(ctx, claimSelect+" WHERE project_id = ? AND kind = ? AND status = 'active' ORDER BY id", projectID, kind)
	if err != nil {
		return nil, storageFailure("list active project claims", err)
	}
	return scanClaims(rows)
}

func activeClaimsForCheckout(ctx context.Context, database queryContext, checkoutID string) ([]domain.WorkClaim, error) {
	rows, err := database.QueryContext(ctx, claimSelect+" WHERE checkout_id = ? AND kind = 'path' AND status = 'active' ORDER BY task_id, id", checkoutID)
	if err != nil {
		return nil, storageFailure("list active checkout claims", err)
	}
	return scanClaims(rows)
}

const claimSelect = `
SELECT id, workspace_id, project_id, task_id, COALESCE(checkout_id, ''), kind, target, mode,
       conflict_policy, status, baseline_paths_json, lease_expires_at, revision,
       created_at, updated_at, created_by, updated_by
FROM work_claims`

func queryClaim(ctx context.Context, database queryRower, workspaceID, claimID string) (domain.WorkClaim, error) {
	var claim domain.WorkClaim
	var baselineJSON string
	err := database.QueryRowContext(ctx, claimSelect+" WHERE workspace_id = ? AND id = ?", workspaceID, claimID).Scan(
		&claim.ID, &claim.WorkspaceID, &claim.ProjectID, &claim.TaskID, &claim.CheckoutID, &claim.Kind, &claim.Target,
		&claim.Mode, &claim.ConflictPolicy, &claim.Status, &baselineJSON, &claim.LeaseExpiresAt, &claim.Revision,
		&claim.CreatedAt, &claim.UpdatedAt, &claim.CreatedBy, &claim.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkClaim{}, &Error{Code: CodeClaimNotFound, Message: fmt.Sprintf("claim %q was not found", claimID)}
	}
	if err != nil {
		return domain.WorkClaim{}, storageFailure("query claim", err)
	}
	if err := json.Unmarshal([]byte(baselineJSON), &claim.BaselinePaths); err != nil {
		return domain.WorkClaim{}, storageFailure("decode claim baseline", err)
	}
	return claim, nil
}

func scanClaims(rows *sql.Rows) ([]domain.WorkClaim, error) {
	defer rows.Close()
	result := make([]domain.WorkClaim, 0)
	for rows.Next() {
		var claim domain.WorkClaim
		var baselineJSON string
		if err := rows.Scan(&claim.ID, &claim.WorkspaceID, &claim.ProjectID, &claim.TaskID, &claim.CheckoutID, &claim.Kind, &claim.Target, &claim.Mode, &claim.ConflictPolicy, &claim.Status, &baselineJSON, &claim.LeaseExpiresAt, &claim.Revision, &claim.CreatedAt, &claim.UpdatedAt, &claim.CreatedBy, &claim.UpdatedBy); err != nil {
			return nil, storageFailure("scan claim", err)
		}
		if err := json.Unmarshal([]byte(baselineJSON), &claim.BaselinePaths); err != nil {
			return nil, storageFailure("decode claim baseline", err)
		}
		result = append(result, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate claims", err)
	}
	return result, nil
}

const overlapSelect = `
SELECT id, workspace_id, project_id, claim_low_id, claim_high_id, task_low_id, task_high_id,
       kind, witness, severity, policy_response, scheduling_paused, resolution_required, status,
       explanation_json, detected_at, COALESCE(resolved_at, ''), COALESCE(resolution_reason, ''), revision
FROM work_overlaps`

func queryOverlap(ctx context.Context, database queryRower, workspaceID, overlapID string) (domain.WorkOverlap, error) {
	var overlap domain.WorkOverlap
	var lowClaim, highClaim, lowTask, highTask, explanationJSON string
	err := database.QueryRowContext(ctx, overlapSelect+" WHERE workspace_id = ? AND id = ?", workspaceID, overlapID).Scan(
		&overlap.ID, &overlap.WorkspaceID, &overlap.ProjectID, &lowClaim, &highClaim, &lowTask, &highTask,
		&overlap.Kind, &overlap.Witness, &overlap.Severity, &overlap.PolicyResponse, &overlap.SchedulingPaused,
		&overlap.ResolutionRequired, &overlap.Status, &explanationJSON, &overlap.DetectedAt, &overlap.ResolvedAt,
		&overlap.ResolutionReason, &overlap.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkOverlap{}, &Error{Code: CodeOverlapNotFound, Message: fmt.Sprintf("overlap %q was not found", overlapID)}
	}
	if err != nil {
		return domain.WorkOverlap{}, storageFailure("query overlap", err)
	}
	overlap.ClaimIDs = []string{lowClaim, highClaim}
	overlap.TaskIDs = []string{lowTask, highTask}
	if err := json.Unmarshal([]byte(explanationJSON), &overlap.Explanation); err != nil {
		return domain.WorkOverlap{}, storageFailure("decode overlap explanation", err)
	}
	return overlap, nil
}

func scanOverlaps(rows *sql.Rows) ([]domain.WorkOverlap, error) {
	defer rows.Close()
	result := make([]domain.WorkOverlap, 0)
	for rows.Next() {
		var overlap domain.WorkOverlap
		var lowClaim, highClaim, lowTask, highTask, explanationJSON string
		if err := rows.Scan(&overlap.ID, &overlap.WorkspaceID, &overlap.ProjectID, &lowClaim, &highClaim, &lowTask, &highTask, &overlap.Kind, &overlap.Witness, &overlap.Severity, &overlap.PolicyResponse, &overlap.SchedulingPaused, &overlap.ResolutionRequired, &overlap.Status, &explanationJSON, &overlap.DetectedAt, &overlap.ResolvedAt, &overlap.ResolutionReason, &overlap.Revision); err != nil {
			return nil, storageFailure("scan overlap", err)
		}
		overlap.ClaimIDs = []string{lowClaim, highClaim}
		overlap.TaskIDs = []string{lowTask, highTask}
		if err := json.Unmarshal([]byte(explanationJSON), &overlap.Explanation); err != nil {
			return nil, storageFailure("decode overlap explanation", err)
		}
		result = append(result, overlap)
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate overlaps", err)
	}
	return result, nil
}

const driftSelect = `
SELECT id, workspace_id, project_id, claim_id, task_id, checkout_id, path,
       COALESCE(head_commit, ''), observation_gap, status, first_observed_at, last_observed_at,
       COALESCE(resolved_at, ''), revision
FROM claim_drifts`

func queryDrift(ctx context.Context, database queryRower, workspaceID, driftID string) (domain.ClaimDrift, error) {
	var drift domain.ClaimDrift
	err := database.QueryRowContext(ctx, driftSelect+" WHERE workspace_id = ? AND id = ?", workspaceID, driftID).Scan(
		&drift.ID, &drift.WorkspaceID, &drift.ProjectID, &drift.ClaimID, &drift.TaskID,
		&drift.CheckoutID, &drift.Path, &drift.HeadCommit, &drift.ObservationGap, &drift.Status,
		&drift.FirstObservedAt, &drift.LastObservedAt, &drift.ResolvedAt, &drift.Revision,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ClaimDrift{}, &Error{Code: CodeClaimNotFound, Message: fmt.Sprintf("claim drift %q was not found", driftID)}
	}
	if err != nil {
		return domain.ClaimDrift{}, storageFailure("query claim drift", err)
	}
	return drift, nil
}

func scanDrifts(rows *sql.Rows) ([]domain.ClaimDrift, error) {
	defer rows.Close()
	result := make([]domain.ClaimDrift, 0)
	for rows.Next() {
		var drift domain.ClaimDrift
		if err := rows.Scan(&drift.ID, &drift.WorkspaceID, &drift.ProjectID, &drift.ClaimID, &drift.TaskID, &drift.CheckoutID, &drift.Path, &drift.HeadCommit, &drift.ObservationGap, &drift.Status, &drift.FirstObservedAt, &drift.LastObservedAt, &drift.ResolvedAt, &drift.Revision); err != nil {
			return nil, storageFailure("scan claim drift", err)
		}
		result = append(result, drift)
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate claim drift", err)
	}
	return result, nil
}

func derivedCorrelationID(base, suffix string) string {
	value := base + "-" + suffix
	if len(value) <= 128 {
		return value
	}
	return base[:128-len(suffix)-1] + "-" + suffix
}
