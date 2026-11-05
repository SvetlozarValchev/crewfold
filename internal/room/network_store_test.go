package room

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestNetworkBrowserSessionSurvivesStoreRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	store, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "persistent-browser", Title: "Persistent browser"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfigureNetwork(ctx, ConfigureNetworkInput{Enabled: true, PublicOrigin: "https://crewfold.example.ts.net"}); err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateNetworkInvite(ctx, CreateNetworkInviteInput{Room: created.Room.ID})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := store.EnrollNetworkDevice(ctx, NetworkEnrollInput{Invite: invite.Secret, DeviceName: "persistent-device"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateNetworkBrowserSession(ctx, enrollment.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	device, err := store.AuthenticateNetworkBrowserSession(ctx, token)
	if err != nil || device.ID != enrollment.DeviceID {
		t.Fatalf("persistent browser session = %#v, %v", device, err)
	}
}

func TestNetworkEnrollmentParticipantAndDurableDelivery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "remote-room", Title: "Remote room", Topic: "Test authenticated remote collaboration."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "private-room", Title: "Private room"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateNetworkInvite(ctx, CreateNetworkInviteInput{Room: created.Room.ID}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("invite while disabled = %v", err)
	}
	settings, err := store.ConfigureNetwork(ctx, ConfigureNetworkInput{Enabled: true, PublicOrigin: "https://crewfold.example.ts.net"})
	if err != nil || !settings.Enabled {
		t.Fatalf("configure network = %#v, %v", settings, err)
	}
	invite, err := store.CreateNetworkInvite(ctx, CreateNetworkInviteInput{Room: created.Room.ID, ExpiresSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := store.EnrollNetworkDevice(ctx, NetworkEnrollInput{Invite: invite.Secret, DeviceName: "alice-laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Room.ID != created.Room.ID || enrollment.Token == "" {
		t.Fatalf("enrollment = %#v", enrollment)
	}
	if _, err := store.EnrollNetworkDevice(ctx, NetworkEnrollInput{Invite: invite.Secret, DeviceName: "replay"}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("reused invite = %v", err)
	}
	device, err := store.AuthenticateNetworkDevice(ctx, enrollment.Token)
	if err != nil || device.ID != enrollment.DeviceID {
		t.Fatalf("authenticate = %#v, %v", device, err)
	}
	rooms, err := store.NetworkRooms(ctx, device.ID)
	if err != nil || len(rooms) != 1 || rooms[0].ID != created.Room.ID {
		t.Fatalf("network rooms = %#v, %v", rooms, err)
	}
	participant, err := store.JoinNetworkParticipant(ctx, device, JoinInput{
		Room: created.Room.Slug, Handle: "alice-agent", DisplayName: "Alice Agent",
		WorkingDirectory: "/home/alice/project", Delivery: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	if participant.Delivery == nil || participant.Delivery.Kind != "codex" || !strings.Contains(participant.WorkingDirectory, "alice-laptop:/home/alice/project") {
		t.Fatalf("remote participant = %#v", participant)
	}
	identity, err := store.SetNetworkBrowserIdentity(ctx, device.ID, "alice")
	if err != nil || identity.Handle != "alice" {
		t.Fatalf("set browser identity = %#v, %v", identity, err)
	}
	identitySnapshot, err := store.Snapshot(ctx, created.Room.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(identitySnapshot.Participants) != 1 || identitySnapshot.Participants[0].HumanHandle != "alice" {
		t.Fatalf("participant ownership after later human join = %#v", identitySnapshot.Participants)
	}
	identityMessage := identitySnapshot.Messages[len(identitySnapshot.Messages)-1]
	if identityMessage.SenderKind != "member" || identityMessage.Kind != "system" || !strings.Contains(identityMessage.Body, "@alice-agent") {
		t.Fatalf("human identity announcement = %#v", identityMessage)
	}
	identityBatch, err := store.NetworkDeliveryBatch(ctx, device.ID, participant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(identityBatch.Messages) != 1 || identityBatch.Messages[0].ID != identityMessage.ID || identityBatch.Participant.HumanHandle != "alice" {
		t.Fatalf("human identity delivery = %#v", identityBatch)
	}
	if _, err := store.RecordNetworkDelivery(ctx, device.ID, NetworkDeliveryResultInput{ParticipantID: participant.ID, Through: identityBatch.Through, Status: "delivered"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.JoinNetworkParticipant(ctx, device, JoinInput{
		Room: created.Room.Slug, Handle: "alice", WorkingDirectory: "/home/alice/conflict", Delivery: "none",
	}); err == nil || !strings.Contains(err.Error(), "human collaborator") {
		t.Fatalf("agent reused its human handle: %v", err)
	}
	ownerMessage, err := store.Send(ctx, SendInput{Room: created.Room.ID, Owner: true, Body: "@alice-agent please report the current result."})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := store.NetworkDeliveryBatch(ctx, device.ID, participant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.After != identityMessage.Sequence || batch.Through != ownerMessage.Sequence || len(batch.Messages) != 1 || batch.Messages[0].ID != ownerMessage.ID {
		t.Fatalf("delivery batch = %#v", batch)
	}
	binding, err := store.RecordNetworkDelivery(ctx, device.ID, NetworkDeliveryResultInput{ParticipantID: participant.ID, Through: batch.Through, Status: "delivered"})
	if err != nil || binding.LastDeliveredSequence != ownerMessage.Sequence || binding.Status != "delivered" {
		t.Fatalf("delivery acknowledgement = %#v, %v", binding, err)
	}
	reply, err := store.Send(ctx, SendInput{Room: created.Room.ID, ParticipantID: participant.ID, Body: "Current result is ready."})
	if err != nil || reply.SenderHandle != participant.Handle {
		t.Fatalf("remote reply = %#v, %v", reply, err)
	}
	if _, err := store.NetworkDeliveryBatch(ctx, "device_not_owned", participant.ID); err == nil {
		t.Fatal("another device could read the participant delivery")
	}
	secondInvite, err := store.CreateNetworkInvite(ctx, CreateNetworkInviteInput{Room: created.Room.ID})
	if err != nil {
		t.Fatal(err)
	}
	secondEnrollment, err := store.EnrollNetworkDevice(ctx, NetworkEnrollInput{Invite: secondInvite.Secret, DeviceName: "bob-laptop"})
	if err != nil {
		t.Fatal(err)
	}
	secondDevice, err := store.AuthenticateNetworkDevice(ctx, secondEnrollment.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.JoinNetworkParticipant(ctx, secondDevice, JoinInput{
		Room: created.Room.Slug, Handle: participant.Handle, WorkingDirectory: "/home/bob/project", Delivery: "claude",
	}); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("second device took over an existing handle: %v", err)
	}
}
