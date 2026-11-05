package room

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type DeliveryRuntime interface {
	Validate(context.Context, string) error
	Deliver(context.Context, string, string) error
}

type DeliveryManager struct {
	store             *Store
	runtimes          map[string]DeliveryRuntime
	ctx               context.Context
	cancel            context.CancelFunc
	batchQuietPeriod  time.Duration
	maximumBatchDelay time.Duration
	wake              chan struct{}
}

func NewDeliveryManager(parent context.Context, store *Store, codexRuntime, claudeRuntime DeliveryRuntime) *DeliveryManager {
	ctx, cancel := context.WithCancel(parent)
	return &DeliveryManager{
		store:             store,
		runtimes:          map[string]DeliveryRuntime{"codex": codexRuntime, "claude": claudeRuntime},
		ctx:               ctx,
		cancel:            cancel,
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

func (m *DeliveryManager) Validate(ctx context.Context, provider, sessionID string) error {
	runtime := m.runtimes[provider]
	if runtime == nil {
		return fmt.Errorf("%s delivery is unavailable", provider)
	}
	return runtime.Validate(ctx, sessionID)
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
	routes, err := m.store.pendingDeliveries(m.ctx)
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

func shouldWaitForRoomBatch(now, first, last time.Time, quietPeriod, maximumDelay time.Duration) bool {
	if first.IsZero() || last.IsZero() {
		return false
	}
	return now.Sub(last) < quietPeriod && now.Sub(first) < maximumDelay
}

func (m *DeliveryManager) deliver(route deliveryRoute) {
	messages, err := m.store.messages(m.ctx, route.Room.ID, route.Delivery.LastDeliveredSequence, 100)
	if err != nil || len(messages) == 0 {
		return
	}
	latest := route.Delivery.LastDeliveredSequence
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
		createdAt, _ := time.Parse(time.RFC3339Nano, message.CreatedAt)
		if firstRelevantSequence == 0 {
			firstRelevantSequence = message.Sequence
			firstRelevantAt = createdAt
		}
		lastRelevantSequence = message.Sequence
		lastRelevantAt = createdAt
		if messageNeedsImmediateDelivery(body, route.Participant.Handle) {
			directlyAddressed = true
		}
		if message.Sequence > latest {
			latest = message.Sequence
		}
	}
	if firstRelevantSequence == 0 {
		_ = m.store.advanceDelivery(m.ctx, route.Participant.ID, latest)
		return
	}
	if !directlyAddressed && shouldWaitForRoomBatch(m.store.now(), firstRelevantAt, lastRelevantAt, m.batchQuietPeriod, m.maximumBatchDelay) {
		return
	}
	_ = m.store.recordDeliveryAttempt(m.ctx, route.Participant.ID, "queued", "")
	prompt := m.prompt(route, firstRelevantSequence, lastRelevantSequence, latest)
	runtime := m.runtimes[route.Delivery.Kind]
	if runtime == nil {
		_ = m.store.recordDeliveryAttempt(context.Background(), route.Participant.ID, "error", route.Delivery.Kind+" delivery is unavailable")
		return
	}
	timeout := 12 * time.Second
	if route.Delivery.Kind == "claude" {
		timeout = 45 * time.Second
	}
	deliveryCtx, cancel := context.WithTimeout(m.ctx, timeout)
	err = runtime.Deliver(deliveryCtx, route.Delivery.Target, prompt)
	cancel()
	if err != nil {
		_ = m.store.recordDeliveryAttempt(context.Background(), route.Participant.ID, "error", err.Error())
		return
	}
	_ = m.store.advanceDelivery(m.ctx, route.Participant.ID, latest)
}

func (m *DeliveryManager) prompt(route deliveryRoute, firstSequence, lastSequence, through int64) string {
	sequence := fmt.Sprintf("#%d", lastSequence)
	if firstSequence != lastSequence {
		sequence = fmt.Sprintf("#%d–#%d", firstSequence, lastSequence)
	}
	return fmt.Sprintf("[CREWFOLD · %s · %s] Run crewfold room receive %s --after %d --through %d", route.Room.Slug, sequence, strconv.Quote(route.Room.Slug), route.Delivery.LastDeliveredSequence, through)
}

func messageNeedsImmediateDelivery(body, handle string) bool {
	body = strings.ToLower(body)
	return containsMention(body, "@"+strings.ToLower(handle)) || containsMention(body, "@everyone")
}

func containsMention(body, mention string) bool {
	for offset := 0; offset < len(body); {
		index := strings.Index(body[offset:], mention)
		if index < 0 {
			return false
		}
		index += offset
		end := index + len(mention)
		beforeBoundary := index == 0 || !isHandleByte(body[index-1])
		afterBoundary := isMentionEnd(body, end)
		if beforeBoundary && afterBoundary {
			return true
		}
		offset = index + 1
	}
	return false
}

func isMentionEnd(body string, end int) bool {
	if end == len(body) || !isHandleByte(body[end]) {
		return true
	}
	return (body[end] == '.' || body[end] == '-' || body[end] == '_') && (end+1 == len(body) || !isHandleByte(body[end+1]))
}

func isHandleByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '-' || value == '_' || value == '.'
}
