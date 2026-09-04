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
	var older Snapshot
	if err := client.Call(context.Background(), "room.snapshot", ListMessagesInput{Room: "shared-test", Before: message.Sequence, Limit: 1}, &older); err != nil {
		t.Fatal(err)
	}
	if len(older.Messages) != 1 || older.Messages[0].Sequence >= message.Sequence {
		t.Fatalf("older message window = %#v", older.Messages)
	}
	if err := client.Call(context.Background(), "room.snapshot", ListMessagesInput{Room: "shared-test", After: 1, Before: message.Sequence, Limit: 1}, &older); err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("conflicting message cursors were not rejected: %v", err)
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

func TestOwnerWebStatePersistsAcrossRestarts(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	token, digest, err := loadOwnerWebSession(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	reloadedToken, reloadedDigest, err := loadOwnerWebSession(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if token != reloadedToken || digest != reloadedDigest {
		t.Fatal("owner web credential changed across reload")
	}
	server := Server{ownerHash: reloadedDigest}
	if !server.authorizeWeb("Bearer "+reloadedToken) || server.authorizeWeb("Bearer "+strings.Repeat("0", 64)) {
		t.Fatal("persisted owner credential was not enforced")
	}

	first, err := listenOwnerWeb(dataDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := first.Addr().String()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := listenOwnerWeb(dataDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if second.Addr().String() != address {
		t.Fatalf("web address changed from %s to %s", address, second.Addr())
	}
	for _, name := range []string{"web-owner-token", "web-address"} {
		info, err := os.Stat(filepath.Join(dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", name, info.Mode().Perm())
		}
	}
}
