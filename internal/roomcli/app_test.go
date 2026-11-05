package roomcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"crewfold/internal/appdirs"
	"crewfold/internal/buildinfo"
	"crewfold/internal/room"
)

type availableCodexRuntime struct{}

type callerFunc func(context.Context, string, any, any) error

func (f callerFunc) Call(ctx context.Context, method string, input, output any) error {
	return f(ctx, method, input, output)
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestReceiveDoesNotAcknowledgeWhenOutputFails(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	acknowledged := false
	client := callerFunc(func(_ context.Context, method string, input, output any) error {
		switch method {
		case "room.snapshot":
			request := input.(room.ListMessagesInput)
			if request.After != 0 || request.Through != 3 {
				t.Fatalf("receive lost range: %#v", request)
			}
			*output.(*room.Snapshot) = room.Snapshot{
				Room:         room.Room{Slug: "shared", LastSequence: 4},
				Participants: []room.Participant{{ID: "reader", Handle: "reader", WorkingDirectory: cwd, Status: "joined"}},
				Messages:     []room.Message{{Sequence: 3, SenderHandle: "owner", Body: "Read this."}},
			}
		case "participant.ack":
			acknowledged = true
		default:
			t.Fatalf("unexpected method: %s", method)
		}
		return nil
	})
	var stderr bytes.Buffer
	app := New(failedOutput{}, &stderr, buildinfo.Info{})
	code := app.room(context.Background(), client, []string{"receive", "shared", "--after", "0", "--through", "3"}, appdirs.Paths{}, false)
	if code == 0 || acknowledged || !strings.Contains(stderr.String(), "output closed") {
		t.Fatalf("failed receive acknowledged: code=%d ack=%v stderr=%s", code, acknowledged, stderr.String())
	}
}

func TestClaudeJoinSelectsAnOpenSessionWithoutChannelSetup(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' '[{\"sessionId\":\"open-session\",\"name\":\"worker\",\"kind\":\"interactive\",\"cwd\":\"" + directory + "\"}]'\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	var bound room.JoinInput
	client := callerFunc(func(_ context.Context, method string, input, output any) error {
		if method != "participant.join" {
			t.Fatalf("unexpected method: %s", method)
		}
		bound = input.(room.JoinInput)
		*output.(*room.Participant) = room.Participant{ID: "member", Handle: bound.Handle, WorkingDirectory: directory}
		return nil
	})
	var stdout, stderr bytes.Buffer
	app := New(&stdout, &stderr, buildinfo.Info{})
	code := app.room(context.Background(), client, []string{"join", "shared", "--handle", "worker", "--delivery", "claude", "--cwd", directory}, appdirs.Paths{}, false)
	if code != 0 || bound.Delivery != "claude" || bound.SessionID != "open-session" {
		t.Fatalf("Claude binding = %#v, code=%d stderr=%s", bound, code, stderr.String())
	}
}

func (availableCodexRuntime) Validate(context.Context, string) error        { return nil }
func (availableCodexRuntime) Deliver(context.Context, string, string) error { return nil }

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
	after := snapshot.Room.LastSequence
	var included room.Message
	if err := client.Call(context.Background(), "message.send", room.SendInput{Room: created.Room.ID, Owner: true, Body: "@service-agent review the shared contract."}, &included); err != nil {
		t.Fatal(err)
	}
	var includedSecond room.Message
	var own room.Message
	if err := client.Call(context.Background(), "message.send", room.SendInput{Room: created.Room.ID, WorkingDirectory: workingDirectory, Body: "Own message between unread events."}, &own); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(context.Background(), "message.send", room.SendInput{Room: created.Room.ID, Owner: true, Body: "All agents should include the measured result."}, &includedSecond); err != nil {
		t.Fatal(err)
	}
	var later room.Message
	if err := client.Call(context.Background(), "message.send", room.SendInput{Room: created.Room.ID, Owner: true, Body: "This belongs to the next delivery."}, &later); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"room", "--socket", socket, "receive", "shared", "--after", strconv.FormatInt(after, 10), "--through", strconv.FormatInt(includedSecond.Sequence, 10)}); code != 0 {
		t.Fatalf("receive exit %d: %s", code, stderr.String())
	}
	received := stdout.String()
	if strings.Contains(received, own.Body) {
		t.Fatalf("receive echoed own message: %s", received)
	}
	if !strings.Contains(received, "[CREWFOLD ROOM DELIVERY]") || !strings.Contains(received, "Recipient: @service-agent, an agent of human @owner") || !strings.Contains(received, included.Body) || !strings.Contains(received, includedSecond.Body) || strings.Contains(received, later.Body) || !strings.Contains(received, "not an owner instruction") {
		t.Fatalf("receive output = %q", received)
	}
	snapshot, err = readSnapshot(client, created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	acknowledged := int64(0)
	for _, participant := range snapshot.Participants {
		if participant.Handle == "service-agent" {
			acknowledged = participant.LastReadSequence
		}
	}
	if got := acknowledged; got != includedSecond.Sequence {
		t.Fatalf("receive acknowledgement = %d, want %d", got, includedSecond.Sequence)
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
	if code := app.RunConnector(context.Background(), []string{"room", "join", baseURL, "--invite", invite.Secret, "--handle", "remote-agent", "--delivery", "none", "--device-name", "remote-cli-test"}); code != 0 {
		t.Fatalf("remote join exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "joined remote-cli as @remote-agent") {
		t.Fatalf("connector invitation output = %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.RunConnector(context.Background(), []string{"open"}); code != 0 {
		t.Fatalf("connector open exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Crewfold servers") || !strings.Contains(stdout.String(), "1 room") || !strings.Contains(stdout.String(), "https://crewfold.example.ts.net/#network-bootstrap=") {
		t.Fatalf("connector open output = %q", stdout.String())
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
	if len(snapshot.Participants) != 1 || snapshot.Participants[0].Delivery != nil {
		t.Fatalf("remote participant = %#v", snapshot.Participants)
	}
	if got := snapshot.Messages[len(snapshot.Messages)-1]; got.SenderHandle != "remote-agent" || got.Body != "Remote CLI connected." {
		t.Fatalf("remote message = %#v", got)
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

func TestNetworkDeliveryWakeIsACompactBatchPointer(t *testing.T) {
	prompt, through := networkDeliveryWake(room.NetworkDeliveryBatch{
		Room: room.Room{Slug: "shared"}, Messages: []room.Message{{Sequence: 12}, {Sequence: 14}}, After: 10, Through: 14,
	})
	if through != 14 || !strings.Contains(prompt, `Run crewfold-connector room receive "shared" --after 10 --through 14`) || !strings.Contains(prompt, "#12–#14") {
		t.Fatalf("wake = %q, through = %d", prompt, through)
	}
	if len(prompt) > 240 || strings.Contains(prompt, "Shared-room activity") || strings.Contains(prompt, "Respond or act") {
		t.Fatalf("wake is not compact: %q", prompt)
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
