package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type SessionSourceRefRow struct {
	SessionID               string
	Backend                 string
	BackendSessionID        string
	SourcePath              string
	SourceConfidence        string
	FirstUserMessage        string
	HasLegacySessionUIState bool
	ForkParentSessionID     string
	ForkParentBackendID     string
	ForkParentSourcePath    string
}

type CodexRuntimeClaimRow struct {
	SessionID         string
	BackendSessionID  string
	SourcePath        string
	RuntimeInstanceID string
	HelperPID         int
	ChildPID          int
	ControlSocketPath string
	ChildSocketPath   string
	LastHelloAt       *time.Time
	LastLiveEventAt   *time.Time
	State             string
	UpdatedAt         time.Time
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
		if err := upsertLiveStateTx(ctx, tx, snapshot.Session.SessionID, snapshot.Live); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	for _, row := range bundle.SessionSourceRefs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_source_refs(session_id, backend, backend_session_id, source_path, source_confidence, first_user_message, has_legacy_session_ui_state, fork_parent_session_id, fork_parent_backend_id, fork_parent_source_path) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.SessionID, row.Backend, row.BackendSessionID, row.SourcePath, row.SourceConfidence, row.FirstUserMessage, boolToInt(row.HasLegacySessionUIState), row.ForkParentSessionID, row.ForkParentBackendID, row.ForkParentSourcePath); err != nil {
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
		`DELETE FROM codex_session_commands`,
		`DELETE FROM codex_runtime_claims`,
		`DELETE FROM session_queue_items`,
		`DELETE FROM session_workspace_open_paths`,
		`DELETE FROM session_workspace_history_items`,
		`DELETE FROM session_workspace_state`,
		`DELETE FROM session_live_state`,
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

func (c *SessionCatalog) UpsertSessionSourceRef(ctx context.Context, row SessionSourceRefRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO session_source_refs(session_id, backend, backend_session_id, source_path, source_confidence, first_user_message, has_legacy_session_ui_state, fork_parent_session_id, fork_parent_backend_id, fork_parent_source_path) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET backend=excluded.backend, backend_session_id=excluded.backend_session_id, source_path=excluded.source_path, source_confidence=excluded.source_confidence, first_user_message=excluded.first_user_message, has_legacy_session_ui_state=excluded.has_legacy_session_ui_state, fork_parent_session_id=excluded.fork_parent_session_id, fork_parent_backend_id=excluded.fork_parent_backend_id, fork_parent_source_path=excluded.fork_parent_source_path`, row.SessionID, row.Backend, row.BackendSessionID, row.SourcePath, row.SourceConfidence, row.FirstUserMessage, boolToInt(row.HasLegacySessionUIState), row.ForkParentSessionID, row.ForkParentBackendID, row.ForkParentSourcePath)
	if err != nil {
		return fmt.Errorf("upsert session source ref %q: %w", row.SessionID, err)
	}
	return nil
}

func (c *SessionCatalog) ListSessionSourceRefs(ctx context.Context) ([]SessionSourceRefRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT session_id, backend, backend_session_id, source_path, source_confidence, first_user_message, has_legacy_session_ui_state, fork_parent_session_id, fork_parent_backend_id, fork_parent_source_path FROM session_source_refs ORDER BY session_id ASC`)
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
		if err := rows.Scan(&row.SessionID, &row.Backend, &row.BackendSessionID, &row.SourcePath, &row.SourceConfidence, &row.FirstUserMessage, &hasLegacySessionUIState, &row.ForkParentSessionID, &row.ForkParentBackendID, &row.ForkParentSourcePath); err != nil {
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

func (c *SessionCatalog) UpsertCodexRuntimeClaim(ctx context.Context, row CodexRuntimeClaimRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO codex_runtime_claims(
			session_id, backend_session_id, source_path, runtime_instance_id,
			helper_pid, child_pid, control_socket_path, child_socket_path,
			last_hello_at, last_live_event_at, state, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			backend_session_id=excluded.backend_session_id,
			source_path=excluded.source_path,
			runtime_instance_id=excluded.runtime_instance_id,
			helper_pid=excluded.helper_pid,
			child_pid=excluded.child_pid,
			control_socket_path=excluded.control_socket_path,
			child_socket_path=excluded.child_socket_path,
			last_hello_at=excluded.last_hello_at,
			last_live_event_at=excluded.last_live_event_at,
			state=excluded.state,
			updated_at=excluded.updated_at`,
		strings.TrimSpace(row.SessionID),
		strings.TrimSpace(row.BackendSessionID),
		strings.TrimSpace(row.SourcePath),
		strings.TrimSpace(row.RuntimeInstanceID),
		row.HelperPID,
		row.ChildPID,
		strings.TrimSpace(row.ControlSocketPath),
		strings.TrimSpace(row.ChildSocketPath),
		formatNullableTime(row.LastHelloAt),
		formatNullableTime(row.LastLiveEventAt),
		codexRuntimeClaimState(row.State),
		formatTime(row.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert codex runtime claim %q: %w", row.SessionID, err)
	}
	return nil
}

func (c *SessionCatalog) LookupCodexRuntimeClaimByThread(ctx context.Context, backendSessionID string) (CodexRuntimeClaimRow, bool, error) {
	if c == nil || c.db == nil {
		return CodexRuntimeClaimRow{}, false, fmt.Errorf("sqlite catalog is not initialized")
	}
	threadID := strings.TrimSpace(backendSessionID)
	if threadID == "" {
		return CodexRuntimeClaimRow{}, false, nil
	}
	row, ok, err := c.lookupCodexRuntimeClaim(ctx, `backend_session_id = ?`, threadID)
	if err != nil {
		return CodexRuntimeClaimRow{}, false, fmt.Errorf("lookup codex runtime claim for thread %q: %w", threadID, err)
	}
	return row, ok, nil
}

func (c *SessionCatalog) ListCodexRuntimeClaims(ctx context.Context) ([]CodexRuntimeClaimRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT session_id, backend_session_id, source_path, runtime_instance_id, helper_pid, child_pid, control_socket_path, child_socket_path, last_hello_at, last_live_event_at, state, updated_at FROM codex_runtime_claims ORDER BY backend_session_id ASC, session_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query codex runtime claims: %w", err)
	}
	defer rows.Close()
	items := make([]CodexRuntimeClaimRow, 0)
	for rows.Next() {
		row, err := scanCodexRuntimeClaim(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate codex runtime claims: %w", err)
	}
	return items, nil
}

type codexRuntimeClaimScanner interface {
	Scan(dest ...any) error
}

func (c *SessionCatalog) lookupCodexRuntimeClaim(ctx context.Context, where string, args ...any) (CodexRuntimeClaimRow, bool, error) {
	query := `SELECT session_id, backend_session_id, source_path, runtime_instance_id, helper_pid, child_pid, control_socket_path, child_socket_path, last_hello_at, last_live_event_at, state, updated_at FROM codex_runtime_claims WHERE ` + where + ` LIMIT 1`
	row, err := scanCodexRuntimeClaim(c.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CodexRuntimeClaimRow{}, false, nil
		}
		return CodexRuntimeClaimRow{}, false, err
	}
	return row, true, nil
}

func scanCodexRuntimeClaim(scanner codexRuntimeClaimScanner) (CodexRuntimeClaimRow, error) {
	var (
		row                       CodexRuntimeClaimRow
		lastHelloRaw, lastLiveRaw sql.NullString
		updatedAtRaw              string
	)
	if err := scanner.Scan(
		&row.SessionID,
		&row.BackendSessionID,
		&row.SourcePath,
		&row.RuntimeInstanceID,
		&row.HelperPID,
		&row.ChildPID,
		&row.ControlSocketPath,
		&row.ChildSocketPath,
		&lastHelloRaw,
		&lastLiveRaw,
		&row.State,
		&updatedAtRaw,
	); err != nil {
		return CodexRuntimeClaimRow{}, err
	}
	var err error
	if row.LastHelloAt, err = parseNullableTime(lastHelloRaw); err != nil {
		return CodexRuntimeClaimRow{}, err
	}
	if row.LastLiveEventAt, err = parseNullableTime(lastLiveRaw); err != nil {
		return CodexRuntimeClaimRow{}, err
	}
	if row.UpdatedAt, err = parseTime(updatedAtRaw); err != nil {
		return CodexRuntimeClaimRow{}, err
	}
	return row, nil
}

func codexRuntimeClaimState(raw string) string {
	state := strings.TrimSpace(raw)
	if state == "" {
		return "unknown"
	}
	return state
}

func (c *SessionCatalog) DeleteCodexRuntimeClaim(ctx context.Context, sessionID string) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	if _, err := c.db.ExecContext(ctx, `DELETE FROM codex_runtime_claims WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("delete codex runtime claim %q: %w", sessionID, err)
	}
	return nil
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
