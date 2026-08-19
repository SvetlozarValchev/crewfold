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
	agentCreated                      = "agent.created"
	agentUpdated                      = "agent.updated"
	objectiveCreated                  = "objective.created"
	objectiveUpdated                  = "objective.updated"
	taskCreated                       = "task.created"
	taskDependencyAdded               = "task.dependency_added"
	taskAssigned                      = "task.assigned"
	taskStarted                       = "task.started"
	taskBlocked                       = "task.blocked"
	taskReadied                       = "task.readied"
	taskCancelled                     = "task.cancelled"
	taskAssignmentExpired             = "task.assignment_expired"
	ownerTaskCancellationIntentReason = "task cancelled by local owner"
	// MaximumLeaseReconciliationBatchLimit bounds each daemon-owned lease
	// transaction. Command paths that require complete reconciliation continue to
	// use ReconcileExpiredAssignments without a batch limit.
	MaximumLeaseReconciliationBatchLimit = 32
)

func (s *Store) CreateAgent(ctx context.Context, command CreateAgentCommand) (MutationResult[domain.AgentDefinition], error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	name := strings.TrimSpace(command.Name)
	role := strings.TrimSpace(command.Role)
	provider := strings.TrimSpace(command.Provider)
	runtimeName := strings.TrimSpace(command.Runtime)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	if runtimeName == "" {
		runtimeName = "unconfigured"
	}
	if command.MaxConcurrency == 0 {
		command.MaxConcurrency = 1
	}
	if workspaceIdentifier == "" || !workspaceNamePattern.MatchString(name) || !validShortText(role) || !validShortText(provider) || !validShortText(runtimeName) || command.MaxConcurrency < 1 || command.MaxConcurrency > 100 {
		return MutationResult[domain.AgentDefinition]{}, &Error{Code: CodeInvalidAgent, Message: "agent requires a workspace, lowercase name, role, provider, runtime, and max concurrency from 1 to 100"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidAgent); err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	}
	requestHash, err := hashCommand("agent.create", map[string]any{"workspace": workspaceIdentifier, "name": name, "role": role, "provider": provider, "runtime": runtimeName, "max_concurrency": command.MaxConcurrency})
	if err != nil {
		return MutationResult[domain.AgentDefinition]{}, storageFailure("hash agent creation", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.AgentDefinition]{}, storageFailure("begin agent creation", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.AgentDefinition]
	if found, err := lookupIdempotency(ctx, tx, key, "agent.create", requestHash, &replay); err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	}
	var existingID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM agents WHERE workspace_id = ? AND name = ?", workspace.ID, name).Scan(&existingID)
	if err == nil {
		return MutationResult[domain.AgentDefinition]{}, &Error{Code: CodeAgentExists, Message: fmt.Sprintf("agent %q already exists as %s", name, existingID)}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MutationResult[domain.AgentDefinition]{}, storageFailure("check agent name", err)
	}
	now := s.nowText()
	id, err := randomID("agent_")
	if err != nil {
		return MutationResult[domain.AgentDefinition]{}, storageFailure("generate agent id", err)
	}
	agent := domain.AgentDefinition{ID: id, WorkspaceID: workspace.ID, Name: name, Role: role, Provider: provider, Runtime: runtimeName, Enabled: true, MaxConcurrency: command.MaxConcurrency, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO agents(id, workspace_id, name, role, provider, runtime, enabled, max_concurrency, revision, created_at, updated_at, created_by, updated_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, agent.ID, agent.WorkspaceID, agent.Name, agent.Role, agent.Provider, agent.Runtime, agent.Enabled, agent.MaxConcurrency, agent.Revision, agent.CreatedAt, agent.UpdatedAt, agent.CreatedBy, agent.UpdatedBy); err != nil {
		return MutationResult[domain.AgentDefinition]{}, storageFailure("insert agent projection", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "agent", agent.ID, agent.Revision, agentCreated, correlationID, now, map[string]any{"name": agent.Name, "role": agent.Role, "provider": agent.Provider, "runtime": agent.Runtime, "enabled": agent.Enabled, "max_concurrency": agent.MaxConcurrency})
	if err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	}
	result := MutationResult[domain.AgentDefinition]{Value: agent, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	}
	if err := recordIdempotency(ctx, tx, key, "agent.create", requestHash, result, now); err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.AgentDefinition]{}, storageFailure("commit agent creation", err)
	}
	return result, nil
}

func (s *Store) UpdateAgent(ctx context.Context, command UpdateAgentCommand) (MutationResult[domain.AgentDefinition], error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	agentIdentifier := strings.TrimSpace(command.AgentIdentifier)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	if workspaceIdentifier == "" || agentIdentifier == "" || command.ExpectedRevision < 1 || (command.Role == nil && command.Provider == nil && command.Runtime == nil && command.Enabled == nil && command.MaxConcurrency == nil) {
		return MutationResult[domain.AgentDefinition]{}, &Error{Code: CodeInvalidAgent, Message: "agent update requires workspace, agent, expected revision, and at least one changed field"}
	}
	if command.Role != nil && !validShortText(strings.TrimSpace(*command.Role)) || command.Provider != nil && !validShortText(strings.TrimSpace(*command.Provider)) || command.Runtime != nil && !validShortText(strings.TrimSpace(*command.Runtime)) || command.MaxConcurrency != nil && (*command.MaxConcurrency < 1 || *command.MaxConcurrency > 100) {
		return MutationResult[domain.AgentDefinition]{}, &Error{Code: CodeInvalidAgent, Message: "agent update fields are invalid"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidAgent); err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	}
	requestHash, err := hashCommand("agent.update", map[string]any{
		"workspace": workspaceIdentifier, "agent": agentIdentifier, "role": command.Role,
		"provider": command.Provider, "runtime": command.Runtime, "enabled": command.Enabled,
		"max_concurrency": command.MaxConcurrency, "expected_revision": command.ExpectedRevision,
	})
	if err != nil {
		return MutationResult[domain.AgentDefinition]{}, storageFailure("hash agent update", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.AgentDefinition]{}, storageFailure("begin agent update", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.AgentDefinition]
	if found, err := lookupIdempotency(ctx, tx, key, "agent.update", requestHash, &replay); err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, agentIdentifier)
	if err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	}
	if agent.Revision != command.ExpectedRevision {
		return MutationResult[domain.AgentDefinition]{}, revisionConflict("agent", agent.ID, command.ExpectedRevision, agent.Revision)
	}
	if command.Role != nil {
		agent.Role = strings.TrimSpace(*command.Role)
	}
	if command.Provider != nil {
		agent.Provider = strings.TrimSpace(*command.Provider)
	}
	if command.Runtime != nil {
		agent.Runtime = strings.TrimSpace(*command.Runtime)
	}
	if command.Enabled != nil {
		agent.Enabled = *command.Enabled
	}
	if command.MaxConcurrency != nil {
		agent.MaxConcurrency = *command.MaxConcurrency
	}
	now := s.nowText()
	agent.Revision++
	agent.UpdatedAt, agent.UpdatedBy = now, localOwnerActorID
	if _, err := tx.ExecContext(ctx, "UPDATE agents SET role = ?, provider = ?, runtime = ?, enabled = ?, max_concurrency = ?, revision = ?, updated_at = ?, updated_by = ? WHERE id = ?", agent.Role, agent.Provider, agent.Runtime, agent.Enabled, agent.MaxConcurrency, agent.Revision, agent.UpdatedAt, agent.UpdatedBy, agent.ID); err != nil {
		return MutationResult[domain.AgentDefinition]{}, storageFailure("update agent projection", err)
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "agent", agent.ID, agent.Revision, agentUpdated, correlationID, now, map[string]any{"role": agent.Role, "provider": agent.Provider, "runtime": agent.Runtime, "enabled": agent.Enabled, "max_concurrency": agent.MaxConcurrency})
	if err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	}
	result := MutationResult[domain.AgentDefinition]{Value: agent, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "agent.update", requestHash, result, now); err != nil {
		return MutationResult[domain.AgentDefinition]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.AgentDefinition]{}, storageFailure("commit agent update", err)
	}
	return result, nil
}

func (s *Store) Agent(ctx context.Context, workspaceIdentifier, agentIdentifier string) (domain.AgentDefinition, error) {
	workspace, err := s.Workspace(ctx, workspaceIdentifier)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	return queryAgent(ctx, s.db, workspace.ID, strings.TrimSpace(agentIdentifier))
}

func (s *Store) CreateObjective(ctx context.Context, command CreateObjectiveCommand) (MutationResult[domain.Objective], error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	projectIdentifier := strings.TrimSpace(command.ProjectIdentifier)
	title := strings.TrimSpace(command.Title)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	primaryCheckoutID := strings.TrimSpace(command.PrimaryCheckoutID)
	if workspaceIdentifier == "" || projectIdentifier == "" || !validTitle(title) || !validBudget(command.Budget) {
		return MutationResult[domain.Objective]{}, &Error{Code: CodeInvalidObjective, Message: "objective requires workspace, project, a title of at most 256 characters, and non-negative budgets"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidObjective); err != nil {
		return MutationResult[domain.Objective]{}, err
	}
	requestHash, err := hashCommand("objective.create", map[string]any{"workspace": workspaceIdentifier, "project": projectIdentifier, "primary_checkout_id": primaryCheckoutID, "reference_checkout_ids": command.ReferenceCheckoutIDs, "title": title, "budget": command.Budget})
	if err != nil {
		return MutationResult[domain.Objective]{}, storageFailure("hash objective creation", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.Objective]{}, storageFailure("begin objective creation", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.Objective]
	if found, err := lookupIdempotency(ctx, tx, key, "objective.create", requestHash, &replay); err != nil {
		return MutationResult[domain.Objective]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return MutationResult[domain.Objective]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, projectIdentifier)
	if err != nil {
		return MutationResult[domain.Objective]{}, err
	}
	referenceCheckoutIDs, err := validateObjectiveCheckouts(ctx, tx, project.ID, primaryCheckoutID, command.ReferenceCheckoutIDs)
	if err != nil {
		return MutationResult[domain.Objective]{}, err
	}
	now := s.nowText()
	id, err := randomID("obj_")
	if err != nil {
		return MutationResult[domain.Objective]{}, storageFailure("generate objective id", err)
	}
	objective := domain.Objective{ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, PrimaryCheckoutID: primaryCheckoutID, Title: title, Status: domain.ObjectiveActive, Budget: command.Budget, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO objectives(id, workspace_id, project_id, primary_checkout_id, title, status, budget_tokens, budget_cost_cents, budget_time_seconds, revision, created_at, updated_at, created_by, updated_by)
VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, objective.ID, objective.WorkspaceID, objective.ProjectID, objective.PrimaryCheckoutID, objective.Title, objective.Status, objective.Budget.TokenLimit, objective.Budget.CostCents, objective.Budget.TimeSeconds, objective.Revision, objective.CreatedAt, objective.UpdatedAt, objective.CreatedBy, objective.UpdatedBy); err != nil {
		return MutationResult[domain.Objective]{}, storageFailure("insert objective projection", err)
	}
	for ordinal, checkoutID := range referenceCheckoutIDs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO objective_reference_checkouts(objective_id,checkout_id,ordinal,created_at,created_by) VALUES(?,?,?,?,?)", objective.ID, checkoutID, ordinal, now, localOwnerActorID); err != nil {
			return MutationResult[domain.Objective]{}, storageFailure("insert objective reference checkout", err)
		}
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "objective", objective.ID, objective.Revision, objectiveCreated, correlationID, now, map[string]any{"project_id": objective.ProjectID, "primary_checkout_id": objective.PrimaryCheckoutID, "reference_checkout_ids": referenceCheckoutIDs, "title": objective.Title, "budget": objective.Budget})
	if err != nil {
		return MutationResult[domain.Objective]{}, err
	}
	result := MutationResult[domain.Objective]{Value: objective, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "objective.create", requestHash, result, now); err != nil {
		return MutationResult[domain.Objective]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.Objective]{}, storageFailure("commit objective creation", err)
	}
	return result, nil
}

func (s *Store) UpdateObjective(ctx context.Context, command UpdateObjectiveCommand) (MutationResult[domain.Objective], error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	objectiveID := strings.TrimSpace(command.ObjectiveID)
	key, correlationID := strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if workspaceIdentifier == "" || objectiveID == "" || command.ExpectedRevision < 1 || (command.Title == nil && command.Status == nil && command.Budget == nil) {
		return MutationResult[domain.Objective]{}, &Error{Code: CodeInvalidObjective, Message: "objective update requires workspace, objective ID, expected revision, and a changed field"}
	}
	if command.Title != nil && !validTitle(strings.TrimSpace(*command.Title)) || command.Status != nil && !validObjectiveStatus(strings.TrimSpace(*command.Status)) || command.Budget != nil && !validBudget(*command.Budget) {
		return MutationResult[domain.Objective]{}, &Error{Code: CodeInvalidObjective, Message: "objective update fields are invalid"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidObjective); err != nil {
		return MutationResult[domain.Objective]{}, err
	}
	requestHash, err := hashCommand("objective.update", command)
	if err != nil {
		return MutationResult[domain.Objective]{}, storageFailure("hash objective update", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.Objective]{}, storageFailure("begin objective update", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.Objective]
	if found, err := lookupIdempotency(ctx, tx, key, "objective.update", requestHash, &replay); err != nil {
		return MutationResult[domain.Objective]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return MutationResult[domain.Objective]{}, err
	}
	objective, err := queryObjective(ctx, tx, workspace.ID, objectiveID)
	if err != nil {
		return MutationResult[domain.Objective]{}, err
	}
	if objective.Revision != command.ExpectedRevision {
		return MutationResult[domain.Objective]{}, revisionConflict("objective", objective.ID, command.ExpectedRevision, objective.Revision)
	}
	if command.Title != nil {
		objective.Title = strings.TrimSpace(*command.Title)
	}
	if command.Status != nil {
		objective.Status = strings.TrimSpace(*command.Status)
	}
	if command.Budget != nil {
		objective.Budget = *command.Budget
	}
	now := s.nowText()
	objective.Revision++
	objective.UpdatedAt, objective.UpdatedBy = now, localOwnerActorID
	if _, err := tx.ExecContext(ctx, "UPDATE objectives SET title = ?, status = ?, budget_tokens = ?, budget_cost_cents = ?, budget_time_seconds = ?, revision = ?, updated_at = ?, updated_by = ? WHERE id = ?", objective.Title, objective.Status, objective.Budget.TokenLimit, objective.Budget.CostCents, objective.Budget.TimeSeconds, objective.Revision, objective.UpdatedAt, objective.UpdatedBy, objective.ID); err != nil {
		return MutationResult[domain.Objective]{}, storageFailure("update objective projection", err)
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "objective", objective.ID, objective.Revision, objectiveUpdated, correlationID, now, map[string]any{"title": objective.Title, "status": objective.Status, "budget": objective.Budget})
	if err != nil {
		return MutationResult[domain.Objective]{}, err
	}
	result := MutationResult[domain.Objective]{Value: objective, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "objective.update", requestHash, result, now); err != nil {
		return MutationResult[domain.Objective]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.Objective]{}, storageFailure("commit objective update", err)
	}
	return result, nil
}

func (s *Store) Objective(ctx context.Context, workspaceIdentifier, objectiveID string) (domain.Objective, error) {
	workspace, err := s.Workspace(ctx, workspaceIdentifier)
	if err != nil {
		return domain.Objective{}, err
	}
	return queryObjective(ctx, s.db, workspace.ID, strings.TrimSpace(objectiveID))
}

func (s *Store) CreateTask(ctx context.Context, command CreateTaskCommand) (TaskMutationResult, error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	projectIdentifier := strings.TrimSpace(command.ProjectIdentifier)
	title := strings.TrimSpace(command.Title)
	description := strings.TrimSpace(command.Description)
	objectiveID := strings.TrimSpace(command.ObjectiveID)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	if workspaceIdentifier == "" || projectIdentifier == "" || !validTitle(title) || len(description) > 4096 || command.Priority < 0 || command.Priority > 1000 || !validBudget(command.Budget) {
		return TaskMutationResult{}, &Error{Code: CodeInvalidTask, Message: "task requires workspace, project, a title, priority from 0 to 1000, and non-negative budgets"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidTask); err != nil {
		return TaskMutationResult{}, err
	}
	requestHash, err := hashCommand("task.create", map[string]any{"workspace": workspaceIdentifier, "project": projectIdentifier, "objective_id": objectiveID, "title": title, "description": description, "priority": command.Priority, "budget": command.Budget})
	if err != nil {
		return TaskMutationResult{}, storageFailure("hash task creation", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return TaskMutationResult{}, storageFailure("begin task creation", err)
	}
	defer tx.Rollback()
	var replay TaskMutationResult
	if found, err := lookupIdempotency(ctx, tx, key, "task.create", requestHash, &replay); err != nil {
		return TaskMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return TaskMutationResult{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, projectIdentifier)
	if err != nil {
		return TaskMutationResult{}, err
	}
	if objectiveID != "" {
		objective, err := queryObjective(ctx, tx, workspace.ID, objectiveID)
		if err != nil {
			return TaskMutationResult{}, err
		}
		if objective.ProjectID != project.ID || objective.Status != domain.ObjectiveActive {
			return TaskMutationResult{}, &Error{Code: CodeInvalidTask, Message: "task objective must be active and belong to the selected project"}
		}
		objectiveID = objective.ID
	}
	now := s.nowText()
	id, err := randomID("task_")
	if err != nil {
		return TaskMutationResult{}, storageFailure("generate task id", err)
	}
	task := domain.Task{ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, ObjectiveID: objectiveID, Title: title, Description: description, Status: domain.TaskReady, Priority: command.Priority, Budget: command.Budget, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO tasks(id, workspace_id, project_id, objective_id, title, description, status, blocked_reason, priority, budget_tokens, budget_cost_cents, budget_time_seconds, revision, created_at, updated_at, created_by, updated_by)
VALUES (?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, task.ID, task.WorkspaceID, task.ProjectID, task.ObjectiveID, task.Title, task.Description, task.Status, task.Priority, task.Budget.TokenLimit, task.Budget.CostCents, task.Budget.TimeSeconds, task.Revision, task.CreatedAt, task.UpdatedAt, task.CreatedBy, task.UpdatedBy); err != nil {
		return TaskMutationResult{}, storageFailure("insert task projection", err)
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "task", task.ID, task.Revision, taskCreated, correlationID, now, map[string]any{"project_id": task.ProjectID, "objective_id": task.ObjectiveID, "title": task.Title, "description": task.Description, "priority": task.Priority, "budget": task.Budget})
	if err != nil {
		return TaskMutationResult{}, err
	}
	detail := domain.TaskDetail{Task: task, Dependencies: []domain.TaskDependency{}, Readiness: domain.TaskReadiness{Ready: true, Reason: "task has no incomplete dependencies and is unassigned"}}
	result := TaskMutationResult{Detail: detail, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "task.create", requestHash, result, now); err != nil {
		return TaskMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskMutationResult{}, storageFailure("commit task creation", err)
	}
	return result, nil
}

func (s *Store) AddTaskDependency(ctx context.Context, command AddTaskDependencyCommand) (TaskMutationResult, error) {
	deliveryRequirement := strings.TrimSpace(command.DeliveryRequirement)
	if deliveryRequirement == "" {
		deliveryRequirement = domain.DependencyDeliveryCompletion
	}
	if !validDependencyDelivery(deliveryRequirement) {
		return TaskMutationResult{}, &Error{Code: CodeInvalidTask, Message: "task dependency delivery requirement is invalid"}
	}
	command.DeliveryRequirement = deliveryRequirement
	return s.mutateTask(ctx, "task.dependency.add", command.IdempotencyKey, command.CorrelationID, command, func(tx *sql.Tx, workspace Workspace, task *domain.Task, now string) (string, map[string]any, error) {
		dependsOn, err := queryTask(ctx, tx, workspace.ID, strings.TrimSpace(command.DependsOnTaskID))
		if err != nil {
			return "", nil, err
		}
		if task.ProjectID != dependsOn.ProjectID || task.ID == dependsOn.ID {
			return "", nil, &Error{Code: CodeDependencyCycle, Message: "task dependencies must be distinct tasks in one project"}
		}
		var existing int
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM task_dependencies WHERE task_id = ? AND depends_on_task_id = ?", task.ID, dependsOn.ID).Scan(&existing)
		if err == nil {
			return "", nil, &Error{Code: CodeDependencyExists, Message: "task dependency already exists"}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", nil, storageFailure("check task dependency", err)
		}
		var cycle int
		err = tx.QueryRowContext(ctx, `
WITH RECURSIVE reachable(id) AS (
    SELECT depends_on_task_id FROM task_dependencies WHERE task_id = ?
    UNION
    SELECT td.depends_on_task_id FROM task_dependencies td JOIN reachable r ON td.task_id = r.id
)
SELECT 1 FROM reachable WHERE id = ? LIMIT 1`, dependsOn.ID, task.ID).Scan(&cycle)
		if err == nil {
			return "", nil, &Error{Code: CodeDependencyCycle, Message: "task dependency would create a cycle"}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", nil, storageFailure("check task dependency cycle", err)
		}
		if task.Status != domain.TaskReady {
			return "", nil, &Error{Code: CodeInvalidTransition, Message: "dependencies can only be added while a task is ready and unassigned"}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO task_dependencies(task_id, depends_on_task_id, delivery_requirement, created_at, created_by) VALUES (?, ?, ?, ?, ?)", task.ID, dependsOn.ID, deliveryRequirement, now, localOwnerActorID); err != nil {
			return "", nil, storageFailure("insert task dependency", err)
		}
		return taskDependencyAdded, map[string]any{"depends_on_task_id": dependsOn.ID, "delivery_requirement": deliveryRequirement}, nil
	})
}

func (s *Store) UpdateTask(ctx context.Context, command UpdateTaskCommand) (TaskMutationResult, error) {
	if command.Title == nil && command.Description == nil && command.Priority == nil && command.Budget == nil {
		return TaskMutationResult{}, &Error{Code: CodeInvalidTask, Message: "task update requires at least one changed field"}
	}
	if command.Title != nil && !validTitle(strings.TrimSpace(*command.Title)) || command.Description != nil && len(strings.TrimSpace(*command.Description)) > 4096 || command.Priority != nil && (*command.Priority < 0 || *command.Priority > 1000) || command.Budget != nil && !validBudget(*command.Budget) {
		return TaskMutationResult{}, &Error{Code: CodeInvalidTask, Message: "task update fields are invalid"}
	}
	return s.mutateTask(ctx, "task.update", command.IdempotencyKey, command.CorrelationID, command, func(_ *sql.Tx, _ Workspace, task *domain.Task, _ string) (string, map[string]any, error) {
		if task.Status == domain.TaskCancelled || task.Status == domain.TaskCompleted {
			return "", nil, invalidTaskTransition(task.Status, "update")
		}
		if command.Title != nil {
			task.Title = strings.TrimSpace(*command.Title)
		}
		if command.Description != nil {
			task.Description = strings.TrimSpace(*command.Description)
		}
		if command.Priority != nil {
			task.Priority = *command.Priority
		}
		if command.Budget != nil {
			task.Budget = *command.Budget
		}
		return "task.updated", map[string]any{"title": task.Title, "description": task.Description, "priority": task.Priority, "budget": task.Budget}, nil
	})
}

func (s *Store) AssignTask(ctx context.Context, command AssignTaskCommand) (TaskMutationResult, error) {
	if command.LeaseSeconds < 1 || command.LeaseSeconds > 30*24*60*60 {
		return TaskMutationResult{}, &Error{Code: CodeInvalidTask, Message: "assignment lease must be from 1 second to 30 days"}
	}
	return s.mutateTask(ctx, "task.assign", command.IdempotencyKey, command.CorrelationID, command, func(tx *sql.Tx, workspace Workspace, task *domain.Task, now string) (string, map[string]any, error) {
		if task.AssignmentID != "" {
			return "", nil, &Error{Code: CodeAssignmentConflict, Message: fmt.Sprintf("task already has active assignment %s", task.AssignmentID)}
		}
		var openIntentID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM scheduling_intents
WHERE task_id=? AND status IN ('pending','deferred','awaiting_approval','run_requested')
ORDER BY created_at,id LIMIT 1`, task.ID).Scan(&openIntentID)
		if err == nil {
			return "", nil, &Error{Code: CodeAssignmentConflict, Message: fmt.Sprintf("task already has open scheduling intent %s", openIntentID)}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", nil, storageFailure("check task scheduling intent before assignment", err)
		}
		agent, err := queryAgent(ctx, tx, workspace.ID, strings.TrimSpace(command.AgentIdentifier))
		if err != nil {
			return "", nil, err
		}
		if !agent.Enabled {
			return "", nil, &Error{Code: CodeAssignmentConflict, Message: "disabled agent cannot receive an assignment"}
		}
		readiness, err := taskReadiness(ctx, tx, *task)
		if err != nil {
			return "", nil, err
		}
		if !readiness.Ready {
			return "", nil, &Error{Code: CodeTaskNotReady, Message: readiness.Reason}
		}
		assignmentID, err := randomID("asg_")
		if err != nil {
			return "", nil, storageFailure("generate assignment id", err)
		}
		baseTime, err := time.Parse(time.RFC3339Nano, now)
		if err != nil {
			return "", nil, storageFailure("parse assignment timestamp", err)
		}
		leaseExpiresAt := baseTime.Add(time.Duration(command.LeaseSeconds) * time.Second).Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO task_assignments(id, task_id, agent_id, status, lease_expires_at, revision, created_at, updated_at, created_by, updated_by)
VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`, assignmentID, task.ID, agent.ID, domain.AssignmentActive, leaseExpiresAt, now, now, localOwnerActorID, localOwnerActorID); err != nil {
			return "", nil, storageFailure("insert task assignment", err)
		}
		task.Status = domain.TaskAssigned
		return taskAssigned, map[string]any{"assignment_id": assignmentID, "agent_id": agent.ID, "lease_expires_at": leaseExpiresAt}, nil
	})
}

func (s *Store) TransitionTask(ctx context.Context, command TransitionTaskCommand) (TaskMutationResult, error) {
	action := strings.TrimSpace(command.Action)
	return s.mutateTask(ctx, "task."+action, command.IdempotencyKey, command.CorrelationID, command, func(tx *sql.Tx, workspace Workspace, task *domain.Task, now string) (string, map[string]any, error) {
		switch action {
		case "start":
			if task.Status != domain.TaskAssigned {
				return "", nil, invalidTaskTransition(task.Status, action)
			}
			if err := rejectOwnerTransitionWithReservedRun(ctx, tx, task.ID, action); err != nil {
				return "", nil, err
			}
			task.Status = domain.TaskActive
			return taskStarted, map[string]any{}, nil
		case "block":
			reason := strings.TrimSpace(command.Reason)
			if reason == "" || len(reason) > 1024 || (task.Status != domain.TaskReady && task.Status != domain.TaskAssigned && task.Status != domain.TaskActive) {
				return "", nil, invalidTaskTransition(task.Status, action)
			}
			if err := rejectOwnerTransitionWithReservedRun(ctx, tx, task.ID, action); err != nil {
				return "", nil, err
			}
			task.Status, task.BlockedReason = domain.TaskBlocked, reason
			return taskBlocked, map[string]any{"reason": reason}, nil
		case "unblock":
			if task.Status != domain.TaskBlocked {
				return "", nil, invalidTaskTransition(task.Status, action)
			}
			if err := rejectOwnerTransitionWithReservedRun(ctx, tx, task.ID, action); err != nil {
				return "", nil, err
			}
			var active int
			err := tx.QueryRowContext(ctx, "SELECT 1 FROM task_assignments WHERE task_id = ? AND status = 'active' LIMIT 1", task.ID).Scan(&active)
			if err == nil {
				task.Status = domain.TaskAssigned
			} else if errors.Is(err, sql.ErrNoRows) {
				task.Status = domain.TaskReady
			} else {
				return "", nil, storageFailure("check task assignment while unblocking", err)
			}
			task.BlockedReason = ""
			return taskReadied, map[string]any{}, nil
		case "cancel":
			if task.Status == domain.TaskCancelled || task.Status == domain.TaskCompleted {
				return "", nil, invalidTaskTransition(task.Status, action)
			}
			if err := rejectOwnerTransitionWithReservedRun(ctx, tx, task.ID, action); err != nil {
				return "", nil, err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE task_assignments SET status = 'released', revision = revision + 1, updated_at = ?, updated_by = ? WHERE task_id = ? AND status = 'active'", now, localOwnerActorID, task.ID); err != nil {
				return "", nil, storageFailure("release cancelled task assignment", err)
			}
			task.Status, task.BlockedReason = domain.TaskCancelled, ""
			return taskCancelled, map[string]any{}, nil
		default:
			return "", nil, &Error{Code: CodeInvalidTransition, Message: "task action must be start, block, unblock, or cancel"}
		}
	})
}

func (s *Store) mutateTask(ctx context.Context, commandName, key, correlationID string, command any, change func(*sql.Tx, Workspace, *domain.Task, string) (string, map[string]any, error)) (TaskMutationResult, error) {
	key, correlationID = strings.TrimSpace(key), strings.TrimSpace(correlationID)
	if err := validateMutationMetadata(key, correlationID, CodeInvalidTask); err != nil {
		return TaskMutationResult{}, err
	}
	commandData, err := json.Marshal(command)
	if err != nil {
		return TaskMutationResult{}, storageFailure("encode task command", err)
	}
	var common struct {
		WorkspaceIdentifier string
		TaskID              string
		ExpectedRevision    int64
	}
	if err := json.Unmarshal(commandData, &common); err != nil || strings.TrimSpace(common.WorkspaceIdentifier) == "" || strings.TrimSpace(common.TaskID) == "" || common.ExpectedRevision < 1 {
		return TaskMutationResult{}, &Error{Code: CodeInvalidTask, Message: "task mutation requires workspace, task ID, and expected revision"}
	}
	requestHash, err := hashCommand(commandName, command)
	if err != nil {
		return TaskMutationResult{}, storageFailure("hash task mutation", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return TaskMutationResult{}, storageFailure("begin task mutation", err)
	}
	defer tx.Rollback()
	var replay TaskMutationResult
	if found, err := lookupIdempotency(ctx, tx, key, commandName, requestHash, &replay); err != nil {
		return TaskMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(common.WorkspaceIdentifier))
	if err != nil {
		return TaskMutationResult{}, err
	}
	now := s.nowText()
	task, err := queryTask(ctx, tx, workspace.ID, strings.TrimSpace(common.TaskID))
	if err != nil {
		return TaskMutationResult{}, err
	}
	if task.Revision != common.ExpectedRevision {
		return TaskMutationResult{}, revisionConflict("task", task.ID, common.ExpectedRevision, task.Revision)
	}
	eventType, eventData, err := change(tx, workspace, &task, now)
	if err != nil {
		return TaskMutationResult{}, err
	}
	task.Revision++
	task.UpdatedAt, task.UpdatedBy = now, localOwnerActorID
	if _, err := tx.ExecContext(ctx, `
UPDATE tasks SET title = ?, description = NULLIF(?, ''), status = ?, blocked_reason = NULLIF(?, ''), priority = ?,
    budget_tokens = ?, budget_cost_cents = ?, budget_time_seconds = ?, revision = ?, updated_at = ?, updated_by = ?
WHERE id = ?`, task.Title, task.Description, task.Status, task.BlockedReason, task.Priority, task.Budget.TokenLimit, task.Budget.CostCents, task.Budget.TimeSeconds, task.Revision, task.UpdatedAt, task.UpdatedBy, task.ID); err != nil {
		return TaskMutationResult{}, storageFailure("update task projection", err)
	}
	eventData["status"] = task.Status
	sequence, err := appendEvent(ctx, tx, workspace.ID, "task", task.ID, task.Revision, eventType, correlationID, now, eventData)
	if err != nil {
		return TaskMutationResult{}, err
	}
	if eventType == taskCancelled {
		if err := cancelPendingSchedulingIntentForTask(ctx, tx, workspace.ID, task, correlationID, now); err != nil {
			return TaskMutationResult{}, err
		}
	}
	detail, err := taskDetailInTransaction(ctx, tx, task)
	if err != nil {
		return TaskMutationResult{}, err
	}
	result := TaskMutationResult{Detail: detail, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, commandName, requestHash, result, now); err != nil {
		return TaskMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskMutationResult{}, storageFailure("commit task mutation", err)
	}
	return result, nil
}

// cancelPendingSchedulingIntentForTask closes accepted work that the owner
// cancelled before scheduling or while the exact latest bounded retry is a
// definite start failure. The task projection and both journal facts share the
// caller's transaction.
func cancelPendingSchedulingIntentForTask(ctx context.Context, tx *sql.Tx, workspaceID string, task domain.Task, correlationID, now string) error {
	var intentID, intentStatus, latestRunID string
	var intentRevision int64
	err := tx.QueryRowContext(ctx, `WITH candidate AS (
  SELECT intent.*,
    COALESCE((
      SELECT retry.run_id FROM run_retry_receipts retry
      WHERE retry.intent_id=intent.id
      ORDER BY retry.attempt DESC,retry.run_id DESC LIMIT 1
    ),intent.run_id,'') AS latest_run_id
  FROM scheduling_intents intent
  WHERE intent.workspace_id=? AND intent.task_id=?
    AND intent.status IN ('pending','deferred','run_requested')
)
SELECT candidate.id,candidate.revision,candidate.status,
  CASE WHEN candidate.status='run_requested' THEN candidate.latest_run_id ELSE '' END
FROM candidate
WHERE candidate.status IN ('pending','deferred') OR (
  candidate.status='run_requested' AND EXISTS (
    SELECT 1 FROM runs latest
    JOIN run_jobs job ON job.run_id=latest.id
    WHERE latest.id=candidate.latest_run_id
      AND latest.workspace_id=candidate.workspace_id AND latest.task_id=candidate.task_id
      AND latest.assignment_id=candidate.assignment_id
      AND latest.status='start_failed' AND latest.step_cursor=0
      AND latest.finished_at=latest.updated_at
      AND job.status='complete' AND job.origin='supervisor'
      AND (
        (latest.id=candidate.run_id AND EXISTS (
          SELECT 1 FROM run_scheduling_receipts initial
          WHERE initial.run_id=latest.id AND initial.intent_id=candidate.id
        ))
        OR EXISTS (
          SELECT 1 FROM run_retry_receipts source
          WHERE source.run_id=latest.id AND source.intent_id=candidate.id
        )
      )
      AND NOT EXISTS (SELECT 1 FROM run_retry_receipts successor WHERE successor.prior_run_id=latest.id)
      AND EXISTS (
        SELECT 1 FROM events failure
        WHERE failure.workspace_id=candidate.workspace_id
          AND failure.entity_type='run' AND failure.entity_id=latest.id
          AND failure.entity_revision=latest.revision AND failure.type='run.start_failed'
          AND failure.occurred_at=latest.updated_at AND failure.recorded_at=latest.updated_at
          AND failure.actor_id='subsystem:run-worker' AND failure.actor_type='subsystem'
          AND json_extract(failure.data_json,'$.code')='runtime_start_failed'
      )
  )
)
ORDER BY candidate.created_at,candidate.id LIMIT 1`, workspaceID, task.ID).
		Scan(&intentID, &intentRevision, &intentStatus, &latestRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return storageFailure("query task scheduling intent before cancellation", err)
	}
	reason := ownerTaskCancellationIntentReason
	nextRevision := intentRevision + 1
	eventData := map[string]any{
		"task_id": task.ID, "status": domain.SchedulingIntentCancelled, "reason": reason,
	}
	if intentStatus == domain.SchedulingIntentRunRequested {
		eventData["run_id"] = latestRunID
		eventData["run_status"] = domain.RunStartFailed
	}
	if _, err := appendEvent(ctx, tx, workspaceID, "scheduling_intent", intentID, nextRevision,
		schedulingIntentCancelledEvent, correlationID, now, eventData); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduling_intents
SET status='cancelled',reason=?,revision=?,updated_at=?,next_attempt_at=NULL,updated_by=?
WHERE id=? AND status=? AND revision=?`, reason, nextRevision, now, localOwnerActorID, intentID, intentStatus, intentRevision)
	if err != nil {
		return storageFailure("cancel task scheduling intent", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return storageFailure("count cancelled task scheduling intent", err)
		}
		return &Error{Code: CodeAssignmentConflict, Message: "task scheduling intent changed before cancellation"}
	}
	return nil
}

func (s *Store) TaskDetail(ctx context.Context, workspaceIdentifier, taskID string) (domain.TaskDetail, error) {
	workspace, err := s.Workspace(ctx, workspaceIdentifier)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	task, err := queryTask(ctx, s.db, workspace.ID, strings.TrimSpace(taskID))
	if err != nil {
		return domain.TaskDetail{}, err
	}
	return taskDetail(ctx, s.db, task)
}

func (s *Store) ReconcileExpiredAssignments(ctx context.Context, workspaceIdentifier, correlationID string) (int, error) {
	return s.reconcileExpiredAssignments(ctx, workspaceIdentifier, correlationID, 0)
}

// ReconcileExpiredAssignmentsBatch advances at most limit elapsed assignments.
// Repeated calls drain the stable lease-expiry/id order without a persisted
// cursor because each committed row leaves the active candidate set.
func (s *Store) ReconcileExpiredAssignmentsBatch(ctx context.Context, workspaceIdentifier, correlationID string, limit int) (int, error) {
	if limit < 1 || limit > MaximumLeaseReconciliationBatchLimit {
		return 0, &Error{Code: CodeInvalidTask, Message: fmt.Sprintf("assignment reconciliation limit must be between 1 and %d", MaximumLeaseReconciliationBatchLimit)}
	}
	return s.reconcileExpiredAssignments(ctx, workspaceIdentifier, correlationID, limit)
}

func (s *Store) reconcileExpiredAssignments(ctx context.Context, workspaceIdentifier, correlationID string, limit int) (int, error) {
	if strings.TrimSpace(correlationID) == "" {
		correlationID = "lease-reconciliation"
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return 0, storageFailure("begin assignment reconciliation", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return 0, err
	}
	count, err := expireAssignmentsInTransactionLimit(ctx, tx, workspace.ID, s.clock().UTC(), correlationID, limit)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, storageFailure("commit assignment reconciliation", err)
	}
	return count, nil
}

func expireAssignmentsInTransaction(ctx context.Context, tx *sql.Tx, workspaceID string, now time.Time, correlationID string) (int, error) {
	return expireAssignmentsInTransactionLimit(ctx, tx, workspaceID, now, correlationID, 0)
}

func expireAssignmentsInTransactionLimit(ctx context.Context, tx *sql.Tx, workspaceID string, now time.Time, correlationID string, limit int) (int, error) {
	query := `
SELECT a.id, a.task_id
FROM task_assignments a JOIN tasks t ON t.id = a.task_id
WHERE t.workspace_id = ? AND a.status = 'active'
  AND crewfold_timestamp_key(a.lease_expires_at) <= crewfold_timestamp_key(?)
  AND NOT EXISTS (
      SELECT 1 FROM runs run
      WHERE run.assignment_id = a.id
        AND run.status IN ('requested','starting','active','blocked','stopping','lost')
  )
ORDER BY crewfold_timestamp_key(a.lease_expires_at), a.id`
	arguments := []any{workspaceID, now.Format(time.RFC3339Nano)}
	if limit > 0 {
		query += "\nLIMIT ?"
		arguments = append(arguments, limit)
	}
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return 0, storageFailure("list expired assignments", err)
	}
	type expired struct{ assignmentID, taskID string }
	var values []expired
	for rows.Next() {
		var value expired
		if err := rows.Scan(&value.assignmentID, &value.taskID); err != nil {
			_ = rows.Close()
			return 0, storageFailure("scan expired assignment", err)
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return 0, storageFailure("close expired assignments", err)
	}
	nowText := now.Format(time.RFC3339Nano)
	for _, value := range values {
		if _, err := tx.ExecContext(ctx, "UPDATE task_assignments SET status = 'expired', revision = revision + 1, updated_at = ?, updated_by = ? WHERE id = ? AND status = 'active'", nowText, leaseActorID, value.assignmentID); err != nil {
			return 0, storageFailure("expire assignment", err)
		}
		task, err := queryTask(ctx, tx, workspaceID, value.taskID)
		if err != nil {
			return 0, err
		}
		if task.Status == domain.TaskAssigned || task.Status == domain.TaskActive {
			task.Status = domain.TaskReady
		}
		task.Revision++
		if _, err := tx.ExecContext(ctx, "UPDATE tasks SET status = ?, revision = ?, updated_at = ?, updated_by = ? WHERE id = ?", task.Status, task.Revision, nowText, leaseActorID, task.ID); err != nil {
			return 0, storageFailure("update task after assignment expiry", err)
		}
		if _, err := appendEventForActor(ctx, tx, workspaceID, "task", task.ID, task.Revision, taskAssignmentExpired, correlationID, nowText, leaseActorID, domain.EventActorSubsystem, map[string]any{"assignment_id": value.assignmentID, "status": task.Status}); err != nil {
			return 0, err
		}
	}
	return len(values), nil
}

func (s *Store) CoordinationStatus(ctx context.Context, workspaceIdentifier string) (domain.CoordinationStatus, error) {
	workspace, err := s.Workspace(ctx, workspaceIdentifier)
	if err != nil {
		return domain.CoordinationStatus{}, err
	}
	var status domain.CoordinationStatus
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(enabled), 0) FROM agents WHERE workspace_id = ?", workspace.ID).Scan(&status.AgentsRegistered, &status.AgentsEnabled); err != nil {
		return domain.CoordinationStatus{}, storageFailure("count agents", err)
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN t.status = 'ready' AND NOT EXISTS (
           SELECT 1 FROM task_dependencies d JOIN tasks dependency ON dependency.id = d.depends_on_task_id
           WHERE d.task_id = t.id AND dependency.status <> 'completed'
       ) THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(t.status = 'assigned'), 0),
	       COALESCE(SUM(t.status = 'active'), 0),
	       COALESCE(SUM(t.status = 'blocked'), 0),
	       COALESCE(SUM(t.status = 'completed'), 0),
	       COALESCE(SUM(t.status = 'cancelled'), 0)
FROM tasks t WHERE t.workspace_id = ?`, workspace.ID).Scan(&status.TasksRegistered, &status.TasksReady, &status.TasksAssigned, &status.TasksActive, &status.TasksBlocked, &status.TasksCompleted, &status.TasksCancelled); err != nil {
		return domain.CoordinationStatus{}, storageFailure("count task states", err)
	}
	return status, nil
}

func queryAgent(ctx context.Context, database queryRower, workspaceID, identifier string) (domain.AgentDefinition, error) {
	var agent domain.AgentDefinition
	err := scanAgent(database.QueryRowContext(ctx, agentSelect+" WHERE workspace_id = ? AND id = ?", workspaceID, identifier), &agent)
	if err == nil {
		return agent, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.AgentDefinition{}, storageFailure("query agent by id", err)
	}
	err = scanAgent(database.QueryRowContext(ctx, agentSelect+" WHERE workspace_id = ? AND name = ?", workspaceID, identifier), &agent)
	if err == nil {
		return agent, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentDefinition{}, &Error{Code: CodeAgentNotFound, Message: fmt.Sprintf("agent %q was not found", identifier)}
	}
	return domain.AgentDefinition{}, storageFailure("query agent by name", err)
}

const agentSelect = "SELECT id, workspace_id, name, role, provider, runtime, enabled, max_concurrency, revision, created_at, updated_at, created_by, updated_by FROM agents"

func scanAgent(row rowScanner, agent *domain.AgentDefinition) error {
	return row.Scan(&agent.ID, &agent.WorkspaceID, &agent.Name, &agent.Role, &agent.Provider, &agent.Runtime, &agent.Enabled, &agent.MaxConcurrency, &agent.Revision, &agent.CreatedAt, &agent.UpdatedAt, &agent.CreatedBy, &agent.UpdatedBy)
}

func queryObjective(ctx context.Context, database queryRower, workspaceID, identifier string) (domain.Objective, error) {
	var objective domain.Objective
	err := database.QueryRowContext(ctx, `
SELECT id, workspace_id, project_id, COALESCE(primary_checkout_id,''), title, status, budget_tokens, budget_cost_cents, budget_time_seconds, revision, created_at, updated_at, created_by, updated_by
FROM objectives WHERE workspace_id = ? AND id = ?`, workspaceID, identifier).Scan(&objective.ID, &objective.WorkspaceID, &objective.ProjectID, &objective.PrimaryCheckoutID, &objective.Title, &objective.Status, &objective.Budget.TokenLimit, &objective.Budget.CostCents, &objective.Budget.TimeSeconds, &objective.Revision, &objective.CreatedAt, &objective.UpdatedAt, &objective.CreatedBy, &objective.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Objective{}, &Error{Code: CodeObjectiveNotFound, Message: fmt.Sprintf("objective %q was not found", identifier)}
	}
	if err != nil {
		return domain.Objective{}, storageFailure("query objective", err)
	}
	return objective, nil
}

func queryTask(ctx context.Context, database queryRower, workspaceID, taskID string) (domain.Task, error) {
	var task domain.Task
	err := scanTask(database.QueryRowContext(ctx, taskSelect+" WHERE t.workspace_id = ? AND t.id = ?", workspaceID, taskID), &task)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, &Error{Code: CodeTaskNotFound, Message: fmt.Sprintf("task %q was not found", taskID)}
	}
	if err != nil {
		return domain.Task{}, storageFailure("query task", err)
	}
	return task, nil
}

const taskSelect = `
SELECT t.id, t.workspace_id, t.project_id, COALESCE(t.objective_id, ''), t.title,
       COALESCE(t.description, ''), t.status, COALESCE(t.blocked_reason, ''),
       t.priority, t.budget_tokens, t.budget_cost_cents, t.budget_time_seconds,
       COALESCE(a.id, ''), COALESCE(a.agent_id, ''), COALESCE(a.lease_expires_at, ''),
       t.revision, t.created_at, t.updated_at, t.created_by, t.updated_by
FROM tasks t LEFT JOIN task_assignments a ON a.task_id = t.id AND a.status = 'active'`

func scanTask(row rowScanner, task *domain.Task) error {
	return row.Scan(&task.ID, &task.WorkspaceID, &task.ProjectID, &task.ObjectiveID, &task.Title, &task.Description, &task.Status, &task.BlockedReason, &task.Priority, &task.Budget.TokenLimit, &task.Budget.CostCents, &task.Budget.TimeSeconds, &task.AssignmentID, &task.AssignedAgentID, &task.AssignmentLeaseExpiresAt, &task.Revision, &task.CreatedAt, &task.UpdatedAt, &task.CreatedBy, &task.UpdatedBy)
}

func taskDetail(ctx context.Context, database *sql.DB, task domain.Task) (domain.TaskDetail, error) {
	dependencies, err := taskDependencies(ctx, database, task.ID)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	readiness, err := taskReadiness(ctx, database, task)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	assignment, err := activeAssignment(ctx, database, task.AssignmentID)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	return domain.TaskDetail{Task: task, Dependencies: dependencies, Assignment: assignment, Readiness: readiness}, nil
}

func taskDetailInTransaction(ctx context.Context, tx *sql.Tx, task domain.Task) (domain.TaskDetail, error) {
	dependencies, err := taskDependencies(ctx, tx, task.ID)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	readiness, err := taskReadiness(ctx, tx, task)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	refreshed, err := queryTask(ctx, tx, task.WorkspaceID, task.ID)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	assignment, err := activeAssignment(ctx, tx, refreshed.AssignmentID)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	return domain.TaskDetail{Task: refreshed, Dependencies: dependencies, Assignment: assignment, Readiness: readiness}, nil
}

type queryContext interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func taskDependencies(ctx context.Context, database queryContext, taskID string) ([]domain.TaskDependency, error) {
	rows, err := database.QueryContext(ctx, "SELECT task_id, depends_on_task_id, delivery_requirement, created_at, created_by FROM task_dependencies WHERE task_id = ? ORDER BY depends_on_task_id", taskID)
	if err != nil {
		return nil, storageFailure("list task dependencies", err)
	}
	defer rows.Close()
	result := make([]domain.TaskDependency, 0)
	for rows.Next() {
		var dependency domain.TaskDependency
		if err := rows.Scan(&dependency.TaskID, &dependency.DependsOnTaskID, &dependency.DeliveryRequirement, &dependency.CreatedAt, &dependency.CreatedBy); err != nil {
			return nil, storageFailure("scan task dependency", err)
		}
		result = append(result, dependency)
	}
	return result, rows.Err()
}

func taskReadiness(ctx context.Context, database queryRower, task domain.Task) (domain.TaskReadiness, error) {
	if task.Status != domain.TaskReady {
		return domain.TaskReadiness{Ready: false, Reason: "task status is " + task.Status}, nil
	}
	var dependencyID, status string
	err := database.QueryRowContext(ctx, `
SELECT d.depends_on_task_id, dependency.status
FROM task_dependencies d JOIN tasks dependency ON dependency.id = d.depends_on_task_id
WHERE d.task_id = ? AND dependency.status <> 'completed'
ORDER BY d.depends_on_task_id LIMIT 1`, task.ID).Scan(&dependencyID, &status)
	if err == nil {
		return domain.TaskReadiness{Ready: false, Reason: fmt.Sprintf("waiting for dependency %s (%s)", dependencyID, status)}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.TaskReadiness{}, storageFailure("evaluate task readiness", err)
	}
	var deliveryDependencyID, deliveryRequirement string
	err = database.QueryRowContext(ctx, `
SELECT d.depends_on_task_id, d.delivery_requirement
FROM task_dependencies d
WHERE d.task_id = ? AND d.delivery_requirement <> 'completion'
  AND NOT (
    EXISTS (
      SELECT 1 FROM runs r JOIN run_handoffs h ON h.run_id=r.id
      WHERE r.task_id=d.depends_on_task_id AND r.status IN ('completed','review')
    )
    AND (
      d.delivery_requirement='handoff'
      OR EXISTS (
        SELECT 1 FROM runs r JOIN run_reports report ON report.run_id=r.id
        WHERE r.task_id=d.depends_on_task_id AND report.kind='completion' AND report.status='applied'
          AND (COALESCE(json_array_length(report.evidence_json),0)>0
               OR COALESCE(json_array_length(json_extract(report.payload_json,'$.changed_paths')),0)>0
               OR COALESCE(json_array_length(json_extract(report.payload_json,'$.checks')),0)>0
               OR COALESCE(json_array_length(json_extract(report.payload_json,'$.remaining_risks')),0)>0
               OR COALESCE(json_array_length(json_extract(report.payload_json,'$.unknowns')),0)>0)
      )
    )
  )
ORDER BY d.depends_on_task_id LIMIT 1`, task.ID).Scan(&deliveryDependencyID, &deliveryRequirement)
	if err == nil {
		return domain.TaskReadiness{Ready: false, Reason: fmt.Sprintf("waiting for %s delivery from dependency %s", deliveryRequirement, deliveryDependencyID)}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.TaskReadiness{}, storageFailure("evaluate dependency delivery readiness", err)
	}
	return domain.TaskReadiness{Ready: true, Reason: "task has no incomplete dependencies and is unassigned"}, nil
}

func validDependencyDelivery(value string) bool {
	return value == domain.DependencyDeliveryCompletion || value == domain.DependencyDeliveryHandoff || value == domain.DependencyDeliveryHandoffWithEvidence
}

func validateObjectiveCheckouts(ctx context.Context, tx *sql.Tx, projectID, primary string, references []string) ([]string, error) {
	if len(references) > 8 {
		return nil, &Error{Code: CodeInvalidObjective, Message: "objective may reference at most 8 additional checkouts"}
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(references))
	for index, raw := range append([]string{primary}, references...) {
		checkoutID := strings.TrimSpace(raw)
		if checkoutID == "" {
			if index == 0 {
				continue
			}
			return nil, &Error{Code: CodeInvalidObjective, Message: "objective reference checkout ID is empty"}
		}
		if seen[checkoutID] {
			return nil, &Error{Code: CodeInvalidObjective, Message: "objective checkout selections must be unique"}
		}
		seen[checkoutID] = true
		checkout, err := queryCheckoutByID(ctx, tx, checkoutID)
		if err != nil || checkout.ProjectID != projectID {
			return nil, &Error{Code: CodeInvalidObjective, Message: "objective checkout must belong to the selected project", Cause: err}
		}
		if index == 0 {
			if checkout.Availability != domain.CheckoutAvailable || checkout.WriteMode == domain.WriteModeReadOnly {
				return nil, &Error{Code: CodeInvalidObjective, Message: "objective primary checkout must be available and writable"}
			}
			continue
		}
		result = append(result, checkout.ID)
	}
	return result, nil
}

func activeAssignment(ctx context.Context, database queryRower, assignmentID string) (*domain.TaskAssignment, error) {
	if assignmentID == "" {
		return nil, nil
	}
	var assignment domain.TaskAssignment
	err := database.QueryRowContext(ctx, "SELECT id, task_id, agent_id, status, lease_expires_at, revision, created_at, updated_at, created_by, updated_by FROM task_assignments WHERE id = ?", assignmentID).Scan(&assignment.ID, &assignment.TaskID, &assignment.AgentID, &assignment.Status, &assignment.LeaseExpiresAt, &assignment.Revision, &assignment.CreatedAt, &assignment.UpdatedAt, &assignment.CreatedBy, &assignment.UpdatedBy)
	if err != nil {
		return nil, storageFailure("query active assignment", err)
	}
	return &assignment, nil
}

func revisionConflict(entityType, id string, expected, actual int64) *Error {
	return &Error{Code: CodeRevisionConflict, Message: fmt.Sprintf("%s %s revision is %d, expected %d", entityType, id, actual, expected)}
}

func invalidTaskTransition(status, action string) *Error {
	return &Error{Code: CodeInvalidTransition, Message: fmt.Sprintf("cannot %s task in %s state", action, status)}
}

// Owner task transitions cannot independently rewrite a task projection while
// a run owns its launch/runtime reservation. Worker and agent paths perform the
// paired run+task transitions atomically (for example, blocked and resume) and
// therefore do not pass through this guard.
func rejectOwnerTransitionWithReservedRun(ctx context.Context, tx *sql.Tx, taskID, action string) error {
	var runID string
	err := tx.QueryRowContext(ctx, `
SELECT id FROM runs
WHERE task_id=? AND status IN ('requested','starting','active','blocked','stopping','lost')
ORDER BY created_at,id LIMIT 1`, taskID).Scan(&runID)
	if err == nil {
		return &Error{Code: CodeRunConflict, Message: fmt.Sprintf("task cannot transition via %s while run %s retains launch or runtime authority", action, runID)}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return storageFailure("check task run reservation for owner transition", err)
}

func validShortText(value string) bool { return value != "" && len(value) <= 128 }
func validTitle(value string) bool     { return value != "" && len(value) <= 256 }
func validBudget(value domain.Budget) bool {
	return value.TokenLimit >= 0 && value.CostCents >= 0 && value.TimeSeconds >= 0
}

func validObjectiveStatus(value string) bool {
	return value == domain.ObjectiveActive || value == domain.ObjectiveCompleted || value == domain.ObjectiveCancelled
}

func (s *Store) nowText() string { return s.clock().UTC().Format(time.RFC3339Nano) }
