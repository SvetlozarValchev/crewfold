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
	ID               string `json:"id"`
	RoomID           string `json:"room_id"`
	Handle           string `json:"handle"`
	DisplayName      string `json:"display_name"`
	Kind             string `json:"kind"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	Status           string `json:"status"`
	Context          string `json:"context,omitempty"`
	ContextUpdatedAt string `json:"context_updated_at,omitempty"`
	LastReadSequence int64  `json:"last_read_sequence"`
	JoinedAt         string `json:"joined_at"`
	LastSeenAt       string `json:"last_seen_at"`
	UnreadCount      int    `json:"unread_count"`
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
	Room         Room          `json:"room"`
	Participants []Participant `json:"participants"`
	Messages     []Message     `json:"messages"`
	Documents    []Document    `json:"documents"`
}

type CreateRoomInput struct {
	Slug          string `json:"slug"`
	Title         string `json:"title"`
	Topic         string `json:"topic"`
	StewardHandle string `json:"steward_handle,omitempty"`
}

type JoinInput struct {
	Room             string `json:"room"`
	Handle           string `json:"handle"`
	DisplayName      string `json:"display_name"`
	WorkingDirectory string `json:"working_directory"`
	Kind             string `json:"kind,omitempty"`
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
	Room  string `json:"room"`
	After int64  `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}
