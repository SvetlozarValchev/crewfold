package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryOperationsUseStableCancellationCodeBeforePublication(t *testing.T) {
	bundle := createRecoveryTestBundle(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifyBundle(cancelled, bundle); ErrorCode(err) != CodeOperationCancelled {
		t.Fatalf("VerifyBundle(cancelled) error = %v, code = %q", err, ErrorCode(err))
	}
	restoreTarget := filepath.Join(t.TempDir(), "cancelled-restore")
	if _, err := RestorePending(cancelled, bundle, restoreTarget); ErrorCode(err) != CodeOperationCancelled {
		t.Fatalf("RestorePending(cancelled) error = %v, code = %q", err, ErrorCode(err))
	}
	if _, err := os.Lstat(restoreTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled restore published a target: %v", err)
	}

	pendingTarget := restoreRecoveryTestBundle(t)
	if _, err := Activate(cancelled, pendingTarget, true); ErrorCode(err) != CodeOperationCancelled {
		t.Fatalf("Activate(cancelled) error = %v, code = %q", err, ErrorCode(err))
	}
	if _, err := os.Lstat(filepath.Join(pendingTarget, restoreActivationIntentMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled activation persisted an intent: %v", err)
	}

	repairTarget := createRepairTarget(t)
	if _, err := InspectOffline(cancelled, repairTarget); ErrorCode(err) != CodeOperationCancelled {
		t.Fatalf("InspectOffline(cancelled) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestManifestSafetyBoundsUseStableResourceLimitCode(t *testing.T) {
	verified, err := VerifyBundle(context.Background(), createRecoveryTestBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	manifest := verified.Manifest
	manifest.TotalBytes = maximumBundlePayloadBytes + 1
	if _, err := MarshalManifest(manifest); ErrorCode(err) != CodeResourceLimitExceeded {
		t.Fatalf("MarshalManifest(over bound) error = %v, code = %q", err, ErrorCode(err))
	}
	manifest = verified.Manifest
	manifest.Database.Size = maximumDatabaseSize + 1
	if _, err := MarshalManifest(manifest); ErrorCode(err) != CodeResourceLimitExceeded {
		t.Fatalf("MarshalManifest(database over bound) error = %v, code = %q", err, ErrorCode(err))
	}
}
