package domain

const (
	PortableKnowledgeBundleManifestSchema = "urn:crewfold:schema:portable:knowledge-bundle-manifest:v1"
	PortableKnowledgeBundleType           = "portable_knowledge_bundle"
	PortableKnowledgeRenderingPath        = "knowledge.md"
	PortableKnowledgeRenderingMediaType   = "text/markdown; charset=utf-8"
)

// PortableKnowledgeBundleManifest is the canonical, provider-neutral envelope
// for one exact project knowledge snapshot. Field order is part of canonical
// JSON v1 and must not be rearranged.
type PortableKnowledgeBundleManifest struct {
	Schema        string                     `json:"schema"`
	Type          string                     `json:"type"`
	BundleID      string                     `json:"bundle_id"`
	ContentSHA256 string                     `json:"content_sha256"`
	Snapshot      PortableKnowledgeSnapshot  `json:"snapshot"`
	Rendering     PortableKnowledgeRendering `json:"rendering"`
}

type PortableKnowledgeRendering struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	ByteSize  int64  `json:"byte_size"`
	SHA256    string `json:"sha256"`
}

// PortableKnowledgeSnapshot contains canonical knowledge only. It deliberately
// excludes local event sequences, authority ledgers, curator state, retrieval
// indexes, context packets, transcripts, credentials, and provider state.
type PortableKnowledgeSnapshot struct {
	Scope            PortableKnowledgeScope             `json:"scope"`
	Counts           PortableKnowledgeCounts            `json:"counts"`
	TaskScopeAnchors []PortableKnowledgeTaskScopeAnchor `json:"task_scope_anchors"`
	Items            []PortableKnowledgeItem            `json:"items"`
	Contradictions   []PortableKnowledgeContradiction   `json:"contradictions"`
}

type PortableKnowledgeScope struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	ProjectID     string `json:"project_id"`
	ProjectName   string `json:"project_name"`
}

type PortableKnowledgeCounts struct {
	TaskScopeAnchors int64 `json:"task_scope_anchors"`
	Items            int64 `json:"items"`
	Revisions        int64 `json:"revisions"`
	Contradictions   int64 `json:"contradictions"`
}

type PortableKnowledgeTaskScopeAnchor struct {
	TaskID      string `json:"task_id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	CreatedAt   string `json:"created_at"`
	CreatedBy   string `json:"created_by"`
}

type PortableKnowledgeItem struct {
	Item      KnowledgeItem       `json:"item"`
	Revisions []KnowledgeRevision `json:"revisions"`
}

// PortableKnowledgeContradiction is the complete final lifecycle snapshot with
// local numeric event linkage intentionally omitted.
type PortableKnowledgeContradiction struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspace_id"`
	ProjectID        string `json:"project_id"`
	LeftRevisionID   string `json:"left_revision_id"`
	RightRevisionID  string `json:"right_revision_id"`
	Status           string `json:"status"`
	StateRevision    int64  `json:"state_revision"`
	ReportNote       string `json:"report_note"`
	ReportedAt       string `json:"reported_at"`
	ReportedBy       string `json:"reported_by"`
	ReportedByType   string `json:"reported_by_type"`
	ConfirmedAt      string `json:"confirmed_at,omitempty"`
	ConfirmedBy      string `json:"confirmed_by,omitempty"`
	ConfirmedByType  string `json:"confirmed_by_type,omitempty"`
	ConfirmNote      string `json:"confirm_note,omitempty"`
	DismissedAt      string `json:"dismissed_at,omitempty"`
	DismissedBy      string `json:"dismissed_by,omitempty"`
	DismissedByType  string `json:"dismissed_by_type,omitempty"`
	DismissNote      string `json:"dismiss_note,omitempty"`
	ResolutionReason string `json:"resolution_reason,omitempty"`
	ResolvedAt       string `json:"resolved_at,omitempty"`
	ResolvedBy       string `json:"resolved_by,omitempty"`
	ResolvedByType   string `json:"resolved_by_type,omitempty"`
	ResolutionNote   string `json:"resolution_note,omitempty"`
}

type KnowledgeImportReceipt struct {
	ID                     string `json:"id"`
	BundleID               string `json:"bundle_id"`
	WorkspaceID            string `json:"workspace_id"`
	ProjectID              string `json:"project_id"`
	ContentSHA256          string `json:"content_sha256"`
	RenderingSHA256        string `json:"rendering_sha256"`
	ImportedAt             string `json:"imported_at"`
	ImportedBy             string `json:"imported_by"`
	ImportedByType         string `json:"imported_by_type"`
	CompletedEventSequence int64  `json:"completed_event_sequence"`
}
