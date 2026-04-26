package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/config"
)

func TestPersistentStubListSessionsPrefersImportedPISourceActivityWhenLegacyUIStateWasImported(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourceDir := t.TempDir()
	olderSourcePath := filepath.Join(sourceDir, "imported-pi-1.jsonl")
	newerSourcePath := filepath.Join(sourceDir, "imported-pi-2.jsonl")
	for _, path := range []string{olderSourcePath, newerSourcePath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	olderActivity := time.Unix(1759992800, 0).UTC()
	newerActivity := time.Unix(1759996400, 0).UTC()
	if err := os.Chtimes(olderSourcePath, olderActivity, olderActivity); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", olderSourcePath, err)
	}
	if err := os.Chtimes(newerSourcePath, newerActivity, newerActivity); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", newerSourcePath, err)
	}

	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()
	createdAt := now.Add(-48 * time.Hour)
	if err := catalog.ReplaceImportBundle(context.Background(), sqlitestore.ImportBundle{
		Sessions: []sqlitestore.SessionSnapshotRow{
			{
				Session: sqlitestore.SessionRow{
					SessionID:  "imported-pi-1",
					Backend:    "pi",
					CWD:        "/workspace/older",
					Title:      "Imported Pi 1",
					CreatedAt:  createdAt,
					UpdatedAt:  now.Add(-30 * time.Minute),
					ActivityAt: now.Add(-30 * time.Minute),
				},
				Queue: []sqlitestore.QueueItemRow{},
				Workspace: sqlitestore.WorkspaceStateRow{
					OpenPaths:    []string{},
					HistoryItems: []sqlitestore.WorkspaceHistoryItemRow{},
				},
			},
			{
				Session: sqlitestore.SessionRow{
					SessionID:  "imported-pi-2",
					Backend:    "pi",
					CWD:        "/workspace/newer",
					Title:      "Imported Pi 2",
					CreatedAt:  createdAt,
					UpdatedAt:  now.Add(-90 * time.Minute),
					ActivityAt: now.Add(-90 * time.Minute),
				},
				Queue: []sqlitestore.QueueItemRow{},
				Workspace: sqlitestore.WorkspaceStateRow{
					OpenPaths:    []string{},
					HistoryItems: []sqlitestore.WorkspaceHistoryItemRow{},
				},
			},
		},
		SessionSourceRefs: []sqlitestore.SessionSourceRefRow{
			{SessionID: "imported-pi-1", Backend: "pi", SourcePath: olderSourcePath, HasLegacySessionUIState: true},
			{SessionID: "imported-pi-2", Backend: "pi", SourcePath: newerSourcePath, HasLegacySessionUIState: true},
		},
		AppState:          sqlitestore.AppStateRow{RecentCwds: []string{}, CwdGroups: []sqlitestore.CwdGroupRow{}},
		HiddenSessionKeys: []sqlitestore.HiddenSessionKeyRow{},
		AppKV:             []sqlitestore.AppKVRow{},
		Warnings:          []sqlitestore.MigrationWarningRow{},
		Provenance: sqlitestore.ImportProvenanceRow{
			Source:      "fixture",
			SnapshotAt:  now,
			DetailsJSON: `{"fixture":"display-order"}`,
		},
	}); err != nil {
		t.Fatalf("ReplaceImportBundle() error = %v", err)
	}

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("len(ListSessions().Items) = %d, want 2", len(listed.Items))
	}
	if listed.Items[0].SessionID != "imported-pi-2" || listed.Items[1].SessionID != "imported-pi-1" {
		t.Fatalf("ListSessions() order = [%q %q], want [imported-pi-2 imported-pi-1]", listed.Items[0].SessionID, listed.Items[1].SessionID)
	}
	if listed.Items[0].UpdatedTS != timestampSeconds(newerActivity) || listed.Items[0].LastUpdatedTS != timestampSeconds(newerActivity) {
		t.Fatalf("ListSessions().Items[0] timestamps = (%v, %v), want %v", listed.Items[0].LastUpdatedTS, listed.Items[0].UpdatedTS, timestampSeconds(newerActivity))
	}
	if listed.Items[1].UpdatedTS != timestampSeconds(olderActivity) || listed.Items[1].LastUpdatedTS != timestampSeconds(olderActivity) {
		t.Fatalf("ListSessions().Items[1] timestamps = (%v, %v), want %v", listed.Items[1].LastUpdatedTS, listed.Items[1].UpdatedTS, timestampSeconds(olderActivity))
	}
}

func TestStubListSessionsDemotesBlockedAndSnoozedPriority(t *testing.T) {
	cfg := config.Load()
	now := time.Unix(1760000000, 0).UTC()
	svc := newStub(cfg, func() time.Time { return now })
	for _, cwd := range []string{"/tmp/active", "/tmp/blocked", "/tmp/snoozed"} {
		if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: cwd}); err != nil {
			t.Fatalf("CreateSession(%q) error = %v", cwd, err)
		}
	}
	blockedPriority := 1.0
	blockedDependency := "s_1"
	if _, err := svc.EditSession(context.Background(), EditSessionRequest{
		SessionID:           mustSessionID(t, "s_2"),
		PriorityOffset:      Float64Patch{Present: true, Value: &blockedPriority},
		DependencySessionID: StringPatch{Present: true, Value: &blockedDependency},
	}); err != nil {
		t.Fatalf("EditSession(blocked) error = %v", err)
	}
	snoozedPriority := 1.0
	snoozeUntil := now.Add(time.Hour).Unix()
	if _, err := svc.EditSession(context.Background(), EditSessionRequest{
		SessionID:      mustSessionID(t, "s_3"),
		PriorityOffset: Float64Patch{Present: true, Value: &snoozedPriority},
		SnoozeUntil:    Int64Patch{Present: true, Value: &snoozeUntil},
	}); err != nil {
		t.Fatalf("EditSession(snoozed) error = %v", err)
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 3 {
		t.Fatalf("len(ListSessions().Items) = %d, want 3", len(listed.Items))
	}
	got := []string{listed.Items[0].SessionID, listed.Items[1].SessionID, listed.Items[2].SessionID}
	want := []string{"s_1", "s_2", "s_3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListSessions() order = %#v, want %#v", got, want)
		}
	}
}
