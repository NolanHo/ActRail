package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

type SessionSourceRefRow struct {
	SessionID               string
	Backend                 string
	SourcePath              string
	FirstUserMessage        string
	HasLegacySessionUIState bool
}

type HiddenSessionKeyRow struct {
	Key string
}

type AppKVRow struct {
	Namespace string
	Key       string
	ValueJSON string
}

type MigrationWarningRow struct {
	SourceTable string
	LegacyKey   string
	WarningCode string
	Message     string
	PayloadJSON string
}

type ImportProvenanceRow struct {
	Source      string
	SnapshotAt  time.Time
	DetailsJSON string
}

type ImportBundle struct {
	Sessions          []SessionSnapshotRow
	SessionSourceRefs []SessionSourceRefRow
	AppState          AppStateRow
	HiddenSessionKeys []HiddenSessionKeyRow
	AppKV             []AppKVRow
	Warnings          []MigrationWarningRow
	Provenance        ImportProvenanceRow
}

func (c *SessionCatalog) ReplaceImportBundle(ctx context.Context, bundle ImportBundle) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin import replace: %w", err)
	}
	if err := clearImportBundleTx(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, snapshot := range bundle.Sessions {
		if err := upsertSessionTx(ctx, tx, snapshot.Session); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := replaceQueueTx(ctx, tx, snapshot.Session.SessionID, snapshot.Queue); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := replaceWorkspaceTx(ctx, tx, snapshot.Session.SessionID, snapshot.Workspace); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	for _, row := range bundle.SessionSourceRefs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_source_refs(session_id, backend, source_path, first_user_message, has_legacy_session_ui_state) VALUES(?, ?, ?, ?, ?)`, row.SessionID, row.Backend, row.SourcePath, row.FirstUserMessage, boolToInt(row.HasLegacySessionUIState)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert session source ref %q: %w", row.SessionID, err)
		}
	}
	if err := replaceAppStateTx(ctx, tx, bundle.AppState); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, row := range bundle.HiddenSessionKeys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hidden_session_keys(key) VALUES(?)`, row.Key); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert hidden session key %q: %w", row.Key, err)
		}
	}
	for _, row := range bundle.AppKV {
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_kv(namespace, key, value_json) VALUES(?, ?, ?)`, row.Namespace, row.Key, row.ValueJSON); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert app kv %q/%q: %w", row.Namespace, row.Key, err)
		}
	}
	for _, row := range bundle.Warnings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO migration_warnings(source_table, legacy_key, warning_code, message, payload_json) VALUES(?, ?, ?, ?, ?)`, row.SourceTable, row.LegacyKey, row.WarningCode, row.Message, row.PayloadJSON); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert migration warning %q/%q: %w", row.SourceTable, row.LegacyKey, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO import_provenance(source, snapshot_at, details_json) VALUES(?, ?, ?)`, bundle.Provenance.Source, formatTime(bundle.Provenance.SnapshotAt), bundle.Provenance.DetailsJSON); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("insert import provenance: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit import replace: %w", err)
	}
	return nil
}

func clearImportBundleTx(ctx context.Context, tx execer) error {
	statements := []string{
		`DELETE FROM session_queue_items`,
		`DELETE FROM session_workspace_open_paths`,
		`DELETE FROM session_workspace_history_items`,
		`DELETE FROM session_workspace_state`,
		`DELETE FROM session_source_refs`,
		`DELETE FROM session_catalog`,
		`DELETE FROM app_recent_cwds`,
		`DELETE FROM cwd_groups`,
		`DELETE FROM hidden_session_keys`,
		`DELETE FROM app_kv`,
		`DELETE FROM migration_warnings`,
		`DELETE FROM import_provenance`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("clear import bundle state: %w", err)
		}
	}
	return nil
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func replaceAppStateTx(ctx context.Context, tx execer, state AppStateRow) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_recent_cwds`); err != nil {
		return fmt.Errorf("clear recent cwds: %w", err)
	}
	for idx, cwd := range state.RecentCwds {
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_recent_cwds(ordinal, cwd) VALUES(?, ?)`, idx, cwd); err != nil {
			return fmt.Errorf("insert recent cwd %q: %w", cwd, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cwd_groups`); err != nil {
		return fmt.Errorf("clear cwd groups: %w", err)
	}
	for _, row := range state.CwdGroups {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cwd_groups(cwd, label, collapsed) VALUES(?, ?, ?)`, row.CWD, row.Label, boolToInt(row.Collapsed)); err != nil {
			return fmt.Errorf("insert cwd group %q: %w", row.CWD, err)
		}
	}
	return nil
}

func (c *SessionCatalog) ListSessionSourceRefs(ctx context.Context) ([]SessionSourceRefRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT session_id, backend, source_path, first_user_message, has_legacy_session_ui_state FROM session_source_refs ORDER BY session_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query session source refs: %w", err)
	}
	defer rows.Close()
	items := make([]SessionSourceRefRow, 0)
	for rows.Next() {
		var (
			row                     SessionSourceRefRow
			hasLegacySessionUIState int
		)
		if err := rows.Scan(&row.SessionID, &row.Backend, &row.SourcePath, &row.FirstUserMessage, &hasLegacySessionUIState); err != nil {
			return nil, fmt.Errorf("scan session source ref row: %w", err)
		}
		row.HasLegacySessionUIState = hasLegacySessionUIState != 0
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session source ref rows: %w", err)
	}
	return items, nil
}

func (c *SessionCatalog) ListHiddenSessionKeys(ctx context.Context) ([]HiddenSessionKeyRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT key FROM hidden_session_keys ORDER BY key ASC`)
	if err != nil {
		return nil, fmt.Errorf("query hidden session keys: %w", err)
	}
	defer rows.Close()
	items := make([]HiddenSessionKeyRow, 0)
	for rows.Next() {
		var row HiddenSessionKeyRow
		if err := rows.Scan(&row.Key); err != nil {
			return nil, fmt.Errorf("scan hidden session key row: %w", err)
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hidden session key rows: %w", err)
	}
	return items, nil
}

func (c *SessionCatalog) ListAppKV(ctx context.Context) ([]AppKVRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT namespace, key, value_json FROM app_kv ORDER BY namespace ASC, key ASC`)
	if err != nil {
		return nil, fmt.Errorf("query app kv: %w", err)
	}
	defer rows.Close()
	items := make([]AppKVRow, 0)
	for rows.Next() {
		var row AppKVRow
		if err := rows.Scan(&row.Namespace, &row.Key, &row.ValueJSON); err != nil {
			return nil, fmt.Errorf("scan app kv row: %w", err)
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate app kv rows: %w", err)
	}
	return items, nil
}

func (c *SessionCatalog) ListMigrationWarnings(ctx context.Context) ([]MigrationWarningRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT source_table, legacy_key, warning_code, message, payload_json FROM migration_warnings ORDER BY source_table ASC, legacy_key ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query migration warnings: %w", err)
	}
	defer rows.Close()
	items := make([]MigrationWarningRow, 0)
	for rows.Next() {
		var row MigrationWarningRow
		if err := rows.Scan(&row.SourceTable, &row.LegacyKey, &row.WarningCode, &row.Message, &row.PayloadJSON); err != nil {
			return nil, fmt.Errorf("scan migration warning row: %w", err)
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration warning rows: %w", err)
	}
	return items, nil
}

func (c *SessionCatalog) ListImportProvenance(ctx context.Context) ([]ImportProvenanceRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT source, snapshot_at, details_json FROM import_provenance ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query import provenance: %w", err)
	}
	defer rows.Close()
	items := make([]ImportProvenanceRow, 0)
	for rows.Next() {
		var (
			row         ImportProvenanceRow
			snapshotRaw string
		)
		if err := rows.Scan(&row.Source, &snapshotRaw, &row.DetailsJSON); err != nil {
			return nil, fmt.Errorf("scan import provenance row: %w", err)
		}
		ts, err := parseTime(snapshotRaw)
		if err != nil {
			return nil, err
		}
		row.SnapshotAt = ts
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate import provenance rows: %w", err)
	}
	return items, nil
}

func sortImportBundle(bundle *ImportBundle) {
	sort.Slice(bundle.Sessions, func(i, j int) bool {
		left := bundle.Sessions[i].Session
		right := bundle.Sessions[j].Session
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		if left.Backend != right.Backend {
			return left.Backend < right.Backend
		}
		return left.SessionID < right.SessionID
	})
	sort.Slice(bundle.SessionSourceRefs, func(i, j int) bool {
		return bundle.SessionSourceRefs[i].SessionID < bundle.SessionSourceRefs[j].SessionID
	})
	sort.Slice(bundle.HiddenSessionKeys, func(i, j int) bool {
		return bundle.HiddenSessionKeys[i].Key < bundle.HiddenSessionKeys[j].Key
	})
	sort.Slice(bundle.AppKV, func(i, j int) bool {
		left := bundle.AppKV[i]
		right := bundle.AppKV[j]
		if left.Namespace != right.Namespace {
			return left.Namespace < right.Namespace
		}
		return left.Key < right.Key
	})
	sort.Slice(bundle.Warnings, func(i, j int) bool {
		left := bundle.Warnings[i]
		right := bundle.Warnings[j]
		if left.SourceTable != right.SourceTable {
			return left.SourceTable < right.SourceTable
		}
		if left.LegacyKey != right.LegacyKey {
			return left.LegacyKey < right.LegacyKey
		}
		return left.WarningCode < right.WarningCode
	})
}
