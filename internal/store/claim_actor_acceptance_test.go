package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestM19ClaimScanFactsUseClaimWatcherSubsystemProvenance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	checkout := claimTestCheckout(t, storage, workspace.ID, project.ID)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "claim watcher provenance", "m19-claim-watcher-task")
	claim := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID,
		ProjectIdentifier:   project.ID,
		TaskID:              task.Task.ID,
		CheckoutIdentifier:  checkout.ID,
		Kind:                domain.ClaimKindPath,
		Target:              "src/**",
		Mode:                domain.ClaimModeExclusive,
		ConflictPolicy:      domain.ClaimPolicyNotify,
		LeaseDuration:       time.Hour,
		IdempotencyKey:      "m19-claim-watcher-claim",
		CorrelationID:       "m19-claim-watcher-claim",
	})

	const correlationID = "m19-claim-watcher-scan"
	scan, err := storage.RecordCheckoutClaimScan(ctx, RecordCheckoutClaimScanCommand{
		CheckoutID:    checkout.ID,
		WatcherID:     "daemon-instance-opaque-and-inert",
		HeadCommit:    checkout.HeadCommit,
		DirtyPaths:    []string{"docs/outside-claim.md"},
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if scan.DriftsOpened != 1 || scan.DriftsResolved != 0 {
		t.Fatalf("claim watcher scan = %#v, want one opened drift", scan)
	}

	var checkoutUpdatedBy string
	if err := storage.db.QueryRowContext(ctx, `SELECT updated_by FROM checkouts WHERE id = ?`, checkout.ID).Scan(&checkoutUpdatedBy); err != nil {
		t.Fatal(err)
	}
	if checkoutUpdatedBy != "subsystem:claim-watcher" {
		t.Fatalf("checkout.updated_by = %q, want subsystem:claim-watcher", checkoutUpdatedBy)
	}

	rows, err := storage.db.QueryContext(ctx, `
SELECT type, actor_id, actor_type
FROM events
WHERE correlation_id = ?
ORDER BY sequence`, correlationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantTypes := []string{"checkout.git_observed", "claim.drift_opened"}
	index := 0
	for rows.Next() {
		var eventType, actorID, actorType string
		if err := rows.Scan(&eventType, &actorID, &actorType); err != nil {
			t.Fatal(err)
		}
		if index >= len(wantTypes) {
			t.Fatalf("claim watcher emitted unexpected additional event %q", eventType)
		}
		if eventType != wantTypes[index] || actorID != "subsystem:claim-watcher" || actorType != domain.EventActorSubsystem {
			t.Fatalf("claim watcher event %d = %q actor %q/%q, want %q actor subsystem:claim-watcher/subsystem", index, eventType, actorID, actorType, wantTypes[index])
		}
		index++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if index != len(wantTypes) {
		t.Fatalf("claim watcher emitted %d correlated events, want %d for claim %s", index, len(wantTypes), claim.Claim.ID)
	}
}
