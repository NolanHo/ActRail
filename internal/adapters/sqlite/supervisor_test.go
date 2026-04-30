package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSupervisorMigrationCreatesTables(t *testing.T) {
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
	for _, table := range []string{"supervisor_provider_settings", "session_supervisor_config", "supervisor_runs"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query sqlite_master(%q) error = %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %q count = %d, want 1", table, count)
		}
	}
}

func TestSupervisorProviderSettingsRoundTrip(t *testing.T) {
	catalog, err := OpenSessionCatalog(filepath.Join(t.TempDir(), "actrail.db"))
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()
	now := time.Unix(1760000000, 0).UTC()
	if err := catalog.UpsertSupervisorProviderSettings(context.Background(), SupervisorProviderSettingsRow{BaseURL: " https://llm.invalid/v1 ", APIKey: " secret ", Model: " model-a ", UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertSupervisorProviderSettings() error = %v", err)
	}
	row, ok, err := catalog.LookupSupervisorProviderSettings(context.Background())
	if err != nil {
		t.Fatalf("LookupSupervisorProviderSettings() error = %v", err)
	}
	if !ok || row.BaseURL != "https://llm.invalid/v1" || row.APIKey != "secret" || row.Model != "model-a" || !row.UpdatedAt.Equal(now) {
		t.Fatalf("LookupSupervisorProviderSettings() = %+v, %v", row, ok)
	}
}

func TestSessionSupervisorConfigRoundTrip(t *testing.T) {
	catalog, err := OpenSessionCatalog(filepath.Join(t.TempDir(), "actrail.db"))
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()
	now := time.Unix(1760000000, 0).UTC()
	if err := catalog.UpsertSessionSnapshot(context.Background(), SessionSnapshotRow{Session: SessionRow{SessionID: "s_1", Backend: "pi", CWD: "/tmp/project", CreatedAt: now, UpdatedAt: now, ActivityAt: now}}); err != nil {
		t.Fatalf("UpsertSessionSnapshot() error = %v", err)
	}
	stored := SessionSupervisorConfigRow{SessionID: "s_1", Enabled: true, IdleAfterMinutes: 3, MaxConsecutiveInjections: 11, ConsecutiveInjections: 2, Goal: "goal", AcceptanceCriteria: "criteria", ContextFiles: []string{"README.md"}, UpdatedAt: now}
	if err := catalog.UpsertSessionSupervisorConfig(context.Background(), stored); err != nil {
		t.Fatalf("UpsertSessionSupervisorConfig() error = %v", err)
	}
	row, ok, err := catalog.LookupSessionSupervisorConfig(context.Background(), "s_1")
	if err != nil {
		t.Fatalf("LookupSessionSupervisorConfig() error = %v", err)
	}
	if !ok || !row.Enabled || row.IdleAfterMinutes != 3 || row.MaxConsecutiveInjections != 11 || row.ConsecutiveInjections != 2 || row.Goal != "goal" || row.AcceptanceCriteria != "criteria" || len(row.ContextFiles) != 1 || row.ContextFiles[0] != "README.md" {
		t.Fatalf("LookupSessionSupervisorConfig() = %+v, %v", row, ok)
	}
}
