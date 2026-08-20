package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/servicehost"
	"crewfold/internal/store"
)

const (
	managedServiceJobLease      = 30 * time.Second
	managedServiceIdlePollDelay = 250 * time.Millisecond
	managedServiceCallTimeout   = 10 * time.Second
)

func (s *server) startManagedServiceWorker() {
	if s.config.DisableManagedServiceWorker {
		return
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		s.managedServiceWorker(s.leaseReconcileCtx)
	}()
}

func (s *server) managedServiceWorker(ctx context.Context) {
	for ctx.Err() == nil {
		work, found, err := s.store.ClaimManagedServiceJob(ctx, managedServiceJobLease)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.config.Logger.Error("managed-service worker could not claim work", "component", "managed_service_worker", "error", err)
			s.waitForManagedServiceWork(ctx)
			continue
		}
		if !found {
			s.waitForManagedServiceWork(ctx)
			continue
		}
		s.processManagedServiceWork(ctx, work)
	}
}

func (s *server) waitForManagedServiceWork(ctx context.Context) bool {
	timer := time.NewTimer(managedServiceIdlePollDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *server) processManagedServiceWork(ctx context.Context, work store.ManagedServiceWork) {
	correlationID := fmt.Sprintf("service-worker-%s-%d", work.Instance.ID, work.Instance.Revision)
	switch work.Job.Action {
	case domain.ManagedServiceJobStart, domain.ManagedServiceJobRestart:
		if work.Job.Action == domain.ManagedServiceJobRestart && work.Instance.Status == domain.ManagedServiceStopping {
			if !s.store.ManagedServiceBindingIsCurrent(work.Instance) {
				_, err := s.store.LoseManagedService(ctx, work.Instance.ID, "managed-service restart binding does not belong to this node", correlationID)
				s.logManagedServiceError(work.Instance.ID, "record unsafe restart binding", err)
				return
			}
			stopContext, cancelStop := context.WithTimeout(ctx, time.Duration(work.Definition.StopGraceMillis)*time.Millisecond+time.Second)
			_, stopErr := s.serviceHost.Stop(stopContext, work.Instance.ID, managedServiceHostBinding(work.Instance), time.Duration(work.Definition.StopGraceMillis)*time.Millisecond)
			cancelStop()
			if stopErr != nil {
				_, recordErr := s.store.LoseManagedService(ctx, work.Instance.ID, "managed-service restart could not prove the prior process ended: "+stopErr.Error(), correlationID)
				s.logManagedServiceError(work.Instance.ID, "record unsafe restart stop", recordErr)
				return
			}
			s.serviceHost.Forget(work.Instance.ID)
		}
		work, err := s.store.MarkManagedServiceStarting(ctx, work.Instance.ID, correlationID)
		if err != nil {
			s.logManagedServiceError(work.Instance.ID, "mark starting", err)
			return
		}
		launchContext, cancelLaunch := context.WithTimeout(ctx, managedServiceCallTimeout)
		binding, err := s.serviceHost.Start(launchContext, work.Instance.ID, work.Instance.RestartCount+1, work.CheckoutPath, work.Definition)
		cancelLaunch()
		if err != nil {
			if work.Definition.RestartPolicy == domain.ManagedServiceRestartOnFailure && work.Instance.RestartCount < work.Definition.MaximumRestarts {
				_, restartErr := s.store.ScheduleManagedServiceRestart(ctx, work.Instance.ID, err.Error(), correlationID, time.Duration(work.Definition.RestartCooldownMillis)*time.Millisecond)
				s.logManagedServiceError(work.Instance.ID, "schedule process restart after launch failure", restartErr)
				return
			}
			_, recordErr := s.store.FailManagedServiceWithLogs(ctx, work.Instance.ID, "service_start_failed", err.Error(), nil, "process did not start, so no terminal logs exist", correlationID)
			s.logManagedServiceError(work.Instance.ID, "record start failure", recordErr)
			s.cleanupManagedServiceRuntime(work.Instance.ID, recordErr)
			return
		}
		probeContext, cancelProbe := context.WithTimeout(ctx, time.Duration(work.Definition.Health.TimeoutMillis)*time.Millisecond)
		snapshot := s.serviceHost.Inspect(probeContext, work.Instance.ID, binding, work.Definition.Health)
		cancelProbe()
		health := snapshot.Health
		if !snapshot.Running {
			_, _ = s.serviceHost.Stop(context.Background(), work.Instance.ID, binding, 100*time.Millisecond)
			archive, unavailable := s.captureManagedServiceLogArchive(ctx, work, binding)
			s.serviceHost.Forget(work.Instance.ID)
			if snapshot.ExitKnown && snapshot.ExitCode != 0 && work.Definition.RestartPolicy == domain.ManagedServiceRestartOnFailure && work.Instance.RestartCount < work.Definition.MaximumRestarts {
				_, restartErr := s.store.ScheduleManagedServiceRestart(ctx, work.Instance.ID, snapshot.Diagnostic, correlationID, time.Duration(work.Definition.RestartCooldownMillis)*time.Millisecond)
				s.logManagedServiceError(work.Instance.ID, "schedule process restart after startup exit", restartErr)
				return
			}
			_, recordErr := s.store.FailManagedServiceWithLogs(ctx, work.Instance.ID, "service_start_failed", snapshot.Diagnostic, archive, unavailable, correlationID)
			s.logManagedServiceError(work.Instance.ID, "record early process exit", recordErr)
			s.cleanupManagedServiceRuntime(work.Instance.ID, recordErr)
			return
		}
		_, err = s.store.RecordManagedServiceStarted(ctx, work.Instance.ID, store.ManagedServiceRuntimeBindingInput{
			PID: binding.PID, ProcessGroupID: binding.ProcessGroupID, ProcessStartTicks: binding.ProcessStartTicks,
			StdoutPath: binding.StdoutPath, StderrPath: binding.StderrPath,
		}, health, snapshot.Diagnostic, correlationID)
		if err != nil {
			stopContext, cancelStop := context.WithTimeout(context.Background(), time.Duration(work.Definition.StopGraceMillis)*time.Millisecond+time.Second)
			_, _ = s.serviceHost.Stop(stopContext, work.Instance.ID, binding, time.Duration(work.Definition.StopGraceMillis)*time.Millisecond)
			cancelStop()
			archive, unavailable := s.captureManagedServiceLogArchive(ctx, work, binding)
			s.serviceHost.Forget(work.Instance.ID)
			s.logManagedServiceError(work.Instance.ID, "record started process", err)
			diagnostic := "managed-service runtime binding could not be recorded: " + err.Error()
			if work.Definition.RestartPolicy == domain.ManagedServiceRestartOnFailure && work.Instance.RestartCount < work.Definition.MaximumRestarts {
				_, restartErr := s.store.ScheduleManagedServiceRestart(ctx, work.Instance.ID, diagnostic, correlationID, time.Duration(work.Definition.RestartCooldownMillis)*time.Millisecond)
				s.logManagedServiceError(work.Instance.ID, "schedule process restart after binding failure", restartErr)
				return
			}
			if archive == nil && unavailable == "" {
				unavailable = "runtime binding failed before terminal logs could be sealed"
			}
			_, recordErr := s.store.FailManagedServiceWithLogs(ctx, work.Instance.ID, "service_start_record_failed", diagnostic, archive, unavailable, correlationID)
			s.logManagedServiceError(work.Instance.ID, "record runtime binding failure", recordErr)
			s.cleanupManagedServiceRuntime(work.Instance.ID, recordErr)
		}
	case domain.ManagedServiceJobProbe:
		binding := managedServiceHostBinding(work.Instance)
		if !s.store.ManagedServiceBindingIsCurrent(work.Instance) {
			_, err := s.store.LoseManagedService(ctx, work.Instance.ID, "managed-service runtime binding does not belong to this node", correlationID)
			s.logManagedServiceError(work.Instance.ID, "record foreign binding", err)
			return
		}
		probeContext, cancelProbe := context.WithTimeout(ctx, time.Duration(work.Definition.Health.TimeoutMillis)*time.Millisecond)
		snapshot := s.serviceHost.Inspect(probeContext, work.Instance.ID, binding, work.Definition.Health)
		cancelProbe()
		if !snapshot.Running {
			restart := snapshot.ExitKnown && snapshot.ExitCode != 0 && work.Definition.RestartPolicy == domain.ManagedServiceRestartOnFailure
			if !snapshot.Tracked && !snapshot.ExitKnown && work.Definition.RestartPolicy == domain.ManagedServiceRestartOnDaemon {
				restart = true
			}
			if restart && work.Instance.RestartCount < work.Definition.MaximumRestarts {
				s.serviceHost.Forget(work.Instance.ID)
				_, err := s.store.ScheduleManagedServiceRestart(ctx, work.Instance.ID, snapshot.Diagnostic, correlationID, time.Duration(work.Definition.RestartCooldownMillis)*time.Millisecond)
				s.logManagedServiceError(work.Instance.ID, "schedule process restart", err)
				return
			}
			if !snapshot.ExitKnown {
				archive, unavailable := s.captureManagedServiceLogArchive(ctx, work, binding)
				if archive == nil && unavailable == "" {
					unavailable = "runtime ownership was unavailable after daemon restart"
				}
				_, err := s.store.FailManagedServiceWithLogs(ctx, work.Instance.ID, "service_runtime_ended", snapshot.Diagnostic, archive, unavailable, correlationID)
				s.logManagedServiceError(work.Instance.ID, "record ended process after daemon restart", err)
				s.cleanupManagedServiceRuntime(work.Instance.ID, err)
				return
			}
			if snapshot.ExitCode == 0 {
				archive, unavailable := s.captureManagedServiceLogArchive(ctx, work, binding)
				s.serviceHost.Forget(work.Instance.ID)
				_, err := s.store.StopManagedServiceWithLogs(ctx, work.Instance.ID, snapshot.ExitCode, snapshot.Diagnostic, archive, unavailable, correlationID)
				s.logManagedServiceError(work.Instance.ID, "record clean process exit", err)
				s.cleanupManagedServiceRuntime(work.Instance.ID, err)
				return
			}
			archive, unavailable := s.captureManagedServiceLogArchive(ctx, work, binding)
			s.serviceHost.Forget(work.Instance.ID)
			_, err := s.store.FailManagedServiceWithLogs(ctx, work.Instance.ID, "service_process_exited", snapshot.Diagnostic, archive, unavailable, correlationID)
			s.logManagedServiceError(work.Instance.ID, "record failed process exit", err)
			s.cleanupManagedServiceRuntime(work.Instance.ID, err)
			return
		}
		_, err := s.store.ObserveManagedService(ctx, work.Instance.ID, snapshot.Health, snapshot.Diagnostic, correlationID, time.Duration(work.Definition.Health.IntervalMillis)*time.Millisecond)
		s.logManagedServiceError(work.Instance.ID, "record health observation", err)
	case domain.ManagedServiceJobStop:
		if !s.store.ManagedServiceBindingIsCurrent(work.Instance) {
			_, err := s.store.LoseManagedService(ctx, work.Instance.ID, "managed-service runtime binding does not belong to this node", correlationID)
			s.logManagedServiceError(work.Instance.ID, "record unsafe stop binding", err)
			return
		}
		stopContext, cancelStop := context.WithTimeout(ctx, time.Duration(work.Definition.StopGraceMillis)*time.Millisecond+time.Second)
		forced, err := s.serviceHost.Stop(stopContext, work.Instance.ID, managedServiceHostBinding(work.Instance), time.Duration(work.Definition.StopGraceMillis)*time.Millisecond)
		cancelStop()
		if err != nil {
			_, recordErr := s.store.FailManagedServiceWithLogs(ctx, work.Instance.ID, "service_stop_failed", err.Error(), nil, "the stop outcome did not permit a trusted terminal log capture", correlationID)
			s.logManagedServiceError(work.Instance.ID, "record stop failure", recordErr)
			return
		}
		diagnostic := "stopped by owner"
		if forced {
			diagnostic = "stopped by owner after grace; process group was killed"
		}
		archive, unavailable := s.captureManagedServiceLogArchive(ctx, work, managedServiceHostBinding(work.Instance))
		s.serviceHost.Forget(work.Instance.ID)
		_, err = s.store.StopManagedServiceWithLogs(ctx, work.Instance.ID, 0, diagnostic, archive, unavailable, correlationID)
		s.logManagedServiceError(work.Instance.ID, "record stopped service", err)
		s.cleanupManagedServiceRuntime(work.Instance.ID, err)
	}
}

func (s *server) cleanupManagedServiceRuntime(instanceID string, terminalErr error) {
	if terminalErr != nil {
		return
	}
	if err := s.serviceHost.RemoveRuntime(instanceID); err != nil {
		s.logManagedServiceError(instanceID, "remove terminal node-local runtime", err)
	}
}

func (s *server) captureManagedServiceLogArchive(ctx context.Context, work store.ManagedServiceWork, binding servicehost.Binding) (*domain.ManagedServiceLogArchive, string) {
	stdout, stderr, stdoutOmitted, stderrOmitted, err := s.serviceHost.ReadLogs(work.Instance.ID, binding, work.Definition.OutputByteLimit)
	if err != nil {
		return nil, boundedManagedServiceLogFailure("terminal managed-service logs could not be read safely", err)
	}
	stdoutText := execution.RedactTerminalOutput(string(stdout))
	stderrText := execution.RedactTerminalOutput(string(stderr))
	archive, err := s.store.PrepareManagedServiceLogArchive(ctx, work.Instance.ID, domain.ManagedServiceLogs{
		InstanceID: work.Instance.ID,
		State:      "terminal",
		Stdout:     domain.CapturedLog{Text: stdoutText, CapturedBytes: int64(len(stdoutText)), OmittedBytes: stdoutOmitted, Truncated: stdoutOmitted > 0},
		Stderr:     domain.CapturedLog{Text: stderrText, CapturedBytes: int64(len(stderrText)), OmittedBytes: stderrOmitted, Truncated: stderrOmitted > 0},
	}, work.Definition.OutputByteLimit)
	if err != nil {
		return nil, boundedManagedServiceLogFailure("terminal managed-service logs could not be archived safely", err)
	}
	return &archive, ""
}

func boundedManagedServiceLogFailure(prefix string, err error) string {
	if err == nil {
		err = errors.New("unknown log capture failure")
	}
	message := prefix + ": " + err.Error()
	if len(message) > 4096 {
		message = message[:4096]
	}
	return message
}

func managedServiceHostBinding(instance domain.ManagedServiceInstance) servicehost.Binding {
	return servicehost.Binding{
		PID: instance.RuntimePID, ProcessGroupID: instance.RuntimeProcessGroupID, ProcessStartTicks: instance.RuntimeStartTicks,
		StdoutPath: instance.RuntimeStdoutPath, StderrPath: instance.RuntimeStderrPath,
	}
}

func (s *server) logManagedServiceError(instanceID, action string, err error) {
	if err != nil {
		s.config.Logger.Error("managed-service worker store failure", "component", "managed_service_worker", "instance_id", instanceID, "action", action, "error", err)
	}
}
