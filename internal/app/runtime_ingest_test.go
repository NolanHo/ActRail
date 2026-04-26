package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

type captureRuntimeSink struct {
	mu          sync.Mutex
	states      []SessionStateEvent
	deltas      []MessageDeltaEvent
	commits     []MessageCommitEvent
	queueStates []QueueStateEvent
	uiRequests  []UIRequestEvent
	uiResolved  []UIResolvedEvent
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

func (s *captureRuntimeSink) snapshot() captureRuntimeSink {
	s.mu.Lock()
	defer s.mu.Unlock()
	return captureRuntimeSink{
		states:      append([]SessionStateEvent(nil), s.states...),
		deltas:      append([]MessageDeltaEvent(nil), s.deltas...),
		commits:     append([]MessageCommitEvent(nil), s.commits...),
		queueStates: append([]QueueStateEvent(nil), s.queueStates...),
		uiRequests:  append([]UIRequestEvent(nil), s.uiRequests...),
		uiResolved:  append([]UIResolvedEvent(nil), s.uiResolved...),
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
		"{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}],\"stopReason\":\"stop\",\"timestamp\":1774708716099}}" + "\n" +
		"{\"type\":\"turn_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Codoxear serves a browser UI for Codex-style sessions.\"}],\"stopReason\":\"stop\",\"timestamp\":1774708716099},\"toolResults\":[]}" + "\n"))
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

func TestCreateSessionConsumesHelperBackedPIRuntimeOutputIntoStateAndTranscript(t *testing.T) {
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
	proof, err := iod.NewHelloProof(os.Getpid(), nil, filepath.Join(t.TempDir(), "transport.wal"), filepath.Join(t.TempDir(), "control.sock"), float64(time.Now().UTC().Unix()))
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

	runtime := svc.runtimeForSession(sessionID, record.runtime)
	svc.startRuntimeIngest(sessionID, session.BackendPI, runtime)

	enc := json.NewEncoder(serverConn)
	seq1 := iod.EventSeq(1)
	fact1, err := iod.NewHelperFact(iod.FactOutputDelta, &seq1, json.RawMessage(`{"stream":"pty","data":"{\"type\":\"extension_ui_request\",\"id\":\"ui-req-helper\",\"method\":\"select\",\"question\":\"Where should this go?\",\"options\":[\"Details\",\"Sidebar\"]}\n{\"type\":\"message.delta\",\"turn_id\":\"turn-helper-1\",\"role\":\"assistant\",\"delta\":\"Helper-backed "}`))
	if err != nil {
		t.Fatalf("NewHelperFact(first) error = %v", err)
	}
	packet1, err := iod.NewStatePacket(sessionID, generationID, fact1)
	if err != nil {
		t.Fatalf("NewStatePacket(first) error = %v", err)
	}
	if err := enc.Encode(packet1); err != nil {
		t.Fatalf("Encode(first) error = %v", err)
	}
	seq2 := iod.EventSeq(2)
	fact2, err := iod.NewHelperFact(iod.FactOutputDelta, &seq2, json.RawMessage(`{"stream":"pty","data":"PI output reached the session transcript.\"}\n{\"type\":\"turn.completed\",\"turn_id\":\"turn-helper-1\",\"role\":\"assistant\",\"text\":\"Helper-backed PI output reached the session transcript.\"}\n"}`))
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
		if err != nil || state.UIRequest == nil {
			return false
		}
		return messages.Items[0].Text == "Helper-backed PI output reached the session transcript."
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
	if state.UIRequest == nil || state.UIRequest.RequestID != "ui-req-helper" {
		t.Fatalf("SessionState().UIRequest = %+v, want helper ui request", state.UIRequest)
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
