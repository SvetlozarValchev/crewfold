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
)

const (
	domainWorkProposalSubmitted = "domain.work_proposal_submitted"
	domainWorkProposalAccepted  = "domain.work_proposal_accepted"
	domainWorkProposalRejected  = "domain.work_proposal_rejected"
	domainWorkProposalStale     = "domain.work_proposal_stale"
)

func (s *Store) SubmitDomainWorkProposal(ctx context.Context, command SubmitDomainWorkProposalCommand) (MutationResult[domain.DomainWorkProposal], error) {
	command.ThreadID = strings.TrimSpace(command.ThreadID)
	command.StaffingGrantID = strings.TrimSpace(command.StaffingGrantID)
	command.Summary = strings.TrimSpace(command.Summary)
	normalizeDomainWorkContent(&command.Content)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.ThreadID == "" || command.StaffingGrantID == "" || !validManagerText(command.Summary, 2048) {
		return MutationResult[domain.DomainWorkProposal]{}, domainAgentError(CodeDomainStaffingDenied, "work proposal requires a durable session, staffing grant, summary, and typed graph")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeDomainStaffingDenied); err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, err
	}
	contentJSON, contentHash, err := canonicalContent(command.Content)
	if err != nil || len(contentJSON) > 131072 {
		return MutationResult[domain.DomainWorkProposal]{}, domainAgentError(CodeDomainStaffingDenied, "work proposal content is not a bounded canonical object")
	}
	requestHash, err := hashCommand("domain.work-proposal.submit", map[string]any{"thread_id": command.ThreadID, "staffing_grant_id": command.StaffingGrantID, "summary": command.Summary, "content_sha256": contentHash})
	if err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, storageFailure("hash domain work proposal", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, storageFailure("begin domain work proposal", err)
	}
	defer tx.Rollback()
	scope, err := s.domainAgentSessionScopeInTransaction(ctx, tx, command.ThreadID)
	if err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, err
	}
	scopedKey := "domain-work-proposal:" + scope.Agent.ID + ":" + command.IdempotencyKey
	var replay MutationResult[domain.DomainWorkProposal]
	if found, err := lookupIdempotency(ctx, tx, scopedKey, "domain.work-proposal.submit", requestHash, &replay); err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, err
	} else if found {
		return replay, nil
	}
	grant, err := queryDomainAgentStaffingGrant(ctx, tx, command.StaffingGrantID)
	if err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, err
	}
	if err := validateDomainWorkContent(ctx, tx, s.clock().UTC(), scope, grant, command.Content); err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, err
	}
	var highWater int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?", scope.Workspace.ID).Scan(&highWater); err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, storageFailure("capture work proposal event cut", err)
	}
	id, err := randomID("workprop_")
	if err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, storageFailure("generate domain work proposal", err)
	}
	now, actor := s.nowText(), "agent:"+scope.Agent.ID
	proposal := domain.DomainWorkProposal{ID: id, WorkspaceID: scope.Workspace.ID, ProjectID: scope.Project.ID, SourceAgentID: scope.Agent.ID, SourceThreadID: command.ThreadID, StaffingGrantID: grant.ID, StaffingGrantRevision: grant.Revision, Summary: command.Summary, AsOfEventSequence: highWater, Content: command.Content, ContentSHA256: contentHash, Status: domain.DomainWorkProposalPending, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: actor, UpdatedBy: actor}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_work_proposals(id,workspace_id,project_id,source_agent_id,source_thread_id,staffing_grant_id,staffing_grant_revision,summary,as_of_event_sequence,content_json,content_sha256,status,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,?,?,?,?,?,?,?,?,?,'pending',1,?,?,?,?)`, proposal.ID, proposal.WorkspaceID, proposal.ProjectID, proposal.SourceAgentID, proposal.SourceThreadID, proposal.StaffingGrantID, proposal.StaffingGrantRevision, proposal.Summary, proposal.AsOfEventSequence, string(contentJSON), proposal.ContentSHA256, proposal.CreatedAt, proposal.UpdatedAt, proposal.CreatedBy, proposal.UpdatedBy); err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, storageFailure("insert domain work proposal", err)
	}
	sequence, err := appendEventForActor(ctx, tx, proposal.WorkspaceID, "domain_work_proposal", proposal.ID, 1, domainWorkProposalSubmitted, command.CorrelationID, now, scope.Agent.ID, domain.EventActorIntegration, map[string]any{"project_id": proposal.ProjectID, "source_agent_id": proposal.SourceAgentID, "staffing_grant_id": proposal.StaffingGrantID, "content_sha256": proposal.ContentSHA256, "task_count": len(proposal.Content.Tasks), "status": proposal.Status})
	if err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, err
	}
	result := MutationResult[domain.DomainWorkProposal]{Value: proposal, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, scopedKey, "domain.work-proposal.submit", requestHash, result, now); err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.DomainWorkProposal]{}, storageFailure("commit domain work proposal", err)
	}
	return result, nil
}

func validateDomainWorkContent(ctx context.Context, tx *sql.Tx, now time.Time, scope domain.DomainAgentSessionScope, grant domain.DomainAgentStaffingGrant, content domain.DomainWorkProposalContent) error {
	if grant.ProjectID != scope.Project.ID || grant.ManagerAgentID != scope.Agent.ID || grant.ManagerMembershipRevision != scope.Membership.Revision || grant.Status != domain.DomainStaffingGrantActive {
		return domainAgentError(CodeDomainStaffingDenied, "work proposal staffing grant is not current for this durable agent")
	}
	if grant.ExpiresAt != "" {
		expiry, _ := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
		if !expiry.After(now) {
			return domainAgentError(CodeDomainStaffingDenied, "work proposal staffing grant has expired")
		}
	}
	if !validTitle(strings.TrimSpace(content.ObjectiveTitle)) || !validBudget(content.ObjectiveBudget) || len(content.Tasks) < 1 || len(content.Tasks) > 16 {
		return domainAgentError(CodeDomainStaffingDenied, "work proposal must contain one bounded workstream and 1 to 16 tasks")
	}
	keys := make(map[string]domain.OwnerPlanTask, len(content.Tasks))
	var total domain.Budget
	for _, item := range content.Tasks {
		if !ownerPlanKeyPattern.MatchString(item.Key) {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task key %q must be a lowercase identifier", item.Key))
		}
		if !validTitle(item.Title) {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task %q has an invalid title", item.Key))
		}
		if len(item.Description) > 4096 || !utf8.ValidString(item.Description) || strings.ContainsRune(item.Description, '\x00') {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task %q has an invalid description", item.Key))
		}
		if !workspaceNamePattern.MatchString(item.TaskClass) {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task %q has invalid task class %q", item.Key, item.TaskClass))
		}
		if !containsExact(grant.TaskClasses, item.TaskClass) {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task %q uses task class %q outside staffing grant %s", item.Key, item.TaskClass, grant.ID))
		}
		if item.Priority < 0 || item.Priority > 1000 {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task %q priority must be between 0 and 1000", item.Key))
		}
		if !validBudget(item.Budget) {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task %q has an invalid budget", item.Key))
		}
		if len(item.DependsOn) > 15 {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task %q has more than 15 dependencies", item.Key))
		}
		if item.LaunchProfileID == "" {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task %q is missing its durable agent launch profile", item.Key))
		}
		if _, exists := keys[item.Key]; exists {
			return domainAgentError(CodeDomainStaffingDenied, "work proposal task keys must be unique")
		}
		profile, err := queryLaunchProfile(ctx, tx, scope.Workspace.ID, item.LaunchProfileID)
		if err != nil {
			return err
		}
		agent, err := queryAgent(ctx, tx, scope.Workspace.ID, profile.AgentID)
		if err != nil {
			return err
		}
		if profile.ProjectID != scope.Project.ID || profile.Status != domain.LaunchProfileActive || profile.ManagerGrantID != "" || !agent.Enabled || agent.Revision != profile.AgentRevision || !staffingProfileAllows(grant.Profiles, profile.Provider, profile.Runtime, agent.MaxConcurrency) {
			return domainAgentError(CodeDomainStaffingDenied, "work proposal launch profile is stale or outside the staffing grant")
		}
		var descendant int
		if err := tx.QueryRowContext(ctx, `WITH RECURSIVE descendants(agent_id) AS (SELECT ? UNION ALL SELECT child.agent_id FROM domain_agent_memberships child JOIN descendants parent ON child.parent_agent_id=parent.agent_id WHERE child.project_id=? AND child.status='active') SELECT EXISTS(SELECT 1 FROM descendants WHERE agent_id=?)`, scope.Agent.ID, scope.Project.ID, agent.ID).Scan(&descendant); err != nil {
			return storageFailure("validate proposed durable assignee", err)
		}
		if descendant == 0 {
			return domainAgentError(CodeDomainStaffingDenied, "work proposal assignee is outside this durable agent subtree")
		}
		total.TokenLimit += item.Budget.TokenLimit
		total.CostCents += item.Budget.CostCents
		total.TimeSeconds += item.Budget.TimeSeconds
		keys[item.Key] = domain.OwnerPlanTask{Key: item.Key, DependsOn: item.DependsOn}
	}
	for _, item := range content.Tasks {
		seen := map[string]bool{}
		for _, dependency := range item.DependsOn {
			if dependency == item.Key || seen[dependency] {
				return domainAgentError(CodeDomainStaffingDenied, "work proposal contains a repeated or self dependency")
			}
			if _, ok := keys[dependency]; !ok {
				return domainAgentError(CodeDomainStaffingDenied, "work proposal dependency references an unknown task")
			}
			seen[dependency] = true
		}
	}
	if ownerPlanHasCycle(keys) {
		return domainAgentError(CodeDomainStaffingDenied, "work proposal dependency graph contains a cycle")
	}
	if !delegatedBudgetAllows(grant.Budget, total) {
		return domainAgentError(CodeDomainStaffingDenied, "work proposal task budgets exceed the staffing grant")
	}
	if !delegatedBudgetAllows(content.ObjectiveBudget, total) {
		return domainAgentError(CodeDomainStaffingDenied, "work proposal task budgets exceed the objective budget")
	}
	return nil
}

func normalizeDomainWorkContent(content *domain.DomainWorkProposalContent) {
	content.ObjectiveTitle = strings.TrimSpace(content.ObjectiveTitle)
	for index := range content.Tasks {
		task := &content.Tasks[index]
		task.Key = strings.TrimSpace(task.Key)
		task.Title = strings.TrimSpace(task.Title)
		task.Description = strings.TrimSpace(task.Description)
		task.TaskClass = strings.TrimSpace(task.TaskClass)
		task.LaunchProfileID = strings.TrimSpace(task.LaunchProfileID)
		for dependency := range task.DependsOn {
			task.DependsOn[dependency] = strings.TrimSpace(task.DependsOn[dependency])
		}
	}
}

func (s *Store) DomainWorkProposals(ctx context.Context, workspaceIdentifier, projectIdentifier string) ([]domain.DomainWorkProposal, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageFailure("begin domain work proposal list", err)
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
	rows, err := tx.QueryContext(ctx, "SELECT id FROM domain_work_proposals WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT 100", project.ID)
	if err != nil {
		return nil, storageFailure("list domain work proposals", err)
	}
	defer rows.Close()
	result := []domain.DomainWorkProposal{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, storageFailure("scan domain work proposal", err)
		}
		value, err := queryDomainWorkProposal(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func queryDomainWorkProposal(ctx context.Context, database messageQueryContext, id string) (domain.DomainWorkProposal, error) {
	var value domain.DomainWorkProposal
	var content string
	err := database.QueryRowContext(ctx, `SELECT id,workspace_id,project_id,source_agent_id,source_thread_id,staffing_grant_id,staffing_grant_revision,summary,as_of_event_sequence,content_json,content_sha256,status,COALESCE(decision_note,''),revision,created_at,updated_at,created_by,updated_by,COALESCE(decided_at,''),COALESCE(decided_by,'') FROM domain_work_proposals WHERE id=?`, strings.TrimSpace(id)).Scan(&value.ID, &value.WorkspaceID, &value.ProjectID, &value.SourceAgentID, &value.SourceThreadID, &value.StaffingGrantID, &value.StaffingGrantRevision, &value.Summary, &value.AsOfEventSequence, &content, &value.ContentSHA256, &value.Status, &value.DecisionNote, &value.Revision, &value.CreatedAt, &value.UpdatedAt, &value.CreatedBy, &value.UpdatedBy, &value.DecidedAt, &value.DecidedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return value, domainAgentError(CodeDomainStaffingGrantNotFound, "domain work proposal was not found")
	}
	if err != nil {
		return value, storageFailure("read domain work proposal", err)
	}
	if err := json.Unmarshal([]byte(content), &value.Content); err != nil {
		return value, storageFailure("decode domain work proposal", err)
	}
	return value, nil
}

func (s *Store) AcceptDomainWorkProposal(ctx context.Context, command DecideDomainWorkProposalCommand) (domain.DomainWorkProposalDecision, error) {
	return s.decideDomainWorkProposal(ctx, command, true)
}
func (s *Store) RejectDomainWorkProposal(ctx context.Context, command DecideDomainWorkProposalCommand) (domain.DomainWorkProposalDecision, error) {
	return s.decideDomainWorkProposal(ctx, command, false)
}

func (s *Store) decideDomainWorkProposal(ctx context.Context, command DecideDomainWorkProposalCommand, accept bool) (domain.DomainWorkProposalDecision, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProposalID = strings.TrimSpace(command.ProposalID)
	command.DecisionNote = strings.TrimSpace(command.DecisionNote)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.ProposalID == "" || command.ExpectedRevision < 1 || !validDecisionNote(command.DecisionNote) {
		return domain.DomainWorkProposalDecision{}, domainAgentError(CodeDomainStaffingDenied, "work proposal decision requires exact revision and bounded owner note")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeDomainStaffingDenied); err != nil {
		return domain.DomainWorkProposalDecision{}, err
	}
	operation := "domain.work-proposal.reject"
	if accept {
		operation = "domain.work-proposal.accept"
	}
	requestHash, err := hashCommand(operation, command)
	if err != nil {
		return domain.DomainWorkProposalDecision{}, storageFailure("hash work proposal decision", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.DomainWorkProposalDecision{}, storageFailure("begin work proposal decision", err)
	}
	defer tx.Rollback()
	var replay domain.DomainWorkProposalDecision
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, operation, requestHash, &replay); err != nil {
		return domain.DomainWorkProposalDecision{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return domain.DomainWorkProposalDecision{}, err
	}
	proposal, err := queryDomainWorkProposal(ctx, tx, command.ProposalID)
	if err != nil {
		return domain.DomainWorkProposalDecision{}, err
	}
	if proposal.WorkspaceID != workspace.ID {
		return domain.DomainWorkProposalDecision{}, domainAgentError(CodeDomainStaffingGrantNotFound, "work proposal is outside this workspace")
	}
	if proposal.Revision != command.ExpectedRevision {
		return domain.DomainWorkProposalDecision{}, revisionConflict("domain_work_proposal", proposal.ID, command.ExpectedRevision, proposal.Revision)
	}
	if proposal.Status != domain.DomainWorkProposalPending {
		return domain.DomainWorkProposalDecision{}, domainAgentError(CodeDomainStaffingDenied, "only a pending work proposal can be decided")
	}
	now := s.nowText()
	proposal.Status = domain.DomainWorkProposalRejected
	if accept {
		proposal.Status = domain.DomainWorkProposalAccepted
	}
	proposal.DecisionNote = command.DecisionNote
	proposal.Revision = 2
	proposal.UpdatedAt = now
	proposal.UpdatedBy = localOwnerActorID
	proposal.DecidedAt = now
	proposal.DecidedBy = localOwnerActorID
	if accept {
		scope, err := s.domainAgentSessionScopeInTransaction(ctx, tx, proposal.SourceThreadID)
		if err != nil {
			return domain.DomainWorkProposalDecision{}, err
		}
		grant, err := queryDomainAgentStaffingGrant(ctx, tx, proposal.StaffingGrantID)
		if err != nil {
			return domain.DomainWorkProposalDecision{}, err
		}
		if grant.Revision != proposal.StaffingGrantRevision {
			proposal.Status = domain.DomainWorkProposalStale
		} else if err := validateDomainWorkContent(ctx, tx, s.clock().UTC(), scope, grant, proposal.Content); err != nil {
			proposal.Status = domain.DomainWorkProposalStale
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE domain_work_proposals SET status=?,decision_note=?,revision=2,updated_at=?,updated_by='local-owner',decided_at=?,decided_by='local-owner' WHERE id=?`, proposal.Status, proposal.DecisionNote, now, now, proposal.ID); err != nil {
		return domain.DomainWorkProposalDecision{}, storageFailure("decide domain work proposal", err)
	}
	effects := []domain.DomainWorkProposalEffect{}
	if proposal.Status == domain.DomainWorkProposalAccepted {
		var applyErr error
		effects, applyErr = s.applyDomainWorkProposal(ctx, tx, proposal, command.CorrelationID, now)
		if applyErr != nil {
			return domain.DomainWorkProposalDecision{}, applyErr
		}
	}
	eventType := domainWorkProposalRejected
	if proposal.Status == domain.DomainWorkProposalAccepted {
		eventType = domainWorkProposalAccepted
	} else if proposal.Status == domain.DomainWorkProposalStale {
		eventType = domainWorkProposalStale
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "domain_work_proposal", proposal.ID, proposal.Revision, eventType, command.CorrelationID, now, map[string]any{"status": proposal.Status, "decision_note": proposal.DecisionNote, "effect_count": len(effects), "content_sha256": proposal.ContentSHA256})
	if err != nil {
		return domain.DomainWorkProposalDecision{}, err
	}
	result := domain.DomainWorkProposalDecision{Proposal: proposal, Effects: effects, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, operation, requestHash, result, now); err != nil {
		return domain.DomainWorkProposalDecision{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.DomainWorkProposalDecision{}, storageFailure("commit domain work proposal decision", err)
	}
	return result, nil
}

func (s *Store) applyDomainWorkProposal(ctx context.Context, tx *sql.Tx, proposal domain.DomainWorkProposal, correlationID, now string) ([]domain.DomainWorkProposalEffect, error) {
	objectiveID, err := randomID("obj_")
	if err != nil {
		return nil, storageFailure("generate workstream id", err)
	}
	objective := domain.Objective{ID: objectiveID, WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, Title: strings.TrimSpace(proposal.Content.ObjectiveTitle), Status: domain.ObjectiveActive, Budget: proposal.Content.ObjectiveBudget, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	if _, err := tx.ExecContext(ctx, `INSERT INTO objectives(id,workspace_id,project_id,title,status,budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,?,?,'active',?,?,?,1,?,?,?,?)`, objective.ID, objective.WorkspaceID, objective.ProjectID, objective.Title, objective.Budget.TokenLimit, objective.Budget.CostCents, objective.Budget.TimeSeconds, now, now, localOwnerActorID, localOwnerActorID); err != nil {
		return nil, storageFailure("create proposed workstream", err)
	}
	objectiveEvent, err := appendEvent(ctx, tx, proposal.WorkspaceID, "objective", objective.ID, 1, objectiveCreated, correlationID, now, map[string]any{"project_id": proposal.ProjectID, "title": objective.Title, "budget": objective.Budget})
	if err != nil {
		return nil, err
	}
	effects := []domain.DomainWorkProposalEffect{{EntityType: "objective", EntityID: objective.ID, EventSequence: objectiveEvent}}
	tasks := map[string]domain.Task{}
	for _, item := range proposal.Content.Tasks {
		id, err := randomID("task_")
		if err != nil {
			return nil, storageFailure("generate proposed task", err)
		}
		task := domain.Task{ID: id, WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, ObjectiveID: objective.ID, Title: strings.TrimSpace(item.Title), Description: strings.TrimSpace(item.Description), Status: domain.TaskReady, Priority: item.Priority, Budget: item.Budget, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,workspace_id,project_id,objective_id,title,description,status,blocked_reason,priority,budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,?,?,?,NULLIF(?,''),'ready',NULL,?,?,?,?,1,?,?,?,?)`, task.ID, task.WorkspaceID, task.ProjectID, task.ObjectiveID, task.Title, task.Description, task.Priority, task.Budget.TokenLimit, task.Budget.CostCents, task.Budget.TimeSeconds, now, now, localOwnerActorID, localOwnerActorID); err != nil {
			return nil, storageFailure("create proposed task", err)
		}
		event, err := appendEvent(ctx, tx, proposal.WorkspaceID, "task", task.ID, 1, taskCreated, correlationID, now, map[string]any{"project_id": task.ProjectID, "objective_id": task.ObjectiveID, "title": task.Title, "description": task.Description, "status": task.Status, "priority": task.Priority, "budget": task.Budget})
		if err != nil {
			return nil, err
		}
		tasks[item.Key] = task
		effects = append(effects, domain.DomainWorkProposalEffect{TaskKey: item.Key, EntityType: "task", EntityID: task.ID, EventSequence: event})
	}
	for _, item := range proposal.Content.Tasks {
		task := tasks[item.Key]
		for _, dependencyKey := range item.DependsOn {
			dependency := tasks[dependencyKey]
			if _, err := tx.ExecContext(ctx, "INSERT INTO task_dependencies(task_id,depends_on_task_id,created_at,created_by) VALUES(?,?,?,?)", task.ID, dependency.ID, now, localOwnerActorID); err != nil {
				return nil, storageFailure("create proposed dependency", err)
			}
			task.Revision++
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET revision=?,updated_at=?,updated_by=? WHERE id=?", task.Revision, now, localOwnerActorID, task.ID); err != nil {
				return nil, storageFailure("advance proposed task dependency", err)
			}
			event, err := appendEvent(ctx, tx, proposal.WorkspaceID, "task", task.ID, task.Revision, taskDependencyAdded, correlationID, now, map[string]any{"depends_on_task_id": dependency.ID})
			if err != nil {
				return nil, err
			}
			effects = append(effects, domain.DomainWorkProposalEffect{TaskKey: item.Key, EntityType: "task_dependency", EntityID: task.ID + ":" + dependency.ID, EventSequence: event})
		}
		tasks[item.Key] = task
	}
	for _, item := range proposal.Content.Tasks {
		task := tasks[item.Key]
		profile, err := queryLaunchProfile(ctx, tx, proposal.WorkspaceID, item.LaunchProfileID)
		if err != nil {
			return nil, err
		}
		id, err := randomID("sintent_")
		if err != nil {
			return nil, storageFailure("generate proposed scheduling intent", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO scheduling_intents(id,workspace_id,project_id,objective_id,task_id,agent_id,launch_profile_id,source_domain_work_proposal_id,source_domain_task_key,status,attempts,last_evaluated_event_sequence,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,?,?,?,?,?,?,?,'pending',0,0,1,?,?,?,?)`, id, proposal.WorkspaceID, proposal.ProjectID, objective.ID, task.ID, profile.AgentID, profile.ID, proposal.ID, item.Key, now, now, localOwnerActorID, localOwnerActorID); err != nil {
			return nil, storageFailure("publish proposed scheduling intent", err)
		}
		event, err := appendEvent(ctx, tx, proposal.WorkspaceID, "scheduling_intent", id, 1, schedulingIntentCreatedEvent, correlationID, now, map[string]any{"task_id": task.ID, "agent_id": profile.AgentID, "launch_profile_id": profile.ID, "source_domain_work_proposal_id": proposal.ID, "source_domain_task_key": item.Key})
		if err != nil {
			return nil, err
		}
		effects = append(effects, domain.DomainWorkProposalEffect{TaskKey: item.Key, EntityType: "scheduling_intent", EntityID: id, EventSequence: event})
	}
	return effects, nil
}
