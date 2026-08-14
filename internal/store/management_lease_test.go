package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestReservedRunsRetainExpiredAssignmentAndClaims(t *testing.T) {
	for _, targetStatus := range []string{
		domain.RunRequested,
		domain.RunStarting,
		domain.RunActive,
		domain.RunBlocked,
		domain.RunStopping,
		domain.RunLost,
	} {
		t.Run(targetStatus, func(t *testing.T) {
			now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
			storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
			workspace, project, _, checkout, assigned := initializeRunTest(t, storage, "reserved lease "+targetStatus)

			claim := addClaimTest(t, storage, AddClaimCommand{
				WorkspaceIdentifier: workspace.ID,
				ProjectIdentifier:   project.ID,
				TaskID:              assigned.Task.ID,
				CheckoutIdentifier:  checkout.ID,
				Kind:                domain.ClaimKindPath,
				Target:              "reserved/**",
				Mode:                domain.ClaimModeExclusive,
				ConflictPolicy:      domain.ClaimPolicyNotify,
				LeaseDuration:       time.Second,
				IdempotencyKey:      "reserved-claim-" + targetStatus,
				CorrelationID:       "request-reserved-claim-" + targetStatus,
			})
			scenarioKind := domain.ObservationProgress
			if targetStatus == domain.RunBlocked {
				scenarioKind = domain.ObservationBlocked
			}
			scenario := domain.FakeScenario{
				Schema: execution.FakeScenarioSchema,
				Name:   "reserved-lease-" + targetStatus,
				Steps:  []domain.FakeStep{{Kind: scenarioKind, Message: "retain the exact reservation"}},
			}
			created := createRunTest(t, storage, workspace.ID, assigned, scenario, "reserved-run-"+targetStatus)
			advanceReservedRun(t, storage, workspace.ID, created, targetStatus)

			now = now.Add(10 * time.Minute)
			if count, err := storage.ReconcileExpiredAssignments(context.Background(), workspace.ID, "reconcile-reserved-assignment-"+targetStatus); err != nil || count != 0 {
				t.Fatalf("ReconcileExpiredAssignments(%s) = %d, %v; want no release", targetStatus, count, err)
			}
			if count, err := storage.ReconcileExpiredClaims(context.Background(), workspace.ID, "reconcile-reserved-claim-"+targetStatus); err != nil || count != 0 {
				t.Fatalf("ReconcileExpiredClaims(%s) = %d, %v; want no release", targetStatus, count, err)
			}

			var assignmentStatus, claimStatus string
			if err := storage.db.QueryRow(`SELECT status FROM task_assignments WHERE id=?`, assigned.Assignment.ID).Scan(&assignmentStatus); err != nil {
				t.Fatalf("read assignment status = %v", err)
			}
			if err := storage.db.QueryRow(`SELECT status FROM work_claims WHERE id=?`, claim.Claim.ID).Scan(&claimStatus); err != nil {
				t.Fatalf("read claim status = %v", err)
			}
			if assignmentStatus != domain.AssignmentActive || claimStatus != domain.ClaimActive {
				t.Fatalf("reserved %s released authority: assignment=%q claim=%q", targetStatus, assignmentStatus, claimStatus)
			}
			detail, err := storage.TaskDetail(context.Background(), workspace.ID, assigned.Task.ID)
			if err != nil {
				t.Fatalf("TaskDetail(%s) = %v", targetStatus, err)
			}
			if detail.Task.AssignmentID != assigned.Assignment.ID {
				t.Fatalf("reserved %s task assignment = %q, want %q", targetStatus, detail.Task.AssignmentID, assigned.Assignment.ID)
			}
		})
	}
}

func TestSupervisorExpiresUnreservedClaimBeforeScheduling(t *testing.T) {
	observed := time.Date(2034, 5, 6, 7, 8, 9, 0, time.UTC)
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 8,
		SharedTargetCheckout: true,
		Clock: func() time.Time {
			observed = observed.Add(time.Microsecond)
			return observed
		},
	})
	acceptAdversarialSchedulingPair(t, storage, fixture, "expired-unreserved-claim", domain.ClaimPolicyPauseScheduling)
	blocking, err := storage.AddClaim(context.Background(), AddClaimCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		TaskID:              fixture.planning.Task.ID,
		Kind:                domain.ClaimKindComponent,
		Target:              "shared-renderer",
		Mode:                domain.ClaimModeExclusive,
		ConflictPolicy:      domain.ClaimPolicyPauseScheduling,
		LeaseDuration:       time.Second,
		IdempotencyKey:      "expired-unreserved-blocker",
		CorrelationID:       "request-expired-unreserved-blocker",
	})
	if err != nil {
		t.Fatalf("AddClaim(expiring unreserved blocker) = %v", err)
	}
	observed = observed.Add(2 * time.Second)
	configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
	}, "expired-unreserved-claim")

	result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		Limit:               100,
		IdempotencyKey:      "expired-unreserved-claim-scan",
		CorrelationID:       "request-expired-unreserved-claim-scan",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(expired unreserved claim) = %v", err)
	}
	if len(result.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(expired unreserved claim) scheduled %d runs, want one after expiry reconciliation: %#v", len(result.ScheduledRunIDs), result)
	}
	var status string
	if err := storage.db.QueryRow(`SELECT status FROM work_claims WHERE id=?`, blocking.Claim.ID).Scan(&status); err != nil {
		t.Fatalf("read expired unreserved claim = %v", err)
	}
	if status != domain.ClaimExpired {
		t.Fatalf("unreserved blocking claim status = %q, want %q", status, domain.ClaimExpired)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE type='claim.expired' AND entity_id=?`, blocking.Claim.ID)
}

func advanceReservedRun(t *testing.T, storage *Store, workspaceID string, created domain.RunDetail, targetStatus string) {
	t.Helper()
	ctx := context.Background()
	run := created.Run
	if targetStatus == domain.RunRequested {
		return
	}
	_, err := storage.MarkRunStarting(ctx, run.ID, "reserved-starting-"+targetStatus)
	if err != nil {
		t.Fatalf("MarkRunStarting(%s) = %v", targetStatus, err)
	}
	if targetStatus == domain.RunStarting {
		return
	}
	if targetStatus == domain.RunLost {
		if _, err := storage.LoseRun(ctx, run.ID, "runtime identity is uncertain", "reserved-lost"); err != nil {
			t.Fatalf("LoseRun() = %v", err)
		}
		return
	}
	active, err := storage.MarkRunStarted(ctx, run.ID, "runtime-"+targetStatus, "provider-"+targetStatus, "reserved-active-"+targetStatus)
	if err != nil {
		t.Fatalf("MarkRunStarted(%s) = %v", targetStatus, err)
	}
	if targetStatus == domain.RunActive {
		return
	}
	if targetStatus == domain.RunStopping {
		if _, err := storage.RequestRunStop(ctx, StopRunCommand{
			WorkspaceIdentifier: workspaceID,
			RunID:               run.ID,
			ExpectedRevision:    active.Run.Revision,
			GracePeriodMillis:   1000,
			IdempotencyKey:      "reserved-stop",
			CorrelationID:       "request-reserved-stop",
		}); err != nil {
			t.Fatalf("RequestRunStop() = %v", err)
		}
		return
	}
	if targetStatus == domain.RunBlocked {
		if _, err := storage.ApplyRunObservation(ctx, run.ID, domain.RunObservation{
			Kind:    domain.ObservationBlocked,
			Message: "retain the exact reservation",
		}, true, nil, "reserved-blocked"); err != nil {
			t.Fatalf("ApplyRunObservation(blocked) = %v", err)
		}
		return
	}
	t.Fatalf("unsupported reserved status %q", targetStatus)
}
