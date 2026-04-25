package ws

import (
	"testing"
	"time"
)

func TestConnectionHandleSubscribeAcksAndReplays(t *testing.T) {
	now := time.Unix(1760000000, 0)
	conn, err := NewConnectionState("conn_1", 15*time.Second, NewCounterIDSource("evt"), now)
	if err != nil {
		t.Fatalf("NewConnectionState() error = %v", err)
	}
	buffer, err := NewReplayBuffer(4)
	if err != nil {
		t.Fatalf("NewReplayBuffer() error = %v", err)
	}
	stream := StreamName("session:s_123")
	if err := buffer.Append(stream, 41, Frame{Type: FrameTypeHeartbeat, ID: "evt_old", TS: UnixTS(now), Stream: stream.String(), Payload: map[string]any{"cursor": 41}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	frames, err := conn.HandleFrame(now.Add(time.Second), RawFrame{
		Type:      FrameTypeSubscribe,
		RequestID: "req_sub_1",
		TS:        UnixTS(now),
		Stream:    SystemStream.String(),
		Payload:   []byte(`{"streams":[{"name":"session:s_123","resume_from":40}]}`),
	}, buffer)
	if err != nil {
		t.Fatalf("HandleFrame() error = %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("len(HandleFrame()) = %d, want 2", len(frames))
	}
	if frames[0].Type != FrameTypeAck {
		t.Fatalf("frames[0].Type = %q, want %q", frames[0].Type, FrameTypeAck)
	}
	if frames[1].ID != "evt_old" {
		t.Fatalf("replayed frame id = %q, want %q", frames[1].ID, "evt_old")
	}
	if !conn.subscriptions.Has(stream) {
		t.Fatalf("subscription %q not stored", stream)
	}
}

func TestConnectionHandleSubscribeReturnsResetRequired(t *testing.T) {
	now := time.Unix(1760000000, 0)
	conn, err := NewConnectionState("conn_1", 15*time.Second, NewCounterIDSource("evt"), now)
	if err != nil {
		t.Fatalf("NewConnectionState() error = %v", err)
	}
	buffer, err := NewReplayBuffer(1)
	if err != nil {
		t.Fatalf("NewReplayBuffer() error = %v", err)
	}
	stream := StreamName("session:s_123")
	if err := buffer.Append(stream, 41, Frame{Type: FrameTypeHeartbeat, ID: "evt_old", TS: UnixTS(now), Stream: stream.String(), Payload: map[string]any{"cursor": 41}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	frames, err := conn.HandleFrame(now.Add(time.Second), RawFrame{
		Type:      FrameTypeSubscribe,
		RequestID: "req_sub_1",
		TS:        UnixTS(now),
		Stream:    SystemStream.String(),
		Payload:   []byte(`{"streams":[{"name":"session:s_123","resume_from":39}]}`),
	}, buffer)
	if err != nil {
		t.Fatalf("HandleFrame() error = %v", err)
	}
	if len(frames) != 1 || frames[0].Type != FrameTypeTransportResetRequired {
		t.Fatalf("frames = %#v", frames)
	}
	if conn.subscriptions.Has(stream) {
		t.Fatalf("subscription %q stored after reset-required path", stream)
	}
}

func TestConnectionHandleUnsupportedCommandReturnsErrorFrame(t *testing.T) {
	now := time.Unix(1760000000, 0)
	conn, err := NewConnectionState("conn_1", 15*time.Second, NewCounterIDSource("evt"), now)
	if err != nil {
		t.Fatalf("NewConnectionState() error = %v", err)
	}
	frames, err := conn.HandleFrame(now.Add(time.Second), RawFrame{
		Type:      FrameTypeSend,
		RequestID: "req_send_1",
		TS:        UnixTS(now),
		Stream:    StreamName("session:s_123").String(),
		Payload:   []byte(`{"session_id":"s_123","text":"hello"}`),
	}, nil)
	if err != nil {
		t.Fatalf("HandleFrame() error = %v", err)
	}
	if len(frames) != 1 || frames[0].Type != FrameTypeError {
		t.Fatalf("frames = %#v", frames)
	}
	payload, ok := frames[0].Payload.(ErrorPayload)
	if !ok {
		t.Fatalf("Payload type = %T, want ErrorPayload", frames[0].Payload)
	}
	if payload.Code != ErrorCodeUnsupported {
		t.Fatalf("Error code = %q, want %q", payload.Code, ErrorCodeUnsupported)
	}
}

func TestConnectionHandlePingUpdatesHeartbeat(t *testing.T) {
	start := time.Unix(1760000000, 0)
	conn, err := NewConnectionState("conn_1", 15*time.Second, nil, start)
	if err != nil {
		t.Fatalf("NewConnectionState() error = %v", err)
	}
	pingAt := start.Add(4 * time.Second)
	frames, err := conn.HandleFrame(pingAt, RawFrame{Type: FrameTypePing, TS: UnixTS(pingAt), Stream: SystemStream.String(), Payload: []byte(`{}`)}, nil)
	if err != nil {
		t.Fatalf("HandleFrame() error = %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("len(HandleFrame()) = %d, want 0", len(frames))
	}
	if got := conn.Heartbeat().LastClientPing(); !got.Equal(pingAt) {
		t.Fatalf("LastClientPing() = %s, want %s", got, pingAt)
	}
}
