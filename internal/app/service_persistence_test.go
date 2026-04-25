package app

import (
	"context"
	"path/filepath"
	"reflect"
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

	sessionID := mustSessionID(t, created.Session.SessionID)
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

func TestPersistentStubColdStartRehydratesQueuedPromptExactlyOnce(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/tmp/queue-project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil {
		t.Fatalf("registry.SetBusy() error = %v", err)
	} else if !ok {
		t.Fatal("registry.SetBusy() ok = false")
	}
	queued, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "recover me"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if len(queued.Queue.Items) != 1 {
		t.Fatalf("len(Enqueue().Queue.Items) = %d, want 1", len(queued.Queue.Items))
	}
	firstID := queued.Queue.Items[0].ID

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart1) error = %v", err)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState(restart1) error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState(restart1).Busy = true, want false")
	}
	if len(state.Queue.Items) != 1 {
		t.Fatalf("len(SessionState(restart1).Queue.Items) = %d, want 1", len(state.Queue.Items))
	}
	if state.Queue.Items[0].ID != firstID || state.Queue.Items[0].Text != "recover me" {
		t.Fatalf("SessionState(restart1).Queue.Items[0] = %+v", state.Queue.Items[0])
	}

	rehydratedAgain, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(2 * time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart2) error = %v", err)
	}
	state, err = rehydratedAgain.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState(restart2) error = %v", err)
	}
	if len(state.Queue.Items) != 1 {
		t.Fatalf("len(SessionState(restart2).Queue.Items) = %d, want 1", len(state.Queue.Items))
	}
	if state.Queue.Items[0].ID != firstID {
		t.Fatalf("SessionState(restart2).Queue.Items[0].ID = %q, want %q", state.Queue.Items[0].ID, firstID)
	}
}

func TestPersistentStubColdStartRehydratesWorkspaceBrowserState(t *testing.T) {
	cfg := persistentTestConfig(t)
	rootDir := t.TempDir()
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: rootDir})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	updated, err := svc.UpdateSessionWorkspace(context.Background(), UpdateSessionWorkspaceRequest{
		SessionID:    sessionID,
		SelectedPath: "nested/file.txt",
		OpenPaths:    []string{"nested", "nested/file.txt"},
		HistoryItems: []WorkspaceHistoryItem{{Path: "README.md", Label: "Readme"}},
	})
	if err != nil {
		t.Fatalf("UpdateSessionWorkspace() error = %v", err)
	}
	if updated.SelectedPath != "nested/file.txt" {
		t.Fatalf("UpdateSessionWorkspace().SelectedPath = %q, want %q", updated.SelectedPath, "nested/file.txt")
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	workspaceState, err := rehydrated.SessionWorkspace(context.Background(), SessionWorkspaceRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionWorkspace() error = %v", err)
	}
	if workspaceState.RootPath != rootDir {
		t.Fatalf("SessionWorkspace().RootPath = %q, want %q", workspaceState.RootPath, rootDir)
	}
	if workspaceState.SelectedPath != "nested/file.txt" {
		t.Fatalf("SessionWorkspace().SelectedPath = %q, want %q", workspaceState.SelectedPath, "nested/file.txt")
	}
	if !reflect.DeepEqual(workspaceState.OpenPaths, []string{"nested/file.txt", "nested"}) {
		t.Fatalf("SessionWorkspace().OpenPaths = %#v, want selected path front and nested dir", workspaceState.OpenPaths)
	}
	if !reflect.DeepEqual(workspaceState.HistoryItems, []WorkspaceHistoryItem{{Path: "nested/file.txt", Label: "file.txt"}, {Path: "README.md", Label: "Readme"}}) {
		t.Fatalf("SessionWorkspace().HistoryItems = %#v", workspaceState.HistoryItems)
	}
}

func TestPersistentStubColdStartRehydratesRecentCwdsAndCwdGroups(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/tmp/project-a"}); err != nil {
		t.Fatalf("CreateSession(project-a) error = %v", err)
	}
	if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: "/tmp/project-b"}); err != nil {
		t.Fatalf("CreateSession(project-b) error = %v", err)
	}
	label := "Project A"
	collapsed := true
	if _, err := svc.EditCwdGroup(context.Background(), EditCwdGroupRequest{CWD: "/tmp/project-a", Label: &label, Collapsed: &collapsed}); err != nil {
		t.Fatalf("EditCwdGroup() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	bootstrap := rehydrated.Bootstrap(context.Background())
	if !reflect.DeepEqual(bootstrap.RecentCwds, []string{"/tmp/project-b", "/tmp/project-a"}) {
		t.Fatalf("Bootstrap().RecentCwds = %#v", bootstrap.RecentCwds)
	}
	meta, ok := bootstrap.CwdGroups["/tmp/project-a"]
	if !ok {
		t.Fatal("Bootstrap().CwdGroups missing /tmp/project-a")
	}
	if meta.Label != label || !meta.Collapsed {
		t.Fatalf("Bootstrap().CwdGroups[/tmp/project-a] = %+v", meta)
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
	sessionID := mustSessionID(t, created.Session.SessionID)
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

func mustSessionID(t *testing.T, raw string) session.SessionID {
	t.Helper()
	sessionID, err := session.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	return sessionID
}
