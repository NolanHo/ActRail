package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type WaitThreadRow struct {
	ThreadID  string
	SessionID string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
}

type WaitRow struct {
	WaitID           string
	ThreadID         string
	SessionID        string
	RequestID        string
	State            string
	Question         string
	Context          string
	BlockingReason   string
	Attempted        string
	DefaultIfNoReply string
	Answer           string
	FallbackUsed     string
	ClaimedAt        *time.Time
	AnsweredAt       *time.Time
	CancelledAt      *time.Time
	TimedOutAt       *time.Time
	OrphanedAt       *time.Time
	TimeoutAt        *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Files            []string
}

func withTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *SessionCatalog) InsertWait(ctx context.Context, thread WaitThreadRow, wait WaitRow) error {
	return withTx(ctx, c.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO wait_threads(thread_id, session_id, title, created_at, updated_at, closed_at)
			VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(thread_id) DO UPDATE SET title=excluded.title, updated_at=excluded.updated_at, closed_at=NULL`,
			thread.ThreadID, thread.SessionID, strings.TrimSpace(thread.Title), formatTime(thread.CreatedAt), formatTime(thread.UpdatedAt), formatNullableTime(thread.ClosedAt)); err != nil {
			return err
		}
		filesJSON, err := json.Marshal(wait.Files)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO waits(
			wait_id, thread_id, session_id, request_id, state, question, context, blocking_reason, attempted, default_if_no_reply,
			answer, fallback_used, claimed_at, answered_at, cancelled_at, timed_out_at, orphaned_at, timeout_at, created_at, updated_at, files_json
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			wait.WaitID, wait.ThreadID, wait.SessionID, wait.RequestID, wait.State, wait.Question, wait.Context, wait.BlockingReason, wait.Attempted, wait.DefaultIfNoReply,
			wait.Answer, wait.FallbackUsed, formatNullableTime(wait.ClaimedAt), formatNullableTime(wait.AnsweredAt), formatNullableTime(wait.CancelledAt), formatNullableTime(wait.TimedOutAt), formatNullableTime(wait.OrphanedAt), formatNullableTime(wait.TimeoutAt), formatTime(wait.CreatedAt), formatTime(wait.UpdatedAt), string(filesJSON))
		return err
	})
}

func (c *SessionCatalog) LookupWait(ctx context.Context, sessionID, waitID string) (WaitRow, bool, error) {
	row, err := scanWaitRow(c.db.QueryRowContext(ctx, `SELECT wait_id, thread_id, session_id, request_id, state, question, context, blocking_reason, attempted, default_if_no_reply,
		answer, fallback_used, claimed_at, answered_at, cancelled_at, timed_out_at, orphaned_at, timeout_at, created_at, updated_at, files_json
		FROM waits WHERE session_id = ? AND wait_id = ?`, sessionID, waitID))
	if err != nil {
		if err == sql.ErrNoRows {
			return WaitRow{}, false, nil
		}
		return WaitRow{}, false, err
	}
	return row, true, nil
}

func (c *SessionCatalog) UpdateWait(ctx context.Context, wait WaitRow) error {
	filesJSON, err := json.Marshal(wait.Files)
	if err != nil {
		return err
	}
	return withTx(ctx, c.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE waits SET state=?, answer=?, fallback_used=?, claimed_at=?, answered_at=?, cancelled_at=?, timed_out_at=?, orphaned_at=?, timeout_at=?, updated_at=?, files_json=?
			WHERE session_id=? AND wait_id=?`,
			wait.State, wait.Answer, wait.FallbackUsed, formatNullableTime(wait.ClaimedAt), formatNullableTime(wait.AnsweredAt), formatNullableTime(wait.CancelledAt), formatNullableTime(wait.TimedOutAt), formatNullableTime(wait.OrphanedAt), formatNullableTime(wait.TimeoutAt), formatTime(wait.UpdatedAt), string(filesJSON), wait.SessionID, wait.WaitID); err != nil {
			return err
		}
		closedAt := any(nil)
		if !isActiveWaitState(wait.State) {
			closedAt = formatTime(wait.UpdatedAt)
		}
		_, err := tx.ExecContext(ctx, `UPDATE wait_threads SET updated_at=?, closed_at=CASE WHEN ? IS NULL THEN closed_at ELSE ? END WHERE thread_id=? AND session_id=?`, formatTime(wait.UpdatedAt), closedAt, closedAt, wait.ThreadID, wait.SessionID)
		return err
	})
}

func (c *SessionCatalog) ListActiveWaits(ctx context.Context) ([]WaitRow, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT wait_id, thread_id, session_id, request_id, state, question, context, blocking_reason, attempted, default_if_no_reply,
		answer, fallback_used, claimed_at, answered_at, cancelled_at, timed_out_at, orphaned_at, timeout_at, created_at, updated_at, files_json
		FROM waits WHERE state IN ('pending_unread', 'claimed') ORDER BY updated_at DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWaitRows(rows)
}

func (c *SessionCatalog) ListSessionWaitThreads(ctx context.Context, sessionID string) ([]WaitThreadRow, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT thread_id, session_id, title, created_at, updated_at, closed_at FROM wait_threads WHERE session_id = ? ORDER BY updated_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WaitThreadRow
	for rows.Next() {
		row, err := scanWaitThreadRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (c *SessionCatalog) ListThreadWaits(ctx context.Context, sessionID, threadID string) (WaitThreadRow, []WaitRow, bool, error) {
	thread, err := scanWaitThreadRow(c.db.QueryRowContext(ctx, `SELECT thread_id, session_id, title, created_at, updated_at, closed_at FROM wait_threads WHERE session_id = ? AND thread_id = ?`, sessionID, threadID))
	if err != nil {
		if err == sql.ErrNoRows {
			return WaitThreadRow{}, nil, false, nil
		}
		return WaitThreadRow{}, nil, false, err
	}
	rows, err := c.db.QueryContext(ctx, `SELECT wait_id, thread_id, session_id, request_id, state, question, context, blocking_reason, attempted, default_if_no_reply,
		answer, fallback_used, claimed_at, answered_at, cancelled_at, timed_out_at, orphaned_at, timeout_at, created_at, updated_at, files_json
		FROM waits WHERE session_id = ? AND thread_id = ? ORDER BY created_at DESC`, sessionID, threadID)
	if err != nil {
		return WaitThreadRow{}, nil, false, err
	}
	defer rows.Close()
	waits, err := scanWaitRows(rows)
	return thread, waits, true, err
}

func scanWaitRows(rows *sql.Rows) ([]WaitRow, error) {
	var out []WaitRow
	for rows.Next() {
		row, err := scanWaitRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanWaitThreadRow(scanner interface{ Scan(...any) error }) (WaitThreadRow, error) {
	var row WaitThreadRow
	var createdAt, updatedAt string
	var closedAt sql.NullString
	if err := scanner.Scan(&row.ThreadID, &row.SessionID, &row.Title, &createdAt, &updatedAt, &closedAt); err != nil {
		return WaitThreadRow{}, err
	}
	var err error
	row.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return WaitThreadRow{}, err
	}
	row.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return WaitThreadRow{}, err
	}
	row.ClosedAt, err = parseNullableTime(closedAt)
	if err != nil {
		return WaitThreadRow{}, err
	}
	return row, nil
}

func scanWaitRow(scanner interface{ Scan(...any) error }) (WaitRow, error) {
	var row WaitRow
	var claimedAt, answeredAt, cancelledAt, timedOutAt, orphanedAt, timeoutAt sql.NullString
	var createdAt, updatedAt, filesJSON string
	if err := scanner.Scan(
		&row.WaitID, &row.ThreadID, &row.SessionID, &row.RequestID, &row.State, &row.Question, &row.Context, &row.BlockingReason, &row.Attempted, &row.DefaultIfNoReply,
		&row.Answer, &row.FallbackUsed, &claimedAt, &answeredAt, &cancelledAt, &timedOutAt, &orphanedAt, &timeoutAt, &createdAt, &updatedAt, &filesJSON,
	); err != nil {
		return WaitRow{}, err
	}
	var err error
	if row.ClaimedAt, err = parseNullableTime(claimedAt); err != nil {
		return WaitRow{}, err
	}
	if row.AnsweredAt, err = parseNullableTime(answeredAt); err != nil {
		return WaitRow{}, err
	}
	if row.CancelledAt, err = parseNullableTime(cancelledAt); err != nil {
		return WaitRow{}, err
	}
	if row.TimedOutAt, err = parseNullableTime(timedOutAt); err != nil {
		return WaitRow{}, err
	}
	if row.OrphanedAt, err = parseNullableTime(orphanedAt); err != nil {
		return WaitRow{}, err
	}
	if row.TimeoutAt, err = parseNullableTime(timeoutAt); err != nil {
		return WaitRow{}, err
	}
	if row.CreatedAt, err = parseTime(createdAt); err != nil {
		return WaitRow{}, err
	}
	if row.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return WaitRow{}, err
	}
	if strings.TrimSpace(filesJSON) != "" {
		if err := json.Unmarshal([]byte(filesJSON), &row.Files); err != nil {
			return WaitRow{}, fmt.Errorf("decode wait files: %w", err)
		}
	}
	return row, nil
}

func isActiveWaitState(state string) bool {
	return state == "pending_unread" || state == "claimed"
}
