package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/config"
	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"

	_ "modernc.org/sqlite"
)

func TestCodexSessionFilesListsAllAndCWDFromStateDB(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	cwd := filepath.Join(t.TempDir(), "workspace")
	otherCWD := filepath.Join(t.TempDir(), "other")
	threadOne := "019e1111-0000-7000-8000-000000000101"
	threadTwo := "019e1111-0000-7000-8000-000000000102"
	writeCodexStateDB(t, codexHome, []codexStateDBTestThread{
		{ID: threadOne, CWD: cwd, Title: "Indexed One", FirstUserMessage: "first prompt", UpdatedMS: 1760000300000},
		{ID: threadTwo, CWD: otherCWD, Title: "Indexed Two", FirstUserMessage: "other prompt", UpdatedMS: 1760000200000},
	})
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})

	all, err := svc.CodexSessionFiles(context.Background(), CodexSessionFilesRequest{Scope: "all", Limit: 10})
	if err != nil {
		t.Fatalf("CodexSessionFiles(all) error = %v", err)
	}
	if len(all.Items) != 2 || all.Items[0].ThreadID != threadOne || all.Items[0].Title != "Indexed One" || all.Items[0].Source != "state_db" {
		t.Fatalf("all items = %+v", all.Items)
	}

	filtered, err := svc.CodexSessionFiles(context.Background(), CodexSessionFilesRequest{Scope: "cwd", CWD: cwd, Limit: 10})
	if err != nil {
		t.Fatalf("CodexSessionFiles(cwd) error = %v", err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].ThreadID != threadOne {
		t.Fatalf("cwd items = %+v", filtered.Items)
	}
}

func TestCodexSessionFileDetailGroupsUserAssistantTurns(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	cwd := filepath.Join(t.TempDir(), "workspace")
	threadID := "019e1111-0000-7000-8000-000000000201"
	sourcePath := writeCodexSessionFile(t, codexHome, threadID, []string{
		`{"timestamp":"2026-05-10T08:00:00Z","type":"session_meta","payload":{"id":"` + threadID + `","cwd":` + quoteJSON(cwd) + `}}`,
		`{"timestamp":"2026-05-10T08:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"first prompt"}}`,
		`{"timestamp":"2026-05-10T08:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"first answer","phase":"final_answer"}}`,
		`{"timestamp":"2026-05-10T08:00:03Z","type":"event_msg","payload":{"type":"user_message","message":"second prompt"}}`,
	})
	writeCodexStateDB(t, codexHome, []codexStateDBTestThread{
		{ID: threadID, CWD: cwd, Title: "Grouped Thread", FirstUserMessage: "first prompt", UpdatedMS: 1760000300000, RolloutPath: sourcePath},
	})
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})

	detail, err := svc.CodexSessionFile(context.Background(), CodexSessionFileRequest{ThreadID: threadID})
	if err != nil {
		t.Fatalf("CodexSessionFile() error = %v", err)
	}
	if detail.Summary.Path != sourcePath || detail.Summary.Title != "Grouped Thread" {
		t.Fatalf("summary = %+v, want path %q and title", detail.Summary, sourcePath)
	}
	if got := messageRolesAndText(detail.Items); strings.Join(got, "\n") != "user:first prompt\nassistant:first answer\nuser:second prompt" {
		t.Fatalf("items = %#v", got)
	}
	if len(detail.Turns) != 2 || detail.Turns[0].User == nil || detail.Turns[0].Assistant == nil || detail.Turns[1].User == nil {
		t.Fatalf("turns = %+v", detail.Turns)
	}
}

func TestCodexSessionFileDetailReturnsEmptyForMissingStateDBRollout(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	cwd := filepath.Join(t.TempDir(), "workspace")
	threadID := "019e1111-0000-7000-8000-000000000211"
	missingPath := filepath.Join(codexHome, "sessions", "2026", "05", "08", "rollout-missing-"+threadID+".jsonl")
	writeCodexStateDB(t, codexHome, []codexStateDBTestThread{
		{ID: threadID, CWD: cwd, Title: "Missing Rollout", FirstUserMessage: "first prompt", UpdatedMS: 1760000300000, RolloutPath: missingPath},
	})
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})

	detail, err := svc.CodexSessionFile(context.Background(), CodexSessionFileRequest{ThreadID: threadID})
	if err != nil {
		t.Fatalf("CodexSessionFile() error = %v", err)
	}
	if !detail.OK || len(detail.Items) != 0 || len(detail.Turns) != 0 || detail.TailSeq != 0 {
		t.Fatalf("detail = %+v, want empty successful response", detail)
	}
	if detail.Summary.ThreadID != threadID || detail.Summary.Title != "Missing Rollout" || detail.Summary.Path != "" {
		t.Fatalf("summary = %+v, want state db metadata without missing path", detail.Summary)
	}
}

func TestCodexSessionFileDetailReturnsEmptyForZeroByteRollout(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	cwd := filepath.Join(t.TempDir(), "workspace")
	threadID := "019e1111-0000-7000-8000-000000000212"
	sourcePath := filepath.Join(codexHome, "sessions", "2026", "05", "08", "rollout-empty-"+threadID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(sourcePath, nil, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	writeCodexStateDB(t, codexHome, []codexStateDBTestThread{
		{ID: threadID, CWD: cwd, Title: "Empty Rollout", FirstUserMessage: "first prompt", UpdatedMS: 1760000300000, RolloutPath: sourcePath},
	})
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})

	detail, err := svc.CodexSessionFile(context.Background(), CodexSessionFileRequest{ThreadID: threadID})
	if err != nil {
		t.Fatalf("CodexSessionFile() error = %v", err)
	}
	if !detail.OK || len(detail.Items) != 0 || len(detail.Turns) != 0 || detail.Summary.Path != filepath.Clean(sourcePath) {
		t.Fatalf("detail = %+v, want empty successful response with source path", detail)
	}
}

func TestRenameCodexSessionFileWritesCodexMetadataAndActRailRecord(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	cwd := filepath.Join(t.TempDir(), "workspace")
	threadID := "019e1111-0000-7000-8000-000000000301"
	sourcePath := writeCodexResumeCandidateFile(t, codexHome, threadID, cwd, "first prompt", time.Unix(1760000300, 0))
	writeCodexStateDB(t, codexHome, []codexStateDBTestThread{
		{ID: threadID, CWD: cwd, Title: "Before Rename", FirstUserMessage: "first prompt", UpdatedMS: 1760000300000},
	})
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	record, err := svc.registry.Create(sessionCreateSpec{
		Backend:          session.BackendCodex,
		CWD:              cwd,
		Title:            "Before Rename",
		SourcePath:       sourcePath,
		BackendSessionID: threadID,
		SourceConfidence: sourceConfidenceExact,
	})
	if err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}

	renamed, err := svc.RenameCodexSessionFile(context.Background(), RenameCodexSessionFileRequest{ThreadID: threadID, Name: "After Rename"})
	if err != nil {
		t.Fatalf("RenameCodexSessionFile() error = %v", err)
	}
	if !renamed.OK || renamed.Summary.Title != "After Rename" {
		t.Fatalf("renamed = %+v", renamed)
	}
	if got := readCodexStateDBTitle(t, codexHome, threadID); got != "After Rename" {
		t.Fatalf("state db title = %q", got)
	}
	if got := codexThreadNameFromIndex(threadID); got != "After Rename" {
		t.Fatalf("session index name = %q", got)
	}
	updated, ok := svc.registry.Lookup(record.identity.SessionID())
	if !ok || updated.title != "After Rename" || updated.alias != "After Rename" {
		t.Fatalf("updated record = %+v ok=%v", updated, ok)
	}
}

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
	attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)

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

func TestSessionMessagesLoadsCodexHistoryFromSourcePathWithoutHelper(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_no_helper")
	threadID := "019e2107-ca2f-7e73-994d-8726965f8c8b"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-13T04:10:19.000Z","type":"session_meta","payload":{"id":"019e2107-ca2f-7e73-994d-8726965f8c8b","cwd":"/tmp/codex-history","originator":"actrail"}}`,
		`{"timestamp":"2026-05-13T04:10:20.000Z","type":"event_msg","payload":{"type":"user_message","message":"resume kv store"}}`,
		`{"timestamp":"2026-05-13T04:11:20.000Z","type":"event_msg","payload":{"type":"agent_message","message":"working from source file","phase":"commentary"}}`,
		`{"timestamp":"2026-05-13T04:12:20.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call_pending","arguments":"{}"}}`,
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
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 2})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	got := messageRolesAndText(messages.Items)
	want := []string{"user:resume kv store", "assistant:working from source file"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
	updated, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatal("session missing after history load")
	}
	if !updated.state.Busy() {
		t.Fatal("state.Busy() = false, want true for incomplete source history")
	}
	if !svc.isRuntimeAgentRunning(sessionID) {
		t.Fatal("runtimeAgentRunning = false, want true for incomplete source history")
	}
}

func TestSessionMessagesCodexSourcePathDoesNotBlockOnSlowHelper(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_fast_helper")
	generationID := mustHelperGenerationID(t, "g_codex_file_fast_helper")
	threadID := "019e2107-ca2f-7e73-994d-8726965f8c8c"
	lines := makeLargeCodexSessionLines(threadID, "/tmp/codex-fast")
	lines = append(lines,
		`{"timestamp":"2026-05-13T04:10:19.000Z","type":"session_meta","payload":{"id":"019e2107-ca2f-7e73-994d-8726965f8c8c","cwd":"/tmp/codex-fast","originator":"actrail"}}`,
		`{"timestamp":"2026-05-13T04:10:20.000Z","type":"event_msg","payload":{"type":"user_message","message":"fast prompt"}}`,
		`{"timestamp":"2026-05-13T04:11:20.000Z","type":"event_msg","payload":{"type":"agent_message","message":"fast answer","phase":"final_answer"}}`,
	)
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, lines)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_fast_file", "t_fast_file", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtimeState := newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-fast",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime: sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			codex:    runtimeState,
			helper: &runtimeIODHelper{
				sessionID:    sessionID,
				generationID: generationID,
				historyFunc: func(ctx context.Context) (iod.SessionHistoryResponsePacket, error) {
					select {
					case <-release:
						return iod.SessionHistoryResponsePacket{}, nil
					case <-ctx.Done():
						return iod.SessionHistoryResponsePacket{}, ctx.Err()
					}
				},
			},
		},
		Transport: SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}

	start := time.Now()
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 2})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 200*time.Millisecond {
		t.Fatalf("SessionMessages() elapsed = %s, want local source page without waiting for helper", elapsed)
	}
	got := messageRolesAndText(messages.Items)
	want := []string{"user:fast prompt", "assistant:fast answer"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
}

func TestSessionMessagesCodexSourcePageSupportsBeforeSeq(t *testing.T) {
	threadID := "019e2107-ca2f-7e73-994d-8726965f8c8d"
	lines := makeLargeCodexSessionLines(threadID, "/tmp/codex-before")
	lines = append(lines,
		`{"timestamp":"2026-05-13T04:10:19.000Z","type":"session_meta","payload":{"id":"019e2107-ca2f-7e73-994d-8726965f8c8d","cwd":"/tmp/codex-before","originator":"actrail"}}`,
		`{"timestamp":"2026-05-13T04:10:20.000Z","type":"event_msg","payload":{"type":"user_message","message":"first prompt"}}`,
		`{"timestamp":"2026-05-13T04:11:20.000Z","type":"event_msg","payload":{"type":"agent_message","message":"first answer","phase":"final_answer"}}`,
		`{"timestamp":"2026-05-13T04:12:20.000Z","type":"event_msg","payload":{"type":"user_message","message":"second prompt"}}`,
		`{"timestamp":"2026-05-13T04:13:20.000Z","type":"event_msg","payload":{"type":"agent_message","message":"second answer","phase":"final_answer"}}`,
	)
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, lines)

	latest, _, ok, err := codexSessionMessagesPageFromFile(context.Background(), sourcePath, SessionMessagesRequest{Limit: 2})
	if err != nil || !ok {
		t.Fatalf("latest page = ok:%v err:%v", ok, err)
	}
	got := messageRolesAndText(latest.Items)
	wantLatest := []string{"user:second prompt", "assistant:second answer"}
	if strings.Join(got, "\n") != strings.Join(wantLatest, "\n") {
		t.Fatalf("latest messages = %#v, want %#v", got, wantLatest)
	}
	if !latest.HasMore || latest.NextBeforeSeq == nil {
		t.Fatalf("latest paging = %+v, want older cursor", latest)
	}

	older, _, ok, err := codexSessionMessagesPageFromFile(context.Background(), sourcePath, SessionMessagesRequest{BeforeSeq: latest.NextBeforeSeq, Limit: 2})
	if err != nil || !ok {
		t.Fatalf("older page = ok:%v err:%v", ok, err)
	}
	got = messageRolesAndText(older.Items)
	wantOlder := []string{"user:first prompt", "assistant:first answer"}
	if strings.Join(got, "\n") != strings.Join(wantOlder, "\n") {
		t.Fatalf("older messages = %#v, want %#v", got, wantOlder)
	}
}

func makeLargeCodexSessionLines(threadID string, cwd string) []string {
	lines := []string{
		`{"timestamp":"2026-05-13T04:00:00.000Z","type":"session_meta","payload":{"id":"` + threadID + `","cwd":"` + cwd + `","originator":"actrail"}}`,
	}
	padding := strings.Repeat("x", 2048)
	for i := 0; i < 700; i++ {
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-05-13T04:00:01.000Z","type":"response_item","payload":{"type":"reasoning","summary":[{"text":"%s-%03d"}]}}`, padding, i))
	}
	return lines
}

func TestWarmCodexSourceHistoriesCachesSourceFile(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_warmup")
	threadID := "019e2107-ca2f-7e73-994d-8726965f8c8b"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-13T04:10:19.000Z","type":"session_meta","payload":{"id":"019e2107-ca2f-7e73-994d-8726965f8c8b","cwd":"/tmp/codex-history","originator":"actrail"}}`,
		`{"timestamp":"2026-05-13T04:10:20.000Z","type":"event_msg","payload":{"type":"user_message","message":"warm this source"}}`,
		`{"timestamp":"2026-05-13T04:11:20.000Z","type":"event_msg","payload":{"type":"agent_message","message":"warmed from startup","phase":"final_answer"}}`,
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
	signature, ok := codexSessionFileSignature(sourcePath)
	if !ok {
		t.Fatalf("codexSessionFileSignature(%q) = false", sourcePath)
	}
	if _, ok := svc.messageCache.Get(sessionID, "codex-source-file:"+signature); ok {
		t.Fatal("message cache unexpectedly warm before warmCodexSourceHistories")
	}

	svc.warmCodexSourceHistories(context.Background())

	items, ok := svc.messageCache.Get(sessionID, "codex-source-file:"+signature)
	if !ok {
		t.Fatal("message cache missing source file after warmCodexSourceHistories")
	}
	got := messageRolesAndText(items)
	want := []string{"user:warm this source", "assistant:warmed from startup"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("cached messages = %#v, want %#v", got, want)
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

func TestCodexSessionTurnAbortedIsTerminalForActiveChecks(t *testing.T) {
	sessionID := mustSessionID(t, "s_codex_turn_aborted_terminal")
	generationID := mustHelperGenerationID(t, "g_codex_turn_aborted_terminal")
	lines := []string{
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"user_message","message":"run this"}}`,
		`{"timestamp":"2026-05-08T15:58:04.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"working"}]}}`,
		`{"timestamp":"2026-05-08T15:58:05.000Z","type":"event_msg","payload":{"type":"turn_aborted"}}`,
	}
	if !codexSessionLinesHaveTaskComplete(context.Background(), lines) {
		t.Fatal("codexSessionLinesHaveTaskComplete() = false, want true for turn_aborted")
	}
	if codexSessionLinesIndicateActiveTurn(lines) {
		t.Fatal("codexSessionLinesIndicateActiveTurn() = true, want false after turn_aborted")
	}
	packet, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		Lines:    lines,
		Messages: []iod.SessionHistoryMessage{{Seq: 1, Role: "user", Kind: "message", Text: "run this"}},
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket() error = %v", err)
	}
	if codexIODHistoryPacketActiveTurn(packet) {
		t.Fatal("codexIODHistoryPacketActiveTurn() = true, want false after turn_aborted")
	}
}

func attachCodexHistoryIODHelperFromFile(t *testing.T, svc *Stub, cfg config.Config, sessionID session.SessionID, sourcePath string) iod.SessionHistoryResponsePacket {
	t.Helper()
	return attachCodexHistoryIODHelperFromFileWithComplete(t, svc, cfg, sessionID, sourcePath, true)
}

func attachCodexHistoryIODHelperFromFileWithComplete(t *testing.T, svc *Stub, cfg config.Config, sessionID session.SessionID, sourcePath string, complete bool) iod.SessionHistoryResponsePacket {
	t.Helper()
	_ = cfg
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", sourcePath, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	appItems, err := codexSessionMessagesFromJSONLLines(context.Background(), sourcePath, lines)
	if err != nil {
		t.Fatalf("codexSessionMessagesFromJSONLLines() error = %v", err)
	}
	messages := make([]iod.SessionHistoryMessage, 0, len(appItems))
	for _, item := range appItems {
		messages = append(messages, iod.SessionHistoryMessage{
			Seq:         item.Seq,
			Role:        item.Role,
			Kind:        item.Kind,
			Type:        item.Type,
			Text:        item.Text,
			TS:          item.TS,
			EventID:     item.EventID,
			SourceOrder: item.SourceOrder,
			Name:        item.Name,
			Summary:     item.Summary,
			ToolCallID:  item.ToolCallID,
			IsError:     item.IsError,
			Details:     item.Details,
		})
	}
	generationID := mustHelperGenerationID(t, "g_"+sessionID.String()+"_history")
	runtimeRoot := filepath.Join("/tmp", fmt.Sprintf("arhist-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	manifestPath := filepath.Join(runtimeRoot, "manifest.json")
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	packet, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		SourcePath:   sourcePath,
		Lines:        lines,
		Messages:     messages,
		IndexedCount: len(messages),
		TaskComplete: codexSessionLinesHaveTaskComplete(context.Background(), lines),
		Warmed:       true,
		Complete:     complete,
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket() error = %v", err)
	}
	cleanup := startReplayHelper(t, manifest, helperReplayScript{SkipReplay: true, History: &packet})
	t.Cleanup(cleanup)
	hello, err := iod.NewHelloPacket(sessionID, generationID, 1, manifest.HelloProof)
	if err != nil {
		t.Fatalf("NewHelloPacket() error = %v", err)
	}
	svc.helpers.replaceAll(map[session.SessionID]attachedHelper{
		sessionID: {
			Binding:  helperGenerationBinding{SessionID: sessionID, GenerationID: generationID},
			Manifest: manifest,
			Hello:    hello,
			Client:   &iodclient.Client{},
		},
	}, nil)
	return packet
}

func primeCodexIODHistoryCache(t *testing.T, svc *Stub, sessionID session.SessionID, packet iod.SessionHistoryResponsePacket) {
	t.Helper()
	svc.codexIODHistoryMu.Lock()
	if svc.codexIODHistory == nil {
		svc.codexIODHistory = map[session.SessionID]codexIODHistoryCacheEntry{}
	}
	svc.codexIODHistory[sessionID] = codexIODHistoryCacheEntry{packet: packet, checkedAt: time.Now()}
	svc.codexIODHistoryMu.Unlock()
}

func TestSessionStateDoesNotBlockOnCodexIODHistoryCacheMiss(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_state_fast_cache_miss")
	generationID := mustHelperGenerationID(t, "g_codex_state_fast_cache_miss")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_state_fast", "t_state_fast", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtimeState := newCodexRuntimeStateWithResumeThread(session.BackendCodex, "019e084e-63e0-7320-9a4a-84f68f656827")
	runtimeState.setActiveTurnID("turn-state-fast")
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity: &identity,
		Backend:  session.BackendCodex,
		CWD:      "/tmp/codex-state-fast",
		Runtime: sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			codex:    runtimeState,
			helper: &runtimeIODHelper{
				sessionID:    sessionID,
				generationID: generationID,
				historyFunc: func(ctx context.Context) (iod.SessionHistoryResponsePacket, error) {
					select {
					case <-release:
						return iod.SessionHistoryResponsePacket{}, nil
					case <-ctx.Done():
						return iod.SessionHistoryResponsePacket{}, ctx.Err()
					}
				},
			},
		},
		Transport: SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}

	start := time.Now()
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 200*time.Millisecond {
		t.Fatalf("SessionState() elapsed = %s, want cache-miss path to return without waiting for IOD history", elapsed)
	}
	if !state.Busy {
		t.Fatal("SessionState().Busy = false, want current in-memory busy state while history refresh runs")
	}
}

func TestSessionStateCodexSkipsNonFinalIODProjection(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_state_skip_nonfinal_projection")
	generationID := mustHelperGenerationID(t, "g_codex_state_skip_nonfinal_projection")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656830"
	sourcePath := filepath.Join(t.TempDir(), "rollout-"+threadID+".jsonl")
	lines := []string{
		`{"timestamp":"2026-05-10T08:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-nonfinal","started_at":1760000001}}`,
		`{"timestamp":"2026-05-10T08:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"still running","phase":"commentary"}}`,
	}
	packet, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		SourcePath: sourcePath,
		Lines:      lines,
		Messages: []iod.SessionHistoryMessage{{
			Seq:     2,
			Role:    "assistant",
			Kind:    "message",
			Text:    "still running",
			Details: map[string]any{"phase": "commentary"},
		}},
		Warmed:   true,
		Complete: true,
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket() error = %v", err)
	}

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_state_nonfinal_projection", "t_state_nonfinal_projection", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtimeState := newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)
	runtimeState.setActiveTurnID("turn-nonfinal")
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity: &identity,
		Backend:  session.BackendCodex,
		CWD:      "/tmp/codex-nonfinal-projection",
		Runtime: sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			codex:    runtimeState,
			helper: &runtimeIODHelper{
				sessionID:    sessionID,
				generationID: generationID,
			},
		},
		SourcePath:       sourcePath,
		BackendSessionID: threadID,
		Transport:        SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}
	primeCodexIODHistoryCache(t, svc, sessionID, packet)

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if !state.Busy || state.RuntimeState != string(codexRuntimePhaseRunning) {
		t.Fatalf("SessionState() = busy:%v runtime:%q, want running without non-final state projection", state.Busy, state.RuntimeState)
	}
	key := codexIODHistoryCacheKey(packet)
	svc.codexIODHistoryMu.Lock()
	entry := svc.codexIODHistory[sessionID]
	svc.codexIODHistoryMu.Unlock()
	if entry.stateAppliedKey == key {
		t.Fatal("non-final IOD history packet was marked state-applied by SessionState")
	}
}

func TestCodexRuntimeMutationKeepsIODHistoryCaches(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_runtime_keeps_history_cache")
	generationID := mustHelperGenerationID(t, "g_codex_runtime_keeps_history_cache")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656828"
	sourcePath := filepath.Join(t.TempDir(), "rollout-"+threadID+".jsonl")

	packet, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		SourcePath: sourcePath,
		Lines: []string{
			`{"timestamp":"2026-05-10T08:00:01Z","type":"event_msg","payload":{"type":"agent_message","message":"cached answer","phase":"final_answer"}}`,
		},
		Messages: []iod.SessionHistoryMessage{{
			Seq:         1,
			Role:        "assistant",
			Kind:        "message",
			Text:        "cached answer",
			EventID:     "codex:event:assistant:000001",
			SourceOrder: "codex:000001",
			Details:     map[string]any{"phase": "final_answer"},
		}},
		Warmed:       true,
		Complete:     true,
		TaskComplete: true,
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket() error = %v", err)
	}
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_runtime_cache", "t_runtime_cache", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtimeState := newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)
	historyCalls := make(chan struct{}, 1)
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity: &identity,
		Backend:  session.BackendCodex,
		CWD:      t.TempDir(),
		Runtime: sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			codex:    runtimeState,
			helper: &runtimeIODHelper{
				sessionID:    sessionID,
				generationID: generationID,
				historyFunc: func(context.Context) (iod.SessionHistoryResponsePacket, error) {
					select {
					case historyCalls <- struct{}{}:
					default:
					}
					return packet, nil
				},
			},
		},
		SourcePath:       sourcePath,
		BackendSessionID: threadID,
		Transport:        SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	primeCodexIODHistoryCache(t, svc, sessionID, packet)
	cacheKey := codexIODHistoryCacheKey(packet)
	svc.messageCache.PutWithCompletion(sessionID, cacheKey, []SessionMessage{{
		Seq:         1,
		Role:        "assistant",
		Kind:        "message",
		Text:        "cached answer",
		EventID:     "codex:event:assistant:000001",
		SourceOrder: "codex:000001",
		Details:     map[string]any{"phase": "final_answer"},
	}}, true)

	err = svc.applyPIEvent(sessionID, pi.Event{
		Kind:    pi.EventKindMessageDelta,
		RawType: "item/agentMessage/delta",
		TurnID:  "turn-runtime-cache",
		Delta:   &pi.MessageDelta{Role: pi.MessageRoleAssistant, Text: "still running"},
	})
	if err != nil {
		t.Fatalf("applyPIEvent() error = %v", err)
	}
	if _, _, ok := svc.messageCache.GetWithCompletion(sessionID, cacheKey); !ok {
		t.Fatalf("message cache entry for %q was invalidated by Codex runtime mutation", cacheKey)
	}
	svc.codexIODHistoryMu.Lock()
	_, ok := svc.codexIODHistory[sessionID]
	svc.codexIODHistoryMu.Unlock()
	if !ok {
		t.Fatal("codex IOD history packet was invalidated by Codex runtime mutation")
	}
	select {
	case <-historyCalls:
		t.Fatal("fresh Codex IOD history cache was refreshed synchronously for runtime mutation")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCodexTurnCompletedForcesIODHistoryRefresh(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_turn_complete_refresh")
	generationID := mustHelperGenerationID(t, "g_codex_turn_complete_refresh")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656829"
	sourcePath := filepath.Join(t.TempDir(), "rollout-"+threadID+".jsonl")
	stalePacket, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		SourcePath: sourcePath,
		Messages: []iod.SessionHistoryMessage{{
			Seq:  1,
			Role: "user",
			Kind: "message",
			Text: "old prompt",
		}},
		Warmed:   true,
		Complete: true,
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket(stale) error = %v", err)
	}
	freshPacket, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		SourcePath: sourcePath,
		Lines: []string{
			`{"timestamp":"2026-05-10T08:00:01Z","type":"event_msg","payload":{"type":"agent_message","message":"fresh answer","phase":"final_answer"}}`,
		},
		Messages: []iod.SessionHistoryMessage{{
			Seq:         2,
			Role:        "assistant",
			Kind:        "message",
			Text:        "fresh answer",
			EventID:     "codex:event:assistant:000002",
			SourceOrder: "codex:000002",
			Details:     map[string]any{"phase": "final_answer"},
		}},
		Warmed:       true,
		Complete:     true,
		TaskComplete: true,
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket(fresh) error = %v", err)
	}
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_turn_refresh", "t_turn_refresh", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtimeState := newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)
	runtimeState.setActiveTurnID("turn-refresh")
	historyCalls := make(chan struct{}, 1)
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity: &identity,
		Backend:  session.BackendCodex,
		CWD:      t.TempDir(),
		Runtime: sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			codex:    runtimeState,
			helper: &runtimeIODHelper{
				sessionID:    sessionID,
				generationID: generationID,
				historyFunc: func(context.Context) (iod.SessionHistoryResponsePacket, error) {
					select {
					case historyCalls <- struct{}{}:
					default:
					}
					return freshPacket, nil
				},
			},
		},
		SourcePath:       sourcePath,
		BackendSessionID: threadID,
		Transport:        SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	primeCodexIODHistoryCache(t, svc, sessionID, stalePacket)
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}

	if err := svc.applyPIEvent(sessionID, pi.Event{
		Kind:     pi.EventKindBoundary,
		RawType:  "turn/completed",
		TurnID:   "turn-refresh",
		ThreadID: threadID,
		Boundary: &pi.Boundary{
			Kind:       pi.BoundaryKindTurnCompleted,
			CommitLike: true,
			Reason:     "turn/completed",
		},
	}); err != nil {
		t.Fatalf("applyPIEvent() error = %v", err)
	}
	select {
	case <-historyCalls:
	case <-time.After(time.Second):
		t.Fatal("turn completion did not request fresh IOD history")
	}
	waitForAppCondition(t, func() bool {
		svc.codexIODHistoryMu.Lock()
		packet := svc.codexIODHistory[sessionID].packet
		svc.codexIODHistoryMu.Unlock()
		return codexIODHistoryCacheKey(packet) == codexIODHistoryCacheKey(freshPacket)
	})
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	got := messageRolesAndText(messages.Items)
	want := []string{"assistant:fresh answer"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages = %#v, want %#v", got, want)
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
	attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)

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
	attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)

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

func TestSessionMessagesCodexIODHistoryShowsIncompleteFailedToolOutput(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_incomplete_error_history")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-incomplete","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"run failing task"}}`,
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"pytest\"}","call_id":"call-pytest"}}`,
		`{"timestamp":"2026-05-08T15:58:05.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-pytest","output":"ImportError: cannot import name 'AsyncEngineArgs' from 'vllm'\nProcess exited with code 1\n"}}`,
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
		CWD:              "/tmp/codex-incomplete",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	attachCodexHistoryIODHelperFromFileWithComplete(t, svc, cfg, sessionID, sourcePath, false)

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 10, IncludeToolEvents: true})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 3 {
		t.Fatalf("messages = %+v, want user, tool, failed tool_result from incomplete history", messages.Items)
	}
	if messages.Items[2].Kind != "tool_result" || !messages.Items[2].IsError || !strings.Contains(messages.Items[2].Text, "ImportError: cannot import name 'AsyncEngineArgs'") {
		t.Fatalf("failed tool result = %+v, want visible import error", messages.Items[2])
	}

	visible, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionMessages(default) error = %v", err)
	}
	if len(visible.Items) != 2 || visible.Items[1].Kind != "tool_result" || !visible.Items[1].IsError {
		t.Fatalf("SessionMessages(default) = %+v, want visible failed tool result without summary fallback", visible.Items)
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

func TestCodexRuntimeRestartPrefersExactSourceBindingOverStaleRuntimeThread(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_restart_prefers_source_binding")
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
	staleRuntime := newCodexRuntimeState(session.BackendCodex)
	if accepted, _ := staleRuntime.setThreadID("019e2107-ca2f-7e73-994d-8726965f8c8b"); !accepted {
		t.Fatal("setThreadID(stale) = false, want true")
	}
	record, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-binding",
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: staleRuntime},
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
	})
	if err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}

	resumeID, err := svc.codexThreadIDForRuntimeRestart(context.Background(), record)
	if err != nil {
		t.Fatalf("codexThreadIDForRuntimeRestart() error = %v", err)
	}
	if resumeID != threadID {
		t.Fatalf("codexThreadIDForRuntimeRestart() = %q, want exact binding %q", resumeID, threadID)
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

func TestCodexIODHistoryRebindsSessionSourcePath(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_iod_rebinds_source")
	oldThreadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	newThreadID := "019e084e-63e0-7320-9a4a-84f68f656828"
	oldSourcePath := writeCodexSessionFile(t, t.TempDir(), oldThreadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-rebind","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"agent_message","message":"old answer","phase":"final_answer"}}`,
	})
	newSourcePath := writeCodexSessionFile(t, t.TempDir(), newThreadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656828","cwd":"/tmp/codex-rebind","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"agent_message","message":"new answer","phase":"final_answer"}}`,
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
		CWD:              "/tmp/codex-rebind",
		BackendSessionID: oldThreadID,
		SourcePath:       oldSourcePath,
		SourceConfidence: sourceConfidenceExact,
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, newSourcePath)

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 10, IncludeToolEvents: true})
	if err != nil {
		t.Fatalf("SessionMessages(include tools) error = %v", err)
	}
	got := messageRolesAndText(messages.Items)
	want := []string{"assistant:new answer"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
	updated, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatal("session missing after IOD history load")
	}
	if updated.importedBackendSessionID != newThreadID || updated.importedSourcePath != filepath.Clean(newSourcePath) {
		t.Fatalf("binding = (%q, %q), want (%q, %q)", updated.importedBackendSessionID, updated.importedSourcePath, newThreadID, filepath.Clean(newSourcePath))
	}
	defaultMessages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionMessages(default) error = %v", err)
	}
	got = messageRolesAndText(defaultMessages.Items)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("default messages = %#v, want %#v", got, want)
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
	attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)
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
	attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)
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
	packet := attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)
	primeCodexIODHistoryCache(t, svc, sessionID, packet)
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
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-task-complete",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: runtimeState},
		Transport:        SessionTransportSnapshot{State: SessionTransportStateAttached, Reason: "codex_replay_failed:replay_failed"},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	packet := attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)
	primeCodexIODHistoryCache(t, svc, sessionID, packet)
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

func TestSessionStateCodexTaskCompleteWithoutMessagesClearsRuntimeAgentRunning(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_task_complete_only")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-task-complete-only","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-codex-task-complete-only","last_agent_message":null,"completed_at":1760000350}}`,
	})
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_task_complete_only", "t_task_complete_only", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtimeState := newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)
	runtimeState.setActiveTurnID("turn-codex-task-complete-only")
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-task-complete-only",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: runtimeState},
		Transport:        SessionTransportSnapshot{State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	packet := attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)
	primeCodexIODHistoryCache(t, svc, sessionID, packet)
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}
	if _, err := svc.AppendAssistantDelta(sessionID, "turn-codex-task-complete-only", "stale partial"); err != nil {
		t.Fatalf("AppendAssistantDelta() error = %v", err)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || state.RuntimeState != string(codexRuntimePhaseIdle) || state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState() = busy:%v runtime:%q partial:%+v, want idle with no partial after task_complete-only", state.Busy, state.RuntimeState, state.PartialAssistantTurn)
	}
}

func TestSessionStateCodexTaskStartedAfterTaskCompleteDoesNotApplyActiveHistory(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_task_started_keeps_running")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-task-started","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-codex-old","started_at":1760000282,"model_context_window":228000}}`,
		`{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-codex-old","last_agent_message":null,"completed_at":1760000350}}`,
		`{"timestamp":"2026-05-08T16:00:10.297Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-codex-new","started_at":1760000410,"model_context_window":228000}}`,
	})
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_task_started", "t_task_started", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtimeState := newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)
	runtimeState.setActiveTurnID("turn-codex-old")
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-task-started",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: runtimeState},
		Transport:        SessionTransportSnapshot{State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	packet := attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)
	primeCodexIODHistoryCache(t, svc, sessionID, packet)
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
		t.Fatalf("SessionState() = busy:%v runtime:%q, want running after newer task_started", state.Busy, state.RuntimeState)
	}
	key := codexIODHistoryCacheKey(packet)
	svc.codexIODHistoryMu.Lock()
	entry := svc.codexIODHistory[sessionID]
	svc.codexIODHistoryMu.Unlock()
	if entry.stateAppliedKey == key {
		t.Fatal("SessionState applied active non-final IOD history after newer task_started")
	}
}

func TestSessionMessagesCodexIODHistoryLooksUpToolByID(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_iod_tool_lookup")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-tool-lookup","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call-iod-tool","arguments":"{\"cmd\":\"date\"}"}}`,
		`{"timestamp":"2026-05-08T15:58:04.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-iod-tool","output":"ok"}}`,
	})
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_tool_lookup", "t_tool_lookup", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-tool-lookup",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)
	response, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, ToolCallID: "call-iod-tool", IncludeToolEvents: true})
	if err != nil {
		t.Fatalf("SessionMessages(tool_call_id) error = %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].ToolCallID != "call-iod-tool" || response.Items[0].Kind != "tool" {
		t.Fatalf("SessionMessages(tool_call_id) = %+v, want IOD tool call detail", response)
	}
	response, err = svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, EventID: "codex:item:tool-result:000003", IncludeToolEvents: true})
	if err != nil {
		t.Fatalf("SessionMessages(event_id) error = %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Kind != "tool_result" || response.Items[0].Text != "ok" {
		t.Fatalf("SessionMessages(event_id) = %+v, want IOD tool result detail", response)
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
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-newer-user",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: runtimeState},
		Transport:        SessionTransportSnapshot{State: SessionTransportStateAttached, Reason: "codex_thread"},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	packet := attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)
	primeCodexIODHistoryCache(t, svc, sessionID, packet)
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
	packet := attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)
	primeCodexIODHistoryCache(t, svc, sessionID, packet)
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

func TestSessionStateCodexSourceTailFinalClearsPartialTurn(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_state_source_final_clears_partial")
	generationID := mustHelperGenerationID(t, "g_codex_state_source_final_clears_partial")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656831"
	turnID := "019e3e2a-92c5-7b62-886e-59b89d94f95e"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-19T03:00:01.000Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656831","cwd":"/tmp/codex-source-final","originator":"actrail"}}`,
		`{"timestamp":"2026-05-19T03:00:02.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"` + turnID + `","started_at":1779159602}}`,
		`{"timestamp":"2026-05-19T03:08:12.605Z","type":"event_msg","payload":{"type":"agent_message","message":"done from source","phase":"final_answer"}}`,
		`{"timestamp":"2026-05-19T03:08:12.613Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turnID + `","completed_at":1779160092}}`,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_state_source_final", "t_state_source_final", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtimeState := newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)
	runtimeState.setActiveTurnID(turnID)
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-source-final",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: runtimeState},
		Transport:        SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}
	if _, err := svc.AppendAssistantDelta(sessionID, turnID, "stale partial"); err != nil {
		t.Fatalf("AppendAssistantDelta() error = %v", err)
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState() = busy:%v partial:%+v, want source tail final to clear partial", state.Busy, state.PartialAssistantTurn)
	}
	if svc.isRuntimeAgentRunning(sessionID) {
		t.Fatal("runtimeAgentRunning = true, want false after source tail final")
	}
}

func TestSessionStateCodexUsesAuthoritativeSourceTail(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_state_source_tail")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	lines := []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-source-tail","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"run this"}}`,
	}
	padding := strings.Repeat("x", 4096)
	for i := 0; i < 300; i++ {
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"token_count","input_tokens":%d,"padding":%q}}`, i, padding))
	}
	lines = append(lines, `{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"latest answer","phase":"commentary"}}`)
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, lines)

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_state_source_tail", "t_state_source_tail", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-source-tail",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime:          sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID)},
		Transport:        SessionTransportSnapshot{State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.TailSeq == 0 {
		t.Fatal("SessionState().TailSeq = 0, want authoritative Codex file tail")
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 1})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if state.TailSeq != messages.TailSeq {
		t.Fatalf("SessionState().TailSeq = %d, want SessionMessages tail %d", state.TailSeq, messages.TailSeq)
	}
}

func TestSessionStateCodexUsesSourceTailWhenIODCacheIsStale(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_file_state_stale_iod_tail")
	generationID := mustHelperGenerationID(t, "g_codex_file_state_stale_iod_tail")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	lines := []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-stale-tail","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"run this"}}`,
	}
	for i := 0; i < 260; i++ {
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"token_count","input_tokens":%d}}`, i))
	}
	lines = append(lines, `{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"source is newer","phase":"commentary"}}`)
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, lines)

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_state_stale_tail", "t_state_stale_tail", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              "/tmp/codex-stale-tail",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
		Runtime: sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			codex:    newCodexRuntimeStateWithResumeThread(session.BackendCodex, threadID),
			helper: &runtimeIODHelper{
				sessionID:    sessionID,
				generationID: generationID,
			},
		},
		Transport: SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	packet, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		SourcePath: sourcePath,
		Messages: []iod.SessionHistoryMessage{{
			Seq:  3,
			Role: "assistant",
			Kind: "message",
			Text: "stale cache",
		}},
		Warmed: true,
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket() error = %v", err)
	}
	primeCodexIODHistoryCache(t, svc, sessionID, packet)

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 1})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if messages.TailSeq != 3 {
		t.Fatalf("SessionMessages().TailSeq = %d, want stale IOD cache tail 3", messages.TailSeq)
	}
	if state.TailSeq <= messages.TailSeq {
		t.Fatalf("SessionState().TailSeq = %d, want newer source tail over stale IOD cache tail %d", state.TailSeq, messages.TailSeq)
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
	packet := attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)
	primeCodexIODHistoryCache(t, svc, sessionID, packet)
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

func TestListSessionsCodexFinalAnswerTailClearsStaleRunning(t *testing.T) {
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
		t.Fatalf("ListSessions().Items[0] = busy:%v runtime:%q, want trusted final source tail to clear stale running", listed.Items[0].Busy, listed.Items[0].RuntimeState)
	}
	if svc.isRuntimeAgentRunning(sessionID) {
		t.Fatal("runtimeAgentRunning = true, want false after trusted final source tail")
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

func readCodexStateDBTitle(t *testing.T, codexHome, threadID string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(codexHome, "state_6.sqlite"))
	if err != nil {
		t.Fatalf("open codex state db: %v", err)
	}
	defer db.Close()
	var title string
	if err := db.QueryRow(`SELECT title FROM threads WHERE id = ?`, threadID).Scan(&title); err != nil {
		t.Fatalf("select codex thread title: %v", err)
	}
	return title
}
