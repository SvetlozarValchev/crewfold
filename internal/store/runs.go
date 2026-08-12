package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"crewfold/internal/domain"
)

const (
	runRequestedEvent          = "run.requested"
	runStartingEvent           = "run.starting"
	runStartedEvent            = "run.started"
	runRuntimeObservedEvent    = "run.runtime_observed"
	runProgressEvent           = "run.progress_reported"
	runBlockedEvent            = "run.blocked"
	runCompletionProposedEvent = "run.completion_proposed"
	runCompletedEvent          = "run.completed"
	runStartFailedEvent        = "run.start_failed"
	runFailedEvent             = "run.failed"
	runResumedEvent            = "run.resumed"
	runStopRequestedEvent      = "run.stop_requested"
	runStoppedEvent            = "run.stopped"
	runLostEvent               = "run.lost"
	taskCompletionProposed     = "task.completion_proposed"
	taskChangesRequestedEvent  = "task.changes_requested"
	taskCompletedEvent         = "task.completed"
	taskFailedEvent            = "task.failed"
	taskHandoffRecorded        = "task.handoff_recorded"
	taskRunStoppedEvent        = "task.run_stopped"
	defaultRunCapabilityTTL    = time.Hour
	maximumRunCapabilityTTL    = 24 * time.Hour
)

func (s *Store) CreateRun(ctx context.Context, command CreateRunCommand) (RunMutationResult, error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	taskID := strings.TrimSpace(command.TaskID)
	checkoutIdentifier := strings.TrimSpace(command.CheckoutIdentifier)
	runtimeName := strings.TrimSpace(command.Runtime)
	providerName := strings.TrimSpace(command.Provider)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	contextPacketID := strings.TrimSpace(command.ContextPacketID)
	capabilityTTL := command.CapabilityTTL
	if capabilityTTL == 0 {
		capabilityTTL = defaultRunCapabilityTTL
	}
	if workspaceIdentifier == "" || taskID == "" || runtimeName == "" || providerName == "" || command.ExpectedTaskRevision < 1 || !validStoredScenario(command.Scenario) {
		return RunMutationResult{}, &Error{Code: CodeInvalidRun, Message: "run start requires workspace, task, runtime, provider, a valid scenario, and expected task revision"}
	}
	if capabilityTTL < time.Second || capabilityTTL > maximumRunCapabilityTTL {
		return RunMutationResult{}, &Error{Code: CodeInvalidRun, Message: "run capability lifetime must be between one second and 24 hours"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidRun); err != nil {
		return RunMutationResult{}, err
	}
	command.WorkspaceIdentifier = workspaceIdentifier
	command.TaskID = taskID
	command.CheckoutIdentifier = checkoutIdentifier
	command.ContextPacketID = contextPacketID
	command.Runtime = runtimeName
	command.Provider = providerName
	command.CapabilityTTL = capabilityTTL
	command.IdempotencyKey = key
	command.CorrelationID = correlationID
	requestHash, err := hashCommand("run.start", command)
	if err != nil {
		return RunMutationResult{}, storageFailure("hash run start", err)
	}
	if _, err := s.ReconcileExpiredAssignments(ctx, workspaceIdentifier, correlationID+"-lease"); err != nil {
		return RunMutationResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunMutationResult{}, storageFailure("begin run start", err)
	}
	defer tx.Rollback()
	var replay RunMutationResult
	if found, err := lookupIdempotency(ctx, tx, key, "run.start", requestHash, &replay); err != nil {
		return RunMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return RunMutationResult{}, err
	}
	task, err := queryTask(ctx, tx, workspace.ID, taskID)
	if err != nil {
		return RunMutationResult{}, err
	}
	if task.Revision != command.ExpectedTaskRevision {
		return RunMutationResult{}, revisionConflict("task", task.ID, command.ExpectedTaskRevision, task.Revision)
	}
	if task.Status != domain.TaskAssigned || task.AssignmentID == "" {
		return RunMutationResult{}, &Error{Code: CodeRunConflict, Message: "run start requires a task with an active assignment"}
	}
	var existingRunID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM runs WHERE task_id = ? AND status IN ('requested', 'starting', 'active', 'blocked', 'stopping', 'lost') ORDER BY created_at, id LIMIT 1", task.ID).Scan(&existingRunID)
	if err == nil {
		return RunMutationResult{}, &Error{Code: CodeRunConflict, Message: fmt.Sprintf("task %s already has live run %s", task.ID, existingRunID)}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RunMutationResult{}, storageFailure("check existing live task run", err)
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, task.AssignedAgentID)
	if err != nil {
		return RunMutationResult{}, err
	}
	if !agent.Enabled {
		return RunMutationResult{}, &Error{Code: CodeRunConflict, Message: "disabled agent cannot start a run"}
	}
	if agent.Runtime != runtimeName || agent.Provider != providerName {
		return RunMutationResult{}, &Error{Code: CodeAdapterUnavailable, Message: fmt.Sprintf("agent %s is configured for runtime %s and provider %s", agent.ID, agent.Runtime, agent.Provider)}
	}
	var activeForAgent int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE agent_id = ? AND status IN ('requested', 'starting', 'active', 'blocked', 'stopping', 'lost')", agent.ID).Scan(&activeForAgent); err != nil {
		return RunMutationResult{}, storageFailure("count active agent runs", err)
	}
	if activeForAgent >= agent.MaxConcurrency {
		return RunMutationResult{}, &Error{Code: CodePlacementUnavailable, Message: fmt.Sprintf("agent %s has reached concurrency %d", agent.ID, agent.MaxConcurrency)}
	}
	var packet domain.ContextPacket
	if contextPacketID != "" {
		packet, err = queryContextPacket(ctx, tx, workspace.ID, contextPacketID)
		if err != nil {
			return RunMutationResult{}, err
		}
		if packet.ProjectID != task.ProjectID || packet.TaskID != task.ID || packet.AgentID != agent.ID || packet.Task.Revision != task.Revision || packet.Role.Revision != agent.Revision {
			return RunMutationResult{}, &Error{Code: CodeInvalidContext, Message: "context packet no longer matches the task assignment or its revisions"}
		}
		if checkoutIdentifier != "" && checkoutIdentifier != packet.CheckoutID {
			return RunMutationResult{}, &Error{Code: CodeInvalidContext, Message: "requested checkout differs from the context packet checkout"}
		}
		checkoutIdentifier = packet.CheckoutID
	}
	checkout, err := selectRunCheckout(ctx, tx, task.ProjectID, checkoutIdentifier)
	if err != nil {
		return RunMutationResult{}, err
	}
	if contextPacketID != "" {
		if packet.CheckoutID != checkout.ID || packet.Checkout.Revision != checkout.Revision {
			return RunMutationResult{}, &Error{Code: CodeInvalidContext, Message: "context packet checkout is stale or differs from placement"}
		}
		var boundRunID string
		err = tx.QueryRowContext(ctx, "SELECT run_id FROM run_context_bindings WHERE context_packet_id = ?", packet.ID).Scan(&boundRunID)
		if err == nil {
			return RunMutationResult{}, &Error{Code: CodeInvalidContext, Message: fmt.Sprintf("context packet is already bound to run %s", boundRunID)}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return RunMutationResult{}, storageFailure("check context packet binding", err)
		}
	} else {
		packet, _, err = s.buildContextPacketInTransaction(ctx, tx, workspace.ID, task, agent, checkout, correlationID+"-context", s.nowText())
		if err != nil {
			return RunMutationResult{}, err
		}
		contextPacketID = packet.ID
	}
	reasons := []string{
		fmt.Sprintf("task %s is assigned to enabled agent %s", task.ID, agent.ID),
		fmt.Sprintf("agent runtime/provider match %s/%s", runtimeName, providerName),
		fmt.Sprintf("checkout %s is available with %s write policy", checkout.ID, checkout.WriteMode),
		fmt.Sprintf("agent concurrency %d/%d before placement", activeForAgent, agent.MaxConcurrency),
		fmt.Sprintf("context packet %s fixes task, role, checkout, and reporting policy", contextPacketID),
	}
	scenarioJSON, err := json.Marshal(command.Scenario)
	if err != nil {
		return RunMutationResult{}, storageFailure("encode run scenario", err)
	}
	reasonsJSON, err := json.Marshal(reasons)
	if err != nil {
		return RunMutationResult{}, storageFailure("encode placement reasons", err)
	}
	now := s.nowText()
	runID, err := randomID("run_")
	if err != nil {
		return RunMutationResult{}, storageFailure("generate run id", err)
	}
	run := domain.Run{
		ID: runID, WorkspaceID: workspace.ID, ProjectID: task.ProjectID, TaskID: task.ID,
		AgentID: agent.ID, CheckoutID: checkout.ID, ContextPacketID: contextPacketID, Runtime: runtimeName, Provider: providerName,
		ScenarioName: command.Scenario.Name, Status: domain.RunRequested, Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
		Placement: domain.RunPlacement{TaskID: task.ID, AgentID: agent.ID, CheckoutID: checkout.ID, CheckoutPath: checkout.Path, WriteMode: checkout.WriteMode, Runtime: runtimeName, Provider: providerName, Reasons: reasons},
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO runs(id, workspace_id, project_id, task_id, agent_id, checkout_id, runtime, provider,
    scenario_name, scenario_json, placement_reasons_json, status, step_cursor, revision,
    created_at, updated_at, created_by, updated_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 1, ?, ?, ?, ?)`,
		run.ID, run.WorkspaceID, run.ProjectID, run.TaskID, run.AgentID, run.CheckoutID,
		run.Runtime, run.Provider, run.ScenarioName, string(scenarioJSON), string(reasonsJSON),
		run.Status, now, now, run.CreatedBy, run.UpdatedBy); err != nil {
		return RunMutationResult{}, storageFailure("insert run projection", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_context_bindings(run_id, context_packet_id, bound_at) VALUES (?, ?, ?)", run.ID, contextPacketID, now); err != nil {
		return RunMutationResult{}, storageFailure("bind run context packet", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_capabilities(run_id, expires_at, created_at) VALUES (?, ?, ?)", run.ID, s.clock().UTC().Add(capabilityTTL).Format(time.RFC3339Nano), now); err != nil {
		return RunMutationResult{}, storageFailure("create run capability", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_jobs(run_id, status, available_at, attempts, created_at, updated_at) VALUES (?, 'pending', ?, 0, ?, ?)", run.ID, now, now, now); err != nil {
		return RunMutationResult{}, storageFailure("enqueue run", err)
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runRequestedEvent, "run accepted for asynchronous execution", nil, now); err != nil {
		return RunMutationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return RunMutationResult{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "run", run.ID, run.Revision, runRequestedEvent, correlationID, now, map[string]any{"placement": run.Placement, "scenario": run.ScenarioName})
	if err != nil {
		return RunMutationResult{}, err
	}
	detail, err := runDetailInTransaction(ctx, tx, run)
	if err != nil {
		return RunMutationResult{}, err
	}
	result := RunMutationResult{Detail: detail, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return RunMutationResult{}, err
	}
	if err := recordIdempotency(ctx, tx, key, "run.start", requestHash, result, now); err != nil {
		return RunMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunMutationResult{}, storageFailure("commit run start", err)
	}
	return result, nil
}

func (s *Store) RunDetail(ctx context.Context, workspaceIdentifier, runID string) (domain.RunDetail, error) {
	workspace, err := s.Workspace(ctx, workspaceIdentifier)
	if err != nil {
		return domain.RunDetail{}, err
	}
	run, err := queryRun(ctx, s.db, workspace.ID, strings.TrimSpace(runID))
	if err != nil {
		return domain.RunDetail{}, err
	}
	return runDetail(ctx, s.db, run)
}

// RunByID is an internal control-plane lookup for already-authorized durable
// jobs such as mailbox wake delivery. Run IDs are globally unique.
func (s *Store) RunByID(ctx context.Context, runID string) (domain.Run, error) {
	var run domain.Run
	row := s.db.QueryRowContext(ctx, runSelect+" WHERE r.id = ?", strings.TrimSpace(runID))
	if err := scanRun(row, &run); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Run{}, &Error{Code: CodeRunNotFound, Message: "run not found"}
		}
		return domain.Run{}, storageFailure("query run by ID", err)
	}
	return run, nil
}

func (s *Store) Runs(ctx context.Context, workspaceIdentifier, taskID, status string) ([]domain.RunDetail, error) {
	status = strings.TrimSpace(status)
	if status != "" && !validRunStatus(status) {
		return nil, &Error{Code: CodeInvalidRun, Message: fmt.Sprintf("unsupported run status %q", status)}
	}
	workspace, err := s.Workspace(ctx, workspaceIdentifier)
	if err != nil {
		return nil, err
	}
	arguments := []any{workspace.ID}
	query := runSelect + " WHERE r.workspace_id = ?"
	if strings.TrimSpace(taskID) != "" {
		query += " AND r.task_id = ?"
		arguments = append(arguments, strings.TrimSpace(taskID))
	}
	if status != "" {
		query += " AND r.status = ?"
		arguments = append(arguments, status)
	}
	query += " ORDER BY r.created_at, r.id"
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, storageFailure("list runs", err)
	}
	var runs []domain.Run
	for rows.Next() {
		var run domain.Run
		if err := scanRun(rows, &run); err != nil {
			_ = rows.Close()
			return nil, storageFailure("scan run", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Close(); err != nil {
		return nil, storageFailure("close run list", err)
	}
	result := make([]domain.RunDetail, 0, len(runs))
	for _, run := range runs {
		detail, err := runDetail(ctx, s.db, run)
		if err != nil {
			return nil, err
		}
		result = append(result, detail)
	}
	return result, nil
}

func (s *Store) TaskTimeline(ctx context.Context, workspaceIdentifier, taskID string) (domain.TaskTimeline, error) {
	workspace, err := s.Workspace(ctx, workspaceIdentifier)
	if err != nil {
		return domain.TaskTimeline{}, err
	}
	task, err := queryTask(ctx, s.db, workspace.ID, strings.TrimSpace(taskID))
	if err != nil {
		return domain.TaskTimeline{}, err
	}
	runs, err := queryRunsForTask(ctx, s.db, task.ID)
	if err != nil {
		return domain.TaskTimeline{}, err
	}
	entries, err := queryTaskRunTimeline(ctx, s.db, task.ID)
	if err != nil {
		return domain.TaskTimeline{}, err
	}
	return domain.TaskTimeline{Task: task, Runs: runs, Entries: entries}, nil
}

func (s *Store) ClaimRunJob(ctx context.Context, lease time.Duration) (RunWork, bool, error) {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunWork{}, false, storageFailure("begin run job claim", err)
	}
	defer tx.Rollback()
	nowTime := s.clock().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, "UPDATE run_jobs SET status = 'pending', lease_expires_at = NULL, updated_at = ? WHERE status = 'leased' AND julianday(lease_expires_at) <= julianday(?)", now, now); err != nil {
		return RunWork{}, false, storageFailure("recover expired run jobs", err)
	}
	var runID string
	err = tx.QueryRowContext(ctx, "SELECT run_id FROM run_jobs WHERE status = 'pending' AND julianday(available_at) <= julianday(?) ORDER BY available_at, run_id LIMIT 1", now).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return RunWork{}, false, storageFailure("commit empty run job claim", err)
		}
		return RunWork{}, false, nil
	}
	if err != nil {
		return RunWork{}, false, storageFailure("select run job", err)
	}
	leaseExpires := nowTime.Add(lease).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, "UPDATE run_jobs SET status = 'leased', lease_expires_at = ?, attempts = attempts + 1, updated_at = ? WHERE run_id = ? AND status = 'pending'", leaseExpires, now, runID); err != nil {
		return RunWork{}, false, storageFailure("claim run job", err)
	}
	run, err := queryRun(ctx, tx, "", runID)
	if err != nil {
		return RunWork{}, false, err
	}
	var scenarioJSON string
	if err := tx.QueryRowContext(ctx, "SELECT scenario_json FROM runs WHERE id = ?", run.ID).Scan(&scenarioJSON); err != nil {
		return RunWork{}, false, storageFailure("read run scenario", err)
	}
	var scenario domain.FakeScenario
	if err := json.Unmarshal([]byte(scenarioJSON), &scenario); err != nil {
		return RunWork{}, false, storageFailure("decode run scenario", err)
	}
	if err := tx.Commit(); err != nil {
		return RunWork{}, false, storageFailure("commit run job claim", err)
	}
	return RunWork{Run: run, Scenario: scenario}, true, nil
}

func (s *Store) MarkRunStarting(ctx context.Context, runID, correlationID string) (domain.Run, error) {
	return s.mutateWorkerRun(ctx, runID, correlationID, func(tx *sql.Tx, run *domain.Run, now string) (int64, error) {
		if run.Status == domain.RunStarting {
			return 0, nil
		}
		if run.Status != domain.RunRequested {
			return 0, &Error{Code: CodeRunConflict, Message: "only a requested run can start"}
		}
		run.Status = domain.RunStarting
		run.Revision++
		if err := updateRunProjection(ctx, tx, *run, now); err != nil {
			return 0, err
		}
		if err := setRunJob(ctx, tx, run.ID, "pending", now); err != nil {
			return 0, err
		}
		if err := appendRunTimeline(ctx, tx, run.ID, runStartingEvent, "runtime launch is starting", nil, now); err != nil {
			return 0, err
		}
		return appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runStartingEvent, correlationID, now, map[string]any{})
	})
}

func (s *Store) MarkRunStarted(ctx context.Context, runID, runtimeHandle, providerHandle, correlationID string) (domain.RunDetail, error) {
	runtimeHandle, providerHandle = strings.TrimSpace(runtimeHandle), strings.TrimSpace(providerHandle)
	if runtimeHandle == "" || providerHandle == "" {
		return domain.RunDetail{}, &Error{Code: CodeInvalidRun, Message: "run start requires runtime and provider handles"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunDetail{}, storageFailure("begin run started transition", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", runID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if run.Status == domain.RunActive && run.RuntimeHandle == runtimeHandle && run.ProviderHandle == providerHandle {
		detail, detailErr := runDetailInTransaction(ctx, tx, run)
		if detailErr != nil {
			return domain.RunDetail{}, detailErr
		}
		if err := tx.Commit(); err != nil {
			return domain.RunDetail{}, storageFailure("commit replayed run start", err)
		}
		return detail, nil
	}
	if run.Status != domain.RunStarting {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "only a starting run can become active"}
	}
	now := s.nowText()
	run.Status, run.RuntimeHandle, run.ProviderHandle = domain.RunActive, runtimeHandle, providerHandle
	run.StartedAt = now
	run.Revision++
	if err := updateRunProjection(ctx, tx, run, now); err != nil {
		return domain.RunDetail{}, err
	}
	task, err := queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if task.Status != domain.TaskAssigned {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "run task is no longer assigned"}
	}
	task.Status, task.BlockedReason, task.Revision = domain.TaskActive, "", task.Revision+1
	if err := updateTaskState(ctx, tx, task, now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := setRunJob(ctx, tx, run.ID, "pending", now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runStartedEvent, "runtime and provider are bound", nil, now); err != nil {
		return domain.RunDetail{}, err
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runStartedEvent, correlationID, now, map[string]any{"runtime_handle": runtimeHandle, "provider_handle": providerHandle}); err != nil {
		return domain.RunDetail{}, err
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskStarted, correlationID, now, map[string]any{"run_id": run.ID, "status": task.Status}); err != nil {
		return domain.RunDetail{}, err
	}
	detail, err := runDetailInTransaction(ctx, tx, run)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunDetail{}, storageFailure("commit run started transition", err)
	}
	return detail, nil
}

func (s *Store) RecordRunRuntimeBinding(ctx context.Context, runID, runtimeHandle, correlationID string) (domain.Run, error) {
	runtimeHandle = strings.TrimSpace(runtimeHandle)
	if runtimeHandle == "" {
		return domain.Run{}, &Error{Code: CodeInvalidRun, Message: "runtime binding requires a handle"}
	}
	return s.mutateWorkerRun(ctx, runID, correlationID, func(tx *sql.Tx, run *domain.Run, now string) (int64, error) {
		if run.Status != domain.RunStarting {
			return 0, &Error{Code: CodeRunConflict, Message: "runtime binding requires a starting run"}
		}
		if run.RuntimeHandle == runtimeHandle {
			return 0, nil
		}
		if run.RuntimeHandle != "" {
			return 0, &Error{Code: CodeRunConflict, Message: "runtime binding cannot replace an existing handle"}
		}
		run.RuntimeHandle = runtimeHandle
		run.Revision++
		if err := updateRunProjection(ctx, tx, *run, now); err != nil {
			return 0, err
		}
		if err := setRunJob(ctx, tx, run.ID, "pending", now); err != nil {
			return 0, err
		}
		return appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runRuntimeObservedEvent, correlationID, now, map[string]any{"runtime_handle": runtimeHandle})
	})
}

func (s *Store) FailRunStart(ctx context.Context, runID, message, correlationID string) (domain.RunDetail, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "runtime failed to start"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunDetail{}, storageFailure("begin run start failure", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", runID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if run.Status == domain.RunStartFailed {
		return runDetailInTransaction(ctx, tx, run)
	}
	if run.Status != domain.RunStarting && run.Status != domain.RunRequested {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "run cannot record a start failure from its current state"}
	}
	now := s.nowText()
	run.Status, run.FailureCode, run.FailureMessage, run.FinishedAt = domain.RunStartFailed, "runtime_start_failed", message, now
	run.Revision++
	if err := updateRunProjection(ctx, tx, run, now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := setRunJob(ctx, tx, run.ID, "complete", now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runStartFailedEvent, message, nil, now); err != nil {
		return domain.RunDetail{}, err
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runStartFailedEvent, correlationID, now, map[string]any{"code": run.FailureCode, "message": message}); err != nil {
		return domain.RunDetail{}, err
	}
	detail, err := runDetailInTransaction(ctx, tx, run)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunDetail{}, storageFailure("commit run start failure", err)
	}
	return detail, nil
}

func (s *Store) ApplyRunObservation(ctx context.Context, runID string, observation domain.RunObservation, accepted bool, missing []string, correlationID string) (domain.RunDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunDetail{}, storageFailure("begin run observation", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", strings.TrimSpace(runID))
	if err != nil {
		return domain.RunDetail{}, err
	}
	detail, err := s.applyRunObservationInTransaction(ctx, tx, &run, observation, accepted, missing, correlationID, s.nowText())
	if err != nil {
		return domain.RunDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunDetail{}, storageFailure("commit run observation", err)
	}
	return detail, nil
}

// ApplyQueuedRunReport applies a capability-submitted report and marks that exact
// report consumed in the same transaction as the run/task state transition.
func (s *Store) ApplyQueuedRunReport(ctx context.Context, runID, reportID string, accepted bool, missing []string, correlationID string) (domain.RunDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunDetail{}, storageFailure("begin queued run report", err)
	}
	defer tx.Rollback()
	report, err := queryRunReportByID(ctx, tx, strings.TrimSpace(reportID))
	if err != nil {
		return domain.RunDetail{}, err
	}
	if report.RunID != strings.TrimSpace(runID) {
		return domain.RunDetail{}, &Error{Code: CodeInvalidReport, Message: "run report belongs to a different run"}
	}
	if report.Status != "pending" {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "run report was already applied"}
	}
	run, err := queryRun(ctx, tx, "", report.RunID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	observation := domain.RunObservation{Kind: report.Kind, Message: report.Message, Evidence: append([]string(nil), report.Evidence...), Handoff: report.Handoff}
	now := s.nowText()
	detail, err := s.applyRunObservationInTransaction(ctx, tx, &run, observation, accepted, missing, correlationID, now)
	if err != nil {
		return domain.RunDetail{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE run_reports SET status = 'applied', applied_at = ? WHERE id = ? AND status = 'pending'", now, report.ID)
	if err != nil {
		return domain.RunDetail{}, storageFailure("mark run report applied", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return domain.RunDetail{}, storageFailure("verify run report application", errors.New("report application lost its pending state"))
	}
	if err := tx.Commit(); err != nil {
		return domain.RunDetail{}, storageFailure("commit queued run report", err)
	}
	return detail, nil
}

func (s *Store) applyRunObservationInTransaction(ctx context.Context, tx *sql.Tx, run *domain.Run, observation domain.RunObservation, accepted bool, missing []string, correlationID, now string) (domain.RunDetail, error) {
	if run.Status != domain.RunActive {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "observations require an active run"}
	}
	run.StepCursor++
	run.Revision++
	var jobStatus = "pending"
	switch observation.Kind {
	case domain.ObservationProgress:
		if strings.TrimSpace(observation.Message) == "" {
			observation.Message = "progress reported"
		}
		if observation.Pause {
			jobStatus = "complete"
		}
		if err := appendRunTimeline(ctx, tx, run.ID, runProgressEvent, observation.Message, observation.Evidence, now); err != nil {
			return domain.RunDetail{}, err
		}
		if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runProgressEvent, correlationID, now, map[string]any{"message": observation.Message, "evidence": observation.Evidence}); err != nil {
			return domain.RunDetail{}, err
		}
	case domain.ObservationBlocked:
		question := strings.TrimSpace(observation.Message)
		if question == "" {
			return domain.RunDetail{}, &Error{Code: CodeInvalidRun, Message: "blocked observation requires a question"}
		}
		run.Status, run.BlockedQuestion, jobStatus = domain.RunBlocked, question, "complete"
		task, err := queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
		if err != nil {
			return domain.RunDetail{}, err
		}
		task.Status, task.BlockedReason, task.Revision = domain.TaskBlocked, question, task.Revision+1
		if err := updateTaskState(ctx, tx, task, now); err != nil {
			return domain.RunDetail{}, err
		}
		if err := appendRunTimeline(ctx, tx, run.ID, runBlockedEvent, question, observation.Evidence, now); err != nil {
			return domain.RunDetail{}, err
		}
		if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runBlockedEvent, correlationID, now, map[string]any{"question": question}); err != nil {
			return domain.RunDetail{}, err
		}
		if _, err := appendEvent(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskBlocked, correlationID, now, map[string]any{"run_id": run.ID, "reason": question, "status": task.Status}); err != nil {
			return domain.RunDetail{}, err
		}
	case domain.ObservationCompletion:
		if err := applyCompletionObservation(ctx, tx, run, observation, accepted, missing, correlationID, now); err != nil {
			return domain.RunDetail{}, err
		}
		jobStatus = "complete"
	default:
		return domain.RunDetail{}, &Error{Code: CodeInvalidRun, Message: fmt.Sprintf("unsupported observation kind %q", observation.Kind)}
	}
	if err := updateRunProjection(ctx, tx, *run, now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := setRunJob(ctx, tx, run.ID, jobStatus, now); err != nil {
		return domain.RunDetail{}, err
	}
	detail, err := runDetailInTransaction(ctx, tx, *run)
	if err != nil {
		return domain.RunDetail{}, err
	}
	return detail, nil
}

func applyCompletionObservation(ctx context.Context, tx *sql.Tx, run *domain.Run, observation domain.RunObservation, accepted bool, missing []string, correlationID, now string) error {
	summary := strings.TrimSpace(observation.Message)
	if summary == "" {
		return &Error{Code: CodeInvalidRun, Message: "completion observation requires a summary"}
	}
	if accepted && strings.TrimSpace(observation.Handoff) == "" {
		return &Error{Code: CodeInvalidRun, Message: "accepted completion requires a handoff"}
	}
	task, err := queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
	if err != nil {
		return err
	}
	run.Status, run.ResultSummary = domain.RunReview, summary
	task.Status, task.BlockedReason, task.Revision = domain.TaskReview, "", task.Revision+1
	if err := appendRunTimeline(ctx, tx, run.ID, runCompletionProposedEvent, summary, observation.Evidence, now); err != nil {
		return err
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runCompletionProposedEvent, correlationID, now, map[string]any{"summary": summary, "evidence": observation.Evidence}); err != nil {
		return err
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskCompletionProposed, correlationID, now, map[string]any{"run_id": run.ID, "summary": summary, "evidence": observation.Evidence}); err != nil {
		return err
	}
	if accepted {
		handoffID, err := randomID("handoff_")
		if err != nil {
			return storageFailure("generate handoff id", err)
		}
		evidenceJSON, err := json.Marshal(observation.Evidence)
		if err != nil {
			return storageFailure("encode handoff evidence", err)
		}
		handoffSummary := strings.TrimSpace(observation.Handoff)
		if _, err := tx.ExecContext(ctx, "INSERT INTO run_handoffs(id, run_id, task_id, summary, evidence_json, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?)", handoffID, run.ID, task.ID, handoffSummary, string(evidenceJSON), now, localOwnerActorID); err != nil {
			return storageFailure("insert run handoff", err)
		}
		run.Status, run.FinishedAt = domain.RunCompleted, now
		run.Revision++
		task.Status, task.Revision = domain.TaskCompleted, task.Revision+1
		if _, err := tx.ExecContext(ctx, "UPDATE task_assignments SET status = 'released', revision = revision + 1, updated_at = ?, updated_by = ? WHERE task_id = ? AND status = 'active'", now, localOwnerActorID, task.ID); err != nil {
			return storageFailure("release completed task assignment", err)
		}
		if err := appendRunTimeline(ctx, tx, run.ID, taskHandoffRecorded, handoffSummary, observation.Evidence, now); err != nil {
			return err
		}
		if err := appendRunTimeline(ctx, tx, run.ID, runCompletedEvent, summary, observation.Evidence, now); err != nil {
			return err
		}
		if _, err := appendEvent(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskHandoffRecorded, correlationID, now, map[string]any{"run_id": run.ID, "handoff_id": handoffID, "summary": handoffSummary}); err != nil {
			return err
		}
		if _, err := appendEvent(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskCompletedEvent, correlationID, now, map[string]any{"run_id": run.ID, "status": task.Status}); err != nil {
			return err
		}
		if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runCompletedEvent, correlationID, now, map[string]any{"task_id": task.ID, "handoff_id": handoffID}); err != nil {
			return err
		}
	} else {
		reason := "completion evidence did not satisfy acceptance"
		if len(missing) != 0 {
			reason += ": missing " + strings.Join(missing, ", ")
		}
		task.Status, task.BlockedReason = domain.TaskChangesRequested, reason
		task.Revision++
		if err := appendRunTimeline(ctx, tx, run.ID, taskChangesRequestedEvent, reason, observation.Evidence, now); err != nil {
			return err
		}
		if _, err := appendEvent(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskChangesRequestedEvent, correlationID, now, map[string]any{"run_id": run.ID, "reason": reason, "missing_evidence": missing}); err != nil {
			return err
		}
	}
	return updateTaskState(ctx, tx, task, now)
}

func (s *Store) ResumeRun(ctx context.Context, command ResumeRunCommand) (RunMutationResult, error) {
	workspaceIdentifier, runID := strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.RunID)
	key, correlationID := strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if workspaceIdentifier == "" || runID == "" || command.ExpectedRevision < 1 {
		return RunMutationResult{}, &Error{Code: CodeInvalidRun, Message: "run resume requires workspace, run, and expected revision"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidRun); err != nil {
		return RunMutationResult{}, err
	}
	requestHash, err := hashCommand("run.resume", command)
	if err != nil {
		return RunMutationResult{}, storageFailure("hash run resume", err)
	}
	if _, err := s.ReconcileExpiredAssignments(ctx, workspaceIdentifier, correlationID+"-lease"); err != nil {
		return RunMutationResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunMutationResult{}, storageFailure("begin run resume", err)
	}
	defer tx.Rollback()
	var replay RunMutationResult
	if found, err := lookupIdempotency(ctx, tx, key, "run.resume", requestHash, &replay); err != nil {
		return RunMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return RunMutationResult{}, err
	}
	run, err := queryRun(ctx, tx, workspace.ID, runID)
	if err != nil {
		return RunMutationResult{}, err
	}
	if run.Revision != command.ExpectedRevision {
		return RunMutationResult{}, revisionConflict("run", run.ID, command.ExpectedRevision, run.Revision)
	}
	if run.Status != domain.RunBlocked && run.Status != domain.RunActive {
		return RunMutationResult{}, &Error{Code: CodeRunConflict, Message: "only a blocked or explicitly paused active run can resume"}
	}
	var jobStatus string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM run_jobs WHERE run_id = ?", run.ID).Scan(&jobStatus); err != nil {
		return RunMutationResult{}, storageFailure("query run job before resume", err)
	}
	if jobStatus != "complete" {
		return RunMutationResult{}, &Error{Code: CodeRunConflict, Message: "run already has pending work"}
	}
	task, err := queryTask(ctx, tx, workspace.ID, run.TaskID)
	if err != nil {
		return RunMutationResult{}, err
	}
	if task.AssignmentID == "" || task.AssignedAgentID != run.AgentID {
		return RunMutationResult{}, &Error{Code: CodeRunConflict, Message: "run cannot resume without its active task assignment"}
	}
	now := s.nowText()
	if run.Status == domain.RunBlocked {
		run.Status, run.BlockedQuestion = domain.RunActive, ""
		task.Status, task.BlockedReason, task.Revision = domain.TaskActive, "", task.Revision+1
		if err := updateTaskState(ctx, tx, task, now); err != nil {
			return RunMutationResult{}, err
		}
	}
	run.Revision++
	if err := updateRunProjection(ctx, tx, run, now); err != nil {
		return RunMutationResult{}, err
	}
	if err := setRunJob(ctx, tx, run.ID, "pending", now); err != nil {
		return RunMutationResult{}, err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runResumedEvent, "run resumed", nil, now); err != nil {
		return RunMutationResult{}, err
	}
	sequence, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runResumedEvent, correlationID, now, map[string]any{"step_cursor": run.StepCursor})
	if err != nil {
		return RunMutationResult{}, err
	}
	detail, err := runDetailInTransaction(ctx, tx, run)
	if err != nil {
		return RunMutationResult{}, err
	}
	result := RunMutationResult{Detail: detail, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "run.resume", requestHash, result, now); err != nil {
		return RunMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunMutationResult{}, storageFailure("commit run resume", err)
	}
	return result, nil
}

func (s *Store) RequestRunStop(ctx context.Context, command StopRunCommand) (RunMutationResult, error) {
	workspaceIdentifier, runID := strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.RunID)
	key, correlationID := strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if workspaceIdentifier == "" || runID == "" || command.ExpectedRevision < 1 || command.GracePeriodMillis < 1 || command.GracePeriodMillis > 30000 {
		return RunMutationResult{}, &Error{Code: CodeInvalidRun, Message: "run stop requires workspace, run, expected revision, and a bounded grace period"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidRun); err != nil {
		return RunMutationResult{}, err
	}
	requestHash, err := hashCommand("run.stop", command)
	if err != nil {
		return RunMutationResult{}, storageFailure("hash run stop", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunMutationResult{}, storageFailure("begin run stop", err)
	}
	defer tx.Rollback()
	var replay RunMutationResult
	if found, err := lookupIdempotency(ctx, tx, key, "run.stop", requestHash, &replay); err != nil {
		return RunMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return RunMutationResult{}, err
	}
	run, err := queryRun(ctx, tx, workspace.ID, runID)
	if err != nil {
		return RunMutationResult{}, err
	}
	if run.Revision != command.ExpectedRevision {
		return RunMutationResult{}, revisionConflict("run", run.ID, command.ExpectedRevision, run.Revision)
	}
	if run.Status != domain.RunActive && run.Status != domain.RunBlocked {
		return RunMutationResult{}, &Error{Code: CodeRunConflict, Message: "only an active or blocked run can be stopped"}
	}
	now := s.nowText()
	run.Status, run.StopGraceMillis, run.Revision = domain.RunStopping, command.GracePeriodMillis, run.Revision+1
	if err := updateRunProjection(ctx, tx, run, now); err != nil {
		return RunMutationResult{}, err
	}
	if err := setRunJob(ctx, tx, run.ID, "pending", now); err != nil {
		return RunMutationResult{}, err
	}
	message := fmt.Sprintf("graceful stop requested with %d ms grace period", command.GracePeriodMillis)
	if err := appendRunTimeline(ctx, tx, run.ID, runStopRequestedEvent, message, nil, now); err != nil {
		return RunMutationResult{}, err
	}
	sequence, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runStopRequestedEvent, correlationID, now, map[string]any{"grace_period_millis": command.GracePeriodMillis})
	if err != nil {
		return RunMutationResult{}, err
	}
	detail, err := runDetailInTransaction(ctx, tx, run)
	if err != nil {
		return RunMutationResult{}, err
	}
	result := RunMutationResult{Detail: detail, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "run.stop", requestHash, result, now); err != nil {
		return RunMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunMutationResult{}, storageFailure("commit run stop", err)
	}
	return result, nil
}

func (s *Store) MarkRunStopped(ctx context.Context, runID string, forced bool, diagnostic, correlationID string) (domain.RunDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunDetail{}, storageFailure("begin run stopped transition", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", strings.TrimSpace(runID))
	if err != nil {
		return domain.RunDetail{}, err
	}
	if run.Status == domain.RunStopped {
		return runDetailInTransaction(ctx, tx, run)
	}
	if run.Status != domain.RunStopping {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "only a stopping run can become stopped"}
	}
	now := s.nowText()
	run.Status, run.StopGraceMillis, run.StopForced, run.ResultSummary, run.FinishedAt, run.Revision = domain.RunStopped, 0, forced, strings.TrimSpace(diagnostic), now, run.Revision+1
	if err := updateRunProjection(ctx, tx, run, now); err != nil {
		return domain.RunDetail{}, err
	}
	task, err := queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if task.AssignmentID == "" || task.AssignedAgentID != run.AgentID {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "stopped run lost its task assignment"}
	}
	task.Status, task.BlockedReason, task.Revision = domain.TaskAssigned, "", task.Revision+1
	if err := updateTaskState(ctx, tx, task, now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := setRunJob(ctx, tx, run.ID, "complete", now); err != nil {
		return domain.RunDetail{}, err
	}
	message := run.ResultSummary
	if message == "" {
		message = "run stopped"
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runStoppedEvent, message, nil, now); err != nil {
		return domain.RunDetail{}, err
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runStoppedEvent, correlationID, now, map[string]any{"forced": forced, "diagnostic": message}); err != nil {
		return domain.RunDetail{}, err
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskRunStoppedEvent, correlationID, now, map[string]any{"run_id": run.ID, "status": task.Status}); err != nil {
		return domain.RunDetail{}, err
	}
	detail, err := runDetailInTransaction(ctx, tx, run)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunDetail{}, storageFailure("commit run stopped transition", err)
	}
	return detail, nil
}

func (s *Store) LoseRun(ctx context.Context, runID, message, correlationID string) (domain.RunDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunDetail{}, storageFailure("begin run lost transition", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", strings.TrimSpace(runID))
	if err != nil {
		return domain.RunDetail{}, err
	}
	if run.Status == domain.RunLost {
		return runDetailInTransaction(ctx, tx, run)
	}
	if run.Status != domain.RunStarting && run.Status != domain.RunActive && run.Status != domain.RunStopping {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "only a starting, active, or stopping run can become lost"}
	}
	now := s.nowText()
	message = strings.TrimSpace(message)
	if message == "" {
		message = "runtime identity or outcome cannot be trusted"
	}
	run.Status, run.FailureCode, run.FailureMessage, run.Revision = domain.RunLost, "runtime_state_unknown", message, run.Revision+1
	if err := updateRunProjection(ctx, tx, run, now); err != nil {
		return domain.RunDetail{}, err
	}
	task, err := queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	task.Status, task.BlockedReason, task.Revision = domain.TaskBlocked, message, task.Revision+1
	if err := updateTaskState(ctx, tx, task, now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := setRunJob(ctx, tx, run.ID, "complete", now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runLostEvent, message, nil, now); err != nil {
		return domain.RunDetail{}, err
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runLostEvent, correlationID, now, map[string]any{"code": run.FailureCode, "message": message, "capacity_retained": true}); err != nil {
		return domain.RunDetail{}, err
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskBlocked, correlationID, now, map[string]any{"run_id": run.ID, "reason": message, "status": task.Status}); err != nil {
		return domain.RunDetail{}, err
	}
	detail, err := runDetailInTransaction(ctx, tx, run)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunDetail{}, storageFailure("commit run lost transition", err)
	}
	return detail, nil
}

func (s *Store) DeferRunJob(ctx context.Context, runID string, delay time.Duration) error {
	if delay < 0 {
		delay = 0
	}
	nowTime := s.clock().UTC()
	now, available := nowTime.Format(time.RFC3339Nano), nowTime.Add(delay).Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, "UPDATE run_jobs SET status = 'pending', available_at = ?, lease_expires_at = NULL, updated_at = ? WHERE run_id = ? AND status = 'leased'", available, now, strings.TrimSpace(runID))
	if err != nil {
		return storageFailure("defer run job", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &Error{Code: CodeRunConflict, Message: "run job is not leased for deferral"}
	}
	return nil
}

func (s *Store) FailRun(ctx context.Context, runID, code, message, correlationID string) (domain.RunDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunDetail{}, storageFailure("begin run failure", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", runID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if run.Status == domain.RunFailed {
		return runDetailInTransaction(ctx, tx, run)
	}
	if run.Status != domain.RunActive {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "only an active run can fail during observation"}
	}
	now := s.nowText()
	run.Status, run.FailureCode, run.FailureMessage, run.FinishedAt = domain.RunFailed, strings.TrimSpace(code), strings.TrimSpace(message), now
	run.Revision++
	if err := updateRunProjection(ctx, tx, run, now); err != nil {
		return domain.RunDetail{}, err
	}
	task, err := queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	task.Status, task.BlockedReason, task.Revision = domain.TaskFailed, run.FailureMessage, task.Revision+1
	if err := updateTaskState(ctx, tx, task, now); err != nil {
		return domain.RunDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE task_assignments SET status = 'released', revision = revision + 1, updated_at = ?, updated_by = ? WHERE task_id = ? AND status = 'active'", now, localOwnerActorID, task.ID); err != nil {
		return domain.RunDetail{}, storageFailure("release failed run assignment", err)
	}
	if err := setRunJob(ctx, tx, run.ID, "complete", now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runFailedEvent, run.FailureMessage, nil, now); err != nil {
		return domain.RunDetail{}, err
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runFailedEvent, correlationID, now, map[string]any{"code": run.FailureCode, "message": run.FailureMessage}); err != nil {
		return domain.RunDetail{}, err
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskFailedEvent, correlationID, now, map[string]any{"run_id": run.ID, "reason": run.FailureMessage}); err != nil {
		return domain.RunDetail{}, err
	}
	detail, err := runDetailInTransaction(ctx, tx, run)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunDetail{}, storageFailure("commit run failure", err)
	}
	return detail, nil
}

func (s *Store) mutateWorkerRun(ctx context.Context, runID, correlationID string, mutation func(*sql.Tx, *domain.Run, string) (int64, error)) (domain.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Run{}, storageFailure("begin worker run mutation", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", strings.TrimSpace(runID))
	if err != nil {
		return domain.Run{}, err
	}
	if _, err := mutation(tx, &run, s.nowText()); err != nil {
		return domain.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, storageFailure("commit worker run mutation", err)
	}
	return run, nil
}

func selectRunCheckout(ctx context.Context, tx *sql.Tx, projectID, identifier string) (domain.Checkout, error) {
	query := `
SELECT c.id, c.project_id, c.repository_id, c.path, c.write_mode, c.revision, c.availability,
       c.checkout_kind, COALESCE(c.branch, ''), COALESCE(c.head_commit, ''), c.dirty,
       COALESCE(c.git_dir, ''), COALESCE(c.git_common_dir, ''), c.observed_at,
       COALESCE(c.diagnostic_code, ''), COALESCE(c.diagnostic, ''),
       c.created_at, c.updated_at, c.created_by, c.updated_by
FROM checkouts c
WHERE c.project_id = ? AND c.availability = 'available' AND c.write_mode <> 'read_only'
  AND (c.write_mode = 'shared' OR NOT EXISTS (
      SELECT 1 FROM runs r WHERE r.checkout_id = c.id AND r.status IN ('requested', 'starting', 'active', 'blocked', 'stopping', 'lost')
  ))`
	arguments := []any{projectID}
	if identifier != "" {
		query += " AND c.id = ?"
		arguments = append(arguments, identifier)
	}
	query += " ORDER BY CASE c.write_mode WHEN 'exclusive' THEN 0 WHEN 'claimed' THEN 1 ELSE 2 END, c.path, c.id LIMIT 1"
	var checkout domain.Checkout
	err := tx.QueryRowContext(ctx, query, arguments...).Scan(&checkout.ID, &checkout.ProjectID, &checkout.RepositoryID, &checkout.Path, &checkout.WriteMode, &checkout.Revision, &checkout.Availability, &checkout.CheckoutKind, &checkout.Branch, &checkout.HeadCommit, &checkout.Dirty, &checkout.GitDir, &checkout.GitCommonDir, &checkout.ObservedAt, &checkout.DiagnosticCode, &checkout.Diagnostic, &checkout.CreatedAt, &checkout.UpdatedAt, &checkout.CreatedBy, &checkout.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		if identifier == "" {
			return domain.Checkout{}, &Error{Code: CodePlacementUnavailable, Message: "project has no available writable checkout with capacity"}
		}
		return domain.Checkout{}, &Error{Code: CodePlacementUnavailable, Message: fmt.Sprintf("checkout %q is unavailable, read-only, outside the project, or already reserved", identifier)}
	}
	if err != nil {
		return domain.Checkout{}, storageFailure("select run checkout", err)
	}
	return checkout, nil
}

const runSelect = `
SELECT r.id, r.workspace_id, r.project_id, r.task_id, r.agent_id, r.checkout_id,
       COALESCE(b.context_packet_id, ''),
       r.runtime, r.provider, r.scenario_name, r.status, r.step_cursor,
       COALESCE(r.runtime_handle, ''), COALESCE(r.provider_handle, ''),
       COALESCE(r.blocked_question, ''), COALESCE(r.result_summary, ''),
       COALESCE(r.failure_code, ''), COALESCE(r.failure_message, ''), r.stop_grace_millis, r.stop_forced, r.revision,
       r.created_at, r.updated_at, COALESCE(r.started_at, ''), COALESCE(r.finished_at, ''),
       r.created_by, r.updated_by, c.path, c.write_mode, r.placement_reasons_json
FROM runs r JOIN checkouts c ON c.id = r.checkout_id
LEFT JOIN run_context_bindings b ON b.run_id = r.id`

func queryRun(ctx context.Context, database queryRower, workspaceID, runID string) (domain.Run, error) {
	query := runSelect + " WHERE r.id = ?"
	arguments := []any{runID}
	if workspaceID != "" {
		query += " AND r.workspace_id = ?"
		arguments = append(arguments, workspaceID)
	}
	var run domain.Run
	err := scanRun(database.QueryRowContext(ctx, query, arguments...), &run)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, &Error{Code: CodeRunNotFound, Message: fmt.Sprintf("run %q was not found", runID)}
	}
	if err != nil {
		return domain.Run{}, storageFailure("query run", err)
	}
	return run, nil
}

func scanRun(row rowScanner, run *domain.Run) error {
	var reasonsJSON string
	err := row.Scan(&run.ID, &run.WorkspaceID, &run.ProjectID, &run.TaskID, &run.AgentID, &run.CheckoutID,
		&run.ContextPacketID,
		&run.Runtime, &run.Provider, &run.ScenarioName, &run.Status, &run.StepCursor,
		&run.RuntimeHandle, &run.ProviderHandle, &run.BlockedQuestion, &run.ResultSummary,
		&run.FailureCode, &run.FailureMessage, &run.StopGraceMillis, &run.StopForced, &run.Revision, &run.CreatedAt, &run.UpdatedAt,
		&run.StartedAt, &run.FinishedAt, &run.CreatedBy, &run.UpdatedBy,
		&run.Placement.CheckoutPath, &run.Placement.WriteMode, &reasonsJSON)
	if err != nil {
		return err
	}
	run.Placement.TaskID, run.Placement.AgentID, run.Placement.CheckoutID = run.TaskID, run.AgentID, run.CheckoutID
	run.Placement.Runtime, run.Placement.Provider = run.Runtime, run.Provider
	return json.Unmarshal([]byte(reasonsJSON), &run.Placement.Reasons)
}

type runQueryContext interface {
	queryRower
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func runDetail(ctx context.Context, database runQueryContext, run domain.Run) (domain.RunDetail, error) {
	task, err := queryTask(ctx, database, run.WorkspaceID, run.TaskID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	agent, err := queryAgent(ctx, database, run.WorkspaceID, run.AgentID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	checkout, err := queryCheckoutByID(ctx, database, run.CheckoutID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	timeline, err := queryRunTimeline(ctx, database, run.ID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	handoff, err := queryRunHandoff(ctx, database, run.ID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	return domain.RunDetail{Run: run, Task: task, Agent: agent, Checkout: checkout, Timeline: timeline, Handoff: handoff}, nil
}

func runDetailInTransaction(ctx context.Context, tx *sql.Tx, run domain.Run) (domain.RunDetail, error) {
	refreshed, err := queryRun(ctx, tx, run.WorkspaceID, run.ID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	return runDetail(ctx, tx, refreshed)
}

func queryCheckoutByID(ctx context.Context, database queryRower, checkoutID string) (domain.Checkout, error) {
	var checkout domain.Checkout
	err := database.QueryRowContext(ctx, `
SELECT id, project_id, repository_id, path, write_mode, revision, availability,
       checkout_kind, COALESCE(branch, ''), COALESCE(head_commit, ''), dirty,
       COALESCE(git_dir, ''), COALESCE(git_common_dir, ''), observed_at,
       COALESCE(diagnostic_code, ''), COALESCE(diagnostic, ''),
       created_at, updated_at, created_by, updated_by
FROM checkouts WHERE id = ?`, checkoutID).Scan(&checkout.ID, &checkout.ProjectID, &checkout.RepositoryID, &checkout.Path, &checkout.WriteMode, &checkout.Revision, &checkout.Availability, &checkout.CheckoutKind, &checkout.Branch, &checkout.HeadCommit, &checkout.Dirty, &checkout.GitDir, &checkout.GitCommonDir, &checkout.ObservedAt, &checkout.DiagnosticCode, &checkout.Diagnostic, &checkout.CreatedAt, &checkout.UpdatedAt, &checkout.CreatedBy, &checkout.UpdatedBy)
	if err != nil {
		return domain.Checkout{}, storageFailure("query run checkout", err)
	}
	return checkout, nil
}

func queryRunTimeline(ctx context.Context, database runQueryContext, runID string) ([]domain.RunTimelineEntry, error) {
	rows, err := database.QueryContext(ctx, "SELECT sequence, run_id, kind, COALESCE(message, ''), evidence_json, recorded_at FROM run_timeline WHERE run_id = ? ORDER BY sequence", runID)
	if err != nil {
		return nil, storageFailure("query run timeline", err)
	}
	defer rows.Close()
	entries := make([]domain.RunTimelineEntry, 0)
	for rows.Next() {
		var entry domain.RunTimelineEntry
		var evidenceJSON string
		if err := rows.Scan(&entry.Sequence, &entry.RunID, &entry.Kind, &entry.Message, &evidenceJSON, &entry.RecordedAt); err != nil {
			return nil, storageFailure("scan run timeline", err)
		}
		if err := json.Unmarshal([]byte(evidenceJSON), &entry.Evidence); err != nil {
			return nil, storageFailure("decode run timeline evidence", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func queryRunHandoff(ctx context.Context, database queryRower, runID string) (*domain.Handoff, error) {
	var handoff domain.Handoff
	var evidenceJSON string
	err := database.QueryRowContext(ctx, "SELECT id, run_id, task_id, summary, evidence_json, created_at, created_by FROM run_handoffs WHERE run_id = ?", runID).Scan(&handoff.ID, &handoff.RunID, &handoff.TaskID, &handoff.Summary, &evidenceJSON, &handoff.CreatedAt, &handoff.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storageFailure("query run handoff", err)
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &handoff.Evidence); err != nil {
		return nil, storageFailure("decode run handoff evidence", err)
	}
	return &handoff, nil
}

func queryRunsForTask(ctx context.Context, database runQueryContext, taskID string) ([]domain.Run, error) {
	rows, err := database.QueryContext(ctx, runSelect+" WHERE r.task_id = ? ORDER BY r.created_at, r.id", taskID)
	if err != nil {
		return nil, storageFailure("query task runs", err)
	}
	defer rows.Close()
	result := make([]domain.Run, 0)
	for rows.Next() {
		var run domain.Run
		if err := scanRun(rows, &run); err != nil {
			return nil, storageFailure("scan task run", err)
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

func queryTaskRunTimeline(ctx context.Context, database runQueryContext, taskID string) ([]domain.RunTimelineEntry, error) {
	rows, err := database.QueryContext(ctx, `
SELECT timeline.sequence, timeline.run_id, timeline.kind, COALESCE(timeline.message, ''), timeline.evidence_json, timeline.recorded_at
FROM run_timeline timeline JOIN runs r ON r.id = timeline.run_id
WHERE r.task_id = ? ORDER BY timeline.sequence`, taskID)
	if err != nil {
		return nil, storageFailure("query task run timeline", err)
	}
	defer rows.Close()
	entries := make([]domain.RunTimelineEntry, 0)
	for rows.Next() {
		var entry domain.RunTimelineEntry
		var evidenceJSON string
		if err := rows.Scan(&entry.Sequence, &entry.RunID, &entry.Kind, &entry.Message, &evidenceJSON, &entry.RecordedAt); err != nil {
			return nil, storageFailure("scan task run timeline", err)
		}
		if err := json.Unmarshal([]byte(evidenceJSON), &entry.Evidence); err != nil {
			return nil, storageFailure("decode task run timeline evidence", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func updateRunProjection(ctx context.Context, tx *sql.Tx, run domain.Run, now string) error {
	run.UpdatedAt, run.UpdatedBy = now, localOwnerActorID
	_, err := tx.ExecContext(ctx, `
UPDATE runs SET status = ?, step_cursor = ?, runtime_handle = NULLIF(?, ''), provider_handle = NULLIF(?, ''),
    blocked_question = NULLIF(?, ''), result_summary = NULLIF(?, ''), failure_code = NULLIF(?, ''),
    failure_message = NULLIF(?, ''), stop_grace_millis = ?, stop_forced = ?, revision = ?, updated_at = ?, started_at = NULLIF(?, ''),
    finished_at = NULLIF(?, ''), updated_by = ? WHERE id = ?`, run.Status, run.StepCursor,
		run.RuntimeHandle, run.ProviderHandle, run.BlockedQuestion, run.ResultSummary, run.FailureCode,
		run.FailureMessage, run.StopGraceMillis, run.StopForced, run.Revision, now, run.StartedAt, run.FinishedAt, localOwnerActorID, run.ID)
	if err != nil {
		return storageFailure("update run projection", err)
	}
	return nil
}

func updateTaskState(ctx context.Context, tx *sql.Tx, task domain.Task, now string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE tasks SET status = ?, blocked_reason = NULLIF(?, ''), revision = ?, updated_at = ?, updated_by = ? WHERE id = ?", task.Status, task.BlockedReason, task.Revision, now, localOwnerActorID, task.ID); err != nil {
		return storageFailure("update run task state", err)
	}
	return nil
}

func setRunJob(ctx context.Context, tx *sql.Tx, runID, status, now string) error {
	lease := any(nil)
	if status == "leased" {
		lease = now
	}
	if _, err := tx.ExecContext(ctx, "UPDATE run_jobs SET status = ?, available_at = ?, lease_expires_at = ?, updated_at = ? WHERE run_id = ?", status, now, lease, now, runID); err != nil {
		return storageFailure("update run job", err)
	}
	return nil
}

func appendRunTimeline(ctx context.Context, tx *sql.Tx, runID, kind, message string, evidence []string, now string) error {
	if evidence == nil {
		evidence = []string{}
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return storageFailure("encode run timeline evidence", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_timeline(run_id, kind, message, evidence_json, recorded_at) VALUES (?, ?, NULLIF(?, ''), ?, ?)", runID, kind, strings.TrimSpace(message), string(evidenceJSON), now); err != nil {
		return storageFailure("append run timeline", err)
	}
	return nil
}

func validStoredScenario(scenario domain.FakeScenario) bool {
	data, err := json.Marshal(scenario)
	if err != nil || len(data) > 60*1024 || strings.TrimSpace(scenario.Schema) == "" || strings.TrimSpace(scenario.Name) == "" || len(scenario.Steps) > 32 {
		return false
	}
	return scenario.StartFailure != "" || len(scenario.Steps) != 0
}

func validRunStatus(status string) bool {
	switch status {
	case domain.RunRequested, domain.RunStarting, domain.RunActive, domain.RunBlocked, domain.RunStopping, domain.RunStopped, domain.RunLost, domain.RunReview, domain.RunCompleted, domain.RunStartFailed, domain.RunFailed:
		return true
	default:
		return false
	}
}
