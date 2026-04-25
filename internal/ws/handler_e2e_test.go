package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"actrail/internal/config"
	"actrail/internal/httpapi/authn"

	"github.com/gorilla/websocket"
)

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(now time.Time) *manualClock {
	return &manualClock{now: now}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

type manualTicker struct {
	ch chan time.Time
}

func newManualTicker() *manualTicker {
	return &manualTicker{ch: make(chan time.Time, 8)}
}

func (t *manualTicker) Chan() <-chan time.Time {
	return t.ch
}

func (t *manualTicker) Stop() {}

func (t *manualTicker) Tick(now time.Time) {
	t.ch <- now
}

func TestHandlerUpgradeSendsHelloAndTracksRegistry(t *testing.T) {
	start := time.Unix(1760000000, 0)
	clock := newManualClock(start)
	hbTicker := newManualTicker()
	registry := NewRegistry()
	cfg := testWSConfig()
	h := NewHandler(cfg,
		WithNow(clock.Now),
		WithTickerFactory(func(time.Duration) Ticker { return hbTicker }),
		WithRegistry(registry),
		WithConnectionIDs(NewCounterIDSource("conn")),
		WithFrameIDs(NewCounterIDSource("evt")),
	)
	server := httptest.NewServer(h)
	defer server.Close()

	conn := dialTestWebSocket(t, server, cfg, "?protocol_version=1")
	defer conn.Close()

	raw := readRawFrame(t, conn)
	if raw.Type != FrameTypeHello {
		t.Fatalf("hello frame type = %q, want %q", raw.Type, FrameTypeHello)
	}
	payload := decodePayload[HelloPayload](t, raw)
	if payload.ProtocolVersion != cfg.Protocol.Version {
		t.Fatalf("hello protocol_version = %d, want %d", payload.ProtocolVersion, cfg.Protocol.Version)
	}
	if payload.HeartbeatIntervalMS != cfg.HeartbeatIntervalMillis() {
		t.Fatalf("hello heartbeat_interval_ms = %d, want %d", payload.HeartbeatIntervalMS, cfg.HeartbeatIntervalMillis())
	}
	if payload.ResumeBufferEvents != cfg.Protocol.ResumeBuffer {
		t.Fatalf("hello resume_buffer_events = %d, want %d", payload.ResumeBufferEvents, cfg.Protocol.ResumeBuffer)
	}
	if registry.Count() != 1 {
		t.Fatalf("registry.Count() = %d, want 1", registry.Count())
	}
	if _, ok := registry.Get(payload.ConnectionID); !ok {
		t.Fatalf("registry missing connection %q", payload.ConnectionID)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	waitFor(t, time.Second, func() bool { return registry.Count() == 0 }, "registry remove closed connection")
}

func TestHandlerUpgradeAllowsLocalNoAuthModeWithoutCookie(t *testing.T) {
	start := time.Unix(1760000000, 0)
	clock := newManualClock(start)
	hbTicker := newManualTicker()
	registry := NewRegistry()
	cfg := config.Load()
	cfg.Protocol.HeartbeatInterval = 15 * time.Second
	cfg.Protocol.ResumeBuffer = 8
	h := NewHandler(cfg,
		WithNow(clock.Now),
		WithTickerFactory(func(time.Duration) Ticker { return hbTicker }),
		WithRegistry(registry),
	)
	server := httptest.NewServer(h)
	defer server.Close()

	conn := dialTestWebSocket(t, server, cfg, "")
	defer conn.Close()

	raw := readRawFrame(t, conn)
	if raw.Type != FrameTypeHello {
		t.Fatalf("hello frame type = %q, want %q", raw.Type, FrameTypeHello)
	}
}

func TestHandlerHandlesCoreCommandsAndHeartbeats(t *testing.T) {
	start := time.Unix(1760000000, 0)
	clock := newManualClock(start)
	hbTicker := newManualTicker()
	registry := NewRegistry()
	cfg := testWSConfig()
	h := NewHandler(cfg,
		WithNow(clock.Now),
		WithTickerFactory(func(time.Duration) Ticker { return hbTicker }),
		WithRegistry(registry),
	)
	server := httptest.NewServer(h)
	defer server.Close()

	conn := dialTestWebSocket(t, server, cfg, "")
	defer conn.Close()

	hello := readRawFrame(t, conn)
	connID := decodePayload[HelloPayload](t, hello).ConnectionID

	subscribeAt := start.Add(time.Second)
	clock.Set(subscribeAt)
	writeFrame(t, conn, Frame{
		Type:      FrameTypeSubscribe,
		RequestID: "req_sub_1",
		TS:        UnixTS(subscribeAt),
		Stream:    SystemStream.String(),
		Payload:   SubscribePayload{Streams: []Subscription{{Name: SessionsStream}, {Name: StreamName("session:s_123")}}},
	})
	ack := readRawFrame(t, conn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("subscribe response type = %q, want %q", ack.Type, FrameTypeAck)
	}
	ackPayload := decodePayload[AckPayload](t, ack)
	if ackPayload.RequestID != "req_sub_1" || ackPayload.Command != string(FrameTypeSubscribe) {
		t.Fatalf("subscribe ack payload = %#v", ackPayload)
	}
	waitFor(t, time.Second, func() bool {
		state, ok := registry.Get(connID)
		return ok && len(state.Subscriptions()) == 2
	}, "subscribe state update")

	pingAt := start.Add(2 * time.Second)
	clock.Set(pingAt)
	writeFrame(t, conn, Frame{
		Type:    FrameTypePing,
		TS:      UnixTS(pingAt),
		Stream:  SystemStream.String(),
		Payload: map[string]any{},
	})
	waitFor(t, time.Second, func() bool {
		state, ok := registry.Get(connID)
		return ok && state.Heartbeat().LastClientPing().Equal(pingAt)
	}, "ping heartbeat update")

	unsubscribeAt := start.Add(3 * time.Second)
	clock.Set(unsubscribeAt)
	writeFrame(t, conn, Frame{
		Type:      FrameTypeUnsubscribe,
		RequestID: "req_unsub_1",
		TS:        UnixTS(unsubscribeAt),
		Stream:    SystemStream.String(),
		Payload:   UnsubscribePayload{Streams: []string{SessionsStream.String(), "session:s_123"}},
	})
	ack = readRawFrame(t, conn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("unsubscribe response type = %q, want %q", ack.Type, FrameTypeAck)
	}
	ackPayload = decodePayload[AckPayload](t, ack)
	if ackPayload.RequestID != "req_unsub_1" || ackPayload.Command != string(FrameTypeUnsubscribe) {
		t.Fatalf("unsubscribe ack payload = %#v", ackPayload)
	}
	waitFor(t, time.Second, func() bool {
		state, ok := registry.Get(connID)
		return ok && len(state.Subscriptions()) == 0
	}, "unsubscribe state update")

	heartbeatAt := unsubscribeAt.Add(cfg.Protocol.HeartbeatInterval)
	hbTicker.Tick(heartbeatAt)
	heartbeat := readRawFrame(t, conn)
	if heartbeat.Type != FrameTypeHeartbeat {
		t.Fatalf("heartbeat frame type = %q, want %q", heartbeat.Type, FrameTypeHeartbeat)
	}
	heartbeatPayload := decodePayload[HeartbeatPayload](t, heartbeat)
	if heartbeatPayload.ConnectionID != connID {
		t.Fatalf("heartbeat connection_id = %q, want %q", heartbeatPayload.ConnectionID, connID)
	}
}

func TestHandlerPublishSessionStoresReplayAndDeliversLive(t *testing.T) {
	start := time.Unix(1760000000, 0)
	clock := newManualClock(start)
	hbTicker := newManualTicker()
	cfg := testWSConfig()
	h := NewHandler(cfg,
		WithNow(clock.Now),
		WithTickerFactory(func(time.Duration) Ticker { return hbTicker }),
		WithRegistry(NewRegistry()),
	)
	server := httptest.NewServer(h)
	defer server.Close()

	connA := dialTestWebSocket(t, server, cfg, "")
	defer connA.Close()
	_ = readRawFrame(t, connA)

	subscribeAt := start.Add(time.Second)
	clock.Set(subscribeAt)
	writeFrame(t, connA, Frame{
		Type:      FrameTypeSubscribe,
		RequestID: "req_sub_live",
		TS:        UnixTS(subscribeAt),
		Stream:    SystemStream.String(),
		Payload:   SubscribePayload{Streams: []Subscription{{Name: StreamName("session:s_123")}}},
	})
	ack := readRawFrame(t, connA)
	if ack.Type != FrameTypeAck {
		t.Fatalf("subscribe ack type = %q, want %q", ack.Type, FrameTypeAck)
	}

	publishAt := subscribeAt.Add(time.Second)
	clock.Set(publishAt)
	frame := Frame{
		Type:   FrameTypeSessionState,
		ID:     "evt_pub_1",
		TS:     UnixTS(publishAt),
		Stream: "session:s_123",
		Payload: map[string]any{
			"session_id": "s_123",
			"stream_seq": 41,
			"busy":       true,
		},
	}
	report, err := h.PublishSession(41, frame)
	if err != nil {
		t.Fatalf("PublishSession() error = %v", err)
	}
	if !report.Stored || report.Delivered != 1 {
		t.Fatalf("PublishSession() report = %#v", report)
	}
	live := readRawFrame(t, connA)
	if live.Type != FrameTypeSessionState || live.ID != frame.ID {
		t.Fatalf("live frame = %#v, want id %q", live, frame.ID)
	}

	connB := dialTestWebSocket(t, server, cfg, "")
	defer connB.Close()
	_ = readRawFrame(t, connB)

	resumeAt := publishAt.Add(time.Second)
	clock.Set(resumeAt)
	cursor := int64(40)
	writeFrame(t, connB, Frame{
		Type:      FrameTypeSubscribe,
		RequestID: "req_sub_resume",
		TS:        UnixTS(resumeAt),
		Stream:    SystemStream.String(),
		Payload:   SubscribePayload{Streams: []Subscription{{Name: StreamName("session:s_123"), ResumeFrom: &cursor}}},
	})
	ack = readRawFrame(t, connB)
	if ack.Type != FrameTypeAck {
		t.Fatalf("resume ack type = %q, want %q", ack.Type, FrameTypeAck)
	}
	replayed := readRawFrame(t, connB)
	if replayed.Type != FrameTypeSessionState || replayed.ID != frame.ID {
		t.Fatalf("replayed frame = %#v, want id %q", replayed, frame.ID)
	}
}

func TestHandlerDispatchesCommandFramesThroughTarget(t *testing.T) {
	start := time.Unix(1760000000, 0)
	clock := newManualClock(start)
	hbTicker := newManualTicker()
	cfg := testWSConfig()
	target := &fakeCommandTarget{}
	h := NewHandler(cfg,
		WithNow(clock.Now),
		WithTickerFactory(func(time.Duration) Ticker { return hbTicker }),
		WithCommandTarget(target),
	)
	server := httptest.NewServer(h)
	defer server.Close()

	conn := dialTestWebSocket(t, server, cfg, "")
	defer conn.Close()
	_ = readRawFrame(t, conn)

	frames := []struct {
		at      time.Time
		frame   Frame
		command FrameType
	}{
		{
			at:      start.Add(time.Second),
			frame:   Frame{Type: FrameTypeSend, RequestID: "req_send_1", TS: UnixTS(start.Add(time.Second)), Stream: "session:s_123", Payload: map[string]any{"session_id": "s_123", "text": "hello"}},
			command: FrameTypeSend,
		},
		{
			at:      start.Add(2 * time.Second),
			frame:   Frame{Type: FrameTypeEnqueue, RequestID: "req_enqueue_1", TS: UnixTS(start.Add(2 * time.Second)), Stream: "session:s_123", Payload: map[string]any{"session_id": "s_123", "text": "queued"}},
			command: FrameTypeEnqueue,
		},
		{
			at:      start.Add(3 * time.Second),
			frame:   Frame{Type: FrameTypeInterrupt, RequestID: "req_interrupt_1", TS: UnixTS(start.Add(3 * time.Second)), Stream: "session:s_123", Payload: map[string]any{"session_id": "s_123"}},
			command: FrameTypeInterrupt,
		},
		{
			at:      start.Add(4 * time.Second),
			frame:   Frame{Type: FrameTypeUIResponse, RequestID: "req_ui_1", TS: UnixTS(start.Add(4 * time.Second)), Stream: "session:s_123:ui", Payload: map[string]any{"session_id": "s_123", "response_to": "ask_1", "value": map[string]any{"choice": "A"}}},
			command: FrameTypeUIResponse,
		},
	}

	for _, tt := range frames {
		clock.Set(tt.at)
		writeFrame(t, conn, tt.frame)
		raw := readRawFrame(t, conn)
		if raw.Type != FrameTypeAck {
			t.Fatalf("response type = %q, want %q", raw.Type, FrameTypeAck)
		}
		payload := decodePayload[AckPayload](t, raw)
		if payload.RequestID != tt.frame.RequestID || payload.Command != string(tt.command) || !payload.Accepted {
			t.Fatalf("ack payload = %#v", payload)
		}
	}

	if len(target.sends) != 1 || target.sends[0].Text != "hello" || target.sends[0].SessionID.String() != "s_123" {
		t.Fatalf("target.sends = %#v", target.sends)
	}
	if len(target.enqueues) != 1 || target.enqueues[0].Text != "queued" || target.enqueues[0].SessionID.String() != "s_123" {
		t.Fatalf("target.enqueues = %#v", target.enqueues)
	}
	if len(target.interrupts) != 1 || target.interrupts[0].SessionID.String() != "s_123" {
		t.Fatalf("target.interrupts = %#v", target.interrupts)
	}
	if len(target.uiResponses) != 1 || target.uiResponses[0].ResponseTo != "ask_1" || string(target.uiResponses[0].Value) != `{"choice":"A"}` {
		t.Fatalf("target.uiResponses = %#v", target.uiResponses)
	}
}

func TestHandlerMapsCommandTargetErrorsToWebSocketErrors(t *testing.T) {
	start := time.Unix(1760000000, 0)
	clock := newManualClock(start)
	hbTicker := newManualTicker()
	cfg := testWSConfig()
	target := &fakeCommandTarget{
		sendErr:       NewCommandError(ErrorCodeConflict, "session busy", "session_id"),
		enqueueErr:    NewCommandError(ErrorCodeNotFound, "session missing", "session_id"),
		interruptErr:  NewCommandError(ErrorCodeUnsupported, "interrupt disabled", "type"),
		uiResponseErr: NewCommandError(ErrorCodeInvalidRequest, "stale response", "response_to"),
	}
	h := NewHandler(cfg,
		WithNow(clock.Now),
		WithTickerFactory(func(time.Duration) Ticker { return hbTicker }),
		WithCommandTarget(target),
	)
	server := httptest.NewServer(h)
	defer server.Close()

	conn := dialTestWebSocket(t, server, cfg, "")
	defer conn.Close()
	_ = readRawFrame(t, conn)

	tests := []struct {
		at      time.Time
		frame   Frame
		code    ErrorCode
		message string
		field   string
	}{
		{
			at:    start.Add(time.Second),
			frame: Frame{Type: FrameTypeSend, RequestID: "req_send_1", TS: UnixTS(start.Add(time.Second)), Stream: "session:s_123", Payload: map[string]any{"session_id": "s_123", "text": "hello"}},
			code:  ErrorCodeConflict, message: "session busy", field: "session_id",
		},
		{
			at:    start.Add(2 * time.Second),
			frame: Frame{Type: FrameTypeEnqueue, RequestID: "req_enqueue_1", TS: UnixTS(start.Add(2 * time.Second)), Stream: "session:s_123", Payload: map[string]any{"session_id": "s_123", "text": "queued"}},
			code:  ErrorCodeNotFound, message: "session missing", field: "session_id",
		},
		{
			at:    start.Add(3 * time.Second),
			frame: Frame{Type: FrameTypeInterrupt, RequestID: "req_interrupt_1", TS: UnixTS(start.Add(3 * time.Second)), Stream: "session:s_123", Payload: map[string]any{"session_id": "s_123"}},
			code:  ErrorCodeUnsupported, message: "interrupt disabled", field: "type",
		},
		{
			at:    start.Add(4 * time.Second),
			frame: Frame{Type: FrameTypeUIResponse, RequestID: "req_ui_1", TS: UnixTS(start.Add(4 * time.Second)), Stream: "session:s_123:ui", Payload: map[string]any{"session_id": "s_123", "response_to": "ask_1", "value": "A"}},
			code:  ErrorCodeInvalidRequest, message: "stale response", field: "response_to",
		},
	}

	for _, tt := range tests {
		clock.Set(tt.at)
		writeFrame(t, conn, tt.frame)
		raw := readRawFrame(t, conn)
		if raw.Type != FrameTypeError {
			t.Fatalf("response type = %q, want %q", raw.Type, FrameTypeError)
		}
		payload := decodePayload[ErrorPayload](t, raw)
		if payload.RequestID != tt.frame.RequestID || payload.Code != tt.code || payload.Message != tt.message || payload.Field != tt.field {
			t.Fatalf("error payload = %#v", payload)
		}
	}
}

func TestHandlerReturnsUnsupportedErrorWithoutCommandTarget(t *testing.T) {
	start := time.Unix(1760000000, 0)
	clock := newManualClock(start)
	hbTicker := newManualTicker()
	cfg := testWSConfig()
	h := NewHandler(cfg,
		WithNow(clock.Now),
		WithTickerFactory(func(time.Duration) Ticker { return hbTicker }),
	)
	server := httptest.NewServer(h)
	defer server.Close()

	conn := dialTestWebSocket(t, server, cfg, "")
	defer conn.Close()
	_ = readRawFrame(t, conn)

	writeFrame(t, conn, Frame{
		Type:      FrameTypeSend,
		RequestID: "req_send_1",
		TS:        UnixTS(start.Add(time.Second)),
		Stream:    "session:s_123",
		Payload:   map[string]any{"session_id": "s_123", "text": "hello"},
	})
	raw := readRawFrame(t, conn)
	if raw.Type != FrameTypeError {
		t.Fatalf("error frame type = %q, want %q", raw.Type, FrameTypeError)
	}
	payload := decodePayload[ErrorPayload](t, raw)
	if payload.RequestID != "req_send_1" || payload.Code != ErrorCodeUnsupported || payload.Message != `command "send" requires a command target` {
		t.Fatalf("error payload = %#v", payload)
	}
}

func testWSConfig() config.Config {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	cfg.Protocol.HeartbeatInterval = 15 * time.Second
	cfg.Protocol.ResumeBuffer = 8
	return cfg
}

func dialTestWebSocket(t *testing.T, server *httptest.Server, cfg config.Config, query string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	if authn.Configured(cfg.Auth) {
		cookie, err := authn.SessionCookie(cfg.Auth)
		if err != nil {
			t.Fatalf("SessionCookie() error = %v", err)
		}
		header.Add("Cookie", cookie.Name+"="+cookie.Value)
	}
	conn, res, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+cfg.Protocol.WebSocketPath+query, header)
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		t.Fatalf("Dial() error = %v, status = %d", err, status)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	return conn
}

func writeFrame(t *testing.T, conn *websocket.Conn, frame Frame) {
	t.Helper()
	payload, err := (Codec{}).Encode(frame)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("WriteMessage() error = %v", err)
	}
}

func readRawFrame(t *testing.T, conn *websocket.Conn) RawFrame {
	t.Helper()
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	frame, err := (Codec{}).Decode(payload)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	return frame
}

func decodePayload[T any](t *testing.T, frame RawFrame) T {
	t.Helper()
	var payload T
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	return payload
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", label)
}
