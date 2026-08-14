package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"crewfold/internal/domain"
)

type schedulingClaimOverlapPlan struct {
	other    domain.WorkClaim
	witness  string
	severity string
	response string
}

type schedulingClaimRequirementPlan struct {
	kind, target, mode, policy string
	overlaps                   []schedulingClaimOverlapPlan
}

type schedulingClaimPlan struct {
	requirements []schedulingClaimRequirementPlan
}

type schedulingPlacementPlan struct {
	agent    domain.AgentDefinition
	checkout domain.Checkout
	claims   schedulingClaimPlan
	evidence map[string]any
}

type schedulingDeferral struct {
	code, message, dimension string
}

const maxSchedulingEvidenceItems = 32

// preflightSchedulingPlacement performs the complete read-only placement
// decision. It deliberately continues after recoverable failures so a deferred
// action freezes the same policy, capacity, dependency, checkout, hold, and
// claim evidence that a successful action uses.
func (s *Store) preflightSchedulingPlacement(ctx context.Context, tx *sql.Tx, policy domain.SupervisorPolicy, intent domain.SchedulingIntent, task domain.Task, profile domain.LaunchProfile, now string) (schedulingPlacementPlan, error) {
	plan := schedulingPlacementPlan{evidence: map[string]any{
		"snapshot_schema": "supervisor-placement:v1",
		"policy_revision": policy.Revision,
		"intent_revision": intent.Revision,
	}}
	deferrals := make([]schedulingDeferral, 0)
	deferFor := func(code, dimension, message string) {
		deferrals = append(deferrals, schedulingDeferral{code: code, dimension: dimension, message: message})
	}

	agent, err := queryAgent(ctx, tx, intent.WorkspaceID, intent.AgentID)
	if err != nil {
		return schedulingPlacementPlan{}, err
	}
	plan.agent = agent
	profileValid := profile.ID == intent.LaunchProfileID && profile.Status == domain.LaunchProfileActive &&
		profile.ProjectID == intent.ProjectID && profile.AgentID == intent.AgentID && profile.ManagerGrantID == "" &&
		agent.Enabled && agent.Revision == profile.AgentRevision && agent.Runtime == profile.Runtime && agent.Provider == profile.Provider
	plan.evidence["launch_profile"] = map[string]any{
		"id": profile.ID, "revision": profile.Revision, "status": profile.Status,
		"project_id": profile.ProjectID, "agent_id": profile.AgentID, "agent_revision": profile.AgentRevision,
		"runtime": profile.Runtime, "provider": profile.Provider, "checkout_id": profile.CheckoutID,
		"manager_grant_id": profile.ManagerGrantID, "valid": profileValid,
	}
	plan.evidence["agent"] = map[string]any{
		"id": agent.ID, "revision": agent.Revision, "enabled": agent.Enabled,
		"runtime": agent.Runtime, "provider": agent.Provider, "max_concurrency": agent.MaxConcurrency,
	}
	if !profileValid {
		deferFor(CodePlacementUnavailable, "launch_profile", "launch profile or exact agent authority is stale")
	}

	reserved := "('requested','starting','active','blocked','stopping','lost')"
	var nodeActive, workspaceActive, workspaceStarting, projectActive, providerActive, agentActive int
	counts := []struct {
		statement string
		dest      *int
		args      []any
		name      string
	}{
		{`SELECT COUNT(*) FROM runs WHERE status IN ` + reserved, &nodeActive, nil, "node active"},
		{`SELECT COUNT(*) FROM runs WHERE workspace_id=? AND status IN ` + reserved, &workspaceActive, []any{intent.WorkspaceID}, "workspace active"},
		{`SELECT COUNT(*) FROM runs WHERE workspace_id=? AND status IN ('requested','starting')`, &workspaceStarting, []any{intent.WorkspaceID}, "workspace starting"},
		{`SELECT COUNT(*) FROM runs WHERE project_id=? AND status IN ` + reserved, &projectActive, []any{intent.ProjectID}, "project active"},
		{`SELECT COUNT(*) FROM runs WHERE workspace_id=? AND provider=? AND status IN ` + reserved, &providerActive, []any{intent.WorkspaceID, profile.Provider}, "provider active"},
		{`SELECT COUNT(*) FROM runs WHERE agent_id=? AND status IN ` + reserved, &agentActive, []any{agent.ID}, "agent active"},
	}
	for _, count := range counts {
		if err := tx.QueryRowContext(ctx, count.statement, count.args...).Scan(count.dest); err != nil {
			return schedulingPlacementPlan{}, storageFailure("count "+count.name+" placement capacity", err)
		}
	}
	projectLimit := policy.Limits.DefaultProjectConcurrency
	if value, ok := policy.Limits.ProjectConcurrency[intent.ProjectID]; ok {
		projectLimit = value
	}
	providerLimit := policy.Limits.DefaultProviderConcurrency
	if value, ok := policy.Limits.ProviderConcurrency[profile.Provider]; ok {
		providerLimit = value
	}
	capacities := []struct {
		dimension, scope string
		actual, limit    int
	}{
		{"node_unresolved", "node", nodeActive, NodeExecutionCapacityLimit},
		{"workspace_starting", intent.WorkspaceID, workspaceStarting, policy.Limits.MaxStartingRuns},
		{"workspace_unresolved", intent.WorkspaceID, workspaceActive, policy.Limits.MaxActiveRuns},
		{"project_unresolved", intent.ProjectID, projectActive, projectLimit},
		{"provider_unresolved", profile.Provider, providerActive, providerLimit},
		{"agent_active_runs", agent.ID, agentActive, agent.MaxConcurrency},
	}
	for _, capacity := range capacities {
		plan.evidence[capacity.dimension] = map[string]any{
			"scope": capacity.scope, "actual": capacity.actual, "limit": capacity.limit,
			"available": capacity.actual < capacity.limit,
		}
		if capacity.actual >= capacity.limit {
			code := CodeExecutionCapacityExhausted
			if capacity.dimension == "agent_active_runs" {
				code = CodePlacementUnavailable
			}
			deferFor(code, capacity.dimension,
				fmt.Sprintf("%s capacity exhausted: actual=%d limit=%d", capacity.dimension, capacity.actual, capacity.limit))
		}
	}

	readiness, err := taskReadiness(ctx, tx, task)
	if err != nil {
		return schedulingPlacementPlan{}, err
	}
	var dependencyCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies WHERE task_id=?`, task.ID).Scan(&dependencyCount); err != nil {
		return schedulingPlacementPlan{}, storageFailure("count scheduling dependencies", err)
	}
	dependencyRows, err := tx.QueryContext(ctx, `SELECT dependency.id,dependency.revision,dependency.status,dependency.updated_at
FROM task_dependencies edge JOIN tasks dependency ON dependency.id=edge.depends_on_task_id
WHERE edge.task_id=? ORDER BY dependency.id LIMIT ?`, task.ID, maxSchedulingEvidenceItems)
	if err != nil {
		return schedulingPlacementPlan{}, storageFailure("read scheduling dependency proof", err)
	}
	dependencies := make([]map[string]any, 0)
	dependencyDigest := sha256.New()
	for dependencyRows.Next() {
		var id, status, updatedAt string
		var revision int64
		if err := dependencyRows.Scan(&id, &revision, &status, &updatedAt); err != nil {
			dependencyRows.Close()
			return schedulingPlacementPlan{}, storageFailure("scan scheduling dependency proof", err)
		}
		writeSchedulingEvidenceDigest(dependencyDigest, []any{id, revision, status, updatedAt})
		dependencies = append(dependencies, map[string]any{"task_id": id, "revision": revision, "status": status, "updated_at": updatedAt})
	}
	if err := dependencyRows.Close(); err != nil {
		return schedulingPlacementPlan{}, storageFailure("close scheduling dependency proof", err)
	}
	plan.evidence["task_readiness"] = map[string]any{
		"task_id": task.ID, "task_revision": task.Revision, "task_status": task.Status,
		"assignment_id": task.AssignmentID, "ready": readiness.Ready && task.AssignmentID == "", "reason": readiness.Reason,
		"dependencies":     dependencies,
		"dependency_count": dependencyCount, "dependencies_truncated": dependencyCount > len(dependencies),
		"dependency_witnesses_sha256": hex.EncodeToString(dependencyDigest.Sum(nil)),
	}
	if dependencyCount > maxSchedulingEvidenceItems {
		deferFor(CodeSchedulingPaused, "dependency_evidence_bound",
			fmt.Sprintf("task has %d dependencies, exceeding the automatic scheduling evidence bound %d", dependencyCount, maxSchedulingEvidenceItems))
	}
	if !readiness.Ready || task.AssignmentID != "" {
		deferFor(CodeSchedulingPaused, "task_readiness", readiness.Reason)
	}

	effectiveHoldPredicate := `FROM task_coordination_holds hold
JOIN work_overlaps overlap ON overlap.id=hold.overlap_id
JOIN work_claims low_claim ON low_claim.id=overlap.claim_low_id
JOIN work_claims high_claim ON high_claim.id=overlap.claim_high_id
WHERE hold.task_id=?
  AND low_claim.status='active' AND high_claim.status='active'
  AND (crewfold_timestamp_key(low_claim.lease_expires_at)>crewfold_timestamp_key(?) OR EXISTS (
    SELECT 1 FROM runs run WHERE run.task_id=low_claim.task_id
      AND run.status IN ('requested','starting','active','blocked','stopping','lost')
  ))
  AND (crewfold_timestamp_key(high_claim.lease_expires_at)>crewfold_timestamp_key(?) OR EXISTS (
    SELECT 1 FROM runs run WHERE run.task_id=high_claim.task_id
      AND run.status IN ('requested','starting','active','blocked','stopping','lost')
  ))`
	var holdCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) `+effectiveHoldPredicate, task.ID, now, now).Scan(&holdCount); err != nil {
		return schedulingPlacementPlan{}, storageFailure("count effective scheduling coordination holds", err)
	}
	holdRows, err := tx.QueryContext(ctx, `SELECT hold.overlap_id `+effectiveHoldPredicate+`
ORDER BY hold.overlap_id LIMIT ?`, task.ID, now, now, maxSchedulingEvidenceItems)
	if err != nil {
		return schedulingPlacementPlan{}, storageFailure("read scheduling coordination holds", err)
	}
	holdIDs := make([]string, 0)
	holdDigest := sha256.New()
	for holdRows.Next() {
		var id string
		if err := holdRows.Scan(&id); err != nil {
			holdRows.Close()
			return schedulingPlacementPlan{}, storageFailure("scan scheduling coordination hold", err)
		}
		writeSchedulingEvidenceDigest(holdDigest, id)
		holdIDs = append(holdIDs, id)
	}
	if err := holdRows.Close(); err != nil {
		return schedulingPlacementPlan{}, storageFailure("close scheduling coordination holds", err)
	}
	plan.evidence["coordination_holds"] = holdIDs
	plan.evidence["coordination_hold_count"] = holdCount
	plan.evidence["coordination_holds_truncated"] = holdCount > len(holdIDs)
	plan.evidence["coordination_hold_witnesses_sha256"] = hex.EncodeToString(holdDigest.Sum(nil))
	if holdCount > 0 {
		deferFor(CodeSchedulingPaused, "coordination_hold", "task has active coordination hold "+holdIDs[0])
	}

	checkoutCandidatePredicate := `FROM checkouts checkout WHERE checkout.project_id=? AND (?='' OR checkout.id=?)`
	var candidateCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) `+checkoutCandidatePredicate,
		intent.ProjectID, profile.CheckoutID, profile.CheckoutID).Scan(&candidateCount); err != nil {
		return schedulingPlacementPlan{}, storageFailure("count scheduling checkout candidates", err)
	}
	candidateRows, err := tx.QueryContext(ctx, `SELECT checkout.id,checkout.revision,checkout.write_mode,checkout.availability,
  (SELECT COUNT(*) FROM runs reserved WHERE reserved.checkout_id=checkout.id
    AND reserved.status IN ('requested','starting','active','blocked','stopping','lost')) AS reserved_runs
`+checkoutCandidatePredicate+`
ORDER BY CASE checkout.write_mode WHEN 'exclusive' THEN 0 WHEN 'claimed' THEN 1 ELSE 2 END,checkout.path,checkout.id LIMIT ?`,
		intent.ProjectID, profile.CheckoutID, profile.CheckoutID, maxSchedulingEvidenceItems)
	if err != nil {
		return schedulingPlacementPlan{}, storageFailure("read scheduling checkout candidates", err)
	}
	candidates := make([]map[string]any, 0)
	candidateDigest := sha256.New()
	for candidateRows.Next() {
		var id, writeMode, availability string
		var revision int64
		var reservedRuns int
		if err := candidateRows.Scan(&id, &revision, &writeMode, &availability, &reservedRuns); err != nil {
			candidateRows.Close()
			return schedulingPlacementPlan{}, storageFailure("scan scheduling checkout candidate", err)
		}
		eligible := availability == domain.CheckoutAvailable && writeMode != domain.WriteModeReadOnly &&
			(writeMode == domain.WriteModeShared || reservedRuns == 0)
		writeSchedulingEvidenceDigest(candidateDigest, []any{id, revision, writeMode, availability, reservedRuns, eligible})
		candidates = append(candidates, map[string]any{
			"id": id, "revision": revision, "write_mode": writeMode, "availability": availability,
			"reserved_run_count": reservedRuns, "eligible": eligible,
		})
	}
	if err := candidateRows.Close(); err != nil {
		return schedulingPlacementPlan{}, storageFailure("close scheduling checkout candidates", err)
	}
	plan.evidence["checkout_candidates"] = candidates
	plan.evidence["checkout_candidate_count"] = candidateCount
	plan.evidence["checkout_candidates_truncated"] = candidateCount > len(candidates)
	plan.evidence["checkout_candidate_witnesses_sha256"] = hex.EncodeToString(candidateDigest.Sum(nil))
	if candidateCount > maxSchedulingEvidenceItems {
		deferFor(CodePlacementUnavailable, "checkout_candidate_bound",
			fmt.Sprintf("project has %d checkout candidates, exceeding the automatic placement bound %d", candidateCount, maxSchedulingEvidenceItems))
	}
	checkout, checkoutErr := selectRunCheckout(ctx, tx, task.ProjectID, profile.CheckoutID)
	if checkoutErr != nil {
		if ErrorCode(checkoutErr) != CodePlacementUnavailable {
			return schedulingPlacementPlan{}, checkoutErr
		}
		plan.evidence["checkout"] = nil
		deferFor(CodePlacementUnavailable, "checkout", checkoutErr.Error())
	} else {
		plan.checkout = checkout
		plan.evidence["checkout"] = map[string]any{
			"id": checkout.ID, "revision": checkout.Revision, "write_mode": checkout.WriteMode,
			"availability": checkout.Availability,
		}
	}

	claimPlan, claimEvidence, claimDeferrals, err := inspectSchedulingClaimPlan(ctx, tx, task, now)
	if err != nil {
		return schedulingPlacementPlan{}, err
	}
	plan.claims = claimPlan
	for key, value := range claimEvidence {
		plan.evidence[key] = value
	}
	deferrals = append(deferrals, claimDeferrals...)
	// Seal one deterministic witness per failing dimension. The detailed
	// dependency/checkout/claim arrays carry bounded canonical witnesses; a
	// repeated conflict must not inflate the action envelope past its hard cap.
	uniqueDeferrals := make([]schedulingDeferral, 0, len(deferrals))
	seenDeferralDimensions := make(map[string]struct{})
	for _, deferral := range deferrals {
		if _, seen := seenDeferralDimensions[deferral.dimension]; seen {
			continue
		}
		seenDeferralDimensions[deferral.dimension] = struct{}{}
		uniqueDeferrals = append(uniqueDeferrals, deferral)
	}
	deferrals = uniqueDeferrals
	if len(deferrals) > 0 {
		dimensions := make([]string, 0, len(deferrals))
		deferralProof := make([]map[string]any, 0, len(deferrals))
		for _, deferral := range deferrals {
			dimensions = append(dimensions, deferral.dimension)
			deferralProof = append(deferralProof, map[string]any{"code": deferral.code, "dimension": deferral.dimension, "message": deferral.message})
		}
		plan.evidence["deferral_code"] = deferrals[0].code
		plan.evidence["failing_dimensions"] = dimensions
		plan.evidence["deferrals"] = deferralProof
		return plan, newSupervisorDeferral(deferrals[0].code, deferrals[0].message, plan.evidence)
	}
	plan.evidence["deferral_code"] = ""
	plan.evidence["failing_dimensions"] = []string{}
	plan.evidence["deferrals"] = []map[string]any{}
	return plan, nil
}

func inspectSchedulingClaimPlan(ctx context.Context, tx *sql.Tx, task domain.Task, now string) (schedulingClaimPlan, map[string]any, []schedulingDeferral, error) {
	var requirementCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_claim_requirements WHERE task_id=?`, task.ID).Scan(&requirementCount); err != nil {
		return schedulingClaimPlan{}, nil, nil, storageFailure("count scheduling claim requirements", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind,target,mode,conflict_policy FROM task_claim_requirements WHERE task_id=? ORDER BY kind,target,id LIMIT ?`, task.ID, maxSchedulingEvidenceItems)
	if err != nil {
		return schedulingClaimPlan{}, nil, nil, storageFailure("read scheduling claim requirements", err)
	}
	plan := schedulingClaimPlan{requirements: make([]schedulingClaimRequirementPlan, 0)}
	for rows.Next() {
		var value schedulingClaimRequirementPlan
		if err := rows.Scan(&value.kind, &value.target, &value.mode, &value.policy); err != nil {
			rows.Close()
			return schedulingClaimPlan{}, nil, nil, storageFailure("scan scheduling claim requirement", err)
		}
		plan.requirements = append(plan.requirements, value)
	}
	if err := rows.Close(); err != nil {
		return schedulingClaimPlan{}, nil, nil, storageFailure("close scheduling claim requirements", err)
	}
	requirementProof := make([]map[string]any, 0, len(plan.requirements))
	conflictProof := make([]map[string]any, 0)
	conflictCount := 0
	conflictDigest := sha256.New()
	deferrals := make([]schedulingDeferral, 0)
	if requirementCount > maxSchedulingEvidenceItems {
		deferrals = append(deferrals, schedulingDeferral{code: CodeSchedulingPaused, dimension: "claim_requirement_bound",
			message: fmt.Sprintf("task has %d claim requirements, exceeding the automatic scheduling bound %d", requirementCount, maxSchedulingEvidenceItems)})
	}
	for index := range plan.requirements {
		value := &plan.requirements[index]
		requirementProof = append(requirementProof, map[string]any{
			"kind": value.kind, "target": value.target, "mode": value.mode, "conflict_policy": value.policy,
		})
		existing, candidateCount, err := effectiveClaimsForScheduling(ctx, tx, task.ProjectID, value.kind, now)
		if err != nil {
			return schedulingClaimPlan{}, nil, nil, err
		}
		if candidateCount > maxSchedulingEvidenceItems {
			deferrals = append(deferrals, schedulingDeferral{code: CodeSchedulingPaused, dimension: "claim_candidate_bound",
				message: fmt.Sprintf("%s claim placement has %d active candidates, exceeding the evidence bound %d", value.kind, candidateCount, maxSchedulingEvidenceItems)})
		}
		for _, other := range existing {
			if other.TaskID == task.ID {
				continue
			}
			witness, intersects := domain.ClaimScopesOverlap(value.kind, value.target, other.Target)
			if !intersects {
				continue
			}
			response := domain.ClaimPolicyResponse(value.policy, other.ConflictPolicy)
			overlap := schedulingClaimOverlapPlan{other: other, witness: witness, response: response,
				severity: domain.ClaimOverlapSeverity(value.mode, other.Mode)}
			value.overlaps = append(value.overlaps, overlap)
			conflictCount++
			writeSchedulingEvidenceDigest(conflictDigest, []any{
				value.kind, value.target, value.mode, value.policy, other.ID, other.TaskID, other.Target,
				other.Mode, other.ConflictPolicy, witness, overlap.severity, response,
			})
			if len(conflictProof) < maxSchedulingEvidenceItems {
				conflictProof = append(conflictProof, map[string]any{
					"kind": value.kind, "target": value.target, "mode": value.mode, "conflict_policy": value.policy,
					"conflicting_claim_id": other.ID, "conflicting_task_id": other.TaskID,
					"conflicting_target": other.Target, "conflicting_mode": other.Mode,
					"conflicting_policy": other.ConflictPolicy, "witness": witness,
					"severity": overlap.severity, "policy_response": response,
				})
			}
			if response != domain.ClaimPolicyNotify {
				deferrals = append(deferrals, schedulingDeferral{
					code: CodeClaimConflict, dimension: "claim",
					message: fmt.Sprintf("required %s claim %q conflicts with active claim %s at %s", value.kind, value.target, other.ID, witness),
				})
			}
		}
	}
	if conflictCount > maxSchedulingEvidenceItems {
		deferrals = append(deferrals, schedulingDeferral{code: CodeSchedulingPaused, dimension: "claim_evidence_bound",
			message: fmt.Sprintf("claim placement evidence has %d overlaps, exceeding the per-action bound %d", conflictCount, maxSchedulingEvidenceItems)})
	}
	evidence := map[string]any{
		"required_claim_count": requirementCount, "claim_requirements": requirementProof,
		"claim_requirements_truncated": requirementCount > len(requirementProof),
		"claim_conflict_count":         conflictCount, "claim_conflicts": conflictProof,
		"claim_conflicts_truncated": conflictCount > len(conflictProof),
		"claim_conflicts_sha256":    hex.EncodeToString(conflictDigest.Sum(nil)),
		"claim_overlaps":            []map[string]any{}, "acquired_claim_ids": []string{},
	}
	return plan, evidence, deferrals, nil
}

func effectiveClaimsForScheduling(ctx context.Context, tx *sql.Tx, projectID, kind, now string) ([]domain.WorkClaim, int, error) {
	predicate := ` WHERE project_id=? AND kind=? AND status='active'
AND (crewfold_timestamp_key(lease_expires_at)>crewfold_timestamp_key(?) OR EXISTS (
  SELECT 1 FROM runs run WHERE run.task_id=work_claims.task_id
    AND run.status IN ('requested','starting','active','blocked','stopping','lost')
))`
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_claims`+predicate, projectID, kind, now).Scan(&total); err != nil {
		return nil, 0, storageFailure("count effective scheduling claims", err)
	}
	rows, err := tx.QueryContext(ctx, claimSelect+predicate+` ORDER BY id LIMIT ?`, projectID, kind, now, maxSchedulingEvidenceItems)
	if err != nil {
		return nil, 0, storageFailure("list effective scheduling claims", err)
	}
	values, err := scanClaims(rows)
	return values, total, err
}

func writeSchedulingEvidenceDigest(digest interface{ Write([]byte) (int, error) }, value any) {
	encoded, _ := json.Marshal(value)
	_, _ = digest.Write(encoded)
	_, _ = digest.Write([]byte{'\n'})
}

func (s *Store) materializeSchedulingClaims(ctx context.Context, tx *sql.Tx, intent domain.SchedulingIntent, task domain.Task, checkout domain.Checkout, profile domain.LaunchProfile, plan schedulingClaimPlan, correlationID, now string, proof map[string]any) error {
	acquiredClaimIDs := make([]string, 0, len(plan.requirements))
	overlapProof := make([]map[string]any, 0)
	for _, value := range plan.requirements {
		id, err := randomID("claim_")
		if err != nil {
			return storageFailure("generate scheduled claim", err)
		}
		checkoutID := ""
		baseline := []string{}
		if value.kind == domain.ClaimKindPath {
			checkoutID, baseline = checkout.ID, append([]string(nil), checkout.DirtyPaths...)
		}
		baselineJSON, _ := json.Marshal(baseline)
		leaseExpiry := s.clock().UTC().Add(time.Duration(profile.AssignmentLeaseSeconds) * time.Second).Format(time.RFC3339Nano)
		claim := domain.WorkClaim{ID: id, WorkspaceID: intent.WorkspaceID, ProjectID: task.ProjectID, TaskID: task.ID, CheckoutID: checkoutID,
			Kind: value.kind, Target: value.target, Mode: value.mode, ConflictPolicy: value.policy, Status: domain.ClaimActive,
			BaselinePaths: baseline, LeaseExpiresAt: leaseExpiry, Revision: 1, CreatedAt: now, UpdatedAt: now,
			CreatedBy: "subsystem:supervisor", UpdatedBy: "subsystem:supervisor"}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_claims(id,workspace_id,project_id,task_id,checkout_id,kind,target,mode,conflict_policy,status,baseline_paths_json,lease_expires_at,revision,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,?,NULLIF(?,''),?,?,?,?,'active',?,?,1,?,?,?,?)`, id, intent.WorkspaceID, task.ProjectID, task.ID, checkoutID, value.kind, value.target, value.mode, value.policy, string(baselineJSON), leaseExpiry, now, now, "subsystem:supervisor", "subsystem:supervisor"); err != nil {
			return storageFailure("insert scheduled required claim", err)
		}
		acquiredClaimIDs = append(acquiredClaimIDs, id)
		if _, err := appendEventForActor(ctx, tx, intent.WorkspaceID, "claim", claim.ID, 1, claimAddedEvent, correlationID, now, "subsystem:supervisor", "subsystem", claim); err != nil {
			return err
		}
		for _, pending := range value.overlaps {
			if pending.response != domain.ClaimPolicyNotify {
				return &Error{Code: CodeClaimConflict, Message: "non-notify claim conflict passed scheduling preflight"}
			}
			overlap, _, err := insertOverlapForActor(ctx, tx, claim, pending.other, pending.witness, pending.severity, pending.response,
				correlationID, now, "subsystem:supervisor", "subsystem")
			if err != nil {
				return err
			}
			overlapProof = append(overlapProof, map[string]any{
				"overlap_id": overlap.ID, "kind": value.kind, "target": value.target,
				"conflicting_claim_id": pending.other.ID, "witness": pending.witness,
				"severity": pending.severity, "policy_response": pending.response,
			})
		}
	}
	proof["claim_overlaps"] = overlapProof
	proof["acquired_claim_ids"] = acquiredClaimIDs
	return nil
}
