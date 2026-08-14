package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"crewfold/internal/execution"
	"crewfold/internal/store"
	"golang.org/x/sys/unix"
)

const (
	restoreActivationIntentSchema = "urn:crewfold:schema:restore-activation-intent:v1"
	restoreActivatedSchema        = "urn:crewfold:schema:restore-activated:v1"
	restoreConsumedSchema         = "urn:crewfold:schema:restore-consumed:v1"
	restorePendingMarker          = ".restore-pending.json"
	restoreActivationIntentMarker = ".restore-activation-intent.json"
	restoreActivatedMarker        = ".restore-activated.json"
	restoreConsumedMarker         = ".restore-consumed.json"
	maximumActivationSealSize     = maximumManifestSize + 16<<10
)

var activationNodeIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type restorePendingSeal struct {
	Schema         string   `json:"schema"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	Manifest       Manifest `json:"manifest"`
}

type restoreActivationIntent struct {
	Schema         string `json:"schema"`
	BackupID       string `json:"backup_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ActivatedAt    string `json:"activated_at"`
	SourceRetired  bool   `json:"source_retired"`
}

type restoreActivatedSeal struct {
	Schema           string   `json:"schema"`
	ManifestSHA256   string   `json:"manifest_sha256"`
	Manifest         Manifest `json:"manifest"`
	NodeFingerprint  string   `json:"node_fingerprint"`
	NodeKeySHA256    string   `json:"node_key_sha256"`
	ActivatedAt      string   `json:"activated_at"`
	SourceRetired    bool     `json:"source_retired"`
	ActivationSHA256 string   `json:"activation_sha256"`
}

type restoreConsumedSeal struct {
	Schema           string `json:"schema"`
	BackupID         string `json:"backup_id"`
	ActivationSHA256 string `json:"activation_sha256"`
}

type activationHooks struct {
	afterIntent          func() error
	afterNodeID          func() error
	afterNodeKey         func() error
	afterOperationalRoot func() error
	afterActivatedSeal   func() error
	afterPendingRemoval  func() error
}

type consumptionHooks struct {
	afterConsumedSeal func() error
}

// Activate converts one pending restored directory into an activated but
// first-start-unconsumed installation. It owns the existing daemon lock for the
// whole operation and is resumable at every durable boundary.
func Activate(ctx context.Context, dataDir string, confirmSourceRetired bool) (ActivatedRestore, error) {
	return activateWithHooks(ctx, dataDir, confirmSourceRetired, activationHooks{})
}

func activateWithHooks(ctx context.Context, dataDir string, confirmSourceRetired bool, hooks activationHooks) (ActivatedRestore, error) {
	if !confirmSourceRetired {
		return ActivatedRestore{}, &Error{Code: CodeRestoreSourceRetirementUnconfirmed, Message: "restore activation requires explicit source-retirement confirmation"}
	}
	if err := ctx.Err(); err != nil {
		return ActivatedRestore{}, recoveryContextError("activate restored data directory", err)
	}
	dataDir, err := exactSelectedRecoveryPath(dataDir)
	if err != nil {
		return ActivatedRestore{}, &Error{Code: CodeRestoreNotActivated, Message: "restore target path must be canonical and absolute", Cause: err}
	}
	root, err := openExactPrivateDirectory(dataDir)
	if err != nil {
		return ActivatedRestore{}, &Error{Code: CodeRestoreNotActivated, Message: "restore target must be exact owner-controlled mode 0700 with no symlink ancestors", Cause: err}
	}
	defer root.Close()
	lock, err := acquireExistingDataLock(root, CodeDatabaseBusy)
	if err != nil {
		return ActivatedRestore{}, err
	}
	defer releaseDataLock(lock)

	if present, err := secureEntryPresent(root, restoreConsumedMarker); err != nil {
		return ActivatedRestore{}, activationStateError("inspect consumed activation seal", err)
	} else if present {
		for _, marker := range []string{restorePendingMarker, restoreActivationIntentMarker} {
			conflict, inspectErr := secureEntryPresent(root, marker)
			if inspectErr != nil {
				return ActivatedRestore{}, activationStateError("inspect marker alongside consumed activation seal", inspectErr)
			}
			if conflict {
				return ActivatedRestore{}, activationStateError("consumed restore marker conflicts with another activation marker", nil)
			}
		}
		return ActivatedRestore{}, &Error{Code: CodeRestoreNotActivated, Message: "restore activation was already consumed by a successful first-start verification"}
	}
	activatedPresent, err := secureEntryPresent(root, restoreActivatedMarker)
	if err != nil {
		return ActivatedRestore{}, activationStateError("inspect activated restore seal", err)
	}
	if activatedPresent {
		return reconcileActivated(ctx, root, dataDir, hooks)
	}

	pending, err := readPendingSeal(root)
	if err != nil {
		return ActivatedRestore{}, err
	}
	// A fresh activation must prove the selected cut is still exact and
	// quiescent before recording the retirement assertion or creating any new
	// node authority. Interrupted activations repeat the same proof here before
	// resuming their already-durable intent.
	if err := verifyRestoredCut(ctx, root, pending.ManifestSHA256, pending.Manifest); err != nil {
		return ActivatedRestore{}, err
	}
	intentPresent, err := secureEntryPresent(root, restoreActivationIntentMarker)
	if err != nil {
		return ActivatedRestore{}, activationStateError("inspect restore activation intent", err)
	}
	var intent restoreActivationIntent
	if intentPresent {
		intent, err = readActivationIntent(root)
		if err != nil {
			return ActivatedRestore{}, err
		}
		if intent.BackupID != pending.Manifest.BackupID || intent.ManifestSHA256 != pending.ManifestSHA256 {
			return ActivatedRestore{}, activationStateError("restore activation intent does not match the pending restore", nil)
		}
		if err := verifyActivationTree(ctx, root, pending.Manifest, activationTreePreparing); err != nil {
			return ActivatedRestore{}, err
		}
	} else {
		if err := verifyActivationTree(ctx, root, pending.Manifest, activationTreePending); err != nil {
			return ActivatedRestore{}, err
		}
		intent = restoreActivationIntent{
			Schema: restoreActivationIntentSchema, BackupID: pending.Manifest.BackupID,
			ManifestSHA256: pending.ManifestSHA256, ActivatedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceRetired: true,
		}
		intentBytes, err := marshalActivationIntent(intent)
		if err != nil {
			return ActivatedRestore{}, activationStateError("encode restore activation intent", err)
		}
		if err := writeSecureExclusive(root, restoreActivationIntentMarker, intentBytes); err != nil {
			return ActivatedRestore{}, activationStateError("persist restore activation intent", err)
		}
	}
	if err := callActivationHook(hooks.afterIntent); err != nil {
		return ActivatedRestore{}, err
	}

	nodeID, err := ensureActivationNodeID(root, dataDir)
	if err != nil {
		return ActivatedRestore{}, err
	}
	if err := callActivationHook(hooks.afterNodeID); err != nil {
		return ActivatedRestore{}, err
	}
	nodeKey, err := ensureActivationNodeKey(root, dataDir)
	if err != nil {
		return ActivatedRestore{}, err
	}
	if err := callActivationHook(hooks.afterNodeKey); err != nil {
		return ActivatedRestore{}, err
	}
	for _, name := range []string{"capabilities", "runtime", "check-runtime"} {
		if err := ensureEmptyOperationalRoot(root, name); err != nil {
			return ActivatedRestore{}, err
		}
		if err := callActivationHook(hooks.afterOperationalRoot); err != nil {
			return ActivatedRestore{}, err
		}
	}
	if err := verifyActivationTree(ctx, root, pending.Manifest, activationTreePreparingComplete); err != nil {
		return ActivatedRestore{}, err
	}

	keyDigest := sha256.Sum256(nodeKey)
	keySHA := hex.EncodeToString(keyDigest[:])
	seal := restoreActivatedSeal{
		Schema: restoreActivatedSchema, ManifestSHA256: pending.ManifestSHA256, Manifest: pending.Manifest,
		NodeFingerprint: activationNodeFingerprint(nodeID, keySHA), NodeKeySHA256: keySHA,
		ActivatedAt: intent.ActivatedAt, SourceRetired: true,
	}
	seal.ActivationSHA256, err = activationSealDigest(seal)
	if err != nil {
		return ActivatedRestore{}, activationStateError("compute restore activation digest", err)
	}
	sealBytes, err := marshalActivatedSeal(seal)
	if err != nil {
		return ActivatedRestore{}, activationStateError("encode restore activation seal", err)
	}
	if err := writeSecureExclusive(root, restoreActivatedMarker, sealBytes); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return ActivatedRestore{}, activationStateError("persist restore activation seal", err)
		}
		existing, readErr := readActivatedSeal(root, restoreActivatedMarker)
		if readErr != nil || existing.ActivationSHA256 != seal.ActivationSHA256 {
			return ActivatedRestore{}, activationStateError("existing restore activation seal differs from the resumed activation", errors.Join(err, readErr))
		}
		seal = existing
	}
	if err := callActivationHook(hooks.afterActivatedSeal); err != nil {
		return ActivatedRestore{}, err
	}
	if err := removeSecureEntry(root, restorePendingMarker); err != nil {
		return ActivatedRestore{}, activationStateError("remove pending restore seal", err)
	}
	if err := callActivationHook(hooks.afterPendingRemoval); err != nil {
		return ActivatedRestore{}, err
	}
	if err := removeSecureEntry(root, restoreActivationIntentMarker); err != nil {
		return ActivatedRestore{}, activationStateError("remove restore activation intent", err)
	}
	if err := verifyActivationTree(ctx, root, seal.Manifest, activationTreeActivated); err != nil {
		return ActivatedRestore{}, err
	}
	return activatedRestore(dataDir, seal), nil
}

func reconcileActivated(ctx context.Context, root *secureDirectory, dataDir string, hooks activationHooks) (ActivatedRestore, error) {
	seal, err := readActivatedSeal(root, restoreActivatedMarker)
	if err != nil {
		return ActivatedRestore{}, err
	}
	if err := verifyActivatedIdentity(root, seal); err != nil {
		return ActivatedRestore{}, err
	}
	if err := verifyRestoredCut(ctx, root, seal.ManifestSHA256, seal.Manifest); err != nil {
		return ActivatedRestore{}, err
	}
	pendingPresent, err := secureEntryPresent(root, restorePendingMarker)
	if err != nil {
		return ActivatedRestore{}, activationStateError("inspect pending restore during activation reconciliation", err)
	}
	intentPresent, err := secureEntryPresent(root, restoreActivationIntentMarker)
	if err != nil {
		return ActivatedRestore{}, activationStateError("inspect restore intent during activation reconciliation", err)
	}
	if pendingPresent {
		pending, err := readPendingSeal(root)
		if err != nil {
			return ActivatedRestore{}, err
		}
		if pending.ManifestSHA256 != seal.ManifestSHA256 || pending.Manifest.BackupID != seal.Manifest.BackupID {
			return ActivatedRestore{}, activationStateError("activated and pending restore seals do not describe the same cut", nil)
		}
	}
	if intentPresent {
		intent, err := readActivationIntent(root)
		if err != nil {
			return ActivatedRestore{}, err
		}
		if intent.ManifestSHA256 != seal.ManifestSHA256 || intent.BackupID != seal.Manifest.BackupID || intent.ActivatedAt != seal.ActivatedAt {
			return ActivatedRestore{}, activationStateError("activated restore seal does not match its activation intent", nil)
		}
	}
	if err := verifyActivationTree(ctx, root, seal.Manifest, activationTreeReconciling); err != nil {
		return ActivatedRestore{}, err
	}
	if pendingPresent {
		if err := removeSecureEntry(root, restorePendingMarker); err != nil {
			return ActivatedRestore{}, activationStateError("remove reconciled pending restore seal", err)
		}
		if err := callActivationHook(hooks.afterPendingRemoval); err != nil {
			return ActivatedRestore{}, err
		}
	}
	if intentPresent {
		if err := removeSecureEntry(root, restoreActivationIntentMarker); err != nil {
			return ActivatedRestore{}, activationStateError("remove reconciled restore activation intent", err)
		}
	}
	if err := verifyActivationTree(ctx, root, seal.Manifest, activationTreeActivated); err != nil {
		return ActivatedRestore{}, err
	}
	return activatedRestore(dataDir, seal), nil
}

// CheckActivationState performs the cheap marker gate used after the daemon has
// acquired its data-directory lock. It does not create, rename, or remove any
// path. Pending and interrupted activation states are deliberately inert.
func CheckActivationState(dataDir string) (ActivationState, error) {
	dataDir, err := exactSelectedRecoveryPath(dataDir)
	if err != nil {
		return ActivationState{}, activationStateError("activation-state path must be canonical and absolute", err)
	}
	root, stat, err := openAbsoluteDirectoryNoFollow(dataDir)
	if err != nil {
		return ActivationState{}, activationStateError("open activation-state directory without following links", err)
	}
	defer root.Close()
	if stat.Uid != uint32(os.Geteuid()) {
		return ActivationState{}, activationStateError("activation-state directory is not owner-controlled", nil)
	}
	pending, err := secureEntryPresent(root, restorePendingMarker)
	if err != nil {
		return ActivationState{}, activationStateError("inspect pending restore marker", err)
	}
	intent, err := secureEntryPresent(root, restoreActivationIntentMarker)
	if err != nil {
		return ActivationState{}, activationStateError("inspect activation intent marker", err)
	}
	activated, err := secureEntryPresent(root, restoreActivatedMarker)
	if err != nil {
		return ActivationState{}, activationStateError("inspect activated restore marker", err)
	}
	consumed, err := secureEntryPresent(root, restoreConsumedMarker)
	if err != nil {
		return ActivationState{}, activationStateError("inspect consumed restore marker", err)
	}
	if consumed && (pending || intent) {
		return ActivationState{}, activationStateError("consumed restore marker conflicts with another activation marker", nil)
	}
	if consumed {
		consumedSeal, err := readConsumedSeal(root)
		if err != nil {
			return ActivationState{}, err
		}
		if activated {
			activatedSeal, err := readActivatedSeal(root, restoreActivatedMarker)
			if err != nil {
				return ActivationState{}, err
			}
			if consumedSeal.BackupID != activatedSeal.Manifest.BackupID || consumedSeal.ActivationSHA256 != activatedSeal.ActivationSHA256 {
				return ActivationState{}, activationStateError("activated and consumed restore markers describe different activations", nil)
			}
			// A crash after the compact consumed seal was synced but before the
			// large activated seal was unlinked is safely reverified once and
			// reconciled by ConsumeActivated.
			return ActivationState{Status: ActivationStateActivated, BackupID: consumedSeal.BackupID, ActivationSHA256: consumedSeal.ActivationSHA256}, nil
		}
		return ActivationState{Status: ActivationStateConsumed, BackupID: consumedSeal.BackupID, ActivationSHA256: consumedSeal.ActivationSHA256}, nil
	}
	if pending || intent {
		state := ActivationState{Status: ActivationStatePending}
		manifestSHA := ""
		if pending {
			seal, readErr := readPendingSeal(root)
			if readErr != nil {
				return ActivationState{}, readErr
			}
			state.BackupID = seal.Manifest.BackupID
			manifestSHA = seal.ManifestSHA256
		}
		if intent {
			activationIntent, readErr := readActivationIntent(root)
			if readErr != nil {
				return ActivationState{}, readErr
			}
			if state.BackupID != "" && (state.BackupID != activationIntent.BackupID || manifestSHA != activationIntent.ManifestSHA256) {
				return ActivationState{}, activationStateError("pending restore marker and activation intent describe different backups", nil)
			}
			state.BackupID = activationIntent.BackupID
			manifestSHA = activationIntent.ManifestSHA256
		}
		if activated {
			seal, readErr := readActivatedSeal(root, restoreActivatedMarker)
			if readErr != nil {
				return ActivationState{}, readErr
			}
			if state.BackupID != "" && (state.BackupID != seal.Manifest.BackupID || manifestSHA != seal.ManifestSHA256) {
				return ActivationState{}, activationStateError("pending and activated markers describe different backups", nil)
			}
			state.BackupID = seal.Manifest.BackupID
			state.ActivationSHA256 = seal.ActivationSHA256
		}
		return state, nil
	}
	if activated {
		seal, err := readActivatedSeal(root, restoreActivatedMarker)
		if err != nil {
			return ActivationState{}, err
		}
		return ActivationState{Status: ActivationStateActivated, BackupID: seal.Manifest.BackupID, ActivationSHA256: seal.ActivationSHA256}, nil
	}
	return ActivationState{Status: ActivationStateNormal}, nil
}

// VerifyActivated performs the mandatory first-start full verification. The
// caller must already own the daemon data lock and must call ConsumeActivated
// before any database mutation, worker, runtime driver, provider, or listener.
func VerifyActivated(ctx context.Context, dataDir string) (ActivatedRestore, error) {
	dataDir, err := exactSelectedRecoveryPath(dataDir)
	if err != nil {
		return ActivatedRestore{}, activationStateError("activated restore path must be canonical and absolute", err)
	}
	root, err := openExactPrivateDirectory(dataDir)
	if err != nil {
		return ActivatedRestore{}, activationStateError("activated restore directory is unsafe", err)
	}
	defer root.Close()
	state, err := CheckActivationState(dataDir)
	if err != nil {
		return ActivatedRestore{}, err
	}
	if state.Status != ActivationStateActivated {
		return ActivatedRestore{}, &Error{Code: CodeRestoreNotActivated, Message: "restore has not reached an unconsumed activated state"}
	}
	seal, err := readActivatedSeal(root, restoreActivatedMarker)
	if err != nil {
		return ActivatedRestore{}, err
	}
	if err := verifyActivatedIdentity(root, seal); err != nil {
		return ActivatedRestore{}, err
	}
	if err := verifyRestoredCut(ctx, root, seal.ManifestSHA256, seal.Manifest); err != nil {
		return ActivatedRestore{}, err
	}
	if err := verifyActivationTree(ctx, root, seal.Manifest, activationTreeActivated); err != nil {
		return ActivatedRestore{}, err
	}
	return activatedRestore(dataDir, seal), nil
}

// ConsumeActivated crash-safely marks a successfully verified first startup as
// consumed. The caller must hold the daemon lock and supply the digest returned
// by VerifyActivated, preventing consumption of a replaced seal.
func ConsumeActivated(dataDir, activationSHA256 string) error {
	return consumeActivatedWithHooks(dataDir, activationSHA256, consumptionHooks{})
}

func consumeActivatedWithHooks(dataDir, activationSHA256 string, hooks consumptionHooks) error {
	dataDir, err := exactSelectedRecoveryPath(dataDir)
	if err != nil || !sha256Pattern.MatchString(activationSHA256) {
		return activationStateError("consume activation request is invalid", err)
	}
	root, err := openExactPrivateDirectory(dataDir)
	if err != nil {
		return activationStateError("open activated restore for consumption", err)
	}
	defer root.Close()
	consumedPresent, err := secureEntryPresent(root, restoreConsumedMarker)
	if err != nil {
		return activationStateError("inspect consumed activation marker", err)
	}
	seal, err := readActivatedSeal(root, restoreActivatedMarker)
	if err != nil {
		return err
	}
	if seal.ActivationSHA256 != activationSHA256 {
		return activationStateError("activation seal changed after first-start verification", nil)
	}
	if consumedPresent {
		consumed, err := readConsumedSeal(root)
		if err != nil {
			return err
		}
		if consumed.BackupID != seal.Manifest.BackupID || consumed.ActivationSHA256 != seal.ActivationSHA256 {
			return activationStateError("consumed restore marker differs from the activated restore being reconciled", nil)
		}
	} else {
		consumedBytes, err := marshalConsumedSeal(restoreConsumedSeal{
			Schema: restoreConsumedSchema, BackupID: seal.Manifest.BackupID, ActivationSHA256: seal.ActivationSHA256,
		})
		if err != nil {
			return activationStateError("encode compact consumed restore seal", err)
		}
		if err := writeSecureExclusive(root, restoreConsumedMarker, consumedBytes); err != nil {
			return activationStateError("persist compact consumed restore seal", err)
		}
	}
	if hooks.afterConsumedSeal != nil {
		if err := hooks.afterConsumedSeal(); err != nil {
			return err
		}
	}
	if err := removeSecureEntry(root, restoreActivatedMarker); err != nil {
		return activationStateError("remove consumed full activation seal", err)
	}
	return nil
}

func verifyRestoredCut(ctx context.Context, root *secureDirectory, manifestSHA256 string, manifest Manifest) error {
	manifestBytes, err := MarshalManifest(manifest)
	if err != nil {
		return activationStateError("restore seal contains an invalid manifest", err)
	}
	digest := sha256.Sum256(manifestBytes)
	if hex.EncodeToString(digest[:]) != manifestSHA256 {
		return activationStateError("restore seal manifest digest is invalid", nil)
	}
	database, metadata, err := root.openRelativeFile("crewfold.db", unix.O_RDONLY, true)
	if err != nil {
		return &Error{Code: CodeBackupIntegrityFailed, Message: "open restored database without following links", Cause: err}
	}
	defer database.Close()
	if err := validatePrivateRegular(metadata, maximumDatabaseSize); err != nil {
		return &Error{Code: CodeBackupIntegrityFailed, Message: "restored database filesystem contract is unsafe", Cause: err}
	}
	size, databaseSHA, err := hashOpenFile(ctx, database, maximumDatabaseSize)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return recoveryContextError("hash restored database content", ctxErr)
		}
		return &Error{Code: CodeBackupIntegrityFailed, Message: "hash restored database content", Cause: err}
	}
	payloadMatches := size == manifest.Database.Size && databaseSHA == manifest.Database.SHA256
	if !payloadMatches {
		return &Error{Code: CodeBackupIntegrityFailed, Message: "restored database content differs from its manifest"}
	}
	if _, err := database.Seek(0, io.SeekStart); err != nil {
		return &Error{Code: CodeBackupIntegrityFailed, Message: "rewind restored database for verification", Cause: err}
	}
	report, err := store.VerifyDatabaseSnapshotFile(ctx, database, store.CanonicalVerifyOptions{Full: true})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return recoveryContextError("verify restored database canonical integrity", ctxErr)
		}
		return &Error{Code: CodeCanonicalIntegrityFailed, Message: "verify restored database canonical integrity", Cause: err}
	}
	if !report.Quiescence.Quiescent {
		return &Error{
			Code: CodeRestoreUnsafeNonterminal, Message: "restored database contains actionable nonterminal work",
			Quiescence: &BackupNotQuiescentDetails{Counts: report.Quiescence.Counts, Samples: append([]store.QuiescenceBlocker(nil), report.QuiescenceBlockers...)},
		}
	}
	if !report.Complete || report.Status != "ok" {
		code := CodeCanonicalIntegrityFailed
		for _, failure := range report.Failures {
			if failure.Check == "current_baseline" {
				code = CodeCurrentBaselineMismatch
				break
			}
		}
		return &Error{Code: code, Message: "restored database failed full canonical integrity"}
	}
	if report.Baseline.SourceSHA256 != manifest.BaselineSHA256 || report.Baseline.CatalogSHA256 != manifest.SQLiteSchemaSHA256 {
		return &Error{Code: CodeCurrentBaselineMismatch, Message: "restored database baseline differs from its restore seal"}
	}
	if report.LogicalSHA256 != manifest.LogicalSHA256 || report.EventHighWater != manifest.EventHighWater || report.Quiescence != manifest.Quiescence {
		return &Error{Code: CodeBackupIntegrityFailed, Message: "restored database logical state, event cursor, or quiescence differs from its restore seal"}
	}
	if !equalArtifactEntries(ArtifactEntries(report.ArtifactReferences), manifest.Entries) {
		return &Error{Code: CodeBackupIntegrityFailed, Message: "restored database artifact closure differs from its restore seal"}
	}
	artifacts, err := VerifyLiveArtifacts(ctx, root.path, report.ArtifactReferences)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return recoveryContextError("verify restored immutable artifacts", ctxErr)
		}
		return &Error{Code: CodeBackupIntegrityFailed, Message: "verify restored immutable artifacts", Cause: err}
	}
	if !artifacts.Complete || artifacts.IssueCount != 0 || artifacts.WarningCount != 0 {
		return &Error{Code: CodeBackupIntegrityFailed, Message: "restored immutable artifact tree is incomplete, corrupt, unsafe, or contains undeclared entries"}
	}
	return nil
}

type activationTreePhase int

const (
	activationTreePending activationTreePhase = iota
	activationTreePreparing
	activationTreePreparingComplete
	activationTreeReconciling
	activationTreeActivated
)

func verifyActivationTree(ctx context.Context, root *secureDirectory, manifest Manifest, phase activationTreePhase) error {
	requiredFiles := map[string]bool{"crewfold.db": true, "daemon.lock": true}
	allowedFiles := map[string]bool{"crewfold.db": true, "daemon.lock": true}
	requiredDirectories := map[string]bool{".": true}
	allowedDirectories := map[string]bool{".": true}
	for _, entry := range manifest.Entries {
		requiredFiles[entry.Path] = true
		allowedFiles[entry.Path] = true
		for directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(entry.Path))); directory != "."; directory = filepath.ToSlash(filepath.Dir(filepath.FromSlash(directory))) {
			requiredDirectories[directory] = true
			allowedDirectories[directory] = true
		}
	}
	switch phase {
	case activationTreePending:
		requiredFiles[restorePendingMarker] = true
		allowedFiles[restorePendingMarker] = true
	case activationTreePreparing:
		requiredFiles[restorePendingMarker] = true
		requiredFiles[restoreActivationIntentMarker] = true
		allowedFiles[restorePendingMarker] = true
		allowedFiles[restoreActivationIntentMarker] = true
		for _, path := range []string{"node.id", "node.key"} {
			allowedFiles[path] = true
		}
		for _, path := range []string{"capabilities", "runtime", "check-runtime"} {
			allowedDirectories[path] = true
		}
	case activationTreePreparingComplete:
		requiredFiles[restorePendingMarker] = true
		requiredFiles[restoreActivationIntentMarker] = true
		allowedFiles[restorePendingMarker] = true
		allowedFiles[restoreActivationIntentMarker] = true
		for _, path := range []string{"node.id", "node.key"} {
			requiredFiles[path] = true
			allowedFiles[path] = true
		}
		for _, path := range []string{"capabilities", "runtime", "check-runtime"} {
			requiredDirectories[path] = true
			allowedDirectories[path] = true
		}
	case activationTreeReconciling:
		requiredFiles[restoreActivatedMarker] = true
		allowedFiles[restoreActivatedMarker] = true
		for _, path := range []string{restorePendingMarker, restoreActivationIntentMarker} {
			allowedFiles[path] = true
		}
		for _, path := range []string{"node.id", "node.key"} {
			requiredFiles[path] = true
			allowedFiles[path] = true
		}
		for _, path := range []string{"capabilities", "runtime", "check-runtime"} {
			requiredDirectories[path] = true
			allowedDirectories[path] = true
		}
	case activationTreeActivated:
		requiredFiles[restoreActivatedMarker] = true
		allowedFiles[restoreActivatedMarker] = true
		// The compact marker can coexist only across the crash-safe consumed
		// transition; CheckActivationState correlates its digest before this
		// verification path is reached.
		allowedFiles[restoreConsumedMarker] = true
		for _, path := range []string{"node.id", "node.key"} {
			requiredFiles[path] = true
			allowedFiles[path] = true
		}
		for _, path := range []string{"capabilities", "runtime", "check-runtime"} {
			requiredDirectories[path] = true
			allowedDirectories[path] = true
		}
	}
	tree, err := walkSecureTree(ctx, root)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return recoveryContextError("verify restored filesystem tree", ctxErr)
		}
		return &Error{Code: CodeBackupIntegrityFailed, Message: "restored filesystem tree is unsafe", Cause: err}
	}
	for path := range requiredFiles {
		if _, exists := tree.files[path]; !exists {
			return &Error{Code: CodeBackupIntegrityFailed, Message: "restored filesystem is missing required file " + path}
		}
	}
	for path := range requiredDirectories {
		if _, exists := tree.directories[path]; !exists {
			return &Error{Code: CodeBackupIntegrityFailed, Message: "restored filesystem is missing required directory " + path}
		}
	}
	for path := range tree.files {
		if !allowedFiles[path] {
			return &Error{Code: CodeBackupIntegrityFailed, Message: "restored filesystem contains undeclared file " + path}
		}
	}
	for path := range tree.directories {
		if !allowedDirectories[path] {
			return &Error{Code: CodeBackupIntegrityFailed, Message: "restored filesystem contains undeclared directory " + path}
		}
	}
	return nil
}

func readPendingSeal(root *secureDirectory) (restorePendingSeal, error) {
	data, err := readSecureRegular(root, restorePendingMarker, maximumActivationSealSize)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return restorePendingSeal{}, &Error{Code: CodeRestoreNotActivated, Message: "data directory is not a pending restore"}
	}
	if err != nil {
		return restorePendingSeal{}, activationStateError("read pending restore seal", err)
	}
	var seal restorePendingSeal
	if err := decodeExactJSON(data, &seal); err != nil {
		return restorePendingSeal{}, activationStateError("decode pending restore seal", err)
	}
	canonical, err := marshalRestorePendingSeal(seal)
	if err != nil || !bytes.Equal(data, canonical) {
		return restorePendingSeal{}, activationStateError("pending restore seal is not exact canonical current state", err)
	}
	return seal, nil
}

func marshalRestorePendingSeal(seal restorePendingSeal) ([]byte, error) {
	if seal.Schema != restorePendingSchema || !sha256Pattern.MatchString(seal.ManifestSHA256) {
		return nil, errors.New("pending restore seal fields are invalid")
	}
	manifestBytes, err := MarshalManifest(seal.Manifest)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(manifestBytes)
	if hex.EncodeToString(digest[:]) != seal.ManifestSHA256 {
		return nil, errors.New("pending restore seal manifest digest is invalid")
	}
	data, err := json.Marshal(seal)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > maximumActivationSealSize {
		return nil, errors.New("pending restore seal exceeds its bound")
	}
	return data, nil
}

func readActivationIntent(root *secureDirectory) (restoreActivationIntent, error) {
	data, err := readSecureRegular(root, restoreActivationIntentMarker, 16<<10)
	if err != nil {
		return restoreActivationIntent{}, activationStateError("read restore activation intent", err)
	}
	var intent restoreActivationIntent
	if err := decodeExactJSON(data, &intent); err != nil {
		return restoreActivationIntent{}, activationStateError("decode restore activation intent", err)
	}
	canonical, err := marshalActivationIntent(intent)
	if err != nil || !bytes.Equal(data, canonical) {
		return restoreActivationIntent{}, activationStateError("restore activation intent is not exact canonical current state", err)
	}
	return intent, nil
}

func marshalActivationIntent(intent restoreActivationIntent) ([]byte, error) {
	if intent.Schema != restoreActivationIntentSchema || !backupIDPattern.MatchString(intent.BackupID) ||
		!sha256Pattern.MatchString(intent.ManifestSHA256) || !canonicalTimestamp(intent.ActivatedAt) || !intent.SourceRetired {
		return nil, errors.New("restore activation intent fields are invalid")
	}
	data, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func readActivatedSeal(root *secureDirectory, marker string) (restoreActivatedSeal, error) {
	data, err := readSecureRegular(root, marker, maximumActivationSealSize)
	if err != nil {
		return restoreActivatedSeal{}, activationStateError("read restore activation seal", err)
	}
	var seal restoreActivatedSeal
	if err := decodeExactJSON(data, &seal); err != nil {
		return restoreActivatedSeal{}, activationStateError("decode restore activation seal", err)
	}
	canonical, err := marshalActivatedSeal(seal)
	if err != nil || !bytes.Equal(data, canonical) {
		return restoreActivatedSeal{}, activationStateError("restore activation seal is not exact canonical current state", err)
	}
	return seal, nil
}

func readConsumedSeal(root *secureDirectory) (restoreConsumedSeal, error) {
	data, err := readSecureRegular(root, restoreConsumedMarker, 1024)
	if err != nil {
		return restoreConsumedSeal{}, activationStateError("read compact consumed restore seal", err)
	}
	var seal restoreConsumedSeal
	if err := decodeExactJSON(data, &seal); err != nil {
		return restoreConsumedSeal{}, activationStateError("decode compact consumed restore seal", err)
	}
	canonical, err := marshalConsumedSeal(seal)
	if err != nil || !bytes.Equal(data, canonical) {
		return restoreConsumedSeal{}, activationStateError("consumed restore seal is not exact canonical current state", err)
	}
	return seal, nil
}

func marshalConsumedSeal(seal restoreConsumedSeal) ([]byte, error) {
	if seal.Schema != restoreConsumedSchema || !backupIDPattern.MatchString(seal.BackupID) || !sha256Pattern.MatchString(seal.ActivationSHA256) {
		return nil, errors.New("consumed restore seal fields are invalid")
	}
	data, err := json.Marshal(seal)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > 1024 {
		return nil, errors.New("consumed restore seal exceeds its bound")
	}
	return data, nil
}

func marshalActivatedSeal(seal restoreActivatedSeal) ([]byte, error) {
	if seal.Schema != restoreActivatedSchema || !sha256Pattern.MatchString(seal.ManifestSHA256) ||
		!sha256Pattern.MatchString(seal.NodeFingerprint) || !sha256Pattern.MatchString(seal.NodeKeySHA256) ||
		!sha256Pattern.MatchString(seal.ActivationSHA256) || !canonicalTimestamp(seal.ActivatedAt) || !seal.SourceRetired {
		return nil, errors.New("restore activation seal fields are invalid")
	}
	manifestBytes, err := MarshalManifest(seal.Manifest)
	if err != nil {
		return nil, err
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	if hex.EncodeToString(manifestDigest[:]) != seal.ManifestSHA256 {
		return nil, errors.New("restore activation seal manifest digest is invalid")
	}
	digest, err := activationSealDigest(seal)
	if err != nil || digest != seal.ActivationSHA256 {
		return nil, errors.New("restore activation digest is invalid")
	}
	data, err := json.Marshal(seal)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > maximumActivationSealSize {
		return nil, errors.New("restore activation seal exceeds its bound")
	}
	return data, nil
}

func activationSealDigest(seal restoreActivatedSeal) (string, error) {
	payload := struct {
		Schema          string   `json:"schema"`
		ManifestSHA256  string   `json:"manifest_sha256"`
		Manifest        Manifest `json:"manifest"`
		NodeFingerprint string   `json:"node_fingerprint"`
		NodeKeySHA256   string   `json:"node_key_sha256"`
		ActivatedAt     string   `json:"activated_at"`
		SourceRetired   bool     `json:"source_retired"`
	}{
		Schema: seal.Schema, ManifestSHA256: seal.ManifestSHA256, Manifest: seal.Manifest,
		NodeFingerprint: seal.NodeFingerprint, NodeKeySHA256: seal.NodeKeySHA256,
		ActivatedAt: seal.ActivatedAt, SourceRetired: seal.SourceRetired,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func decodeExactJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON document has trailing content")
	}
	return nil
}

func ensureActivationNodeID(root *secureDirectory, dataDir string) (string, error) {
	present, err := secureEntryPresent(root, "node.id")
	if err != nil {
		return "", activationStateError("inspect activation node identity", err)
	}
	if !present {
		if _, err := execution.CreateNodeID(dataDir); err != nil {
			return "", activationStateError("create fresh restore node identity", err)
		}
	}
	return readActivationNodeID(root)
}

func ensureActivationNodeKey(root *secureDirectory, dataDir string) ([]byte, error) {
	present, err := secureEntryPresent(root, "node.key")
	if err != nil {
		return nil, activationStateError("inspect activation node key", err)
	}
	if !present {
		if _, err := execution.CreateNodeKey(dataDir); err != nil {
			return nil, activationStateError("create fresh restore node key", err)
		}
	}
	return readActivationNodeKey(root)
}

func readActivationNodeID(root *secureDirectory) (string, error) {
	data, err := readSecureRegular(root, "node.id", 33)
	if err != nil {
		return "", activationStateError("read restore node identity", err)
	}
	if len(data) != 33 || data[32] != '\n' || !activationNodeIDPattern.Match(data[:32]) {
		return "", activationStateError("restore node identity is not exact canonical current state", nil)
	}
	return string(data[:32]), nil
}

func readActivationNodeKey(root *secureDirectory) ([]byte, error) {
	data, err := readSecureRegular(root, "node.key", 32)
	if err != nil {
		return nil, activationStateError("read restore node key", err)
	}
	if len(data) != 32 {
		return nil, activationStateError("restore node key is not exactly 32 bytes", nil)
	}
	return data, nil
}

func verifyActivatedIdentity(root *secureDirectory, seal restoreActivatedSeal) error {
	nodeID, err := readActivationNodeID(root)
	if err != nil {
		return err
	}
	nodeKey, err := readActivationNodeKey(root)
	if err != nil {
		return err
	}
	keyDigest := sha256.Sum256(nodeKey)
	keySHA := hex.EncodeToString(keyDigest[:])
	if keySHA != seal.NodeKeySHA256 || activationNodeFingerprint(nodeID, keySHA) != seal.NodeFingerprint {
		return activationStateError("restore node identity or key differs from its activation seal", nil)
	}
	for _, name := range []string{"capabilities", "runtime", "check-runtime"} {
		if err := requireEmptyOperationalRoot(root, name); err != nil {
			return err
		}
	}
	return nil
}

func activationNodeFingerprint(nodeID, keySHA string) string {
	digest := sha256.Sum256([]byte("crewfold.restore.node-fingerprint.v1\n" + nodeID + "\n" + keySHA + "\n"))
	return hex.EncodeToString(digest[:])
}

func ensureEmptyOperationalRoot(root *secureDirectory, name string) error {
	present, err := secureEntryPresent(root, name)
	if err != nil {
		return activationStateError("inspect restore operational root "+name, err)
	}
	if !present {
		if err := root.mkdirAll(name); err != nil {
			return activationStateError("create empty restore operational root "+name, err)
		}
	}
	return requireEmptyOperationalRoot(root, name)
}

func requireEmptyOperationalRoot(root *secureDirectory, name string) error {
	directory, _, err := root.openRelativeDirectory(name)
	if err != nil {
		return activationStateError("open restore operational root "+name, err)
	}
	defer directory.Close()
	tree, err := walkSecureTree(context.Background(), directory)
	if err != nil || len(tree.files) != 0 || len(tree.directories) != 1 {
		return activationStateError("restore operational root "+name+" is not empty and private", err)
	}
	return nil
}

func activatedRestore(dataDir string, seal restoreActivatedSeal) ActivatedRestore {
	return ActivatedRestore{
		Path: dataDir, BackupID: seal.Manifest.BackupID, ManifestSHA256: seal.ManifestSHA256,
		EventHighWater: seal.Manifest.EventHighWater, LogicalSHA256: seal.Manifest.LogicalSHA256,
		NodeFingerprint: seal.NodeFingerprint, ActivationSHA256: seal.ActivationSHA256,
		ActivatedAt: seal.ActivatedAt, SourceRetired: seal.SourceRetired,
	}
}

func acquireExistingDataLock(root *secureDirectory, busyCode string) (*os.File, error) {
	lock, metadata, err := root.openRelativeFile("daemon.lock", unix.O_RDWR, true)
	if err != nil {
		return nil, &Error{Code: busyCode, Message: "open existing daemon data lock without following links", Cause: err}
	}
	if err := validatePrivateRegular(metadata, 1<<20); err != nil {
		_ = lock.Close()
		return nil, &Error{Code: busyCode, Message: "daemon data lock is unsafe", Cause: err}
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, &Error{Code: busyCode, Message: "data directory is owned by a live daemon", Cause: err}
	}
	return lock, nil
}

func releaseDataLock(lock *os.File) {
	if lock == nil {
		return
	}
	_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	_ = lock.Close()
}

func secureEntryPresent(root *secureDirectory, name string) (bool, error) {
	if !validRelativePath(name) || filepath.ToSlash(filepath.Base(filepath.FromSlash(name))) != name {
		return false, errors.New("secure entry name is invalid")
	}
	var stat unix.Stat_t
	err := unix.Fstatat(int(root.file.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	return err == nil, err
}

func removeSecureEntry(root *secureDirectory, name string) error {
	if err := unix.Unlinkat(int(root.file.Fd()), name, 0); err != nil {
		return err
	}
	return root.Sync()
}

func callActivationHook(hook func() error) error {
	if hook == nil {
		return nil
	}
	return hook()
}

func activationStateError(message string, cause error) *Error {
	return &Error{Code: CodeRestoreNotActivated, Message: message, Cause: cause}
}
