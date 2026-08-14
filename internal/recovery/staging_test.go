package recovery

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSecureExclusiveAnonymousPublicationCannotLeavePartialFinalOrTemporaryName(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := openExactPrivateDirectory(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	interrupted := errors.New("simulated process loss")
	if err := writeSecureExclusiveWithHooks(root, "content-sync", []byte("complete\n"), func() error { return interrupted }, nil); !errors.Is(err, interrupted) {
		t.Fatalf("writeSecureExclusiveWithHooks(content sync) error = %v", err)
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("anonymous content interruption entries = %v, %v; want empty", entries, err)
	}
	if err := writeSecureExclusiveWithHooks(root, "name-published", []byte("complete\n"), nil, func() error { return interrupted }); !errors.Is(err, interrupted) {
		t.Fatalf("writeSecureExclusiveWithHooks(name publish) error = %v", err)
	}
	data, err := readSecureRegular(root, "name-published", 64)
	if err != nil || string(data) != "complete\n" {
		t.Fatalf("published file = %q, %v", data, err)
	}
	entries, err = os.ReadDir(rootPath)
	if err != nil || len(entries) != 1 || entries[0].Name() != "name-published" {
		t.Fatalf("name publication entries = %v, %v", entries, err)
	}
}

func TestAtomicReceiptReplacementReconcilesItsDeterministicInterruptedFile(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := openExactPrivateDirectory(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := replaceSecureFileAtomic(root, "receipt.json", []byte("old\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeSecureExclusive(root, ".receipt.json.replacement", []byte("interrupted\n")); err != nil {
		t.Fatal(err)
	}
	if err := replaceSecureFileAtomic(root, "receipt.json", []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	data, err := readSecureRegular(root, "receipt.json", 64)
	if err != nil || string(data) != "new\n" {
		t.Fatalf("reconciled receipt = %q, %v", data, err)
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil || len(entries) != 1 || entries[0].Name() != "receipt.json" {
		t.Fatalf("reconciled receipt entries = %v, %v", entries, err)
	}
}

func TestRecoveryStagingReapsTwentyInterruptedCyclesWithoutResourceGrowth(t *testing.T) {
	parent := t.TempDir()
	targetPath := filepath.Join(parent, "published")
	target, err := resolveSecureTarget(targetPath)
	if err != nil {
		t.Fatalf("resolveSecureTarget() error = %v", err)
	}
	defer target.Close()
	baselineFDs := recoveryTestOpenFDs(t)
	baselineGoroutines := runtime.NumGoroutine()
	unownedName := recoveryStagingPrefix + strings.Repeat("f", sha256.Size*2)
	if unownedName == recoveryStagingName("restore", targetPath) {
		t.Fatal("fixed unowned witness unexpectedly equals the current deterministic stage")
	}
	unowned := filepath.Join(parent, unownedName)
	if err := os.Mkdir(unowned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unowned, "witness"), []byte("unowned"), 0o600); err != nil {
		t.Fatal(err)
	}

	for cycle := 0; cycle < 20; cycle++ {
		name, staging, release, err := target.createStaging(context.Background(), "restore")
		if err != nil {
			t.Fatalf("cycle %d createStaging() error = %v", cycle, err)
		}
		if err := writeSecureExclusive(staging, "partial", []byte("interrupted")); err != nil {
			_ = staging.Close()
			release()
			t.Fatalf("cycle %d write partial stage: %v", cycle, err)
		}
		if err := staging.Close(); err != nil {
			release()
			t.Fatalf("cycle %d close interrupted stage: %v", cycle, err)
		}
		release() // models the kernel releasing flock/file descriptors on process death.
		if _, err := os.Stat(filepath.Join(parent, name, "partial")); err != nil {
			t.Fatalf("cycle %d interrupted stage was not present: %v", cycle, err)
		}
		if data, err := os.ReadFile(filepath.Join(unowned, "witness")); err != nil || string(data) != "unowned" {
			t.Fatalf("cycle %d changed unowned reserved-looking sibling: %q, %v", cycle, data, err)
		}
	}

	name, staging, release, err := target.createStaging(context.Background(), "restore")
	if err != nil {
		t.Fatalf("final createStaging() error = %v", err)
	}
	if err := staging.Close(); err != nil {
		t.Fatalf("close final stage: %v", err)
	}
	if err := target.cleanupStaging(name); err != nil {
		t.Fatalf("cleanup final stage: %v", err)
	}
	release()

	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read staging parent: %v", err)
	}
	for _, entry := range entries {
		if validRecoveryStagingName(entry.Name()) && entry.Name() != unownedName {
			t.Fatalf("owned recovery stage remains after exact retry: %s", entry.Name())
		}
	}
	if data, err := os.ReadFile(filepath.Join(unowned, "witness")); err != nil || string(data) != "unowned" {
		t.Fatalf("unowned reserved-looking sibling changed after exact retries: %q, %v", data, err)
	}
	if current := recoveryTestOpenFDs(t); current > baselineFDs+1 {
		t.Fatalf("open FDs grew from %d to %d", baselineFDs, current)
	}
	if current := runtime.NumGoroutine(); current > baselineGoroutines+1 {
		t.Fatalf("goroutines grew from %d to %d", baselineGoroutines, current)
	}
}

func TestRecoveryStagingRefusesComputedExactUnownedCollisionWithoutDeletingIt(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	targetPath := filepath.Join(parent, "published")
	stageName := recoveryStagingName("backup", targetPath)
	stagePath := filepath.Join(parent, stageName)
	if err := os.Mkdir(stagePath, 0o700); err != nil {
		t.Fatal(err)
	}
	witness := filepath.Join(stagePath, "owner-data")
	if err := os.WriteFile(witness, []byte("must-survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := resolveSecureTarget(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if _, _, release, err := target.createStaging(context.Background(), "backup"); err == nil {
		if release != nil {
			release()
		}
		t.Fatal("createStaging accepted an unowned exact-name collision")
	}
	if data, err := os.ReadFile(witness); err != nil || string(data) != "must-survive" {
		t.Fatalf("computed exact collision witness changed: %q, %v", data, err)
	}
}

func TestRecoveryStagingReapsMarkerOwnedPriorTargetWithoutTouchingUnownedSibling(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	firstPath := filepath.Join(parent, "first")
	first, err := resolveSecureTarget(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	firstName, firstStage, releaseFirst, err := first.createStaging(context.Background(), "backup")
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	if err := writeSecureExclusive(firstStage, "partial", []byte("owned-crash")); err != nil {
		firstStage.Close()
		releaseFirst()
		first.Close()
		t.Fatal(err)
	}
	if err := firstStage.Close(); err != nil {
		releaseFirst()
		first.Close()
		t.Fatal(err)
	}
	releaseFirst() // process loss leaves the stage and its durable intent
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	unownedName := recoveryStagingPrefix + strings.Repeat("e", sha256.Size*2)
	if unownedName == firstName || unownedName == recoveryStagingName("restore", filepath.Join(parent, "second")) {
		t.Fatal("fixed unowned witness unexpectedly collides with an owned stage")
	}
	unowned := filepath.Join(parent, unownedName)
	if err := os.Mkdir(unowned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unowned, "witness"), []byte("unowned"), 0o600); err != nil {
		t.Fatal(err)
	}

	secondPath := filepath.Join(parent, "second")
	second, err := resolveSecureTarget(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondName, secondStage, releaseSecond, err := second.createStaging(context.Background(), "restore")
	if err != nil {
		t.Fatalf("createStaging(second target) error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(parent, firstName)); !os.IsNotExist(err) {
		t.Fatalf("marker-owned prior stage remains: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(unowned, "witness")); err != nil || string(data) != "unowned" {
		t.Fatalf("cross-target cleanup changed unowned sibling: %q, %v", data, err)
	}
	if err := secondStage.Close(); err != nil {
		releaseSecond()
		t.Fatal(err)
	}
	if err := second.cleanupStaging(secondName); err != nil {
		releaseSecond()
		t.Fatal(err)
	}
	releaseSecond()
}

func recoveryTestOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("/proc/self/fd is unavailable: %v", err)
	}
	return len(entries)
}
