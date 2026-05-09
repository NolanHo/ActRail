package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"actrail/internal/domain/session"
)

func TestSessionMessagesLoadsCodexHistoryFromSessionFile(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_history")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-history","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.547Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"internal instructions"}]}}`,
		`{"timestamp":"2026-05-08T15:58:02.547Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /tmp/codex-history\n\n<INSTRUCTIONS>\nUse repo rules.\n</INSTRUCTIONS><environment_context><cwd>/tmp/codex-history</cwd></environment_context>"}]}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"first prompt"}}`,
		`{"timestamp":"2026-05-08T15:58:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"commentary progress","phase":"commentary"}}`,
		`{"timestamp":"2026-05-08T15:58:10.298Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"commentary progress"}]}}`,
		`{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"final answer","phase":"final_answer"}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewDetachedIdentity(sessionID.String(), session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewDetachedIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-history",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	got := messageRolesAndText(messages.Items)
	want := []string{"system:internal instructions", "system:# AGENTS.md instructions for /tmp/codex-history\n\n<INSTRUCTIONS>\nUse repo rules.\n</INSTRUCTIONS><environment_context><cwd>/tmp/codex-history</cwd></environment_context>", "user:first prompt", "assistant:commentary progress", "assistant:final answer"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
	if messages.TailSeq != 7 {
		t.Fatalf("TailSeq = %d, want 7", messages.TailSeq)
	}
	if messages.Items[0].Kind != "system_prompt" || messages.Items[1].Kind != "system_prompt" {
		t.Fatalf("system prompt kinds = (%q, %q), want system_prompt", messages.Items[0].Kind, messages.Items[1].Kind)
	}
	if messages.Items[3].Details["phase"] != "commentary" || messages.Items[4].Details["phase"] != "final_answer" {
		t.Fatalf("assistant phases = (%v, %v), want commentary/final_answer", messages.Items[3].Details, messages.Items[4].Details)
	}
}

func TestSessionMessagesCodexHistoryUsesSourceLineSeqForIncrementalResume(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_line_seq")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	lines := []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-line-seq","originator":"actrail"}}`,
	}
	for i := 0; i < 260; i++ {
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-05-08T15:58:02.546Z","type":"event_msg","payload":{"type":"token_count","input_tokens":%d}}`, i))
	}
	lines = append(lines,
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"user_message","message":"after restart"}}`,
		`{"timestamp":"2026-05-08T15:58:04.000Z","type":"event_msg","payload":{"type":"agent_message","message":"visible final","phase":"final_answer"}}`,
	)
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, lines)

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewDetachedIdentity(sessionID.String(), session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewDetachedIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-line-seq",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}

	afterSeq := uint64(258)
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, AfterSeq: &afterSeq, Limit: 10})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	got := messageRolesAndText(messages.Items)
	want := []string{"user:after restart", "assistant:visible final"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
	if messages.TailSeq <= afterSeq {
		t.Fatalf("TailSeq = %d, want > %d", messages.TailSeq, afterSeq)
	}
}

func TestCodexThreadBindingStoresSessionFilePath(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_binding")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	sourcePath := writeCodexSessionFile(t, codexHome, threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-binding","originator":"actrail"}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_1", "t_1", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	record, err := svc.registry.Create(sessionCreateSpec{
		Identity: &identity,
		Backend:  session.BackendCodex,
		CWD:      "/tmp/codex-binding",
	})
	if err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}

	svc.rememberCodexThreadBinding(record, threadID)
	updated, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatal("session missing after binding")
	}
	if updated.importedBackendSessionID != threadID || updated.importedSourcePath != filepath.Clean(sourcePath) {
		t.Fatalf("binding = (%q, %q), want (%q, %q)", updated.importedBackendSessionID, updated.importedSourcePath, threadID, filepath.Clean(sourcePath))
	}

	resumeID, err := svc.codexThreadIDForRuntimeRestart(context.Background(), updated)
	if err != nil {
		t.Fatalf("codexThreadIDForRuntimeRestart() error = %v", err)
	}
	if resumeID != threadID {
		t.Fatalf("codexThreadIDForRuntimeRestart() = %q, want %q", resumeID, threadID)
	}
}

func TestCodexThreadBindingBackfillsMissingSessionFilePath(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_binding_backfill")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	sourcePath := writeCodexSessionFile(t, codexHome, threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-binding","originator":"actrail"}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewDetachedIdentity(sessionID.String(), session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewDetachedIdentity() error = %v", err)
	}
	record, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-binding",
		BackendSessionID: threadID,
		SourceConfidence: sourceConfidenceExact,
	})
	if err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}

	svc.rememberCodexThreadBinding(record, threadID)
	updated, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatal("session missing after binding")
	}
	if updated.importedSourcePath != filepath.Clean(sourcePath) {
		t.Fatalf("source path = %q, want %q", updated.importedSourcePath, filepath.Clean(sourcePath))
	}
}

func TestSessionMessagesCodexFinalAnswerClearsPersistedBusyState(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_final_clears_busy")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-final","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"weekly report"}}`,
		`{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"done","phase":"final_answer"}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewDetachedIdentity(sessionID.String(), session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewDetachedIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-final",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}

	if _, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 10}); err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	updated, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatal("session missing after history load")
	}
	if updated.state.Busy() {
		t.Fatal("state.Busy() = true, want false after codex final_answer")
	}
	if svc.isRuntimeAgentRunning(sessionID) {
		t.Fatal("runtimeAgentRunning = true, want false after codex final_answer")
	}
}

func TestSessionMessagesCodexFinalAnswerOverridesLocalTranscript(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_final_overrides_transcript")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-final","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"weekly report"}}`,
		`{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"authoritative done","phase":"final_answer"}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewDetachedIdentity(sessionID.String(), session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewDetachedIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-final",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	if _, err := svc.AppendSessionMessage(sessionID, "assistant", "message", "stale local transcript"); err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	got := messageRolesAndText(messages.Items)
	want := []string{"user:weekly report", "assistant:authoritative done"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
	updated, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatal("session missing after history load")
	}
	if updated.state.Busy() {
		t.Fatal("state.Busy() = true, want false after authoritative final answer")
	}
}

func TestSessionStateCodexFinalAnswerClearsRuntimeAgentRunning(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_state_clears_running")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-state","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"weekly report"}}`,
		`{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"done","phase":"final_answer"}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_state", "t_state", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-state",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)},
		Transport:        SessionTransportSnapshot{State: SessionTransportStateStarting, Reason: "codex_thread_resuming"},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || state.RuntimeState != string(codexRuntimePhaseIdle) {
		t.Fatalf("SessionState() = busy:%v runtime:%q, want idle", state.Busy, state.RuntimeState)
	}
	if svc.isRuntimeAgentRunning(sessionID) {
		t.Fatal("runtimeAgentRunning = true, want false after codex final_answer state reconcile")
	}
}

func TestListSessionsCodexFinalAnswerClearsRuntimeAgentRunning(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_list_clears_running")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-list","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"weekly report"}}`,
		`{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"done","phase":"final_answer"}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_list", "t_list", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-list",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)},
		Transport:        SessionTransportSnapshot{State: SessionTransportStateStarting, Reason: "codex_thread_resuming"},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{AgentBackend: session.BackendCodex.String()})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	if listed.Items[0].Busy || listed.Items[0].RuntimeState != string(codexRuntimePhaseIdle) {
		t.Fatalf("ListSessions().Items[0] = busy:%v runtime:%q, want idle", listed.Items[0].Busy, listed.Items[0].RuntimeState)
	}
}

func writeCodexSessionFile(t *testing.T, codexHome, threadID string, lines []string) string {
	t.Helper()
	path := filepath.Join(codexHome, "sessions", "2026", "05", "08", fmt.Sprintf("rollout-2026-05-08T08-56-55-%s.jsonl", threadID))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return filepath.Clean(path)
}

func messageRolesAndText(items []SessionMessage) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Role+":"+item.Text)
	}
	return out
}
