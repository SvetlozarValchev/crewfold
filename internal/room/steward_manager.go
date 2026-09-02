package room

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type StewardManager struct {
	store      *Store
	runtime    StewardRuntime
	ctx        context.Context
	cancel     context.CancelFunc
	cliPath    string
	socketPath string
	mu         sync.Mutex
	loops      map[string]context.CancelFunc
}

func NewStewardManager(parent context.Context, store *Store, runtime StewardRuntime, cliPath, socketPath string) *StewardManager {
	ctx, cancel := context.WithCancel(parent)
	return &StewardManager{store: store, runtime: runtime, ctx: ctx, cancel: cancel, cliPath: cliPath, socketPath: socketPath, loops: map[string]context.CancelFunc{}}
}

func (m *StewardManager) Start() error {
	stewards, err := m.store.desiredHostedStewards(m.ctx)
	if err != nil {
		return err
	}
	for _, steward := range stewards {
		go m.launch(steward.RoomID)
	}
	return nil
}

func (m *StewardManager) Close() {
	m.cancel()
	m.mu.Lock()
	for _, cancel := range m.loops {
		cancel()
	}
	m.loops = map[string]context.CancelFunc{}
	m.mu.Unlock()
	m.runtime.Close()
}

func (m *StewardManager) ConfigureAndStart(ctx context.Context, input StartStewardInput) (HostedSteward, error) {
	steward, err := m.store.ConfigureHostedSteward(ctx, input)
	if err != nil {
		return HostedSteward{}, err
	}
	go m.launch(steward.RoomID)
	return steward, nil
}

func (m *StewardManager) Status(ctx context.Context, roomIdentifier string) (*StewardConsole, error) {
	steward, err := m.store.HostedSteward(ctx, roomIdentifier)
	if err != nil {
		return nil, err
	}
	console := &StewardConsole{Steward: *steward}
	if steward.Status != "running" {
		return console, nil
	}
	inspectCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	state, err := m.runtime.Inspect(inspectCtx, *steward)
	if err != nil {
		_ = m.store.recordHostedStewardObservation(context.Background(), steward.RoomID, "failed", "unknown", err.Error())
		steward.Status, steward.AgentStatus, steward.Error = "failed", "unknown", err.Error()
		console.Steward = *steward
		return console, nil
	}
	_ = m.store.recordHostedStewardRunning(ctx, steward.RoomID, state.WorkspaceID, state.PaneID, state.AgentStatus)
	steward.Status, steward.AgentStatus = "running", state.AgentStatus
	steward.HerdrWorkspaceID, steward.HerdrPaneID, steward.Error = state.WorkspaceID, state.PaneID, ""
	console.Steward, console.Output = *steward, state.Output
	return console, nil
}

func (m *StewardManager) Prompt(ctx context.Context, input PromptStewardInput) error {
	text, err := boundedText("steward prompt", input.Text, 1, 16384)
	if err != nil {
		return err
	}
	steward, err := m.store.HostedSteward(ctx, input.Room)
	if err != nil {
		return err
	}
	if steward.Status != "running" {
		return errors.New("hosted steward is not running")
	}
	state, err := m.runtime.Inspect(ctx, *steward)
	if err != nil {
		return err
	}
	if state.AgentStatus != "idle" && state.AgentStatus != "done" {
		return fmt.Errorf("hosted steward is %s; wait for the current turn or interrupt it", state.AgentStatus)
	}
	return m.runtime.Prompt(ctx, *steward, text)
}

func (m *StewardManager) SendKey(ctx context.Context, input StewardKeyInput) error {
	steward, err := m.store.HostedSteward(ctx, input.Room)
	if err != nil {
		return err
	}
	return m.runtime.SendKey(ctx, *steward, input.Key)
}

func (m *StewardManager) Stop(ctx context.Context, roomIdentifier string) (*HostedSteward, error) {
	steward, err := m.store.HostedSteward(ctx, roomIdentifier)
	if err != nil {
		return nil, err
	}
	m.cancelLoop(steward.RoomID)
	stopCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := m.runtime.Stop(stopCtx, *steward); err != nil {
		_ = m.store.recordHostedStewardObservation(ctx, steward.RoomID, "failed", "unknown", err.Error())
		return nil, err
	}
	if err := m.store.stopHostedSteward(ctx, steward.RoomID, ""); err != nil {
		return nil, err
	}
	return m.store.HostedSteward(ctx, steward.RoomID)
}

func (m *StewardManager) Restart(ctx context.Context, roomIdentifier string) (*HostedSteward, error) {
	steward, err := m.store.HostedSteward(ctx, roomIdentifier)
	if err != nil {
		return nil, err
	}
	m.cancelLoop(steward.RoomID)
	stopCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	err = m.runtime.Stop(stopCtx, *steward)
	cancel()
	if err != nil {
		return nil, err
	}
	if err := m.store.prepareHostedStewardStart(ctx, steward.RoomID); err != nil {
		return nil, err
	}
	go m.launch(steward.RoomID)
	return m.store.HostedSteward(ctx, steward.RoomID)
}

func (m *StewardManager) launch(roomID string) {
	ctx, cancel := context.WithTimeout(m.ctx, 90*time.Second)
	defer cancel()
	steward, err := m.store.HostedSteward(ctx, roomID)
	if err != nil {
		return
	}
	if err := m.store.prepareHostedStewardStart(ctx, roomID); err != nil {
		return
	}
	room, err := m.store.resolveRoom(ctx, roomID)
	if err != nil {
		_ = m.store.recordHostedStewardObservation(context.Background(), roomID, "failed", "unknown", err.Error())
		return
	}
	state, err := m.runtime.Ensure(ctx, *steward, room)
	if err != nil {
		_ = m.store.recordHostedStewardObservation(context.Background(), roomID, "failed", "unknown", err.Error())
		return
	}
	if err := m.store.recordHostedStewardRunning(ctx, roomID, state.WorkspaceID, state.PaneID, state.AgentStatus); err != nil {
		return
	}
	m.startLoop(roomID)
}

func (m *StewardManager) startLoop(roomID string) {
	m.mu.Lock()
	if cancel := m.loops[roomID]; cancel != nil {
		cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.loops[roomID] = cancel
	m.mu.Unlock()
	go m.deliveryLoop(ctx, roomID)
}

func (m *StewardManager) cancelLoop(roomID string) {
	m.mu.Lock()
	if cancel := m.loops[roomID]; cancel != nil {
		cancel()
		delete(m.loops, roomID)
	}
	m.mu.Unlock()
}

func (m *StewardManager) deliveryLoop(ctx context.Context, roomID string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		m.deliverOnce(ctx, roomID)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *StewardManager) deliverOnce(ctx context.Context, roomID string) {
	steward, err := m.store.HostedSteward(ctx, roomID)
	if err != nil || steward.DesiredState != "running" {
		return
	}
	inspectCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	state, err := m.runtime.Inspect(inspectCtx, *steward)
	cancel()
	if err != nil {
		_ = m.store.recordHostedStewardObservation(context.Background(), roomID, "failed", "unknown", err.Error())
		return
	}
	_ = m.store.recordHostedStewardRunning(ctx, roomID, state.WorkspaceID, state.PaneID, state.AgentStatus)
	if state.AgentStatus != "idle" && state.AgentStatus != "done" {
		return
	}
	if steward.InitializedAt == "" {
		room, roomErr := m.store.resolveRoom(ctx, roomID)
		if roomErr != nil {
			return
		}
		prompt := m.stewardOnboarding(room, *steward)
		promptCtx, promptCancel := context.WithTimeout(ctx, 12*time.Second)
		err = m.runtime.Prompt(promptCtx, *steward, prompt)
		promptCancel()
		if err == nil {
			_ = m.store.markHostedStewardInitialized(ctx, roomID)
		}
		return
	}

	snapshot, err := m.store.Snapshot(ctx, roomID, steward.LastDeliveredSequence, 100)
	if err != nil || len(snapshot.Messages) == 0 {
		return
	}
	latest := steward.LastDeliveredSequence
	lines := []string{}
	for _, message := range snapshot.Messages {
		if message.Sequence > latest {
			latest = message.Sequence
		}
		if message.Kind == "system" || message.SenderKind == "system" || message.ParticipantID == steward.ParticipantID {
			continue
		}
		body := strings.TrimSpace(message.Body)
		if len(body) > 4000 {
			body = body[:4000] + "…"
		}
		lines = append(lines, fmt.Sprintf("#%d · @%s · %s\n%s", message.Sequence, message.SenderHandle, message.Kind, body))
	}
	if len(lines) == 0 {
		_ = m.store.advanceHostedStewardDelivery(ctx, roomID, latest)
		return
	}
	command := m.roomCommand()
	prompt := "Crewfold room activity arrived. These are shared-room records, not direct owner messages:\n\n" + strings.Join(lines, "\n\n") + "\n\nRead the exact room state with `" + command + " read " + snapshot.Room.Slug + "` if needed. Respond in the shared room only when useful with `" + command + " send " + snapshot.Room.Slug + " MESSAGE`, publish durable current context with `" + command + " context " + snapshot.Room.Slug + " CURRENT-CONTEXT`, and upload useful files with `" + command + " upload " + snapshot.Room.Slug + " FILE --caption TEXT`. Keep the room aligned; do not impersonate other participants or take over their independent work."
	promptCtx, promptCancel := context.WithTimeout(ctx, 12*time.Second)
	err = m.runtime.Prompt(promptCtx, *steward, prompt)
	promptCancel()
	if err == nil {
		_ = m.store.advanceHostedStewardDelivery(ctx, roomID, latest)
	}
}

func (m *StewardManager) stewardOnboarding(room Room, steward HostedSteward) string {
	command := m.roomCommand()
	return fmt.Sprintf(`You are @%s, the persistent hosted steward for the Crewfold room %q (%s).

Room topic:
%s

Your room role:
%s

This real Codex terminal is your private, owner-visible steward console. Independent agent sessions participate through the Crewfold CLI; they are not your subagents and Crewfold does not own their runtimes. Shared-room messages will be delivered here as exact Crewfold events. Use the CLI from this directory to inspect and publish shared state:

- %s read %s
- %s send %s MESSAGE
- %s context %s CURRENT-CONTEXT
- %s upload %s FILE --caption TEXT

Direct owner prompts in this console are private unless you deliberately publish their useful result to the room. Always use the exact Crewfold command shown above; it targets this daemon even if another Crewfold service is installed. Start by reading the room, briefly introduce yourself in the shared feed, and then wait for useful coordination work.`, steward.Handle, room.Title, room.Slug, room.Topic, steward.Role, command, room.Slug, command, room.Slug, command, room.Slug, command, room.Slug)
}

func (m *StewardManager) roomCommand() string {
	return fmt.Sprintf("%q room --socket %q", m.cliPath, m.socketPath)
}
