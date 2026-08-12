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
	runJobLease  = 30 * time.Second
	runPollDelay = 20 * time.Millisecond
)

func (s *server) startRunWorker() {
	if s.config.DisableRunWorker {
		return
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		s.runWorker()
	}()
}

func (s *server) runWorker() {
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		work, found, err := s.store.ClaimRunJob(context.Background(), runJobLease)
		if err != nil {
			s.config.Logger.Error("run worker could not claim work", "component", "run_worker", "error", err)
			if !s.waitForRunWork() {
				return
			}
			continue
		}
		if !found {
			if !s.waitForRunWork() {
				return
			}
			continue
		}
		if err := s.processRunWork(work); err != nil {
			s.config.Logger.Error("run worker stopped after a fault barrier", "component", "run_worker", "run_id", work.Run.ID, "error", err)
			s.requestStop("run worker fault barrier")
			return
		}
	}
}

func (s *server) waitForRunWork() bool {
	timer := time.NewTimer(runPollDelay)
	defer timer.Stop()
	select {
	case <-s.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

func (s *server) processRunWork(work store.RunWork) error {
	run := work.Run
	correlationID := fmt.Sprintf("worker-%s-%d", run.ID, run.Revision)
	runtimeDriver, runtimeFound := s.runtimes[run.Runtime]
	providerAdapter, providerFound := s.providers[run.Provider]
	if !runtimeFound || !providerFound {
		message := fmt.Sprintf("configured adapters are unavailable for %s/%s", run.Runtime, run.Provider)
		if run.Status == domain.RunRequested || run.Status == domain.RunStarting {
			_, err := s.store.FailRunStart(context.Background(), run.ID, message, correlationID)
			s.logRunWorkerStoreError(run.ID, "record unavailable start adapter", err)
		} else if run.Status == domain.RunActive {
			_, err := s.store.FailRun(context.Background(), run.ID, "adapter_unavailable", message, correlationID)
			s.logRunWorkerStoreError(run.ID, "record unavailable active adapter", err)
		}
		return nil
	}

	switch run.Status {
	case domain.RunRequested:
		starting, err := s.store.MarkRunStarting(context.Background(), run.ID, correlationID)
		if err != nil {
			return s.recordWorkerTransitionFailure(run, "mark run starting", err, correlationID)
		}
		run = starting
		if err := s.runWorkerBarrier("after_run_starting", run); err != nil {
			return err
		}
		fallthrough
	case domain.RunStarting:
		if run.RuntimeHandle != "" {
			binding, reconcileErr := runtimeDriver.Reconcile(context.Background(), run.ID, run.RuntimeHandle)
			if reconcileErr != nil {
				_, recordErr := s.store.LoseRun(context.Background(), run.ID, "runtime launch outcome could not be reconciled safely: "+reconcileErr.Error(), correlationID)
				s.logRunWorkerStoreError(run.ID, "record lost runtime start", recordErr)
				return nil
			}
			providerBinding, bindErr := providerAdapter.Bind(context.Background(), run, binding)
			if bindErr != nil {
				s.handleProviderBindingFailure(run, runtimeDriver, binding.RuntimeHandle, bindErr, correlationID)
				return nil
			}
			_, startErr := s.store.MarkRunStarted(context.Background(), run.ID, binding.RuntimeHandle, providerBinding.ProviderHandle, correlationID)
			s.logRunWorkerStoreError(run.ID, "mark reconciled run started", startErr)
			return nil
		}
		spec, err := providerAdapter.Prepare(context.Background(), run, work.Scenario)
		if err != nil {
			_, recordErr := s.store.FailRunStart(context.Background(), run.ID, err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record provider preparation failure", recordErr)
			return nil
		}
		binding, err := runtimeDriver.Launch(context.Background(), run.ID, run.Placement, spec)
		if err != nil {
			var startError *execution.StartError
			var unknownError *execution.OutcomeUnknownError
			message := err.Error()
			if errors.As(err, &unknownError) {
				_, recordErr := s.store.LoseRun(context.Background(), run.ID, message, correlationID)
				s.logRunWorkerStoreError(run.ID, "record unknown runtime launch outcome", recordErr)
				return nil
			}
			if !errors.As(err, &startError) {
				message = "runtime launch failed: " + message
			}
			_, recordErr := s.store.FailRunStart(context.Background(), run.ID, message, correlationID)
			s.logRunWorkerStoreError(run.ID, "record runtime start failure", recordErr)
			return nil
		}
		if _, err := s.store.RecordRunRuntimeBinding(context.Background(), run.ID, binding.RuntimeHandle, correlationID); err != nil {
			s.config.Logger.Error("run worker could not persist runtime binding; the durable job will reconcile", "component", "run_worker", "run_id", run.ID, "error", err)
			return nil
		}
		if err := s.runWorkerBarrier("after_runtime_launch", run); err != nil {
			return err
		}
		providerBinding, err := providerAdapter.Bind(context.Background(), run, binding)
		if err != nil {
			s.handleProviderBindingFailure(run, runtimeDriver, binding.RuntimeHandle, err, correlationID)
			return nil
		}
		_, err = s.store.MarkRunStarted(context.Background(), run.ID, binding.RuntimeHandle, providerBinding.ProviderHandle, correlationID)
		s.logRunWorkerStoreError(run.ID, "mark run started", err)
		return nil
	case domain.RunActive:
		snapshot, err := runtimeDriver.Inspect(context.Background(), run.ID, run.RuntimeHandle)
		if err != nil {
			_, recordErr := s.store.LoseRun(context.Background(), run.ID, "runtime inspection failed without a trustworthy process outcome: "+err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record lost runtime after inspection failure", recordErr)
			return nil
		}
		observation, found, err := providerAdapter.Next(context.Background(), run, work.Scenario, snapshot)
		if err != nil {
			_, recordErr := s.store.FailRun(context.Background(), run.ID, "provider_observation_failed", err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record provider observation failure", recordErr)
			return nil
		}
		if !found {
			s.handleRunWithoutObservation(run, snapshot, correlationID)
			return nil
		}
		accepted, missing := true, []string(nil)
		if observation.Kind == domain.ObservationCompletion {
			if !snapshot.CompletionReady {
				if err := s.store.DeferRunJob(context.Background(), run.ID, 25*time.Millisecond); err != nil {
					s.logRunWorkerStoreError(run.ID, "defer completion until runtime settles", err)
				}
				return nil
			}
			accepted, missing = execution.AcceptancePasses(work.Scenario.Acceptance, observation.Evidence)
		}
		_, err = s.store.ApplyRunObservation(context.Background(), run.ID, observation, accepted, missing, correlationID)
		s.logRunWorkerStoreError(run.ID, "apply run observation", err)
		return nil
	case domain.RunStopping:
		result, err := runtimeDriver.Stop(context.Background(), run.ID, run.RuntimeHandle, execution.StopSpec{GracePeriod: time.Duration(run.StopGraceMillis) * time.Millisecond})
		if err != nil {
			var unknownError *execution.OutcomeUnknownError
			if errors.As(err, &unknownError) {
				_, recordErr := s.store.LoseRun(context.Background(), run.ID, "runtime stop outcome could not be proven: "+err.Error(), correlationID)
				s.logRunWorkerStoreError(run.ID, "record lost runtime during stop", recordErr)
				return nil
			}
			s.config.Logger.Error("run worker could not stop runtime", "component", "run_worker", "run_id", run.ID, "error", err)
			if deferErr := s.store.DeferRunJob(context.Background(), run.ID, 250*time.Millisecond); deferErr != nil {
				s.logRunWorkerStoreError(run.ID, "defer failed stop reconciliation", deferErr)
			}
			return nil
		}
		_, recordErr := s.store.MarkRunStopped(context.Background(), run.ID, result.Forced, result.Diagnostic, correlationID)
		s.logRunWorkerStoreError(run.ID, "mark run stopped", recordErr)
		return nil
	default:
		return nil
	}
}

func (s *server) handleProviderBindingFailure(run domain.Run, runtimeDriver execution.RuntimeDriver, runtimeHandle string, bindingErr error, correlationID string) {
	_, stopErr := runtimeDriver.Stop(context.Background(), run.ID, runtimeHandle, execution.StopSpec{GracePeriod: 500 * time.Millisecond})
	if stopErr != nil {
		message := fmt.Sprintf("provider binding failed: %v; runtime cleanup could not be proven: %v", bindingErr, stopErr)
		_, recordErr := s.store.LoseRun(context.Background(), run.ID, message, correlationID)
		s.logRunWorkerStoreError(run.ID, "record lost runtime after provider binding failure", recordErr)
		return
	}
	_, recordErr := s.store.FailRunStart(context.Background(), run.ID, bindingErr.Error(), correlationID)
	s.logRunWorkerStoreError(run.ID, "record provider binding failure after runtime cleanup", recordErr)
}

func (s *server) handleRunWithoutObservation(run domain.Run, snapshot execution.RuntimeSnapshot, correlationID string) {
	var code, message string
	switch snapshot.State {
	case execution.RuntimeStateStarting, execution.RuntimeStateRunning:
		if err := s.store.DeferRunJob(context.Background(), run.ID, 25*time.Millisecond); err != nil {
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
		_, err := s.store.LoseRun(context.Background(), run.ID, message, correlationID)
		s.logRunWorkerStoreError(run.ID, "record lost runtime state", err)
		return
	}
	_, err := s.store.FailRun(context.Background(), run.ID, code, message, correlationID)
	s.logRunWorkerStoreError(run.ID, "record terminal runtime without provider observation", err)
}

func (s *server) logRunWorkerStoreError(runID, operation string, err error) {
	if err != nil {
		s.config.Logger.Error("run worker could not persist a transition", "component", "run_worker", "run_id", runID, "operation", operation, "error", err)
	}
}

func (s *server) recordWorkerTransitionFailure(run domain.Run, operation string, err error, correlationID string) error {
	s.config.Logger.Error("run transition failed", "component", "run_worker", "run_id", run.ID, "operation", operation, "error", err)
	if run.Status == domain.RunRequested || run.Status == domain.RunStarting {
		_, _ = s.store.FailRunStart(context.Background(), run.ID, err.Error(), correlationID)
	}
	return nil
}

func (s *server) runWorkerBarrier(stage string, run domain.Run) error {
	if s.config.RunWorkerHook == nil {
		return nil
	}
	return s.config.RunWorkerHook(stage, run)
}
