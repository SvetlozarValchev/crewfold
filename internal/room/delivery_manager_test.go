package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"crewfold/internal/codexapp"
)

type fakeCodexDelivery struct {
	mu         sync.Mutex
	threads    map[string]codexapp.Thread
	prompts    []string
	targets    []string
	messageIDs []string
	deliverErr error
}

func (f *fakeCodexDelivery) Inspect(_ context.Context, threadID string) (codexapp.Thread, error) {
	thread, ok := f.threads[threadID]
	if !ok {
		return codexapp.Thread{}, errors.New("thread not found")
	}
	return thread, nil
}

func (f *fakeCodexDelivery) Deliver(_ context.Context, threadID, prompt, messageID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, threadID)
	f.prompts = append(f.prompts, prompt)
	f.messageIDs = append(f.messageIDs, messageID)
	return f.deliverErr
}

func TestCodexDeliveryIsDurableAndRebindsTheSameParticipant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "shared", Title: "Shared", Topic: "Coordinate two sessions."}); err != nil {
		t.Fatal(err)
	}
	firstDirectory := filepath.Join(t.TempDir(), "first")
	secondDirectory := filepath.Join(t.TempDir(), "second")
	for _, directory := range []string{firstDirectory, secondDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	available := true
	runtime := &fakeCodexDelivery{threads: map[string]codexapp.Thread{
		"thread-one": {ID: "thread-one", Status: codexapp.ThreadStatus{Type: "idle"}, CanAcceptDirectInput: &available},
		"thread-two": {ID: "thread-two", Status: codexapp.ThreadStatus{Type: "idle"}, CanAcceptDirectInput: &available},
	}}
	manager := NewDeliveryManager(ctx, store, runtime, "/usr/bin/crewfold", "/run/crewfold.sock")
	first, err := store.Join(ctx, JoinInput{Room: "shared", Handle: "first", WorkingDirectory: firstDirectory, Delivery: "codex", ThreadID: "thread-one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Join(ctx, JoinInput{Room: "shared", Handle: "second", WorkingDirectory: secondDirectory}); err != nil {
		t.Fatal(err)
	}
	message, err := store.Send(ctx, SendInput{Room: "shared", WorkingDirectory: secondDirectory, Body: "compare the slip curve"})
	if err != nil {
		t.Fatal(err)
	}
	manager.deliverPending()
	if len(runtime.prompts) != 1 || runtime.targets[0] != "thread-one" || !strings.Contains(runtime.prompts[0], "compare the slip curve") || !strings.Contains(runtime.prompts[0], "Do not poll") {
		t.Fatalf("unexpected delivery: targets=%#v prompts=%#v", runtime.targets, runtime.prompts)
	}
	if runtime.messageIDs[0] != "crewfold:"+first.ID+":"+fmt.Sprint(message.Sequence) {
		t.Fatalf("delivery id = %q", runtime.messageIDs[0])
	}
	snapshot, err := store.Snapshot(ctx, "shared", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	delivery := participantNamed(snapshot.Participants, first.Handle).Delivery
	if delivery == nil || delivery.Status != "delivered" || delivery.LastDeliveredSequence != message.Sequence {
		t.Fatalf("delivery state = %#v", delivery)
	}

	rebound, err := store.Join(ctx, JoinInput{Room: "shared", Handle: "first", WorkingDirectory: firstDirectory, Delivery: "codex", ThreadID: "thread-two"})
	if err != nil {
		t.Fatal(err)
	}
	if rebound.ID != first.ID || rebound.Delivery == nil || rebound.Delivery.Target != "thread-two" || rebound.Delivery.LastDeliveredSequence != message.Sequence {
		t.Fatalf("rebound participant = %#v", rebound)
	}
}

func TestUnavailableCodexThreadLeavesDeliveryQueued(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "queued", Title: "Queued", Topic: "Retain delivery."}); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "participant")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Join(ctx, JoinInput{Room: "queued", Handle: "agent", WorkingDirectory: directory, Delivery: "codex", ThreadID: "offline"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Send(ctx, SendInput{Room: "queued", Owner: true, Body: "new evidence"}); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeCodexDelivery{threads: map[string]codexapp.Thread{}, deliverErr: codexapp.ErrThreadNotLoaded}
	manager := NewDeliveryManager(ctx, store, runtime, "crewfold", "/tmp/crewfold.sock")
	// Validation is deliberately separate from delivery. Simulate a route that
	// was valid when it joined but whose terminal is now unloaded.
	runtime.threads["offline"] = codexapp.Thread{ID: "offline", Status: codexapp.ThreadStatus{Type: "notLoaded"}}
	manager.deliverPending()
	snapshot, err := store.Snapshot(ctx, "queued", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	delivery := participantNamed(snapshot.Participants, "agent").Delivery
	if delivery == nil || delivery.Status != "queued" || delivery.LastDeliveredSequence != 0 || !strings.Contains(delivery.Error, "not currently loaded") {
		t.Fatalf("queued delivery = %#v", delivery)
	}
}
