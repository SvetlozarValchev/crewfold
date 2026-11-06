package room

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestLocalDeliverySkipsAcknowledgedBacklogAfterProviderChange(t *testing.T) {
	ctx := context.Background()
	s, a, _ := boardTestStore(t)
	join := JoinInput{Room: "shared", Handle: a.Handle, WorkingDirectory: a.WorkingDirectory, Delivery: "codex", SessionID: "old-session"}
	if _, err := s.Join(ctx, join); err != nil {
		t.Fatal(err)
	}
	var acknowledged Message
	for range 205 {
		var err error
		acknowledged, err = s.Send(ctx, SendInput{Room: "shared", Owner: true, Body: "Old history"})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Capture the route before the read: a previous agent's slow delivery can
	// leave this route stale even within the same dispatch pass.
	routes, err := s.pendingDeliveries(ctx)
	if err != nil || len(routes) != 1 {
		t.Fatalf("routes: %v %v", routes, err)
	}
	if _, err := s.Ack(ctx, AckInput{Room: "shared", ParticipantID: a.ID, Through: acknowledged.Sequence}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeCodexDelivery{}
	manager := NewDeliveryManager(ctx, s, fake, fake)
	manager.batchQuietPeriod = 0
	manager.deliver(routes[0])
	if len(fake.prompts) != 0 {
		t.Fatal("stale route replayed acknowledged history")
	}
	routes, err = s.pendingDeliveries(ctx)
	if err != nil || len(routes) != 0 {
		t.Fatalf("acknowledged backlog still pending: %v %v", routes, err)
	}
	join.Delivery, join.SessionID = "claude", "new-session"
	if _, err := s.Join(ctx, join); err != nil {
		t.Fatal(err)
	}
	unread, err := s.Send(ctx, SendInput{Room: "shared", Owner: true, Body: "Genuinely unread"})
	if err != nil {
		t.Fatal(err)
	}
	manager.deliverPending()
	if len(fake.prompts) != 1 || fake.targets[0] != "new-session" || !strings.Contains(fake.prompts[0], "--after "+strconv.FormatInt(acknowledged.Sequence, 10)+" --through "+strconv.FormatInt(unread.Sequence, 10)) {
		t.Fatalf("wrong resumed batch: %v", fake.prompts)
	}
	// Delivery can also be ahead of acknowledgement. Do not rewind in that case.
	routes, err = s.pendingDeliveries(ctx)
	if err != nil || len(routes) != 0 {
		t.Fatalf("delivered event replayed: %v %v", routes, err)
	}
	current, err := s.participant(ctx, a.RoomID, a.ID)
	if err != nil || current.LastReadSequence != acknowledged.Sequence {
		t.Fatal("delivery marked unread content as read")
	}
}

func TestRemoteDeliveryUsesLaterAcknowledgedOrDeliveredCursor(t *testing.T) {
	ctx := context.Background()
	s, _, _ := boardTestStore(t)
	if _, err := s.ConfigureNetwork(ctx, ConfigureNetworkInput{Enabled: true, PublicOrigin: "https://crewfold.example.ts.net"}); err != nil {
		t.Fatal(err)
	}
	invite, err := s.CreateNetworkInvite(ctx, CreateNetworkInviteInput{Room: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := s.EnrollNetworkDevice(ctx, NetworkEnrollInput{Invite: invite.Secret, DeviceName: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.AuthenticateNetworkDevice(ctx, enrollment.Token)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.JoinNetworkParticipant(ctx, device, JoinInput{Room: "shared", Handle: "remote-agent", WorkingDirectory: "/project", Delivery: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	var acknowledged Message
	for range 105 {
		acknowledged, err = s.Send(ctx, SendInput{Room: "shared", Owner: true, Body: "Old remote history"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Ack(ctx, AckInput{Room: "shared", ParticipantID: p.ID, Through: acknowledged.Sequence}); err != nil {
		t.Fatal(err)
	}
	batch, err := s.NetworkDeliveryBatch(ctx, device.ID, p.ID)
	if err != nil || len(batch.Messages) != 0 || batch.After != acknowledged.Sequence || batch.Through != acknowledged.Sequence {
		t.Fatalf("acknowledged remote replay: %+v %v", batch, err)
	}
	unread, err := s.Send(ctx, SendInput{Room: "shared", Owner: true, Body: "Unread remote message"})
	if err != nil {
		t.Fatal(err)
	}
	batch, err = s.NetworkDeliveryBatch(ctx, device.ID, p.ID)
	if err != nil || len(batch.Messages) != 1 || batch.Messages[0].Sequence != unread.Sequence || batch.After != acknowledged.Sequence {
		t.Fatalf("wrong remote batch: %+v %v", batch, err)
	}
	if _, err := s.RecordNetworkDelivery(ctx, device.ID, NetworkDeliveryResultInput{ParticipantID: p.ID, Through: batch.Through, Status: "delivered"}); err != nil {
		t.Fatal(err)
	}
	batch, err = s.NetworkDeliveryBatch(ctx, device.ID, p.ID)
	if err != nil || len(batch.Messages) != 0 || batch.After != unread.Sequence {
		t.Fatalf("delivered remote message replayed: %+v %v", batch, err)
	}
}
