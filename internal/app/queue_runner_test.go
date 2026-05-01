package app

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func TestEnqueueDispatchesWhenSessionAlreadyIdle(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)

	queued, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "queued prompt"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if queued.Busy {
		t.Fatalf("Enqueue().Busy = %v, want false before async dispatch", queued.Busy)
	}
	if len(queued.Queue.Items) != 1 || queued.Queue.Items[0].Text != "queued prompt" {
		t.Fatalf("Enqueue().Queue = %+v, want queued prompt", queued.Queue.Items)
	}

	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return !state.Busy && len(state.Queue.Items) == 0 && len(pty.Writes()) == 1
	})

	writes := pty.Writes()
	if len(writes) != 1 || writes[0] != "{\"type\":\"prompt\",\"message\":\"queued prompt\"}\n" {
		t.Fatalf("pty writes after idle Enqueue() = %#v, want queued RPC prompt command", writes)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Role != "user" || messages.Items[0].Text != "queued prompt" {
		t.Fatalf("SessionMessages() = %+v, want one queued user message", messages)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || len(state.Queue.Items) != 0 || state.TailSeq != 1 {
		t.Fatalf("SessionState() after idle dispatch = %+v, want idle empty queue tail_seq 1 before Pi reports runtime state", state)
	}
}

type blockingWritePTY struct {
	mu      sync.Mutex
	writes  []string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingWritePTY() *blockingWritePTY {
	return &blockingWritePTY{entered: make(chan struct{}), release: make(chan struct{})}
}

func (p *blockingWritePTY) Read([]byte) (int, error) { return 0, io.EOF }

func (p *blockingWritePTY) Write(data []byte) (int, error) {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, string(data))
	return len(data), nil
}

func (p *blockingWritePTY) Close() error { return nil }

func (p *blockingWritePTY) Resize(process.PTYSize) error { return nil }

func (p *blockingWritePTY) Writes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	copied := make([]string, len(p.writes))
	copy(copied, p.writes)
	return copied
}

func TestCancelQueueWaitsForQueuedDispatchCriticalSection(t *testing.T) {
	pty := newBlockingWritePTY()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPTY(pty)
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: &process.FakeRunner{NextHandle: handle}})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	if _, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "queued prompt"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	select {
	case <-pty.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("queued dispatch did not reach runtime write")
	}

	cancelDone := make(chan error, 1)
	go func() {
		_, err := svc.CancelQueue(context.Background(), CancelQueueRequest{SessionID: sessionID})
		cancelDone <- err
	}()
	select {
	case err := <-cancelDone:
		t.Fatalf("CancelQueue returned before queued dispatch finished; err=%v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(pty.release)
	select {
	case err := <-cancelDone:
		if err != nil {
			t.Fatalf("CancelQueue() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelQueue did not return after queued dispatch finished")
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "queued prompt" {
		t.Fatalf("SessionMessages() = %+v, want queued prompt committed", messages.Items)
	}
	if _, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "next prompt"}); err != nil {
		t.Fatalf("Send() after CancelQueue() error = %v", err)
	}
	if writes := pty.Writes(); len(writes) != 2 {
		t.Fatalf("pty writes = %#v, want queued and next prompts", writes)
	}
}

func TestPIRPCQueuedPromptDoesNotSetBusyBeforeRuntimeState(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	if _, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "first prompt"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	queued, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "second prompt"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if queued.Busy || len(queued.Queue.Items) != 1 || queued.Queue.Items[0].Text != "second prompt" {
		t.Fatalf("Enqueue() = %+v, want queued second prompt with idle session state", queued)
	}

	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return !state.Busy && len(state.Queue.Items) == 0 && state.TailSeq == 2 && len(pty.Writes()) == 2
	})

	writes := pty.Writes()
	if len(writes) != 2 || writes[1] != "{\"type\":\"prompt\",\"message\":\"second prompt\"}\n" {
		t.Fatalf("pty writes after queued dispatch = %#v, want queued second prompt RPC command", writes)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() after dispatch error = %v", err)
	}
	if len(messages.Items) != 2 || messages.Items[1].Role != "user" || messages.Items[1].Text != "second prompt" {
		t.Fatalf("SessionMessages() after dispatch = %+v, want queued second prompt committed", messages)
	}

	decoder := runtimeEventDecoder{backend: session.BackendPI}
	if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(`{"id":"actrail-state-busy","type":"response","command":"get_state","success":true,"data":{"isStreaming":true,"isCompacting":false,"pendingMessageCount":0}}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(get_state busy) error = %v", err)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after busy state error = %v", err)
	}
	if !state.Busy || len(state.Queue.Items) != 0 || state.TailSeq != 2 {
		t.Fatalf("SessionState() after busy state = %+v, want busy empty queue tail_seq 2", state)
	}

	after := sink.snapshot()
	if len(after.commits) != 2 {
		t.Fatalf("commit events count = %d, want 2", len(after.commits))
	}
	if got := after.commits[len(after.commits)-1].Message.Text; got != "second prompt" {
		t.Fatalf("last commit event text = %q, want %q", got, "second prompt")
	}
	if len(after.queueStates) == 0 || len(after.queueStates[len(after.queueStates)-1].Queue.Items) != 0 {
		t.Fatalf("last queue event = %+v, want empty queue", after.queueStates)
	}
	if len(after.states) == 0 || !after.states[len(after.states)-1].Busy || after.states[len(after.states)-1].QueueLen != 0 {
		t.Fatalf("last state event = %+v, want busy true queue_len 0 from Pi runtime state", after.states)
	}
}
