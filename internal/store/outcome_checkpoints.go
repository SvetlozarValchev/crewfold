package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

func (s *Store) CreateOwnerCheckpoint(ctx context.Context, command CreateOwnerCheckpointCommand) (OwnerCheckpointMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ScopeType = strings.TrimSpace(command.ScopeType)
	command.ScopeIdentifier = strings.TrimSpace(command.ScopeIdentifier)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || !validOutcomeScopeType(command.ScopeType) || command.ScopeIdentifier == "" {
		return OwnerCheckpointMutationResult{}, outcomeError(CodeInvalidOwnerCheckpoint, "checkpoint requires an exact workspace and task, objective, project, or workspace scope")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidOwnerCheckpoint); err != nil {
		return OwnerCheckpointMutationResult{}, err
	}
	requestHash, err := hashCommand("owner_checkpoint.create", map[string]any{
		"workspace": command.WorkspaceIdentifier, "scope_type": command.ScopeType, "scope_identifier": command.ScopeIdentifier,
	})
	if err != nil {
		return OwnerCheckpointMutationResult{}, storageFailure("hash owner checkpoint", err)
	}

	var result OwnerCheckpointMutationResult
	err = s.withOutcomeMutation(ctx, "owner checkpoint", func(tx *sql.Tx) error {
		if found, lookupErr := lookupIdempotency(ctx, tx, command.IdempotencyKey, "owner_checkpoint.create", requestHash, &result); lookupErr != nil {
			return lookupErr
		} else if found {
			return nil
		}
		workspace, workspaceErr := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
		if workspaceErr != nil {
			return workspaceErr
		}
		queries := dbgen.New(tx)
		scope, scopeErr := resolveOutcomeScope(ctx, queries, workspace, command.ScopeType, command.ScopeIdentifier)
		if scopeErr != nil {
			return scopeErr
		}
		id, idErr := randomID("outcpnt_")
		if idErr != nil {
			return storageFailure("generate owner checkpoint id", idErr)
		}
		now := s.nowText()
		sequence, eventErr := appendEvent(ctx, tx, workspace.ID, "owner_checkpoint", id, 1, ownerCheckpointCreatedEvent, command.CorrelationID, now, map[string]any{
			"scope_type": command.ScopeType, "scope_id": scopeID(scope),
		})
		if eventErr != nil {
			return eventErr
		}
		if hookErr := s.runMutationHook(MutationAfterOwnerCheckpointEvent); hookErr != nil {
			return hookErr
		}
		checkpoint := domain.OwnerCheckpoint{
			ID: id, WorkspaceID: workspace.ID, ScopeType: command.ScopeType, ScopeID: scopeID(scope),
			EventSequence: sequence, CreatedAt: now, CreatedBy: localOwnerActorID,
		}
		if insertErr := queries.InsertOwnerCheckpoint(ctx, dbgen.InsertOwnerCheckpointParams{
			ID: checkpoint.ID, WorkspaceID: checkpoint.WorkspaceID, ScopeType: checkpoint.ScopeType,
			ScopeID: checkpoint.ScopeID, EventSequence: checkpoint.EventSequence, CreatedAt: checkpoint.CreatedAt,
		}); insertErr != nil {
			return storageFailure("insert owner checkpoint", insertErr)
		}
		if hookErr := s.runMutationHook(MutationAfterOwnerCheckpoint); hookErr != nil {
			return hookErr
		}
		result = OwnerCheckpointMutationResult{Checkpoint: checkpoint, EventSequence: sequence}
		if recordErr := recordIdempotency(ctx, tx, command.IdempotencyKey, "owner_checkpoint.create", requestHash, result, now); recordErr != nil {
			return recordErr
		}
		if hookErr := s.runMutationHook(MutationAfterOwnerCheckpointIdempotency); hookErr != nil {
			return hookErr
		}
		return nil
	})
	return result, err
}

func (s *Store) OwnerCheckpoint(ctx context.Context, workspaceIdentifier, checkpointID string) (domain.OwnerCheckpoint, error) {
	workspaceIdentifier = strings.TrimSpace(workspaceIdentifier)
	checkpointID = strings.TrimSpace(checkpointID)
	if workspaceIdentifier == "" || checkpointID == "" {
		return domain.OwnerCheckpoint{}, outcomeError(CodeInvalidOwnerCheckpoint, "workspace and checkpoint id are required")
	}
	workspace, err := s.Workspace(ctx, workspaceIdentifier)
	if err != nil {
		return domain.OwnerCheckpoint{}, err
	}
	row, err := dbgen.New(s.db).GetOwnerCheckpoint(ctx, dbgen.GetOwnerCheckpointParams{WorkspaceID: workspace.ID, ID: checkpointID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OwnerCheckpoint{}, outcomeError(CodeOwnerCheckpointNotFound, fmt.Sprintf("owner checkpoint %q was not found", checkpointID))
	}
	if err != nil {
		return domain.OwnerCheckpoint{}, storageFailure("read owner checkpoint", err)
	}
	return validateOwnerCheckpoint(ctx, dbgen.New(s.db), workspace, row)
}

func (s *Store) OwnerCheckpoints(ctx context.Context, query ListOwnerCheckpointsQuery) ([]domain.OwnerCheckpoint, error) {
	query.WorkspaceIdentifier = strings.TrimSpace(query.WorkspaceIdentifier)
	query.ScopeType = strings.TrimSpace(query.ScopeType)
	query.ScopeIdentifier = strings.TrimSpace(query.ScopeIdentifier)
	if query.WorkspaceIdentifier == "" || (query.ScopeType != "" && !validOutcomeScopeType(query.ScopeType)) || (query.ScopeIdentifier != "" && query.ScopeType == "") || query.Limit < 0 || query.Limit > 100 {
		return nil, outcomeError(CodeInvalidOwnerCheckpoint, "checkpoint list requires a valid bounded scope and limit")
	}
	workspace, err := s.Workspace(ctx, query.WorkspaceIdentifier)
	if err != nil {
		return nil, err
	}
	scopeIdentifier := ""
	if query.ScopeType != "" {
		scope, scopeErr := resolveOutcomeScope(ctx, dbgen.New(s.db), workspace, query.ScopeType, query.ScopeIdentifier)
		if scopeErr != nil {
			return nil, scopeErr
		}
		scopeIdentifier = scopeID(scope)
	}
	limit := query.Limit
	if limit == 0 {
		limit = 100
	}
	rows, err := dbgen.New(s.db).ListOwnerCheckpoints(ctx, dbgen.ListOwnerCheckpointsParams{
		WorkspaceID: workspace.ID, ScopeType: query.ScopeType, ScopeID: scopeIdentifier, ResultLimit: int64(limit),
	})
	if err != nil {
		return nil, storageFailure("list owner checkpoints", err)
	}
	result := make([]domain.OwnerCheckpoint, 0, len(rows))
	for _, row := range rows {
		value, validateErr := validateOwnerCheckpoint(ctx, dbgen.New(s.db), workspace, row)
		if validateErr != nil {
			return nil, validateErr
		}
		result = append(result, value)
	}
	return result, nil
}

func resolveOutcomeScope(ctx context.Context, queries *dbgen.Queries, workspace domain.Workspace, scopeType, identifier string) (domain.BriefingScope, error) {
	scope := domain.BriefingScope{Type: scopeType, WorkspaceID: workspace.ID}
	switch scopeType {
	case domain.OwnerCheckpointTask:
		row, err := queries.GetOutcomeScopeTask(ctx, dbgen.GetOutcomeScopeTaskParams{WorkspaceID: workspace.ID, ScopeID: identifier})
		if errors.Is(err, sql.ErrNoRows) || row.ObjectiveID == nil {
			return domain.BriefingScope{}, outcomeError(CodeInvalidOwnerCheckpoint, "task scope was not found in the workspace or has no objective")
		}
		if err != nil {
			return domain.BriefingScope{}, storageFailure("resolve outcome task scope", err)
		}
		scope.ProjectID, scope.ObjectiveID, scope.TaskID = row.ProjectID, *row.ObjectiveID, row.ID
	case domain.OwnerCheckpointObjective:
		row, err := queries.GetOutcomeScopeObjective(ctx, dbgen.GetOutcomeScopeObjectiveParams{WorkspaceID: workspace.ID, ScopeID: identifier})
		if errors.Is(err, sql.ErrNoRows) {
			return domain.BriefingScope{}, outcomeError(CodeInvalidOwnerCheckpoint, "objective scope was not found in the workspace")
		}
		if err != nil {
			return domain.BriefingScope{}, storageFailure("resolve outcome objective scope", err)
		}
		scope.ProjectID, scope.ObjectiveID = row.ProjectID, row.ID
	case domain.OwnerCheckpointProject:
		row, err := queries.GetOutcomeScopeProject(ctx, dbgen.GetOutcomeScopeProjectParams{WorkspaceID: workspace.ID, ScopeID: identifier})
		if errors.Is(err, sql.ErrNoRows) {
			byName, projectErr := queries.GetOutcomeScopeProjectByName(ctx, dbgen.GetOutcomeScopeProjectByNameParams{WorkspaceID: workspace.ID, ScopeName: identifier})
			if projectErr != nil {
				return domain.BriefingScope{}, outcomeError(CodeInvalidOwnerCheckpoint, "project scope was not found in the workspace")
			}
			row.ID, row.WorkspaceID, err = byName.ID, byName.WorkspaceID, nil
		}
		if err != nil {
			return domain.BriefingScope{}, storageFailure("resolve outcome project scope", err)
		}
		scope.ProjectID = row.ID
	case domain.OwnerCheckpointWorkspace:
		if identifier != workspace.ID && identifier != workspace.Name {
			return domain.BriefingScope{}, outcomeError(CodeInvalidOwnerCheckpoint, "workspace scope identifier must name the selected workspace")
		}
	default:
		return domain.BriefingScope{}, outcomeError(CodeInvalidOwnerCheckpoint, "scope type must be task, objective, project, or workspace")
	}
	return scope, nil
}

func scopeID(scope domain.BriefingScope) string {
	switch scope.Type {
	case domain.OwnerCheckpointTask:
		return scope.TaskID
	case domain.OwnerCheckpointObjective:
		return scope.ObjectiveID
	case domain.OwnerCheckpointProject:
		return scope.ProjectID
	default:
		return scope.WorkspaceID
	}
}

func validOutcomeScopeType(value string) bool {
	return value == domain.OwnerCheckpointTask || value == domain.OwnerCheckpointObjective || value == domain.OwnerCheckpointProject || value == domain.OwnerCheckpointWorkspace
}

func domainOwnerCheckpoint(row dbgen.OwnerCheckpoint) domain.OwnerCheckpoint {
	return domain.OwnerCheckpoint{
		ID: row.ID, WorkspaceID: row.WorkspaceID, ScopeType: row.ScopeType, ScopeID: row.ScopeID,
		EventSequence: row.EventSequence, CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy,
	}
}

func validateOwnerCheckpoint(ctx context.Context, queries *dbgen.Queries, workspace domain.Workspace, row dbgen.OwnerCheckpoint) (domain.OwnerCheckpoint, error) {
	value := domainOwnerCheckpoint(row)
	if value.WorkspaceID != workspace.ID || !validOutcomeScopeType(value.ScopeType) || !canonicalTimestamp(value.CreatedAt) || value.CreatedBy != localOwnerActorID {
		return domain.OwnerCheckpoint{}, storageFailure("validate owner checkpoint", errors.New("checkpoint lifecycle fields are not canonical"))
	}
	scope, err := resolveOutcomeScope(ctx, queries, workspace, value.ScopeType, value.ScopeID)
	if err != nil || scopeID(scope) != value.ScopeID {
		return domain.OwnerCheckpoint{}, storageFailure("validate owner checkpoint scope", errors.New("checkpoint scope does not resolve exactly"))
	}
	event, err := queries.GetOutcomeJournalEvent(ctx, value.EventSequence)
	expectedData, _ := json.Marshal(map[string]any{"scope_type": value.ScopeType, "scope_id": value.ScopeID})
	if err != nil || event.WorkspaceID != workspace.ID || event.EntityType != "owner_checkpoint" || event.EntityID != value.ID || event.EntityRevision != 1 || event.Type != ownerCheckpointCreatedEvent || event.OccurredAt != value.CreatedAt || event.RecordedAt != value.CreatedAt || event.ActorID != localOwnerActorID || event.ActorType != localActorType || event.DataJson != string(expectedData) {
		return domain.OwnerCheckpoint{}, storageFailure("validate owner checkpoint event", errors.New("checkpoint journal fact differs from its immutable row"))
	}
	return value, nil
}
