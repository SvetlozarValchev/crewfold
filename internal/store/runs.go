package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
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
	runLostResolvedEvent       = "run.lost_resolved"
	taskCompletionProposed     = "task.completion_proposed"
	taskChangesRequestedEvent  = "task.changes_requested"
	taskCompletedEvent         = "task.completed"
	taskFailedEvent            = "task.failed"
	taskHandoffRecorded        = "task.handoff_recorded"
	taskRunStoppedEvent        = "task.run_stopped"
	runWorkerActorID           = "subsystem:run-worker"
	defaultRunCapabilityTTL    = time.Hour
	maximumRunCapabilityTTL    = 24 * time.Hour
)

const runLossResolutionOwnerConfirmed = "owner_confirmed_effects_ended"

type ResolveLostRunCommand struct {
	WorkspaceIdentifier     string
	RunID                   string
	ExpectedRevision        int64
	Note                    string
	RuntimeRetiredConfirmed bool
	IdempotencyKey          string
	CorrelationID           string
}

type RunLossResolutionResult struct {
	Detail        domain.RunDetail         `json:"detail"`
	Resolution    domain.RunLossResolution `json:"resolution"`
	EventSequence int64                    `json:"event_sequence"`
}

func runStartRequestHash(command CreateRunCommand) (string, error) {
	payload := map[string]any{
		"workspace":                           command.WorkspaceIdentifier,
		"task_id":                             command.TaskID,
		"checkout_id":                         command.CheckoutIdentifier,
		"context_packet_id":                   command.ContextPacketID,
		"runtime":                             command.Runtime,
		"provider":                            command.Provider,
		"scenario":                            command.Scenario,
		"expected_task_revision":              command.ExpectedTaskRevision,
		"capability_ttl":                      command.CapabilityTTL,
		"check_watch_grant_id":                command.CheckWatchGrantID,
		"expected_check_watch_grant_revision": command.ExpectedCheckWatchGrantRevision,
	}
	if command.reviewedPriorRunID != "" {
		payload["reviewed_prior_run_id"] = command.reviewedPriorRunID
		payload["expected_reviewed_run_revision"] = command.expectedReviewedRunRevision
	}
	return hashCommand("run.start", payload)
}

// RetryReviewedRun creates a fresh owner-directed run after an exact rejected
// completion. The prior review remains immutable; the retained assignment is
// reopened and the successor run is committed in the same transaction.
func (s *Store) RetryReviewedRun(ctx context.Context, command RetryReviewedRunCommand) (RunMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.PriorRunID = strings.TrimSpace(command.PriorRunID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.PriorRunID == "" || command.ExpectedRunRevision < 1 || command.ExpectedTaskRevision < 1 || !validStoredScenario(command.Scenario) {
		return RunMutationResult{}, &Error{Code: CodeInvalidRun, Message: "review retry requires workspace, prior run, exact run and task revisions, and a valid scenario"}
	}
	prior, err := s.RunDetail(ctx, command.WorkspaceIdentifier, command.PriorRunID)
	if err != nil {
		return RunMutationResult{}, err
	}
	return s.CreateRun(ctx, CreateRunCommand{
		WorkspaceIdentifier: command.WorkspaceIdentifier,
		TaskID:              prior.Run.TaskID, CheckoutIdentifier: prior.Run.CheckoutID,
		Runtime: prior.Run.Runtime, Provider: prior.Run.Provider, Scenario: command.Scenario,
		ExpectedTaskRevision: command.ExpectedTaskRevision,
		IdempotencyKey:       command.IdempotencyKey, CorrelationID: command.CorrelationID,
		reviewedPriorRunID: command.PriorRunID, expectedReviewedRunRevision: command.ExpectedRunRevision,
	})
}

func runResumeRequestHash(workspaceIdentifier, runID string, expectedRevision int64) (string, error) {
	return hashCommand("run.resume", map[string]any{
		"workspace": workspaceIdentifier, "run_id": runID, "expected_revision": expectedRevision,
	})
}

func runStopRequestHash(workspaceIdentifier, runID string, expectedRevision, gracePeriodMillis int64) (string, error) {
	return hashCommand("run.stop", map[string]any{
		"workspace": workspaceIdentifier, "run_id": runID, "expected_revision": expectedRevision, "grace_period_millis": gracePeriodMillis,
	})
}

func runLostResolveRequestHash(command ResolveLostRunCommand) (string, error) {
	return hashCommand("run.lost.resolve", map[string]any{
		"workspace": command.WorkspaceIdentifier, "run_id": command.RunID, "expected_revision": command.ExpectedRevision,
		"note": command.Note, "runtime_retired_confirmed": command.RuntimeRetiredConfirmed,
	})
}

func (s *Store) CreateRun(ctx context.Context, command CreateRunCommand) (RunMutationResult, error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	taskID := strings.TrimSpace(command.TaskID)
	checkoutIdentifier := strings.TrimSpace(command.CheckoutIdentifier)
	runtimeName := strings.TrimSpace(command.Runtime)
	providerName := strings.TrimSpace(command.Provider)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	contextPacketID := strings.TrimSpace(command.ContextPacketID)
	checkWatchGrantID := strings.TrimSpace(command.CheckWatchGrantID)
	command.reviewedPriorRunID = strings.TrimSpace(command.reviewedPriorRunID)
	capabilityTTL := command.CapabilityTTL
	if capabilityTTL == 0 {
		capabilityTTL = defaultRunCapabilityTTL
	}
	if workspaceIdentifier == "" || taskID == "" || runtimeName == "" || providerName == "" || command.ExpectedTaskRevision < 1 || !validStoredScenario(command.Scenario) {
		return RunMutationResult{}, &Error{Code: CodeInvalidRun, Message: "run start requires workspace, task, runtime, provider, a valid scenario, and expected task revision"}
	}
	if (checkWatchGrantID == "") != (command.ExpectedCheckWatchGrantRevision == 0) || checkWatchGrantID != "" && contextPacketID != "" || command.reviewedPriorRunID != "" && (contextPacketID != "" || checkWatchGrantID != "" || command.expectedReviewedRunRevision < 1) {
		return RunMutationResult{}, &Error{Code: CodeInvalidRun, Message: "run start requires both-or-neither exact check-watch grant fields and forbids combining them with a supplied context packet"}
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
	command.CheckWatchGrantID = checkWatchGrantID
	command.Runtime = runtimeName
	command.Provider = providerName
	command.CapabilityTTL = capabilityTTL
	command.IdempotencyKey = key
	command.CorrelationID = correlationID
	requestHash, err := runStartRequestHash(command)
	if err != nil {
		return RunMutationResult{}, storageFailure("hash run start", err)
	}
	var replay RunMutationResult
	if found, err := s.lookupIdempotencyBeforeEffects(ctx, key, "run.start", requestHash, &replay); err != nil {
		return RunMutationResult{}, err
	} else if found {
		return replay, nil
	}
	if _, err := s.ReconcileExpiredAssignments(ctx, workspaceIdentifier, correlationID+"-lease"); err != nil {
		return RunMutationResult{}, err
	}
	if _, err := s.ReconcileExpiredClaims(ctx, workspaceIdentifier, derivedCorrelationID(correlationID, "claims")); err != nil {
		return RunMutationResult{}, err
	}

	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return RunMutationResult{}, storageFailure("begin run start", err)
	}
	defer tx.Rollback()
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
	var coordinationOverlapID string
	err = tx.QueryRowContext(ctx, "SELECT overlap_id FROM task_coordination_holds WHERE task_id = ? ORDER BY overlap_id LIMIT 1", task.ID).Scan(&coordinationOverlapID)
	if err == nil {
		return RunMutationResult{}, &Error{Code: CodeSchedulingPaused, Message: fmt.Sprintf("task %s scheduling is paused by open overlap %s", task.ID, coordinationOverlapID)}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RunMutationResult{}, storageFailure("check task coordination hold", err)
	}
	if command.reviewedPriorRunID == "" {
		if task.Status != domain.TaskAssigned || task.AssignmentID == "" {
			return RunMutationResult{}, &Error{Code: CodeRunConflict, Message: "run start requires a task with an active assignment"}
		}
	} else {
		prior, err := queryRun(ctx, tx, workspace.ID, command.reviewedPriorRunID)
		if err != nil {
			return RunMutationResult{}, err
		}
		var latestRunID string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM runs WHERE task_id=? ORDER BY created_at DESC,id DESC LIMIT 1", task.ID).Scan(&latestRunID); err != nil {
			return RunMutationResult{}, storageFailure("read latest reviewed run", err)
		}
		var activeAssignment int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_assignments WHERE id=? AND task_id=? AND agent_id=? AND status='active'`, task.AssignmentID, task.ID, task.AssignedAgentID).Scan(&activeAssignment); err != nil {
			return RunMutationResult{}, storageFailure("validate reviewed task assignment", err)
		}
		if prior.Revision != command.expectedReviewedRunRevision || prior.Status != domain.RunReview || prior.TaskID != task.ID || prior.AssignmentID != task.AssignmentID ||
			prior.AgentID != task.AssignedAgentID || prior.CheckoutID != checkoutIdentifier || prior.Runtime != runtimeName || prior.Provider != providerName || latestRunID != prior.ID ||
			task.Status != domain.TaskChangesRequested || task.AssignmentID == "" || activeAssignment != 1 {
			return RunMutationResult{}, &Error{Code: CodeRunConflict, Message: "reviewed run or retained task assignment changed before retry"}
		}
		task.Status, task.BlockedReason, task.Revision = domain.TaskAssigned, "", task.Revision+1
		now := s.nowText()
		if err := updateTaskState(ctx, tx, task, now); err != nil {
			return RunMutationResult{}, err
		}
		if _, err := appendEvent(ctx, tx, workspace.ID, "task", task.ID, task.Revision, taskAssigned, correlationID, now, map[string]any{
			"assignment_id": task.AssignmentID, "agent_id": task.AssignedAgentID, "prior_run_id": prior.ID, "reason": "owner retried requested changes",
		}); err != nil {
			return RunMutationResult{}, err
		}
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
	if err := s.enforceExecutionCapacity(ctx, tx, workspace.ID, task.ProjectID, providerName); err != nil {
		return RunMutationResult{}, err
	}
	var packet domain.ContextPacket
	if contextPacketID != "" {
		packet, err = queryContextPacket(ctx, tx, workspace.ID, contextPacketID)
		if err != nil {
			return RunMutationResult{}, err
		}
		// A delegated packet is only safe
		// when the store derives the run recipe from the exact grant-linked
		// launch profile. The public run.start command accepts a caller supplied
		// recipe, so it must never bind delegated authority.
		if packet.ManagementGrant != nil || packet.CheckWatchGrant != nil {
			return RunMutationResult{}, &Error{Code: CodeInvalidContext, Message: "delegated-authority context packets can only be launched through their explicit owner-authorized path"}
		}
		if packet.ProjectID != task.ProjectID || packet.TaskID != task.ID || packet.AgentID != agent.ID || packet.Task.Revision != task.Revision || packet.Role.Revision != agent.Revision {
			return RunMutationResult{}, &Error{Code: CodeInvalidContext, Message: "context packet no longer matches the task assignment or its revisions"}
		}
		if err := s.validateLiveContextPacketAgainstCanonical(ctx, tx, packet); err != nil {
			return RunMutationResult{}, err
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
	if task.ObjectiveID != "" {
		objective, err := queryObjective(ctx, tx, workspace.ID, task.ObjectiveID)
		if err != nil {
			return RunMutationResult{}, err
		}
		if objective.ProjectID != task.ProjectID {
			return RunMutationResult{}, &Error{Code: CodeInvalidContext, Message: "task objective is outside the task project"}
		}
		if objective.PrimaryCheckoutID != "" && checkout.ID != objective.PrimaryCheckoutID {
			return RunMutationResult{}, &Error{Code: CodePlacementUnavailable, Message: fmt.Sprintf("task belongs to workstream %s bound to checkout %s; requested checkout %s is outside that workstream", objective.ID, objective.PrimaryCheckoutID, checkout.ID)}
		}
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
	} else if checkWatchGrantID != "" {
		grant, err := queryContextCheckWatchGrantAuthority(ctx, tx, workspace.ID, checkWatchGrantID)
		if err != nil {
			return RunMutationResult{}, err
		}
		if grant.Revision != command.ExpectedCheckWatchGrantRevision {
			return RunMutationResult{}, revisionConflict("check-watch grant", grant.ID, command.ExpectedCheckWatchGrantRevision, grant.Revision)
		}
		packet, _, err = s.buildCheckWatchContextPacketInTransaction(ctx, tx, workspace.ID, task, agent, checkout, grant, correlationID+"-context", s.nowText())
		if err != nil {
			return RunMutationResult{}, err
		}
		contextPacketID = packet.ID
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
		AssignmentID: task.AssignmentID,
		AgentID:      agent.ID, CheckoutID: checkout.ID, ContextPacketID: contextPacketID, Runtime: runtimeName, Provider: providerName,
		ScenarioName: command.Scenario.Name, Status: domain.RunRequested, Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
		Placement: domain.RunPlacement{TaskID: task.ID, AgentID: agent.ID, CheckoutID: checkout.ID, CheckoutPath: checkout.Path, WriteMode: checkout.WriteMode, Runtime: runtimeName, Provider: providerName, Reasons: reasons},
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO runs(id, workspace_id, project_id, task_id, agent_id, checkout_id, runtime, provider,
    scenario_name, scenario_json, placement_reasons_json, status, step_cursor, revision,
    created_at, updated_at, created_by, updated_by, assignment_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 1, ?, ?, ?, ?, ?)`,
		run.ID, run.WorkspaceID, run.ProjectID, run.TaskID, run.AgentID, run.CheckoutID,
		run.Runtime, run.Provider, run.ScenarioName, string(scenarioJSON), string(reasonsJSON),
		run.Status, now, now, run.CreatedBy, run.UpdatedBy, run.AssignmentID); err != nil {
		return RunMutationResult{}, storageFailure("insert run projection", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_context_bindings(run_id, context_packet_id, bound_at) VALUES (?, ?, ?)", run.ID, contextPacketID, now); err != nil {
		return RunMutationResult{}, storageFailure("bind run context packet", err)
	}
	if err := dbgen.New(tx).InsertRunContextDeltaState(ctx, dbgen.InsertRunContextDeltaStateParams{
		RunID: run.ID, ContextPacketID: contextPacketID, ScanEventSequence: packet.AsOfEventSequence,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return RunMutationResult{}, storageFailure("initialize run context delta state", err)
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
	clearRunRuntimeProjection(&detail.Run)
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

type runJobClass string

const (
	runJobLaunch  runJobClass = "launch"
	runJobControl runJobClass = "control"
)

// RecoverRunJobLeases releases every run-job lease owned by a previous daemon
// session. Callers must hold the daemon's exclusive data-directory lock. A
// bound starting/active/stopping operation retains its exact runtime binding;
// only the abandoned queue lease is made immediately claimable so the new
// daemon reconciles rather than waiting for the wall-clock lease to expire.
func (s *Store) RecoverRunJobLeases(ctx context.Context) error {
	now := s.nowText()
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin run-job lease recovery", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE run_jobs
SET status='pending',lease_expires_at=NULL,updated_at=?
WHERE status='leased'`, now); err != nil {
		return storageFailure("recover run-job leases", err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit run-job lease recovery", err)
	}
	return nil
}

func (s *Store) ClaimRunLaunchJob(ctx context.Context, lease time.Duration) (RunWork, bool, error) {
	return s.claimRunJob(ctx, lease, runJobLaunch)
}

func (s *Store) ClaimRunControlJob(ctx context.Context, lease time.Duration) (RunWork, bool, error) {
	return s.claimRunJob(ctx, lease, runJobControl)
}

func (s *Store) claimRunJob(ctx context.Context, lease time.Duration, class runJobClass) (RunWork, bool, error) {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	statusPredicate := "(run.status = 'requested' OR (run.status = 'starting' AND NOT EXISTS(SELECT 1 FROM run_runtime_bindings binding WHERE binding.run_id=run.id)))"
	if class == runJobControl {
		statusPredicate = "((run.status = 'starting' AND EXISTS(SELECT 1 FROM run_runtime_bindings binding WHERE binding.run_id=run.id)) OR run.status IN ('active','stopping'))"
	}
	tx, err := s.beginTx(ctx, nil)
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
	err = tx.QueryRowContext(ctx, `SELECT job.run_id
FROM run_jobs job
JOIN runs run ON run.id = job.run_id
WHERE job.status = 'pending' AND julianday(job.available_at) <= julianday(?) AND `+statusPredicate+`
ORDER BY job.available_at, job.run_id LIMIT 1`, now).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return RunWork{}, false, storageFailure("commit empty run job claim", err)
		}
		return RunWork{}, false, nil
	}
	if err != nil {
		return RunWork{}, false, storageFailure("select run job", err)
	}
	var jobOrigin string
	if err := tx.QueryRowContext(ctx, "SELECT origin FROM run_jobs WHERE run_id = ?", runID).Scan(&jobOrigin); err != nil {
		return RunWork{}, false, storageFailure("read run job origin", err)
	}
	if jobOrigin == "supervisor" {
		var validReceipt int
		if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM run_scheduling_receipts receipt
    JOIN runs run ON run.id = receipt.run_id
    JOIN run_jobs job ON job.run_id=run.id
    JOIN scheduling_intents intent ON intent.id = receipt.intent_id
    JOIN supervisor_actions action ON action.id = receipt.action_id
    JOIN task_assignments assignment ON assignment.id=receipt.assignment_id
    JOIN launch_profiles profile ON profile.id=receipt.launch_profile_id
    JOIN run_context_bindings binding ON binding.run_id=run.id
    JOIN context_packets packet ON packet.id=binding.context_packet_id
    WHERE receipt.run_id = ? AND receipt.workspace_id=run.workspace_id
      AND job.origin='supervisor' AND run.assignment_id = receipt.assignment_id
      AND intent.run_id = run.id AND intent.assignment_id = receipt.assignment_id
      AND intent.supervisor_action_id = action.id AND intent.status = 'run_requested'
      AND action.intent_id = intent.id AND action.run_id=run.id
      AND action.response='schedule' AND action.status = 'applied'
      AND intent.launch_profile_id=receipt.launch_profile_id
      AND assignment.task_id=run.task_id AND assignment.agent_id=run.agent_id
      AND json_extract(action.constraint_snapshot_json,'$.launch_profile.id')=receipt.launch_profile_id
      AND json_extract(action.constraint_snapshot_json,'$.launch_profile.revision')=receipt.launch_profile_revision
      AND profile.agent_id=run.agent_id AND profile.runtime=run.runtime AND profile.provider=run.provider
      AND (profile.checkout_id IS NULL OR profile.checkout_id=run.checkout_id)
      AND lower(hex(sha256(CAST(run.scenario_json AS BLOB))))=profile.scenario_sha256
      AND json_extract(packet.packet_json,'$.workspace_id')=run.workspace_id
      AND json_extract(packet.packet_json,'$.project_id')=run.project_id
      AND json_extract(packet.packet_json,'$.task_id')=run.task_id
      AND json_extract(packet.packet_json,'$.agent_id')=run.agent_id
      AND json_extract(packet.packet_json,'$.checkout_id')=run.checkout_id
      AND json_extract(packet.packet_json,'$.task.assignment_id')=run.assignment_id
	  AND EXISTS (SELECT 1 FROM events event WHERE event.workspace_id=run.workspace_id
	    AND event.entity_type='run' AND event.entity_id=run.id AND event.entity_revision=run.revision
	    AND ((run.status='requested' AND event.type='run.requested')
	      OR (run.status='starting' AND NOT EXISTS(SELECT 1 FROM run_runtime_bindings runtime_binding WHERE runtime_binding.run_id=run.id) AND event.type='run.starting')
	      OR (run.status='starting' AND EXISTS(SELECT 1 FROM run_runtime_bindings runtime_binding WHERE runtime_binding.run_id=run.id) AND event.type='run.runtime_observed'
	        AND json_extract(event.data_json,'$.runtime_bound')=1)
	      OR (run.status='active' AND event.type IN ('run.started','run.progress_reported','run.resumed'))
	      OR (run.status='stopping' AND event.type='run.stop_requested')))
)
OR EXISTS (
    SELECT 1 FROM run_retry_receipts receipt
    JOIN runs run ON run.id=receipt.run_id
    JOIN runs prior ON prior.id=receipt.prior_run_id
    JOIN run_jobs job ON job.run_id=run.id
    JOIN scheduling_intents intent ON intent.id=receipt.intent_id
    JOIN supervisor_actions action ON action.id=receipt.action_id
    JOIN task_assignments assignment ON assignment.id=receipt.assignment_id
    JOIN launch_profiles profile ON profile.id=receipt.launch_profile_id
    JOIN run_context_bindings binding ON binding.run_id=run.id
    JOIN context_packets packet ON packet.id=binding.context_packet_id
    WHERE receipt.run_id=? AND receipt.workspace_id=run.workspace_id AND job.origin='supervisor'
      AND run.assignment_id=receipt.assignment_id AND prior.workspace_id=run.workspace_id
      AND prior.project_id=run.project_id AND prior.task_id=run.task_id
      AND prior.agent_id=run.agent_id AND prior.checkout_id=run.checkout_id
      AND prior.runtime=run.runtime AND prior.provider=run.provider
      AND action.prior_run_id=prior.id AND action.run_id=run.id
      AND action.task_id=run.task_id AND action.agent_id=run.agent_id
      AND action.response='retry_task' AND action.status='applied'
      AND intent.id=receipt.intent_id AND intent.task_id=run.task_id AND intent.agent_id=run.agent_id
      AND intent.launch_profile_id=receipt.launch_profile_id
      AND assignment.task_id=run.task_id AND assignment.agent_id=run.agent_id
      AND json_extract(action.constraint_snapshot_json,'$.launch_profile_id')=receipt.launch_profile_id
      AND json_extract(action.constraint_snapshot_json,'$.launch_profile_revision')=receipt.launch_profile_revision
      AND profile.agent_id=run.agent_id AND profile.runtime=run.runtime AND profile.provider=run.provider
      AND (profile.checkout_id IS NULL OR profile.checkout_id=run.checkout_id)
      AND lower(hex(sha256(CAST(run.scenario_json AS BLOB))))=profile.scenario_sha256
      AND json_extract(packet.packet_json,'$.schema')='urn:crewfold:schema:domain:context-packet:v1'
      AND json_extract(packet.packet_json,'$.workspace_id')=run.workspace_id
      AND json_extract(packet.packet_json,'$.project_id')=run.project_id
      AND json_extract(packet.packet_json,'$.task_id')=run.task_id
      AND json_extract(packet.packet_json,'$.agent_id')=run.agent_id
      AND json_extract(packet.packet_json,'$.checkout_id')=run.checkout_id
      AND json_extract(packet.packet_json,'$.task.assignment_id')=run.assignment_id
	  AND EXISTS (SELECT 1 FROM events event WHERE event.workspace_id=run.workspace_id
	    AND event.entity_type='run' AND event.entity_id=run.id AND event.entity_revision=run.revision
	    AND ((run.status='requested' AND event.type='run.requested')
	      OR (run.status='starting' AND NOT EXISTS(SELECT 1 FROM run_runtime_bindings runtime_binding WHERE runtime_binding.run_id=run.id) AND event.type='run.starting')
	      OR (run.status='starting' AND EXISTS(SELECT 1 FROM run_runtime_bindings runtime_binding WHERE runtime_binding.run_id=run.id) AND event.type='run.runtime_observed'
	        AND json_extract(event.data_json,'$.runtime_bound')=1)
	      OR (run.status='active' AND event.type IN ('run.started','run.progress_reported','run.resumed'))
	      OR (run.status='stopping' AND event.type='run.stop_requested')))
)`, runID, runID).Scan(&validReceipt); err != nil {
			return RunWork{}, false, storageFailure("validate supervisor scheduling receipt", err)
		}
		if validReceipt != 1 {
			return RunWork{}, false, &Error{Code: CodeRunConflict, Message: "supervisor run job has no exact scheduling receipt"}
		}
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
		if run.Status != domain.RunRequested && run.Status != domain.RunStarting {
			return 0, &Error{Code: CodeRunConflict, Message: "only a requested run can start"}
		}
		// This transaction is the final durable boundary before an external
		// runtime may be launched. Scheduling and owner commands can race with a
		// later task decision, so revalidate the exact live assignment rather than
		// trusting the run's immutable placement receipt alone.
		task, err := queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
		if err != nil {
			return 0, err
		}
		var assignmentCurrent int
		if task.Status != domain.TaskAssigned || task.AssignmentID != run.AssignmentID || task.AssignedAgentID != run.AgentID {
			return 0, &Error{Code: CodeRunConflict, Message: "run task no longer retains its exact active assignment"}
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM task_assignments
WHERE id=? AND task_id=? AND agent_id=? AND status='active'
)`, run.AssignmentID, run.TaskID, run.AgentID).Scan(&assignmentCurrent); err != nil {
			return 0, storageFailure("revalidate run launch assignment", err)
		}
		if assignmentCurrent != 1 {
			return 0, &Error{Code: CodeRunConflict, Message: "run task no longer retains its exact active assignment"}
		}
		if run.Status == domain.RunStarting {
			return 0, nil
		}
		run.Status = domain.RunStarting
		run.Revision++
		if err := updateRunProjectionForActor(ctx, tx, *run, now, runWorkerActorID); err != nil {
			return 0, err
		}
		if err := setRunJob(ctx, tx, run.ID, "pending", now); err != nil {
			return 0, err
		}
		if err := appendRunTimeline(ctx, tx, run.ID, runStartingEvent, "runtime launch is starting", nil, now); err != nil {
			return 0, err
		}
		return appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runStartingEvent, correlationID, now, runWorkerActorID, domain.EventActorSubsystem, map[string]any{})
	})
}

func (s *Store) MarkRunStarted(ctx context.Context, runID, runtimeHandle, providerHandle, correlationID string) (domain.RunDetail, error) {
	runtimeHandle, providerHandle = strings.TrimSpace(runtimeHandle), strings.TrimSpace(providerHandle)
	if runtimeHandle == "" || providerHandle == "" {
		return domain.RunDetail{}, &Error{Code: CodeInvalidRun, Message: "run start requires runtime and provider handles"}
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.RunDetail{}, storageFailure("begin run started transition", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", runID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if run.Status == domain.RunActive && run.RuntimeHandle == runtimeHandle && run.ProviderHandle == providerHandle && s.runBindingIsCurrent(run) {
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
	if run.RuntimeHandle == "" {
		if err := s.insertRunRuntimeBinding(ctx, tx, &run, runtimeHandle, providerHandle, now); err != nil {
			return domain.RunDetail{}, err
		}
	} else {
		if run.RuntimeHandle != runtimeHandle || !s.runBindingIsCurrent(run) {
			return domain.RunDetail{}, runtimeBindingConflict("run", run.ID)
		}
		if run.ProviderHandle == "" {
			if err := s.bindRunProvider(ctx, tx, &run, providerHandle, now); err != nil {
				return domain.RunDetail{}, err
			}
		} else if run.ProviderHandle != providerHandle {
			return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "provider binding cannot change"}
		}
	}
	run.Status = domain.RunActive
	run.StartedAt = now
	run.Revision++
	if err := updateRunProjectionForActor(ctx, tx, run, now, runWorkerActorID); err != nil {
		return domain.RunDetail{}, err
	}
	task, err := queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if task.Status != domain.TaskAssigned {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "run task is no longer assigned"}
	}
	isExecutive := ownerExecutiveRunInTransaction(ctx, tx, run.ID)
	if !isExecutive {
		task.Status, task.BlockedReason, task.Revision = domain.TaskActive, "", task.Revision+1
		if err := updateTaskStateForActor(ctx, tx, task, now, runWorkerActorID); err != nil {
			return domain.RunDetail{}, err
		}
	}
	if err := setRunJob(ctx, tx, run.ID, "pending", now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runStartedEvent, "runtime and provider are bound", nil, now); err != nil {
		return domain.RunDetail{}, err
	}
	if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runStartedEvent, correlationID, now, runWorkerActorID, domain.EventActorSubsystem, map[string]any{"runtime_bound": true, "provider_bound": true}); err != nil {
		return domain.RunDetail{}, err
	}
	if !isExecutive {
		if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskStarted, correlationID, now, runWorkerActorID, domain.EventActorSubsystem, map[string]any{"run_id": run.ID, "status": task.Status}); err != nil {
			return domain.RunDetail{}, err
		}
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
		if run.RuntimeHandle == runtimeHandle && s.runBindingIsCurrent(*run) {
			return 0, nil
		}
		if run.RuntimeHandle != "" {
			return 0, &Error{Code: CodeRunConflict, Message: "runtime binding cannot replace an existing handle"}
		}
		if err := s.insertRunRuntimeBinding(ctx, tx, run, runtimeHandle, "", now); err != nil {
			return 0, err
		}
		run.Revision++
		if err := updateRunProjectionForActor(ctx, tx, *run, now, runWorkerActorID); err != nil {
			return 0, err
		}
		if err := setRunJob(ctx, tx, run.ID, "pending", now); err != nil {
			return 0, err
		}
		return appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runRuntimeObservedEvent, correlationID, now, runWorkerActorID, domain.EventActorSubsystem, map[string]any{"runtime_bound": true})
	})
}

func (s *Store) FailRunStart(ctx context.Context, runID, message, correlationID string) (domain.RunDetail, error) {
	message = boundedRunText(message, 1024)
	if message == "" {
		message = "runtime failed to start"
	}
	tx, err := s.beginTx(ctx, nil)
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
	if err := deleteRunRuntimeBinding(ctx, tx, run.ID); err != nil {
		return domain.RunDetail{}, err
	}
	clearRunRuntimeProjection(&run)
	run.Revision++
	if err := updateRunProjectionForActor(ctx, tx, run, now, runWorkerActorID); err != nil {
		return domain.RunDetail{}, err
	}
	if err := setRunJob(ctx, tx, run.ID, "complete", now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runStartFailedEvent, message, nil, now); err != nil {
		return domain.RunDetail{}, err
	}
	eventSequence, err := appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runStartFailedEvent, correlationID, now, runWorkerActorID, domain.EventActorSubsystem, map[string]any{"code": run.FailureCode, "message": message, "logs_available": false, "logs_unavailable_reason": "runtime did not produce a trustworthy terminal capture"})
	if err != nil {
		return domain.RunDetail{}, err
	}
	isExecutive, err := failOwnerExecutiveRunInTransaction(ctx, tx, run.ID, message, now)
	if err != nil {
		return domain.RunDetail{}, err
	}
	// A start failure may keep its accepted intent open only while the current
	// supervisor policy can still authorize a bounded retry. In particular, a
	// disabled policy has no worker that could later close the intent, so leaving
	// it run_requested would permanently prevent an owner from replacing it.
	policy, err := querySupervisorPolicy(ctx, tx, run.WorkspaceID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	retryAuthorized := policy.Enabled && policy.AutoRetryLimit > 0
	if retryAuthorized {
		_, retryAuthorized, err = s.supervisorRetryAuthorityForRun(ctx, tx, policy, run, now)
		if err != nil {
			return domain.RunDetail{}, err
		}
	}
	if !retryAuthorized {
		if err := terminalizeSchedulingIntentForRun(ctx, tx, run, "definite start failure has no enabled authorized retry", correlationID, now); err != nil {
			return domain.RunDetail{}, err
		}
		if !isExecutive {
			if err := enqueueOwnerManagerReview(ctx, tx, run.WorkspaceID, run.ProjectID, eventSequence, now); err != nil {
				return domain.RunDetail{}, err
			}
		}
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
	if observation.Kind == domain.ObservationCompletion || observation.Kind == domain.ObservationExecutiveResponse {
		if err := s.validateTerminalLogOutcome(strings.TrimSpace(runID), observation.LogArchive, observation.LogUnavailableReason); err != nil {
			return domain.RunDetail{}, err
		}
	} else if observation.LogArchive != nil || strings.TrimSpace(observation.LogUnavailableReason) != "" {
		return domain.RunDetail{}, &Error{Code: CodeInvalidRun, Message: "only a completion observation can carry terminal logs"}
	}
	tx, err := s.beginTx(ctx, nil)
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
	detail.Assessment = strings.TrimSpace(observation.Assessment)
	if err := tx.Commit(); err != nil {
		return domain.RunDetail{}, storageFailure("commit run observation", err)
	}
	return detail, nil
}

// ApplyQueuedRunReport applies a capability-submitted report and marks that exact
// report consumed in the same transaction as the run/task state transition.
func (s *Store) ApplyQueuedRunReport(ctx context.Context, runID, reportID string, accepted bool, missing []string, archive *domain.RunLogArchive, logsUnavailableReason, correlationID string) (domain.RunDetail, error) {
	tx, err := s.beginTx(ctx, nil)
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
	observation := domain.RunObservation{Kind: report.Kind, Message: report.Message, Evidence: append([]string(nil), report.Evidence...), Handoff: report.Handoff, Assessment: report.Assessment, LogArchive: archive, LogUnavailableReason: strings.TrimSpace(logsUnavailableReason)}
	if observation.Kind == domain.ObservationCompletion || observation.Kind == domain.ObservationExecutiveResponse {
		if err := s.validateTerminalLogOutcome(report.RunID, observation.LogArchive, observation.LogUnavailableReason); err != nil {
			return domain.RunDetail{}, err
		}
	} else if observation.LogArchive != nil || observation.LogUnavailableReason != "" {
		return domain.RunDetail{}, &Error{Code: CodeInvalidRun, Message: "only a terminal report can carry terminal logs"}
	}
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
	// applyRunObservationInTransaction materializes the detail before this exact
	// report is marked applied. Preserve the already-validated structured
	// assessment in the returned read model; subsequent run.show calls recover
	// the same value from the applied report row.
	detail.Assessment = observation.Assessment
	if err := tx.Commit(); err != nil {
		return domain.RunDetail{}, storageFailure("commit queued run report", err)
	}
	return detail, nil
}

func (s *Store) applyRunObservationInTransaction(ctx context.Context, tx *sql.Tx, run *domain.Run, observation domain.RunObservation, accepted bool, missing []string, correlationID, now string) (domain.RunDetail, error) {
	if run.Status != domain.RunActive {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "observations require an active run"}
	}
	isExecutive := ownerExecutiveRunInTransaction(ctx, tx, run.ID)
	if isExecutive != (observation.Kind == domain.ObservationExecutiveResponse) {
		return domain.RunDetail{}, &Error{Code: CodeInvalidRun, Message: "project executive runs accept only their exact typed owner response"}
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
		if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runProgressEvent, correlationID, now, run.ID, domain.EventActorAgentRun, map[string]any{"message": observation.Message, "evidence": observation.Evidence}); err != nil {
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
		if err := updateTaskStateForActor(ctx, tx, task, now, run.ID); err != nil {
			return domain.RunDetail{}, err
		}
		if err := appendRunTimeline(ctx, tx, run.ID, runBlockedEvent, question, observation.Evidence, now); err != nil {
			return domain.RunDetail{}, err
		}
		if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runBlockedEvent, correlationID, now, run.ID, domain.EventActorAgentRun, map[string]any{"question": question}); err != nil {
			return domain.RunDetail{}, err
		}
		if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskBlocked, correlationID, now, run.ID, domain.EventActorAgentRun, map[string]any{"run_id": run.ID, "reason": question, "status": task.Status}); err != nil {
			return domain.RunDetail{}, err
		}
	case domain.ObservationCompletion:
		if err := applyCompletionObservation(ctx, tx, run, observation, accepted, missing, correlationID, now); err != nil {
			return domain.RunDetail{}, err
		}
		if err := deleteRunRuntimeBinding(ctx, tx, run.ID); err != nil {
			return domain.RunDetail{}, err
		}
		clearRunRuntimeProjection(run)
		jobStatus = "complete"
	case domain.ObservationExecutiveResponse:
		if err := applyOwnerExecutiveResponseObservation(ctx, tx, run, observation, correlationID, now); err != nil {
			return domain.RunDetail{}, err
		}
		if err := deleteRunRuntimeBinding(ctx, tx, run.ID); err != nil {
			return domain.RunDetail{}, err
		}
		clearRunRuntimeProjection(run)
		jobStatus = "complete"
	default:
		return domain.RunDetail{}, &Error{Code: CodeInvalidRun, Message: fmt.Sprintf("unsupported observation kind %q", observation.Kind)}
	}
	if err := updateRunProjectionForActor(ctx, tx, *run, now, run.ID); err != nil {
		return domain.RunDetail{}, err
	}
	if run.Status == domain.RunReview || run.Status == domain.RunCompleted {
		if observation.LogArchive != nil {
			if err := s.insertRunLogArchive(ctx, tx, run.ID, now, observation.LogArchive); err != nil {
				return domain.RunDetail{}, err
			}
		}
	}
	if run.Status == domain.RunCompleted && !ownerExecutiveRunInTransaction(ctx, tx, run.ID) {
		// The raw reservation guard must observe the run's definite terminal
		// projection before its exact assignment can be released.
		if _, err := tx.ExecContext(ctx, "UPDATE task_assignments SET status = 'released', revision = revision + 1, updated_at = ?, updated_by = ? WHERE task_id = ? AND status = 'active'", now, run.ID, run.TaskID); err != nil {
			return domain.RunDetail{}, storageFailure("release completed task assignment", err)
		}
	}
	if err := terminalizeSchedulingIntentForRun(ctx, tx, *run, run.ResultSummary, correlationID, now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := setRunJob(ctx, tx, run.ID, jobStatus, now); err != nil {
		return domain.RunDetail{}, err
	}
	if !isExecutive {
		// The project executive must observe the applied canonical outcome, not
		// race the run worker after a report has merely arrived. Queue the review
		// at the newest event produced by this same state-transition transaction.
		var appliedEventSequence int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?`, run.WorkspaceID).Scan(&appliedEventSequence); err != nil {
			return domain.RunDetail{}, storageFailure("read applied worker report event cut", err)
		}
		if err := enqueueOwnerManagerReview(ctx, tx, run.WorkspaceID, run.ProjectID, appliedEventSequence, now); err != nil {
			return domain.RunDetail{}, err
		}
	}
	detail, err := runDetailInTransaction(ctx, tx, *run)
	if err != nil {
		return domain.RunDetail{}, err
	}
	return detail, nil
}

func applyOwnerExecutiveResponseObservation(ctx context.Context, tx *sql.Tx, run *domain.Run, observation domain.RunObservation, correlationID, now string) error {
	var exchangeStatus, turnStatus string
	err := tx.QueryRowContext(ctx, `SELECT exchange.status,turn.status FROM owner_executive_exchanges exchange JOIN owner_turns turn ON turn.id=exchange.turn_id WHERE exchange.run_id=?`, run.ID).Scan(&exchangeStatus, &turnStatus)
	if err != nil || exchangeStatus != "responded" || turnStatus != "completed" {
		return &Error{Code: CodeOwnerTurnConflict, Message: "owner executive terminal report has no exact completed response", Cause: err}
	}
	summary := strings.TrimSpace(observation.Message)
	if summary == "" {
		summary = "Executive response recorded"
	}
	run.Status, run.ResultSummary, run.FinishedAt = domain.RunCompleted, summary, now
	if err := appendRunTimeline(ctx, tx, run.ID, runCompletedEvent, summary, nil, now); err != nil {
		return err
	}
	_, err = appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runCompletedEvent, correlationID, now, run.ID, domain.EventActorAgentRun, mergeEventData(map[string]any{"summary": summary, "executive_exchange": true}, terminalLogEventData(observation.LogArchive, observation.LogUnavailableReason)))
	return err
}

func ownerExecutiveRunInTransaction(ctx context.Context, tx *sql.Tx, runID string) bool {
	var count int
	return tx.QueryRowContext(ctx, `SELECT count(*) FROM owner_executive_exchanges WHERE run_id=?`, runID).Scan(&count) == nil && count == 1
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
	run.Status, run.ResultSummary, run.FinishedAt = domain.RunReview, summary, now
	clearRunRuntimeProjection(run)
	task.Status, task.BlockedReason, task.Revision = domain.TaskReview, "", task.Revision+1
	if err := appendRunTimeline(ctx, tx, run.ID, runCompletionProposedEvent, summary, observation.Evidence, now); err != nil {
		return err
	}
	if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runCompletionProposedEvent, correlationID, now, run.ID, domain.EventActorAgentRun, mergeEventData(map[string]any{"summary": summary, "evidence": observation.Evidence}, terminalLogEventData(observation.LogArchive, observation.LogUnavailableReason))); err != nil {
		return err
	}
	if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskCompletionProposed, correlationID, now, run.ID, domain.EventActorAgentRun, map[string]any{"run_id": run.ID, "summary": summary, "evidence": observation.Evidence}); err != nil {
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
		if _, err := tx.ExecContext(ctx, "INSERT INTO run_handoffs(id, run_id, task_id, summary, evidence_json, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?)", handoffID, run.ID, task.ID, handoffSummary, string(evidenceJSON), now, run.ID); err != nil {
			return storageFailure("insert run handoff", err)
		}
		// Structured assessment describes the reviewed deliverable, not whether
		// the reviewer completed its own task. PASS, BLOCK, and CHANGES_REQUESTED
		// all finish an accepted review handoff. A downstream remediation edge
		// consumes the immutable findings; without one, delivery remains blocked.
		run.Status, run.FinishedAt = domain.RunCompleted, now
		task.Status, task.Revision = domain.TaskCompleted, task.Revision+1
		run.Revision++
		if err := appendRunTimeline(ctx, tx, run.ID, taskHandoffRecorded, handoffSummary, observation.Evidence, now); err != nil {
			return err
		}
		completionKind := runCompletedEvent
		if err := appendRunTimeline(ctx, tx, run.ID, completionKind, structuredAssessmentSummary(observation.Assessment, summary), observation.Evidence, now); err != nil {
			return err
		}
		if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskHandoffRecorded, correlationID, now, run.ID, domain.EventActorAgentRun, map[string]any{"run_id": run.ID, "handoff_id": handoffID, "summary": handoffSummary}); err != nil {
			return err
		}
		taskEvent := taskCompletedEvent
		if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskEvent, correlationID, now, run.ID, domain.EventActorAgentRun, map[string]any{"run_id": run.ID, "status": task.Status, "assessment": observation.Assessment}); err != nil {
			return err
		}
		if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, completionKind, correlationID, now, run.ID, domain.EventActorAgentRun, mergeEventData(map[string]any{"task_id": task.ID, "handoff_id": handoffID, "assessment": observation.Assessment}, terminalLogEventData(observation.LogArchive, observation.LogUnavailableReason))); err != nil {
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
		if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskChangesRequestedEvent, correlationID, now, run.ID, domain.EventActorAgentRun, map[string]any{"run_id": run.ID, "reason": reason, "missing_evidence": missing}); err != nil {
			return err
		}
	}
	return updateTaskStateForActor(ctx, tx, task, now, run.ID)
}

func structuredAssessmentSummary(assessment, summary string) string {
	if assessment == "" {
		return summary
	}
	return strings.ToUpper(assessment) + ": " + summary
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
	requestHash, err := runResumeRequestHash(workspaceIdentifier, runID, command.ExpectedRevision)
	if err != nil {
		return RunMutationResult{}, storageFailure("hash run resume", err)
	}
	var replay RunMutationResult
	if found, err := s.lookupIdempotencyBeforeEffects(ctx, key, "run.resume", requestHash, &replay); err != nil {
		return RunMutationResult{}, err
	} else if found {
		return replay, nil
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return RunMutationResult{}, storageFailure("begin run resume", err)
	}
	defer tx.Rollback()
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
	if !s.runBindingIsCurrent(run) {
		return RunMutationResult{}, runtimeBindingUnavailable("run", run.ID)
	}
	// Reconcile elapsed assignments only after the target control authority has
	// been proved, and in this same transaction. A rejected foreign control must
	// not mutate unrelated assignment or event state as a side effect.
	if _, err := expireAssignmentsInTransaction(ctx, tx, workspace.ID, s.clock().UTC(), correlationID+"-lease"); err != nil {
		return RunMutationResult{}, err
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
	clearRunRuntimeProjection(&detail.Run)
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
	requestHash, err := runStopRequestHash(workspaceIdentifier, runID, command.ExpectedRevision, command.GracePeriodMillis)
	if err != nil {
		return RunMutationResult{}, storageFailure("hash run stop", err)
	}
	tx, err := s.beginTx(ctx, nil)
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
	if !s.runBindingIsCurrent(run) {
		return RunMutationResult{}, runtimeBindingUnavailable("run", run.ID)
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
	clearRunRuntimeProjection(&detail.Run)
	result := RunMutationResult{Detail: detail, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "run.stop", requestHash, result, now); err != nil {
		return RunMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunMutationResult{}, storageFailure("commit run stop", err)
	}
	return result, nil
}

func (s *Store) MarkRunStopped(ctx context.Context, runID string, forced bool, diagnostic string, archive *domain.RunLogArchive, logsUnavailableReason, correlationID string) (domain.RunDetail, error) {
	if err := s.validateTerminalLogOutcome(strings.TrimSpace(runID), archive, logsUnavailableReason); err != nil {
		return domain.RunDetail{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.RunDetail{}, storageFailure("begin run stopped transition", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", strings.TrimSpace(runID))
	if err != nil {
		return domain.RunDetail{}, err
	}
	if run.Status == domain.RunStopped {
		matches, matchErr := s.terminalRunLogOutcomeReplayMatches(ctx, tx, run, archive, logsUnavailableReason)
		if matchErr != nil {
			return domain.RunDetail{}, matchErr
		}
		if !matches {
			return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "stopped run log outcome replay differs"}
		}
		return runDetailInTransaction(ctx, tx, run)
	}
	if run.Status != domain.RunStopping {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "only a stopping run can become stopped"}
	}
	now := s.nowText()
	run.Status, run.StopGraceMillis, run.StopForced, run.ResultSummary, run.FinishedAt, run.Revision = domain.RunStopped, 0, forced, strings.TrimSpace(diagnostic), now, run.Revision+1
	if err := deleteRunRuntimeBinding(ctx, tx, run.ID); err != nil {
		return domain.RunDetail{}, err
	}
	clearRunRuntimeProjection(&run)
	if err := updateRunProjectionForActor(ctx, tx, run, now, runWorkerActorID); err != nil {
		return domain.RunDetail{}, err
	}
	if archive != nil {
		if err := s.insertRunLogArchive(ctx, tx, run.ID, now, archive); err != nil {
			return domain.RunDetail{}, err
		}
	}
	isExecutive, err := failOwnerExecutiveRunInTransaction(ctx, tx, run.ID, "project executive run was stopped before responding", now)
	if err != nil {
		return domain.RunDetail{}, err
	}
	var task domain.Task
	if !isExecutive {
		task, err = queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
		if err != nil {
			return domain.RunDetail{}, err
		}
		if task.AssignmentID == "" || task.AssignedAgentID != run.AgentID {
			return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "stopped run lost its task assignment"}
		}
		task.Status, task.BlockedReason, task.Revision = domain.TaskAssigned, "", task.Revision+1
		if err := updateTaskStateForActor(ctx, tx, task, now, runWorkerActorID); err != nil {
			return domain.RunDetail{}, err
		}
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
	if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runStoppedEvent, correlationID, now, runWorkerActorID, domain.EventActorSubsystem, mergeEventData(map[string]any{"forced": forced, "diagnostic": message}, terminalLogEventData(archive, logsUnavailableReason))); err != nil {
		return domain.RunDetail{}, err
	}
	if !isExecutive {
		if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskRunStoppedEvent, correlationID, now, runWorkerActorID, domain.EventActorSubsystem, map[string]any{"run_id": run.ID, "status": task.Status}); err != nil {
			return domain.RunDetail{}, err
		}
	}
	if err := terminalizeSchedulingIntentForRun(ctx, tx, run, message, correlationID, now); err != nil {
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
	tx, err := s.beginTx(ctx, nil)
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
	message = boundedRunText(message, 1024)
	run.Status, run.FailureCode, run.FailureMessage, run.Revision = domain.RunLost, "runtime_state_unknown", message, run.Revision+1
	if err := updateRunProjectionForActor(ctx, tx, run, now, runWorkerActorID); err != nil {
		return domain.RunDetail{}, err
	}
	isExecutive, err := failOwnerExecutiveRunInTransaction(ctx, tx, run.ID, message, now)
	if err != nil {
		return domain.RunDetail{}, err
	}
	var task domain.Task
	if !isExecutive {
		task, err = queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
		if err != nil {
			return domain.RunDetail{}, err
		}
		task.Status, task.BlockedReason, task.Revision = domain.TaskBlocked, message, task.Revision+1
		if err := updateTaskStateForActor(ctx, tx, task, now, runWorkerActorID); err != nil {
			return domain.RunDetail{}, err
		}
	}
	if err := setRunJob(ctx, tx, run.ID, "complete", now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runLostEvent, message, nil, now); err != nil {
		return domain.RunDetail{}, err
	}
	appliedEventSequence, err := appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runLostEvent, correlationID, now, runWorkerActorID, domain.EventActorSubsystem, map[string]any{"code": run.FailureCode, "message": message, "capacity_retained": true, "logs_available": false, "logs_unavailable_reason": "runtime identity or outcome is not trusted"})
	if err != nil {
		return domain.RunDetail{}, err
	}
	if !isExecutive {
		appliedEventSequence, err = appendEventForActor(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskBlocked, correlationID, now, runWorkerActorID, domain.EventActorSubsystem, map[string]any{"run_id": run.ID, "reason": message, "status": task.Status})
		if err != nil {
			return domain.RunDetail{}, err
		}
		if err := enqueueOwnerManagerReview(ctx, tx, run.WorkspaceID, run.ProjectID, appliedEventSequence, now); err != nil {
			return domain.RunDetail{}, err
		}
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

func (s *Store) ResolveLostRun(ctx context.Context, command ResolveLostRunCommand) (RunLossResolutionResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.RunID = strings.TrimSpace(command.RunID)
	command.Note = strings.TrimSpace(command.Note)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.RunID == "" || command.ExpectedRevision < 1 || !command.RuntimeRetiredConfirmed || !validMessageText(command.Note, 2048) {
		return RunLossResolutionResult{}, &Error{Code: CodeInvalidRun, Message: "lost-run resolution requires workspace, run, exact revision, a 1 to 2048 byte note, and explicit runtime retirement confirmation"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidRun); err != nil {
		return RunLossResolutionResult{}, err
	}
	requestHash, err := runLostResolveRequestHash(command)
	if err != nil {
		return RunLossResolutionResult{}, storageFailure("hash lost-run resolution", err)
	}
	var replay RunLossResolutionResult
	if found, err := s.lookupIdempotencyBeforeEffects(ctx, command.IdempotencyKey, "run.lost.resolve", requestHash, &replay); err != nil {
		return RunLossResolutionResult{}, err
	} else if found {
		return replay, nil
	}

	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return RunLossResolutionResult{}, storageFailure("begin lost-run resolution", err)
	}
	defer tx.Rollback()
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "run.lost.resolve", requestHash, &replay); err != nil {
		return RunLossResolutionResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return RunLossResolutionResult{}, err
	}
	run, err := queryRun(ctx, tx, workspace.ID, command.RunID)
	if err != nil {
		return RunLossResolutionResult{}, err
	}
	if run.Revision != command.ExpectedRevision {
		return RunLossResolutionResult{}, revisionConflict("run", run.ID, command.ExpectedRevision, run.Revision)
	}
	if run.Status != domain.RunLost {
		return RunLossResolutionResult{}, &Error{Code: CodeRunConflict, Message: "only a lost run can have its retired runtime resolved"}
	}
	task, err := queryTask(ctx, tx, workspace.ID, run.TaskID)
	if err != nil {
		return RunLossResolutionResult{}, err
	}
	isExecutive := ownerExecutiveRunInTransaction(ctx, tx, run.ID)
	validTaskStatus := task.Status == domain.TaskBlocked || isExecutive && task.Status == domain.TaskAssigned
	if !validTaskStatus || task.AssignmentID != run.AssignmentID || task.AssignedAgentID != run.AgentID {
		return RunLossResolutionResult{}, &Error{Code: CodeRunConflict, Message: "lost-run resolution requires its exact blocked task reservation"}
	}
	if !s.runLossResolutionActive.CompareAndSwap(false, true) {
		return RunLossResolutionResult{}, storageFailure("enter owner lost-run resolution", errors.New("lost-run resolution seal is already active"))
	}
	sealHeld := true
	defer func() {
		if sealHeld {
			s.runLossResolutionActive.Store(false)
		}
	}()

	now := s.nowText()
	lostRevision := run.Revision
	run.Status = domain.RunFailed
	run.FailureCode = "runtime_retired_by_owner"
	run.FailureMessage = command.Note
	if err := deleteRunRuntimeBinding(ctx, tx, run.ID); err != nil {
		return RunLossResolutionResult{}, err
	}
	clearRunRuntimeProjection(&run)
	run.FinishedAt = now
	run.Revision++
	if err := updateRunProjectionForActor(ctx, tx, run, now, localOwnerActorID); err != nil {
		return RunLossResolutionResult{}, err
	}
	if err := setRunJob(ctx, tx, run.ID, "complete", now); err != nil {
		return RunLossResolutionResult{}, err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runLostResolvedEvent, command.Note, nil, now); err != nil {
		return RunLossResolutionResult{}, err
	}
	sequence, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runLostResolvedEvent, command.CorrelationID, now, map[string]any{
		"prior_status":            domain.RunLost,
		"status":                  domain.RunFailed,
		"resolution":              runLossResolutionOwnerConfirmed,
		"lost_revision":           lostRevision,
		"note":                    command.Note,
		"capacity_released":       true,
		"binding_cleared":         true,
		"logs_available":          false,
		"logs_unavailable_reason": "runtime outcome was lost before owner retirement confirmation",
	})
	if err != nil {
		return RunLossResolutionResult{}, err
	}
	queries := dbgen.New(tx)
	if err := queries.InsertRunLossResolution(ctx, dbgen.InsertRunLossResolutionParams{
		RunID: run.ID, LostRevision: lostRevision, Note: command.Note, EventSequence: sequence, ResolvedAt: now,
	}); err != nil {
		return RunLossResolutionResult{}, storageFailure("insert lost-run resolution receipt", err)
	}

	if !isExecutive {
		assignmentResult, err := tx.ExecContext(ctx, `UPDATE task_assignments
SET status='released', revision=revision+1, updated_at=?, updated_by=?
WHERE id=? AND task_id=? AND agent_id=? AND status='active'`, now, localOwnerActorID, run.AssignmentID, run.TaskID, run.AgentID)
		if err != nil {
			return RunLossResolutionResult{}, storageFailure("release lost-run assignment", err)
		}
		if changed, err := assignmentResult.RowsAffected(); err != nil || changed != 1 {
			return RunLossResolutionResult{}, storageFailure("verify lost-run assignment release", errors.New("exact active assignment was not released"))
		}
	}

	claimRows, err := tx.QueryContext(ctx, claimSelect+" WHERE workspace_id=? AND task_id=? AND status='active' ORDER BY id", run.WorkspaceID, run.TaskID)
	if err != nil {
		return RunLossResolutionResult{}, storageFailure("list lost-run claims", err)
	}
	claims, err := scanClaims(claimRows)
	if err != nil {
		return RunLossResolutionResult{}, err
	}
	for _, claim := range claims {
		if isExecutive {
			break
		}
		claim.Status = domain.ClaimReleased
		claim.Revision++
		if _, err := tx.ExecContext(ctx, `UPDATE work_claims
SET status='released', revision=?, updated_at=?, updated_by=?
WHERE id=? AND status='active'`, claim.Revision, now, localOwnerActorID, claim.ID); err != nil {
			return RunLossResolutionResult{}, storageFailure("release lost-run claim", err)
		}
		claimSequence, err := appendEvent(ctx, tx, run.WorkspaceID, "claim", claim.ID, claim.Revision, claimReleasedEvent, command.CorrelationID, now, map[string]any{
			"status": domain.ClaimReleased,
			"run_id": run.ID,
			"reason": "owner confirmed lost runtime retirement",
		})
		if err != nil {
			return RunLossResolutionResult{}, err
		}
		if _, _, err := resolveClaimOverlapsForActor(ctx, tx, run.WorkspaceID, claim.ID, "lost runtime retired by owner", command.CorrelationID, now, claimSequence, localOwnerActorID, domain.EventActorHuman); err != nil {
			return RunLossResolutionResult{}, err
		}
	}
	if err := terminalizeSchedulingIntentForRun(ctx, tx, run, command.Note, command.CorrelationID, now); err != nil {
		return RunLossResolutionResult{}, err
	}
	detail, err := runDetailInTransaction(ctx, tx, run)
	if err != nil {
		return RunLossResolutionResult{}, err
	}
	resolution := domain.RunLossResolution{
		RunID: run.ID, LostRevision: lostRevision, Resolution: runLossResolutionOwnerConfirmed,
		Note: command.Note, EventSequence: sequence, ResolvedAt: now, ResolvedBy: localOwnerActorID,
	}
	result := RunLossResolutionResult{Detail: detail, Resolution: resolution, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "run.lost.resolve", requestHash, result, now); err != nil {
		return RunLossResolutionResult{}, err
	}
	// Clear the construction seal while this transaction still owns the only
	// SQLite connection; commit cannot execute lifecycle triggers.
	s.runLossResolutionActive.Store(false)
	sealHeld = false
	if err := tx.Commit(); err != nil {
		return RunLossResolutionResult{}, storageFailure("commit lost-run resolution", err)
	}
	return result, nil
}

func (s *Store) DeferRunJob(ctx context.Context, runID string, delay time.Duration) error {
	if delay < 0 {
		delay = 0
	}
	nowTime := s.clock().UTC()
	now, available := nowTime.Format(time.RFC3339Nano), nowTime.Add(delay).Format(time.RFC3339Nano)
	result, err := s.writeDB.ExecContext(ctx, "UPDATE run_jobs SET status = 'pending', available_at = ?, lease_expires_at = NULL, updated_at = ? WHERE run_id = ? AND status = 'leased'", available, now, strings.TrimSpace(runID))
	if err != nil {
		return storageFailure("defer run job", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &Error{Code: CodeRunConflict, Message: "run job is not leased for deferral"}
	}
	return nil
}

func (s *Store) FailRun(ctx context.Context, runID, code, message string, archive *domain.RunLogArchive, logsUnavailableReason, correlationID string) (domain.RunDetail, error) {
	if err := s.validateTerminalLogOutcome(strings.TrimSpace(runID), archive, logsUnavailableReason); err != nil {
		return domain.RunDetail{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.RunDetail{}, storageFailure("begin run failure", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", runID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	if run.Status == domain.RunFailed {
		matches, matchErr := s.terminalRunLogOutcomeReplayMatches(ctx, tx, run, archive, logsUnavailableReason)
		if matchErr != nil {
			return domain.RunDetail{}, matchErr
		}
		if !matches {
			return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "failed run log outcome replay differs"}
		}
		return runDetailInTransaction(ctx, tx, run)
	}
	if run.Status != domain.RunActive {
		return domain.RunDetail{}, &Error{Code: CodeRunConflict, Message: "only an active run can fail during observation"}
	}
	now := s.nowText()
	run.Status, run.FailureCode, run.FailureMessage, run.FinishedAt = domain.RunFailed, boundedRunText(code, 128), boundedRunText(message, 1024), now
	if err := deleteRunRuntimeBinding(ctx, tx, run.ID); err != nil {
		return domain.RunDetail{}, err
	}
	clearRunRuntimeProjection(&run)
	run.Revision++
	if err := updateRunProjectionForActor(ctx, tx, run, now, runWorkerActorID); err != nil {
		return domain.RunDetail{}, err
	}
	if archive != nil {
		if err := s.insertRunLogArchive(ctx, tx, run.ID, now, archive); err != nil {
			return domain.RunDetail{}, err
		}
	}
	isExecutive, err := failOwnerExecutiveRunInTransaction(ctx, tx, run.ID, run.FailureMessage, now)
	if err != nil {
		return domain.RunDetail{}, err
	}
	var task domain.Task
	if !isExecutive {
		task, err = queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
		if err != nil {
			return domain.RunDetail{}, err
		}
		task.Status, task.BlockedReason, task.Revision = domain.TaskFailed, run.FailureMessage, task.Revision+1
		if err := updateTaskStateForActor(ctx, tx, task, now, runWorkerActorID); err != nil {
			return domain.RunDetail{}, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE task_assignments SET status = 'released', revision = revision + 1, updated_at = ?, updated_by = ? WHERE task_id = ? AND status = 'active'", now, runWorkerActorID, task.ID); err != nil {
			return domain.RunDetail{}, storageFailure("release failed run assignment", err)
		}
	}
	if err := setRunJob(ctx, tx, run.ID, "complete", now); err != nil {
		return domain.RunDetail{}, err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runFailedEvent, run.FailureMessage, nil, now); err != nil {
		return domain.RunDetail{}, err
	}
	appliedEventSequence, err := appendEventForActor(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runFailedEvent, correlationID, now, runWorkerActorID, domain.EventActorSubsystem, mergeEventData(map[string]any{"code": run.FailureCode, "message": run.FailureMessage}, terminalLogEventData(archive, logsUnavailableReason)))
	if err != nil {
		return domain.RunDetail{}, err
	}
	if !isExecutive {
		appliedEventSequence, err = appendEventForActor(ctx, tx, run.WorkspaceID, "task", task.ID, task.Revision, taskFailedEvent, correlationID, now, runWorkerActorID, domain.EventActorSubsystem, map[string]any{"run_id": run.ID, "reason": run.FailureMessage})
		if err != nil {
			return domain.RunDetail{}, err
		}
	}
	if err := terminalizeSchedulingIntentForRun(ctx, tx, run, run.FailureMessage, correlationID, now); err != nil {
		return domain.RunDetail{}, err
	}
	if !isExecutive {
		if err := enqueueOwnerManagerReview(ctx, tx, run.WorkspaceID, run.ProjectID, appliedEventSequence, now); err != nil {
			return domain.RunDetail{}, err
		}
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
	tx, err := s.beginTx(ctx, nil)
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
       c.checkout_kind, COALESCE(c.branch, ''), COALESCE(c.head_commit, ''), c.dirty, c.dirty_paths_json,
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
	var dirtyPathsJSON string
	err := tx.QueryRowContext(ctx, query, arguments...).Scan(&checkout.ID, &checkout.ProjectID, &checkout.RepositoryID, &checkout.Path, &checkout.WriteMode, &checkout.Revision, &checkout.Availability, &checkout.CheckoutKind, &checkout.Branch, &checkout.HeadCommit, &checkout.Dirty, &dirtyPathsJSON, &checkout.GitDir, &checkout.GitCommonDir, &checkout.ObservedAt, &checkout.DiagnosticCode, &checkout.Diagnostic, &checkout.CreatedAt, &checkout.UpdatedAt, &checkout.CreatedBy, &checkout.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		if identifier == "" {
			return domain.Checkout{}, &Error{Code: CodePlacementUnavailable, Message: "project has no available writable checkout with capacity"}
		}
		return domain.Checkout{}, &Error{Code: CodePlacementUnavailable, Message: fmt.Sprintf("checkout %q is unavailable, read-only, outside the project, or already reserved", identifier)}
	}
	if err != nil {
		return domain.Checkout{}, storageFailure("select run checkout", err)
	}
	if err := json.Unmarshal([]byte(dirtyPathsJSON), &checkout.DirtyPaths); err != nil {
		return domain.Checkout{}, storageFailure("decode run checkout dirty paths", err)
	}
	return checkout, nil
}

const runSelect = `
SELECT r.id, r.workspace_id, r.project_id, r.task_id, COALESCE(r.assignment_id, ''), r.agent_id, r.checkout_id,
       COALESCE(b.context_packet_id, ''),
       r.runtime, r.provider, r.scenario_name, r.status, r.step_cursor,
       COALESCE(runtime_binding.runtime_handle, ''), COALESCE(runtime_binding.provider_handle, ''),
       COALESCE(runtime_binding.node_id, ''), COALESCE(runtime_binding.node_fingerprint, ''),
       COALESCE(runtime_binding.operation_id, ''),
       COALESCE(r.blocked_question, ''), COALESCE(r.result_summary, ''),
       COALESCE(r.failure_code, ''), COALESCE(r.failure_message, ''), r.stop_grace_millis, r.stop_forced, r.revision,
       r.created_at, r.updated_at, COALESCE(r.started_at, ''), COALESCE(r.finished_at, ''),
       r.created_by, r.updated_by, c.path, c.write_mode, r.placement_reasons_json
FROM runs r JOIN checkouts c ON c.id = r.checkout_id
LEFT JOIN run_context_bindings b ON b.run_id = r.id
LEFT JOIN run_runtime_bindings runtime_binding ON runtime_binding.run_id = r.id`

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
	err := row.Scan(&run.ID, &run.WorkspaceID, &run.ProjectID, &run.TaskID, &run.AssignmentID, &run.AgentID, &run.CheckoutID,
		&run.ContextPacketID,
		&run.Runtime, &run.Provider, &run.ScenarioName, &run.Status, &run.StepCursor,
		&run.RuntimeHandle, &run.ProviderHandle, &run.RuntimeNodeID, &run.RuntimeNodeFingerprint, &run.RuntimeOperationID,
		&run.BlockedQuestion, &run.ResultSummary,
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
	blocker, err := queryRunBlocker(ctx, database, run)
	if err != nil {
		return domain.RunDetail{}, err
	}
	var assessment string
	err = database.QueryRowContext(ctx, `SELECT assessment FROM run_reports WHERE run_id=? AND kind='completion' AND status='applied' ORDER BY sequence DESC LIMIT 1`, run.ID).Scan(&assessment)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.RunDetail{}, storageFailure("query run assessment", err)
	}
	return domain.RunDetail{Run: run, Task: task, Agent: agent, Checkout: checkout, Timeline: timeline, Blocker: blocker, Handoff: handoff, Assessment: assessment}, nil
}

func queryRunBlocker(ctx context.Context, database queryRower, run domain.Run) (*domain.RunBlocker, error) {
	if run.Status != domain.RunBlocked {
		return nil, nil
	}
	var payloadJSON string
	err := database.QueryRowContext(ctx, `
SELECT payload_json FROM run_reports
WHERE run_id=? AND kind='blocked' AND status='applied'
ORDER BY sequence DESC LIMIT 1`, run.ID).Scan(&payloadJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return &domain.RunBlocker{Reason: run.BlockedQuestion, Needs: []string{}, RelatedIDs: []string{}}, nil
	}
	if err != nil {
		return nil, storageFailure("query run blocker", err)
	}
	var blocker domain.RunBlocker
	if err := json.Unmarshal([]byte(payloadJSON), &blocker); err != nil {
		return nil, storageFailure("decode run blocker", err)
	}
	blocker.Reason = strings.TrimSpace(blocker.Reason)
	if blocker.Reason == "" {
		blocker.Reason = run.BlockedQuestion
	}
	blocker.Needs = nonNilStrings(blocker.Needs)
	blocker.RelatedIDs = nonNilStrings(blocker.RelatedIDs)
	return &blocker, nil
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
	var dirtyPathsJSON string
	err := database.QueryRowContext(ctx, `
SELECT id, project_id, repository_id, path, write_mode, revision, availability,
       checkout_kind, COALESCE(branch, ''), COALESCE(head_commit, ''), dirty, dirty_paths_json,
       COALESCE(git_dir, ''), COALESCE(git_common_dir, ''), observed_at,
       COALESCE(diagnostic_code, ''), COALESCE(diagnostic, ''),
       created_at, updated_at, created_by, updated_by
FROM checkouts WHERE id = ?`, checkoutID).Scan(&checkout.ID, &checkout.ProjectID, &checkout.RepositoryID, &checkout.Path, &checkout.WriteMode, &checkout.Revision, &checkout.Availability, &checkout.CheckoutKind, &checkout.Branch, &checkout.HeadCommit, &checkout.Dirty, &dirtyPathsJSON, &checkout.GitDir, &checkout.GitCommonDir, &checkout.ObservedAt, &checkout.DiagnosticCode, &checkout.Diagnostic, &checkout.CreatedAt, &checkout.UpdatedAt, &checkout.CreatedBy, &checkout.UpdatedBy)
	if err != nil {
		return domain.Checkout{}, storageFailure("query run checkout", err)
	}
	if err := json.Unmarshal([]byte(dirtyPathsJSON), &checkout.DirtyPaths); err != nil {
		return domain.Checkout{}, storageFailure("decode run checkout dirty paths", err)
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
	return updateRunProjectionForActor(ctx, tx, run, now, localOwnerActorID)
}

func updateRunProjectionForActor(ctx context.Context, tx *sql.Tx, run domain.Run, now, actorID string) error {
	run.UpdatedAt, run.UpdatedBy = now, actorID
	_, err := tx.ExecContext(ctx, `
UPDATE runs SET status = ?, step_cursor = ?,
    blocked_question = NULLIF(?, ''), result_summary = NULLIF(?, ''), failure_code = NULLIF(?, ''),
    failure_message = NULLIF(?, ''), stop_grace_millis = ?, stop_forced = ?, revision = ?, updated_at = ?, started_at = NULLIF(?, ''),
	    finished_at = NULLIF(?, ''), updated_by = ? WHERE id = ?`, run.Status, run.StepCursor,
		run.BlockedQuestion, run.ResultSummary, run.FailureCode,
		run.FailureMessage, run.StopGraceMillis, run.StopForced, run.Revision, now, run.StartedAt, run.FinishedAt, actorID, run.ID)
	if err != nil {
		return storageFailure("update run projection", err)
	}
	return nil
}

func updateTaskState(ctx context.Context, tx *sql.Tx, task domain.Task, now string) error {
	return updateTaskStateForActor(ctx, tx, task, now, localOwnerActorID)
}

func updateTaskStateForActor(ctx context.Context, tx *sql.Tx, task domain.Task, now, actorID string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE tasks SET status = ?, blocked_reason = NULLIF(?, ''), revision = ?, updated_at = ?, updated_by = ? WHERE id = ?", task.Status, task.BlockedReason, task.Revision, now, actorID, task.ID); err != nil {
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
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_timeline(run_id, kind, message, evidence_json, recorded_at) VALUES (?, ?, NULLIF(?, ''), ?, ?)", runID, kind, boundedRunText(message, 4096), string(evidenceJSON), now); err != nil {
		return storageFailure("append run timeline", err)
	}
	return nil
}

func boundedRunText(value string, maximum int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	if maximum <= 0 || len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return strings.TrimSpace(value[:end])
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
