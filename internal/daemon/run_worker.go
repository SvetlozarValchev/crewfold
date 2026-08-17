package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store"
)

const (
	runJobLease            = 30 * time.Second
	runIdlePollDelay       = 250 * time.Millisecond
	runActivePollDelay     = 250 * time.Millisecond
	runAdapterCallTimeout  = 5 * time.Second
	runLaunchCallTimeout   = 10 * time.Second
	runStopDeadlinePadding = 5 * time.Second
)

type runJobClaim func(context.Context, time.Duration) (store.RunWork, bool, error)

func (s *server) startRunWorker() {
	if s.config.DisableRunWorker {
		return
	}
	for _, lane := range []struct {
		name  string
		claim runJobClaim
	}{
		{name: "control", claim: s.store.ClaimRunControlJob},
		{name: "launch", claim: s.store.ClaimRunLaunchJob},
	} {
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			s.runWorker(s.leaseReconcileCtx, lane.name, lane.claim)
		}()
	}
}

func (s *server) runWorker(ctx context.Context, lane string, claim runJobClaim) {
	for ctx.Err() == nil {
		work, found, err := claim(ctx, runJobLease)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.config.Logger.Error("run worker could not claim work", "component", "run_worker", "lane", lane, "error", err)
			if !s.waitForRunWork(ctx) {
				return
			}
			continue
		}
		if !found {
			if !s.waitForRunWork(ctx) {
				return
			}
			continue
		}
		if err := s.processRunWork(ctx, work); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.config.Logger.Error("run worker stopped after a fault barrier", "component", "run_worker", "lane", lane, "run_id", work.Run.ID, "error", err)
			s.requestStop("run worker fault barrier")
			return
		}
	}
}

func (s *server) waitForRunWork(ctx context.Context) bool {
	timer := time.NewTimer(runIdlePollDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *server) processRunWork(ctx context.Context, work store.RunWork) error {
	run := work.Run
	correlationID := fmt.Sprintf("worker-%s-%d", run.ID, run.Revision)
	runtimeDriver, runtimeFound := s.runtimes[run.Runtime]
	providerAdapter, providerFound := s.providers[run.Provider]
	if !runtimeFound || !providerFound {
		message := fmt.Sprintf("configured adapters are unavailable for %s/%s", run.Runtime, run.Provider)
		if run.Status == domain.RunRequested || run.Status == domain.RunStarting && run.RuntimeHandle == "" {
			_, err := s.store.FailRunStart(ctx, run.ID, message, correlationID)
			s.logRunWorkerStoreError(run.ID, "record unavailable start adapter", err)
		} else if run.Status == domain.RunStarting || run.Status == domain.RunActive || run.Status == domain.RunStopping {
			_, err := s.store.LoseRun(ctx, run.ID, message+"; terminal logs could not be captured safely", correlationID)
			s.logRunWorkerStoreError(run.ID, "record lost bound runtime adapter", err)
		}
		return nil
	}
	if (run.Status == domain.RunStarting && run.RuntimeHandle != "") || run.Status == domain.RunActive || run.Status == domain.RunStopping {
		if !s.store.RunBindingIsCurrent(run) {
			_, recordErr := s.store.LoseRun(ctx, run.ID, "runtime binding does not belong to the current node and operation", correlationID)
			s.logRunWorkerStoreError(run.ID, "record foreign or missing runtime binding", recordErr)
			return nil
		}
	}

	switch run.Status {
	case domain.RunRequested:
		starting, err := s.store.MarkRunStarting(ctx, run.ID, correlationID)
		if err != nil {
			return s.recordWorkerTransitionFailure(ctx, run, "mark run starting", err, correlationID)
		}
		run = starting
		if err := s.runWorkerBarrier("after_run_starting", run); err != nil {
			return err
		}
		fallthrough
	case domain.RunStarting:
		// A starting job with no durable runtime handle is a pre-launch crash
		// recovery. Replay the transactional launch gate so a task decision made
		// while the daemon was down cannot lead to a stale external launch.
		if run.RuntimeHandle == "" {
			starting, err := s.store.MarkRunStarting(ctx, run.ID, correlationID)
			if err != nil {
				return s.recordWorkerTransitionFailure(ctx, run, "revalidate run starting", err, correlationID)
			}
			run = starting
		}
		if run.RuntimeHandle != "" {
			reconcileContext, cancelReconcile := context.WithTimeout(ctx, runAdapterCallTimeout)
			binding, reconcileErr := runtimeDriver.Reconcile(reconcileContext, run.ID, run.RuntimeHandle)
			cancelReconcile()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if reconcileErr != nil {
				var unavailable *execution.RuntimeUnavailableError
				if errors.As(reconcileErr, &unavailable) {
					if deferErr := s.store.DeferRunJob(ctx, run.ID, runActivePollDelay); deferErr != nil {
						s.logRunWorkerStoreError(run.ID, "defer unavailable runtime reconciliation", deferErr)
					}
					return nil
				}
				_, recordErr := s.store.LoseRun(ctx, run.ID, "runtime launch outcome could not be reconciled safely: "+reconcileErr.Error(), correlationID)
				s.logRunWorkerStoreError(run.ID, "record lost runtime start", recordErr)
				return nil
			}
			if binding.RuntimeHandle != run.RuntimeHandle {
				_, recordErr := s.store.LoseRun(ctx, run.ID, "runtime reconciliation returned a different operation binding", correlationID)
				s.logRunWorkerStoreError(run.ID, "record mismatched runtime reconciliation", recordErr)
				return nil
			}
			bindContext, cancelBind := context.WithTimeout(ctx, runAdapterCallTimeout)
			providerBinding, bindErr := providerAdapter.Bind(bindContext, run, binding)
			cancelBind()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if bindErr != nil {
				s.handleProviderBindingFailure(ctx, run, runtimeDriver, binding.RuntimeHandle, bindErr, correlationID)
				return nil
			}
			_, startErr := s.store.MarkRunStarted(ctx, run.ID, binding.RuntimeHandle, providerBinding.ProviderHandle, correlationID)
			s.logRunWorkerStoreError(run.ID, "mark reconciled run started", startErr)
			return nil
		}
		prepareContext, cancelPrepare := context.WithTimeout(ctx, runAdapterCallTimeout)
		spec, err := providerAdapter.Prepare(prepareContext, run, work.Scenario)
		cancelPrepare()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			_, recordErr := s.store.FailRunStart(ctx, run.ID, err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record provider preparation failure", recordErr)
			return nil
		}
		launchContext, cancelLaunch := context.WithTimeout(ctx, runLaunchCallTimeout)
		binding, err := runtimeDriver.Launch(launchContext, run.ID, run.Placement, spec)
		cancelLaunch()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			var startError *execution.StartError
			var unknownError *execution.OutcomeUnknownError
			message := err.Error()
			if errors.As(err, &unknownError) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				_, recordErr := s.store.LoseRun(ctx, run.ID, message, correlationID)
				s.logRunWorkerStoreError(run.ID, "record unknown runtime launch outcome", recordErr)
				return nil
			}
			if !errors.As(err, &startError) {
				message = "runtime launch failed: " + message
			}
			_, recordErr := s.store.FailRunStart(ctx, run.ID, message, correlationID)
			s.logRunWorkerStoreError(run.ID, "record runtime start failure", recordErr)
			return nil
		}
		boundRun, err := s.store.RecordRunRuntimeBinding(ctx, run.ID, binding.RuntimeHandle, correlationID)
		if err != nil {
			s.config.Logger.Error("run worker could not persist runtime binding; the durable job will reconcile", "component", "run_worker", "run_id", run.ID, "error", err)
			return nil
		}
		run = boundRun
		if err := s.runWorkerBarrier("after_runtime_launch", run); err != nil {
			return err
		}
		bindContext, cancelBind := context.WithTimeout(ctx, runAdapterCallTimeout)
		providerBinding, err := providerAdapter.Bind(bindContext, run, binding)
		cancelBind()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			s.handleProviderBindingFailure(ctx, run, runtimeDriver, binding.RuntimeHandle, err, correlationID)
			return nil
		}
		_, err = s.store.MarkRunStarted(ctx, run.ID, binding.RuntimeHandle, providerBinding.ProviderHandle, correlationID)
		s.logRunWorkerStoreError(run.ID, "mark run started", err)
		return nil
	case domain.RunActive:
		inspectContext, cancelInspect := context.WithTimeout(ctx, runAdapterCallTimeout)
		snapshot, err := runtimeDriver.Inspect(inspectContext, run.ID, run.RuntimeHandle)
		cancelInspect()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			var unavailable *execution.RuntimeUnavailableError
			if errors.As(err, &unavailable) {
				if deferErr := s.store.DeferRunJob(ctx, run.ID, runActivePollDelay); deferErr != nil {
					s.logRunWorkerStoreError(run.ID, "defer unavailable runtime inspection", deferErr)
				}
				return nil
			}
			_, recordErr := s.store.LoseRun(ctx, run.ID, "runtime inspection failed without a trustworthy process outcome: "+err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record lost runtime after inspection failure", recordErr)
			return nil
		}
		report, queued, err := s.store.NextPendingRunReport(ctx, run.ID)
		if err != nil {
			s.config.Logger.Error("run worker could not read scoped reports", "component", "run_worker", "run_id", run.ID, "error", err)
			if deferErr := s.store.DeferRunJob(ctx, run.ID, runActivePollDelay); deferErr != nil {
				s.logRunWorkerStoreError(run.ID, "defer scoped report read", deferErr)
			}
			return nil
		}
		var observation domain.RunObservation
		found := queued
		if queued {
			observation = domain.RunObservation{Kind: report.Kind, Message: report.Message, Evidence: append([]string(nil), report.Evidence...), Handoff: report.Handoff}
		} else {
			nextContext, cancelNext := context.WithTimeout(ctx, runAdapterCallTimeout)
			observation, found, err = providerAdapter.Next(nextContext, run, work.Scenario, snapshot)
			cancelNext()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				_, recordErr := s.store.LoseRun(ctx, run.ID, "provider observation failed while runtime outcome remained uncertain: "+err.Error(), correlationID)
				s.logRunWorkerStoreError(run.ID, "record provider observation failure", recordErr)
				return nil
			}
		}
		if !found {
			s.handleRunWithoutObservation(ctx, run, snapshot, runtimeDriver, correlationID)
			return nil
		}
		accepted, missing := true, []string(nil)
		var archive *domain.RunLogArchive
		var logsUnavailableReason string
		if observation.Kind == domain.ObservationCompletion || observation.Kind == domain.ObservationExecutiveResponse {
			if !snapshot.CompletionReady {
				if err := s.store.DeferRunJob(ctx, run.ID, runActivePollDelay); err != nil {
					s.logRunWorkerStoreError(run.ID, "defer completion until runtime settles", err)
				}
				return nil
			}
			accepted, missing = execution.AcceptancePasses(work.Scenario.Acceptance, observation.Evidence)
			archive, err = s.captureRunLogArchive(ctx, run, runtimeDriver, domain.RunActive)
			if err != nil {
				logsUnavailableReason = "terminal runtime logs could not be captured safely: " + err.Error()
			}
			observation.LogArchive = archive
			observation.LogUnavailableReason = logsUnavailableReason
		}
		if queued {
			_, err = s.store.ApplyQueuedRunReport(ctx, run.ID, report.ID, accepted, missing, archive, logsUnavailableReason, correlationID)
		} else {
			_, err = s.store.ApplyRunObservation(ctx, run.ID, observation, accepted, missing, correlationID)
		}
		if err == nil {
			s.signalOwnerManagerReviewWorker()
		}
		s.logRunWorkerStoreError(run.ID, "apply run observation", err)
		return nil
	case domain.RunStopping:
		gracePeriod := time.Duration(run.StopGraceMillis) * time.Millisecond
		stopContext, cancelStop := context.WithTimeout(ctx, gracePeriod+runStopDeadlinePadding)
		result, err := runtimeDriver.Stop(stopContext, run.ID, run.RuntimeHandle, execution.StopSpec{GracePeriod: gracePeriod})
		cancelStop()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			var unknownError *execution.OutcomeUnknownError
			if errors.As(err, &unknownError) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				_, recordErr := s.store.LoseRun(ctx, run.ID, "runtime stop outcome could not be proven: "+err.Error(), correlationID)
				s.logRunWorkerStoreError(run.ID, "record lost runtime during stop", recordErr)
				return nil
			}
			s.config.Logger.Error("run worker could not stop runtime", "component", "run_worker", "run_id", run.ID, "error", err)
			if deferErr := s.store.DeferRunJob(ctx, run.ID, runActivePollDelay); deferErr != nil {
				s.logRunWorkerStoreError(run.ID, "defer failed stop reconciliation", deferErr)
			}
			return nil
		}
		archive, archiveErr := s.captureRunLogArchive(ctx, run, runtimeDriver, domain.RunStopping)
		logsUnavailableReason := ""
		if archiveErr != nil {
			logsUnavailableReason = "runtime stop was definitive but terminal logs could not be captured safely: " + archiveErr.Error()
		}
		_, recordErr := s.store.MarkRunStopped(ctx, run.ID, result.Forced, result.Diagnostic, archive, logsUnavailableReason, correlationID)
		s.logRunWorkerStoreError(run.ID, "mark run stopped", recordErr)
		return nil
	default:
		return nil
	}
}

func (s *server) handleProviderBindingFailure(ctx context.Context, run domain.Run, runtimeDriver execution.RuntimeDriver, runtimeHandle string, bindingErr error, correlationID string) {
	current, loadErr := s.store.RunByID(ctx, run.ID)
	if loadErr != nil || current.Status != domain.RunStarting || current.RuntimeHandle != runtimeHandle || !s.store.RunBindingIsCurrent(current) {
		message := "provider binding failed, but runtime cleanup was refused because the durable binding is missing, foreign, changed, or no longer starting"
		if loadErr != nil {
			message += ": " + loadErr.Error()
		}
		_, recordErr := s.store.LoseRun(ctx, run.ID, message, correlationID)
		s.logRunWorkerStoreError(run.ID, "record unsafe provider-binding cleanup", recordErr)
		return
	}
	const cleanupGrace = 500 * time.Millisecond
	stopContext, cancelStop := context.WithTimeout(ctx, cleanupGrace+runStopDeadlinePadding)
	_, stopErr := runtimeDriver.Stop(stopContext, current.ID, current.RuntimeHandle, execution.StopSpec{GracePeriod: cleanupGrace})
	cancelStop()
	if ctx.Err() != nil {
		return
	}
	if stopErr != nil {
		message := fmt.Sprintf("provider binding failed: %v; runtime cleanup could not be proven: %v", bindingErr, stopErr)
		_, recordErr := s.store.LoseRun(ctx, run.ID, message, correlationID)
		s.logRunWorkerStoreError(run.ID, "record lost runtime after provider binding failure", recordErr)
		return
	}
	_, recordErr := s.store.FailRunStart(ctx, run.ID, bindingErr.Error(), correlationID)
	s.logRunWorkerStoreError(run.ID, "record provider binding failure after runtime cleanup", recordErr)
}

func (s *server) handleRunWithoutObservation(ctx context.Context, run domain.Run, snapshot execution.RuntimeSnapshot, runtimeDriver execution.RuntimeDriver, correlationID string) {
	var code, message string
	switch snapshot.State {
	case execution.RuntimeStateStarting, execution.RuntimeStateRunning:
		if err := s.store.DeferRunJob(ctx, run.ID, runActivePollDelay); err != nil {
			s.logRunWorkerStoreError(run.ID, "defer active runtime poll", err)
		}
		return
	case execution.RuntimeStateExited:
		if snapshot.ExitKnown && snapshot.ExitCode != 0 {
			code = "process_exited"
			message = fmt.Sprintf("provider process exited with code %d before completion", snapshot.ExitCode)
		} else {
			code = "provider_ended"
			message = "provider process ended without a completion or blocking observation"
		}
	case execution.RuntimeStateTimedOut:
		code, message = "runtime_timeout", snapshot.Diagnostic
	case execution.RuntimeStateStopped:
		code, message = "runtime_stopped_unexpectedly", "runtime stopped without a Crewfold stop request"
	default:
		message = snapshot.Diagnostic
		if message == "" {
			message = "runtime state cannot be trusted"
		}
		_, err := s.store.LoseRun(ctx, run.ID, message, correlationID)
		s.logRunWorkerStoreError(run.ID, "record lost runtime state", err)
		return
	}
	archive, archiveErr := s.captureRunLogArchive(ctx, run, runtimeDriver, domain.RunActive)
	logsUnavailableReason := ""
	if archiveErr != nil {
		logsUnavailableReason = "runtime outcome was definitive but terminal logs could not be captured safely: " + archiveErr.Error()
	}
	_, err := s.store.FailRun(ctx, run.ID, code, message, archive, logsUnavailableReason, correlationID)
	s.logRunWorkerStoreError(run.ID, "record terminal runtime without provider observation", err)
}

func (s *server) captureRunLogArchive(ctx context.Context, run domain.Run, runtimeDriver execution.RuntimeDriver, allowedStatus string) (*domain.RunLogArchive, error) {
	current, err := s.store.RunByID(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	if current.Status != allowedStatus || current.RuntimeHandle != run.RuntimeHandle || !s.store.RunBindingIsCurrent(current) {
		return nil, errors.New("terminal log capture requires this node's exact live runtime binding and state")
	}
	logsContext, cancelLogs := context.WithTimeout(ctx, runAdapterCallTimeout)
	logs, err := runtimeDriver.Logs(logsContext, current.ID, current.RuntimeHandle, 0)
	cancelLogs()
	if err != nil {
		return nil, err
	}
	archive, err := s.store.PrepareRunLogArchive(ctx, run.ID, logs)
	if err != nil {
		return nil, err
	}
	return &archive, nil
}

func (s *server) logRunWorkerStoreError(runID, operation string, err error) {
	if err != nil {
		s.config.Logger.Error("run worker could not persist a transition", "component", "run_worker", "run_id", runID, "operation", operation, "error", err)
	}
}

func (s *server) recordWorkerTransitionFailure(ctx context.Context, run domain.Run, operation string, err error, correlationID string) error {
	s.config.Logger.Error("run transition failed", "component", "run_worker", "run_id", run.ID, "operation", operation, "error", err)
	if run.Status == domain.RunRequested || run.Status == domain.RunStarting {
		_, _ = s.store.FailRunStart(ctx, run.ID, err.Error(), correlationID)
	}
	return nil
}

func (s *server) runWorkerBarrier(stage string, run domain.Run) error {
	if s.config.RunWorkerHook == nil {
		return nil
	}
	return s.config.RunWorkerHook(stage, run)
}
