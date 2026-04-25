package ws

import (
	"bytes"
	"testing"
)

func TestDecodeSubscribeCommand(t *testing.T) {
	cursor := int64(40)
	cmd, err := DecodeSubscribeCommand(RawFrame{
		Type:      FrameTypeSubscribe,
		RequestID: "req_sub_1",
		TS:        1760000000,
		Stream:    SystemStream.String(),
		Payload:   []byte(`{"streams":[{"name":"sessions"},{"name":"session:s_123","resume_from":40},{"name":"session:s_123:ui","resume_from":44}]}`),
	})
	if err != nil {
		t.Fatalf("DecodeSubscribeCommand() error = %v", err)
	}
	if cmd.RequestID != "req_sub_1" {
		t.Fatalf("RequestID = %q, want %q", cmd.RequestID, "req_sub_1")
	}
	if len(cmd.Payload.Streams) != 3 {
		t.Fatalf("len(Streams) = %d, want 3", len(cmd.Payload.Streams))
	}
	if cmd.Payload.Streams[1].Name != StreamName("session:s_123") {
		t.Fatalf("stream[1].Name = %q", cmd.Payload.Streams[1].Name)
	}
	if cmd.Payload.Streams[1].ResumeFrom == nil || *cmd.Payload.Streams[1].ResumeFrom != cursor {
		t.Fatalf("stream[1].ResumeFrom = %#v, want %d", cmd.Payload.Streams[1].ResumeFrom, cursor)
	}
}

func TestDecodeSubscribeCommandRejectsDuplicateStream(t *testing.T) {
	_, err := DecodeSubscribeCommand(RawFrame{
		Type:      FrameTypeSubscribe,
		RequestID: "req_sub_1",
		TS:        1760000000,
		Stream:    SystemStream.String(),
		Payload:   []byte(`{"streams":[{"name":"sessions"},{"name":"sessions"}]}`),
	})
	if err == nil {
		t.Fatal("DecodeSubscribeCommand() error = nil, want error")
	}
}

func TestDecodeUnsubscribeCommand(t *testing.T) {
	cmd, err := DecodeUnsubscribeCommand(RawFrame{
		Type:      FrameTypeUnsubscribe,
		RequestID: "req_unsub_1",
		TS:        1760000000,
		Stream:    SystemStream.String(),
		Payload:   []byte(`{"streams":["sessions","session:s_123:ui"]}`),
	})
	if err != nil {
		t.Fatalf("DecodeUnsubscribeCommand() error = %v", err)
	}
	if len(cmd.Streams) != 2 {
		t.Fatalf("len(Streams) = %d, want 2", len(cmd.Streams))
	}
	if cmd.Streams[1] != StreamName("session:s_123:ui") {
		t.Fatalf("Streams[1] = %q", cmd.Streams[1])
	}
}

func TestDecodePingCommand(t *testing.T) {
	cmd, err := DecodePingCommand(RawFrame{
		Type:    FrameTypePing,
		TS:      1760000000,
		Stream:  SystemStream.String(),
		Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("DecodePingCommand() error = %v", err)
	}
	if cmd.Stream != SystemStream {
		t.Fatalf("Stream = %q, want %q", cmd.Stream, SystemStream)
	}
}

func TestDecodeSendCommand(t *testing.T) {
	cmd, err := DecodeSendCommand(RawFrame{
		Type:      FrameTypeSend,
		RequestID: "req_send_1",
		TS:        1760000000,
		Stream:    "session:s_123",
		Payload:   []byte(`{"session_id":"s_123","text":"  hello  "}`),
	})
	if err != nil {
		t.Fatalf("DecodeSendCommand() error = %v", err)
	}
	if cmd.RequestID != "req_send_1" || cmd.SessionID.String() != "s_123" || cmd.Text != "hello" {
		t.Fatalf("cmd = %#v", cmd)
	}
}

func TestDecodeEnqueueCommand(t *testing.T) {
	cmd, err := DecodeEnqueueCommand(RawFrame{
		Type:      FrameTypeEnqueue,
		RequestID: "req_enqueue_1",
		TS:        1760000000,
		Stream:    "session:s_123",
		Payload:   []byte(`{"session_id":"s_123","text":"queued"}`),
	})
	if err != nil {
		t.Fatalf("DecodeEnqueueCommand() error = %v", err)
	}
	if cmd.RequestID != "req_enqueue_1" || cmd.SessionID.String() != "s_123" || cmd.Text != "queued" {
		t.Fatalf("cmd = %#v", cmd)
	}
}

func TestDecodeInterruptCommand(t *testing.T) {
	cmd, err := DecodeInterruptCommand(RawFrame{
		Type:      FrameTypeInterrupt,
		RequestID: "req_interrupt_1",
		TS:        1760000000,
		Stream:    "session:s_123",
		Payload:   []byte(`{"session_id":"s_123"}`),
	})
	if err != nil {
		t.Fatalf("DecodeInterruptCommand() error = %v", err)
	}
	if cmd.RequestID != "req_interrupt_1" || cmd.SessionID.String() != "s_123" {
		t.Fatalf("cmd = %#v", cmd)
	}
}

func TestDecodeUIResponseCommand(t *testing.T) {
	cmd, err := DecodeUIResponseCommand(RawFrame{
		Type:      FrameTypeUIResponse,
		RequestID: "req_ui_1",
		TS:        1760000000,
		Stream:    "session:s_123:ui",
		Payload:   []byte(`{"session_id":"s_123","response_to":"ask_1","value":{"choice":"A"}}`),
	})
	if err != nil {
		t.Fatalf("DecodeUIResponseCommand() error = %v", err)
	}
	if cmd.RequestID != "req_ui_1" || cmd.SessionID.String() != "s_123" || cmd.ResponseTo != "ask_1" {
		t.Fatalf("cmd = %#v", cmd)
	}
	if !bytes.Equal(cmd.Value, []byte(`{"choice":"A"}`)) {
		t.Fatalf("Value = %s", string(cmd.Value))
	}
}

func TestDecodeSendCommandRejectsSessionMismatch(t *testing.T) {
	_, err := DecodeSendCommand(RawFrame{
		Type:      FrameTypeSend,
		RequestID: "req_send_1",
		TS:        1760000000,
		Stream:    "session:s_123",
		Payload:   []byte(`{"session_id":"s_456","text":"hello"}`),
	})
	if err == nil {
		t.Fatal("DecodeSendCommand() error = nil, want error")
	}
}

func TestDecodeUIResponseCommandRejectsMainStream(t *testing.T) {
	_, err := DecodeUIResponseCommand(RawFrame{
		Type:      FrameTypeUIResponse,
		RequestID: "req_ui_1",
		TS:        1760000000,
		Stream:    "session:s_123",
		Payload:   []byte(`{"session_id":"s_123","response_to":"ask_1","value":"A"}`),
	})
	if err == nil {
		t.Fatal("DecodeUIResponseCommand() error = nil, want error")
	}
}
