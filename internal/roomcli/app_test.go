package roomcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/appdirs"
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
	participantID := snapshot.Participants[0].ID

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

	stdout.Reset()
	if code := app.Run(context.Background(), []string{"room", "--socket", socket, "leave", "shared"}); code != 0 {
		t.Fatalf("leave exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "left shared as @service-agent") || !strings.Contains(stdout.String(), "history preserved") {
		t.Fatalf("leave output = %q", stdout.String())
	}
	snapshot, err = readSnapshot(client, created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Participants[0].Status != "left" || snapshot.Participants[0].Delivery == nil {
		t.Fatalf("participant after leave = %#v", snapshot.Participants[0])
	}

	t.Setenv("CODEX_THREAD_ID", "01a09999-7a48-70d2-b540-112ccf94e5bf")
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"room", "--socket", socket, "join", "shared", "--handle", "service-agent"}); code != 0 {
		t.Fatalf("rejoin exit %d: %s", code, stderr.String())
	}
	snapshot, err = readSnapshot(client, created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Participants[0].ID != participantID || snapshot.Participants[0].Status != "joined" || snapshot.Participants[0].Delivery == nil || snapshot.Participants[0].Delivery.Target != "01a09999-7a48-70d2-b540-112ccf94e5bf" {
		t.Fatalf("participant after rejoin = %#v", snapshot.Participants[0])
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

func TestRemoteConnectJoinAndRoomCommandsRouteThroughHub(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "hub-state")
	socket := filepath.Join(root, "hub-runtime", "crewfold.sock")
	serverContext, stopServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- room.RunServer(serverContext, room.ServerConfig{
			DataDir: dataDir, SocketPath: socket, WebAddress: "127.0.0.1:0",
			Version: buildinfo.Info{Version: "test"}, CodexRuntime: availableCodexRuntime{},
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
	waitForSocket(t, filepath.Join(dataDir, "web-address"))
	address, err := os.ReadFile(filepath.Join(dataDir, "web-address"))
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "http://" + strings.TrimSpace(string(address))
	local := room.Client{SocketPath: socket}
	var created room.Snapshot
	if err := local.Call(context.Background(), "room.create", room.CreateRoomInput{Slug: "remote-cli", Title: "Remote CLI"}, &created); err != nil {
		t.Fatal(err)
	}
	var settings room.NetworkSettings
	if err := local.Call(context.Background(), "network.configure", room.ConfigureNetworkInput{Enabled: true, PublicOrigin: "https://crewfold.example.ts.net"}, &settings); err != nil {
		t.Fatal(err)
	}
	var invite room.NetworkInvite
	if err := local.Call(context.Background(), "network.invite.create", room.CreateNetworkInviteInput{Room: created.Room.ID}, &invite); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	runtimeRoot, err := os.MkdirTemp("/tmp", "crewfold-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	workspace := filepath.Join(root, "remote-workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDirectory) })
	var stdout, stderr bytes.Buffer
	app := NewConnector(&stdout, &stderr, buildinfo.Info{Version: "test"})
	if code := app.RunConnector(context.Background(), []string{"room", "join", baseURL, "--invite", invite.Secret, "--handle", "claude-agent", "--delivery", "claude", "--device-name", "remote-cli-test"}); code != 0 {
		t.Fatalf("remote join exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "joined remote-cli as @claude-agent") {
		t.Fatalf("connector invitation output = %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.RunConnector(context.Background(), []string{"room", "send", "remote-cli", "Remote CLI connected."}); code != 0 {
		t.Fatalf("remote send exit %d: %s", code, stderr.String())
	}
	snapshot, err := readSnapshot(local, created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Participants) != 1 || snapshot.Participants[0].Delivery == nil || snapshot.Participants[0].Delivery.Kind != "claude" {
		t.Fatalf("remote participant = %#v", snapshot.Participants)
	}
	if got := snapshot.Messages[len(snapshot.Messages)-1]; got.SenderHandle != "claude-agent" || got.Body != "Remote CLI connected." {
		t.Fatalf("remote message = %#v", got)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.RunConnector(context.Background(), []string{"channel", "setup", "remote-cli"}); code != 0 {
		t.Fatalf("channel setup exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "--dangerously-load-development-channels server:crewfold") {
		t.Fatalf("channel setup output = %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.RunConnector(context.Background(), []string{"room", "leave", "remote-cli"}); code != 0 {
		t.Fatalf("remote leave exit %d: %s", code, stderr.String())
	}
	paths, err := appdirs.Default()
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadRemoteConfig(paths.ConnectorConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if config == nil || len(config.Hubs) != 1 || len(config.Hubs[0].Bindings) != 0 {
		t.Fatalf("remote bindings after leave = %#v", config)
	}
}

func TestConnectorHelpAndCommandSurfaceExcludeOwnerOperations(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	var stdout, stderr bytes.Buffer
	app := NewConnector(&stdout, &stderr, buildinfo.Info{Version: "test"})
	if code := app.RunConnector(context.Background(), []string{"help"}); code != 0 {
		t.Fatalf("help exit %d: %s", code, stderr.String())
	}
	help := stdout.String()
	for _, forbidden := range []string{"service install", "daemon run", "network enable", "room create", "room steward"} {
		if strings.Contains(help, forbidden) {
			t.Fatalf("connector help exposes %q: %s", forbidden, help)
		}
	}
	if !strings.Contains(help, "does not run a Crewfold hub") || !strings.Contains(help, "crewfold-connector setup") || !strings.Contains(help, "crewfold-connector service") {
		t.Fatalf("connector help = %q", help)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.RunConnector(context.Background(), []string{"service", "install"}); code == 0 || !strings.Contains(stderr.String(), "service start|stop|restart|status") {
		t.Fatalf("owner command exit %d, stderr %q", code, stderr.String())
	}
}

func TestClaudeChannelDeclaresPushCapabilityAndTools(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}, "\n") + "\n"
	var stdout, stderr bytes.Buffer
	err := runClaudeChannel(context.Background(), remoteClaudeBackend{member: remoteBinding{Room: "room", Handle: "agent"}}, strings.NewReader(input), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("MCP output = %q", stdout.String())
	}
	var initialized map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &initialized); err != nil {
		t.Fatal(err)
	}
	result := initialized["result"].(map[string]any)
	capabilities := result["capabilities"].(map[string]any)
	experimental := capabilities["experimental"].(map[string]any)
	if _, exists := experimental["claude/channel"]; !exists {
		t.Fatalf("channel capability = %#v", initialized)
	}
	var tools map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &tools); err != nil {
		t.Fatal(err)
	}
	listed := tools["result"].(map[string]any)["tools"].([]any)
	if len(listed) != 3 {
		t.Fatalf("tools = %#v", listed)
	}
}

func TestLocalClaudeChannelSetupUsesOwnerSocketWithoutRemoteProfile(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "runtime", "crewfold.sock")
	serverContext, stopServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- room.RunServer(serverContext, room.ServerConfig{
			DataDir: filepath.Join(root, "state"), SocketPath: socket, WebAddress: "127.0.0.1:0",
			Version: buildinfo.Info{Version: "test"}, CodexRuntime: availableCodexRuntime{},
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
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "client-state"))
	workspace := filepath.Join(root, "claude-workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDirectory) })
	client := room.Client{SocketPath: socket}
	var created room.Snapshot
	if err := client.Call(context.Background(), "room.create", room.CreateRoomInput{Slug: "local-claude", Title: "Local Claude"}, &created); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := New(&stdout, &stderr, buildinfo.Info{Version: "test"})
	if code := app.Run(context.Background(), []string{"room", "--socket", socket, "join", "local-claude", "--handle", "local-claude", "--delivery", "claude"}); code != 0 {
		t.Fatalf("local Claude join exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Claude Channel ready") || !strings.Contains(stdout.String(), "claude --continue") {
		t.Fatalf("local Claude join did not provide its resume command: %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"channel", "--socket", socket, "setup", "local-claude"}); code != 0 {
		t.Fatalf("local Claude setup exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "--dangerously-load-development-channels server:crewfold") {
		t.Fatalf("local Claude setup output = %q", stdout.String())
	}
	paths, err := appdirs.Default()
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := filepath.Glob(filepath.Join(paths.ConfigDir, "channels", "*.binding.json"))
	if err != nil || len(bindings) != 1 {
		t.Fatalf("local Claude bindings = %#v, %v", bindings, err)
	}
	binding, err := loadLocalClaudeBinding(bindings[0])
	if err != nil {
		t.Fatal(err)
	}
	if binding.Socket != socket || binding.RoomID != created.Room.ID || binding.Directory != workspace {
		t.Fatalf("local Claude binding = %#v", binding)
	}
	mcpContent, err := os.ReadFile(filepath.Join(paths.ConfigDir, "channels", binding.ParticipantID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mcpContent), `"--binding"`) || !strings.Contains(string(mcpContent), bindings[0]) {
		t.Fatalf("local Claude MCP config = %s", mcpContent)
	}
}

func TestNetworkDeliveryPromptDoesNotAcknowledgeOmittedMessages(t *testing.T) {
	messages := make([]room.Message, 0, 5)
	for sequence := int64(1); sequence <= 5; sequence++ {
		messages = append(messages, room.Message{Sequence: sequence, SenderHandle: "peer", Body: strings.Repeat(string(rune('a'+sequence)), 4000)})
	}
	prompt, through := networkDeliveryPrompt(room.NetworkDeliveryBatch{
		Room: room.Room{Slug: "shared"}, Participant: room.Participant{Handle: "receiver"}, Messages: messages, Through: 5,
	})
	if through >= 5 {
		t.Fatalf("acknowledgement advanced through omitted content: %d", through)
	}
	if through < 1 || !strings.Contains(prompt, fmt.Sprintf("#%d", through)) {
		t.Fatalf("prompt cursor = %d, prompt = %q", through, prompt)
	}
	if strings.Contains(prompt, "#5 · @peer") {
		t.Fatal("prompt unexpectedly included the final message")
	}
}

func TestSnapshotCountsLogicalDocumentsSeparatelyFromRevisions(t *testing.T) {
	var output bytes.Buffer
	printSnapshot(&output, room.Snapshot{
		Room: room.Room{Title: "Shared", Status: "open", LastSequence: 12},
		Documents: []room.Document{
			{Name: "requirements.md", ParticipantID: "member-one"},
			{Name: "requirements.md", ParticipantID: "member-one"},
			{Name: "architecture.md", ParticipantID: "member-one"},
			{ID: "doc_archived", Name: "requirements.md", ParticipantID: "member-two", ArchivedAt: "2100-01-01T00:00:00Z"},
		},
	}, false)
	if !strings.Contains(output.String(), "3 documents · 4 revisions · 12 messages · 1 archived") || !strings.Contains(output.String(), "archived\t@owner\trequirements.md\t1 revision\tdoc_archived") {
		t.Fatalf("snapshot summary = %q", output.String())
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
