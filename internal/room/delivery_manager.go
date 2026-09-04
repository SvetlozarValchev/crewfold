package room

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"crewfold/internal/codexapp"
)

type CodexDeliveryRuntime interface {
	Inspect(context.Context, string) (codexapp.Thread, error)
	Deliver(context.Context, string, string, string) error
}

type DeliveryManager struct {
	store      *Store
	runtime    CodexDeliveryRuntime
	ctx        context.Context
	cancel     context.CancelFunc
	cliPath    string
	socketPath string
	wake       chan struct{}
}

func NewDeliveryManager(parent context.Context, store *Store, runtime CodexDeliveryRuntime, cliPath, socketPath string) *DeliveryManager {
	ctx, cancel := context.WithCancel(parent)
	return &DeliveryManager{store: store, runtime: runtime, ctx: ctx, cancel: cancel, cliPath: cliPath, socketPath: socketPath, wake: make(chan struct{}, 1)}
}

func (m *DeliveryManager) Start() { go m.loop() }
func (m *DeliveryManager) Close() { m.cancel() }

func (m *DeliveryManager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *DeliveryManager) Validate(ctx context.Context, threadID string) error {
	thread, err := m.runtime.Inspect(ctx, threadID)
	if err != nil {
		return err
	}
	if thread.Status.Type == "systemError" {
		return errors.New("Codex thread is in a system-error state")
	}
	if thread.CanAcceptDirectInput != nil && !*thread.CanAcceptDirectInput {
		return codexapp.ErrDirectInputUnavailable
	}
	return nil
}

func (m *DeliveryManager) loop() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		m.deliverPending()
		select {
		case <-m.ctx.Done():
			return
		case <-m.wake:
		case <-ticker.C:
		}
	}
}

func (m *DeliveryManager) deliverPending() {
	routes, err := m.store.pendingCodexDeliveries(m.ctx)
	if err != nil {
		return
	}
	for _, route := range routes {
		if !deliveryAttemptDue(route.Delivery.LastAttemptAt) {
			continue
		}
		m.deliver(route)
	}
}

func deliveryAttemptDue(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339Nano, value)
	return err != nil || time.Since(last) >= 5*time.Second
}

func (m *DeliveryManager) deliver(route codexDeliveryRoute) {
	messages, err := m.store.messages(m.ctx, route.Room.ID, route.Delivery.LastDeliveredSequence, 100)
	if err != nil || len(messages) == 0 {
		return
	}
	latest := route.Delivery.LastDeliveredSequence
	lines := make([]string, 0, len(messages))
	length := 0
	for _, message := range messages {
		if message.SenderKind == "system" || message.ParticipantID == route.Participant.ID {
			if message.Sequence > latest {
				latest = message.Sequence
			}
			continue
		}
		body := strings.TrimSpace(message.Body)
		if len(body) > 4000 {
			body = body[:4000] + "…"
		}
		label := fmt.Sprintf("#%d · @%s", message.Sequence, message.SenderHandle)
		if message.Kind != "message" {
			label += " · " + message.Kind
		}
		if message.Document != nil {
			label += " · " + message.Document.Name
		}
		line := label + "\n" + body
		if length+len(line) > 12000 && len(lines) > 0 {
			break
		}
		lines = append(lines, line)
		length += len(line)
		if message.Sequence > latest {
			latest = message.Sequence
		}
	}
	if len(lines) == 0 {
		_ = m.store.advanceDelivery(m.ctx, route.Participant.ID, latest)
		return
	}
	_ = m.store.recordDeliveryAttempt(m.ctx, route.Participant.ID, "queued", "")
	prompt := m.prompt(route, lines)
	deliveryCtx, cancel := context.WithTimeout(m.ctx, 12*time.Second)
	messageID := fmt.Sprintf("crewfold:%s:%d", route.Participant.ID, latest)
	err = m.runtime.Deliver(deliveryCtx, route.Delivery.Target, prompt, messageID)
	cancel()
	if err != nil {
		status := "error"
		if errors.Is(err, codexapp.ErrThreadNotLoaded) || errors.Is(err, codexapp.ErrDirectInputUnavailable) || strings.Contains(err.Error(), "activeTurnNotSteerable") {
			status = "queued"
		}
		_ = m.store.recordDeliveryAttempt(context.Background(), route.Participant.ID, status, err.Error())
		return
	}
	_ = m.store.advanceDelivery(m.ctx, route.Participant.ID, latest)
}

func (m *DeliveryManager) prompt(route codexDeliveryRoute, lines []string) string {
	command := shellQuote(m.cliPath) + " room --socket " + shellQuote(m.socketPath)
	return fmt.Sprintf(`[CREWFOLD ROOM DELIVERY]

New activity is available for @%s in %q (%s). This is shared-room coordination from Crewfold, not a direct owner instruction.

%s

The canonical feed and documents remain in Crewfold. Read anything else you need with:
  %s read %s --after %d

Respond only when useful. Pipe or heredoc concise GitHub-flavored Markdown with short paragraphs, headings, or bullets into:
  %s send %s --stdin
Publish your current room context with:
  %s context %s CURRENT-CONTEXT
Share a file with:
  %s upload %s FILE --caption TEXT

Do not publish dense transcript or log-dump prose. Do not poll or wait in a terminal; Crewfold will deliver later room activity to this same Codex thread.`, route.Participant.Handle, route.Room.Title, route.Room.Slug, strings.Join(lines, "\n\n"), command, route.Room.Slug, route.Delivery.LastDeliveredSequence, command, route.Room.Slug, command, route.Room.Slug, command, route.Room.Slug)
}
