package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store"
)

const (
	ownerManagerReviewLease     = 3 * time.Minute
	ownerManagerReviewPassLimit = 2
	ownerManagerReviewIdleWait  = 500 * time.Millisecond
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

func (s *server) ownerInterpreterForProvider(provider string) execution.OwnerInterpreter {
	if !s.ownerInterpreterInjected && (provider == "fixture" || strings.HasPrefix(provider, "fixture-")) {
		return execution.FixtureOwnerInterpreter{}
	}
	return s.ownerInterpreter
}

func (s *server) processOwnerManagerReview(ctx context.Context, job domain.OwnerManagerReviewJob) error {
	snapshot, err := s.store.BuildOwnerInterpretationSnapshot(ctx, job.WorkspaceID, job.ProjectID)
	if err != nil {
		_ = s.store.FailOwnerManagerReview(ctx, job.ProjectID, job.RequestedEventSequence, err)
		return err
	}
	if err := s.store.AdvanceOwnerManagerReviewCut(ctx, job.ProjectID, snapshot.EventSequence); err != nil {
		_ = s.store.FailOwnerManagerReview(ctx, job.ProjectID, job.RequestedEventSequence, err)
		return err
	}
	interpreter := s.ownerInterpreterForProvider(snapshot.Provider)
	if interpreter == nil {
		err := &store.Error{Code: store.CodeAdapterUnavailable, Message: "owner manager interpreter is unavailable"}
		_ = s.store.FailOwnerManagerReview(ctx, job.ProjectID, snapshot.EventSequence, err)
		return err
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("owner-manager-review:%s:%d", job.ProjectID, snapshot.EventSequence)))
	operationID := "run_" + hex.EncodeToString(digest[:16])
	instruction := fmt.Sprintf("Review worker reports and agent messages through canonical event cut %d. Summarize material progress, raise exactly one consequential owner decision when needed, or freeze only genuinely new dependency-aware work for owner review. Do not execute effects or duplicate existing work.", snapshot.EventSequence)
	command := store.PrepareOwnerTurnCommand{
		WorkspaceIdentifier: job.WorkspaceID, ProjectIdentifier: job.ProjectID, ConversationID: job.ConversationID,
		Instruction: instruction, Kind: "review", IdempotencyKey: fmt.Sprintf("manager-review:%s:%d", job.ProjectID, snapshot.EventSequence),
		InitiatedBy: "manager", TriggerEventSequence: snapshot.EventSequence, ExpectedEventSequence: snapshot.EventSequence,
	}
	if replay, found, replayErr := s.store.OwnerTurnReplay(ctx, command); replayErr != nil {
		_ = s.store.FailOwnerManagerReview(ctx, job.ProjectID, snapshot.EventSequence, replayErr)
		return replayErr
	} else if found {
		return s.store.CompleteOwnerManagerReview(ctx, job.ProjectID, snapshot.EventSequence, replay.Turn.ID)
	}
	interpretation, err := interpreter.Interpret(ctx, execution.OwnerInterpretationRequest{
		OperationID: operationID, Kind: "review", Instruction: instruction, Provider: snapshot.Provider,
		CheckoutPath: snapshot.CheckoutPath, CanonicalContext: snapshot.CanonicalContext, EventCut: snapshot.EventSequence,
	})
	if err != nil {
		_ = s.store.FailOwnerManagerReview(ctx, job.ProjectID, snapshot.EventSequence, err)
		return err
	}
	citations, err := store.ResolveOwnerCitations(snapshot, interpretation.CitationRefs)
	if err != nil {
		_ = s.store.FailOwnerManagerReview(ctx, job.ProjectID, snapshot.EventSequence, err)
		return err
	}
	command.Interpretation, command.Citations = interpretation, citations
	turn, err := s.store.PrepareOwnerTurn(ctx, command)
	if err != nil {
		_ = s.store.FailOwnerManagerReview(ctx, job.ProjectID, snapshot.EventSequence, err)
		return err
	}
	if err := s.store.CompleteOwnerManagerReview(ctx, job.ProjectID, snapshot.EventSequence, turn.Turn.ID); err != nil {
		return err
	}
	s.config.Logger.Info("owner manager reviewed worker activity", "component", "owner_manager", "project_id", job.ProjectID,
		"turn_id", turn.Turn.ID, "event_sequence", snapshot.EventSequence, "disposition", turn.Turn.Interpretation.Disposition)
	return nil
}
