package connectapi

import (
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"

	"actrail/internal/ws"
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
	subscribers map[chan EventEnvelope]struct{}
}

func NewBroker(limit int) *Broker {
	if limit < 1 {
		limit = defaultBrokerLimit
	}
	return &Broker{limit: limit, subscribers: map[chan EventEnvelope]struct{}{}}
}

func (b *Broker) ObserveFrame(frame ws.Frame) {
	if b == nil {
		return
	}
	payload, err := json.Marshal(frame.Payload)
	if err != nil {
		payload = json.RawMessage(`null`)
	}
	unixMillis := int64(frame.TS * 1000)
	if unixMillis <= 0 {
		unixMillis = time.Now().UnixMilli()
	}

	b.mu.Lock()
	b.nextID++
	envelope := EventEnvelope{
		ID:          b.nextID,
		Type:        string(frame.Type),
		Stream:      frame.Stream,
		UnixMillis:  unixMillis,
		PayloadJSON: base64.StdEncoding.EncodeToString(payload),
	}
	b.events = append(b.events, envelope)
	if len(b.events) > b.limit {
		b.events = append([]EventEnvelope(nil), b.events[len(b.events)-b.limit:]...)
	}
	for ch := range b.subscribers {
		select {
		case ch <- envelope:
		default:
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

func (b *Broker) Subscribe() (<-chan EventEnvelope, func()) {
	ch := make(chan EventEnvelope, 256)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subscribers, ch)
		close(ch)
		b.mu.Unlock()
	}
}

func (b *Broker) resyncLocked(after uint64, reason string) EventEnvelope {
	b.nextID++
	payload, _ := json.Marshal(map[string]any{"after_event_id": after, "reason": reason})
	event := EventEnvelope{
		ID:          b.nextID,
		Type:        "stream.resync",
		Stream:      string(ws.SystemStream),
		UnixMillis:  time.Now().UnixMilli(),
		PayloadJSON: base64.StdEncoding.EncodeToString(payload),
	}
	b.events = append(b.events, event)
	if len(b.events) > b.limit {
		b.events = append([]EventEnvelope(nil), b.events[len(b.events)-b.limit:]...)
	}
	return event
}
