package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
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
