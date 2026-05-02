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
	generationBroken       []GenerationBrokenEvent
	transportResetRequired []TransportResetRequiredEvent
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
		generationBroken:       append([]GenerationBrokenEvent(nil), s.generationBroken...),
		transportResetRequired: append([]TransportResetRequiredEvent(nil), s.transportResetRequired...),
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

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
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

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
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
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
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

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
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
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
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
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
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
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
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

func TestPIRPCBusyHoldIgnoresEarlyIdleGetState(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
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

func TestPIRPCIdleHoldIgnoresStaleBusyGetStateAfterCompletion(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	generationID, err := iod.NewGenerationID("g_rpc_state_idle_hold")
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

	decoder := runtimeEventDecoder{backend: session.BackendPI}
	if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(`{"id":"stale-busy","type":"response","command":"get_state","success":true,"data":{"isStreaming":true,"isCompacting":false,"pendingMessageCount":0}}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(get_state busy) error = %v", err)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatalf("SessionState().Busy = true, want false during idle hold")
	}
}

func TestIODTransportResetRequiredEmitsDiagnosticMessage(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
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
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
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

func TestNextPIRPCStatePollIntervalUsesBusyIntervalForPendingProbe(t *testing.T) {
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

	if got := svc.nextPIRPCStatePollInterval(sessionID, generationID); got != piRPCStateBusyPollInterval {
		t.Fatalf("nextPIRPCStatePollInterval() = %s, want %s", got, piRPCStateBusyPollInterval)
	}
	svc.startPIRPCStartupProbe(sessionID, generationID)
	if got := svc.nextPIRPCStatePollInterval(sessionID, generationID); got != piRPCStateStartupPollInterval {
		t.Fatalf("nextPIRPCStatePollInterval() after startup = %s, want %s", got, piRPCStateStartupPollInterval)
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
		return len(messages.Items) == 1 && messages.Items[0].Text == "Codex runtime reached the session transcript."
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

func TestCreateSessionConsumesHelperBackedPIReplayAndLiveOutputIntoStateTranscriptAndEvents(t *testing.T) {
	generationID := mustHelperGenerationID(t, "g_helper_ingest")
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID}))
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
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

	runtime := svc.runtimeForSession(sessionID, session.BackendPI, record.runtime)
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

	runtime := svc.runtimeForSession(sessionID, session.BackendCodex, record.runtime)
	svc.startRuntimeIngest(sessionID, session.BackendCodex, runtime)

	enc := json.NewEncoder(serverConn)
	seq1 := iod.EventSeq(1)
	fact1, err := iod.NewHelperFact(iod.FactOutputDelta, &seq1, json.RawMessage(`{"stream":"stdout","data":"{\"id\":\"init-1\",\"result\":{\"userAgent\":\"actrail-test\"}}\n{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-helper-1\"}}}\n{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-helper-1\",\"turn\":{\"id\":\"turn-codex-helper-1\",\"status\":\"inProgress\",\"error\":null}}}\n{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-helper-1\",\"turnId\":\"turn-codex-helper-1\",\"itemId\":\"item-codex-helper-1\",\"delta\":\"Helper-backed Codex \"}}\n"}`))
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
	fact2, err := iod.NewHelperFact(iod.FactOutputDelta, &seq2, json.RawMessage(`{"stream":"stdout","data":"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-helper-1\",\"turnId\":\"turn-codex-helper-1\",\"itemId\":\"item-codex-helper-1\",\"delta\":\"reached the session transcript.\"}}\n{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-helper-1\",\"turnId\":\"turn-codex-helper-1\",\"item\":{\"type\":\"agentMessage\",\"id\":\"item-codex-helper-1\",\"text\":\"Helper-backed Codex reached the session transcript.\"}}}\n{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-helper-1\",\"turn\":{\"id\":\"turn-codex-helper-1\",\"status\":\"completed\",\"error\":null}}}\n"}`))
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
