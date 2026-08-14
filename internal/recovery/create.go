package recovery

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/store"
	"golang.org/x/sys/unix"
)

const (
	backupReceiptSchema      = "urn:crewfold:schema:backup-create-receipt:v1"
	maximumBackupReceiptSize = 16 << 10
)

type backupCreateReceipt struct {
	Schema         string `json:"schema"`
	State          string `json:"state"`
	KeySHA256      string `json:"key_sha256"`
	RequestSHA256  string `json:"request_sha256"`
	TargetPath     string `json:"target_path"`
	BackupID       string `json:"backup_id"`
	CreatedAt      string `json:"created_at"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
}

// createHooks names the durable recovery boundaries used by the adversarial
// crash matrix. Production uses the zero value. Tests inject process-loss or
// cancellation immediately after a boundary and then exercise the ordinary
// public retry path; the hooks never select alternate recovery behavior.
type createHooks struct {
	afterSnapshot  func() error
	afterArtifacts func() error
	afterManifest  func() error
	afterPublish   func() error
	beforeResponse func() error
}

// CreateBundle captures one online snapshot, verifies that captured cut, copies
// its exact typed immutable-artifact closure, and publishes one complete bundle
// into a caller-selected nonexistent absolute path.
func CreateBundle(ctx context.Context, source *store.Store, sourceDataDir, targetPath, idempotencyKey string) (VerifiedBundle, error) {
	return createBundleWithHooks(ctx, source, sourceDataDir, targetPath, idempotencyKey, createHooks{})
}

func createBundleWithHooks(ctx context.Context, source *store.Store, sourceDataDir, targetPath, idempotencyKey string, hooks createHooks) (VerifiedBundle, error) {
	if source == nil {
		return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "backup source store is missing"}
	}
	sourceDataDir, err := exactSelectedRecoveryPath(sourceDataDir)
	if err != nil || filepath.Dir(source.Path()) != sourceDataDir {
		return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "backup source data directory does not exactly own the open database", Cause: err}
	}
	targetPath, err = exactSelectedRecoveryPath(targetPath)
	if err != nil {
		return VerifiedBundle{}, &Error{Code: CodeBackupTargetInvalid, Message: "backup target must be a canonical absolute path", Cause: err}
	}
	targetInsideSource, err := pathContains(sourceDataDir, targetPath)
	if err != nil {
		return VerifiedBundle{}, &Error{Code: CodeBackupTargetInvalid, Message: "compare backup target with source data directory", Cause: err}
	}
	if targetInsideSource {
		return VerifiedBundle{}, &Error{Code: CodeBackupTargetInvalid, Message: "backup target must be outside the source data directory"}
	}
	if recoveryStagingPath("backup", targetPath) == sourceDataDir {
		return VerifiedBundle{}, &Error{Code: CodeBackupTargetInvalid, Message: "backup staging path collides with the source data directory"}
	}
	if idempotencyKey != "" && (len(idempotencyKey) > 128 || !utf8.ValidString(idempotencyKey) || strings.ContainsRune(idempotencyKey, 0)) {
		return VerifiedBundle{}, &Error{Code: CodeBackupTargetInvalid, Message: "backup idempotency key is invalid"}
	}
	target, err := resolveSecureTarget(targetPath)
	if err != nil {
		return VerifiedBundle{}, &Error{Code: CodeBackupTargetInvalid, Message: "backup target parent is unsafe", Cause: err}
	}
	defer target.Close()

	sourceRoot, sourceStat, err := openAbsoluteDirectoryNoFollow(sourceDataDir)
	if err != nil || sourceStat.Uid != uint32(os.Geteuid()) || sourceStat.Mode&0o777 != bundleDirectoryMode {
		if sourceRoot != nil {
			_ = sourceRoot.Close()
		}
		return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "backup source data directory must be exact owner-controlled mode 0700 with no symlink ancestors", Cause: err}
	}
	defer sourceRoot.Close()
	if err := sourceRoot.mkdirAll("maintenance/backup-create/receipts"); err != nil {
		return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "initialize private backup receipt storage", Cause: err}
	}
	lock, err := openOrCreatePrivateFile(sourceRoot, "maintenance/backup-create/create.lock")
	if err != nil {
		return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "open private backup creation lock", Cause: err}
	}
	defer lock.Close()
	if err := flockWithContext(ctx, lock); err != nil {
		return VerifiedBundle{}, recoveryContextError("acquire backup creation lock", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	receipts, _, err := sourceRoot.openRelativeDirectory("maintenance/backup-create/receipts")
	if err != nil {
		return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "open private backup receipt directory", Cause: err}
	}
	defer receipts.Close()

	requestDigest := sha256.Sum256([]byte("crewfold.backup.create.v1\n" + targetPath))
	requestSHA256 := hex.EncodeToString(requestDigest[:])
	keySHA256 := ""
	var receipt backupCreateReceipt
	hasReceipt := false
	if idempotencyKey != "" {
		keyDigest := sha256.Sum256([]byte(idempotencyKey))
		keySHA256 = hex.EncodeToString(keyDigest[:])
		receipt, hasReceipt, err = readBackupReceipt(receipts, keySHA256)
		if err != nil {
			return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "read durable backup creation receipt", Cause: err}
		}
		if hasReceipt && (receipt.RequestSHA256 != requestSHA256 || receipt.TargetPath != targetPath) {
			return VerifiedBundle{}, &Error{Code: CodeIdempotencyConflict, Message: "backup idempotency key was already bound to a different target request"}
		}
	}

	targetAbsentErr := target.requireAbsent()
	if targetAbsentErr != nil && !errors.Is(targetAbsentErr, os.ErrExist) && !errors.Is(targetAbsentErr, unix.EEXIST) {
		return VerifiedBundle{}, &Error{Code: CodeBackupTargetInvalid, Message: "inspect backup target", Cause: targetAbsentErr}
	}
	if targetAbsentErr != nil {
		if !hasReceipt {
			return VerifiedBundle{}, &Error{Code: CodeBackupTargetExists, Message: "backup target already exists"}
		}
		verified, verifyErr := VerifyBundle(ctx, targetPath)
		if verifyErr != nil {
			return VerifiedBundle{}, verifyErr
		}
		if verified.Manifest.BackupID != receipt.BackupID || verified.Manifest.CreatedAt != receipt.CreatedAt ||
			(receipt.State == "complete" && verified.ManifestSHA256 != receipt.ManifestSHA256) {
			return VerifiedBundle{}, &Error{Code: CodeBackupIntegrityFailed, Message: "existing backup target does not match its durable creation receipt"}
		}
		if receipt.State == "pending" {
			receipt.State = "complete"
			receipt.ManifestSHA256 = verified.ManifestSHA256
			if err := writeBackupReceipt(receipts, receipt); err != nil {
				return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "complete reconciled backup receipt", Cause: err}
			}
		}
		return verified, nil
	}
	if hasReceipt && receipt.State == "complete" {
		return VerifiedBundle{}, &Error{Code: CodeBackupIntegrityFailed, Message: "completed backup target is missing"}
	}
	if !hasReceipt {
		backupID, idErr := newBackupID()
		if idErr != nil {
			return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "generate backup identity", Cause: idErr}
		}
		receipt = backupCreateReceipt{
			Schema: backupReceiptSchema, State: "pending", KeySHA256: keySHA256, RequestSHA256: requestSHA256,
			TargetPath: targetPath, BackupID: backupID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		if idempotencyKey != "" {
			if err := writeBackupReceipt(receipts, receipt); err != nil {
				return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "persist pending backup receipt", Cause: err}
			}
		}
	}

	stagingName, staging, releaseStaging, err := target.createStaging(ctx, "backup")
	if err != nil {
		return VerifiedBundle{}, &Error{Code: CodeBackupTargetInvalid, Message: "create private backup staging directory", Cause: err}
	}
	defer releaseStaging()
	published := false
	defer func() {
		if staging != nil {
			_ = staging.Close()
		}
		if !published {
			_ = target.cleanupStaging(stagingName)
		}
	}()

	snapshot, err := source.BackupSnapshot(ctx, filepath.Join(staging.path, "crewfold.db"))
	if err != nil {
		return VerifiedBundle{}, mapSnapshotCreationError(ctx, err)
	}
	if err := callRecoveryBoundary(ctx, "interrupt after database snapshot", hooks.afterSnapshot); err != nil {
		return VerifiedBundle{}, err
	}
	databaseFile, databaseMetadata, err := staging.openRelativeFile("crewfold.db", unix.O_RDONLY, true)
	if err != nil || validatePrivateRegular(databaseMetadata, maximumDatabaseSize) != nil || databaseMetadata.size != snapshot.ByteSize {
		if databaseFile != nil {
			_ = databaseFile.Close()
		}
		return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "captured database snapshot is unsafe or changed", Cause: err}
	}
	integrity, err := store.VerifyDatabaseSnapshotFile(ctx, databaseFile, store.CanonicalVerifyOptions{Full: true})
	_ = databaseFile.Close()
	if err != nil || !integrity.Complete || integrity.Status != "ok" {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return VerifiedBundle{}, recoveryContextError("verify captured database", ctxErr)
		}
		return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "captured database failed full canonical integrity", Cause: err}
	}
	if !integrity.Quiescence.Quiescent {
		return VerifiedBundle{}, &Error{
			Code: CodeBackupNotQuiescent, Message: "captured database contains actionable nonterminal work",
			Quiescence: &BackupNotQuiescentDetails{Counts: integrity.Quiescence.Counts, Samples: append([]store.QuiescenceBlocker(nil), integrity.QuiescenceBlockers...)},
		}
	}
	artifactReport, err := VerifyLiveArtifacts(ctx, sourceDataDir, integrity.ArtifactReferences)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return VerifiedBundle{}, recoveryContextError("verify source artifact closure", ctxErr)
		}
		return VerifiedBundle{}, err
	}
	if err := artifactReportFailure(artifactReport); err != nil {
		return VerifiedBundle{}, err
	}
	entries := ArtifactEntries(integrity.ArtifactReferences)
	for _, entry := range entries {
		directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(entry.Path)))
		if err := staging.mkdirAll(directory); err != nil {
			return VerifiedBundle{}, &Error{Code: CodeBackupTargetInvalid, Message: "create backup artifact staging directory", Cause: err}
		}
		if err := copySecurePayload(ctx, sourceRoot, entry.Path, staging, entry.Path, entry.Size, entry.SHA256); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return VerifiedBundle{}, recoveryContextError("copy immutable source artifact", ctxErr)
			}
			return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "copy immutable source artifact " + entry.Path, Cause: err}
		}
	}
	if err := callRecoveryBoundary(ctx, "interrupt after artifact copy", hooks.afterArtifacts); err != nil {
		return VerifiedBundle{}, err
	}
	manifest, err := NewManifest(receipt.BackupID, receipt.CreatedAt, snapshot, integrity, entries)
	if err != nil {
		return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "construct backup manifest from captured cut", Cause: err}
	}
	if _, err := WriteManifest(staging.path, manifest); err != nil {
		return VerifiedBundle{}, err
	}
	if err := callRecoveryBoundary(ctx, "interrupt after manifest publication", hooks.afterManifest); err != nil {
		return VerifiedBundle{}, err
	}
	verified, err := verifyBundleAtPath(ctx, staging.path)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return VerifiedBundle{}, recoveryContextError("verify staged backup bundle", ctxErr)
		}
		return VerifiedBundle{}, err
	}
	if err := staging.Close(); err != nil {
		staging = nil
		return VerifiedBundle{}, &Error{Code: CodeBackupTargetInvalid, Message: "close complete backup staging directory", Cause: err}
	}
	staging = nil
	if err := target.publish(stagingName); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return VerifiedBundle{}, &Error{Code: CodeBackupTargetExists, Message: "backup target appeared before publication", Cause: err}
		}
		return VerifiedBundle{}, &Error{Code: CodeBackupTargetInvalid, Message: "publish complete backup bundle", Cause: err}
	}
	published = true
	verified.Root = targetPath
	if err := callRecoveryBoundary(ctx, "interrupt after backup publication", hooks.afterPublish); err != nil {
		return VerifiedBundle{}, err
	}
	if idempotencyKey != "" {
		receipt.State = "complete"
		receipt.ManifestSHA256 = verified.ManifestSHA256
		if err := writeBackupReceipt(receipts, receipt); err != nil {
			return VerifiedBundle{}, &Error{Code: CodeBackupSourceUnhealthy, Message: "persist completed backup receipt", Cause: err}
		}
	}
	if err := callRecoveryBoundary(ctx, "interrupt before backup response", hooks.beforeResponse); err != nil {
		return VerifiedBundle{}, err
	}
	return verified, nil
}

func callRecoveryBoundary(ctx context.Context, message string, hook func() error) error {
	if hook == nil {
		return nil
	}
	err := hook()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return recoveryContextError(message, ctxErr)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return recoveryContextError(message, err)
	}
	return err
}

func flockWithContext(ctx context.Context, file *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func readBackupReceipt(directory *secureDirectory, keySHA256 string) (backupCreateReceipt, bool, error) {
	name := keySHA256 + ".json"
	data, err := readSecureRegular(directory, name, maximumBackupReceiptSize)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return backupCreateReceipt{}, false, nil
	}
	if err != nil {
		return backupCreateReceipt{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt backupCreateReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return backupCreateReceipt{}, false, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return backupCreateReceipt{}, false, errors.New("backup receipt has trailing JSON")
	}
	canonical, err := marshalBackupReceipt(receipt)
	if err != nil || !bytes.Equal(canonical, data) || !validBackupReceipt(receipt, keySHA256) {
		return backupCreateReceipt{}, false, errors.New("backup receipt is not exact canonical current state")
	}
	return receipt, true, nil
}

func writeBackupReceipt(directory *secureDirectory, receipt backupCreateReceipt) error {
	if !validBackupReceipt(receipt, receipt.KeySHA256) {
		return errors.New("backup receipt is invalid")
	}
	data, err := marshalBackupReceipt(receipt)
	if err != nil {
		return err
	}
	return replaceSecureFileAtomic(directory, receipt.KeySHA256+".json", data)
}

func marshalBackupReceipt(receipt backupCreateReceipt) ([]byte, error) {
	data, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > maximumBackupReceiptSize {
		return nil, errors.New("backup receipt exceeds its bound")
	}
	return data, nil
}

func validBackupReceipt(receipt backupCreateReceipt, keySHA256 string) bool {
	return receipt.Schema == backupReceiptSchema && (receipt.State == "pending" || receipt.State == "complete") &&
		sha256Pattern.MatchString(receipt.KeySHA256) && receipt.KeySHA256 == keySHA256 && sha256Pattern.MatchString(receipt.RequestSHA256) &&
		receipt.TargetPath != "" && filepath.IsAbs(receipt.TargetPath) && filepath.Clean(receipt.TargetPath) == receipt.TargetPath &&
		backupIDPattern.MatchString(receipt.BackupID) && canonicalTimestamp(receipt.CreatedAt) &&
		((receipt.State == "pending" && receipt.ManifestSHA256 == "") || (receipt.State == "complete" && sha256Pattern.MatchString(receipt.ManifestSHA256)))
}

func newBackupID() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return "backup_" + hex.EncodeToString(randomBytes), nil
}

func mapSnapshotCreationError(ctx context.Context, err error) error {
	if ctxErr := context.Cause(ctx); ctxErr != nil {
		return recoveryContextError("capture database snapshot", ctxErr)
	}
	if store.ErrorCode(err) == store.CodeDatabaseBusy {
		return &Error{Code: CodeDatabaseBusy, Message: "database remained busy while capturing backup", Cause: err}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return recoveryContextError("capture database snapshot", err)
	}
	return &Error{Code: CodeBackupSourceUnhealthy, Message: "capture database snapshot", Cause: err}
}

func recoveryContextError(message string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeOperationCancelled, Message: message, Cause: err}
	}
	return &Error{Code: CodeDatabaseBusy, Message: message, Cause: err}
}
