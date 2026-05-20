package connectapi

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"actrail/internal/realtime"
)

func TestBrokerAssignsMonotonicIDsAndReplays(t *testing.T) {
	broker := NewBroker(3)
	for _, typ := range []string{"session.state", "message.commit"} {
		broker.ObserveEvent(realtime.Event{Type: typ, UnixMillis: 10_000, Stream: "session:s_1", Payload: map[string]any{"ok": true}})
	}
	events, gap := broker.Replay(1)
	if gap {
		t.Fatal("Replay() gap = true, want false")
	}
	if len(events) != 1 || events[0].ID != 2 || events[0].Type != "message.commit" {
		t.Fatalf("Replay() = %+v, want event id 2", events)
	}
}

func TestBrokerReplayGapEmitsResync(t *testing.T) {
	broker := NewBroker(2)
	for i := 0; i < 4; i++ {
		broker.ObserveEvent(realtime.Event{Type: "session.state", UnixMillis: 10_000, Stream: "session:s_1", Payload: map[string]any{"i": i}})
	}
	events, gap := broker.Replay(1)
	if !gap {
		t.Fatal("Replay() gap = false, want true")
	}
	if len(events) != 1 || events[0].Type != "stream.resync" {
		t.Fatalf("Replay() = %+v, want stream.resync", events)
	}
	decoded, err := base64.StdEncoding.DecodeString(events[0].PayloadJSON)
	if err != nil {
		t.Fatalf("decode payload_json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("decode resync payload: %v", err)
	}
	if payload["reason"] != "replay_gap" {
		t.Fatalf("resync payload = %+v", payload)
	}
}

func TestBrokerSubscribeAfterAtomicallyReplaysAndSubscribes(t *testing.T) {
	broker := NewBroker(10)
	broker.ObserveEvent(realtime.Event{Type: "session.state", UnixMillis: 10_000, Stream: "session:s_1", Payload: map[string]any{"i": 1}})

	replay, ch, unsubscribe := broker.SubscribeAfter(0)
	defer unsubscribe()
	if len(replay) != 1 || replay[0].ID != 1 {
		t.Fatalf("SubscribeAfter() replay = %+v, want event id 1", replay)
	}

	broker.ObserveEvent(realtime.Event{Type: "message.commit", UnixMillis: 11_000, Stream: "session:s_1", Payload: map[string]any{"i": 2}})
	select {
	case event := <-ch:
		if event.ID != 2 || event.Type != "message.commit" {
			t.Fatalf("live event = %+v, want message.commit id 2", event)
		}
	default:
		t.Fatal("live event not delivered after SubscribeAfter")
	}
}

func TestBrokerUnsubscribeIsIdempotentAfterSlowSubscriberClose(t *testing.T) {
	broker := NewBroker(300)
	ch, unsubscribe := broker.Subscribe()

	for i := 0; i < 257; i++ {
		broker.ObserveEvent(realtime.Event{Type: "session.state", UnixMillis: 10_000, Stream: "session:s_1", Payload: map[string]any{"i": i}})
	}
	closed := false
	for i := 0; i < 258; i++ {
		_, ok := <-ch
		if !ok {
			closed = true
			break
		}
	}
	if !closed {
		t.Fatal("slow subscriber channel still open after draining buffered events")
	}

	unsubscribe()
	unsubscribe()
}
