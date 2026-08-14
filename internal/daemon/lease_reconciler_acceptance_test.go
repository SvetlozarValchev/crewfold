package daemon

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func TestM19LeaseReconcilerConfigurationIsBoundedAndCurrent(t *testing.T) {
	t.Parallel()
	base := testConfig(t)
	base.DisableRunWorker = true
	base.DisableCheckWorker = true
	base.DisableCheckWatcher = true
	base.DisableClaimWatcher = true
	base.DisableSupervisor = true

	resolved, err := resolveConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.DisableLeaseReconciler || resolved.LeaseReconcileInterval != 2*time.Second {
		t.Fatalf("default lease reconciler = disabled %t interval %s", resolved.DisableLeaseReconciler, resolved.LeaseReconcileInterval)
	}
	tooFast := base
	tooFast.LeaseReconcileInterval = 19 * time.Millisecond
	if _, err := resolveConfig(tooFast); ErrorCode(err) != CodeInvalidConfiguration {
		t.Fatalf("resolveConfig(too-fast lease reconciler) = %v, code %q", err, ErrorCode(err))
	}
	disabled := tooFast
	disabled.DisableLeaseReconciler = true
	if _, err := resolveConfig(disabled); err != nil {
		t.Fatalf("resolveConfig(disabled lease reconciler) = %v", err)
	}
}

func TestM19LeaseReconcilerOwnsElapsedFactsAndRestartIsIdempotent(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	config := testConfig(t)
	config.StoreOptions.Clock = func() time.Time { return now }
	config.DisableRunWorker = true
	config.DisableCheckWorker = true
	config.DisableCheckWatcher = true
	config.DisableClaimWatcher = true
	config.DisableSupervisor = true
	config.LeaseReconcileInterval = 20 * time.Millisecond
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	storage, err := store.Open(ctx, config.DataDir, config.StoreOptions)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{
		Name: "personal", IdempotencyKey: "m19-lease-workspace", CorrelationID: "m19-lease-workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkoutPath := filepath.Join(t.TempDir(), "operator-fixture")
	project, err := storage.RegisterProject(ctx, store.RegisterProjectCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, Name: "operator-fixture", WriteMode: domain.WriteModeExclusive,
		IdempotencyKey: "m19-lease-project", CorrelationID: "m19-lease-project",
		Observation: domain.CheckoutObservation{
			Path: checkoutPath, Availability: domain.CheckoutAvailable, CheckoutKind: domain.CheckoutStandalone,
			Branch: "main", HeadCommit: "2222222222222222222222222222222222222222",
			GitDir: filepath.Join(checkoutPath, ".git"), GitCommonDir: filepath.Join(checkoutPath, ".git"),
			Repository: domain.RepositoryObservation{
				Fingerprint:  "git_1111111111111111111111111111111111111111111111111111111111111111",
				ObjectFormat: "sha1", RootCommits: []string{"0000000000000000000000000000000000000000"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := storage.CreateAgent(ctx, store.CreateAgentCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, Name: "fixture-agent", Role: "manager-owner-approver",
		Provider: "fake", Runtime: "fake", IdempotencyKey: "m19-lease-agent", CorrelationID: "m19-lease-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := storage.CreateTask(ctx, store.CreateTaskCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, ProjectIdentifier: project.Project.ID,
		Title: "elapsed operator work", Priority: 100,
		IdempotencyKey: "m19-lease-task", CorrelationID: "m19-lease-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := storage.AssignTask(ctx, store.AssignTaskCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, TaskID: task.Detail.Task.ID, AgentIdentifier: agent.Value.ID,
		LeaseSeconds: 60, ExpectedRevision: task.Detail.Task.Revision,
		IdempotencyKey: "m19-lease-assignment", CorrelationID: "m19-lease-assignment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if assigned.Detail.Assignment == nil {
		t.Fatal("assigned task did not return its durable assignment")
	}
	secondTask, err := storage.CreateTask(ctx, store.CreateTaskCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, ProjectIdentifier: project.Project.ID,
		Title: "overlapping elapsed operator work", Priority: 99,
		IdempotencyKey: "m19-lease-overlap-task", CorrelationID: "m19-lease-overlap-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstClaim, err := storage.AddClaim(ctx, store.AddClaimCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, ProjectIdentifier: project.Project.ID,
		TaskID: task.Detail.Task.ID, CheckoutIdentifier: project.Checkout.ID,
		Kind: domain.ClaimKindPath, Target: "src/**", Mode: domain.ClaimModeExclusive,
		ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Minute,
		IdempotencyKey: "m19-lease-claim", CorrelationID: "m19-lease-claim",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondClaim, err := storage.AddClaim(ctx, store.AddClaimCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, ProjectIdentifier: project.Project.ID,
		TaskID: secondTask.Detail.Task.ID, CheckoutIdentifier: project.Checkout.ID,
		Kind: domain.ClaimKindPath, Target: "src/exact.go", Mode: domain.ClaimModeExclusive,
		ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Minute,
		IdempotencyKey: "m19-lease-overlap-claim", CorrelationID: "m19-lease-overlap-claim",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstClaim.Overlaps) != 0 || len(secondClaim.Overlaps) != 1 {
		t.Fatalf("elapsed claim fixture overlaps = first %d second %d, want 0/1", len(firstClaim.Overlaps), len(secondClaim.Overlaps))
	}
	overlapID := secondClaim.Overlaps[0].ID
	initial, err := storage.ListEvents(ctx, store.ListEventsQuery{
		WorkspaceIdentifier: workspace.Workspace.ID, After: 0, Limit: store.MaximumEventPageLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Minute)
	first := startTestServer(t, config)
	database, err := sql.Open("sqlite3", filepath.Join(config.DataDir, "crewfold.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var taskStatus, assignmentStatus, overlapStatus string
	var expiredClaims, assignmentEvents, claimEvents, overlapEvents, highWater int64
	waitForCondition(t, 5*time.Second, func() bool {
		if err := database.QueryRow(`SELECT status FROM tasks WHERE id = ?`, task.Detail.Task.ID).Scan(&taskStatus); err != nil {
			return false
		}
		if err := database.QueryRow(`SELECT status FROM task_assignments WHERE id = ?`, assigned.Detail.Assignment.ID).Scan(&assignmentStatus); err != nil {
			return false
		}
		if err := database.QueryRow(`SELECT COUNT(*) FROM work_claims WHERE project_id = ? AND status = 'expired'`, project.Project.ID).Scan(&expiredClaims); err != nil {
			return false
		}
		if err := database.QueryRow(`SELECT status FROM work_overlaps WHERE id = ?`, overlapID).Scan(&overlapStatus); err != nil {
			return false
		}
		if err := database.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'task.assignment_expired'`).Scan(&assignmentEvents); err != nil {
			return false
		}
		if err := database.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'claim.expired'`).Scan(&claimEvents); err != nil {
			return false
		}
		if err := database.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'overlap.resolved'`).Scan(&overlapEvents); err != nil {
			return false
		}
		return taskStatus == domain.TaskReady && assignmentStatus == "expired" && expiredClaims == 2 && overlapStatus == domain.OverlapResolved &&
			assignmentEvents == 1 && claimEvents == 2 && overlapEvents == 1
	}, "lease reconciler to own elapsed assignment, claim, and overlap-resolution facts")
	assertM19LeaseProjectionProvenance(t, database, task.Detail.Task.ID, assigned.Detail.Assignment.ID, []string{firstClaim.Claim.ID, secondClaim.Claim.ID}, overlapID)
	if err := database.QueryRow(`SELECT COALESCE(MAX(sequence),0) FROM events`).Scan(&highWater); err != nil {
		t.Fatal(err)
	}
	if highWater != initial.HighWater+4 {
		t.Fatalf("reconciled high-water = %d, want initial %d + exactly four events", highWater, initial.HighWater)
	}

	// Repeated enabled ticks are exact no-ops once the elapsed rows have moved.
	time.Sleep(100 * time.Millisecond)
	assertM19LeaseEventCounts(t, database, highWater)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.wait(); err != nil {
		t.Fatal(err)
	}

	// Restart performs its immediate sweep over the same durable projections and
	// must not create a second expiry event under a new subsystem correlation ID.
	second := startTestServer(t, config)
	time.Sleep(100 * time.Millisecond)
	assertM19LeaseEventCounts(t, database, highWater)
	if _, err := client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := second.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestM19DaemonStopCancelsBlockedLeaseReconciliation(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	reconcileContext, cancelReconciliation := context.WithCancel(context.Background())
	entered := make(chan struct{}, 1)
	blockClock := false
	config := testConfig(t)
	config.StoreOptions.Clock = func() time.Time {
		if blockClock {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-reconcileContext.Done()
		}
		return now
	}
	storage, err := store.Open(context.Background(), t.TempDir(), config.StoreOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	if _, err := storage.InitWorkspace(context.Background(), store.InitWorkspaceCommand{
		Name: "personal", IdempotencyKey: "m19-cancel-workspace", CorrelationID: "m19-cancel-workspace",
	}); err != nil {
		t.Fatal(err)
	}

	instance := &server{
		config: config, store: storage, startedAt: now, stopCh: make(chan struct{}),
		leaseReconcileCtx: reconcileContext, leaseReconcileCancel: cancelReconciliation,
	}
	blockClock = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		instance.runLeaseReconciliationSweep(reconcileContext)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("lease reconciliation did not enter the deliberately blocked Store operation")
	}
	instance.requestStop("test cancellation")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("daemon stop did not cancel the blocked lease reconciliation sweep")
	}
	if reconcileContext.Err() == nil {
		t.Fatal("daemon stop left the lease reconciliation context live")
	}
}

func assertM19LeaseEventCounts(t *testing.T, database *sql.DB, wantHighWater int64) {
	t.Helper()
	var assignments, claims, overlaps, leaseActors, wrongActors, highWater int64
	if err := database.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'task.assignment_expired'`).Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'claim.expired'`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'overlap.resolved'`).Scan(&overlaps); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`
SELECT COUNT(*) FROM events
WHERE type IN ('task.assignment_expired','claim.expired','overlap.resolved')
  AND actor_id = 'subsystem:lease' AND actor_type = 'subsystem'`).Scan(&leaseActors); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`
SELECT COUNT(*) FROM events
WHERE type IN ('task.assignment_expired','claim.expired','overlap.resolved')
  AND (actor_id <> 'subsystem:lease' OR actor_type <> 'subsystem')`).Scan(&wrongActors); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COALESCE(MAX(sequence),0) FROM events`).Scan(&highWater); err != nil {
		t.Fatal(err)
	}
	if assignments != 1 || claims != 2 || overlaps != 1 || leaseActors != 4 || wrongActors != 0 || highWater != wantHighWater {
		t.Fatalf("lease reconciliation churned or lost provenance: assignments=%d claims=%d overlaps=%d lease-actors=%d wrong-actors=%d high-water=%d, want 1/2/1/4/0/%d", assignments, claims, overlaps, leaseActors, wrongActors, highWater, wantHighWater)
	}
}

func assertM19LeaseProjectionProvenance(t *testing.T, database *sql.DB, taskID, assignmentID string, claimIDs []string, overlapID string) {
	t.Helper()
	var taskStatus, taskUpdatedBy, assignmentStatus, assignmentUpdatedBy string
	if err := database.QueryRow(`SELECT status, updated_by FROM tasks WHERE id = ?`, taskID).Scan(&taskStatus, &taskUpdatedBy); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT status, updated_by FROM task_assignments WHERE id = ?`, assignmentID).Scan(&assignmentStatus, &assignmentUpdatedBy); err != nil {
		t.Fatal(err)
	}
	if taskStatus != domain.TaskReady || taskUpdatedBy != "subsystem:lease" || assignmentStatus != "expired" || assignmentUpdatedBy != "subsystem:lease" {
		t.Fatalf("assignment expiry projections = task %q/%q assignment %q/%q, want ready/subsystem:lease expired/subsystem:lease", taskStatus, taskUpdatedBy, assignmentStatus, assignmentUpdatedBy)
	}
	for _, claimID := range claimIDs {
		var status, updatedBy string
		if err := database.QueryRow(`SELECT status, updated_by FROM work_claims WHERE id = ?`, claimID).Scan(&status, &updatedBy); err != nil {
			t.Fatal(err)
		}
		if status != domain.ClaimExpired || updatedBy != "subsystem:lease" {
			t.Fatalf("claim %s expiry projection = %q/%q, want expired/subsystem:lease", claimID, status, updatedBy)
		}
	}
	var overlapStatus, resolutionReason string
	var overlapRevision int64
	if err := database.QueryRow(`SELECT status, resolution_reason, revision FROM work_overlaps WHERE id = ?`, overlapID).Scan(&overlapStatus, &resolutionReason, &overlapRevision); err != nil {
		t.Fatal(err)
	}
	if overlapStatus != domain.OverlapResolved || resolutionReason != "claim lease expired" || overlapRevision != 2 {
		t.Fatalf("overlap expiry projection = %q/%q/rev%d, want resolved/claim lease expired/rev2", overlapStatus, resolutionReason, overlapRevision)
	}
}
