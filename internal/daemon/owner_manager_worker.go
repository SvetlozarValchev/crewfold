package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store"
)

const (
	ownerManagerReviewLease     = 3 * time.Minute
	ownerManagerReviewPassLimit = 2
	ownerManagerReviewIdleWait  = 500 * time.Millisecond
	ownerManagerFreezeAttempts  = 8
	ownerManagerFreezeRetryWait = 10 * time.Millisecond
)

func (s *server) startOwnerManagerReviewWorker() {
	if s.config.DisableOwnerManagerReview {
		return
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		for s.leaseReconcileCtx.Err() == nil {
			processed := 0
			for processed < ownerManagerReviewPassLimit && s.leaseReconcileCtx.Err() == nil {
				job, found, err := s.store.ClaimOwnerManagerReview(s.leaseReconcileCtx, ownerManagerReviewLease)
				if err != nil {
					s.config.Logger.Error("owner manager review claim failed", "component", "owner_manager", "error", err)
					break
				}
				if !found {
					break
				}
				processed++
				if err := s.processOwnerManagerReview(s.leaseReconcileCtx, job); err != nil {
					s.config.Logger.Error("owner manager review failed", "component", "owner_manager", "project_id", job.ProjectID, "event_sequence", job.RequestedEventSequence, "error", err)
				}
			}
			if !waitForMessageWake(s.leaseReconcileCtx, s.ownerManagerReviewSignal, ownerManagerReviewWait(processed)) {
				return
			}
		}
	}()
}

func ownerManagerReviewWait(processed int) time.Duration {
	if processed == ownerManagerReviewPassLimit {
		return time.Millisecond
	}
	return ownerManagerReviewIdleWait
}

func (s *server) signalOwnerManagerReviewWorker() {
	if s.ownerManagerReviewSignal == nil {
		return
	}
	select {
	case s.ownerManagerReviewSignal <- struct{}{}:
	default:
	}
}

func (s *server) processOwnerManagerReview(ctx context.Context, job domain.OwnerManagerReviewJob) error {
	var snapshot store.OwnerInterpretationSnapshot
	var result store.OwnerExecutiveTurnResult
	var err error
	for attempt := 0; attempt < ownerManagerFreezeAttempts; attempt++ {
		snapshot, err = s.store.BuildOwnerInterpretationSnapshot(ctx, job.WorkspaceID, job.ProjectID)
		if err != nil {
			break
		}
		if err = s.store.AdvanceOwnerManagerReviewCut(ctx, job.ProjectID, snapshot.EventSequence); err != nil {
			break
		}
		instruction := fmt.Sprintf("Review worker reports and agent messages through canonical event cut %d. Summarize material progress, raise exactly one consequential owner decision when needed, or freeze only genuinely new dependency-aware work for owner review. Do not execute effects or duplicate existing work.", snapshot.EventSequence)
		result, err = s.store.RequestOwnerExecutiveTurn(ctx, store.RequestOwnerExecutiveTurnCommand{
			WorkspaceIdentifier: job.WorkspaceID, ProjectIdentifier: job.ProjectID, ConversationID: job.ConversationID,
			Instruction: instruction, Kind: "review", IdempotencyKey: fmt.Sprintf("manager-review:%s:%d", job.ProjectID, snapshot.EventSequence),
			InitiatedBy: "executive", TriggerEventSequence: snapshot.EventSequence, Snapshot: snapshot,
		})
		if err == nil || store.ErrorCode(err) != store.CodeOwnerTurnConflict || !strings.Contains(err.Error(), "canonical project state changed") {
			break
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
			attempt = ownerManagerFreezeAttempts
		case <-time.After(ownerManagerFreezeRetryWait):
		}
	}
	if err != nil {
		failedCut := job.RequestedEventSequence
		if snapshot.EventSequence > 0 {
			failedCut = snapshot.EventSequence
		}
		_ = s.store.FailOwnerManagerReview(ctx, job.ProjectID, failedCut, err)
		return err
	}
	if err := s.store.CompleteOwnerManagerReview(ctx, job.ProjectID, snapshot.EventSequence, result.Detail.Turn.ID); err != nil {
		return err
	}
	s.signalOwnerExecutiveWorker()
	s.config.Logger.Info("owner manager reviewed worker activity", "component", "owner_manager", "project_id", job.ProjectID,
		"turn_id", result.Detail.Turn.ID, "event_sequence", snapshot.EventSequence, "exchange_id", result.Exchange.ID)
	return nil
}
