package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSubagentSnapshotPersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actrail.db")
	catalog, err := OpenSessionCatalog(path)
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	now := time.Unix(1760000000, 0).UTC()
	if err := catalog.UpsertSession(context.Background(), SessionRow{SessionID: "child", Backend: "pi", CWD: "/repo", Title: "child", CreatedAt: now, UpdatedAt: now, ActivityAt: now, Hidden: true}); err != nil {
		t.Fatalf("UpsertSession() error = %v", err)
	}
	snapshot := SubagentSnapshotRow{
		Actor: SubagentActorRow{ActorID: "actor_7", ChildSessionID: "child", ParentSessionID: "parent", Name: "reviewer", Role: "review", Status: "waiting_for_parent", TurnID: "turn_3", QuestionID: "question_2", QuestionTurnID: "turn_3", Question: "Use A?", QuestionContext: "ctx", QuestionCreatedTS: 42, LastEventID: "event_9", LastEventAt: &now, CWD: "/repo", CreatedAt: now, UpdatedAt: now},
		Events: []SubagentEventRow{
			{ActorID: "actor_7", EventID: "event_8", Type: "subagent.question", ChildSessionID: "child", ParentSessionID: "parent", TurnID: "turn_3", QuestionID: "question_2", Message: "Use A?", Status: "waiting_for_parent", TS: 42},
			{ActorID: "actor_7", EventID: "event_9", Type: "subagent.status", ChildSessionID: "child", ParentSessionID: "parent", Status: "waiting_for_parent", TS: 43},
		},
		Messages: []SubagentMessageRow{{ActorID: "actor_7", MessageID: "question_2", Kind: "member", Label: "reviewer", Body: "Use A?", TS: 42, Meta: "ask_parent"}},
	}
	if err := catalog.ReplaceSubagentSnapshot(context.Background(), snapshot); err != nil {
		t.Fatalf("ReplaceSubagentSnapshot() error = %v", err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reloaded, err := OpenSessionCatalog(path)
	if err != nil {
		t.Fatalf("OpenSessionCatalog(reload) error = %v", err)
	}
	defer func() { _ = reloaded.Close() }()
	loaded, err := reloaded.ListSubagentSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ListSubagentSnapshots() error = %v", err)
	}
	if len(loaded) != 1 || loaded[0].Actor.ActorID != "actor_7" || len(loaded[0].Events) != 2 || len(loaded[0].Messages) != 1 {
		t.Fatalf("loaded snapshot = %+v", loaded)
	}
}

func TestOpenSessionCatalogMigratesSubagentTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actrail.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES(8, '2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("db.Exec(%q) error = %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}
	catalog, err := OpenSessionCatalog(path)
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()
	for _, table := range []string{"subagent_actors", "subagent_events", "subagent_messages"} {
		var count int
		if err := catalog.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}
}
