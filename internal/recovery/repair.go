package recovery

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"crewfold/internal/store"
	"golang.org/x/sys/unix"
)

const maximumRepairFindings = 20

const (
	repairScratchRootPrefix  = "crewfold-recovery-v1-"
	repairScratchStagePrefix = ".crewfold-recovery-v1-stage-"
	repairScratchLockName    = ".crewfold-repair-root.lock"
	repairScratchMarker      = "crewfold-repair-scratch-owner-v1\n"
)

var repairScratchProcessSlot = func() chan struct{} {
	result := make(chan struct{}, 1)
	result <- struct{}{}
	return result
}()

type repairScratch struct {
	target    *secureTarget
	temporary *secureDirectory
	lifetime  *os.File
	rootName  string
	stageName string
	slotOwned bool
	closed    bool
}

// InspectOffline acquires the selected directory's existing daemon lock,
// copies its DB/WAL/SHM bytes without following links, and runs recovery plus
// full canonical verification only against that private disposable copy.
func InspectOffline(ctx context.Context, dataDir string) (RepairInspection, error) {
	report := RepairInspection{
		Path: dataDir, Status: "failed", Findings: []RepairFinding{},
		Integrity: store.CanonicalIntegrityReport{},
		Artifacts: ArtifactFilesystemReport{Status: "failed", Issues: []ArtifactFilesystemIssue{}, Warnings: []ArtifactFilesystemIssue{}},
	}
	dataDir, err := exactSelectedRecoveryPath(dataDir)
	if err != nil {
		return report, &Error{Code: CodeRepairTargetInvalid, Message: "repair target must be a canonical absolute path", Cause: err}
	}
	report.Path = dataDir
	root, rootStat, err := openAbsoluteDirectoryNoFollow(dataDir)
	if err != nil {
		return report, &Error{Code: CodeRepairTargetInvalid, Message: "open repair target without following links", Cause: err}
	}
	defer root.Close()
	if rootStat.Uid != uint32(os.Geteuid()) || rootStat.Mode&0o777 != bundleDirectoryMode {
		return report, &Error{Code: CodeRepairTargetInvalid, Message: "repair target must be exact owner-controlled mode 0700"}
	}
	lock, err := acquireRepairDataLock(root)
	if err != nil {
		return report, err
	}
	defer releaseDataLock(lock)
	if err := ctx.Err(); err != nil {
		return report, recoveryContextError("inspect offline repair target", err)
	}

	scratch, err := prepareRepairScratch(ctx, os.TempDir())
	if err != nil {
		return report, &Error{Code: CodeRepairTargetInvalid, Message: "open private repair inspection scratch root", Cause: err}
	}
	defer scratch.Close()
	stagingName, copyRoot, releaseStaging, err := scratch.target.createStaging(ctx, "repair")
	if err != nil {
		return report, &Error{Code: CodeRepairTargetInvalid, Message: "create retry-cleaned private repair inspection directory", Cause: err}
	}
	defer releaseStaging()
	defer func() {
		_ = copyRoot.Close()
		_ = scratch.target.cleanupStaging(stagingName)
	}()
	temporary := copyRoot.path

	databaseBytes, err := copyRepairFile(ctx, root, copyRoot, "crewfold.db", true)
	if err != nil {
		return report, err
	}
	report.Copied.DatabaseBytes = databaseBytes
	walBytes, walPresent, err := copyOptionalRepairFile(ctx, root, copyRoot, "crewfold.db-wal")
	if err != nil {
		return report, err
	}
	report.Copied.WALBytes, report.Copied.WALPresent = walBytes, walPresent
	shmBytes, shmPresent, err := copyOptionalRepairFile(ctx, root, copyRoot, "crewfold.db-shm")
	if err != nil {
		return report, err
	}
	report.Copied.SHMBytes, report.Copied.SHMPresent = shmBytes, shmPresent
	if databaseBytes > maximumBundlePayloadBytes-walBytes || databaseBytes+walBytes > maximumBundlePayloadBytes-shmBytes {
		return report, &Error{Code: CodeResourceLimitExceeded, Message: "repair DB/WAL/SHM copy exceeds the documented payload safety bound"}
	}
	if err := copyRoot.Sync(); err != nil {
		return report, &Error{Code: CodeRepairTargetInvalid, Message: "sync private repair inspection copy", Cause: err}
	}

	integrity, verifyErr := store.VerifyDatabaseRecoveryCopy(ctx, filepath.Join(temporary, "crewfold.db"), store.CanonicalVerifyOptions{Full: true})
	report.Integrity = integrity
	if ctxErr := ctx.Err(); ctxErr != nil {
		return report, recoveryContextError("verify offline repair copy", ctxErr)
	}
	if verifyErr != nil {
		code := "database_unreadable"
		summary := boundedRepairText(verifyErr.Error())
		if store.ErrorCode(verifyErr) == store.CodeCurrentBaselineMismatch {
			code = CodeCurrentBaselineMismatch
			summary = "database does not match the exact embedded current baseline"
		}
		appendRepairFinding(&report, RepairFinding{
			Code: code, Status: "failed", Summary: summary, Remediation: "restore_verified_backup",
		})
		report.Artifacts = unavailableRepairArtifactReport("artifact closure could not be derived from the unreadable database copy")
		return finalizeRepairInspection(report), nil
	}

	if integrity.Complete {
		artifacts, artifactErr := VerifyLiveArtifacts(ctx, dataDir, integrity.ArtifactReferences)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return report, recoveryContextError("verify offline repair artifact closure", ctxErr)
		}
		if artifactErr != nil {
			report.Artifacts = unavailableRepairArtifactReport(artifactErr.Error())
		} else {
			report.Artifacts = artifacts
		}
	} else {
		report.Artifacts = unavailableRepairArtifactReport("artifact closure was not available because full canonical verification did not complete")
	}
	buildRepairFindings(&report)
	return finalizeRepairInspection(report), nil
}

func prepareRepairScratch(ctx context.Context, temporaryPath string) (*repairScratch, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-repairScratchProcessSlot:
	}
	scratch := &repairScratch{slotOwned: true}
	fail := func(err error) (*repairScratch, error) {
		_ = scratch.Close()
		return nil, err
	}

	temporaryPath, err := exactAbsolutePath(temporaryPath)
	if err != nil {
		return fail(err)
	}
	temporary, _, err := openAbsoluteDirectoryNoFollow(temporaryPath)
	if err != nil {
		return fail(err)
	}
	scratch.temporary = temporary
	if err := flockWithContext(ctx, temporary.file); err != nil {
		return fail(err)
	}
	temporaryLocked := true
	defer func() {
		if temporaryLocked {
			_ = unix.Flock(int(temporary.file.Fd()), unix.LOCK_UN)
		}
	}()
	if err := reapRepairScratchRoots(temporary); err != nil {
		return fail(err)
	}

	randomBytes := make([]byte, 12)
	if _, err := rand.Read(randomBytes); err != nil {
		return fail(err)
	}
	uid, pid := os.Geteuid(), os.Getpid()
	stageName := fmt.Sprintf("%s%d-%d-%s", repairScratchStagePrefix, uid, pid, hex.EncodeToString(randomBytes))
	rootName := fmt.Sprintf("%s%d-%d", repairScratchRootPrefix, uid, pid)
	if !validRepairScratchStageName(stageName) || !validRepairScratchRootName(rootName) {
		return fail(errors.New("generated repair scratch name is invalid"))
	}
	if err := unix.Mkdirat(int(temporary.file.Fd()), stageName, bundleDirectoryMode); err != nil {
		return fail(err)
	}
	scratch.stageName = stageName
	if err := temporary.Sync(); err != nil {
		return fail(err)
	}
	stage, metadata, err := temporary.openRelativeDirectory(stageName)
	if err != nil || metadata.uid != uint32(uid) || metadata.mode&0o777 != bundleDirectoryMode {
		if stage != nil {
			_ = stage.Close()
		}
		if err != nil {
			return fail(err)
		}
		return fail(errors.New("repair scratch stage is not exact private storage"))
	}
	scratch.target = &secureTarget{absolute: filepath.Join(temporaryPath, rootName, "repair-copy"), name: "repair-copy", parent: stage}
	lifetime, err := openOrCreatePrivateFile(stage, repairScratchLockName)
	if err != nil {
		return fail(err)
	}
	scratch.lifetime = lifetime
	if err := flockWithContext(ctx, lifetime); err != nil {
		return fail(err)
	}
	if err := writeRepairScratchMarker(lifetime, stage); err != nil {
		return fail(err)
	}
	if err := unix.Renameat2(int(temporary.file.Fd()), stageName, int(temporary.file.Fd()), rootName, unix.RENAME_NOREPLACE); err != nil {
		return fail(err)
	}
	scratch.rootName = rootName
	scratch.stageName = ""
	stage.path = filepath.Join(temporaryPath, rootName)
	if err := temporary.Sync(); err != nil {
		return fail(err)
	}
	if err := unix.Flock(int(temporary.file.Fd()), unix.LOCK_UN); err != nil {
		return fail(err)
	}
	temporaryLocked = false
	return scratch, nil
}

func (scratch *repairScratch) Close() error {
	if scratch == nil || scratch.closed {
		return nil
	}
	scratch.closed = true
	var result error
	if scratch.target != nil {
		result = errors.Join(result, scratch.target.Close())
	}
	removeName := scratch.rootName
	if removeName == "" {
		removeName = scratch.stageName
	}
	if scratch.temporary != nil && removeName != "" {
		if err := flockWithContext(context.Background(), scratch.temporary.file); err != nil {
			result = errors.Join(result, err)
		} else {
			result = errors.Join(result, removeTreeAt(int(scratch.temporary.file.Fd()), removeName), scratch.temporary.Sync())
			result = errors.Join(result, unix.Flock(int(scratch.temporary.file.Fd()), unix.LOCK_UN))
		}
	}
	if scratch.lifetime != nil {
		result = errors.Join(result, unix.Flock(int(scratch.lifetime.Fd()), unix.LOCK_UN), scratch.lifetime.Close())
	}
	if scratch.temporary != nil {
		result = errors.Join(result, scratch.temporary.Close())
	}
	if scratch.slotOwned {
		repairScratchProcessSlot <- struct{}{}
		scratch.slotOwned = false
	}
	return result
}

func reapRepairScratchRoots(temporary *secureDirectory) error {
	listingFD, err := unix.Openat(int(temporary.file.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	listing := os.NewFile(uintptr(listingFD), temporary.path)
	defer listing.Close()
	for {
		names, readErr := listing.Readdirnames(256)
		sort.Strings(names)
		for _, name := range names {
			// Random stage-looking names are never ownership proof: a crash can
			// happen between mkdir and marker creation, and an unrelated owner
			// directory may match the grammar. Only a completed private root with
			// Crewfold's nofollow lifetime marker is eligible for stale cleanup.
			if !validRepairScratchRootName(name) {
				continue
			}
			candidate, directoryMetadata, err := temporary.openRelativeDirectory(name)
			if err != nil {
				continue
			}
			if directoryMetadata.uid != uint32(os.Geteuid()) || directoryMetadata.mode&0o777 != bundleDirectoryMode {
				_ = candidate.Close()
				continue
			}
			lifetime, _, markerErr := candidate.openRelativeFile(repairScratchLockName, unix.O_RDWR, true)
			if markerErr != nil {
				if lifetime != nil {
					_ = lifetime.Close()
				}
				_ = candidate.Close()
				continue
			}
			if lockErr := unix.Flock(int(lifetime.Fd()), unix.LOCK_EX|unix.LOCK_NB); lockErr != nil {
				_ = lifetime.Close()
				_ = candidate.Close()
				continue
			}
			if !exactRepairScratchMarker(lifetime) {
				_ = unix.Flock(int(lifetime.Fd()), unix.LOCK_UN)
				_ = lifetime.Close()
				_ = candidate.Close()
				continue
			}
			_ = candidate.Close()
			if err := removeTreeAt(int(temporary.file.Fd()), name); err != nil {
				_ = unix.Flock(int(lifetime.Fd()), unix.LOCK_UN)
				_ = lifetime.Close()
				return fmt.Errorf("remove stale repair scratch %q: %w", name, err)
			}
			_ = unix.Flock(int(lifetime.Fd()), unix.LOCK_UN)
			_ = lifetime.Close()
		}
		if errors.Is(readErr, io.EOF) {
			return temporary.Sync()
		}
		if readErr != nil {
			return readErr
		}
	}
}

func writeRepairScratchMarker(lifetime *os.File, directory *secureDirectory) error {
	if lifetime == nil || directory == nil {
		return errors.New("repair scratch ownership marker target is missing")
	}
	if err := lifetime.Truncate(0); err != nil {
		return err
	}
	written, err := lifetime.WriteAt([]byte(repairScratchMarker), 0)
	if err == nil && written != len(repairScratchMarker) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = lifetime.Sync()
	}
	if err == nil {
		err = directory.Sync()
	}
	return err
}

func exactRepairScratchMarker(lifetime *os.File) bool {
	if lifetime == nil {
		return false
	}
	metadata, err := metadataForDescriptor(int(lifetime.Fd()))
	if err != nil || validatePrivateRegular(metadata, int64(len(repairScratchMarker))) != nil || metadata.size != int64(len(repairScratchMarker)) {
		return false
	}
	data := make([]byte, len(repairScratchMarker))
	if _, err := lifetime.ReadAt(data, 0); err != nil {
		return false
	}
	return bytes.Equal(data, []byte(repairScratchMarker))
}

func validRepairScratchRootName(name string) bool {
	prefix := fmt.Sprintf("%s%d-", repairScratchRootPrefix, os.Geteuid())
	return strings.HasPrefix(name, prefix) && decimalProcessID(strings.TrimPrefix(name, prefix))
}

func validRepairScratchStageName(name string) bool {
	prefix := fmt.Sprintf("%s%d-", repairScratchStagePrefix, os.Geteuid())
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(name, prefix)
	separator := strings.IndexByte(remainder, '-')
	if separator <= 0 || !decimalProcessID(remainder[:separator]) {
		return false
	}
	digest := remainder[separator+1:]
	if len(digest) != 24 {
		return false
	}
	for _, character := range digest {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func decimalProcessID(value string) bool {
	if value == "" || len(value) > 20 || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func acquireRepairDataLock(root *secureDirectory) (*os.File, error) {
	lock, metadata, err := root.openRelativeFile("daemon.lock", unix.O_RDWR, true)
	if err != nil {
		return nil, &Error{Code: CodeRepairTargetInvalid, Message: "repair requires an existing nofollow-safe daemon lock", Cause: err}
	}
	if err := validatePrivateRegular(metadata, 1<<20); err != nil {
		_ = lock.Close()
		return nil, &Error{Code: CodeRepairTargetInvalid, Message: "repair daemon lock is unsafe", Cause: err}
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, &Error{Code: CodeRepairSourceInUse, Message: "repair target is owned by a live daemon", Cause: err}
		}
		return nil, &Error{Code: CodeRepairTargetInvalid, Message: "lock repair target", Cause: err}
	}
	return lock, nil
}

func copyOptionalRepairFile(ctx context.Context, source, destination *secureDirectory, name string) (int64, bool, error) {
	present, err := secureEntryPresent(source, name)
	if err != nil {
		return 0, false, &Error{Code: CodeRepairTargetInvalid, Message: "inspect optional repair file " + name, Cause: err}
	}
	if !present {
		return 0, false, nil
	}
	size, err := copyRepairFile(ctx, source, destination, name, false)
	return size, true, err
}

func copyRepairFile(ctx context.Context, source, destination *secureDirectory, name string, required bool) (int64, error) {
	if !required {
		return copyRepairAuxiliaryFile(ctx, source, destination, name)
	}
	size, digest, err := hashSecureRegular(ctx, source, name, maximumDatabaseSize)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, recoveryContextError("copy offline repair bytes", ctxErr)
		}
		message := "repair database is missing, unsafe, or exceeds its bound"
		if !required {
			message = "repair WAL/SHM file is unsafe or exceeds its bound"
		}
		return 0, &Error{Code: CodeRepairTargetInvalid, Message: message, Cause: err}
	}
	if err := copySecurePayload(ctx, source, name, destination, name, size, digest); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, recoveryContextError("copy offline repair bytes", ctxErr)
		}
		return 0, &Error{Code: CodeRepairTargetInvalid, Message: "copy nofollow-safe repair file " + name, Cause: err}
	}
	return size, nil
}

func copyRepairAuxiliaryFile(ctx context.Context, source, destination *secureDirectory, name string) (int64, error) {
	sourceFile, metadata, err := source.openRelativeFile(name, unix.O_RDONLY, true)
	if err != nil {
		return 0, &Error{Code: CodeRepairTargetInvalid, Message: "open nofollow-safe repair WAL/SHM file", Cause: err}
	}
	defer sourceFile.Close()
	// SQLite derives WAL/SHM creation modes from the process umask and may leave
	// group/other read bits set. The exact 0700 owner-controlled root prevents
	// traversal by those users; require an unaliased owner-readable regular inode
	// and always publish only a 0600 private copy.
	if metadata.mode&unix.S_IFMT != unix.S_IFREG || metadata.uid != uint32(os.Geteuid()) || metadata.nlink != 1 ||
		metadata.mode&0o700 != 0o600 || metadata.mode&0o111 != 0 || metadata.size < 0 || metadata.size > maximumDatabaseSize {
		return 0, &Error{Code: CodeRepairTargetInvalid, Message: "repair WAL/SHM file is unsafe or exceeds its bound"}
	}
	destinationFile, err := destination.createRelativeFile(name, bundleFileMode)
	if err != nil {
		return 0, &Error{Code: CodeRepairTargetInvalid, Message: "create private repair WAL/SHM copy", Cause: err}
	}
	written, copyErr := copyWithContext(ctx, destinationFile, io.LimitReader(sourceFile, metadata.size+1))
	if copyErr == nil && written != metadata.size {
		copyErr = errors.New("repair WAL/SHM size changed while being copied")
	}
	if copyErr == nil {
		copyErr = destinationFile.Sync()
	}
	closeErr := destinationFile.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil {
		copyErr = destination.syncRelativeParent(name)
	}
	if copyErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, recoveryContextError("copy offline repair WAL/SHM bytes", ctxErr)
		}
		return 0, &Error{Code: CodeRepairTargetInvalid, Message: "copy private repair WAL/SHM bytes", Cause: copyErr}
	}
	return written, nil
}

func unavailableRepairArtifactReport(detail string) ArtifactFilesystemReport {
	report := ArtifactFilesystemReport{
		Status: "failed", Complete: false, IssueCount: 1,
		Issues:   []ArtifactFilesystemIssue{{Code: "artifact_closure_unavailable", Path: ".", Detail: boundedRepairText(detail)}},
		Warnings: []ArtifactFilesystemIssue{},
	}
	return report
}

func buildRepairFindings(report *RepairInspection) {
	baselineFailure := false
	canonicalFailures := int64(0)
	for _, failure := range report.Integrity.Failures {
		if failure.Check == "current_baseline" {
			baselineFailure = true
			continue
		}
		canonicalFailures++
	}
	if baselineFailure {
		appendRepairFinding(report, RepairFinding{
			Code: CodeCurrentBaselineMismatch, Status: "failed", Summary: "database does not match the exact embedded current baseline", Remediation: "restore_verified_backup",
		})
	}
	if !report.Integrity.Complete || canonicalFailures != 0 || report.Integrity.Status != "ok" && !baselineFailure {
		appendRepairFinding(report, RepairFinding{
			Code: CodeCanonicalIntegrityFailed, Status: "failed",
			Summary:     fmt.Sprintf("full canonical verification completed=%t with %d non-baseline failures", report.Integrity.Complete, canonicalFailures),
			Remediation: "restore_verified_backup",
		})
	}
	for _, projection := range report.Integrity.DerivedProjections {
		if projection.Status != "ok" {
			appendRepairFinding(report, RepairFinding{
				Code: "derived_knowledge_index", Status: "warning", Summary: boundedRepairText(projection.Diagnosis), Remediation: "rebuild_derived_index",
			})
		}
	}
	if report.Integrity.Complete && !report.Integrity.Quiescence.Quiescent {
		appendRepairFinding(report, RepairFinding{
			Code: CodeRestoreUnsafeNonterminal, Status: "warning", Summary: "database contains actionable nonterminal work, bindings, or unsettled external-effect queues", Remediation: "retire_lost_runtime",
		})
	}
	if report.Artifacts.IssueCount != 0 || !report.Artifacts.Complete {
		appendRepairFinding(report, RepairFinding{
			Code: "artifact_integrity", Status: "failed", Summary: fmt.Sprintf("artifact verification found %d issues", report.Artifacts.IssueCount), Remediation: "restore_verified_backup",
		})
	} else if report.Artifacts.WarningCount != 0 {
		appendRepairFinding(report, RepairFinding{
			Code: "orphan_artifacts", Status: "warning", Summary: fmt.Sprintf("artifact verification found %d unreferenced private entries", report.Artifacts.WarningCount), Remediation: "retry",
		})
	}
	if len(report.Findings) == 0 {
		appendRepairFinding(report, RepairFinding{Code: "offline_integrity", Status: "ok", Summary: "private recovery copy passed the exact current full verifier", Remediation: "retry"})
	}
}

func appendRepairFinding(report *RepairInspection, finding RepairFinding) {
	if len(report.Findings) >= maximumRepairFindings {
		return
	}
	finding.Summary = boundedRepairText(finding.Summary)
	report.Findings = append(report.Findings, finding)
}

func finalizeRepairInspection(report RepairInspection) RepairInspection {
	report.Status = "ok"
	for _, finding := range report.Findings {
		if finding.Status == "failed" {
			report.Status = "failed"
			return report
		}
		if finding.Status == "warning" {
			report.Status = "degraded"
		}
	}
	return report
}

func boundedRepairText(value string) string {
	value = strings.Map(func(character rune) rune {
		if character == 0 || character < 0x20 && character != '\t' {
			return -1
		}
		return character
	}, value)
	if len(value) <= 2048 {
		return value
	}
	end := 0
	for index := range value {
		if index > 2048 {
			break
		}
		end = index
	}
	if end == 0 {
		return "repair finding exceeded its text bound"
	}
	return value[:end]
}
