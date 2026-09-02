package roomcli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/buildinfo"
	"crewfold/internal/codexapp"
	"crewfold/internal/room"
)

type availableCodexRuntime struct{}

func (availableCodexRuntime) Inspect(_ context.Context, threadID string) (codexapp.Thread, error) {
	available := true
	return codexapp.Thread{ID: threadID, Status: codexapp.ThreadStatus{Type: "idle"}, CanAcceptDirectInput: &available}, nil
}

func (availableCodexRuntime) Deliver(context.Context, string, string, string) error { return nil }

func TestJoinDefaultsToCurrentCodexThread(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "runtime", "crewfold.sock")
	serverContext, stopServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- room.RunServer(serverContext, room.ServerConfig{
			DataDir:      filepath.Join(root, "state"),
			SocketPath:   socket,
			WebAddress:   "127.0.0.1:0",
			Version:      buildinfo.Info{Version: "test"},
			CodexRuntime: availableCodexRuntime{},
		})
	}()
	t.Cleanup(func() {
		stopServer()
		select {
		case err := <-serverDone:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server did not stop")
		}
	})
	waitForSocket(t, socket)

	client := room.Client{SocketPath: socket}
	var created room.Snapshot
	if err := client.Call(context.Background(), "room.create", room.CreateRoomInput{Slug: "shared", Title: "Shared", Topic: "Coordinate sessions."}, &created); err != nil {
		t.Fatal(err)
	}
	workingDirectory := filepath.Join(root, "checkout")
	if err := os.Mkdir(workingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_THREAD_ID", "01a0626f-7a48-70d2-b540-112ccf94e5bf")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, buildinfo.Info{Version: "test"})
	code := app.Run(context.Background(), []string{"room", "--socket", socket, "join", "shared", "--handle", "world-engine-2", "--cwd", workingDirectory})
	if code != 0 {
		t.Fatalf("join exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "delivery codex") {
		t.Fatalf("join output = %q", stdout.String())
	}
	snapshot, err := readSnapshot(client, created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Participants) != 1 || snapshot.Participants[0].Delivery == nil {
		t.Fatalf("participants = %#v", snapshot.Participants)
	}
	delivery := snapshot.Participants[0].Delivery
	if delivery.Kind != "codex" || delivery.Target != "01a0626f-7a48-70d2-b540-112ccf94e5bf" || (delivery.Status != "bound" && delivery.Status != "delivered") {
		t.Fatalf("delivery = %#v", delivery)
	}
}

func waitForSocket(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("server socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readSnapshot(client room.Client, roomID string) (room.Snapshot, error) {
	var snapshot room.Snapshot
	err := client.Call(context.Background(), "room.snapshot", room.ListMessagesInput{Room: roomID, Limit: 50}, &snapshot)
	return snapshot, err
}
