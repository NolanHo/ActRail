package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"actrail/internal/config"
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

func TestSessionSupervisorDefaultsUseMaxConsecutiveInjectionsTen(t *testing.T) {
	svc := newSupervisorTestStub(time.Unix(1760000000, 0).UTC())
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir()})
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

func TestSessionSupervisorRejectsNonPIBackend(t *testing.T) {
	svc := newSupervisorTestStub(time.Unix(1760000000, 0).UTC())
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession(codex) error = %v", err)
	}
	_, err = svc.SessionSupervisor(context.Background(), SessionSupervisorRequest{SessionID: mustSessionID(t, created.Session.SessionID)})
	if err == nil {
		t.Fatal("SessionSupervisor(codex) error = nil, want unsupported_backend")
	}
	appErr, ok := err.(*Error)
	if !ok || appErr.Code != "unsupported_backend" {
		t.Fatalf("SessionSupervisor(codex) error = %v, want unsupported_backend", err)
	}
}

func TestRunSupervisorOnceAnchorsToLastStablePIAssistantEventID(t *testing.T) {
	now := time.Unix(1760000000, 0).UTC()
	svc := newSupervisorTestStub(now)
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir()})
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
	if _, err := svc.UpdateSessionSupervisor(context.Background(), UpdateSessionSupervisorRequest{SessionID: record.identity.SessionID(), Enabled: &enabled}); err != nil {
		t.Fatalf("UpdateSessionSupervisor() error = %v", err)
	}

	run, err := svc.RunSupervisorOnce(context.Background(), SupervisorRunOnceRequest{SessionID: record.identity.SessionID(), DryRun: true})
	if err != nil {
		t.Fatalf("RunSupervisorOnce() error = %v", err)
	}
	if run.Run.AnchorAssistantEventID != "pi:message:a1" || run.Run.Status != "stop" || run.Run.Action != "stop" {
		t.Fatalf("RunSupervisorOnce() = %+v", run)
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

func TestRunSupervisorOnceRejectsAssistantWithoutPIMessageEventID(t *testing.T) {
	svc := newSupervisorTestStub(time.Unix(1760000000, 0).UTC())
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir()})
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
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir()})
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
