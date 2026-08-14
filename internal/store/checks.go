package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	checkDefinitionCreatedEvent  = "check.definition_created"
	checkDefinitionRetiredEvent  = "check.definition_retired"
	checkRequirementCreatedEvent = "check.requirement_created"
	checkRequirementRetiredEvent = "check.requirement_retired"
	checkGrantCreatedEvent       = "check.grant_created"
	checkGrantRevokedEvent       = "check.grant_revoked"
	checkPolicyConfiguredEvent   = "check.policy_configured"
	checkRouteCreatedEvent       = "check.route_created"
	checkRouteRetiredEvent       = "check.route_retired"
	checkRunRequestedEvent       = "check.run_requested"
)

type checkDefinitionContent struct {
	WorkspaceID      string   `json:"workspace_id"`
	ProjectID        string   `json:"project_id"`
	Name             string   `json:"name"`
	Executable       string   `json:"executable"`
	Arguments        []string `json:"arguments"`
	WorkingDirectory string   `json:"working_directory"`
	TimeoutMillis    int64    `json:"timeout_millis"`
	OutputByteLimit  int64    `json:"output_byte_limit"`
}

type checkWatchGrantContent struct {
	WorkspaceID   string                             `json:"workspace_id"`
	ProjectID     string                             `json:"project_id"`
	AgentID       string                             `json:"agent_id"`
	AgentRevision int64                              `json:"agent_revision"`
	Operations    []string                           `json:"operations"`
	Definitions   []domain.CheckWatchGrantDefinition `json:"definitions"`
	MaxPending    int                                `json:"max_pending"`
	MaxInFlight   int                                `json:"max_in_flight"`
	ExpiresAt     string                             `json:"expires_at,omitempty"`
}

func (s *Store) withCheckMutationSeal(fn func() error) error {
	if !s.checkMutationSealActive.CompareAndSwap(false, true) {
		return storageFailure("enter authenticated check mutation", errors.New("check mutation seal is already active"))
	}
	defer s.checkMutationSealActive.Store(false)
	return fn()
}

func (s *Store) CreateCheckDefinition(ctx context.Context, command CreateCheckDefinitionCommand) (MutationResult[domain.CheckDefinition], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.Name = strings.TrimSpace(command.Name)
	command.Executable = filepath.Clean(strings.TrimSpace(command.Executable))
	command.WorkingDirectory = filepath.ToSlash(filepath.Clean(strings.TrimSpace(command.WorkingDirectory)))
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkingDirectory == "" {
		command.WorkingDirectory = "."
	}
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" || !validCheckText(command.Name, 128) || !filepath.IsAbs(command.Executable) ||
		!validCheckRelativePath(command.WorkingDirectory) || command.TimeoutMillis < 100 || command.TimeoutMillis > 3600000 || command.OutputByteLimit < 1024 || command.OutputByteLimit > 1048576 || len(command.Arguments) > 64 {
		return MutationResult[domain.CheckDefinition]{}, checkError(CodeInvalidCheckDefinition, "check definition requires bounded project/name, absolute executable, relative working directory, timeout, and output limit")
	}
	for _, argument := range command.Arguments {
		if !validCheckArgument(argument) {
			return MutationResult[domain.CheckDefinition]{}, checkError(CodeInvalidCheckDefinition, "check definition arguments must be NUL-free UTF-8 and at most 4096 bytes")
		}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidCheckDefinition); err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	}
	hash, err := checkSemanticHash("check.definition.create", command)
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, storageFailure("hash check definition", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, storageFailure("begin check definition", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.CheckDefinition]
	idempotencyKey := ownerCheckIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.definition.create", hash, &replay); err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	}
	id, err := randomID("checkdef_")
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, storageFailure("generate check definition id", err)
	}
	content := checkDefinitionContent{workspace.ID, project.ID, command.Name, command.Executable, append([]string{}, command.Arguments...), command.WorkingDirectory, command.TimeoutMillis, command.OutputByteLimit}
	contentJSON, contentHash, err := canonicalContent(content)
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, storageFailure("seal check definition", err)
	}
	argumentsJSON, _ := json.Marshal(command.Arguments)
	now := s.nowText()
	definition := domain.CheckDefinition{ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, Name: command.Name, Executable: command.Executable, Arguments: append([]string{}, command.Arguments...), WorkingDirectory: command.WorkingDirectory, TimeoutMillis: command.TimeoutMillis, OutputByteLimit: command.OutputByteLimit, ContentRevision: 1, ContentSHA256: contentHash, Status: domain.CheckDefinitionActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	queries := dbgen.New(tx)
	err = s.withCheckMutationSeal(func() error {
		for ordinal, argument := range command.Arguments {
			if err := queries.InsertCheckDefinitionArgument(ctx, dbgen.InsertCheckDefinitionArgumentParams{DefinitionID: id, Ordinal: int64(ordinal), Argument: argument}); err != nil {
				return storageFailure("insert check definition argument", err)
			}
		}
		return queries.InsertCheckDefinition(ctx, dbgen.InsertCheckDefinitionParams{ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, Name: command.Name, Executable: command.Executable, WorkingDirectory: command.WorkingDirectory, TimeoutMillis: command.TimeoutMillis, OutputByteLimit: command.OutputByteLimit, ArgumentsJson: string(argumentsJSON), ContentJson: string(contentJSON), ContentSha256: contentHash, CreatedAt: now})
	})
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, checkConstraint("insert check definition", CodeInvalidCheckDefinition, err)
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "check_definition", id, 1, checkDefinitionCreatedEvent, command.CorrelationID, now, map[string]any{"project_id": project.ID, "content_sha256": contentHash})
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	}
	result := MutationResult[domain.CheckDefinition]{Value: definition, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, idempotencyKey, "check.definition.create", hash, result, now); err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckDefinition]{}, storageFailure("commit check definition", err)
	}
	return result, nil
}

func (s *Store) CheckDefinition(ctx context.Context, workspaceIdentifier, identifier string) (domain.CheckDefinition, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.CheckDefinition{}, err
	}
	return queryCheckDefinition(ctx, dbgen.New(s.db), workspace.ID, strings.TrimSpace(identifier))
}

func (s *Store) CheckDefinitions(ctx context.Context, query ListCheckDefinitionsQuery) ([]domain.CheckDefinition, error) {
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
	queries := dbgen.New(s.db)
	ids, err := queries.ListCheckDefinitionIDs(ctx, dbgen.ListCheckDefinitionIDsParams{WorkspaceID: workspace.ID, ProjectID: projectID, Status: strings.TrimSpace(query.Status), ResultLimit: int64(boundedCheckLimit(query.Limit))})
	if err != nil {
		return nil, storageFailure("list check definitions", err)
	}
	result := make([]domain.CheckDefinition, 0, len(ids))
	for _, id := range ids {
		definition, err := queryCheckDefinition(ctx, queries, workspace.ID, id)
		if err != nil {
			return nil, err
		}
		result = append(result, definition)
	}
	return result, nil
}

func (s *Store) RetireCheckDefinition(ctx context.Context, command RetireCheckDefinitionCommand) (MutationResult[domain.CheckDefinition], error) {
	return s.retireCheckDefinition(ctx, command)
}

func (s *Store) retireCheckDefinition(ctx context.Context, command RetireCheckDefinitionCommand) (MutationResult[domain.CheckDefinition], error) {
	command.WorkspaceIdentifier, command.CheckDefinitionID, command.Reason, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.CheckDefinitionID), strings.TrimSpace(command.Reason), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ExpectedRevision < 1 || !validCheckText(command.Reason, 2048) {
		return MutationResult[domain.CheckDefinition]{}, checkError(CodeInvalidCheckDefinition, "retirement requires exact revision and bounded reason")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidCheckDefinition); err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	}
	hash, _ := checkSemanticHash("check.definition.retire", command)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, storageFailure("begin check definition retirement", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.CheckDefinition]
	idempotencyKey := ownerCheckIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.definition.retire", hash, &replay); err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	}
	queries := dbgen.New(tx)
	definition, err := queryCheckDefinition(ctx, queries, workspace.ID, command.CheckDefinitionID)
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	}
	if definition.Revision != command.ExpectedRevision {
		return MutationResult[domain.CheckDefinition]{}, revisionConflict("check definition", definition.ID, command.ExpectedRevision, definition.Revision)
	}
	now := s.nowText()
	err = s.withCheckMutationSeal(func() error {
		affected, err := queries.RetireCheckDefinition(ctx, dbgen.RetireCheckDefinitionParams{UpdatedAt: now, ID: definition.ID, WorkspaceID: workspace.ID, ExpectedRevision: command.ExpectedRevision})
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("definition lost active revision")
		}
		return nil
	})
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, checkConstraint("retire check definition", CodeInvalidCheckDefinition, err)
	}
	definition.Status, definition.Revision, definition.UpdatedAt = domain.CheckDefinitionRetired, definition.Revision+1, now
	sequence, err := appendEvent(ctx, tx, workspace.ID, "check_definition", definition.ID, definition.Revision, checkDefinitionRetiredEvent, command.CorrelationID, now, map[string]any{"reason": command.Reason})
	if err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	}
	result := MutationResult[domain.CheckDefinition]{Value: definition, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, idempotencyKey, "check.definition.retire", hash, result, now); err != nil {
		return MutationResult[domain.CheckDefinition]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckDefinition]{}, storageFailure("commit check definition retirement", err)
	}
	return result, nil
}

func (s *Store) CreateTaskCheckRequirement(ctx context.Context, command CreateTaskCheckRequirementCommand) (MutationResult[domain.TaskCheckRequirement], error) {
	command.WorkspaceIdentifier, command.TaskID, command.CriterionKey, command.Statement, command.CheckDefinitionID, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.TaskID), strings.TrimSpace(command.CriterionKey), strings.TrimSpace(command.Statement), strings.TrimSpace(command.CheckDefinitionID), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ExpectedTaskRevision < 1 || command.DefinitionContentRevision < 1 || !validCheckText(command.CriterionKey, 128) || !validCheckText(command.Statement, 2048) {
		return MutationResult[domain.TaskCheckRequirement]{}, checkError(CodeInvalidCheckRequirement, "check requirement requires exact task/definition revisions and bounded criterion")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidCheckRequirement); err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	}
	hash, _ := checkSemanticHash("check.requirement.create", command)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, storageFailure("begin check requirement", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.TaskCheckRequirement]
	idempotencyKey := ownerCheckIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.requirement.create", hash, &replay); err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	}
	task, err := queryTask(ctx, tx, workspace.ID, command.TaskID)
	if err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	}
	if task.Revision != command.ExpectedTaskRevision {
		return MutationResult[domain.TaskCheckRequirement]{}, revisionConflict("task", task.ID, command.ExpectedTaskRevision, task.Revision)
	}
	queries := dbgen.New(tx)
	definition, err := queryCheckDefinition(ctx, queries, workspace.ID, command.CheckDefinitionID)
	if err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	}
	if definition.ProjectID != task.ProjectID || definition.Status != domain.CheckDefinitionActive || definition.ContentRevision != command.DefinitionContentRevision {
		return MutationResult[domain.TaskCheckRequirement]{}, checkError(CodeInvalidCheckRequirement, "requirement definition must be the exact active task-project definition")
	}
	id, _ := randomID("checkreq_")
	now := s.nowText()
	requirement := domain.TaskCheckRequirement{ID: id, WorkspaceID: workspace.ID, ProjectID: task.ProjectID, TaskID: task.ID, TaskRevisionAtCreation: task.Revision, CriterionKey: command.CriterionKey, Statement: command.Statement, DefinitionID: definition.ID, DefinitionContentRevision: definition.ContentRevision, Status: domain.CheckRequirementActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	err = s.withCheckMutationSeal(func() error {
		return queries.InsertTaskCheckRequirement(ctx, dbgen.InsertTaskCheckRequirementParams{ID: id, WorkspaceID: workspace.ID, ProjectID: task.ProjectID, TaskID: task.ID, TaskRevision: task.Revision, CriterionKey: command.CriterionKey, Statement: command.Statement, DefinitionID: definition.ID, DefinitionContentRevision: definition.ContentRevision, CreatedAt: now})
	})
	if err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, checkConstraint("insert check requirement", CodeCheckRequirementConflict, err)
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "task_check_requirement", id, 1, checkRequirementCreatedEvent, command.CorrelationID, now, map[string]any{"task_id": task.ID, "definition_id": definition.ID, "definition_content_revision": definition.ContentRevision})
	if err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	}
	result := MutationResult[domain.TaskCheckRequirement]{Value: requirement, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, idempotencyKey, "check.requirement.create", hash, result, now); err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, storageFailure("commit check requirement", err)
	}
	return result, nil
}

func (s *Store) RetireTaskCheckRequirement(ctx context.Context, command RetireTaskCheckRequirementCommand) (MutationResult[domain.TaskCheckRequirement], error) {
	command.WorkspaceIdentifier, command.RequirementID, command.Reason, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.RequirementID), strings.TrimSpace(command.Reason), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ExpectedRevision < 1 || !validCheckText(command.Reason, 2048) {
		return MutationResult[domain.TaskCheckRequirement]{}, checkError(CodeInvalidCheckRequirement, "requirement retirement requires exact revision and reason")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidCheckRequirement); err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	}
	hash, _ := checkSemanticHash("check.requirement.retire", command)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, storageFailure("begin requirement retirement", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.TaskCheckRequirement]
	idempotencyKey := ownerCheckIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.requirement.retire", hash, &replay); err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	}
	queries := dbgen.New(tx)
	req, err := queryTaskCheckRequirement(ctx, queries, workspace.ID, command.RequirementID)
	if err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	}
	if req.Revision != command.ExpectedRevision {
		return MutationResult[domain.TaskCheckRequirement]{}, revisionConflict("check requirement", req.ID, command.ExpectedRevision, req.Revision)
	}
	now := s.nowText()
	err = s.withCheckMutationSeal(func() error {
		affected, err := queries.RetireTaskCheckRequirement(ctx, dbgen.RetireTaskCheckRequirementParams{UpdatedAt: now, ID: req.ID, WorkspaceID: workspace.ID, ExpectedRevision: command.ExpectedRevision})
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("requirement lost active revision")
		}
		return nil
	})
	if err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, checkConstraint("retire check requirement", CodeCheckRequirementConflict, err)
	}
	req.Status, req.Revision, req.UpdatedAt = domain.CheckRequirementRetired, req.Revision+1, now
	seq, err := appendEvent(ctx, tx, workspace.ID, "task_check_requirement", req.ID, req.Revision, checkRequirementRetiredEvent, command.CorrelationID, now, map[string]any{"reason": command.Reason})
	if err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	}
	result := MutationResult[domain.TaskCheckRequirement]{Value: req, EventSequence: seq}
	if err := recordIdempotency(ctx, tx, idempotencyKey, "check.requirement.retire", hash, result, now); err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.TaskCheckRequirement]{}, storageFailure("commit requirement retirement", err)
	}
	return result, nil
}

func (s *Store) TaskCheckRequirements(ctx context.Context, query ListTaskCheckRequirementsQuery) ([]domain.TaskCheckRequirementView, error) {
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
	ids, err := dbgen.New(s.db).ListTaskCheckRequirementIDs(ctx, dbgen.ListTaskCheckRequirementIDsParams{WorkspaceID: workspace.ID, ProjectID: projectID, TaskID: strings.TrimSpace(query.TaskID), Status: strings.TrimSpace(query.Status), ResultLimit: int64(boundedCheckLimit(query.Limit))})
	if err != nil {
		return nil, storageFailure("list check requirements", err)
	}
	result := make([]domain.TaskCheckRequirementView, 0, len(ids))
	queries := dbgen.New(s.db)
	for _, id := range ids {
		req, err := queryTaskCheckRequirement(ctx, queries, workspace.ID, id)
		if err != nil {
			return nil, err
		}
		state, runID, resultRow, freshness, err := deriveRequirementState(ctx, s.db, req)
		if err != nil {
			return nil, err
		}
		result = append(result, domain.TaskCheckRequirementView{Requirement: req, State: state, LatestCheckRunID: runID, LatestResult: resultRow, CurrentFreshness: freshness})
	}
	return result, nil
}

func queryCheckDefinition(ctx context.Context, queries *dbgen.Queries, workspaceID, identifier string) (domain.CheckDefinition, error) {
	row, err := queries.GetCheckDefinitionByID(ctx, dbgen.GetCheckDefinitionByIDParams{WorkspaceID: workspaceID, Identifier: identifier})
	if errors.Is(err, sql.ErrNoRows) {
		nameRow, nameErr := queries.GetActiveCheckDefinitionByName(ctx, dbgen.GetActiveCheckDefinitionByNameParams{WorkspaceID: workspaceID, Identifier: identifier})
		row, err = dbgen.GetCheckDefinitionByIDRow(nameRow), nameErr
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CheckDefinition{}, checkError(CodeCheckDefinitionNotFound, "check definition was not found")
	}
	if err != nil {
		return domain.CheckDefinition{}, storageFailure("read check definition", err)
	}
	args, err := queries.ListCheckDefinitionArguments(ctx, row.ID)
	if err != nil {
		return domain.CheckDefinition{}, storageFailure("read check definition arguments", err)
	}
	definition := domain.CheckDefinition{ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, Name: row.Name, Executable: row.Executable, Arguments: append([]string{}, args...), WorkingDirectory: row.WorkingDirectory, TimeoutMillis: row.TimeoutMillis, OutputByteLimit: row.OutputByteLimit, ContentRevision: row.ContentRevision, ContentSHA256: row.ContentSha256, Status: row.Status, Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy}
	_, hash, err := canonicalContent(checkDefinitionContent{WorkspaceID: definition.WorkspaceID, ProjectID: definition.ProjectID, Name: definition.Name, Executable: definition.Executable, Arguments: definition.Arguments, WorkingDirectory: definition.WorkingDirectory, TimeoutMillis: definition.TimeoutMillis, OutputByteLimit: definition.OutputByteLimit})
	if err != nil || hash != definition.ContentSHA256 || definition.ContentRevision != 1 || definition.Revision < 1 || (definition.Status != domain.CheckDefinitionActive && definition.Status != domain.CheckDefinitionRetired) {
		return domain.CheckDefinition{}, storageFailure("validate canonical check definition", errors.New("definition content, hash, or lifecycle diverged"))
	}
	return definition, nil
}
func queryTaskCheckRequirement(ctx context.Context, queries *dbgen.Queries, workspaceID, id string) (domain.TaskCheckRequirement, error) {
	row, err := queries.GetTaskCheckRequirement(ctx, dbgen.GetTaskCheckRequirementParams{WorkspaceID: workspaceID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TaskCheckRequirement{}, checkError(CodeCheckRequirementNotFound, "check requirement was not found")
	}
	if err != nil {
		return domain.TaskCheckRequirement{}, storageFailure("read check requirement", err)
	}
	return domain.TaskCheckRequirement(row), nil
}
func deriveRequirementState(ctx context.Context, db dbgen.DBTX, req domain.TaskCheckRequirement) (string, string, *domain.CheckResult, *domain.CheckResultFreshness, error) {
	row, err := dbgen.New(db).GetLatestCheckRunForRequirement(ctx, dbgen.GetLatestCheckRunForRequirementParams{RequirementID: req.ID, RequirementRevision: req.Revision})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CheckRequirementMissing, "", nil, nil, nil
	}
	if err != nil {
		return "", "", nil, nil, storageFailure("derive requirement state", err)
	}
	if row.Status != domain.CheckRunFinished {
		return domain.CheckRequirementRunning, row.ID, nil, nil, nil
	}
	result, freshness, err := queryCheckResultAndFreshness(ctx, db, row.ID)
	if err != nil {
		return "", "", nil, nil, err
	}
	state := domain.CheckRequirementUnknown
	if freshness != nil && freshness.Status == domain.CheckFreshnessStale {
		state = domain.CheckRequirementStale
	} else if result.Outcome == domain.CheckOutcomePassed && freshness != nil && freshness.Status == domain.CheckFreshnessFresh {
		state = domain.CheckRequirementVerified
	} else if result.Outcome == domain.CheckOutcomeFailed || result.Outcome == domain.CheckOutcomeTimedOut || result.Outcome == domain.CheckOutcomeStartFailed {
		state = domain.CheckRequirementFailed
	}
	return state, row.ID, result, freshness, nil
}

func queryCheckResultAndFreshness(ctx context.Context, db dbgen.DBTX, runID string) (*domain.CheckResult, *domain.CheckResultFreshness, error) {
	queries := dbgen.New(db)
	resultRow, err := queries.GetCheckResultByRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, checkError(CodeCheckRuntimeUnknown, "finished check run has no result")
	}
	if err != nil {
		return nil, nil, storageFailure("read check result", err)
	}
	result, err := checkResultFromRow(resultRow)
	if err != nil {
		return nil, nil, storageFailure("decode check terminal dirty paths", err)
	}
	if (result.DiagnosticCode != "" && !validCheckText(result.DiagnosticCode, 128)) ||
		(result.Diagnostic != "" && !validCheckText(result.Diagnostic, 4096)) ||
		!validCheckObservation(result.TerminalObservation) {
		return nil, nil, checkError(CodeInvalidRun, "committed check result is not canonical")
	}
	freshnessRow, err := queries.GetLatestCheckFreshnessByResult(ctx, result.ID)
	if err != nil {
		return &result, nil, storageFailure("read current check freshness", err)
	}
	freshness, err := checkFreshnessFromRow(freshnessRow)
	if err != nil {
		return &result, nil, storageFailure("decode freshness dirty paths", err)
	}
	return &result, &freshness, nil
}

func checkResultFromRow(row dbgen.CheckResult) (domain.CheckResult, error) {
	result := domain.CheckResult{
		ID: row.ID, CheckRunID: row.CheckRunID, RequirementID: row.RequirementID, RequirementRevision: row.RequirementRevision,
		DefinitionID: row.DefinitionID, DefinitionContentRevision: row.DefinitionContentRevision, Outcome: row.Outcome,
		Forced: row.Forced != 0, DiagnosticCode: stringValue(row.DiagnosticCode), Diagnostic: stringValue(row.Diagnostic),
		TerminalObservation: domain.CheckGitObservation{Available: row.ObservationAvailable != 0, RepositoryID: row.RepositoryID, ObjectFormat: row.ObjectFormat, CheckoutID: row.CheckoutID, Branch: stringValue(row.Branch), HeadCommit: stringValue(row.HeadCommit), Dirty: row.Dirty != 0, ObservedAt: row.ObservedAt, DiagnosticCode: stringValue(row.ObservationDiagnosticCode), Diagnostic: stringValue(row.ObservationDiagnostic)},
		CreatedAt:           row.CreatedAt, CreatedBy: row.CreatedBy,
	}
	if row.ExitCode != nil {
		value := int(*row.ExitCode)
		result.ExitCode = &value
	}
	if err := json.Unmarshal([]byte(row.DirtyPathsJson), &result.TerminalObservation.DirtyPaths); err != nil {
		return domain.CheckResult{}, err
	}
	return result, nil
}

func checkFreshnessFromRow(row dbgen.CheckResultFreshness) (domain.CheckResultFreshness, error) {
	freshness := domain.CheckResultFreshness{
		ID: row.ID, CheckResultID: row.CheckResultID, Revision: row.Revision, Status: row.Status, Reason: row.Reason,
		InitiallyEligible: row.InitiallyEligible != 0, EverStale: row.EverStale != 0,
		Observation: domain.CheckGitObservation{Available: row.ObservationAvailable != 0, RepositoryID: row.RepositoryID, ObjectFormat: row.ObjectFormat, CheckoutID: row.CheckoutID, Branch: stringValue(row.Branch), HeadCommit: stringValue(row.HeadCommit), Dirty: row.Dirty != 0, ObservedAt: row.ObservedAt, DiagnosticCode: stringValue(row.DiagnosticCode), Diagnostic: stringValue(row.Diagnostic)},
		CreatedAt:   row.CreatedAt, CreatedBy: row.CreatedBy,
	}
	if err := json.Unmarshal([]byte(row.DirtyPathsJson), &freshness.Observation.DirtyPaths); err != nil {
		return domain.CheckResultFreshness{}, err
	}
	return freshness, nil
}

func validCheckText(value string, max int) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') && len([]byte(value)) <= max
}
func validCheckArgument(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') && len([]byte(value)) <= 4096
}
func validCheckRelativePath(value string) bool {
	if value == "" || filepath.IsAbs(value) || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	return clean != ".." && !strings.HasPrefix(clean, "../") && clean == value
}
func boundedCheckLimit(value int) int {
	if value <= 0 {
		return 100
	}
	if value > 200 {
		return 200
	}
	return value
}
func checkError(code, message string) error { return &Error{Code: code, Message: message} }
func checkConstraint(action, code string, err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "constraint") || strings.Contains(strings.ToLower(err.Error()), "check ") {
		return &Error{Code: code, Message: action, Cause: err}
	}
	return storageFailure(action, err)
}

// Keep imported helpers exercised while the remaining vertical is split into
// checks_authority.go and checks_execution.go.
var _ = fmt.Sprintf
var _ = sort.Strings
var _ = time.Second
