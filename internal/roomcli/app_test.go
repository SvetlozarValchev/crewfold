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
	"crewfold/internal/localipc"
	"crewfold/internal/room"
)

type availableCodexRuntime struct{}

func (availableCodexRuntime) Inspect(_ context.Context, threadID string) (codexapp.Thread, error) {
	available := true
	return codexapp.Thread{ID: threadID, Status: codexapp.ThreadStatus{Type: "idle"}, CanAcceptDirectInput: &available}, nil
}

func (availableCodexRuntime) Deliver(context.Context, string, string, string) error { return nil }

func TestJoinDefaultsToCurrentCodexThread(t *testing.T) {
	root, err := os.MkdirTemp("", "cf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := localipc.Endpoint(filepath.Join(root, "runtime"))
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
	code := app.Run(context.Background(), []string{"room", "--socket", socket, "join", "shared", "--handle", "service-agent", "--cwd", workingDirectory})
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

	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workingDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDirectory) })
	app.stdin = strings.NewReader("## Finding\n\n- client and service differ\n- owner review needed\n")
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"room", "--socket", socket, "send", "shared", "--stdin"}); code != 0 {
		t.Fatalf("stdin send exit %d: %s", code, stderr.String())
	}
	snapshot, err = readSnapshot(client, created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantBody := "## Finding\n\n- client and service differ\n- owner review needed"
	if got := snapshot.Messages[len(snapshot.Messages)-1].Body; got != wantBody {
		t.Fatalf("multiline message body = %q, want %q", got, wantBody)
	}
}

func TestMessageReadabilityRequiresStructuredStdinForSubstantialPosts(t *testing.T) {
	if err := validateMessageReadability("shared", "A short conversational update.", false); err != nil {
		t.Fatalf("short message rejected: %v", err)
	}
	structured := "## Findings\n\n" + strings.Repeat("Detailed verified evidence. ", 40)
	if err := validateMessageReadability("shared", structured, true); err != nil {
		t.Fatalf("structured message rejected: %v", err)
	}
	err := validateMessageReadability("shared", structured, false)
	if err == nil || !strings.Contains(err.Error(), "substantial room posts must use") {
		t.Fatalf("inline substantial message error = %v", err)
	}
	dense := strings.Repeat("Detailed verified evidence without any useful structure. ", 20)
	err = validateMessageReadability("shared", dense, true)
	if err == nil || !strings.Contains(err.Error(), "send shared --stdin") || !strings.Contains(err.Error(), "short Markdown paragraphs or bullets") {
		t.Fatalf("dense message error = %v", err)
	}
}

func waitForSocket(t *testing.T, socket string) {
	t.Helper()
	client := room.Client{SocketPath: socket}
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

func readSnapshot(client room.Client, roomID string) (room.Snapshot, error) {
	var snapshot room.Snapshot
	err := client.Call(context.Background(), "room.snapshot", room.ListMessagesInput{Room: roomID, Limit: 50}, &snapshot)
	return snapshot, err
}
