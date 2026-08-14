package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestM19AddClaimReplayAfterLeaseExpiryHasNoReconciliationSideEffect(t *testing.T) {
	observed := time.Date(2035, 5, 6, 7, 8, 9, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return observed }})
	workspace, project := initializeWorkTestProject(t, storage)
	checkout := claimTestCheckout(t, storage, workspace.ID, project.ID)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "elapsed add-claim replay", "elapsed-add-claim-replay")
	command := AddClaimCommand{
		WorkspaceIdentifier: workspace.ID,
		ProjectIdentifier:   project.ID,
		TaskID:              task.Task.ID,
		CheckoutIdentifier:  checkout.ID,
		Kind:                domain.ClaimKindPath,
		Target:              "internal/replay/**",
		Mode:                domain.ClaimModeExclusive,
		ConflictPolicy:      domain.ClaimPolicyNotify,
		LeaseDuration:       time.Second,
		IdempotencyKey:      "claim-add-elapsed-replay",
		CorrelationID:       "request-elapsed-add-claim-replay",
	}
	added, err := storage.AddClaim(context.Background(), command)
	if err != nil || added.Claim.Status != domain.ClaimActive || added.Replayed {
		t.Fatalf("AddClaim() = %#v, %v", added, err)
	}

	observed = observed.Add(2 * time.Second)
	beforeEvents := m19StoreEventCount(t, storage)
	replayed, err := storage.AddClaim(context.Background(), command)
	wantReplay := added
	wantReplay.Replayed = true
	if err != nil || !reflect.DeepEqual(replayed, wantReplay) {
		t.Fatalf("AddClaim(elapsed exact replay) = %#v, %v; want frozen %#v", replayed, err, wantReplay)
	}
	m19AssertClaimReplayDidNotReconcile(t, storage, added.Claim.ID, domain.ClaimActive, added.Claim.Revision, beforeEvents)

	changed := command
	changed.Target = "internal/replay/changed/**"
	if _, err := storage.AddClaim(context.Background(), changed); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("AddClaim(changed elapsed replay) error = %v, code = %q", err, ErrorCode(err))
	}
	m19AssertClaimReplayDidNotReconcile(t, storage, added.Claim.ID, domain.ClaimActive, added.Claim.Revision, beforeEvents)
	var addedEvents, expiredEvents int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE entity_id = ? AND type = 'claim.added'", added.Claim.ID).Scan(&addedEvents); err != nil {
		t.Fatalf("count claim.added events: %v", err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE entity_id = ? AND type = 'claim.expired'", added.Claim.ID).Scan(&expiredEvents); err != nil {
		t.Fatalf("count claim.expired events: %v", err)
	}
	if addedEvents != 1 || expiredEvents != 0 {
		t.Fatalf("elapsed AddClaim replay events = added:%d expired:%d, want 1/0", addedEvents, expiredEvents)
	}
}

func TestM19ReleaseClaimReplayAfterLeaseExpiryHasNoReconciliationSideEffect(t *testing.T) {
	observed := time.Date(2035, 6, 7, 8, 9, 10, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return observed }})
	workspace, project := initializeWorkTestProject(t, storage)
	checkout := claimTestCheckout(t, storage, workspace.ID, project.ID)
	targetTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "release replay target", "release-replay-target")
	witnessTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "release replay elapsed witness", "release-replay-witness")
	target := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
		TaskID: targetTask.Task.ID, CheckoutIdentifier: checkout.ID,
		Kind: domain.ClaimKindPath, Target: "release/target/**", Mode: domain.ClaimModeExclusive,
		ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Hour,
		IdempotencyKey: "claim-release-target-fixture", CorrelationID: "request-release-replay-target",
	})
	witness := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
		TaskID: witnessTask.Task.ID, CheckoutIdentifier: checkout.ID,
		Kind: domain.ClaimKindPath, Target: "release/witness/**", Mode: domain.ClaimModeExclusive,
		ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Second,
		IdempotencyKey: "claim-release-witness-fixture", CorrelationID: "request-release-replay-witness",
	})
	releaseCommand := ReleaseClaimCommand{
		WorkspaceIdentifier: workspace.ID,
		ClaimID:             target.Claim.ID,
		ExpectedRevision:    target.Claim.Revision,
		IdempotencyKey:      "elapsed-release-claim-replay",
		CorrelationID:       "request-elapsed-release-claim-replay",
	}
	released, err := storage.ReleaseClaim(context.Background(), releaseCommand)
	if err != nil || released.Claim.Status != domain.ClaimReleased || released.Replayed {
		t.Fatalf("ReleaseClaim() = %#v, %v", released, err)
	}

	observed = observed.Add(2 * time.Second)
	beforeEvents := m19StoreEventCount(t, storage)
	replayed, err := storage.ReleaseClaim(context.Background(), releaseCommand)
	wantReplay := released
	wantReplay.Replayed = true
	if err != nil || !reflect.DeepEqual(replayed, wantReplay) {
		t.Fatalf("ReleaseClaim(elapsed exact replay) = %#v, %v; want frozen %#v", replayed, err, wantReplay)
	}
	m19AssertClaimReplayDidNotReconcile(t, storage, witness.Claim.ID, domain.ClaimActive, witness.Claim.Revision, beforeEvents)

	changed := releaseCommand
	changed.ExpectedRevision++
	if _, err := storage.ReleaseClaim(context.Background(), changed); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("ReleaseClaim(changed elapsed replay) error = %v, code = %q", err, ErrorCode(err))
	}
	m19AssertClaimReplayDidNotReconcile(t, storage, witness.Claim.ID, domain.ClaimActive, witness.Claim.Revision, beforeEvents)
	var releasedEvents, witnessExpiredEvents int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE entity_id = ? AND type = 'claim.released'", target.Claim.ID).Scan(&releasedEvents); err != nil {
		t.Fatalf("count claim.released events: %v", err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE entity_id = ? AND type = 'claim.expired'", witness.Claim.ID).Scan(&witnessExpiredEvents); err != nil {
		t.Fatalf("count witness claim.expired events: %v", err)
	}
	if releasedEvents != 1 || witnessExpiredEvents != 0 {
		t.Fatalf("elapsed ReleaseClaim replay events = released:%d witness-expired:%d, want 1/0", releasedEvents, witnessExpiredEvents)
	}
}

func m19AssertClaimReplayDidNotReconcile(t *testing.T, storage *Store, claimID, wantStatus string, wantRevision int64, wantEvents int) {
	t.Helper()
	if gotEvents := m19StoreEventCount(t, storage); gotEvents != wantEvents {
		t.Fatalf("claim replay appended reconciliation events: before=%d after=%d", wantEvents, gotEvents)
	}
	var status string
	var revision int64
	if err := storage.db.QueryRow("SELECT status, revision FROM work_claims WHERE id = ?", claimID).Scan(&status, &revision); err != nil {
		t.Fatalf("read claim after replay: %v", err)
	}
	if status != wantStatus || revision != wantRevision {
		t.Fatalf("claim replay reconciled projection = status:%q revision:%d, want %q/%d", status, revision, wantStatus, wantRevision)
	}
}
