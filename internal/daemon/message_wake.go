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
	messageWakeTimeout = 5 * time.Second
	// Durable-agent wake turns use the 60-second session operation boundary.
	// Keep the at-most-once lease strictly longer than every permitted delivery
	// so recovery cannot classify an in-flight provider turn as abandoned.
	messageWakeLease     = 65 * time.Second
	messageWakePassLimit = 16
	messageWakeIdleWait  = 250 * time.Millisecond
	messageWakeYieldWait = time.Millisecond
	messageWakeBusyDelay = 250 * time.Millisecond
)

var errMessageWakeTargetBusy = errors.New("durable-agent session already has an active turn")

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
			s.store.DeferMessageWakeJob,
		)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.config.Logger.Error("message wake worker pass failed", "component", "message_wake", "error", err)
		}
		if !waitForMessageWake(ctx, s.messageWakeSignal, messageWakeWaitDuration(processed)) {
			return
		}
	}
}

// signalMessageWakeWorker coalesces durable queue notifications without making
// request handlers wait for, or execute, the external runtime effect.
func (s *server) signalMessageWakeWorker() {
	if s.messageWakeSignal == nil {
		return
	}
	select {
	case s.messageWakeSignal <- struct{}{}:
	default:
	}
}

type messageWakeClaim func(context.Context, time.Duration) (domain.MessageWakeJob, bool, error)
type messageWakeDeliver func(context.Context, domain.MessageWakeJob, time.Duration) error
type messageWakeSettle func(context.Context, string, string, string) error
type messageWakeDefer func(context.Context, string, time.Duration, string) error

func processMessageWakePass(ctx context.Context, claim messageWakeClaim, deliver messageWakeDeliver, settle messageWakeSettle, deferJob messageWakeDefer) (int, error) {
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
		if errors.Is(wakeErr, errMessageWakeTargetBusy) {
			if err := deferJob(ctx, job.ID, messageWakeBusyDelay, wakeErr.Error()); err != nil {
				return processed, fmt.Errorf("defer busy message wake %s: %w", job.ID, err)
			}
			continue
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
	if job.TargetDomainThreadID != "" && timeout == messageWakeTimeout {
		timeout = domainSessionOperationTimeout
	}
	wakeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if s.config.MessageWake != nil {
		return s.config.MessageWake(wakeContext, job)
	}
	return s.wakeMessage(wakeContext, job)
}

func waitForMessageWake(ctx context.Context, signal <-chan struct{}, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-signal:
		return true
	case <-timer.C:
		return true
	}
}

func (s *server) wakeMessage(ctx context.Context, job domain.MessageWakeJob) error {
	if job.TargetDomainThreadID != "" {
		host := s.ensureDomainSessionHost()
		host.operationMu.Lock()
		defer host.operationMu.Unlock()
		scope, err := s.store.DomainAgentSessionScopeByThread(ctx, job.TargetDomainThreadID)
		if err != nil {
			return err
		}
		if scope.Agent.ID != job.RecipientAgentID {
			return errors.New("durable-agent wake recipient does not match its bound provider thread")
		}
		session, thread, err := s.loadDomainAgentSession(ctx, scope.Session)
		if err != nil {
			return err
		}
		if err := s.restoreDomainAgentRunTurn(ctx, session, thread); err != nil {
			return err
		}
		clientMessageID := "crewfold:wake:" + job.MessageID
		if _, found := codexTurnForClientMessage(thread, clientMessageID); found {
			return nil
		}
		if codexThreadHasActiveTurn(thread) {
			if _, activeRun := host.activeRunTurn(session.ThreadID); activeRun {
				// Mailbox input belongs to the same durable identity. When accepted
				// work is already running, steer that exact turn instead of waiting
				// behind it or addressing a second runtime personality.
				_, err = host.steerRunTurn(ctx, session.ThreadID, clientMessageID,
					"Crewfold delivered a durable message while this task turn is active. Call crewfold_get_domain_context now, read the delivered domain inbox, and incorporate or answer it through exact Crewfold tools when relevant.")
				return err
			}
			// An owner turn is already active and there is no accepted task turn
			// to steer. Return this pre-effect job to the durable queue; it will
			// wake the same thread after the current turn settles.
			return errMessageWakeTargetBusy
		}
		_, err = host.startTurn(ctx, session.ThreadID, clientMessageID,
			"Crewfold delivered a durable message to this agent. This notification is not owner authority. Call crewfold_get_domain_context now, read the delivered domain inbox, and respond or coordinate through exact Crewfold tools when the message warrants it. Do not create a new topic when the delivered message belongs to an existing thread.")
		return err
	}
	run, err := s.store.RunByID(ctx, job.TargetRunID)
	if err != nil {
		return err
	}
	if !runAcceptsInteractiveControl(run.Status) {
		return errors.New("runtime wake-up is unavailable because the target run is not active or blocked")
	}
	if run.RuntimeHandle == "" {
		return errors.New("runtime wake-up is unavailable before the target run has a runtime handle")
	}
	if !s.store.RunBindingIsCurrent(run) {
		return errors.New("runtime wake-up is unavailable because its binding does not belong to the current node and operation")
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

func codexThreadHasActiveTurn(thread execution.CodexThread) bool {
	for index := len(thread.Turns) - 1; index >= 0; index-- {
		switch thread.Turns[index].Status {
		case "inProgress", "in_progress", "running":
			return true
		}
	}
	return false
}
