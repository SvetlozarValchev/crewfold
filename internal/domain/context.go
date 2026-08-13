package domain

import "encoding/json"

const (
	ContextPacketSchemaV1 = "urn:crewfold:schema:domain:context-packet:v1"
	ContextPacketSchemaV2 = "urn:crewfold:schema:domain:context-packet:v2"
	ContextPacketSchemaV3 = "urn:crewfold:schema:domain:context-packet:v3"
	// ContextPacketSchema remains the packet-v4 identifier for source and wire
	// compatibility with the M15 API. Manager runs use the explicitly separate
	// v5 schema below; ordinary packets are not silently upgraded into a wider
	// capability.
	ContextPacketSchema   = "urn:crewfold:schema:domain:context-packet:v4"
	ContextPacketSchemaV4 = ContextPacketSchema
	ContextPacketSchemaV5 = "urn:crewfold:schema:domain:context-packet:v5"

	ContextLivePolicySchema   = "urn:crewfold:schema:domain:live-context-policy:v1"
	ContextManagerGrantSchema = "urn:crewfold:schema:domain:context-manager-grant:v1"
	ContextDeltaSchema        = "urn:crewfold:schema:domain:context-delta:v1"

	ContextLiveDeliveryExplicitPull = "explicit_pull"
	ContextLiveAckBoundRun          = "bound_run"

	ContextRefreshCreated        = "created"
	ContextRefreshPending        = "pending"
	ContextRefreshUpToDate       = "up_to_date"
	ContextRefreshRebaseRequired = "rebase_required"

	ContextDeltaPending        = "pending"
	ContextDeltaNonePending    = "none_pending"
	ContextDeltaRebaseRequired = "rebase_required"

	ContextDeltaMessageReceived          = "message_received"
	ContextDeltaKnowledgeAccepted        = "knowledge_accepted"
	ContextDeltaKnowledgeWithdrawn       = "knowledge_withdrawn"
	ContextDeltaContradictionOpened      = "contradiction_opened"
	ContextDeltaContradictionClosed      = "contradiction_closed"
	ContextDeltaDependentAdded           = "dependent_added"
	ContextDeltaDependentUpdated         = "dependent_updated"
	ContextDeltaParticipantRosterUpdated = "participant_roster_updated"

	ContextRebaseUnsupportedPacket       = "unsupported_packet"
	ContextRebaseBaseContractChanged     = "base_contract_changed"
	ContextRebaseDependencySetChanged    = "dependency_set_changed"
	ContextRebaseEventWindowExceeded     = "event_window_exceeded"
	ContextRebaseDeltaLimitExceeded      = "delta_limit_exceeded"
	ContextRebaseCumulativeLimitExceeded = "cumulative_limit_exceeded"
	ContextRebaseUnsupportedEventType    = "unsupported_event_type"
)

// IsLiveContextPacketSchema reports whether a packet carries the frozen bounded
// live-context contract. It deliberately excludes v1-v3 compatibility packets.
func IsLiveContextPacketSchema(schema string) bool {
	return schema == ContextPacketSchemaV4 || schema == ContextPacketSchemaV5
}

type ContextRole struct {
	AgentID  string `json:"agent_id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Provider string `json:"provider"`
	Runtime  string `json:"runtime"`
	Revision int64  `json:"revision"`
}

type ContextTask struct {
	TaskID       string `json:"task_id"`
	AssignmentID string `json:"assignment_id"`
	ObjectiveID  string `json:"objective_id,omitempty"`
	Title        string `json:"title"`
	Description  string `json:"description,omitempty"`
	Priority     int    `json:"priority"`
	Budget       Budget `json:"budget"`
	Revision     int64  `json:"revision"`
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
	Total         ContextBudgetUsage `json:"total"`
	Knowledge     ContextBudgetUsage `json:"knowledge"`
	Collaboration ContextBudgetUsage `json:"collaboration"`
}

// ContextLivePolicy freezes the delivery, acknowledgement, and byte bounds for
// a run. A daemon upgrade cannot silently widen a packet's live authority.
type ContextLivePolicy struct {
	Schema                    string `json:"schema"`
	Delivery                  string `json:"delivery"`
	AckAuthority              string `json:"ack_authority"`
	MaxPending                int    `json:"max_pending"`
	MaxRelevantEvents         int    `json:"max_relevant_events"`
	PerDeltaLimitBytes        int    `json:"per_delta_limit_bytes"`
	CumulativeDeltaLimitBytes int    `json:"cumulative_delta_limit_bytes"`
}

// ContextManagerLaunchProfile freezes one owner-defined scheduling capability.
// Runtime, provider, checkout, and scenario remain behind the canonical profile
// rather than becoming model-selected packet fields.
type ContextManagerLaunchProfile struct {
	LaunchProfileID string `json:"launch_profile_id"`
	Revision        int64  `json:"revision"`
	AgentID         string `json:"agent_id"`
	AgentRevision   int64  `json:"agent_revision"`
}

// ContextManagerGrant is the immutable v5 capability snapshot. Each proposal
// call still revalidates the current canonical grant; this copy explains the
// exact authority with which the run was launched.
type ContextManagerGrant struct {
	Schema               string                        `json:"schema"`
	GrantID              string                        `json:"grant_id"`
	GrantRevision        int64                         `json:"grant_revision"`
	WorkspaceID          string                        `json:"workspace_id"`
	ProjectID            string                        `json:"project_id"`
	ObjectiveID          string                        `json:"objective_id"`
	ObjectiveRevision    int64                         `json:"objective_revision"`
	ManagerAgentID       string                        `json:"manager_agent_id"`
	ManagerAgentRevision int64                         `json:"manager_agent_revision"`
	ManagerTaskID        string                        `json:"manager_task_id"`
	ManagerTaskRevision  int64                         `json:"manager_task_revision"`
	AllowedProposalKinds []string                      `json:"allowed_proposal_kinds"`
	LaunchProfiles       []ContextManagerLaunchProfile `json:"launch_profiles"`
	AllowedClaimKinds    []string                      `json:"allowed_claim_kinds"`
	MaxOpenProposals     int                           `json:"max_open_proposals"`
	MaxActions           int                           `json:"max_actions"`
	MaxTasks             int                           `json:"max_tasks"`
	MaxDependencies      int                           `json:"max_dependencies"`
	MaxClaimRequirements int                           `json:"max_claim_requirements"`
	Budget               Budget                        `json:"budget"`
	ExpiresAt            string                        `json:"expires_at,omitempty"`
}

type ContextPacket struct {
	Schema                        string               `json:"schema"`
	ID                            string               `json:"id"`
	WorkspaceID                   string               `json:"workspace_id"`
	ProjectID                     string               `json:"project_id"`
	TaskID                        string               `json:"task_id"`
	AgentID                       string               `json:"agent_id"`
	CheckoutID                    string               `json:"checkout_id"`
	Role                          ContextRole          `json:"role"`
	Task                          ContextTask          `json:"task"`
	Checkout                      ContextCheckout      `json:"checkout"`
	Dependencies                  []ContextDependency  `json:"dependencies"`
	Dependents                    []ContextDependency  `json:"dependents"`
	DependentTaskCount            int                  `json:"dependent_task_count"`
	Inbox                         InboxSummary         `json:"inbox,omitzero"`
	ParticipantThreads            []ParticipantThread  `json:"participant_threads"`
	RequestedKnowledgeRevisionIDs []string             `json:"requested_knowledge_revision_ids,omitzero"`
	AcceptedKnowledge             []KnowledgeRevision  `json:"accepted_knowledge,omitzero"`
	Policy                        ContextPolicy        `json:"policy"`
	LiveContext                   ContextLivePolicy    `json:"live_context"`
	ManagementGrant               *ContextManagerGrant `json:"management_grant,omitempty"`
	Reporting                     ContextReporting     `json:"reporting"`
	Included                      []ContextSelection   `json:"included"`
	Excluded                      []ContextExclusion   `json:"excluded"`
	Budget                        ContextBudget        `json:"budget,omitzero"`
	AsOfEventSequence             int64                `json:"as_of_event_sequence"`
	ContentHash                   string               `json:"content_hash"`
	ByteSize                      int                  `json:"byte_size"`
	CreatedAt                     string               `json:"created_at"`
	CreatedBy                     string               `json:"created_by"`
}

// MarshalJSON keeps v1-v3 wire shapes free of fields introduced by packet v4,
// keeps packet v4 byte-compatible, and makes every v4/v5 bounded collection
// explicit.
func (packet ContextPacket) MarshalJSON() ([]byte, error) {
	type plainContextPacket ContextPacket
	if IsLiveContextPacketSchema(packet.Schema) {
		copy := packet
		if copy.Dependencies == nil {
			copy.Dependencies = []ContextDependency{}
		}
		if copy.Dependents == nil {
			copy.Dependents = []ContextDependency{}
		}
		if copy.ParticipantThreads == nil {
			copy.ParticipantThreads = []ParticipantThread{}
		}
		return json.Marshal(plainContextPacket(copy))
	}
	type legacyContextTask struct {
		TaskID      string `json:"task_id"`
		ObjectiveID string `json:"objective_id,omitempty"`
		Title       string `json:"title"`
		Description string `json:"description,omitempty"`
		Priority    int    `json:"priority"`
		Budget      Budget `json:"budget"`
		Revision    int64  `json:"revision"`
	}
	type legacyContextBudget struct {
		Total     ContextBudgetUsage `json:"total"`
		Knowledge ContextBudgetUsage `json:"knowledge"`
	}
	legacy := struct {
		Schema                        string              `json:"schema"`
		ID                            string              `json:"id"`
		WorkspaceID                   string              `json:"workspace_id"`
		ProjectID                     string              `json:"project_id"`
		TaskID                        string              `json:"task_id"`
		AgentID                       string              `json:"agent_id"`
		CheckoutID                    string              `json:"checkout_id"`
		Role                          ContextRole         `json:"role"`
		Task                          legacyContextTask   `json:"task"`
		Checkout                      ContextCheckout     `json:"checkout"`
		Dependencies                  []ContextDependency `json:"dependencies"`
		Inbox                         InboxSummary        `json:"inbox,omitzero"`
		RequestedKnowledgeRevisionIDs []string            `json:"requested_knowledge_revision_ids,omitzero"`
		AcceptedKnowledge             []KnowledgeRevision `json:"accepted_knowledge,omitzero"`
		Policy                        ContextPolicy       `json:"policy"`
		Reporting                     ContextReporting    `json:"reporting"`
		Included                      []ContextSelection  `json:"included"`
		Excluded                      []ContextExclusion  `json:"excluded"`
		Budget                        legacyContextBudget `json:"budget,omitzero"`
		ContentHash                   string              `json:"content_hash"`
		ByteSize                      int                 `json:"byte_size"`
		CreatedAt                     string              `json:"created_at"`
		CreatedBy                     string              `json:"created_by"`
	}{
		Schema: packet.Schema, ID: packet.ID, WorkspaceID: packet.WorkspaceID,
		ProjectID: packet.ProjectID, TaskID: packet.TaskID, AgentID: packet.AgentID,
		CheckoutID: packet.CheckoutID, Role: packet.Role,
		Task: legacyContextTask{TaskID: packet.Task.TaskID, ObjectiveID: packet.Task.ObjectiveID,
			Title: packet.Task.Title, Description: packet.Task.Description, Priority: packet.Task.Priority,
			Budget: packet.Task.Budget, Revision: packet.Task.Revision},
		Checkout: packet.Checkout, Dependencies: packet.Dependencies, Inbox: packet.Inbox,
		RequestedKnowledgeRevisionIDs: packet.RequestedKnowledgeRevisionIDs,
		AcceptedKnowledge:             packet.AcceptedKnowledge, Policy: packet.Policy, Reporting: packet.Reporting,
		Included: packet.Included, Excluded: packet.Excluded,
		Budget:      legacyContextBudget{Total: packet.Budget.Total, Knowledge: packet.Budget.Knowledge},
		ContentHash: packet.ContentHash, ByteSize: packet.ByteSize, CreatedAt: packet.CreatedAt, CreatedBy: packet.CreatedBy,
	}
	return json.Marshal(legacy)
}

type ContextDeltaCause struct {
	EventSequence int64  `json:"event_sequence,omitempty"`
	Reason        string `json:"reason"`
}

type ContextKnowledgeWithdrawal struct {
	RevisionID             string   `json:"revision_id"`
	StateRevision          int64    `json:"state_revision"`
	Reason                 string   `json:"reason"`
	ReplacementRevisionID  string   `json:"replacement_revision_id,omitempty"`
	OpenContradictionIDs   []string `json:"open_contradiction_ids"`
	OpenContradictionCount int      `json:"open_contradiction_count"`
}

type ContextContradictionSnapshot struct {
	Contradiction KnowledgeContradiction `json:"contradiction"`
	LeftRevision  KnowledgeRevision      `json:"left_revision,omitzero"`
	RightRevision KnowledgeRevision      `json:"right_revision,omitzero"`
}

// ContextDeltaChange is a closed typed union. Exactly the payload selected by
// Kind is populated; consumers can reject unknown kinds without decoding raw
// authority data.
type ContextDeltaChange struct {
	Kind              string                        `json:"kind"`
	EntityType        string                        `json:"entity_type"`
	EntityID          string                        `json:"entity_id"`
	Revision          int64                         `json:"revision"`
	Cause             ContextDeltaCause             `json:"cause"`
	Message           *InboxSummaryItem             `json:"message,omitempty"`
	Knowledge         *KnowledgeRevision            `json:"knowledge,omitempty"`
	Withdrawal        *ContextKnowledgeWithdrawal   `json:"withdrawal,omitempty"`
	Contradiction     *ContextContradictionSnapshot `json:"contradiction,omitempty"`
	Dependency        *ContextDependency            `json:"dependency,omitempty"`
	ParticipantThread *ParticipantThread            `json:"participant_thread,omitempty"`
}

type ContextDelta struct {
	Schema               string               `json:"schema"`
	ID                   string               `json:"id"`
	RunID                string               `json:"run_id"`
	ContextPacketID      string               `json:"context_packet_id"`
	WorkspaceID          string               `json:"workspace_id"`
	ProjectID            string               `json:"project_id"`
	TaskID               string               `json:"task_id"`
	AgentID              string               `json:"agent_id"`
	BasePacketSchema     string               `json:"base_packet_schema"`
	Sequence             int64                `json:"sequence"`
	ParentDeltaID        string               `json:"parent_delta_id,omitempty"`
	FromEventSequence    int64                `json:"from_event_sequence"`
	ThroughEventSequence int64                `json:"through_event_sequence"`
	EvaluatedAt          string               `json:"evaluated_at"`
	Changes              []ContextDeltaChange `json:"changes"`
	Included             []ContextSelection   `json:"included"`
	Excluded             []ContextExclusion   `json:"excluded"`
	Budget               ContextDeltaBudget   `json:"budget"`
	ContentHash          string               `json:"content_hash"`
	ByteSize             int                  `json:"byte_size"`
	CreatedAt            string               `json:"created_at"`
	CreatedBy            string               `json:"created_by"`
}

type ContextDeltaBudget struct {
	Total ContextBudgetUsage `json:"total"`
	Chain ContextBudgetUsage `json:"chain"`
}

type ContextRefreshResult struct {
	Status                      string            `json:"status"`
	RunID                       string            `json:"run_id"`
	ContextPacketID             string            `json:"context_packet_id"`
	StateRevision               int64             `json:"state_revision"`
	ScannedFromEventSequence    int64             `json:"scanned_from_event_sequence"`
	ScannedThroughEventSequence int64             `json:"scanned_through_event_sequence"`
	Chain                       ContextDeltaChain `json:"chain"`
	Delta                       *ContextDelta     `json:"delta,omitempty"`
	RebaseReason                string            `json:"rebase_reason,omitempty"`
	EventSequence               int64             `json:"event_sequence"`
	Replayed                    bool              `json:"-"`
}

type ContextDeltaFetchResult struct {
	Status                      string            `json:"status"`
	RunID                       string            `json:"run_id"`
	ContextPacketID             string            `json:"context_packet_id"`
	StateRevision               int64             `json:"state_revision"`
	ScannedThroughEventSequence int64             `json:"scanned_through_event_sequence"`
	Chain                       ContextDeltaChain `json:"chain"`
	Delta                       *ContextDelta     `json:"delta,omitempty"`
	RebaseReason                string            `json:"rebase_reason,omitempty"`
}

type ContextDeltaAcknowledgement struct {
	ID              string `json:"id"`
	RunID           string `json:"run_id"`
	ContextPacketID string `json:"context_packet_id"`
	DeltaID         string `json:"delta_id"`
	Sequence        int64  `json:"sequence"`
	AcknowledgedAt  string `json:"acknowledged_at"`
	AcknowledgedBy  string `json:"acknowledged_by"`
	EventSequence   int64  `json:"event_sequence"`
	Replayed        bool   `json:"-"`
}

type ContextDeltaExplanation struct {
	DeltaID              string             `json:"delta_id"`
	RunID                string             `json:"run_id"`
	ContextPacketID      string             `json:"context_packet_id"`
	Sequence             int64              `json:"sequence"`
	ParentDeltaID        string             `json:"parent_delta_id,omitempty"`
	FromEventSequence    int64              `json:"from_event_sequence"`
	ThroughEventSequence int64              `json:"through_event_sequence"`
	ChangeKinds          []string           `json:"change_kinds"`
	ContentHash          string             `json:"content_hash"`
	ByteSize             int                `json:"byte_size"`
	Included             []ContextSelection `json:"included"`
	Excluded             []ContextExclusion `json:"excluded"`
	Budget               ContextDeltaBudget `json:"budget"`
}

type ContextDeltaChain struct {
	RunID                       string `json:"run_id"`
	ContextPacketID             string `json:"context_packet_id"`
	BaseEventSequence           int64  `json:"base_event_sequence"`
	ScannedThroughEventSequence int64  `json:"scanned_through_event_sequence"`
	LatestDeltaID               string `json:"latest_delta_id,omitempty"`
	LatestSequence              int64  `json:"latest_sequence"`
	PendingDeltaID              string `json:"pending_delta_id,omitempty"`
	PendingSequence             int64  `json:"pending_sequence"`
	LastAcknowledgedDeltaID     string `json:"last_acknowledged_delta_id,omitempty"`
	LastAcknowledgedSequence    int64  `json:"last_acknowledged_sequence"`
	DeltaCount                  int64  `json:"delta_count"`
	CumulativeByteSize          int    `json:"cumulative_byte_size"`
	RebaseReason                string `json:"rebase_reason,omitempty"`
	RebaseEventSequence         int64  `json:"rebase_event_sequence,omitempty"`
	Revision                    int64  `json:"revision"`
}

type ContextDeltaList struct {
	Chain         ContextDeltaChain `json:"chain"`
	AfterSequence int64             `json:"after_sequence"`
	NextSequence  int64             `json:"next_sequence"`
	HasMore       bool              `json:"has_more"`
	Deltas        []ContextDelta    `json:"deltas"`
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
