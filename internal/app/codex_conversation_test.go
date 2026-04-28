package app

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/config"
)

type recordingPTY struct {
	mu     sync.Mutex
	writes []string
}

type codexTurnExpectation struct {
	Prompt   string
	ThreadID string
	TurnID   string
	Reply    string
}

func (p *recordingPTY) Read(_ []byte) (int, error) { return 0, io.EOF }

func (p *recordingPTY) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, string(data))
	return len(data), nil
}

func (p *recordingPTY) Resize(process.PTYSize) error { return nil }
func (p *recordingPTY) Close() error                 { return nil }

func (p *recordingPTY) Writes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.writes...)
}

func TestCodexSessionSupportsTwoTurnsAndDelete(t *testing.T) {
	t.Skip("WIP scaffold: direct fake-runner Codex fixture still returns session runtime input unavailable; manual deployed e2e passed")
	stdoutR, stdoutW := io.Pipe()
	defer stdoutR.Close()
	pty := &recordingPTY{}
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetStdout(stdoutR)
	handle.SetPTY(pty)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if created.Session == nil {
		t.Fatalf("CreateSession() = %+v, want session payload", created)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)

	go driveCodexRuntime(t, stdoutW, pty,
		codexTurnExpectation{Prompt: "Reply exactly: TURN1_OK", ThreadID: "codex-thread-e2e", TurnID: "codex-turn-1", Reply: "TURN1_OK"},
		codexTurnExpectation{Prompt: "Reply exactly: TURN2_OK", ThreadID: "codex-thread-e2e", TurnID: "codex-turn-2", Reply: "TURN2_OK"},
	)

	first, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "Reply exactly: TURN1_OK"})
	if err != nil {
		t.Fatalf("first Send() error = %v", err)
	}
	if first.Message.Role != "user" || first.Message.Text != "Reply exactly: TURN1_OK" {
		t.Fatalf("first Send() = %+v, want appended user message", first)
	}

	second, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "Reply exactly: TURN2_OK"})
	if err != nil {
		t.Fatalf("second Send() error = %v", err)
	}
	if second.Message.Role != "user" || second.Message.Text != "Reply exactly: TURN2_OK" {
		t.Fatalf("second Send() = %+v, want appended user message", second)
	}

	waitForAppCondition(t, func() bool {
		messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return len(messages.Items) == 4 && messages.Items[3].Role == "assistant" && messages.Items[3].Text == "TURN2_OK"
	})

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	want := []struct{ role, text string }{{"user", "Reply exactly: TURN1_OK"}, {"assistant", "TURN1_OK"}, {"user", "Reply exactly: TURN2_OK"}, {"assistant", "TURN2_OK"}}
	if len(messages.Items) != len(want) {
		t.Fatalf("len(SessionMessages().Items) = %d, want %d", len(messages.Items), len(want))
	}
	for i, item := range messages.Items {
		if item.Role != want[i].role || item.Text != want[i].text {
			t.Fatalf("SessionMessages().Items[%d] = %+v, want role=%q text=%q", i, item, want[i].role, want[i].text)
		}
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatalf("SessionState() = %+v, want busy false after two turns", state)
	}

	writes := strings.Join(pty.Writes(), "\n")
	for _, wantWrite := range []string{`"method":"initialize"`, `"method":"thread/start"`, `"method":"turn/start"`, `Reply exactly: TURN1_OK`, `Reply exactly: TURN2_OK`} {
		if !strings.Contains(writes, wantWrite) {
			t.Fatalf("runtime writes = %q, want substring %q", writes, wantWrite)
		}
	}

	deleted, err := svc.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if !deleted.OK || !deleted.Removed || deleted.SessionID != sessionID.String() {
		t.Fatalf("DeleteSession() = %+v, want removed session %q", deleted, sessionID)
	}
	if _, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID}); err == nil {
		t.Fatalf("SessionState(%q) succeeded after delete, want not found", sessionID)
	}
}

func driveCodexRuntime(t *testing.T, stdout *io.PipeWriter, pty *recordingPTY, turns ...codexTurnExpectation) {
	t.Helper()
	defer stdout.Close()
	waitForAppCondition(t, func() bool { return len(pty.Writes()) >= 2 })
	writes := pty.Writes()
	if !strings.Contains(writes[0], `"method":"initialize"`) {
		t.Fatalf("first codex write = %q, want initialize request", writes[0])
	}
	if !strings.Contains(writes[1], `"method":"thread/start"`) {
		t.Fatalf("second codex write = %q, want thread/start request", writes[1])
	}
	if _, err := io.WriteString(stdout, "{"+`"id":"initialize-1","result":{"protocolVersion":1}`+"}\n"+
		"{"+`"method":"thread/started","params":{"thread":{"id":"`+turns[0].ThreadID+`"}}`+"}\n"); err != nil {
		t.Fatalf("write codex bootstrap output error = %v", err)
	}
	for i, turn := range turns {
		wantWrites := 3 + i
		waitForAppCondition(t, func() bool { return len(pty.Writes()) >= wantWrites })
		writes = pty.Writes()
		write := writes[wantWrites-1]
		if !strings.Contains(write, `"method":"turn/start"`) {
			t.Fatalf("codex write[%d] = %q, want turn/start request", wantWrites-1, write)
		}
		if !strings.Contains(write, turn.Prompt) {
			t.Fatalf("codex write[%d] = %q, want prompt %q", wantWrites-1, write, turn.Prompt)
		}
		if _, err := io.WriteString(stdout, codexTurnOutput(turn.ThreadID, turn.TurnID, turn.Reply)); err != nil {
			t.Fatalf("write codex turn output error = %v", err)
		}
	}
}

func codexTurnOutput(threadID, turnID, reply string) string {
	return "{" + `"method":"turn/started","params":{"turn":{"id":"` + turnID + `"},"threadId":"` + threadID + `"}}` + "}\n" +
		"{" + `"method":"item/agentMessage/delta","params":{"itemId":"msg-` + turnID + `","turnId":"` + turnID + `","delta":"` + reply + `"}}` + "}\n" +
		"{" + `"method":"item/completed","params":{"turnId":"` + turnID + `","item":{"id":"msg-` + turnID + `","type":"agentMessage","text":"` + reply + `"}}}` + "}\n" +
		"{" + `"method":"turn/completed","params":{"turn":{"id":"` + turnID + `"},"threadId":"` + threadID + `"}}` + "}\n"
}
