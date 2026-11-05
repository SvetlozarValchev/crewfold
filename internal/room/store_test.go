package room

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/driver"
)

func TestDeliveryRangesStartAtZeroAndRemainBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "ranges", Title: "Ranges"})
	if err != nil {
		t.Fatal(err)
	}
	var cutoff int64
	for index := 0; index < 600; index++ {
		message, err := store.Send(ctx, SendInput{Room: created.Room.ID, Owner: true, Body: fmt.Sprintf("event %d", index)})
		if err != nil {
			t.Fatal(err)
		}
		if index == 19 {
			cutoff = message.Sequence
		}
	}
	first, err := store.messages(ctx, created.Room.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 100 || first[0].Sequence != created.Messages[0].Sequence {
		t.Fatalf("delivery skipped beginning: %#v", first)
	}
	snapshot, err := store.SnapshotRange(ctx, created.Room.ID, 0, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 21 || snapshot.Messages[0].Sequence != created.Messages[0].Sequence || snapshot.Messages[20].Sequence != cutoff {
		t.Fatalf("bounded range moved with new activity: %#v", snapshot.Messages)
	}
	if _, err := store.SnapshotRange(ctx, created.Room.ID, 0, snapshot.Room.LastSequence); err == nil {
		t.Fatal("oversized delivery range silently truncated")
	}
}

func TestRemoveHostedStatePreservesRoomHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "preserved", Title: "Preserved"})
	if err != nil {
		t.Fatal(err)
	}
	participant, err := store.Join(ctx, JoinInput{Room: created.Room.ID, Handle: "curator", Kind: "steward", WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.Send(ctx, SendInput{Room: created.Room.ID, ParticipantID: participant.ID, Body: "Durable finding."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE rooms ADD COLUMN steward_id TEXT;
CREATE TABLE hosted_stewards(participant_id TEXT PRIMARY KEY REFERENCES participants(id));`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO hosted_stewards(participant_id) VALUES (?)`, participant.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot(ctx, created.Room.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Participants[0].Status != "left" || snapshot.Messages[len(snapshot.Messages)-1].ID != message.ID {
		t.Fatalf("migration lost history or kept managed participant active: %#v", snapshot)
	}
	var remaining int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name='hosted_stewards'`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("hosted table remains: %d, %v", remaining, err)
	}
	if _, err := store.Join(ctx, JoinInput{Room: created.Room.ID, Handle: "claude-agent", WorkingDirectory: t.TempDir(), Delivery: "claude", SessionID: "open-session"}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryProviderMigrationPreservesCodexBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "bindings", Title: "Bindings"}); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := store.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name='participant_deliveries'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	schema = strings.ReplaceAll(schema, "'codex','claude'", "'codex'")
	if _, err := store.db.ExecContext(ctx, "DROP TABLE participant_deliveries;"+schema); err != nil {
		t.Fatal(err)
	}
	worker, err := store.Join(ctx, JoinInput{Room: "bindings", Handle: "worker", WorkingDirectory: t.TempDir(), Delivery: "codex", SessionID: "open-thread"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.advanceDelivery(ctx, worker.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.Snapshot(ctx, "bindings", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	delivery := participantNamed(state.Participants, "worker").Delivery
	if delivery == nil || delivery.Target != "open-thread" || delivery.LastDeliveredSequence != 2 {
		t.Fatalf("Codex binding lost during migration: %#v", delivery)
	}
	if _, err := store.Join(ctx, JoinInput{Room: "bindings", Handle: "claude", WorkingDirectory: t.TempDir(), Delivery: "claude", SessionID: "open-session"}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAddsDocumentArchiveStateToExistingStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	location := &url.URL{Scheme: "file", Path: filepath.Join(root, databaseFilename)}
	database, err := driver.Open(location.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE documents (
id TEXT PRIMARY KEY,
room_id TEXT NOT NULL,
participant_id TEXT,
name TEXT NOT NULL,
media_type TEXT NOT NULL,
byte_size INTEGER NOT NULL,
sha256 TEXT NOT NULL,
relative_path TEXT NOT NULL UNIQUE,
created_at TEXT NOT NULL
) STRICT`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var archiveColumns int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('documents') WHERE name='archived_at'`).Scan(&archiveColumns); err != nil {
		t.Fatal(err)
	}
	if archiveColumns != 1 {
		t.Fatalf("archived_at columns = %d", archiveColumns)
	}
}

func TestOpenMigratesBrowserIdentityAndHumanMessageKinds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	location := &url.URL{Scheme: "file", Path: filepath.Join(root, databaseFilename)}
	database, err := driver.Open(location.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `
CREATE TABLE rooms (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  topic TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('open','archived')),
  steward_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;
CREATE TABLE network_devices (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  joined_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
) STRICT;
CREATE TABLE messages (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  room_id TEXT NOT NULL,
  participant_id TEXT,
  sender_handle TEXT NOT NULL,
  sender_name TEXT NOT NULL,
  sender_kind TEXT NOT NULL CHECK(sender_kind IN ('owner','agent','steward','system')),
  kind TEXT NOT NULL CHECK(kind IN ('message','context','document','system')),
  body TEXT NOT NULL,
  document_id TEXT,
  created_at TEXT NOT NULL
) STRICT;
INSERT INTO rooms(id,slug,title,topic,status,created_at,updated_at)
VALUES('room_legacy','legacy','Legacy','Migration fixture','open','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO messages(sequence,id,room_id,sender_handle,sender_name,sender_kind,kind,body,created_at)
VALUES(42,'message_legacy','room_legacy','owner','You','owner','message','Preserve me.','2026-01-01T00:00:00Z');`
	if _, err := database.ExecContext(ctx, legacy); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var browserColumns int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('network_devices') WHERE name='browser_handle'`).Scan(&browserColumns); err != nil {
		t.Fatal(err)
	}
	if browserColumns != 1 {
		t.Fatalf("browser_handle columns = %d", browserColumns)
	}
	var messagesSchema string
	if err := store.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='messages'`).Scan(&messagesSchema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(messagesSchema, "'member'") {
		t.Fatalf("messages schema was not migrated: %s", messagesSchema)
	}
	snapshot, err := store.Snapshot(ctx, "legacy", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].Sequence != 42 || snapshot.Messages[0].Body != "Preserve me." {
		t.Fatalf("migrated messages = %#v", snapshot.Messages)
	}
	message, err := store.Send(ctx, SendInput{Room: "legacy", Owner: true, Body: "After migration."})
	if err != nil {
		t.Fatal(err)
	}
	if message.Sequence != 43 {
		t.Fatalf("post-migration sequence = %d", message.Sequence)
	}
}

func TestCanonicalFeedRejectsUnstructuredSubstantialContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "readable", Title: "Readable", Topic: "Keep the shared feed readable."}); err != nil {
		t.Fatal(err)
	}
	participantDirectory := filepath.Join(t.TempDir(), "participant")
	if err := os.Mkdir(participantDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Join(ctx, JoinInput{Room: "readable", Handle: "participant", WorkingDirectory: participantDirectory}); err != nil {
		t.Fatal(err)
	}
	dense := strings.Repeat("Substantial verified status without any block structure. ", 10)
	if _, err := store.Send(ctx, SendInput{Room: "readable", WorkingDirectory: participantDirectory, Body: dense}); err == nil || !strings.Contains(err.Error(), "unstructured prose") {
		t.Fatalf("canonical message validation error = %v", err)
	}
	structured := "## Findings\n\n- " + dense
	if _, err := store.Send(ctx, SendInput{Room: "readable", WorkingDirectory: participantDirectory, Body: structured}); err != nil {
		t.Fatalf("structured message rejected: %v", err)
	}
	ownerMessage, err := store.Send(ctx, SendInput{Room: "readable", Owner: true, Body: dense})
	if err != nil {
		t.Fatalf("owner conversation was subjected to participant anti-dump validation: %v", err)
	}
	if ownerMessage.SenderKind != "owner" || ownerMessage.Body != strings.TrimSpace(dense) {
		t.Fatalf("owner message = %#v", ownerMessage)
	}
	if _, err := store.Send(ctx, SendInput{Room: "readable", Owner: true, Kind: "context", Body: dense}); err != nil {
		t.Fatalf("replaceable context rejected: %v", err)
	}
	if _, err := store.Upload(ctx, UploadInput{Room: "readable", Owner: true, Name: "finding.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("# Finding\n")), Caption: dense}); err == nil || !strings.Contains(err.Error(), "document caption") {
		t.Fatalf("canonical document-caption validation error = %v", err)
	}
}

func TestLeaveDisablesDeliveryAndRejoinPreservesParticipantState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "handoff", Title: "Handoff", Topic: "Exercise participant leave and rejoin."})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "agent")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	joined, err := store.Join(ctx, JoinInput{Room: created.Room.ID, Handle: "worker", WorkingDirectory: directory, Delivery: "codex", SessionID: "thread-old"})
	if err != nil {
		t.Fatal(err)
	}
	if joined.HumanHandle != "owner" {
		t.Fatalf("local participant human = %#v", joined)
	}
	if _, err := store.Send(ctx, SendInput{Room: created.Room.ID, WorkingDirectory: directory, Kind: "context", Body: "Current durable finding."}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upload(ctx, UploadInput{Room: created.Room.ID, WorkingDirectory: directory, Name: "finding.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("# Finding\n"))}); err != nil {
		t.Fatal(err)
	}
	beforeLeave, err := store.Snapshot(ctx, created.Room.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.advanceDelivery(ctx, joined.ID, beforeLeave.Room.LastSequence); err != nil {
		t.Fatal(err)
	}

	left, err := store.Leave(ctx, LeaveInput{Room: created.Room.ID, WorkingDirectory: directory})
	if err != nil {
		t.Fatal(err)
	}
	if left.ID != joined.ID || left.Status != "left" || left.Context != "Current durable finding." || left.Delivery == nil || left.Delivery.LastDeliveredSequence != beforeLeave.Room.LastSequence {
		t.Fatalf("left participant = %#v", left)
	}
	if _, err := store.Send(ctx, SendInput{Room: created.Room.ID, WorkingDirectory: directory, Body: "should fail"}); err == nil || !strings.Contains(err.Error(), "has not joined") {
		t.Fatalf("left participant could still send: %v", err)
	}
	if _, err := store.Send(ctx, SendInput{Room: created.Room.ID, Owner: true, Body: "Update while worker is away."}); err != nil {
		t.Fatal(err)
	}
	routes, err := store.pendingDeliveries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("left participant still had an active delivery route: %#v", routes)
	}

	rejoined, err := store.Join(ctx, JoinInput{Room: created.Room.ID, Handle: "worker", WorkingDirectory: directory, Delivery: "codex", SessionID: "thread-new"})
	if err != nil {
		t.Fatal(err)
	}
	if rejoined.ID != joined.ID || rejoined.Status != "joined" || rejoined.Context != left.Context || rejoined.Delivery == nil || rejoined.Delivery.Target != "thread-new" || rejoined.Delivery.LastDeliveredSequence != beforeLeave.Room.LastSequence {
		t.Fatalf("rejoined participant = %#v", rejoined)
	}
	afterRejoin, err := store.Snapshot(ctx, created.Room.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRejoin.Documents) != 1 {
		t.Fatalf("documents after rejoin = %#v", afterRejoin.Documents)
	}
	var sawLeave bool
	for _, message := range afterRejoin.Messages {
		if message.Kind == "system" && message.Body == "worker left the room." {
			sawLeave = true
		}
	}
	if !sawLeave {
		t.Fatalf("leave event missing from history: %#v", afterRejoin.Messages)
	}
}

func TestRoomCollaborationLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	snapshot, err := store.CreateRoom(ctx, CreateRoomInput{
		Slug:  "compatibility-review",
		Title: "Compatibility review",
		Topic: "Compare an interface contract across two independently run sessions.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Participants) != 0 {
		t.Fatalf("new room unexpectedly has participants: %#v", snapshot.Participants)
	}

	clientDirectory := filepath.Join(root, "client")
	serviceDirectory := filepath.Join(root, "service")
	stewardHome := filepath.Join(root, "steward")
	for _, directory := range []string{clientDirectory, serviceDirectory, stewardHome} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Join(ctx, JoinInput{Room: "compatibility-review", Handle: "client-agent", DisplayName: "Client Agent", WorkingDirectory: clientDirectory})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Join(ctx, JoinInput{Room: "compatibility-review", Handle: "service-agent", DisplayName: "Service Agent", WorkingDirectory: serviceDirectory})
	if err != nil {
		t.Fatal(err)
	}
	steward, err := store.Join(ctx, JoinInput{Room: "compatibility-review", Handle: "room-steward", DisplayName: "Room Steward", WorkingDirectory: stewardHome, Kind: "steward"})
	if err != nil {
		t.Fatal(err)
	}
	if steward.Kind != "steward" || steward.Status != "joined" {
		t.Fatalf("external steward did not join: %#v", steward)
	}

	if _, err := store.Send(ctx, SendInput{Room: "compatibility-review", WorkingDirectory: clientDirectory, Kind: "context", Body: "Checking the client against the proposed response schema."}); err != nil {
		t.Fatal(err)
	}
	observation, err := store.Send(ctx, SendInput{Room: "compatibility-review", WorkingDirectory: serviceDirectory, Body: "The response schema differs on one required field."})
	if err != nil {
		t.Fatal(err)
	}
	if observation.SenderHandle != second.Handle {
		t.Fatalf("message attributed to %q, want %q", observation.SenderHandle, second.Handle)
	}

	documentText := "# Compatibility findings\n\nThe response differs on one **required field**.\n"
	uploaded, err := store.Upload(ctx, UploadInput{
		Room:             "compatibility-review",
		WorkingDirectory: clientDirectory,
		Name:             "compatibility-findings.md",
		MediaType:        "text/markdown",
		ContentBase64:    base64.StdEncoding.EncodeToString([]byte(documentText)),
		Caption:          "Uploaded the cross-session comparison.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.Document == nil {
		t.Fatal("upload did not return document metadata")
	}
	document, content, err := store.ReadDocument(ctx, "compatibility-review", uploaded.Document.ID)
	if err != nil {
		t.Fatal(err)
	}
	if document.Name != "compatibility-findings.md" || string(content) != documentText {
		t.Fatalf("unexpected document: %#v %q", document, content)
	}

	store.now = func() time.Time { return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) }
	revisedText := "# Compatibility findings\n\nThe response now matches every **required field**.\n"
	revised, err := store.Upload(ctx, UploadInput{
		Room:             "compatibility-review",
		WorkingDirectory: clientDirectory,
		Name:             "compatibility-findings.md",
		MediaType:        "text/markdown",
		ContentBase64:    base64.StdEncoding.EncodeToString([]byte(revisedText)),
		Caption:          "Revised the cross-session comparison.",
	})
	if err != nil {
		t.Fatal(err)
	}
	current, currentContent, err := store.ReadDocument(ctx, "compatibility-review", "compatibility-findings.md")
	if err != nil {
		t.Fatal(err)
	}
	if revised.Document == nil || current.ID != revised.Document.ID || string(currentContent) != revisedText {
		t.Fatalf("current filename revision = %#v %q, want %#v %q", current, currentContent, revised.Document, revisedText)
	}
	historical, historicalContent, err := store.ReadDocument(ctx, "compatibility-review", uploaded.Document.ID)
	if err != nil {
		t.Fatal(err)
	}
	if historical.ID != uploaded.Document.ID || string(historicalContent) != documentText {
		t.Fatalf("historical document revision = %#v %q", historical, historicalContent)
	}
	otherPublisherText := "# Compatibility findings\n\nService-side evidence for the same filename.\n"
	otherPublisher, err := store.Upload(ctx, UploadInput{
		Room:             "compatibility-review",
		WorkingDirectory: serviceDirectory,
		Name:             "compatibility-findings.md",
		MediaType:        "text/markdown",
		ContentBase64:    base64.StdEncoding.EncodeToString([]byte(otherPublisherText)),
		Caption:          "Uploaded the service-side comparison.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReadDocument(ctx, "compatibility-review", "compatibility-findings.md"); err == nil || !strings.Contains(err.Error(), "ambiguous across participants") {
		t.Fatalf("same filename from different publishers error = %v", err)
	}
	if otherPublisher.Document == nil {
		t.Fatal("other publisher upload did not return document metadata")
	}
	otherDocument, otherContent, err := store.ReadDocument(ctx, "compatibility-review", otherPublisher.Document.ID)
	if err != nil {
		t.Fatal(err)
	}
	if otherDocument.ID != otherPublisher.Document.ID || string(otherContent) != otherPublisherText {
		t.Fatalf("other publisher document = %#v %q", otherDocument, otherContent)
	}

	snapshot, err = store.Snapshot(ctx, "compatibility-review", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Participants) != 3 || len(snapshot.Documents) != 3 {
		t.Fatalf("unexpected room projection: %d participants, %d documents", len(snapshot.Participants), len(snapshot.Documents))
	}
	if got := participantNamed(snapshot.Participants, first.Handle).Context; got != "Checking the client against the proposed response schema." {
		t.Fatalf("published context = %q", got)
	}
	if participantNamed(snapshot.Participants, steward.Handle).UnreadCount == 0 {
		t.Fatal("steward should see unread room activity")
	}

	acknowledged, err := store.Ack(ctx, AckInput{Room: "compatibility-review", WorkingDirectory: stewardHome, Through: snapshot.Room.LastSequence + 1000})
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged.LastReadSequence != snapshot.Room.LastSequence || acknowledged.UnreadCount != 0 {
		t.Fatalf("acknowledgement was not clamped to the room cursor: %#v", acknowledged)
	}
	if _, err := store.Send(ctx, SendInput{Room: "compatibility-review", WorkingDirectory: serviceDirectory, Handle: first.Handle, Body: "impersonation"}); err == nil || !strings.Contains(err.Error(), "has not joined") {
		t.Fatalf("expected handle/folder mismatch rejection, got %v", err)
	}

	if _, err := store.Archive(ctx, "compatibility-review"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upload(ctx, UploadInput{Room: "compatibility-review", WorkingDirectory: clientDirectory, Name: "late.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("late"))}); err == nil || err.Error() != "room is archived" {
		t.Fatalf("archived upload error = %v", err)
	}
}

func TestDocumentArchivingCuratesLogicalDocumentsWithoutHidingNewRevisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "curation", Title: "Curation", Topic: "Keep canonical documentation visible."}); err != nil {
		t.Fatal(err)
	}
	writerDirectory := filepath.Join(root, "writer")
	otherDirectory := filepath.Join(root, "other")
	stewardDirectory := filepath.Join(root, "steward")
	for _, directory := range []string{writerDirectory, otherDirectory, stewardDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Join(ctx, JoinInput{Room: "curation", Handle: "writer", WorkingDirectory: writerDirectory}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Join(ctx, JoinInput{Room: "curation", Handle: "other", WorkingDirectory: otherDirectory}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Join(ctx, JoinInput{Room: "curation", Handle: "curator", WorkingDirectory: stewardDirectory, Kind: "steward"}); err != nil {
		t.Fatal(err)
	}

	upload := func(body string) Message {
		message, uploadErr := store.Upload(ctx, UploadInput{Room: "curation", WorkingDirectory: writerDirectory, Name: "README.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte(body))})
		if uploadErr != nil {
			t.Fatal(uploadErr)
		}
		return message
	}
	first := upload("first")
	store.now = func() time.Time { return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) }
	second := upload("second")
	archived, err := store.SetDocumentArchived(ctx, ArchiveDocumentInput{Room: "curation", Document: second.Document.ID, WorkingDirectory: writerDirectory}, true)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ArchivedAt == "" {
		t.Fatal("archive did not return archive state")
	}
	for _, documentID := range []string{first.Document.ID, second.Document.ID} {
		document, _, readErr := store.ReadDocument(ctx, "curation", documentID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if document.ArchivedAt == "" {
			t.Fatalf("logical revision %s remained active", documentID)
		}
	}

	store.now = func() time.Time { return time.Date(2100, 1, 2, 0, 0, 0, 0, time.UTC) }
	third := upload("third")
	if third.Document.ArchivedAt != "" {
		t.Fatalf("new revision remained archived: %#v", third.Document)
	}
	current, _, err := store.ReadDocument(ctx, "curation", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != third.Document.ID || current.ArchivedAt != "" {
		t.Fatalf("new revision did not reactivate logical document: %#v", current)
	}

	if _, err := store.SetDocumentArchived(ctx, ArchiveDocumentInput{Room: "curation", Document: third.Document.ID, WorkingDirectory: otherDirectory}, true); err == nil || !strings.Contains(err.Error(), "only their own") {
		t.Fatalf("cross-participant archive error = %v", err)
	}
	if _, err := store.SetDocumentArchived(ctx, ArchiveDocumentInput{Room: "curation", Document: third.Document.ID, WorkingDirectory: stewardDirectory}, true); err != nil {
		t.Fatalf("steward archive: %v", err)
	}
	restored, err := store.SetDocumentArchived(ctx, ArchiveDocumentInput{Room: "curation", Document: third.Document.ID, Owner: true}, false)
	if err != nil {
		t.Fatalf("owner restore: %v", err)
	}
	if restored.ArchivedAt != "" {
		t.Fatalf("restored document is still archived: %#v", restored)
	}
}

func participantNamed(participants []Participant, handle string) Participant {
	for _, participant := range participants {
		if participant.Handle == handle {
			return participant
		}
	}
	return Participant{}
}

func TestInitialSnapshotUsesNewestBoundedWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "bounded", Title: "Bounded", Topic: "Newest messages remain visible."})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 8; index++ {
		if _, err := store.Send(ctx, SendInput{Room: created.Room.ID, Owner: true, Body: "message"}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.Snapshot(ctx, "bounded", 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 3 || snapshot.Messages[0].Sequence != 7 || snapshot.Messages[2].Sequence != 9 {
		t.Fatalf("initial window = %#v", snapshot.Messages)
	}
	incremental, err := store.Snapshot(ctx, "bounded", 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(incremental.Messages) != 3 || incremental.Messages[0].Sequence != 4 || incremental.Messages[2].Sequence != 6 {
		t.Fatalf("incremental window = %#v", incremental.Messages)
	}
	older, err := store.SnapshotBefore(ctx, "bounded", snapshot.Messages[0].Sequence, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Messages) != 3 || older.Messages[0].Sequence != 4 || older.Messages[2].Sequence != 6 {
		t.Fatalf("older window = %#v", older.Messages)
	}
	oldest, err := store.SnapshotBefore(ctx, "bounded", older.Messages[0].Sequence, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldest.Messages) != 3 || oldest.Messages[0].Sequence != 1 || oldest.Messages[2].Sequence != 3 {
		t.Fatalf("oldest window = %#v", oldest.Messages)
	}
}
