package room

import (
	"context"
	"strings"
	"testing"
)

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
	ownerMessage, err := store.Send(ctx, SendInput{Room: created.Room.ID, Owner: true, Body: "@alice-agent please report the current result."})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := store.NetworkDeliveryBatch(ctx, device.ID, participant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Through != ownerMessage.Sequence || len(batch.Messages) != 1 || batch.Messages[0].ID != ownerMessage.ID {
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
