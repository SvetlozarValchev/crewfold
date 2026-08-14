package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

const (
	messageWakeTimeout   = 5 * time.Second
	messageWakeLease     = 10 * time.Second
	messageWakePassLimit = 16
	messageWakeIdleWait  = 250 * time.Millisecond
	messageWakeYieldWait = time.Millisecond
)

// startMessageWakeWorker starts one daemon-owned delivery lane. Request handlers
// only commit durable wake intents; they never perform runtime effects or wait
// for this worker.
func (s *server) startMessageWakeWorker() {
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		s.runMessageWakeWorker(s.leaseReconcileCtx)
	}()
}

func (s *server) runMessageWakeWorker(ctx context.Context) {
	for ctx.Err() == nil {
		processed, err := processMessageWakePass(
			ctx,
			s.store.ClaimMessageWakeJob,
			s.deliverMessageWake,
			s.store.SettleMessageWakeJob,
		)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.config.Logger.Error("message wake worker pass failed", "component", "message_wake", "error", err)
		}
		if !waitForMessageWake(ctx, messageWakeWaitDuration(processed)) {
			return
		}
	}
}

type messageWakeClaim func(context.Context, time.Duration) (domain.MessageWakeJob, bool, error)
type messageWakeDeliver func(context.Context, domain.MessageWakeJob, time.Duration) error
type messageWakeSettle func(context.Context, string, string, string) error

func processMessageWakePass(ctx context.Context, claim messageWakeClaim, deliver messageWakeDeliver, settle messageWakeSettle) (int, error) {
	processed := 0
	for processed < messageWakePassLimit && ctx.Err() == nil {
		job, found, err := claim(ctx, messageWakeLease)
		if err != nil {
			return processed, fmt.Errorf("claim message wake: %w", err)
		}
		if !found {
			return processed, nil
		}
		processed++

		wakeErr := deliver(ctx, job, messageWakeTimeout)
		if ctx.Err() != nil {
			// The durable lease is deliberately left incomplete. Recovery settles
			// it as failed_unknown: the external effect may have happened, so it
			// must never be issued a second time.
			return processed, ctx.Err()
		}
		diagnostic := ""
		outcome := domain.WakeSucceeded
		if wakeErr != nil {
			diagnostic = wakeErr.Error()
			outcome = domain.WakeFailed
			if errors.Is(wakeErr, context.DeadlineExceeded) || errors.Is(wakeErr, context.Canceled) {
				outcome = domain.WakeFailedUnknown
			}
		}
		if err := settle(ctx, job.ID, outcome, diagnostic); err != nil {
			return processed, fmt.Errorf("settle message wake %s: %w", job.ID, err)
		}
	}
	return processed, ctx.Err()
}

func messageWakeWaitDuration(processed int) time.Duration {
	if processed == messageWakePassLimit {
		// A full pass yields to request/control traffic and checks shutdown
		// before the next bounded pass while still draining a backlog quickly.
		return messageWakeYieldWait
	}
	return messageWakeIdleWait
}

func (s *server) deliverMessageWake(ctx context.Context, job domain.MessageWakeJob, timeout time.Duration) error {
	wakeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if s.config.MessageWake != nil {
		return s.config.MessageWake(wakeContext, job)
	}
	return s.wakeMessage(wakeContext, job)
}

func waitForMessageWake(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *server) wakeMessage(ctx context.Context, job domain.MessageWakeJob) error {
	run, err := s.store.RunByID(ctx, job.TargetRunID)
	if err != nil {
		return err
	}
	if run.RuntimeHandle == "" {
		return errors.New("runtime wake-up is unavailable before the target run has a runtime handle")
	}
	driver, exists := s.runtimes[run.Runtime]
	if !exists {
		return errors.New("runtime wake-up is unavailable because the target runtime driver is not registered")
	}
	prompter, supportsPrompt := driver.(execution.RuntimePrompter)
	if !supportsPrompt {
		return errors.New("runtime wake-up is unavailable for this runtime driver")
	}
	return prompter.Prompt(ctx, run.ID, run.RuntimeHandle, "Crewfold mailbox updated. Use crewfold_list_inbox to inspect durable messages.")
}
