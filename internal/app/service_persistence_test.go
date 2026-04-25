package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func persistentTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Load()
	cfg.Storage.DataDir = t.TempDir()
	return cfg
}

func fakeRuntimeConfig() RuntimeConfig {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPID(321)
	return RuntimeConfig{Runner: &process.FakeRunner{NextHandle: handle}}
}

func TestPersistentStubColdStartRehydratesSessionCatalog(t *testing.T) {
	cfg := persistentTestConfig(t)
	cwd := filepath.Join(t.TempDir(), "project")
	now := time.Unix(1760000000, 0).UTC()
	provider := "openrouter"
	model := "gpt-test"
	reasoning := "high"

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend:    "pi",
		CWD:             cwd,
		Provider:        &provider,
		Model:           &model,
		ReasoningEffort: &reasoning,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if created.Session == nil {
		t.Fatal("CreateSession().Session = nil")
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	item := listed.Items[0]
	if item.SessionID != created.Session.SessionID {
		t.Fatalf("SessionID = %q, want %q", item.SessionID, created.Session.SessionID)
	}
	if item.RuntimeID != "" {
		t.Fatalf("RuntimeID = %q, want empty after cold start", item.RuntimeID)
	}
	if item.DisplayName != cwd {
		t.Fatalf("DisplayName = %q, want %q", item.DisplayName, cwd)
	}
	if item.Title != cwd || item.Alias != cwd {
		t.Fatalf("cold-start title/alias = (%q, %q), want (%q, %q)", item.Title, item.Alias, cwd, cwd)
	}
	if item.ProviderChoice != provider || item.Model != model || item.ReasoningEffort != reasoning {
		t.Fatalf("persisted launch metadata = %+v", item)
	}

	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	details, err := rehydrated.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	if details.SessionID != created.Session.SessionID {
		t.Fatalf("details.SessionID = %q, want %q", details.SessionID, created.Session.SessionID)
	}
	if details.DisplayName != cwd || details.Provider != provider || details.Model != model {
		t.Fatalf("details = %+v", details)
	}
	if details.RuntimeID != "" {
		t.Fatalf("details.RuntimeID = %q, want empty after cold start", details.RuntimeID)
	}
}

func TestPersistentStubDeleteArchivesSessionRow(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/tmp/archive-me"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	deleted, err := svc.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if !deleted.OK || !deleted.Removed {
		t.Fatalf("DeleteSession() = %+v", deleted)
	}
	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 0 {
		t.Fatalf("len(ListSessions().Items) = %d, want 0 after archive", len(listed.Items))
	}

	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()
	active, err := catalog.ListSessions(context.Background(), false)
	if err != nil {
		t.Fatalf("ListSessions(false) error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("len(ListSessions(false)) = %d, want 0", len(active))
	}
	all, err := catalog.ListSessions(context.Background(), true)
	if err != nil {
		t.Fatalf("ListSessions(true) error = %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(ListSessions(true)) = %d, want 1", len(all))
	}
	if all[0].SessionID != created.Session.SessionID {
		t.Fatalf("archived row session_id = %q, want %q", all[0].SessionID, created.Session.SessionID)
	}
	if all[0].ArchivedAt == nil {
		t.Fatal("archived row ArchivedAt = nil")
	}
}
