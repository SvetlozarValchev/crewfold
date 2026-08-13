package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"crewfold/internal/domain"
)

// validateManagerProposalAggregate applies the validations that require a
// complete view of both the existing objective and the proposed batch. The
// caller performs field/union/revision validation first; this helper never
// mutates operational state.
func (s *Store) validateManagerProposalAggregate(ctx context.Context, tx *sql.Tx, grant domain.ManagerGrant, actions []domain.ManagerProposalAction) ([]domain.ProposalValidationIssue, error) {
	issues := make([]domain.ProposalValidationIssue, 0)
	errorCount, warningCount := 0, 0
	add := func(code, path, message, severity string) {
		// Bound each class independently. The caller orders errors before warnings
		// and applies the final protocol cap, preserving denial evidence even when
		// an objective contains many warning-only claim overlaps.
		if severity == domain.ProposalIssueError {
			if errorCount >= maximumManagerValidationIssues {
				return
			}
			errorCount++
		} else {
			if warningCount >= maximumManagerValidationIssues {
				return
			}
			warningCount++
		}
		issues = append(issues, domain.ProposalValidationIssue{Code: code, Path: path, Message: message, Severity: severity})
	}
	if err := validateManagerProposalDAG(ctx, tx, grant, actions, add); err != nil {
		return nil, err
	}
	if err := validateManagerProposalObjectiveBudget(ctx, tx, grant, actions, add); err != nil {
		return nil, err
	}
	if err := validateManagerProposalClaims(ctx, tx, grant, actions, add); err != nil {
		return nil, err
	}
	return issues, nil
}

type proposalIssueAdder func(code, path, message, severity string)

func proposalGraphNode(ref domain.ProposalTaskRef, aliases map[string]string) (string, bool) {
	if ref.TaskID != "" && ref.ProposalTaskKey == "" {
		return ref.TaskID, true
	}
	value, ok := aliases[ref.ProposalTaskKey]
	return value, ok
}

func validateManagerProposalDAG(ctx context.Context, tx *sql.Tx, grant domain.ManagerGrant, actions []domain.ManagerProposalAction, add proposalIssueAdder) error {
	nodes := make(map[string]struct{})
	edges := make(map[string]map[string]struct{})
	rows, err := tx.QueryContext(ctx, `
SELECT task.id FROM tasks task WHERE task.workspace_id=? AND task.project_id=? ORDER BY task.id`, grant.WorkspaceID, grant.ProjectID)
	if err != nil {
		return storageFailure("read manager proposal graph tasks", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return storageFailure("scan manager proposal graph task", err)
		}
		nodes[id] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return storageFailure("close manager proposal graph tasks", err)
	}
	rows, err = tx.QueryContext(ctx, `
SELECT dependency.task_id,dependency.depends_on_task_id
FROM task_dependencies dependency
JOIN tasks task ON task.id=dependency.task_id
JOIN tasks upstream ON upstream.id=dependency.depends_on_task_id
WHERE task.workspace_id=? AND task.project_id=? AND upstream.project_id=task.project_id
ORDER BY dependency.task_id,dependency.depends_on_task_id`, grant.WorkspaceID, grant.ProjectID)
	if err != nil {
		return storageFailure("read manager proposal graph edges", err)
	}
	for rows.Next() {
		var taskID, dependsOnID string
		if err := rows.Scan(&taskID, &dependsOnID); err != nil {
			rows.Close()
			return storageFailure("scan manager proposal graph edge", err)
		}
		if edges[taskID] == nil {
			edges[taskID] = make(map[string]struct{})
		}
		edges[taskID][dependsOnID] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return storageFailure("close manager proposal graph edges", err)
	}

	aliases := make(map[string]string)
	for _, action := range actions {
		if action.Type == domain.ProposalActionCreateTask && action.CreateTask != nil && action.CreateTask.TaskKey != "" {
			node := "proposal:" + action.CreateTask.TaskKey
			aliases[action.CreateTask.TaskKey] = node
			nodes[node] = struct{}{}
		}
	}
	for index, action := range actions {
		if action.Type != domain.ProposalActionAddDependency || action.AddDependency == nil {
			continue
		}
		left, leftOK := proposalGraphNode(action.AddDependency.Task, aliases)
		right, rightOK := proposalGraphNode(action.AddDependency.DependsOn, aliases)
		if !leftOK || !rightOK {
			continue // The field validator emits the exact reference issue.
		}
		path := fmt.Sprintf("actions[%d]", index)
		if left == right {
			add("dependency_cycle", path, "a task cannot depend on itself", domain.ProposalIssueError)
			continue
		}
		if edges[left] == nil {
			edges[left] = make(map[string]struct{})
		}
		if _, exists := edges[left][right]; exists {
			add("duplicate_dependency", path, "dependency already exists in the current graph or this proposal", domain.ProposalIssueError)
			continue
		}
		edges[left][right] = struct{}{}
	}

	orderedNodes := make([]string, 0, len(nodes))
	for node := range nodes {
		orderedNodes = append(orderedNodes, node)
	}
	sort.Strings(orderedNodes)
	state := make(map[string]uint8, len(nodes))
	var visit func(string) bool
	visit = func(node string) bool {
		if state[node] == 1 {
			return true
		}
		if state[node] == 2 {
			return false
		}
		state[node] = 1
		children := make([]string, 0, len(edges[node]))
		for child := range edges[node] {
			children = append(children, child)
		}
		sort.Strings(children)
		for _, child := range children {
			if visit(child) {
				return true
			}
		}
		state[node] = 2
		return false
	}
	for _, node := range orderedNodes {
		if visit(node) {
			add("dependency_cycle", "actions", "existing and proposed dependencies form a cycle", domain.ProposalIssueError)
			break
		}
	}
	return nil
}

type budgetDimension struct {
	name              string
	objectiveLimit    int64
	grantLimit        int64
	existingAllocated int64
	proposedAllocated int64
	existingUnlimited bool
	proposedUnlimited bool
	overflow          bool
}

func validateManagerProposalObjectiveBudget(ctx context.Context, tx *sql.Tx, grant domain.ManagerGrant, actions []domain.ManagerProposalAction, add proposalIssueAdder) error {
	objective, err := queryObjective(ctx, tx, grant.WorkspaceID, grant.ObjectiveID)
	if err != nil {
		return err
	}
	if objective.ProjectID != grant.ProjectID || objective.Status != domain.ObjectiveActive {
		add("objective_not_active", "objective", "the exact manager objective is no longer active in the granted project", domain.ProposalIssueError)
	}
	dimensions := []*budgetDimension{
		{name: "token_limit", objectiveLimit: objective.Budget.TokenLimit, grantLimit: grant.Limits.Budget.TokenLimit},
		{name: "cost_cents", objectiveLimit: objective.Budget.CostCents, grantLimit: grant.Limits.Budget.CostCents},
		{name: "time_seconds", objectiveLimit: objective.Budget.TimeSeconds, grantLimit: grant.Limits.Budget.TimeSeconds},
	}
	rows, err := tx.QueryContext(ctx, `
SELECT budget_tokens,budget_cost_cents,budget_time_seconds
FROM tasks WHERE workspace_id=? AND objective_id=? AND status<>'cancelled' ORDER BY id`, grant.WorkspaceID, grant.ObjectiveID)
	if err != nil {
		return storageFailure("read objective task allocations", err)
	}
	for rows.Next() {
		var value domain.Budget
		if err := rows.Scan(&value.TokenLimit, &value.CostCents, &value.TimeSeconds); err != nil {
			rows.Close()
			return storageFailure("scan objective task allocation", err)
		}
		addBudgetAllocation(dimensions, value, false)
	}
	if err := rows.Close(); err != nil {
		return storageFailure("close objective task allocations", err)
	}
	for _, action := range actions {
		switch action.Type {
		case domain.ProposalActionCreateTask:
			if action.CreateTask != nil {
				addBudgetAllocation(dimensions, action.CreateTask.Budget, true)
			}
		case domain.ProposalActionRequestReview:
			if action.RequestReview != nil {
				addBudgetAllocation(dimensions, action.RequestReview.Budget, true)
			}
		}
	}
	for _, dimension := range dimensions {
		path := "objective.budget." + dimension.name
		if dimension.proposedUnlimited && (dimension.objectiveLimit > 0 || dimension.grantLimit > 0) {
			add("unlimited_budget_under_finite_envelope", path, "a proposed zero budget is unlimited beneath a finite objective or grant envelope", domain.ProposalIssueError)
		}
		if dimension.objectiveLimit > 0 && dimension.existingUnlimited {
			add("objective_budget_overcommitted", path, "an existing non-cancelled task has an unlimited allocation beneath this finite objective envelope", domain.ProposalIssueError)
			continue
		}
		if dimension.objectiveLimit > 0 && (dimension.overflow || dimension.existingAllocated > dimension.objectiveLimit ||
			dimension.proposedAllocated > dimension.objectiveLimit-dimension.existingAllocated) {
			add("objective_budget_exceeded", path, "existing non-cancelled allocations plus this proposal exceed the finite objective envelope", domain.ProposalIssueError)
		}
	}
	return nil
}

func addBudgetAllocation(dimensions []*budgetDimension, value domain.Budget, proposed bool) {
	values := []int64{value.TokenLimit, value.CostCents, value.TimeSeconds}
	for index, allocation := range values {
		dimension := dimensions[index]
		if allocation == 0 {
			if proposed {
				dimension.proposedUnlimited = true
			} else {
				dimension.existingUnlimited = true
			}
			continue
		}
		if proposed {
			if allocation > maxSignedInt64-dimension.proposedAllocated {
				dimension.overflow = true
			} else {
				dimension.proposedAllocated += allocation
			}
		} else if allocation > maxSignedInt64-dimension.existingAllocated {
			dimension.overflow = true
		} else {
			dimension.existingAllocated += allocation
		}
	}
}

const maxSignedInt64 = int64(^uint64(0) >> 1)

type proposalClaimScope struct {
	taskKey string
	kind    string
	target  string
	mode    string
	policy  string
	path    string
}

func validateManagerProposalClaims(ctx context.Context, tx *sql.Tx, grant domain.ManagerGrant, actions []domain.ManagerProposalAction, add proposalIssueAdder) error {
	proposed := make([]proposalClaimScope, 0)
	for index, action := range actions {
		if action.Type != domain.ProposalActionDeclareClaimRequirement || action.DeclareClaimRequirement == nil {
			continue
		}
		value := action.DeclareClaimRequirement
		target, err := domain.NormalizeClaimTarget(value.Kind, value.Target)
		if err != nil || target != value.Target || !domain.ValidClaimMode(value.Mode) || !domain.ValidClaimPolicy(value.ConflictPolicy) {
			continue // The field validator owns malformed-shape diagnostics.
		}
		taskKey, ok := proposalClaimTaskKey(value.Task)
		if !ok {
			continue
		}
		proposed = append(proposed, proposalClaimScope{taskKey: taskKey, kind: value.Kind, target: target, mode: value.Mode,
			policy: value.ConflictPolicy, path: fmt.Sprintf("actions[%d]", index)})
	}

	rows, err := tx.QueryContext(ctx, `
SELECT claim.task_id,claim.kind,claim.target,claim.mode,claim.conflict_policy
FROM work_claims claim JOIN tasks task ON task.id=claim.task_id
WHERE claim.workspace_id=? AND claim.project_id=? AND claim.status='active'
  AND task.status NOT IN ('completed','cancelled')
ORDER BY claim.kind,claim.target,claim.id`, grant.WorkspaceID, grant.ProjectID)
	if err != nil {
		return storageFailure("read active claims for manager proposal", err)
	}
	existing := make([]proposalClaimScope, 0)
	for rows.Next() {
		var value proposalClaimScope
		if err := rows.Scan(&value.taskKey, &value.kind, &value.target, &value.mode, &value.policy); err != nil {
			rows.Close()
			return storageFailure("scan active claim for manager proposal", err)
		}
		existing = append(existing, value)
	}
	if err := rows.Close(); err != nil {
		return storageFailure("close active claims for manager proposal", err)
	}
	rows, err = tx.QueryContext(ctx, `
SELECT requirement.task_id,requirement.kind,requirement.target,requirement.mode,requirement.conflict_policy
FROM task_claim_requirements requirement JOIN tasks task ON task.id=requirement.task_id
WHERE requirement.workspace_id=? AND requirement.project_id=?
  AND task.status NOT IN ('completed','cancelled')
ORDER BY requirement.kind,requirement.target,requirement.id`, grant.WorkspaceID, grant.ProjectID)
	if err != nil {
		return storageFailure("read accepted claim requirements for manager proposal", err)
	}
	for rows.Next() {
		var value proposalClaimScope
		if err := rows.Scan(&value.taskKey, &value.kind, &value.target, &value.mode, &value.policy); err != nil {
			rows.Close()
			return storageFailure("scan accepted claim requirement", err)
		}
		existing = append(existing, value)
	}
	if err := rows.Close(); err != nil {
		return storageFailure("close accepted claim requirements", err)
	}

	for index, value := range proposed {
		for _, other := range existing {
			compareManagerProposalClaim(value, other, add)
		}
		for prior := 0; prior < index; prior++ {
			compareManagerProposalClaim(value, proposed[prior], add)
		}
	}
	return nil
}

func proposalClaimTaskKey(ref domain.ProposalTaskRef) (string, bool) {
	if ref.TaskID != "" && ref.ProposalTaskKey == "" {
		return ref.TaskID, true
	}
	if ref.ProposalTaskKey != "" && ref.TaskID == "" {
		return "proposal:" + ref.ProposalTaskKey, true
	}
	return "", false
}

func compareManagerProposalClaim(value, other proposalClaimScope, add proposalIssueAdder) {
	if value.kind != other.kind {
		return
	}
	witness, overlaps := domain.ClaimScopesOverlap(value.kind, value.target, other.target)
	if !overlaps {
		return
	}
	if value.taskKey == other.taskKey {
		add("duplicate_claim_requirement", value.path, fmt.Sprintf("task repeats an overlapping %s claim requirement at %s", value.kind, witness), domain.ProposalIssueError)
		return
	}
	switch response := domain.ClaimPolicyResponse(value.policy, other.policy); response {
	case domain.ClaimPolicyDenyNew:
		add("claim_conflict", value.path, fmt.Sprintf("claim requirement overlaps another task at %s and deterministic policy denies it", witness), domain.ProposalIssueError)
	case domain.ClaimPolicyPauseScheduling:
		add("claim_pause_required", value.path, fmt.Sprintf("claim overlap at %s will defer scheduling", witness), domain.ProposalIssueWarning)
	case domain.ClaimPolicyRequestResolution:
		add("claim_resolution_required", value.path, fmt.Sprintf("claim overlap at %s requires owner resolution before scheduling", witness), domain.ProposalIssueWarning)
	default:
		add("claim_overlap_notify", value.path, fmt.Sprintf("claim overlap at %s will be recorded when scheduled", witness), domain.ProposalIssueWarning)
	}
}
