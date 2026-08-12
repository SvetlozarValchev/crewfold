package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestClaimsPersistDeterministicOverlapsAndPolicyResponses(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, project := initializeWorkTestProject(t, storage)
	checkout := claimTestCheckout(t, storage, workspace.ID, project.ID)
	firstTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "contact owner", "contact-owner")
	secondTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "cache owner", "cache-owner")

	first := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: firstTask.Task.ID, CheckoutIdentifier: checkout.ID,
		Kind: domain.ClaimKindPath, Target: "src/contact/**", Mode: domain.ClaimModeExclusive,
		ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Hour, IdempotencyKey: "claim-contact", CorrelationID: "request-claim-contact",
	})
	second := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: secondTask.Task.ID, CheckoutIdentifier: checkout.ID,
		Kind: domain.ClaimKindPath, Target: "src/contact/cache.go", Mode: domain.ClaimModeExclusive,
		ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Hour, IdempotencyKey: "claim-cache", CorrelationID: "request-claim-cache",
	})
	if len(second.Overlaps) != 1 || second.Overlaps[0].Severity != domain.OverlapSeverityCritical || second.Overlaps[0].PolicyResponse != domain.ClaimPolicyNotify || second.Overlaps[0].Witness != "src/contact/cache.go" || len(second.Overlaps[0].Explanation) != 4 {
		t.Fatalf("second claim overlaps = %#v", second.Overlaps)
	}
	inspected, err := storage.Overlap(context.Background(), workspace.ID, second.Overlaps[0].ID, "inspect-overlap")
	if err != nil || inspected.ID != second.Overlaps[0].ID {
		t.Fatalf("Overlap() = %#v, %v", inspected, err)
	}

	thirdTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "denied owner", "denied-owner")
	_, err = storage.AddClaim(context.Background(), AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: thirdTask.Task.ID, CheckoutIdentifier: checkout.ID,
		Kind: domain.ClaimKindPath, Target: "src/contact/*.go", Mode: domain.ClaimModeShared,
		ConflictPolicy: domain.ClaimPolicyDenyNew, LeaseDuration: time.Hour, IdempotencyKey: "claim-denied", CorrelationID: "request-claim-denied",
	})
	if ErrorCode(err) != CodeClaimConflict {
		t.Fatalf("AddClaim(deny_new) error = %v, code = %q", err, ErrorCode(err))
	}
	claims, err := storage.ListClaims(context.Background(), workspace.ID, project.ID, domain.ClaimActive, "list-active-claims")
	if err != nil || len(claims) != 2 {
		t.Fatalf("ListClaims() = %#v, %v", claims, err)
	}

	released, err := storage.ReleaseClaim(context.Background(), ReleaseClaimCommand{WorkspaceIdentifier: workspace.ID, ClaimID: second.Claim.ID, ExpectedRevision: second.Claim.Revision, IdempotencyKey: "release-cache", CorrelationID: "request-release-cache"})
	if err != nil || released.Claim.Status != domain.ClaimReleased || len(released.Overlaps) != 1 || released.Overlaps[0].Status != domain.OverlapResolved {
		t.Fatalf("ReleaseClaim() = %#v, %v", released, err)
	}
	if first.Claim.Status != domain.ClaimActive {
		t.Fatalf("first claim unexpectedly changed: %#v", first.Claim)
	}
}

func TestPauseSchedulingClaimBlocksRunUntilResolution(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	firstTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "component reviewer", "component-reviewer")
	secondTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "component implementer", "component-implementer")
	addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: firstTask.Task.ID,
		Kind: domain.ClaimKindComponent, Target: "contact-service", Mode: domain.ClaimModeShared,
		ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Hour, IdempotencyKey: "claim-component-review", CorrelationID: "request-claim-component-review",
	})
	paused := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: secondTask.Task.ID,
		Kind: domain.ClaimKindComponent, Target: "contact-service", Mode: domain.ClaimModeExclusive,
		ConflictPolicy: domain.ClaimPolicyPauseScheduling, LeaseDuration: time.Hour, IdempotencyKey: "claim-component-build", CorrelationID: "request-claim-component-build",
	})
	if len(paused.Overlaps) != 1 || !paused.Overlaps[0].SchedulingPaused || !paused.Overlaps[0].ResolutionRequired || paused.Overlaps[0].Severity != domain.OverlapSeverityHigh {
		t.Fatalf("paused overlap = %#v", paused.Overlaps)
	}
	agent, err := storage.CreateAgent(context.Background(), CreateAgentCommand{WorkspaceIdentifier: workspace.ID, Name: "builder", Role: "implementer", Provider: "fake", Runtime: "fake", IdempotencyKey: "pause-builder", CorrelationID: "request-pause-builder"})
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: secondTask.Task.ID, AgentIdentifier: agent.Value.ID, LeaseSeconds: 300, ExpectedRevision: secondTask.Task.Revision, IdempotencyKey: "assign-paused", CorrelationID: "request-assign-paused"})
	if err != nil {
		t.Fatalf("AssignTask() error = %v", err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "paused-run", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "work"}}}
	_, err = storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: secondTask.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Detail.Task.Revision, IdempotencyKey: "start-paused", CorrelationID: "request-start-paused"})
	if ErrorCode(err) != CodeSchedulingPaused {
		t.Fatalf("CreateRun(paused) error = %v, code = %q", err, ErrorCode(err))
	}
	if _, err := storage.ReleaseClaim(context.Background(), ReleaseClaimCommand{WorkspaceIdentifier: workspace.ID, ClaimID: paused.Claim.ID, ExpectedRevision: paused.Claim.Revision, IdempotencyKey: "release-paused", CorrelationID: "request-release-paused"}); err != nil {
		t.Fatalf("ReleaseClaim(paused) error = %v", err)
	}
	if _, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: secondTask.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Detail.Task.Revision, IdempotencyKey: "start-after-resolution", CorrelationID: "request-start-after-resolution"}); err != nil {
		t.Fatalf("CreateRun(after resolution) error = %v", err)
	}
}

func TestClaimScanDetectsDriftGapWithoutRewritingScope(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	checkout := claimTestCheckout(t, storage, workspace.ID, project.ID)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "source owner", "source-owner")
	claim := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: task.Task.ID, CheckoutIdentifier: checkout.ID,
		Kind: domain.ClaimKindPath, Target: "src/**", Mode: domain.ClaimModeExclusive,
		ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Hour, IdempotencyKey: "claim-source", CorrelationID: "request-claim-source",
	})

	first, err := storage.RecordCheckoutClaimScan(context.Background(), RecordCheckoutClaimScanCommand{CheckoutID: checkout.ID, WatcherID: "watcher-one", HeadCommit: checkout.HeadCommit, DirtyPaths: []string{"src/inside.go"}, CorrelationID: "scan-one"})
	if err != nil || first.ObservationGap || first.DriftsOpened != 0 {
		t.Fatalf("first scan = %#v, %v", first, err)
	}
	second, err := storage.RecordCheckoutClaimScan(context.Background(), RecordCheckoutClaimScanCommand{CheckoutID: checkout.ID, WatcherID: "watcher-two", HeadCommit: checkout.HeadCommit, DirtyPaths: []string{"docs/outside.md", "src/inside.go"}, CorrelationID: "scan-after-restart"})
	if err != nil || !second.ObservationGap || second.DriftsOpened != 1 {
		t.Fatalf("restart scan = %#v, %v", second, err)
	}
	drifts, err := storage.ListClaimDrifts(context.Background(), workspace.ID, domain.DriftOpen)
	if err != nil || len(drifts) != 1 || drifts[0].Path != "docs/outside.md" || !drifts[0].ObservationGap || drifts[0].ClaimID != claim.Claim.ID {
		t.Fatalf("ListClaimDrifts(open) = %#v, %v", drifts, err)
	}
	claims, err := storage.ListClaims(context.Background(), workspace.ID, project.ID, domain.ClaimActive, "verify-declared-scope")
	if err != nil || len(claims) != 1 || claims[0].Target != "src/**" {
		t.Fatalf("claim scope after drift = %#v, %v", claims, err)
	}
	third, err := storage.RecordCheckoutClaimScan(context.Background(), RecordCheckoutClaimScanCommand{CheckoutID: checkout.ID, WatcherID: "watcher-two", HeadCommit: checkout.HeadCommit, DirtyPaths: []string{"src/inside.go"}, CorrelationID: "scan-resolved"})
	if err != nil || third.ObservationGap || third.DriftsResolved != 1 {
		t.Fatalf("resolution scan = %#v, %v", third, err)
	}

	adjacent, err := storage.AddCheckout(context.Background(), AddCheckoutCommand{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, WriteMode: domain.WriteModeExclusive, IdempotencyKey: "claim-adjacent", CorrelationID: "request-claim-adjacent", Observation: sourceTestObservation(filepath.Join(t.TempDir(), "world-engine-2"), "other")})
	if err != nil {
		t.Fatalf("AddCheckout(adjacent) error = %v", err)
	}
	adjacentScan, err := storage.RecordCheckoutClaimScan(context.Background(), RecordCheckoutClaimScanCommand{CheckoutID: adjacent.Checkout.ID, WatcherID: "watcher-two", HeadCommit: adjacent.Checkout.HeadCommit, DirtyPaths: []string{"docs/outside.md"}, CorrelationID: "scan-adjacent"})
	if err != nil || adjacentScan.DriftsOpened != 0 {
		t.Fatalf("adjacent scan = %#v, %v", adjacentScan, err)
	}
	targets, err := storage.ClaimWatchTargets(context.Background())
	if err != nil || len(targets) != 1 || targets[0].CheckoutID != checkout.ID {
		t.Fatalf("ClaimWatchTargets() = %#v, %v", targets, err)
	}
}

func TestClaimLeaseExpiresAfterStoreRestartWithControlledClock(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	storage, err := Open(context.Background(), dataDir, Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	workspace, project := initializeWorkTestProject(t, storage)
	checkout := claimTestCheckout(t, storage, workspace.ID, project.ID)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "short owner", "short-owner")
	added := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: task.Task.ID, CheckoutIdentifier: checkout.ID,
		Kind: domain.ClaimKindPath, Target: "cmd/**", LeaseDuration: time.Second, IdempotencyKey: "short-claim", CorrelationID: "request-short-claim",
	})
	if err := storage.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	now = now.Add(2 * time.Second)
	storage, err = Open(context.Background(), dataDir, Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Open(after restart) error = %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	claims, err := storage.ListClaims(context.Background(), workspace.ID, project.ID, domain.ClaimExpired, "reconcile-after-restart")
	if err != nil || len(claims) != 1 || claims[0].ID != added.Claim.ID || claims[0].Revision != 2 {
		t.Fatalf("expired claims = %#v, %v", claims, err)
	}
}

func TestClaimMutationFailureRollsBackProjectionAndEvents(t *testing.T) {
	t.Parallel()
	injected := errors.New("injected claim persistence failure")
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	checkout := claimTestCheckout(t, storage, workspace.ID, project.ID)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "rollback owner", "rollback-owner")
	storage.mutationHook = func(stage string) error {
		if stage == MutationAfterProjection {
			return injected
		}
		return nil
	}
	var beforeEvents int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&beforeEvents); err != nil {
		t.Fatalf("count events before mutation: %v", err)
	}
	_, err := storage.AddClaim(context.Background(), AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: task.Task.ID, CheckoutIdentifier: checkout.ID,
		Kind: domain.ClaimKindPath, Target: "pkg/**", LeaseDuration: time.Hour, IdempotencyKey: "rollback-claim", CorrelationID: "request-rollback-claim",
	})
	if !errors.Is(err, injected) {
		t.Fatalf("AddClaim() error = %v, want injected error", err)
	}
	var claims, overlaps, afterEvents int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM work_claims").Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM work_overlaps").Scan(&overlaps); err != nil {
		t.Fatalf("count overlaps: %v", err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&afterEvents); err != nil {
		t.Fatalf("count events after mutation: %v", err)
	}
	if claims != 0 || overlaps != 0 || afterEvents != beforeEvents {
		t.Fatalf("partial mutation persisted: claims=%d overlaps=%d events before=%d after=%d", claims, overlaps, beforeEvents, afterEvents)
	}
}

func addClaimTest(t *testing.T, storage *Store, command AddClaimCommand) ClaimMutationResult {
	t.Helper()
	result, err := storage.AddClaim(context.Background(), command)
	if err != nil {
		t.Fatalf("AddClaim(%s) error = %v", command.Target, err)
	}
	return result
}

func claimTestCheckout(t *testing.T, storage *Store, workspaceID, projectID string) domain.Checkout {
	t.Helper()
	inspection, err := storage.InspectProject(context.Background(), workspaceID, projectID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	return inspection.Checkouts[0]
}
