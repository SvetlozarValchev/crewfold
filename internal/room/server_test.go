package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/buildinfo"
)

func TestUnixServerExposesRoomWorkflow(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	socket := filepath.Join(root, "runtime", "crewfold.sock")
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

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}

	client := Client{SocketPath: socket}
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
	err := client.Call(context.Background(), "message.send", map[string]any{"room": "shared-test", "body": "bad", "invented": true}, &rejected)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown request field was not rejected: %v", err)
	}
}

func TestServerRejectsSecondDaemonOnSocket(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	socket := filepath.Join(root, "occupied")
	if err := os.WriteFile(socket, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := RunServer(context.Background(), ServerConfig{DataDir: filepath.Join(root, "state"), SocketPath: socket, WebAddress: "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "non-socket") || errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected occupied socket error: %v", err)
	}
}
