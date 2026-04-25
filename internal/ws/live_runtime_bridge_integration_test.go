package ws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/app"
	"actrail/internal/config"
	"actrail/internal/domain/session"
	"actrail/internal/httpapi/authn"

	"github.com/gorilla/websocket"
)

type testPTY struct {
	mu     sync.Mutex
	writes []string
}

func (p *testPTY) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (p *testPTY) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, string(data))
	return len(data), nil
}

func (p *testPTY) Close() error {
	return nil
}

func (p *testPTY) Resize(process.PTYSize) error {
	return nil
}

func (p *testPTY) Writes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.writes))
	copy(out, p.writes)
	return out
}

func TestLiveRuntimeBridgePublishesRuntimeEventsOverWebSocket(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	svc, cfg, server, cookie := newLiveBridgeServer(t, &process.FakeRunner{NextHandle: handle})
	defer server.Close()

	created, err := svc.CreateSession(context.Background(), app.CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if created.Session == nil || created.WSAttach == nil {
		t.Fatalf("CreateSessionResponse = %+v, want session and ws attach", created)
	}

	conn := dialBridgeWebSocket(t, server.URL, cfg, cookie)
	defer conn.Close()
	subscribeBridgeStreams(t, conn, []Subscription{
		{Name: StreamName(created.WSAttach.SuggestSubscriptions[0])},
		{Name: StreamName("session:" + created.Session.SessionID + ":ui")},
	})

	_, _ = stdoutW.Write([]byte("{" +
		"\"type\":\"extension_ui_request\",\"id\":\"ui-req-1\",\"method\":\"select\",\"question\":\"Where should this go?\",\"options\":[\"Details\",\"Sidebar\"]}" + "\n" +
		"{\"type\":\"message.delta\",\"turn_id\":\"turn-001\",\"role\":\"assistant\",\"delta\":\"Codoxear serves a browser UI for Codex-style sessions.\"}" + "\n" +
		"{\"type\":\"message_end\",\"message\":{\"role\":\"toolResult\",\"toolCallId\":\"ui-req-1\",\"toolName\":\"ask_user\",\"details\":{\"answer\":\"Sidebar\",\"cancelled\":false}}}" + "\n" +
		"{\"type\":\"turn.completed\",\"turn_id\":\"turn-001\",\"role\":\"assistant\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}" + "\n"))
	_ = stdoutW.Close()

	frames := collectBridgeFrames(t, conn, 2*time.Second, map[FrameType]bool{
		FrameTypeMessageDelta:  false,
		FrameTypeMessageCommit: false,
		FrameTypeUIRequest:     false,
		FrameTypeUIResolved:    false,
	})
	var delta messageDeltaPayload
	decodeBridgePayload(t, frames[FrameTypeMessageDelta], &delta)
	if delta.Delta != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("message.delta payload = %+v", delta)
	}
	var commit messageCommitPayload
	decodeBridgePayload(t, frames[FrameTypeMessageCommit], &commit)
	if commit.Message.Role != "assistant" || commit.Message.Text != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("message.commit payload = %+v", commit)
	}
	var request uiRequestPayload
	decodeBridgePayload(t, frames[FrameTypeUIRequest], &request)
	if request.Request.RequestID != "ui-req-1" || request.Request.Prompt != "Where should this go?" {
		t.Fatalf("ui.request payload = %+v", request)
	}
	if len(request.Request.Options) != 2 || request.Request.Options[1] != "Sidebar" {
		t.Fatalf("ui.request options = %#v", request.Request.Options)
	}
	var resolved uiResolvedPayload
	decodeBridgePayload(t, frames[FrameTypeUIResolved], &resolved)
	if resolved.RequestID != "ui-req-1" {
		t.Fatalf("ui.resolved payload = %+v", resolved)
	}

	waitForBridgeCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), app.SessionStateRequest{SessionID: mustSessionID(t, created.Session.SessionID)})
		if err != nil {
			return false
		}
		return state.TailSeq == 1 && state.ResumeCursors.Session != "" && state.ResumeCursors.UI != ""
	})

	messages, err := svc.SessionMessages(context.Background(), app.SessionMessagesRequest{SessionID: mustSessionID(t, created.Session.SessionID)})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("SessionMessages() = %+v", messages)
	}
	state, err := svc.SessionState(context.Background(), app.SessionStateRequest{SessionID: mustSessionID(t, created.Session.SessionID)})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.ResumeCursors.Session == "" || state.ResumeCursors.UI == "" {
		t.Fatalf("SessionState().ResumeCursors = %+v, want session/ui cursors", state.ResumeCursors)
	}
}

func TestLiveRuntimeBridgeRoutesWebSocketCommandsIntoAppControl(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	pty := &testPTY{}
	handle.SetPTY(pty)
	svc, cfg, server, cookie := newLiveBridgeServer(t, &process.FakeRunner{NextHandle: handle})
	defer server.Close()

	created, err := svc.CreateSession(context.Background(), app.CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if created.Session == nil {
		t.Fatalf("CreateSessionResponse = %+v, want session", created)
	}
	sessionID := created.Session.SessionID
	parsed := mustSessionID(t, sessionID)
	if err := svc.SetSessionUIRequest(parsed, app.SessionUIRequestSnapshot{RequestID: "ask_1", Kind: "ask_user", Prompt: "Choose one option"}); err != nil {
		t.Fatalf("SetSessionUIRequest() error = %v", err)
	}

	subConn := dialBridgeWebSocket(t, server.URL, cfg, cookie)
	defer subConn.Close()
	subscribeBridgeStreams(t, subConn, []Subscription{
		{Name: StreamName("session:" + sessionID)},
		{Name: StreamName("session:" + sessionID + ":ui")},
	})

	cmdConn := dialBridgeWebSocket(t, server.URL, cfg, cookie)
	defer cmdConn.Close()

	writeBridgeFrame(t, cmdConn, Frame{
		Type:      FrameTypeSend,
		RequestID: "req_send_1",
		TS:        UnixTS(time.Now()),
		Stream:    "session:" + sessionID,
		Payload:   map[string]any{"session_id": sessionID, "text": "Implement runtime bridge"},
	})
	ack := readBridgeFrame(t, cmdConn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("send ack type = %q, want %q", ack.Type, FrameTypeAck)
	}
	sendCommit := waitForBridgeFrame(t, subConn, 2*time.Second, func(frame RawFrame) bool { return frame.Type == FrameTypeMessageCommit })
	var sendPayload messageCommitPayload
	decodeBridgePayload(t, sendCommit, &sendPayload)
	if sendPayload.Message.Role != "user" || sendPayload.Message.Text != "Implement runtime bridge" {
		t.Fatalf("send commit payload = %+v", sendPayload)
	}

	writeBridgeFrame(t, cmdConn, Frame{
		Type:      FrameTypeEnqueue,
		RequestID: "req_enqueue_1",
		TS:        UnixTS(time.Now()),
		Stream:    "session:" + sessionID,
		Payload:   map[string]any{"session_id": sessionID, "text": "queued task"},
	})
	ack = readBridgeFrame(t, cmdConn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("enqueue ack type = %q, want %q", ack.Type, FrameTypeAck)
	}
	enqueueState := waitForBridgeFrame(t, subConn, 2*time.Second, func(frame RawFrame) bool {
		if frame.Type != FrameTypeQueueState {
			return false
		}
		var payload queueStatePayload
		decodeBridgePayload(t, frame, &payload)
		return len(payload.Items) == 1 && payload.Items[0].Text == "queued task"
	})
	var queuePayload queueStatePayload
	decodeBridgePayload(t, enqueueState, &queuePayload)
	if len(queuePayload.Items) != 1 || queuePayload.Items[0].Text != "queued task" {
		t.Fatalf("enqueue queue payload = %+v", queuePayload)
	}

	writeBridgeFrame(t, cmdConn, Frame{
		Type:      FrameTypeInterrupt,
		RequestID: "req_interrupt_1",
		TS:        UnixTS(time.Now()),
		Stream:    "session:" + sessionID,
		Payload:   map[string]any{"session_id": sessionID},
	})
	ack = readBridgeFrame(t, cmdConn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("interrupt ack type = %q, want %q", ack.Type, FrameTypeAck)
	}
	interruptState := waitForBridgeFrame(t, subConn, 2*time.Second, func(frame RawFrame) bool {
		if frame.Type != FrameTypeSessionState {
			return false
		}
		var payload sessionStatePayload
		decodeBridgePayload(t, frame, &payload)
		return !payload.Busy
	})
	var statePayload sessionStatePayload
	decodeBridgePayload(t, interruptState, &statePayload)
	if statePayload.Busy {
		t.Fatalf("interrupt session.state payload = %+v", statePayload)
	}

	writeBridgeFrame(t, cmdConn, Frame{
		Type:      FrameTypeUIResponse,
		RequestID: "req_ui_1",
		TS:        UnixTS(time.Now()),
		Stream:    "session:" + sessionID + ":ui",
		Payload:   map[string]any{"session_id": sessionID, "response_to": "ask_1", "value": "A"},
	})
	ack = readBridgeFrame(t, cmdConn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("ui.response ack type = %q, want %q", ack.Type, FrameTypeAck)
	}
	resolved := waitForBridgeFrame(t, subConn, 2*time.Second, func(frame RawFrame) bool { return frame.Type == FrameTypeUIResolved })
	var resolvedPayload uiResolvedPayload
	decodeBridgePayload(t, resolved, &resolvedPayload)
	if resolvedPayload.RequestID != "ask_1" {
		t.Fatalf("ui.resolved payload = %+v", resolvedPayload)
	}

	writes := pty.Writes()
	if len(writes) != 2 || writes[0] != "Implement runtime bridge\n" || writes[1] != "A\n" {
		t.Fatalf("pty writes = %#v", writes)
	}
	state, err := svc.SessionState(context.Background(), app.SessionStateRequest{SessionID: parsed})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy = true, want false")
	}
	if len(state.Queue.Items) != 1 || state.Queue.Items[0].Text != "queued task" {
		t.Fatalf("SessionState().Queue = %+v", state.Queue)
	}
	if state.ResumeCursors.Session == "" || state.ResumeCursors.UI == "" {
		t.Fatalf("SessionState().ResumeCursors = %+v, want session/ui cursors", state.ResumeCursors)
	}
}

func newLiveBridgeServer(t *testing.T, runner *process.FakeRunner) (*app.Stub, config.Config, *httptest.Server, *http.Cookie) {
	t.Helper()
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	svc := app.NewStubForTest(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, app.RuntimeConfig{Runner: runner})
	replay, err := NewReplayBuffer(cfg.Protocol.ResumeBuffer)
	if err != nil {
		t.Fatalf("NewReplayBuffer() error = %v", err)
	}
	registry := NewRegistry()
	publisher := NewPublisher(registry, replay)
	bridge := NewAppBridge(svc, svc, publisher)
	svc.SetRuntimeEventSink(bridge)
	server := httptest.NewServer(NewHandler(cfg,
		WithRegistry(registry),
		WithReplayBuffer(replay),
		WithCommandTarget(bridge),
	))
	cookie, err := authn.SessionCookie(cfg.Auth)
	if err != nil {
		server.Close()
		t.Fatalf("SessionCookie() error = %v", err)
	}
	return svc, cfg, server, cookie
}

func dialBridgeWebSocket(t *testing.T, baseURL string, cfg config.Config, cookie *http.Cookie) *websocket.Conn {
	t.Helper()
	url := strings.Replace(baseURL, "http://", "ws://", 1) + cfg.Protocol.WebSocketPath
	headers := http.Header{}
	headers.Add("Cookie", cookie.String())
	conn, _, err := websocket.DefaultDialer.Dial(url, headers)
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	_ = readBridgeFrame(t, conn)
	return conn
}

func subscribeBridgeStreams(t *testing.T, conn *websocket.Conn, streams []Subscription) {
	t.Helper()
	writeBridgeFrame(t, conn, Frame{
		Type:      FrameTypeSubscribe,
		RequestID: "req_sub_1",
		TS:        UnixTS(time.Now()),
		Stream:    SystemStream.String(),
		Payload:   SubscribePayload{Streams: streams},
	})
	ack := readBridgeFrame(t, conn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("subscribe ack type = %q, want %q", ack.Type, FrameTypeAck)
	}
}

func writeBridgeFrame(t *testing.T, conn *websocket.Conn, frame Frame) {
	t.Helper()
	payload, err := (Codec{}).Encode(frame)
	if err != nil {
		t.Fatalf("Codec.Encode() error = %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("WriteMessage() error = %v", err)
	}
}

func readBridgeFrame(t *testing.T, conn *websocket.Conn) RawFrame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	frame, err := (Codec{}).Decode(payload)
	if err != nil {
		t.Fatalf("Codec.Decode() error = %v", err)
	}
	return frame
}

func waitForBridgeFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration, pred func(RawFrame) bool) RawFrame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		frame := readBridgeFrame(t, conn)
		if pred(frame) {
			return frame
		}
	}
	t.Fatal("matching websocket frame not received before timeout")
	return RawFrame{}
}

func collectBridgeFrames(t *testing.T, conn *websocket.Conn, timeout time.Duration, want map[FrameType]bool) map[FrameType]RawFrame {
	t.Helper()
	frames := make(map[FrameType]RawFrame, len(want))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		frame := readBridgeFrame(t, conn)
		if _, ok := want[frame.Type]; ok && !want[frame.Type] {
			want[frame.Type] = true
			frames[frame.Type] = frame
			complete := true
			for _, seen := range want {
				if !seen {
					complete = false
					break
				}
			}
			if complete {
				return frames
			}
		}
	}
	t.Fatal("expected websocket frames not received before timeout")
	return nil
}

func decodeBridgePayload(t *testing.T, frame RawFrame, dst any) {
	t.Helper()
	if err := json.Unmarshal(frame.Payload, dst); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", frame.Type, err)
	}
}

func waitForBridgeCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func mustSessionID(t *testing.T, raw string) session.SessionID {
	t.Helper()
	id, err := session.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	return id
}
