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
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/store"
	"golang.org/x/sys/unix"
)

const (
	bundleDirectoryMode       = 0o700
	bundleFileMode            = 0o600
	maximumManifestSize       = 32 << 20
	maximumArtifactEntries    = 200_000
	maximumManifestPathBytes  = 4096
	maximumDatabaseSize       = 8 << 30
	maximumBundlePayloadBytes = 16 << 30
	maximumArtifactSize       = 1 << 20
)

var (
	backupIDPattern = regexp.MustCompile(`^backup_[0-9a-f]{32}$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func NewManifest(backupID, createdAt string, snapshot store.SnapshotMetadata, integrity store.CanonicalIntegrityReport, entries []ArtifactEntry) (Manifest, error) {
	if !integrity.Complete || integrity.Status != "ok" || !integrity.Quiescence.Quiescent ||
		snapshot.Baseline != integrity.Baseline || snapshot.EventHighWater != integrity.EventHighWater ||
		!equalArtifactEntries(ArtifactEntries(integrity.ArtifactReferences), entries) {
		return Manifest{}, &Error{Code: CodeCanonicalIntegrityFailed, Message: "snapshot, canonical integrity, quiescence, and artifact closure must be one exact cut"}
	}
	manifest := Manifest{
		Schema: ManifestSchema, BackupID: backupID, CreatedAt: createdAt,
		BaselineSHA256: snapshot.Baseline.SourceSHA256, SQLiteSchemaSHA256: snapshot.Baseline.CatalogSHA256,
		LogicalSHA256: integrity.LogicalSHA256, EventHighWater: integrity.EventHighWater,
		Quiescence: integrity.Quiescence,
		Database:   DatabaseEntry{Path: "crewfold.db", Size: snapshot.ByteSize, SHA256: snapshot.SHA256},
		Entries:    append([]ArtifactEntry(nil), entries...),
	}
	sort.Slice(manifest.Entries, func(left, right int) bool { return manifest.Entries[left].Path < manifest.Entries[right].Path })
	if len(manifest.Entries) > maximumArtifactEntries {
		return Manifest{}, resourceError("backup manifest exceeds its artifact entry bound", nil)
	}
	manifest.EntryCount = len(manifest.Entries) + 1
	totalBytes, err := boundedPayloadTotal(manifest.Database.Size, manifest.Entries)
	if err != nil {
		return Manifest{}, err
	}
	manifest.TotalBytes = totalBytes
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ArtifactEntries(references []store.ImmutableArtifactReference) []ArtifactEntry {
	entries := make([]ArtifactEntry, len(references))
	for index, reference := range references {
		shard := ""
		if len(reference.ContentSHA256) >= 2 {
			shard = reference.ContentSHA256[:2]
		}
		root := "check-artifacts"
		if reference.Kind == "run_log_artifact" {
			root = "run-artifacts"
		} else if reference.Kind == "service_log_artifact" {
			root = "service-artifacts"
		}
		entries[index] = ArtifactEntry{
			Path:   root + "/" + shard + "/" + reference.ContentSHA256,
			Kind:   reference.Kind,
			Mode:   bundleFileMode,
			Size:   reference.ByteSize,
			SHA256: reference.ContentSHA256,
		}
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	return entries
}

func MarshalManifest(manifest Manifest) ([]byte, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, contractError("encode backup manifest", err)
	}
	data = append(data, '\n')
	if len(data) > maximumManifestSize {
		return nil, resourceError("backup manifest exceeds 32 MiB", nil)
	}
	return data, nil
}

func WriteManifest(root string, manifest Manifest) (string, error) {
	directory, err := openExactPrivateDirectory(root)
	if err != nil {
		return "", &Error{Code: CodeBackupTargetInvalid, Message: "backup root must be an exact owner-controlled 0700 directory with no symlink ancestors", Cause: err}
	}
	defer directory.Close()
	data, err := MarshalManifest(manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	digestText := hex.EncodeToString(digest[:])
	if err := writeSecureExclusive(directory, "manifest.json", data); err != nil {
		return "", &Error{Code: CodeBackupTargetInvalid, Message: "write backup manifest", Cause: err}
	}
	if err := writeSecureExclusive(directory, "manifest.sha256", []byte(digestText+"\n")); err != nil {
		_ = unix.Unlinkat(int(directory.file.Fd()), "manifest.json", 0)
		return "", &Error{Code: CodeBackupTargetInvalid, Message: "write backup manifest digest", Cause: err}
	}
	if err := directory.Sync(); err != nil {
		return "", &Error{Code: CodeBackupTargetInvalid, Message: "sync backup manifest directory", Cause: err}
	}
	return digestText, nil
}

func VerifyBundle(ctx context.Context, root string) (VerifiedBundle, error) {
	root, err := exactSelectedRecoveryPath(root)
	if err != nil {
		return VerifiedBundle{}, markVerificationPhase(integrityError("backup root path must be canonical, absolute, and outside reserved staging", err), VerificationPhaseManifest)
	}
	return verifyBundleAtPath(ctx, root)
}

// verifyBundleAtPath is also used for Crewfold's own unpublished stage after a
// durable parent intent has established its ownership. Public callers always go
// through VerifyBundle and exactSelectedRecoveryPath.
func verifyBundleAtPath(ctx context.Context, root string) (VerifiedBundle, error) {
	verified, directory, err := openVerifiedBundle(ctx, root)
	if directory != nil {
		_ = directory.Close()
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return VerifiedBundle{}, recoveryContextError("verify backup bundle", ctxErr)
		}
	}
	return verified, err
}

func openVerifiedBundle(ctx context.Context, root string) (VerifiedBundle, *secureDirectory, error) {
	root, err := exactAbsolutePath(root)
	if err != nil {
		return VerifiedBundle{}, nil, markVerificationPhase(integrityError("backup root path must be canonical and absolute", err), VerificationPhaseManifest)
	}
	directory, err := openExactPrivateDirectory(root)
	if err != nil {
		return VerifiedBundle{}, nil, markVerificationPhase(integrityError("backup root must be an exact owner-controlled 0700 directory with no symlink ancestors", err), VerificationPhaseManifest)
	}
	fail := func(verifyErr error) (VerifiedBundle, *secureDirectory, error) {
		_ = directory.Close()
		return VerifiedBundle{}, nil, verifyErr
	}
	manifestBytes, err := readSecureRegular(directory, "manifest.json", maximumManifestSize)
	if err != nil {
		return fail(markVerificationPhase(integrityError("read backup manifest", err), VerificationPhaseManifest))
	}
	manifest, err := decodeManifest(manifestBytes)
	if err != nil {
		return fail(markVerificationPhase(err, VerificationPhaseManifest))
	}
	canonical, err := MarshalManifest(manifest)
	if err != nil {
		return fail(markVerificationPhase(err, VerificationPhaseManifest))
	}
	if !bytes.Equal(manifestBytes, canonical) {
		return fail(markVerificationPhase(contractError("manifest.json is not the exact canonical encoding", nil), VerificationPhaseManifest))
	}
	digest := sha256.Sum256(manifestBytes)
	digestText := hex.EncodeToString(digest[:])
	digestBytes, err := readSecureRegular(directory, "manifest.sha256", 65)
	if err != nil || string(digestBytes) != digestText+"\n" {
		return fail(markVerificationPhase(integrityError("manifest SHA-256 does not match manifest.json", err), VerificationPhaseManifest))
	}
	databaseFile, err := verifyBundleTree(ctx, directory, manifest)
	if err != nil {
		return fail(markVerificationPhase(err, VerificationPhaseFileClosure))
	}
	defer databaseFile.Close()
	report, err := store.VerifyDatabaseSnapshotFile(ctx, databaseFile, store.CanonicalVerifyOptions{Full: true})
	if err != nil {
		return fail(markVerificationPhase(integrityError("verify backup database snapshot", err), VerificationPhaseCanonicalIntegrity))
	}
	if !report.Complete || report.Status != "ok" {
		code := CodeCanonicalIntegrityFailed
		for _, failure := range report.Failures {
			if failure.Check == "current_baseline" {
				code = CodeCurrentBaselineMismatch
				break
			}
		}
		return fail(markVerificationPhase(&Error{Code: code, Message: "backup database failed full canonical integrity"}, VerificationPhaseCanonicalIntegrity))
	}
	if report.Baseline.SourceSHA256 != manifest.BaselineSHA256 ||
		report.Baseline.CatalogSHA256 != manifest.SQLiteSchemaSHA256 {
		return fail(markVerificationPhase(&Error{Code: CodeCurrentBaselineMismatch, Message: "backup manifest baseline identity differs from its database"}, VerificationPhaseCanonicalIntegrity))
	}
	if report.LogicalSHA256 != manifest.LogicalSHA256 || report.EventHighWater != manifest.EventHighWater {
		return fail(markVerificationPhase(integrityError("backup logical state or event high-water differs from its database", nil), VerificationPhaseCanonicalIntegrity))
	}
	if report.Quiescence != manifest.Quiescence || !report.Quiescence.Quiescent {
		return fail(markVerificationPhase(&Error{Code: CodeRestoreUnsafeNonterminal, Message: "backup quiescence proof differs from its database or is nonterminal"}, VerificationPhaseQuiescence))
	}
	expectedEntries := ArtifactEntries(report.ArtifactReferences)
	if !equalArtifactEntries(expectedEntries, manifest.Entries) {
		return fail(markVerificationPhase(integrityError("backup artifact closure differs from database references", nil), VerificationPhaseFileClosure))
	}
	return VerifiedBundle{Root: root, Manifest: manifest, ManifestSHA256: digestText, Integrity: report}, directory, nil
}

func decodeManifest(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, contractError("decode backup manifest", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Manifest{}, contractError("backup manifest has trailing JSON", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Database.Size > maximumDatabaseSize || len(manifest.Entries) > maximumArtifactEntries ||
		manifest.EntryCount > maximumArtifactEntries+1 || manifest.TotalBytes > maximumBundlePayloadBytes {
		return resourceError("backup manifest exceeds its frozen entry or payload bound", nil)
	}
	if manifest.Schema != ManifestSchema || !backupIDPattern.MatchString(manifest.BackupID) || !canonicalTimestamp(manifest.CreatedAt) ||
		!sha256Pattern.MatchString(manifest.BaselineSHA256) || !sha256Pattern.MatchString(manifest.SQLiteSchemaSHA256) ||
		!sha256Pattern.MatchString(manifest.LogicalSHA256) || manifest.EventHighWater < 0 ||
		manifest.Database.Path != "crewfold.db" || manifest.Database.Size <= 0 || !sha256Pattern.MatchString(manifest.Database.SHA256) ||
		manifest.EntryCount != len(manifest.Entries)+1 || manifest.TotalBytes < manifest.Database.Size ||
		!manifest.Quiescence.Quiescent || manifest.Quiescence.EventHighWater != manifest.EventHighWater ||
		!sha256Pattern.MatchString(manifest.Quiescence.ProofSHA256) || !quiescenceCountsZero(manifest.Quiescence.Counts) {
		return contractError("backup manifest identity, database, or quiescence fields are invalid", nil)
	}
	proof, err := quiescenceProof(manifest.Quiescence.EventHighWater, manifest.Quiescence.Counts)
	if err != nil || proof != manifest.Quiescence.ProofSHA256 {
		return contractError("backup manifest quiescence proof is invalid", err)
	}
	seen := make(map[string]bool, len(manifest.Entries))
	total := manifest.Database.Size
	prior := ""
	for _, entry := range manifest.Entries {
		if entry.Size > maximumArtifactSize {
			return resourceError("backup manifest artifact exceeds its frozen per-entry bound", nil)
		}
		if entry.Path <= prior || seen[entry.Path] || !validArtifactEntry(entry) {
			return contractError("backup manifest artifact entries are invalid or unsorted", nil)
		}
		seen[entry.Path] = true
		prior = entry.Path
		if entry.Size > maximumBundlePayloadBytes-total {
			return resourceError("backup manifest payload total overflows its frozen bound", nil)
		}
		total += entry.Size
	}
	if total != manifest.TotalBytes {
		return contractError("backup manifest total_bytes does not equal its payload sizes", nil)
	}
	return nil
}

func validArtifactEntry(entry ArtifactEntry) bool {
	if entry.Kind != "check_artifact" && entry.Kind != "run_log_artifact" && entry.Kind != "service_log_artifact" || entry.Mode != bundleFileMode || entry.Size < 0 || entry.Size > maximumArtifactSize || !sha256Pattern.MatchString(entry.SHA256) {
		return false
	}
	root := "check-artifacts"
	if entry.Kind == "run_log_artifact" {
		root = "run-artifacts"
	} else if entry.Kind == "service_log_artifact" {
		root = "service-artifacts"
	}
	want := root + "/" + entry.SHA256[:2] + "/" + entry.SHA256
	return entry.Path == want && validRelativePath(entry.Path)
}

func verifyBundleTree(ctx context.Context, root *secureDirectory, manifest Manifest) (*os.File, error) {
	expectedFiles := map[string]bool{"manifest.json": true, "manifest.sha256": true, manifest.Database.Path: true}
	expectedDirectories := map[string]bool{".": true}
	for _, entry := range manifest.Entries {
		expectedFiles[entry.Path] = true
	}
	for path := range expectedFiles {
		for directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(path))); directory != "."; directory = filepath.ToSlash(filepath.Dir(filepath.FromSlash(directory))) {
			expectedDirectories[directory] = true
		}
	}
	tree, err := walkSecureTree(ctx, root)
	if err != nil {
		return nil, integrityError("backup filesystem contract failed", err)
	}
	for path := range expectedFiles {
		if _, exists := tree.files[path]; !exists {
			return nil, integrityError("backup is missing required file "+path, nil)
		}
	}
	for path := range expectedDirectories {
		if _, exists := tree.directories[path]; !exists {
			return nil, integrityError("backup is missing required directory "+path, nil)
		}
	}
	if len(tree.files) != len(expectedFiles) || len(tree.directories) != len(expectedDirectories) {
		for path := range tree.files {
			if !expectedFiles[path] {
				return nil, integrityError("backup contains extra file "+path, nil)
			}
		}
		for path := range tree.directories {
			if !expectedDirectories[path] {
				return nil, integrityError("backup contains extra directory "+path, nil)
			}
		}
	}
	databaseFile, databaseMetadata, err := root.openRelativeFile(manifest.Database.Path, unix.O_RDONLY, true)
	if err != nil {
		return nil, integrityError("open backup database payload", err)
	}
	if err := validatePrivateRegular(databaseMetadata, maximumDatabaseSize); err != nil || databaseMetadata.size != manifest.Database.Size {
		_ = databaseFile.Close()
		return nil, integrityError("backup database payload differs from its manifest", err)
	}
	databaseSize, databaseSHA, err := hashOpenFile(ctx, databaseFile, maximumDatabaseSize)
	if err != nil || databaseSize != manifest.Database.Size || databaseSHA != manifest.Database.SHA256 {
		_ = databaseFile.Close()
		return nil, integrityError("backup database payload differs from its manifest", err)
	}
	for _, entry := range manifest.Entries {
		size, digest, err := hashSecureRegular(ctx, root, entry.Path, maximumArtifactSize)
		if err != nil || size != entry.Size || digest != entry.SHA256 {
			_ = databaseFile.Close()
			return nil, integrityError("backup artifact payload differs from its manifest: "+entry.Path, err)
		}
	}
	return databaseFile, nil
}

func validRelativePath(path string) bool {
	return path != "" && path != "." && len(path) <= maximumManifestPathBytes && utf8.ValidString(path) &&
		!strings.HasPrefix(path, "/") && filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) == path && !strings.Contains(path, `\`)
}

func boundedPayloadTotal(databaseSize int64, entries []ArtifactEntry) (int64, error) {
	if databaseSize <= 0 || databaseSize > maximumDatabaseSize || databaseSize > maximumBundlePayloadBytes {
		return 0, resourceError("backup database exceeds its frozen size bound", nil)
	}
	total := databaseSize
	for _, entry := range entries {
		if entry.Size < 0 || entry.Size > maximumArtifactSize || entry.Size > maximumBundlePayloadBytes-total {
			return 0, resourceError("backup artifact sizes exceed the frozen payload bound", nil)
		}
		total += entry.Size
	}
	return total, nil
}

func canonicalTimestamp(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC && parsed.Format(time.RFC3339Nano) == value
}

func quiescenceProof(eventHighWater int64, counts store.QuiescenceCounts) (string, error) {
	data, err := json.Marshal(struct {
		EventHighWater int64                  `json:"event_high_water"`
		Counts         store.QuiescenceCounts `json:"counts"`
	}{EventHighWater: eventHighWater, Counts: counts})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func quiescenceCountsZero(counts store.QuiescenceCounts) bool {
	return counts.NonterminalRuns == 0 && counts.UnsettledRunJobs == 0 && counts.RuntimeBindings == 0 &&
		counts.UnfinishedCheckRuns == 0 && counts.UnsettledCheckJobs == 0 && counts.OpenWakeJobs == 0 &&
		counts.OpenSchedulingIntents == 0 && counts.OpenSupervisorActions == 0 && counts.OpenApprovals == 0 &&
		counts.OpenOwnerManagerReviews == 0 && counts.OpenOwnerExecutiveExchanges == 0 &&
		counts.NonterminalManagedServices == 0 && counts.ManagedServiceBindings == 0 &&
		counts.UnsettledManagedServiceJobs == 0
}

func equalArtifactEntries(left, right []ArtifactEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func contractError(message string, cause error) *Error {
	return &Error{Code: CodeBackupContractMismatch, Message: message, Cause: cause}
}

func integrityError(message string, cause error) *Error {
	return &Error{Code: CodeBackupIntegrityFailed, Message: message, Cause: cause}
}

func markVerificationPhase(err error, phase string) error {
	if current, ok := err.(*Error); ok {
		copy := *current
		copy.VerificationPhase = phase
		return &copy
	}
	return &Error{Code: CodeBackupIntegrityFailed, Message: "backup verification failed", Cause: err, VerificationPhase: phase}
}

func resourceError(message string, cause error) *Error {
	return &Error{Code: CodeResourceLimitExceeded, Message: message, Cause: cause}
}
