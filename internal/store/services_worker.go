package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"crewfold/internal/domain"
)

const managedServiceWorkerActorID = "managed-service-worker"

type ManagedServiceRuntimeBindingInput struct {
	PID               int
	ProcessGroupID    int
	ProcessStartTicks uint64
	StdoutPath        string
	StderrPath        string
}

func (s *Store) RecoverManagedServiceJobLeases(ctx context.Context) error {
	now := s.nowText()
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin managed-service lease recovery", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE managed_service_jobs SET status='pending',lease_expires_at=NULL,updated_at=? WHERE status='leased'`, now); err != nil {
		return storageFailure("recover managed-service leases", err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit managed-service lease recovery", err)
	}
	return nil
}

func (s *Store) ClaimManagedServiceJob(ctx context.Context, lease time.Duration) (ManagedServiceWork, bool, error) {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return ManagedServiceWork{}, false, storageFailure("begin managed-service job claim", err)
	}
	defer tx.Rollback()
	nowTime := s.clock().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE managed_service_jobs SET status='pending',lease_expires_at=NULL,updated_at=? WHERE status='leased' AND julianday(lease_expires_at)<=julianday(?)`, now, now); err != nil {
		return ManagedServiceWork{}, false, storageFailure("recover expired managed-service jobs", err)
	}
	var jobID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM managed_service_jobs WHERE status='pending' AND julianday(available_at)<=julianday(?) ORDER BY available_at,created_at,id LIMIT 1`, now).Scan(&jobID); errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return ManagedServiceWork{}, false, storageFailure("commit empty managed-service claim", err)
		}
		return ManagedServiceWork{}, false, nil
	} else if err != nil {
		return ManagedServiceWork{}, false, storageFailure("select managed-service job", err)
	}
	leaseUntil := nowTime.Add(lease).Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE managed_service_jobs SET status='leased',lease_expires_at=?,attempts=attempts+1,updated_at=? WHERE id=? AND status='pending'`, leaseUntil, now, jobID)
	if err != nil {
		return ManagedServiceWork{}, false, managedServiceConstraint("lease managed-service job", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ManagedServiceWork{}, false, managedServiceError(CodeManagedServiceConflict, "managed-service job was claimed concurrently")
	}
	work, err := managedServiceWorkInTransaction(ctx, tx, jobID)
	if err != nil {
		return ManagedServiceWork{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ManagedServiceWork{}, false, storageFailure("commit managed-service claim", err)
	}
	return work, true, nil
}

func (s *Store) MarkManagedServiceStarting(ctx context.Context, instanceID, correlationID string) (ManagedServiceWork, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return ManagedServiceWork{}, storageFailure("begin managed-service starting", err)
	}
	defer tx.Rollback()
	work, err := managedServiceWorkForInstanceInTransaction(ctx, tx, strings.TrimSpace(instanceID))
	if err != nil {
		return ManagedServiceWork{}, err
	}
	if work.Job.Status != domain.ManagedServiceJobLeased || (work.Job.Action != domain.ManagedServiceJobStart && work.Job.Action != domain.ManagedServiceJobRestart) {
		return ManagedServiceWork{}, managedServiceError(CodeManagedServiceConflict, "managed-service start requires the exact leased start job")
	}
	// Starting is the durable pre-effect checkpoint. A daemon can lose its
	// response after committing that checkpoint, and a transient failure while
	// recording the runtime binding can leave the exact job leased until
	// recovery. Reclaiming that same start/restart job must therefore resume
	// from the checkpoint instead of wedging it as a lifecycle conflict.
	if work.Instance.Status == domain.ManagedServiceStarting &&
		(work.Job.Action == domain.ManagedServiceJobStart || work.Job.Action == domain.ManagedServiceJobRestart) {
		return work, nil
	}
	if work.Job.Action == domain.ManagedServiceJobRestart && work.Instance.Status == domain.ManagedServiceStopping {
		now := s.nowText()
		work.Instance.Status = domain.ManagedServiceStarting
		work.Instance.HealthStatus = domain.ManagedServiceHealthPending
		work.Instance.RestartCount++
		work.Instance.ExitCode = nil
		work.Instance.DiagnosticCode = "service_restart_pending"
		work.Instance.Diagnostic = "restart requested by owner"
		work.Instance.Revision++
		work.Instance.UpdatedAt = now
		if err := updateManagedServiceInstance(ctx, tx, work.Instance); err != nil {
			return ManagedServiceWork{}, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM managed_service_runtime_bindings WHERE instance_id=?`, work.Instance.ID); err != nil {
			return ManagedServiceWork{}, managedServiceConstraint("clear managed-service restart binding", err)
		}
		if _, err := appendEventForActor(ctx, tx, work.Instance.WorkspaceID, "managed_service_instance", work.Instance.ID, work.Instance.Revision, "service.starting", correlationID, now, managedServiceWorkerActorID, domain.EventActorSubsystem, map[string]any{"definition_id": work.Definition.ID, "restart_count": work.Instance.RestartCount}); err != nil {
			return ManagedServiceWork{}, err
		}
		if err := tx.Commit(); err != nil {
			return ManagedServiceWork{}, storageFailure("commit managed-service restart starting", err)
		}
		return managedServiceWorkByJob(ctx, s.db, work.Job.ID)
	}
	if work.Instance.Status != domain.ManagedServiceRequested || work.Job.Action != domain.ManagedServiceJobStart {
		return ManagedServiceWork{}, managedServiceError(CodeManagedServiceConflict, "managed service cannot start from its current state")
	}
	now := s.nowText()
	work.Instance.Status = domain.ManagedServiceStarting
	work.Instance.Revision++
	work.Instance.UpdatedAt = now
	if err := updateManagedServiceInstance(ctx, tx, work.Instance); err != nil {
		return ManagedServiceWork{}, err
	}
	if _, err := appendEventForActor(ctx, tx, work.Instance.WorkspaceID, "managed_service_instance", work.Instance.ID, work.Instance.Revision, "service.starting", correlationID, now, managedServiceWorkerActorID, domain.EventActorSubsystem, map[string]any{"definition_id": work.Definition.ID}); err != nil {
		return ManagedServiceWork{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManagedServiceWork{}, storageFailure("commit managed-service starting", err)
	}
	return managedServiceWorkByJob(ctx, s.db, work.Job.ID)
}

func (s *Store) RecordManagedServiceStarted(ctx context.Context, instanceID string, binding ManagedServiceRuntimeBindingInput, health, diagnostic, correlationID string) (domain.ManagedServiceInstance, error) {
	health, diagnostic = strings.TrimSpace(health), boundedManagedServiceDiagnostic(diagnostic)
	if health != domain.ManagedServiceHealthHealthy && health != domain.ManagedServiceHealthUnhealthy {
		return domain.ManagedServiceInstance{}, managedServiceError(CodeInvalidManagedService, "managed-service start requires a concrete health result")
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.ManagedServiceInstance{}, storageFailure("begin managed-service started", err)
	}
	defer tx.Rollback()
	work, err := managedServiceWorkForInstanceInTransaction(ctx, tx, strings.TrimSpace(instanceID))
	if err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if work.Instance.Status != domain.ManagedServiceStarting || work.Job.Status != domain.ManagedServiceJobLeased {
		return domain.ManagedServiceInstance{}, managedServiceError(CodeManagedServiceConflict, "managed-service binding requires a leased starting operation")
	}
	if binding.PID <= 1 || binding.ProcessGroupID <= 1 || binding.ProcessStartTicks == 0 || !validCheckRelativePath(binding.StdoutPath) || !validCheckRelativePath(binding.StderrPath) {
		return domain.ManagedServiceInstance{}, managedServiceError(CodeInvalidManagedService, "managed-service runtime binding is invalid")
	}
	now := s.nowText()
	generation := work.Instance.RestartCount + 1
	_, err = tx.ExecContext(ctx, `INSERT INTO managed_service_runtime_bindings(instance_id,node_id,node_fingerprint,operation_id,pid,process_group_id,process_start_ticks,stdout_path,stderr_path,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		work.Instance.ID, s.runtimeNodeID, s.runtimeNodeFingerprint, fmt.Sprintf("%s:%d", work.Instance.ID, generation), binding.PID, binding.ProcessGroupID, binding.ProcessStartTicks, binding.StdoutPath, binding.StderrPath, generation, now, now)
	if err != nil {
		return domain.ManagedServiceInstance{}, managedServiceConstraint("record managed-service runtime binding", err)
	}
	work.Instance.Status = domain.ManagedServiceHealthy
	if health == domain.ManagedServiceHealthUnhealthy {
		work.Instance.Status = domain.ManagedServiceDegraded
	}
	work.Instance.HealthStatus = health
	work.Instance.Diagnostic = diagnostic
	work.Instance.DiagnosticCode = ""
	if diagnostic != "" {
		work.Instance.DiagnosticCode = "health_check_failed"
	}
	work.Instance.StartedAt = now
	if health == domain.ManagedServiceHealthHealthy {
		work.Instance.HealthyAt = now
	}
	work.Instance.Revision++
	work.Instance.UpdatedAt = now
	if err := updateManagedServiceInstance(ctx, tx, work.Instance); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if err := completeManagedServiceJob(ctx, tx, work.Job.ID, "", now); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if err := insertManagedServiceJob(ctx, tx, work.Instance.ID, domain.ManagedServiceJobProbe, now); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	eventType := "service.started"
	if health == domain.ManagedServiceHealthUnhealthy {
		eventType = "service.health_changed"
	}
	if _, err := appendEventForActor(ctx, tx, work.Instance.WorkspaceID, "managed_service_instance", work.Instance.ID, work.Instance.Revision, eventType, correlationID, now, managedServiceWorkerActorID, domain.EventActorSubsystem, map[string]any{"health": health, "restart_count": work.Instance.RestartCount, "runtime_bound": true}); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ManagedServiceInstance{}, storageFailure("commit managed-service started", err)
	}
	return queryManagedServiceInstance(ctx, s.db, work.Instance.ID)
}

func (s *Store) ObserveManagedService(ctx context.Context, instanceID, health, diagnostic, correlationID string, delay time.Duration) (domain.ManagedServiceInstance, error) {
	health, diagnostic = strings.TrimSpace(health), boundedManagedServiceDiagnostic(diagnostic)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.ManagedServiceInstance{}, storageFailure("begin managed-service observation", err)
	}
	defer tx.Rollback()
	work, err := managedServiceWorkForInstanceInTransaction(ctx, tx, strings.TrimSpace(instanceID))
	if err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if work.Job.Status != domain.ManagedServiceJobLeased || work.Job.Action != domain.ManagedServiceJobProbe || (work.Instance.Status != domain.ManagedServiceHealthy && work.Instance.Status != domain.ManagedServiceDegraded) {
		return domain.ManagedServiceInstance{}, managedServiceError(CodeManagedServiceConflict, "managed-service observation requires the leased live probe")
	}
	nowTime := s.clock().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	changed := work.Instance.HealthStatus != health
	if changed {
		if health == domain.ManagedServiceHealthHealthy {
			work.Instance.Status = domain.ManagedServiceHealthy
			if work.Instance.HealthyAt == "" {
				work.Instance.HealthyAt = now
			}
		} else {
			work.Instance.Status = domain.ManagedServiceDegraded
		}
		work.Instance.HealthStatus = health
		work.Instance.Diagnostic = diagnostic
		work.Instance.DiagnosticCode = ""
		if diagnostic != "" {
			work.Instance.DiagnosticCode = "health_check_failed"
		}
		work.Instance.Revision++
		work.Instance.UpdatedAt = now
		if err := updateManagedServiceInstance(ctx, tx, work.Instance); err != nil {
			return domain.ManagedServiceInstance{}, err
		}
		if _, err := appendEventForActor(ctx, tx, work.Instance.WorkspaceID, "managed_service_instance", work.Instance.ID, work.Instance.Revision, "service.health_changed", correlationID, now, managedServiceWorkerActorID, domain.EventActorSubsystem, map[string]any{"health": health}); err != nil {
			return domain.ManagedServiceInstance{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE managed_service_jobs SET status='pending',lease_expires_at=NULL,available_at=?,updated_at=? WHERE id=? AND status='leased'`, nowTime.Add(delay).Format(time.RFC3339Nano), now, work.Job.ID); err != nil {
		return domain.ManagedServiceInstance{}, managedServiceConstraint("defer managed-service probe", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.ManagedServiceInstance{}, storageFailure("commit managed-service observation", err)
	}
	return queryManagedServiceInstance(ctx, s.db, work.Instance.ID)
}

func (s *Store) FailManagedService(ctx context.Context, instanceID, code, diagnostic, correlationID string) (domain.ManagedServiceInstance, error) {
	return s.FailManagedServiceWithLogs(ctx, instanceID, code, diagnostic, nil, "managed-service terminal logs were not available to the caller", correlationID)
}

func (s *Store) FailManagedServiceWithLogs(ctx context.Context, instanceID, code, diagnostic string, archive *domain.ManagedServiceLogArchive, logsUnavailableReason, correlationID string) (domain.ManagedServiceInstance, error) {
	return s.finishManagedService(ctx, instanceID, domain.ManagedServiceFailed, nil, code, diagnostic, archive, logsUnavailableReason, correlationID)
}

func (s *Store) LoseManagedService(ctx context.Context, instanceID, diagnostic, correlationID string) (domain.ManagedServiceInstance, error) {
	diagnostic = boundedManagedServiceDiagnostic(diagnostic)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.ManagedServiceInstance{}, storageFailure("begin managed-service unknown transition", err)
	}
	defer tx.Rollback()
	work, err := managedServiceWorkForInstanceInTransaction(ctx, tx, strings.TrimSpace(instanceID))
	if err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if work.Job.Status != domain.ManagedServiceJobLeased || (work.Instance.Status != domain.ManagedServiceStarting && work.Instance.Status != domain.ManagedServiceHealthy && work.Instance.Status != domain.ManagedServiceDegraded && work.Instance.Status != domain.ManagedServiceStopping) {
		return domain.ManagedServiceInstance{}, managedServiceError(CodeManagedServiceConflict, "managed-service runtime loss requires its leased live operation")
	}
	now := s.nowText()
	work.Instance.Status = domain.ManagedServiceUnknown
	work.Instance.HealthStatus = domain.ManagedServiceHealthUnknown
	work.Instance.DiagnosticCode = "service_runtime_unknown"
	work.Instance.Diagnostic = diagnostic
	work.Instance.Revision++
	work.Instance.UpdatedAt = now
	if err := updateManagedServiceInstance(ctx, tx, work.Instance); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if err := completeManagedServiceJob(ctx, tx, work.Job.ID, diagnostic, now); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if _, err := appendEventForActor(ctx, tx, work.Instance.WorkspaceID, "managed_service_instance", work.Instance.ID, work.Instance.Revision, "service.runtime_unknown", correlationID, now, managedServiceWorkerActorID, domain.EventActorSubsystem, map[string]any{"runtime_bound": work.Instance.RuntimeOperationID != ""}); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ManagedServiceInstance{}, storageFailure("commit managed-service unknown transition", err)
	}
	return queryManagedServiceInstance(ctx, s.db, work.Instance.ID)
}

func (s *Store) ScheduleManagedServiceRestart(ctx context.Context, instanceID, diagnostic, correlationID string, delay time.Duration) (domain.ManagedServiceInstance, error) {
	diagnostic = boundedManagedServiceDiagnostic(diagnostic)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.ManagedServiceInstance{}, storageFailure("begin managed-service restart", err)
	}
	defer tx.Rollback()
	work, err := managedServiceWorkForInstanceInTransaction(ctx, tx, strings.TrimSpace(instanceID))
	if err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if work.Job.Status != domain.ManagedServiceJobLeased ||
		(work.Job.Action != domain.ManagedServiceJobProbe && work.Job.Action != domain.ManagedServiceJobStart && work.Job.Action != domain.ManagedServiceJobRestart) ||
		(work.Instance.Status != domain.ManagedServiceHealthy && work.Instance.Status != domain.ManagedServiceDegraded && work.Instance.Status != domain.ManagedServiceStarting) ||
		work.Instance.RestartCount >= work.Definition.MaximumRestarts {
		return domain.ManagedServiceInstance{}, managedServiceError(CodeManagedServiceConflict, "managed service is not eligible for another restart")
	}
	nowTime := s.clock().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	work.Instance.Status = domain.ManagedServiceStarting
	work.Instance.HealthStatus = domain.ManagedServiceHealthPending
	work.Instance.RestartCount++
	work.Instance.ExitCode = nil
	work.Instance.DiagnosticCode = "service_restart_pending"
	work.Instance.Diagnostic = diagnostic
	work.Instance.Revision++
	work.Instance.UpdatedAt = now
	if err := updateManagedServiceInstance(ctx, tx, work.Instance); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM managed_service_runtime_bindings WHERE instance_id=?`, work.Instance.ID); err != nil {
		return domain.ManagedServiceInstance{}, managedServiceConstraint("clear prior managed-service generation", err)
	}
	if err := completeManagedServiceJob(ctx, tx, work.Job.ID, diagnostic, now); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if delay < 0 {
		delay = 0
	}
	if err := insertManagedServiceJob(ctx, tx, work.Instance.ID, domain.ManagedServiceJobRestart, nowTime.Add(delay).Format(time.RFC3339Nano)); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if _, err := appendEventForActor(ctx, tx, work.Instance.WorkspaceID, "managed_service_instance", work.Instance.ID, work.Instance.Revision, "service.restart_requested", correlationID, now, managedServiceWorkerActorID, domain.EventActorSubsystem, map[string]any{"restart_count": work.Instance.RestartCount, "diagnostic": diagnostic}); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ManagedServiceInstance{}, storageFailure("commit managed-service restart", err)
	}
	return queryManagedServiceInstance(ctx, s.db, work.Instance.ID)
}

func (s *Store) StopManagedService(ctx context.Context, instanceID string, exitCode int, diagnostic, correlationID string) (domain.ManagedServiceInstance, error) {
	return s.StopManagedServiceWithLogs(ctx, instanceID, exitCode, diagnostic, nil, "managed-service terminal logs were not available to the caller", correlationID)
}

func (s *Store) StopManagedServiceWithLogs(ctx context.Context, instanceID string, exitCode int, diagnostic string, archive *domain.ManagedServiceLogArchive, logsUnavailableReason, correlationID string) (domain.ManagedServiceInstance, error) {
	return s.finishManagedService(ctx, instanceID, domain.ManagedServiceStopped, &exitCode, "", diagnostic, archive, logsUnavailableReason, correlationID)
}

func (s *Store) finishManagedService(ctx context.Context, instanceID, status string, exitCode *int, code, diagnostic string, archive *domain.ManagedServiceLogArchive, logsUnavailableReason, correlationID string) (domain.ManagedServiceInstance, error) {
	diagnostic, code = boundedManagedServiceDiagnostic(diagnostic), strings.TrimSpace(code)
	logsUnavailableReason = boundedManagedServiceDiagnostic(logsUnavailableReason)
	if archive == nil && logsUnavailableReason == "" {
		return domain.ManagedServiceInstance{}, managedServiceLogsUnavailable("terminal managed-service transition requires an archive or explicit unavailability diagnosis", nil)
	}
	if archive != nil && logsUnavailableReason != "" {
		return domain.ManagedServiceInstance{}, managedServiceLogsUnavailable("managed-service terminal logs cannot be both archived and unavailable", nil)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.ManagedServiceInstance{}, storageFailure("begin managed-service terminal transition", err)
	}
	defer tx.Rollback()
	work, err := managedServiceWorkForInstanceInTransaction(ctx, tx, strings.TrimSpace(instanceID))
	if err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if work.Job.Status != domain.ManagedServiceJobLeased || (work.Instance.Status != domain.ManagedServiceStarting && work.Instance.Status != domain.ManagedServiceHealthy && work.Instance.Status != domain.ManagedServiceDegraded && work.Instance.Status != domain.ManagedServiceStopping) {
		return domain.ManagedServiceInstance{}, managedServiceError(CodeManagedServiceConflict, "managed-service terminal transition requires its leased live operation")
	}
	now := s.nowText()
	work.Instance.Status, work.Instance.DesiredState = status, domain.ManagedServiceDesiredStopped
	work.Instance.HealthStatus, work.Instance.ExitCode = domain.ManagedServiceHealthUnhealthy, exitCode
	work.Instance.DiagnosticCode, work.Instance.Diagnostic = code, diagnostic
	work.Instance.FinishedAt, work.Instance.UpdatedAt = now, now
	work.Instance.Revision++
	if err := updateManagedServiceInstance(ctx, tx, work.Instance); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if archive != nil {
		if err := s.insertManagedServiceLogArchive(ctx, tx, work.Instance.ID, now, work.Definition.OutputByteLimit, archive); err != nil {
			return domain.ManagedServiceInstance{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM managed_service_runtime_bindings WHERE instance_id=?`, work.Instance.ID); err != nil {
		return domain.ManagedServiceInstance{}, managedServiceConstraint("clear managed-service runtime binding", err)
	}
	if err := completeManagedServiceJob(ctx, tx, work.Job.ID, diagnostic, now); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	eventType := "service.failed"
	if status == domain.ManagedServiceStopped {
		eventType = "service.stopped"
	}
	eventData := map[string]any{"exit_code": exitCode, "diagnostic_code": code, "logs_available": archive != nil}
	if archive != nil {
		eventData["stdout_sha256"] = archive.Stdout.ContentSHA256
		eventData["stderr_sha256"] = archive.Stderr.ContentSHA256
	} else {
		eventData["logs_unavailable_reason"] = logsUnavailableReason
	}
	if _, err := appendEventForActor(ctx, tx, work.Instance.WorkspaceID, "managed_service_instance", work.Instance.ID, work.Instance.Revision, eventType, correlationID, now, managedServiceWorkerActorID, domain.EventActorSubsystem, eventData); err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ManagedServiceInstance{}, storageFailure("commit managed-service terminal transition", err)
	}
	return queryManagedServiceInstance(ctx, s.db, work.Instance.ID)
}

func (s *Store) RequestManagedServiceAction(ctx context.Context, command RequestManagedServiceActionCommand) (MutationResult[domain.ManagedServiceInstance], error) {
	command.WorkspaceIdentifier, command.InstanceID, command.Action = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.InstanceID), strings.TrimSpace(command.Action)
	command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if (command.Action != domain.ManagedServiceJobStop && command.Action != domain.ManagedServiceJobRestart) || command.ExpectedRevision < 1 {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceError(CodeInvalidManagedService, "managed-service action requires an exact stop or restart request")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	operation := "service." + command.Action
	requestHash, _ := checkSemanticHash(operation, command)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("begin managed-service stop", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.ManagedServiceInstance]
	key := managedServiceOwnerIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, key, operation, requestHash, &replay); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	instance, err := queryManagedServiceInstance(ctx, tx, command.InstanceID)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if instance.WorkspaceID != workspace.ID || instance.Revision != command.ExpectedRevision {
		return MutationResult[domain.ManagedServiceInstance]{}, revisionConflict("managed service", instance.ID, command.ExpectedRevision, instance.Revision)
	}
	result, err := s.requestManagedServiceActionInTx(ctx, tx, instance, command.Action, localOwnerActorID, domain.EventActorHuman, command.CorrelationID, s.nowText())
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if err := recordIdempotency(ctx, tx, key, operation, requestHash, result, result.Value.UpdatedAt); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("commit managed-service stop", err)
	}
	return result, nil
}

// ResolveManagedServiceUnknown records the owner's explicit attestation that an
// unobservable process has ended. Crewfold does not contact or kill the old
// process: it retires the node-local binding and closes the canonical instance
// as failed so a later start creates a fresh, independently bound instance.
func (s *Store) ResolveManagedServiceUnknown(ctx context.Context, command ResolveManagedServiceUnknownCommand) (MutationResult[domain.ManagedServiceInstance], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.InstanceID = strings.TrimSpace(command.InstanceID)
	command.Reason = strings.TrimSpace(command.Reason)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.InstanceID == "" || command.ExpectedRevision < 1 || !command.RuntimeRetiredConfirmed || !validManagedServiceText(command.Reason, 2048) {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceError(CodeInvalidManagedService, "unknown managed-service resolution requires the exact revision, explicit runtime-retired confirmation, and a bounded reason")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	requestHash, _ := checkSemanticHash("service.resolve_unknown", command)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("begin managed-service unknown resolution", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.ManagedServiceInstance]
	key := managedServiceOwnerIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, key, "service.resolve_unknown", requestHash, &replay); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	instance, err := queryManagedServiceInstance(ctx, tx, command.InstanceID)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if instance.WorkspaceID != workspace.ID || instance.Revision != command.ExpectedRevision {
		return MutationResult[domain.ManagedServiceInstance]{}, revisionConflict("managed service", instance.ID, command.ExpectedRevision, instance.Revision)
	}
	if instance.Status != domain.ManagedServiceUnknown {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceError(CodeManagedServiceConflict, "only an unknown managed-service runtime can be owner-resolved")
	}
	now := s.nowText()
	instance.Status = domain.ManagedServiceFailed
	instance.DesiredState = domain.ManagedServiceDesiredStopped
	instance.HealthStatus = domain.ManagedServiceHealthUnknown
	instance.DiagnosticCode = "service_runtime_owner_resolved"
	instance.Diagnostic = boundedManagedServiceDiagnostic(command.Reason)
	instance.FinishedAt = now
	instance.Revision++
	instance.UpdatedAt = now
	if err := updateManagedServiceInstance(ctx, tx, instance); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM managed_service_runtime_bindings WHERE instance_id=?`, instance.ID); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceConstraint("retire managed-service runtime binding", err)
	}
	instance.RuntimeNodeID = ""
	instance.RuntimeNodeFingerprint = ""
	instance.RuntimeOperationID = ""
	instance.RuntimePID = 0
	instance.RuntimeProcessGroupID = 0
	instance.RuntimeStartTicks = 0
	instance.RuntimeStdoutPath = ""
	instance.RuntimeStderrPath = ""
	if _, err := tx.ExecContext(ctx, `UPDATE managed_service_jobs SET status='complete',lease_expires_at=NULL,diagnostic=?,updated_at=? WHERE instance_id=? AND status IN ('pending','leased')`, command.Reason, now, instance.ID); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceConstraint("close managed-service jobs after owner resolution", err)
	}
	sequence, err := appendEvent(ctx, tx, instance.WorkspaceID, "managed_service_instance", instance.ID, instance.Revision, "service.runtime_resolved", command.CorrelationID, now, map[string]any{
		"runtime_retired_confirmed": true,
		"reason":                    command.Reason,
	})
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	result := MutationResult[domain.ManagedServiceInstance]{Value: instance, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "service.resolve_unknown", requestHash, result, now); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("commit managed-service unknown resolution", err)
	}
	return result, nil
}

func (s *Store) requestManagedServiceActionInTx(ctx context.Context, tx *sql.Tx, instance domain.ManagedServiceInstance, action, actorID, actorType, correlationID, now string) (MutationResult[domain.ManagedServiceInstance], error) {
	if instance.Status != domain.ManagedServiceHealthy && instance.Status != domain.ManagedServiceDegraded {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceError(CodeManagedServiceConflict, "only a live managed service can be stopped")
	}
	// A public health observation makes the instance visible before the probe
	// worker returns its lease. Owner stop/restart must be able to supersede that
	// exact read-only probe without racing the one-open-job constraint. A worker
	// already holding the probe may finish its OS inspection, but its subsequent
	// Store transition will fail closed because this lease is no longer open.
	if _, err := tx.ExecContext(ctx, `UPDATE managed_service_jobs SET status='complete',lease_expires_at=NULL,updated_at=? WHERE instance_id=? AND action='probe' AND status IN ('pending','leased')`, now, instance.ID); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceConstraint("supersede managed-service probe", err)
	}
	instance.Status = domain.ManagedServiceStopping
	instance.DesiredState = domain.ManagedServiceDesiredStopped
	if action == domain.ManagedServiceJobRestart {
		var maximumRestarts int
		if err := tx.QueryRowContext(ctx, `SELECT maximum_restarts FROM managed_service_definitions WHERE id=?`, instance.DefinitionID).Scan(&maximumRestarts); err != nil {
			return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("read managed-service restart limit", err)
		}
		if instance.RestartCount >= maximumRestarts {
			return MutationResult[domain.ManagedServiceInstance]{}, managedServiceError(CodeManagedServiceConflict, "managed service exhausted its restart limit")
		}
		instance.DesiredState = domain.ManagedServiceDesiredRunning
	}
	instance.Revision++
	instance.UpdatedAt = now
	if err := updateManagedServiceInstance(ctx, tx, instance); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if err := insertManagedServiceJob(ctx, tx, instance.ID, action, now); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	eventType := managedServiceStopRequestedEvent
	if action == domain.ManagedServiceJobRestart {
		eventType = "service.restart_requested"
	}
	var sequence int64
	var err error
	if actorType == domain.EventActorHuman {
		sequence, err = appendEvent(ctx, tx, instance.WorkspaceID, "managed_service_instance", instance.ID, instance.Revision, eventType, correlationID, now, map[string]any{"definition_id": instance.DefinitionID})
	} else {
		sequence, err = appendEventForActor(ctx, tx, instance.WorkspaceID, "managed_service_instance", instance.ID, instance.Revision, eventType, correlationID, now, actorID, actorType, map[string]any{"definition_id": instance.DefinitionID})
	}
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	return MutationResult[domain.ManagedServiceInstance]{Value: instance, EventSequence: sequence}, nil
}

func (s *Store) ManagedServiceInstance(ctx context.Context, workspaceIdentifier, instanceID string) (domain.ManagedServiceInstance, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	instance, err := queryManagedServiceInstance(ctx, s.db, strings.TrimSpace(instanceID))
	if err != nil {
		return domain.ManagedServiceInstance{}, err
	}
	if instance.WorkspaceID != workspace.ID {
		return domain.ManagedServiceInstance{}, managedServiceError(CodeManagedServiceNotFound, "managed service was not found")
	}
	return instance, nil
}

func (s *Store) ManagedServiceInstances(ctx context.Context, query ListManagedServiceInstancesQuery) ([]domain.ManagedServiceInstance, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return nil, err
	}
	projectID := ""
	if strings.TrimSpace(query.ProjectIdentifier) != "" {
		project, err := queryProject(ctx, s.db, workspace.ID, strings.TrimSpace(query.ProjectIdentifier))
		if err != nil {
			return nil, err
		}
		projectID = project.ID
	}
	workstreamID, checkoutID, status := strings.TrimSpace(query.WorkstreamID), strings.TrimSpace(query.CheckoutID), strings.TrimSpace(query.Status)
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM managed_service_instances WHERE workspace_id=? AND (?='' OR project_id=?) AND (?='' OR workstream_id=?) AND (?='' OR checkout_id=?) AND (?='' OR status=?) ORDER BY updated_at DESC,id DESC LIMIT ?`,
		workspace.ID, projectID, projectID, workstreamID, workstreamID, checkoutID, checkoutID, status, status, boundedManagedServiceLimit(query.Limit))
	if err != nil {
		return nil, storageFailure("list managed-service instances", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, storageFailure("scan managed-service instance id", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("list managed-service instance ids", err)
	}
	instances := make([]domain.ManagedServiceInstance, 0, len(ids))
	for _, id := range ids {
		instance, err := queryManagedServiceInstance(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		instances = append(instances, instance)
	}
	return instances, nil
}

func (s *Store) ManagedServiceDetail(ctx context.Context, workspaceIdentifier, instanceID string) (domain.ManagedServiceDetail, error) {
	instance, err := s.ManagedServiceInstance(ctx, workspaceIdentifier, instanceID)
	if err != nil {
		return domain.ManagedServiceDetail{}, err
	}
	return managedServiceDetailFromDatabase(ctx, s.db, instance)
}

func managedServiceDetailFromDatabase(ctx context.Context, database messageQueryContext, instance domain.ManagedServiceInstance) (domain.ManagedServiceDetail, error) {
	definition, err := queryManagedServiceDefinition(ctx, database, instance.WorkspaceID, instance.DefinitionID)
	if err != nil {
		return domain.ManagedServiceDetail{}, err
	}
	jobs := []domain.ManagedServiceJob{}
	rows, err := database.QueryContext(ctx, `SELECT id,instance_id,action,status,available_at,COALESCE(lease_expires_at,''),attempts,COALESCE(diagnostic,''),created_at,updated_at FROM managed_service_jobs WHERE instance_id=? ORDER BY created_at,id`, instance.ID)
	if err != nil {
		return domain.ManagedServiceDetail{}, storageFailure("list managed-service jobs", err)
	}
	for rows.Next() {
		var job domain.ManagedServiceJob
		if err := rows.Scan(&job.ID, &job.InstanceID, &job.Action, &job.Status, &job.AvailableAt, &job.LeaseExpiresAt, &job.Attempts, &job.Diagnostic, &job.CreatedAt, &job.UpdatedAt); err != nil {
			rows.Close()
			return domain.ManagedServiceDetail{}, storageFailure("scan managed-service job", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Close(); err != nil {
		return domain.ManagedServiceDetail{}, storageFailure("close managed-service jobs", err)
	}
	logs := []domain.ManagedServiceLogArtifact{}
	logRows, err := database.QueryContext(ctx, `SELECT id,instance_id,kind,content_sha256,captured_bytes,omitted_bytes,truncated,created_at FROM managed_service_log_artifacts WHERE instance_id=? ORDER BY kind`, instance.ID)
	if err != nil {
		return domain.ManagedServiceDetail{}, storageFailure("list managed-service log artifacts", err)
	}
	for logRows.Next() {
		var log domain.ManagedServiceLogArtifact
		var truncated int
		if err := logRows.Scan(&log.ID, &log.InstanceID, &log.Kind, &log.ContentSHA256, &log.CapturedBytes, &log.OmittedBytes, &truncated, &log.CreatedAt); err != nil {
			logRows.Close()
			return domain.ManagedServiceDetail{}, storageFailure("scan managed-service log artifact", err)
		}
		log.Truncated = truncated != 0
		logs = append(logs, log)
	}
	if err := logRows.Close(); err != nil {
		return domain.ManagedServiceDetail{}, storageFailure("close managed-service log artifacts", err)
	}
	return domain.ManagedServiceDetail{Definition: definition, Instance: instance, Jobs: jobs, Logs: logs}, nil
}

func (s *Store) ManagedServiceBindingIsCurrent(instance domain.ManagedServiceInstance) bool {
	return instance.RuntimeNodeID != "" && instance.RuntimeNodeID == s.runtimeNodeID &&
		instance.RuntimeNodeFingerprint == s.runtimeNodeFingerprint &&
		instance.RuntimeOperationID == fmt.Sprintf("%s:%d", instance.ID, instance.RestartCount+1)
}

func managedServiceWorkForInstanceInTransaction(ctx context.Context, tx *sql.Tx, instanceID string) (ManagedServiceWork, error) {
	var jobID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM managed_service_jobs WHERE instance_id=? AND status IN ('pending','leased') ORDER BY created_at DESC,id DESC LIMIT 1`, instanceID).Scan(&jobID); err != nil {
		return ManagedServiceWork{}, managedServiceError(CodeManagedServiceConflict, "managed service has no actionable job")
	}
	return managedServiceWorkInTransaction(ctx, tx, jobID)
}

func managedServiceWorkInTransaction(ctx context.Context, tx *sql.Tx, jobID string) (ManagedServiceWork, error) {
	return managedServiceWorkByJob(ctx, tx, jobID)
}

func managedServiceWorkByJob(ctx context.Context, database queryRower, jobID string) (ManagedServiceWork, error) {
	var work ManagedServiceWork
	var instanceID string
	if err := database.QueryRowContext(ctx, `SELECT job.id,job.instance_id,job.action,job.status,job.available_at,COALESCE(job.lease_expires_at,''),job.attempts,COALESCE(job.diagnostic,''),job.created_at,job.updated_at,checkout.path FROM managed_service_jobs job JOIN managed_service_instances instance ON instance.id=job.instance_id JOIN checkouts checkout ON checkout.id=instance.checkout_id WHERE job.id=?`, jobID).Scan(
		&work.Job.ID, &instanceID, &work.Job.Action, &work.Job.Status, &work.Job.AvailableAt, &work.Job.LeaseExpiresAt, &work.Job.Attempts, &work.Job.Diagnostic, &work.Job.CreatedAt, &work.Job.UpdatedAt, &work.CheckoutPath); err != nil {
		return ManagedServiceWork{}, storageFailure("query managed-service work", err)
	}
	work.Job.InstanceID = instanceID
	var err error
	work.Instance, err = queryManagedServiceInstance(ctx, database, instanceID)
	if err != nil {
		return ManagedServiceWork{}, err
	}
	work.Definition, err = queryManagedServiceDefinition(ctx, database, work.Instance.WorkspaceID, work.Instance.DefinitionID)
	return work, err
}

func queryManagedServiceInstance(ctx context.Context, database queryRower, instanceID string) (domain.ManagedServiceInstance, error) {
	var instance domain.ManagedServiceInstance
	var exitCode sql.NullInt64
	err := database.QueryRowContext(ctx, `
SELECT instance.id,instance.workspace_id,instance.project_id,COALESCE(instance.workstream_id,''),instance.checkout_id,
 instance.definition_id,instance.definition_revision,instance.definition_content_sha256,instance.source_type,instance.source_actor_id,
 COALESCE(instance.source_agent_id,''),COALESCE(instance.source_agent_revision,0),COALESCE(instance.source_thread_id,''),COALESCE(instance.source_request_id,''),COALESCE(instance.source_grant_id,''),COALESCE(instance.source_grant_revision,0),
 instance.status,instance.desired_state,instance.health_status,instance.restart_count,instance.exit_code,COALESCE(instance.diagnostic_code,''),COALESCE(instance.diagnostic,''),
 instance.revision,instance.created_at,instance.updated_at,COALESCE(instance.started_at,''),COALESCE(instance.healthy_at,''),COALESCE(instance.finished_at,''),
 COALESCE(binding.node_id,''),COALESCE(binding.node_fingerprint,''),COALESCE(binding.operation_id,''),COALESCE(binding.pid,0),COALESCE(binding.process_group_id,0),COALESCE(binding.process_start_ticks,0),COALESCE(binding.stdout_path,''),COALESCE(binding.stderr_path,'')
FROM managed_service_instances instance LEFT JOIN managed_service_runtime_bindings binding ON binding.instance_id=instance.id WHERE instance.id=?`, instanceID).Scan(
		&instance.ID, &instance.WorkspaceID, &instance.ProjectID, &instance.WorkstreamID, &instance.CheckoutID,
		&instance.DefinitionID, &instance.DefinitionRevision, &instance.DefinitionContentSHA256, &instance.Source.Type, &instance.Source.ActorID,
		&instance.Source.AgentID, &instance.Source.AgentRevision, &instance.Source.ThreadID, &instance.Source.RequestID, &instance.Source.GrantID, &instance.Source.GrantRevision,
		&instance.Status, &instance.DesiredState, &instance.HealthStatus, &instance.RestartCount, &exitCode, &instance.DiagnosticCode, &instance.Diagnostic,
		&instance.Revision, &instance.CreatedAt, &instance.UpdatedAt, &instance.StartedAt, &instance.HealthyAt, &instance.FinishedAt,
		&instance.RuntimeNodeID, &instance.RuntimeNodeFingerprint, &instance.RuntimeOperationID, &instance.RuntimePID, &instance.RuntimeProcessGroupID, &instance.RuntimeStartTicks, &instance.RuntimeStdoutPath, &instance.RuntimeStderrPath)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ManagedServiceInstance{}, managedServiceError(CodeManagedServiceNotFound, "managed service was not found")
	}
	if err != nil {
		return domain.ManagedServiceInstance{}, storageFailure("query managed-service instance", err)
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		instance.ExitCode = &value
	}
	return instance, nil
}

func updateManagedServiceInstance(ctx context.Context, tx *sql.Tx, instance domain.ManagedServiceInstance) error {
	_, err := tx.ExecContext(ctx, `UPDATE managed_service_instances SET status=?,desired_state=?,health_status=?,restart_count=?,exit_code=?,diagnostic_code=NULLIF(?,''),diagnostic=NULLIF(?,''),revision=?,updated_at=?,started_at=NULLIF(?,''),healthy_at=NULLIF(?,''),finished_at=NULLIF(?,''),updated_by=? WHERE id=?`,
		instance.Status, instance.DesiredState, instance.HealthStatus, instance.RestartCount, instance.ExitCode, instance.DiagnosticCode, instance.Diagnostic, instance.Revision, instance.UpdatedAt, instance.StartedAt, instance.HealthyAt, instance.FinishedAt, managedServiceWorkerActorID, instance.ID)
	if err != nil {
		return managedServiceConstraint("update managed-service instance", err)
	}
	return nil
}

func completeManagedServiceJob(ctx context.Context, tx *sql.Tx, jobID, diagnostic, now string) error {
	_, err := tx.ExecContext(ctx, `UPDATE managed_service_jobs SET status='complete',lease_expires_at=NULL,diagnostic=NULLIF(?,''),updated_at=? WHERE id=? AND status='leased'`, boundedManagedServiceDiagnostic(diagnostic), now, jobID)
	if err != nil {
		return managedServiceConstraint("complete managed-service job", err)
	}
	return nil
}

func insertManagedServiceJob(ctx context.Context, tx *sql.Tx, instanceID, action, now string) error {
	id, err := randomID("svcjob_")
	if err != nil {
		return storageFailure("generate managed-service job id", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO managed_service_jobs(id,instance_id,action,status,available_at,lease_expires_at,attempts,diagnostic,created_at,updated_at) VALUES(?,?,?,'pending',?,NULL,0,NULL,?,?)`, id, instanceID, action, now, now, now)
	if err != nil {
		return managedServiceConstraint("insert managed-service job", err)
	}
	return nil
}

func boundedManagedServiceDiagnostic(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	if len(value) > 4096 {
		value = value[:4096]
	}
	return value
}
