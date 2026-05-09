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

type codexTestRuntimeIO struct {
	pty    *recordingPTY
	stdout *io.PipeWriter
}

func TestRestartCodexSessionResumesExistingThread(t *testing.T) {
	const threadID = "thread-codex-resume-1"

	runtimes := make([]codexTestRuntimeIO, 0, 2)
	var runtimesMu sync.Mutex
	runner := &process.FakeRunner{
		HandleBuild: func(spec process.LaunchSpec) process.Handle {
			stdoutR, stdoutW := io.Pipe()
			pty := &recordingPTY{reader: stdoutR}
			handle := process.NewFakeHandle(spec)
			handle.SetPTY(pty)
			runtimesMu.Lock()
			runtimes = append(runtimes, codexTestRuntimeIO{pty: pty, stdout: stdoutW})
			runtimesMu.Unlock()
			return handle
		},
	}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)

	first := waitForCodexTestRuntime(t, &runtimesMu, &runtimes, 0)
	waitForAppCondition(t, func() bool {
		return writesContain(first.pty.Writes(), `"method":"initialize"`)
	})
	if _, err := io.WriteString(first.stdout, `{"id":"initialize-1","result":{"protocolVersion":1}}`+"\n"); err != nil {
		t.Fatalf("write first initialize response: %v", err)
	}
	waitForAppCondition(t, func() bool {
		return writesContain(first.pty.Writes(), `"method":"thread/start"`)
	})
	if _, err := io.WriteString(first.stdout, `{"method":"thread/started","params":{"thread":{"id":"`+threadID+`","status":{"type":"idle"}}}}`+"\n"); err != nil {
		t.Fatalf("write first thread started: %v", err)
	}
	waitForAppCondition(t, func() bool {
		record, ok := svc.registry.Lookup(sessionID)
		return ok && record.importedBackendSessionID == threadID
	})

	if _, err := svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: sessionID}); err != nil {
		t.Fatalf("RestartSession() error = %v", err)
	}

	second := waitForCodexTestRuntime(t, &runtimesMu, &runtimes, 1)
	waitForAppCondition(t, func() bool {
		return writesContain(second.pty.Writes(), `"method":"initialize"`)
	})
	if _, err := io.WriteString(second.stdout, `{"id":"initialize-1","result":{"protocolVersion":1}}`+"\n"); err != nil {
		t.Fatalf("write second initialize response: %v", err)
	}
	waitForAppCondition(t, func() bool {
		writes := second.pty.Writes()
		return writesContain(writes, `"method":"thread/resume"`) && writesContain(writes, `"threadId":"`+threadID+`"`)
	})
	if _, err := io.WriteString(second.stdout, `{"id":"thread-resume-2","result":{"thread":{"id":"`+threadID+`","status":{"type":"idle"}}}}`+"\n"); err != nil {
		t.Fatalf("write second thread resume response: %v", err)
	}
	if writesContain(second.pty.Writes(), `"method":"thread/start"`) {
		t.Fatalf("second runtime writes = %q, want no new thread/start", strings.Join(second.pty.Writes(), "\n"))
	}
	if _, err := io.WriteString(second.stdout, `{"method":"thread/started","params":{"thread":{"id":"`+threadID+`","status":{"type":"idle"}}}}`+"\n"); err != nil {
		t.Fatalf("write second thread started: %v", err)
	}

	if _, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "继续"}); err != nil {
		t.Fatalf("Send() after restart error = %v", err)
	}
	waitForAppCondition(t, func() bool {
		writes := second.pty.Writes()
		return writesContain(writes, `"method":"turn/start"`) && writesContain(writes, `"threadId":"`+threadID+`"`) && writesContain(writes, "继续")
	})
	_ = first.stdout.Close()
	_ = second.stdout.Close()
}

func waitForCodexTestRuntime(t *testing.T, mu *sync.Mutex, runtimes *[]codexTestRuntimeIO, index int) codexTestRuntimeIO {
	t.Helper()
	var runtime codexTestRuntimeIO
	waitForAppCondition(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		if len(*runtimes) <= index {
			return false
		}
		runtime = (*runtimes)[index]
		return true
	})
	return runtime
}

func writesContain(writes []string, needle string) bool {
	for _, write := range writes {
		if strings.Contains(write, needle) {
			return true
		}
	}
	return false
}
