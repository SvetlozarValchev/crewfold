package room

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/buildinfo"
	"crewfold/internal/localipc"
)

func TestLocalServerExposesRoomWorkflow(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("", "cf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := localipc.Endpoint(filepath.Join(root, "runtime"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunServer(ctx, ServerConfig{
			DataDir:    filepath.Join(root, "state"),
			SocketPath: socket,
			WebAddress: "127.0.0.1:0",
			Version:    buildinfo.Info{Version: "test"},
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server did not stop")
		}
	})

	client := Client{SocketPath: socket}
	waitForServer(t, client)
	var status map[string]any
	if err := client.Call(context.Background(), "status", map[string]any{}, &status); err != nil {
		t.Fatal(err)
	}
	if status["status"] != "ok" {
		t.Fatalf("unexpected status: %#v", status)
	}
	var created Snapshot
	if err := client.Call(context.Background(), "room.create", CreateRoomInput{Slug: "shared-test", Title: "Shared test", Topic: "Exercise the current API."}, &created); err != nil {
		t.Fatal(err)
	}
	var absentSteward *StewardConsole
	if err := client.Call(context.Background(), "steward.status", map[string]any{"room": created.Room.ID}, &absentSteward); err != nil {
		t.Fatalf("read absent hosted steward: %v", err)
	}
	if absentSteward != nil {
		t.Fatalf("unexpected hosted steward: %#v", absentSteward)
	}
	participantDirectory := filepath.Join(root, "participant")
	if err := os.Mkdir(participantDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	var participant Participant
	if err := client.Call(context.Background(), "participant.join", JoinInput{Room: created.Room.ID, Handle: "tester", WorkingDirectory: participantDirectory}, &participant); err != nil {
		t.Fatal(err)
	}
	var message Message
	if err := client.Call(context.Background(), "message.send", SendInput{Room: "shared-test", WorkingDirectory: participantDirectory, Body: "hello from the real Unix protocol"}, &message); err != nil {
		t.Fatal(err)
	}
	if message.SenderHandle != "tester" {
		t.Fatalf("message sender = %q", message.SenderHandle)
	}
	var bootstrap map[string]any
	if err := client.Call(context.Background(), "web.bootstrap", map[string]any{}, &bootstrap); err != nil {
		t.Fatal(err)
	}
	if address, _ := bootstrap["url"].(string); !strings.HasPrefix(address, "http://127.0.0.1:") || !strings.Contains(address, "#bootstrap=") {
		t.Fatalf("unexpected browser bootstrap: %#v", bootstrap)
	}

	var rejected any
	err = client.Call(context.Background(), "message.send", map[string]any{"room": "shared-test", "body": "bad", "invented": true}, &rejected)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown request field was not rejected: %v", err)
	}
}

func waitForServer(t *testing.T, client Client) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var status map[string]any
		if err := client.Call(context.Background(), "status", map[string]any{}, &status); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("local server did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
