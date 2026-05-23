package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"

	_ "modernc.org/sqlite"
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

func TestRestartCodexSessionIgnoresStaleRuntimeOutputAfterReconcile(t *testing.T) {
	const threadID = "thread-codex-reconcile-1"

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
		return writesContain(second.pty.Writes(), `"method":"thread/resume"`)
	})
	if _, err := io.WriteString(second.stdout, `{"method":"thread/started","params":{"thread":{"id":"`+threadID+`","status":{"type":"idle"}}}}`+"\n"); err != nil {
		t.Fatalf("write second thread started: %v", err)
	}
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy && state.RuntimeState == string(codexRuntimePhaseIdle) && state.Transport.State == SessionTransportStateAttached
	})

	if _, err := io.WriteString(first.stdout, `{"method":"turn/started","params":{"threadId":"`+threadID+`","turn":{"id":"old-turn-after-restart","status":"inProgress","error":null}}}`+"\n"); err != nil {
		t.Fatalf("write stale first runtime event: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || state.RuntimeState != string(codexRuntimePhaseIdle) || state.Transport.State != SessionTransportStateAttached {
		t.Fatalf("SessionState() after stale old runtime output = busy:%v runtime:%q transport:%+v, want idle attached", state.Busy, state.RuntimeState, state.Transport)
	}

	if err := svc.markSessionGenerationBroken(sessionID, iod.GenerationID("g_current_codex_terminal"), "current_generation_failed"); err != nil {
		t.Fatalf("mark current generation broken: %v", err)
	}
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after current broken error = %v", err)
	}
	if state.RuntimeState != string(codexRuntimePhaseFailed) || state.Transport.State != SessionTransportStateBroken {
		t.Fatalf("SessionState() after current generation broken = runtime:%q transport:%+v, want failed broken", state.RuntimeState, state.Transport)
	}
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

func TestCodexResumeCandidatesUseCodexStateDBIndex(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	otherCWD := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(otherCWD, 0o755); err != nil {
		t.Fatalf("mkdir other cwd: %v", err)
	}
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	writeCodexStateDB(t, codexHome, []codexStateDBTestThread{
		{
			ID:               "019e1111-0000-7000-8000-000000000101",
			CWD:              cwd,
			Title:            "Newest indexed title",
			FirstUserMessage: "newest indexed prompt",
			UpdatedMS:        1760000300000,
		},
		{
			ID:               "019e1111-0000-7000-8000-000000000102",
			CWD:              cwd,
			Title:            "Middle indexed title",
			FirstUserMessage: "middle indexed prompt",
			UpdatedMS:        1760000200000,
		},
		{
			ID:               "019e1111-0000-7000-8000-000000000103",
			CWD:              cwd,
			Title:            "Oldest indexed title",
			FirstUserMessage: "oldest indexed prompt",
			UpdatedMS:        1760000100000,
		},
		{
			ID:               "019e1111-0000-7000-8000-000000000104",
			CWD:              otherCWD,
			Title:            "Other cwd title",
			FirstUserMessage: "other cwd prompt",
			UpdatedMS:        1760000400000,
		},
	})

	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	first, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          cwd,
		AgentBackend: "codex",
		ScanOffset:   0,
		ScanLimit:    2,
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates(first) error = %v", err)
	}
	if first.Scanned != 2 || first.ScanRemaining != 1 || first.ScanComplete {
		t.Fatalf("first scan metadata = scanned:%d remaining:%d complete:%v, want 2/1/false", first.Scanned, first.ScanRemaining, first.ScanComplete)
	}
	if got := resumeSessionIDs(first.Sessions); strings.Join(got, ",") != "history:codex:019e1111-0000-7000-8000-000000000101,history:codex:019e1111-0000-7000-8000-000000000102" {
		t.Fatalf("first sessions = %+v, want newest two cwd-indexed sessions", first.Sessions)
	}
	if first.Sessions[0].Title != "Newest indexed title" || first.Sessions[0].FirstUserMessage != "newest indexed prompt" {
		t.Fatalf("first candidate = %+v, want Codex state DB metadata", first.Sessions[0])
	}

	second, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          cwd,
		AgentBackend: "codex",
		ScanOffset:   2,
		ScanLimit:    2,
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates(second) error = %v", err)
	}
	if second.Scanned != 1 || second.ScanRemaining != 0 || !second.ScanComplete {
		t.Fatalf("second scan metadata = scanned:%d remaining:%d complete:%v, want 1/0/true", second.Scanned, second.ScanRemaining, second.ScanComplete)
	}
	if got := resumeSessionIDs(second.Sessions); strings.Join(got, ",") != "history:codex:019e1111-0000-7000-8000-000000000103" {
		t.Fatalf("second sessions = %+v, want oldest cwd-indexed session", second.Sessions)
	}
}

func TestCodexResumeCandidatesUseActRailRename(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	threadID := "019e1111-0000-7000-8000-000000000004"
	sourcePath := writeCodexResumeCandidateFile(t, codexHome, threadID, cwd, "first prompt should not be title", time.Unix(1760000300, 0))
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	record, err := svc.registry.Create(sessionCreateSpec{
		Backend:          session.BackendCodex,
		CWD:              cwd,
		Title:            "Original Title",
		SourcePath:       sourcePath,
		BackendSessionID: threadID,
		SourceConfidence: sourceConfidenceExact,
	})
	if err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	if _, err := svc.RenameSession(context.Background(), RenameSessionRequest{SessionID: record.identity.SessionID(), Name: "Renamed In ActRail"}); err != nil {
		t.Fatalf("RenameSession() error = %v", err)
	}

	resume, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          cwd,
		AgentBackend: "codex",
		ScanOffset:   0,
		ScanLimit:    1,
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	var candidate SessionResumeCandidate
	for _, item := range resume.Sessions {
		if item.SessionID == "history:codex:"+threadID {
			candidate = item
			break
		}
	}
	if candidate.SessionID != "history:codex:"+threadID || candidate.Title != "Renamed In ActRail" || candidate.Alias != "Renamed In ActRail" || candidate.DisplayName != "Renamed In ActRail" {
		t.Fatalf("resume candidate = %+v, want ActRail rename on historical candidate", candidate)
	}
	if candidate.FirstUserMessage != "first prompt should not be title" {
		t.Fatalf("FirstUserMessage = %q", candidate.FirstUserMessage)
	}
}

func TestCodexResumeCandidatesUseCodexSessionIndexName(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	threadID := "019e1111-0000-7000-8000-000000000005"
	writeCodexResumeCandidateFile(t, codexHome, threadID, cwd, "first prompt should not be title", time.Unix(1760000300, 0))
	writeCodexSessionIndex(t, codexHome,
		`{"id":"`+threadID+`","thread_name":"Old Codex Name","updated_at":"2026-05-10T08:00:00Z"}`,
		`{"id":"`+threadID+`","thread_name":"Codex Indexed Rename","updated_at":"2026-05-10T08:01:00Z"}`,
	)
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})

	resume, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          cwd,
		AgentBackend: "codex",
		ScanOffset:   0,
		ScanLimit:    1,
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	if len(resume.Sessions) != 1 {
		t.Fatalf("resume sessions = %+v, want one candidate", resume.Sessions)
	}
	candidate := resume.Sessions[0]
	if candidate.SessionID != "history:codex:"+threadID || candidate.Title != "Codex Indexed Rename" || candidate.Alias != "Codex Indexed Rename" || candidate.DisplayName != "Codex Indexed Rename" {
		t.Fatalf("resume candidate = %+v, want Codex indexed name", candidate)
	}
	if candidate.FirstUserMessage != "first prompt should not be title" {
		t.Fatalf("FirstUserMessage = %q", candidate.FirstUserMessage)
	}
}

func TestCodexResumeCandidatesUseThreadNameUpdatedEvent(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	threadID := "019e1111-0000-7000-8000-000000000006"
	sourcePath := writeCodexResumeCandidateFile(t, codexHome, threadID, cwd, "first prompt should not be title", time.Unix(1760000300, 0))
	file, err := os.OpenFile(sourcePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open source path: %v", err)
	}
	if _, err := file.WriteString(`{"timestamp":"2026-05-10T08:00:02Z","type":"event_msg","payload":{"type":"thread_name_updated","thread_id":"` + threadID + `","thread_name":"Event Rename"}}` + "\n"); err != nil {
		_ = file.Close()
		t.Fatalf("append source path: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close source path: %v", err)
	}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})

	resume, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          cwd,
		AgentBackend: "codex",
		ScanOffset:   0,
		ScanLimit:    1,
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	if len(resume.Sessions) != 1 {
		t.Fatalf("resume sessions = %+v, want one candidate", resume.Sessions)
	}
	candidate := resume.Sessions[0]
	if candidate.SessionID != "history:codex:"+threadID || candidate.Title != "Event Rename" || candidate.Alias != "Event Rename" || candidate.DisplayName != "Event Rename" {
		t.Fatalf("resume candidate = %+v, want thread_name_updated event name", candidate)
	}
}

func TestCodexResumeCandidatePreviewSkipsSyntheticUserContext(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	threadID := "019e1111-0000-7000-8000-000000000007"
	path := filepath.Join(codexHome, "sessions", "2026", "05", "10", "rollout-2026-05-10T01-02-03-"+threadID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir codex session dir: %v", err)
	}
	body := `{"timestamp":"2026-05-10T08:00:00Z","type":"session_meta","payload":{"id":"` + threadID + `","cwd":` + quoteJSON(cwd) + `}}` + "\n" +
		`{"timestamp":"2026-05-10T08:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /root\n\n<INSTRUCTIONS>@RTK.md</INSTRUCTIONS>"}]}}` + "\n" +
		`{"timestamp":"2026-05-10T08:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/root</cwd>\n</environment_context>"}]}}` + "\n" +
		`{"timestamp":"2026-05-10T08:00:03Z","type":"event_msg","payload":{"type":"user_message","message":"real prompt for resume preview"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex session file: %v", err)
	}
	if err := os.Chtimes(path, time.Unix(1760000300, 0), time.Unix(1760000300, 0)); err != nil {
		t.Fatalf("chtimes codex session file: %v", err)
	}

	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	resume, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          cwd,
		AgentBackend: "codex",
		ScanOffset:   0,
		ScanLimit:    1,
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	if len(resume.Sessions) != 1 {
		t.Fatalf("resume sessions = %+v, want one candidate", resume.Sessions)
	}
	if got := resume.Sessions[0].FirstUserMessage; got != "real prompt for resume preview" {
		t.Fatalf("FirstUserMessage = %q, want real user prompt", got)
	}
}

type codexStateDBTestThread struct {
	ID               string
	CWD              string
	Title            string
	FirstUserMessage string
	UpdatedMS        int64
	RolloutPath      string
}

func writeCodexStateDB(t *testing.T, codexHome string, rows []codexStateDBTestThread) {
	t.Helper()
	path := filepath.Join(codexHome, "state_6.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open codex state db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY,
		rollout_path TEXT NOT NULL,
		created_at INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT 0,
		source TEXT NOT NULL DEFAULT '',
		model_provider TEXT NOT NULL DEFAULT '',
		cwd TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		sandbox_policy TEXT NOT NULL DEFAULT '',
		approval_mode TEXT NOT NULL DEFAULT '',
		tokens_used INTEGER NOT NULL DEFAULT 0,
		has_user_event INTEGER NOT NULL DEFAULT 0,
		archived INTEGER NOT NULL DEFAULT 0,
		archived_at INTEGER,
		git_sha TEXT,
		git_branch TEXT,
		cli_version TEXT NOT NULL DEFAULT '',
		first_user_message TEXT NOT NULL DEFAULT '',
		model TEXT,
		reasoning_effort TEXT,
		created_at_ms INTEGER,
		updated_at_ms INTEGER
	)`); err != nil {
		t.Fatalf("create codex state threads table: %v", err)
	}
	for _, row := range rows {
		rolloutPath := row.RolloutPath
		if strings.TrimSpace(rolloutPath) == "" {
			rolloutPath = filepath.Join(codexHome, "sessions", "2026", "05", "10", "rollout-2026-05-10T01-02-03-"+row.ID+".jsonl")
		}
		if _, err := db.Exec(`INSERT INTO threads (
			id, rollout_path, cwd, title, first_user_message, updated_at_ms, created_at_ms, archived
		) VALUES (?, ?, ?, ?, ?, ?, ?, 0)`,
			row.ID,
			rolloutPath,
			row.CWD,
			row.Title,
			row.FirstUserMessage,
			row.UpdatedMS,
			row.UpdatedMS,
		); err != nil {
			t.Fatalf("insert codex state thread: %v", err)
		}
	}
}

func resumeSessionIDs(items []SessionResumeCandidate) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.SessionID)
	}
	return ids
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

func writeCodexSessionIndex(t *testing.T, codexHome string, lines ...string) {
	t.Helper()
	path := filepath.Join(codexHome, "session_index.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex session index: %v", err)
	}
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
