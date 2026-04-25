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
	reader *io.PipeReader
	writer *io.PipeWriter
}

func newStreamingPTY() *testPTY {
	reader, writer := io.Pipe()
	return &testPTY{reader: reader, writer: writer}
}

func (p *testPTY) Read(data []byte) (int, error) {
	if p.reader == nil {
		return 0, io.EOF
	}
	return p.reader.Read(data)
}

func (p *testPTY) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, string(data))
	return len(data), nil
}

func (p *testPTY) Close() error {
	if p.reader != nil {
		_ = p.reader.Close()
	}
	if p.writer != nil {
		return p.writer.Close()
	}
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

func (p *testPTY) PushOutput(data string) {
	if p.writer == nil {
		return
	}
	_, _ = io.WriteString(p.writer, data)
}

func (p *testPTY) FinishOutput() {
	if p.writer == nil {
		return
	}
	_ = p.writer.Close()
	p.writer = nil
}

func TestLiveRuntimeBridgePublishesAssistantReplyPathOverWebSocketAndMessages(t *testing.T) {
	pty := newStreamingPTY()
	defer pty.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPTY(pty)
	svc, cfg, server, cookie := newLiveBridgeServer(t, &process.FakeRunner{NextHandle: handle})
	defer server.Close()

	created, err := svc.CreateSession(context.Background(), app.CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if created.Session == nil || created.WSAttach == nil {
		t.Fatalf("CreateSessionResponse = %+v, want session and ws attach", created)
	}
	mainStream := StreamName(created.WSAttach.SuggestSubscriptions[0])

	subConn := dialBridgeWebSocket(t, server.URL, cfg, cookie)
	defer subConn.Close()
	subscribeBridgeStreams(t, subConn, []Subscription{{Name: mainStream}})

	cmdConn := dialBridgeWebSocket(t, server.URL, cfg, cookie)
	defer cmdConn.Close()
	writeBridgeFrame(t, cmdConn, Frame{
		Type:      FrameTypeSend,
		RequestID: "req_send_reply_1",
		TS:        UnixTS(time.Now()),
		Stream:    mainStream.String(),
		Payload:   map[string]any{"session_id": created.Session.SessionID, "text": "Explain ActRail"},
	})
	ack := readBridgeFrame(t, cmdConn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("send ack type = %q, want %q", ack.Type, FrameTypeAck)
	}

	waitForBridgeCondition(t, func() bool {
		writes := pty.Writes()
		return len(writes) == 1
	})
	writes := pty.Writes()
	if len(writes) != 1 || writes[0] != "{\"type\":\"prompt\",\"message\":\"Explain ActRail\"}\n" {
		t.Fatalf("pty writes after send = %#v, want RPC prompt command", writes)
	}

	pty.PushOutput("{" +
		"\"id\":\"req_prompt_1\",\"type\":\"response\",\"command\":\"prompt\",\"success\":true}" + "\n" +
		"{\"type\":\"turn_start\"}" + "\n" +
		"{\"type\":\"message_update\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves \"}],\"timestamp\":1774708716099},\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"Codoxear serves \",\"partial\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves \"}],\"timestamp\":1774708716099}}}" + "\n" +
		"{\"type\":\"message_update\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}],\"timestamp\":1774708716099},\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"a browser UI for Codex-style sessions.\",\"partial\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}],\"timestamp\":1774708716099}}}" + "\n" +
		"{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}],\"stopReason\":\"stop\",\"timestamp\":1774708716099}}" + "\n" +
		"{\"type\":\"turn_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}],\"stopReason\":\"stop\",\"timestamp\":1774708716099},\"toolResults\":[]}" + "\n")
	pty.FinishOutput()

	deltaFrame := waitForBridgeFrame(t, subConn, 2*time.Second, func(frame RawFrame) bool {
		return frame.Type == FrameTypeMessageDelta
	})
	var delta messageDeltaPayload
	decodeBridgePayload(t, deltaFrame, &delta)
	if delta.Delta != "Codoxear serves " {
		t.Fatalf("message.delta payload = %+v", delta)
	}

	commitFrame := waitForBridgeFrame(t, subConn, 2*time.Second, func(frame RawFrame) bool {
		if frame.Type != FrameTypeMessageCommit {
			return false
		}
		var payload messageCommitPayload
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return false
		}
		return payload.Message.Role == "assistant"
	})
	var commit messageCommitPayload
	decodeBridgePayload(t, commitFrame, &commit)
	if commit.Message.Role != "assistant" || commit.Message.Text != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("message.commit payload = %+v", commit)
	}

	waitForBridgeCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), app.SessionStateRequest{SessionID: mustSessionID(t, created.Session.SessionID)})
		if err != nil {
			return false
		}
		return !state.Busy && state.TailSeq == 2 && state.ResumeCursors.Session != ""
	})

	messages, err := svc.SessionMessages(context.Background(), app.SessionMessagesRequest{SessionID: mustSessionID(t, created.Session.SessionID)})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 2 {
		t.Fatalf("len(SessionMessages().Items) = %d, want 2", len(messages.Items))
	}
	if messages.Items[0].Role != "user" || messages.Items[0].Text != "Explain ActRail" {
		t.Fatalf("SessionMessages().Items[0] = %+v, want committed user prompt", messages.Items[0])
	}
	if messages.Items[1].Role != "assistant" || messages.Items[1].Text != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("SessionMessages().Items[1] = %+v, want committed assistant reply", messages.Items[1])
	}
	state, err := svc.SessionState(context.Background(), app.SessionStateRequest{SessionID: mustSessionID(t, created.Session.SessionID)})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.ResumeCursors.Session == "" {
		t.Fatalf("SessionState().ResumeCursors = %+v, want session cursor", state.ResumeCursors)
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

	mainStream := StreamName("session:" + sessionID)
	uiStream := StreamName("session:" + sessionID + ":ui")
	cmdConn := dialBridgeWebSocket(t, server.URL, cfg, cookie)
	defer cmdConn.Close()

	sendSub := dialBridgeWebSocket(t, server.URL, cfg, cookie)
	subscribeBridgeStreams(t, sendSub, []Subscription{{Name: mainStream}})
	writeBridgeFrame(t, cmdConn, Frame{
		Type:      FrameTypeSend,
		RequestID: "req_send_1",
		TS:        UnixTS(time.Now()),
		Stream:    mainStream.String(),
		Payload:   map[string]any{"session_id": sessionID, "text": "Implement runtime bridge"},
	})
	ack := readBridgeFrame(t, cmdConn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("send ack type = %q, want %q", ack.Type, FrameTypeAck)
	}
	sendFrames := readBridgeFrames(t, sendSub, 3)
	_ = sendSub.Close()
	assertBridgeFrameCounts(t, sendFrames, map[FrameType]int{
		FrameTypeMessageCommit: 1,
		FrameTypeQueueState:    1,
		FrameTypeSessionState:  1,
	})
	var sendPayload messageCommitPayload
	decodeBridgePayload(t, firstBridgeFrameOfType(t, sendFrames, FrameTypeMessageCommit), &sendPayload)
	if sendPayload.Message.Role != "user" || sendPayload.Message.Text != "Implement runtime bridge" {
		t.Fatalf("send commit payload = %+v", sendPayload)
	}
	waitForBridgeCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), app.SessionStateRequest{SessionID: parsed})
		if err != nil {
			return false
		}
		return state.ResumeCursors.Session == "3" && state.ResumeCursors.UI == ""
	})

	enqueueSub := dialBridgeWebSocket(t, server.URL, cfg, cookie)
	subscribeBridgeStreams(t, enqueueSub, []Subscription{{Name: mainStream}})
	writeBridgeFrame(t, cmdConn, Frame{
		Type:      FrameTypeEnqueue,
		RequestID: "req_enqueue_1",
		TS:        UnixTS(time.Now()),
		Stream:    mainStream.String(),
		Payload:   map[string]any{"session_id": sessionID, "text": "queued task"},
	})
	ack = readBridgeFrame(t, cmdConn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("enqueue ack type = %q, want %q", ack.Type, FrameTypeAck)
	}
	enqueueFrames := readBridgeFrames(t, enqueueSub, 2)
	_ = enqueueSub.Close()
	assertBridgeFrameCounts(t, enqueueFrames, map[FrameType]int{
		FrameTypeQueueState:   1,
		FrameTypeSessionState: 1,
	})
	var queuePayload queueStatePayload
	decodeBridgePayload(t, firstBridgeFrameOfType(t, enqueueFrames, FrameTypeQueueState), &queuePayload)
	if len(queuePayload.Items) != 1 || queuePayload.Items[0].Text != "queued task" {
		t.Fatalf("enqueue queue payload = %+v", queuePayload)
	}
	waitForBridgeCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), app.SessionStateRequest{SessionID: parsed})
		if err != nil {
			return false
		}
		return state.ResumeCursors.Session == "5" && state.ResumeCursors.UI == ""
	})

	interruptSub := dialBridgeWebSocket(t, server.URL, cfg, cookie)
	subscribeBridgeStreams(t, interruptSub, []Subscription{{Name: mainStream}})
	writeBridgeFrame(t, cmdConn, Frame{
		Type:      FrameTypeInterrupt,
		RequestID: "req_interrupt_1",
		TS:        UnixTS(time.Now()),
		Stream:    mainStream.String(),
		Payload:   map[string]any{"session_id": sessionID},
	})
	ack = readBridgeFrame(t, cmdConn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("interrupt ack type = %q, want %q", ack.Type, FrameTypeAck)
	}
	interruptFrames := readBridgeFrames(t, interruptSub, 2)
	_ = interruptSub.Close()
	assertBridgeFrameCounts(t, interruptFrames, map[FrameType]int{
		FrameTypeQueueState:   1,
		FrameTypeSessionState: 1,
	})
	var statePayload sessionStatePayload
	decodeBridgePayload(t, firstBridgeFrameOfType(t, interruptFrames, FrameTypeSessionState), &statePayload)
	if statePayload.Busy {
		t.Fatalf("interrupt session.state payload = %+v", statePayload)
	}
	waitForBridgeCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), app.SessionStateRequest{SessionID: parsed})
		if err != nil {
			return false
		}
		return state.ResumeCursors.Session == "7" && state.ResumeCursors.UI == ""
	})

	uiSub := dialBridgeWebSocket(t, server.URL, cfg, cookie)
	subscribeBridgeStreams(t, uiSub, []Subscription{{Name: mainStream}, {Name: uiStream}})
	writeBridgeFrame(t, cmdConn, Frame{
		Type:      FrameTypeUIResponse,
		RequestID: "req_ui_1",
		TS:        UnixTS(time.Now()),
		Stream:    uiStream.String(),
		Payload:   map[string]any{"session_id": sessionID, "response_to": "ask_1", "value": "A"},
	})
	ack = readBridgeFrame(t, cmdConn)
	if ack.Type != FrameTypeAck {
		t.Fatalf("ui.response ack type = %q, want %q", ack.Type, FrameTypeAck)
	}
	uiFrames := readBridgeFrames(t, uiSub, 2)
	_ = uiSub.Close()
	assertBridgeFrameCounts(t, uiFrames, map[FrameType]int{
		FrameTypeUIResolved:   1,
		FrameTypeSessionState: 1,
	})
	var resolvedPayload uiResolvedPayload
	decodeBridgePayload(t, firstBridgeFrameOfType(t, uiFrames, FrameTypeUIResolved), &resolvedPayload)
	if resolvedPayload.RequestID != "ask_1" {
		t.Fatalf("ui.resolved payload = %+v", resolvedPayload)
	}
	waitForBridgeCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), app.SessionStateRequest{SessionID: parsed})
		if err != nil {
			return false
		}
		return state.ResumeCursors.Session == "8" && state.ResumeCursors.UI == "1"
	})

	writes := pty.Writes()
	if len(writes) != 3 || writes[0] != "{\"type\":\"prompt\",\"message\":\"Implement runtime bridge\"}\n" || writes[1] != "{\"type\":\"abort\"}\n" || writes[2] != "{\"type\":\"extension_ui_response\",\"id\":\"ask_1\",\"value\":\"A\"}\n" {
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
	if state.ResumeCursors.Session != "8" || state.ResumeCursors.UI != "1" {
		t.Fatalf("SessionState().ResumeCursors = %+v, want session=8 ui=1", state.ResumeCursors)
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

func readBridgeFrames(t *testing.T, conn *websocket.Conn, count int) []RawFrame {
	t.Helper()
	frames := make([]RawFrame, 0, count)
	for i := 0; i < count; i++ {
		frames = append(frames, readBridgeFrame(t, conn))
	}
	return frames
}

func assertBridgeFrameCounts(t *testing.T, frames []RawFrame, want map[FrameType]int) {
	t.Helper()
	counts := make(map[FrameType]int, len(frames))
	for _, frame := range frames {
		counts[frame.Type]++
	}
	wantTotal := 0
	for typ, count := range want {
		wantTotal += count
		if counts[typ] != count {
			t.Fatalf("frame counts = %#v, want %q=%d in %#v", counts, typ, count, bridgeFrameTypes(frames))
		}
	}
	if len(frames) != wantTotal {
		t.Fatalf("frame count = %d, want %d in %#v", len(frames), wantTotal, bridgeFrameTypes(frames))
	}
}

func firstBridgeFrameOfType(t *testing.T, frames []RawFrame, typ FrameType) RawFrame {
	t.Helper()
	for _, frame := range frames {
		if frame.Type == typ {
			return frame
		}
	}
	t.Fatalf("frame type %q not found in %#v", typ, bridgeFrameTypes(frames))
	return RawFrame{}
}

func bridgeFrameTypes(frames []RawFrame) []FrameType {
	out := make([]FrameType, 0, len(frames))
	for _, frame := range frames {
		out = append(out, frame.Type)
	}
	return out
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
