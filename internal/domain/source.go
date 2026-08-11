package domain

const (
	WriteModeExclusive = "exclusive"
	WriteModeClaimed   = "claimed"
	WriteModeShared    = "shared"
	WriteModeReadOnly  = "read_only"

	CheckoutAvailable   = "available"
	CheckoutUnavailable = "unavailable"

	CheckoutStandalone     = "standalone"
	CheckoutLinkedWorktree = "linked_worktree"
	CheckoutKindUnknown    = "unknown"
)

type Project struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Revision    int64  `json:"revision"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	CreatedBy   string `json:"created_by"`
	UpdatedBy   string `json:"updated_by"`
}

type Repository struct {
	ID           string   `json:"id"`
	WorkspaceID  string   `json:"workspace_id"`
	Fingerprint  string   `json:"fingerprint"`
	ObjectFormat string   `json:"object_format"`
	RootCommits  []string `json:"root_commits"`
	Revision     int64    `json:"revision"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	CreatedBy    string   `json:"created_by"`
	UpdatedBy    string   `json:"updated_by"`
}

type Checkout struct {
	ID             string `json:"id"`
	ProjectID      string `json:"project_id"`
	RepositoryID   string `json:"repository_id"`
	Path           string `json:"path"`
	WriteMode      string `json:"write_mode"`
	Revision       int64  `json:"revision"`
	Availability   string `json:"availability"`
	CheckoutKind   string `json:"checkout_kind"`
	Branch         string `json:"branch,omitempty"`
	HeadCommit     string `json:"head_commit,omitempty"`
	Dirty          bool   `json:"dirty"`
	GitDir         string `json:"git_dir,omitempty"`
	GitCommonDir   string `json:"git_common_dir,omitempty"`
	ObservedAt     string `json:"observed_at"`
	DiagnosticCode string `json:"diagnostic_code,omitempty"`
	Diagnostic     string `json:"diagnostic,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	CreatedBy      string `json:"created_by"`
	UpdatedBy      string `json:"updated_by"`
}

type RepositoryObservation struct {
	Fingerprint  string   `json:"fingerprint"`
	ObjectFormat string   `json:"object_format"`
	RootCommits  []string `json:"root_commits"`
}

type CheckoutObservation struct {
	Path           string                `json:"path"`
	Availability   string                `json:"availability"`
	CheckoutKind   string                `json:"checkout_kind"`
	Branch         string                `json:"branch,omitempty"`
	HeadCommit     string                `json:"head_commit,omitempty"`
	Dirty          bool                  `json:"dirty"`
	GitDir         string                `json:"git_dir,omitempty"`
	GitCommonDir   string                `json:"git_common_dir,omitempty"`
	DiagnosticCode string                `json:"diagnostic_code,omitempty"`
	Diagnostic     string                `json:"diagnostic,omitempty"`
	Repository     RepositoryObservation `json:"repository"`
}
