package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	checkRunStartingEvent    = "check.run_starting"
	checkRunStartedEvent     = "check.run_started"
	checkRunFinishedEvent    = "check.run_finished"
	checkResultRecordedEvent = "check.result_recorded"
)

func (s *Store) RequestCheckRun(ctx context.Context, command RequestCheckRunCommand) (MutationResult[domain.CheckRun], error) {
	command.WorkspaceIdentifier, command.TaskID, command.RequirementID, command.CheckDefinitionIdentifier, command.CheckoutIdentifier, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.TaskID), strings.TrimSpace(command.RequirementID), strings.TrimSpace(command.CheckDefinitionIdentifier), strings.TrimSpace(command.CheckoutIdentifier), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.TaskID == "" || command.CheckDefinitionIdentifier == "" || command.ExpectedRequirementRevision < 0 || command.ExpectedDefinitionContentRevision < 0 || command.ExpectedCheckoutRevision < 0 || (command.CheckoutIdentifier == "" && command.ExpectedCheckoutRevision != 0) {
		return MutationResult[domain.CheckRun]{}, checkError(CodeInvalidCheckRequirement, "check request has invalid scope or optimistic revisions")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidCheckRequirement); err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	hash, _ := checkSemanticHash("check.run.request", command)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	defer tx.Rollback()
	var replay MutationResult[domain.CheckRun]
	idempotencyKey := ownerCheckIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.run.request", hash, &replay); err != nil {
		return MutationResult[domain.CheckRun]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	task, err := queryTask(ctx, tx, workspace.ID, command.TaskID)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	queries := dbgen.New(tx)
	definition, err := queryCheckDefinition(ctx, queries, workspace.ID, command.CheckDefinitionIdentifier)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	requirement, err := resolveCheckRequirement(ctx, queries, workspace.ID, task.ID, definition.ID, command.RequirementID)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	checkout, repository, err := resolveCheckCheckout(ctx, tx, task, command.CheckoutIdentifier)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	if command.ExpectedRequirementRevision > 0 && requirement.Revision != command.ExpectedRequirementRevision {
		return MutationResult[domain.CheckRun]{}, revisionConflict("check requirement", requirement.ID, command.ExpectedRequirementRevision, requirement.Revision)
	}
	if command.ExpectedDefinitionContentRevision > 0 && definition.ContentRevision != command.ExpectedDefinitionContentRevision {
		return MutationResult[domain.CheckRun]{}, revisionConflict("check definition content", definition.ID, command.ExpectedDefinitionContentRevision, definition.ContentRevision)
	}
	if command.ExpectedCheckoutRevision > 0 && checkout.Revision != command.ExpectedCheckoutRevision {
		return MutationResult[domain.CheckRun]{}, revisionConflict("checkout", checkout.ID, command.ExpectedCheckoutRevision, checkout.Revision)
	}
	return s.insertCheckRun(ctx, tx, workspace, task, requirement, definition, checkout, repository, domain.CheckRunSource{Type: domain.CheckRunSourceOwner, ActorID: localOwnerActorID}, idempotencyKey, "check.run.request", hash, command.CorrelationID)
}

func (s *Store) RunGrantedCheck(ctx context.Context, command RequestGrantedCheckRunCommand) (MutationResult[domain.CheckRun], error) {
	command.SourceRunID, command.CheckWatchGrantID, command.RequirementID, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.SourceRunID), strings.TrimSpace(command.CheckWatchGrantID), strings.TrimSpace(command.RequirementID), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ExpectedGrantRevision < 1 || command.RequirementID == "" {
		return MutationResult[domain.CheckRun]{}, checkError(CodeInvalidCheckRequirement, "granted check request requires exact run/grant/requirement")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidCheckRequirement); err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	hash, _ := checkSemanticHash("check.run.granted", command)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	defer tx.Rollback()
	grant, err := s.authorizeRunCheckWatchGrant(ctx, tx, command.SourceRunID, domain.CheckWatchOperationRun)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	if grant.ID != command.CheckWatchGrantID || grant.Revision != command.ExpectedGrantRevision {
		return MutationResult[domain.CheckRun]{}, checkError(CodeCheckWatchGrantDenied, "granted check request differs from current bound grant")
	}
	var replay MutationResult[domain.CheckRun]
	idempotencyKey := runCheckIdempotencyKey(command.SourceRunID, command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.run.granted", hash, &replay); err != nil {
		return MutationResult[domain.CheckRun]{}, err
	} else if found {
		return replay, nil
	}
	capacity, err := dbgen.New(tx).CountGrantedCheckCapacity(ctx, optionalStringPointer(grant.ID))
	if err != nil {
		return MutationResult[domain.CheckRun]{}, storageFailure("count granted check capacity", err)
	}
	if capacity.UnresolvedCount >= int64(grant.MaxPending) {
		return MutationResult[domain.CheckRun]{}, checkError(CodeCheckWatchGrantDenied, "check-watch grant pending limit is exhausted")
	}
	queries := dbgen.New(tx)
	requirement, err := queryTaskCheckRequirement(ctx, queries, grant.WorkspaceID, command.RequirementID)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	if requirement.ProjectID != grant.ProjectID || requirement.Status != domain.CheckRequirementActive {
		return MutationResult[domain.CheckRun]{}, checkError(CodeCheckWatchGrantDenied, "requirement is outside current grant project")
	}
	definition, err := queryCheckDefinition(ctx, queries, grant.WorkspaceID, requirement.DefinitionID)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	allowed := false
	for _, item := range grant.Definitions {
		if item.DefinitionID == definition.ID && item.ContentRevision == definition.ContentRevision && item.DefinitionSHA256 == definition.ContentSHA256 {
			allowed = true
		}
	}
	if !allowed || definition.Status != domain.CheckDefinitionActive {
		return MutationResult[domain.CheckRun]{}, checkError(CodeCheckWatchGrantDenied, "requirement definition is not in current grant")
	}
	task, err := queryTask(ctx, tx, grant.WorkspaceID, requirement.TaskID)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	checkout, repository, err := resolveCheckCheckout(ctx, tx, task, "")
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	workspace, err := workspaceInTransaction(ctx, tx, grant.WorkspaceID)
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	source := domain.CheckRunSource{Type: domain.CheckRunSourceAgentRun, ActorID: command.SourceRunID, AgentID: grant.AgentID, AgentRevision: grant.AgentRevision, AgentRunID: command.SourceRunID, GrantID: grant.ID, GrantRevision: grant.Revision}
	return s.insertCheckRun(ctx, tx, workspace, task, requirement, definition, checkout, repository, source, idempotencyKey, "check.run.granted", hash, command.CorrelationID)
}

func (s *Store) insertCheckRun(ctx context.Context, tx *sql.Tx, workspace domain.Workspace, task domain.Task, requirement domain.TaskCheckRequirement, definition domain.CheckDefinition, checkout domain.Checkout, repository domain.Repository, source domain.CheckRunSource, key, operation, hash, correlation string) (MutationResult[domain.CheckRun], error) {
	id, _ := randomID("checkrun_")
	jobID, _ := randomID("checkjob_")
	now := s.nowText()
	sourceMaxInFlight := 0
	if source.Type == domain.CheckRunSourceAgentRun {
		value, err := dbgen.New(tx).GetCheckGrantMaxInFlight(ctx, dbgen.GetCheckGrantMaxInFlightParams{GrantID: source.GrantID, GrantRevision: source.GrantRevision})
		if err != nil {
			return MutationResult[domain.CheckRun]{}, storageFailure("freeze granted check in-flight bound", err)
		}
		sourceMaxInFlight = int(value)
	}
	run := domain.CheckRun{ID: id, WorkspaceID: workspace.ID, ProjectID: task.ProjectID, TaskID: task.ID, TaskRevision: task.Revision, RequirementID: requirement.ID, RequirementRevision: requirement.Revision, DefinitionID: definition.ID, DefinitionContentRevision: definition.ContentRevision, DefinitionSHA256: definition.ContentSHA256, CheckoutID: checkout.ID, CheckoutRevision: checkout.Revision, RepositoryID: repository.ID, RepositoryObjectFormat: repository.ObjectFormat, CheckoutPath: checkout.Path, CheckoutWriteMode: checkout.WriteMode, Source: source, SourceMaxInFlight: sourceMaxInFlight, Status: domain.CheckRunRequested, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: source.ActorID, UpdatedBy: source.ActorID}
	queries := dbgen.New(tx)
	err := s.withCheckMutationSeal(func() error {
		return queries.InsertCheckRun(ctx, dbgen.InsertCheckRunParams{ID: id, WorkspaceID: workspace.ID, ProjectID: task.ProjectID, TaskID: task.ID, TaskRevision: task.Revision, RequirementID: requirement.ID, RequirementRevision: requirement.Revision, DefinitionID: definition.ID, DefinitionContentRevision: definition.ContentRevision, DefinitionSha256: definition.ContentSHA256, CheckoutID: checkout.ID, CheckoutRevision: checkout.Revision, RepositoryID: repository.ID, RepositoryObjectFormat: repository.ObjectFormat, CheckoutPath: checkout.Path, CheckoutWriteMode: checkout.WriteMode, SourceType: source.Type, SourceActorID: source.ActorID, SourceAgentID: source.AgentID, SourceAgentRevision: source.AgentRevision, SourceRunID: source.AgentRunID, SourceGrantID: source.GrantID, SourceGrantRevision: source.GrantRevision, SourceMaxInFlight: int64(sourceMaxInFlight), CreatedAt: now, CreatedBy: source.ActorID})
	})
	if err != nil {
		return MutationResult[domain.CheckRun]{}, checkConstraint("insert check run request", CodeCheckRunConflict, err)
	}
	if err := s.runMutationHook(MutationAfterCheckRequestProjection); err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	err = s.withCheckMutationSeal(func() error {
		return queries.InsertCheckJob(ctx, dbgen.InsertCheckJobParams{ID: jobID, CheckRunID: id, CreatedAt: now})
	})
	if err != nil {
		return MutationResult[domain.CheckRun]{}, checkConstraint("insert check run job", CodeCheckRunConflict, err)
	}
	if err := s.runMutationHook(MutationAfterCheckRequestJob); err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	seq, err := appendEventForActor(ctx, tx, workspace.ID, "check_run", id, 1, checkRunRequestedEvent, correlation, now, source.ActorID, checkActorType(source), map[string]any{"task_id": task.ID, "requirement_id": requirement.ID, "definition_id": definition.ID, "definition_content_revision": definition.ContentRevision, "checkout_id": checkout.ID})
	if err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	if err := s.runMutationHook(MutationAfterCheckRequestEvent); err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	result := MutationResult[domain.CheckRun]{Value: run, EventSequence: seq}
	if err := recordIdempotency(ctx, tx, key, operation, hash, result, now); err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	if err := s.runMutationHook(MutationAfterCheckRequestIdempotency); err != nil {
		return MutationResult[domain.CheckRun]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckRun]{}, storageFailure("commit check run request", err)
	}
	return result, nil
}

func resolveCheckRequirement(ctx context.Context, queries *dbgen.Queries, workspaceID, taskID, definitionID, explicit string) (domain.TaskCheckRequirement, error) {
	if explicit != "" {
		req, err := queryTaskCheckRequirement(ctx, queries, workspaceID, explicit)
		if err != nil {
			return domain.TaskCheckRequirement{}, err
		}
		if req.TaskID != taskID || req.DefinitionID != definitionID || req.Status != domain.CheckRequirementActive {
			return domain.TaskCheckRequirement{}, checkError(CodeInvalidCheckRequirement, "explicit requirement does not match active task definition")
		}
		return req, nil
	}
	row, err := queries.GetActiveTaskCheckRequirementByDefinition(ctx, dbgen.GetActiveTaskCheckRequirementByDefinitionParams{WorkspaceID: workspaceID, TaskID: taskID, DefinitionID: definitionID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TaskCheckRequirement{}, checkError(CodeCheckRequirementNotFound, "task has no active requirement for definition")
	}
	if err != nil {
		return domain.TaskCheckRequirement{}, storageFailure("resolve active check requirement", err)
	}
	return domain.TaskCheckRequirement(row), nil
}
func resolveCheckCheckout(ctx context.Context, tx *sql.Tx, task domain.Task, identifier string) (domain.Checkout, domain.Repository, error) {
	queries := dbgen.New(tx)
	checkoutID := identifier
	if checkoutID == "" {
		var err error
		checkoutID, err = queries.GetLatestTaskRunCheckoutID(ctx, dbgen.GetLatestTaskRunCheckoutIDParams{TaskID: task.ID, ProjectID: task.ProjectID})
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Checkout{}, domain.Repository{}, checkError(CodeInvalidCheckout, "check task has no deterministic checkout")
		}
		if err != nil {
			return domain.Checkout{}, domain.Repository{}, storageFailure("select check checkout", err)
		}
	}
	checkout, err := queryCheckoutByID(ctx, tx, checkoutID)
	if err != nil {
		return domain.Checkout{}, domain.Repository{}, err
	}
	if checkout.ProjectID != task.ProjectID || checkout.Availability != domain.CheckoutAvailable {
		return domain.Checkout{}, domain.Repository{}, checkError(CodeInvalidCheckout, "check checkout is outside project or unavailable")
	}
	repositoryRow, err := queries.GetCheckRepository(ctx, checkout.RepositoryID)
	if err != nil {
		return domain.Checkout{}, domain.Repository{}, storageFailure("read check repository", err)
	}
	repository, err := checkRepositoryFromRow(repositoryRow)
	if err != nil {
		return domain.Checkout{}, domain.Repository{}, err
	}
	return checkout, repository, nil
}

func checkRepositoryFromRow(row dbgen.Repository) (domain.Repository, error) {
	repository := domain.Repository{ID: row.ID, WorkspaceID: row.WorkspaceID, Fingerprint: row.Fingerprint, ObjectFormat: row.ObjectFormat, Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy}
	if err := json.Unmarshal([]byte(row.RootCommitsJson), &repository.RootCommits); err != nil {
		return domain.Repository{}, err
	}
	return repository, nil
}
func checkActorType(source domain.CheckRunSource) string {
	if source.Type == domain.CheckRunSourceOwner {
		return localActorType
	}
	return "agent"
}

func (s *Store) ClaimCheckJob(ctx context.Context, lease time.Duration) (CheckWork, bool, error) {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CheckWork{}, false, err
	}
	defer tx.Rollback()
	nowTime := s.clock().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	var runID string
	queries := dbgen.New(tx)
	err = s.withCheckMutationSeal(func() error {
		if err := queries.ResetExpiredCheckJobLeases(ctx, now); err != nil {
			return err
		}
		var err error
		runID, err = queries.GetNextPendingCheckRunID(ctx, now)
		if err != nil {
			return err
		}
		leaseExpiresAt := nowTime.Add(lease).Format(time.RFC3339Nano)
		affected, err := queries.LeaseCheckJob(ctx, dbgen.LeaseCheckJobParams{LeaseExpiresAt: &leaseExpiresAt, UpdatedAt: now, CheckRunID: runID})
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("check job claim raced")
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return CheckWork{}, false, err
		}
		return CheckWork{}, false, nil
	}
	if err != nil {
		return CheckWork{}, false, storageFailure("claim check job", err)
	}
	work, err := checkWorkInTransaction(ctx, tx, runID)
	if err != nil {
		return CheckWork{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return CheckWork{}, false, err
	}
	return work, true, nil
}

// RecoverCheckJobLeases releases every check-job lease owned by a previous
// daemon session. Callers must hold the daemon's exclusive data-directory lock:
// unlike ordinary expiry reclamation, this operation deliberately treats even
// an unexpired lease as abandoned. Receipted starting/running operations retain
// their immutable run state and are therefore replayed with the same operation
// ID; requested operations simply become claimable again.
func (s *Store) RecoverCheckJobLeases(ctx context.Context) error {
	now := s.nowText()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin check-job lease recovery", err)
	}
	defer tx.Rollback()
	err = s.withCheckMutationSeal(func() error {
		return dbgen.New(tx).RecoverAllCheckJobLeases(ctx, now)
	})
	if err != nil {
		return checkConstraint("recover check-job leases", CodeCheckRunConflict, err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit check-job lease recovery", err)
	}
	return nil
}

func checkWorkInTransaction(ctx context.Context, tx *sql.Tx, runID string) (CheckWork, error) {
	queries := dbgen.New(tx)
	row, err := queries.GetCheckRun(ctx, runID)
	if err != nil {
		return CheckWork{}, err
	}
	run := checkRunFromRow(row)
	definition, err := queryCheckDefinition(ctx, queries, run.WorkspaceID, run.DefinitionID)
	if err != nil {
		return CheckWork{}, err
	}
	requirement, err := queryTaskCheckRequirement(ctx, queries, run.WorkspaceID, run.RequirementID)
	if err != nil {
		return CheckWork{}, err
	}
	checkout := domain.Checkout{ID: run.CheckoutID, ProjectID: run.ProjectID, RepositoryID: run.RepositoryID, Path: run.CheckoutPath, WriteMode: run.CheckoutWriteMode, Revision: run.CheckoutRevision, Availability: domain.CheckoutAvailable}
	repositoryRow, err := queries.GetFrozenCheckRepository(ctx, dbgen.GetFrozenCheckRepositoryParams{RepositoryID: run.RepositoryID, ObjectFormat: run.RepositoryObjectFormat})
	if err != nil {
		return CheckWork{}, storageFailure("corroborate frozen check repository", err)
	}
	repository, err := checkRepositoryFromRow(repositoryRow)
	if err != nil {
		return CheckWork{}, err
	}
	jobRow, err := queries.GetCheckJobByRun(ctx, run.ID)
	if err != nil {
		return CheckWork{}, err
	}
	job := checkJobFromRow(jobRow)
	var receipt *domain.CheckLaunchReceipt
	receiptRow, err := queries.GetCheckLaunchReceiptByRun(ctx, run.ID)
	if err == nil {
		value, err := checkLaunchReceiptFromRow(receiptRow)
		if err != nil {
			return CheckWork{}, err
		}
		receipt = &value
	} else if !errors.Is(err, sql.ErrNoRows) {
		return CheckWork{}, err
	}
	return CheckWork{Run: run, Definition: definition, Requirement: requirement, Job: job, Checkout: checkout, Repository: repository, LaunchReceipt: receipt}, nil
}

func checkRunFromRow(row dbgen.CheckRun) domain.CheckRun {
	return domain.CheckRun{ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, TaskID: row.TaskID, TaskRevision: row.TaskRevision, RequirementID: row.RequirementID, RequirementRevision: row.RequirementRevision, DefinitionID: row.DefinitionID, DefinitionContentRevision: row.DefinitionContentRevision, DefinitionSHA256: row.DefinitionSha256, CheckoutID: row.CheckoutID, CheckoutRevision: row.CheckoutRevision, RepositoryID: row.RepositoryID, RepositoryObjectFormat: row.RepositoryObjectFormat, CheckoutPath: row.CheckoutPath, CheckoutWriteMode: row.CheckoutWriteMode, Source: domain.CheckRunSource{Type: row.SourceType, ActorID: row.SourceActorID, AgentID: stringValue(row.SourceAgentID), AgentRevision: int64Value(row.SourceAgentRevision), AgentRunID: stringValue(row.SourceRunID), GrantID: stringValue(row.SourceGrantID), GrantRevision: int64Value(row.SourceGrantRevision)}, SourceMaxInFlight: int(row.SourceMaxInFlight), Status: row.Status, RuntimeHandle: stringValue(row.RuntimeHandle), Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: stringValue(row.StartedAt), FinishedAt: stringValue(row.FinishedAt), CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy}
}
func checkJobFromRow(row dbgen.CheckJob) domain.CheckJob {
	return domain.CheckJob{ID: row.ID, CheckRunID: row.CheckRunID, Status: row.Status, AvailableAt: row.AvailableAt, LeaseExpiresAt: stringValue(row.LeaseExpiresAt), Attempts: int(row.Attempts), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func checkLaunchReceiptFromRow(row dbgen.CheckLaunchReceipt) (domain.CheckLaunchReceipt, error) {
	var paths []string
	if err := json.Unmarshal([]byte(row.DirtyPathsJson), &paths); err != nil {
		return domain.CheckLaunchReceipt{}, err
	}
	receipt := domain.CheckLaunchReceipt{ID: row.ID, CheckRunID: row.CheckRunID, CheckJobID: row.CheckJobID, OperationID: row.OperationID, EffectiveSpecSHA256: row.EffectiveSpecSha256, EffectiveWorkingDirectory: row.EffectiveWorkingDirectory, Launchable: row.Launchable != 0, PreflightFailureCode: stringValue(row.PreflightFailureCode), PreflightFailureDiagnostic: stringValue(row.PreflightFailureDiagnostic), DefinitionSHA256: row.DefinitionSha256, Source: domain.CheckRunSource{Type: row.SourceType, ActorID: row.SourceActorID, AgentID: stringValue(row.SourceAgentID), AgentRevision: int64Value(row.SourceAgentRevision), AgentRunID: stringValue(row.SourceRunID), GrantID: stringValue(row.SourceGrantID), GrantRevision: int64Value(row.SourceGrantRevision)}, Observation: domain.CheckGitObservation{Available: row.ObservationAvailable != 0, RepositoryID: row.RepositoryID, ObjectFormat: row.ObjectFormat, CheckoutID: row.CheckoutID, Branch: stringValue(row.Branch), HeadCommit: stringValue(row.HeadCommit), Dirty: row.Dirty != 0, DirtyPaths: paths, ObservedAt: row.ObservedAt, DiagnosticCode: stringValue(row.DiagnosticCode), Diagnostic: stringValue(row.Diagnostic)}, CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy}
	if !validCheckObservation(receipt.Observation) {
		return domain.CheckLaunchReceipt{}, checkError(CodeInvalidRun, "committed check launch observation is not canonical")
	}
	return receipt, nil
}

func (s *Store) MarkCheckStarting(ctx context.Context, command MarkCheckStartingCommand) (domain.CheckRunDetail, error) {
	command.CheckRunID, command.OperationID, command.EffectiveSpecSHA256, command.EffectiveWorkingDirectory, command.PreflightFailureCode, command.PreflightFailureDiagnostic, command.CorrelationID = strings.TrimSpace(command.CheckRunID), strings.TrimSpace(command.OperationID), strings.TrimSpace(command.EffectiveSpecSHA256), strings.TrimSpace(command.EffectiveWorkingDirectory), strings.TrimSpace(command.PreflightFailureCode), strings.TrimSpace(command.PreflightFailureDiagnostic), strings.TrimSpace(command.CorrelationID)
	if command.CheckRunID == "" || command.OperationID != command.CheckRunID || !validLowerSHA256(command.EffectiveSpecSHA256) || !filepath.IsAbs(command.EffectiveWorkingDirectory) || !validCheckObservation(command.Observation) || (command.Launchable && (command.PreflightFailureCode != "" || command.PreflightFailureDiagnostic != "")) || (!command.Launchable && (command.PreflightFailureCode != domain.CheckPreflightWorkingDirectoryInvalid || !validCheckText(command.PreflightFailureDiagnostic, 4096))) {
		return domain.CheckRunDetail{}, checkError(CodeInvalidRun, "check start receipt is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	defer tx.Rollback()
	work, err := checkWorkInTransaction(ctx, tx, command.CheckRunID)
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	if work.Job.Status != domain.CheckJobLeased || !checkJobLeaseCurrent(work.Job, s.clock().UTC()) {
		return domain.CheckRunDetail{}, checkError(CodeCheckRunConflict, "check start requires current leased job")
	}
	if work.Run.Status == domain.CheckRunStarting && work.LaunchReceipt != nil {
		decisionMatches := work.LaunchReceipt.Launchable == command.Launchable && work.LaunchReceipt.PreflightFailureCode == command.PreflightFailureCode && work.LaunchReceipt.PreflightFailureDiagnostic == command.PreflightFailureDiagnostic
		if command.Launchable && command.PreflightFailureCode == "" && command.PreflightFailureDiagnostic == "" && !work.LaunchReceipt.Launchable && internallyDerivedCheckLaunchDenial(work.LaunchReceipt.PreflightFailureCode) {
			decisionMatches = true
		}
		if work.LaunchReceipt.OperationID != command.OperationID || work.LaunchReceipt.EffectiveSpecSHA256 != command.EffectiveSpecSHA256 || work.LaunchReceipt.EffectiveWorkingDirectory != command.EffectiveWorkingDirectory || !decisionMatches || !equalCheckObservation(work.LaunchReceipt.Observation, command.Observation) {
			return domain.CheckRunDetail{}, checkError(CodeCheckRunConflict, "check launch receipt replay differs")
		}
		detail, err := checkRunDetailInTransaction(ctx, tx, work.Run.ID)
		if err != nil {
			return domain.CheckRunDetail{}, err
		}
		_ = tx.Commit()
		return detail, nil
	}
	if work.Run.Status != domain.CheckRunRequested {
		return domain.CheckRunDetail{}, checkError(CodeCheckRunConflict, "only requested check can start")
	}
	if command.Observation.RepositoryID != work.Run.RepositoryID || command.Observation.ObjectFormat != work.Run.RepositoryObjectFormat || command.Observation.CheckoutID != work.Run.CheckoutID {
		return domain.CheckRunDetail{}, checkError(CodeInvalidRun, "check start observation differs from frozen source identity")
	}
	denialCode, denialDiagnostic, err := s.deriveCheckLaunchDenial(ctx, tx, work)
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	derivedDenial := denialCode != ""
	if derivedDenial {
		command.Launchable = false
		command.PreflightFailureCode = denialCode
		command.PreflightFailureDiagnostic = denialDiagnostic
	}
	if command.Launchable {
		if err := validateEffectiveCheckWorkingDirectory(work.Run.CheckoutPath, command.EffectiveWorkingDirectory); err != nil {
			return domain.CheckRunDetail{}, checkError(CodeCheckRunConflict, "effective check working directory is outside checkout")
		}
	} else if !derivedDenial {
		expected := filepath.Clean(filepath.Join(work.Run.CheckoutPath, filepath.FromSlash(work.Definition.WorkingDirectory)))
		if filepath.Clean(command.EffectiveWorkingDirectory) != expected {
			return domain.CheckRunDetail{}, checkError(CodeCheckRunConflict, "preflight path differs from immutable logical definition")
		}
	}
	now := s.nowText()
	receiptID, _ := randomID("checklaunch_")
	dirtyJSON, _ := json.Marshal(command.Observation.DirtyPaths)
	queries := dbgen.New(tx)
	receiptAuthority, err := queries.CheckLaunchReceiptAuthorityMatches(ctx, dbgen.CheckLaunchReceiptAuthorityMatchesParams{
		CheckRunID: work.Run.ID, CheckJobID: work.Job.ID, DefinitionSha256: work.Run.DefinitionSHA256,
		RepositoryID: command.Observation.RepositoryID, RepositoryObjectFormat: command.Observation.ObjectFormat, CheckoutID: command.Observation.CheckoutID,
		SourceType: work.Run.Source.Type, SourceActorID: work.Run.Source.ActorID, SourceAgentID: optionalStringPointer(work.Run.Source.AgentID), SourceAgentRevision: optionalInt64Pointer(work.Run.Source.AgentRevision),
		SourceRunID: optionalStringPointer(work.Run.Source.AgentRunID), SourceGrantID: optionalStringPointer(work.Run.Source.GrantID), SourceGrantRevision: optionalInt64Pointer(work.Run.Source.GrantRevision),
	})
	if err != nil {
		return domain.CheckRunDetail{}, storageFailure("corroborate check launch authority", err)
	}
	if receiptAuthority != 1 {
		return domain.CheckRunDetail{}, checkError(CodeCheckRunConflict, "check launch receipt differs from frozen run/job authority")
	}
	if work.Run.Source.Type == domain.CheckRunSourceAgentRun {
		current, err := queries.CountGrantActiveChecks(ctx, optionalStringPointer(work.Run.Source.GrantID))
		if err != nil {
			return domain.CheckRunDetail{}, err
		}
		if current >= int64(work.Run.SourceMaxInFlight) {
			return domain.CheckRunDetail{}, checkError(CodeCheckCapacityDeferred, "check-watch grant in-flight limit is exhausted")
		}
	}
	err = s.withCheckMutationSeal(func() error {
		if _, err := queries.MarkCheckRunStarting(ctx, dbgen.MarkCheckRunStartingParams{UpdatedAt: now, CheckRunID: work.Run.ID}); err != nil {
			return err
		}
		return queries.InsertCheckLaunchReceipt(ctx, dbgen.InsertCheckLaunchReceiptParams{
			ID: receiptID, CheckRunID: work.Run.ID, CheckJobID: work.Job.ID, OperationID: work.Run.ID,
			EffectiveSpecSha256: command.EffectiveSpecSHA256, EffectiveWorkingDirectory: command.EffectiveWorkingDirectory,
			Launchable: boolInteger(command.Launchable), PreflightFailureCode: nullableText(command.PreflightFailureCode), PreflightFailureDiagnostic: nullableText(command.PreflightFailureDiagnostic), DefinitionSha256: work.Run.DefinitionSHA256,
			SourceType: work.Run.Source.Type, SourceActorID: work.Run.Source.ActorID, SourceAgentID: nullableText(work.Run.Source.AgentID), SourceAgentRevision: nullableInt64(work.Run.Source.AgentRevision), SourceRunID: nullableText(work.Run.Source.AgentRunID), SourceGrantID: nullableText(work.Run.Source.GrantID), SourceGrantRevision: nullableInt64(work.Run.Source.GrantRevision),
			ObservationAvailable: boolInteger(command.Observation.Available), RepositoryID: command.Observation.RepositoryID, ObjectFormat: command.Observation.ObjectFormat, CheckoutID: command.Observation.CheckoutID,
			Branch: nullableText(command.Observation.Branch), HeadCommit: nullableText(command.Observation.HeadCommit), Dirty: boolInteger(command.Observation.Dirty), DirtyPathsJson: string(dirtyJSON), ObservedAt: command.Observation.ObservedAt,
			DiagnosticCode: nullableText(command.Observation.DiagnosticCode), Diagnostic: nullableText(command.Observation.Diagnostic), CreatedAt: now,
		})
	})
	if err != nil {
		return domain.CheckRunDetail{}, checkConstraint("record check launch receipt", CodeCheckRunConflict, err)
	}
	if err := s.runMutationHook(MutationAfterCheckLaunchReceipt); err != nil {
		return domain.CheckRunDetail{}, err
	}
	work.Run.Status = domain.CheckRunStarting
	work.Run.Revision++
	work.Run.UpdatedAt = now
	seq, err := appendEventForActor(ctx, tx, work.Run.WorkspaceID, "check_run", work.Run.ID, work.Run.Revision, checkRunStartingEvent, command.CorrelationID, now, "crewfold-check-worker", "subsystem", map[string]any{"launch_receipt_id": receiptID, "effective_spec_sha256": command.EffectiveSpecSHA256, "launchable": command.Launchable})
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	if err := s.runMutationHook(MutationAfterCheckLaunchEvent); err != nil {
		return domain.CheckRunDetail{}, err
	}
	_ = seq
	detail, err := checkRunDetailInTransaction(ctx, tx, work.Run.ID)
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CheckRunDetail{}, err
	}
	return detail, nil
}

func internallyDerivedCheckLaunchDenial(code string) bool {
	switch code {
	case domain.CheckPreflightAuthorityRevoked, domain.CheckPreflightDefinitionRetired, domain.CheckPreflightRequirementRetired, domain.CheckPreflightCheckoutChanged:
		return true
	default:
		return false
	}
}

func (s *Store) RecordCheckRuntimeBinding(ctx context.Context, runID, runtimeHandle, correlationID string) (domain.CheckRun, error) {
	runID, runtimeHandle, correlationID = strings.TrimSpace(runID), strings.TrimSpace(runtimeHandle), strings.TrimSpace(correlationID)
	if runtimeHandle != "direct:"+runID {
		return domain.CheckRun{}, checkError(CodeInvalidRun, "runtime binding requires handle")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.CheckRun{}, err
	}
	defer tx.Rollback()
	queries := dbgen.New(tx)
	row, err := queries.GetCheckRun(ctx, runID)
	if err != nil {
		return domain.CheckRun{}, checkRunQueryError(err)
	}
	run := checkRunFromRow(row)
	jobStatus, err := queries.GetCheckJobStatus(ctx, run.ID)
	if err != nil || jobStatus != domain.CheckJobLeased {
		return domain.CheckRun{}, checkError(CodeCheckRunConflict, "runtime binding requires leased check job")
	}
	if run.Status != domain.CheckRunStarting {
		return domain.CheckRun{}, checkError(CodeCheckRunConflict, "runtime binding requires starting check")
	}
	if run.RuntimeHandle != "" {
		if run.RuntimeHandle == runtimeHandle {
			return run, nil
		}
		return domain.CheckRun{}, checkError(CodeCheckRunConflict, "runtime binding cannot change")
	}
	launchable, err := queries.GetCheckLaunchReceiptLaunchable(ctx, run.ID)
	if err != nil || launchable != 1 {
		return domain.CheckRun{}, checkError(CodeCheckRunConflict, "runtime binding requires launchable receipt")
	}
	now := s.nowText()
	err = s.withCheckMutationSeal(func() error {
		_, err := queries.BindCheckRuntime(ctx, dbgen.BindCheckRuntimeParams{RuntimeHandle: optionalStringPointer(runtimeHandle), UpdatedAt: now, CheckRunID: run.ID})
		return err
	})
	if err != nil {
		return domain.CheckRun{}, err
	}
	run.RuntimeHandle = runtimeHandle
	run.Revision++
	run.UpdatedAt = now
	if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "check_run", run.ID, run.Revision, "check.run_runtime_observed", correlationID, now, "crewfold-check-worker", "subsystem", map[string]any{"runtime_handle": runtimeHandle}); err != nil {
		return domain.CheckRun{}, err
	}
	if err := s.runMutationHook(MutationAfterCheckRuntimeBinding); err != nil {
		return domain.CheckRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CheckRun{}, err
	}
	return run, nil
}

func (s *Store) MarkCheckRunning(ctx context.Context, runID, correlationID string) (domain.CheckRunDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	defer tx.Rollback()
	queries := dbgen.New(tx)
	row, err := queries.GetCheckRun(ctx, strings.TrimSpace(runID))
	if err != nil {
		return domain.CheckRunDetail{}, checkRunQueryError(err)
	}
	run := checkRunFromRow(row)
	jobStatus, err := queries.GetCheckJobStatus(ctx, run.ID)
	if err != nil || jobStatus != domain.CheckJobLeased {
		return domain.CheckRunDetail{}, checkError(CodeCheckRunConflict, "running transition requires leased check job")
	}
	if run.Status == domain.CheckRunRunning {
		detail, err := checkRunDetailInTransaction(ctx, tx, run.ID)
		_ = tx.Commit()
		return detail, err
	}
	if run.Status != domain.CheckRunStarting || run.RuntimeHandle == "" {
		return domain.CheckRunDetail{}, checkError(CodeCheckRunConflict, "running requires starting check with runtime binding")
	}
	now := s.nowText()
	err = s.withCheckMutationSeal(func() error {
		if _, err := queries.MarkCheckRunRunning(ctx, dbgen.MarkCheckRunRunningParams{StartedAt: optionalStringPointer(now), CheckRunID: run.ID}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	run.Status = domain.CheckRunRunning
	run.Revision++
	if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "check_run", run.ID, run.Revision, checkRunStartedEvent, correlationID, now, "crewfold-check-worker", "subsystem", map[string]any{}); err != nil {
		return domain.CheckRunDetail{}, err
	}
	detail, err := checkRunDetailInTransaction(ctx, tx, run.ID)
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CheckRunDetail{}, err
	}
	return detail, nil
}

func (s *Store) DeferCheckJob(ctx context.Context, runID string, delay time.Duration) error {
	if delay < 0 {
		delay = 0
	}
	now := s.clock().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	queries := dbgen.New(tx)
	err = s.withCheckMutationSeal(func() error {
		affected, err := queries.ReleaseCheckJobLease(ctx, dbgen.ReleaseCheckJobLeaseParams{AvailableAt: now.Add(delay).Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano), CheckRunID: strings.TrimSpace(runID)})
		if err != nil {
			return storageFailure("defer check job", err)
		}
		if affected != 1 {
			return checkError(CodeCheckRunConflict, "check job is not leased")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}

func validLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
func validCheckObservation(value domain.CheckGitObservation) bool {
	if value.RepositoryID == "" || value.CheckoutID == "" || (value.ObjectFormat != "sha1" && value.ObjectFormat != "sha256") || !canonicalTimestamp(value.ObservedAt) || len(value.DirtyPaths) > 256 || !utf8.ValidString(value.Branch) || strings.ContainsRune(value.Branch, '\x00') || len([]byte(value.Branch)) > 1024 {
		return false
	}
	if value.Available {
		if !validCheckCommit(value.HeadCommit, value.ObjectFormat) || value.DiagnosticCode != "" || value.Diagnostic != "" || (!value.Dirty && len(value.DirtyPaths) != 0) {
			return false
		}
	} else if value.Branch != "" || value.HeadCommit != "" || value.Dirty || len(value.DirtyPaths) != 0 || !validCheckText(value.DiagnosticCode, 128) || !validCheckText(value.Diagnostic, 4096) {
		return false
	}
	previous := ""
	for _, path := range value.DirtyPaths {
		clean := filepath.ToSlash(filepath.Clean(path))
		if path == "" || path == "." || filepath.IsAbs(path) || clean != path || strings.HasPrefix(path, "../") || !utf8.ValidString(path) || strings.ContainsRune(path, '\x00') || len([]byte(path)) > 1024 || path <= previous {
			return false
		}
		previous = path
	}
	encoded, err := json.Marshal(value.DirtyPaths)
	return err == nil && len(encoded) <= 256*1024
}

func validCheckCommit(value, objectFormat string) bool {
	want := 40
	if objectFormat == "sha256" {
		want = 64
	}
	if len(value) != want {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func checkJobLeaseCurrent(job domain.CheckJob, now time.Time) bool {
	lease, err := time.Parse(time.RFC3339Nano, job.LeaseExpiresAt)
	return err == nil && now.Before(lease)
}

func (s *Store) deriveCheckLaunchDenial(ctx context.Context, tx *sql.Tx, work CheckWork) (string, string, error) {
	if work.Run.Source.Type == domain.CheckRunSourceAgentRun {
		grant, err := s.authorizeRunCheckWatchGrant(ctx, tx, work.Run.Source.AgentRunID, domain.CheckWatchOperationRun)
		if err != nil {
			switch ErrorCode(err) {
			case CodeCheckWatchGrantDenied, CodeCheckWatchGrantNotFound, CodeCapabilityExpired, CodeCapabilityInactive, CodeRunNotFound:
				return domain.CheckPreflightAuthorityRevoked, "the exact check-watch authority is no longer current", nil
			default:
				return "", "", err
			}
		}
		allowed := false
		for _, definition := range grant.Definitions {
			if definition.DefinitionID == work.Run.DefinitionID && definition.ContentRevision == work.Run.DefinitionContentRevision && definition.DefinitionSHA256 == work.Run.DefinitionSHA256 {
				allowed = true
				break
			}
		}
		if grant.ID != work.Run.Source.GrantID || grant.Revision != work.Run.Source.GrantRevision || grant.WorkspaceID != work.Run.WorkspaceID || grant.ProjectID != work.Run.ProjectID || grant.AgentID != work.Run.Source.AgentID || grant.AgentRevision != work.Run.Source.AgentRevision || !allowed {
			return domain.CheckPreflightAuthorityRevoked, "the exact check-watch authority is no longer current", nil
		}
	}
	if work.Definition.Status != domain.CheckDefinitionActive || work.Definition.ContentRevision != work.Run.DefinitionContentRevision || work.Definition.ContentSHA256 != work.Run.DefinitionSHA256 {
		return domain.CheckPreflightDefinitionRetired, "the exact check definition was retired before launch", nil
	}
	if work.Requirement.Status != domain.CheckRequirementActive || work.Requirement.Revision != work.Run.RequirementRevision || work.Requirement.DefinitionID != work.Run.DefinitionID || work.Requirement.DefinitionContentRevision != work.Run.DefinitionContentRevision {
		return domain.CheckPreflightRequirementRetired, "the exact task check requirement was retired before launch", nil
	}
	checkout, err := dbgen.New(tx).GetFrozenCheckCheckoutState(ctx, work.Run.CheckoutID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (checkout.ProjectID != work.Run.ProjectID || checkout.RepositoryID != work.Run.RepositoryID || checkout.Path != work.Run.CheckoutPath || checkout.WriteMode != work.Run.CheckoutWriteMode || checkout.Availability != domain.CheckoutAvailable || checkout.Revision != work.Run.CheckoutRevision)) {
		return domain.CheckPreflightCheckoutChanged, "the exact checkout changed or became unavailable before launch", nil
	}
	if err != nil {
		return "", "", storageFailure("revalidate check launch checkout", err)
	}
	return "", "", nil
}
func equalCheckObservation(a, b domain.CheckGitObservation) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}
func validateEffectiveCheckWorkingDirectory(checkoutPath, effective string) error {
	root, err := filepath.EvalSymlinks(checkoutPath)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	candidate, err := filepath.EvalSymlinks(effective)
	if err != nil {
		return err
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("outside checkout")
	}
	return nil
}
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func optionalInt64Pointer(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func checkRunQueryError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return checkError(CodeCheckRunNotFound, "check run was not found")
	}
	return storageFailure("read check run", err)
}

func (s *Store) CheckRunDetail(ctx context.Context, workspaceIdentifier, runID string) (domain.CheckRunDetail, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	defer tx.Rollback()
	detail, err := checkRunDetailInTransaction(ctx, tx, strings.TrimSpace(runID))
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	if detail.Run.WorkspaceID != workspace.ID {
		return domain.CheckRunDetail{}, checkError(CodeCheckRunNotFound, "check run was not found")
	}
	if err := tx.Commit(); err != nil {
		return domain.CheckRunDetail{}, err
	}
	return detail, nil
}

func checkRunDetailInTransaction(ctx context.Context, tx *sql.Tx, runID string) (domain.CheckRunDetail, error) {
	work, err := checkWorkInTransaction(ctx, tx, runID)
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	detail := domain.CheckRunDetail{Run: work.Run, Definition: work.Definition, Requirement: work.Requirement, Job: work.Job, LaunchReceipt: work.LaunchReceipt, FreshnessHistory: []domain.CheckResultFreshness{}, Artifacts: []domain.CheckArtifact{}, Evidence: domain.CheckEvidenceBuckets{AgentSelfReport: []domain.CheckRequirementEvidence{}, MechanicalCheck: []domain.CheckRequirementEvidence{}, IndependentReview: []domain.CheckRequirementEvidence{}, PolicyAcceptance: []domain.CheckRequirementEvidence{}}, Notifications: []domain.CheckNotificationReceipt{}, RouteFailures: []domain.CheckRouteFailure{}, RequirementState: domain.CheckRequirementRunning}
	if work.Run.Status == domain.CheckRunFinished {
		result, freshness, err := queryCheckResultAndFreshness(ctx, tx, work.Run.ID)
		if err != nil {
			return domain.CheckRunDetail{}, err
		}
		detail.Result = result
		detail.CurrentFreshness = freshness
		if result.CheckRunID != work.Run.ID || result.RequirementID != work.Run.RequirementID || result.RequirementRevision != work.Run.RequirementRevision || result.DefinitionID != work.Run.DefinitionID || result.DefinitionContentRevision != work.Run.DefinitionContentRevision || !validCheckOutcome(result.Outcome, result.ExitCode, result.Forced) || !validCheckObservation(result.TerminalObservation) {
			return domain.CheckRunDetail{}, storageFailure("validate immutable check result", errors.New("result differs from frozen check run"))
		}
		detail.FreshnessHistory, err = queryCheckFreshnessHistory(ctx, tx, result.ID)
		if err != nil {
			return domain.CheckRunDetail{}, err
		}
		if len(detail.FreshnessHistory) == 0 || freshness == nil || detail.FreshnessHistory[len(detail.FreshnessHistory)-1].ID != freshness.ID {
			return domain.CheckRunDetail{}, storageFailure("validate check freshness history", errors.New("current freshness is absent or divergent"))
		}
		detail.Artifacts, err = queryCheckArtifacts(ctx, tx, result.ID, work.Definition.OutputByteLimit)
		if err != nil {
			return domain.CheckRunDetail{}, err
		}
		detail.Evidence, err = queryCheckEvidence(ctx, tx, *result, detail.FreshnessHistory)
		if err != nil {
			return domain.CheckRunDetail{}, err
		}
		detail.Notifications, detail.RouteFailures, err = loadCheckNotifications(ctx, dbgen.New(tx), result.ID)
		if err != nil {
			return domain.CheckRunDetail{}, storageFailure("read check notification history", err)
		}
		proposalRow, proposalErr := dbgen.New(tx).GetLatestCheckRepairProposalByResult(ctx, dbgen.GetLatestCheckRepairProposalByResultParams{WorkspaceID: work.Run.WorkspaceID, CheckResultID: result.ID})
		if proposalErr == nil {
			proposal, err := checkRepairProposalFromRow(proposalRow)
			if err != nil {
				return domain.CheckRunDetail{}, err
			}
			detail.RepairProposal = &proposal
		} else if !errors.Is(proposalErr, sql.ErrNoRows) {
			return domain.CheckRunDetail{}, storageFailure("read check repair proposal", proposalErr)
		}
		state := domain.CheckRequirementUnknown
		if freshness != nil && freshness.Status == domain.CheckFreshnessStale {
			state = domain.CheckRequirementStale
		} else if result.Outcome == domain.CheckOutcomePassed && freshness != nil && freshness.Status == domain.CheckFreshnessFresh {
			state = domain.CheckRequirementVerified
		} else if result.Outcome == domain.CheckOutcomeFailed || result.Outcome == domain.CheckOutcomeTimedOut || result.Outcome == domain.CheckOutcomeStartFailed {
			state = domain.CheckRequirementFailed
		}
		detail.RequirementState = state
	}
	return detail, nil
}

func queryCheckFreshnessHistory(ctx context.Context, db dbgen.DBTX, resultID string) ([]domain.CheckResultFreshness, error) {
	rows, err := dbgen.New(db).ListCheckFreshnessHistory(ctx, resultID)
	if err != nil {
		return nil, storageFailure("read check freshness history", err)
	}
	history := []domain.CheckResultFreshness{}
	initiallyEligible := false
	everStale := false
	for _, row := range rows {
		item, err := checkFreshnessFromRow(row)
		if err != nil {
			return nil, storageFailure("decode check freshness observation", err)
		}
		if item.CheckResultID != resultID || item.Revision != int64(len(history)+1) || !validCheckObservation(item.Observation) || !validCheckText(item.Reason, 4096) || (item.Status != domain.CheckFreshnessFresh && item.Status != domain.CheckFreshnessStale && item.Status != domain.CheckFreshnessUnknown) {
			return nil, storageFailure("validate check freshness history", errors.New("freshness row is malformed"))
		}
		if item.Revision == 1 {
			initiallyEligible = item.InitiallyEligible
		} else if item.InitiallyEligible != initiallyEligible {
			return nil, storageFailure("validate check freshness history", errors.New("initial eligibility changed"))
		}
		if everStale && (item.Status != domain.CheckFreshnessStale || !item.EverStale) || item.Status == domain.CheckFreshnessFresh && (!item.InitiallyEligible || item.EverStale) || item.Status == domain.CheckFreshnessStale && !item.EverStale {
			return nil, storageFailure("validate check freshness history", errors.New("freshness monotonicity diverged"))
		}
		everStale = item.EverStale
		history = append(history, item)
	}
	return history, nil
}

func queryCheckArtifacts(ctx context.Context, db dbgen.DBTX, resultID string, outputLimit int64) ([]domain.CheckArtifact, error) {
	rows, err := dbgen.New(db).ListCheckArtifactsForResult(ctx, resultID)
	if err != nil {
		return nil, storageFailure("read check artifacts", err)
	}
	artifacts := []domain.CheckArtifact{}
	for _, row := range rows {
		item := domain.CheckArtifact{ID: row.ID, CheckResultID: row.CheckResultID, Kind: row.Kind, ContentSHA256: row.ContentSha256, CapturedBytes: row.CapturedBytes, OmittedBytes: row.OmittedBytes, Truncated: row.Truncated != 0, CreatedAt: row.CreatedAt}
		limit := maximumCheckArtifactBytes(item.Kind)
		if item.Kind != domain.CheckArtifactDiagnostic && outputLimit < limit {
			limit = outputLimit
		}
		if item.CheckResultID != resultID || !validCheckArtifactKind(item.Kind) || !validLowerSHA256(item.ContentSHA256) || item.CapturedBytes < 0 || item.CapturedBytes > limit || item.OmittedBytes < 0 || item.Truncated != (item.OmittedBytes > 0) {
			return nil, storageFailure("validate check artifact metadata", errors.New("artifact metadata is malformed"))
		}
		artifacts = append(artifacts, item)
	}
	return artifacts, nil
}

func queryCheckEvidence(ctx context.Context, db dbgen.DBTX, result domain.CheckResult, freshness []domain.CheckResultFreshness) (domain.CheckEvidenceBuckets, error) {
	buckets := domain.CheckEvidenceBuckets{AgentSelfReport: []domain.CheckRequirementEvidence{}, MechanicalCheck: []domain.CheckRequirementEvidence{}, IndependentReview: []domain.CheckRequirementEvidence{}, PolicyAcceptance: []domain.CheckRequirementEvidence{}}
	rows, err := dbgen.New(db).ListCheckEvidenceForRequirement(ctx, dbgen.ListCheckEvidenceForRequirementParams{RequirementID: result.RequirementID, RequirementRevision: result.RequirementRevision})
	if err != nil {
		return buckets, storageFailure("read check evidence", err)
	}
	freshnessByRevision := map[int64]domain.CheckResultFreshness{}
	for _, item := range freshness {
		freshnessByRevision[item.Revision] = item
	}
	for _, row := range rows {
		item := domain.CheckRequirementEvidence{ID: row.ID, RequirementID: row.RequirementID, RequirementRevision: row.RequirementRevision, CheckResultID: row.CheckResultID, FreshnessRevision: row.FreshnessRevision, Class: row.Class, Effect: row.Effect, CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy}
		if item.CheckResultID != result.ID {
			continue
		}
		observation, ok := freshnessByRevision[item.FreshnessRevision]
		expected := domain.CheckEvidenceInconclusive
		if result.Outcome == domain.CheckOutcomePassed && observation.Status == domain.CheckFreshnessFresh {
			expected = domain.CheckEvidenceSupports
		} else if result.Outcome == domain.CheckOutcomeFailed && observation.Status == domain.CheckFreshnessFresh {
			expected = domain.CheckEvidenceContradicts
		}
		if !ok || item.RequirementID != result.RequirementID || item.RequirementRevision != result.RequirementRevision || item.Class != domain.EvidenceMechanicalCheck || item.Effect != expected {
			return buckets, storageFailure("validate check evidence", errors.New("mechanical evidence binding diverged"))
		}
		buckets.MechanicalCheck = append(buckets.MechanicalCheck, item)
	}
	if len(buckets.MechanicalCheck) != len(freshness) {
		return buckets, storageFailure("validate check evidence", errors.New("finished check must have one mechanical evidence row per freshness revision"))
	}
	for index, evidence := range buckets.MechanicalCheck {
		if evidence.FreshnessRevision != freshness[index].Revision {
			return buckets, storageFailure("validate check evidence", errors.New("mechanical evidence freshness history is not contiguous"))
		}
	}
	return buckets, nil
}

func (s *Store) CheckRuns(ctx context.Context, query ListCheckRunsQuery) ([]domain.CheckRunListItem, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(query.ProjectIdentifier)
	if projectID != "" {
		project, err := queryProject(ctx, s.db, workspace.ID, projectID)
		if err != nil {
			return nil, err
		}
		projectID = project.ID
	}
	ids, err := dbgen.New(s.db).ListCheckRunIDs(ctx, dbgen.ListCheckRunIDsParams{WorkspaceID: workspace.ID, ProjectID: projectID, TaskID: strings.TrimSpace(query.TaskID), RequirementID: strings.TrimSpace(query.RequirementID), DefinitionID: strings.TrimSpace(query.DefinitionID), Status: strings.TrimSpace(query.Status), Outcome: strings.TrimSpace(query.Outcome), ResultLimit: int64(boundedCheckLimit(query.Limit))})
	if err != nil {
		return nil, storageFailure("list check runs", err)
	}
	result := make([]domain.CheckRunListItem, 0, len(ids))
	for _, id := range ids {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		detail, err := checkRunDetailInTransaction(ctx, tx, id)
		_ = tx.Rollback()
		if err != nil {
			return nil, err
		}
		item := domain.CheckRunListItem{Run: detail.Run, CurrentFreshness: detail.CurrentFreshness, RequirementState: detail.RequirementState}
		if detail.Result != nil {
			item.Outcome = detail.Result.Outcome
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Store) InspectGrantedCheckResult(ctx context.Context, sourceRunID, checkRunID string) (domain.CheckRunDetail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	defer tx.Rollback()
	grant, err := s.authorizeRunCheckWatchGrant(ctx, tx, sourceRunID, domain.CheckWatchOperationInspect)
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	detail, err := checkRunDetailInTransaction(ctx, tx, strings.TrimSpace(checkRunID))
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	allowed := false
	for _, definition := range grant.Definitions {
		if definition.DefinitionID == detail.Run.DefinitionID && definition.ContentRevision == detail.Run.DefinitionContentRevision && definition.DefinitionSHA256 == detail.Run.DefinitionSHA256 {
			allowed = true
		}
	}
	if detail.Run.ProjectID != grant.ProjectID || !allowed {
		return domain.CheckRunDetail{}, checkError(CodeCheckWatchGrantDenied, "check result is outside current grant")
	}
	if err := tx.Commit(); err != nil {
		return domain.CheckRunDetail{}, err
	}
	return detail, nil
}

func (s *Store) ListGrantedCheckResults(ctx context.Context, query ListGrantedCheckResultsQuery) (GrantedCheckResultPage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GrantedCheckResultPage{}, err
	}
	defer tx.Rollback()
	grant, err := s.authorizeRunCheckWatchGrant(ctx, tx, query.SourceRunID, domain.CheckWatchOperationInspect)
	if err != nil {
		return GrantedCheckResultPage{}, err
	}
	limit := boundedCheckLimit(query.Limit)
	ids, err := dbgen.New(tx).ListGrantedCheckRunIDs(ctx, dbgen.ListGrantedCheckRunIDsParams{GrantID: grant.ID, ProjectID: grant.ProjectID, AfterRunID: strings.TrimSpace(query.After), ResultLimit: int64(limit + 1)})
	if err != nil {
		return GrantedCheckResultPage{}, err
	}
	page := GrantedCheckResultPage{Items: []domain.CheckRunListItem{}}
	if len(ids) > limit {
		page.NextCursor = ids[limit-1]
		ids = ids[:limit]
	}
	for _, id := range ids {
		detail, err := checkRunDetailInTransaction(ctx, tx, id)
		if err != nil {
			return GrantedCheckResultPage{}, err
		}
		item := domain.CheckRunListItem{Run: detail.Run, CurrentFreshness: detail.CurrentFreshness, RequirementState: detail.RequirementState}
		if detail.Result != nil {
			item.Outcome = detail.Result.Outcome
		}
		page.Items = append(page.Items, item)
	}
	if err := tx.Commit(); err != nil {
		return GrantedCheckResultPage{}, err
	}
	return page, nil
}

var _ = fmt.Sprintf
var _ = filepath.Clean
