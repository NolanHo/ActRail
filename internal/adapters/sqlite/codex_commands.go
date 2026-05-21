package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type CodexSessionCommandRow struct {
	CommandID    string
	SessionID    string
	RuntimeID    string
	Kind         string
	Text         string
	MessageID    string
	FollowUp     bool
	State        string
	AttemptCount int
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ClaimedAt    *time.Time
	AcceptedAt   *time.Time
	ReflectedAt  *time.Time
	CompletedAt  *time.Time
}

func (c *SessionCatalog) InsertCodexSessionCommand(ctx context.Context, row CodexSessionCommandRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	if err := upsertCodexSessionCommandTx(ctx, c.db, row); err != nil {
		return fmt.Errorf("insert codex session command %q: %w", row.CommandID, err)
	}
	return nil
}

func (c *SessionCatalog) UpsertSessionSnapshotWithCodexCommand(ctx context.Context, snapshot SessionSnapshotRow, command CodexSessionCommandRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session snapshot command upsert: %w", err)
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
	if err := upsertCodexSessionCommandTx(ctx, tx, command); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session snapshot command %q/%q: %w", snapshot.Session.SessionID, command.CommandID, err)
	}
	return nil
}

func (c *SessionCatalog) ListOpenCodexSessionCommands(ctx context.Context, sessionID string) ([]CodexSessionCommandRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT command_id, session_id, runtime_id, kind, text, message_id, follow_up, state, attempt_count, last_error, created_at, updated_at, claimed_at, accepted_at, reflected_at, completed_at
		FROM codex_session_commands
		WHERE session_id = ? AND state IN ('pending', 'dispatching', 'accepted', 'reflected')
		ORDER BY created_at ASC, command_id ASC`, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, fmt.Errorf("query open codex session commands %q: %w", sessionID, err)
	}
	defer rows.Close()
	items := make([]CodexSessionCommandRow, 0)
	for rows.Next() {
		row, err := scanCodexSessionCommand(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open codex session commands %q: %w", sessionID, err)
	}
	return items, nil
}

func (c *SessionCatalog) UpdateCodexSessionCommandState(ctx context.Context, commandID, state, runtimeID, lastError string, updatedAt time.Time) (bool, error) {
	if c == nil || c.db == nil {
		return false, fmt.Errorf("sqlite catalog is not initialized")
	}
	now := formatTime(updatedAt)
	state = strings.TrimSpace(state)
	if state == "" {
		return false, fmt.Errorf("codex session command state is required")
	}
	result, err := c.db.ExecContext(ctx, `UPDATE codex_session_commands SET
			state = ?,
			runtime_id = CASE WHEN ? = '' THEN runtime_id ELSE ? END,
			last_error = ?,
			attempt_count = attempt_count + CASE WHEN ? = 'dispatching' THEN 1 ELSE 0 END,
			updated_at = ?,
			claimed_at = CASE WHEN ? = 'dispatching' THEN ? ELSE claimed_at END,
			accepted_at = CASE WHEN ? = 'accepted' THEN ? ELSE accepted_at END,
			reflected_at = CASE WHEN ? = 'reflected' THEN ? ELSE reflected_at END,
			completed_at = CASE WHEN ? IN ('completed', 'failed', 'cancelled', 'rejected') THEN ? ELSE completed_at END
		WHERE command_id = ?`,
		state,
		strings.TrimSpace(runtimeID), strings.TrimSpace(runtimeID),
		strings.TrimSpace(lastError),
		state,
		now,
		state, now,
		state, now,
		state, now,
		state, now,
		strings.TrimSpace(commandID))
	if err != nil {
		return false, fmt.Errorf("update codex session command %q: %w", commandID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("update codex session command %q rows affected: %w", commandID, err)
	}
	return affected > 0, nil
}

type codexSessionCommandScanner interface {
	Scan(dest ...any) error
}

type codexSessionCommandExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func upsertCodexSessionCommandTx(ctx context.Context, tx codexSessionCommandExecer, row CodexSessionCommandRow) error {
	row, err := normalizeCodexSessionCommandRow(row)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO codex_session_commands(
			command_id, session_id, runtime_id, kind, text, message_id, follow_up,
			state, attempt_count, last_error, created_at, updated_at,
			claimed_at, accepted_at, reflected_at, completed_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.CommandID,
		row.SessionID,
		row.RuntimeID,
		row.Kind,
		row.Text,
		row.MessageID,
		boolToInt(row.FollowUp),
		row.State,
		row.AttemptCount,
		row.LastError,
		formatTime(row.CreatedAt),
		formatTime(row.UpdatedAt),
		formatNullableTime(row.ClaimedAt),
		formatNullableTime(row.AcceptedAt),
		formatNullableTime(row.ReflectedAt),
		formatNullableTime(row.CompletedAt))
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected > 0 {
		return nil
	}
	if err := ensureCodexSessionCommandDuplicateMatches(ctx, tx, row); err != nil {
		return err
	}
	return nil
}

func normalizeCodexSessionCommandRow(row CodexSessionCommandRow) (CodexSessionCommandRow, error) {
	row.CommandID = strings.TrimSpace(row.CommandID)
	row.SessionID = strings.TrimSpace(row.SessionID)
	if row.CommandID == "" {
		return CodexSessionCommandRow{}, fmt.Errorf("command_id is required")
	}
	if row.SessionID == "" {
		return CodexSessionCommandRow{}, fmt.Errorf("session_id is required")
	}
	row.State = strings.TrimSpace(row.State)
	if row.State == "" {
		row.State = "pending"
	}
	row.Kind = strings.TrimSpace(row.Kind)
	if row.Kind == "" {
		row.Kind = "send"
	}
	row.RuntimeID = strings.TrimSpace(row.RuntimeID)
	row.MessageID = strings.TrimSpace(row.MessageID)
	row.LastError = strings.TrimSpace(row.LastError)
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = row.CreatedAt
	}
	return row, nil
}

func ensureCodexSessionCommandDuplicateMatches(ctx context.Context, tx codexSessionCommandExecer, row CodexSessionCommandRow) error {
	var (
		sessionID string
		kind      string
		text      string
		messageID string
		followUp  int
	)
	if err := tx.QueryRowContext(ctx, `SELECT session_id, kind, text, message_id, follow_up
		FROM codex_session_commands
		WHERE command_id = ?`, row.CommandID).Scan(&sessionID, &kind, &text, &messageID, &followUp); err != nil {
		return fmt.Errorf("lookup duplicate codex session command %q: %w", row.CommandID, err)
	}
	if sessionID != row.SessionID || kind != row.Kind || text != row.Text || messageID != row.MessageID || (followUp != 0) != row.FollowUp {
		return fmt.Errorf("codex session command %q conflicts with existing ledger row", row.CommandID)
	}
	return nil
}

func scanCodexSessionCommand(scanner codexSessionCommandScanner) (CodexSessionCommandRow, error) {
	var (
		row                                                 CodexSessionCommandRow
		followUp                                            int
		createdRaw                                          string
		updatedRaw                                          string
		claimedRaw, acceptedRaw, reflectedRaw, completedRaw sql.NullString
	)
	if err := scanner.Scan(
		&row.CommandID,
		&row.SessionID,
		&row.RuntimeID,
		&row.Kind,
		&row.Text,
		&row.MessageID,
		&followUp,
		&row.State,
		&row.AttemptCount,
		&row.LastError,
		&createdRaw,
		&updatedRaw,
		&claimedRaw,
		&acceptedRaw,
		&reflectedRaw,
		&completedRaw,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CodexSessionCommandRow{}, err
		}
		return CodexSessionCommandRow{}, fmt.Errorf("scan codex session command: %w", err)
	}
	var err error
	row.FollowUp = followUp != 0
	if row.CreatedAt, err = parseTime(createdRaw); err != nil {
		return CodexSessionCommandRow{}, err
	}
	if row.UpdatedAt, err = parseTime(updatedRaw); err != nil {
		return CodexSessionCommandRow{}, err
	}
	if row.ClaimedAt, err = parseNullableTime(claimedRaw); err != nil {
		return CodexSessionCommandRow{}, err
	}
	if row.AcceptedAt, err = parseNullableTime(acceptedRaw); err != nil {
		return CodexSessionCommandRow{}, err
	}
	if row.ReflectedAt, err = parseNullableTime(reflectedRaw); err != nil {
		return CodexSessionCommandRow{}, err
	}
	if row.CompletedAt, err = parseNullableTime(completedRaw); err != nil {
		return CodexSessionCommandRow{}, err
	}
	return row, nil
}
