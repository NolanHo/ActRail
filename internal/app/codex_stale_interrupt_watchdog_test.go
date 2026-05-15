package app

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func TestCodexStaleInterruptWatchdogRestartsRuntime(t *testing.T) {
	restore := setCodexStaleInterruptWatchDelayForTest(20 * time.Millisecond)
	defer restore()

	svc, handles, sessionID, pty, writer := newCodexStaleInterruptWatchdogFixture(t)
	settleCodexStaleInterruptWatchdogRuntime(t, svc, sessionID, pty, writer)
	runtime := stageStaleCodexInterrupt(t, svc, sessionID)
	if _, _, _, ok := runtime.codex.interruptWatchSnapshot(); !ok {
		t.Fatal("interruptWatchSnapshot() ok = false before watchdog, want true")
	}

	svc.startCodexStaleInterruptWatch(sessionID, runtime)

	waitForAppCondition(t, func() bool {
		return len(*handles) == 2
	})
	if (*handles)[0].KillCalls() != 1 {
		t.Fatalf("old handle KillCalls() = %d, want 1", (*handles)[0].KillCalls())
	}
	if (*handles)[1].KillCalls() != 0 {
		t.Fatalf("new handle KillCalls() = %d, want 0", (*handles)[1].KillCalls())
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if !state.Busy || state.RuntimeState != string(codexRuntimePhaseInitializing) {
		t.Fatalf("SessionState() after watchdog = %+v, want initializing busy", state)
	}
}

func TestCodexStaleInterruptWatchdogSkipsWhenTurnProgresses(t *testing.T) {
	restore := setCodexStaleInterruptWatchDelayForTest(20 * time.Millisecond)
	defer restore()

	svc, handles, sessionID, pty, writer := newCodexStaleInterruptWatchdogFixture(t)
	settleCodexStaleInterruptWatchdogRuntime(t, svc, sessionID, pty, writer)
	runtime := stageStaleCodexInterrupt(t, svc, sessionID)

	svc.startCodexStaleInterruptWatch(sessionID, runtime)
	runtime.codex.markProgress()
	time.Sleep(80 * time.Millisecond)

	if len(*handles) != 1 {
		t.Fatalf("len(handles) = %d, want 1 after progress", len(*handles))
	}
	if (*handles)[0].KillCalls() != 0 {
		t.Fatalf("old handle KillCalls() = %d, want 0 after progress", (*handles)[0].KillCalls())
	}
}

func stageStaleCodexInterrupt(t *testing.T, svc *Stub, sessionID session.SessionID) sessionRuntime {
	t.Helper()
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	record.runtime = svc.runtimeForRecord(record)
	if record.runtime.codex == nil {
		t.Fatal("runtime.codex = nil, want codex runtime state")
	}
	accepted, _ := record.runtime.codex.setThreadID("thread-stale-interrupt")
	if !accepted {
		t.Fatal("setThreadID(thread-stale-interrupt) = false, want true")
	}
	record.runtime.codex.setActiveTurnID("turn-stale-interrupt")
	record.runtime.codex.requestInterrupt()
	record.runtime.codex.markInterruptSent("turn-stale-interrupt")
	if _, err := svc.setSessionTransport(sessionID, transportSnapshotCodexAttached()); err != nil {
		t.Fatalf("setSessionTransport() error = %v", err)
	}
	if err := svc.syncCodexRuntimeActivity(sessionID, "test_stale_interrupt", true); err != nil {
		t.Fatalf("syncCodexRuntimeActivity() error = %v", err)
	}
	return record.runtime
}

func newCodexStaleInterruptWatchdogFixture(t *testing.T) (*Stub, *[]*process.FakeHandle, session.SessionID, *recordingPTY, *io.PipeWriter) {
	t.Helper()
	handles := &[]*process.FakeHandle{}
	ptys := []*recordingPTY{}
	writers := []*io.PipeWriter{}
	runner := &process.FakeRunner{HandleBuild: func(spec process.LaunchSpec) process.Handle {
		reader, writer := io.Pipe()
		t.Cleanup(func() {
			_ = writer.Close()
			_ = reader.Close()
		})
		handle := process.NewFakeHandle(spec)
		handle.SetPID(321 + len(*handles))
		pty := &recordingPTY{reader: reader}
		handle.SetPTY(pty)
		*handles = append(*handles, handle)
		ptys = append(ptys, pty)
		writers = append(writers, writer)
		return handle
	}}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	return svc, handles, mustSessionID(t, created.Session.SessionID), ptys[0], writers[0]
}

func settleCodexStaleInterruptWatchdogRuntime(t *testing.T, svc *Stub, sessionID session.SessionID, pty *recordingPTY, writer *io.PipeWriter) {
	t.Helper()
	waitForAppCondition(t, func() bool {
		return containsRuntimeWrite(pty, `"method":"initialize"`)
	})
	_, _ = writer.Write([]byte("{\"id\":\"initialize-1\",\"result\":{\"userAgent\":\"actrail-test\"}}\n"))
	waitForAppCondition(t, func() bool {
		return containsRuntimeWrite(pty, `"method":"thread/start"`)
	})
	_, _ = writer.Write([]byte("{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-stale-interrupt\",\"status\":{\"type\":\"idle\"}}}}\n"))
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy && state.RuntimeState == string(codexRuntimePhaseIdle)
	})
}

func containsRuntimeWrite(pty *recordingPTY, needle string) bool {
	for _, write := range pty.Writes() {
		if strings.Contains(write, needle) {
			return true
		}
	}
	return false
}

func setCodexStaleInterruptWatchDelayForTest(delay time.Duration) func() {
	previous := codexStaleInterruptWatchDelay
	codexStaleInterruptWatchDelay = delay
	return func() {
		codexStaleInterruptWatchDelay = previous
	}
}
