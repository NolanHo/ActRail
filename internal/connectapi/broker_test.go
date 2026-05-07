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
