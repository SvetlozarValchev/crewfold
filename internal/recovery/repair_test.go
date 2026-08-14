package recovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/store"
	"golang.org/x/sys/unix"
)

func TestInspectOfflineUsesRecoveredPrivateDBWALCopyWithoutMutatingSource(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	workspace, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{Name: "repair-copy", IdempotencyKey: "repair-copy-workspace", CorrelationID: "request-repair-copy-workspace"})
	if err != nil {
		t.Fatal(err)
	}

	before := readPresentRepairFiles(t, dataDir)
	if len(before["crewfold.db-wal"]) == 0 || len(before["crewfold.db-shm"]) == 0 {
		t.Fatalf("fixture did not retain live WAL/SHM bytes: wal=%d shm=%d", len(before["crewfold.db-wal"]), len(before["crewfold.db-shm"]))
	}
	report, err := InspectOffline(ctx, dataDir)
	if err != nil {
		t.Fatalf("InspectOffline() error = %v", err)
	}
	if report.Status != "ok" || !report.Integrity.Complete || report.Integrity.Status != "ok" ||
		report.Integrity.EventHighWater != workspace.EventSequence || !report.Copied.WALPresent || !report.Copied.SHMPresent ||
		report.Copied.WALBytes != int64(len(before["crewfold.db-wal"])) || report.Copied.SHMBytes != int64(len(before["crewfold.db-shm"])) {
		t.Fatalf("InspectOffline() = %#v", report)
	}
	after := readPresentRepairFiles(t, dataDir)
	if len(before) != len(after) {
		t.Fatalf("source file set changed: before=%v after=%v", repairFileSizes(before), repairFileSizes(after))
	}
	for name, content := range before {
		if !bytes.Equal(content, after[name]) {
			t.Fatalf("InspectOffline() changed selected source bytes for %s", name)
		}
	}
}

func TestInspectOfflineRefusesLiveOwnerAndUnsafeTargets(t *testing.T) {
	t.Run("live daemon lock", func(t *testing.T) {
		dataDir := createRepairTarget(t)
		lock, err := os.OpenFile(filepath.Join(dataDir, "daemon.lock"), os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		if _, err := InspectOffline(context.Background(), dataDir); ErrorCode(err) != CodeRepairSourceInUse {
			t.Fatalf("InspectOffline(locked) error = %v, code = %q", err, ErrorCode(err))
		}
	})
	t.Run("missing existing lock", func(t *testing.T) {
		dataDir := t.TempDir()
		if err := os.Chmod(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		storage, err := store.Open(context.Background(), dataDir, store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := InspectOffline(context.Background(), dataDir); ErrorCode(err) != CodeRepairTargetInvalid {
			t.Fatalf("InspectOffline(missing lock) error = %v, code = %q", err, ErrorCode(err))
		}
	})
	t.Run("symlink database", func(t *testing.T) {
		dataDir := t.TempDir()
		if err := os.Chmod(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, "daemon.lock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(t.TempDir(), "external.db")
		if err := os.WriteFile(external, []byte("do not follow"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(dataDir, "crewfold.db")); err != nil {
			t.Fatal(err)
		}
		if _, err := InspectOffline(context.Background(), dataDir); ErrorCode(err) != CodeRepairTargetInvalid {
			t.Fatalf("InspectOffline(symlink DB) error = %v, code = %q", err, ErrorCode(err))
		}
		content, err := os.ReadFile(external)
		if err != nil || string(content) != "do not follow" {
			t.Fatalf("external target changed: %q, %v", content, err)
		}
	})
}

func TestInspectOfflineReturnsBoundedFailureForUnreadableCopiedDatabase(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "crewfold.db"), []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := InspectOffline(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("InspectOffline(unreadable) error = %v", err)
	}
	if report.Status != "failed" || len(report.Findings) == 0 || len(report.Findings) > maximumRepairFindings || report.Findings[0].Code != "database_unreadable" {
		t.Fatalf("InspectOffline(unreadable) = %#v", report)
	}
	for _, finding := range report.Findings {
		if len(finding.Summary) > 2048 {
			t.Fatalf("unbounded repair finding: %d bytes", len(finding.Summary))
		}
	}
}

func TestRepairScratchNormallyRemovesItsEntirePrivateRoot(t *testing.T) {
	parent := t.TempDir()
	scratch, err := prepareRepairScratch(context.Background(), parent)
	if err != nil {
		t.Fatalf("prepareRepairScratch() error = %v", err)
	}
	root := filepath.Join(parent, scratch.rootName)
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("repair scratch root = %#v, %v", info, err)
	}
	if err := scratch.Close(); err != nil {
		t.Fatalf("repairScratch.Close() error = %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private temp after normal repair cleanup = %#v, %v", entries, err)
	}
}

func TestRepairScratchReapsStaleExactRootsAndSkipsLiveOwner(t *testing.T) {
	parent := t.TempDir()
	uid := os.Geteuid()
	staleName := fmt.Sprintf("%s%d-%d", repairScratchRootPrefix, uid, 999998)
	stale := filepath.Join(parent, staleName)
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, repairScratchLockName), []byte(repairScratchMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "partial"), []byte("crash"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageName := fmt.Sprintf("%s%d-%d-%s", repairScratchStagePrefix, uid, 999997, strings.Repeat("a", 24))
	if err := os.Mkdir(filepath.Join(parent, stageName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, stageName, "partial"), []byte("crash"), 0o600); err != nil {
		t.Fatal(err)
	}
	unmarkedName := fmt.Sprintf("%s%d-%d", repairScratchRootPrefix, uid, 999995)
	unmarked := filepath.Join(parent, unmarkedName)
	if err := os.Mkdir(unmarked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unmarked, "owner-data"), []byte("unmarked"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeMarkerName := fmt.Sprintf("%s%d-%d", repairScratchRootPrefix, uid, 999994)
	fakeMarker := filepath.Join(parent, fakeMarkerName)
	if err := os.Mkdir(fakeMarker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeMarker, repairScratchLockName), []byte("not-a-crewfold-marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeMarker, "owner-data"), []byte("fake-marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsafeModeName := fmt.Sprintf("%s%d-%d", repairScratchRootPrefix, uid, 999993)
	unsafeMode := filepath.Join(parent, unsafeModeName)
	if err := os.Mkdir(unsafeMode, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unsafeMode, repairScratchLockName), []byte(repairScratchMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unsafeMode, "owner-data"), []byte("unsafe-mode"), 0o600); err != nil {
		t.Fatal(err)
	}

	liveName := fmt.Sprintf("%s%d-%d", repairScratchRootPrefix, uid, 999996)
	live := filepath.Join(parent, liveName)
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, repairScratchLockName), []byte(repairScratchMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	liveLock, err := os.OpenFile(filepath.Join(live, repairScratchLockName), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(liveLock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = liveLock.Close()
		t.Fatal(err)
	}

	scratch, err := prepareRepairScratch(context.Background(), parent)
	if err != nil {
		_ = unix.Flock(int(liveLock.Fd()), unix.LOCK_UN)
		_ = liveLock.Close()
		t.Fatalf("prepareRepairScratch() error = %v", err)
	}
	if _, err := os.Lstat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marked stale repair scratch remains %s: %v", staleName, err)
	}
	if data, err := os.ReadFile(filepath.Join(parent, stageName, "partial")); err != nil || string(data) != "crash" {
		t.Fatalf("unmarked stage-looking sibling changed: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(unmarked, "owner-data")); err != nil || string(data) != "unmarked" {
		t.Fatalf("unmarked root-looking sibling changed: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(fakeMarker, "owner-data")); err != nil || string(data) != "fake-marker" {
		t.Fatalf("fake-marker root-looking sibling changed: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(unsafeMode, "owner-data")); err != nil || string(data) != "unsafe-mode" {
		t.Fatalf("unsafe-mode marked sibling changed: %q, %v", data, err)
	}
	if info, err := os.Lstat(live); err != nil || !info.IsDir() {
		t.Fatalf("live repair scratch was reaped: %#v, %v", info, err)
	}
	if err := scratch.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(liveLock.Fd()), unix.LOCK_UN); err != nil {
		_ = liveLock.Close()
		t.Fatal(err)
	}
	if err := liveLock.Close(); err != nil {
		t.Fatal(err)
	}

	reaper, err := prepareRepairScratch(context.Background(), parent)
	if err != nil {
		t.Fatalf("prepareRepairScratch(reap formerly live) error = %v", err)
	}
	if _, err := os.Lstat(live); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released stale repair scratch remains: %v", err)
	}
	if err := reaper.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(parent, stageName, "partial")); err != nil || string(data) != "crash" {
		t.Fatalf("stage-looking sibling changed after stale cleanup: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(unmarked, "owner-data")); err != nil || string(data) != "unmarked" {
		t.Fatalf("root-looking sibling changed after stale cleanup: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(fakeMarker, "owner-data")); err != nil || string(data) != "fake-marker" {
		t.Fatalf("fake-marker sibling changed after stale cleanup: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(unsafeMode, "owner-data")); err != nil || string(data) != "unsafe-mode" {
		t.Fatalf("unsafe-mode sibling changed after stale cleanup: %q, %v", data, err)
	}
}

func TestInspectOfflineRejectsSelectedRepairScratchRootWithoutMutation(t *testing.T) {
	name := fmt.Sprintf("%s%d-%d", repairScratchRootPrefix, os.Geteuid(), os.Getpid()+1000000)
	dataDir := filepath.Join(os.TempDir(), name)
	if err := os.Mkdir(dataDir, 0o700); errors.Is(err, os.ErrExist) {
		t.Skipf("repair grammar witness already exists: %s", dataDir)
	} else if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(context.Background(), dataDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotRecoveryTree(t, dataDir)
	report, err := InspectOffline(context.Background(), dataDir)
	if ErrorCode(err) != CodeRepairTargetInvalid || report.Status != "failed" {
		t.Fatalf("InspectOffline(reserved repair scratch source) = %#v, %v, code=%q", report, err, ErrorCode(err))
	}
	assertRecoveryTreeUnchanged(t, dataDir, before)
}

func createRepairTarget(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(context.Background(), dataDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func readPresentRepairFiles(t *testing.T, dataDir string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	for _, name := range []string{"crewfold.db", "crewfold.db-wal", "crewfold.db-shm", "daemon.lock"} {
		content, err := os.ReadFile(filepath.Join(dataDir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result[name] = content
	}
	return result
}

func repairFileSizes(files map[string][]byte) map[string]int {
	result := make(map[string]int, len(files))
	for name, content := range files {
		result[name] = len(content)
	}
	return result
}
