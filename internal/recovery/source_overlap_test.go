package recovery

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/store"
)

type recoveryTreeEntrySnapshot struct {
	Mode       fs.FileMode
	Size       int64
	SHA256     [sha256.Size]byte
	LinkTarget string
}

func snapshotRecoveryTree(t *testing.T, root string) map[string]recoveryTreeEntrySnapshot {
	t.Helper()
	result := make(map[string]recoveryTreeEntrySnapshot)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		snapshot := recoveryTreeEntrySnapshot{Mode: info.Mode(), Size: info.Size()}
		switch {
		case info.Mode().IsRegular():
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot.SHA256 = sha256.Sum256(content)
		case info.Mode()&os.ModeSymlink != 0:
			snapshot.LinkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		result[relative] = snapshot
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot recovery tree %s: %v", root, err)
	}
	return result
}

func assertRecoveryTreeUnchanged(t *testing.T, root string, before map[string]recoveryTreeEntrySnapshot) {
	t.Helper()
	after := snapshotRecoveryTree(t, root)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("recovery source tree changed after rejected operation:\nbefore = %#v\nafter  = %#v", before, after)
	}
}

func moveRecoveryFixtureBelowReservedStage(t *testing.T, source, selectedName string) string {
	t.Helper()
	parent := t.TempDir()
	reserved := filepath.Join(parent, recoveryStagingPrefix+strings.Repeat("d", sha256.Size*2))
	if err := os.Mkdir(reserved, 0o700); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(reserved, selectedName)
	if err := os.Rename(source, selected); err != nil {
		t.Fatal(err)
	}
	return selected
}

func TestCreateBundleRejectsTargetInsideSourceBeforeMutationAndAllowsSiblingPrefix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	parent := t.TempDir()
	dataDirectory := filepath.Join(parent, "crewfold")
	if err := os.Mkdir(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDirectory, store.Options{})
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	before := snapshotRecoveryTree(t, dataDirectory)
	inside := filepath.Join(dataDirectory, "nested-backup")
	if _, err := CreateBundle(ctx, storage, dataDirectory, inside, "inside-source"); ErrorCode(err) != CodeBackupTargetInvalid {
		t.Fatalf("CreateBundle(inside source) error = %v, code = %q", err, ErrorCode(err))
	}
	assertRecoveryTreeUnchanged(t, dataDirectory, before)

	siblingPrefix := dataDirectory + "2"
	if _, err := CreateBundle(ctx, storage, dataDirectory, siblingPrefix, "sibling-prefix"); err != nil {
		t.Fatalf("CreateBundle(sibling prefix) error = %v", err)
	}
	if _, err := VerifyBundle(ctx, siblingPrefix); err != nil {
		t.Fatalf("VerifyBundle(sibling prefix) error = %v", err)
	}
}

func TestRestorePendingRejectsTargetInsideBundleBeforeMutationAndAllowsSiblingPrefix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bundle := createRecoveryTestBundle(t)
	before := snapshotRecoveryTree(t, bundle)
	inside := filepath.Join(bundle, "restored")
	if _, err := RestorePending(ctx, bundle, inside); ErrorCode(err) != CodeBackupTargetInvalid {
		t.Fatalf("RestorePending(inside bundle) error = %v, code = %q", err, ErrorCode(err))
	}
	assertRecoveryTreeUnchanged(t, bundle, before)
	if _, err := VerifyBundle(ctx, bundle); err != nil {
		t.Fatalf("VerifyBundle(after rejected nested restore) error = %v", err)
	}

	siblingPrefix := bundle + "2"
	if _, err := RestorePending(ctx, bundle, siblingPrefix); err != nil {
		t.Fatalf("RestorePending(sibling prefix) error = %v", err)
	}
	if _, err := VerifyBundle(ctx, bundle); err != nil {
		t.Fatalf("VerifyBundle(after sibling restore) error = %v", err)
	}
}

func TestRecoveryTargetsRejectReservedParentNamesBeforeMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sourceData := t.TempDir()
	if err := os.Chmod(sourceData, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, sourceData, store.Options{})
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	bundle := createRecoveryTestBundle(t)

	reservedNames := []string{
		recoveryParentLockName,
		recoveryStagingPrefix + strings.Repeat("a", sha256.Size*2),
	}
	for _, name := range reservedNames {
		name := name
		t.Run(name, func(t *testing.T) {
			targetParent := t.TempDir()
			target := filepath.Join(targetParent, name)
			sourceBefore := snapshotRecoveryTree(t, sourceData)
			parentBefore := snapshotRecoveryTree(t, targetParent)
			if _, err := CreateBundle(ctx, storage, sourceData, target, "reserved-"+name); ErrorCode(err) != CodeBackupTargetInvalid {
				t.Fatalf("CreateBundle(reserved target) error = %v, code = %q", err, ErrorCode(err))
			}
			assertRecoveryTreeUnchanged(t, sourceData, sourceBefore)
			assertRecoveryTreeUnchanged(t, targetParent, parentBefore)
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("rejected backup target was created: %v", err)
			}

			if _, err := RestorePending(ctx, bundle, target); ErrorCode(err) != CodeBackupTargetInvalid {
				t.Fatalf("RestorePending(reserved target) error = %v, code = %q", err, ErrorCode(err))
			}
			assertRecoveryTreeUnchanged(t, targetParent, parentBefore)
			if _, err := VerifyBundle(ctx, bundle); err != nil {
				t.Fatalf("VerifyBundle(after reserved restore refusal) error = %v", err)
			}
		})
	}

	// Rejection happens before a durable receipt is written. The same key can
	// therefore be used for the caller's corrected, non-reserved request.
	validTarget := filepath.Join(t.TempDir(), "valid-bundle")
	if _, err := CreateBundle(ctx, storage, sourceData, validTarget, "reserved-"+recoveryParentLockName); err != nil {
		t.Fatalf("CreateBundle(corrected target with same key) error = %v", err)
	}
	if _, err := VerifyBundle(ctx, validTarget); err != nil {
		t.Fatalf("VerifyBundle(corrected target) error = %v", err)
	}
}

func TestRecoveryStagingCollisionCannotDeleteSelectedSourceOrBundle(t *testing.T) {
	t.Parallel()
	t.Run("backup source", func(t *testing.T) {
		ctx := context.Background()
		parent := t.TempDir()
		target := filepath.Join(parent, "published-backup")
		sourceData := recoveryStagingPath("backup", target)
		if err := os.Mkdir(sourceData, 0o700); err != nil {
			t.Fatal(err)
		}
		storage, err := store.Open(ctx, sourceData, store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer storage.Close()
		before := snapshotRecoveryTree(t, sourceData)
		if _, err := CreateBundle(ctx, storage, sourceData, target, "stage-source-collision"); ErrorCode(err) != CodeBackupSourceUnhealthy {
			t.Fatalf("CreateBundle(stage/source collision) error = %v, code = %q", err, ErrorCode(err))
		}
		assertRecoveryTreeUnchanged(t, sourceData, before)
	})

	t.Run("restore bundle", func(t *testing.T) {
		ctx := context.Background()
		fixture := createRecoveryTestBundle(t)
		parent := t.TempDir()
		target := filepath.Join(parent, "published-restore")
		bundle := recoveryStagingPath("restore", target)
		if err := os.Rename(fixture, bundle); err != nil {
			t.Fatal(err)
		}
		before := snapshotRecoveryTree(t, bundle)
		if _, err := RestorePending(ctx, bundle, target); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("RestorePending(stage/bundle collision) error = %v, code = %q", err, ErrorCode(err))
		}
		assertRecoveryTreeUnchanged(t, bundle, before)
		if _, err := VerifyBundle(ctx, bundle); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("VerifyBundle(reserved selected bundle) error = %v, code = %q", err, ErrorCode(err))
		}
	})
}

func TestSelectedRecoveryPathsBelowReservedStageAreRejectedWithoutMutation(t *testing.T) {
	ctx := context.Background()

	t.Run("backup source and live artifact verification", func(t *testing.T) {
		dataDirectory := moveRecoveryFixtureBelowReservedStage(t, t.TempDir(), "source")
		storage, err := store.Open(ctx, dataDirectory, store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer storage.Close()
		before := snapshotRecoveryTree(t, dataDirectory)
		target := filepath.Join(t.TempDir(), "bundle")
		if _, err := CreateBundle(ctx, storage, dataDirectory, target, "reserved-ancestor-source"); ErrorCode(err) != CodeBackupSourceUnhealthy {
			t.Fatalf("CreateBundle(reserved source ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		if _, err := VerifyLiveArtifacts(ctx, dataDirectory, nil); ErrorCode(err) != CodeBackupSourceUnhealthy {
			t.Fatalf("VerifyLiveArtifacts(reserved source ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		assertRecoveryTreeUnchanged(t, dataDirectory, before)
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatalf("rejected backup target was created: %v", err)
		}
	})

	t.Run("backup and restore target", func(t *testing.T) {
		source := t.TempDir()
		storage, err := store.Open(ctx, source, store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer storage.Close()
		bundle := createRecoveryTestBundle(t)
		parent := t.TempDir()
		reserved := filepath.Join(parent, recoveryStagingPrefix+strings.Repeat("e", sha256.Size*2))
		if err := os.Mkdir(reserved, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(reserved, "selected-target")
		sourceBefore := snapshotRecoveryTree(t, source)
		bundleBefore := snapshotRecoveryTree(t, bundle)
		parentBefore := snapshotRecoveryTree(t, parent)
		if _, err := CreateBundle(ctx, storage, source, target, "reserved-ancestor-target"); ErrorCode(err) != CodeBackupTargetInvalid {
			t.Fatalf("CreateBundle(reserved target ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		if _, err := RestorePending(ctx, bundle, target); ErrorCode(err) != CodeBackupTargetInvalid {
			t.Fatalf("RestorePending(reserved target ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		assertRecoveryTreeUnchanged(t, source, sourceBefore)
		assertRecoveryTreeUnchanged(t, bundle, bundleBefore)
		assertRecoveryTreeUnchanged(t, parent, parentBefore)
	})

	t.Run("bundle verify and restore source", func(t *testing.T) {
		bundle := moveRecoveryFixtureBelowReservedStage(t, createRecoveryTestBundle(t), "bundle")
		before := snapshotRecoveryTree(t, bundle)
		target := filepath.Join(t.TempDir(), "restored")
		if _, err := VerifyBundle(ctx, bundle); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("VerifyBundle(reserved bundle ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		if _, err := RestorePending(ctx, bundle, target); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("RestorePending(reserved bundle ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		assertRecoveryTreeUnchanged(t, bundle, before)
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatalf("rejected restore target was created: %v", err)
		}
	})

	t.Run("repair source", func(t *testing.T) {
		dataDirectory := moveRecoveryFixtureBelowReservedStage(t, createRepairTarget(t), "repair-source")
		before := snapshotRecoveryTree(t, dataDirectory)
		if _, err := InspectOffline(ctx, dataDirectory); ErrorCode(err) != CodeRepairTargetInvalid {
			t.Fatalf("InspectOffline(reserved source ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		assertRecoveryTreeUnchanged(t, dataDirectory, before)
	})

	t.Run("pending activation target", func(t *testing.T) {
		dataDirectory := moveRecoveryFixtureBelowReservedStage(t, restoreRecoveryTestBundle(t), "pending")
		before := snapshotRecoveryTree(t, dataDirectory)
		if _, err := Activate(ctx, dataDirectory, true); ErrorCode(err) != CodeRestoreNotActivated {
			t.Fatalf("Activate(reserved target ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		if _, err := CheckActivationState(dataDirectory); ErrorCode(err) != CodeRestoreNotActivated {
			t.Fatalf("CheckActivationState(reserved target ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		assertRecoveryTreeUnchanged(t, dataDirectory, before)
	})

	t.Run("activated target verification and consumption", func(t *testing.T) {
		dataDirectory := restoreRecoveryTestBundle(t)
		activated, err := Activate(ctx, dataDirectory, true)
		if err != nil {
			t.Fatal(err)
		}
		dataDirectory = moveRecoveryFixtureBelowReservedStage(t, dataDirectory, "activated")
		before := snapshotRecoveryTree(t, dataDirectory)
		if _, err := VerifyActivated(ctx, dataDirectory); ErrorCode(err) != CodeRestoreNotActivated {
			t.Fatalf("VerifyActivated(reserved target ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		if err := ConsumeActivated(dataDirectory, activated.ActivationSHA256); ErrorCode(err) != CodeRestoreNotActivated {
			t.Fatalf("ConsumeActivated(reserved target ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		assertRecoveryTreeUnchanged(t, dataDirectory, before)
	})
}

func TestSelectedRecoveryPathsBelowRepairScratchAreRejectedWithoutMutation(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	reservedNames := []string{
		fmt.Sprintf("%s%d-%d", repairScratchRootPrefix, os.Geteuid(), os.Getpid()),
		fmt.Sprintf("%s%d-%d-%s", repairScratchStagePrefix, os.Geteuid(), os.Getpid(), strings.Repeat("a", 24)),
	}
	for _, reservedName := range reservedNames {
		reservedName := reservedName
		t.Run(reservedName, func(t *testing.T) {
			reserved := filepath.Join(parent, reservedName)
			selected := filepath.Join(reserved, "selected")
			if err := os.MkdirAll(selected, 0o700); err != nil {
				t.Fatal(err)
			}
			witness := filepath.Join(selected, "witness")
			if err := os.WriteFile(witness, []byte("must remain unchanged\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotRecoveryTree(t, reserved)
			for _, candidate := range []string{reserved, selected} {
				if _, err := ValidateSelectedPath(candidate); err == nil {
					t.Fatalf("ValidateSelectedPath(%q) succeeded", candidate)
				}
				if _, err := CheckActivationState(candidate); ErrorCode(err) != CodeRestoreNotActivated {
					t.Fatalf("CheckActivationState(%q) error = %v, code = %q", candidate, err, ErrorCode(err))
				}
			}
			assertRecoveryTreeUnchanged(t, reserved, before)
		})
	}
}
