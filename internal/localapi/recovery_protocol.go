package localapi

const (
	MethodSystemDoctorFull = "system.doctor.full"
	MethodBackupCreate     = "backup.create"

	FullDoctorSchema   = "urn:crewfold:schema:local-api:full-doctor-result:v1"
	BackupCreateSchema = "urn:crewfold:schema:local-api:backup-create-result:v1"
)

var fullDoctorCheckCodes = [...]string{
	"current_baseline",
	"sqlite_integrity_check",
	"foreign_keys",
	"canonical_integrity",
	"event_contract",
	"projection_receipt_parity",
	"artifact_integrity",
	"derived_knowledge_index",
	"runtime_bindings",
	"managed_services",
	"durable_queues",
	"filesystem_permissions",
	"resource_budget",
	"restore_activation",
}

func FullDoctorCheckOrder() []string {
	return append([]string(nil), fullDoctorCheckCodes[:]...)
}

type SystemDoctorFullParams struct{}

type FullDoctorBaseline struct {
	SHA256                string `json:"sha256"`
	InstalledSchemaSHA256 string `json:"installed_schema_sha256"`
}

type FullDoctorResources struct {
	DatabaseBytes           int64 `json:"database_bytes"`
	ReferencedArtifactBytes int64 `json:"referenced_artifact_bytes"`
	RSSBytes                int64 `json:"rss_bytes"`
	Goroutines              int64 `json:"goroutines"`
	OpenFDs                 int64 `json:"open_fds"`
	FilesystemFreeBytes     int64 `json:"filesystem_free_bytes"`
}

type FullDoctorLimits struct {
	BriefingClaims     int64 `json:"briefing_claims"`
	BriefingBytes      int64 `json:"briefing_bytes"`
	NodeUnresolvedRuns int64 `json:"node_unresolved_runs"`
}

type FullDoctorSample struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Code       string `json:"code"`
	Detail     string `json:"detail"`
}

type FullDoctorRemediation struct {
	Kind    string   `json:"kind"`
	Command []string `json:"command"`
}

type FullDoctorCheck struct {
	Code         string                `json:"code"`
	Status       string                `json:"status"`
	CheckedCount int64                 `json:"checked_count"`
	IssueCount   int64                 `json:"issue_count"`
	Summary      string                `json:"summary"`
	Samples      []FullDoctorSample    `json:"samples"`
	Remediation  FullDoctorRemediation `json:"remediation"`
}

type FullDoctorResult struct {
	Schema        string              `json:"schema"`
	Type          string              `json:"type"`
	Status        string              `json:"status"`
	EventSequence int64               `json:"event_sequence"`
	Baseline      FullDoctorBaseline  `json:"baseline"`
	Resources     FullDoctorResources `json:"resources"`
	Limits        FullDoctorLimits    `json:"limits"`
	Checks        []FullDoctorCheck   `json:"checks"`
}

type BackupCreateParams struct {
	TargetPath     string `json:"target_path"`
	IdempotencyKey string `json:"idempotency_key"`
}

type BackupSummary struct {
	ID                 string `json:"id"`
	Path               string `json:"path"`
	CreatedAt          string `json:"created_at"`
	BaselineSHA256     string `json:"baseline_sha256"`
	EventSequence      int64  `json:"event_sequence"`
	LogicalStateSHA256 string `json:"logical_state_sha256"`
	DatabaseSHA256     string `json:"database_sha256"`
	ManifestSHA256     string `json:"manifest_sha256"`
	ArtifactCount      int64  `json:"artifact_count"`
	TotalBytes         int64  `json:"total_bytes"`
}

type BackupCreateResult struct {
	Schema string        `json:"schema"`
	Type   string        `json:"type"`
	Backup BackupSummary `json:"backup"`
}
