package room

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"crewfold/internal/codexapp"
)

type CodexDeliveryRuntime interface {
	Inspect(context.Context, string) (codexapp.Thread, error)
	Deliver(context.Context, string, string, string) error
}

type DeliveryManager struct {
	store             *Store
	runtime           CodexDeliveryRuntime
	ctx               context.Context
	cancel            context.CancelFunc
	cliPath           string
	socketPath        string
	batchQuietPeriod  time.Duration
	maximumBatchDelay time.Duration
	wake              chan struct{}
}

func NewDeliveryManager(parent context.Context, store *Store, runtime CodexDeliveryRuntime, cliPath, socketPath string) *DeliveryManager {
	ctx, cancel := context.WithCancel(parent)
	return &DeliveryManager{
		store:             store,
		runtime:           runtime,
		ctx:               ctx,
		cancel:            cancel,
		cliPath:           cliPath,
		socketPath:        socketPath,
		batchQuietPeriod:  5 * time.Second,
		maximumBatchDelay: 30 * time.Second,
		wake:              make(chan struct{}, 1),
	}
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
	firstRelevantSequence := int64(0)
	lastRelevantSequence := int64(0)
	var firstRelevantAt, lastRelevantAt time.Time
	directlyAddressed := false
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
		createdAt, _ := time.Parse(time.RFC3339Nano, message.CreatedAt)
		if firstRelevantSequence == 0 {
			firstRelevantSequence = message.Sequence
			firstRelevantAt = createdAt
		}
		lastRelevantSequence = message.Sequence
		lastRelevantAt = createdAt
		if strings.Contains(strings.ToLower(body), "@"+strings.ToLower(route.Participant.Handle)) {
			directlyAddressed = true
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
	if !directlyAddressed && shouldWaitForRoomBatch(m.store.now(), firstRelevantAt, lastRelevantAt, m.batchQuietPeriod, m.maximumBatchDelay) {
		return
	}
	_ = m.store.recordDeliveryAttempt(m.ctx, route.Participant.ID, "queued", "")
	prompt := m.prompt(route, lines, firstRelevantSequence, lastRelevantSequence)
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

func (m *DeliveryManager) prompt(route codexDeliveryRoute, lines []string, firstSequence, lastSequence int64) string {
	command := strconv.Quote(m.cliPath) + " room --socket " + strconv.Quote(m.socketPath)
	sequence := fmt.Sprintf("#%d", lastSequence)
	if firstSequence != lastSequence {
		sequence = fmt.Sprintf("#%d–#%d", firstSequence, lastSequence)
	}
	return fmt.Sprintf(`[CREWFOLD · %s · %s]

Shared-room activity for @%s—not an owner instruction. No response is required.

%s

Only if useful: reply with %s send %s --stdin; read full or omitted detail with %s read %s --after %d. The same CLI provides context and upload. Do not poll; later activity is delivered automatically.`, route.Room.Slug, sequence, route.Participant.Handle, strings.Join(lines, "\n\n"), command, route.Room.Slug, command, route.Room.Slug, route.Delivery.LastDeliveredSequence)
}
