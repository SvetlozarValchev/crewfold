package room

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/buildinfo"
)

func TestNetworkHTTPWorkflowUsesRoomScopedDeviceIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "state")
	socket := filepath.Join(root, "runtime", "crewfold.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunServer(ctx, ServerConfig{DataDir: dataDir, SocketPath: socket, WebAddress: "127.0.0.1:0", Version: buildinfo.Info{Version: "test"}})
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
	waitForTestPath(t, socket)
	waitForTestPath(t, filepath.Join(dataDir, "web-address"))
	addressBytes, err := os.ReadFile(filepath.Join(dataDir, "web-address"))
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "http://" + strings.TrimSpace(string(addressBytes))
	local := Client{SocketPath: socket}
	var created Snapshot
	if err := local.Call(ctx, "room.create", CreateRoomInput{Slug: "tailnet-test", Title: "Tailnet test"}, &created); err != nil {
		t.Fatal(err)
	}
	var settings NetworkSettings
	if err := local.Call(ctx, "network.configure", ConfigureNetworkInput{Enabled: true, PublicOrigin: "https://crewfold.example.ts.net"}, &settings); err != nil {
		t.Fatal(err)
	}
	var invite NetworkInvite
	if err := local.Call(ctx, "network.invite.create", CreateNetworkInviteInput{Room: created.Room.ID}, &invite); err != nil {
		t.Fatal(err)
	}
	network := NetworkClient{BaseURL: baseURL}
	enrollment, err := network.Enroll(ctx, NetworkEnrollInput{Invite: invite.Secret, DeviceName: "remote-test"})
	if err != nil {
		t.Fatal(err)
	}
	network.Token = enrollment.Token
	var participant Participant
	if err := network.Call(ctx, "participant.join", JoinInput{Room: created.Room.Slug, Handle: "remote-agent", WorkingDirectory: "/remote/project", Delivery: "codex"}, &participant); err != nil {
		t.Fatal(err)
	}
	network.ParticipantID = participant.ID
	var ownerMessage Message
	if err := local.Call(ctx, "message.send", SendInput{Room: created.Room.ID, Owner: true, Body: "@remote-agent inspect the network path."}, &ownerMessage); err != nil {
		t.Fatal(err)
	}
	var batch NetworkDeliveryBatch
	if err := network.Call(ctx, "delivery.poll", NetworkDeliveryPollInput{ParticipantID: participant.ID}, &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Messages) != 1 || batch.Messages[0].ID != ownerMessage.ID {
		t.Fatalf("network batch = %#v", batch)
	}
	var binding NetworkBinding
	if err := network.Call(ctx, "delivery.report", NetworkDeliveryResultInput{ParticipantID: participant.ID, Through: batch.Through, Status: "delivered"}, &binding); err != nil {
		t.Fatal(err)
	}
	var reply Message
	if err := network.Call(ctx, "message.send", SendInput{Room: created.Room.ID, Body: "The remote path works."}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.SenderHandle != "remote-agent" {
		t.Fatalf("reply sender = %#v", reply)
	}
	var left Participant
	if err := network.Call(ctx, "participant.leave", LeaveInput{Room: created.Room.ID}, &left); err != nil {
		t.Fatal(err)
	}
	if err := network.Call(ctx, "message.send", SendInput{Room: created.Room.ID, Body: "This must not be published."}, &reply); err == nil || !strings.Contains(err.Error(), "has left") {
		t.Fatalf("left participant could still publish: %v", err)
	}
	var forbidden any
	if err := network.Call(ctx, "room.create", CreateRoomInput{Slug: "forbidden", Title: "Forbidden"}, &forbidden); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("remote owner operation = %v", err)
	}
	unauthorized := NetworkClient{BaseURL: baseURL, Token: strings.Repeat("0", 64)}
	if err := unauthorized.Call(ctx, "room.list", map[string]any{}, &forbidden); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("invalid credential = %v", err)
	}
}

func waitForTestPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("path was not created: %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
