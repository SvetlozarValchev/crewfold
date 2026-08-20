package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"crewfold/internal/domain"
)

const (
	managedServiceGrantCreatedEvent    = "service.grant_created"
	managedServiceGrantDelegatedEvent  = "service.grant_delegated"
	managedServiceGrantRevokedEvent    = "service.grant_revoked"
	managedServiceRequestCreatedEvent  = "service.request_created"
	managedServiceRequestAcceptedEvent = "service.request_accepted"
	managedServiceRequestRejectedEvent = "service.request_rejected"
)

func (s *Store) CreateManagedServiceGrant(ctx context.Context, command CreateManagedServiceGrantCommand) (MutationResult[domain.ManagedServiceGrant], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.DefinitionID = strings.TrimSpace(command.DefinitionID)
	command.ManagerAgentIdentifier = strings.TrimSpace(command.ManagerAgentIdentifier)
	command.ExpiresAt = strings.TrimSpace(command.ExpiresAt)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	actions, err := normalizeManagedServiceActions(command.Actions)
	if err != nil || command.ExpectedDefinitionRevision < 1 || command.ExpectedMembershipRevision < 1 || command.MaximumInstances < 1 || command.MaximumInstances > 8 {
		return MutationResult[domain.ManagedServiceGrant]{}, managedServiceError(CodeInvalidManagedService, "managed-service grant requires an exact definition, active agent membership, action set, and instance ceiling")
	}
	command.Actions = actions
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	requestHash, err := checkSemanticHash("service.grant.create", command)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, storageFailure("hash managed-service grant", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, storageFailure("begin managed-service grant", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.ManagedServiceGrant]
	key := managedServiceOwnerIdempotencyKey(command.IdempotencyKey)
	if found, lookupErr := lookupIdempotency(ctx, tx, key, "service.grant.create", requestHash, &replay); lookupErr != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, lookupErr
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	definition, err := queryManagedServiceDefinition(ctx, tx, workspace.ID, command.DefinitionID)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	if definition.Status != domain.ManagedServiceDefinitionActive || definition.Revision != command.ExpectedDefinitionRevision {
		return MutationResult[domain.ManagedServiceGrant]{}, revisionConflict("managed-service definition", definition.ID, command.ExpectedDefinitionRevision, definition.Revision)
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, command.ManagerAgentIdentifier)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	membership, err := queryDomainAgentMembership(ctx, tx, definition.ProjectID, agent.ID)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	if !agent.Enabled || membership.Status != domain.DomainAgentActive || membership.Revision != command.ExpectedMembershipRevision {
		return MutationResult[domain.ManagedServiceGrant]{}, managedServiceError(CodeManagedServiceDenied, "managed-service grant target is not the exact current active domain membership")
	}
	now := s.nowText()
	if err := validateManagedServiceGrantExpiry(command.ExpiresAt, "", now); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	grant, err := insertManagedServiceGrant(ctx, tx, definition, membership, "", actions, command.MaximumInstances, command.ExpiresAt, localOwnerActorID, now)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "managed_service_grant", grant.ID, 1, managedServiceGrantCreatedEvent, command.CorrelationID, now, managedServiceGrantEventData(grant))
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	result := MutationResult[domain.ManagedServiceGrant]{Value: grant, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "service.grant.create", requestHash, result, now); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, storageFailure("commit managed-service grant", err)
	}
	return result, nil
}

func (s *Store) DelegateManagedServiceGrant(ctx context.Context, command DelegateManagedServiceGrantCommand) (MutationResult[domain.ManagedServiceGrant], error) {
	command.ThreadID = strings.TrimSpace(command.ThreadID)
	command.ParentGrantID = strings.TrimSpace(command.ParentGrantID)
	command.ManagerAgentIdentifier = strings.TrimSpace(command.ManagerAgentIdentifier)
	command.ExpiresAt = strings.TrimSpace(command.ExpiresAt)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	actions, err := normalizeManagedServiceActions(command.Actions)
	if err != nil || command.ExpectedParentRevision < 1 || command.ExpectedMembershipRevision < 1 || command.MaximumInstances < 1 || command.MaximumInstances > 8 {
		return MutationResult[domain.ManagedServiceGrant]{}, managedServiceError(CodeInvalidManagedService, "delegated managed-service grant requires exact parent, child, actions, and limits")
	}
	command.Actions = actions
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	requestHash, err := checkSemanticHash("service.grant.delegate", command)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, storageFailure("hash delegated managed-service grant", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, storageFailure("begin delegated managed-service grant", err)
	}
	defer tx.Rollback()
	scope, err := s.domainAgentSessionScopeInTransaction(ctx, tx, command.ThreadID)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	var replay MutationResult[domain.ManagedServiceGrant]
	key := managedServiceAgentIdempotencyKey(scope.Agent.ID, command.IdempotencyKey)
	if found, lookupErr := lookupIdempotency(ctx, tx, key, "service.grant.delegate", requestHash, &replay); lookupErr != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, lookupErr
	} else if found {
		return replay, nil
	}
	parent, err := queryManagedServiceGrant(ctx, tx, command.ParentGrantID)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	now := s.nowText()
	if parent.Status != domain.ManagedServiceGrantActive || parent.Revision != command.ExpectedParentRevision || parent.ProjectID != scope.Project.ID || parent.ManagerAgentID != scope.Agent.ID || parent.ManagerMembershipRevision != scope.Membership.Revision || !containsExact(parent.Actions, domain.ManagedServiceActionDelegate) {
		return MutationResult[domain.ManagedServiceGrant]{}, managedServiceError(CodeManagedServiceDenied, "parent managed-service grant is not current and delegable for this durable agent")
	}
	if !managedServiceActionSubset(actions, parent.Actions) || command.MaximumInstances > parent.MaximumInstances {
		return MutationResult[domain.ManagedServiceGrant]{}, managedServiceError(CodeManagedServiceDenied, "delegated managed-service grant exceeds its parent")
	}
	if err := validateManagedServiceGrantExpiry(command.ExpiresAt, parent.ExpiresAt, now); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	child, err := queryAgent(ctx, tx, scope.Workspace.ID, command.ManagerAgentIdentifier)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	membership, err := queryDomainAgentMembership(ctx, tx, scope.Project.ID, child.ID)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	if !child.Enabled || membership.Status != domain.DomainAgentActive || membership.Revision != command.ExpectedMembershipRevision || membership.ParentAgentID != scope.Agent.ID {
		return MutationResult[domain.ManagedServiceGrant]{}, managedServiceError(CodeManagedServiceDenied, "delegated managed-service grant target must be one exact active direct child")
	}
	definition, err := queryManagedServiceDefinition(ctx, tx, scope.Workspace.ID, parent.DefinitionID)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	grant, err := insertManagedServiceGrant(ctx, tx, definition, membership, parent.ID, actions, command.MaximumInstances, command.ExpiresAt, scope.Agent.ID, now)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	sequence, err := appendEventForActor(ctx, tx, scope.Workspace.ID, "managed_service_grant", grant.ID, 1, managedServiceGrantDelegatedEvent, command.CorrelationID, now, scope.Agent.ID, domain.EventActorIntegration, managedServiceGrantEventData(grant))
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	result := MutationResult[domain.ManagedServiceGrant]{Value: grant, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "service.grant.delegate", requestHash, result, now); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, storageFailure("commit delegated managed-service grant", err)
	}
	return result, nil
}

func (s *Store) RevokeManagedServiceGrant(ctx context.Context, command RevokeManagedServiceGrantCommand) (MutationResult[domain.ManagedServiceGrant], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.GrantID = strings.TrimSpace(command.GrantID)
	command.Reason = strings.TrimSpace(command.Reason)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.ExpectedRevision < 1 || !validManagedServiceText(command.Reason, 2048) {
		return MutationResult[domain.ManagedServiceGrant]{}, managedServiceError(CodeInvalidManagedService, "managed-service grant revocation requires exact revision and reason")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, storageFailure("begin managed-service grant revocation", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	requestHash, err := checkSemanticHash("service.grant.revoke", command)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, storageFailure("hash managed-service grant revocation", err)
	}
	var replay MutationResult[domain.ManagedServiceGrant]
	key := managedServiceOwnerIdempotencyKey(command.IdempotencyKey)
	if found, lookupErr := lookupIdempotency(ctx, tx, key, "service.grant.revoke", requestHash, &replay); lookupErr != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, lookupErr
	} else if found {
		return replay, nil
	}
	grant, err := queryManagedServiceGrant(ctx, tx, command.GrantID)
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	if grant.WorkspaceID != workspace.ID || grant.Revision != command.ExpectedRevision || grant.Status != domain.ManagedServiceGrantActive {
		return MutationResult[domain.ManagedServiceGrant]{}, managedServiceError(CodeManagedServiceDenied, "managed-service grant is not the exact active grant")
	}
	var activeChildren int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_service_grants WHERE parent_grant_id=? AND status='active'`, grant.ID).Scan(&activeChildren); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, storageFailure("count delegated managed-service grants", err)
	}
	if activeChildren != 0 {
		return MutationResult[domain.ManagedServiceGrant]{}, managedServiceError(CodeManagedServiceConflict, "revoke delegated managed-service grants before their parent")
	}
	now := s.nowText()
	grant.Status, grant.Revision, grant.UpdatedAt, grant.UpdatedBy = domain.ManagedServiceGrantRevoked, grant.Revision+1, now, localOwnerActorID
	if _, err := tx.ExecContext(ctx, `UPDATE managed_service_grants SET status='revoked',revision=?,updated_at=?,updated_by='local-owner' WHERE id=?`, grant.Revision, now, grant.ID); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, managedServiceConstraint("revoke managed-service grant", err)
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "managed_service_grant", grant.ID, grant.Revision, managedServiceGrantRevokedEvent, command.CorrelationID, now, map[string]any{"status": grant.Status, "reason": command.Reason})
	if err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	result := MutationResult[domain.ManagedServiceGrant]{Value: grant, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "service.grant.revoke", requestHash, result, now); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceGrant]{}, storageFailure("commit managed-service grant revocation", err)
	}
	return result, nil
}

func (s *Store) ManagedServiceGrants(ctx context.Context, query ListManagedServiceGrantsQuery) ([]domain.ManagedServiceGrant, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM managed_service_grants WHERE workspace_id=?
AND (?='' OR project_id=(SELECT id FROM projects WHERE workspace_id=? AND (id=? OR name=?)))
AND (?='' OR manager_agent_id=(SELECT id FROM agents WHERE workspace_id=? AND (id=? OR name=?)))
AND (?='' OR definition_id=?) AND (?='' OR status=?) ORDER BY created_at,id LIMIT ?`, workspace.ID,
		strings.TrimSpace(query.ProjectIdentifier), workspace.ID, strings.TrimSpace(query.ProjectIdentifier), strings.TrimSpace(query.ProjectIdentifier),
		strings.TrimSpace(query.ManagerIdentifier), workspace.ID, strings.TrimSpace(query.ManagerIdentifier), strings.TrimSpace(query.ManagerIdentifier),
		strings.TrimSpace(query.DefinitionID), strings.TrimSpace(query.DefinitionID), strings.TrimSpace(query.Status), strings.TrimSpace(query.Status), boundedManagedServiceLimit(query.Limit))
	if err != nil {
		return nil, storageFailure("list managed-service grants", err)
	}
	defer rows.Close()
	result := make([]domain.ManagedServiceGrant, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, storageFailure("scan managed-service grant id", err)
		}
		grant, err := queryManagedServiceGrant(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		result = append(result, grant)
	}
	return result, rows.Err()
}

func (s *Store) StartManagedServiceAsAgent(ctx context.Context, threadID, grantID string, expectedGrantRevision int64, definitionID string, expectedDefinitionRevision int64, idempotencyKey, correlationID string) (MutationResult[domain.ManagedServiceInstance], error) {
	threadID, grantID, definitionID = strings.TrimSpace(threadID), strings.TrimSpace(grantID), strings.TrimSpace(definitionID)
	idempotencyKey, correlationID = strings.TrimSpace(idempotencyKey), strings.TrimSpace(correlationID)
	command := struct {
		ThreadID                   string `json:"thread_id"`
		GrantID                    string `json:"grant_id"`
		ExpectedGrantRevision      int64  `json:"expected_grant_revision"`
		DefinitionID               string `json:"definition_id"`
		ExpectedDefinitionRevision int64  `json:"expected_definition_revision"`
	}{threadID, grantID, expectedGrantRevision, definitionID, expectedDefinitionRevision}
	if expectedGrantRevision < 1 || expectedDefinitionRevision < 1 || definitionID == "" {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceError(CodeInvalidManagedService, "agent managed-service start requires exact grant and definition revisions")
	}
	if err := validateMutationMetadata(idempotencyKey, correlationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	requestHash, err := checkSemanticHash("service.agent.start", command)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("hash agent managed-service start", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("begin agent managed-service start", err)
	}
	defer tx.Rollback()
	scope, err := s.domainAgentSessionScopeInTransaction(ctx, tx, threadID)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	var replay MutationResult[domain.ManagedServiceInstance]
	key := managedServiceAgentIdempotencyKey(scope.Agent.ID, idempotencyKey)
	if found, lookupErr := lookupIdempotency(ctx, tx, key, "service.agent.start", requestHash, &replay); lookupErr != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, lookupErr
	} else if found {
		return replay, nil
	}
	grant, definition, err := validateManagedServiceAgentGrant(ctx, tx, scope, grantID, expectedGrantRevision, definitionID, expectedDefinitionRevision, domain.ManagedServiceActionStart, s.nowText())
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_service_instances WHERE source_grant_id=? AND status IN ('requested','starting','healthy','degraded','stopping','unknown')`, grant.ID).Scan(&active); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("count grant managed-service instances", err)
	}
	if active >= grant.MaximumInstances {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceError(CodeManagedServiceCapacity, fmt.Sprintf("managed-service grant capacity is exhausted: %d of %d instances are unresolved", active, grant.MaximumInstances))
	}
	now := s.nowText()
	source := domain.ManagedServiceSource{Type: domain.ManagedServiceSourceAgent, ActorID: scope.Agent.ID, AgentID: scope.Agent.ID, AgentRevision: scope.Membership.Revision, ThreadID: scope.Session.ThreadID, GrantID: grant.ID, GrantRevision: grant.Revision}
	result, err := s.startManagedServiceInTx(ctx, tx, scope.Workspace.ID, definition, source, correlationID, now)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if err := recordIdempotency(ctx, tx, key, "service.agent.start", requestHash, result, now); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("commit agent managed-service start", err)
	}
	return result, nil
}

func (s *Store) RequestManagedServiceActionAsAgent(ctx context.Context, command AgentManagedServiceActionCommand) (MutationResult[domain.ManagedServiceInstance], error) {
	command.ThreadID, command.GrantID, command.InstanceID, command.Action = strings.TrimSpace(command.ThreadID), strings.TrimSpace(command.GrantID), strings.TrimSpace(command.InstanceID), strings.TrimSpace(command.Action)
	command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ExpectedGrantRevision < 1 || command.ExpectedRevision < 1 || (command.Action != domain.ManagedServiceActionStop && command.Action != domain.ManagedServiceActionRestart) {
		return MutationResult[domain.ManagedServiceInstance]{}, managedServiceError(CodeInvalidManagedService, "agent managed-service control requires exact grant and instance revisions and stop or restart")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	requestHash, err := checkSemanticHash("service.agent."+command.Action, command)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("hash agent managed-service control", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("begin agent managed-service control", err)
	}
	defer tx.Rollback()
	scope, err := s.domainAgentSessionScopeInTransaction(ctx, tx, command.ThreadID)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	operation := "service.agent." + command.Action
	key := managedServiceAgentIdempotencyKey(scope.Agent.ID, command.IdempotencyKey)
	var replay MutationResult[domain.ManagedServiceInstance]
	if found, lookupErr := lookupIdempotency(ctx, tx, key, operation, requestHash, &replay); lookupErr != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, lookupErr
	} else if found {
		return replay, nil
	}
	instance, err := queryManagedServiceInstance(ctx, tx, command.InstanceID)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if instance.ProjectID != scope.Project.ID || instance.Revision != command.ExpectedRevision {
		return MutationResult[domain.ManagedServiceInstance]{}, revisionConflict("managed service", instance.ID, command.ExpectedRevision, instance.Revision)
	}
	if _, _, err := validateManagedServiceAgentGrant(ctx, tx, scope, command.GrantID, command.ExpectedGrantRevision, instance.DefinitionID, instance.DefinitionRevision, command.Action, s.nowText()); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	now := s.nowText()
	result, err := s.requestManagedServiceActionInTx(ctx, tx, instance, command.Action, scope.Agent.ID, domain.EventActorIntegration, command.CorrelationID, now)
	if err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if err := recordIdempotency(ctx, tx, key, operation, requestHash, result, now); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceInstance]{}, storageFailure("commit agent managed-service control", err)
	}
	return result, nil
}

func (s *Store) SubmitManagedServiceRequest(ctx context.Context, command SubmitManagedServiceRequestCommand) (MutationResult[domain.ManagedServiceRequest], error) {
	command.ThreadID, command.DefinitionID, command.Summary = strings.TrimSpace(command.ThreadID), strings.TrimSpace(command.DefinitionID), strings.TrimSpace(command.Summary)
	command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ExpectedRevision < 1 || !validManagedServiceText(command.Summary, 2048) {
		return MutationResult[domain.ManagedServiceRequest]{}, managedServiceError(CodeInvalidManagedService, "managed-service request requires an exact definition revision and bounded summary")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceRequest]{}, err
	}
	requestHash, err := checkSemanticHash("service.request", command)
	if err != nil {
		return MutationResult[domain.ManagedServiceRequest]{}, storageFailure("hash managed-service request", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceRequest]{}, storageFailure("begin managed-service request", err)
	}
	defer tx.Rollback()
	scope, err := s.domainAgentSessionScopeInTransaction(ctx, tx, command.ThreadID)
	if err != nil {
		return MutationResult[domain.ManagedServiceRequest]{}, err
	}
	var replay MutationResult[domain.ManagedServiceRequest]
	key := managedServiceAgentIdempotencyKey(scope.Agent.ID, command.IdempotencyKey)
	if found, lookupErr := lookupIdempotency(ctx, tx, key, "service.request", requestHash, &replay); lookupErr != nil {
		return MutationResult[domain.ManagedServiceRequest]{}, lookupErr
	} else if found {
		return replay, nil
	}
	definition, err := queryManagedServiceDefinition(ctx, tx, scope.Workspace.ID, command.DefinitionID)
	if err != nil {
		return MutationResult[domain.ManagedServiceRequest]{}, err
	}
	if definition.ProjectID != scope.Project.ID || definition.Status != domain.ManagedServiceDefinitionActive || definition.Revision != command.ExpectedRevision {
		return MutationResult[domain.ManagedServiceRequest]{}, managedServiceError(CodeManagedServiceDenied, "managed-service request definition is outside this durable agent's exact current domain")
	}
	id, err := randomID("svcrequest_")
	if err != nil {
		return MutationResult[domain.ManagedServiceRequest]{}, storageFailure("generate managed-service request id", err)
	}
	now := s.nowText()
	request := domain.ManagedServiceRequest{ID: id, WorkspaceID: scope.Workspace.ID, ProjectID: scope.Project.ID, DefinitionID: definition.ID, DefinitionRevision: definition.Revision, AgentID: scope.Agent.ID, AgentMembershipRevision: scope.Membership.Revision, ThreadID: scope.Session.ThreadID, Summary: command.Summary, Status: domain.ManagedServiceRequestPending, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO managed_service_requests(id,workspace_id,project_id,definition_id,definition_revision,agent_id,agent_membership_revision,thread_id,summary,status,revision,created_at,updated_at,decided_at,decision_reason) VALUES(?,?,?,?,?,?,?,?,?,'pending',1,?,?,NULL,NULL)`, request.ID, request.WorkspaceID, request.ProjectID, request.DefinitionID, request.DefinitionRevision, request.AgentID, request.AgentMembershipRevision, request.ThreadID, request.Summary, now, now); err != nil {
		return MutationResult[domain.ManagedServiceRequest]{}, managedServiceConstraint("insert managed-service request", err)
	}
	sequence, err := appendEventForActor(ctx, tx, scope.Workspace.ID, "managed_service_request", request.ID, 1, managedServiceRequestCreatedEvent, command.CorrelationID, now, scope.Agent.ID, domain.EventActorIntegration, managedServiceRequestEventData(request))
	if err != nil {
		return MutationResult[domain.ManagedServiceRequest]{}, err
	}
	result := MutationResult[domain.ManagedServiceRequest]{Value: request, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "service.request", requestHash, result, now); err != nil {
		return MutationResult[domain.ManagedServiceRequest]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceRequest]{}, storageFailure("commit managed-service request", err)
	}
	return result, nil
}

func (s *Store) DecideManagedServiceRequest(ctx context.Context, command DecideManagedServiceRequestCommand) (MutationResult[domain.ManagedServiceRequestDecision], error) {
	command.WorkspaceIdentifier, command.RequestID, command.Reason = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.RequestID), strings.TrimSpace(command.Reason)
	command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ExpectedRevision < 1 || !validManagedServiceText(command.Reason, 2048) {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, managedServiceError(CodeInvalidManagedService, "managed-service request decision requires exact revision and reason")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagedService); err != nil {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, err
	}
	requestHash, err := checkSemanticHash("service.request.decide", command)
	if err != nil {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, storageFailure("hash managed-service request decision", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, storageFailure("begin managed-service request decision", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, err
	}
	var replay MutationResult[domain.ManagedServiceRequestDecision]
	key := managedServiceOwnerIdempotencyKey(command.IdempotencyKey)
	if found, lookupErr := lookupIdempotency(ctx, tx, key, "service.request.decide", requestHash, &replay); lookupErr != nil {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, lookupErr
	} else if found {
		return replay, nil
	}
	request, err := queryManagedServiceRequest(ctx, tx, command.RequestID)
	if err != nil {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, err
	}
	if request.WorkspaceID != workspace.ID || request.Status != domain.ManagedServiceRequestPending || request.Revision != command.ExpectedRevision {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, managedServiceError(CodeManagedServiceDenied, "managed-service request is not the exact pending owner-review item")
	}
	now := s.nowText()
	request.Revision++
	request.UpdatedAt, request.DecidedAt, request.DecisionReason = now, now, command.Reason
	if command.Accept {
		request.Status = domain.ManagedServiceRequestAccepted
	} else {
		request.Status = domain.ManagedServiceRequestRejected
	}
	if _, err := tx.ExecContext(ctx, `UPDATE managed_service_requests SET status=?,revision=?,updated_at=?,decided_at=?,decision_reason=? WHERE id=?`, request.Status, request.Revision, now, now, request.DecisionReason, request.ID); err != nil {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, managedServiceConstraint("decide managed-service request", err)
	}
	eventType := managedServiceRequestRejectedEvent
	if command.Accept {
		eventType = managedServiceRequestAcceptedEvent
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "managed_service_request", request.ID, request.Revision, eventType, command.CorrelationID, now, map[string]any{"status": request.Status, "reason": request.DecisionReason})
	if err != nil {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, err
	}
	decision := domain.ManagedServiceRequestDecision{Request: request}
	definition, queryErr := queryManagedServiceDefinition(ctx, tx, workspace.ID, request.DefinitionID)
	if queryErr != nil {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, queryErr
	}
	if definition.Revision != request.DefinitionRevision {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, managedServiceError(CodeManagedServiceConflict, "managed-service request definition is no longer current")
	}
	if command.Accept {
		if definition.Status != domain.ManagedServiceDefinitionActive {
			return MutationResult[domain.ManagedServiceRequestDecision]{}, managedServiceError(CodeManagedServiceConflict, "managed-service request definition is no longer current")
		}
		if definition.CreatedBy == request.AgentID {
			membership, membershipErr := queryDomainAgentMembership(ctx, tx, definition.ProjectID, request.AgentID)
			if membershipErr != nil {
				return MutationResult[domain.ManagedServiceRequestDecision]{}, membershipErr
			}
			if membership.Status != domain.DomainAgentActive || membership.Revision != request.AgentMembershipRevision {
				return MutationResult[domain.ManagedServiceRequestDecision]{}, managedServiceError(CodeManagedServiceDenied, "managed-service proposal agent membership is no longer current")
			}
			actions, normalizeErr := normalizeManagedServiceActions([]string{
				domain.ManagedServiceActionInspect,
				domain.ManagedServiceActionLogs,
				domain.ManagedServiceActionStart,
				domain.ManagedServiceActionStop,
				domain.ManagedServiceActionRestart,
			})
			if normalizeErr != nil {
				return MutationResult[domain.ManagedServiceRequestDecision]{}, normalizeErr
			}
			grant, grantErr := insertManagedServiceGrant(ctx, tx, definition, membership, "", actions, 1, "", localOwnerActorID, now)
			if grantErr != nil {
				return MutationResult[domain.ManagedServiceRequestDecision]{}, grantErr
			}
			sequence, grantErr = appendEvent(ctx, tx, workspace.ID, "managed_service_grant", grant.ID, grant.Revision, managedServiceGrantCreatedEvent, command.CorrelationID, now, managedServiceGrantEventData(grant))
			if grantErr != nil {
				return MutationResult[domain.ManagedServiceRequestDecision]{}, grantErr
			}
			decision.Grant = &grant
		}
		source := domain.ManagedServiceSource{Type: domain.ManagedServiceSourceRequest, ActorID: request.AgentID, AgentID: request.AgentID, AgentRevision: request.AgentMembershipRevision, ThreadID: request.ThreadID, RequestID: request.ID}
		started, startErr := s.startManagedServiceInTx(ctx, tx, workspace.ID, definition, source, command.CorrelationID, now)
		if startErr != nil {
			return MutationResult[domain.ManagedServiceRequestDecision]{}, startErr
		}
		decision.Instance = &started.Value
		sequence = started.EventSequence
	} else if definition.CreatedBy == request.AgentID && definition.Status == domain.ManagedServiceDefinitionActive {
		definition.Status = domain.ManagedServiceDefinitionRetired
		definition.Revision++
		definition.UpdatedAt = now
		definition.UpdatedBy = localOwnerActorID
		if _, updateErr := tx.ExecContext(ctx, `UPDATE managed_service_definitions SET status='retired',revision=?,updated_at=?,updated_by='local-owner' WHERE id=? AND status='active'`, definition.Revision, now, definition.ID); updateErr != nil {
			return MutationResult[domain.ManagedServiceRequestDecision]{}, managedServiceConstraint("retire rejected agent-authored managed-service definition", updateErr)
		}
		sequence, err = appendEvent(ctx, tx, workspace.ID, "managed_service_definition", definition.ID, definition.Revision, managedServiceDefinitionRetiredEvent, command.CorrelationID, now, map[string]any{"reason": "owner rejected agent-authored process proposal", "request_id": request.ID})
		if err != nil {
			return MutationResult[domain.ManagedServiceRequestDecision]{}, err
		}
	}
	result := MutationResult[domain.ManagedServiceRequestDecision]{Value: decision, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "service.request.decide", requestHash, result, now); err != nil {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagedServiceRequestDecision]{}, storageFailure("commit managed-service request decision", err)
	}
	return result, nil
}

func (s *Store) ManagedServiceRequests(ctx context.Context, query ListManagedServiceRequestsQuery) ([]domain.ManagedServiceRequest, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM managed_service_requests WHERE workspace_id=?
AND (?='' OR project_id=(SELECT id FROM projects WHERE workspace_id=? AND (id=? OR name=?)))
AND (?='' OR agent_id=(SELECT id FROM agents WHERE workspace_id=? AND (id=? OR name=?)))
AND (?='' OR definition_id=?) AND (?='' OR status=?) ORDER BY created_at,id LIMIT ?`, workspace.ID,
		strings.TrimSpace(query.ProjectIdentifier), workspace.ID, strings.TrimSpace(query.ProjectIdentifier), strings.TrimSpace(query.ProjectIdentifier),
		strings.TrimSpace(query.AgentIdentifier), workspace.ID, strings.TrimSpace(query.AgentIdentifier), strings.TrimSpace(query.AgentIdentifier),
		strings.TrimSpace(query.DefinitionID), strings.TrimSpace(query.DefinitionID), strings.TrimSpace(query.Status), strings.TrimSpace(query.Status), boundedManagedServiceLimit(query.Limit))
	if err != nil {
		return nil, storageFailure("list managed-service requests", err)
	}
	defer rows.Close()
	result := make([]domain.ManagedServiceRequest, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, storageFailure("scan managed-service request id", err)
		}
		request, err := queryManagedServiceRequest(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		result = append(result, request)
	}
	return result, rows.Err()
}

// ManagedServiceDetailAsAgent returns one current service projection only
// while the calling durable session holds the exact current inspect or logs
// grant for that instance's frozen definition. The scope, grant, instance, and
// detail are resolved in one read snapshot.
func (s *Store) ManagedServiceDetailAsAgent(ctx context.Context, threadID, grantID string, expectedGrantRevision int64, instanceID string, expectedInstanceRevision int64, action string) (domain.ManagedServiceDetail, error) {
	threadID, grantID, instanceID, action = strings.TrimSpace(threadID), strings.TrimSpace(grantID), strings.TrimSpace(instanceID), strings.TrimSpace(action)
	if expectedGrantRevision < 1 || expectedInstanceRevision < 1 || (action != domain.ManagedServiceActionInspect && action != domain.ManagedServiceActionLogs) {
		return domain.ManagedServiceDetail{}, managedServiceError(CodeInvalidManagedService, "agent managed-service read requires an exact grant, instance revision, and inspect or logs action")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ManagedServiceDetail{}, storageFailure("begin agent managed-service read", err)
	}
	defer tx.Rollback()
	scope, err := s.domainAgentSessionScopeInTransaction(ctx, tx, threadID)
	if err != nil {
		return domain.ManagedServiceDetail{}, err
	}
	instance, err := queryManagedServiceInstance(ctx, tx, instanceID)
	if err != nil {
		return domain.ManagedServiceDetail{}, err
	}
	if instance.ProjectID != scope.Project.ID || instance.Revision != expectedInstanceRevision {
		return domain.ManagedServiceDetail{}, managedServiceError(CodeManagedServiceDenied, "managed-service instance is not the exact current instance in this durable agent's project")
	}
	if _, _, err := validateManagedServiceAgentGrant(ctx, tx, scope, grantID, expectedGrantRevision, instance.DefinitionID, instance.DefinitionRevision, action, s.nowText()); err != nil {
		return domain.ManagedServiceDetail{}, err
	}
	detail, err := managedServiceDetailFromDatabase(ctx, tx, instance)
	if err != nil {
		return domain.ManagedServiceDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ManagedServiceDetail{}, storageFailure("commit agent managed-service read", err)
	}
	return detail, nil
}

func insertManagedServiceGrant(ctx context.Context, tx *sql.Tx, definition domain.ManagedServiceDefinition, membership domain.DomainAgentMembership, parentGrantID string, actions []string, maximumInstances int, expiresAt, actor, now string) (domain.ManagedServiceGrant, error) {
	id, err := randomID("svcgrant_")
	if err != nil {
		return domain.ManagedServiceGrant{}, storageFailure("generate managed-service grant id", err)
	}
	actionsJSON, err := json.Marshal(actions)
	if err != nil {
		return domain.ManagedServiceGrant{}, storageFailure("encode managed-service grant actions", err)
	}
	grant := domain.ManagedServiceGrant{ID: id, WorkspaceID: definition.WorkspaceID, ProjectID: definition.ProjectID, DefinitionID: definition.ID, DefinitionRevision: definition.Revision, ManagerAgentID: membership.AgentID, ManagerMembershipRevision: membership.Revision, ParentGrantID: parentGrantID, Actions: actions, MaximumInstances: maximumInstances, ExpiresAt: expiresAt, Status: domain.ManagedServiceGrantActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: actor, UpdatedBy: actor}
	if _, err := tx.ExecContext(ctx, `INSERT INTO managed_service_grants(id,workspace_id,project_id,definition_id,definition_revision,manager_agent_id,manager_membership_revision,parent_grant_id,actions_json,maximum_instances,expires_at,status,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,?,?,?,?,?,NULLIF(?,''),?,?,NULLIF(?,''),'active',1,?,?,?,?)`, grant.ID, grant.WorkspaceID, grant.ProjectID, grant.DefinitionID, grant.DefinitionRevision, grant.ManagerAgentID, grant.ManagerMembershipRevision, grant.ParentGrantID, string(actionsJSON), grant.MaximumInstances, grant.ExpiresAt, now, now, actor, actor); err != nil {
		return domain.ManagedServiceGrant{}, managedServiceConstraint("insert managed-service grant", err)
	}
	return grant, nil
}

func queryManagedServiceGrant(ctx context.Context, database messageQueryContext, id string) (domain.ManagedServiceGrant, error) {
	var grant domain.ManagedServiceGrant
	var actionsJSON string
	err := database.QueryRowContext(ctx, `SELECT id,workspace_id,project_id,definition_id,definition_revision,manager_agent_id,manager_membership_revision,COALESCE(parent_grant_id,''),actions_json,maximum_instances,COALESCE(expires_at,''),status,revision,created_at,updated_at,created_by,updated_by FROM managed_service_grants WHERE id=?`, strings.TrimSpace(id)).Scan(&grant.ID, &grant.WorkspaceID, &grant.ProjectID, &grant.DefinitionID, &grant.DefinitionRevision, &grant.ManagerAgentID, &grant.ManagerMembershipRevision, &grant.ParentGrantID, &actionsJSON, &grant.MaximumInstances, &grant.ExpiresAt, &grant.Status, &grant.Revision, &grant.CreatedAt, &grant.UpdatedAt, &grant.CreatedBy, &grant.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return grant, managedServiceError(CodeManagedServiceGrantNotFound, fmt.Sprintf("managed-service grant %q was not found", id))
	}
	if err != nil {
		return grant, storageFailure("query managed-service grant", err)
	}
	if err := json.Unmarshal([]byte(actionsJSON), &grant.Actions); err != nil {
		return grant, storageFailure("decode managed-service grant actions", err)
	}
	return grant, nil
}

func queryManagedServiceRequest(ctx context.Context, database queryRower, id string) (domain.ManagedServiceRequest, error) {
	var request domain.ManagedServiceRequest
	err := database.QueryRowContext(ctx, `SELECT id,workspace_id,project_id,definition_id,definition_revision,agent_id,agent_membership_revision,thread_id,summary,status,revision,created_at,updated_at,COALESCE(decided_at,''),COALESCE(decision_reason,'') FROM managed_service_requests WHERE id=?`, strings.TrimSpace(id)).Scan(&request.ID, &request.WorkspaceID, &request.ProjectID, &request.DefinitionID, &request.DefinitionRevision, &request.AgentID, &request.AgentMembershipRevision, &request.ThreadID, &request.Summary, &request.Status, &request.Revision, &request.CreatedAt, &request.UpdatedAt, &request.DecidedAt, &request.DecisionReason)
	if errors.Is(err, sql.ErrNoRows) {
		return request, managedServiceError(CodeManagedServiceRequestNotFound, fmt.Sprintf("managed-service request %q was not found", id))
	}
	if err != nil {
		return request, storageFailure("query managed-service request", err)
	}
	return request, nil
}

func validateManagedServiceAgentGrant(ctx context.Context, tx *sql.Tx, scope domain.DomainAgentSessionScope, grantID string, expectedGrantRevision int64, definitionID string, expectedDefinitionRevision int64, action, now string) (domain.ManagedServiceGrant, domain.ManagedServiceDefinition, error) {
	grant, err := queryManagedServiceGrant(ctx, tx, grantID)
	if err != nil {
		return grant, domain.ManagedServiceDefinition{}, err
	}
	if grant.Status != domain.ManagedServiceGrantActive || grant.Revision != expectedGrantRevision || grant.ProjectID != scope.Project.ID || grant.ManagerAgentID != scope.Agent.ID || grant.ManagerMembershipRevision != scope.Membership.Revision || grant.DefinitionID != definitionID || grant.DefinitionRevision != expectedDefinitionRevision || !containsExact(grant.Actions, action) {
		return grant, domain.ManagedServiceDefinition{}, managedServiceError(CodeManagedServiceDenied, "managed-service action is outside this durable agent's exact current grant")
	}
	if grant.ExpiresAt != "" && timestampNotAfter(grant.ExpiresAt, now) {
		return grant, domain.ManagedServiceDefinition{}, managedServiceError(CodeManagedServiceDenied, "managed-service grant has expired")
	}
	definition, err := queryManagedServiceDefinition(ctx, tx, scope.Workspace.ID, definitionID)
	if err != nil {
		return grant, domain.ManagedServiceDefinition{}, err
	}
	if definition.Status != domain.ManagedServiceDefinitionActive || definition.Revision != expectedDefinitionRevision {
		return grant, domain.ManagedServiceDefinition{}, managedServiceError(CodeManagedServiceDenied, "managed-service definition is no longer the exact granted revision")
	}
	return grant, definition, nil
}

func normalizeManagedServiceActions(values []string) ([]string, error) {
	result := make([]string, len(values))
	seen := make(map[string]bool, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		switch value {
		case domain.ManagedServiceActionInspect, domain.ManagedServiceActionLogs, domain.ManagedServiceActionStart, domain.ManagedServiceActionStop, domain.ManagedServiceActionRestart, domain.ManagedServiceActionDelegate:
		default:
			return nil, managedServiceError(CodeInvalidManagedService, "managed-service grant contains an unknown action")
		}
		if seen[value] {
			return nil, managedServiceError(CodeInvalidManagedService, "managed-service grant actions must be distinct")
		}
		seen[value], result[index] = true, value
	}
	if len(result) == 0 || len(result) > 6 {
		return nil, managedServiceError(CodeInvalidManagedService, "managed-service grant requires one to six actions")
	}
	sort.Strings(result)
	return result, nil
}

func managedServiceActionSubset(child, parent []string) bool {
	for _, action := range child {
		if !containsExact(parent, action) {
			return false
		}
	}
	return true
}

func validateManagedServiceGrantExpiry(expiresAt, parentExpiresAt, now string) error {
	if expiresAt == "" {
		if parentExpiresAt != "" {
			return managedServiceError(CodeManagedServiceDenied, "delegated managed-service grant cannot outlive its parent")
		}
		return nil
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || expires.Format(time.RFC3339Nano) != expiresAt || timestampNotAfter(expiresAt, now) {
		return managedServiceError(CodeInvalidManagedService, "managed-service grant expiry must be a canonical future timestamp")
	}
	if parentExpiresAt != "" {
		parent, parentErr := time.Parse(time.RFC3339Nano, parentExpiresAt)
		if parentErr != nil || expires.After(parent) {
			return managedServiceError(CodeManagedServiceDenied, "delegated managed-service grant cannot outlive its parent")
		}
	}
	return nil
}

func timestampNotAfter(left, right string) bool {
	l, leftErr := time.Parse(time.RFC3339Nano, left)
	r, rightErr := time.Parse(time.RFC3339Nano, right)
	return leftErr != nil || rightErr != nil || !l.After(r)
}

func managedServiceAgentIdempotencyKey(agentID, raw string) string {
	return "service:agent:" + agentID + ":" + strings.TrimSpace(raw)
}

func managedServiceGrantEventData(grant domain.ManagedServiceGrant) map[string]any {
	return map[string]any{"project_id": grant.ProjectID, "definition_id": grant.DefinitionID, "definition_revision": grant.DefinitionRevision, "manager_agent_id": grant.ManagerAgentID, "manager_membership_revision": grant.ManagerMembershipRevision, "parent_grant_id": grant.ParentGrantID, "actions": grant.Actions, "maximum_instances": grant.MaximumInstances, "expires_at": grant.ExpiresAt, "status": grant.Status}
}

func managedServiceRequestEventData(request domain.ManagedServiceRequest) map[string]any {
	return map[string]any{"project_id": request.ProjectID, "definition_id": request.DefinitionID, "definition_revision": request.DefinitionRevision, "agent_id": request.AgentID, "agent_membership_revision": request.AgentMembershipRevision, "summary": request.Summary, "status": request.Status}
}
