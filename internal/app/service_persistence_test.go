package app

import (
	"context"
	"os"
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

type importedPIDetachedFixture struct {
	SessionID           string
	CWD                 string
	Title               string
	UpdatedAt           time.Time
	ActivityAt          time.Time
	SourcePath          string
	FirstUserMessage    string
	PriorityOffset      float64
	SnoozeUntil         *time.Time
	DependencySessionID *string
}

func seedImportedPIDetachedSessions(t *testing.T, cfg config.Config, now time.Time, fixtures ...importedPIDetachedFixture) {
	t.Helper()
	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()

	snapshots := make([]sqlitestore.SessionSnapshotRow, 0, len(fixtures))
	refs := make([]sqlitestore.SessionSourceRefRow, 0, len(fixtures))
	createdAt := now.Add(-48 * time.Hour)
	for _, fixture := range fixtures {
		snapshots = append(snapshots, sqlitestore.SessionSnapshotRow{
			Session: sqlitestore.SessionRow{
				SessionID:           fixture.SessionID,
				Backend:             "pi",
				CWD:                 fixture.CWD,
				Title:               fixture.Title,
				CreatedAt:           createdAt,
				UpdatedAt:           fixture.UpdatedAt,
				ActivityAt:          fixture.ActivityAt,
				PriorityOffset:      fixture.PriorityOffset,
				SnoozeUntil:         fixture.SnoozeUntil,
				DependencySessionID: fixture.DependencySessionID,
			},
			Queue: []sqlitestore.QueueItemRow{},
			Workspace: sqlitestore.WorkspaceStateRow{
				OpenPaths:    []string{},
				HistoryItems: []sqlitestore.WorkspaceHistoryItemRow{},
			},
		})
		refs = append(refs, sqlitestore.SessionSourceRefRow{
			SessionID:        fixture.SessionID,
			Backend:          "pi",
			SourcePath:       fixture.SourcePath,
			FirstUserMessage: fixture.FirstUserMessage,
		})
	}
	if err := catalog.ReplaceImportBundle(context.Background(), sqlitestore.ImportBundle{
		Sessions:          snapshots,
		SessionSourceRefs: refs,
		AppState:          sqlitestore.AppStateRow{RecentCwds: []string{}, CwdGroups: []sqlitestore.CwdGroupRow{}},
		HiddenSessionKeys: []sqlitestore.HiddenSessionKeyRow{},
		AppKV:             []sqlitestore.AppKVRow{},
		Warnings:          []sqlitestore.MigrationWarningRow{},
		Provenance: sqlitestore.ImportProvenanceRow{
			Source:      "fixture",
			SnapshotAt:  now,
			DetailsJSON: `{"fixture":"imported-pi-detached"}`,
		},
	}); err != nil {
		t.Fatalf("ReplaceImportBundle() error = %v", err)
	}
}

func writeImportedPISourceFile(t *testing.T, dir, name string, activity time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	if err := os.Chtimes(path, activity, activity); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", path, err)
	}
	return path
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

func TestPersistentStubColdStartResumeCandidatesUseImportedPISourceActivity(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourceDir := t.TempDir()
	olderActivity := now.Add(-2 * time.Hour)
	newerActivity := now.Add(-30 * time.Minute)
	olderSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-1.jsonl", olderActivity)
	newerSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-2.jsonl", newerActivity)
	cwd := "/workspace/shared"
	seedImportedPIDetachedSessions(t, cfg, now,
		importedPIDetachedFixture{
			SessionID:  "imported-pi-1",
			CWD:        cwd,
			Title:      "Imported Pi 1",
			UpdatedAt:  now.Add(-10 * time.Minute),
			ActivityAt: now.Add(-10 * time.Minute),
			SourcePath: olderSourcePath,
		},
		importedPIDetachedFixture{
			SessionID:  "imported-pi-2",
			CWD:        cwd,
			Title:      "Imported Pi 2",
			UpdatedAt:  now.Add(-3 * time.Hour),
			ActivityAt: now.Add(-3 * time.Hour),
			SourcePath: newerSourcePath,
		},
	)

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	resume, err := rehydrated.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          cwd,
		AgentBackend: "pi",
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	if len(resume.Sessions) != 2 {
		t.Fatalf("len(SessionResumeCandidates().Sessions) = %d, want 2", len(resume.Sessions))
	}
	if resume.Sessions[0].SessionID != "imported-pi-2" || resume.Sessions[1].SessionID != "imported-pi-1" {
		t.Fatalf("SessionResumeCandidates() order = [%q %q], want [imported-pi-2 imported-pi-1]", resume.Sessions[0].SessionID, resume.Sessions[1].SessionID)
	}
	if resume.Sessions[0].UpdatedTS != timestampSeconds(newerActivity) {
		t.Fatalf("SessionResumeCandidates().Sessions[0].UpdatedTS = %v, want %v", resume.Sessions[0].UpdatedTS, timestampSeconds(newerActivity))
	}
	if resume.Sessions[1].UpdatedTS != timestampSeconds(olderActivity) {
		t.Fatalf("SessionResumeCandidates().Sessions[1].UpdatedTS = %v, want %v", resume.Sessions[1].UpdatedTS, timestampSeconds(olderActivity))
	}
}

func TestPersistentStubColdStartResumeCandidatesMatchSidebarOrderingForDemotedImportedSessions(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourceDir := t.TempDir()
	activeActivity := now.Add(-2 * time.Hour)
	blockedActivity := now.Add(-5 * time.Minute)
	snoozedActivity := now.Add(-2 * time.Minute)
	activeSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-1.jsonl", activeActivity)
	blockedSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-2.jsonl", blockedActivity)
	snoozedSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-3.jsonl", snoozedActivity)
	cwd := "/workspace/shared"
	priority := 1.0
	blockedDependency := "imported-pi-1"
	snoozeUntil := now.Add(2 * time.Hour)
	seedImportedPIDetachedSessions(t, cfg, now,
		importedPIDetachedFixture{
			SessionID:      "imported-pi-1",
			CWD:            cwd,
			Title:          "Active",
			UpdatedAt:      now.Add(-90 * time.Minute),
			ActivityAt:     now.Add(-90 * time.Minute),
			SourcePath:     activeSourcePath,
			PriorityOffset: 0,
		},
		importedPIDetachedFixture{
			SessionID:           "imported-pi-2",
			CWD:                 cwd,
			Title:               "Blocked",
			UpdatedAt:           now.Add(-30 * time.Second),
			ActivityAt:          now.Add(-30 * time.Second),
			SourcePath:          blockedSourcePath,
			PriorityOffset:      priority,
			DependencySessionID: &blockedDependency,
		},
		importedPIDetachedFixture{
			SessionID:      "imported-pi-3",
			CWD:            cwd,
			Title:          "Snoozed",
			UpdatedAt:      now.Add(-15 * time.Second),
			ActivityAt:     now.Add(-15 * time.Second),
			SourcePath:     snoozedSourcePath,
			PriorityOffset: priority,
			SnoozeUntil:    &snoozeUntil,
		},
	)

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	want := []string{"imported-pi-1", "imported-pi-3", "imported-pi-2"}
	listedOrder := make([]string, 0, len(want))
	for _, item := range listed.Items {
		if item.CWD == cwd && item.AgentBackend == "pi" {
			listedOrder = append(listedOrder, item.SessionID)
		}
	}
	assertSessionIDOrder(t, "ListSessions() filtered", listedOrder, want)

	resume, err := rehydrated.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{CWD: cwd, AgentBackend: "pi"})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	resumeOrder := make([]string, 0, len(resume.Sessions))
	for _, item := range resume.Sessions {
		resumeOrder = append(resumeOrder, item.SessionID)
	}
	assertSessionIDOrder(t, "SessionResumeCandidates()", resumeOrder, want)
}

func TestPersistentStubColdStartSessionDetailsUsesImportedPIDisplayTimestamp(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourcePath := writeImportedPISourceFile(t, t.TempDir(), "imported-pi.jsonl", now.Add(-45*time.Minute))
	recordedUpdatedAt := now.Add(-5 * time.Minute)
	recordedActivityAt := now.Add(-2 * time.Hour)
	seedImportedPIDetachedSessions(t, cfg, now, importedPIDetachedFixture{
		SessionID:  "imported-pi-1",
		CWD:        "/workspace/details",
		Title:      "Imported Pi",
		UpdatedAt:  recordedUpdatedAt,
		ActivityAt: recordedActivityAt,
		SourcePath: sourcePath,
	})

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	sessionID := mustSessionID(t, "imported-pi-1")
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	details, err := rehydrated.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	wantDisplayUpdated := timestampSeconds(now.Add(-45 * time.Minute))
	if details.LastUpdatedTS != wantDisplayUpdated {
		t.Fatalf("SessionDetails().LastUpdatedTS = %v, want %v", details.LastUpdatedTS, wantDisplayUpdated)
	}
	if details.LastUpdatedTS != listed.Items[0].LastUpdatedTS || details.LastUpdatedTS != listed.Items[0].UpdatedTS {
		t.Fatalf("SessionDetails/ListSessions timestamps = (%v, %v, %v), want identical display timestamp", details.LastUpdatedTS, listed.Items[0].LastUpdatedTS, listed.Items[0].UpdatedTS)
	}
	if details.LastActivityTS != timestampSeconds(recordedActivityAt) {
		t.Fatalf("SessionDetails().LastActivityTS = %v, want %v", details.LastActivityTS, timestampSeconds(recordedActivityAt))
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

func TestPersistentStubSessionMessagesLoadsImportedPIHistoryFromSourcePath(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()

	sourcePath := filepath.Join("..", "domain", "pi", "testdata", "runtime_session.jsonl")
	if err := catalog.ReplaceImportBundle(context.Background(), sqlitestore.ImportBundle{
		Sessions: []sqlitestore.SessionSnapshotRow{{
			Session: sqlitestore.SessionRow{
				SessionID:   "imported-pi-1",
				Backend:     "pi",
				CWD:         "/workspace/codoxear",
				Title:       "Imported Pi",
				CreatedAt:   now,
				UpdatedAt:   now,
				ActivityAt:  now,
				Focused:     false,
				Hidden:      false,
				ArchivedAt:  nil,
				SnoozeUntil: nil,
			},
			Queue: []sqlitestore.QueueItemRow{},
			Workspace: sqlitestore.WorkspaceStateRow{
				OpenPaths:    []string{},
				HistoryItems: []sqlitestore.WorkspaceHistoryItemRow{},
			},
		}},
		SessionSourceRefs: []sqlitestore.SessionSourceRefRow{{
			SessionID:        "imported-pi-1",
			Backend:          "pi",
			SourcePath:       sourcePath,
			FirstUserMessage: "Summarize the current repository state.",
		}},
		AppState:          sqlitestore.AppStateRow{RecentCwds: []string{}, CwdGroups: []sqlitestore.CwdGroupRow{}},
		HiddenSessionKeys: []sqlitestore.HiddenSessionKeyRow{},
		AppKV:             []sqlitestore.AppKVRow{},
		Warnings:          []sqlitestore.MigrationWarningRow{},
		Provenance: sqlitestore.ImportProvenanceRow{
			Source:      "fixture",
			SnapshotAt:  now,
			DetailsJSON: `{"fixture":"runtime_session.jsonl"}`,
		},
	}); err != nil {
		t.Fatalf("ReplaceImportBundle() error = %v", err)
	}

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	sessionID := mustSessionID(t, "imported-pi-1")

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	if listed.Items[0].FirstUserMessage != "Summarize the current repository state." {
		t.Fatalf("ListSessions().Items[0].FirstUserMessage = %q", listed.Items[0].FirstUserMessage)
	}

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 2 {
		t.Fatalf("len(SessionMessages().Items) = %d, want 2", len(messages.Items))
	}
	if messages.Items[0].Seq != 1 || messages.Items[0].Role != "user" || messages.Items[0].Text != "Summarize the current repository state." {
		t.Fatalf("SessionMessages().Items[0] = %+v", messages.Items[0])
	}
	if messages.Items[1].Seq != 2 || messages.Items[1].Role != "assistant" || messages.Items[1].Text == "" {
		t.Fatalf("SessionMessages().Items[1] = %+v", messages.Items[1])
	}
	if messages.TailSeq != 2 || messages.HasMore {
		t.Fatalf("SessionMessages() = %+v", messages)
	}

	latest, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 1})
	if err != nil {
		t.Fatalf("SessionMessages(limit=1) error = %v", err)
	}
	if len(latest.Items) != 1 || latest.Items[0].Seq != 2 || latest.Items[0].Role != "assistant" {
		t.Fatalf("SessionMessages(limit=1) = %+v", latest)
	}
	if !latest.HasMore || latest.NextBeforeSeq == nil || *latest.NextBeforeSeq != 2 {
		t.Fatalf("SessionMessages(limit=1) paging = %+v", latest)
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
