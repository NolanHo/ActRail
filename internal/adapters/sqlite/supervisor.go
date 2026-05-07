package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const defaultContextFilesJSON = "[]"

type SupervisorProviderSettingsRow struct {
	BaseURL   string
	APIKey    string
	Model     string
	UpdatedAt time.Time
}

type SessionSupervisorConfigRow struct {
	SessionID                string
	Enabled                  bool
	IdleAfterMinutes         int
	MaxConsecutiveInjections int
	ConsecutiveInjections    int
	Goal                     string
	AcceptanceCriteria       string
	ContextFiles             []string
	UpdatedAt                time.Time
}

type SupervisorRunRow struct {
	RunID                   string
	SessionID               string
	AnchorAssistantEventID  string
	AnchorAssistantTS       float64
	AnchorAssistantTextHash string
	Status                  string
	Action                  string
	InjectedText            string
	Reason                  string
	Error                   string
	Model                   string
	BaseURL                 string
	SnapshotJSON            string
	RawOutput               string
	CreatedAt               time.Time
}

func (c *SessionCatalog) LookupSupervisorProviderSettings(ctx context.Context) (SupervisorProviderSettingsRow, bool, error) {
	row := c.db.QueryRowContext(ctx, `SELECT base_url, api_key, model, updated_at FROM supervisor_provider_settings WHERE id = 1`)
	var out SupervisorProviderSettingsRow
	var updatedAt string
	if err := row.Scan(&out.BaseURL, &out.APIKey, &out.Model, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return SupervisorProviderSettingsRow{}, false, nil
		}
		return SupervisorProviderSettingsRow{}, false, err
	}
	ts, err := parseTime(updatedAt)
	if err != nil {
		return SupervisorProviderSettingsRow{}, false, err
	}
	out.UpdatedAt = ts
	return out, true, nil
}

func (c *SessionCatalog) UpsertSupervisorProviderSettings(ctx context.Context, row SupervisorProviderSettingsRow) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO supervisor_provider_settings(id, base_url, api_key, model, updated_at)
		VALUES(1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET base_url = excluded.base_url, api_key = excluded.api_key, model = excluded.model, updated_at = excluded.updated_at`,
		strings.TrimSpace(row.BaseURL), strings.TrimSpace(row.APIKey), strings.TrimSpace(row.Model), formatTime(row.UpdatedAt))
	return err
}

func (c *SessionCatalog) LookupSessionSupervisorConfig(ctx context.Context, sessionID string) (SessionSupervisorConfigRow, bool, error) {
	row := c.db.QueryRowContext(ctx, `SELECT session_id, enabled, idle_after_minutes, max_consecutive_injections, consecutive_injections, goal, acceptance_criteria, context_files_json, updated_at
		FROM session_supervisor_config WHERE session_id = ?`, sessionID)
	out, err := scanSessionSupervisorConfigRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return SessionSupervisorConfigRow{}, false, nil
		}
		return SessionSupervisorConfigRow{}, false, err
	}
	return out, true, nil
}

func (c *SessionCatalog) UpsertSessionSupervisorConfig(ctx context.Context, row SessionSupervisorConfigRow) error {
	filesJSON, err := json.Marshal(row.ContextFiles)
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx, `INSERT INTO session_supervisor_config(session_id, enabled, idle_after_minutes, max_consecutive_injections, consecutive_injections, goal, acceptance_criteria, context_files_json, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET enabled = excluded.enabled, idle_after_minutes = excluded.idle_after_minutes, max_consecutive_injections = excluded.max_consecutive_injections, consecutive_injections = excluded.consecutive_injections, goal = excluded.goal, acceptance_criteria = excluded.acceptance_criteria, context_files_json = excluded.context_files_json, updated_at = excluded.updated_at`,
		strings.TrimSpace(row.SessionID), boolToInt(row.Enabled), row.IdleAfterMinutes, row.MaxConsecutiveInjections, row.ConsecutiveInjections, strings.TrimSpace(row.Goal), strings.TrimSpace(row.AcceptanceCriteria), string(filesJSON), formatTime(row.UpdatedAt))
	return err
}

func (c *SessionCatalog) InsertSupervisorRun(ctx context.Context, row SupervisorRunRow) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO supervisor_runs(run_id, session_id, anchor_assistant_event_id, anchor_assistant_ts, anchor_assistant_text_hash, status, action, injected_text, reason, error, model, base_url, snapshot_json, raw_output, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.RunID, row.SessionID, row.AnchorAssistantEventID, row.AnchorAssistantTS, row.AnchorAssistantTextHash, row.Status, row.Action, row.InjectedText, row.Reason, row.Error, row.Model, row.BaseURL, row.SnapshotJSON, row.RawOutput, formatTime(row.CreatedAt))
	return err
}

func (c *SessionCatalog) UpdateSupervisorRun(ctx context.Context, row SupervisorRunRow) error {
	res, err := c.db.ExecContext(ctx, `UPDATE supervisor_runs SET
		session_id = ?,
		anchor_assistant_event_id = ?,
		anchor_assistant_ts = ?,
		anchor_assistant_text_hash = ?,
		status = ?,
		action = ?,
		injected_text = ?,
		reason = ?,
		error = ?,
		model = ?,
		base_url = ?,
		snapshot_json = ?,
		raw_output = ?,
		created_at = ?
		WHERE run_id = ?`,
		row.SessionID, row.AnchorAssistantEventID, row.AnchorAssistantTS, row.AnchorAssistantTextHash, row.Status, row.Action, row.InjectedText, row.Reason, row.Error, row.Model, row.BaseURL, row.SnapshotJSON, row.RawOutput, formatTime(row.CreatedAt), row.RunID)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("update supervisor run %q: not found", row.RunID)
	}
	return nil
}

func (c *SessionCatalog) ListSupervisorRuns(ctx context.Context, sessionID string, limit int) ([]SupervisorRunRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := c.db.QueryContext(ctx, `SELECT run_id, session_id, anchor_assistant_event_id, anchor_assistant_ts, anchor_assistant_text_hash, status, action, injected_text, reason, error, model, base_url, snapshot_json, raw_output, created_at
		FROM supervisor_runs WHERE session_id = ? ORDER BY created_at DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SupervisorRunRow
	for rows.Next() {
		row, err := scanSupervisorRunRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (c *SessionCatalog) LookupSupervisorRunByAnchor(ctx context.Context, sessionID, anchor string) (SupervisorRunRow, bool, error) {
	row := c.db.QueryRowContext(ctx, `SELECT run_id, session_id, anchor_assistant_event_id, anchor_assistant_ts, anchor_assistant_text_hash, status, action, injected_text, reason, error, model, base_url, snapshot_json, raw_output, created_at
		FROM supervisor_runs WHERE session_id = ? AND anchor_assistant_event_id = ?`, sessionID, anchor)
	out, err := scanSupervisorRunRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return SupervisorRunRow{}, false, nil
		}
		return SupervisorRunRow{}, false, err
	}
	return out, true, nil
}

func scanSessionSupervisorConfigRow(scanner interface{ Scan(...any) error }) (SessionSupervisorConfigRow, error) {
	var row SessionSupervisorConfigRow
	var enabled int
	var filesJSON string
	var updatedAt string
	if err := scanner.Scan(&row.SessionID, &enabled, &row.IdleAfterMinutes, &row.MaxConsecutiveInjections, &row.ConsecutiveInjections, &row.Goal, &row.AcceptanceCriteria, &filesJSON, &updatedAt); err != nil {
		return SessionSupervisorConfigRow{}, err
	}
	row.Enabled = enabled != 0
	if strings.TrimSpace(filesJSON) == "" {
		filesJSON = defaultContextFilesJSON
	}
	if err := json.Unmarshal([]byte(filesJSON), &row.ContextFiles); err != nil {
		return SessionSupervisorConfigRow{}, fmt.Errorf("parse supervisor context files: %w", err)
	}
	ts, err := parseTime(updatedAt)
	if err != nil {
		return SessionSupervisorConfigRow{}, err
	}
	row.UpdatedAt = ts
	return row, nil
}

func scanSupervisorRunRow(scanner interface{ Scan(...any) error }) (SupervisorRunRow, error) {
	var row SupervisorRunRow
	var createdAt string
	if err := scanner.Scan(&row.RunID, &row.SessionID, &row.AnchorAssistantEventID, &row.AnchorAssistantTS, &row.AnchorAssistantTextHash, &row.Status, &row.Action, &row.InjectedText, &row.Reason, &row.Error, &row.Model, &row.BaseURL, &row.SnapshotJSON, &row.RawOutput, &createdAt); err != nil {
		return SupervisorRunRow{}, err
	}
	ts, err := parseTime(createdAt)
	if err != nil {
		return SupervisorRunRow{}, err
	}
	row.CreatedAt = ts
	return row, nil
}
