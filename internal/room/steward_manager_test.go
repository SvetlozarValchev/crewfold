package room

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeStewardRuntime struct {
	mu      sync.Mutex
	state   StewardRuntimeState
	prompts []string
	stopped bool
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
	defer manager.Close()
	steward, err := manager.ConfigureAndStart(ctx, StartStewardInput{Room: created.Room.ID, Handle: "relay-steward"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return runtime.promptCount() >= 1 })
	if first := runtime.prompt(0); !strings.Contains(first, `"/opt/crewfold" room --socket "/run/crewfold.sock" read relay`) {
		t.Fatalf("onboarding does not target exact daemon: %s", first)
	}

	external := t.TempDir()
	if _, err := store.Join(ctx, JoinInput{Room: "relay", Handle: "external", WorkingDirectory: external}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Send(ctx, SendInput{Room: "relay", WorkingDirectory: external, Body: "The slip curves disagree."}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return runtime.promptCount() >= 2 })
	if second := runtime.prompt(1); !strings.Contains(second, "The slip curves disagree.") || !strings.Contains(second, "shared-room records") {
		t.Fatalf("room relay prompt lost the exact event: %s", second)
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
