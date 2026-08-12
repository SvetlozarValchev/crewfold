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
		spec, err := providerAdapter.Prepare(context.Background(), run, work.Scenario)
		if err != nil {
			_, recordErr := s.store.FailRunStart(context.Background(), run.ID, err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record provider preparation failure", recordErr)
			return nil
		}
		binding, err := runtimeDriver.Launch(context.Background(), run.ID, run.Placement, spec)
		if err != nil {
			var startError *execution.StartError
			message := err.Error()
			if !errors.As(err, &startError) {
				message = "runtime launch failed: " + message
			}
			_, recordErr := s.store.FailRunStart(context.Background(), run.ID, message, correlationID)
			s.logRunWorkerStoreError(run.ID, "record runtime start failure", recordErr)
			return nil
		}
		if err := s.runWorkerBarrier("after_runtime_launch", run); err != nil {
			return err
		}
		providerBinding, err := providerAdapter.Bind(context.Background(), run, binding)
		if err != nil {
			_, recordErr := s.store.FailRunStart(context.Background(), run.ID, err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record provider binding failure", recordErr)
			return nil
		}
		_, err = s.store.MarkRunStarted(context.Background(), run.ID, binding.RuntimeHandle, providerBinding.ProviderHandle, correlationID)
		s.logRunWorkerStoreError(run.ID, "mark run started", err)
		return nil
	case domain.RunActive:
		observation, found, err := providerAdapter.Next(context.Background(), run, work.Scenario)
		if err != nil {
			_, recordErr := s.store.FailRun(context.Background(), run.ID, "provider_observation_failed", err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record provider observation failure", recordErr)
			return nil
		}
		if !found {
			_, recordErr := s.store.FailRun(context.Background(), run.ID, "scenario_exhausted", "provider ended without a completion or blocking observation", correlationID)
			s.logRunWorkerStoreError(run.ID, "record exhausted provider scenario", recordErr)
			return nil
		}
		accepted, missing := true, []string(nil)
		if observation.Kind == domain.ObservationCompletion {
			accepted, missing = execution.AcceptancePasses(work.Scenario.Acceptance, observation.Evidence)
		}
		_, err = s.store.ApplyRunObservation(context.Background(), run.ID, observation, accepted, missing, correlationID)
		s.logRunWorkerStoreError(run.ID, "apply run observation", err)
		return nil
	default:
		return nil
	}
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
