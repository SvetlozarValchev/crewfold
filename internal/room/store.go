package room

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ncruces/go-sqlite3/driver"
)

const (
	databaseFilename     = "rooms.db"
	maximumDocumentBytes = 4 * 1024 * 1024
)

type Store struct {
	db      *sql.DB
	dataDir string
	now     func() time.Time
}

func Open(ctx context.Context, dataDir string) (*Store, error) {
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("secure data directory: %w", err)
	}
	location := &url.URL{Scheme: "file", Path: filepath.Join(root, databaseFilename)}
	query := location.Query()
	query.Set("mode", "rwc")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Set("_txlock", "immediate")
	location.RawQuery = query.Encode()
	database, err := driver.Open(location.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("open room database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	store := &Store{db: database, dataDir: root, now: time.Now}
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to room database: %w", err)
	}
	if _, err := database.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("enable room database WAL: %w", err)
	}
	if err := store.initialize(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initialize(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS rooms (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  topic TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('open','archived')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS participants (
  id TEXT PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  handle TEXT NOT NULL,
  display_name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('agent','steward')),
  working_directory TEXT,
  status TEXT NOT NULL CHECK(status IN ('joined','left')),
  context TEXT NOT NULL DEFAULT '',
  context_updated_at TEXT,
  last_read_sequence INTEGER NOT NULL DEFAULT 0 CHECK(last_read_sequence >= 0),
  joined_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  UNIQUE(room_id, handle),
  UNIQUE(room_id, working_directory)
) STRICT;
CREATE TABLE IF NOT EXISTS participant_deliveries (
  participant_id TEXT PRIMARY KEY REFERENCES participants(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK(kind IN ('codex','claude')),
  target TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('bound','queued','delivered','error')),
  last_delivered_sequence INTEGER NOT NULL DEFAULT 0 CHECK(last_delivered_sequence >= 0),
  last_attempt_at TEXT,
  last_delivered_at TEXT,
  error TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS network_settings (
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
  public_origin TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS network_devices (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  browser_handle TEXT NOT NULL DEFAULT '',
  joined_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS network_room_memberships (
  device_id TEXT NOT NULL REFERENCES network_devices(id) ON DELETE CASCADE,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  PRIMARY KEY(device_id,room_id)
) STRICT;
CREATE TABLE IF NOT EXISTS network_invites (
  id TEXT PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  secret_hash TEXT NOT NULL UNIQUE,
  expires_at TEXT NOT NULL,
  used_by_device_id TEXT REFERENCES network_devices(id),
  used_at TEXT,
  created_at TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS network_participant_bindings (
  participant_id TEXT PRIMARY KEY REFERENCES participants(id) ON DELETE CASCADE,
  device_id TEXT NOT NULL REFERENCES network_devices(id) ON DELETE CASCADE,
  provider TEXT NOT NULL CHECK(provider IN ('codex','claude','none')),
  remote_directory TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('bound','queued','delivered','error','left')),
  last_delivered_sequence INTEGER NOT NULL DEFAULT 0 CHECK(last_delivered_sequence >= 0),
  last_attempt_at TEXT,
  last_delivered_at TEXT,
  error TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS network_browser_sessions (
  token_hash TEXT PRIMARY KEY,
  device_id TEXT NOT NULL REFERENCES network_devices(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS documents (
  id TEXT PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  participant_id TEXT REFERENCES participants(id),
  name TEXT NOT NULL,
  media_type TEXT NOT NULL,
  byte_size INTEGER NOT NULL CHECK(byte_size >= 0 AND byte_size <= 4194304),
  sha256 TEXT NOT NULL,
  relative_path TEXT NOT NULL UNIQUE,
	archived_at TEXT,
  created_at TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS messages (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  participant_id TEXT REFERENCES participants(id),
  sender_handle TEXT NOT NULL,
  sender_name TEXT NOT NULL,
  sender_kind TEXT NOT NULL CHECK(sender_kind IN ('owner','member','agent','steward','system')),
  kind TEXT NOT NULL CHECK(kind IN ('message','context','document','system')),
  body TEXT NOT NULL,
  document_id TEXT REFERENCES documents(id),
  created_at TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS messages_room_sequence ON messages(room_id, sequence);
CREATE INDEX IF NOT EXISTS documents_room_created ON documents(room_id, created_at);
CREATE INDEX IF NOT EXISTS participants_room_handle ON participants(room_id, handle);
CREATE INDEX IF NOT EXISTS participant_deliveries_status ON participant_deliveries(kind,status,updated_at);
CREATE INDEX IF NOT EXISTS network_invites_room ON network_invites(room_id,expires_at);
CREATE INDEX IF NOT EXISTS network_bindings_device ON network_participant_bindings(device_id,status,updated_at);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize room database: %w", err)
	}
	if err := s.initializeBoard(ctx); err != nil {
		return fmt.Errorf("initialize board: %w", err)
	}
	if err := s.removeHostedStewardSchema(ctx); err != nil {
		return err
	}
	if err := s.migrateDeliveryProviders(ctx); err != nil {
		return err
	}
	var archivedColumn int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('documents') WHERE name='archived_at'`).Scan(&archivedColumn); err != nil {
		return fmt.Errorf("inspect room database: %w", err)
	}
	if archivedColumn == 0 {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE documents ADD COLUMN archived_at TEXT`); err != nil {
			return fmt.Errorf("add document archive state: %w", err)
		}
	}
	var browserHandleColumn int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('network_devices') WHERE name='browser_handle'`).Scan(&browserHandleColumn); err != nil {
		return fmt.Errorf("inspect network device identity: %w", err)
	}
	if browserHandleColumn == 0 {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE network_devices ADD COLUMN browser_handle TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add network browser identity: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS network_devices_browser_handle ON network_devices(browser_handle) WHERE browser_handle<>''`); err != nil {
		return fmt.Errorf("index network browser identity: %w", err)
	}
	var messagesSchema string
	if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='messages'`).Scan(&messagesSchema); err != nil {
		return fmt.Errorf("inspect message sender kinds: %w", err)
	}
	if !strings.Contains(messagesSchema, "'member'") {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		migration := `
CREATE TABLE messages_member_migration (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  participant_id TEXT REFERENCES participants(id),
  sender_handle TEXT NOT NULL,
  sender_name TEXT NOT NULL,
  sender_kind TEXT NOT NULL CHECK(sender_kind IN ('owner','member','agent','steward','system')),
  kind TEXT NOT NULL CHECK(kind IN ('message','context','document','system')),
  body TEXT NOT NULL,
  document_id TEXT REFERENCES documents(id),
  created_at TEXT NOT NULL
) STRICT;
INSERT INTO messages_member_migration(sequence,id,room_id,participant_id,sender_handle,sender_name,sender_kind,kind,body,document_id,created_at)
SELECT sequence,id,room_id,participant_id,sender_handle,sender_name,sender_kind,kind,body,document_id,created_at FROM messages;
DROP TABLE messages;
ALTER TABLE messages_member_migration RENAME TO messages;
CREATE INDEX messages_room_sequence ON messages(room_id, sequence);`
		if _, err := tx.ExecContext(ctx, migration); err != nil {
			return fmt.Errorf("add human member message identity: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit human member identity: %w", err)
		}
	}
	return nil
}

func (s *Store) removeHostedStewardSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hostedTable int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='hosted_stewards'`).Scan(&hostedTable); err != nil {
		return fmt.Errorf("inspect hosted steward state: %w", err)
	}
	if hostedTable > 0 {
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE participants SET status='left',last_seen_at=? WHERE id IN (SELECT participant_id FROM hosted_stewards)`, now); err != nil {
			return fmt.Errorf("retire hosted steward participants: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE hosted_stewards`); err != nil {
			return fmt.Errorf("remove hosted steward state: %w", err)
		}
	}
	var stewardColumn int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('rooms') WHERE name='steward_id'`).Scan(&stewardColumn); err != nil {
		return fmt.Errorf("inspect room steward state: %w", err)
	}
	if stewardColumn > 0 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE rooms DROP COLUMN steward_id`); err != nil {
			return fmt.Errorf("remove room steward state: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit hosted steward removal: %w", err)
	}
	return nil
}

func (s *Store) migrateDeliveryProviders(ctx context.Context) error {
	var definition string
	if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='participant_deliveries'`).Scan(&definition); err != nil {
		return fmt.Errorf("inspect participant delivery providers: %w", err)
	}
	if strings.Contains(definition, "'claude'") {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const migration = `
CREATE TABLE participant_deliveries_provider_migration (
  participant_id TEXT PRIMARY KEY REFERENCES participants(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK(kind IN ('codex','claude')),
  target TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('bound','queued','delivered','error')),
  last_delivered_sequence INTEGER NOT NULL DEFAULT 0 CHECK(last_delivered_sequence >= 0),
  last_attempt_at TEXT,
  last_delivered_at TEXT,
  error TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
) STRICT;
INSERT INTO participant_deliveries_provider_migration
SELECT * FROM participant_deliveries;
DROP TABLE participant_deliveries;
ALTER TABLE participant_deliveries_provider_migration RENAME TO participant_deliveries;
CREATE INDEX participant_deliveries_status ON participant_deliveries(kind,status,updated_at);`
	if _, err := tx.ExecContext(ctx, migration); err != nil {
		return fmt.Errorf("add Claude delivery provider: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Claude delivery provider: %w", err)
	}
	return nil
}

func (s *Store) CreateRoom(ctx context.Context, input CreateRoomInput) (Snapshot, error) {
	slug := strings.ToLower(strings.TrimSpace(input.Slug))
	if !validSlug(slug) {
		return Snapshot{}, errors.New("room slug must use 1-63 lowercase letters, numbers, or hyphens")
	}
	title, err := boundedText("room title", input.Title, 1, 120)
	if err != nil {
		return Snapshot{}, err
	}
	topic, err := boundedText("room topic", input.Topic, 0, 2048)
	if err != nil {
		return Snapshot{}, err
	}
	roomID, err := randomID("room_")
	if err != nil {
		return Snapshot{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO rooms(id,slug,title,topic,status,created_at,updated_at) VALUES(?,?,?,?, 'open',?,?)`, roomID, slug, title, topic, now, now); err != nil {
		return Snapshot{}, fmt.Errorf("create room: %w", err)
	}
	if _, err := insertMessage(ctx, tx, roomID, nil, "crewfold", "Crewfold", "system", "system", "Room created.", "", now); err != nil {
		return Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	return s.Snapshot(ctx, roomID, 0, 200)
}

func (s *Store) ListRooms(ctx context.Context) ([]Room, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.slug,r.title,r.topic,r.status,r.created_at,r.updated_at,COALESCE(MAX(m.sequence),0)
FROM rooms r LEFT JOIN messages m ON m.room_id=r.id GROUP BY r.id ORDER BY r.status='open' DESC,r.updated_at DESC,r.slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Room{}
	for rows.Next() {
		var room Room
		if err := rows.Scan(&room.ID, &room.Slug, &room.Title, &room.Topic, &room.Status, &room.CreatedAt, &room.UpdatedAt, &room.LastSequence); err != nil {
			return nil, err
		}
		result = append(result, room)
	}
	return result, rows.Err()
}

func (s *Store) Join(ctx context.Context, input JoinInput) (Participant, error) {
	room, err := s.resolveRoom(ctx, input.Room)
	if err != nil {
		return Participant{}, err
	}
	if room.Status != "open" {
		return Participant{}, errors.New("room is archived")
	}
	handle := strings.ToLower(strings.TrimSpace(input.Handle))
	if !validHandle(handle) {
		return Participant{}, errors.New("participant handle must use 1-63 lowercase letters, numbers, dots, underscores, or hyphens")
	}
	if handle == "owner" {
		return Participant{}, errors.New("participant handle @owner is reserved for the room owner")
	}
	display := strings.TrimSpace(input.DisplayName)
	if display == "" {
		display = handle
	}
	display, err = boundedText("display name", display, 1, 120)
	if err != nil {
		return Participant{}, err
	}
	cwd, err := exactDirectory(input.WorkingDirectory)
	if err != nil {
		return Participant{}, err
	}
	kind := input.Kind
	if kind == "" {
		kind = "agent"
	}
	if kind != "agent" && kind != "steward" {
		return Participant{}, errors.New("participant kind must be agent or steward")
	}
	delivery := strings.ToLower(strings.TrimSpace(input.Delivery))
	sessionID := strings.TrimSpace(input.SessionID)
	if delivery != "" && delivery != "none" && delivery != "codex" && delivery != "claude" {
		return Participant{}, errors.New("participant delivery must be codex, claude, or none")
	}
	if delivery == "codex" || delivery == "claude" {
		if sessionID == "" || len(sessionID) > 128 || strings.ContainsAny(sessionID, " \t\r\n") {
			return Participant{}, fmt.Errorf("%s delivery requires one bounded session ID", delivery)
		}
	} else if sessionID != "" {
		return Participant{}, errors.New("session ID is only valid for active delivery")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Participant{}, err
	}
	defer tx.Rollback()
	var humanHandleConflict bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM network_devices d JOIN network_room_memberships n ON n.device_id=d.id
WHERE n.room_id=? AND d.browser_handle=?)`, room.ID, handle).Scan(&humanHandleConflict); err != nil {
		return Participant{}, err
	}
	if humanHandleConflict {
		return Participant{}, errors.New("participant handle belongs to a human collaborator in this room")
	}
	var participantID, existingKind, existingDeviceID string
	err = tx.QueryRowContext(ctx, `SELECT p.id,p.kind,COALESCE(n.device_id,'')
FROM participants p LEFT JOIN network_participant_bindings n ON n.participant_id=p.id
WHERE p.room_id=? AND p.handle=?`, room.ID, handle).Scan(&participantID, &existingKind, &existingDeviceID)
	if errors.Is(err, sql.ErrNoRows) {
		participantID, err = randomID("member_")
		if err != nil {
			return Participant{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO participants(id,room_id,handle,display_name,kind,working_directory,status,joined_at,last_seen_at) VALUES(?,?,?,?,?,?, 'joined',?,?)`, participantID, room.ID, handle, display, kind, cwd, now, now); err != nil {
			return Participant{}, fmt.Errorf("join room: %w", err)
		}
	} else if err != nil {
		return Participant{}, err
	} else {
		if existingDeviceID != "" {
			return Participant{}, errors.New("participant handle belongs to a remote collaborator in this room")
		}
		if existingKind == "steward" {
			kind = "steward"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE participants SET display_name=?,kind=?,working_directory=?,status='joined',last_seen_at=? WHERE id=?`, display, kind, cwd, now, participantID); err != nil {
			return Participant{}, fmt.Errorf("rejoin room: %w", err)
		}
	}
	if delivery == "codex" || delivery == "claude" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO participant_deliveries(participant_id,kind,target,status,updated_at)
VALUES(?,?,?,'bound',?)
ON CONFLICT(participant_id) DO UPDATE SET kind=excluded.kind,target=excluded.target,status='bound',error='',updated_at=excluded.updated_at`, participantID, delivery, sessionID, now); err != nil {
			return Participant{}, fmt.Errorf("bind %s delivery: %w", delivery, err)
		}
	} else if delivery == "none" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM participant_deliveries WHERE participant_id=?`, participantID); err != nil {
			return Participant{}, fmt.Errorf("remove participant delivery: %w", err)
		}
	}
	message := display + " joined from " + cwd + "."
	if kind == "agent" {
		message = display + " joined as an agent of human @owner from " + cwd + "."
	} else if kind == "steward" {
		message = display + " joined as a steward from " + cwd + "."
	}
	if _, err := insertMessage(ctx, tx, room.ID, &participantID, handle, display, kind, "system", message, "", now); err != nil {
		return Participant{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rooms SET updated_at=? WHERE id=?`, now, room.ID); err != nil {
		return Participant{}, err
	}
	if err := tx.Commit(); err != nil {
		return Participant{}, err
	}
	return s.participant(ctx, room.ID, participantID)
}

func (s *Store) Leave(ctx context.Context, input LeaveInput) (Participant, error) {
	room, err := s.resolveRoom(ctx, input.Room)
	if err != nil {
		return Participant{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Participant{}, err
	}
	defer tx.Rollback()
	participant, err := resolveParticipantActorTx(ctx, tx, room.ID, input.ParticipantID, input.WorkingDirectory, input.Handle)
	if err != nil {
		return Participant{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE participants SET status='left',last_seen_at=? WHERE id=?`, now, participant.ID); err != nil {
		return Participant{}, fmt.Errorf("leave room: %w", err)
	}
	if input.ParticipantID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE network_participant_bindings SET status='left',updated_at=? WHERE participant_id=?`, now, participant.ID); err != nil {
			return Participant{}, fmt.Errorf("disable network delivery: %w", err)
		}
	}
	message := participant.DisplayName + " left the room."
	if _, err := insertMessage(ctx, tx, room.ID, &participant.ID, participant.Handle, participant.DisplayName, participant.Kind, "system", message, "", now); err != nil {
		return Participant{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rooms SET updated_at=? WHERE id=?`, now, room.ID); err != nil {
		return Participant{}, err
	}
	if err := tx.Commit(); err != nil {
		return Participant{}, err
	}
	return s.participant(ctx, room.ID, participant.ID)
}

func (s *Store) Send(ctx context.Context, input SendInput) (Message, error) {
	room, err := s.resolveRoom(ctx, input.Room)
	if err != nil {
		return Message{}, err
	}
	if room.Status != "open" {
		return Message{}, errors.New("room is archived")
	}
	body, err := boundedText("message", input.Body, 1, 16384)
	if err != nil {
		return Message{}, err
	}
	kind := input.Kind
	if kind == "" {
		kind = "message"
	}
	if kind != "message" && kind != "context" {
		return Message{}, errors.New("message kind must be message or context")
	}
	if kind == "message" && !input.Owner {
		if err := ValidateSharedMessage(body); err != nil {
			return Message{}, err
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	var participantID *string
	handle, name, senderKind := "owner", "You", "owner"
	if !input.Owner {
		participant, resolveErr := resolveParticipantActorTx(ctx, tx, room.ID, input.ParticipantID, input.WorkingDirectory, input.Handle)
		if resolveErr != nil {
			return Message{}, resolveErr
		}
		participantID = &participant.ID
		handle, name, senderKind = participant.Handle, participant.DisplayName, participant.Kind
		if _, err := tx.ExecContext(ctx, `UPDATE participants SET last_seen_at=? WHERE id=?`, now, participant.ID); err != nil {
			return Message{}, err
		}
		if kind == "context" {
			if _, err := tx.ExecContext(ctx, `UPDATE participants SET context=?,context_updated_at=? WHERE id=?`, body, now, participant.ID); err != nil {
				return Message{}, err
			}
		}
	}
	message, err := insertMessage(ctx, tx, room.ID, participantID, handle, name, senderKind, kind, body, "", now)
	if err != nil {
		return Message{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rooms SET updated_at=? WHERE id=?`, now, room.ID); err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, err
	}
	return message, nil
}

func (s *Store) Upload(ctx context.Context, input UploadInput) (Message, error) {
	room, err := s.resolveRoom(ctx, input.Room)
	if err != nil {
		return Message{}, err
	}
	if room.Status != "open" {
		return Message{}, errors.New("room is archived")
	}
	name := filepath.Base(strings.TrimSpace(input.Name))
	if name == "." || name == "" || name != strings.TrimSpace(input.Name) || len(name) > 160 || !utf8.ValidString(name) {
		return Message{}, errors.New("document name must be one bounded file name")
	}
	content, err := base64.StdEncoding.DecodeString(input.ContentBase64)
	if err != nil || len(content) > maximumDocumentBytes {
		return Message{}, fmt.Errorf("document content must be valid base64 no larger than %d bytes", maximumDocumentBytes)
	}
	mediaType := strings.TrimSpace(input.MediaType)
	if mediaType == "" {
		mediaType = mime.TypeByExtension(filepath.Ext(name))
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	if len(mediaType) > 128 {
		return Message{}, errors.New("document media type is too long")
	}
	caption, err := boundedText("document caption", input.Caption, 0, 2048)
	if err != nil {
		return Message{}, err
	}
	if caption == "" {
		caption = "Shared " + name
	}
	if err := ValidateSharedMessage(caption); err != nil {
		return Message{}, fmt.Errorf("document caption: %w", err)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	documentID, err := randomID("doc_")
	if err != nil {
		return Message{}, err
	}
	digest := sha256.Sum256(content)
	relative := filepath.Join("rooms", room.ID, "documents", documentID, name)
	absolute := filepath.Join(s.dataDir, relative)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return Message{}, err
	}
	temporary := absolute + ".partial"
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return Message{}, err
	}
	if err := os.Rename(temporary, absolute); err != nil {
		_ = os.Remove(temporary)
		return Message{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		_ = os.Remove(absolute)
		return Message{}, err
	}
	defer tx.Rollback()
	var participantID *string
	handle, display, senderKind := "owner", "You", "owner"
	if !input.Owner {
		participant, resolveErr := resolveParticipantActorTx(ctx, tx, room.ID, input.ParticipantID, input.WorkingDirectory, input.Handle)
		if resolveErr != nil {
			_ = os.Remove(absolute)
			return Message{}, resolveErr
		}
		participantID = &participant.ID
		handle, display, senderKind = participant.Handle, participant.DisplayName, participant.Kind
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO documents(id,room_id,participant_id,name,media_type,byte_size,sha256,relative_path,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, documentID, room.ID, participantID, name, mediaType, len(content), hex.EncodeToString(digest[:]), relative, now); err != nil {
		_ = os.Remove(absolute)
		return Message{}, err
	}
	message, err := insertMessage(ctx, tx, room.ID, participantID, handle, display, senderKind, "document", caption, documentID, now)
	if err != nil {
		_ = os.Remove(absolute)
		return Message{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rooms SET updated_at=? WHERE id=?`, now, room.ID); err != nil {
		_ = os.Remove(absolute)
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = os.Remove(absolute)
		return Message{}, err
	}
	message.Document = &Document{ID: documentID, RoomID: room.ID, Name: name, MediaType: mediaType, ByteSize: int64(len(content)), SHA256: hex.EncodeToString(digest[:]), CreatedAt: now}
	if participantID != nil {
		message.Document.ParticipantID = *participantID
	}
	return message, nil
}

func (s *Store) Ack(ctx context.Context, input AckInput) (Participant, error) {
	room, err := s.resolveRoom(ctx, input.Room)
	if err != nil {
		return Participant{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Participant{}, err
	}
	defer tx.Rollback()
	participant, err := resolveParticipantActorTx(ctx, tx, room.ID, input.ParticipantID, input.WorkingDirectory, input.Handle)
	if err != nil {
		return Participant{}, err
	}
	through := input.Through
	if through < 0 {
		return Participant{}, errors.New("acknowledgement sequence cannot be negative")
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM messages WHERE room_id=?`, room.ID).Scan(&current); err != nil {
		return Participant{}, err
	}
	if through == 0 || through > current {
		through = current
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE participants SET last_read_sequence=MAX(last_read_sequence,?),last_seen_at=? WHERE id=?`, through, now, participant.ID); err != nil {
		return Participant{}, err
	}
	if err := tx.Commit(); err != nil {
		return Participant{}, err
	}
	return s.participant(ctx, room.ID, participant.ID)
}

func (s *Store) Snapshot(ctx context.Context, roomIdentifier string, after int64, limit int) (Snapshot, error) {
	return s.snapshot(ctx, roomIdentifier, after, 0, 0, limit)
}

func (s *Store) SnapshotRange(ctx context.Context, roomIdentifier string, after, through int64) (Snapshot, error) {
	if after < 0 || through <= after {
		return Snapshot{}, errors.New("invalid delivery range")
	}
	return s.snapshot(ctx, roomIdentifier, after, 0, through, 500)
}

func (s *Store) SnapshotBefore(ctx context.Context, roomIdentifier string, before int64, limit int) (Snapshot, error) {
	if before <= 0 {
		return Snapshot{}, errors.New("before must be greater than zero")
	}
	return s.snapshot(ctx, roomIdentifier, 0, before, 0, limit)
}

func (s *Store) snapshot(ctx context.Context, roomIdentifier string, after, before, through int64, limit int) (Snapshot, error) {
	room, err := s.resolveRoom(ctx, roomIdentifier)
	if err != nil {
		return Snapshot{}, err
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	participants, err := s.participants(ctx, room.ID)
	if err != nil {
		return Snapshot{}, err
	}
	var messages []Message
	if through > 0 {
		if through > room.LastSequence {
			return Snapshot{}, errors.New("delivery range exceeds room history")
		}
		messages, err = s.messagesRange(ctx, room.ID, after, through, 501)
		if len(messages) > 500 {
			return Snapshot{}, errors.New("delivery range exceeds 500 events; split the range before acknowledging")
		}
	} else if before > 0 {
		messages, err = s.messagesBefore(ctx, room.ID, before, limit)
	} else if after == 0 {
		messages, err = s.messagesBefore(ctx, room.ID, room.LastSequence+1, limit)
	} else {
		messages, err = s.messages(ctx, room.ID, after, limit)
	}
	if err != nil {
		return Snapshot{}, err
	}
	documents, err := s.documents(ctx, room.ID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM messages WHERE room_id=?`, room.ID).Scan(&room.LastSequence); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Room: room, Participants: participants, Messages: messages, Documents: documents}, nil
}

func (s *Store) ReadDocument(ctx context.Context, roomIdentifier, documentIdentifier string) (Document, []byte, error) {
	room, err := s.resolveRoom(ctx, roomIdentifier)
	if err != nil {
		return Document{}, nil, err
	}
	var document Document
	var relative string
	scan := func(row *sql.Row) error {
		return row.Scan(&document.ID, &document.RoomID, &document.ParticipantID, &document.Name, &document.MediaType, &document.ByteSize, &document.SHA256, &relative, &document.ArchivedAt, &document.CreatedAt)
	}
	columns := `SELECT id,room_id,COALESCE(participant_id,''),name,media_type,byte_size,sha256,relative_path,COALESCE(archived_at,''),created_at FROM documents`
	// An exact document ID addresses one immutable historical revision.
	err = scan(s.db.QueryRowContext(ctx, columns+` WHERE room_id=? AND id=?`, room.ID, documentIdentifier))
	if errors.Is(err, sql.ErrNoRows) {
		var publishers int
		if countErr := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT COALESCE(participant_id,'')) FROM documents WHERE room_id=? AND name=?`, room.ID, documentIdentifier).Scan(&publishers); countErr != nil {
			return Document{}, nil, countErr
		}
		if publishers > 1 {
			return Document{}, nil, errors.New("document filename is ambiguous across participants; use an exact document ID")
		}
		// Within one publisher, a filename addresses the newest revision of the
		// same logical document.
		err = scan(s.db.QueryRowContext(ctx, columns+` WHERE room_id=? AND name=? ORDER BY created_at DESC,id DESC LIMIT 1`, room.ID, documentIdentifier))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, nil, errors.New("document not found")
	}
	if err != nil {
		return Document{}, nil, err
	}
	absolute := filepath.Join(s.dataDir, relative)
	content, err := os.ReadFile(absolute)
	if err != nil {
		return Document{}, nil, err
	}
	digest := sha256.Sum256(content)
	if int64(len(content)) != document.ByteSize || hex.EncodeToString(digest[:]) != document.SHA256 {
		return Document{}, nil, errors.New("document content does not match its recorded identity")
	}
	return document, content, nil
}

func (s *Store) SetDocumentArchived(ctx context.Context, input ArchiveDocumentInput, archived bool) (Document, error) {
	room, err := s.resolveRoom(ctx, input.Room)
	if err != nil {
		return Document{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback()

	var actor Participant
	if !input.Owner {
		actor, err = resolveParticipantActorTx(ctx, tx, room.ID, input.ParticipantID, input.WorkingDirectory, input.Handle)
		if err != nil {
			return Document{}, err
		}
	}

	document, err := resolveDocumentTx(ctx, tx, room.ID, input.Document)
	if err != nil {
		return Document{}, err
	}
	if !input.Owner && actor.Kind != "steward" && document.ParticipantID != actor.ID {
		return Document{}, errors.New("participants may archive only their own documents")
	}

	now := s.now().UTC().Format(time.RFC3339Nano)
	var archivedAt any
	if archived {
		archivedAt = now
	}
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET archived_at=? WHERE room_id=? AND name=? AND COALESCE(participant_id,'')=?`, archivedAt, room.ID, document.Name, document.ParticipantID); err != nil {
		return Document{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rooms SET updated_at=? WHERE id=?`, now, room.ID); err != nil {
		return Document{}, err
	}
	if err := tx.Commit(); err != nil {
		return Document{}, err
	}
	if archived {
		document.ArchivedAt = now
	} else {
		document.ArchivedAt = ""
	}
	return document, nil
}

func (s *Store) Archive(ctx context.Context, roomIdentifier string) (Room, error) {
	room, err := s.resolveRoom(ctx, roomIdentifier)
	if err != nil {
		return Room{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `UPDATE rooms SET status='archived',updated_at=? WHERE id=?`, now, room.ID); err != nil {
		return Room{}, err
	}
	return s.resolveRoom(ctx, room.ID)
}

func (s *Store) resolveRoom(ctx context.Context, identifier string) (Room, error) {
	identifier = strings.TrimSpace(identifier)
	var room Room
	err := s.db.QueryRowContext(ctx, `SELECT r.id,r.slug,r.title,r.topic,r.status,r.created_at,r.updated_at,COALESCE((SELECT MAX(sequence) FROM messages WHERE room_id=r.id),0) FROM rooms r WHERE r.id=? OR r.slug=?`, identifier, identifier).Scan(&room.ID, &room.Slug, &room.Title, &room.Topic, &room.Status, &room.CreatedAt, &room.UpdatedAt, &room.LastSequence)
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, errors.New("room not found")
	}
	return room, err
}

func (s *Store) participants(ctx context.Context, roomID string) ([]Participant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.room_id,p.handle,p.display_name,p.kind,
CASE WHEN p.kind='agent' AND n.participant_id IS NULL THEN 'owner' ELSE COALESCE(nd.browser_handle,'') END,
CASE WHEN n.participant_id IS NOT NULL THEN COALESCE(nd.name,'remote') || ':' || n.remote_directory ELSE COALESCE(p.working_directory,'') END,
p.status,p.context,COALESCE(p.context_updated_at,''),p.last_read_sequence,p.joined_at,p.last_seen_at,
(SELECT COUNT(*) FROM messages m WHERE m.room_id=p.room_id AND m.sequence>p.last_read_sequence AND (m.participant_id IS NULL OR m.participant_id<>p.id)),
COALESCE(d.kind,CASE WHEN n.provider<>'none' THEN n.provider ELSE '' END,''),
COALESCE(d.target,n.device_id,''),COALESCE(d.status,CASE WHEN n.provider<>'none' THEN n.status ELSE '' END,''),
COALESCE(d.last_delivered_sequence,n.last_delivered_sequence,0),COALESCE(d.last_attempt_at,n.last_attempt_at,''),
COALESCE(d.last_delivered_at,n.last_delivered_at,''),COALESCE(d.error,n.error,''),COALESCE(d.updated_at,n.updated_at,'')
FROM participants p
LEFT JOIN participant_deliveries d ON d.participant_id=p.id
LEFT JOIN network_participant_bindings n ON n.participant_id=p.id
LEFT JOIN network_devices nd ON nd.id=n.device_id
WHERE p.room_id=? ORDER BY p.kind='steward' DESC,p.joined_at,p.handle`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Participant{}
	for rows.Next() {
		var participant Participant
		var delivery ParticipantDelivery
		if err := rows.Scan(&participant.ID, &participant.RoomID, &participant.Handle, &participant.DisplayName, &participant.Kind, &participant.HumanHandle, &participant.WorkingDirectory, &participant.Status, &participant.Context, &participant.ContextUpdatedAt, &participant.LastReadSequence, &participant.JoinedAt, &participant.LastSeenAt, &participant.UnreadCount, &delivery.Kind, &delivery.Target, &delivery.Status, &delivery.LastDeliveredSequence, &delivery.LastAttemptAt, &delivery.LastDeliveredAt, &delivery.Error, &delivery.UpdatedAt); err != nil {
			return nil, err
		}
		if delivery.Kind != "" {
			participant.Delivery = &delivery
		}
		result = append(result, participant)
	}
	return result, rows.Err()
}

func (s *Store) participant(ctx context.Context, roomID, participantID string) (Participant, error) {
	participants, err := s.participants(ctx, roomID)
	if err != nil {
		return Participant{}, err
	}
	for _, participant := range participants {
		if participant.ID == participantID {
			return participant, nil
		}
	}
	return Participant{}, errors.New("participant not found")
}

type deliveryRoute struct {
	Room        Room
	Participant Participant
	Delivery    ParticipantDelivery
}

func (s *Store) pendingDeliveries(ctx context.Context) ([]deliveryRoute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.slug,r.title,r.topic,r.status,r.created_at,r.updated_at,
COALESCE((SELECT MAX(sequence) FROM messages WHERE room_id=r.id),0),
p.id,p.room_id,p.handle,p.display_name,p.kind,'owner',COALESCE(p.working_directory,''),p.status,p.context,COALESCE(p.context_updated_at,''),p.last_read_sequence,p.joined_at,p.last_seen_at,
d.kind,d.target,d.status,MAX(d.last_delivered_sequence,p.last_read_sequence),COALESCE(d.last_attempt_at,''),COALESCE(d.last_delivered_at,''),d.error,d.updated_at
FROM participant_deliveries d
JOIN participants p ON p.id=d.participant_id
JOIN rooms r ON r.id=p.room_id
WHERE d.kind IN ('codex','claude') AND p.status='joined' AND r.status='open'
AND EXISTS(SELECT 1 FROM messages m WHERE m.room_id=p.room_id AND m.sequence>MAX(d.last_delivered_sequence,p.last_read_sequence))
ORDER BY d.updated_at,p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var routes []deliveryRoute
	for rows.Next() {
		var route deliveryRoute
		if err := rows.Scan(
			&route.Room.ID, &route.Room.Slug, &route.Room.Title, &route.Room.Topic, &route.Room.Status, &route.Room.CreatedAt, &route.Room.UpdatedAt, &route.Room.LastSequence,
			&route.Participant.ID, &route.Participant.RoomID, &route.Participant.Handle, &route.Participant.DisplayName, &route.Participant.Kind, &route.Participant.HumanHandle, &route.Participant.WorkingDirectory, &route.Participant.Status, &route.Participant.Context, &route.Participant.ContextUpdatedAt, &route.Participant.LastReadSequence, &route.Participant.JoinedAt, &route.Participant.LastSeenAt,
			&route.Delivery.Kind, &route.Delivery.Target, &route.Delivery.Status, &route.Delivery.LastDeliveredSequence, &route.Delivery.LastAttemptAt, &route.Delivery.LastDeliveredAt, &route.Delivery.Error, &route.Delivery.UpdatedAt,
		); err != nil {
			return nil, err
		}
		route.Participant.Delivery = &route.Delivery
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

func (s *Store) recordDeliveryAttempt(ctx context.Context, participantID, status, detail string) error {
	if status != "queued" && status != "error" {
		return errors.New("delivery attempt status must be queued or error")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE participant_deliveries SET status=?,last_attempt_at=?,error=?,updated_at=? WHERE participant_id=?`, status, now, detail, now, participantID)
	return err
}

func (s *Store) advanceDelivery(ctx context.Context, participantID string, sequence int64) error {
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE participant_deliveries
SET status='delivered',last_delivered_sequence=MAX(last_delivered_sequence,?),last_attempt_at=?,last_delivered_at=?,error='',updated_at=?
WHERE participant_id=?`, sequence, now, now, now, participantID)
	return err
}

func (s *Store) messages(ctx context.Context, roomID string, after int64, limit int) ([]Message, error) {
	return s.messagesRange(ctx, roomID, after, 9223372036854775807, limit)
}

func (s *Store) messagesRange(ctx context.Context, roomID string, after, through int64, limit int) ([]Message, error) {
	query := `SELECT m.sequence,m.id,m.room_id,COALESCE(m.participant_id,''),m.sender_handle,m.sender_name,m.sender_kind,m.kind,m.body,COALESCE(m.document_id,''),m.created_at,
COALESCE(d.name,''),COALESCE(d.media_type,''),COALESCE(d.byte_size,0),COALESCE(d.sha256,''),COALESCE(d.archived_at,''),COALESCE(d.created_at,'')
	FROM messages m LEFT JOIN documents d ON d.id=m.document_id WHERE m.room_id=? AND m.sequence>? AND m.sequence<=? ORDER BY m.sequence LIMIT ?`
	return s.queryMessages(ctx, query, roomID, after, through, limit)
}

func (s *Store) messagesBefore(ctx context.Context, roomID string, before int64, limit int) ([]Message, error) {
	query := `WITH selected AS (SELECT * FROM messages WHERE room_id=? AND sequence<? ORDER BY sequence DESC LIMIT ?)
		SELECT m.sequence,m.id,m.room_id,COALESCE(m.participant_id,''),m.sender_handle,m.sender_name,m.sender_kind,m.kind,m.body,COALESCE(m.document_id,''),m.created_at,
		COALESCE(d.name,''),COALESCE(d.media_type,''),COALESCE(d.byte_size,0),COALESCE(d.sha256,''),COALESCE(d.archived_at,''),COALESCE(d.created_at,'')
		FROM selected m LEFT JOIN documents d ON d.id=m.document_id ORDER BY m.sequence`
	return s.queryMessages(ctx, query, roomID, before, limit)
}

func (s *Store) queryMessages(ctx context.Context, query string, arguments ...any) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Message{}
	for rows.Next() {
		var message Message
		var documentID, documentName, documentMedia, documentSHA, documentArchived, documentCreated string
		var documentBytes int64
		if err := rows.Scan(&message.Sequence, &message.ID, &message.RoomID, &message.ParticipantID, &message.SenderHandle, &message.SenderName, &message.SenderKind, &message.Kind, &message.Body, &documentID, &message.CreatedAt, &documentName, &documentMedia, &documentBytes, &documentSHA, &documentArchived, &documentCreated); err != nil {
			return nil, err
		}
		if documentID != "" {
			message.Document = &Document{ID: documentID, RoomID: message.RoomID, ParticipantID: message.ParticipantID, Name: documentName, MediaType: documentMedia, ByteSize: documentBytes, SHA256: documentSHA, ArchivedAt: documentArchived, CreatedAt: documentCreated}
		}
		result = append(result, message)
	}
	return result, rows.Err()
}

func (s *Store) documents(ctx context.Context, roomID string) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,room_id,COALESCE(participant_id,''),name,media_type,byte_size,sha256,COALESCE(archived_at,''),created_at FROM documents WHERE room_id=? ORDER BY created_at DESC,id DESC`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Document{}
	for rows.Next() {
		var document Document
		if err := rows.Scan(&document.ID, &document.RoomID, &document.ParticipantID, &document.Name, &document.MediaType, &document.ByteSize, &document.SHA256, &document.ArchivedAt, &document.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, document)
	}
	return result, rows.Err()
}

func resolveDocumentTx(ctx context.Context, tx *sql.Tx, roomID, identifier string) (Document, error) {
	var document Document
	scan := func(row *sql.Row) error {
		return row.Scan(&document.ID, &document.RoomID, &document.ParticipantID, &document.Name, &document.MediaType, &document.ByteSize, &document.SHA256, &document.ArchivedAt, &document.CreatedAt)
	}
	columns := `SELECT id,room_id,COALESCE(participant_id,''),name,media_type,byte_size,sha256,COALESCE(archived_at,''),created_at FROM documents`
	err := scan(tx.QueryRowContext(ctx, columns+` WHERE room_id=? AND id=?`, roomID, identifier))
	if errors.Is(err, sql.ErrNoRows) {
		var publishers int
		if countErr := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT COALESCE(participant_id,'')) FROM documents WHERE room_id=? AND name=?`, roomID, identifier).Scan(&publishers); countErr != nil {
			return Document{}, countErr
		}
		if publishers > 1 {
			return Document{}, errors.New("document filename is ambiguous across participants; use an exact document ID")
		}
		err = scan(tx.QueryRowContext(ctx, columns+` WHERE room_id=? AND name=? ORDER BY created_at DESC,id DESC LIMIT 1`, roomID, identifier))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, errors.New("document not found")
	}
	return document, err
}

func resolveParticipantTx(ctx context.Context, tx *sql.Tx, roomID, cwd, handle string) (Participant, error) {
	var participant Participant
	var err error
	if strings.TrimSpace(handle) != "" {
		cwd, err = exactDirectory(cwd)
		if err != nil {
			return Participant{}, err
		}
		err = tx.QueryRowContext(ctx, `SELECT id,room_id,handle,display_name,kind,COALESCE(working_directory,''),status FROM participants WHERE room_id=? AND handle=? AND working_directory=? AND status='joined'`, roomID, strings.ToLower(strings.TrimSpace(handle)), cwd).Scan(&participant.ID, &participant.RoomID, &participant.Handle, &participant.DisplayName, &participant.Kind, &participant.WorkingDirectory, &participant.Status)
	} else {
		cwd, err = exactDirectory(cwd)
		if err != nil {
			return Participant{}, err
		}
		err = tx.QueryRowContext(ctx, `SELECT id,room_id,handle,display_name,kind,COALESCE(working_directory,''),status FROM participants WHERE room_id=? AND working_directory=? AND status='joined'`, roomID, cwd).Scan(&participant.ID, &participant.RoomID, &participant.Handle, &participant.DisplayName, &participant.Kind, &participant.WorkingDirectory, &participant.Status)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Participant{}, errors.New("this session has not joined the room; run crewfold room join first")
	}
	return participant, err
}

func resolveParticipantActorTx(ctx context.Context, tx *sql.Tx, roomID, participantID, cwd, handle string) (Participant, error) {
	if strings.TrimSpace(participantID) == "" {
		return resolveParticipantTx(ctx, tx, roomID, cwd, handle)
	}
	var participant Participant
	err := tx.QueryRowContext(ctx, `SELECT id,room_id,handle,display_name,kind,COALESCE(working_directory,''),status
FROM participants WHERE id=? AND room_id=? AND status='joined'`, participantID, roomID).Scan(
		&participant.ID, &participant.RoomID, &participant.Handle, &participant.DisplayName,
		&participant.Kind, &participant.WorkingDirectory, &participant.Status,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Participant{}, errors.New("this session has not joined the room; run crewfold room join first")
	}
	return participant, err
}

func insertMessage(ctx context.Context, tx *sql.Tx, roomID string, participantID *string, handle, name, senderKind, kind, body, documentID, now string) (Message, error) {
	id, err := randomID("msg_")
	if err != nil {
		return Message{}, err
	}
	var participant any
	if participantID != nil {
		participant = *participantID
	}
	var document any
	if documentID != "" {
		document = documentID
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO messages(id,room_id,participant_id,sender_handle,sender_name,sender_kind,kind,body,document_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, roomID, participant, handle, name, senderKind, kind, body, document, now)
	if err != nil {
		return Message{}, err
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return Message{}, err
	}
	participantValue := ""
	if participantID != nil {
		participantValue = *participantID
	}
	return Message{Sequence: sequence, ID: id, RoomID: roomID, ParticipantID: participantValue, SenderHandle: handle, SenderName: name, SenderKind: senderKind, Kind: kind, Body: body, CreatedAt: now}, nil
}

func exactDirectory(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("working directory is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", errors.New("working directory must be an existing directory")
	}
	return absolute, nil
}

func boundedText(name, value string, minimum, maximum int) (string, error) {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	if len(value) < minimum || len(value) > maximum || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s must be between %d and %d bytes", name, minimum, maximum)
	}
	return value, nil
}

func validSlug(value string) bool {
	if len(value) < 1 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, current := range value {
		if (current < 'a' || current > 'z') && (current < '0' || current > '9') && current != '-' {
			return false
		}
	}
	return true
}

func validHandle(value string) bool {
	if len(value) < 1 || len(value) > 63 {
		return false
	}
	for _, current := range value {
		if (current < 'a' || current > 'z') && (current < '0' || current > '9') && current != '-' && current != '_' && current != '.' {
			return false
		}
	}
	return true
}

func randomID(prefix string) (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buffer), nil
}
