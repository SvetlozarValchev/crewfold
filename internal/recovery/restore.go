package recovery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const restorePendingSchema = "urn:crewfold:schema:restore-pending:v1"

type restoreHooks struct {
	afterDatabase  func() error
	afterArtifacts func() error
	afterSeal      func() error
	afterPublish   func() error
	beforeResponse func() error
}

func RestorePending(ctx context.Context, bundleRoot, target string) (PendingRestore, error) {
	return restorePendingWithHooks(ctx, bundleRoot, target, restoreHooks{})
}

func restorePendingWithHooks(ctx context.Context, bundleRoot, target string, hooks restoreHooks) (PendingRestore, error) {
	bundleRoot, err := exactSelectedRecoveryPath(bundleRoot)
	if err != nil {
		return PendingRestore{}, integrityError("restore bundle path must be canonical, absolute, and outside reserved staging", err)
	}
	verified, bundleDirectory, err := openVerifiedBundle(ctx, bundleRoot)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PendingRestore{}, recoveryContextError("verify restore bundle", ctxErr)
		}
		return PendingRestore{}, err
	}
	defer bundleDirectory.Close()
	target, err = exactSelectedRecoveryPath(target)
	if err != nil {
		return PendingRestore{}, &Error{Code: CodeBackupTargetInvalid, Message: "restore target must be a canonical absolute path", Cause: err}
	}
	targetInsideBundle, err := pathContains(verified.Root, target)
	if err != nil {
		return PendingRestore{}, &Error{Code: CodeBackupTargetInvalid, Message: "compare restore target with source bundle", Cause: err}
	}
	if targetInsideBundle {
		return PendingRestore{}, &Error{Code: CodeBackupTargetInvalid, Message: "restore target must be outside the source bundle"}
	}
	if recoveryStagingPath("restore", target) == verified.Root {
		return PendingRestore{}, &Error{Code: CodeBackupTargetInvalid, Message: "restore staging path collides with the source bundle"}
	}
	secureTarget, err := prepareSecureTarget(target)
	if err != nil {
		code := CodeBackupTargetInvalid
		message := "restore target or parent is unsafe"
		if errors.Is(err, os.ErrExist) || errors.Is(err, unix.EEXIST) {
			code = CodeRestoreTargetExists
			message = "restore target already exists"
		}
		return PendingRestore{}, &Error{Code: code, Message: message, Cause: err}
	}
	defer secureTarget.Close()
	stagingName, stagingDirectory, releaseStaging, err := secureTarget.createStaging(ctx, "restore")
	if err != nil {
		return PendingRestore{}, &Error{Code: CodeBackupTargetInvalid, Message: "create restore staging directory", Cause: err}
	}
	defer releaseStaging()
	published := false
	defer func() {
		if stagingDirectory != nil {
			_ = stagingDirectory.Close()
		}
		if !published {
			_ = secureTarget.cleanupStaging(stagingName)
		}
	}()
	if err := copySecurePayload(ctx, bundleDirectory, "crewfold.db", stagingDirectory, "crewfold.db", verified.Manifest.Database.Size, verified.Manifest.Database.SHA256); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PendingRestore{}, recoveryContextError("copy restored database", ctxErr)
		}
		return PendingRestore{}, integrityError("copy restored database", err)
	}
	if err := callRecoveryBoundary(ctx, "interrupt after restored database copy", hooks.afterDatabase); err != nil {
		return PendingRestore{}, err
	}
	for _, entry := range verified.Manifest.Entries {
		directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(entry.Path)))
		if err := stagingDirectory.mkdirAll(directory); err != nil {
			return PendingRestore{}, &Error{Code: CodeBackupTargetInvalid, Message: "create restored artifact directory", Cause: err}
		}
		if err := copySecurePayload(ctx, bundleDirectory, entry.Path, stagingDirectory, entry.Path, entry.Size, entry.SHA256); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return PendingRestore{}, recoveryContextError("copy restored artifact", ctxErr)
			}
			return PendingRestore{}, integrityError("copy restored artifact "+entry.Path, err)
		}
	}
	if err := callRecoveryBoundary(ctx, "interrupt after restored artifact copy", hooks.afterArtifacts); err != nil {
		return PendingRestore{}, err
	}
	if err := writeSecureExclusive(stagingDirectory, "daemon.lock", nil); err != nil {
		return PendingRestore{}, &Error{Code: CodeBackupTargetInvalid, Message: "create restored daemon lock", Cause: err}
	}
	pendingBytes, err := marshalRestorePendingSeal(restorePendingSeal{
		Schema: restorePendingSchema, ManifestSHA256: verified.ManifestSHA256, Manifest: verified.Manifest,
	})
	if err != nil {
		return PendingRestore{}, contractError("encode restore pending seal", err)
	}
	if err := writeSecureExclusive(stagingDirectory, ".restore-pending.json", pendingBytes); err != nil {
		return PendingRestore{}, &Error{Code: CodeBackupTargetInvalid, Message: "create restore pending seal", Cause: err}
	}
	if err := callRecoveryBoundary(ctx, "interrupt after restore pending seal", hooks.afterSeal); err != nil {
		return PendingRestore{}, err
	}
	if err := stagingDirectory.Sync(); err != nil {
		return PendingRestore{}, &Error{Code: CodeBackupTargetInvalid, Message: "sync restored payload", Cause: err}
	}
	if err := stagingDirectory.Close(); err != nil {
		stagingDirectory = nil
		return PendingRestore{}, &Error{Code: CodeBackupTargetInvalid, Message: "close restored staging directory", Cause: err}
	}
	stagingDirectory = nil
	if err := secureTarget.publish(stagingName); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return PendingRestore{}, &Error{Code: CodeRestoreTargetExists, Message: "restore target appeared before publication", Cause: err}
		}
		return PendingRestore{}, &Error{Code: CodeBackupTargetInvalid, Message: "publish pending restore", Cause: err}
	}
	published = true
	pending := PendingRestore{
		Path: secureTarget.absolute, BackupID: verified.Manifest.BackupID, ManifestSHA256: verified.ManifestSHA256,
		EventHighWater: verified.Manifest.EventHighWater, LogicalSHA256: verified.Manifest.LogicalSHA256,
	}
	if err := callRecoveryBoundary(ctx, "interrupt after pending restore publication", hooks.afterPublish); err != nil {
		return PendingRestore{}, err
	}
	if err := callRecoveryBoundary(ctx, "interrupt before pending restore response", hooks.beforeResponse); err != nil {
		return PendingRestore{}, err
	}
	return pending, nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
