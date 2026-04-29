package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type SessionSnapshotRow struct {
	Session   SessionRow
	Queue     []QueueItemRow
	Workspace WorkspaceStateRow
	Live      LiveStateRow
}

// LiveStateRow stores session state that otherwise exists only in memory while a runtime is attached.
type LiveStateRow struct {
	Busy                   bool
	TailSeq                uint64
	TailOwner              string
	TailTurnID             string
	PartialTurnID          string
	PartialText            string
	UIRequestJSON          string
	TransportGenerationID  string
	TransportState         string
	TransportResetRequired bool
	TransportReason        string
	ResumeSessionCursor    string
	ResumeUICursor         string
	ResumeTransportCursor  string
	ContextUsageJSON       string
	TurnTimingJSON         string
	RuntimeAgentRunning    bool
	UpdatedAt              time.Time
}

type QueueItemRow struct {
	Ordinal int
	ItemID  string
	Text    string
	State   string
}

type WorkspaceStateRow struct {
	SelectedPath string
	OpenPaths    []string
	HistoryItems []WorkspaceHistoryItemRow
}

type WorkspaceHistoryItemRow struct {
	Ordinal int
	Path    string
	Label   string
}

type AppStateRow struct {
	RecentCwds []string
	CwdGroups  []CwdGroupRow
}

type CwdGroupRow struct {
	CWD       string
	Label     string
	Collapsed bool
}

func (c *SessionCatalog) UpsertSessionSnapshot(ctx context.Context, snapshot SessionSnapshotRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session snapshot upsert: %w", err)
	}
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
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session snapshot %q: %w", snapshot.Session.SessionID, err)
	}
	return nil
}

func (c *SessionCatalog) ListSessionSnapshots(ctx context.Context, includeArchived bool) ([]SessionSnapshotRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	sessions, err := c.ListSessions(ctx, includeArchived)
	if err != nil {
		return nil, err
	}
	snapshots := make([]SessionSnapshotRow, 0, len(sessions))
	for _, row := range sessions {
		queue, err := c.listQueueRows(ctx, row.SessionID)
		if err != nil {
			return nil, err
		}
		workspaceState, err := c.lookupWorkspaceState(ctx, row.SessionID)
		if err != nil {
			return nil, err
		}
		liveState, err := c.lookupLiveState(ctx, row.SessionID)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, SessionSnapshotRow{
			Session:   row,
			Queue:     queue,
			Workspace: workspaceState,
			Live:      liveState,
		})
	}
	return snapshots, nil
}

func (c *SessionCatalog) LoadAppState(ctx context.Context) (AppStateRow, error) {
	if c == nil || c.db == nil {
		return AppStateRow{}, fmt.Errorf("sqlite catalog is not initialized")
	}
	recentRows, err := c.db.QueryContext(ctx, `SELECT cwd FROM app_recent_cwds ORDER BY ordinal ASC`)
	if err != nil {
		return AppStateRow{}, fmt.Errorf("query recent cwds: %w", err)
	}
	defer recentRows.Close()

	state := AppStateRow{RecentCwds: []string{}, CwdGroups: []CwdGroupRow{}}
	for recentRows.Next() {
		var cwd string
		if err := recentRows.Scan(&cwd); err != nil {
			return AppStateRow{}, fmt.Errorf("scan recent cwd row: %w", err)
		}
		state.RecentCwds = append(state.RecentCwds, cwd)
	}
	if err := recentRows.Err(); err != nil {
		return AppStateRow{}, fmt.Errorf("iterate recent cwd rows: %w", err)
	}

	groupRows, err := c.db.QueryContext(ctx, `SELECT cwd, label, collapsed FROM cwd_groups ORDER BY cwd ASC`)
	if err != nil {
		return AppStateRow{}, fmt.Errorf("query cwd groups: %w", err)
	}
	defer groupRows.Close()
	for groupRows.Next() {
		var (
			row       CwdGroupRow
			collapsed int
		)
		if err := groupRows.Scan(&row.CWD, &row.Label, &collapsed); err != nil {
			return AppStateRow{}, fmt.Errorf("scan cwd group row: %w", err)
		}
		row.Collapsed = collapsed != 0
		state.CwdGroups = append(state.CwdGroups, row)
	}
	if err := groupRows.Err(); err != nil {
		return AppStateRow{}, fmt.Errorf("iterate cwd group rows: %w", err)
	}
	return state, nil
}

func (c *SessionCatalog) ReplaceAppState(ctx context.Context, state AppStateRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin app state replace: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_recent_cwds`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("clear recent cwds: %w", err)
	}
	for idx, cwd := range state.RecentCwds {
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_recent_cwds(ordinal, cwd) VALUES(?, ?)`, idx, cwd); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert recent cwd %q: %w", cwd, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cwd_groups`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("clear cwd groups: %w", err)
	}
	for _, row := range state.CwdGroups {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cwd_groups(cwd, label, collapsed) VALUES(?, ?, ?)`, row.CWD, row.Label, boolToInt(row.Collapsed)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert cwd group %q: %w", row.CWD, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit app state: %w", err)
	}
	return nil
}

func upsertSessionTx(ctx context.Context, tx *sql.Tx, row SessionRow) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_catalog(
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
		return fmt.Errorf("upsert session %q: %w", row.SessionID, err)
	}
	return nil
}

func replaceQueueTx(ctx context.Context, tx *sql.Tx, sessionID string, items []QueueItemRow) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_queue_items WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("clear session queue %q: %w", sessionID, err)
	}
	for idx, item := range items {
		ordinal := item.Ordinal
		if ordinal < 0 {
			ordinal = idx
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_queue_items(session_id, ordinal, item_id, text, state) VALUES(?, ?, ?, ?, ?)`, sessionID, ordinal, item.ItemID, item.Text, item.State); err != nil {
			return fmt.Errorf("insert session queue item %q/%q: %w", sessionID, item.ItemID, err)
		}
	}
	return nil
}

func upsertLiveStateTx(ctx context.Context, tx *sql.Tx, sessionID string, row LiveStateRow) error {
	updatedAt := row.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO session_live_state(
		session_id, busy, tail_seq, tail_owner, tail_turn_id, partial_turn_id, partial_text,
		ui_request_json, transport_generation_id, transport_state, transport_reset_required, transport_reason,
		resume_session_cursor, resume_ui_cursor, resume_transport_cursor, context_usage_json, turn_timing_json,
		runtime_agent_running, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(session_id) DO UPDATE SET
		busy = excluded.busy,
		tail_seq = excluded.tail_seq,
		tail_owner = excluded.tail_owner,
		tail_turn_id = excluded.tail_turn_id,
		partial_turn_id = excluded.partial_turn_id,
		partial_text = excluded.partial_text,
		ui_request_json = excluded.ui_request_json,
		transport_generation_id = excluded.transport_generation_id,
		transport_state = excluded.transport_state,
		transport_reset_required = excluded.transport_reset_required,
		transport_reason = excluded.transport_reason,
		resume_session_cursor = excluded.resume_session_cursor,
		resume_ui_cursor = excluded.resume_ui_cursor,
		resume_transport_cursor = excluded.resume_transport_cursor,
		context_usage_json = excluded.context_usage_json,
		turn_timing_json = excluded.turn_timing_json,
		runtime_agent_running = excluded.runtime_agent_running,
		updated_at = excluded.updated_at`,
		sessionID,
		boolToInt(row.Busy),
		row.TailSeq,
		row.TailOwner,
		row.TailTurnID,
		row.PartialTurnID,
		row.PartialText,
		row.UIRequestJSON,
		row.TransportGenerationID,
		row.TransportState,
		boolToInt(row.TransportResetRequired),
		row.TransportReason,
		row.ResumeSessionCursor,
		row.ResumeUICursor,
		row.ResumeTransportCursor,
		row.ContextUsageJSON,
		row.TurnTimingJSON,
		boolToInt(row.RuntimeAgentRunning),
		formatTime(updatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert live state %q: %w", sessionID, err)
	}
	return nil
}

func (c *SessionCatalog) lookupLiveState(ctx context.Context, sessionID string) (LiveStateRow, error) {
	var row LiveStateRow
	var updatedAt string
	var busy, resetRequired, runtimeAgentRunning int
	err := c.db.QueryRowContext(ctx, `SELECT
		busy, tail_seq, tail_owner, tail_turn_id, partial_turn_id, partial_text,
		ui_request_json, transport_generation_id, transport_state, transport_reset_required, transport_reason,
		resume_session_cursor, resume_ui_cursor, resume_transport_cursor, context_usage_json, turn_timing_json,
		runtime_agent_running, updated_at
		FROM session_live_state WHERE session_id = ?`, sessionID).Scan(
		&busy,
		&row.TailSeq,
		&row.TailOwner,
		&row.TailTurnID,
		&row.PartialTurnID,
		&row.PartialText,
		&row.UIRequestJSON,
		&row.TransportGenerationID,
		&row.TransportState,
		&resetRequired,
		&row.TransportReason,
		&row.ResumeSessionCursor,
		&row.ResumeUICursor,
		&row.ResumeTransportCursor,
		&row.ContextUsageJSON,
		&row.TurnTimingJSON,
		&runtimeAgentRunning,
		&updatedAt,
	)
	if err == sql.ErrNoRows {
		return LiveStateRow{}, nil
	}
	if err != nil {
		return LiveStateRow{}, fmt.Errorf("lookup live state %q: %w", sessionID, err)
	}
	parsed, err := parseTime(updatedAt)
	if err != nil {
		return LiveStateRow{}, fmt.Errorf("parse live state updated_at %q: %w", sessionID, err)
	}
	row.Busy = busy != 0
	row.TransportResetRequired = resetRequired != 0
	row.RuntimeAgentRunning = runtimeAgentRunning != 0
	row.UpdatedAt = parsed
	return row, nil
}

func replaceWorkspaceTx(ctx context.Context, tx *sql.Tx, sessionID string, state WorkspaceStateRow) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_workspace_state(session_id, selected_path) VALUES(?, ?)
	ON CONFLICT(session_id) DO UPDATE SET selected_path = excluded.selected_path`, sessionID, state.SelectedPath); err != nil {
		return fmt.Errorf("upsert workspace state %q: %w", sessionID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_workspace_open_paths WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("clear workspace open paths %q: %w", sessionID, err)
	}
	for idx, path := range state.OpenPaths {
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_workspace_open_paths(session_id, ordinal, path) VALUES(?, ?, ?)`, sessionID, idx, path); err != nil {
			return fmt.Errorf("insert workspace open path %q/%q: %w", sessionID, path, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_workspace_history_items WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("clear workspace history %q: %w", sessionID, err)
	}
	for idx, item := range state.HistoryItems {
		ordinal := item.Ordinal
		if ordinal < 0 {
			ordinal = idx
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_workspace_history_items(session_id, ordinal, path, label) VALUES(?, ?, ?, ?)`, sessionID, ordinal, item.Path, item.Label); err != nil {
			return fmt.Errorf("insert workspace history %q/%q: %w", sessionID, item.Path, err)
		}
	}
	return nil
}

func (c *SessionCatalog) listQueueRows(ctx context.Context, sessionID string) ([]QueueItemRow, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT ordinal, item_id, text, state FROM session_queue_items WHERE session_id = ? ORDER BY ordinal ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session queue %q: %w", sessionID, err)
	}
	defer rows.Close()
	items := make([]QueueItemRow, 0)
	for rows.Next() {
		var row QueueItemRow
		if err := rows.Scan(&row.Ordinal, &row.ItemID, &row.Text, &row.State); err != nil {
			return nil, fmt.Errorf("scan session queue row: %w", err)
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session queue rows: %w", err)
	}
	return items, nil
}

func (c *SessionCatalog) lookupWorkspaceState(ctx context.Context, sessionID string) (WorkspaceStateRow, error) {
	state := WorkspaceStateRow{OpenPaths: []string{}, HistoryItems: []WorkspaceHistoryItemRow{}}
	var selected sql.NullString
	err := c.db.QueryRowContext(ctx, `SELECT selected_path FROM session_workspace_state WHERE session_id = ? LIMIT 1`, sessionID).Scan(&selected)
	switch {
	case err == nil:
		if selected.Valid {
			state.SelectedPath = selected.String
		}
	case err != nil && err != sql.ErrNoRows:
		return WorkspaceStateRow{}, fmt.Errorf("query workspace state %q: %w", sessionID, err)
	}

	openRows, err := c.db.QueryContext(ctx, `SELECT path FROM session_workspace_open_paths WHERE session_id = ? ORDER BY ordinal ASC`, sessionID)
	if err != nil {
		return WorkspaceStateRow{}, fmt.Errorf("query workspace open paths %q: %w", sessionID, err)
	}
	defer openRows.Close()
	for openRows.Next() {
		var path string
		if err := openRows.Scan(&path); err != nil {
			return WorkspaceStateRow{}, fmt.Errorf("scan workspace open path row: %w", err)
		}
		state.OpenPaths = append(state.OpenPaths, path)
	}
	if err := openRows.Err(); err != nil {
		return WorkspaceStateRow{}, fmt.Errorf("iterate workspace open path rows: %w", err)
	}

	historyRows, err := c.db.QueryContext(ctx, `SELECT ordinal, path, label FROM session_workspace_history_items WHERE session_id = ? ORDER BY ordinal ASC`, sessionID)
	if err != nil {
		return WorkspaceStateRow{}, fmt.Errorf("query workspace history %q: %w", sessionID, err)
	}
	defer historyRows.Close()
	for historyRows.Next() {
		var item WorkspaceHistoryItemRow
		if err := historyRows.Scan(&item.Ordinal, &item.Path, &item.Label); err != nil {
			return WorkspaceStateRow{}, fmt.Errorf("scan workspace history row: %w", err)
		}
		state.HistoryItems = append(state.HistoryItems, item)
	}
	if err := historyRows.Err(); err != nil {
		return WorkspaceStateRow{}, fmt.Errorf("iterate workspace history rows: %w", err)
	}
	return state, nil
}
