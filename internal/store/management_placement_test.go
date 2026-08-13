package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestDeferredSchedulingActionSealsCompletePlacementPreflight(t *testing.T) {
	const key = "complete-deferred-placement"
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 8,
		SharedTargetCheckout: true,
	})
	acceptAdversarialSchedulingPair(t, storage, fixture, key, domain.ClaimPolicyPauseScheduling)
	configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
	}, key)

	result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		Limit:               100,
		IdempotencyKey:      key + "-scan",
		CorrelationID:       "request-" + key + "-scan",
	})
	if err != nil || len(result.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor() = %#v, %v; want one schedule and one sealed deferral", result, err)
	}
	var deferred *domain.SupervisorAction
	for index := range result.Actions {
		if result.Actions[index].Status == domain.SupervisorActionDeferred {
			deferred = &result.Actions[index]
		}
	}
	if deferred == nil {
		t.Fatalf("RunSupervisor actions = %#v; want deferred action", result.Actions)
	}
	snapshot := deferred.ConstraintSnapshot
	if snapshot["snapshot_schema"] != "supervisor-placement:v1" || snapshot["deferral_code"] != CodeClaimConflict {
		t.Fatalf("deferred placement seal = %#v; want stable schema/code", snapshot)
	}
	for _, field := range []string{
		"launch_profile", "agent", "workspace_active_runs", "workspace_starting_runs",
		"project_active_runs", "provider_active_runs", "agent_active_runs", "task_readiness",
		"coordination_holds", "checkout_candidates", "checkout", "claim_requirements", "claim_conflicts",
	} {
		if _, exists := snapshot[field]; !exists {
			t.Fatalf("deferred placement seal omitted %q: %#v", field, snapshot)
		}
	}
	for _, field := range []string{"workspace_active_runs", "workspace_starting_runs", "project_active_runs", "provider_active_runs", "agent_active_runs"} {
		capacity, ok := snapshot[field].(map[string]any)
		if !ok || capacity["scope"] == nil || capacity["actual"] == nil || capacity["limit"] == nil || capacity["available"] == nil {
			t.Fatalf("deferred capacity %q = %#v; want exact scope/actual/limit/available", field, snapshot[field])
		}
	}
	failing, ok := snapshot["failing_dimensions"].([]string)
	if !ok || len(failing) != 1 || failing[0] != "claim" {
		t.Fatalf("deferred failing dimensions = %#v; want exact claim failure", snapshot["failing_dimensions"])
	}
	conflicts, ok := snapshot["claim_conflicts"].([]map[string]any)
	if !ok || len(conflicts) != 1 || conflicts[0]["conflicting_claim_id"] == "" || conflicts[0]["witness"] == "" || conflicts[0]["policy_response"] != domain.ClaimPolicyPauseScheduling {
		t.Fatalf("deferred claim proof = %#v; want exact conflicting claim/witness/policy", snapshot["claim_conflicts"])
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM work_claims WHERE task_id=?`, deferred.TaskID)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM task_assignments WHERE task_id=?`, deferred.TaskID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM supervisor_action_receipts WHERE action_id=? AND condition_key=?`, deferred.ID, deferred.ConditionKey)
}

func TestStaleProfileDeferralStillSealsCompletePlacementPreflight(t *testing.T) {
	const key = "stale-profile-complete-placement"
	storage, fixture, _, _ := acceptedSingleSchedulingIntent(t, key)
	if _, err := storage.RetireLaunchProfile(context.Background(), RetireLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		LaunchProfileID:     fixture.target.ID,
		ExpectedRevision:    fixture.target.Revision,
		Reason:              "retire exact target to verify fail-closed placement evidence",
		IdempotencyKey:      key + "-retire",
		CorrelationID:       "request-" + key + "-retire",
	}); err != nil {
		t.Fatalf("RetireLaunchProfile() = %v", err)
	}
	result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: key + "-scan", CorrelationID: "request-" + key + "-scan",
	})
	if err != nil || len(result.Actions) != 1 || result.Actions[0].Status != domain.SupervisorActionDeferred {
		t.Fatalf("RunSupervisor(stale profile) = %#v, %v; want one deferred action", result, err)
	}
	snapshot := result.Actions[0].ConstraintSnapshot
	if snapshot["snapshot_schema"] != "supervisor-placement:v1" || snapshot["deferral_code"] != CodePlacementUnavailable {
		t.Fatalf("stale profile snapshot = %#v; want complete stable placement seal", snapshot)
	}
	for _, field := range []string{"launch_profile", "agent", "workspace_active_runs", "task_readiness", "checkout_candidates", "claim_requirements"} {
		if _, exists := snapshot[field]; !exists {
			t.Fatalf("stale profile snapshot omitted %q: %#v", field, snapshot)
		}
	}
	profile, ok := snapshot["launch_profile"].(map[string]any)
	if !ok || profile["id"] != fixture.target.ID || profile["valid"] != false || profile["status"] != domain.LaunchProfileRetired {
		t.Fatalf("stale profile proof = %#v", snapshot["launch_profile"])
	}
}

func TestExplainSupervisorUsesSharedEffectiveLeasePreflight(t *testing.T) {
	t.Run("expired unreserved claim is ignored exactly as next pass reconciles it", func(t *testing.T) {
		observed := time.Date(2033, 4, 5, 6, 7, 8, 0, time.UTC)
		clock := func() time.Time {
			observed = observed.Add(time.Millisecond)
			return observed
		}
		storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
			TargetMaxConcurrency: 8, SharedTargetCheckout: true, Clock: clock,
		})
		const key = "explain-expired-unreserved"
		acceptAdversarialSchedulingPair(t, storage, fixture, key, domain.ClaimPolicyPauseScheduling)
		configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
			MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
		}, key)
		claim, err := storage.AddClaim(context.Background(), AddClaimCommand{
			WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
			TaskID: fixture.planning.Task.ID, Kind: domain.ClaimKindComponent, Target: "shared-renderer",
			Mode: domain.ClaimModeExclusive, ConflictPolicy: domain.ClaimPolicyPauseScheduling,
			LeaseDuration: time.Second, IdempotencyKey: key + "-claim", CorrelationID: "request-" + key + "-claim",
		})
		if err != nil {
			t.Fatalf("AddClaim(expiring witness) = %v", err)
		}
		observed = observed.Add(2 * time.Second)
		explanation, err := storage.ExplainSupervisor(context.Background(), ExplainSupervisorQuery{
			WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		})
		if err != nil || len(explanation.Candidates) != 2 {
			t.Fatalf("ExplainSupervisor(expired claim) = %#v, %v", explanation, err)
		}
		for _, candidate := range explanation.Candidates {
			if !candidate.Eligible || candidate.Constraints["snapshot_schema"] != "supervisor-placement:v1" || candidate.Constraints["claim_conflict_count"] != 0 {
				t.Fatalf("expired unreserved claim candidate = %#v; want eligible shared preflight", candidate)
			}
		}
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM work_claims WHERE id=? AND status='active'`, claim.Claim.ID)
		scheduled, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
			IdempotencyKey: key + "-scan", CorrelationID: "request-" + key + "-scan",
		})
		if err != nil || len(scheduled.ScheduledRunIDs) != 1 {
			t.Fatalf("RunSupervisor(after expired explain) = %#v, %v", scheduled, err)
		}
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM work_claims WHERE id=? AND status='expired'`, claim.Claim.ID)
	})

	t.Run("expired claim retained by reserved run remains an exact blocker", func(t *testing.T) {
		observed := time.Date(2034, 4, 5, 6, 7, 8, 0, time.UTC)
		clock := func() time.Time {
			observed = observed.Add(time.Millisecond)
			return observed
		}
		storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
			TargetMaxConcurrency: 8, SharedTargetCheckout: true, Clock: clock,
		})
		const key = "explain-expired-reserved"
		acceptAdversarialSchedulingPair(t, storage, fixture, key, domain.ClaimPolicyPauseScheduling)
		configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
			MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
		}, key)
		first, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
			IdempotencyKey: key + "-first", CorrelationID: "request-" + key + "-first",
		})
		if err != nil || len(first.ScheduledRunIDs) != 1 {
			t.Fatalf("RunSupervisor(first) = %#v, %v", first, err)
		}
		var deferredIntentID, protectedClaimID string
		if err := storage.db.QueryRow(`SELECT id FROM scheduling_intents WHERE status='deferred'`).Scan(&deferredIntentID); err != nil {
			t.Fatalf("read deferred intent = %v", err)
		}
		if err := storage.db.QueryRow(`SELECT id FROM work_claims WHERE status='active' AND target='shared-renderer'`).Scan(&protectedClaimID); err != nil {
			t.Fatalf("read protected claim = %v", err)
		}
		observed = observed.Add(901 * time.Second)
		explanation, err := storage.ExplainSupervisor(context.Background(), ExplainSupervisorQuery{
			WorkspaceIdentifier: fixture.workspace.ID, IntentID: deferredIntentID, Limit: 100,
		})
		if err != nil || len(explanation.Candidates) != 1 {
			t.Fatalf("ExplainSupervisor(protected expired claim) = %#v, %v", explanation, err)
		}
		candidate := explanation.Candidates[0]
		if candidate.Eligible || candidate.Constraints["deferral_code"] != CodeClaimConflict || candidate.Constraints["claim_conflict_count"] != 1 {
			t.Fatalf("protected expired claim explanation = %#v; want exact retained blocker", candidate)
		}
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM work_claims WHERE id=? AND status='active'`, protectedClaimID)
	})
}
