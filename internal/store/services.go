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
	"unicode/utf8"

	"crewfold/internal/domain"
)

const (
	managedServiceDefinitionCreatedEvent = "service.definition_created"
	managedServiceDefinitionRetiredEvent = "service.definition_retired"
	managedServiceStartRequestedEvent    = "service.start_requested"
	managedServiceStopRequestedEvent     = "service.stop_requested"
	managedServiceNodeCapacity           = 8
	managedServiceProjectCapacity        = 4
)

type managedServiceHealthContent struct {
	Type           string `json:"type"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Path           string `json:"path"`
	IntervalMillis int64  `json:"interval_millis"`
	TimeoutMillis  int64  `json:"timeout_millis"`
}

type managedServiceDefinitionContent struct {
	WorkspaceID           string                                     `json:"workspace_id"`
	ProjectID             string                                     `json:"project_id"`
	WorkstreamID          string                                     `json:"workstream_id"`
	CheckoutID            string                                     `json:"checkout_id"`
	Name                  string                                     `json:"name"`
	Description           string                                     `json:"description"`
	Executable            string                                     `json:"executable"`
	Arguments             []string                                   `json:"arguments"`
	WorkingDirectory      string                                     `json:"working_directory"`
	Environment           []domain.ManagedServiceEnvironmentVariable `json:"environment"`
	Profile               string                                     `json:"profile"`
	ProfileRevision       int64                                      `json:"profile_revision"`
	NetworkMode           string                                     `json:"network_mode"`
	Health                managedServiceHealthContent                `json:"health"`
	RestartPolicy         string                                     `json:"restart_policy"`
	MaximumRestarts       int                                        `json:"maximum_restarts"`
	RestartCooldownMillis int64                                      `json:"restart_cooldown_millis"`
	StopSignal            string                                     `json:"stop_signal"`
	StopGraceMillis       int64                                      `json:"stop_grace_millis"`
	OutputByteLimit       int64                                      `json:"output_byte_limit"`
	CapacityClass         string                                     `json:"capacity_class"`
}

func (s *Store) CreateManagedServiceDefinition(ctx context.Context, command CreateManagedServiceDefinitionCommand) (MutationResult[domain.ManagedServiceDefinition], error) {
	return s.createManagedServiceDefinition(ctx, "", command)
}

// CreateManagedServiceDefinitionAsAgent records an inert, exact process
// definition authored by one current durable agent. It does not grant process
// authority or start anything; the corresponding owner-review request remains
// the authority boundary for the first effect.
func (s *Store) CreateManagedServiceDefinitionAsAgent(ctx context.Context, threadID string, command CreateManagedServiceDefinitionCommand) (MutationResult[domain.ManagedServiceDefinition], error) {
	return s.createManagedServiceDefinition(ctx, strings.TrimSpace(threadID), command)
}

func (s *Store) createManagedServiceDefinition(ctx context.Context, threadID string, command CreateManagedServiceDefinitionCommand) (MutationResult[domain.ManagedServiceDefinition], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.WorkstreamID = strings.TrimSpace(command.WorkstreamID)
	command.CheckoutID = strings.TrimSpace(command.CheckoutID)
	command.Name = strings.TrimSpace(command.Name)
	command.Description = strings.TrimSpace(command.Description)
	command.Executable = strings.TrimSpace(command.Executable)
	command.WorkingDirectory = filepath.ToSlash(filepath.Clean(strings.TrimSpace(command.WorkingDirectory)))
	command.Profile = strings.TrimSpace(command.Profile)
	command.StopSignal = strings.TrimSpace(command.StopSignal)
	command.CapacityClass = strings.TrimSpace(command.CapacityClass)
	command.NetworkMode = strings.TrimSpace(command.NetworkMode)
	command.Health.Type = strings.TrimSpace(command.Health.Type)
	command.Health.Host = strings.TrimSpace(command.Health.Host)
	command.Health.Path = strings.TrimSpace(command.Health.Path)
	command.RestartPolicy = strings.TrimSpace(command.RestartPolicy)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkingDirectory == "" {
		command.WorkingDirectory = "."
	}
	command.Environment = append([]domain.ManagedServiceEnvironmentVariable(nil), command.Environment...)
	if command.Arguments == nil {
		command.Arguments = []string{}
	}
	if command.Environment == nil {
		command.Environment = []domain.ManagedServiceEnvironmentVariable{}
	}
	sort.Slice(command.Environment, func(i, j int) bool { return command.Environment[i].Name < command.Environment[j].Name })
	if err := validateManagedServiceDefinition(command); err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	operation := "service.definition.create"
	if threadID != "" {
		operation = "service.definition.propose"
	}
	requestHash, err := checkSemanticHash(operation, command)
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, storageFailure("hash managed-service definition", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, storageFailure("begin managed-service definition", err)
	}
	defer tx.Rollback()
	actor := localOwnerActorID
	if threadID != "" {
		scope, scopeErr := s.domainAgentSessionScopeInTransaction(ctx, tx, threadID)
		if scopeErr != nil {
			return MutationResult[domain.ManagedServiceDefinition]{}, scopeErr
		}
		if scope.Workspace.ID != command.WorkspaceIdentifier && scope.Workspace.Name != command.WorkspaceIdentifier {
			return MutationResult[domain.ManagedServiceDefinition]{}, managedServiceError(CodeManagedServiceDenied, "managed-service proposal workspace is outside the durable agent scope")
		}
		if scope.Project.ID != command.ProjectIdentifier && scope.Project.Name != command.ProjectIdentifier {
			return MutationResult[domain.ManagedServiceDefinition]{}, managedServiceError(CodeManagedServiceDenied, "managed-service proposal project is outside the durable agent scope")
		}
		actor = scope.Agent.ID
	}
	var replay MutationResult[domain.ManagedServiceDefinition]
	key := managedServiceOwnerIdempotencyKey(command.IdempotencyKey)
	if threadID != "" {
		key = managedServiceAgentIdempotencyKey(actor, command.IdempotencyKey)
	}
	if found, err := lookupIdempotency(ctx, tx, key, operation, requestHash, &replay); err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	checkout, err := queryCheckoutByID(ctx, tx, command.CheckoutID)
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	if checkout.ProjectID != project.ID || checkout.Availability != domain.CheckoutAvailable {
		return MutationResult[domain.ManagedServiceDefinition]{}, managedServiceError(CodeInvalidManagedService, "managed service requires an available checkout in the exact project")
	}
	if command.WorkstreamID != "" {
		workstream, err := queryObjective(ctx, tx, workspace.ID, command.WorkstreamID)
		if err != nil {
			return MutationResult[domain.ManagedServiceDefinition]{}, err
		}
		if workstream.ProjectID != project.ID {
			return MutationResult[domain.ManagedServiceDefinition]{}, managedServiceError(CodeInvalidManagedService, "managed-service workstream belongs to a different project")
		}
		if workstream.PrimaryCheckoutID == "" || workstream.PrimaryCheckoutID != checkout.ID {
			return MutationResult[domain.ManagedServiceDefinition]{}, managedServiceError(CodeInvalidManagedService, "managed service attached to a workstream must use that workstream's exact primary checkout")
		}
	}
	id, err := randomID("svcdef_")
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, storageFailure("generate managed-service definition id", err)
	}
	content := managedServiceDefinitionContent{
		WorkspaceID: workspace.ID, ProjectID: project.ID, WorkstreamID: command.WorkstreamID, CheckoutID: checkout.ID,
		Name: command.Name, Description: command.Description, Executable: command.Executable, Arguments: append([]string{}, command.Arguments...),
		WorkingDirectory: command.WorkingDirectory, Environment: append([]domain.ManagedServiceEnvironmentVariable{}, command.Environment...),
		Profile: command.Profile, ProfileRevision: command.ProfileRevision, NetworkMode: command.NetworkMode,
		Health:        managedServiceHealthContent{Type: command.Health.Type, Host: command.Health.Host, Port: command.Health.Port, Path: command.Health.Path, IntervalMillis: command.Health.IntervalMillis, TimeoutMillis: command.Health.TimeoutMillis},
		RestartPolicy: command.RestartPolicy, MaximumRestarts: command.MaximumRestarts, RestartCooldownMillis: command.RestartCooldownMillis,
		StopSignal: command.StopSignal, StopGraceMillis: command.StopGraceMillis, OutputByteLimit: command.OutputByteLimit, CapacityClass: command.CapacityClass,
	}
	contentJSON, contentSHA, err := canonicalContent(content)
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, storageFailure("seal managed-service definition", err)
	}
	argumentsJSON, _ := json.Marshal(command.Arguments)
	environmentJSON, _ := json.Marshal(command.Environment)
	now := s.nowText()
	for ordinal, argument := range command.Arguments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO managed_service_arguments(definition_id,ordinal,argument) VALUES(?,?,?)`, id, ordinal, argument); err != nil {
			return MutationResult[domain.ManagedServiceDefinition]{}, managedServiceConstraint("insert managed-service argument", err)
		}
	}
	for ordinal, environment := range command.Environment {
		if _, err := tx.ExecContext(ctx, `INSERT INTO managed_service_environment(definition_id,ordinal,name,value) VALUES(?,?,?,?)`, id, ordinal, environment.Name, environment.Value); err != nil {
			return MutationResult[domain.ManagedServiceDefinition]{}, managedServiceConstraint("insert managed-service environment", err)
		}
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO managed_service_definitions(
 id,workspace_id,project_id,workstream_id,checkout_id,name,description,executable,arguments_json,working_directory,environment_json,
 profile,profile_revision,network_mode,health_type,health_host,health_port,health_path,health_interval_millis,health_timeout_millis,
 restart_policy,maximum_restarts,restart_cooldown_millis,stop_signal,stop_grace_millis,output_byte_limit,capacity_class,content_json,content_revision,content_sha256,status,revision,
 created_at,updated_at,created_by,updated_by
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,0),NULLIF(?,''),?,?,?,?,?,?,?,?,?,?,1,?,'active',1,?,?,?,?)`,
		id, workspace.ID, project.ID, nullText(command.WorkstreamID), checkout.ID, command.Name, command.Description, command.Executable, string(argumentsJSON), command.WorkingDirectory, string(environmentJSON),
		command.Profile, command.ProfileRevision, command.NetworkMode, command.Health.Type, command.Health.Host, command.Health.Port, command.Health.Path, command.Health.IntervalMillis, command.Health.TimeoutMillis,
		command.RestartPolicy, command.MaximumRestarts, command.RestartCooldownMillis, command.StopSignal, command.StopGraceMillis, command.OutputByteLimit, command.CapacityClass, string(contentJSON), contentSHA, now, now, actor, actor)
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, managedServiceConstraint("insert managed-service definition", err)
	}
	definition := domain.ManagedServiceDefinition{
		ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, WorkstreamID: command.WorkstreamID, CheckoutID: checkout.ID,
		Name: command.Name, Description: command.Description, Executable: command.Executable, Arguments: append([]string{}, command.Arguments...), WorkingDirectory: command.WorkingDirectory,
		Environment: append([]domain.ManagedServiceEnvironmentVariable{}, command.Environment...), Profile: command.Profile, ProfileRevision: command.ProfileRevision, NetworkMode: command.NetworkMode,
		Health: command.Health, RestartPolicy: command.RestartPolicy, MaximumRestarts: command.MaximumRestarts,
		RestartCooldownMillis: command.RestartCooldownMillis, StopSignal: command.StopSignal, StopGraceMillis: command.StopGraceMillis,
		OutputByteLimit: command.OutputByteLimit, CapacityClass: command.CapacityClass, ContentRevision: 1, ContentSHA256: contentSHA,
		Status: domain.ManagedServiceDefinitionActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: actor, UpdatedBy: actor,
	}
	eventData := map[string]any{
		"project_id": project.ID, "workstream_id": command.WorkstreamID, "checkout_id": checkout.ID, "content_sha256": contentSHA,
	}
	var sequence int64
	if threadID == "" {
		sequence, err = appendEvent(ctx, tx, workspace.ID, "managed_service_definition", id, 1, managedServiceDefinitionCreatedEvent, command.CorrelationID, now, eventData)
	} else {
		sequence, err = appendEventForActor(ctx, tx, workspace.ID, "managed_service_definition", id, 1, managedServiceDefinitionCreatedEvent, command.CorrelationID, now, actor, domain.EventActorIntegration, eventData)
	}
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	result := MutationResult[domain.ManagedServiceDefinition]{Value: definition, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, operation, requestHash, result, now); err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, storageFailure("commit managed-service definition", err)
	}
	return result, nil
}

func (s *Store) ManagedServiceDefinition(ctx context.Context, workspaceIdentifier, identifier string) (domain.ManagedServiceDefinition, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.ManagedServiceDefinition{}, err
	}
	return queryManagedServiceDefinition(ctx, s.db, workspace.ID, strings.TrimSpace(identifier))
}

func (s *Store) ManagedServiceDefinitions(ctx context.Context, query ListManagedServiceDefinitionsQuery) ([]domain.ManagedServiceDefinition, error) {
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
	rows, err := s.db.QueryContext(ctx, `
SELECT id FROM managed_service_definitions
WHERE workspace_id=? AND (?='' OR project_id=?) AND (?='' OR workstream_id=?) AND (?='' OR checkout_id=?) AND (?='' OR status=?)
ORDER BY project_id,COALESCE(workstream_id,''),name,id LIMIT ?`, workspace.ID, projectID, projectID,
		strings.TrimSpace(query.WorkstreamID), strings.TrimSpace(query.WorkstreamID), strings.TrimSpace(query.CheckoutID), strings.TrimSpace(query.CheckoutID), strings.TrimSpace(query.Status), strings.TrimSpace(query.Status), boundedManagedServiceLimit(query.Limit))
	if err != nil {
		return nil, storageFailure("list managed-service definitions", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, storageFailure("scan managed-service definition id", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("list managed-service definition ids", err)
	}
	result := make([]domain.ManagedServiceDefinition, 0, len(ids))
	for _, id := range ids {
		definition, err := queryManagedServiceDefinition(ctx, s.db, workspace.ID, id)
		if err != nil {
			return nil, err
		}
		result = append(result, definition)
	}
	return result, nil
}

func (s *Store) RetireManagedServiceDefinition(ctx context.Context, command RetireManagedServiceDefinitionCommand) (MutationResult[domain.ManagedServiceDefinition], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.DefinitionID = strings.TrimSpace(command.DefinitionID)
	command.Reason = strings.TrimSpace(command.Reason)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.ExpectedRevision < 1 || !validManagedServiceText(command.Reason, 2048) {
		return MutationResult[domain.ManagedServiceDefinition]{}, managedServiceError(CodeInvalidManagedService, "managed-service retirement requires an exact revision and bounded reason")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	requestHash, err := checkSemanticHash("service.definition.retire", command)
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, storageFailure("hash managed-service retirement", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, storageFailure("begin managed-service retirement", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.ManagedServiceDefinition]
	key := managedServiceOwnerIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, key, "service.definition.retire", requestHash, &replay); err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	definition, err := queryManagedServiceDefinition(ctx, tx, workspace.ID, command.DefinitionID)
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	if definition.Status != domain.ManagedServiceDefinitionActive || definition.Revision != command.ExpectedRevision {
		return MutationResult[domain.ManagedServiceDefinition]{}, revisionConflict("managed-service definition", definition.ID, command.ExpectedRevision, definition.Revision)
	}
	var live int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM managed_service_instances WHERE definition_id=? AND status IN ('requested','starting','healthy','degraded','stopping','unknown')`, definition.ID).Scan(&live); err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, storageFailure("check managed-service retirement", err)
	}
	if live != 0 {
		return MutationResult[domain.ManagedServiceDefinition]{}, managedServiceError(CodeManagedServiceConflict, "stop or resolve the current managed service before retiring its definition")
	}
	now := s.nowText()
	definition.Status = domain.ManagedServiceDefinitionRetired
	definition.Revision++
	definition.UpdatedAt = now
	definition.UpdatedBy = localOwnerActorID
	if _, err := tx.ExecContext(ctx, `UPDATE managed_service_definitions SET status='retired',revision=?,updated_at=?,updated_by='local-owner' WHERE id=? AND status='active'`, definition.Revision, now, definition.ID); err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, managedServiceConstraint("retire managed-service definition", err)
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "managed_service_definition", definition.ID, definition.Revision, managedServiceDefinitionRetiredEvent, command.CorrelationID, now, map[string]any{"reason": command.Reason})
	if err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	result := MutationResult[domain.ManagedServiceDefinition]{Value: definition, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "service.definition.retire", requestHash, result, now); err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceDefinition]{}, storageFailure("commit managed-service retirement", err)
	}
	return result, nil
}

func (s *Store) StartManagedService(ctx context.Context, command StartManagedServiceCommand) (MutationResult[domain.ManagedServiceInstance], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.DefinitionID = strings.TrimSpace(command.DefinitionID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.ExpectedRevision < 1 || command.DefinitionID == "" {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceError(CodeInvalidManagedService, "managed-service start requires an exact active definition revision")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	requestHash, err := checkSemanticHash("service.start", command)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("hash managed-service start", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("begin managed-service start", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.ManagedServiceInstance]
	key := managedServiceOwnerIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, key, "service.start", requestHash, &replay); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	definition, err := queryManagedServiceDefinition(ctx, tx, workspace.ID, command.DefinitionID)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if definition.Status != domain.ManagedServiceDefinitionActive || definition.Revision != command.ExpectedRevision {
		return MutationResult[domain.ManagedServiceInstance]{}, revisionConflict("managed-service definition", definition.ID, command.ExpectedRevision, definition.Revision)
	}
	now := s.nowText()
	result, err := s.startManagedServiceInTx(ctx, tx, workspace.ID, definition, domain.ManagedServiceSource{Type: domain.ManagedServiceSourceOwner, ActorID: localOwnerActorID}, command.CorrelationID, now)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if err := recordIdempotency(ctx, tx, key, "service.start", requestHash, result, now); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("commit managed-service start", err)
	}
	return result, nil
}

func (s *Store) startManagedServiceInTx(ctx context.Context, tx *sql.Tx, workspaceID string, definition domain.ManagedServiceDefinition, source domain.ManagedServiceSource, correlationID, now string) (MutationResult[domain.ManagedServiceInstance], error) {
	var nodeActive, projectActive int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN project_id=? THEN 1 ELSE 0 END),0) FROM managed_service_instances WHERE status IN ('requested','starting','healthy','degraded','stopping','unknown')`, definition.ProjectID).Scan(&nodeActive, &projectActive); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("count managed-service capacity", err)
	}
	if nodeActive >= managedServiceNodeCapacity {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceError(CodeManagedServiceCapacity, fmt.Sprintf("managed-service node capacity is exhausted: %d of %d instances are unresolved", nodeActive, managedServiceNodeCapacity))
	}
	if projectActive >= managedServiceProjectCapacity {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceError(CodeManagedServiceCapacity, fmt.Sprintf("managed-service project capacity is exhausted: %d of %d instances are unresolved", projectActive, managedServiceProjectCapacity))
	}
	instanceID, err := randomID("svcinst_")
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("generate managed-service instance id", err)
	}
	jobID, err := randomID("svcjob_")
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("generate managed-service job id", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO managed_service_instances(
 id,workspace_id,project_id,workstream_id,checkout_id,definition_id,definition_revision,definition_content_sha256,
 source_type,source_actor_id,source_agent_id,source_agent_revision,source_thread_id,source_request_id,source_grant_id,source_grant_revision,
 status,desired_state,health_status,restart_count,exit_code,diagnostic_code,diagnostic,revision,created_at,updated_at,
 started_at,healthy_at,finished_at,created_by,updated_by
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'requested','running','pending',0,NULL,NULL,NULL,1,?,?,NULL,NULL,NULL,?,?)`,
		instanceID, workspaceID, definition.ProjectID, nullText(definition.WorkstreamID), definition.CheckoutID,
		definition.ID, definition.Revision, definition.ContentSHA256,
		source.Type, source.ActorID, nullText(source.AgentID), nullablePositive(source.AgentRevision), nullText(source.ThreadID), nullText(source.RequestID), nullText(source.GrantID), nullablePositive(source.GrantRevision),
		now, now, source.ActorID, source.ActorID)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceConstraint("request managed-service instance", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO managed_service_jobs(id,instance_id,action,status,available_at,lease_expires_at,attempts,diagnostic,created_at,updated_at) VALUES(?,?,'start','pending',?,NULL,0,NULL,?,?)`, jobID, instanceID, now, now, now); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceConstraint("queue managed-service start", err)
	}
	instance := domain.ManagedServiceInstance{
		ID: instanceID, WorkspaceID: workspaceID, ProjectID: definition.ProjectID, WorkstreamID: definition.WorkstreamID, CheckoutID: definition.CheckoutID,
		DefinitionID: definition.ID, DefinitionRevision: definition.Revision, DefinitionContentSHA256: definition.ContentSHA256,
		Source: source,
		Status: domain.ManagedServiceRequested, DesiredState: domain.ManagedServiceDesiredRunning, HealthStatus: domain.ManagedServiceHealthPending,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	eventData := map[string]any{"definition_id": definition.ID, "definition_revision": definition.Revision, "checkout_id": definition.CheckoutID, "workstream_id": definition.WorkstreamID, "source_type": source.Type}
	var sequence int64
	if source.Type == domain.ManagedServiceSourceOwner {
		sequence, err = appendEvent(ctx, tx, workspaceID, "managed_service_instance", instanceID, 1, managedServiceStartRequestedEvent, correlationID, now, eventData)
	} else {
		sequence, err = appendEventForActor(ctx, tx, workspaceID, "managed_service_instance", instanceID, 1, managedServiceStartRequestedEvent, correlationID, now, source.AgentID, domain.EventActorIntegration, eventData)
	}
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	return MutationResult[domain.ManagedServiceInstance]{Value: instance, EventSequence: sequence}, nil
}

func queryManagedServiceDefinition(ctx context.Context, database queryRower, workspaceID, identifier string) (domain.ManagedServiceDefinition, error) {
	const selectDefinition = `
SELECT id,workspace_id,project_id,COALESCE(workstream_id,''),checkout_id,name,description,executable,working_directory,profile,profile_revision,network_mode,
 health_type,COALESCE(health_host,''),COALESCE(health_port,0),COALESCE(health_path,''),health_interval_millis,health_timeout_millis,
 restart_policy,maximum_restarts,restart_cooldown_millis,stop_signal,stop_grace_millis,output_byte_limit,capacity_class,content_revision,content_sha256,status,revision,created_at,updated_at,created_by,updated_by
FROM managed_service_definitions WHERE workspace_id=? AND `
	var definition domain.ManagedServiceDefinition
	scan := func(row *sql.Row) error {
		return row.Scan(&definition.ID, &definition.WorkspaceID, &definition.ProjectID, &definition.WorkstreamID, &definition.CheckoutID,
			&definition.Name, &definition.Description, &definition.Executable, &definition.WorkingDirectory, &definition.Profile, &definition.ProfileRevision, &definition.NetworkMode,
			&definition.Health.Type, &definition.Health.Host, &definition.Health.Port, &definition.Health.Path,
			&definition.Health.IntervalMillis, &definition.Health.TimeoutMillis, &definition.RestartPolicy, &definition.MaximumRestarts,
			&definition.RestartCooldownMillis, &definition.StopSignal, &definition.StopGraceMillis, &definition.OutputByteLimit, &definition.CapacityClass, &definition.ContentRevision, &definition.ContentSHA256,
			&definition.Status, &definition.Revision, &definition.CreatedAt, &definition.UpdatedAt, &definition.CreatedBy, &definition.UpdatedBy)
	}
	err := scan(database.QueryRowContext(ctx, selectDefinition+"id=?", workspaceID, identifier))
	if errors.Is(err, sql.ErrNoRows) {
		err = scan(database.QueryRowContext(ctx, selectDefinition+"name=? AND status='active'", workspaceID, identifier))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ManagedServiceDefinition{}, managedServiceError(CodeManagedServiceNotFound, fmt.Sprintf("managed service %q was not found", identifier))
	}
	if err != nil {
		return domain.ManagedServiceDefinition{}, storageFailure("query managed-service definition", err)
	}
	rows, err := queryServiceRows(ctx, database, `SELECT argument FROM managed_service_arguments WHERE definition_id=? ORDER BY ordinal`, definition.ID)
	if err != nil {
		return domain.ManagedServiceDefinition{}, storageFailure("query managed-service arguments", err)
	}
	for rows.Next() {
		var argument string
		if err := rows.Scan(&argument); err != nil {
			rows.Close()
			return domain.ManagedServiceDefinition{}, storageFailure("scan managed-service argument", err)
		}
		definition.Arguments = append(definition.Arguments, argument)
	}
	if err := rows.Close(); err != nil {
		return domain.ManagedServiceDefinition{}, storageFailure("close managed-service arguments", err)
	}
	environmentRows, err := queryServiceRows(ctx, database, `SELECT name,value FROM managed_service_environment WHERE definition_id=? ORDER BY ordinal`, definition.ID)
	if err != nil {
		return domain.ManagedServiceDefinition{}, storageFailure("query managed-service environment", err)
	}
	for environmentRows.Next() {
		var item domain.ManagedServiceEnvironmentVariable
		if err := environmentRows.Scan(&item.Name, &item.Value); err != nil {
			environmentRows.Close()
			return domain.ManagedServiceDefinition{}, storageFailure("scan managed-service environment", err)
		}
		definition.Environment = append(definition.Environment, item)
	}
	if err := environmentRows.Close(); err != nil {
		return domain.ManagedServiceDefinition{}, storageFailure("close managed-service environment", err)
	}
	if definition.Arguments == nil {
		definition.Arguments = []string{}
	}
	if definition.Environment == nil {
		definition.Environment = []domain.ManagedServiceEnvironmentVariable{}
	}
	return definition, nil
}

type rowQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryServiceRows(ctx context.Context, database queryRower, statement string, args ...any) (*sql.Rows, error) {
	queryer, ok := database.(rowQueryer)
	if !ok {
		return nil, errors.New("database does not support row queries")
	}
	return queryer.QueryContext(ctx, statement, args...)
}

func validateManagedServiceDefinition(command CreateManagedServiceDefinitionCommand) error {
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" || command.CheckoutID == "" ||
		!validManagedServiceText(command.Name, 128) || !validManagedServiceText(command.Description, 1024) || !validManagedServiceExecutable(command.Executable) ||
		!validCheckRelativePath(command.WorkingDirectory) || command.Profile != "local-process" ||
		command.ProfileRevision != 1 ||
		!validManagedServiceNetwork(command.NetworkMode) || !validManagedServiceHealth(command.Health) ||
		!validManagedServiceRestart(command.RestartPolicy) || command.MaximumRestarts < 0 || command.MaximumRestarts > 20 || command.RestartCooldownMillis < 0 || command.RestartCooldownMillis > 60000 ||
		command.StopSignal != domain.ManagedServiceStopSignalTerm || command.CapacityClass != domain.ManagedServiceCapacityLocalDevelop ||
		command.StopGraceMillis < 100 || command.StopGraceMillis > 60000 || command.OutputByteLimit < 4096 || command.OutputByteLimit > 1048576 ||
		len(command.Arguments) > 64 || len(command.Environment) > 64 {
		return managedServiceError(CodeInvalidManagedService, "managed service requires a bounded checkout launch, health, restart, stop, and output contract")
	}
	if managedServiceUsesOpaqueShell(command.Executable, command.Arguments) {
		return managedServiceError(CodeInvalidManagedService, "managed service requires an executable and argument array, not an opaque shell command")
	}
	if command.NetworkMode == domain.ManagedServiceNetworkNone && command.Health.Type != domain.ManagedServiceHealthProcess {
		return managedServiceError(CodeInvalidManagedService, "managed service endpoint health requires loopback network exposure")
	}
	for _, argument := range command.Arguments {
		if !validCheckArgument(argument) {
			return managedServiceError(CodeInvalidManagedService, "managed-service arguments must be NUL-free UTF-8 and at most 4096 bytes")
		}
	}
	seen := map[string]bool{}
	for _, item := range command.Environment {
		if !validEnvironmentName(item.Name) || managedServiceSecretEnvironmentName(item.Name) || !utf8.ValidString(item.Value) || strings.ContainsRune(item.Value, '\x00') || len([]byte(item.Value)) > 4096 || seen[item.Name] {
			return managedServiceError(CodeInvalidManagedService, "managed-service environment requires unique portable names and bounded UTF-8 values")
		}
		seen[item.Name] = true
	}
	return nil
}

func validManagedServiceText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') && len([]byte(value)) <= maximum
}

func validManagedServiceExecutable(value string) bool {
	if !validManagedServiceText(value, 4096) {
		return false
	}
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}

func validEnvironmentName(value string) bool {
	if value == "" || len(value) > 128 || !((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z') || value[0] == '_') {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_') {
			return false
		}
	}
	return true
}

func validManagedServiceNetwork(value string) bool {
	return value == domain.ManagedServiceNetworkNone || value == domain.ManagedServiceNetworkLoopback
}

func validManagedServiceHealth(value domain.ManagedServiceHealthCheck) bool {
	if value.IntervalMillis < 100 || value.IntervalMillis > 60000 || value.TimeoutMillis < 50 || value.TimeoutMillis > 30000 || value.TimeoutMillis > value.IntervalMillis {
		return false
	}
	switch value.Type {
	case domain.ManagedServiceHealthProcess:
		return value.Host == "" && value.Port == 0 && value.Path == ""
	case domain.ManagedServiceHealthTCP:
		return validManagedServiceLoopbackHost(value.Host) && value.Port >= 1 && value.Port <= 65535 && value.Path == ""
	case domain.ManagedServiceHealthHTTP:
		return validManagedServiceLoopbackHost(value.Host) && value.Port >= 1 && value.Port <= 65535 && strings.HasPrefix(value.Path, "/") && validManagedServiceText(value.Path, 2048)
	default:
		return false
	}
}

func validManagedServiceLoopbackHost(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func managedServiceUsesOpaqueShell(executable string, arguments []string) bool {
	switch strings.ToLower(filepath.Base(executable)) {
	case "sh", "bash", "dash", "zsh", "ksh", "fish":
		for _, argument := range arguments {
			if argument == "-c" || argument == "-lc" {
				return true
			}
		}
	case "cmd", "cmd.exe":
		for _, argument := range arguments {
			if strings.EqualFold(argument, "/c") {
				return true
			}
		}
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		for _, argument := range arguments {
			if strings.EqualFold(argument, "-command") || strings.EqualFold(argument, "-encodedcommand") {
				return true
			}
		}
	}
	return false
}

func managedServiceSecretEnvironmentName(value string) bool {
	upper := strings.ToUpper(value)
	for _, fragment := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "PRIVATE_KEY", "CREDENTIAL", "AUTHORIZATION", "COOKIE"} {
		if upper == fragment || strings.HasSuffix(upper, "_"+fragment) || strings.HasPrefix(upper, fragment+"_") {
			return true
		}
	}
	return false
}

func validManagedServiceRestart(value string) bool {
	return value == domain.ManagedServiceRestartNever || value == domain.ManagedServiceRestartOnFailure || value == domain.ManagedServiceRestartOnDaemon
}

func boundedManagedServiceLimit(value int) int {
	if value <= 0 {
		return 100
	}
	if value > 200 {
		return 200
	}
	return value
}

func managedServiceOwnerIdempotencyKey(raw string) string {
	return "service:owner:" + strings.TrimSpace(raw)
}

func managedServiceError(code, message string) error {
	return &Error{Code: code, Message: message}
}

func managedServiceConstraint(action string, err error) error {
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "constraint") || strings.Contains(lower, "managed-service") {
		return &Error{Code: CodeInvalidManagedService, Message: action, Cause: err}
	}
	return storageFailure(action, err)
}

func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullablePositive(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}
