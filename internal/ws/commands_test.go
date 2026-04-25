package ws

import (
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
