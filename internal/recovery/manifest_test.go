package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store"
	"golang.org/x/sys/unix"
)

func TestBundleVerifyAndPendingRestoreAreExactAndSourceIndependent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDirectory := t.TempDir()
	storage, err := store.Open(ctx, dataDirectory, store.Options{})
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if _, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{Name: "backup", IdempotencyKey: "backup-workspace", CorrelationID: "backup-workspace-request"}); err != nil {
		t.Fatalf("InitWorkspace() error = %v", err)
	}

	bundle := filepath.Join(t.TempDir(), "bundle")
	if err := os.Mkdir(bundle, 0o700); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	snapshot, err := storage.BackupSnapshot(ctx, filepath.Join(bundle, "crewfold.db"))
	if err != nil {
		t.Fatalf("BackupSnapshot() error = %v", err)
	}
	integrity, err := store.VerifyDatabaseSnapshot(ctx, snapshot.Path, store.CanonicalVerifyOptions{Full: true})
	if err != nil || integrity.Status != "ok" || !integrity.Complete || !integrity.Quiescence.Quiescent {
		t.Fatalf("VerifyDatabaseSnapshot() = %#v, %v", integrity, err)
	}
	manifest, err := NewManifest("backup_00000000000000000000000000000001", "2026-08-14T12:00:00Z", snapshot, integrity, ArtifactEntries(integrity.ArtifactReferences))
	if err != nil {
		t.Fatalf("NewManifest() error = %v", err)
	}
	manifestSHA256, err := WriteManifest(bundle, manifest)
	if err != nil || len(manifestSHA256) != 64 {
		t.Fatalf("WriteManifest() = %q, %v", manifestSHA256, err)
	}
	verified, err := VerifyBundle(ctx, bundle)
	if err != nil || verified.ManifestSHA256 != manifestSHA256 || verified.Integrity.LogicalSHA256 != integrity.LogicalSHA256 {
		t.Fatalf("VerifyBundle() = %#v, %v", verified, err)
	}

	target := filepath.Join(t.TempDir(), "restored")
	restored, err := RestorePending(ctx, bundle, target)
	if err != nil || restored.Path != target || restored.BackupID != manifest.BackupID {
		t.Fatalf("RestorePending() = %#v, %v", restored, err)
	}
	for _, path := range []string{"crewfold.db", "daemon.lock", ".restore-pending.json"} {
		info, statErr := os.Lstat(filepath.Join(target, path))
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("restored %s = %#v, %v, want exact 0600 regular", path, info, statErr)
		}
	}
	if _, err := os.Lstat(filepath.Join(target, "node.key")); !os.IsNotExist(err) {
		t.Fatalf("pending restore unexpectedly contains node.key: %v", err)
	}
	restoredIntegrity, err := store.VerifyDatabaseSnapshot(ctx, filepath.Join(target, "crewfold.db"), store.CanonicalVerifyOptions{Full: true})
	if err != nil || restoredIntegrity.LogicalSHA256 != integrity.LogicalSHA256 || restoredIntegrity.EventHighWater != integrity.EventHighWater {
		t.Fatalf("restored database integrity = %#v, %v", restoredIntegrity, err)
	}
	if _, err := RestorePending(ctx, bundle, target); ErrorCode(err) != CodeRestoreTargetExists {
		t.Fatalf("RestorePending(existing) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestBundleVerifierRejectsExtraAndTamperedPayloads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bundle := createRecoveryTestBundle(t)
	if err := os.WriteFile(filepath.Join(bundle, "extra"), []byte("not declared"), 0o600); err != nil {
		t.Fatalf("write extra file: %v", err)
	}
	if _, err := VerifyBundle(ctx, bundle); ErrorCode(err) != CodeBackupIntegrityFailed {
		t.Fatalf("VerifyBundle(extra) error = %v, code = %q", err, ErrorCode(err))
	}
	if err := os.Remove(filepath.Join(bundle, "extra")); err != nil {
		t.Fatalf("remove exact extra fixture: %v", err)
	}
	databasePath := filepath.Join(bundle, "crewfold.db")
	database, err := os.OpenFile(databasePath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open database tamper fixture: %v", err)
	}
	if _, err := database.WriteAt([]byte{0xff}, 4096); err != nil {
		_ = database.Close()
		t.Fatalf("tamper database payload: %v", err)
	}
	_ = database.Close()
	if _, err := VerifyBundle(ctx, bundle); ErrorCode(err) != CodeBackupIntegrityFailed {
		t.Fatalf("VerifyBundle(tampered database) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestRestorePendingPreservesUnownedReservedLookingSibling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bundle := createRecoveryTestBundle(t)
	parent := t.TempDir()
	abandoned := filepath.Join(parent, recoveryStagingPrefix+strings.Repeat("2", sha256.Size*2))
	if err := os.Mkdir(abandoned, 0o700); err != nil {
		t.Fatalf("mkdir abandoned restore stage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(abandoned, "crewfold.db"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write partial restored database: %v", err)
	}
	target := filepath.Join(parent, "restored")
	if _, err := RestorePending(ctx, bundle, target); err != nil {
		t.Fatalf("RestorePending() error = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(abandoned, "crewfold.db")); err != nil || string(data) != "partial" {
		t.Fatalf("unowned reserved-looking restore sibling changed: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(target, ".restore-pending.json")); err != nil {
		t.Fatalf("restored pending seal missing: %v", err)
	}
}

func TestBundleVerifierRejectsEveryUnsafeFilesystemClass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "missing declared database", mutate: func(t *testing.T, bundle string) {
			if err := os.Remove(filepath.Join(bundle, "crewfold.db")); err != nil {
				t.Fatalf("remove database: %v", err)
			}
		}},
		{name: "database mode", mutate: func(t *testing.T, bundle string) {
			if err := os.Chmod(filepath.Join(bundle, "crewfold.db"), 0o640); err != nil {
				t.Fatalf("chmod database: %v", err)
			}
		}},
		{name: "truncated database", mutate: func(t *testing.T, bundle string) {
			path := filepath.Join(bundle, "crewfold.db")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(path, info.Size()-1); err != nil {
				t.Fatalf("truncate database: %v", err)
			}
		}},
		{name: "root mode", mutate: func(t *testing.T, bundle string) {
			if err := os.Chmod(bundle, 0o750); err != nil {
				t.Fatalf("chmod bundle: %v", err)
			}
		}},
		{name: "hard link alias", mutate: func(t *testing.T, bundle string) {
			if err := os.Link(filepath.Join(bundle, "crewfold.db"), filepath.Join(bundle, "database-alias")); err != nil {
				t.Fatalf("hard-link database: %v", err)
			}
		}},
		{name: "symlink escape", mutate: func(t *testing.T, bundle string) {
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
				t.Fatalf("write outside payload: %v", err)
			}
			if err := os.Symlink(outside, filepath.Join(bundle, "escape")); err != nil {
				t.Fatalf("symlink outside bundle: %v", err)
			}
		}},
		{name: "fifo", mutate: func(t *testing.T, bundle string) {
			if err := unix.Mkfifo(filepath.Join(bundle, "pipe"), 0o600); err != nil {
				t.Fatalf("mkfifo: %v", err)
			}
		}},
		{name: "unix socket", mutate: func(t *testing.T, bundle string) {
			listener, err := net.Listen("unix", filepath.Join(bundle, "socket"))
			if err != nil {
				t.Fatalf("listen unix socket: %v", err)
			}
			t.Cleanup(func() { _ = listener.Close() })
		}},
		{name: "device", mutate: func(t *testing.T, bundle string) {
			err := unix.Mknod(filepath.Join(bundle, "device"), unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3)))
			if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
				t.Skipf("device creation is unavailable: %v", err)
			}
			if err != nil {
				t.Fatalf("mknod: %v", err)
			}
		}},
		{name: "manifest traversal", mutate: func(t *testing.T, bundle string) {
			path := filepath.Join(bundle, "manifest.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read manifest: %v", err)
			}
			var manifest Manifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatalf("decode manifest fixture: %v", err)
			}
			manifest.Database.Path = "../crewfold.db"
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatalf("encode traversal manifest: %v", err)
			}
			data = append(data, '\n')
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write traversal manifest: %v", err)
			}
		}},
		{name: "manifest digest mismatch", mutate: func(t *testing.T, bundle string) {
			if err := os.WriteFile(filepath.Join(bundle, "manifest.sha256"), []byte(strings.Repeat("0", sha256.Size*2)+"\n"), 0o600); err != nil {
				t.Fatalf("write mismatched manifest digest: %v", err)
			}
		}},
		{name: "unknown manifest schema", mutate: func(t *testing.T, bundle string) {
			manifest := readManifestFixture(t, bundle)
			manifest.Schema = "urn:crewfold:schema:backup-manifest:v2"
			rewriteManifestJSONUnchecked(t, bundle, manifest)
		}},
		{name: "unknown manifest field", mutate: func(t *testing.T, bundle string) {
			path := filepath.Join(bundle, "manifest.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = append(bytes.TrimSuffix(data, []byte("}\n")), []byte(",\"unknown\":true}\n")...)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "invented manifest entry", mutate: func(t *testing.T, bundle string) {
			content := []byte("invented artifact")
			digest := sha256.Sum256(content)
			digestText := hex.EncodeToString(digest[:])
			relative := filepath.Join("check-artifacts", digestText[:2], digestText)
			if err := os.MkdirAll(filepath.Join(bundle, filepath.Dir(relative)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bundle, relative), content, 0o600); err != nil {
				t.Fatal(err)
			}
			manifest := readManifestFixture(t, bundle)
			manifest.Entries = []ArtifactEntry{{Path: filepath.ToSlash(relative), Kind: "check_artifact", Mode: 0o600, Size: int64(len(content)), SHA256: digestText}}
			manifest.EntryCount++
			manifest.TotalBytes += int64(len(content))
			rewriteManifestEnvelope(t, bundle, manifest)
		}},
		{name: "logical state mismatch", mutate: func(t *testing.T, bundle string) {
			manifest := readManifestFixture(t, bundle)
			manifest.LogicalSHA256 = strings.Repeat("f", sha256.Size*2)
			rewriteManifestEnvelope(t, bundle, manifest)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := createRecoveryTestBundle(t)
			test.mutate(t, bundle)
			if _, err := VerifyBundle(context.Background(), bundle); ErrorCode(err) != CodeBackupIntegrityFailed && ErrorCode(err) != CodeBackupContractMismatch {
				t.Fatalf("VerifyBundle() error = %v, code = %q", err, ErrorCode(err))
			}
		})
	}
}

func TestBundleVerifierRejectsSymlinkAncestorWithoutFollowingOutside(t *testing.T) {
	bundle := createRecoveryTestBundle(t)
	aliasRoot := t.TempDir()
	alias := filepath.Join(aliasRoot, "outside")
	if err := os.Symlink(filepath.Dir(bundle), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(context.Background(), filepath.Join(alias, filepath.Base(bundle))); ErrorCode(err) != CodeBackupIntegrityFailed {
		t.Fatalf("VerifyBundle(symlink ancestor) error = %v, code = %q", err, ErrorCode(err))
	}
	if _, err := VerifyBundle(context.Background(), bundle); err != nil {
		t.Fatalf("original bundle changed while refusing symlink ancestor: %v", err)
	}
}

func TestReferencedArtifactMatrixRejectsMissingAlteredModeAndAlias(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, ArtifactEntry)
	}{
		{name: "missing", mutate: func(t *testing.T, bundle string, entry ArtifactEntry) {
			if err := os.Remove(filepath.Join(bundle, filepath.FromSlash(entry.Path))); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "altered", mutate: func(t *testing.T, bundle string, entry ArtifactEntry) {
			if err := os.WriteFile(filepath.Join(bundle, filepath.FromSlash(entry.Path)), []byte("altered"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "bad mode", mutate: func(t *testing.T, bundle string, entry ArtifactEntry) {
			if err := os.Chmod(filepath.Join(bundle, filepath.FromSlash(entry.Path)), 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hard link alias", mutate: func(t *testing.T, bundle string, entry ArtifactEntry) {
			if err := os.Link(filepath.Join(bundle, filepath.FromSlash(entry.Path)), filepath.Join(bundle, "artifact-alias")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			bundle, manifest := createRecoveryTestBundleWithRunArtifacts(t)
			if len(manifest.Entries) < 2 {
				t.Fatalf("artifact fixture entries = %#v", manifest.Entries)
			}
			test.mutate(t, bundle, manifest.Entries[0])
			if _, err := VerifyBundle(context.Background(), bundle); ErrorCode(err) != CodeBackupIntegrityFailed {
				t.Fatalf("VerifyBundle() error = %v, code = %q", err, ErrorCode(err))
			}
		})
	}
}

func TestRestoreCopiesEveryReferencedArtifactAndActivationReverifiesIt(t *testing.T) {
	bundle, manifest := createRecoveryTestBundleWithRunArtifacts(t)
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := RestorePending(context.Background(), bundle, target); err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Entries {
		size, digest, err := hashSecureRegularPathForTest(target, entry.Path)
		if err != nil || size != entry.Size || digest != entry.SHA256 {
			t.Fatalf("restored artifact %s = %d/%s, %v; want %d/%s", entry.Path, size, digest, err, entry.Size, entry.SHA256)
		}
	}
	if _, err := Activate(context.Background(), target, true); err != nil {
		t.Fatalf("Activate(restored artifacts) error = %v", err)
	}
}

func createRecoveryTestBundleWithRunArtifacts(t *testing.T) (string, Manifest) {
	t.Helper()
	ctx := context.Background()
	storage, dataDir, workspaceID, assigned, _ := newAssignedRecoveryFixture(t)
	created, err := storage.CreateRun(ctx, store.CreateRunCommand{
		WorkspaceIdentifier: workspaceID, TaskID: assigned.Task.ID, Runtime: "fake", Provider: "fake",
		Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "artifact-cut", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"}}},
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "artifact-cut-run", CorrelationID: "artifact-cut-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkRunStarting(ctx, created.Detail.Run.ID, "artifact-cut-starting"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkRunStarted(ctx, created.Detail.Run.ID, "runtime-handle", "provider-handle", "artifact-cut-started"); err != nil {
		t.Fatal(err)
	}
	archive, err := storage.PrepareRunLogArchive(ctx, created.Detail.Run.ID, domain.RunLogs{
		RunID: created.Detail.Run.ID, State: "exited",
		Stdout: domain.CapturedLog{Text: "referenced stdout\n", CapturedBytes: 18},
		Stderr: domain.CapturedLog{Text: "referenced stderr\n", CapturedBytes: 18},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := storage.ApplyRunObservation(ctx, created.Detail.Run.ID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "done", Handoff: "review", LogArchive: &archive,
	}, true, nil, "artifact-cut-complete")
	if err != nil || completed.Run.Status != domain.RunCompleted {
		t.Fatalf("ApplyRunObservation() = %#v, %v", completed, err)
	}
	bundle := filepath.Join(t.TempDir(), "artifact-bundle")
	createdBundle, err := CreateBundle(ctx, storage, dataDir, bundle, "artifact-cut-bundle")
	if err != nil {
		t.Fatal(err)
	}
	return bundle, createdBundle.Manifest
}

func hashSecureRegularPathForTest(root, relative string) (int64, string, error) {
	directory, err := openExactPrivateDirectory(root)
	if err != nil {
		return 0, "", err
	}
	defer directory.Close()
	return hashSecureRegular(context.Background(), directory, relative, maximumArtifactSize)
}

func readManifestFixture(t *testing.T, bundle string) Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func rewriteManifestEnvelope(t *testing.T, bundle string, manifest Manifest) {
	t.Helper()
	data, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatalf("MarshalManifest() error = %v", err)
	}
	digest := sha256.Sum256(data)
	if err := os.WriteFile(filepath.Join(bundle, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "manifest.sha256"), []byte(hex.EncodeToString(digest[:])+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func rewriteManifestJSONUnchecked(t *testing.T, bundle string, manifest Manifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	digest := sha256.Sum256(data)
	if err := os.WriteFile(filepath.Join(bundle, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "manifest.sha256"), []byte(hex.EncodeToString(digest[:])+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRestorePendingRefusesExistingOrSymlinkTargetsWithoutChangingBytes(t *testing.T) {
	t.Parallel()
	bundle := createRecoveryTestBundle(t)
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir existing target: %v", err)
	}
	witness := filepath.Join(target, "witness")
	if err := os.WriteFile(witness, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("write existing target witness: %v", err)
	}
	if _, err := RestorePending(context.Background(), bundle, target); ErrorCode(err) != CodeRestoreTargetExists {
		t.Fatalf("RestorePending(existing) error = %v, code = %q", err, ErrorCode(err))
	}
	if data, err := os.ReadFile(witness); err != nil || string(data) != "unchanged" {
		t.Fatalf("existing target changed: %q, %v", data, err)
	}

	existingFile := filepath.Join(parent, "existing-file")
	if err := os.WriteFile(existingFile, []byte("unchanged-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestorePending(context.Background(), bundle, existingFile); ErrorCode(err) != CodeRestoreTargetExists {
		t.Fatalf("RestorePending(existing file) error = %v, code = %q", err, ErrorCode(err))
	}
	if data, err := os.ReadFile(existingFile); err != nil || string(data) != "unchanged-file" {
		t.Fatalf("existing file target changed: %q, %v", data, err)
	}

	outside := t.TempDir()
	symlink := filepath.Join(parent, "symlink-target")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatalf("create restore target symlink: %v", err)
	}
	if _, err := RestorePending(context.Background(), bundle, symlink); ErrorCode(err) != CodeRestoreTargetExists {
		t.Fatalf("RestorePending(symlink) error = %v, code = %q", err, ErrorCode(err))
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("restore followed symlink target: %#v, %v", entries, err)
	}
}

func createRecoveryTestBundle(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	storage, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	bundle := filepath.Join(t.TempDir(), "bundle")
	if err := os.Mkdir(bundle, 0o700); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	snapshot, err := storage.BackupSnapshot(ctx, filepath.Join(bundle, "crewfold.db"))
	if err != nil {
		t.Fatalf("BackupSnapshot() error = %v", err)
	}
	integrity, err := store.VerifyDatabaseSnapshot(ctx, snapshot.Path, store.CanonicalVerifyOptions{Full: true})
	if err != nil {
		t.Fatalf("VerifyDatabaseSnapshot() error = %v", err)
	}
	manifest, err := NewManifest("backup_00000000000000000000000000000002", "2026-08-14T12:00:00Z", snapshot, integrity, nil)
	if err != nil {
		t.Fatalf("NewManifest() error = %v", err)
	}
	if _, err := WriteManifest(bundle, manifest); err != nil {
		t.Fatalf("WriteManifest() error = %v", err)
	}
	return bundle
}
