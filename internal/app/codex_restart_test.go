package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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

func TestCreateCodexSessionFromLiveResumeReusesExistingSession(t *testing.T) {
	const threadID = "thread-codex-live-resume-1"

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

	resumeID := created.Session.SessionID
	resumed, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/code/ActRail", ResumeSessionID: &resumeID})
	if err != nil {
		t.Fatalf("CreateSession(resume codex) error = %v", err)
	}
	if resumed.Session.SessionID != created.Session.SessionID {
		t.Fatalf("resumed session id = %q, want existing %q", resumed.Session.SessionID, created.Session.SessionID)
	}
	runtimesMu.Lock()
	runtimeCount := len(runtimes)
	runtimesMu.Unlock()
	if runtimeCount != 1 {
		t.Fatalf("runtime count = %d, want existing runtime only", runtimeCount)
	}
	_ = first.stdout.Close()
}

func TestCreateCodexSessionFromHistoricalResumeUsesSessionFileThread(t *testing.T) {
	const threadID = "019e0500-1111-7222-8333-444455556666"
	cwd := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	sessionDir := filepath.Join(codexHome, "sessions", "2026", "05", "10")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir codex session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "rollout-2026-05-10T01-02-03-"+threadID+".jsonl")
	body := `{"timestamp":"2026-05-10T08:00:00Z","type":"session_meta","payload":{"id":"` + threadID + `","cwd":"` + cwd + `"}}` + "\n" +
		`{"timestamp":"2026-05-10T08:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"resume codex please"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex session file: %v", err)
	}

	runtimes := make([]codexTestRuntimeIO, 0, 1)
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
	candidates, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{CWD: cwd, AgentBackend: "codex"})
	if err != nil {
		t.Fatalf("SessionResumeCandidates(codex) error = %v", err)
	}
	if len(candidates.Sessions) != 1 || candidates.Sessions[0].SessionID != "history:codex:"+threadID || candidates.Sessions[0].FirstUserMessage != "resume codex please" {
		t.Fatalf("SessionResumeCandidates(codex) = %+v, want historical codex candidate", candidates)
	}

	resumeID := candidates.Sessions[0].SessionID
	if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: cwd, ResumeSessionID: &resumeID}); err != nil {
		t.Fatalf("CreateSession(historical codex resume) error = %v", err)
	}
	runtime := waitForCodexTestRuntime(t, &runtimesMu, &runtimes, 0)
	waitForAppCondition(t, func() bool {
		return writesContain(runtime.pty.Writes(), `"method":"initialize"`)
	})
	if _, err := io.WriteString(runtime.stdout, `{"id":"initialize-1","result":{"protocolVersion":1}}`+"\n"); err != nil {
		t.Fatalf("write initialize response: %v", err)
	}
	waitForAppCondition(t, func() bool {
		writes := runtime.pty.Writes()
		return writesContain(writes, `"method":"thread/resume"`) && writesContain(writes, `"threadId":"`+threadID+`"`)
	})
	if writesContain(runtime.pty.Writes(), `"method":"thread/start"`) {
		t.Fatalf("runtime writes = %q, want no new thread/start", strings.Join(runtime.pty.Writes(), "\n"))
	}
	_ = runtime.stdout.Close()
}

func TestCodexResumeCandidatesScanOnlyRequestedBatch(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	newest := "019e1111-0000-7000-8000-000000000001"
	middle := "019e1111-0000-7000-8000-000000000002"
	oldest := "019e1111-0000-7000-8000-000000000003"
	writeCodexResumeCandidateFile(t, codexHome, newest, cwd, "newest prompt", time.Unix(1760000300, 0))
	writeCodexResumeCandidateFile(t, codexHome, middle, cwd, "middle prompt", time.Unix(1760000200, 0))
	writeCodexResumeCandidateFile(t, codexHome, oldest, cwd, "oldest prompt", time.Unix(1760000100, 0))

	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	first, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          cwd,
		AgentBackend: "codex",
		ScanOffset:   0,
		ScanLimit:    1,
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates(first) error = %v", err)
	}
	if first.Scanned != 1 || first.ScanRemaining != 2 || first.ScanComplete {
		t.Fatalf("first scan metadata = scanned:%d remaining:%d complete:%v, want 1/2/false", first.Scanned, first.ScanRemaining, first.ScanComplete)
	}
	if len(first.Sessions) != 1 || first.Sessions[0].SessionID != "history:codex:"+newest || first.Sessions[0].FirstUserMessage != "newest prompt" {
		t.Fatalf("first scan sessions = %+v, want newest only", first.Sessions)
	}

	second, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          cwd,
		AgentBackend: "codex",
		ScanOffset:   1,
		ScanLimit:    1,
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates(second) error = %v", err)
	}
	if second.Scanned != 1 || second.ScanRemaining != 1 || second.ScanComplete {
		t.Fatalf("second scan metadata = scanned:%d remaining:%d complete:%v, want 1/1/false", second.Scanned, second.ScanRemaining, second.ScanComplete)
	}
	if len(second.Sessions) != 1 || second.Sessions[0].SessionID != "history:codex:"+middle || second.Sessions[0].FirstUserMessage != "middle prompt" {
		t.Fatalf("second scan sessions = %+v, want middle only", second.Sessions)
	}
}

func writeCodexResumeCandidateFile(t *testing.T, codexHome, threadID, cwd, prompt string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(codexHome, "sessions", "2026", "05", "10", "rollout-2026-05-10T01-02-03-"+threadID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir codex session dir: %v", err)
	}
	body := `{"timestamp":"2026-05-10T08:00:00Z","type":"session_meta","payload":{"id":"` + threadID + `","cwd":` + quoteJSON(cwd) + `}}` + "\n" +
		`{"timestamp":"2026-05-10T08:00:01Z","type":"event_msg","payload":{"type":"user_message","message":` + quoteJSON(prompt) + `}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex session file: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes codex session file: %v", err)
	}
	return filepath.Clean(path)
}

func quoteJSON(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
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
