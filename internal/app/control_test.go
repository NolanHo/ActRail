package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

type fakePTY struct {
	mu     sync.Mutex
	writes []string
}

func (p *fakePTY) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (p *fakePTY) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, string(data))
	return len(data), nil
}

func (p *fakePTY) Close() error {
	return nil
}

func (p *fakePTY) Resize(process.PTYSize) error {
	return nil
}

func (p *fakePTY) Writes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	copied := make([]string, len(p.writes))
	copy(copied, p.writes)
	return copied
}

func newControlFixture(t *testing.T) (*Stub, session.SessionID, *process.FakeHandle, *fakePTY) {
	t.Helper()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	pty := &fakePTY{}
	handle.SetPTY(pty)
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
	return svc, sessionID, handle, pty
}

func TestStubControlMethodsMutateRuntimeAndSessionState(t *testing.T) {
	svc, sessionID, handle, pty := newControlFixture(t)

	sent, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "Implement runtime control"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if sent.Message.Seq != 1 || sent.Message.Role != "user" || sent.Message.Text != "Implement runtime control" {
		t.Fatalf("Send() = %+v, want seq 1 user message", sent)
	}
	if !sent.Busy {
		t.Fatal("Send().Busy = false, want true")
	}
	if len(sent.Queue.Items) != 0 {
		t.Fatalf("Send().Queue.Items = %+v, want empty", sent.Queue.Items)
	}
	writes := pty.Writes()
	if len(writes) != 1 || writes[0] != "Implement runtime control\n" {
		t.Fatalf("pty writes after Send() = %#v, want prompt with newline", writes)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after Send() error = %v", err)
	}
	if !state.Busy || state.TailSeq != 1 {
		t.Fatalf("SessionState() after Send() = %+v, want busy tail_seq 1", state)
	}

	queued, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "first queued"})
	if err != nil {
		t.Fatalf("Enqueue(first) error = %v", err)
	}
	if len(queued.Queue.Items) != 1 || queued.Queue.Items[0].Text != "first queued" {
		t.Fatalf("Enqueue(first) = %+v, want first queued item", queued)
	}
	queued, err = svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "replacement queued"})
	if err != nil {
		t.Fatalf("Enqueue(replacement) error = %v", err)
	}
	if len(queued.Queue.Items) != 1 || queued.Queue.Items[0].Text != "replacement queued" {
		t.Fatalf("Enqueue(replacement) = %+v, want replacement queued item", queued)
	}
	if !queued.Busy {
		t.Fatal("Enqueue().Busy = false, want true")
	}

	interrupted, err := svc.Interrupt(context.Background(), InterruptRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	if interrupted.Busy {
		t.Fatalf("Interrupt() = %+v, want busy false", interrupted)
	}
	if len(interrupted.Queue.Items) != 1 || interrupted.Queue.Items[0].Text != "replacement queued" {
		t.Fatalf("Interrupt().Queue = %+v, want retained queued item", interrupted.Queue.Items)
	}
	if handle.InterruptCalls() != 1 {
		t.Fatalf("handle.InterruptCalls() = %d, want 1", handle.InterruptCalls())
	}
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after Interrupt() error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy after Interrupt() = true, want false")
	}
	if len(state.Queue.Items) != 1 || state.Queue.Items[0].Text != "replacement queued" {
		t.Fatalf("SessionState().Queue after Interrupt() = %+v, want retained queue", state.Queue.Items)
	}

	if err := svc.SetSessionUIRequest(sessionID, SessionUIRequestSnapshot{RequestID: "ask_1", Kind: "ask_user", Prompt: "Choose one option"}); err != nil {
		t.Fatalf("SetSessionUIRequest() error = %v", err)
	}
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() with ui request error = %v", err)
	}
	if state.UIRequest == nil || state.UIRequest.RequestID != "ask_1" {
		t.Fatalf("SessionState().UIRequest = %+v, want ask_1", state.UIRequest)
	}
	resolved, err := svc.RespondUI(context.Background(), UIResponseRequest{SessionID: sessionID, ResponseTo: "ask_1", Value: "A"})
	if err != nil {
		t.Fatalf("RespondUI() error = %v", err)
	}
	if resolved.ResolvedRequestID != "ask_1" {
		t.Fatalf("RespondUI().ResolvedRequestID = %q, want %q", resolved.ResolvedRequestID, "ask_1")
	}
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after RespondUI() error = %v", err)
	}
	if state.UIRequest != nil {
		t.Fatalf("SessionState().UIRequest after RespondUI() = %+v, want nil", state.UIRequest)
	}
	writes = pty.Writes()
	if len(writes) != 2 || writes[1] != "A\n" {
		t.Fatalf("pty writes after RespondUI() = %#v, want response newline", writes)
	}
}

func TestStubControlMethodsReturnNotFoundForUnknownSession(t *testing.T) {
	svc := newStub(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() })
	unknown, err := session.ParseSessionID("s_404")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	cases := []struct {
		name string
		run  func() error
	}{
		{name: "send", run: func() error {
			_, err := svc.Send(context.Background(), SendRequest{SessionID: unknown, Text: "prompt"})
			return err
		}},
		{name: "enqueue", run: func() error {
			_, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: unknown, Text: "prompt"})
			return err
		}},
		{name: "interrupt", run: func() error {
			_, err := svc.Interrupt(context.Background(), InterruptRequest{SessionID: unknown})
			return err
		}},
		{name: "ui response", run: func() error {
			_, err := svc.RespondUI(context.Background(), UIResponseRequest{SessionID: unknown, ResponseTo: "ask_1", Value: "A"})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertNotFound(t, tc.run())
		})
	}
}

func TestSendAndUIResponseReturnConflictWithoutRuntimeInput(t *testing.T) {
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
	_, err = svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "prompt"})
	assertConflict(t, err)
	if err := svc.SetSessionUIRequest(sessionID, SessionUIRequestSnapshot{RequestID: "ask_1", Kind: "ask_user", Prompt: "Choose"}); err != nil {
		t.Fatalf("SetSessionUIRequest() error = %v", err)
	}
	_, err = svc.RespondUI(context.Background(), UIResponseRequest{SessionID: sessionID, ResponseTo: "ask_1", Value: "A"})
	assertConflict(t, err)
}

func assertConflict(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("err = nil, want conflict")
	}
	var appErr *Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if appErr.Code != "conflict" {
		t.Fatalf("error code = %q, want %q", appErr.Code, "conflict")
	}
}
