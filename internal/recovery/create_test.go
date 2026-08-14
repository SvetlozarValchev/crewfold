package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/store"
)

func TestCreateBundlePublishesAndReplaysOneDurableReceipt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDirectory := t.TempDir()
	if err := os.Chmod(dataDirectory, 0o700); err != nil {
		t.Fatalf("chmod source data directory: %v", err)
	}
	storage, err := store.Open(ctx, dataDirectory, store.Options{})
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if _, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{
		Name: "backup", IdempotencyKey: "backup-workspace", CorrelationID: "backup-workspace-request",
	}); err != nil {
		t.Fatalf("InitWorkspace() error = %v", err)
	}

	target := filepath.Join(t.TempDir(), "published-bundle")
	const key = "durable-backup-key"
	created, err := CreateBundle(ctx, storage, dataDirectory, target, key)
	if err != nil {
		t.Fatalf("CreateBundle() error = %v", err)
	}
	if created.Root != target || created.Manifest.BackupID == "" || created.ManifestSHA256 == "" || created.Integrity.Status != "ok" {
		t.Fatalf("CreateBundle() = %#v, want exact verified published result", created)
	}
	replayed, err := CreateBundle(ctx, storage, dataDirectory, target, key)
	if err != nil || replayed.ManifestSHA256 != created.ManifestSHA256 || replayed.Manifest.BackupID != created.Manifest.BackupID {
		t.Fatalf("CreateBundle(replay) = %#v, %v; want %#v", replayed, err, created)
	}
	changedTarget := filepath.Join(filepath.Dir(target), "other-bundle")
	if _, err := CreateBundle(ctx, storage, dataDirectory, changedTarget, key); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("CreateBundle(conflicting target) error = %v, code = %q", err, ErrorCode(err))
	}

	keyDigest := sha256Text(key)
	receiptBytes, err := os.ReadFile(filepath.Join(dataDirectory, "maintenance", "backup-create", "receipts", keyDigest+".json"))
	if err != nil {
		t.Fatalf("read durable receipt: %v", err)
	}
	if strings.Contains(string(receiptBytes), key) || !strings.Contains(string(receiptBytes), `"state":"complete"`) {
		t.Fatalf("durable receipt leaks raw key or is incomplete: %s", receiptBytes)
	}
}

func TestVerifyLiveArtifactsDistinguishesOrphansFromFailures(t *testing.T) {
	t.Parallel()
	dataDirectory := t.TempDir()
	if err := os.Chmod(dataDirectory, 0o700); err != nil {
		t.Fatalf("chmod source data directory: %v", err)
	}
	empty, err := VerifyLiveArtifacts(context.Background(), dataDirectory, nil)
	if err != nil || !empty.Complete || empty.Status != "ok" || empty.IssueCount != 0 {
		t.Fatalf("VerifyLiveArtifacts(empty) = %#v, %v", empty, err)
	}
	orphanDigest := sha256Text("orphan")
	orphanDirectory := filepath.Join(dataDirectory, "check-artifacts", orphanDigest[:2])
	if err := os.MkdirAll(orphanDirectory, 0o700); err != nil {
		t.Fatalf("mkdir orphan shard: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanDirectory, orphanDigest), []byte("orphan"), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	warning, err := VerifyLiveArtifacts(context.Background(), dataDirectory, nil)
	if err != nil || !warning.Complete || warning.Status != "warning" || warning.WarningCount == 0 || warning.IssueCount != 0 || warning.ExtraCount == 0 {
		t.Fatalf("VerifyLiveArtifacts(orphan) = %#v, %v", warning, err)
	}
	missingDigest := sha256Text("missing")
	failed, err := VerifyLiveArtifacts(context.Background(), dataDirectory, []store.ImmutableArtifactReference{{
		ContentSHA256: missingDigest, ByteSize: int64(len("missing")), Kind: "check_artifact",
	}})
	if err != nil || !failed.Complete || failed.Status != "failed" || failed.MissingCount != 1 || failed.IssueCount == 0 {
		t.Fatalf("VerifyLiveArtifacts(missing) = %#v, %v", failed, err)
	}
}

func TestCreateBundlePreservesUnownedReservedLookingSibling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDirectory := t.TempDir()
	if err := os.Chmod(dataDirectory, 0o700); err != nil {
		t.Fatalf("chmod source data directory: %v", err)
	}
	storage, err := store.Open(ctx, dataDirectory, store.Options{})
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	parent := t.TempDir()
	abandoned := filepath.Join(parent, recoveryStagingPrefix+strings.Repeat("0", sha256.Size*2))
	if err := os.Mkdir(abandoned, 0o700); err != nil {
		t.Fatalf("mkdir abandoned stage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(abandoned, "partial"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write abandoned stage: %v", err)
	}
	unrelated := filepath.Join(parent, recoveryStagingPrefix+"not-a-digest")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatalf("mkdir unrelated directory: %v", err)
	}
	target := filepath.Join(parent, "bundle")
	if _, err := CreateBundle(ctx, storage, dataDirectory, target, "reap-stage"); err != nil {
		t.Fatalf("CreateBundle() error = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(abandoned, "partial")); err != nil || string(data) != "partial" {
		t.Fatalf("unowned reserved-looking sibling changed: %q, %v", data, err)
	}
	if info, err := os.Lstat(unrelated); err != nil || !info.IsDir() {
		t.Fatalf("non-reserved sibling was changed: %#v, %v", info, err)
	}
}

func TestCreateBundleIgnoresUnownedReservedLookingSymlinkWithoutFollowingIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDirectory := t.TempDir()
	if err := os.Chmod(dataDirectory, 0o700); err != nil {
		t.Fatalf("chmod source data directory: %v", err)
	}
	storage, err := store.Open(ctx, dataDirectory, store.Options{})
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	parent := t.TempDir()
	outside := t.TempDir()
	witness := filepath.Join(outside, "witness")
	if err := os.WriteFile(witness, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("write outside witness: %v", err)
	}
	reserved := filepath.Join(parent, recoveryStagingPrefix+strings.Repeat("1", sha256.Size*2))
	if err := os.Symlink(outside, reserved); err != nil {
		t.Fatalf("create reserved-name symlink: %v", err)
	}
	target := filepath.Join(parent, "bundle")
	if _, err := CreateBundle(ctx, storage, dataDirectory, target, "unowned-stage-symlink"); err != nil {
		t.Fatalf("CreateBundle() error = %v", err)
	}
	data, err := os.ReadFile(witness)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("outside witness changed: %q, %v", data, err)
	}
	if info, err := os.Lstat(reserved); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unowned reserved-looking symlink changed: %#v, %v", info, err)
	}
	if _, err := VerifyBundle(ctx, target); err != nil {
		t.Fatalf("VerifyBundle() error = %v", err)
	}
}

func sha256Text(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
