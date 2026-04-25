package ws

import (
	"testing"
	"time"
)

func TestNewAckFrame(t *testing.T) {
	now := time.Unix(1760000000, 0)
	frame := NewAckFrame(now, "evt_1", StreamName("session:s_123"), "req_1", FrameTypeSend)

	if frame.Type != FrameTypeAck {
		t.Fatalf("Type = %q, want %q", frame.Type, FrameTypeAck)
	}
	payload, ok := frame.Payload.(AckPayload)
	if !ok {
		t.Fatalf("Payload type = %T, want AckPayload", frame.Payload)
	}
	if payload.RequestID != "req_1" || !payload.Accepted || payload.Command != "send" {
		t.Fatalf("AckPayload = %#v", payload)
	}
}

func TestNewErrorFrame(t *testing.T) {
	now := time.Unix(1760000000, 0)
	frame := NewErrorFrame(now, "evt_2", StreamName("session:s_123"), "req_2", ErrorCodeInvalidRequest, "text required", "text")

	if frame.Type != FrameTypeError {
		t.Fatalf("Type = %q, want %q", frame.Type, FrameTypeError)
	}
	payload, ok := frame.Payload.(ErrorPayload)
	if !ok {
		t.Fatalf("Payload type = %T, want ErrorPayload", frame.Payload)
	}
	if payload.RequestID != "req_2" || payload.Code != ErrorCodeInvalidRequest || payload.Message != "text required" || payload.Field != "text" {
		t.Fatalf("ErrorPayload = %#v", payload)
	}
}

func TestNewResetRequiredFrame(t *testing.T) {
	now := time.Unix(1760000000, 0)
	frame, err := NewResetRequiredFrame(now, "evt_3", StreamName("session:s_123:ui"), "resume_cursor_expired")
	if err != nil {
		t.Fatalf("NewResetRequiredFrame() error = %v", err)
	}
	if frame.Type != FrameTypeTransportResetRequired {
		t.Fatalf("Type = %q, want %q", frame.Type, FrameTypeTransportResetRequired)
	}
	payload, ok := frame.Payload.(ResetRequiredPayload)
	if !ok {
		t.Fatalf("Payload type = %T, want ResetRequiredPayload", frame.Payload)
	}
	if payload.SessionID != "s_123" || payload.Reason != "resume_cursor_expired" {
		t.Fatalf("ResetRequiredPayload = %#v", payload)
	}
	if len(payload.Refresh) != 2 {
		t.Fatalf("Refresh paths = %#v, want 2 entries", payload.Refresh)
	}
}

func TestCodecRoundTrip(t *testing.T) {
	now := time.Unix(1760000000, 0)
	codec := Codec{}
	encoded, err := codec.Encode(NewHeartbeatFrame(now, "evt_4", "conn_1"))
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded.Type != FrameTypeHeartbeat {
		t.Fatalf("decoded type = %q, want %q", decoded.Type, FrameTypeHeartbeat)
	}
	if decoded.Stream != SystemStream.String() {
		t.Fatalf("decoded stream = %q, want %q", decoded.Stream, SystemStream)
	}
}
