package room

// Room is one medium-lived collaboration space shared by independently run
// agent sessions and their human owner.
type Room struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Title        string `json:"title"`
	Topic        string `json:"topic"`
	Status       string `json:"status"`
	StewardID    string `json:"steward_id,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	LastSequence int64  `json:"last_sequence"`
}

type Participant struct {
	ID               string               `json:"id"`
	RoomID           string               `json:"room_id"`
	Handle           string               `json:"handle"`
	DisplayName      string               `json:"display_name"`
	Kind             string               `json:"kind"`
	WorkingDirectory string               `json:"working_directory,omitempty"`
	Status           string               `json:"status"`
	Context          string               `json:"context,omitempty"`
	ContextUpdatedAt string               `json:"context_updated_at,omitempty"`
	LastReadSequence int64                `json:"last_read_sequence"`
	JoinedAt         string               `json:"joined_at"`
	LastSeenAt       string               `json:"last_seen_at"`
	UnreadCount      int                  `json:"unread_count"`
	Delivery         *ParticipantDelivery `json:"delivery,omitempty"`
}

// ParticipantDelivery is the durable route Crewfold uses to notify an
// independently run participant. It is separate from the participant's room
// acknowledgement cursor: delivery means Codex received a visible prompt,
// while acknowledgement means the participant consumed canonical room state.
type ParticipantDelivery struct {
	Kind                  string `json:"kind"`
	Target                string `json:"target"`
	Status                string `json:"status"`
	LastDeliveredSequence int64  `json:"last_delivered_sequence"`
	LastAttemptAt         string `json:"last_attempt_at,omitempty"`
	LastDeliveredAt       string `json:"last_delivered_at,omitempty"`
	Error                 string `json:"error,omitempty"`
	UpdatedAt             string `json:"updated_at"`
}

// HostedSteward is the one optional room participant whose real Codex terminal
// is kept alive by a named Herdr session. Other room participants remain
// independently run processes that join through the CLI.
type HostedSteward struct {
	RoomID                  string `json:"room_id"`
	ParticipantID           string `json:"participant_id"`
	Handle                  string `json:"handle"`
	DisplayName             string `json:"display_name"`
	Role                    string `json:"role"`
	WorkingDirectory        string `json:"working_directory"`
	ManagedWorkingDirectory bool   `json:"managed_working_directory"`
	HerdrSession            string `json:"herdr_session"`
	HerdrWorkspaceID        string `json:"herdr_workspace_id,omitempty"`
	HerdrPaneID             string `json:"herdr_pane_id,omitempty"`
	AgentName               string `json:"agent_name"`
	DesiredState            string `json:"desired_state"`
	Status                  string `json:"status"`
	AgentStatus             string `json:"agent_status,omitempty"`
	LastDeliveredSequence   int64  `json:"last_delivered_sequence"`
	Error                   string `json:"error,omitempty"`
	InitializedAt           string `json:"initialized_at,omitempty"`
	StartedAt               string `json:"started_at,omitempty"`
	UpdatedAt               string `json:"updated_at"`
}

type StewardConsole struct {
	Steward HostedSteward `json:"steward"`
	Output  string        `json:"output"`
}

type Document struct {
	ID            string `json:"id"`
	RoomID        string `json:"room_id"`
	ParticipantID string `json:"participant_id,omitempty"`
	Name          string `json:"name"`
	MediaType     string `json:"media_type"`
	ByteSize      int64  `json:"byte_size"`
	SHA256        string `json:"sha256"`
	CreatedAt     string `json:"created_at"`
}

type Message struct {
	Sequence      int64     `json:"sequence"`
	ID            string    `json:"id"`
	RoomID        string    `json:"room_id"`
	ParticipantID string    `json:"participant_id,omitempty"`
	SenderHandle  string    `json:"sender_handle"`
	SenderName    string    `json:"sender_name"`
	SenderKind    string    `json:"sender_kind"`
	Kind          string    `json:"kind"`
	Body          string    `json:"body"`
	Document      *Document `json:"document,omitempty"`
	CreatedAt     string    `json:"created_at"`
}

type Snapshot struct {
	Room         Room           `json:"room"`
	Participants []Participant  `json:"participants"`
	Messages     []Message      `json:"messages"`
	Documents    []Document     `json:"documents"`
	Steward      *HostedSteward `json:"steward,omitempty"`
}

type CreateRoomInput struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Topic string `json:"topic"`
}

type StartStewardInput struct {
	Room             string `json:"room"`
	Handle           string `json:"handle"`
	DisplayName      string `json:"display_name,omitempty"`
	Role             string `json:"role,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
}

type PromptStewardInput struct {
	Room string `json:"room"`
	Text string `json:"text"`
}

type StewardKeyInput struct {
	Room string `json:"room"`
	Key  string `json:"key"`
}

type JoinInput struct {
	Room             string `json:"room"`
	Handle           string `json:"handle"`
	DisplayName      string `json:"display_name"`
	WorkingDirectory string `json:"working_directory"`
	Kind             string `json:"kind,omitempty"`
	Delivery         string `json:"delivery,omitempty"`
	ThreadID         string `json:"thread_id,omitempty"`
}

type SendInput struct {
	Room             string `json:"room"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	Handle           string `json:"handle,omitempty"`
	Owner            bool   `json:"owner,omitempty"`
	Kind             string `json:"kind,omitempty"`
	Body             string `json:"body"`
}

type UploadInput struct {
	Room             string `json:"room"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	Handle           string `json:"handle,omitempty"`
	Owner            bool   `json:"owner,omitempty"`
	Name             string `json:"name"`
	MediaType        string `json:"media_type"`
	ContentBase64    string `json:"content_base64"`
	Caption          string `json:"caption,omitempty"`
}

type AckInput struct {
	Room             string `json:"room"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	Handle           string `json:"handle,omitempty"`
	Through          int64  `json:"through,omitempty"`
}

type ListMessagesInput struct {
	Room   string `json:"room"`
	After  int64  `json:"after,omitempty"`
	Before int64  `json:"before,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
