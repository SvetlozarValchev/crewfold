package domain

const (
	ContextPacketSchemaV1 = "urn:crewfold:schema:domain:context-packet:v1"
	ContextPacketSchemaV2 = "urn:crewfold:schema:domain:context-packet:v2"
	ContextPacketSchema   = "urn:crewfold:schema:domain:context-packet:v3"
)

type ContextRole struct {
	AgentID  string `json:"agent_id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Provider string `json:"provider"`
	Runtime  string `json:"runtime"`
	Revision int64  `json:"revision"`
}

type ContextTask struct {
	TaskID      string `json:"task_id"`
	ObjectiveID string `json:"objective_id,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Priority    int    `json:"priority"`
	Budget      Budget `json:"budget"`
	Revision    int64  `json:"revision"`
}

type ContextCheckout struct {
	CheckoutID            string `json:"checkout_id"`
	ProjectID             string `json:"project_id"`
	ProjectName           string `json:"project_name"`
	RepositoryID          string `json:"repository_id"`
	RepositoryFingerprint string `json:"repository_fingerprint"`
	Path                  string `json:"path"`
	WriteMode             string `json:"write_mode"`
	CheckoutKind          string `json:"checkout_kind"`
	Branch                string `json:"branch,omitempty"`
	HeadCommit            string `json:"head_commit,omitempty"`
	Dirty                 bool   `json:"dirty"`
	Revision              int64  `json:"revision"`
}

type ContextDependency struct {
	TaskID   string `json:"task_id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Revision int64  `json:"revision"`
}

type ContextPolicy struct {
	AllowedTools     []string `json:"allowed_tools"`
	DeniedOperations []string `json:"denied_operations"`
	ApprovalRequired []string `json:"approval_required"`
}

type ContextReporting struct {
	Progress   string `json:"progress"`
	Blocked    string `json:"blocked"`
	Artifact   string `json:"artifact"`
	Completion string `json:"completion"`
}

type ContextSelection struct {
	Section    string `json:"section"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Revision   int64  `json:"revision"`
	Reason     string `json:"reason"`
}

type ContextExclusion struct {
	Section               string `json:"section"`
	EntityType            string `json:"entity_type,omitempty"`
	EntityID              string `json:"entity_id,omitempty"`
	Revision              int64  `json:"revision,omitempty"`
	RequestedRevisionID   string `json:"requested_revision_id,omitempty"`
	ReplacementRevisionID string `json:"replacement_revision_id,omitempty"`
	ReasonCode            string `json:"reason_code,omitempty"`
	Reason                string `json:"reason"`
	ByteSize              int    `json:"byte_size,omitempty"`
}

type ContextBudgetUsage struct {
	LimitBytes     int `json:"limit_bytes"`
	UsedBytes      int `json:"used_bytes"`
	RemainingBytes int `json:"remaining_bytes"`
}

type ContextBudget struct {
	Total     ContextBudgetUsage `json:"total"`
	Knowledge ContextBudgetUsage `json:"knowledge"`
}

type ContextPacket struct {
	Schema                        string              `json:"schema"`
	ID                            string              `json:"id"`
	WorkspaceID                   string              `json:"workspace_id"`
	ProjectID                     string              `json:"project_id"`
	TaskID                        string              `json:"task_id"`
	AgentID                       string              `json:"agent_id"`
	CheckoutID                    string              `json:"checkout_id"`
	Role                          ContextRole         `json:"role"`
	Task                          ContextTask         `json:"task"`
	Checkout                      ContextCheckout     `json:"checkout"`
	Dependencies                  []ContextDependency `json:"dependencies"`
	Inbox                         InboxSummary        `json:"inbox,omitzero"`
	RequestedKnowledgeRevisionIDs []string            `json:"requested_knowledge_revision_ids,omitzero"`
	AcceptedKnowledge             []KnowledgeRevision `json:"accepted_knowledge,omitzero"`
	Policy                        ContextPolicy       `json:"policy"`
	Reporting                     ContextReporting    `json:"reporting"`
	Included                      []ContextSelection  `json:"included"`
	Excluded                      []ContextExclusion  `json:"excluded"`
	Budget                        ContextBudget       `json:"budget,omitzero"`
	ContentHash                   string              `json:"content_hash"`
	ByteSize                      int                 `json:"byte_size"`
	CreatedAt                     string              `json:"created_at"`
	CreatedBy                     string              `json:"created_by"`
}

type ContextExplanation struct {
	PacketID    string             `json:"packet_id"`
	ContentHash string             `json:"content_hash"`
	ByteSize    int                `json:"byte_size"`
	Included    []ContextSelection `json:"included"`
	Excluded    []ContextExclusion `json:"excluded"`
	Budget      ContextBudget      `json:"budget,omitzero"`
}

type RunCapability struct {
	RunID           string `json:"run_id"`
	ContextPacketID string `json:"context_packet_id"`
	ExpiresAt       string `json:"expires_at"`
}

type RunBriefing struct {
	Run       Run           `json:"run"`
	Task      Task          `json:"task"`
	Packet    ContextPacket `json:"packet"`
	ExpiresAt string        `json:"capability_expires_at"`
	Resource  string        `json:"resource"`
}

type RunReport struct {
	ID             string   `json:"id"`
	RunID          string   `json:"run_id"`
	Kind           string   `json:"kind"`
	Message        string   `json:"message"`
	Evidence       []string `json:"evidence"`
	Handoff        string   `json:"handoff,omitempty"`
	IdempotencyKey string   `json:"idempotency_key"`
	Status         string   `json:"status"`
	CreatedAt      string   `json:"created_at"`
	AppliedAt      string   `json:"applied_at,omitempty"`
}

type RunArtifact struct {
	ID             string `json:"id"`
	RunID          string `json:"run_id"`
	Name           string `json:"name"`
	MediaType      string `json:"media_type"`
	ContentHash    string `json:"content_hash"`
	ByteSize       int    `json:"byte_size"`
	IdempotencyKey string `json:"idempotency_key"`
	CreatedAt      string `json:"created_at"`
}

type RunToolCall struct {
	ID         string `json:"id"`
	RunID      string `json:"run_id"`
	RequestID  string `json:"request_id"`
	Method     string `json:"method"`
	TargetID   string `json:"target_id,omitempty"`
	Outcome    string `json:"outcome"`
	ErrorCode  string `json:"error_code,omitempty"`
	RecordedAt string `json:"recorded_at"`
}
