package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"crewfold/internal/domain"
)

const (
	domainAgentAttached = "domain.agent_attached"
	domainAgentUpdated  = "domain.agent_updated"
	maximumDomainAgents = 1000
)

// CreateDomainAgent records the owner-visible durable definition and its
// domain membership in one transaction. It is intentionally separate from
// provider-originated child creation, which requires an exact staffing grant.
func (s *Store) CreateDomainAgent(ctx context.Context, command CreateDomainAgentCommand) (domain.DomainAgentCreation, error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	projectIdentifier := strings.TrimSpace(command.ProjectIdentifier)
	name, role := strings.TrimSpace(command.Name), strings.TrimSpace(command.Role)
	provider, runtimeName := strings.TrimSpace(command.Provider), strings.TrimSpace(command.Runtime)
	parentIdentifier := strings.TrimSpace(command.ParentAgentIdentifier)
	workstreamIdentifier := strings.TrimSpace(command.WorkstreamIdentifier)
	operatingCharter := strings.TrimSpace(command.OperatingCharter)
	delegationPolicy := strings.TrimSpace(command.DelegationPolicy)
	key, correlationID := strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if workspaceIdentifier == "" || projectIdentifier == "" || !workspaceNamePattern.MatchString(name) ||
		!validShortText(role) || !validShortText(provider) || !validShortText(runtimeName) ||
		!validDomainAgentCharter(operatingCharter) || !validDomainAgentDelegationPolicy(delegationPolicy) ||
		command.MaxConcurrency < 1 || command.MaxConcurrency > 100 {
		return domain.DomainAgentCreation{}, domainAgentError(CodeInvalidDomainAgent, "domain agent creation requires exact scope, lowercase name, role, provider, runtime, and max concurrency from 1 to 100")
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidDomainAgent); err != nil {
		return domain.DomainAgentCreation{}, err
	}
	requestHash, err := hashCommand("domain.agent.create", map[string]any{
		"workspace": workspaceIdentifier, "project": projectIdentifier, "name": name, "role": role,
		"provider": provider, "runtime": runtimeName, "max_concurrency": command.MaxConcurrency,
		"parent_agent": parentIdentifier, "workstream": workstreamIdentifier, "preferred_entry": command.PreferredEntry,
		"operating_charter": operatingCharter, "delegation_policy": delegationPolicy,
	})
	if err != nil {
		return domain.DomainAgentCreation{}, storageFailure("hash domain agent creation", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.DomainAgentCreation{}, storageFailure("begin domain agent creation", err)
	}
	defer tx.Rollback()
	var replay domain.DomainAgentCreation
	if found, lookupErr := lookupIdempotency(ctx, tx, key, "domain.agent.create", requestHash, &replay); lookupErr != nil {
		return domain.DomainAgentCreation{}, lookupErr
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return domain.DomainAgentCreation{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, projectIdentifier)
	if err != nil {
		return domain.DomainAgentCreation{}, err
	}
	var existingID string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM agents WHERE workspace_id=? AND name=?", workspace.ID, name).Scan(&existingID); err == nil {
		return domain.DomainAgentCreation{}, domainAgentError(CodeDomainAgentExists, fmt.Sprintf("agent %q already exists as %s", name, existingID))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.DomainAgentCreation{}, storageFailure("check domain agent name", err)
	}
	var membershipCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM domain_agent_memberships WHERE project_id=?", project.ID).Scan(&membershipCount); err != nil {
		return domain.DomainAgentCreation{}, storageFailure("count domain agents", err)
	}
	if membershipCount >= maximumDomainAgents {
		return domain.DomainAgentCreation{}, domainAgentError(CodeInvalidDomainAgent, "domain agent limit is 1000")
	}
	id, err := randomID("agent_")
	if err != nil {
		return domain.DomainAgentCreation{}, storageFailure("generate domain agent id", err)
	}
	parentID, err := resolveDomainParent(ctx, tx, workspace.ID, project.ID, parentIdentifier, id)
	if err != nil {
		return domain.DomainAgentCreation{}, err
	}
	workstreamID, err := resolveDomainWorkstream(ctx, tx, workspace.ID, project.ID, workstreamIdentifier)
	if err != nil {
		return domain.DomainAgentCreation{}, err
	}
	if command.PreferredEntry {
		if err := requirePreferredDomainEntryAvailable(ctx, tx, project.ID, id); err != nil {
			return domain.DomainAgentCreation{}, err
		}
	}
	now := s.nowText()
	agent := domain.AgentDefinition{ID: id, WorkspaceID: workspace.ID, Name: name, Role: role, Provider: provider, Runtime: runtimeName, Enabled: true, MaxConcurrency: command.MaxConcurrency, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id,workspace_id,name,role,provider,runtime,enabled,max_concurrency,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, agent.ID, agent.WorkspaceID, agent.Name, agent.Role, agent.Provider, agent.Runtime, agent.Enabled, agent.MaxConcurrency, agent.Revision, agent.CreatedAt, agent.UpdatedAt, agent.CreatedBy, agent.UpdatedBy); err != nil {
		return domain.DomainAgentCreation{}, storageFailure("insert domain agent definition", err)
	}
	membership := domain.DomainAgentMembership{ProjectID: project.ID, AgentID: agent.ID, ParentAgentID: parentID, WorkstreamID: workstreamID, OperatingCharter: operatingCharter, DelegationPolicy: delegationPolicy, PreferredEntry: command.PreferredEntry, Status: domain.DomainAgentActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_agent_memberships(project_id,agent_id,parent_agent_id,workstream_id,operating_charter,delegation_policy,preferred_entry,status,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,NULLIF(?,''),NULLIF(?,''),?,?,?,?,?,?,?,?,?)`, membership.ProjectID, membership.AgentID, membership.ParentAgentID, membership.WorkstreamID, membership.OperatingCharter, membership.DelegationPolicy, membership.PreferredEntry, membership.Status, membership.Revision, membership.CreatedAt, membership.UpdatedAt, membership.CreatedBy, membership.UpdatedBy); err != nil {
		return domain.DomainAgentCreation{}, storageFailure("insert domain agent membership", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return domain.DomainAgentCreation{}, err
	}
	agentSequence, err := appendEvent(ctx, tx, workspace.ID, "agent", agent.ID, agent.Revision, agentCreated, correlationID, now, map[string]any{"name": agent.Name, "role": agent.Role, "provider": agent.Provider, "runtime": agent.Runtime, "enabled": agent.Enabled, "max_concurrency": agent.MaxConcurrency})
	if err != nil {
		return domain.DomainAgentCreation{}, err
	}
	membershipSequence, err := appendEvent(ctx, tx, workspace.ID, "domain_agent", agent.ID, membership.Revision, domainAgentAttached, correlationID, now, domainAgentEventData(membership))
	if err != nil {
		return domain.DomainAgentCreation{}, err
	}
	result := domain.DomainAgentCreation{Agent: domain.DomainAgent{Definition: agent, Membership: membership}, EventSequences: []int64{agentSequence, membershipSequence}}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return domain.DomainAgentCreation{}, err
	}
	if err := recordIdempotency(ctx, tx, key, "domain.agent.create", requestHash, result, now); err != nil {
		return domain.DomainAgentCreation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.DomainAgentCreation{}, storageFailure("commit domain agent creation", err)
	}
	return result, nil
}

func (s *Store) AttachDomainAgent(ctx context.Context, command AttachDomainAgentCommand) (MutationResult[domain.DomainAgentMembership], error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	projectIdentifier := strings.TrimSpace(command.ProjectIdentifier)
	agentIdentifier := strings.TrimSpace(command.AgentIdentifier)
	parentIdentifier := strings.TrimSpace(command.ParentAgentIdentifier)
	workstreamIdentifier := strings.TrimSpace(command.WorkstreamIdentifier)
	operatingCharter := strings.TrimSpace(command.OperatingCharter)
	delegationPolicy := strings.TrimSpace(command.DelegationPolicy)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	if workspaceIdentifier == "" || projectIdentifier == "" || agentIdentifier == "" || !validDomainAgentCharter(operatingCharter) || !validDomainAgentDelegationPolicy(delegationPolicy) {
		return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeInvalidDomainAgent, "domain agent attachment requires workspace, domain, and agent")
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidDomainAgent); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	requestHash, err := hashCommand("domain.agent.attach", map[string]any{
		"workspace": workspaceIdentifier, "project": projectIdentifier, "agent": agentIdentifier,
		"parent_agent": parentIdentifier, "workstream": workstreamIdentifier, "preferred_entry": command.PreferredEntry,
		"operating_charter": operatingCharter, "delegation_policy": delegationPolicy,
	})
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, storageFailure("hash domain agent attachment", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, storageFailure("begin domain agent attachment", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.DomainAgentMembership]
	if found, err := lookupIdempotency(ctx, tx, key, "domain.agent.attach", requestHash, &replay); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, projectIdentifier)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, agentIdentifier)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	if _, err := queryDomainAgentMembership(ctx, tx, "", agent.ID); err == nil {
		return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeDomainAgentExists, fmt.Sprintf("agent %q is already attached to a domain", agent.Name))
	} else if ErrorCode(err) != CodeDomainAgentNotFound {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	var membershipCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM domain_agent_memberships WHERE project_id=?", project.ID).Scan(&membershipCount); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, storageFailure("count domain agents", err)
	}
	if membershipCount >= maximumDomainAgents {
		return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeInvalidDomainAgent, "domain agent limit is 1000")
	}
	parentID, err := resolveDomainParent(ctx, tx, workspace.ID, project.ID, parentIdentifier, agent.ID)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	workstreamID, err := resolveDomainWorkstream(ctx, tx, workspace.ID, project.ID, workstreamIdentifier)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	if command.PreferredEntry {
		if err := requirePreferredDomainEntryAvailable(ctx, tx, project.ID, agent.ID); err != nil {
			return MutationResult[domain.DomainAgentMembership]{}, err
		}
	}
	now := s.nowText()
	membership := domain.DomainAgentMembership{
		ProjectID: project.ID, AgentID: agent.ID, ParentAgentID: parentID, WorkstreamID: workstreamID,
		OperatingCharter: operatingCharter, DelegationPolicy: delegationPolicy,
		PreferredEntry: command.PreferredEntry, Status: domain.DomainAgentActive, Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_agent_memberships(
project_id,agent_id,parent_agent_id,workstream_id,operating_charter,delegation_policy,preferred_entry,status,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,NULLIF(?,''),NULLIF(?,''),?,?,?,?,?,?,?,?,?)`, membership.ProjectID, membership.AgentID,
		membership.ParentAgentID, membership.WorkstreamID, membership.OperatingCharter, membership.DelegationPolicy, membership.PreferredEntry, membership.Status,
		membership.Revision, membership.CreatedAt, membership.UpdatedAt, membership.CreatedBy, membership.UpdatedBy); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, storageFailure("insert domain agent membership", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "domain_agent", agent.ID, membership.Revision,
		domainAgentAttached, correlationID, now, domainAgentEventData(membership))
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	result := MutationResult[domain.DomainAgentMembership]{Value: membership, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	if err := recordIdempotency(ctx, tx, key, "domain.agent.attach", requestHash, result, now); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, storageFailure("commit domain agent attachment", err)
	}
	return result, nil
}

func (s *Store) UpdateDomainAgent(ctx context.Context, command UpdateDomainAgentCommand) (MutationResult[domain.DomainAgentMembership], error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	projectIdentifier := strings.TrimSpace(command.ProjectIdentifier)
	agentIdentifier := strings.TrimSpace(command.AgentIdentifier)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	if workspaceIdentifier == "" || projectIdentifier == "" || agentIdentifier == "" || command.ExpectedRevision < 1 ||
		(command.ParentAgentIdentifier == nil && command.WorkstreamIdentifier == nil && command.OperatingCharter == nil && command.DelegationPolicy == nil && command.PreferredEntry == nil && command.Status == nil) {
		return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeInvalidDomainAgent, "domain agent update requires scope, agent, expected revision, and a changed field")
	}
	if command.Status != nil {
		status := strings.TrimSpace(*command.Status)
		if status != domain.DomainAgentActive && status != domain.DomainAgentRetired {
			return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeInvalidDomainAgent, "domain agent status is invalid")
		}
	}
	if command.OperatingCharter != nil && !validDomainAgentCharter(strings.TrimSpace(*command.OperatingCharter)) {
		return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeInvalidDomainAgent, "domain agent operating charter is invalid")
	}
	if command.DelegationPolicy != nil && !validDomainAgentDelegationPolicy(strings.TrimSpace(*command.DelegationPolicy)) {
		return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeInvalidDomainAgent, "domain agent delegation policy is invalid")
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidDomainAgent); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	requestHash, err := hashCommand("domain.agent.update", command)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, storageFailure("hash domain agent update", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, storageFailure("begin domain agent update", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.DomainAgentMembership]
	if found, err := lookupIdempotency(ctx, tx, key, "domain.agent.update", requestHash, &replay); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, projectIdentifier)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, agentIdentifier)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	membership, err := queryDomainAgentMembership(ctx, tx, project.ID, agent.ID)
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	if membership.Revision != command.ExpectedRevision {
		return MutationResult[domain.DomainAgentMembership]{}, revisionConflict("domain_agent", agent.ID, command.ExpectedRevision, membership.Revision)
	}
	if command.ParentAgentIdentifier != nil {
		membership.ParentAgentID, err = resolveDomainParent(ctx, tx, workspace.ID, project.ID, strings.TrimSpace(*command.ParentAgentIdentifier), agent.ID)
		if err != nil {
			return MutationResult[domain.DomainAgentMembership]{}, err
		}
		if membership.ParentAgentID != "" {
			cycle, err := domainParentReaches(ctx, tx, project.ID, membership.ParentAgentID, agent.ID)
			if err != nil {
				return MutationResult[domain.DomainAgentMembership]{}, err
			}
			if cycle {
				return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeDomainAgentCycle, "domain agent parent would create an ancestry cycle")
			}
		}
	}
	if command.WorkstreamIdentifier != nil {
		membership.WorkstreamID, err = resolveDomainWorkstream(ctx, tx, workspace.ID, project.ID, strings.TrimSpace(*command.WorkstreamIdentifier))
		if err != nil {
			return MutationResult[domain.DomainAgentMembership]{}, err
		}
	}
	if command.OperatingCharter != nil {
		membership.OperatingCharter = strings.TrimSpace(*command.OperatingCharter)
	}
	if command.DelegationPolicy != nil {
		membership.DelegationPolicy = strings.TrimSpace(*command.DelegationPolicy)
	}
	if command.PreferredEntry != nil {
		membership.PreferredEntry = *command.PreferredEntry
	}
	if command.Status != nil {
		membership.Status = strings.TrimSpace(*command.Status)
		if membership.Status == domain.DomainAgentRetired {
			membership.PreferredEntry = false
			var children, assignments, runs, staffingGrants int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM domain_agent_memberships
WHERE project_id=? AND parent_agent_id=? AND status='active'`, project.ID, agent.ID).Scan(&children); err != nil {
				return MutationResult[domain.DomainAgentMembership]{}, storageFailure("count active domain children", err)
			}
			if children != 0 {
				return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeInvalidDomainAgent, "domain agent with active children cannot retire")
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_assignments assignment
JOIN tasks task ON task.id=assignment.task_id
WHERE assignment.agent_id=? AND assignment.status='active'
  AND task.status NOT IN ('completed','failed','cancelled')`, agent.ID).Scan(&assignments); err != nil {
				return MutationResult[domain.DomainAgentMembership]{}, storageFailure("count active domain agent assignments", err)
			}
			if assignments != 0 {
				return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeInvalidDomainAgent, "domain agent with active assignments cannot retire")
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs
WHERE agent_id=? AND status IN ('requested','starting','active','blocked','stopping','lost')`, agent.ID).Scan(&runs); err != nil {
				return MutationResult[domain.DomainAgentMembership]{}, storageFailure("count unresolved domain agent runs", err)
			}
			if runs != 0 {
				return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeInvalidDomainAgent, "domain agent with live or unresolved runs cannot retire")
			}
			grantRows, err := tx.QueryContext(ctx, `SELECT COALESCE(expires_at,'') FROM domain_agent_staffing_grants
WHERE project_id=? AND manager_agent_id=? AND status='active'`, project.ID, agent.ID)
			if err != nil {
				return MutationResult[domain.DomainAgentMembership]{}, storageFailure("query active domain agent staffing grants", err)
			}
			for grantRows.Next() {
				var expiresAt string
				if err := grantRows.Scan(&expiresAt); err != nil {
					grantRows.Close()
					return MutationResult[domain.DomainAgentMembership]{}, storageFailure("scan active domain agent staffing grant", err)
				}
				if expiresAt == "" {
					staffingGrants++
					continue
				}
				expires, err := time.Parse(time.RFC3339Nano, expiresAt)
				if err != nil {
					grantRows.Close()
					return MutationResult[domain.DomainAgentMembership]{}, storageFailure("parse active domain agent staffing grant expiry", err)
				}
				if expires.After(s.clock().UTC()) {
					staffingGrants++
				}
			}
			if err := grantRows.Close(); err != nil {
				return MutationResult[domain.DomainAgentMembership]{}, storageFailure("close active domain agent staffing grants", err)
			}
			if staffingGrants != 0 {
				return MutationResult[domain.DomainAgentMembership]{}, domainAgentError(CodeInvalidDomainAgent, "domain agent with active staffing grants cannot retire")
			}
		}
	}
	if membership.PreferredEntry && membership.Status == domain.DomainAgentActive {
		if err := requirePreferredDomainEntryAvailable(ctx, tx, project.ID, agent.ID); err != nil {
			return MutationResult[domain.DomainAgentMembership]{}, err
		}
	}
	now := s.nowText()
	membership.Revision++
	membership.UpdatedAt, membership.UpdatedBy = now, localOwnerActorID
	if _, err := tx.ExecContext(ctx, `UPDATE domain_agent_memberships
SET parent_agent_id=NULLIF(?,''),workstream_id=NULLIF(?,''),operating_charter=?,delegation_policy=?,preferred_entry=?,status=?,revision=?,updated_at=?,updated_by=?
WHERE project_id=? AND agent_id=?`, membership.ParentAgentID, membership.WorkstreamID, membership.OperatingCharter, membership.DelegationPolicy, membership.PreferredEntry,
		membership.Status, membership.Revision, membership.UpdatedAt, membership.UpdatedBy,
		membership.ProjectID, membership.AgentID); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, storageFailure("update domain agent membership", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "domain_agent", agent.ID, membership.Revision,
		domainAgentUpdated, correlationID, now, domainAgentEventData(membership))
	if err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	result := MutationResult[domain.DomainAgentMembership]{Value: membership, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	if err := recordIdempotency(ctx, tx, key, "domain.agent.update", requestHash, result, now); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.DomainAgentMembership]{}, storageFailure("commit domain agent update", err)
	}
	return result, nil
}

func (s *Store) DomainAgentTree(ctx context.Context, workspaceIdentifier, projectIdentifier string) (domain.DomainAgentTree, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.DomainAgentTree{}, err
	}
	project, err := queryProject(ctx, s.db, workspace.ID, strings.TrimSpace(projectIdentifier))
	if err != nil {
		return domain.DomainAgentTree{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
a.id,a.workspace_id,a.name,a.role,a.provider,a.runtime,a.enabled,a.max_concurrency,a.revision,a.created_at,a.updated_at,a.created_by,a.updated_by,
m.project_id,m.agent_id,COALESCE(m.parent_agent_id,''),COALESCE(m.workstream_id,''),m.operating_charter,m.delegation_policy,m.preferred_entry,m.status,m.revision,m.created_at,m.updated_at,m.created_by,m.updated_by
FROM domain_agent_memberships m JOIN agents a ON a.id=m.agent_id
WHERE m.project_id=? ORDER BY m.status='active' DESC,m.preferred_entry DESC,COALESCE(m.parent_agent_id,''),a.name,a.id
LIMIT ?`, project.ID, maximumDomainAgents+1)
	if err != nil {
		return domain.DomainAgentTree{}, storageFailure("list domain agents", err)
	}
	defer rows.Close()
	result := domain.DomainAgentTree{ProjectID: project.ID, Agents: make([]domain.DomainAgent, 0)}
	for rows.Next() {
		var item domain.DomainAgent
		if err := scanDomainAgent(rows, &item); err != nil {
			return domain.DomainAgentTree{}, storageFailure("scan domain agent", err)
		}
		result.Agents = append(result.Agents, item)
	}
	if err := rows.Err(); err != nil {
		return domain.DomainAgentTree{}, storageFailure("iterate domain agents", err)
	}
	if len(result.Agents) > maximumDomainAgents {
		return domain.DomainAgentTree{}, domainAgentError(CodeInvalidDomainAgent, "domain agent tree exceeds the current 1000-agent bound")
	}
	return result, nil
}

func resolveDomainParent(ctx context.Context, tx *sql.Tx, workspaceID, projectID, identifier, childAgentID string) (string, error) {
	if identifier == "" {
		return "", nil
	}
	parent, err := queryAgent(ctx, tx, workspaceID, identifier)
	if err != nil {
		return "", err
	}
	if parent.ID == childAgentID {
		return "", domainAgentError(CodeDomainAgentCycle, "domain agent cannot manage itself")
	}
	membership, err := queryDomainAgentMembership(ctx, tx, projectID, parent.ID)
	if err != nil {
		return "", err
	}
	if membership.Status != domain.DomainAgentActive {
		return "", domainAgentError(CodeInvalidDomainAgent, "domain agent parent must be active")
	}
	return parent.ID, nil
}

func resolveDomainWorkstream(ctx context.Context, tx *sql.Tx, workspaceID, projectID, identifier string) (string, error) {
	if identifier == "" {
		return "", nil
	}
	objective, err := queryObjective(ctx, tx, workspaceID, identifier)
	if err != nil {
		return "", err
	}
	if objective.ProjectID != projectID {
		return "", domainAgentError(CodeInvalidDomainAgent, "domain agent workstream must belong to the selected domain")
	}
	return objective.ID, nil
}

func requirePreferredDomainEntryAvailable(ctx context.Context, tx *sql.Tx, projectID, agentID string) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT agent_id FROM domain_agent_memberships
WHERE project_id=? AND preferred_entry=1 AND status='active' AND agent_id<>? LIMIT 1`, projectID, agentID).Scan(&existing)
	if err == nil {
		return domainAgentError(CodeInvalidDomainAgent, "domain already has a preferred entry agent")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storageFailure("query preferred domain entry", err)
	}
	return nil
}

func domainParentReaches(ctx context.Context, tx *sql.Tx, projectID, fromAgentID, targetAgentID string) (bool, error) {
	var found int
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(agent_id,parent_agent_id) AS (
  SELECT agent_id,parent_agent_id FROM domain_agent_memberships WHERE project_id=? AND agent_id=?
  UNION ALL
  SELECT parent.agent_id,parent.parent_agent_id FROM domain_agent_memberships parent
  JOIN ancestors child ON child.parent_agent_id=parent.agent_id
  WHERE parent.project_id=?
)
SELECT 1 FROM ancestors WHERE agent_id=? LIMIT 1`, projectID, fromAgentID, projectID, targetAgentID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storageFailure("inspect domain agent ancestry", err)
	}
	return true, nil
}

func queryDomainAgentMembership(ctx context.Context, database queryRower, projectID, agentID string) (domain.DomainAgentMembership, error) {
	query := `SELECT project_id,agent_id,COALESCE(parent_agent_id,''),COALESCE(workstream_id,''),operating_charter,delegation_policy,preferred_entry,status,revision,created_at,updated_at,created_by,updated_by
FROM domain_agent_memberships WHERE agent_id=?`
	arguments := []any{agentID}
	if projectID != "" {
		query += " AND project_id=?"
		arguments = append(arguments, projectID)
	}
	var membership domain.DomainAgentMembership
	var preferred int
	err := database.QueryRowContext(ctx, query, arguments...).Scan(&membership.ProjectID, &membership.AgentID,
		&membership.ParentAgentID, &membership.WorkstreamID, &membership.OperatingCharter, &membership.DelegationPolicy, &preferred, &membership.Status, &membership.Revision,
		&membership.CreatedAt, &membership.UpdatedAt, &membership.CreatedBy, &membership.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DomainAgentMembership{}, domainAgentError(CodeDomainAgentNotFound, fmt.Sprintf("agent %q is not attached to the selected domain", agentID))
	}
	if err != nil {
		return domain.DomainAgentMembership{}, storageFailure("query domain agent membership", err)
	}
	membership.PreferredEntry = preferred == 1
	return membership, nil
}

func scanDomainAgent(row rowScanner, item *domain.DomainAgent) error {
	var preferred int
	err := row.Scan(&item.Definition.ID, &item.Definition.WorkspaceID, &item.Definition.Name, &item.Definition.Role,
		&item.Definition.Provider, &item.Definition.Runtime, &item.Definition.Enabled, &item.Definition.MaxConcurrency,
		&item.Definition.Revision, &item.Definition.CreatedAt, &item.Definition.UpdatedAt, &item.Definition.CreatedBy, &item.Definition.UpdatedBy,
		&item.Membership.ProjectID, &item.Membership.AgentID, &item.Membership.ParentAgentID, &item.Membership.WorkstreamID, &item.Membership.OperatingCharter, &item.Membership.DelegationPolicy,
		&preferred, &item.Membership.Status, &item.Membership.Revision, &item.Membership.CreatedAt,
		&item.Membership.UpdatedAt, &item.Membership.CreatedBy, &item.Membership.UpdatedBy)
	item.Membership.PreferredEntry = preferred == 1
	return err
}

func domainAgentEventData(value domain.DomainAgentMembership) map[string]any {
	return map[string]any{
		"project_id": value.ProjectID, "agent_id": value.AgentID, "parent_agent_id": value.ParentAgentID,
		"workstream_id": value.WorkstreamID, "operating_charter": value.OperatingCharter, "delegation_policy": value.DelegationPolicy, "preferred_entry": value.PreferredEntry, "status": value.Status,
	}
}

func validDomainAgentDelegationPolicy(value string) bool {
	return value == domain.DomainAgentHandsOn || value == domain.DomainAgentAdaptive || value == domain.DomainAgentDelegationFirst
}

func validDomainAgentCharter(value string) bool {
	if value == "" || len(value) > 8192 || strings.TrimSpace(value) != value || strings.ContainsRune(value, 0) {
		return false
	}
	for _, character := range value {
		if character < 0x20 && character != '\n' && character != '\t' {
			return false
		}
	}
	return true
}

func domainAgentError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}
