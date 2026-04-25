package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestOpenSessionCatalogAppliesSchemaMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actrail.db")
	catalog, err := OpenSessionCatalog(path)
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentSchemaVersion)
	}
}

func TestSessionCatalogPersistsQueueWorkspaceAndAppState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actrail.db")
	catalog, err := OpenSessionCatalog(path)
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()

	now := time.Unix(1760000000, 0).UTC()
	if err := catalog.UpsertSessionSnapshot(context.Background(), SessionSnapshotRow{
		Session: SessionRow{
			SessionID:       "s_1",
			Backend:         "pi",
			CWD:             "/tmp/project",
			Title:           "Task",
			Alias:           "Task",
			CreatedAt:       now,
			UpdatedAt:       now,
			ActivityAt:      now,
			Focused:         true,
			PriorityOffset:  0.5,
			ReasoningEffort: "high",
		},
		Queue: []QueueItemRow{{Ordinal: 0, ItemID: "q_1", Text: "recover me", State: "queued"}},
		Workspace: WorkspaceStateRow{
			SelectedPath: "nested/file.txt",
			OpenPaths:    []string{"nested/file.txt", "nested"},
			HistoryItems: []WorkspaceHistoryItemRow{{Ordinal: 0, Path: "nested/file.txt", Label: "file.txt"}},
		},
	}); err != nil {
		t.Fatalf("UpsertSessionSnapshot() error = %v", err)
	}
	if err := catalog.ReplaceAppState(context.Background(), AppStateRow{
		RecentCwds: []string{"/tmp/project", "/tmp/other"},
		CwdGroups:  []CwdGroupRow{{CWD: "/tmp/project", Label: "Project", Collapsed: true}},
	}); err != nil {
		t.Fatalf("ReplaceAppState() error = %v", err)
	}

	snapshots, err := catalog.ListSessionSnapshots(context.Background(), false)
	if err != nil {
		t.Fatalf("ListSessionSnapshots() error = %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("len(ListSessionSnapshots()) = %d, want 1", len(snapshots))
	}
	if len(snapshots[0].Queue) != 1 || snapshots[0].Queue[0].ItemID != "q_1" {
		t.Fatalf("snapshot queue = %+v", snapshots[0].Queue)
	}
	if snapshots[0].Workspace.SelectedPath != "nested/file.txt" {
		t.Fatalf("snapshot workspace = %+v", snapshots[0].Workspace)
	}
	if !reflect.DeepEqual(snapshots[0].Workspace.OpenPaths, []string{"nested/file.txt", "nested"}) {
		t.Fatalf("snapshot workspace open_paths = %#v", snapshots[0].Workspace.OpenPaths)
	}

	appState, err := catalog.LoadAppState(context.Background())
	if err != nil {
		t.Fatalf("LoadAppState() error = %v", err)
	}
	if !reflect.DeepEqual(appState.RecentCwds, []string{"/tmp/project", "/tmp/other"}) {
		t.Fatalf("appState.RecentCwds = %#v", appState.RecentCwds)
	}
	if len(appState.CwdGroups) != 1 || appState.CwdGroups[0].CWD != "/tmp/project" || !appState.CwdGroups[0].Collapsed {
		t.Fatalf("appState.CwdGroups = %+v", appState.CwdGroups)
	}
}

func TestSessionCatalogArchivesWithoutDroppingRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actrail.db")
	catalog, err := OpenSessionCatalog(path)
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()

	now := time.Unix(1760000000, 0).UTC()
	dependency := "s_2"
	row := SessionRow{
		SessionID:           "s_1",
		Backend:             "pi",
		CWD:                 "/tmp/project",
		Title:               "Work item",
		Alias:               "Work item",
		Provider:            "openrouter",
		Model:               "gpt-test",
		ReasoningEffort:     "high",
		CreatedAt:           now,
		UpdatedAt:           now,
		ActivityAt:          now,
		Focused:             true,
		Hidden:              false,
		PriorityOffset:      1.5,
		DependencySessionID: &dependency,
	}
	if err := catalog.UpsertSession(context.Background(), row); err != nil {
		t.Fatalf("UpsertSession(active) error = %v", err)
	}
	active, err := catalog.ListSessions(context.Background(), false)
	if err != nil {
		t.Fatalf("ListSessions(active) error = %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("len(ListSessions(false)) = %d, want 1", len(active))
	}

	archivedAt := now.Add(5 * time.Minute)
	row.Focused = false
	row.UpdatedAt = archivedAt
	row.ActivityAt = archivedAt
	row.ArchivedAt = &archivedAt
	if err := catalog.UpsertSession(context.Background(), row); err != nil {
		t.Fatalf("UpsertSession(archived) error = %v", err)
	}
	active, err = catalog.ListSessions(context.Background(), false)
	if err != nil {
		t.Fatalf("ListSessions(active after archive) error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("len(ListSessions(false)) after archive = %d, want 0", len(active))
	}
	all, err := catalog.ListSessions(context.Background(), true)
	if err != nil {
		t.Fatalf("ListSessions(all) error = %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(ListSessions(true)) = %d, want 1", len(all))
	}
	if all[0].ArchivedAt == nil || !all[0].ArchivedAt.Equal(archivedAt) {
		t.Fatalf("ArchivedAt = %v, want %v", all[0].ArchivedAt, archivedAt)
	}
	stored, ok, err := catalog.LookupSession(context.Background(), "s_1")
	if err != nil {
		t.Fatalf("LookupSession() error = %v", err)
	}
	if !ok {
		t.Fatal("LookupSession() ok = false, want true")
	}
	if stored.ArchivedAt == nil || !stored.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("stored.ArchivedAt = %v, want %v", stored.ArchivedAt, archivedAt)
	}
}
