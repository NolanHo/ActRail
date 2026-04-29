package ws

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeCommandTarget struct {
	mu             sync.Mutex
	sends          []SendCommand
	enqueues       []EnqueueCommand
	queueCancels   []QueueCancelCommand
	interrupts     []InterruptCommand
	uiResponses    []UIResponseCommand
	sendErr        error
	enqueueErr     error
	queueCancelErr error
	interruptErr   error
	uiResponseErr  error
}

func (f *fakeCommandTarget) HandleSend(cmd SendCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, cmd)
	return f.sendErr
}

func (f *fakeCommandTarget) HandleEnqueue(cmd EnqueueCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueues = append(f.enqueues, cmd)
	return f.enqueueErr
}

func (f *fakeCommandTarget) HandleQueueCancel(cmd QueueCancelCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queueCancels = append(f.queueCancels, cmd)
	return f.queueCancelErr
}

func (f *fakeCommandTarget) HandleInterrupt(cmd InterruptCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interrupts = append(f.interrupts, cmd)
	return f.interruptErr
}

func (f *fakeCommandTarget) HandleUIResponse(cmd UIResponseCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uiResponses = append(f.uiResponses, cmd)
	return f.uiResponseErr
}

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
	}, buffer, nil)
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
	if !conn.HasSubscription(stream) {
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
	}, buffer, nil)
	if err != nil {
		t.Fatalf("HandleFrame() error = %v", err)
	}
	if len(frames) != 1 || frames[0].Type != FrameTypeTransportResetRequired {
		t.Fatalf("frames = %#v", frames)
	}
	if conn.HasSubscription(stream) {
		t.Fatalf("subscription %q stored after reset-required path", stream)
	}
}

func TestConnectionHandleCommandDispatchAcks(t *testing.T) {
	now := time.Unix(1760000000, 0)
	conn, err := NewConnectionState("conn_1", 15*time.Second, NewCounterIDSource("evt"), now)
	if err != nil {
		t.Fatalf("NewConnectionState() error = %v", err)
	}
	target := &fakeCommandTarget{}

	tests := []struct {
		name     string
		frame    RawFrame
		wantType FrameType
		check    func(*testing.T)
	}{
		{
			name:     "send",
			frame:    RawFrame{Type: FrameTypeSend, RequestID: "req_send_1", TS: UnixTS(now), Stream: "session:s_123", Payload: []byte(`{"session_id":"s_123","text":"hello"}`)},
			wantType: FrameTypeSend,
			check: func(t *testing.T) {
				if len(target.sends) != 1 || target.sends[0].SessionID.String() != "s_123" || target.sends[0].Text != "hello" {
					t.Fatalf("target.sends = %#v", target.sends)
				}
			},
		},
		{
			name:     "enqueue",
			frame:    RawFrame{Type: FrameTypeEnqueue, RequestID: "req_enqueue_1", TS: UnixTS(now), Stream: "session:s_123", Payload: []byte(`{"session_id":"s_123","text":"queued"}`)},
			wantType: FrameTypeEnqueue,
			check: func(t *testing.T) {
				if len(target.enqueues) != 1 || target.enqueues[0].SessionID.String() != "s_123" || target.enqueues[0].Text != "queued" {
					t.Fatalf("target.enqueues = %#v", target.enqueues)
				}
			},
		},
		{
			name:     "interrupt",
			frame:    RawFrame{Type: FrameTypeInterrupt, RequestID: "req_interrupt_1", TS: UnixTS(now), Stream: "session:s_123", Payload: []byte(`{"session_id":"s_123"}`)},
			wantType: FrameTypeInterrupt,
			check: func(t *testing.T) {
				if len(target.interrupts) != 1 || target.interrupts[0].SessionID.String() != "s_123" {
					t.Fatalf("target.interrupts = %#v", target.interrupts)
				}
			},
		},
		{
			name:     "ui.response",
			frame:    RawFrame{Type: FrameTypeUIResponse, RequestID: "req_ui_1", TS: UnixTS(now), Stream: "session:s_123:ui", Payload: []byte(`{"session_id":"s_123","response_to":"ask_1","value":{"choice":"A"}}`)},
			wantType: FrameTypeUIResponse,
			check: func(t *testing.T) {
				if len(target.uiResponses) != 1 || target.uiResponses[0].SessionID.String() != "s_123" || target.uiResponses[0].ResponseTo != "ask_1" || !bytes.Equal(target.uiResponses[0].Value, []byte(`{"choice":"A"}`)) {
					t.Fatalf("target.uiResponses = %#v", target.uiResponses)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames, err := conn.HandleFrame(now.Add(time.Second), tt.frame, nil, target)
			if err != nil {
				t.Fatalf("HandleFrame() error = %v", err)
			}
			if len(frames) != 1 || frames[0].Type != FrameTypeAck {
				t.Fatalf("frames = %#v", frames)
			}
			payload, ok := frames[0].Payload.(AckPayload)
			if !ok {
				t.Fatalf("Payload type = %T, want AckPayload", frames[0].Payload)
			}
			if payload.RequestID != tt.frame.RequestID || payload.Command != string(tt.wantType) || !payload.Accepted {
				t.Fatalf("Ack payload = %#v", payload)
			}
			tt.check(t)
		})
	}
}

func TestConnectionHandleCommandDispatchMapsTargetErrors(t *testing.T) {
	now := time.Unix(1760000000, 0)
	conn, err := NewConnectionState("conn_1", 15*time.Second, NewCounterIDSource("evt"), now)
	if err != nil {
		t.Fatalf("NewConnectionState() error = %v", err)
	}
	target := &fakeCommandTarget{sendErr: NewCommandError(ErrorCodeConflict, "session busy", "session_id")}
	frames, err := conn.HandleFrame(now.Add(time.Second), RawFrame{
		Type:      FrameTypeSend,
		RequestID: "req_send_1",
		TS:        UnixTS(now),
		Stream:    "session:s_123",
		Payload:   []byte(`{"session_id":"s_123","text":"hello"}`),
	}, nil, target)
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
	if payload.RequestID != "req_send_1" || payload.Code != ErrorCodeConflict || payload.Message != "session busy" || payload.Field != "session_id" {
		t.Fatalf("payload = %#v", payload)
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
	}, nil, nil)
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
	if payload.Code != ErrorCodeUnsupported || payload.Message != `command "send" requires a command target` {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestConnectionHandleCommandDispatchMapsUntypedErrorsToInternal(t *testing.T) {
	now := time.Unix(1760000000, 0)
	conn, err := NewConnectionState("conn_1", 15*time.Second, NewCounterIDSource("evt"), now)
	if err != nil {
		t.Fatalf("NewConnectionState() error = %v", err)
	}
	target := &fakeCommandTarget{interruptErr: errors.New("target exploded")}
	frames, err := conn.HandleFrame(now.Add(time.Second), RawFrame{
		Type:      FrameTypeInterrupt,
		RequestID: "req_interrupt_1",
		TS:        UnixTS(now),
		Stream:    StreamName("session:s_123").String(),
		Payload:   []byte(`{"session_id":"s_123"}`),
	}, nil, target)
	if err != nil {
		t.Fatalf("HandleFrame() error = %v", err)
	}
	payload, ok := frames[0].Payload.(ErrorPayload)
	if !ok {
		t.Fatalf("Payload type = %T, want ErrorPayload", frames[0].Payload)
	}
	if payload.Code != ErrorCodeInternal || payload.Message != "target exploded" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestConnectionHandlePingUpdatesHeartbeat(t *testing.T) {
	start := time.Unix(1760000000, 0)
	conn, err := NewConnectionState("conn_1", 15*time.Second, nil, start)
	if err != nil {
		t.Fatalf("NewConnectionState() error = %v", err)
	}
	pingAt := start.Add(4 * time.Second)
	frames, err := conn.HandleFrame(pingAt, RawFrame{Type: FrameTypePing, TS: UnixTS(pingAt), Stream: SystemStream.String(), Payload: []byte(`{}`)}, nil, nil)
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
