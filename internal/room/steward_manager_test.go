package room

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeStewardRuntime struct {
	mu                  sync.Mutex
	state               StewardRuntimeState
	prompts             []string
	stopped             bool
	blockDeliveryNumber int
	deliveryGate        chan struct{}
	deliveryStarted     chan int
}

func (f *fakeStewardRuntime) Ensure(context.Context, HostedSteward, Room) (StewardRuntimeState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = StewardRuntimeState{WorkspaceID: "w1", PaneID: "w1:p1", AgentStatus: "idle", Output: "real terminal"}
	return f.state, nil
}
func (f *fakeStewardRuntime) Inspect(context.Context, HostedSteward) (StewardRuntimeState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}
func (f *fakeStewardRuntime) Prompt(_ context.Context, _ HostedSteward, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, text)
	return nil
}
func (f *fakeStewardRuntime) Deliver(ctx context.Context, _ HostedSteward, text string) error {
	f.mu.Lock()
	f.prompts = append(f.prompts, text)
	number := len(f.prompts)
	gate := f.deliveryGate
	block := f.blockDeliveryNumber == number && gate != nil
	started := f.deliveryStarted
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- number:
		default:
		}
	}
	if block {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-gate:
		}
	}
	return nil
}
func (f *fakeStewardRuntime) SendKey(context.Context, HostedSteward, string) error { return nil }
func (f *fakeStewardRuntime) Stop(context.Context, HostedSteward) error {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
	return nil
}
func (f *fakeStewardRuntime) Close() {}
func (f *fakeStewardRuntime) promptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}
func (f *fakeStewardRuntime) prompt(index int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prompts[index]
}

func TestHostedStewardManagerStartsAndRelaysRoomActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "relay", Title: "Relay", Topic: "Keep independent sessions aligned."})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeStewardRuntime{}
	manager := NewStewardManager(ctx, store, runtime, "/opt/crewfold", "/run/crewfold.sock")
	manager.pollInterval = 10 * time.Millisecond
	manager.batchQuietPeriod = 100 * time.Millisecond
	manager.maximumBatchDelay = time.Second
	defer manager.Close()
	steward, err := manager.ConfigureAndStart(ctx, StartStewardInput{Room: created.Room.ID, Handle: "relay-steward"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return runtime.promptCount() >= 1 })
	if first := runtime.prompt(0); !strings.Contains(first, `"/opt/crewfold" room --socket "/run/crewfold.sock" read relay`) || !strings.Contains(first, "send relay --stdin") || !strings.Contains(first, "GitHub-flavored Markdown") || !strings.Contains(first, "never by an internal document ID") {
		t.Fatalf("onboarding does not target exact daemon: %s", first)
	}
	initialState, err := store.HostedSteward(ctx, "relay")
	if err != nil {
		t.Fatal(err)
	}
	initialReader, err := store.participant(ctx, created.Room.ID, steward.ParticipantID)
	if err != nil {
		t.Fatal(err)
	}
	if initialState.InitializedAt == "" || initialReader.LastReadSequence != initialState.LastDeliveredSequence || initialReader.UnreadCount != 0 {
		t.Fatalf("completed onboarding did not establish a clean delivery baseline: steward=%#v participant=%#v", initialState, initialReader)
	}

	external := t.TempDir()
	if _, err := store.Join(ctx, JoinInput{Room: "relay", Handle: "external", WorkingDirectory: external}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Send(ctx, SendInput{Room: "relay", WorkingDirectory: external, Body: "The interface contracts disagree."}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Send(ctx, SendInput{Room: "relay", WorkingDirectory: external, Body: "The participants resolved the contract and established the current boundary."}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return runtime.promptCount() >= 2 })
	if second := runtime.prompt(1); !strings.Contains(second, "[CREWFOLD ROOM DELTA]") || !strings.Contains(second, "Room: relay") || !strings.Contains(second, "Sequence: #") || !strings.Contains(second, "Explicitly addressed: no") || !strings.Contains(second, "The interface contracts disagree.") || !strings.Contains(second, "participants resolved the contract") || !strings.Contains(second, "preserve a material correction") || !strings.Contains(second, "never by internal document ID") || !strings.Contains(second, "NO_ROOM_ACTION") {
		t.Fatalf("room relay prompt lost the exact event: %s", second)
	}
	if second := runtime.prompt(1); strings.Contains(second, "Use the CLI from this directory") || strings.Contains(second, "send relay --stdin") || strings.Contains(second, "document relay DOCUMENT-ID") {
		t.Fatalf("room relay repeated the onboarding policy: %s", second)
	}
	stewardState, err := store.HostedSteward(ctx, "relay")
	if err != nil {
		t.Fatal(err)
	}
	participant, err := store.participant(ctx, created.Room.ID, steward.ParticipantID)
	if err != nil {
		t.Fatal(err)
	}
	if participant.LastReadSequence != stewardState.LastDeliveredSequence || participant.UnreadCount != 0 {
		t.Fatalf("completed steward delivery was not acknowledged: steward=%#v participant=%#v", stewardState, participant)
	}
	if _, err := store.Send(ctx, SendInput{Room: "relay", WorkingDirectory: external, Body: "@relay-steward please arbitrate this contradiction."}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return runtime.promptCount() >= 3 })
	if third := runtime.prompt(2); !strings.Contains(third, "Explicitly addressed: yes") || !strings.Contains(third, "@relay-steward please arbitrate this contradiction.") {
		t.Fatalf("addressed relay prompt = %s", third)
	}
	console, err := manager.Status(ctx, "relay")
	if err != nil || console.Output != "real terminal" || console.Steward.ParticipantID != steward.ParticipantID {
		t.Fatalf("unexpected steward console: %#v, %v", console, err)
	}
	if _, err := manager.Stop(ctx, "relay"); err != nil {
		t.Fatal(err)
	}
	if !runtime.stopped {
		t.Fatal("runtime was not stopped")
	}
}

func TestShouldWaitForStewardBatch(t *testing.T) {
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	quiet := 5 * time.Second
	maximum := 30 * time.Second
	cases := []struct {
		name  string
		first time.Time
		last  time.Time
		wait  bool
	}{
		{name: "collect active burst", first: now.Add(-10 * time.Second), last: now.Add(-time.Second), wait: true},
		{name: "deliver after quiet period", first: now.Add(-10 * time.Second), last: now.Add(-quiet), wait: false},
		{name: "deliver at maximum delay", first: now.Add(-maximum), last: now.Add(-time.Second), wait: false},
		{name: "deliver when timestamps are unavailable", wait: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldWaitForStewardBatch(now, test.first, test.last, quiet, maximum); got != test.wait {
				t.Fatalf("shouldWaitForStewardBatch() = %v, want %v", got, test.wait)
			}
		})
	}
}

func TestHostedStewardDeltaIsBoundedWithoutDroppingEvents(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "bounded", Title: "Bounded", Topic: "Deliver large findings without flooding one turn."})
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if _, err := store.Join(ctx, JoinInput{Room: "bounded", Handle: "external", WorkingDirectory: external}); err != nil {
		t.Fatal(err)
	}
	steward, err := store.ConfigureHostedSteward(ctx, StartStewardInput{Room: created.Room.ID, Handle: "bounded-steward"})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := store.Snapshot(ctx, created.Room.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.completeHostedStewardOnboarding(ctx, created.Room.ID, baseline.Room.LastSequence); err != nil {
		t.Fatal(err)
	}

	messages := make([]Message, 0, 3)
	for index, marker := range []string{"FIRST", "SECOND", "THIRD"} {
		body := "## " + marker + " finding\n\n- " + strings.Repeat(string(rune('a'+index)), 11000)
		message, sendErr := store.Send(ctx, SendInput{Room: "bounded", WorkingDirectory: external, Body: body})
		if sendErr != nil {
			t.Fatal(sendErr)
		}
		messages = append(messages, message)
	}

	runtime := &fakeStewardRuntime{state: StewardRuntimeState{WorkspaceID: "w1", PaneID: "w1:p1", AgentStatus: "idle"}}
	manager := NewStewardManager(ctx, store, runtime, "/opt/crewfold", "/run/crewfold.sock")
	manager.batchQuietPeriod = 0
	manager.maximumBatchDelay = 0
	defer manager.Close()
	manager.deliverOnce(ctx, created.Room.ID)
	if runtime.promptCount() != 1 {
		t.Fatalf("first bounded delivery produced %d prompts", runtime.promptCount())
	}
	firstPrompt := runtime.prompt(0)
	if !strings.Contains(firstPrompt, "FIRST finding") || !strings.Contains(firstPrompt, "SECOND finding") || strings.Contains(firstPrompt, "THIRD finding") {
		t.Fatalf("first bounded delivery split at the wrong event")
	}
	state, err := store.HostedSteward(ctx, created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastDeliveredSequence != messages[1].Sequence {
		t.Fatalf("bounded delivery advanced through %d, want %d", state.LastDeliveredSequence, messages[1].Sequence)
	}

	manager.deliverOnce(ctx, created.Room.ID)
	if runtime.promptCount() != 2 || !strings.Contains(runtime.prompt(1), "THIRD finding") {
		t.Fatal("remaining event was not delivered in the next bounded turn")
	}
	state, err = store.HostedSteward(ctx, created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastDeliveredSequence != messages[2].Sequence || state.ParticipantID != steward.ParticipantID {
		t.Fatalf("final bounded delivery state = %#v", state)
	}
}

func TestHostedStewardDeliveryAdvancesOnlyAfterCompletedTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "completion", Title: "Completion", Topic: "Retain exact delivery until the steward finishes."})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeStewardRuntime{
		blockDeliveryNumber: 2,
		deliveryGate:        make(chan struct{}),
		deliveryStarted:     make(chan int, 4),
	}
	manager := NewStewardManager(ctx, store, runtime, "/opt/crewfold", "/run/crewfold.sock")
	manager.pollInterval = 10 * time.Millisecond
	manager.batchQuietPeriod = time.Hour
	manager.maximumBatchDelay = time.Hour
	defer manager.Close()
	steward, err := manager.ConfigureAndStart(ctx, StartStewardInput{Room: created.Room.ID, Handle: "completion-steward"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return runtime.promptCount() >= 1 })

	external := t.TempDir()
	if _, err := store.Join(ctx, JoinInput{Room: "completion", Handle: "external", WorkingDirectory: external}); err != nil {
		t.Fatal(err)
	}
	message, err := store.Send(ctx, SendInput{Room: "completion", WorkingDirectory: external, Body: "@completion-steward record this material correction."})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		select {
		case number := <-runtime.deliveryStarted:
			return number == 2
		default:
			return false
		}
	})
	before, err := store.HostedSteward(ctx, "completion")
	if err != nil {
		t.Fatal(err)
	}
	readerBefore, err := store.participant(ctx, created.Room.ID, steward.ParticipantID)
	if err != nil {
		t.Fatal(err)
	}
	if before.LastDeliveredSequence >= message.Sequence || readerBefore.LastReadSequence >= message.Sequence {
		t.Fatalf("delivery advanced while steward turn was still running: steward=%#v participant=%#v", before, readerBefore)
	}
	close(runtime.deliveryGate)
	waitFor(t, 3*time.Second, func() bool {
		completed, readErr := store.HostedSteward(ctx, "completion")
		return readErr == nil && completed.LastDeliveredSequence >= message.Sequence
	})
	readerAfter, err := store.participant(ctx, created.Room.ID, steward.ParticipantID)
	if err != nil {
		t.Fatal(err)
	}
	if readerAfter.LastReadSequence < message.Sequence || readerAfter.UnreadCount != 0 {
		t.Fatalf("completed turn did not acknowledge its delta: %#v", readerAfter)
	}
}

func TestHostedStewardManagerResumesWithoutRepeatingOnboarding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "resume", Title: "Resume", Topic: "Keep one persistent steward."})
	if err != nil {
		t.Fatal(err)
	}
	firstRuntime := &fakeStewardRuntime{}
	firstManager := NewStewardManager(ctx, store, firstRuntime, "/opt/crewfold", "/run/crewfold.sock")
	if _, err := firstManager.ConfigureAndStart(ctx, StartStewardInput{Room: created.Room.ID, Handle: "resume-steward"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		steward, readErr := store.HostedSteward(ctx, created.Room.ID)
		return readErr == nil && steward.InitializedAt != ""
	})
	firstManager.Close()

	secondRuntime := &fakeStewardRuntime{}
	secondManager := NewStewardManager(ctx, store, secondRuntime, "/opt/crewfold", "/run/crewfold.sock")
	defer secondManager.Close()
	if err := secondManager.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		steward, readErr := store.HostedSteward(ctx, created.Room.ID)
		return readErr == nil && steward.Status == "running" && steward.AgentStatus == "idle"
	})
	time.Sleep(100 * time.Millisecond)
	if count := secondRuntime.promptCount(); count != 0 {
		t.Fatalf("manager restart repeated steward onboarding with %d prompt(s)", count)
	}
}

func waitFor(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become ready")
}
