package recovery

import "crewfold/internal/store"

const (
	ManifestSchema = "urn:crewfold:schema:backup-manifest:v1"

	VerificationPhaseManifest           = "manifest"
	VerificationPhaseFileClosure        = "file_closure"
	VerificationPhaseCanonicalIntegrity = "canonical_integrity"
	VerificationPhaseQuiescence         = "quiescence"

	CodeCurrentBaselineMismatch            = "current_baseline_mismatch"
	CodeCanonicalIntegrityFailed           = "canonical_integrity_failed"
	CodeDatabaseBusy                       = "database_busy"
	CodeIdempotencyConflict                = "idempotency_conflict"
	CodeBackupSourceUnhealthy              = "backup_source_unhealthy"
	CodeBackupNotQuiescent                 = "backup_not_quiescent"
	CodeBackupTargetInvalid                = "backup_target_invalid"
	CodeBackupTargetExists                 = "backup_target_exists"
	CodeBackupContractMismatch             = "backup_contract_mismatch"
	CodeBackupIntegrityFailed              = "backup_integrity_failed"
	CodeRestoreTargetExists                = "restore_target_exists"
	CodeRestoreNotActivated                = "restore_not_activated"
	CodeRestoreUnsafeNonterminal           = "restore_unsafe_nonterminal"
	CodeRestoreSourceRetirementUnconfirmed = "restore_source_retirement_unconfirmed"
	CodeRepairSourceInUse                  = "repair_source_in_use"
	CodeRepairTargetInvalid                = "repair_target_invalid"
	CodeResourceLimitExceeded              = "resource_limit_exceeded"
	CodeOperationCancelled                 = "operation_cancelled"
)

type Manifest struct {
	Schema             string             `json:"schema"`
	BackupID           string             `json:"backup_id"`
	CreatedAt          string             `json:"created_at"`
	BaselineSHA256     string             `json:"baseline_sha256"`
	SQLiteSchemaSHA256 string             `json:"sqlite_schema_sha256"`
	LogicalSHA256      string             `json:"logical_sha256"`
	EventHighWater     int64              `json:"event_high_water"`
	Quiescence         store.QuiescentCut `json:"quiescence"`
	Database           DatabaseEntry      `json:"database"`
	Entries            []ArtifactEntry    `json:"entries"`
	EntryCount         int                `json:"entry_count"`
	TotalBytes         int64              `json:"total_bytes"`
}

type DatabaseEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type ArtifactEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type VerifiedBundle struct {
	Root           string                         `json:"root"`
	Manifest       Manifest                       `json:"manifest"`
	ManifestSHA256 string                         `json:"manifest_sha256"`
	Integrity      store.CanonicalIntegrityReport `json:"integrity"`
}

type PendingRestore struct {
	Path           string `json:"path"`
	BackupID       string `json:"backup_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
	EventHighWater int64  `json:"event_high_water"`
	LogicalSHA256  string `json:"logical_sha256"`
}

const (
	ActivationStateNormal    = "normal"
	ActivationStatePending   = "pending"
	ActivationStateActivated = "activated"
	ActivationStateConsumed  = "consumed"
)

type ActivationState struct {
	Status           string `json:"status"`
	BackupID         string `json:"backup_id,omitempty"`
	ActivationSHA256 string `json:"activation_sha256,omitempty"`
}

type ActivatedRestore struct {
	Path             string `json:"path"`
	BackupID         string `json:"backup_id"`
	ManifestSHA256   string `json:"manifest_sha256"`
	EventHighWater   int64  `json:"event_high_water"`
	LogicalSHA256    string `json:"logical_sha256"`
	NodeFingerprint  string `json:"node_fingerprint"`
	ActivationSHA256 string `json:"activation_sha256"`
	ActivatedAt      string `json:"activated_at"`
	SourceRetired    bool   `json:"source_retired"`
}

// RepairInspection is the bounded, read-only input returned to the offline
// repair presentation layer. Integrity and artifact diagnostics describe a
// private recovery copy and the selected source artifact tree respectively;
// neither operation mutates the selected source directory.
type RepairInspection struct {
	Path      string                         `json:"path"`
	Status    string                         `json:"status"`
	Copied    RepairCopiedFiles              `json:"copied"`
	Integrity store.CanonicalIntegrityReport `json:"integrity"`
	Artifacts ArtifactFilesystemReport       `json:"artifacts"`
	Findings  []RepairFinding                `json:"findings"`
}

type RepairCopiedFiles struct {
	DatabaseBytes int64 `json:"database_bytes"`
	WALBytes      int64 `json:"wal_bytes"`
	SHMBytes      int64 `json:"shm_bytes"`
	WALPresent    bool  `json:"wal_present"`
	SHMPresent    bool  `json:"shm_present"`
}

type RepairFinding struct {
	Code        string `json:"code"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`
	Remediation string `json:"remediation"`
}

type BackupNotQuiescentDetails struct {
	Counts  store.QuiescenceCounts    `json:"counts"`
	Samples []store.QuiescenceBlocker `json:"samples"`
}

type ArtifactFilesystemReport struct {
	Status            string                    `json:"status"`
	Complete          bool                      `json:"complete"`
	CheckedCount      int64                     `json:"checked_count"`
	IssueCount        int64                     `json:"issue_count"`
	WarningCount      int64                     `json:"warning_count"`
	MissingCount      int64                     `json:"missing_count"`
	HashMismatchCount int64                     `json:"hash_mismatch_count"`
	UnsafeCount       int64                     `json:"unsafe_count"`
	ExtraCount        int64                     `json:"extra_count"`
	FreeBytes         uint64                    `json:"free_bytes"`
	Issues            []ArtifactFilesystemIssue `json:"issues"`
	Warnings          []ArtifactFilesystemIssue `json:"warnings"`
}

type ArtifactFilesystemIssue struct {
	Code   string `json:"code"`
	Kind   string `json:"kind,omitempty"`
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

type Error struct {
	Code              string
	Message           string
	Cause             error
	Quiescence        *BackupNotQuiescentDetails
	VerificationPhase string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return e.Message
	}
	return e.Message + ": " + e.Cause.Error()
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func ErrorCode(err error) string {
	for err != nil {
		if current, ok := err.(*Error); ok {
			return current.Code
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return ""
		}
		err = unwrapper.Unwrap()
	}
	return ""
}

// VerificationFailurePhase reports the completed verification boundary at
// which an offline bundle check failed. It is presentation metadata, separate
// from the stable public error code, so callers never have to infer phase truth
// from human-readable error text.
func VerificationFailurePhase(err error) string {
	for err != nil {
		if current, ok := err.(*Error); ok && current.VerificationPhase != "" {
			return current.VerificationPhase
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return ""
		}
		err = unwrapper.Unwrap()
	}
	return ""
}
