package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
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
	sequence, err := appendEventForActor(ctx, tx, proposal.WorkspaceID, "domain_work_proposal", proposal.ID, 1, domainWorkProposalSubmitted, command.CorrelationID, now, scope.Agent.ID, domain.EventActorIntegration, map[string]any{"project_id": proposal.ProjectID, "source_agent_id": proposal.SourceAgentID, "staffing_grant_id": proposal.StaffingGrantID, "content_sha256": proposal.ContentSHA256, "agent_count": len(proposal.Content.Agents), "task_count": len(proposal.Content.Tasks), "status": proposal.Status})
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
	if !validTitle(strings.TrimSpace(content.ObjectiveTitle)) || !validBudget(content.ObjectiveBudget) || len(content.Agents) < 1 || len(content.Agents) > 16 || len(content.Tasks) < 1 || len(content.Tasks) > 16 {
		return domainAgentError(CodeDomainStaffingDenied, "work proposal must contain one bounded workstream, 1 to 16 agents, and 1 to 16 tasks")
	}
	checkout, err := queryCheckoutByID(ctx, tx, content.PrimaryCheckoutID)
	if err != nil || checkout.ProjectID != scope.Project.ID || checkout.Revision != content.PrimaryCheckoutRevision || checkout.Availability != domain.CheckoutAvailable || checkout.WriteMode == domain.WriteModeReadOnly {
		return domainAgentError(CodeDomainStaffingDenied, "work proposal primary checkout is stale, unavailable, read-only, or outside this domain")
	}
	if _, err := validateObjectiveCheckouts(ctx, tx, scope.Project.ID, content.PrimaryCheckoutID, content.ReferenceCheckoutIDs); err != nil {
		return domainAgentError(CodeDomainStaffingDenied, err.Error())
	}
	agents := make(map[string]domain.DomainWorkProposalAgent, len(content.Agents))
	agentTaskClasses := make(map[string]string, len(content.Agents))
	var proposedDescendants, proposedConcurrency int
	var proposedBudget domain.Budget
	for _, item := range content.Agents {
		if !ownerPlanKeyPattern.MatchString(item.Key) || agents[item.Key].Key != "" {
			return domainAgentError(CodeDomainStaffingDenied, "work proposal agent keys must be unique lowercase identifiers")
		}
		if !validBudget(item.Budget) {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal agent %q has an invalid staffing budget", item.Key))
		}
		existing := item.ExistingAgentID != ""
		if existing {
			if item.ExistingMembershipRevision < 1 || item.ExistingLaunchProfileID == "" || item.Name != "" || item.ParentKey != "" || item.Budget != (domain.Budget{}) {
				return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal existing agent %q has an invalid exact reference", item.Key))
			}
			profile, err := queryLaunchProfile(ctx, tx, scope.Workspace.ID, item.ExistingLaunchProfileID)
			if err != nil {
				return err
			}
			agent, err := queryAgent(ctx, tx, scope.Workspace.ID, item.ExistingAgentID)
			if err != nil {
				return err
			}
			membership, err := queryDomainAgentMembership(ctx, tx, scope.Project.ID, agent.ID)
			if err != nil {
				return err
			}
			if membership.Revision != item.ExistingMembershipRevision || membership.Status != domain.DomainAgentActive || membership.WorkstreamID != "" || profile.ProjectID != scope.Project.ID || profile.AgentID != agent.ID || profile.CheckoutID != content.PrimaryCheckoutID || profile.Status != domain.LaunchProfileActive || profile.ManagerGrantID != "" || !agent.Enabled || agent.Revision != profile.AgentRevision || !staffingProfileAllows(grant.Profiles, profile.Provider, profile.Runtime, agent.MaxConcurrency) {
				return domainAgentError(CodeDomainStaffingDenied, "work proposal existing launch profile is stale or outside the staffing grant")
			}
			var descendant int
			if err := tx.QueryRowContext(ctx, `WITH RECURSIVE descendants(agent_id) AS (SELECT ? UNION ALL SELECT child.agent_id FROM domain_agent_memberships child JOIN descendants parent ON child.parent_agent_id=parent.agent_id WHERE child.project_id=? AND child.status='active') SELECT EXISTS(SELECT 1 FROM descendants WHERE agent_id=?)`, scope.Agent.ID, scope.Project.ID, agent.ID).Scan(&descendant); err != nil {
				return storageFailure("validate proposed durable assignee", err)
			}
			if descendant == 0 {
				return domainAgentError(CodeDomainStaffingDenied, "work proposal existing assignee is outside this durable agent subtree")
			}
			agentTaskClasses[item.Key] = profile.Purpose
		} else {
			if !workspaceNamePattern.MatchString(item.Name) || !validShortText(item.Role) || !validDomainAgentCharter(item.OperatingCharter) || !validDomainAgentDelegationPolicy(item.DelegationPolicy) || !validShortText(item.Provider) || !validShortText(item.Runtime) || item.MaxConcurrency < 1 || item.MaxConcurrency > 100 || !workspaceNamePattern.MatchString(item.TaskClass) || !validBudget(item.Budget) || !staffingProfileAllows(grant.Profiles, item.Provider, item.Runtime, item.MaxConcurrency) || !containsExact(grant.TaskClasses, item.TaskClass) {
				return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal new agent %q is outside the staffing grant", item.Key))
			}
			var duplicate int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM agents WHERE workspace_id=? AND name=?", scope.Workspace.ID, item.Name).Scan(&duplicate); err != nil {
				return storageFailure("validate proposed agent name", err)
			}
			if duplicate != 0 {
				return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal agent name %q already exists", item.Name))
			}
			for _, prior := range content.Agents {
				if prior.Key != item.Key && prior.ExistingAgentID == "" && prior.Name == item.Name {
					return domainAgentError(CodeDomainStaffingDenied, "work proposal new agent names must be unique")
				}
			}
			proposedDescendants++
			proposedConcurrency += item.MaxConcurrency
			proposedBudget.TokenLimit += item.Budget.TokenLimit
			proposedBudget.CostCents += item.Budget.CostCents
			proposedBudget.TimeSeconds += item.Budget.TimeSeconds
			agentTaskClasses[item.Key] = item.TaskClass
		}
		agents[item.Key] = item
	}
	for _, item := range content.Agents {
		if item.ParentKey != "" {
			_, ok := agents[item.ParentKey]
			if !ok || item.ParentKey == item.Key {
				return domainAgentError(CodeDomainStaffingDenied, "work proposal agent parent must name another proposed team member")
			}
		}
	}
	if proposalAgentHierarchyHasCycle(agents) {
		return domainAgentError(CodeDomainStaffingDenied, "work proposal agent hierarchy contains a cycle")
	}
	var descendants, concurrency, membershipCount int
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE descendants(agent_id) AS (SELECT agent_id FROM domain_agent_memberships WHERE project_id=? AND parent_agent_id=? AND status='active' UNION ALL SELECT child.agent_id FROM domain_agent_memberships child JOIN descendants parent ON child.parent_agent_id=parent.agent_id WHERE child.project_id=? AND child.status='active') SELECT COUNT(*),COALESCE(SUM(agent.max_concurrency),0) FROM descendants JOIN agents agent ON agent.id=descendants.agent_id`, scope.Project.ID, scope.Agent.ID, scope.Project.ID).Scan(&descendants, &concurrency); err != nil {
		return storageFailure("count proposed durable descendants", err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM domain_agent_memberships WHERE project_id=?", scope.Project.ID).Scan(&membershipCount); err != nil {
		return storageFailure("count proposed domain agents", err)
	}
	if descendants+proposedDescendants > grant.MaxDescendants || concurrency+proposedConcurrency > grant.MaxConcurrency || membershipCount+proposedDescendants > maximumDomainAgents {
		return domainAgentError(CodeDomainStaffingCapacity, "work proposal team exceeds the staffing descendant or concurrency ceiling")
	}
	var spent domain.Budget
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(budget_tokens),0),COALESCE(SUM(budget_cost_cents),0),COALESCE(SUM(budget_time_seconds),0) FROM domain_agent_staffing_allocations WHERE grant_id=?`, grant.ID).Scan(&spent.TokenLimit, &spent.CostCents, &spent.TimeSeconds); err != nil {
		return storageFailure("sum proposed staffing allocation budget", err)
	}
	if !staffingBudgetDimensionAllows(grant.Budget.TokenLimit, spent.TokenLimit, proposedBudget.TokenLimit) || !staffingBudgetDimensionAllows(grant.Budget.CostCents, spent.CostCents, proposedBudget.CostCents) || !staffingBudgetDimensionAllows(grant.Budget.TimeSeconds, spent.TimeSeconds, proposedBudget.TimeSeconds) {
		return domainAgentError(CodeDomainStaffingCapacity, "work proposal team exceeds the staffing budget")
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
		if len(item.DependsOn) > 15 || len(item.DependencyDelivery) != len(item.DependsOn) {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task %q has more than 15 dependencies", item.Key))
		}
		assignee, ok := agents[item.AssigneeKey]
		if !ok {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task %q references an unknown assignee", item.Key))
		}
		if _, exists := keys[item.Key]; exists {
			return domainAgentError(CodeDomainStaffingDenied, "work proposal task keys must be unique")
		}
		if item.TaskClass != agentTaskClasses[assignee.Key] {
			return domainAgentError(CodeDomainStaffingDenied, fmt.Sprintf("work proposal task %q class does not match its proposed agent allocation", item.Key))
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
			if !validDependencyDelivery(item.DependencyDelivery[dependency]) {
				return domainAgentError(CodeDomainStaffingDenied, "work proposal dependency delivery requirement is invalid")
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
	content.PrimaryCheckoutID = strings.TrimSpace(content.PrimaryCheckoutID)
	for index := range content.ReferenceCheckoutIDs {
		content.ReferenceCheckoutIDs[index] = strings.TrimSpace(content.ReferenceCheckoutIDs[index])
	}
	for index := range content.Agents {
		agent := &content.Agents[index]
		agent.Key = strings.TrimSpace(agent.Key)
		agent.ExistingAgentID = strings.TrimSpace(agent.ExistingAgentID)
		agent.ExistingLaunchProfileID = strings.TrimSpace(agent.ExistingLaunchProfileID)
		agent.Name = strings.TrimSpace(agent.Name)
		agent.Role = strings.TrimSpace(agent.Role)
		agent.ParentKey = strings.TrimSpace(agent.ParentKey)
		agent.OperatingCharter = strings.TrimSpace(agent.OperatingCharter)
		agent.DelegationPolicy = strings.TrimSpace(agent.DelegationPolicy)
		agent.Provider = strings.TrimSpace(agent.Provider)
		agent.Runtime = strings.TrimSpace(agent.Runtime)
		agent.TaskClass = strings.TrimSpace(agent.TaskClass)
	}
	for index := range content.Tasks {
		task := &content.Tasks[index]
		task.Key = strings.TrimSpace(task.Key)
		task.Title = strings.TrimSpace(task.Title)
		task.Description = strings.TrimSpace(task.Description)
		task.TaskClass = strings.TrimSpace(task.TaskClass)
		task.AssigneeKey = strings.TrimSpace(task.AssigneeKey)
		for dependency := range task.DependsOn {
			task.DependsOn[dependency] = strings.TrimSpace(task.DependsOn[dependency])
		}
		if task.DependencyDelivery == nil {
			task.DependencyDelivery = map[string]string{}
		}
		for key, value := range task.DependencyDelivery {
			trimmedKey := strings.TrimSpace(key)
			trimmedValue := strings.TrimSpace(value)
			if trimmedKey != key {
				delete(task.DependencyDelivery, key)
			}
			task.DependencyDelivery[trimmedKey] = trimmedValue
		}
	}
}

func proposalAgentHierarchyHasCycle(agents map[string]domain.DomainWorkProposalAgent) bool {
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(key string) bool {
		if visiting[key] {
			return true
		}
		if visited[key] {
			return false
		}
		visiting[key] = true
		if parent := agents[key].ParentKey; parent != "" && visit(parent) {
			return true
		}
		visiting[key] = false
		visited[key] = true
		return false
	}
	for key := range agents {
		if visit(key) {
			return true
		}
	}
	return false
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
	objective := domain.Objective{ID: objectiveID, WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, PrimaryCheckoutID: proposal.Content.PrimaryCheckoutID, Title: strings.TrimSpace(proposal.Content.ObjectiveTitle), Status: domain.ObjectiveActive, Budget: proposal.Content.ObjectiveBudget, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	if _, err := tx.ExecContext(ctx, `INSERT INTO objectives(id,workspace_id,project_id,primary_checkout_id,title,status,budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,?,NULLIF(?,''),?,'active',?,?,?,1,?,?,?,?)`, objective.ID, objective.WorkspaceID, objective.ProjectID, objective.PrimaryCheckoutID, objective.Title, objective.Budget.TokenLimit, objective.Budget.CostCents, objective.Budget.TimeSeconds, now, now, localOwnerActorID, localOwnerActorID); err != nil {
		return nil, storageFailure("create proposed workstream", err)
	}
	for ordinal, checkoutID := range proposal.Content.ReferenceCheckoutIDs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO objective_reference_checkouts(objective_id,checkout_id,ordinal,created_at,created_by) VALUES(?,?,?,?,?)", objective.ID, checkoutID, ordinal, now, localOwnerActorID); err != nil {
			return nil, storageFailure("create proposed reference checkout", err)
		}
	}
	objectiveEvent, err := appendEvent(ctx, tx, proposal.WorkspaceID, "objective", objective.ID, 1, objectiveCreated, correlationID, now, map[string]any{"project_id": proposal.ProjectID, "primary_checkout_id": objective.PrimaryCheckoutID, "reference_checkout_ids": proposal.Content.ReferenceCheckoutIDs, "title": objective.Title, "budget": objective.Budget})
	if err != nil {
		return nil, err
	}
	effects := []domain.DomainWorkProposalEffect{{EntityType: "objective", EntityID: objective.ID, EventSequence: objectiveEvent}}
	type resolvedAgent struct {
		agentID, profileID string
		membershipRevision int64
		isNew              bool
	}
	resolved := make(map[string]resolvedAgent, len(proposal.Content.Agents))
	for _, item := range proposal.Content.Agents {
		if item.ExistingAgentID != "" {
			resolved[item.Key] = resolvedAgent{agentID: item.ExistingAgentID, profileID: item.ExistingLaunchProfileID, membershipRevision: item.ExistingMembershipRevision}
		}
	}
	remaining := len(proposal.Content.Agents) - len(resolved)
	for remaining > 0 {
		progressed := false
		for _, item := range proposal.Content.Agents {
			if item.ExistingAgentID != "" || resolved[item.Key].agentID != "" {
				continue
			}
			parentID := proposal.SourceAgentID
			if item.ParentKey != "" {
				parent := resolved[item.ParentKey]
				if parent.agentID == "" {
					continue
				}
				parentID = parent.agentID
			}
			created, createdEffects, err := s.createProposedDomainAgent(ctx, tx, proposal, objective.ID, parentID, item, correlationID, now)
			if err != nil {
				return nil, err
			}
			resolved[item.Key] = created
			effects = append(effects, createdEffects...)
			remaining--
			progressed = true
		}
		if !progressed {
			return nil, domainAgentError(CodeDomainStaffingDenied, "proposed agent hierarchy could not be resolved")
		}
	}
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
			deliveryRequirement := item.DependencyDelivery[dependencyKey]
			if _, err := tx.ExecContext(ctx, "INSERT INTO task_dependencies(task_id,depends_on_task_id,delivery_requirement,created_at,created_by) VALUES(?,?,?,?,?)", task.ID, dependency.ID, deliveryRequirement, now, localOwnerActorID); err != nil {
				return nil, storageFailure("create proposed dependency", err)
			}
			task.Revision++
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET revision=?,updated_at=?,updated_by=? WHERE id=?", task.Revision, now, localOwnerActorID, task.ID); err != nil {
				return nil, storageFailure("advance proposed task dependency", err)
			}
			event, err := appendEvent(ctx, tx, proposal.WorkspaceID, "task", task.ID, task.Revision, taskDependencyAdded, correlationID, now, map[string]any{"depends_on_task_id": dependency.ID, "delivery_requirement": deliveryRequirement})
			if err != nil {
				return nil, err
			}
			effects = append(effects, domain.DomainWorkProposalEffect{TaskKey: item.Key, EntityType: "task_dependency", EntityID: task.ID + ":" + dependency.ID, EventSequence: event})
		}
		tasks[item.Key] = task
	}
	placed := map[string]bool{}
	for _, item := range proposal.Content.Agents {
		entry := resolved[item.Key]
		if entry.isNew || placed[entry.agentID] {
			continue
		}
		membership, err := queryDomainAgentMembership(ctx, tx, proposal.ProjectID, entry.agentID)
		if err != nil {
			return nil, err
		}
		if membership.Revision != entry.membershipRevision || membership.WorkstreamID != "" {
			return nil, domainAgentError(CodeDomainStaffingDenied, "proposed durable agent placement is stale")
		}
		membership.WorkstreamID = objective.ID
		membership.Revision++
		membership.UpdatedAt, membership.UpdatedBy = now, localOwnerActorID
		result, err := tx.ExecContext(ctx, `UPDATE domain_agent_memberships SET workstream_id=?,revision=?,updated_at=?,updated_by=? WHERE project_id=? AND agent_id=? AND revision=? AND workstream_id IS NULL`, objective.ID, membership.Revision, now, localOwnerActorID, proposal.ProjectID, entry.agentID, entry.membershipRevision)
		if err != nil {
			return nil, storageFailure("place proposed durable agent", err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			return nil, domainAgentError(CodeDomainStaffingDenied, "proposed durable agent placement changed before acceptance")
		}
		event, err := appendEvent(ctx, tx, proposal.WorkspaceID, "domain_agent", entry.agentID, membership.Revision, domainAgentUpdated, correlationID, now, domainAgentEventData(membership))
		if err != nil {
			return nil, err
		}
		effects = append(effects, domain.DomainWorkProposalEffect{AgentKey: item.Key, EntityType: "domain_agent_placement", EntityID: entry.agentID, EventSequence: event})
		placed[entry.agentID] = true
	}
	for _, item := range proposal.Content.Tasks {
		task := tasks[item.Key]
		entry := resolved[item.AssigneeKey]
		profile, err := queryLaunchProfile(ctx, tx, proposal.WorkspaceID, entry.profileID)
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

func (s *Store) createProposedDomainAgent(ctx context.Context, tx *sql.Tx, proposal domain.DomainWorkProposal, workstreamID, parentID string, item domain.DomainWorkProposalAgent, correlationID, now string) (struct {
	agentID, profileID string
	membershipRevision int64
	isNew              bool
}, []domain.DomainWorkProposalEffect, error) {
	var resolved struct {
		agentID, profileID string
		membershipRevision int64
		isNew              bool
	}
	agentID, err := randomID("agent_")
	if err != nil {
		return resolved, nil, storageFailure("generate proposed durable agent", err)
	}
	allocationID, err := randomID("staffalloc_")
	if err != nil {
		return resolved, nil, storageFailure("generate proposed staffing allocation", err)
	}
	profileID, err := randomID("lprof_")
	if err != nil {
		return resolved, nil, storageFailure("generate proposed launch profile", err)
	}
	agent := domain.AgentDefinition{ID: agentID, WorkspaceID: proposal.WorkspaceID, Name: item.Name, Role: item.Role, Provider: item.Provider, Runtime: item.Runtime, Enabled: true, MaxConcurrency: item.MaxConcurrency, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	membership := domain.DomainAgentMembership{ProjectID: proposal.ProjectID, AgentID: agentID, ParentAgentID: parentID, WorkstreamID: workstreamID, OperatingCharter: item.OperatingCharter, DelegationPolicy: item.DelegationPolicy, Status: domain.DomainAgentActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id,workspace_id,name,role,provider,runtime,enabled,max_concurrency,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,?,?,?,?,1,?,1,?,?,?,?)`, agent.ID, agent.WorkspaceID, agent.Name, agent.Role, agent.Provider, agent.Runtime, agent.MaxConcurrency, now, now, localOwnerActorID, localOwnerActorID); err != nil {
		return resolved, nil, storageFailure("insert proposed durable agent", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_agent_memberships(project_id,agent_id,parent_agent_id,workstream_id,operating_charter,delegation_policy,preferred_entry,status,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,?,?,?,?,0,'active',1,?,?,?,?)`, proposal.ProjectID, agent.ID, parentID, workstreamID, item.OperatingCharter, item.DelegationPolicy, now, now, localOwnerActorID, localOwnerActorID); err != nil {
		return resolved, nil, storageFailure("insert proposed durable membership", err)
	}
	agentEvent, err := appendEvent(ctx, tx, proposal.WorkspaceID, "agent", agent.ID, 1, agentCreated, correlationID, now, map[string]any{"name": agent.Name, "role": agent.Role, "provider": agent.Provider, "runtime": agent.Runtime, "enabled": true, "max_concurrency": agent.MaxConcurrency})
	if err != nil {
		return resolved, nil, err
	}
	membershipEvent, err := appendEvent(ctx, tx, proposal.WorkspaceID, "domain_agent", agent.ID, 1, domainAgentAttached, correlationID, now, domainAgentEventData(membership))
	if err != nil {
		return resolved, nil, err
	}
	_, requestHash, err := canonicalContent(item)
	if err != nil {
		return resolved, nil, storageFailure("hash proposed staffing allocation", err)
	}
	allocationEvent, err := appendEventForActor(ctx, tx, proposal.WorkspaceID, "domain_staffing_allocation", allocationID, 1, domainChildCreated, correlationID, now, proposal.SourceAgentID, domain.EventActorIntegration, map[string]any{"grant_id": proposal.StaffingGrantID, "project_id": proposal.ProjectID, "parent_agent_id": proposal.SourceAgentID, "child_agent_id": agent.ID, "provider": agent.Provider, "runtime": agent.Runtime, "task_class": item.TaskClass, "budget": item.Budget})
	if err != nil {
		return resolved, nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_agent_staffing_allocations(id,grant_id,project_id,parent_agent_id,child_agent_id,provider,runtime,task_class,budget_tokens,budget_cost_cents,budget_time_seconds,request_sha256,event_sequence,created_at,created_by) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, allocationID, proposal.StaffingGrantID, proposal.ProjectID, proposal.SourceAgentID, agent.ID, agent.Provider, agent.Runtime, item.TaskClass, item.Budget.TokenLimit, item.Budget.CostCents, item.Budget.TimeSeconds, requestHash, allocationEvent, now, proposal.SourceAgentID); err != nil {
		return resolved, nil, storageFailure("insert proposed staffing allocation", err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "owner-workbench", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "Owner-directed work completed", Handoff: "Completed the owner-directed work and reported its exact checks and changed paths."}}}
	scenarioJSON, err := json.Marshal(scenario)
	if err != nil {
		return resolved, nil, storageFailure("encode proposed launch scenario", err)
	}
	scenarioDigest := sha256.Sum256(scenarioJSON)
	scenarioHash := hex.EncodeToString(scenarioDigest[:])
	profileContent := launchProfileContent{WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, AgentID: agent.ID, AgentRevision: 1, Purpose: item.TaskClass, Runtime: agent.Runtime, Provider: agent.Provider, CheckoutID: proposal.Content.PrimaryCheckoutID, ScenarioSHA256: scenarioHash, AssignmentLeaseSeconds: 3600, CapabilityTTLSeconds: 3600}
	contentJSON, contentHash, err := canonicalContent(profileContent)
	if err != nil {
		return resolved, nil, storageFailure("hash proposed launch profile", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO launch_profiles(id,workspace_id,project_id,agent_id,agent_revision,purpose,runtime,provider,checkout_id,scenario_json,scenario_sha256,content_json,content_sha256,assignment_lease_seconds,capability_ttl_seconds,manager_grant_id,status,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,'active',1,?,?,?,?)`, profileID, proposal.WorkspaceID, proposal.ProjectID, agent.ID, 1, item.TaskClass, agent.Runtime, agent.Provider, proposal.Content.PrimaryCheckoutID, string(scenarioJSON), scenarioHash, string(contentJSON), contentHash, 3600, 3600, now, now, localOwnerActorID, localOwnerActorID); err != nil {
		return resolved, nil, storageFailure("insert proposed launch profile", err)
	}
	profileEvent, err := appendEvent(ctx, tx, proposal.WorkspaceID, "launch_profile", profileID, 1, launchProfileCreatedEvent, correlationID, now, map[string]any{"project_id": proposal.ProjectID, "agent_id": agent.ID, "agent_revision": 1, "content_sha256": contentHash, "manager_grant_id": ""})
	if err != nil {
		return resolved, nil, err
	}
	resolved.agentID, resolved.profileID, resolved.membershipRevision, resolved.isNew = agent.ID, profileID, 1, true
	effects := []domain.DomainWorkProposalEffect{{AgentKey: item.Key, EntityType: "agent", EntityID: agent.ID, EventSequence: agentEvent}, {AgentKey: item.Key, EntityType: "domain_agent_placement", EntityID: agent.ID, EventSequence: membershipEvent}, {AgentKey: item.Key, EntityType: "domain_staffing_allocation", EntityID: allocationID, EventSequence: allocationEvent}, {AgentKey: item.Key, EntityType: "launch_profile", EntityID: profileID, EventSequence: profileEvent}}
	return resolved, effects, nil
}
