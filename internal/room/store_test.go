package room

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		Slug:          "tire-slip",
		Title:         "Tire slip model",
		Topic:         "Compare the new slip model across two independently run sessions.",
		StewardHandle: "slip-steward",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Participants) != 1 || snapshot.Participants[0].Status != "invited" {
		t.Fatalf("expected one invited steward, got %#v", snapshot.Participants)
	}

	whenTheyFell := filepath.Join(root, "when-they-fell")
	worldEngine := filepath.Join(root, "world-engine-2")
	stewardHome := filepath.Join(root, "steward")
	for _, directory := range []string{whenTheyFell, worldEngine, stewardHome} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Join(ctx, JoinInput{Room: "tire-slip", Handle: "when-they-fell", DisplayName: "When They Fell", WorkingDirectory: whenTheyFell})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Join(ctx, JoinInput{Room: "tire-slip", Handle: "world-engine-2", DisplayName: "World Engine 2", WorkingDirectory: worldEngine})
	if err != nil {
		t.Fatal(err)
	}
	steward, err := store.Join(ctx, JoinInput{Room: "tire-slip", Handle: "slip-steward", DisplayName: "Slip Steward", WorkingDirectory: stewardHome, Kind: "steward"})
	if err != nil {
		t.Fatal(err)
	}
	if steward.Kind != "steward" || steward.Status != "joined" {
		t.Fatalf("invited steward was not claimed: %#v", steward)
	}

	if _, err := store.Send(ctx, SendInput{Room: "tire-slip", WorkingDirectory: whenTheyFell, Kind: "context", Body: "Testing low-speed braking on wet asphalt."}); err != nil {
		t.Fatal(err)
	}
	observation, err := store.Send(ctx, SendInput{Room: "tire-slip", WorkingDirectory: worldEngine, Body: "The combined-slip curve diverges after 11 degrees."})
	if err != nil {
		t.Fatal(err)
	}
	if observation.SenderHandle != second.Handle {
		t.Fatalf("message attributed to %q, want %q", observation.SenderHandle, second.Handle)
	}

	documentText := "# Slip comparison\n\nThe divergence starts at **11 degrees**.\n"
	uploaded, err := store.Upload(ctx, UploadInput{
		Room:             "tire-slip",
		WorkingDirectory: whenTheyFell,
		Name:             "slip-comparison.md",
		MediaType:        "text/markdown",
		ContentBase64:    base64.StdEncoding.EncodeToString([]byte(documentText)),
		Caption:          "Uploaded the cross-scenario comparison.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.Document == nil {
		t.Fatal("upload did not return document metadata")
	}
	document, content, err := store.ReadDocument(ctx, "tire-slip", uploaded.Document.ID)
	if err != nil {
		t.Fatal(err)
	}
	if document.Name != "slip-comparison.md" || string(content) != documentText {
		t.Fatalf("unexpected document: %#v %q", document, content)
	}

	snapshot, err = store.Snapshot(ctx, "tire-slip", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Participants) != 3 || len(snapshot.Documents) != 1 {
		t.Fatalf("unexpected room projection: %d participants, %d documents", len(snapshot.Participants), len(snapshot.Documents))
	}
	if got := participantNamed(snapshot.Participants, first.Handle).Context; got != "Testing low-speed braking on wet asphalt." {
		t.Fatalf("published context = %q", got)
	}
	if participantNamed(snapshot.Participants, steward.Handle).UnreadCount == 0 {
		t.Fatal("steward should see unread room activity")
	}

	acknowledged, err := store.Ack(ctx, AckInput{Room: "tire-slip", WorkingDirectory: stewardHome, Through: snapshot.Room.LastSequence + 1000})
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged.LastReadSequence != snapshot.Room.LastSequence || acknowledged.UnreadCount != 0 {
		t.Fatalf("acknowledgement was not clamped to the room cursor: %#v", acknowledged)
	}
	if _, err := store.Send(ctx, SendInput{Room: "tire-slip", WorkingDirectory: worldEngine, Handle: first.Handle, Body: "impersonation"}); err == nil || !strings.Contains(err.Error(), "has not joined") {
		t.Fatalf("expected handle/folder mismatch rejection, got %v", err)
	}

	if _, err := store.Archive(ctx, "tire-slip"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upload(ctx, UploadInput{Room: "tire-slip", WorkingDirectory: whenTheyFell, Name: "late.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("late"))}); err == nil || err.Error() != "room is archived" {
		t.Fatalf("archived upload error = %v", err)
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
}
