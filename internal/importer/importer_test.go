package importer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/app"
	"actrail/internal/config"

	_ "modernc.org/sqlite"
)

func TestRunImportsLegacySQLiteAndPreservesSessionFallback(t *testing.T) {
	snapshotAt := time.Unix(1760001000, 0).UTC()
	sourcePath := createLegacySourceDB(t, []string{
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'legacy-session-1', '/tmp/project', '/root/.pi/agent/sessions/legacy-session-1/session.jsonl', '', 'hello', 1760000000, 1760000500, 0)`,
		`INSERT INTO session_ui_state(backend, session_id, alias, focused, hidden, priority_offset, snooze_until, dependency_backend, dependency_session_id)
		 VALUES('pi', 'legacy-session-1', '', 1, 0, 1.25, 1760000600, 'pi', 'dependency-1')`,
		`INSERT INTO hidden_session_keys(key) VALUES('legacy-hidden')`,
		`INSERT INTO session_files(backend, session_id, path, ordinal, last_used_ts)
		 VALUES('pi', 'legacy-session-1', '/tmp/project/nested/file.txt', 0, 1760000700)`,
		`INSERT INTO session_queue_items(backend, session_id, ordinal, text)
		 VALUES('pi', 'legacy-session-1', 0, 'recover me')`,
		`INSERT INTO recent_cwds(cwd, last_used_ts) VALUES('/tmp/project', 1760000700)`,
		`INSERT INTO cwd_groups(cwd, label, collapsed) VALUES('/tmp/project', 'Project', 1)`,
		`INSERT INTO app_kv(namespace, key, value_json) VALUES('voice_settings', 'tts_model', '"gpt-4o-mini-tts"')`,
	})
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "sqlite", "actrail.db")

	report, err := Run(context.Background(), Options{
		SourceSQLitePath: sourcePath,
		TargetSQLitePath: targetPath,
		SnapshotAt:       snapshotAt,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.SourceCounts.SessionKeyUnion != 1 {
		t.Fatalf("report.SourceCounts.SessionKeyUnion = %d, want 1", report.SourceCounts.SessionKeyUnion)
	}
	if report.ImportedCounts.SessionCatalogRows != 1 {
		t.Fatalf("report.ImportedCounts.SessionCatalogRows = %d, want 1", report.ImportedCounts.SessionCatalogRows)
	}
	if report.Validation.WarningCount != 0 {
		t.Fatalf("report.Validation.WarningCount = %d, want 0", report.Validation.WarningCount)
	}

	catalog, err := sqlitestore.OpenSessionCatalog(targetPath)
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()

	snapshots, err := catalog.ListSessionSnapshots(context.Background(), true)
	if err != nil {
		t.Fatalf("ListSessionSnapshots() error = %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("len(ListSessionSnapshots()) = %d, want 1", len(snapshots))
	}
	snapshot := snapshots[0]
	if snapshot.Session.SessionID != "legacy-session-1" {
		t.Fatalf("SessionID = %q, want %q", snapshot.Session.SessionID, "legacy-session-1")
	}
	if snapshot.Session.Title != "" || snapshot.Session.Alias != "" {
		t.Fatalf("imported title/alias = (%q, %q), want empty strings preserved", snapshot.Session.Title, snapshot.Session.Alias)
	}
	if snapshot.Session.CWD != "/tmp/project" {
		t.Fatalf("CWD = %q, want /tmp/project", snapshot.Session.CWD)
	}
	if !snapshot.Session.Focused || snapshot.Session.PriorityOffset != 1.25 {
		t.Fatalf("session ui state = %+v", snapshot.Session)
	}
	if snapshot.Session.DependencySessionID == nil || *snapshot.Session.DependencySessionID != "dependency-1" {
		t.Fatalf("DependencySessionID = %v, want dependency-1", snapshot.Session.DependencySessionID)
	}
	if len(snapshot.Queue) != 1 || snapshot.Queue[0].ItemID != "legacy:legacy-session-1:0" || snapshot.Queue[0].Text != "recover me" || snapshot.Queue[0].State != "queued" {
		t.Fatalf("snapshot.Queue = %+v", snapshot.Queue)
	}
	if snapshot.Workspace.SelectedPath != "nested/file.txt" {
		t.Fatalf("SelectedPath = %q, want nested/file.txt", snapshot.Workspace.SelectedPath)
	}
	if len(snapshot.Workspace.HistoryItems) != 1 || snapshot.Workspace.HistoryItems[0].Path != "nested/file.txt" || snapshot.Workspace.HistoryItems[0].Label != "file.txt" {
		t.Fatalf("Workspace.HistoryItems = %+v", snapshot.Workspace.HistoryItems)
	}

	refs, err := catalog.ListSessionSourceRefs(context.Background())
	if err != nil {
		t.Fatalf("ListSessionSourceRefs() error = %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("len(ListSessionSourceRefs()) = %d, want 1", len(refs))
	}
	if refs[0].SourcePath != "/root/.pi/agent/sessions/legacy-session-1/session.jsonl" || refs[0].FirstUserMessage != "hello" || !refs[0].HasLegacySessionUIState {
		t.Fatalf("refs[0] = %+v", refs[0])
	}

	appState, err := catalog.LoadAppState(context.Background())
	if err != nil {
		t.Fatalf("LoadAppState() error = %v", err)
	}
	if len(appState.RecentCwds) != 1 || appState.RecentCwds[0] != "/tmp/project" {
		t.Fatalf("RecentCwds = %#v", appState.RecentCwds)
	}
	if len(appState.CwdGroups) != 1 || appState.CwdGroups[0].Label != "Project" || !appState.CwdGroups[0].Collapsed {
		t.Fatalf("CwdGroups = %+v", appState.CwdGroups)
	}

	cfg := config.Load()
	cfg.Storage.DataDir = targetDir
	svc, err := app.NewPersistentStubForTest(cfg, func() time.Time { return snapshotAt.Add(time.Hour) }, app.RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := svc.ListSessions(context.Background(), app.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	item := listed.Items[0]
	if item.SessionID != "legacy-session-1" {
		t.Fatalf("ListSessions().Items[0].SessionID = %q, want legacy-session-1", item.SessionID)
	}
	if item.DisplayName != "/tmp/project" {
		t.Fatalf("DisplayName = %q, want /tmp/project", item.DisplayName)
	}
	if item.Title != "" || item.Alias != "" {
		t.Fatalf("ListSessions().Items[0] title/alias = (%q, %q), want empty fallback inputs", item.Title, item.Alias)
	}
}

func TestRunPersistsSessionUIStateProvenanceForDefaultValuedRows(t *testing.T) {
	snapshotAt := time.Unix(1760050000, 0).UTC()
	sourcePath := createLegacySourceDB(t, []string{
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'with-default-ui', '/tmp/default-ui', '/tmp/pi/with-default-ui.jsonl', 'Default UI', 'prompt default', 1760049000, 1760049100, 0)`,
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'without-ui', '/tmp/without-ui', '/tmp/pi/without-ui.jsonl', 'Without UI', 'prompt none', 1760049200, 1760049300, 0)`,
		`INSERT INTO session_ui_state(backend, session_id, alias, focused, hidden, priority_offset, snooze_until, dependency_backend, dependency_session_id)
		 VALUES('pi', 'with-default-ui', '', 0, 0, 0, NULL, NULL, NULL)`,
	})
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "sqlite", "actrail.db")

	if _, err := Run(context.Background(), Options{
		SourceSQLitePath: sourcePath,
		TargetSQLitePath: targetPath,
		SnapshotAt:       snapshotAt,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	catalog, err := sqlitestore.OpenSessionCatalog(targetPath)
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	refs, err := catalog.ListSessionSourceRefs(context.Background())
	if err != nil {
		t.Fatalf("ListSessionSourceRefs() error = %v", err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	catalog, err = sqlitestore.OpenSessionCatalog(targetPath)
	if err != nil {
		t.Fatalf("OpenSessionCatalog(reload) error = %v", err)
	}
	defer func() { _ = catalog.Close() }()
	refs, err = catalog.ListSessionSourceRefs(context.Background())
	if err != nil {
		t.Fatalf("ListSessionSourceRefs(reload) error = %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("len(ListSessionSourceRefs()) = %d, want 2", len(refs))
	}
	provenanceBySessionID := map[string]bool{}
	for _, row := range refs {
		provenanceBySessionID[row.SessionID] = row.HasLegacySessionUIState
	}
	if !provenanceBySessionID["with-default-ui"] {
		t.Fatalf("with-default-ui provenance = %v, want true", provenanceBySessionID["with-default-ui"])
	}
	if provenanceBySessionID["without-ui"] {
		t.Fatalf("without-ui provenance = %v, want false", provenanceBySessionID["without-ui"])
	}
}

func TestRunUsesSessionsRowsForVisibleTabsAndPreservesOrphansAsWarnings(t *testing.T) {
	snapshotAt := time.Unix(1760100000, 0).UTC()
	sourcePath := createLegacySourceDB(t, []string{
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'visible-a', '/tmp/project-a', '/tmp/pi/visible-a.jsonl', 'Visible A', 'prompt a', 1760099000, 1760099100, 0)`,
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'hidden-a', '/tmp/project-hidden', '/tmp/pi/hidden-a.jsonl', 'Hidden A', 'prompt hidden', 1760099200, 1760099300, 0)`,
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'visible-b', '/tmp/project-b', '/tmp/pi/visible-b.jsonl', 'Visible B', 'prompt b', 1760099400, 1760099500, 0)`,
		`INSERT INTO session_ui_state(backend, session_id, alias, focused, hidden, priority_offset, snooze_until, dependency_backend, dependency_session_id)
		 VALUES('pi', 'visible-a', 'Alias A', 1, 0, 0.5, NULL, NULL, NULL)`,
		`INSERT INTO session_ui_state(backend, session_id, alias, focused, hidden, priority_offset, snooze_until, dependency_backend, dependency_session_id)
		 VALUES('pi', 'orphan-ui', 'Orphan Alias', 1, 1, 0.5, NULL, NULL, NULL)`,
		`INSERT INTO hidden_session_keys(key) VALUES('history:pi:hidden-a')`,
		`INSERT INTO session_files(backend, session_id, path, ordinal, last_used_ts)
		 VALUES('pi', 'visible-a', '/tmp/project-a/nested/file-a.txt', 0, 1760099600)`,
		`INSERT INTO session_files(backend, session_id, path, ordinal, last_used_ts)
		 VALUES('codex', 'orphan-file', '/tmp/outside/file-a.py', 0, 1760099700)`,
		`INSERT INTO session_queue_items(backend, session_id, ordinal, text)
		 VALUES('pi', 'visible-b', 0, 'recover visible-b')`,
		`INSERT INTO session_queue_items(backend, session_id, ordinal, text)
		 VALUES('pi', 'orphan-queue', 0, 'recover orphan')`,
		`INSERT INTO app_kv(namespace, key, value_json) VALUES('voice_settings', 'tts_enabled_for_final_response', 'false')`,
		`INSERT INTO legacy_import_unmapped(id, source_name, legacy_key, payload_json, imported_at)
		 VALUES(1, 'session_sidebar.json', 'legacy-sidebar-key', '{"alias":"old"}', 1760099800)`,
	})
	sideDir := t.TempDir()
	writeJSONFile(t, filepath.Join(sideDir, "session_aliases.json"), `{"visible-a":"Alias A"}`, snapshotAt.Add(-2*time.Hour))
	writeJSONFile(t, filepath.Join(sideDir, "session_sidebar.json"), `{"visible-b":{"alias":"old"}}`, snapshotAt.Add(-96*time.Hour))
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "sqlite", "actrail.db")

	report, err := Run(context.Background(), Options{
		SourceSQLitePath: sourcePath,
		TargetSQLitePath: targetPath,
		SideDir:          sideDir,
		SnapshotAt:       snapshotAt,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.SourceCounts.Sessions != 3 {
		t.Fatalf("report.SourceCounts.Sessions = %d, want 3", report.SourceCounts.Sessions)
	}
	if report.SourceCounts.SessionKeyUnion != 6 {
		t.Fatalf("report.SourceCounts.SessionKeyUnion = %d, want 6", report.SourceCounts.SessionKeyUnion)
	}
	if report.ImportedCounts.SessionCatalogRows != 3 {
		t.Fatalf("report.ImportedCounts.SessionCatalogRows = %d, want 3", report.ImportedCounts.SessionCatalogRows)
	}
	if report.ImportedCounts.SessionSourceRefRows != 3 {
		t.Fatalf("report.ImportedCounts.SessionSourceRefRows = %d, want 3", report.ImportedCounts.SessionSourceRefRows)
	}
	if report.Validation.OrphanSessionUIState != 1 {
		t.Fatalf("report.Validation.OrphanSessionUIState = %d, want 1", report.Validation.OrphanSessionUIState)
	}
	if report.Validation.OrphanSessionFiles != 1 {
		t.Fatalf("report.Validation.OrphanSessionFiles = %d, want 1", report.Validation.OrphanSessionFiles)
	}
	if report.Validation.OrphanSessionQueueItems != 1 {
		t.Fatalf("report.Validation.OrphanSessionQueueItems = %d, want 1", report.Validation.OrphanSessionQueueItems)
	}
	if report.Validation.WarningCount != 4 {
		t.Fatalf("report.Validation.WarningCount = %d, want 4", report.Validation.WarningCount)
	}
	if report.Validation.UnmappedCount != 1 {
		t.Fatalf("report.Validation.UnmappedCount = %d, want 1", report.Validation.UnmappedCount)
	}
	if report.ImportedCounts.SessionFileMappedRows != 1 || report.ImportedCounts.SessionFileSkippedRows != 0 {
		t.Fatalf("session file import counts = %+v", report.ImportedCounts)
	}
	if len(report.SideJSON) != 2 {
		t.Fatalf("len(report.SideJSON) = %d, want 2", len(report.SideJSON))
	}
	aliasAudit := sideAuditByName(t, report.SideJSON, "session_aliases.json")
	if !aliasAudit.Fresh || aliasAudit.Ignored {
		t.Fatalf("session_aliases.json audit = %+v", aliasAudit)
	}
	sidebarAudit := sideAuditByName(t, report.SideJSON, "session_sidebar.json")
	if sidebarAudit.Fresh || !sidebarAudit.Ignored {
		t.Fatalf("session_sidebar.json audit = %+v", sidebarAudit)
	}

	catalog, err := sqlitestore.OpenSessionCatalog(targetPath)
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()

	snapshots, err := catalog.ListSessionSnapshots(context.Background(), true)
	if err != nil {
		t.Fatalf("ListSessionSnapshots() error = %v", err)
	}
	if len(snapshots) != 3 {
		t.Fatalf("len(ListSessionSnapshots()) = %d, want 3", len(snapshots))
	}
	for _, snapshot := range snapshots {
		if snapshot.Session.SessionID == "orphan-ui" || snapshot.Session.SessionID == "orphan-file" || snapshot.Session.SessionID == "orphan-queue" {
			t.Fatalf("orphan placeholder leaked into session catalog: %+v", snapshot.Session)
		}
		switch snapshot.Session.SessionID {
		case "hidden-a":
			if !snapshot.Session.Hidden {
				t.Fatalf("hidden-a session = %+v, want hidden=true from hidden_session_keys", snapshot.Session)
			}
		case "visible-a":
			if snapshot.Session.Alias != "Alias A" || snapshot.Workspace.SelectedPath != "nested/file-a.txt" {
				t.Fatalf("visible-a snapshot = %+v workspace=%+v", snapshot.Session, snapshot.Workspace)
			}
		case "visible-b":
			if len(snapshot.Queue) != 1 || snapshot.Queue[0].Text != "recover visible-b" {
				t.Fatalf("visible-b queue = %+v", snapshot.Queue)
			}
		}
	}

	warnings, err := catalog.ListMigrationWarnings(context.Background())
	if err != nil {
		t.Fatalf("ListMigrationWarnings() error = %v", err)
	}
	if len(warnings) != 4 {
		t.Fatalf("len(ListMigrationWarnings()) = %d, want 4", len(warnings))
	}
	codes := make([]string, 0, len(warnings))
	for _, row := range warnings {
		codes = append(codes, row.WarningCode)
	}
	for _, want := range []string{"orphan_session_ui_state", "orphan_session_files", "orphan_session_queue_items", "legacy_import_unmapped"} {
		if !contains(codes, want) {
			t.Fatalf("warning codes = %#v, missing %q", codes, want)
		}
	}

	hiddenKeys, err := catalog.ListHiddenSessionKeys(context.Background())
	if err != nil {
		t.Fatalf("ListHiddenSessionKeys() error = %v", err)
	}
	if len(hiddenKeys) != 1 || hiddenKeys[0].Key != "history:pi:hidden-a" {
		t.Fatalf("hidden keys = %+v", hiddenKeys)
	}
	appKV, err := catalog.ListAppKV(context.Background())
	if err != nil {
		t.Fatalf("ListAppKV() error = %v", err)
	}
	if len(appKV) != 1 || appKV[0].Namespace != "voice_settings" || appKV[0].Key != "tts_enabled_for_final_response" {
		t.Fatalf("ListAppKV() = %+v", appKV)
	}
	provenance, err := catalog.ListImportProvenance(context.Background())
	if err != nil {
		t.Fatalf("ListImportProvenance() error = %v", err)
	}
	if len(provenance) != 1 {
		t.Fatalf("len(ListImportProvenance()) = %d, want 1", len(provenance))
	}
	if !strings.Contains(provenance[0].DetailsJSON, "session_aliases.json") {
		t.Fatalf("import provenance details missing side-json audit: %s", provenance[0].DetailsJSON)
	}

	cfg := config.Load()
	cfg.Storage.DataDir = targetDir
	svc, err := app.NewPersistentStubForTest(cfg, func() time.Time { return snapshotAt.Add(time.Hour) }, app.RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := svc.ListSessions(context.Background(), app.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("len(ListSessions().Items) = %d, want 2 visible sessions", len(listed.Items))
	}
	gotIDs := []string{listed.Items[0].SessionID, listed.Items[1].SessionID}
	if strings.Join(gotIDs, ",") != "visible-a,visible-b" {
		t.Fatalf("visible session ids = %#v, want [visible-a visible-b]", gotIDs)
	}
}

func TestSessionHiddenByKeysRecognizesCodoxearKeyForms(t *testing.T) {
	key := sessionKey{Backend: "pi", SessionID: "hidden-a"}
	cases := []struct {
		name string
		keys []string
		want bool
	}{
		{name: "raw session id", keys: []string{"hidden-a"}, want: true},
		{name: "session prefix", keys: []string{"session:hidden-a"}, want: true},
		{name: "history prefix", keys: []string{"history:pi:hidden-a"}, want: true},
		{name: "thread prefix", keys: []string{"thread:pi:hidden-a"}, want: true},
		{name: "resume prefix", keys: []string{"resume:pi:hidden-a"}, want: true},
		{name: "wrong backend", keys: []string{"thread:codex:hidden-a"}, want: false},
		{name: "wrong session id", keys: []string{"session:visible-a"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionHiddenByKeys(key, hiddenSessionKeySet(tc.keys))
			if got != tc.want {
				t.Fatalf("sessionHiddenByKeys(%q) = %v, want %v", tc.keys, got, tc.want)
			}
		})
	}
}

func TestRunMarksSessionsHiddenForAllCodoxearKeyForms(t *testing.T) {
	snapshotAt := time.Unix(1760200000, 0).UTC()
	sourcePath := createLegacySourceDB(t, []string{
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'hidden-raw', '/tmp/hidden-raw', '/tmp/pi/hidden-raw.jsonl', 'Hidden Raw', 'prompt raw', 1760199000, 1760199100, 0)`,
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'hidden-session', '/tmp/hidden-session', '/tmp/pi/hidden-session.jsonl', 'Hidden Session', 'prompt session', 1760199200, 1760199300, 0)`,
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'hidden-history', '/tmp/hidden-history', '/tmp/pi/hidden-history.jsonl', 'Hidden History', 'prompt history', 1760199400, 1760199500, 0)`,
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'hidden-thread', '/tmp/hidden-thread', '/tmp/pi/hidden-thread.jsonl', 'Hidden Thread', 'prompt thread', 1760199600, 1760199700, 0)`,
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'hidden-resume', '/tmp/hidden-resume', '/tmp/pi/hidden-resume.jsonl', 'Hidden Resume', 'prompt resume', 1760199800, 1760199900, 0)`,
		`INSERT INTO sessions(backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup)
		 VALUES('pi', 'visible-a', '/tmp/visible-a', '/tmp/pi/visible-a.jsonl', 'Visible A', 'prompt visible', 1760200000, 1760200100, 0)`,
		`INSERT INTO hidden_session_keys(key) VALUES('hidden-raw')`,
		`INSERT INTO hidden_session_keys(key) VALUES('session:hidden-session')`,
		`INSERT INTO hidden_session_keys(key) VALUES('history:pi:hidden-history')`,
		`INSERT INTO hidden_session_keys(key) VALUES('thread:pi:hidden-thread')`,
		`INSERT INTO hidden_session_keys(key) VALUES('resume:pi:hidden-resume')`,
	})
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "sqlite", "actrail.db")

	if _, err := Run(context.Background(), Options{
		SourceSQLitePath: sourcePath,
		TargetSQLitePath: targetPath,
		SnapshotAt:       snapshotAt,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	catalog, err := sqlitestore.OpenSessionCatalog(targetPath)
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()

	snapshots, err := catalog.ListSessionSnapshots(context.Background(), true)
	if err != nil {
		t.Fatalf("ListSessionSnapshots() error = %v", err)
	}
	if len(snapshots) != 6 {
		t.Fatalf("len(ListSessionSnapshots()) = %d, want 6", len(snapshots))
	}
	wantHidden := map[string]bool{
		"hidden-history": true,
		"hidden-raw":     true,
		"hidden-resume":  true,
		"hidden-session": true,
		"hidden-thread":  true,
		"visible-a":      false,
	}
	for _, snapshot := range snapshots {
		want, ok := wantHidden[snapshot.Session.SessionID]
		if !ok {
			t.Fatalf("unexpected session snapshot = %+v", snapshot.Session)
		}
		if snapshot.Session.Hidden != want {
			t.Fatalf("session %q hidden = %v, want %v", snapshot.Session.SessionID, snapshot.Session.Hidden, want)
		}
	}

	cfg := config.Load()
	cfg.Storage.DataDir = targetDir
	svc, err := app.NewPersistentStubForTest(cfg, func() time.Time { return snapshotAt.Add(time.Hour) }, app.RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := svc.ListSessions(context.Background(), app.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1 visible session", len(listed.Items))
	}
	if listed.Items[0].SessionID != "visible-a" {
		t.Fatalf("visible session id = %q, want visible-a", listed.Items[0].SessionID)
	}
}

func createLegacySourceDB(t *testing.T, inserts []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codoxear.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	statements := []string{
		`CREATE TABLE sessions (
			backend TEXT NOT NULL,
			session_id TEXT NOT NULL,
			cwd TEXT,
			source_path TEXT,
			title TEXT,
			first_user_message TEXT,
			created_at REAL,
			updated_at REAL,
			pending_startup INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(backend, session_id)
		)`,
		`CREATE TABLE session_ui_state (
			backend TEXT NOT NULL,
			session_id TEXT NOT NULL,
			alias TEXT,
			focused INTEGER NOT NULL DEFAULT 0,
			hidden INTEGER NOT NULL DEFAULT 0,
			priority_offset REAL NOT NULL DEFAULT 0,
			snooze_until REAL,
			dependency_backend TEXT,
			dependency_session_id TEXT,
			PRIMARY KEY(backend, session_id)
		)`,
		`CREATE TABLE hidden_session_keys (key TEXT PRIMARY KEY)`,
		`CREATE TABLE session_files (
			backend TEXT NOT NULL,
			session_id TEXT NOT NULL,
			path TEXT NOT NULL,
			ordinal INTEGER NOT NULL,
			last_used_ts REAL,
			PRIMARY KEY(backend, session_id, path)
		)`,
		`CREATE TABLE session_queue_items (
			backend TEXT NOT NULL,
			session_id TEXT NOT NULL,
			ordinal INTEGER NOT NULL,
			text TEXT NOT NULL,
			PRIMARY KEY(backend, session_id, ordinal)
		)`,
		`CREATE TABLE recent_cwds (cwd TEXT PRIMARY KEY, last_used_ts REAL NOT NULL)`,
		`CREATE TABLE cwd_groups (cwd TEXT PRIMARY KEY, label TEXT, collapsed INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE app_kv (
			namespace TEXT NOT NULL,
			key TEXT NOT NULL,
			value_json TEXT NOT NULL,
			PRIMARY KEY(namespace, key)
		)`,
		`CREATE TABLE legacy_import_unmapped (
			id INTEGER PRIMARY KEY,
			source_name TEXT NOT NULL,
			legacy_key TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			imported_at REAL NOT NULL
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("db.Exec(schema) error = %v", err)
		}
	}
	for _, stmt := range inserts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("db.Exec(insert) error = %v\nstmt=%s", err, stmt)
		}
	}
	return path
}

func writeJSONFile(t *testing.T, path, body string, modTime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("os.Chtimes(%q) error = %v", path, err)
	}
}

func sideAuditByName(t *testing.T, items []SideJSONAudit, name string) SideJSONAudit {
	t.Helper()
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("side audit %q not found in %+v", name, items)
	return SideJSONAudit{}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
