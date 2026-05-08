package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

type echoingCodexPTY struct {
	mu       sync.Mutex
	reader   io.Reader
	echoOnce sync.Once
	echo     func()
	writes   []string
}

func (p *echoingCodexPTY) Read(data []byte) (int, error) {
	if p.reader == nil {
		return 0, io.EOF
	}
	return p.reader.Read(data)
}

func (p *echoingCodexPTY) Write(data []byte) (int, error) {
	text := string(data)
	p.mu.Lock()
	p.writes = append(p.writes, text)
	p.mu.Unlock()
	if strings.Contains(text, `"method":"turn/start"`) {
		p.echoOnce.Do(func() {
			if p.echo != nil {
				p.echo()
			}
		})
	}
	return len(data), nil
}

func (p *echoingCodexPTY) Resize(process.PTYSize) error { return nil }
func (p *echoingCodexPTY) Close() error                 { return nil }

func TestCodexEarlyUserMessageEchoDoesNotRaceDuplicateSend(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	pty := &echoingCodexPTY{reader: stdoutR}
	pty.echo = func() {
		_, _ = io.WriteString(stdoutW, `{"method":"item/completed","params":{"threadId":"thread-codex-race","turnId":"turn-codex-race","item":{"type":"userMessage","id":"user-race-1","text":"continue"}}}`+"\n")
	}
	handle := process.NewFakeHandle(process.LaunchSpec{})
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
	if _, err := io.WriteString(stdoutW, `{"id":"initialize-1","result":{"protocolVersion":1}}`+"\n"+`{"method":"thread/started","params":{"thread":{"id":"thread-codex-race"}}}`+"\n"); err != nil {
		t.Fatalf("write codex bootstrap output error = %v", err)
	}
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.RuntimeState == string(codexRuntimePhaseIdle)
	})

	if _, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "continue"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if _, err := io.WriteString(stdoutW, `{"method":"item/completed","params":{"threadId":"thread-codex-race","turnId":"turn-codex-race","item":{"type":"agentMessage","id":"assistant-race-1","text":"done"}}}`+"\n"); err != nil {
		t.Fatalf("write codex assistant output error = %v", err)
	}
	_ = stdoutW.Close()

	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		return err == nil && len(messages.Items) == 2
	})
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	userCount := 0
	for _, item := range messages.Items {
		if item.Role == "user" && item.Text == "continue" {
			userCount++
		}
	}
	if userCount != 1 {
		t.Fatalf("user message count = %d in %+v, want exactly one", userCount, messages.Items)
	}
	if got := fmt.Sprintf("%s:%s", messages.Items[0].Role, messages.Items[1].Role); got != "user:assistant" {
		t.Fatalf("message roles = %s in %+v, want user:assistant", got, messages.Items)
	}
}
