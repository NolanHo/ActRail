package app

import (
	"context"
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
