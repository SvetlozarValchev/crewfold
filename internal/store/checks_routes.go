package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

func (s *Store) CreateCheckRoute(ctx context.Context, command CreateCheckRouteCommand) (MutationResult[domain.CheckRoute], error) {
	command.WorkspaceIdentifier, command.ProjectIdentifier, command.CheckDefinitionID, command.Trigger, command.Duty, command.AgentIdentifier, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.ProjectIdentifier), strings.TrimSpace(command.CheckDefinitionID), strings.TrimSpace(command.Trigger), strings.TrimSpace(command.Duty), strings.TrimSpace(command.AgentIdentifier), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ExpectedAgentRevision < 1 || !validCheckRouteTrigger(command.Trigger) || !validCheckRouteDuty(command.Duty) || ((command.CheckDefinitionID == "") != (command.DefinitionContentRevision == 0)) {
		return MutationResult[domain.CheckRoute]{}, checkError(CodeInvalidCheckRoute, "check route requires exact optional definition, trigger, duty, and agent revision")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidCheckRoute); err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	hash, _ := checkSemanticHash("check.route.create", command)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	defer tx.Rollback()
	var replay MutationResult[domain.CheckRoute]
	idempotencyKey := ownerCheckIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.route.create", hash, &replay); err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, command.AgentIdentifier)
	if err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	if !agent.Enabled || agent.Revision != command.ExpectedAgentRevision {
		return MutationResult[domain.CheckRoute]{}, checkError(CodeInvalidCheckRoute, "route agent must be exact and enabled")
	}
	definitionID := ""
	definitionRevision := int64(0)
	if command.CheckDefinitionID != "" {
		definition, err := queryCheckDefinition(ctx, dbgen.New(tx), workspace.ID, command.CheckDefinitionID)
		if err != nil {
			return MutationResult[domain.CheckRoute]{}, err
		}
		if definition.ProjectID != project.ID || definition.Status != domain.CheckDefinitionActive || definition.ContentRevision != command.DefinitionContentRevision {
			return MutationResult[domain.CheckRoute]{}, checkError(CodeInvalidCheckRoute, "route definition is not exact active project content")
		}
		definitionID, definitionRevision = definition.ID, definition.ContentRevision
	}
	id, _ := randomID("checkroute_")
	now := s.nowText()
	route := domain.CheckRoute{ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, DefinitionID: definitionID, DefinitionContentRevision: definitionRevision, Trigger: command.Trigger, Duty: command.Duty, AgentID: agent.ID, AgentRevision: agent.Revision, Status: domain.CheckRouteActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	queries := dbgen.New(tx)
	err = s.withCheckMutationSeal(func() error {
		return queries.InsertCheckRoute(ctx, dbgen.InsertCheckRouteParams{ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, DefinitionID: definitionID, DefinitionContentRevision: definitionRevision, Trigger: route.Trigger, Duty: route.Duty, AgentID: agent.ID, AgentRevision: agent.Revision, CreatedAt: now})
	})
	if err != nil {
		return MutationResult[domain.CheckRoute]{}, checkConstraint("insert check route", CodeInvalidCheckRoute, err)
	}
	seq, err := appendEvent(ctx, tx, workspace.ID, "check_route", id, 1, checkRouteCreatedEvent, command.CorrelationID, now, map[string]any{"project_id": project.ID, "agent_id": agent.ID, "duty": route.Duty, "trigger": route.Trigger})
	if err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	result := MutationResult[domain.CheckRoute]{Value: route, EventSequence: seq}
	if err := recordIdempotency(ctx, tx, idempotencyKey, "check.route.create", hash, result, now); err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	return result, nil
}

func (s *Store) RetireCheckRoute(ctx context.Context, command RetireCheckRouteCommand) (MutationResult[domain.CheckRoute], error) {
	command.WorkspaceIdentifier, command.CheckRouteID, command.Reason, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.CheckRouteID), strings.TrimSpace(command.Reason), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ExpectedRevision < 1 || (command.Reason != "" && !validCheckText(command.Reason, 2048)) {
		return MutationResult[domain.CheckRoute]{}, checkError(CodeInvalidCheckRoute, "route retirement requires exact revision and bounded reason")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidCheckRoute); err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	hash, _ := checkSemanticHash("check.route.retire", command)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	defer tx.Rollback()
	var replay MutationResult[domain.CheckRoute]
	idempotencyKey := ownerCheckIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.route.retire", hash, &replay); err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	queries := dbgen.New(tx)
	route, err := queryCheckRoute(ctx, queries, workspace.ID, command.CheckRouteID)
	if err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	if route.Revision != command.ExpectedRevision {
		return MutationResult[domain.CheckRoute]{}, revisionConflict("check route", route.ID, command.ExpectedRevision, route.Revision)
	}
	now := s.nowText()
	err = s.withCheckMutationSeal(func() error {
		affected, err := queries.RetireCheckRoute(ctx, dbgen.RetireCheckRouteParams{UpdatedAt: now, ID: route.ID, WorkspaceID: workspace.ID, ExpectedRevision: command.ExpectedRevision})
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("route lost active revision")
		}
		return nil
	})
	if err != nil {
		return MutationResult[domain.CheckRoute]{}, checkConstraint("retire check route", CodeInvalidCheckRoute, err)
	}
	route.Status, route.Revision, route.UpdatedAt = domain.CheckRouteRetired, route.Revision+1, now
	seq, err := appendEvent(ctx, tx, workspace.ID, "check_route", route.ID, route.Revision, checkRouteRetiredEvent, command.CorrelationID, now, map[string]any{"reason": command.Reason})
	if err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	result := MutationResult[domain.CheckRoute]{Value: route, EventSequence: seq}
	if err := recordIdempotency(ctx, tx, idempotencyKey, "check.route.retire", hash, result, now); err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckRoute]{}, err
	}
	return result, nil
}

func (s *Store) CheckRoutes(ctx context.Context, query ListCheckRoutesQuery) ([]domain.CheckRoute, error) {
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
	definitionID := strings.TrimSpace(query.DefinitionID)
	if definitionID != "" {
		definition, err := queryCheckDefinition(ctx, dbgen.New(s.db), workspace.ID, definitionID)
		if err != nil {
			return nil, err
		}
		definitionID = definition.ID
	}
	queries := dbgen.New(s.db)
	ids, err := queries.ListCheckRouteIDs(ctx, dbgen.ListCheckRouteIDsParams{WorkspaceID: workspace.ID, ProjectID: projectID, DefinitionID: definitionID, Trigger: strings.TrimSpace(query.Trigger), Duty: strings.TrimSpace(query.Duty), Status: strings.TrimSpace(query.Status), ResultLimit: int64(boundedCheckLimit(query.Limit))})
	if err != nil {
		return nil, storageFailure("list check routes", err)
	}
	result := make([]domain.CheckRoute, 0, len(ids))
	for _, id := range ids {
		route, err := queryCheckRoute(ctx, queries, workspace.ID, id)
		if err != nil {
			return nil, err
		}
		result = append(result, route)
	}
	return result, nil
}
func queryCheckRoute(ctx context.Context, queries *dbgen.Queries, workspaceID, id string) (domain.CheckRoute, error) {
	row, err := queries.GetCheckRoute(ctx, dbgen.GetCheckRouteParams{WorkspaceID: workspaceID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CheckRoute{}, checkError(CodeInvalidCheckRoute, "check route was not found")
	}
	if err != nil {
		return domain.CheckRoute{}, storageFailure("read check route", err)
	}
	return domain.CheckRoute{ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, DefinitionID: stringValue(row.DefinitionID), DefinitionContentRevision: int64Value(row.DefinitionContentRevision), Trigger: row.Trigger, Duty: row.Duty, AgentID: row.AgentID, AgentRevision: row.AgentRevision, Status: row.Status, Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy}, nil
}
func validCheckRouteTrigger(value string) bool {
	return value == domain.CheckRoutePass || value == domain.CheckRouteNonpass || value == domain.CheckRouteStale
}
func validCheckRouteDuty(value string) bool {
	return value == domain.CheckDutyEvidenceReview || value == domain.CheckDutyCoordination
}
