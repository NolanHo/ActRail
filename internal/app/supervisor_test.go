package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func newSupervisorTestStub(now time.Time) *Stub {
	return NewStubForTest(config.Load(), func() time.Time { return now }, RuntimeConfig{})
}

func TestSupervisorProviderReadDoesNotReturnAPIKey(t *testing.T) {
	svc := newSupervisorTestStub(time.Unix(1760000000, 0).UTC())
	apiKey := "secret-key"
	updated, err := svc.UpdateSupervisorProvider(context.Background(), UpdateSupervisorProviderRequest{
		BaseURL: " https://llm.invalid/v1 ",
		APIKey:  &apiKey,
		Model:   " model-a ",
	})
	if err != nil {
		t.Fatalf("UpdateSupervisorProvider() error = %v", err)
	}
	if !updated.APIKeyConfigured || !updated.Complete || updated.BaseURL != "https://llm.invalid/v1" || updated.Model != "model-a" {
		t.Fatalf("UpdateSupervisorProvider() = %+v", updated)
	}

	read, err := svc.SupervisorProvider(context.Background(), SupervisorProviderRequest{})
	if err != nil {
		t.Fatalf("SupervisorProvider() error = %v", err)
	}
	if !read.APIKeyConfigured || !read.Complete || read.BaseURL != "https://llm.invalid/v1" || read.Model != "model-a" {
		t.Fatalf("SupervisorProvider() = %+v", read)
	}
}

func TestSupervisorProviderTestUsesChatCompletion(t *testing.T) {
	svc := newSupervisorTestStub(time.Unix(1760000000, 0).UTC())
	seenAuthorization := ""
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/chat/completions" {
			t.Fatalf("model path = %q, want /chat/completions", req.URL.Path)
		}
		seenAuthorization = req.Header.Get("Authorization")
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("Decode model request: %v", err)
		}
		if body.Model != "model-a" || len(body.Messages) != 2 || !strings.Contains(body.Messages[1].Content, "hello") {
			t.Fatalf("model request = %+v", body)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello from test model"}}]}`))
	}))
	defer modelServer.Close()
	apiKey := "secret"
	if _, err := svc.UpdateSupervisorProvider(context.Background(), UpdateSupervisorProviderRequest{BaseURL: modelServer.URL, APIKey: &apiKey, Model: "model-a"}); err != nil {
		t.Fatalf("UpdateSupervisorProvider() error = %v", err)
	}

	response, err := svc.TestSupervisorProvider(context.Background(), TestSupervisorProviderRequest{})
	if err != nil {
		t.Fatalf("TestSupervisorProvider() error = %v", err)
	}
	if !response.OK || response.Output != "hello from test model" || response.StatusCode != 200 {
		t.Fatalf("TestSupervisorProvider() = %+v", response)
	}
	if seenAuthorization != "Bearer secret" {
		t.Fatalf("Authorization = %q, want bearer key", seenAuthorization)
	}
}

func TestSessionSupervisorDefaultsUseMaxConsecutiveInjectionsTen(t *testing.T) {
	svc := newSupervisorTestStub(time.Unix(1760000000, 0).UTC())
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	response, err := svc.SessionSupervisor(context.Background(), SessionSupervisorRequest{SessionID: mustSessionID(t, created.Session.SessionID)})
	if err != nil {
		t.Fatalf("SessionSupervisor() error = %v", err)
	}
	if !response.Supported || response.Enabled || response.IdleAfterMinutes != 5 || response.MaxConsecutiveInjections != 10 || response.ConsecutiveInjections != 0 {
		t.Fatalf("SessionSupervisor() = %+v", response)
	}
}

func TestSessionSupervisorAllowsCodexBackend(t *testing.T) {
	svc := newSupervisorTestStub(time.Unix(1760000000, 0).UTC())
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession(codex) error = %v", err)
	}
	response, err := svc.SessionSupervisor(context.Background(), SessionSupervisorRequest{SessionID: mustSessionID(t, created.Session.SessionID)})
	if err != nil {
		t.Fatalf("SessionSupervisor(codex) error = %v", err)
	}
	if !response.Supported || response.IdleAfterMinutes != 5 {
		t.Fatalf("SessionSupervisor(codex) = %+v", response)
	}
}

func TestRunSupervisorOnceAnchorsToLastStablePIAssistantEventID(t *testing.T) {
	t.Setenv("PI_HOME", t.TempDir())
	now := time.Unix(1760000000, 0).UTC()
	svc := newSupervisorTestStub(now)
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	record, err := svc.lookupSession(mustSessionID(t, created.Session.SessionID))
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	body := fmt.Sprintf(`{"type":"session","version":3,"id":"pi-supervisor","cwd":%q}
{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"start"}]}}
{"type":"message","id":"a1","parentId":"u1","message":{"role":"assistant","content":[{"type":"text","text":"partial answer"}],"stopReason":"stop"}}
`, record.cwd)
	if err := os.WriteFile(record.importedSourcePath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", record.importedSourcePath, err)
	}
	if err := os.WriteFile(filepath.Join(record.cwd, "notes.txt"), []byte("local context"), 0o644); err != nil {
		t.Fatalf("WriteFile(context) error = %v", err)
	}
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/chat/completions" {
			t.Fatalf("model path = %q, want /chat/completions", req.URL.Path)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"stop\",\"reason\":\"assistant appears complete\"}"}}]}`))
	}))
	defer modelServer.Close()
	apiKey := "secret"
	if _, err := svc.UpdateSupervisorProvider(context.Background(), UpdateSupervisorProviderRequest{BaseURL: modelServer.URL, APIKey: &apiKey, Model: "model-a"}); err != nil {
		t.Fatalf("UpdateSupervisorProvider() error = %v", err)
	}
	enabled := true
	contextFiles := []string{"notes.txt"}
	if _, err := svc.UpdateSessionSupervisor(context.Background(), UpdateSessionSupervisorRequest{SessionID: record.identity.SessionID(), Enabled: &enabled, ContextFiles: &contextFiles}); err != nil {
		t.Fatalf("UpdateSessionSupervisor() error = %v", err)
	}

	run, err := svc.RunSupervisorOnce(context.Background(), SupervisorRunOnceRequest{SessionID: record.identity.SessionID(), DryRun: true})
	if err != nil {
		t.Fatalf("RunSupervisorOnce() error = %v", err)
	}
	if run.Run.AnchorAssistantEventID != "pi:message:a1" || run.Run.Status != "stop" || run.Run.Action != "stop" {
		t.Fatalf("RunSupervisorOnce() = %+v", run)
	}
	rows, err := svc.supervisorStore.ListSupervisorRuns(context.Background(), record.identity.SessionID().String(), 10)
	if err != nil {
		t.Fatalf("ListSupervisorRuns() error = %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].SnapshotJSON, `"path":"notes.txt"`) || !strings.Contains(rows[0].SnapshotJSON, `"content":"local context"`) {
		t.Fatalf("supervisor rows = %+v", rows)
	}
	second, err := svc.RunSupervisorOnce(context.Background(), SupervisorRunOnceRequest{SessionID: record.identity.SessionID(), DryRun: true})
	if err != nil {
		t.Fatalf("RunSupervisorOnce(second) error = %v", err)
	}
	if second.Run.RunID != run.Run.RunID {
		t.Fatalf("duplicate anchor produced run %q, want existing %q", second.Run.RunID, run.Run.RunID)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: record.identity.SessionID(), Limit: 20})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 2 || len(messages.Items[1].SupervisorRuns) != 1 || messages.Items[1].SupervisorRuns[0].RunID != run.Run.RunID {
		t.Fatalf("annotated messages = %+v", messages.Items)
	}
}

func TestRunSupervisorOnceAnchorsToLastStableCodexAssistantEventID(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Date(2026, 5, 8, 16, 10, 0, 0, time.UTC)
	sessionID := mustSessionID(t, "s_codex_supervisor")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	sourcePath := writeCodexSessionFile(t, t.TempDir(), threadID, []string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"019e084e-63e0-7320-9a4a-84f68f656827","cwd":"/tmp/codex-supervisor","originator":"actrail"}}`,
		`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"user_message","message":"start"}}`,
		`{"timestamp":"2026-05-08T15:59:10.297Z","type":"event_msg","payload":{"type":"agent_message","message":"codex answer","phase":"final_answer"}}`,
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
		CWD:              "/tmp/codex-supervisor",
		BackendSessionID: threadID,
		SourcePath:       sourcePath,
		SourceConfidence: sourceConfidenceExact,
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	attachCodexHistoryIODHelperFromFile(t, svc, cfg, sessionID, sourcePath)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"stop\",\"reason\":\"assistant appears complete\"}"}}]}`))
	}))
	defer modelServer.Close()
	enableSupervisorForTest(t, svc, sessionID, modelServer.URL)

	run, err := svc.RunSupervisorOnce(context.Background(), SupervisorRunOnceRequest{SessionID: sessionID, DryRun: true})
	if err != nil {
		t.Fatalf("RunSupervisorOnce(codex) error = %v", err)
	}
	if !strings.HasPrefix(run.Run.AnchorAssistantEventID, "codex:event:assistant:") || run.Run.Status != supervisorRunStatusStop {
		t.Fatalf("RunSupervisorOnce(codex) = %+v, want codex assistant anchor", run)
	}
}

func TestRunSupervisorOnceRecordsEvaluatingBeforeModelAndInjectsAfterCommitGate(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)
	record := writeSupervisorPIHistory(t, svc, sessionID, "a1", "partial answer")
	seenEvaluating := make(chan error, 1)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rows, err := svc.supervisorStore.ListSupervisorRuns(context.Background(), sessionID.String(), 10)
		if err != nil {
			seenEvaluating <- err
		} else if len(rows) != 1 || rows[0].Status != supervisorRunStatusEvaluating || rows[0].AnchorAssistantEventID != "pi:message:a1" {
			seenEvaluating <- fmt.Errorf("supervisor rows during model call = %+v", rows)
		} else {
			seenEvaluating <- nil
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"inject\",\"message\":\"继续验证\",\"reason\":\"assistant stopped before verification\"}"}}]}`))
	}))
	defer modelServer.Close()
	enableSupervisorForTest(t, svc, sessionID, modelServer.URL)

	run, err := svc.RunSupervisorOnce(context.Background(), SupervisorRunOnceRequest{SessionID: sessionID, DryRun: false})
	if err != nil {
		t.Fatalf("RunSupervisorOnce() error = %v", err)
	}
	if err := <-seenEvaluating; err != nil {
		t.Fatal(err)
	}
	if run.Run.Status != supervisorRunStatusInjected || run.Run.Action != "inject" || run.Run.InjectedText != "继续验证" {
		t.Fatalf("RunSupervisorOnce() = %+v, want injected", run)
	}
	writes := pty.Writes()
	if len(writes) != 1 || writes[0] != "{\"type\":\"prompt\",\"message\":\"继续验证\"}\n" {
		t.Fatalf("pty writes = %#v, want injected prompt", writes)
	}
	supervisor, err := svc.SessionSupervisor(context.Background(), SessionSupervisorRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionSupervisor() error = %v", err)
	}
	if supervisor.ConsecutiveInjections != 1 {
		t.Fatalf("ConsecutiveInjections = %d, want 1", supervisor.ConsecutiveInjections)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: record.identity.SessionID(), Limit: 20})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 3 || len(messages.Items[1].SupervisorRuns) != 1 || messages.Items[1].SupervisorRuns[0].Status != supervisorRunStatusInjected {
		t.Fatalf("annotated messages = %+v", messages.Items)
	}
}

func TestRunSupervisorOnceSkipsLiveInjectWhenInboxIsOpen(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)
	writeSupervisorPIHistory(t, svc, sessionID, "a1", "partial answer")
	modelServer := supervisorInjectServer(t, "继续")
	defer modelServer.Close()
	enableSupervisorForTest(t, svc, sessionID, modelServer.URL)
	now := svc.registry.now()
	if err := svc.schedulerStore.InsertInboxItem(context.Background(), sqlitestore.InboxItemRow{
		ItemID:    "inbox_pending",
		SessionID: sessionID.String(),
		Source:    "self_reminder",
		SourceID:  "self_reminder_pending",
		Title:     "Self Reminder",
		Message:   "higher priority",
		DueAt:     now,
		State:     "pending",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("InsertInboxItem() error = %v", err)
	}

	run, err := svc.RunSupervisorOnce(context.Background(), SupervisorRunOnceRequest{SessionID: sessionID, DryRun: false})
	if err != nil {
		t.Fatalf("RunSupervisorOnce() error = %v", err)
	}
	if run.Run.Status != supervisorRunStatusSkippedBlocked || !strings.Contains(run.Run.Error, "inbox") {
		t.Fatalf("RunSupervisorOnce() = %+v, want skipped_blocked inbox", run)
	}
	if writes := pty.Writes(); len(writes) != 0 {
		t.Fatalf("pty writes = %#v, want no injection", writes)
	}
	supervisor, err := svc.SessionSupervisor(context.Background(), SessionSupervisorRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionSupervisor() error = %v", err)
	}
	if supervisor.ConsecutiveInjections != 0 {
		t.Fatalf("ConsecutiveInjections = %d, want 0", supervisor.ConsecutiveInjections)
	}
}

func TestRunSupervisorOnceSkipsLiveInjectWhenAnchorChangesBeforeCommit(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)
	record := writeSupervisorPIHistory(t, svc, sessionID, "a1", "partial answer")
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeSupervisorPIHistoryBody(t, record.importedSourcePath, record.cwd, "a2", "newer assistant answer")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"inject\",\"message\":\"继续\",\"reason\":\"assistant stopped before verification\"}"}}]}`))
	}))
	defer modelServer.Close()
	enableSupervisorForTest(t, svc, sessionID, modelServer.URL)

	run, err := svc.RunSupervisorOnce(context.Background(), SupervisorRunOnceRequest{SessionID: sessionID, DryRun: false})
	if err != nil {
		t.Fatalf("RunSupervisorOnce() error = %v", err)
	}
	if run.Run.Status != supervisorRunStatusSkippedStale || !strings.Contains(run.Run.Error, "anchor changed") {
		t.Fatalf("RunSupervisorOnce() = %+v, want skipped_stale", run)
	}
	if writes := pty.Writes(); len(writes) != 0 {
		t.Fatalf("pty writes = %#v, want no injection", writes)
	}
}

func TestRunSupervisorOnceRejectsAssistantWithoutPIMessageEventID(t *testing.T) {
	svc := newSupervisorTestStub(time.Unix(1760000000, 0).UTC())
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	if _, _, err := svc.registry.AppendMessage(sessionID, "assistant", "message", "assistant without pi id"); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	apiKey := "secret"
	if _, err := svc.UpdateSupervisorProvider(context.Background(), UpdateSupervisorProviderRequest{BaseURL: "https://llm.invalid/v1", APIKey: &apiKey, Model: "model-a"}); err != nil {
		t.Fatalf("UpdateSupervisorProvider() error = %v", err)
	}
	enabled := true
	if _, err := svc.UpdateSessionSupervisor(context.Background(), UpdateSessionSupervisorRequest{SessionID: sessionID, Enabled: &enabled}); err != nil {
		t.Fatalf("UpdateSessionSupervisor() error = %v", err)
	}
	_, err = svc.RunSupervisorOnce(context.Background(), SupervisorRunOnceRequest{SessionID: sessionID, DryRun: true})
	if err == nil {
		t.Fatal("RunSupervisorOnce() error = nil, want invalid anchor")
	}
}

func TestUpdateSessionSupervisorPersistsConfig(t *testing.T) {
	svc := newSupervisorTestStub(time.Unix(1760000000, 0).UTC())
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	enabled := true
	idle := 2
	max := 12
	goal := "finish issue 11"
	criteria := "tests pass"
	files := []string{"README.md", "README.md", "docs/spec.md", ""}
	updated, err := svc.UpdateSessionSupervisor(context.Background(), UpdateSessionSupervisorRequest{
		SessionID:                mustSessionID(t, created.Session.SessionID),
		Enabled:                  &enabled,
		IdleAfterMinutes:         &idle,
		MaxConsecutiveInjections: &max,
		Goal:                     &goal,
		AcceptanceCriteria:       &criteria,
		ContextFiles:             &files,
	})
	if err != nil {
		t.Fatalf("UpdateSessionSupervisor() error = %v", err)
	}
	if !updated.Enabled || updated.IdleAfterMinutes != 2 || updated.MaxConsecutiveInjections != 12 || updated.Goal != goal || updated.AcceptanceCriteria != criteria {
		t.Fatalf("UpdateSessionSupervisor() = %+v", updated)
	}
	if len(updated.ContextFiles) != 2 || updated.ContextFiles[0] != "README.md" || updated.ContextFiles[1] != "docs/spec.md" {
		t.Fatalf("ContextFiles = %#v, want deduped non-empty paths", updated.ContextFiles)
	}

	read, err := svc.SessionSupervisor(context.Background(), SessionSupervisorRequest{SessionID: mustSessionID(t, created.Session.SessionID)})
	if err != nil {
		t.Fatalf("SessionSupervisor() error = %v", err)
	}
	if !read.Enabled || read.MaxConsecutiveInjections != 12 || read.Goal != goal {
		t.Fatalf("SessionSupervisor() = %+v", read)
	}
}

func supervisorInjectServer(t *testing.T, message string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		escaped, _ := json.Marshal(map[string]string{
			"action":  "inject",
			"message": message,
			"reason":  "assistant stopped before verification",
		})
		response, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": string(escaped)}}},
		})
		_, _ = w.Write(response)
	}))
}

func enableSupervisorForTest(t *testing.T, svc *Stub, sessionID session.SessionID, baseURL string) {
	t.Helper()
	apiKey := "secret"
	if _, err := svc.UpdateSupervisorProvider(context.Background(), UpdateSupervisorProviderRequest{BaseURL: baseURL, APIKey: &apiKey, Model: "model-a"}); err != nil {
		t.Fatalf("UpdateSupervisorProvider() error = %v", err)
	}
	enabled := true
	if _, err := svc.UpdateSessionSupervisor(context.Background(), UpdateSessionSupervisorRequest{SessionID: sessionID, Enabled: &enabled}); err != nil {
		t.Fatalf("UpdateSessionSupervisor() error = %v", err)
	}
}

func writeSupervisorPIHistory(t *testing.T, svc *Stub, sessionID session.SessionID, assistantID, assistantText string) sessionRecord {
	t.Helper()
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	writeSupervisorPIHistoryBody(t, record.importedSourcePath, record.cwd, assistantID, assistantText)
	return record
}

func writeSupervisorPIHistoryBody(t *testing.T, path string, cwd string, assistantID string, assistantText string) {
	t.Helper()
	assistantTextJSON, err := json.Marshal(assistantText)
	if err != nil {
		t.Fatalf("Marshal assistant text error = %v", err)
	}
	body := fmt.Sprintf(`{"type":"session","version":3,"id":"pi-supervisor","cwd":%q}
{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"start"}]}}
{"type":"message","id":%q,"parentId":"u1","message":{"role":"assistant","content":[{"type":"text","text":%s}],"stopReason":"stop"}}
`, cwd, assistantID, string(assistantTextJSON))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
