package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

func TestRuntimeLineBufferReturnsFinalJSONWithoutTrailingNewline(t *testing.T) {
	var buffer runtimeLineBuffer
	buffer.append(`{"type":"turn.completed","turn_id":"turn-no-newline","role":"assistant","text":"final"}`)
	line, ok := buffer.nextLine()
	if !ok {
		t.Fatal("nextLine() ok = false, want true for complete JSON without newline")
	}
	if string(line) != `{"type":"turn.completed","turn_id":"turn-no-newline","role":"assistant","text":"final"}` {
		t.Fatalf("nextLine() = %q", string(line))
	}
	if _, ok := buffer.nextLine(); ok {
		t.Fatal("nextLine() returned a second frame after draining final JSON")
	}
}

func TestRuntimeDecoderIgnoresHelperStderr(t *testing.T) {
	decoder := runtimeEventDecoder{backend: session.BackendPI}
	projection := decoder.decodeHelperOutput(iodTerminalOutputPayload{
		Stream: "stderr",
		Data:   `{"type":"message_update","message":{"timestamp":1777450000000,"model":"gpt-5.5","provider":"openai"}}` + "\n",
	})
	if projection.model != "" || projection.provider != "" || len(projection.events) != 0 || projection.turnTiming != nil {
		t.Fatalf("stderr projection = %#v, want zero projection", projection)
	}
}

func TestRuntimeLineBufferKeepsPartialJSONWithoutTrailingNewline(t *testing.T) {
	var buffer runtimeLineBuffer
	buffer.append(`{"type":"turn.completed"`)
	if _, ok := buffer.nextLine(); ok {
		t.Fatal("nextLine() ok = true, want false for partial JSON")
	}
	buffer.append(`,"turn_id":"turn-finished"}`)
	line, ok := buffer.nextLine()
	if !ok {
		t.Fatal("nextLine() ok = false after JSON completion")
	}
	if string(line) != `{"type":"turn.completed","turn_id":"turn-finished"}` {
		t.Fatalf("nextLine() = %q", string(line))
	}
}

type captureRuntimeSink struct {
	mu                     sync.Mutex
	states                 []SessionStateEvent
	deltas                 []MessageDeltaEvent
	commits                []MessageCommitEvent
	queueStates            []QueueStateEvent
	uiRequests             []UIRequestEvent
	uiResolved             []UIResolvedEvent
	waitLifecycle          []WaitLifecycleEvent
	waitsUpdated           []WaitsUpdatedEvent
	generationBroken       []GenerationBrokenEvent
	transportResetRequired []TransportResetRequiredEvent
	notifications          []NotificationEvent
}

func (s *captureRuntimeSink) PublishSessionState(event SessionStateEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = append(s.states, event)
}

func (s *captureRuntimeSink) PublishMessageDelta(event MessageDeltaEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deltas = append(s.deltas, event)
}

func (s *captureRuntimeSink) PublishMessageCommit(event MessageCommitEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits = append(s.commits, event)
}

func (s *captureRuntimeSink) PublishQueueState(event QueueStateEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queueStates = append(s.queueStates, event)
}

func (s *captureRuntimeSink) PublishUIRequest(event UIRequestEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uiRequests = append(s.uiRequests, event)
}

func (s *captureRuntimeSink) PublishUIResolved(event UIResolvedEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uiResolved = append(s.uiResolved, event)
}

func (s *captureRuntimeSink) PublishWaitLifecycle(event WaitLifecycleEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitLifecycle = append(s.waitLifecycle, event)
}

func (s *captureRuntimeSink) PublishWaitsUpdated(event WaitsUpdatedEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitsUpdated = append(s.waitsUpdated, event)
}

func (s *captureRuntimeSink) PublishGenerationBroken(event GenerationBrokenEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generationBroken = append(s.generationBroken, event)
}

func (s *captureRuntimeSink) PublishTransportResetRequired(event TransportResetRequiredEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transportResetRequired = append(s.transportResetRequired, event)
}

func (s *captureRuntimeSink) PublishNotification(event NotificationEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = append(s.notifications, event)
}

func (s *captureRuntimeSink) snapshot() captureRuntimeSink {
	s.mu.Lock()
	defer s.mu.Unlock()
	return captureRuntimeSink{
		states:                 append([]SessionStateEvent(nil), s.states...),
		deltas:                 append([]MessageDeltaEvent(nil), s.deltas...),
		commits:                append([]MessageCommitEvent(nil), s.commits...),
		queueStates:            append([]QueueStateEvent(nil), s.queueStates...),
		uiRequests:             append([]UIRequestEvent(nil), s.uiRequests...),
		uiResolved:             append([]UIResolvedEvent(nil), s.uiResolved...),
		waitLifecycle:          append([]WaitLifecycleEvent(nil), s.waitLifecycle...),
		waitsUpdated:           append([]WaitsUpdatedEvent(nil), s.waitsUpdated...),
		generationBroken:       append([]GenerationBrokenEvent(nil), s.generationBroken...),
		transportResetRequired: append([]TransportResetRequiredEvent(nil), s.transportResetRequired...),
		notifications:          append([]NotificationEvent(nil), s.notifications...),
	}
}

func TestRuntimeAskUserCreatesWaitAndReturnsStructuredAnswer(t *testing.T) {
	pty := &fakePTY{}
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPTY(pty)
	now := time.Unix(1760000000, 0).UTC()
	svc := NewStubForTest(config.Load(), func() time.Time { return now }, RuntimeConfig{})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	sessionID := createWaitTestSessionID(t, svc)

	event := pi.Event{Kind: pi.EventKindUIRequest, UIRequest: &pi.UIRequest{RequestID: "ask-runtime-1", Kind: pi.UIRequestKindAskUser, Prompt: "Proceed?", Context: "Runtime context", Metadata: map[string]any{"blocking_reason": "needs decision", "attempted": "inspected files", "default_if_no_reply": "use fallback"}}}
	resultCh := make(chan RuntimeWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := svc.AskUserWait(context.Background(), RuntimeWaitRequest{SessionID: sessionID, RequestID: event.UIRequest.RequestID, Question: event.UIRequest.Prompt, Context: event.UIRequest.Context, BlockingReason: "needs decision", Attempted: "inspected files", DefaultIfNoReply: "use fallback"})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()
	waitForAppCondition(t, func() bool { return svc.activeWaitForSession(sessionID) != nil })
	active := svc.activeWaitForSession(sessionID)
	if active.Question != "Proceed?" || active.BlockingReason != "needs decision" || active.Attempted != "inspected files" || active.DefaultIfNoReply != "use fallback" {
		t.Fatalf("active wait = %+v", active)
	}
	if _, err := svc.ClaimWait(context.Background(), WaitLifecycleRequest{SessionID: sessionID, WaitID: active.WaitID}); err != nil {
		t.Fatalf("ClaimWait() error = %v", err)
	}
	if _, err := svc.AnswerWait(context.Background(), WaitLifecycleRequest{SessionID: sessionID, WaitID: active.WaitID, Answer: "Yes"}); err != nil {
		t.Fatalf("AnswerWait() error = %v", err)
	}
	select {
	case err := <-errCh:
		t.Fatalf("AskUserWait() error = %v", err)
	case result := <-resultCh:
		payload, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("Marshal(result) error = %v", err)
		}
		if err := (sessionRuntime{protocol: runtimeProtocolTTY, handle: handle}).RespondUI(context.Background(), event.UIRequest.RequestID, string(payload)); err != nil {
			t.Fatalf("RespondUI() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AskUserWait() did not return")
	}
	waitForAppCondition(t, func() bool { return len(pty.Writes()) > 0 })
	writes := pty.Writes()
	if len(writes) != 1 || !strings.Contains(writes[0], `"state":"answered"`) || !strings.Contains(writes[0], `"answer":"Yes"`) || !strings.Contains(writes[0], active.WaitID) {
		t.Fatalf("runtime ui response writes = %#v", writes)
	}
	snapshot := sink.snapshot()
	if len(snapshot.uiRequests) != 0 {
		t.Fatalf("runtime ui request events = %#v, want none", snapshot.uiRequests)
	}
	if len(snapshot.waitLifecycle) < 3 {
		t.Fatalf("wait lifecycle events = %#v", snapshot.waitLifecycle)
	}
}
func TestCreateSessionConsumesPIRuntimeOutputIntoStateAndTranscript(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	_, _ = stdoutW.Write([]byte("{" +
		"\"type\":\"extension_ui_request\",\"id\":\"ui-req-1\",\"method\":\"select\",\"question\":\"Where should this go?\",\"options\":[\"Details\",\"Sidebar\"]}" + "\n" +
		"{\"type\":\"message.delta\",\"turn_id\":\"turn-001\",\"role\":\"assistant\",\"delta\":\"Codoxear serves a browser UI for Codex-style sessions.\"}" + "\n" +
		"{\"type\":\"message_end\",\"message\":{\"role\":\"toolResult\",\"toolCallId\":\"ui-req-1\",\"toolName\":\"ask_user\",\"details\":{\"answer\":\"Sidebar\",\"cancelled\":false}}}" + "\n" +
		"{\"type\":\"turn.completed\",\"turn_id\":\"turn-001\",\"role\":\"assistant\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}" + "\n"))
	_ = stdoutW.Close()

	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return len(messages.Items) == 1 && messages.Items[0].Text == "Codoxear serves a browser UI for Codex-style sessions."
	})

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 {
		t.Fatalf("len(SessionMessages().Items) = %d, want 1", len(messages.Items))
	}
	if messages.Items[0].Role != "assistant" || messages.Items[0].Text != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("SessionMessages().Items[0] = %+v", messages.Items[0])
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy = true, want false")
	}
	if state.TailSeq != 1 {
		t.Fatalf("SessionState().TailSeq = %d, want 1", state.TailSeq)
	}
	if state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState().PartialAssistantTurn = %+v, want nil", state.PartialAssistantTurn)
	}
	if state.UIRequest != nil {
		t.Fatalf("SessionState().UIRequest = %+v, want nil", state.UIRequest)
	}

	snapshot := sink.snapshot()
	if len(snapshot.deltas) != 1 || snapshot.deltas[0].Delta != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("runtime delta events = %#v", snapshot.deltas)
	}
	if len(snapshot.commits) != 1 || snapshot.commits[0].Message.Text != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("runtime commit events = %#v", snapshot.commits)
	}
	if len(snapshot.notifications) != 1 || snapshot.notifications[0].Body != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("runtime notifications = %#v", snapshot.notifications)
	}
	if len(snapshot.uiRequests) != 1 || snapshot.uiRequests[0].Request.RequestID != "ui-req-1" {
		t.Fatalf("runtime ui request events = %#v", snapshot.uiRequests)
	}
	if len(snapshot.uiResolved) != 1 || snapshot.uiResolved[0].RequestID != "ui-req-1" {
		t.Fatalf("runtime ui resolved events = %#v", snapshot.uiResolved)
	}
	if len(snapshot.states) == 0 {
		t.Fatal("runtime session state events = 0, want at least one")
	}
}

func TestCreateSessionConsumesPIRPCRuntimeOutputIntoStateTranscriptAndEvents(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"req_prompt_1\",\"type\":\"response\",\"command\":\"prompt\",\"success\":true}" + "\n" +
		"{\"type\":\"turn_start\"}" + "\n" +
		"{\"type\":\"message_update\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves \"}],\"timestamp\":1774708716099},\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"Codoxear serves \",\"partial\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves \"}],\"timestamp\":1774708716099}}}" + "\n" +
		"{\"type\":\"message_update\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}],\"timestamp\":1774708716099},\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"a browser UI for Codex-style sessions.\",\"partial\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}],\"timestamp\":1774708716099}}}" + "\n" +
		"{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}],\"provider\":\"openai\",\"model\":\"gpt-5.5\",\"usage\":{\"input\":1200,\"output\":34,\"totalTokens\":1234},\"stopReason\":\"stop\",\"timestamp\":1774708716099}}" + "\n" +
		"{\"type\":\"turn_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}],\"provider\":\"openai\",\"model\":\"gpt-5.5\",\"usage\":{\"input\":1200,\"output\":34,\"totalTokens\":1234},\"stopReason\":\"stop\",\"timestamp\":1774708716099},\"toolResults\":[]}" + "\n"))
	_ = stdoutW.Close()

	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return len(messages.Items) == 1 && messages.Items[0].Text == "Codoxear serves a browser UI for Codex-style sessions."
	})

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 {
		t.Fatalf("len(SessionMessages().Items) = %d, want 1", len(messages.Items))
	}
	if messages.Items[0].Role != "assistant" || messages.Items[0].Text != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("SessionMessages().Items[0] = %+v", messages.Items[0])
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy = true, want false")
	}
	if state.TailSeq != 1 {
		t.Fatalf("SessionState().TailSeq = %d, want 1", state.TailSeq)
	}
	if state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState().PartialAssistantTurn = %+v, want nil", state.PartialAssistantTurn)
	}
	if state.ContextUsage == nil || state.ContextUsage.UsedTokens == nil || *state.ContextUsage.UsedTokens != 1234 {
		t.Fatalf("SessionState().ContextUsage = %+v, want used_tokens=1234", state.ContextUsage)
	}
	details, err := svc.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	if details.Model != "gpt-5.5" || details.Provider != "openai" {
		t.Fatalf("SessionDetails() model/provider = %q/%q, want gpt-5.5/openai", details.Model, details.Provider)
	}

	snapshot := sink.snapshot()
	if len(snapshot.deltas) != 2 || snapshot.deltas[0].Delta != "Codoxear serves " || snapshot.deltas[1].Delta != "a browser UI for Codex-style sessions." {
		t.Fatalf("runtime delta events = %#v", snapshot.deltas)
	}
	if len(snapshot.commits) != 1 || snapshot.commits[0].Message.Text != "Codoxear serves a browser UI for Codex-style sessions." {
		t.Fatalf("runtime commit events = %#v", snapshot.commits)
	}
	if len(snapshot.uiRequests) != 0 || len(snapshot.uiResolved) != 0 {
		t.Fatalf("runtime ui events = requests:%#v resolved:%#v, want none", snapshot.uiRequests, snapshot.uiResolved)
	}
}

func TestFinalAssistantCommitSuppressesDuplicateTurnCompleted(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	decoder := runtimeEventDecoder{backend: session.BackendPI}
	for _, raw := range []string{
		`{"type":"message.delta","turn_id":"turn-dup","role":"assistant","delta":"final"}`,
		`{"type":"message_end","turn_id":"turn-dup","message":{"role":"assistant","content":[{"type":"text","text":"final"}],"stopReason":"stop","timestamp":1774708716099}}`,
		`{"type":"turn.completed","turn_id":"turn-dup","role":"assistant","text":"final"}`,
	} {
		if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(raw))); err != nil {
			t.Fatalf("applyRuntimeProjection(%s) error = %v", raw, err)
		}
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Role != "assistant" || messages.Items[0].Text != "final" {
		t.Fatalf("SessionMessages().Items = %+v, want one final assistant", messages.Items)
	}
	commits := sink.snapshot().commits
	assistantCommits := 0
	for _, commit := range commits {
		if commit.Message.Role == "assistant" && commit.Message.Text == "final" {
			assistantCommits++
		}
	}
	if assistantCommits != 1 {
		t.Fatalf("assistant commit events = %d, want 1: %+v", assistantCommits, commits)
	}
}

func TestPIRPCGetStateControlsBusyWithoutAgentEvents(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	decoder := runtimeEventDecoder{backend: session.BackendPI}
	apply := func(raw string) {
		t.Helper()
		if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(raw))); err != nil {
			t.Fatalf("applyRuntimeProjection(%s) error = %v", raw, err)
		}
	}
	assertBusy := func(want bool) {
		t.Helper()
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			t.Fatalf("SessionState() error = %v", err)
		}
		if state.Busy != want {
			t.Fatalf("SessionState().Busy = %v, want %v", state.Busy, want)
		}
	}

	apply(`{"type":"agent_start"}`)
	assertBusy(true)
	apply(`{"id":"actrail-state-1","type":"response","command":"get_state","success":true,"data":{"isStreaming":true,"isCompacting":false,"pendingMessageCount":0}}`)
	assertBusy(true)
	apply(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"final"}],"stopReason":"stop","timestamp":1774708716099}}`)
	assertBusy(false)
	apply(`{"id":"actrail-state-2","type":"response","command":"get_state","success":true,"data":{"isStreaming":false,"isCompacting":false,"pendingMessageCount":0}}`)
	assertBusy(false)

	snapshot := sink.snapshot()
	if len(snapshot.states) == 0 {
		t.Fatal("runtime session state events = 0, want get_state-derived states")
	}
	if snapshot.states[len(snapshot.states)-1].Busy {
		t.Fatalf("last state Busy = true, want false: %#v", snapshot.states)
	}
}

func TestPIRPCActivityEventMarksIdleSessionBusy(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	generationID, err := iod.NewGenerationID("g_rpc_state_activity_busy")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime.helper = &runtimeIODHelper{generationID: generationID}
		record.runtime.protocol = runtimeProtocolPIRPC
		record.transport = SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached}
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	decoder := runtimeEventDecoder{backend: session.BackendPI}
	if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"working"},"message":{"role":"assistant","timestamp":1774708716099}}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(message_update) error = %v", err)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if !state.Busy {
		t.Fatal("SessionState().Busy = false, want true after assistant delta")
	}
}

func TestPIRPCCompactionEventsCommitPiEventMessages(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	decoder := runtimeEventDecoder{backend: session.BackendPI}
	lines := []string{
		`{"type":"compaction_start","reason":"overflow","inputTokens":258842,"inputTokensK":258.8,"model":{"provider":"deepseek","id":"deepseek-v4-pro","contextWindow":1048576}}`,
		`{"type":"compaction_end","reason":"overflow","result":{"summary":"checkpoint","firstKeptEntryId":"3406d21e","tokensBefore":258842},"aborted":false,"willRetry":true,"tokensAfter":41200,"tokensAfterK":41.2,"durationMs":1834,"model":{"provider":"deepseek","id":"deepseek-v4-pro","contextWindow":1048576}}`,
	}
	for _, line := range lines {
		if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(line))); err != nil {
			t.Fatalf("applyRuntimeProjection(%s) error = %v", line, err)
		}
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 2 {
		t.Fatalf("SessionMessages().Items = %#v, want 2 compaction events", messages.Items)
	}
	if messages.Items[0].Type != "pi_event" || !strings.Contains(messages.Items[0].Text, "Compaction started") {
		t.Fatalf("start message = %#v", messages.Items[0])
	}
	if messages.Items[1].Type != "pi_event" || !strings.Contains(messages.Items[1].Text, "Compaction ended") || !strings.Contains(messages.Items[1].Text, "retrying") {
		t.Fatalf("end message = %#v", messages.Items[1])
	}

	snapshot := sink.snapshot()
	if len(snapshot.commits) != 2 {
		t.Fatalf("runtime commits = %#v, want 2 compaction commits", snapshot.commits)
	}
	if snapshot.commits[0].Message.Summary != "Compaction started" {
		t.Fatalf("start commit = %#v", snapshot.commits[0].Message)
	}
	if snapshot.commits[1].Message.Summary != "Compaction ended, retrying" {
		t.Fatalf("end commit = %#v", snapshot.commits[1].Message)
	}
	compaction, ok := snapshot.commits[1].Message.Details["compaction"].(map[string]any)
	if !ok || compaction["phase"] != "end" || compaction["willRetry"] != true {
		t.Fatalf("end compaction details = %#v", snapshot.commits[1].Message.Details["compaction"])
	}
}

func TestPIRPCTurnCompletedClearsBusyAfterPromptLatch(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}

	decoder := runtimeEventDecoder{backend: session.BackendPI}
	if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(`{"type":"turn_end"}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(turn_end) error = %v", err)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatalf("SessionState().Busy = true, want false after turn_end")
	}
	if svc.isRuntimeAgentRunning(sessionID) {
		t.Fatal("isRuntimeAgentRunning() = true, want false after turn_end")
	}
}

func TestCodexTurnCompletedClearsBusyAndRuntimeRunning(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)

	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-final\",\"status\":{\"type\":\"idle\"}}}}" + "\n" +
		"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-final\",\"turn\":{\"id\":\"turn-codex-final\",\"status\":\"inProgress\",\"error\":null}}}" + "\n" +
		"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-final\",\"turnId\":\"turn-codex-final\",\"itemId\":\"item-codex-final\",\"delta\":\"final answer\"}}" + "\n" +
		"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-final\",\"turnId\":\"turn-codex-final\",\"item\":{\"type\":\"agentMessage\",\"id\":\"item-codex-final\",\"text\":\"final answer\"}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.Busy && state.BusyReason == "codex_running" && state.PartialAssistantTurn == nil && state.RuntimeState == string(codexRuntimePhaseRunning)
	})

	_, _ = stdoutW.Write([]byte("{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-final\",\"turn\":{\"id\":\"turn-codex-final\",\"status\":\"completed\",\"error\":null}}}" + "\n"))
	_ = stdoutW.Close()
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy && state.RuntimeState == string(codexRuntimePhaseIdle) && !svc.isRuntimeAgentRunning(sessionID)
	})

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || state.RuntimeState != string(codexRuntimePhaseIdle) {
		t.Fatalf("SessionState() = %+v, want idle after turn/completed", state)
	}
	if svc.isRuntimeAgentRunning(sessionID) {
		t.Fatal("runtimeAgentRunning = true, want false after Codex turn/completed")
	}
}

func TestPIRPCBusyHoldIgnoresEarlyIdleGetState(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	generationID, err := iod.NewGenerationID("g_rpc_state_hold")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime.helper = &runtimeIODHelper{generationID: generationID}
		record.runtime.protocol = runtimeProtocolPIRPC
		record.transport = SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached}
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}
	svc.holdPIRPCBusy(sessionID, generationID)

	decoder := runtimeEventDecoder{backend: session.BackendPI}
	if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(`{"id":"early-idle","type":"response","command":"get_state","success":true,"data":{"isStreaming":false,"isCompacting":false,"pendingMessageCount":0}}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(get_state idle) error = %v", err)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if !state.Busy {
		t.Fatalf("SessionState().Busy = false, want true during busy hold")
	}
}

func TestPIRPCActiveTurnIgnoresIdleGetState(t *testing.T) {
	svc, sessionID, generationID := newPIRPCStateFailureFixture(t)
	svc.holdPIRPCIdle(sessionID, generationID)
	svc.holdPIRPCBusy(sessionID, generationID)
	decoder := runtimeEventDecoder{backend: session.BackendPI}
	if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(`{"id":"idle-during-active","type":"response","command":"get_state","success":true,"data":{"isStreaming":false,"isCompacting":false,"pendingMessageCount":0}}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(get_state idle) error = %v", err)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if !state.Busy {
		t.Fatalf("SessionState().Busy = false, want true during active turn")
	}
	if got := svc.nextPIRPCStatePollInterval(sessionID, generationID); got != piRPCStateFastPollInterval {
		t.Fatalf("nextPIRPCStatePollInterval() = %s, want %s during active turn", got, piRPCStateFastPollInterval)
	}
}

func TestPIRPCGetStateTimeoutsDoNotEmitUserVisibleWarnings(t *testing.T) {
	svc, sessionID, _ := newPIRPCStateFailureFixture(t)
	decoder := runtimeEventDecoder{backend: session.BackendPI}
	apply := func(raw string) {
		t.Helper()
		if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(raw))); err != nil {
			t.Fatalf("applyRuntimeProjection(%s) error = %v", raw, err)
		}
	}
	apply(`{"id":"fail-1","type":"response","command":"get_state","success":false,"error":"get_state timeout"}`)
	apply(`{"id":"fail-2","type":"response","command":"get_state","success":false,"error":"get_state timeout"}`)
	apply(`{"id":"fail-3","type":"response","command":"get_state","success":false,"error":"get_state timeout"}`)
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 0 {
		t.Fatalf("SessionMessages() = %+v, want no timeout warning", messages.Items)
	}
}

func TestPIRPCSettlingPollsFastUntilGetStateReportsIdle(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	generationID, err := iod.NewGenerationID("g_rpc_state_settling")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime.helper = &runtimeIODHelper{generationID: generationID}
		record.runtime.protocol = runtimeProtocolPIRPC
		record.transport = SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached}
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}
	svc.holdPIRPCIdle(sessionID, generationID)
	if got := svc.nextPIRPCStatePollInterval(sessionID, generationID); got != piRPCStateFastPollInterval {
		t.Fatalf("nextPIRPCStatePollInterval() = %s, want %s during settling", got, piRPCStateFastPollInterval)
	}

	decoder := runtimeEventDecoder{backend: session.BackendPI}
	if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(`{"id":"settled-idle","type":"response","command":"get_state","success":true,"data":{"isStreaming":false,"isCompacting":false,"pendingMessageCount":0}}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(get_state idle) error = %v", err)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatalf("SessionState().Busy = true, want false after idle get_state")
	}
	if got := svc.nextPIRPCStatePollInterval(sessionID, generationID); got != piRPCStateIdlePollInterval {
		t.Fatalf("nextPIRPCStatePollInterval() = %s, want %s after idle get_state", got, piRPCStateIdlePollInterval)
	}
}

func TestIODTransportResetRequiredEmitsDiagnosticMessage(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	generationID, err := iod.NewGenerationID("g_rpc_transport_diag")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	if err := svc.markSessionTransportResetRequired(sessionID, generationID, iod.GenerationBreakAttachLost.String()); err != nil {
		t.Fatalf("markSessionTransportResetRequired() error = %v", err)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Type != "pi_event" || messages.Items[0].Text != "IOD transport reset required: attach_lost" {
		t.Fatalf("SessionMessages() = %+v, want iod diagnostic event", messages.Items)
	}
	snapshot := sink.snapshot()
	if len(snapshot.commits) != 1 || snapshot.commits[0].Message.Details["raw_type"] != "iod_transport_diagnostic" {
		t.Fatalf("runtime commits = %+v, want iod transport diagnostic", snapshot.commits)
	}
}

func TestPIRPCGetStateFailuresEmitWarningWithoutStallingTransport(t *testing.T) {
	svc, sessionID, generationID := newPIRPCStateFailureFixture(t)
	decoder := runtimeEventDecoder{backend: session.BackendPI}
	apply := func(raw string) {
		t.Helper()
		if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(raw))); err != nil {
			t.Fatalf("applyRuntimeProjection(%s) error = %v", raw, err)
		}
	}

	apply(`{"id":"fail-1","type":"response","command":"get_state","success":false,"error":"rpc unavailable"}`)
	apply(`{"id":"fail-2","type":"response","command":"get_state","success":false,"error":"rpc unavailable"}`)
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() before threshold error = %v", err)
	}
	if !state.Busy || state.Transport.ResetRequired || state.Transport.State != SessionTransportStateAttached {
		t.Fatalf("SessionState() before threshold = %+v, want busy attached without reset", state)
	}

	apply(`{"id":"fail-3","type":"response","command":"get_state","success":false,"error":"rpc unavailable"}`)
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after threshold error = %v", err)
	}
	if state.Transport.ResetRequired || state.Transport.State != SessionTransportStateAttached {
		t.Fatalf("SessionState() after threshold = %+v, want attached without reset", state)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Type != "pi_event" || messages.Items[0].Text != "Pi RPC state probe failed: rpc unavailable" {
		t.Fatalf("SessionMessages() = %+v, want state probe warning event", messages.Items)
	}
	if got := svc.recordPIRPCStateTransportFailure(sessionID, generationID, "rpc unavailable"); got {
		t.Fatal("recordPIRPCStateTransportFailure(rpc unavailable) = true, want false")
	}
}

func TestPIRPCControlSocketFailureRequiresTransportResetAndStopsPolling(t *testing.T) {
	svc, sessionID, generationID := newPIRPCStateFailureFixture(t)
	reason := `dial iod control socket "/root/code/ActRail/data/runtime/iod/s_27/g_1777645018083526842/io": dial unix /root/code/ActRail/data/runtime/iod/s_27/g_1777645018083526842/io: connect: no such file or directory`
	if got := svc.recordPIRPCStateTransportFailure(sessionID, generationID, reason); !got {
		t.Fatal("recordPIRPCStateTransportFailure() = false, want true")
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateBroken || !state.Transport.ResetRequired || state.Transport.Reason != reason {
		t.Fatalf("SessionState() = %+v, want broken reset_required control socket reason", state)
	}
	if svc.shouldPollPIRPCState(sessionID, sessionRuntime{protocol: runtimeProtocolPIRPC, helper: &runtimeIODHelper{generationID: generationID}}, generationID) {
		t.Fatal("shouldPollPIRPCState() = true, want false after control socket failure")
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "IOD transport reset required: "+reason {
		t.Fatalf("SessionMessages() = %+v, want one transport diagnostic", messages.Items)
	}
}

func newPIRPCStateFailureFixture(t *testing.T) (*Stub, session.SessionID, iod.GenerationID) {
	t.Helper()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	generationID, err := iod.NewGenerationID("g_rpc_state_fail")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime.helper = &runtimeIODHelper{generationID: generationID}
		record.runtime.protocol = runtimeProtocolPIRPC
		record.transport = SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached}
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}
	return svc, sessionID, generationID
}

func TestNextPIRPCStatePollIntervalUsesCadencePhases(t *testing.T) {
	sessionID, err := session.ParseSessionID("s_rpc_state_pending")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	generationID, err := iod.NewGenerationID("g_rpc_state_pending")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	svc := &Stub{piRPCStates: map[session.SessionID]piRPCStateCache{
		sessionID: {
			GenerationID:   generationID,
			PendingProbeID: "pending-probe",
			LastState:      &piRPCStateSnapshot{ProbeID: "old-state"},
		},
	}}

	if got := svc.nextPIRPCStatePollInterval(sessionID, generationID); got != piRPCStateFastPollInterval {
		t.Fatalf("nextPIRPCStatePollInterval() = %s, want %s", got, piRPCStateFastPollInterval)
	}
	svc.piRPCStates[sessionID] = piRPCStateCache{GenerationID: generationID, LastState: &piRPCStateSnapshot{ProbeID: "busy", IsStreaming: true}}
	if got := svc.nextPIRPCStatePollInterval(sessionID, generationID); got != piRPCStateBusyPollInterval {
		t.Fatalf("nextPIRPCStatePollInterval() after busy get_state = %s, want %s", got, piRPCStateBusyPollInterval)
	}
	svc.holdPIRPCIdle(sessionID, generationID)
	if got := svc.nextPIRPCStatePollInterval(sessionID, generationID); got != piRPCStateFastPollInterval {
		t.Fatalf("nextPIRPCStatePollInterval() during settling = %s, want %s", got, piRPCStateFastPollInterval)
	}
	svc.holdPIRPCBusy(sessionID, generationID)
	if got := svc.nextPIRPCStatePollInterval(sessionID, generationID); got != piRPCStateFastPollInterval {
		t.Fatalf("nextPIRPCStatePollInterval() during active turn = %s, want %s", got, piRPCStateFastPollInterval)
	}
}

func TestPIRPCStatePollerKickRequestsImmediateProbeForActiveGeneration(t *testing.T) {
	sessionID, err := session.ParseSessionID("s_rpc_state_kick")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	generationID, err := iod.NewGenerationID("g_rpc_state_kick")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	svc := &Stub{piRPCStates: map[session.SessionID]piRPCStateCache{
		sessionID: {GenerationID: generationID, Polling: true},
	}}

	if svc.activatePIRPCStatePoller(sessionID, generationID) {
		t.Fatal("activatePIRPCStatePoller() = true, want false for active generation")
	}
	cache := svc.piRPCStates[sessionID]
	if cache.KickSeq != 1 || !cache.Polling {
		t.Fatalf("cache after active generation kick = %+v, want kick_seq 1 polling true", cache)
	}
	if !svc.piRPCStatePollKicked(sessionID, generationID, 0) {
		t.Fatal("piRPCStatePollKicked(seq 0) = false, want true")
	}
	if svc.piRPCStatePollKicked(sessionID, generationID, 1) {
		t.Fatal("piRPCStatePollKicked(seq 1) = true, want false")
	}
}

func TestPIRPCStatePollerResetsFailureBudgetForNewGeneration(t *testing.T) {
	sessionID, err := session.ParseSessionID("s_rpc_state_reset")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	oldGenerationID, err := iod.NewGenerationID("g_rpc_state_old")
	if err != nil {
		t.Fatalf("NewGenerationID(old) error = %v", err)
	}
	newGenerationID, err := iod.NewGenerationID("g_rpc_state_new")
	if err != nil {
		t.Fatalf("NewGenerationID(new) error = %v", err)
	}
	svc := &Stub{piRPCStates: map[session.SessionID]piRPCStateCache{
		sessionID: {
			GenerationID:        oldGenerationID,
			ConsecutiveFailures: piRPCStateMaxFailures - 1,
			LastAckProbeID:      "old-ack",
			PendingProbeID:      "old-pending",
			LastSuccessTS:       time.Unix(10, 0),
			LastFailureTS:       time.Unix(20, 0),
			LastState:           &piRPCStateSnapshot{ProbeID: "old-state", IsStreaming: true},
		},
	}}

	if !svc.activatePIRPCStatePoller(sessionID, newGenerationID) {
		t.Fatal("activatePIRPCStatePoller() = false, want true for new generation")
	}
	cache := svc.piRPCStates[sessionID]
	if cache.GenerationID != newGenerationID || !cache.Polling {
		t.Fatalf("cache generation/polling = (%q, %v), want (%q, true)", cache.GenerationID, cache.Polling, newGenerationID)
	}
	if cache.ConsecutiveFailures != 0 || cache.LastAckProbeID != "" || cache.PendingProbeID != "" || !cache.LastSuccessTS.IsZero() || !cache.LastFailureTS.IsZero() || cache.LastState != nil || cache.StalledResetRequired {
		t.Fatalf("cache after generation switch = %+v, want reset failure state", cache)
	}
}

func TestCreateSessionConsumesCodexRuntimeOutputIntoStateTranscriptAndEvents(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-1\"}}}" + "\n" +
		"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-1\",\"turn\":{\"id\":\"turn-codex-1\",\"status\":\"inProgress\",\"error\":null}}}" + "\n" +
		"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-1\",\"turnId\":\"turn-codex-1\",\"itemId\":\"item-codex-1\",\"delta\":\"Codex runtime \"}}" + "\n" +
		"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-1\",\"turnId\":\"turn-codex-1\",\"itemId\":\"item-codex-1\",\"delta\":\"reached the session transcript.\"}}" + "\n" +
		"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-1\",\"turnId\":\"turn-codex-1\",\"item\":{\"type\":\"agentMessage\",\"id\":\"item-codex-1\",\"text\":\"Codex runtime reached the session transcript.\"}}}" + "\n" +
		"{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-1\",\"turn\":{\"id\":\"turn-codex-1\",\"status\":\"completed\",\"error\":null}}}" + "\n"))
	_ = stdoutW.Close()

	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		if len(messages.Items) != 1 || messages.Items[0].Text != "Codex runtime reached the session transcript." {
			return false
		}
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return !state.Busy && state.TailSeq == 1 && state.PartialAssistantTurn == nil
	})

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 {
		t.Fatalf("len(SessionMessages().Items) = %d, want 1", len(messages.Items))
	}
	if messages.Items[0].Role != "assistant" || messages.Items[0].Text != "Codex runtime reached the session transcript." {
		t.Fatalf("SessionMessages().Items[0] = %+v", messages.Items[0])
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy = true, want false")
	}
	if state.TailSeq != 1 {
		t.Fatalf("SessionState().TailSeq = %d, want 1", state.TailSeq)
	}
	if state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState().PartialAssistantTurn = %+v, want nil", state.PartialAssistantTurn)
	}
	record, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatalf("Lookup(%q) ok = false", sessionID)
	}
	_, threadID, turnID := record.runtime.codex.snapshot()
	if threadID != "thread-codex-1" || turnID != "" {
		t.Fatalf("codex runtime state = (thread=%q turn=%q), want (thread-codex-1, empty)", threadID, turnID)
	}

	snapshot := sink.snapshot()
	if len(snapshot.deltas) != 2 || snapshot.deltas[0].Delta != "Codex runtime " || snapshot.deltas[1].Delta != "reached the session transcript." {
		t.Fatalf("runtime delta events = %#v", snapshot.deltas)
	}
	if len(snapshot.commits) != 1 || snapshot.commits[0].Message.Text != "Codex runtime reached the session transcript." {
		t.Fatalf("runtime commit events = %#v", snapshot.commits)
	}
	if len(snapshot.uiRequests) != 0 || len(snapshot.uiResolved) != 0 {
		t.Fatalf("runtime ui events = requests:%#v resolved:%#v, want none", snapshot.uiRequests, snapshot.uiResolved)
	}
}

func TestCodexUserMessageEchoDoesNotDuplicateActRailPrompt(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	if _, err := svc.AppendSessionMessage(sessionID, "user", "message", "continue"); err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}
	_, _ = stdoutW.Write([]byte(
		"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-echo\",\"turnId\":\"turn-codex-echo\",\"item\":{\"type\":\"userMessage\",\"id\":\"user-echo-1\",\"text\":\"continue\"}}}" + "\n" +
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-echo\",\"turnId\":\"turn-codex-echo\",\"item\":{\"type\":\"agentMessage\",\"id\":\"assistant-echo-1\",\"text\":\"done\"}}}" + "\n"))
	_ = stdoutW.Close()

	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		return err == nil && len(messages.Items) == 2
	})
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 2 {
		t.Fatalf("SessionMessages().Items = %+v, want prompt plus assistant only", messages.Items)
	}
	if messages.Items[0].Role != "user" || messages.Items[0].Text != "continue" || messages.Items[1].Role != "assistant" || messages.Items[1].Text != "done" {
		t.Fatalf("SessionMessages().Items = %+v, want deduped user echo followed by assistant", messages.Items)
	}
}

func TestCodexSubagentUserMessageDoesNotRenderAsMainPrompt(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	if _, err := svc.AppendSessionMessage(sessionID, "user", "message", "review pr with subagent"); err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}
	_, _ = stdoutW.Write([]byte(
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"main-thread\",\"status\":{\"type\":\"idle\"}}}}" + "\n" +
			"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"main-thread\",\"turn\":{\"id\":\"main-turn\",\"status\":\"inProgress\",\"error\":null}}}" + "\n" +
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"main-thread\",\"turnId\":\"main-turn\",\"item\":{\"type\":\"collabAgentToolCall\",\"id\":\"spawn-1\",\"tool\":\"spawnAgent\",\"status\":\"completed\",\"prompt\":\"subagent prompt\"}}}" + "\n" +
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"sub-thread\",\"turnId\":\"sub-turn\",\"item\":{\"type\":\"userMessage\",\"id\":\"sub-user-1\",\"text\":\"subagent prompt\"}}}" + "\n" +
			"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"sub-thread\",\"turnId\":\"sub-turn\",\"itemId\":\"sub-assistant-1\",\"delta\":\"subagent \"}}" + "\n" +
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"sub-thread\",\"turnId\":\"sub-turn\",\"item\":{\"type\":\"agentMessage\",\"id\":\"sub-assistant-1\",\"text\":\"subagent result\"}}}" + "\n" +
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"main-thread\",\"turnId\":\"main-turn\",\"item\":{\"type\":\"agentMessage\",\"id\":\"main-assistant-1\",\"text\":\"review done\"}}}" + "\n"))
	_ = stdoutW.Close()

	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, IncludeToolEvents: true})
		return err == nil && len(messages.Items) == 5
	})
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, IncludeToolEvents: true})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 5 {
		t.Fatalf("SessionMessages().Items = %+v, want prompt, spawn tool, subagent messages, assistant", messages.Items)
	}
	if messages.Items[0].Role != "user" || messages.Items[0].Text != "review pr with subagent" {
		t.Fatalf("SessionMessages().Items[0] = %+v, want original user prompt", messages.Items[0])
	}
	if messages.Items[1].Kind != "tool" && messages.Items[1].Kind != "tool_result" || !strings.Contains(messages.Items[1].Text, "subagent prompt") {
		t.Fatalf("SessionMessages().Items[1] = %+v, want retained subagent tool event", messages.Items[1])
	}
	if messages.Items[2].Type != "custom_message" || messages.Items[2].Details["custom_type"] != "codex-subagent-message" || messages.Items[2].Details["role"] != "user" || messages.Items[2].Text != "subagent prompt" {
		t.Fatalf("SessionMessages().Items[2] = %+v, want rendered subagent prompt", messages.Items[2])
	}
	if messages.Items[3].Type != "custom_message" || messages.Items[3].Details["custom_type"] != "codex-subagent-message" || messages.Items[3].Details["role"] != "assistant" || messages.Items[3].Text != "subagent result" {
		t.Fatalf("SessionMessages().Items[3] = %+v, want rendered subagent result", messages.Items[3])
	}
	if messages.Items[4].Role != "assistant" || messages.Items[4].Text != "review done" {
		t.Fatalf("SessionMessages().Items[4] = %+v, want final assistant", messages.Items[4])
	}
	for _, item := range messages.Items {
		if item.Role == "user" && item.Text == "subagent prompt" {
			t.Fatalf("SessionMessages().Items = %+v, subagent prompt rendered as main user message", messages.Items)
		}
	}
}

func TestCodexSessionWaitsForThreadBeforeInput(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	defer stdoutW.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if created.Session.TransportState != SessionTransportStateStarting.String() || !created.Session.PendingStartup {
		t.Fatalf("CreateSession().Session transport = (%q, pending=%v), want starting pending", created.Session.TransportState, created.Session.PendingStartup)
	}
	if created.Session.RuntimeState != string(codexRuntimePhaseThreadStarting) || !created.Session.Busy {
		t.Fatalf("CreateSession().Session runtime = (%q, busy=%v), want thread_starting busy", created.Session.RuntimeState, created.Session.Busy)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	startingState, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() before thread error = %v", err)
	}
	if startingState.RuntimeState != string(codexRuntimePhaseThreadStarting) || !startingState.Busy {
		t.Fatalf("SessionState() before thread runtime = (%q, busy=%v), want thread_starting busy", startingState.RuntimeState, startingState.Busy)
	}

	if _, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "hello"}); err == nil || !strings.Contains(err.Error(), "session runtime is starting") {
		t.Fatalf("Send() before thread error = %v, want session runtime is starting", err)
	}

	_, _ = stdoutW.Write([]byte("{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-ready\"}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		record, ok := svc.registry.Lookup(sessionID)
		return ok && sessionTransportSnapshot(record).State == SessionTransportStateAttached
	})

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached {
		t.Fatalf("SessionState().Transport.State = %q, want attached", state.Transport.State)
	}
	if len(sink.snapshot().states) == 0 {
		t.Fatal("session state events = 0, want attached transition event")
	}
}

func TestCreateSessionAppliesCodexThreadStatusChangedToBusyState(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-status-1\",\"status\":{\"type\":\"idle\"}}}}" + "\n" +
		"{\"method\":\"thread/status/changed\",\"params\":{\"threadId\":\"thread-codex-status-1\",\"status\":{\"type\":\"active\",\"activeFlags\":[]}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.Busy && state.RuntimeState == string(codexRuntimePhaseRunning)
	})

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() active error = %v", err)
	}
	if !state.Busy {
		t.Fatal("SessionState().Busy = false, want true after Codex active status")
	}
	if !svc.isRuntimeAgentRunning(sessionID) {
		t.Fatal("runtimeAgentRunning = false, want true after Codex active status")
	}

	_, _ = stdoutW.Write([]byte("{\"method\":\"thread/status/changed\",\"params\":{\"threadId\":\"thread-codex-status-1\",\"status\":{\"type\":\"idle\"}}}" + "\n"))
	_ = stdoutW.Close()
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy
	})

	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() idle error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy = true, want false after Codex idle status")
	}
	if svc.isRuntimeAgentRunning(sessionID) {
		t.Fatal("runtimeAgentRunning = true, want false after Codex idle status")
	}
}

func TestCodexVisibleBusyIsConsistentAcrossSummaryDetailsAndState(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)

	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-visible\",\"status\":{\"type\":\"idle\"}}}}" + "\n" +
		"{\"method\":\"thread/status/changed\",\"params\":{\"threadId\":\"thread-codex-visible\",\"status\":{\"type\":\"active\"}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.Busy && state.RuntimeState == string(codexRuntimePhaseRunning)
	})

	assertCodexVisibleState(t, svc, sessionID, true, "codex_running", string(codexRuntimePhaseRunning))

	_, _ = stdoutW.Write([]byte("{\"method\":\"thread/status/changed\",\"params\":{\"threadId\":\"thread-codex-visible\",\"status\":{\"type\":\"idle\"}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy && state.RuntimeState == string(codexRuntimePhaseIdle)
	})
	assertCodexVisibleState(t, svc, sessionID, false, "", string(codexRuntimePhaseIdle))

	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("SetBusy(true) = (_, %v, %v), want ok true err nil", ok, err)
	}
	assertCodexVisibleState(t, svc, sessionID, false, "", string(codexRuntimePhaseIdle))
	_, _ = stdoutW.Write([]byte("{\"method\":\"thread/status/changed\",\"params\":{\"threadId\":\"thread-codex-visible\",\"status\":{\"type\":\"idle\"}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		record, ok := svc.registry.Lookup(sessionID)
		return ok && !record.state.Busy()
	})
	_ = stdoutW.Close()
	assertCodexVisibleState(t, svc, sessionID, false, "", string(codexRuntimePhaseIdle))
}

func TestCodexAssistantItemCompletedWaitsForTurnCompleted(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	defer stdoutW.Close()
	pty := &recordingPTY{reader: stdoutR}
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPTY(pty)
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)

	_, _ = stdoutW.Write([]byte(
		"{\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}\n" +
			"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-final-only\",\"status\":{\"type\":\"idle\"}}}}\n" +
			"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-final-only\",\"turn\":{\"id\":\"turn-codex-final-only\",\"status\":\"inProgress\",\"error\":null}}}\n" +
			"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-final-only\",\"turnId\":\"turn-codex-final-only\",\"itemId\":\"item-codex-final-only\",\"delta\":\"stale partial\"}}\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.Busy && state.BusyReason == "partial_assistant_turn" && state.PartialAssistantTurn != nil
	})

	_, _ = stdoutW.Write([]byte("{\"method\":\"item/completed\",\"params\":{\"item\":{\"type\":\"agentMessage\",\"id\":\"item-codex-final-only\",\"threadId\":\"thread-codex-final-only\",\"turnId\":\"turn-codex-final-only\",\"text\":\"final answer without turn completed\"}}}\n"))
	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		return err == nil && len(messages.Items) == 1 && messages.Items[0].Role == "assistant" && messages.Items[0].Text == "final answer without turn completed"
	})
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after assistant item completed error = %v", err)
	}
	if !state.Busy || state.BusyReason != "codex_running" || state.PartialAssistantTurn != nil || state.RuntimeState != string(codexRuntimePhaseRunning) {
		t.Fatalf("SessionState() after assistant item completed = %+v, want running busy until turn/completed", state)
	}
	waitForAppCondition(t, func() bool {
		for _, write := range pty.Writes() {
			if strings.Contains(write, `"method":"thread/read"`) && strings.Contains(write, `"threadId":"thread-codex-final-only"`) && strings.Contains(write, `"includeTurns":true`) {
				return true
			}
		}
		return false
	})

	_, _ = stdoutW.Write([]byte("{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-final-only\",\"turn\":{\"id\":\"turn-codex-final-only\",\"status\":\"completed\",\"error\":null}}}\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil || state.Busy || state.PartialAssistantTurn != nil || state.RuntimeState != string(codexRuntimePhaseIdle) {
			return false
		}
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		return err == nil && len(messages.Items) == 1 && messages.Items[0].Role == "assistant" && messages.Items[0].Text == "final answer without turn completed"
	})

	assertCodexVisibleState(t, svc, sessionID, false, "", string(codexRuntimePhaseIdle))
}

func TestCodexAssistantCommentaryCommitKeepsRuntimeBusy(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	defer stdoutW.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)

	_, _ = stdoutW.Write([]byte(
		"{\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}\n" +
			"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-commentary\",\"status\":{\"type\":\"idle\"}}}}\n" +
			"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-commentary\",\"turn\":{\"id\":\"turn-codex-commentary\",\"status\":\"inProgress\",\"error\":null}}}\n" +
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-commentary\",\"turnId\":\"turn-codex-commentary\",\"item\":{\"type\":\"agentMessage\",\"id\":\"item-codex-commentary\",\"text\":\"I am checking this now\",\"phase\":\"commentary\"}}}\n"))

	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		return err == nil && len(messages.Items) == 1 && messages.Items[0].Role == "assistant" && messages.Items[0].Text == "I am checking this now"
	})
	snapshot := sink.snapshot()
	if len(snapshot.commits) != 1 || snapshot.commits[0].Message.Details["phase"] != "commentary" {
		t.Fatalf("live commits = %+v, want commentary phase", snapshot.commits)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if !state.Busy || state.RuntimeState != string(codexRuntimePhaseRunning) {
		t.Fatalf("SessionState() after commentary = %+v, want running busy", state)
	}
	if len(snapshot.notifications) != 0 {
		t.Fatalf("notifications after commentary = %+v, want none", snapshot.notifications)
	}
}

func TestCodexHelperChildExitMarksTransportEnded(t *testing.T) {
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_codex_child_exit")
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime.helper = &runtimeIODHelper{generationID: generationID}
		record.transport = SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached}
		record.runtime.codex = newCodexRuntimeState(session.BackendCodex)
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-codex-child-exit")
		record.runtime.codex.setActiveTurnID("turn-codex-child-exit")
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}
	if _, _, err := svc.registry.SetBusy(sessionID, true); err != nil {
		t.Fatalf("registry.SetBusy(true) error = %v", err)
	}

	fact, err := iod.NewHelperFact(iod.FactChildExit, nil, json.RawMessage(`{"status":0}`))
	if err != nil {
		t.Fatalf("NewHelperFact(child_exit) error = %v", err)
	}
	packet, err := iod.NewStatePacket(sessionID, generationID, fact)
	if err != nil {
		t.Fatalf("NewStatePacket(child_exit) error = %v", err)
	}
	if err := svc.applyRuntimeHelperPacket(sessionID, session.BackendCodex, generationID, packet); err != nil {
		t.Fatalf("applyRuntimeHelperPacket(child_exit) error = %v", err)
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || state.Transport.State != SessionTransportStateEnded || state.Transport.Reason != iod.FactChildExit.String() || state.RuntimeState != string(codexRuntimePhaseEnded) {
		t.Fatalf("SessionState() after child_exit = %+v, want ended non-busy Codex transport", state)
	}
}

func TestStaleHelperGenerationDoesNotOverwriteCurrentRuntime(t *testing.T) {
	svc := newStub(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() })
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-stale-helper"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	oldGeneration := mustHelperGenerationID(t, "g_old_helper")
	newGeneration := mustHelperGenerationID(t, "g_new_helper")
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper:   &runtimeIODHelper{generationID: newGeneration},
			codex:    newCodexRuntimeState(session.BackendCodex),
		}
		record.transport = SessionTransportSnapshot{GenerationID: newGeneration.String(), State: SessionTransportStateAttached}
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}
	seq := iod.EventSeq(1)
	fact, err := iod.NewHelperFact(iod.FactOutputDelta, &seq, json.RawMessage(`{"stream":"stdout","data":"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-stale\",\"turn\":{\"id\":\"turn-stale\"}}}\n"}`))
	if err != nil {
		t.Fatalf("NewHelperFact(output) error = %v", err)
	}
	packet, err := iod.NewStatePacket(sessionID, oldGeneration, fact)
	if err != nil {
		t.Fatalf("NewStatePacket(output) error = %v", err)
	}
	if err := svc.applyRuntimeHelperPacket(sessionID, session.BackendCodex, oldGeneration, packet); err != nil {
		t.Fatalf("applyRuntimeHelperPacket(stale) error = %v", err)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.GenerationID != newGeneration.String() || state.Busy || state.RuntimeState != string(codexRuntimePhaseIdle) {
		t.Fatalf("SessionState() after stale helper packet = %+v, want current generation idle", state)
	}
}

func TestCodexTerminalTransportOverridesStalePartialBusy(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	_, _ = stdoutW.Write([]byte(
		"{\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}\n" +
			"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-broken\",\"status\":{\"type\":\"idle\"}}}}\n" +
			"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-broken\",\"turn\":{\"id\":\"turn-codex-broken\",\"status\":\"inProgress\",\"error\":null}}}\n" +
			"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-broken\",\"turnId\":\"turn-codex-broken\",\"itemId\":\"item-codex-broken\",\"delta\":\"stale partial\"}}\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.Busy && state.BusyReason == "partial_assistant_turn"
	})

	generationID := mustHelperGenerationID(t, "g_codex_broken_busy")
	if err := svc.markSessionTransportResetRequired(sessionID, generationID, iod.GenerationBreakAttachLost.String()); err != nil {
		t.Fatalf("markSessionTransportResetRequired() error = %v", err)
	}
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy && state.BusyReason == "" && state.RuntimeState == string(codexRuntimePhaseFailed) && state.RuntimeStateReason == iod.GenerationBreakAttachLost.String()
	})

	assertCodexVisibleState(t, svc, sessionID, false, "", string(codexRuntimePhaseFailed))
	_ = stdoutW.Close()
}

func TestCodexBusyIgnoresStatusFromNonMainThread(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-main\",\"status\":{\"type\":\"idle\"}}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.RuntimeState == string(codexRuntimePhaseIdle)
	})

	_, _ = stdoutW.Write([]byte("{\"method\":\"thread/status/changed\",\"params\":{\"threadId\":\"thread-codex-subagent\",\"status\":{\"type\":\"active\"}}}" + "\n"))
	time.Sleep(50 * time.Millisecond)
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || state.RuntimeState != string(codexRuntimePhaseIdle) {
		t.Fatalf("SessionState() after subagent active = %+v, want idle not busy", state)
	}
	_ = stdoutW.Close()
}

func TestCodexBusyIgnoresTurnLifecycleFromNonMainThread(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-main\",\"status\":{\"type\":\"idle\"}}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.RuntimeState == string(codexRuntimePhaseIdle)
	})

	_, _ = stdoutW.Write([]byte(
		"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-subagent\",\"turn\":{\"id\":\"turn-codex-subagent\",\"status\":\"inProgress\",\"error\":null}}}" + "\n" +
			"{\"method\":\"item/started\",\"params\":{\"threadId\":\"thread-codex-subagent\",\"turnId\":\"turn-codex-subagent\",\"item\":{\"type\":\"commandExecution\",\"id\":\"sub-cmd-1\",\"command\":\"go test ./subagent\",\"status\":\"inProgress\"}}}" + "\n" +
			"{\"method\":\"item/reasoning/summaryTextDelta\",\"params\":{\"threadId\":\"thread-codex-subagent\",\"turnId\":\"turn-codex-subagent\",\"itemId\":\"sub-reason-1\",\"delta\":\"subagent reasoning\",\"summaryIndex\":0}}" + "\n" +
			"{\"method\":\"warning\",\"params\":{\"message\":\"subagent warning\",\"threadId\":\"thread-codex-subagent\",\"turnId\":\"turn-codex-subagent\"}}" + "\n" +
			"{\"method\":\"thread/tokenUsage/updated\",\"params\":{\"threadId\":\"thread-codex-subagent\",\"turnId\":\"turn-codex-subagent\",\"tokenUsage\":{\"total\":{\"totalTokens\":4096,\"inputTokens\":2048,\"outputTokens\":2048},\"modelContextWindow\":8192}}}" + "\n" +
			"{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-subagent\",\"turn\":{\"id\":\"turn-codex-subagent\",\"status\":\"completed\",\"error\":null}}}" + "\n"))
	time.Sleep(50 * time.Millisecond)
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || state.RuntimeState != string(codexRuntimePhaseIdle) {
		t.Fatalf("SessionState() after subagent turn lifecycle = %+v, want idle not busy", state)
	}
	if state.ContextUsage != nil {
		t.Fatalf("SessionState().ContextUsage = %+v, want nil after subagent usage", state.ContextUsage)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 0 {
		t.Fatalf("SessionMessages() = %+v, want no top-level tool/reasoning/diagnostic from subagent", messages.Items)
	}
	record, ok := svc.registry.Lookup(sessionID)
	if !ok || record.runtime.codex == nil {
		t.Fatalf("Lookup(%q) missing codex runtime", sessionID)
	}
	_, threadID, turnID := record.runtime.codex.snapshot()
	if threadID != "thread-codex-main" || turnID != "" {
		t.Fatalf("codex runtime ids = (thread=%q turn=%q), want main thread with no active turn", threadID, turnID)
	}
	_ = stdoutW.Close()
}

func TestCodexErrorIgnoresFailedTurnFromNonMainThread(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-main\",\"status\":{\"type\":\"idle\"}}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.RuntimeState == string(codexRuntimePhaseIdle)
	})

	_, _ = stdoutW.Write([]byte("{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-subagent\",\"turn\":{\"id\":\"turn-codex-subagent\",\"status\":\"failed\",\"error\":{\"message\":\"subagent failed\"}}}}" + "\n"))
	time.Sleep(50 * time.Millisecond)
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 0 {
		t.Fatalf("SessionMessages() = %+v, want no top-level error from subagent failed turn", messages.Items)
	}
	_ = stdoutW.Close()
}

func TestCodexSendDoesNotWaitForTurnStarted(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	pty := &recordingPTY{reader: stdoutR}
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPTY(pty)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)

	waitForAppCondition(t, func() bool {
		return strings.Contains(strings.Join(pty.Writes(), "\n"), `"method":"initialize"`)
	})
	_, _ = stdoutW.Write([]byte("{\"id\":\"initialize-1\",\"result\":{\"userAgent\":\"actrail-test\"}}\n"))
	waitForAppCondition(t, func() bool {
		return strings.Contains(strings.Join(pty.Writes(), "\n"), `"method":"thread/start"`)
	})
	_, _ = stdoutW.Write([]byte("{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-nonblocking\",\"status\":{\"type\":\"idle\"}}}}\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy && state.RuntimeState == string(codexRuntimePhaseIdle)
	})

	startedAt := time.Now()
	sent, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "Do not block on turn started"})
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("Send() took %s, want under 1s without turn/started", elapsed)
	}
	if !sent.Busy {
		t.Fatalf("Send() = %+v, want busy true while turn starts", sent)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if !state.Busy || state.RuntimeState != string(codexRuntimePhaseTurnStarting) {
		t.Fatalf("SessionState() after Send = %+v, want busy turn_starting", state)
	}
	writes := strings.Join(pty.Writes(), "\n")
	if !strings.Contains(writes, `"method":"turn/start"`) || !strings.Contains(writes, "Do not block on turn started") {
		t.Fatalf("runtime writes = %q, want turn/start with prompt", writes)
	}
	_ = stdoutW.Close()
}

func TestCodexInterruptDefersUntilTurnStarts(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	pty := &recordingPTY{reader: stdoutR}
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPTY(pty)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)

	waitForAppCondition(t, func() bool {
		return strings.Contains(strings.Join(pty.Writes(), "\n"), `"method":"initialize"`)
	})
	_, _ = stdoutW.Write([]byte("{\"id\":\"initialize-1\",\"result\":{\"userAgent\":\"actrail-test\"}}\n"))
	waitForAppCondition(t, func() bool {
		return strings.Contains(strings.Join(pty.Writes(), "\n"), `"method":"thread/start"`)
	})
	_, _ = stdoutW.Write([]byte("{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-interrupt\",\"status\":{\"type\":\"idle\"}}}}\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy && state.RuntimeState == string(codexRuntimePhaseIdle)
	})

	if _, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "start then interrupt"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.Busy && state.RuntimeState == string(codexRuntimePhaseTurnStarting)
	})

	if _, _, err := svc.registry.ReplaceQueue(sessionID, "queued after interrupt"); err != nil {
		t.Fatalf("registry.ReplaceQueue() error = %v", err)
	}
	interrupted, err := svc.Interrupt(context.Background(), InterruptRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	if !interrupted.Busy {
		t.Fatalf("Interrupt() = %+v, want busy true while turn interrupt is pending", interrupted)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if !state.Busy || state.RuntimeState != string(codexRuntimePhaseInterrupting) {
		t.Fatalf("SessionState() after Interrupt = %+v, want interrupting busy", state)
	}
	time.Sleep(codexRuntimeBootstrapTimeout + 50*time.Millisecond)
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after watchdog error = %v", err)
	}
	if !state.Busy || state.RuntimeState != string(codexRuntimePhaseInterrupting) {
		t.Fatalf("SessionState() after watchdog = %+v, want still interrupting busy", state)
	}
	if writes := strings.Join(pty.Writes(), "\n"); strings.Contains(writes, "queued after interrupt") {
		t.Fatalf("runtime writes = %q, queued prompt dispatched before interrupted turn settled", writes)
	}

	_, _ = stdoutW.Write([]byte(
		"{\"method\":\"thread/status/changed\",\"params\":{\"threadId\":\"thread-codex-interrupt\",\"status\":{\"type\":\"idle\"}}}" + "\n" +
			"{\"id\":\"thread-read-idle\",\"result\":{\"thread\":{\"id\":\"thread-codex-interrupt\",\"status\":{\"type\":\"idle\"},\"turns\":[]}}}" + "\n"))
	time.Sleep(50 * time.Millisecond)
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after idle projection error = %v", err)
	}
	if !state.Busy || state.RuntimeState != string(codexRuntimePhaseInterrupting) {
		t.Fatalf("SessionState() after idle projection = %+v, want still interrupting busy", state)
	}
	if writes := strings.Join(pty.Writes(), "\n"); strings.Contains(writes, "queued after interrupt") {
		t.Fatalf("runtime writes = %q, queued prompt dispatched after idle projection before turn started", writes)
	}

	_, _ = stdoutW.Write([]byte("{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-interrupt\",\"turn\":{\"id\":\"turn-codex-interrupt\",\"status\":\"inProgress\",\"error\":null}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		writes := strings.Join(pty.Writes(), "\n")
		return strings.Contains(writes, `"method":"turn/interrupt"`) && strings.Contains(writes, `"turnId":"turn-codex-interrupt"`)
	})
	_, _ = stdoutW.Write([]byte(
		"{\"method\":\"thread/status/changed\",\"params\":{\"threadId\":\"thread-codex-interrupt\",\"status\":{\"type\":\"idle\"}}}" + "\n" +
			"{\"id\":\"thread-read-interrupting\",\"result\":{\"thread\":{\"id\":\"thread-codex-interrupt\",\"status\":{\"type\":\"idle\"},\"turns\":[]}}}" + "\n"))
	time.Sleep(50 * time.Millisecond)
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after sent interrupt idle projection error = %v", err)
	}
	if !state.Busy || state.RuntimeState != string(codexRuntimePhaseInterrupting) {
		t.Fatalf("SessionState() after sent interrupt idle projection = %+v, want still interrupting busy", state)
	}
	if writes := strings.Join(pty.Writes(), "\n"); strings.Contains(writes, "queued after interrupt") {
		t.Fatalf("runtime writes = %q, queued prompt dispatched after sent interrupt before turn completed", writes)
	}
	_, _ = stdoutW.Write([]byte("{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-interrupt\",\"turn\":{\"id\":\"turn-codex-interrupt\",\"status\":\"aborted\",\"error\":null}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		return strings.Contains(strings.Join(pty.Writes(), "\n"), "queued after interrupt")
	})
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after queued dispatch error = %v", err)
	}
	if !state.Busy || state.RuntimeState != string(codexRuntimePhaseTurnStarting) {
		t.Fatalf("SessionState() after queued dispatch = %+v, want queued turn_starting busy", state)
	}
	_ = stdoutW.Close()
}

func assertCodexVisibleState(t *testing.T, svc *Stub, sessionID session.SessionID, wantBusy bool, wantReason string, wantRuntimeState string) {
	t.Helper()
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy != wantBusy || state.BusyReason != wantReason || state.RuntimeState != wantRuntimeState {
		t.Fatalf("SessionState() = busy:%v reason:%q runtime:%q, want busy:%v reason:%q runtime:%q", state.Busy, state.BusyReason, state.RuntimeState, wantBusy, wantReason, wantRuntimeState)
	}
	details, err := svc.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	if details.Busy != wantBusy || details.RuntimeState != wantRuntimeState {
		t.Fatalf("SessionDetails() = busy:%v runtime:%q, want busy:%v runtime:%q", details.Busy, details.RuntimeState, wantBusy, wantRuntimeState)
	}
	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	for _, item := range listed.Items {
		if item.SessionID != sessionID.String() {
			continue
		}
		if item.Busy != wantBusy || item.BusyReason != wantReason || item.RuntimeState != wantRuntimeState {
			t.Fatalf("ListSessions item = busy:%v reason:%q runtime:%q, want busy:%v reason:%q runtime:%q", item.Busy, item.BusyReason, item.RuntimeState, wantBusy, wantReason, wantRuntimeState)
		}
		return
	}
	t.Fatalf("ListSessions() missing session %q", sessionID)
}

func TestProbeSessionStateRequestsCodexThreadReadAndAppliesResponse(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	pty := &recordingPTY{reader: stdoutR}
	handle.SetPTY(pty)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-read-1\",\"status\":{\"type\":\"idle\"}}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		record, ok := svc.registry.Lookup(sessionID)
		if !ok || record.runtime.codex == nil {
			return false
		}
		_, threadID, _ := record.runtime.codex.snapshot()
		return threadID == "thread-codex-read-1"
	})

	probed, err := svc.ProbeSessionState(context.Background(), ProbeSessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("ProbeSessionState() error = %v", err)
	}
	if probed.ProbeID == "" {
		t.Fatal("ProbeSessionState().ProbeID = empty")
	}
	waitForAppCondition(t, func() bool {
		for _, write := range pty.Writes() {
			if strings.Contains(write, `"method":"thread/read"`) && strings.Contains(write, `"threadId":"thread-codex-read-1"`) && strings.Contains(write, `"includeTurns":true`) {
				return true
			}
		}
		return false
	})

	_, _ = stdoutW.Write([]byte("{\"id\":\"thread-read-3\",\"result\":{\"thread\":{\"id\":\"thread-codex-read-1\",\"status\":{\"type\":\"active\",\"activeFlags\":[]},\"turns\":[{\"id\":\"turn-codex-read-1\",\"status\":\"inProgress\"}]}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.Busy
	})
	record, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatalf("Lookup(%q) ok = false", sessionID)
	}
	_, _, turnID := record.runtime.codex.snapshot()
	if turnID != "turn-codex-read-1" {
		t.Fatalf("codex active turn = %q, want turn-codex-read-1", turnID)
	}

	_, _ = stdoutW.Write([]byte("{\"id\":\"thread-read-4\",\"result\":{\"thread\":{\"id\":\"thread-codex-read-1\",\"status\":{\"type\":\"idle\"},\"turns\":[{\"id\":\"turn-codex-read-1\",\"status\":\"completed\"}]}}}" + "\n"))
	_ = stdoutW.Close()
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy
	})
	record, ok = svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatalf("Lookup(%q) after idle ok = false", sessionID)
	}
	_, _, turnID = record.runtime.codex.snapshot()
	if turnID != "" {
		t.Fatalf("codex active turn after idle = %q, want empty", turnID)
	}
}

func TestCodexNotInitializedRecoversProtocolBeforeThreadRead(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	pty := &recordingPTY{reader: stdoutR}
	handle.SetPTY(pty)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"initialize-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-recover\",\"status\":{\"type\":\"idle\"}}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		record, ok := svc.registry.Lookup(sessionID)
		if !ok || record.runtime.codex == nil {
			return false
		}
		_, threadID, _ := record.runtime.codex.snapshot()
		return threadID == "thread-codex-recover"
	})

	_, _ = stdoutW.Write([]byte(`{"error":{"code":-32600,"message":"Not initialized"},"id":"turn-start-3"}` + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.Busy && state.RuntimeState == string(codexRuntimePhaseInitializing) && state.RuntimeStateReason == "codex_protocol_recovering"
	})
	record, ok := svc.registry.Lookup(sessionID)
	if !ok || record.runtime.codex == nil {
		t.Fatalf("Lookup(%q) missing runtime", sessionID)
	}
	if initialized, threadID, turnID := record.runtime.codex.snapshot(); initialized || threadID != "" || turnID != "" {
		t.Fatalf("snapshot after desync = initialized:%v thread:%q turn:%q, want reset", initialized, threadID, turnID)
	}
	if pending := record.runtime.PendingCodexResumeThreadID(); pending != "thread-codex-recover" {
		t.Fatalf("PendingCodexResumeThreadID() = %q, want thread-codex-recover", pending)
	}
	waitForAppCondition(t, func() bool {
		writes := strings.Join(pty.Writes(), "\n")
		return strings.Contains(writes, `"method":"initialize"`) && strings.Contains(writes, `"id":"initialize-2"`)
	})
	if _, err := svc.ProbeSessionState(context.Background(), ProbeSessionStateRequest{SessionID: sessionID}); err != nil {
		t.Fatalf("ProbeSessionState() during recovery error = %v", err)
	}
	writesBeforeReady := strings.Join(pty.Writes(), "\n")
	if strings.Contains(writesBeforeReady, `"method":"thread/read"`) {
		t.Fatalf("writes during recovery contain thread/read before ready: %s", writesBeforeReady)
	}

	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"initialize-2\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-recover\",\"status\":{\"type\":\"idle\"}}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy && state.RuntimeState == string(codexRuntimePhaseIdle)
	})
	if _, err := svc.ProbeSessionState(context.Background(), ProbeSessionStateRequest{SessionID: sessionID}); err != nil {
		t.Fatalf("ProbeSessionState() after recovery error = %v", err)
	}
	waitForAppCondition(t, func() bool {
		for _, write := range pty.Writes() {
			if strings.Contains(write, `"method":"thread/read"`) && strings.Contains(write, `"threadId":"thread-codex-recover"`) {
				return true
			}
		}
		return false
	})
	_ = stdoutW.Close()
}

func TestCodexNotInitializedRetriesExplicitlyRejectedPromptWithoutDuplicateUserMessage(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	pty := &recordingPTY{reader: stdoutR}
	handle.SetPTY(pty)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"initialize-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-retry\",\"status\":{\"type\":\"idle\"}}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.RuntimeState == string(codexRuntimePhaseIdle)
	})

	if _, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "retry exactly once"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	waitForAppCondition(t, func() bool {
		return countRuntimeWritesContaining(pty.Writes(), `"method":"turn/start"`) == 1
	})
	_, _ = stdoutW.Write([]byte(`{"error":{"code":-32600,"message":"Not initialized"},"id":"turn-start-3"}` + "\n"))
	waitForAppCondition(t, func() bool {
		writes := strings.Join(pty.Writes(), "\n")
		return strings.Contains(writes, `"method":"initialize"`) && strings.Contains(writes, `"id":"initialize-3"`)
	})
	_, _ = stdoutW.Write([]byte(`{"id":"initialize-3","result":{"userAgent":"actrail-test"}}` + "\n"))
	waitForAppCondition(t, func() bool {
		writes := strings.Join(pty.Writes(), "\n")
		return strings.Contains(writes, `"method":"thread/resume"`) && strings.Contains(writes, `"threadId":"thread-codex-retry"`)
	})
	_, _ = stdoutW.Write([]byte(`{"method":"thread/started","params":{"thread":{"id":"thread-codex-retry","status":{"type":"idle"}}}}` + "\n"))
	waitForAppCondition(t, func() bool {
		writes := pty.Writes()
		return countRuntimeWritesContaining(writes, `"method":"turn/start"`) == 2 &&
			countRuntimeWritesContaining(writes, `retry exactly once`) == 2
	})
	_, _ = stdoutW.Write([]byte(`{"method":"turn/started","params":{"turn":{"id":"turn-codex-retry"},"threadId":"thread-codex-retry"}}` + "\n"))
	waitForAppCondition(t, func() bool {
		return svc.codexOutboundPromptText(sessionID) == ""
	})

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Role != "user" || messages.Items[0].Text != "retry exactly once" {
		t.Fatalf("SessionMessages() = %+v, want one committed user message", messages.Items)
	}
	_ = stdoutW.Close()
}

func TestCodexCapacityErrorRetriesWithVisibleContinueMessage(t *testing.T) {
	originalDelay := codexCapacityRetryDelayForAttempt
	codexCapacityRetryDelayForAttempt = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { codexCapacityRetryDelayForAttempt = originalDelay })

	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	pty := &recordingPTY{reader: stdoutR}
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPTY(pty)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"initialize-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-capacity\",\"status\":{\"type\":\"idle\"}}}}" + "\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.RuntimeState == string(codexRuntimePhaseIdle)
	})

	if _, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "retry after capacity"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	waitForAppCondition(t, func() bool {
		return countRuntimeWritesContaining(pty.Writes(), `"method":"turn/start"`) == 1
	})
	_, _ = stdoutW.Write([]byte(`{"method":"turn/started","params":{"threadId":"thread-codex-capacity","turn":{"id":"turn-codex-capacity-1","status":"inProgress","error":null}}}` + "\n"))
	waitForAppCondition(t, func() bool {
		return svc.codexOutboundPromptText(sessionID) == ""
	})
	_, _ = stdoutW.Write([]byte(`{"method":"turn/completed","params":{"threadId":"thread-codex-capacity","turn":{"id":"turn-codex-capacity-1","status":"failed","error":{"message":"Selected model is at capacity. Please try a different model."}}}}` + "\n"))
	waitForAppCondition(t, func() bool {
		writes := pty.Writes()
		return countRuntimeWritesContaining(writes, `"method":"turn/start"`) == 2 &&
			countRuntimeWritesContaining(writes, `retry after capacity`) == 1 &&
			countRuntimeWritesContaining(writes, codexCapacityRetryPrompt) == 1
	})
	_, _ = stdoutW.Write([]byte(`{"method":"item/completed","params":{"threadId":"thread-codex-capacity","turnId":"turn-codex-capacity-2","item":{"type":"userMessage","id":"user-capacity-2","text":"继续"}}}` + "\n"))
	time.Sleep(50 * time.Millisecond)

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	originalUserCount := 0
	continueUserCount := 0
	retryDiagnostic := false
	errorSeen := false
	for _, item := range messages.Items {
		if item.Role == "user" && item.Text == "retry after capacity" {
			originalUserCount++
		}
		if item.Role == "user" && item.Text == codexCapacityRetryPrompt {
			continueUserCount++
		}
		if item.Type == "pi_event" && strings.Contains(item.Text, "Codex model is at capacity; retrying request") {
			retryDiagnostic = true
		}
		if item.Type == "error" && strings.Contains(item.Text, "Selected model is at capacity") {
			errorSeen = true
		}
	}
	if originalUserCount != 1 || continueUserCount != 1 {
		t.Fatalf("user counts = original:%d continue:%d in %+v, want both visible once", originalUserCount, continueUserCount, messages.Items)
	}
	if !retryDiagnostic || !errorSeen {
		t.Fatalf("messages = %+v, want capacity error and retry diagnostic", messages.Items)
	}
	_ = stdoutW.Close()
}

func countRuntimeWritesContaining(writes []string, needle string) int {
	count := 0
	for _, write := range writes {
		if strings.Contains(write, needle) {
			count++
		}
	}
	return count
}

func TestCreateSessionMapsCodexToolReasoningUsageAndErrors(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	_, _ = stdoutW.Write([]byte("{" +
		"\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}" + "\n" +
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-2\"}}}" + "\n" +
		"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-2\",\"turn\":{\"id\":\"turn-codex-2\",\"status\":\"inProgress\",\"startedAt\":1760000001,\"error\":null}}}" + "\n" +
		"{\"method\":\"item/started\",\"params\":{\"threadId\":\"thread-codex-2\",\"turnId\":\"turn-codex-2\",\"item\":{\"type\":\"commandExecution\",\"id\":\"cmd-1\",\"command\":\"go test ./...\",\"cwd\":\"/root/code/ActRail\",\"status\":\"inProgress\"}}}" + "\n" +
		"{\"method\":\"item/commandExecution/outputDelta\",\"params\":{\"threadId\":\"thread-codex-2\",\"turnId\":\"turn-codex-2\",\"itemId\":\"cmd-1\",\"delta\":\"ok\\n\"}}" + "\n" +
		"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-2\",\"turnId\":\"turn-codex-2\",\"item\":{\"type\":\"commandExecution\",\"id\":\"cmd-1\",\"command\":\"go test ./...\",\"cwd\":\"/root/code/ActRail\",\"status\":\"completed\",\"aggregatedOutput\":\"ok\\n\",\"exitCode\":0,\"durationMs\":1200}}}" + "\n" +
		"{\"method\":\"item/reasoning/summaryTextDelta\",\"params\":{\"threadId\":\"thread-codex-2\",\"turnId\":\"turn-codex-2\",\"itemId\":\"reason-1\",\"delta\":\"Inspecting runtime schema\",\"summaryIndex\":0}}" + "\n" +
		"{\"method\":\"thread/tokenUsage/updated\",\"params\":{\"threadId\":\"thread-codex-2\",\"turnId\":\"turn-codex-2\",\"tokenUsage\":{\"total\":{\"totalTokens\":2048,\"inputTokens\":1024,\"cachedInputTokens\":0,\"outputTokens\":512,\"reasoningOutputTokens\":512},\"last\":{\"totalTokens\":128,\"inputTokens\":64,\"cachedInputTokens\":0,\"outputTokens\":64,\"reasoningOutputTokens\":0},\"modelContextWindow\":8192}}}" + "\n" +
		"{\"method\":\"warning\",\"params\":{\"message\":\"Codex warning surfaced\",\"threadId\":\"thread-codex-2\",\"turnId\":\"turn-codex-2\"}}" + "\n" +
		"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-2\",\"turnId\":\"turn-codex-2\",\"item\":{\"type\":\"agentMessage\",\"id\":\"msg-1\",\"text\":\"done\"}}}" + "\n" +
		"{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-2\",\"turn\":{\"id\":\"turn-codex-2\",\"status\":\"completed\",\"startedAt\":1760000001,\"completedAt\":1760000002,\"error\":null}}}" + "\n"))
	_ = stdoutW.Close()

	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, IncludeToolEvents: true})
		if err != nil {
			return false
		}
		return len(messages.Items) >= 5
	})

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, IncludeToolEvents: true})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	kinds := make([]string, 0, len(messages.Items))
	for _, item := range messages.Items {
		kinds = append(kinds, item.Type)
	}
	wantKinds := []string{"tool", "tool_result", "tool_result", "reasoning", "error", ""}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("message kinds = %#v, want %#v", kinds, wantKinds)
	}
	for index, want := range wantKinds {
		if kinds[index] != want {
			t.Fatalf("message kinds = %#v, want %#v", kinds, wantKinds)
		}
	}
	if messages.Items[0].Text != "go test ./..." || messages.Items[0].Type != "tool" {
		t.Fatalf("tool call message = %+v", messages.Items[0])
	}
	if messages.Items[3].Type != "reasoning" || messages.Items[3].Text != "Inspecting runtime schema" {
		t.Fatalf("reasoning message = %+v", messages.Items[3])
	}
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.TurnTiming != nil && state.TurnTiming.LastEventTS != nil && *state.TurnTiming.LastEventTS == 1760000002
	})
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.ContextUsage == nil || state.ContextUsage.UsedTokens == nil || *state.ContextUsage.UsedTokens != 128 || state.ContextUsage.TotalTokens == nil || *state.ContextUsage.TotalTokens != 8192 || state.ContextUsage.PercentUsed == nil || *state.ContextUsage.PercentUsed != 2 {
		t.Fatalf("SessionState().ContextUsage = %+v", state.ContextUsage)
	}
	if state.TurnTiming == nil || state.TurnTiming.StartedTS != 1760000001 || state.TurnTiming.LastEventTS == nil || *state.TurnTiming.LastEventTS != 1760000002 {
		t.Fatalf("SessionState().TurnTiming = %+v", state.TurnTiming)
	}
}

func TestCreateSessionConsumesHelperBackedPIReplayAndLiveOutputIntoStateTranscriptAndEvents(t *testing.T) {
	generationID := mustHelperGenerationID(t, "g_helper_ingest")
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID}))
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	if _, ok := svc.registry.Lookup(sessionID); !ok {
		t.Fatalf("Lookup(%q) ok = false", sessionID)
	}
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	manifestPath := filepath.Join(t.TempDir(), iodclient.ManifestFilename)
	proof, err := iod.NewHelloProof(os.Getpid(), nil, filepath.Join(t.TempDir(), "transport.wal"), filepath.Join(t.TempDir(), "io"), float64(time.Now().UTC().Unix()))
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	hello, err := iod.NewHelloPacket(sessionID, generationID, 1, proof)
	if err != nil {
		t.Fatalf("NewHelloPacket() error = %v", err)
	}
	svc.helpers.replaceAll(map[session.SessionID]attachedHelper{
		sessionID: {
			Binding:      helperGenerationBinding{SessionID: sessionID, GenerationID: generationID},
			ManifestPath: manifestPath,
			Manifest:     manifest,
			Hello:        hello,
			Client:       iodclient.NewClient(clientConn),
		},
	}, nil)
	defer svc.helpers.replaceAll(nil, nil)

	runtime := svc.runtimeForSession(sessionID, session.BackendPI, sessionRuntime{})
	svc.startRuntimeIngest(sessionID, session.BackendPI, runtime)

	enc := json.NewEncoder(serverConn)
	seq1 := iod.EventSeq(1)
	fact1, err := iod.NewHelperFact(iod.FactOutputDelta, &seq1, json.RawMessage(`{"stream":"pty","data":"{\"type\":\"extension_ui_request\",\"id\":\"ui-req-helper\",\"method\":\"select\",\"question\":\"Where should this go?\",\"options\":[\"Details\",\"Sidebar\"]}\n{\"type\":\"message.delta\",\"turn_id\":\"turn-helper-1\",\"role\":\"assistant\",\"delta\":\"Helper-backed "}`))
	if err != nil {
		t.Fatalf("NewHelperFact(first) error = %v", err)
	}
	item1, err := iod.NewReplayItem(7, fact1)
	if err != nil {
		t.Fatalf("NewReplayItem(first) error = %v", err)
	}
	packet1, err := iod.NewReplayItemPacket(sessionID, generationID, item1)
	if err != nil {
		t.Fatalf("NewReplayItemPacket(first) error = %v", err)
	}
	if err := enc.Encode(packet1); err != nil {
		t.Fatalf("Encode(first) error = %v", err)
	}
	seq2 := iod.EventSeq(2)
	fact2, err := iod.NewHelperFact(iod.FactOutputDelta, &seq2, json.RawMessage(`{"stream":"pty","data":"PI output reached the session transcript.\"}\n{\"type\":\"message_end\",\"message\":{\"role\":\"toolResult\",\"toolCallId\":\"ui-req-helper\",\"toolName\":\"ask_user\",\"details\":{\"answer\":\"Sidebar\",\"cancelled\":false}}}\n{\"type\":\"turn.completed\",\"turn_id\":\"turn-helper-1\",\"role\":\"assistant\",\"text\":\"Helper-backed PI output reached the session transcript.\"}\n"}`))
	if err != nil {
		t.Fatalf("NewHelperFact(second) error = %v", err)
	}
	packet2, err := iod.NewStatePacket(sessionID, generationID, fact2)
	if err != nil {
		t.Fatalf("NewStatePacket(second) error = %v", err)
	}
	if err := enc.Encode(packet2); err != nil {
		t.Fatalf("Encode(second) error = %v", err)
	}

	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		if err != nil || len(messages.Items) != 1 {
			return false
		}
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return state.UIRequest == nil && messages.Items[0].Text == "Helper-backed PI output reached the session transcript."
	})

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 {
		t.Fatalf("len(SessionMessages().Items) = %d, want 1", len(messages.Items))
	}
	if messages.Items[0].Role != "assistant" || messages.Items[0].Text != "Helper-backed PI output reached the session transcript." {
		t.Fatalf("SessionMessages().Items[0] = %+v", messages.Items[0])
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy = true, want false")
	}
	if state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState().PartialAssistantTurn = %+v, want nil", state.PartialAssistantTurn)
	}
	if state.UIRequest != nil {
		t.Fatalf("SessionState().UIRequest = %+v, want nil after helper ui resolution", state.UIRequest)
	}

	snapshot := sink.snapshot()
	if len(snapshot.deltas) != 1 || snapshot.deltas[0].Delta != "Helper-backed PI output reached the session transcript." {
		t.Fatalf("runtime delta events = %#v", snapshot.deltas)
	}
	if len(snapshot.commits) != 1 || snapshot.commits[0].Message.Text != "Helper-backed PI output reached the session transcript." {
		t.Fatalf("runtime commit events = %#v", snapshot.commits)
	}
	if len(snapshot.uiRequests) != 1 || snapshot.uiRequests[0].Request.RequestID != "ui-req-helper" {
		t.Fatalf("runtime ui request events = %#v", snapshot.uiRequests)
	}
	if len(snapshot.uiResolved) != 1 || snapshot.uiResolved[0].RequestID != "ui-req-helper" {
		t.Fatalf("runtime ui resolved events = %#v", snapshot.uiResolved)
	}
}

func TestCreateSessionConsumesHelperBackedCodexReplayAndLiveOutputIntoStateTranscriptAndEvents(t *testing.T) {
	generationID := mustHelperGenerationID(t, "g_helper_codex_ingest")
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID}))
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	record, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatalf("Lookup(%q) ok = false", sessionID)
	}
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	manifestPath := filepath.Join(t.TempDir(), iodclient.ManifestFilename)
	proof, err := iod.NewHelloProof(os.Getpid(), nil, filepath.Join(t.TempDir(), "transport.wal"), filepath.Join(t.TempDir(), "io"), float64(time.Now().UTC().Unix()))
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	hello, err := iod.NewHelloPacket(sessionID, generationID, 1, proof)
	if err != nil {
		t.Fatalf("NewHelloPacket() error = %v", err)
	}
	svc.helpers.replaceAll(map[session.SessionID]attachedHelper{
		sessionID: {
			Binding:      helperGenerationBinding{SessionID: sessionID, GenerationID: generationID},
			ManifestPath: manifestPath,
			Manifest:     manifest,
			Hello:        hello,
			Client:       iodclient.NewClient(clientConn),
		},
	}, nil)
	defer svc.helpers.replaceAll(nil, nil)

	runtime := svc.runtimeForSession(sessionID, session.BackendCodex, sessionRuntime{})
	svc.startRuntimeIngest(sessionID, session.BackendCodex, runtime)

	enc := json.NewEncoder(serverConn)
	seq1 := iod.EventSeq(1)
	fact1, err := iod.NewHelperFact(iod.FactOutputDelta, &seq1, json.RawMessage(`{"stream":"unix","data":"{\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}\n{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-helper-1\"}}}\n{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-helper-1\",\"turn\":{\"id\":\"turn-codex-helper-1\",\"status\":\"inProgress\",\"error\":null}}}\n{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-helper-1\",\"turnId\":\"turn-codex-helper-1\",\"itemId\":\"item-codex-helper-1\",\"delta\":\"Helper-backed Codex \"}}\n"}`))
	if err != nil {
		t.Fatalf("NewHelperFact(first) error = %v", err)
	}
	item1, err := iod.NewReplayItem(7, fact1)
	if err != nil {
		t.Fatalf("NewReplayItem(first) error = %v", err)
	}
	packet1, err := iod.NewReplayItemPacket(sessionID, generationID, item1)
	if err != nil {
		t.Fatalf("NewReplayItemPacket(first) error = %v", err)
	}
	if err := enc.Encode(packet1); err != nil {
		t.Fatalf("Encode(first) error = %v", err)
	}
	seq2 := iod.EventSeq(2)
	fact2, err := iod.NewHelperFact(iod.FactOutputDelta, &seq2, json.RawMessage(`{"stream":"unix","data":"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-helper-1\",\"turnId\":\"turn-codex-helper-1\",\"itemId\":\"item-codex-helper-1\",\"delta\":\"reached the session transcript.\"}}\n{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-helper-1\",\"turnId\":\"turn-codex-helper-1\",\"item\":{\"type\":\"agentMessage\",\"id\":\"item-codex-helper-1\",\"text\":\"Helper-backed Codex reached the session transcript.\"}}}\n{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-helper-1\",\"turn\":{\"id\":\"turn-codex-helper-1\",\"status\":\"completed\",\"error\":null}}}\n"}`))
	if err != nil {
		t.Fatalf("NewHelperFact(second) error = %v", err)
	}
	packet2, err := iod.NewStatePacket(sessionID, generationID, fact2)
	if err != nil {
		t.Fatalf("NewStatePacket(second) error = %v", err)
	}
	if err := enc.Encode(packet2); err != nil {
		t.Fatalf("Encode(second) error = %v", err)
	}

	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		if err != nil || len(messages.Items) != 1 {
			return false
		}
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return !state.Busy && messages.Items[0].Text == "Helper-backed Codex reached the session transcript."
	})

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 {
		t.Fatalf("len(SessionMessages().Items) = %d, want 1", len(messages.Items))
	}
	if messages.Items[0].Role != "assistant" || messages.Items[0].Text != "Helper-backed Codex reached the session transcript." {
		t.Fatalf("SessionMessages().Items[0] = %+v", messages.Items[0])
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy = true, want false")
	}
	if state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState().PartialAssistantTurn = %+v, want nil", state.PartialAssistantTurn)
	}
	record, ok = svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatalf("Lookup(%q) ok = false", sessionID)
	}
	_, threadID, turnID := record.runtime.codex.snapshot()
	if threadID != "thread-codex-helper-1" || turnID != "" {
		t.Fatalf("codex runtime state = (thread=%q turn=%q), want (thread-codex-helper-1, empty)", threadID, turnID)
	}

	snapshot := sink.snapshot()
	if len(snapshot.deltas) != 2 || snapshot.deltas[0].Delta != "Helper-backed Codex " || snapshot.deltas[1].Delta != "reached the session transcript." {
		t.Fatalf("runtime delta events = %#v", snapshot.deltas)
	}
	if len(snapshot.commits) != 1 || snapshot.commits[0].Message.Text != "Helper-backed Codex reached the session transcript." {
		t.Fatalf("runtime commit events = %#v", snapshot.commits)
	}
	if len(snapshot.uiRequests) != 0 || len(snapshot.uiResolved) != 0 {
		t.Fatalf("runtime ui events = requests:%#v resolved:%#v, want none", snapshot.uiRequests, snapshot.uiResolved)
	}
	if len(snapshot.states) == 0 {
		t.Fatal("runtime session state events = 0, want at least one")
	}
}

func waitForAppCondition(t *testing.T, cond func() bool) {
	t.Helper()
	waitForTestCondition(t, cond)
}
