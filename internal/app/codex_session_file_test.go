package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/process"
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

func TestCodexSessionMessagesFromJSONLLines(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827"}}`,
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"user_message","message":"iod user"}}`,
		`{"timestamp":"2026-05-08T15:58:04.000Z","type":"event_msg","payload":{"type":"agent_message","message":"iod final","phase":"final_answer"}}`,
		`{"timestamp":"2026-05-08T15:58:05.000Z","type":"event_msg","payload":{"type":"task_complete"}}`,
	}

	items, err := codexSessionMessagesFromJSONLLines(context.Background(), "/tmp/session.jsonl", lines)
	if err != nil {
		t.Fatalf("codexSessionMessagesFromJSONLLines() error = %v", err)
	}
	got := messageRolesAndText(items)
	want := []string{"user:iod user", "assistant:iod final"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
	if !codexSessionLinesHaveTaskComplete(context.Background(), lines) {
		t.Fatal("codexSessionLinesHaveTaskComplete() = false, want true")
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

func TestSessionMessagesCodexHistorySummarizesHiddenToolActivity(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_tool_summary")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-tools","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"run command"}}`,
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"false\"}","call_id":"call-false"}}`,
		`{"timestamp":"2026-05-08T15:58:05.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-false","output":"Command: false\nProcess exited with code 1\nOutput:\n"}}`,
		`{"timestamp":"2026-05-08T15:58:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"done","phase":"final_answer"}}`,
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
		CWD:              "/tmp/codex-tools",
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
	want := []string{"user:run command", "assistant:done"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
	summary, ok := messages.Items[1].Details[toolActivitySummaryDetailsKey].(sessionToolActivitySummary)
	if !ok {
		t.Fatalf("assistant details = %+v, want hidden tool activity summary", messages.Items[1].Details)
	}
	if summary.TotalTools != 1 || summary.Failed != 1 || summary.SummaryText != "Ran 1 tool · 1 failed · 7s" {
		t.Fatalf("tool activity summary = %+v, want one failed codex tool", summary)
	}

	withTools, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 10, IncludeToolEvents: true})
	if err != nil {
		t.Fatalf("SessionMessages(include tools) error = %v", err)
	}
	if len(withTools.Items) != 4 || withTools.Items[1].Type != "tool" || withTools.Items[2].Type != "tool_result" || !withTools.Items[2].IsError {
		t.Fatalf("with tools = %+v, want raw codex tool call/result", withTools.Items)
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

func TestSessionStateCodexTaskCompleteClearsRuntimeAgentRunning(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_task_complete_clears_running")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-task-complete","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"weekly report"}}`,
		`{"timestamp":"2026-05-08T15:58:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"still checking","phase":"commentary"}}`,
		`{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-codex-task-complete","last_agent_message":null,"completed_at":1760000350}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_task_complete", "t_task_complete", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtimeState := newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)
	runtimeState.setActiveTurnID("turn-codex-task-complete")
	handle := process.NewFakeHandle(process.LaunchSpec{})
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-task-complete",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: runtimeState, handle: handle},
		Transport:        SessionTransportSnapshot{State: SessionTransportStateAttached, Reason: "codex_replay_failed:replay_failed"},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}
	if _, err := svc.AppendAssistantDelta(sessionID, "turn-codex-task-complete", "stale partial"); err != nil {
		t.Fatalf("AppendAssistantDelta() error = %v", err)
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || state.RuntimeState != string(codexRuntimePhaseIdle) {
		t.Fatalf("SessionState() = busy:%v runtime:%q reason:%q, want idle after task_complete", state.Busy, state.RuntimeState, state.RuntimeStateReason)
	}
	if state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState().PartialAssistantTurn = %+v, want nil after task_complete", state.PartialAssistantTurn)
	}
	if svc.isRuntimeAgentRunning(sessionID) {
		t.Fatal("runtimeAgentRunning = true, want false after codex task_complete state reconcile")
	}
}

func TestSessionStateCodexFinalAnswerDoesNotClearNewerUserTurn(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_newer_user_stays_busy")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-newer-user","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"first prompt"}}`,
		`{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"done","phase":"final_answer"}}`,
		`{"timestamp":"2026-05-08T16:00:10.297Z","type":"event_msg","payload":{"type":"user_message","message":"new prompt"}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_newer_user", "t_newer_user", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtimeState := newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)
	runtimeState.setActiveTurnID("turn-codex-newer-user")
	handle := process.NewFakeHandle(process.LaunchSpec{})
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-newer-user",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: runtimeState, handle: handle},
		Transport:        SessionTransportSnapshot{State: SessionTransportStateAttached, Reason: "codex_thread"},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if !state.Busy || state.RuntimeState != string(codexRuntimePhaseRunning) {
		t.Fatalf("SessionState() = busy:%v runtime:%q, want still running for newer user turn", state.Busy, state.RuntimeState)
	}
}

func TestSessionStateCodexFinalAnswerMirrorsLiveCommits(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_state_live_commits")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-state-live","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"weekly report"}}`,
		`{"timestamp":"2026-05-08T15:58:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"checking","phase":"commentary"}}`,
		`{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"done","phase":"final_answer"}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_state_live", "t_state_live", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-state-live",
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
	if state.Busy || state.TailSeq != 4 {
		t.Fatalf("SessionState() = busy:%v tail:%d, want idle tail 4", state.Busy, state.TailSeq)
	}
	waitForAppCondition(t, func() bool {
		return len(sink.snapshot().commits) == 2
	})
	snapshot := sink.snapshot()
	if len(snapshot.commits) != 2 {
		t.Fatalf("live commits = %+v, want commentary/final", snapshot.commits)
	}
	if snapshot.commits[0].Message.Details["phase"] != "commentary" || snapshot.commits[1].Message.Details["phase"] != "final_answer" {
		t.Fatalf("commit phases = (%v, %v), want commentary/final_answer", snapshot.commits[0].Message.Details, snapshot.commits[1].Message.Details)
	}
	_, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState(second) error = %v", err)
	}
	if len(sink.snapshot().commits) != 2 {
		t.Fatalf("live commits after second state = %d, want no duplicates", len(sink.snapshot().commits))
	}
}

func TestSessionStateCodexFinalAnswerDoesNotBlockOnLiveCommitPublish(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_state_nonblocking")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-state-nonblocking","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"weekly report"}}`,
		`{"timestamp":"2026-05-08T15:58:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"checking","phase":"commentary"}}`,
		`{"timestamp":"2026-05-08T15:59:11.297Z","type":"event_msg","payload":{"type":"agent_message","message":"done","phase":"final_answer"}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	sink := &blockingCommitSink{started: make(chan struct{}), release: make(chan struct{})}
	svc.SetRuntimeEventSink(sink)
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_state_nonblocking", "t_state_nonblocking", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-state-nonblocking",
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
	if state.Busy {
		t.Fatalf("SessionState().Busy = true, want idle even when live commit publishing is blocked")
	}
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("PublishMessageCommit did not start")
	}
	close(sink.release)
}

type blockingCommitSink struct {
	captureRuntimeSink
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (s *blockingCommitSink) PublishMessageCommit(event MessageCommitEvent) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	s.captureRuntimeSink.PublishMessageCommit(event)
}

func TestListSessionsCodexFinalAnswerDoesNotScanSessionFile(t *testing.T) {
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
	if !listed.Items[0].Busy || listed.Items[0].RuntimeState != string(codexRuntimePhaseThreadStarting) {
		t.Fatalf("ListSessions().Items[0] = busy:%v runtime:%q, want cached starting state without session-file scan", listed.Items[0].Busy, listed.Items[0].RuntimeState)
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
