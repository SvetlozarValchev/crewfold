package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"crewfold/internal/domain"
)

const (
	domainStaffingGrantCreated = "domain.staffing_grant_created"
	domainStaffingGrantRevoked = "domain.staffing_grant_revoked"
	domainChildCreated         = "domain.child_created"
)

func (s *Store) CreateDomainAgentStaffingGrant(ctx context.Context, command CreateDomainAgentStaffingGrantCommand) (MutationResult[domain.DomainAgentStaffingGrant], error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	projectIdentifier := strings.TrimSpace(command.ProjectIdentifier)
	managerIdentifier := strings.TrimSpace(command.ManagerAgentIdentifier)
	expiresAt := strings.TrimSpace(command.ExpiresAt)
	key, correlationID := strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	profiles, err := normalizeDomainStaffingProfiles(command.Profiles)
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	taskClasses, err := normalizeDomainTaskClasses(command.TaskClasses)
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	if workspaceIdentifier == "" || projectIdentifier == "" || managerIdentifier == "" ||
		command.ExpectedMembershipRevision < 1 || len(profiles) == 0 || len(profiles) > 32 ||
		len(taskClasses) == 0 || len(taskClasses) > 32 || command.MaxDescendants < 1 || command.MaxDescendants > 1000 ||
		command.MaxConcurrency < 1 || command.MaxConcurrency > 100 || !validBudget(command.Budget) ||
		(expiresAt != "" && !canonicalTimestampText(expiresAt)) {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, domainAgentError(CodeInvalidDomainStaffingGrant, "staffing grant requires exact domain, manager revision, profiles, task classes, descendant/concurrency ceilings, non-negative budget, and optional canonical expiry")
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidDomainStaffingGrant); err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	requestHash, err := hashCommand("domain.staffing-grant.create", map[string]any{
		"workspace": workspaceIdentifier, "project": projectIdentifier, "manager": managerIdentifier,
		"manager_membership_revision": command.ExpectedMembershipRevision, "profiles": profiles,
		"task_classes": taskClasses, "max_descendants": command.MaxDescendants,
		"max_concurrency": command.MaxConcurrency, "budget": command.Budget, "expires_at": expiresAt,
	})
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, storageFailure("hash domain staffing grant", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, storageFailure("begin domain staffing grant", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.DomainAgentStaffingGrant]
	if found, lookupErr := lookupIdempotency(ctx, tx, key, "domain.staffing-grant.create", requestHash, &replay); lookupErr != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, lookupErr
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, projectIdentifier)
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	manager, err := queryAgent(ctx, tx, workspace.ID, managerIdentifier)
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	membership, err := queryDomainAgentMembership(ctx, tx, project.ID, manager.ID)
	if err != nil || membership.Status != domain.DomainAgentActive || membership.Revision != command.ExpectedMembershipRevision {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, domainAgentError(CodeInvalidDomainStaffingGrant, "staffing manager must be one exact current active domain membership")
	}
	if expiresAt != "" {
		expires, _ := time.Parse(time.RFC3339Nano, expiresAt)
		if !expires.After(s.clock().UTC()) {
			return MutationResult[domain.DomainAgentStaffingGrant]{}, domainAgentError(CodeInvalidDomainStaffingGrant, "staffing grant expiry must be in the future")
		}
	}
	id, err := randomID("staffgrant_")
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, storageFailure("generate domain staffing grant", err)
	}
	now := s.nowText()
	grant := domain.DomainAgentStaffingGrant{
		ID: id, ProjectID: project.ID, ManagerAgentID: manager.ID, ManagerMembershipRevision: membership.Revision,
		Profiles: profiles, TaskClasses: taskClasses, MaxDescendants: command.MaxDescendants,
		MaxConcurrency: command.MaxConcurrency, Budget: command.Budget, ExpiresAt: expiresAt,
		Status: domain.DomainStaffingGrantActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
		CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_agent_staffing_grants(
id,project_id,manager_agent_id,manager_membership_revision,max_descendants,max_concurrency,
budget_tokens,budget_cost_cents,budget_time_seconds,expires_at,status,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,?,?,?,?,?)`, grant.ID, grant.ProjectID, grant.ManagerAgentID,
		grant.ManagerMembershipRevision, grant.MaxDescendants, grant.MaxConcurrency, grant.Budget.TokenLimit,
		grant.Budget.CostCents, grant.Budget.TimeSeconds, grant.ExpiresAt, grant.Status, grant.Revision,
		grant.CreatedAt, grant.UpdatedAt, grant.CreatedBy, grant.UpdatedBy); err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, storageFailure("insert domain staffing grant", err)
	}
	for _, profile := range profiles {
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_agent_staffing_profiles(grant_id,provider,runtime,max_concurrency) VALUES(?,?,?,?)`,
			grant.ID, profile.Provider, profile.Runtime, profile.MaxConcurrency); err != nil {
			return MutationResult[domain.DomainAgentStaffingGrant]{}, storageFailure("insert domain staffing profile", err)
		}
	}
	for _, taskClass := range taskClasses {
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_agent_staffing_task_classes(grant_id,task_class) VALUES(?,?)`, grant.ID, taskClass); err != nil {
			return MutationResult[domain.DomainAgentStaffingGrant]{}, storageFailure("insert domain staffing task class", err)
		}
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "domain_staffing_grant", grant.ID, grant.Revision,
		domainStaffingGrantCreated, correlationID, now, domainStaffingGrantEventData(grant))
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	result := MutationResult[domain.DomainAgentStaffingGrant]{Value: grant, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "domain.staffing-grant.create", requestHash, result, now); err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, storageFailure("commit domain staffing grant", err)
	}
	return result, nil
}

func (s *Store) RevokeDomainAgentStaffingGrant(ctx context.Context, command RevokeDomainAgentStaffingGrantCommand) (MutationResult[domain.DomainAgentStaffingGrant], error) {
	workspaceIdentifier, grantID := strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.GrantID)
	key, correlationID := strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if workspaceIdentifier == "" || grantID == "" || command.ExpectedRevision < 1 {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, domainAgentError(CodeInvalidDomainStaffingGrant, "staffing grant revocation requires workspace, grant, and expected revision")
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidDomainStaffingGrant); err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, storageFailure("begin domain staffing revocation", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	requestHash, _ := hashCommand("domain.staffing-grant.revoke", map[string]any{"workspace_id": workspace.ID, "grant_id": grantID, "expected_revision": command.ExpectedRevision})
	var replay MutationResult[domain.DomainAgentStaffingGrant]
	if found, lookupErr := lookupIdempotency(ctx, tx, key, "domain.staffing-grant.revoke", requestHash, &replay); lookupErr != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, lookupErr
	} else if found {
		return replay, nil
	}
	grant, err := queryDomainAgentStaffingGrant(ctx, tx, grantID)
	if err != nil || grant.ProjectID == "" {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	var projectWorkspaceID string
	if err := tx.QueryRowContext(ctx, "SELECT workspace_id FROM projects WHERE id=?", grant.ProjectID).Scan(&projectWorkspaceID); err != nil || projectWorkspaceID != workspace.ID {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, domainAgentError(CodeDomainStaffingGrantNotFound, "staffing grant is outside this workspace")
	}
	if grant.Revision != command.ExpectedRevision {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, revisionConflict("domain_staffing_grant", grant.ID, command.ExpectedRevision, grant.Revision)
	}
	if grant.Status != domain.DomainStaffingGrantActive {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, domainAgentError(CodeDomainStaffingDenied, "only an active staffing grant can be revoked")
	}
	now := s.nowText()
	grant.Status, grant.Revision, grant.UpdatedAt = domain.DomainStaffingGrantRevoked, grant.Revision+1, now
	if _, err := tx.ExecContext(ctx, `UPDATE domain_agent_staffing_grants SET status=?,revision=?,updated_at=?,updated_by='local-owner' WHERE id=?`,
		grant.Status, grant.Revision, grant.UpdatedAt, grant.ID); err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, storageFailure("revoke domain staffing grant", err)
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "domain_staffing_grant", grant.ID, grant.Revision,
		domainStaffingGrantRevoked, correlationID, now, domainStaffingGrantEventData(grant))
	if err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	result := MutationResult[domain.DomainAgentStaffingGrant]{Value: grant, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "domain.staffing-grant.revoke", requestHash, result, now); err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.DomainAgentStaffingGrant]{}, storageFailure("commit domain staffing revocation", err)
	}
	return result, nil
}

func (s *Store) CreateDomainAgentChild(ctx context.Context, command CreateDomainAgentChildCommand) (domain.DomainAgentChildCreation, error) {
	command.ThreadID, command.GrantID = strings.TrimSpace(command.ThreadID), strings.TrimSpace(command.GrantID)
	command.Name, command.Role = strings.TrimSpace(command.Name), strings.TrimSpace(command.Role)
	command.Provider, command.Runtime = strings.TrimSpace(command.Provider), strings.TrimSpace(command.Runtime)
	command.Workstream, command.TaskClass = strings.TrimSpace(command.Workstream), strings.TrimSpace(command.TaskClass)
	command.OperatingCharter, command.DelegationPolicy = strings.TrimSpace(command.OperatingCharter), strings.TrimSpace(command.DelegationPolicy)
	command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ThreadID == "" || command.GrantID == "" || !workspaceNamePattern.MatchString(command.Name) ||
		!validShortText(command.Role) || !validShortText(command.Provider) || !validShortText(command.Runtime) ||
		!validDomainAgentCharter(command.OperatingCharter) || !validDomainAgentDelegationPolicy(command.DelegationPolicy) ||
		command.MaxConcurrency < 1 || command.MaxConcurrency > 100 || !workspaceNamePattern.MatchString(command.TaskClass) ||
		!validBudget(command.Budget) {
		return domain.DomainAgentChildCreation{}, domainAgentError(CodeDomainStaffingDenied, "durable child request is outside the typed staffing contract")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeDomainStaffingDenied); err != nil {
		return domain.DomainAgentChildCreation{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.DomainAgentChildCreation{}, storageFailure("begin durable child creation", err)
	}
	defer tx.Rollback()
	scope, err := s.domainAgentSessionScopeInTransaction(ctx, tx, command.ThreadID)
	if err != nil {
		return domain.DomainAgentChildCreation{}, err
	}
	request := map[string]any{
		"project_id": scope.Project.ID, "parent_agent_id": scope.Agent.ID, "grant_id": command.GrantID,
		"name": command.Name, "role": command.Role, "provider": command.Provider, "runtime": command.Runtime,
		"max_concurrency": command.MaxConcurrency, "workstream": command.Workstream,
		"operating_charter": command.OperatingCharter, "delegation_policy": command.DelegationPolicy,
		"task_class": command.TaskClass, "budget": command.Budget,
	}
	requestHash, err := hashCommand("domain.child.create", request)
	if err != nil {
		return domain.DomainAgentChildCreation{}, storageFailure("hash durable child creation", err)
	}
	scopedKey := "domain-child:" + scope.Agent.ID + ":" + command.IdempotencyKey
	var replay domain.DomainAgentChildCreation
	if found, lookupErr := lookupIdempotency(ctx, tx, scopedKey, "domain.child.create", requestHash, &replay); lookupErr != nil {
		return domain.DomainAgentChildCreation{}, lookupErr
	} else if found {
		return replay, nil
	}
	grant, err := queryDomainAgentStaffingGrant(ctx, tx, command.GrantID)
	if err != nil {
		return domain.DomainAgentChildCreation{}, err
	}
	if grant.ProjectID != scope.Project.ID || grant.ManagerAgentID != scope.Agent.ID ||
		grant.ManagerMembershipRevision != scope.Membership.Revision || grant.Status != domain.DomainStaffingGrantActive {
		return domain.DomainAgentChildCreation{}, domainAgentError(CodeDomainStaffingDenied, "staffing grant is not current for this durable manager")
	}
	if grant.ExpiresAt != "" {
		expires, _ := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
		if !expires.After(s.clock().UTC()) {
			return domain.DomainAgentChildCreation{}, domainAgentError(CodeDomainStaffingDenied, "staffing grant has expired")
		}
	}
	if !staffingProfileAllows(grant.Profiles, command.Provider, command.Runtime, command.MaxConcurrency) || !containsExact(grant.TaskClasses, command.TaskClass) {
		return domain.DomainAgentChildCreation{}, domainAgentError(CodeDomainStaffingDenied, "child profile or task class is outside the staffing grant")
	}
	workstreamID, err := resolveDomainWorkstream(ctx, tx, scope.Workspace.ID, scope.Project.ID, command.Workstream)
	if err != nil {
		return domain.DomainAgentChildCreation{}, err
	}
	var descendants, concurrency int
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE descendants(agent_id) AS (
  SELECT agent_id FROM domain_agent_memberships WHERE project_id=? AND parent_agent_id=? AND status='active'
  UNION ALL
  SELECT child.agent_id FROM domain_agent_memberships child JOIN descendants parent ON child.parent_agent_id=parent.agent_id
  WHERE child.project_id=? AND child.status='active'
)
SELECT COUNT(*),COALESCE(SUM(agent.max_concurrency),0) FROM descendants JOIN agents agent ON agent.id=descendants.agent_id`,
		scope.Project.ID, scope.Agent.ID, scope.Project.ID).Scan(&descendants, &concurrency); err != nil {
		return domain.DomainAgentChildCreation{}, storageFailure("count durable descendants", err)
	}
	if descendants+1 > grant.MaxDescendants || concurrency+command.MaxConcurrency > grant.MaxConcurrency {
		return domain.DomainAgentChildCreation{}, domainAgentError(CodeDomainStaffingCapacity, "durable child would exceed the staffing descendant or concurrency ceiling")
	}
	var spent domain.Budget
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(budget_tokens),0),COALESCE(SUM(budget_cost_cents),0),COALESCE(SUM(budget_time_seconds),0)
FROM domain_agent_staffing_allocations WHERE grant_id=?`, grant.ID).Scan(&spent.TokenLimit, &spent.CostCents, &spent.TimeSeconds); err != nil {
		return domain.DomainAgentChildCreation{}, storageFailure("sum staffing allocation budget", err)
	}
	if !staffingBudgetDimensionAllows(grant.Budget.TokenLimit, spent.TokenLimit, command.Budget.TokenLimit) ||
		!staffingBudgetDimensionAllows(grant.Budget.CostCents, spent.CostCents, command.Budget.CostCents) ||
		!staffingBudgetDimensionAllows(grant.Budget.TimeSeconds, spent.TimeSeconds, command.Budget.TimeSeconds) {
		return domain.DomainAgentChildCreation{}, domainAgentError(CodeDomainStaffingCapacity, "durable child would exceed the staffing budget")
	}
	var existing string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM agents WHERE workspace_id=? AND name=?", scope.Workspace.ID, command.Name).Scan(&existing); err == nil {
		return domain.DomainAgentChildCreation{}, &Error{Code: CodeAgentExists, Message: fmt.Sprintf("agent %q already exists as %s", command.Name, existing)}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.DomainAgentChildCreation{}, storageFailure("check durable child name", err)
	}
	var membershipCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM domain_agent_memberships WHERE project_id=?", scope.Project.ID).Scan(&membershipCount); err != nil {
		return domain.DomainAgentChildCreation{}, storageFailure("count domain agents", err)
	}
	if membershipCount >= maximumDomainAgents {
		return domain.DomainAgentChildCreation{}, domainAgentError(CodeDomainStaffingCapacity, "domain agent limit is 1000")
	}
	agentID, err := randomID("agent_")
	if err != nil {
		return domain.DomainAgentChildCreation{}, storageFailure("generate durable child id", err)
	}
	allocationID, err := randomID("staffalloc_")
	if err != nil {
		return domain.DomainAgentChildCreation{}, storageFailure("generate staffing allocation id", err)
	}
	now := s.nowText()
	agent := domain.AgentDefinition{
		ID: agentID, WorkspaceID: scope.Workspace.ID, Name: command.Name, Role: command.Role,
		Provider: command.Provider, Runtime: command.Runtime, Enabled: true, MaxConcurrency: command.MaxConcurrency,
		Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
	}
	membership := domain.DomainAgentMembership{
		ProjectID: scope.Project.ID, AgentID: agent.ID, ParentAgentID: scope.Agent.ID, WorkstreamID: workstreamID,
		OperatingCharter: command.OperatingCharter, DelegationPolicy: command.DelegationPolicy,
		Status: domain.DomainAgentActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
		CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id,workspace_id,name,role,provider,runtime,enabled,max_concurrency,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, agent.ID, agent.WorkspaceID, agent.Name, agent.Role, agent.Provider, agent.Runtime,
		agent.Enabled, agent.MaxConcurrency, agent.Revision, agent.CreatedAt, agent.UpdatedAt, agent.CreatedBy, agent.UpdatedBy); err != nil {
		return domain.DomainAgentChildCreation{}, storageFailure("insert durable child agent", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_agent_memberships(
project_id,agent_id,parent_agent_id,workstream_id,operating_charter,delegation_policy,preferred_entry,status,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,NULLIF(?,''),?,?,0,?,?,?,?,?,?)`, membership.ProjectID, membership.AgentID, membership.ParentAgentID,
		membership.WorkstreamID, membership.OperatingCharter, membership.DelegationPolicy, membership.Status, membership.Revision, membership.CreatedAt, membership.UpdatedAt,
		membership.CreatedBy, membership.UpdatedBy); err != nil {
		return domain.DomainAgentChildCreation{}, storageFailure("insert durable child membership", err)
	}
	agentSequence, err := appendEventForActor(ctx, tx, scope.Workspace.ID, "agent", agent.ID, agent.Revision,
		agentCreated, command.CorrelationID, now, scope.Agent.ID, domain.EventActorIntegration,
		map[string]any{"name": agent.Name, "role": agent.Role, "provider": agent.Provider, "runtime": agent.Runtime, "enabled": agent.Enabled, "max_concurrency": agent.MaxConcurrency})
	if err != nil {
		return domain.DomainAgentChildCreation{}, err
	}
	membershipSequence, err := appendEventForActor(ctx, tx, scope.Workspace.ID, "domain_agent", agent.ID, membership.Revision,
		domainAgentAttached, command.CorrelationID, now, scope.Agent.ID, domain.EventActorIntegration, domainAgentEventData(membership))
	if err != nil {
		return domain.DomainAgentChildCreation{}, err
	}
	allocation := domain.DomainAgentStaffingAllocation{
		ID: allocationID, GrantID: grant.ID, ProjectID: scope.Project.ID, ParentAgentID: scope.Agent.ID,
		ChildAgentID: agent.ID, Provider: agent.Provider, Runtime: agent.Runtime, TaskClass: command.TaskClass,
		Budget: command.Budget, CreatedAt: now, CreatedBy: scope.Agent.ID,
	}
	allocationSequence, err := appendEventForActor(ctx, tx, scope.Workspace.ID, "domain_staffing_allocation", allocation.ID, 1,
		domainChildCreated, command.CorrelationID, now, scope.Agent.ID, domain.EventActorIntegration, map[string]any{
			"grant_id": grant.ID, "project_id": scope.Project.ID, "parent_agent_id": scope.Agent.ID,
			"child_agent_id": agent.ID, "provider": agent.Provider, "runtime": agent.Runtime,
			"task_class": command.TaskClass, "budget": command.Budget,
		})
	if err != nil {
		return domain.DomainAgentChildCreation{}, err
	}
	allocation.EventSequence = allocationSequence
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_agent_staffing_allocations(
id,grant_id,project_id,parent_agent_id,child_agent_id,provider,runtime,task_class,budget_tokens,budget_cost_cents,
budget_time_seconds,request_sha256,event_sequence,created_at,created_by) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		allocation.ID, allocation.GrantID, allocation.ProjectID, allocation.ParentAgentID, allocation.ChildAgentID,
		allocation.Provider, allocation.Runtime, allocation.TaskClass, allocation.Budget.TokenLimit, allocation.Budget.CostCents,
		allocation.Budget.TimeSeconds, requestHash, allocation.EventSequence, allocation.CreatedAt, allocation.CreatedBy); err != nil {
		return domain.DomainAgentChildCreation{}, storageFailure("insert staffing allocation receipt", err)
	}
	result := domain.DomainAgentChildCreation{
		Agent: agent, Membership: membership, Grant: grant, Allocation: allocation,
		EventSequences: []int64{agentSequence, membershipSequence, allocationSequence},
	}
	if err := recordIdempotency(ctx, tx, scopedKey, "domain.child.create", requestHash, result, now); err != nil {
		return domain.DomainAgentChildCreation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.DomainAgentChildCreation{}, storageFailure("commit durable child creation", err)
	}
	return result, nil
}

// A zero budget is the canonical unlimited value. An unlimited child may only
// be created under an unlimited parent dimension; finite grants must retain a
// finite, cumulatively bounded allocation.
func staffingBudgetDimensionAllows(limit, spent, requested int64) bool {
	if limit == 0 {
		return true
	}
	if requested == 0 || requested > limit {
		return false
	}
	return spent <= limit-requested
}

func (s *Store) DomainAgentStaffingGrants(ctx context.Context, workspaceIdentifier, projectIdentifier, managerIdentifier string) ([]domain.DomainAgentStaffingGrant, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageFailure("begin staffing grant list", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return nil, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, strings.TrimSpace(projectIdentifier))
	if err != nil {
		return nil, err
	}
	manager, err := queryAgent(ctx, tx, workspace.ID, strings.TrimSpace(managerIdentifier))
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM domain_agent_staffing_grants WHERE project_id=? AND manager_agent_id=? ORDER BY created_at,id`, project.ID, manager.ID)
	if err != nil {
		return nil, storageFailure("list domain staffing grants", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, storageFailure("scan domain staffing grant id", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, storageFailure("close domain staffing grant list", err)
	}
	result := make([]domain.DomainAgentStaffingGrant, 0, len(ids))
	for _, id := range ids {
		grant, err := queryDomainAgentStaffingGrant(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if grant.Status == domain.DomainStaffingGrantActive && grant.ExpiresAt != "" {
			expires, parseErr := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
			if parseErr == nil && !expires.After(s.clock().UTC()) {
				// Expiry is derived at the read boundary. Merely observing a grant
				// never mutates canonical state, while child creation checks the same
				// clock inside its write transaction.
				grant.Status = domain.DomainStaffingGrantExpired
			}
		}
		result = append(result, grant)
	}
	return result, nil
}

func queryDomainAgentStaffingGrant(ctx context.Context, database messageQueryContext, id string) (domain.DomainAgentStaffingGrant, error) {
	var grant domain.DomainAgentStaffingGrant
	err := database.QueryRowContext(ctx, `SELECT id,project_id,manager_agent_id,manager_membership_revision,max_descendants,max_concurrency,
budget_tokens,budget_cost_cents,budget_time_seconds,COALESCE(expires_at,''),status,revision,created_at,updated_at,created_by,updated_by
FROM domain_agent_staffing_grants WHERE id=?`, strings.TrimSpace(id)).Scan(&grant.ID, &grant.ProjectID, &grant.ManagerAgentID,
		&grant.ManagerMembershipRevision, &grant.MaxDescendants, &grant.MaxConcurrency, &grant.Budget.TokenLimit,
		&grant.Budget.CostCents, &grant.Budget.TimeSeconds, &grant.ExpiresAt, &grant.Status, &grant.Revision,
		&grant.CreatedAt, &grant.UpdatedAt, &grant.CreatedBy, &grant.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return grant, domainAgentError(CodeDomainStaffingGrantNotFound, fmt.Sprintf("staffing grant %q was not found", id))
	}
	if err != nil {
		return grant, storageFailure("query domain staffing grant", err)
	}
	rows, err := database.QueryContext(ctx, `SELECT provider,runtime,max_concurrency FROM domain_agent_staffing_profiles WHERE grant_id=? ORDER BY provider,runtime`, grant.ID)
	if err != nil {
		return grant, storageFailure("query domain staffing profiles", err)
	}
	for rows.Next() {
		var profile domain.DomainAgentStaffingProfile
		if err := rows.Scan(&profile.Provider, &profile.Runtime, &profile.MaxConcurrency); err != nil {
			rows.Close()
			return grant, storageFailure("scan domain staffing profile", err)
		}
		grant.Profiles = append(grant.Profiles, profile)
	}
	if err := rows.Close(); err != nil {
		return grant, storageFailure("close domain staffing profiles", err)
	}
	classRows, err := database.QueryContext(ctx, `SELECT task_class FROM domain_agent_staffing_task_classes WHERE grant_id=? ORDER BY task_class`, grant.ID)
	if err != nil {
		return grant, storageFailure("query domain staffing task classes", err)
	}
	for classRows.Next() {
		var taskClass string
		if err := classRows.Scan(&taskClass); err != nil {
			classRows.Close()
			return grant, storageFailure("scan domain staffing task class", err)
		}
		grant.TaskClasses = append(grant.TaskClasses, taskClass)
	}
	if err := classRows.Close(); err != nil {
		return grant, storageFailure("close domain staffing task classes", err)
	}
	return grant, nil
}

func normalizeDomainStaffingProfiles(values []domain.DomainAgentStaffingProfile) ([]domain.DomainAgentStaffingProfile, error) {
	result := append([]domain.DomainAgentStaffingProfile(nil), values...)
	seen := make(map[string]bool, len(result))
	for index := range result {
		result[index].Provider = strings.TrimSpace(result[index].Provider)
		result[index].Runtime = strings.TrimSpace(result[index].Runtime)
		key := result[index].Provider + "\x00" + result[index].Runtime
		if !validShortText(result[index].Provider) || !validShortText(result[index].Runtime) ||
			result[index].MaxConcurrency < 1 || result[index].MaxConcurrency > 100 || seen[key] {
			return nil, domainAgentError(CodeInvalidDomainStaffingGrant, "staffing profiles must be distinct bounded provider/runtime pairs with concurrency from 1 to 100")
		}
		seen[key] = true
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Provider != result[j].Provider {
			return result[i].Provider < result[j].Provider
		}
		return result[i].Runtime < result[j].Runtime
	})
	return result, nil
}

func normalizeDomainTaskClasses(values []string) ([]string, error) {
	result := make([]string, len(values))
	seen := make(map[string]bool, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		if !workspaceNamePattern.MatchString(value) || seen[value] {
			return nil, domainAgentError(CodeInvalidDomainStaffingGrant, "staffing task classes must be distinct lowercase identifiers")
		}
		seen[value], result[index] = true, value
	}
	sort.Strings(result)
	return result, nil
}

func staffingProfileAllows(profiles []domain.DomainAgentStaffingProfile, provider, runtime string, concurrency int) bool {
	for _, profile := range profiles {
		if profile.Provider == provider && profile.Runtime == runtime && concurrency <= profile.MaxConcurrency {
			return true
		}
	}
	return false
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func domainStaffingGrantEventData(grant domain.DomainAgentStaffingGrant) map[string]any {
	return map[string]any{
		"project_id": grant.ProjectID, "manager_agent_id": grant.ManagerAgentID,
		"manager_membership_revision": grant.ManagerMembershipRevision, "profiles": grant.Profiles,
		"task_classes": grant.TaskClasses, "max_descendants": grant.MaxDescendants,
		"max_concurrency": grant.MaxConcurrency, "budget": grant.Budget,
		"expires_at": grant.ExpiresAt, "status": grant.Status,
	}
}
