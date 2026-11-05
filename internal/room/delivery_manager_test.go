package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCodexDelivery struct {
	mu          sync.Mutex
	prompts     []string
	targets     []string
	validateErr error
	deliverErr  error
}

func (f *fakeCodexDelivery) Validate(context.Context, string) error {
	return f.validateErr
}

func (f *fakeCodexDelivery) Deliver(_ context.Context, threadID, prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, threadID)
	f.prompts = append(f.prompts, prompt)
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
	runtime := &fakeCodexDelivery{}
	manager := NewDeliveryManager(ctx, store, runtime, "/usr/bin/crewfold", "/run/crewfold.sock")
	manager.batchQuietPeriod = 0
	first, err := store.Join(ctx, JoinInput{Room: "shared", Handle: "first", WorkingDirectory: firstDirectory, Delivery: "codex", ThreadID: "thread-one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Join(ctx, JoinInput{Room: "shared", Handle: "second", WorkingDirectory: secondDirectory}); err != nil {
		t.Fatal(err)
	}
	message, err := store.Send(ctx, SendInput{Room: "shared", WorkingDirectory: secondDirectory, Body: "compare the interface contract"})
	if err != nil {
		t.Fatal(err)
	}
	manager.deliverPending()
	if len(runtime.prompts) != 1 || runtime.targets[0] != "thread-one" || !strings.Contains(runtime.prompts[0], `receive "shared" --after 0 --through `+strconv.FormatInt(message.Sequence, 10)) || strings.Contains(runtime.prompts[0], "compare the interface contract") || strings.Contains(runtime.prompts[0], "directly, collectively") || len(runtime.prompts[0]) > 240 {
		t.Fatalf("unexpected delivery: targets=%#v prompts=%#v", runtime.targets, runtime.prompts)
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

func TestFailedCodexQueueLeavesDeliveryPending(t *testing.T) {
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
	runtime := &fakeCodexDelivery{deliverErr: errors.New("queue Codex message: session unavailable")}
	manager := NewDeliveryManager(ctx, store, runtime, "crewfold", "/tmp/crewfold.sock")
	manager.batchQuietPeriod = 0
	manager.deliverPending()
	snapshot, err := store.Snapshot(ctx, "queued", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	delivery := participantNamed(snapshot.Participants, "agent").Delivery
	if delivery == nil || delivery.Status != "error" || delivery.LastDeliveredSequence != 0 || !strings.Contains(delivery.Error, "session unavailable") {
		t.Fatalf("failed delivery = %#v", delivery)
	}
}

func TestCodexDeliveryBatchesBurstsAndBypassesDelayForMentions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	current := time.Now().UTC().Add(-time.Hour)
	store.now = func() time.Time { return current }
	if _, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "batched", Title: "Batched", Topic: "Group nearby events."}); err != nil {
		t.Fatal(err)
	}
	agentDirectory := filepath.Join(t.TempDir(), "agent")
	senderDirectory := filepath.Join(t.TempDir(), "sender")
	for _, directory := range []string{agentDirectory, senderDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Join(ctx, JoinInput{Room: "batched", Handle: "agent", WorkingDirectory: agentDirectory, Delivery: "codex", ThreadID: "thread"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Join(ctx, JoinInput{Room: "batched", Handle: "sender", WorkingDirectory: senderDirectory}); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeCodexDelivery{}
	manager := NewDeliveryManager(ctx, store, runtime, "crewfold", "/tmp/crewfold.sock")
	manager.batchQuietPeriod = 5 * time.Second
	manager.maximumBatchDelay = 30 * time.Second

	if _, err := store.Send(ctx, SendInput{Room: "batched", WorkingDirectory: senderDirectory, Body: "first nearby update"}); err != nil {
		t.Fatal(err)
	}
	manager.deliverPending()
	if len(runtime.prompts) != 0 {
		t.Fatalf("first event bypassed quiet-period batching: %#v", runtime.prompts)
	}

	current = current.Add(2 * time.Second)
	second, err := store.Send(ctx, SendInput{Room: "batched", WorkingDirectory: senderDirectory, Body: "All agents need to report their current state."})
	if err != nil {
		t.Fatal(err)
	}
	manager.deliverPending()
	if len(runtime.prompts) != 0 {
		t.Fatalf("burst delivered before becoming quiet: %#v", runtime.prompts)
	}

	current = current.Add(5 * time.Second)
	manager.deliverPending()
	if len(runtime.prompts) != 1 || !strings.Contains(runtime.prompts[0], `receive "batched" --after 0 --through `+strconv.FormatInt(second.Sequence, 10)) || strings.Contains(runtime.prompts[0], "first nearby update") || strings.Contains(runtime.prompts[0], "All agents need") || len(runtime.prompts[0]) > 240 {
		t.Fatalf("quiet burst was not delivered as one prompt: %#v", runtime.prompts)
	}

	current = current.Add(time.Second)
	direct, err := store.Send(ctx, SendInput{Room: "batched", WorkingDirectory: senderDirectory, Body: "@agent check this now"})
	if err != nil {
		t.Fatal(err)
	}
	manager.deliverPending()
	if len(runtime.prompts) != 2 || !strings.Contains(runtime.prompts[1], `--after `+strconv.FormatInt(second.Sequence, 10)+` --through `+strconv.FormatInt(direct.Sequence, 10)) || strings.Contains(runtime.prompts[1], "check this now") {
		t.Fatalf("direct mention did not bypass batching: %#v", runtime.prompts)
	}

	current = current.Add(time.Second)
	broadcast, err := store.Send(ctx, SendInput{Room: "batched", WorkingDirectory: senderDirectory, Body: "@everyone report current state"})
	if err != nil {
		t.Fatal(err)
	}
	manager.deliverPending()
	if len(runtime.prompts) != 3 || !strings.Contains(runtime.prompts[2], `--after `+strconv.FormatInt(direct.Sequence, 10)+` --through `+strconv.FormatInt(broadcast.Sequence, 10)) || strings.Contains(runtime.prompts[2], "report current state") {
		t.Fatalf("broadcast mention did not bypass batching: %#v", runtime.prompts)
	}
}

func TestMessageNeedsImmediateDeliveryUsesMentionBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		body string
		want bool
	}{
		{body: "@agent report", want: true},
		{body: "Please ask @Agent.", want: true},
		{body: "@everyone report", want: true},
		{body: "All agents need to report", want: false},
		{body: "@agent-helper report", want: false},
		{body: "mail@agent report", want: false},
	}
	for _, test := range cases {
		if got := messageNeedsImmediateDelivery(test.body, "agent"); got != test.want {
			t.Errorf("messageNeedsImmediateDelivery(%q) = %v, want %v", test.body, got, test.want)
		}
	}
}
