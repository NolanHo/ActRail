package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const currentSchemaVersion = 7

const tsLayout = time.RFC3339Nano

type SessionRow struct {
	SessionID           string
	Backend             string
	CWD                 string
	Title               string
	Alias               string
	Provider            string
	Model               string
	ReasoningEffort     string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ActivityAt          time.Time
	Focused             bool
	Hidden              bool
	PriorityOffset      float64
	SnoozeUntil         *time.Time
	DependencySessionID *string
	ArchivedAt          *time.Time
}

type SessionCatalog struct {
	db *sql.DB
}

type migration struct {
	version int
	apply   func(context.Context, *sql.Tx) error
}

var migrations = []migration{
	{
		version: 1,
		apply: func(ctx context.Context, tx *sql.Tx) error {
			statements := []string{
				`CREATE TABLE IF NOT EXISTS schema_migrations (
					version INTEGER PRIMARY KEY,
					applied_at TEXT NOT NULL
				)`,
				`CREATE TABLE IF NOT EXISTS session_catalog (
					session_id TEXT PRIMARY KEY,
					backend TEXT NOT NULL,
					cwd TEXT NOT NULL DEFAULT '',
					title TEXT NOT NULL DEFAULT '',
					alias TEXT NOT NULL DEFAULT '',
					provider TEXT NOT NULL DEFAULT '',
					model TEXT NOT NULL DEFAULT '',
					reasoning_effort TEXT NOT NULL DEFAULT '',
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL,
					activity_at TEXT NOT NULL,
					focused INTEGER NOT NULL DEFAULT 0,
					hidden INTEGER NOT NULL DEFAULT 0,
					priority_offset REAL NOT NULL DEFAULT 0,
					snooze_until TEXT,
					dependency_session_id TEXT,
					archived_at TEXT
				)`,
				`CREATE INDEX IF NOT EXISTS session_catalog_active_idx ON session_catalog(archived_at, created_at, session_id)`,
				`CREATE INDEX IF NOT EXISTS session_catalog_activity_idx ON session_catalog(archived_at, activity_at DESC, session_id DESC)`,
				`CREATE TABLE IF NOT EXISTS import_provenance (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					source TEXT NOT NULL,
					snapshot_at TEXT NOT NULL,
					details_json TEXT NOT NULL DEFAULT '{}'
				)`,
			}
			for _, stmt := range statements {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, 1, time.Now().UTC().Format(tsLayout))
			return err
		},
	},
	{
		version: 2,
		apply: func(ctx context.Context, tx *sql.Tx) error {
			statements := []string{
				`CREATE TABLE IF NOT EXISTS session_queue_items (
					session_id TEXT NOT NULL,
					ordinal INTEGER NOT NULL,
					item_id TEXT NOT NULL,
					text TEXT NOT NULL,
					state TEXT NOT NULL,
					PRIMARY KEY(session_id, item_id),
					UNIQUE(session_id, ordinal),
					FOREIGN KEY(session_id) REFERENCES session_catalog(session_id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS session_queue_items_session_idx ON session_queue_items(session_id, ordinal)`,
				`CREATE TABLE IF NOT EXISTS session_workspace_state (
					session_id TEXT PRIMARY KEY,
					selected_path TEXT NOT NULL DEFAULT '',
					FOREIGN KEY(session_id) REFERENCES session_catalog(session_id) ON DELETE CASCADE
				)`,
				`CREATE TABLE IF NOT EXISTS session_workspace_open_paths (
					session_id TEXT NOT NULL,
					ordinal INTEGER NOT NULL,
					path TEXT NOT NULL,
					PRIMARY KEY(session_id, path),
					UNIQUE(session_id, ordinal),
					FOREIGN KEY(session_id) REFERENCES session_catalog(session_id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS session_workspace_open_paths_session_idx ON session_workspace_open_paths(session_id, ordinal)`,
				`CREATE TABLE IF NOT EXISTS session_workspace_history_items (
					session_id TEXT NOT NULL,
					ordinal INTEGER NOT NULL,
					path TEXT NOT NULL,
					label TEXT NOT NULL DEFAULT '',
					PRIMARY KEY(session_id, ordinal),
					FOREIGN KEY(session_id) REFERENCES session_catalog(session_id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS session_workspace_history_session_idx ON session_workspace_history_items(session_id, ordinal)`,
				`CREATE TABLE IF NOT EXISTS app_recent_cwds (
					ordinal INTEGER PRIMARY KEY,
					cwd TEXT NOT NULL UNIQUE
				)`,
				`CREATE TABLE IF NOT EXISTS cwd_groups (
					cwd TEXT PRIMARY KEY,
					label TEXT NOT NULL DEFAULT '',
					collapsed INTEGER NOT NULL DEFAULT 0
				)`,
			}
			for _, stmt := range statements {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, 2, time.Now().UTC().Format(tsLayout))
			return err
		},
	},
	{
		version: 3,
		apply: func(ctx context.Context, tx *sql.Tx) error {
			statements := []string{
				`CREATE TABLE IF NOT EXISTS session_source_refs (
					session_id TEXT PRIMARY KEY,
					backend TEXT NOT NULL DEFAULT '',
					source_path TEXT NOT NULL DEFAULT '',
					first_user_message TEXT NOT NULL DEFAULT '',
					FOREIGN KEY(session_id) REFERENCES session_catalog(session_id) ON DELETE CASCADE
				)`,
				`CREATE TABLE IF NOT EXISTS hidden_session_keys (
					key TEXT PRIMARY KEY
				)`,
				`CREATE TABLE IF NOT EXISTS app_kv (
					namespace TEXT NOT NULL,
					key TEXT NOT NULL,
					value_json TEXT NOT NULL DEFAULT '',
					PRIMARY KEY(namespace, key)
				)`,
				`CREATE TABLE IF NOT EXISTS migration_warnings (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					source_table TEXT NOT NULL,
					legacy_key TEXT NOT NULL DEFAULT '',
					warning_code TEXT NOT NULL DEFAULT '',
					message TEXT NOT NULL DEFAULT '',
					payload_json TEXT NOT NULL DEFAULT '{}'
				)`,
			}
			for _, stmt := range statements {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, 3, time.Now().UTC().Format(tsLayout))
			return err
		},
	},
	{
		version: 4,
		apply: func(ctx context.Context, tx *sql.Tx) error {
			if err := ensureColumnExists(ctx, tx, "session_source_refs", "has_legacy_session_ui_state", `ALTER TABLE session_source_refs ADD COLUMN has_legacy_session_ui_state INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, 4, time.Now().UTC().Format(tsLayout))
			return err
		},
	},
	{
		version: 5,
		apply: func(ctx context.Context, tx *sql.Tx) error {
			if err := ensureColumnExists(ctx, tx, "session_source_refs", "backend_session_id", `ALTER TABLE session_source_refs ADD COLUMN backend_session_id TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
			if err := ensureColumnExists(ctx, tx, "session_source_refs", "source_confidence", `ALTER TABLE session_source_refs ADD COLUMN source_confidence TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, 5, time.Now().UTC().Format(tsLayout))
			return err
		},
	},
	{
		version: 6,
		apply: func(ctx context.Context, tx *sql.Tx) error {
			statements := []string{
				`CREATE TABLE IF NOT EXISTS wait_threads (
					thread_id TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					title TEXT NOT NULL DEFAULT '',
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL,
					closed_at TEXT,
					FOREIGN KEY(session_id) REFERENCES session_catalog(session_id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS wait_threads_session_idx ON wait_threads(session_id, updated_at DESC)`,
				`CREATE TABLE IF NOT EXISTS waits (
					wait_id TEXT PRIMARY KEY,
					thread_id TEXT NOT NULL,
					session_id TEXT NOT NULL,
					request_id TEXT NOT NULL DEFAULT '',
					state TEXT NOT NULL,
					question TEXT NOT NULL DEFAULT '',
					context TEXT NOT NULL DEFAULT '',
					blocking_reason TEXT NOT NULL DEFAULT '',
					attempted TEXT NOT NULL DEFAULT '',
					default_if_no_reply TEXT NOT NULL DEFAULT '',
					answer TEXT NOT NULL DEFAULT '',
					fallback_used TEXT NOT NULL DEFAULT '',
					claimed_at TEXT,
					answered_at TEXT,
					cancelled_at TEXT,
					timed_out_at TEXT,
					orphaned_at TEXT,
					timeout_at TEXT,
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL,
					files_json TEXT NOT NULL DEFAULT '[]',
					FOREIGN KEY(thread_id) REFERENCES wait_threads(thread_id) ON DELETE CASCADE,
					FOREIGN KEY(session_id) REFERENCES session_catalog(session_id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS waits_session_thread_idx ON waits(session_id, thread_id, created_at DESC)`,
				`CREATE UNIQUE INDEX IF NOT EXISTS active_wait_per_session_idx ON waits(session_id) WHERE state IN ('pending_unread', 'claimed')`,
			}
			for _, stmt := range statements {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, 6, time.Now().UTC().Format(tsLayout))
			return err
		},
	},
	{
		version: 7,
		apply: func(ctx context.Context, tx *sql.Tx) error {
			statements := []string{
				`CREATE TABLE IF NOT EXISTS session_live_state (
					session_id TEXT PRIMARY KEY,
					busy INTEGER NOT NULL DEFAULT 0,
					tail_seq INTEGER NOT NULL DEFAULT 0,
					tail_owner TEXT NOT NULL DEFAULT 'transcript',
					tail_turn_id TEXT NOT NULL DEFAULT '',
					partial_turn_id TEXT NOT NULL DEFAULT '',
					partial_text TEXT NOT NULL DEFAULT '',
					ui_request_json TEXT NOT NULL DEFAULT '',
					transport_generation_id TEXT NOT NULL DEFAULT '',
					transport_state TEXT NOT NULL DEFAULT '',
					transport_reset_required INTEGER NOT NULL DEFAULT 0,
					transport_reason TEXT NOT NULL DEFAULT '',
					resume_session_cursor TEXT NOT NULL DEFAULT '',
					resume_ui_cursor TEXT NOT NULL DEFAULT '',
					resume_transport_cursor TEXT NOT NULL DEFAULT '',
					context_usage_json TEXT NOT NULL DEFAULT '',
					turn_timing_json TEXT NOT NULL DEFAULT '',
					runtime_agent_running INTEGER NOT NULL DEFAULT 0,
					updated_at TEXT NOT NULL,
					FOREIGN KEY(session_id) REFERENCES session_catalog(session_id) ON DELETE CASCADE
				)`,
			}
			for _, stmt := range statements {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, 7, time.Now().UTC().Format(tsLayout))
			return err
		},
	},
}

func OpenSessionCatalog(path string) (*SessionCatalog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("prepare sqlite catalog dir %q: %w", filepath.Dir(path), err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite catalog %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := configure(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SessionCatalog{db: db}, nil
}

func configure(ctx context.Context, db *sql.DB) error {
	for _, stmt := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("configure sqlite catalog: %w", err)
		}
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("ensure schema migrations table: %w", err)
	}
	current, err := schemaVersion(ctx, db)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if err := m.apply(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}
	}
	return nil
}

func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("query schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func ensureColumnExists(ctx context.Context, tx *sql.Tx, table, column, alterStmt string) error {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return fmt.Errorf("query table info for %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return fmt.Errorf("scan table info for %s: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table info for %s: %w", table, err)
	}
	if _, err := tx.ExecContext(ctx, alterStmt); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

func (c *SessionCatalog) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *SessionCatalog) UpsertSession(ctx context.Context, row SessionRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session upsert: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_catalog(
		session_id, backend, cwd, title, alias, provider, model, reasoning_effort,
		created_at, updated_at, activity_at, focused, hidden, priority_offset,
		snooze_until, dependency_session_id, archived_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(session_id) DO UPDATE SET
		backend = excluded.backend,
		cwd = excluded.cwd,
		title = excluded.title,
		alias = excluded.alias,
		provider = excluded.provider,
		model = excluded.model,
		reasoning_effort = excluded.reasoning_effort,
		created_at = excluded.created_at,
		updated_at = excluded.updated_at,
		activity_at = excluded.activity_at,
		focused = excluded.focused,
		hidden = excluded.hidden,
		priority_offset = excluded.priority_offset,
		snooze_until = excluded.snooze_until,
		dependency_session_id = excluded.dependency_session_id,
		archived_at = excluded.archived_at`,
		row.SessionID,
		row.Backend,
		row.CWD,
		row.Title,
		row.Alias,
		row.Provider,
		row.Model,
		row.ReasoningEffort,
		formatTime(row.CreatedAt),
		formatTime(row.UpdatedAt),
		formatTime(row.ActivityAt),
		boolToInt(row.Focused),
		boolToInt(row.Hidden),
		row.PriorityOffset,
		formatNullableTime(row.SnoozeUntil),
		nullableString(row.DependencySessionID),
		formatNullableTime(row.ArchivedAt),
	)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("upsert session %q: %w", row.SessionID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session %q: %w", row.SessionID, err)
	}
	return nil
}

func (c *SessionCatalog) ListSessions(ctx context.Context, includeArchived bool) ([]SessionRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	query := `SELECT session_id, backend, cwd, title, alias, provider, model, reasoning_effort,
		created_at, updated_at, activity_at, focused, hidden, priority_offset,
		snooze_until, dependency_session_id, archived_at
		FROM session_catalog`
	if !includeArchived {
		query += ` WHERE archived_at IS NULL`
	}
	query += ` ORDER BY created_at ASC, session_id ASC`
	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	items := make([]SessionRow, 0)
	for rows.Next() {
		row, err := scanSessionRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return items, nil
}

func (c *SessionCatalog) LookupSession(ctx context.Context, sessionID string) (SessionRow, bool, error) {
	if c == nil || c.db == nil {
		return SessionRow{}, false, fmt.Errorf("sqlite catalog is not initialized")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT session_id, backend, cwd, title, alias, provider, model, reasoning_effort,
		created_at, updated_at, activity_at, focused, hidden, priority_offset,
		snooze_until, dependency_session_id, archived_at
		FROM session_catalog WHERE session_id = ? LIMIT 1`, sessionID)
	if err != nil {
		return SessionRow{}, false, fmt.Errorf("lookup session %q: %w", sessionID, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return SessionRow{}, false, fmt.Errorf("lookup session %q: %w", sessionID, err)
		}
		return SessionRow{}, false, nil
	}
	row, err := scanSessionRow(rows)
	if err != nil {
		return SessionRow{}, false, err
	}
	return row, true, nil
}

func scanSessionRow(scanner interface{ Scan(...any) error }) (SessionRow, error) {
	var (
		row                 SessionRow
		createdAt           string
		updatedAt           string
		activityAt          string
		focused             int
		hidden              int
		snoozeUntil         sql.NullString
		dependencySessionID sql.NullString
		archivedAt          sql.NullString
	)
	if err := scanner.Scan(
		&row.SessionID,
		&row.Backend,
		&row.CWD,
		&row.Title,
		&row.Alias,
		&row.Provider,
		&row.Model,
		&row.ReasoningEffort,
		&createdAt,
		&updatedAt,
		&activityAt,
		&focused,
		&hidden,
		&row.PriorityOffset,
		&snoozeUntil,
		&dependencySessionID,
		&archivedAt,
	); err != nil {
		return SessionRow{}, fmt.Errorf("scan session row: %w", err)
	}
	var err error
	if row.CreatedAt, err = parseTime(createdAt); err != nil {
		return SessionRow{}, err
	}
	if row.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return SessionRow{}, err
	}
	if row.ActivityAt, err = parseTime(activityAt); err != nil {
		return SessionRow{}, err
	}
	row.Focused = focused != 0
	row.Hidden = hidden != 0
	if row.SnoozeUntil, err = parseNullableTime(snoozeUntil); err != nil {
		return SessionRow{}, err
	}
	row.DependencySessionID = nullableStringPtr(dependencySessionID)
	if row.ArchivedAt, err = parseNullableTime(archivedAt); err != nil {
		return SessionRow{}, err
	}
	return row, nil
}

func formatTime(ts time.Time) string {
	return ts.UTC().Format(tsLayout)
}

func parseTime(raw string) (time.Time, error) {
	ts, err := time.Parse(tsLayout, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse sqlite timestamp %q: %w", raw, err)
	}
	return ts.UTC(), nil
}

func formatNullableTime(ts *time.Time) any {
	if ts == nil || ts.IsZero() {
		return nil
	}
	return ts.UTC().Format(tsLayout)
}

func parseNullableTime(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	ts, err := parseTime(raw.String)
	if err != nil {
		return nil, err
	}
	return &ts, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	if *value == "" {
		return nil
	}
	return *value
}

func nullableStringPtr(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	copied := value.String
	return &copied
}
