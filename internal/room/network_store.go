package room

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	defaultNetworkInviteLifetime = 24 * time.Hour
	maximumNetworkInviteLifetime = 30 * 24 * time.Hour
)

func normalizePublicOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("public origin must be one HTTPS origin, for example https://crewfold.example.ts.net")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("public origin cannot contain a path")
	}
	parsed.Path = ""
	return parsed.String(), nil
}

func (s *Store) ConfigureNetwork(ctx context.Context, input ConfigureNetworkInput) (NetworkSettings, error) {
	origin := ""
	var err error
	if input.Enabled {
		origin, err = normalizePublicOrigin(input.PublicOrigin)
		if err != nil {
			return NetworkSettings{}, err
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	enabled := 0
	if input.Enabled {
		enabled = 1
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO network_settings(singleton,enabled,public_origin,updated_at) VALUES(1,?,?,?)
ON CONFLICT(singleton) DO UPDATE SET enabled=excluded.enabled,public_origin=excluded.public_origin,updated_at=excluded.updated_at`, enabled, origin, now); err != nil {
		return NetworkSettings{}, err
	}
	return NetworkSettings{Enabled: input.Enabled, PublicOrigin: origin, UpdatedAt: now}, nil
}

func (s *Store) NetworkSettings(ctx context.Context) (NetworkSettings, error) {
	var result NetworkSettings
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT enabled,public_origin,updated_at FROM network_settings WHERE singleton=1`).Scan(&enabled, &result.PublicOrigin, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	result.Enabled = enabled == 1
	return result, err
}

func (s *Store) CreateNetworkInvite(ctx context.Context, input CreateNetworkInviteInput) (NetworkInvite, error) {
	settings, err := s.NetworkSettings(ctx)
	if err != nil {
		return NetworkInvite{}, err
	}
	if !settings.Enabled {
		return NetworkInvite{}, errors.New("Crewfold networking is disabled; run crewfold network enable first")
	}
	room, err := s.resolveRoom(ctx, input.Room)
	if err != nil {
		return NetworkInvite{}, err
	}
	if room.Status != "open" {
		return NetworkInvite{}, errors.New("room is archived")
	}
	lifetime := defaultNetworkInviteLifetime
	if input.ExpiresSeconds != 0 {
		lifetime = time.Duration(input.ExpiresSeconds) * time.Second
	}
	if lifetime < time.Minute || lifetime > maximumNetworkInviteLifetime {
		return NetworkInvite{}, errors.New("invite lifetime must be between 1 minute and 30 days")
	}
	id, err := randomID("invite_")
	if err != nil {
		return NetworkInvite{}, err
	}
	secretValue, digest, err := secret()
	if err != nil {
		return NetworkInvite{}, err
	}
	now := s.now().UTC()
	expires := now.Add(lifetime)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO network_invites(id,room_id,secret_hash,expires_at,created_at) VALUES(?,?,?,?,?)`, id, room.ID, hex.EncodeToString(digest[:]), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return NetworkInvite{}, err
	}
	return NetworkInvite{ID: id, RoomID: room.ID, RoomSlug: room.Slug, Secret: secretValue, PublicOrigin: settings.PublicOrigin, ExpiresAt: expires.Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano)}, nil
}

func networkSecretDigest(value string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(raw) != 32 {
		return "", errors.New("network credential is invalid")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func consumeNetworkInviteTx(ctx context.Context, tx *sql.Tx, secretHash, deviceID, now string) (Room, error) {
	var room Room
	var expiresAt, usedAt string
	err := tx.QueryRowContext(ctx, `SELECT r.id,r.slug,r.title,r.topic,r.status,r.created_at,r.updated_at,
COALESCE((SELECT MAX(sequence) FROM messages WHERE room_id=r.id),0),i.expires_at,COALESCE(i.used_at,'')
FROM network_invites i JOIN rooms r ON r.id=i.room_id WHERE i.secret_hash=?`, secretHash).Scan(
		&room.ID, &room.Slug, &room.Title, &room.Topic, &room.Status,
		&room.CreatedAt, &room.UpdatedAt, &room.LastSequence, &expiresAt, &usedAt,
	)
	nowTime, nowErr := time.Parse(time.RFC3339Nano, now)
	expiresTime, expiresErr := time.Parse(time.RFC3339Nano, expiresAt)
	if errors.Is(err, sql.ErrNoRows) || usedAt != "" || nowErr != nil || expiresErr != nil || !nowTime.Before(expiresTime) {
		return Room{}, errors.New("network invite is invalid, expired, or already used")
	}
	if err != nil {
		return Room{}, err
	}
	if room.Status != "open" {
		return Room{}, errors.New("room is archived")
	}
	var browserHandle string
	if err := tx.QueryRowContext(ctx, `SELECT browser_handle FROM network_devices WHERE id=?`, deviceID).Scan(&browserHandle); err != nil {
		return Room{}, err
	}
	if browserHandle != "" {
		var conflict bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM participants WHERE room_id=? AND handle=?)`, room.ID, browserHandle).Scan(&conflict); err != nil {
			return Room{}, err
		}
		if conflict {
			return Room{}, errors.New("human collaborator handle conflicts with an agent in this room")
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE network_invites SET used_by_device_id=?,used_at=? WHERE secret_hash=? AND used_at IS NULL`, deviceID, now, secretHash)
	if err != nil {
		return Room{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Room{}, errors.New("network invite was already used")
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO network_room_memberships(device_id,room_id,created_at) VALUES(?,?,?)`, deviceID, room.ID, now); err != nil {
		return Room{}, err
	}
	return room, nil
}

func (s *Store) EnrollNetworkDevice(ctx context.Context, input NetworkEnrollInput) (NetworkEnrollment, error) {
	name, err := boundedText("device name", input.DeviceName, 1, 120)
	if err != nil {
		return NetworkEnrollment{}, err
	}
	inviteHash, err := networkSecretDigest(input.Invite)
	if err != nil {
		return NetworkEnrollment{}, err
	}
	deviceID, err := randomID("device_")
	if err != nil {
		return NetworkEnrollment{}, err
	}
	token, tokenDigest, err := secret()
	if err != nil {
		return NetworkEnrollment{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NetworkEnrollment{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO network_devices(id,name,token_hash,joined_at,last_seen_at) VALUES(?,?,?,?,?)`, deviceID, name, hex.EncodeToString(tokenDigest[:]), now, now); err != nil {
		return NetworkEnrollment{}, err
	}
	room, err := consumeNetworkInviteTx(ctx, tx, inviteHash, deviceID, now)
	if err != nil {
		return NetworkEnrollment{}, err
	}
	if err := tx.Commit(); err != nil {
		return NetworkEnrollment{}, err
	}
	return NetworkEnrollment{DeviceID: deviceID, DeviceName: name, Token: token, Room: room}, nil
}

func (s *Store) AuthenticateNetworkDevice(ctx context.Context, token string) (NetworkDevice, error) {
	digest, err := networkSecretDigest(token)
	if err != nil {
		return NetworkDevice{}, errors.New("unauthorized")
	}
	var device NetworkDevice
	var storedHash string
	err = s.db.QueryRowContext(ctx, `SELECT id,name,token_hash,browser_handle,joined_at,last_seen_at FROM network_devices WHERE token_hash=?`, digest).Scan(&device.ID, &device.Name, &storedHash, &device.BrowserHandle, &device.JoinedAt, &device.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return NetworkDevice{}, errors.New("unauthorized")
	}
	if err != nil || subtle.ConstantTimeCompare([]byte(digest), []byte(storedHash)) != 1 {
		return NetworkDevice{}, errors.New("unauthorized")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, _ = s.db.ExecContext(ctx, `UPDATE network_devices SET last_seen_at=? WHERE id=?`, now, device.ID)
	device.LastSeen = now
	return device, nil
}

func (s *Store) CreateNetworkBrowserSession(ctx context.Context, deviceID string) (string, error) {
	token, digest, err := secret()
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO network_browser_sessions(token_hash,device_id,created_at,last_seen_at)
VALUES(?,?,?,?)`, hex.EncodeToString(digest[:]), deviceID, now, now); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) AuthenticateNetworkBrowserSession(ctx context.Context, token string) (NetworkDevice, error) {
	digest, err := networkSecretDigest(token)
	if err != nil {
		return NetworkDevice{}, errors.New("unauthorized")
	}
	var device NetworkDevice
	var storedHash string
	err = s.db.QueryRowContext(ctx, `SELECT d.id,d.name,d.browser_handle,d.joined_at,d.last_seen_at,s.token_hash
FROM network_browser_sessions s JOIN network_devices d ON d.id=s.device_id WHERE s.token_hash=?`, digest).Scan(
		&device.ID, &device.Name, &device.BrowserHandle, &device.JoinedAt, &device.LastSeen, &storedHash,
	)
	if errors.Is(err, sql.ErrNoRows) || err != nil || subtle.ConstantTimeCompare([]byte(digest), []byte(storedHash)) != 1 {
		return NetworkDevice{}, errors.New("unauthorized")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, _ = s.db.ExecContext(ctx, `UPDATE network_browser_sessions SET last_seen_at=? WHERE token_hash=?`, now, digest)
	_, _ = s.db.ExecContext(ctx, `UPDATE network_devices SET last_seen_at=? WHERE id=?`, now, device.ID)
	device.LastSeen = now
	return device, nil
}

func suggestedBrowserHandle(deviceID string) string {
	adjectives := [...]string{"amber", "brisk", "calm", "clear", "cobalt", "gentle", "lively", "quiet", "silver", "steady", "sunny", "vivid"}
	nouns := [...]string{"badger", "finch", "fox", "heron", "lynx", "otter", "raven", "wren", "yak", "zebra"}
	digest := sha256.Sum256([]byte(deviceID))
	return fmt.Sprintf("%s-%s-%02x", adjectives[int(digest[0])%len(adjectives)], nouns[int(digest[1])%len(nouns)], digest[2])
}

func (s *Store) NetworkBrowserIdentity(ctx context.Context, deviceID string) (NetworkBrowserIdentity, error) {
	var handle string
	if err := s.db.QueryRowContext(ctx, `SELECT browser_handle FROM network_devices WHERE id=?`, deviceID).Scan(&handle); err != nil {
		return NetworkBrowserIdentity{}, err
	}
	identity := NetworkBrowserIdentity{Handle: handle}
	if handle == "" {
		identity.SuggestedHandle = suggestedBrowserHandle(deviceID)
	}
	return identity, nil
}

func (s *Store) SetNetworkBrowserIdentity(ctx context.Context, deviceID, requestedHandle string) (NetworkBrowserIdentity, error) {
	handle := strings.ToLower(strings.TrimSpace(requestedHandle))
	if !validHandle(handle) {
		return NetworkBrowserIdentity{}, errors.New("member handle must use 1-63 lowercase letters, numbers, dots, underscores, or hyphens")
	}
	if handle == "owner" {
		return NetworkBrowserIdentity{}, errors.New("member handle @owner is reserved for the Crewfold owner")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NetworkBrowserIdentity{}, err
	}
	defer tx.Rollback()
	var conflict bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM participants p JOIN network_room_memberships n ON n.room_id=p.room_id
WHERE n.device_id=? AND p.handle=?)`, deviceID, handle).Scan(&conflict); err != nil {
		return NetworkBrowserIdentity{}, err
	}
	if conflict {
		return NetworkBrowserIdentity{}, errors.New("that member handle is already used by an agent in one of this device's rooms")
	}
	result, err := tx.ExecContext(ctx, `UPDATE network_devices SET browser_handle=? WHERE id=? AND browser_handle=''`, handle, deviceID)
	if err != nil {
		return NetworkBrowserIdentity{}, errors.New("that member handle is already used by another collaborator")
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return NetworkBrowserIdentity{}, errors.New("browser identity is already configured for this device")
	}
	type roomIdentity struct {
		id     string
		status string
		agents []string
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.status,COALESCE(p.handle,'')
FROM network_room_memberships m
JOIN rooms r ON r.id=m.room_id
LEFT JOIN network_participant_bindings b ON b.device_id=m.device_id
LEFT JOIN participants p ON p.id=b.participant_id AND p.room_id=m.room_id AND p.status='joined'
WHERE m.device_id=? ORDER BY r.id,p.handle`, deviceID)
	if err != nil {
		return NetworkBrowserIdentity{}, err
	}
	rooms := make([]roomIdentity, 0)
	roomIndexes := map[string]int{}
	for rows.Next() {
		var roomID, status, agent string
		if err := rows.Scan(&roomID, &status, &agent); err != nil {
			rows.Close()
			return NetworkBrowserIdentity{}, err
		}
		index, exists := roomIndexes[roomID]
		if !exists {
			index = len(rooms)
			roomIndexes[roomID] = index
			rooms = append(rooms, roomIdentity{id: roomID, status: status})
		}
		if agent != "" {
			rooms[index].agents = append(rooms[index].agents, "@"+agent)
		}
	}
	if err := rows.Close(); err != nil {
		return NetworkBrowserIdentity{}, err
	}
	if err := rows.Err(); err != nil {
		return NetworkBrowserIdentity{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	for _, room := range rooms {
		if room.status != "open" {
			continue
		}
		body := "Human collaborator @" + handle + " joined this room."
		if len(room.agents) == 0 {
			body += " They do not currently have a connected agent in this room."
		} else {
			body += " Their connected agents are " + strings.Join(room.agents, ", ") + "."
		}
		if _, err := insertMessage(ctx, tx, room.id, nil, handle, handle, "member", "system", body, "", now); err != nil {
			return NetworkBrowserIdentity{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE rooms SET updated_at=? WHERE id=?`, now, room.id); err != nil {
			return NetworkBrowserIdentity{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return NetworkBrowserIdentity{}, err
	}
	return NetworkBrowserIdentity{Handle: handle}, nil
}

func (s *Store) SendNetworkMember(ctx context.Context, deviceID string, input SendInput) (Message, error) {
	room, err := s.NetworkRoom(ctx, deviceID, input.Room)
	if err != nil {
		return Message{}, err
	}
	if room.Status != "open" {
		return Message{}, errors.New("room is archived")
	}
	identity, err := s.NetworkBrowserIdentity(ctx, deviceID)
	if err != nil {
		return Message{}, err
	}
	if identity.Handle == "" {
		return Message{}, errors.New("choose a browser identity before sending messages")
	}
	body, err := boundedText("message", input.Body, 1, 16384)
	if err != nil {
		return Message{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	message, err := insertMessage(ctx, tx, room.ID, nil, identity.Handle, identity.Handle, "member", "message", body, "", now)
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

func (s *Store) AcceptNetworkInvite(ctx context.Context, deviceID string, input AcceptNetworkInviteInput) (Room, error) {
	digest, err := networkSecretDigest(input.Invite)
	if err != nil {
		return Room{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Room{}, err
	}
	defer tx.Rollback()
	room, err := consumeNetworkInviteTx(ctx, tx, digest, deviceID, now)
	if err != nil {
		return Room{}, err
	}
	return room, tx.Commit()
}

func (s *Store) NetworkRooms(ctx context.Context, deviceID string) ([]Room, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.slug,r.title,r.topic,r.status,r.created_at,r.updated_at,
COALESCE((SELECT MAX(sequence) FROM messages WHERE room_id=r.id),0)
FROM network_room_memberships n JOIN rooms r ON r.id=n.room_id WHERE n.device_id=?
ORDER BY r.status='open' DESC,r.updated_at DESC,r.slug`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rooms []Room
	for rows.Next() {
		var room Room
		if err := rows.Scan(&room.ID, &room.Slug, &room.Title, &room.Topic, &room.Status, &room.CreatedAt, &room.UpdatedAt, &room.LastSequence); err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}
	return rooms, rows.Err()
}

func (s *Store) NetworkRoom(ctx context.Context, deviceID, identifier string) (Room, error) {
	room, err := s.resolveRoom(ctx, identifier)
	if err != nil {
		return Room{}, err
	}
	var allowed bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM network_room_memberships WHERE device_id=? AND room_id=?)`, deviceID, room.ID).Scan(&allowed); err != nil {
		return Room{}, err
	}
	if !allowed {
		return Room{}, errors.New("room is not available to this device")
	}
	return room, nil
}

func (s *Store) JoinNetworkParticipant(ctx context.Context, device NetworkDevice, input JoinInput) (Participant, error) {
	room, err := s.NetworkRoom(ctx, device.ID, input.Room)
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
		return Participant{}, errors.New("participant handle @owner is reserved for the Crewfold owner")
	}
	display := strings.TrimSpace(input.DisplayName)
	if display == "" {
		display = handle
	}
	if display, err = boundedText("display name", display, 1, 120); err != nil {
		return Participant{}, err
	}
	remoteDirectory, err := boundedText("remote working directory", input.WorkingDirectory, 1, 4096)
	if err != nil {
		return Participant{}, err
	}
	provider := strings.ToLower(strings.TrimSpace(input.Delivery))
	if provider == "" {
		provider = "codex"
	}
	if provider != "codex" && provider != "claude" && provider != "none" {
		return Participant{}, errors.New("remote delivery must be codex, claude, or none")
	}
	if input.Kind != "" && input.Kind != "agent" {
		return Participant{}, errors.New("remote participants must use kind agent")
	}
	pathDigest := sha256.Sum256([]byte(remoteDirectory))
	storedDirectory := fmt.Sprintf("network://%s/%s", device.ID, hex.EncodeToString(pathDigest[:8]))
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO participants(id,room_id,handle,display_name,kind,working_directory,status,joined_at,last_seen_at) VALUES(?,?,?,?, 'agent',?, 'joined',?,?)`, participantID, room.ID, handle, display, storedDirectory, now, now); err != nil {
			return Participant{}, fmt.Errorf("join room: %w", err)
		}
	} else if err != nil {
		return Participant{}, err
	} else {
		if existingDeviceID != device.ID {
			return Participant{}, errors.New("participant handle is already in use in this room")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE participants SET display_name=?,working_directory=?,status='joined',last_seen_at=? WHERE id=?`, display, storedDirectory, now, participantID); err != nil {
			return Participant{}, fmt.Errorf("rejoin room: %w", err)
		}
	}
	baseline := room.LastSequence
	var existingCursor int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT last_delivered_sequence FROM network_participant_bindings WHERE participant_id=?),(SELECT last_delivered_sequence FROM participant_deliveries WHERE participant_id=?),?)`, participantID, participantID, baseline).Scan(&existingCursor); err != nil {
		return Participant{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM participant_deliveries WHERE participant_id=?`, participantID); err != nil {
		return Participant{}, err
	}
	status := "bound"
	if provider == "none" {
		status = "delivered"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO network_participant_bindings(participant_id,device_id,provider,remote_directory,status,last_delivered_sequence,updated_at)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(participant_id) DO UPDATE SET device_id=excluded.device_id,provider=excluded.provider,remote_directory=excluded.remote_directory,status=excluded.status,error='',updated_at=excluded.updated_at`, participantID, device.ID, provider, remoteDirectory, status, existingCursor, now); err != nil {
		return Participant{}, err
	}
	message := display + " joined from " + device.Name + ":" + remoteDirectory + "."
	if device.BrowserHandle != "" {
		message = display + " joined as an agent of human @" + device.BrowserHandle + " from " + device.Name + ":" + remoteDirectory + "."
	}
	if _, err := insertMessage(ctx, tx, room.ID, &participantID, handle, display, "agent", "system", message, "", now); err != nil {
		return Participant{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rooms SET updated_at=? WHERE id=?`, now, room.ID); err != nil {
		return Participant{}, err
	}
	if err := tx.Commit(); err != nil {
		return Participant{}, err
	}
	participant, err := s.participant(ctx, room.ID, participantID)
	if err == nil {
		participant.WorkingDirectory = device.Name + ":" + remoteDirectory
	}
	return participant, err
}

func (s *Store) NetworkBinding(ctx context.Context, deviceID, participantID string) (NetworkBinding, Participant, Room, error) {
	var binding NetworkBinding
	var participant Participant
	var room Room
	err := s.db.QueryRowContext(ctx, `SELECT n.participant_id,n.device_id,n.provider,n.remote_directory,n.status,n.last_delivered_sequence,
COALESCE(n.last_attempt_at,''),COALESCE(n.last_delivered_at,''),n.error,n.updated_at,
p.id,p.room_id,p.handle,p.display_name,p.kind,COALESCE(d.browser_handle,''),p.status,p.context,COALESCE(p.context_updated_at,''),p.last_read_sequence,p.joined_at,p.last_seen_at,
r.id,r.slug,r.title,r.topic,r.status,r.created_at,r.updated_at,
COALESCE((SELECT MAX(sequence) FROM messages WHERE room_id=r.id),0)
FROM network_participant_bindings n JOIN participants p ON p.id=n.participant_id JOIN network_devices d ON d.id=n.device_id JOIN rooms r ON r.id=p.room_id
WHERE n.device_id=? AND n.participant_id=?`, deviceID, participantID).Scan(
		&binding.ParticipantID, &binding.DeviceID, &binding.Provider, &binding.RemoteDirectory, &binding.Status, &binding.LastDeliveredSequence,
		&binding.LastAttemptAt, &binding.LastDeliveredAt, &binding.Error, &binding.UpdatedAt,
		&participant.ID, &participant.RoomID, &participant.Handle, &participant.DisplayName, &participant.Kind, &participant.HumanHandle, &participant.Status,
		&participant.Context, &participant.ContextUpdatedAt, &participant.LastReadSequence, &participant.JoinedAt, &participant.LastSeenAt,
		&room.ID, &room.Slug, &room.Title, &room.Topic, &room.Status, &room.CreatedAt, &room.UpdatedAt, &room.LastSequence,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return binding, participant, room, errors.New("remote participant binding not found")
	}
	return binding, participant, room, err
}

func (s *Store) NetworkDeliveryBatch(ctx context.Context, deviceID, participantID string) (NetworkDeliveryBatch, error) {
	binding, participant, room, err := s.NetworkBinding(ctx, deviceID, participantID)
	if err != nil {
		return NetworkDeliveryBatch{}, err
	}
	if binding.Provider == "none" || binding.Status == "left" || participant.Status != "joined" || room.Status != "open" {
		return NetworkDeliveryBatch{}, errors.New("automatic delivery is not active for this participant")
	}
	// Manual reads/receives may already have acknowledged past a stale relay
	// cursor. Keep genuinely unread events, but do not replay acknowledged ones.
	after := max(binding.LastDeliveredSequence, participant.LastReadSequence)
	messages, err := s.messages(ctx, room.ID, after, 100)
	if err != nil {
		return NetworkDeliveryBatch{}, err
	}
	participants, err := s.participants(ctx, room.ID)
	if err != nil {
		return NetworkDeliveryBatch{}, err
	}
	through := after
	filtered := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.Sequence > through {
			through = message.Sequence
		}
		if message.SenderKind == "system" || message.ParticipantID == participant.ID {
			continue
		}
		filtered = append(filtered, message)
	}
	return NetworkDeliveryBatch{Room: room, Participant: participant, Participants: participants, Messages: filtered, After: after, Through: through}, nil
}

func (s *Store) RecordNetworkDelivery(ctx context.Context, deviceID string, input NetworkDeliveryResultInput) (NetworkBinding, error) {
	binding, _, room, err := s.NetworkBinding(ctx, deviceID, input.ParticipantID)
	if err != nil {
		return NetworkBinding{}, err
	}
	status := strings.ToLower(strings.TrimSpace(input.Status))
	if status != "delivered" && status != "queued" && status != "error" {
		return NetworkBinding{}, errors.New("delivery status must be delivered, queued, or error")
	}
	detail, err := boundedText("delivery error", input.Error, 0, 2048)
	if err != nil {
		return NetworkBinding{}, err
	}
	through := input.Through
	if through < binding.LastDeliveredSequence || through > room.LastSequence {
		return NetworkBinding{}, errors.New("delivery acknowledgement is outside the available room sequence")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if status == "delivered" {
		_, err = s.db.ExecContext(ctx, `UPDATE network_participant_bindings SET status='delivered',last_delivered_sequence=MAX(last_delivered_sequence,?),last_attempt_at=?,last_delivered_at=?,error='',updated_at=? WHERE participant_id=? AND device_id=?`, through, now, now, now, input.ParticipantID, deviceID)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE network_participant_bindings SET status=?,last_attempt_at=?,error=?,updated_at=? WHERE participant_id=? AND device_id=?`, status, now, detail, now, input.ParticipantID, deviceID)
	}
	if err != nil {
		return NetworkBinding{}, err
	}
	updated, _, _, err := s.NetworkBinding(ctx, deviceID, input.ParticipantID)
	return updated, err
}
