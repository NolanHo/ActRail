package connectapi

import (
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"

	"actrail/internal/realtime"
)

const defaultBrokerLimit = 5000

type EventEnvelope struct {
	ID          uint64 `json:"id"`
	Type        string `json:"type"`
	Stream      string `json:"stream"`
	UnixMillis  int64  `json:"unixMillis"`
	PayloadJSON string `json:"payloadJson"`
}

type Broker struct {
	mu          sync.Mutex
	limit       int
	nextID      uint64
	events      []EventEnvelope
	subscribers map[*brokerSubscriber]struct{}
}

type brokerSubscriber struct {
	ch     chan EventEnvelope
	closed bool
}

func NewBroker(limit int) *Broker {
	if limit < 1 {
		limit = defaultBrokerLimit
	}
	return &Broker{limit: limit, subscribers: map[*brokerSubscriber]struct{}{}}
}

func (b *Broker) ObserveEvent(event realtime.Event) {
	if b == nil {
		return
	}
	payload := event.PayloadJSON()
	unixMillis := event.EventUnixMillis(time.Now)

	b.mu.Lock()
	b.nextID++
	envelope := EventEnvelope{
		ID:          b.nextID,
		Type:        event.Type,
		Stream:      event.Stream,
		UnixMillis:  unixMillis,
		PayloadJSON: base64.StdEncoding.EncodeToString(payload),
	}
	b.events = append(b.events, envelope)
	b.trimLocked()
	for subscriber := range b.subscribers {
		select {
		case subscriber.ch <- envelope:
		default:
			b.closeSubscriberLocked(subscriber)
		}
	}
	b.mu.Unlock()
}

func (b *Broker) Replay(after uint64) ([]EventEnvelope, bool) {
	if b == nil {
		return nil, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.replayLocked(after)
}

func (b *Broker) Subscribe() (<-chan EventEnvelope, func()) {
	replay, ch, unsubscribe := b.SubscribeAfter(0)
	_ = replay
	return ch, unsubscribe
}

func (b *Broker) SubscribeAfter(after uint64) ([]EventEnvelope, <-chan EventEnvelope, func()) {
	if b == nil {
		return nil, nil, func() {}
	}
	subscriber := &brokerSubscriber{ch: make(chan EventEnvelope, 256)}
	b.mu.Lock()
	replay, _ := b.replayLocked(after)
	b.subscribers[subscriber] = struct{}{}
	b.mu.Unlock()
	return replay, subscriber.ch, func() {
		b.mu.Lock()
		b.closeSubscriberLocked(subscriber)
		b.mu.Unlock()
	}
}

func (b *Broker) replayLocked(after uint64) ([]EventEnvelope, bool) {
	if len(b.events) == 0 {
		return nil, false
	}
	oldest := b.events[0].ID
	if after > 0 && after < oldest-1 {
		return []EventEnvelope{b.resyncLocked(after, "replay_gap")}, true
	}
	out := make([]EventEnvelope, 0, len(b.events))
	for _, event := range b.events {
		if event.ID > after {
			out = append(out, event)
		}
	}
	return out, false
}

func (b *Broker) closeSubscriberLocked(subscriber *brokerSubscriber) {
	if subscriber == nil {
		return
	}
	delete(b.subscribers, subscriber)
	if !subscriber.closed {
		subscriber.closed = true
		close(subscriber.ch)
	}
}

func (b *Broker) resyncLocked(after uint64, reason string) EventEnvelope {
	b.nextID++
	payload, _ := json.Marshal(map[string]any{"after_event_id": after, "reason": reason})
	event := EventEnvelope{
		ID:          b.nextID,
		Type:        "stream.resync",
		Stream:      realtime.SystemStream,
		UnixMillis:  time.Now().UnixMilli(),
		PayloadJSON: base64.StdEncoding.EncodeToString(payload),
	}
	b.events = append(b.events, event)
	b.trimLocked()
	return event
}

func (b *Broker) trimLocked() {
	if len(b.events) <= b.limit {
		return
	}
	drop := len(b.events) - b.limit
	copy(b.events, b.events[drop:])
	clear(b.events[len(b.events)-drop:])
	b.events = b.events[:b.limit]
}
