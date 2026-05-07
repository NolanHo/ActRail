package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type SchedulerSettingsRow struct {
	IdleBeforeDeliverySeconds int
	UpdatedAt                 time.Time
}

type SchedulerItemRow struct {
	ItemID    string
	SessionID string
	Kind      string
	SourceRef string
	Title     string
	Message   string
	DueAt     time.Time
	State     string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type InboxItemRow struct {
	ItemID             string
	SessionID          string
	Source             string
	SourceID           string
	Title              string
	Message            string
	Priority           int
	DueAt              time.Time
	State              string
	BlockedReason      string
	DeliveredMessageID string
	Error              string
	ClaimedAt          *time.Time
	DeliveredAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (c *SessionCatalog) LookupSchedulerSettings(ctx context.Context) (SchedulerSettingsRow, bool, error) {
	if c == nil || c.db == nil {
		return SchedulerSettingsRow{}, false, fmt.Errorf("sqlite catalog is not initialized")
	}
	var (
		row       SchedulerSettingsRow
		updatedAt string
	)
	err := c.db.QueryRowContext(ctx, `SELECT idle_before_delivery_seconds, updated_at FROM scheduler_settings WHERE id = 1`).Scan(&row.IdleBeforeDeliverySeconds, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return SchedulerSettingsRow{}, false, nil
		}
		return SchedulerSettingsRow{}, false, fmt.Errorf("lookup scheduler settings: %w", err)
	}
	ts, err := parseTime(updatedAt)
	if err != nil {
		return SchedulerSettingsRow{}, false, err
	}
	row.UpdatedAt = ts
	return row, true, nil
}

func (c *SessionCatalog) UpsertSchedulerSettings(ctx context.Context, row SchedulerSettingsRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO scheduler_settings(id, idle_before_delivery_seconds, updated_at)
		VALUES(1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			idle_before_delivery_seconds = excluded.idle_before_delivery_seconds,
			updated_at = excluded.updated_at`, row.IdleBeforeDeliverySeconds, formatTime(row.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert scheduler settings: %w", err)
	}
	return nil
}

func (c *SessionCatalog) InsertSchedulerItem(ctx context.Context, row SchedulerItemRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO scheduler_items(
		item_id, session_id, kind, source_ref, title, message, due_at, state, created_by, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.ItemID, row.SessionID, row.Kind, row.SourceRef, row.Title, row.Message, formatTime(row.DueAt), row.State, row.CreatedBy, formatTime(row.CreatedAt), formatTime(row.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert scheduler item %q: %w", row.ItemID, err)
	}
	return nil
}

func (c *SessionCatalog) LookupSchedulerItem(ctx context.Context, itemID string) (SchedulerItemRow, bool, error) {
	if c == nil || c.db == nil {
		return SchedulerItemRow{}, false, fmt.Errorf("sqlite catalog is not initialized")
	}
	var row SchedulerItemRow
	queryRow := c.db.QueryRowContext(ctx, `SELECT item_id, session_id, kind, source_ref, title, message, due_at, state, created_by, created_at, updated_at
		FROM scheduler_items WHERE item_id = ?`, itemID)
	row, err := scanSchedulerItemRow(queryRow)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SchedulerItemRow{}, false, nil
		}
		return SchedulerItemRow{}, false, err
	}
	return row, true, nil
}

func (c *SessionCatalog) ListSchedulerItems(ctx context.Context, limit int) ([]SchedulerItemRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := c.db.QueryContext(ctx, `SELECT item_id, session_id, kind, source_ref, title, message, due_at, state, created_by, created_at, updated_at
		FROM scheduler_items ORDER BY due_at ASC, created_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query scheduler items: %w", err)
	}
	defer rows.Close()
	items := []SchedulerItemRow{}
	for rows.Next() {
		row, err := scanSchedulerItemRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scheduler items: %w", err)
	}
	return items, nil
}

func (c *SessionCatalog) ListDueSchedulerItems(ctx context.Context, now time.Time, limit int) ([]SchedulerItemRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := c.db.QueryContext(ctx, `SELECT item_id, session_id, kind, source_ref, title, message, due_at, state, created_by, created_at, updated_at
		FROM scheduler_items WHERE state = 'scheduled' AND due_at <= ? ORDER BY due_at ASC, created_at ASC LIMIT ?`, formatTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("query due scheduler items: %w", err)
	}
	defer rows.Close()
	items := []SchedulerItemRow{}
	for rows.Next() {
		row, err := scanSchedulerItemRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due scheduler items: %w", err)
	}
	return items, nil
}

func (c *SessionCatalog) CountDueSchedulerItemsForSession(ctx context.Context, sessionID string, now time.Time) (int, error) {
	if c == nil || c.db == nil {
		return 0, fmt.Errorf("sqlite catalog is not initialized")
	}
	var count int
	err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_items WHERE session_id = ? AND state = 'scheduled' AND due_at <= ?`, sessionID, formatTime(now)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count due scheduler items for session %q: %w", sessionID, err)
	}
	return count, nil
}

func (c *SessionCatalog) UpdateSchedulerItem(ctx context.Context, row SchedulerItemRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	res, err := c.db.ExecContext(ctx, `UPDATE scheduler_items SET
		session_id = ?,
		kind = ?,
		source_ref = ?,
		title = ?,
		message = ?,
		due_at = ?,
		state = ?,
		created_by = ?,
		created_at = ?,
		updated_at = ?
		WHERE item_id = ?`, row.SessionID, row.Kind, row.SourceRef, row.Title, row.Message, formatTime(row.DueAt), row.State, row.CreatedBy, formatTime(row.CreatedAt), formatTime(row.UpdatedAt), row.ItemID)
	if err != nil {
		return fmt.Errorf("update scheduler item %q: %w", row.ItemID, err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("update scheduler item %q: not found", row.ItemID)
	}
	return nil
}

func (c *SessionCatalog) InsertInboxItem(ctx context.Context, row InboxItemRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO inbox_items(
		item_id, session_id, source, source_id, title, message, priority, due_at, state, blocked_reason, delivered_message_id, error, claimed_at, delivered_at, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.ItemID, row.SessionID, row.Source, row.SourceID, row.Title, row.Message, row.Priority, formatTime(row.DueAt), row.State, row.BlockedReason, row.DeliveredMessageID, row.Error, formatNullableTime(row.ClaimedAt), formatNullableTime(row.DeliveredAt), formatTime(row.CreatedAt), formatTime(row.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert inbox item %q: %w", row.ItemID, err)
	}
	return nil
}

func (c *SessionCatalog) ListReadyInboxItems(ctx context.Context, now time.Time, limit int) ([]InboxItemRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := c.db.QueryContext(ctx, `SELECT item_id, session_id, source, source_id, title, message, priority, due_at, state, blocked_reason, delivered_message_id, error, claimed_at, delivered_at, created_at, updated_at
		FROM inbox_items WHERE state = 'pending' AND due_at <= ? ORDER BY due_at ASC, priority DESC, created_at ASC LIMIT ?`, formatTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("query ready inbox items: %w", err)
	}
	defer rows.Close()
	items := []InboxItemRow{}
	for rows.Next() {
		row, err := scanInboxItemRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ready inbox items: %w", err)
	}
	return items, nil
}

func (c *SessionCatalog) CountOpenInboxItemsForSession(ctx context.Context, sessionID string) (int, error) {
	if c == nil || c.db == nil {
		return 0, fmt.Errorf("sqlite catalog is not initialized")
	}
	var count int
	err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox_items WHERE session_id = ? AND state IN ('pending', 'claimed')`, sessionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count open inbox items for session %q: %w", sessionID, err)
	}
	return count, nil
}

func (c *SessionCatalog) UpdateInboxItem(ctx context.Context, row InboxItemRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	res, err := c.db.ExecContext(ctx, `UPDATE inbox_items SET
		session_id = ?,
		source = ?,
		source_id = ?,
		title = ?,
		message = ?,
		priority = ?,
		due_at = ?,
		state = ?,
		blocked_reason = ?,
		delivered_message_id = ?,
		error = ?,
		claimed_at = ?,
		delivered_at = ?,
		created_at = ?,
		updated_at = ?
		WHERE item_id = ?`, row.SessionID, row.Source, row.SourceID, row.Title, row.Message, row.Priority, formatTime(row.DueAt), row.State, row.BlockedReason, row.DeliveredMessageID, row.Error, formatNullableTime(row.ClaimedAt), formatNullableTime(row.DeliveredAt), formatTime(row.CreatedAt), formatTime(row.UpdatedAt), row.ItemID)
	if err != nil {
		return fmt.Errorf("update inbox item %q: %w", row.ItemID, err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("update inbox item %q: not found", row.ItemID)
	}
	return nil
}

func (c *SessionCatalog) ListInboxItems(ctx context.Context, sessionID string, limit int) ([]InboxItemRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT item_id, session_id, source, source_id, title, message, priority, due_at, state, blocked_reason, delivered_message_id, error, claimed_at, delivered_at, created_at, updated_at FROM inbox_items`
	args := []any{}
	if sessionID != "" {
		query += ` WHERE session_id = ?`
		args = append(args, sessionID)
	}
	query += ` ORDER BY due_at ASC, priority DESC, created_at ASC LIMIT ?`
	args = append(args, limit)
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query inbox items: %w", err)
	}
	defer rows.Close()
	items := []InboxItemRow{}
	for rows.Next() {
		row, err := scanInboxItemRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbox items: %w", err)
	}
	return items, nil
}

func scanSchedulerItemRow(scanner interface{ Scan(...any) error }) (SchedulerItemRow, error) {
	var (
		row                         SchedulerItemRow
		dueAt, createdAt, updatedAt string
	)
	if err := scanner.Scan(&row.ItemID, &row.SessionID, &row.Kind, &row.SourceRef, &row.Title, &row.Message, &dueAt, &row.State, &row.CreatedBy, &createdAt, &updatedAt); err != nil {
		return SchedulerItemRow{}, fmt.Errorf("scan scheduler item: %w", err)
	}
	parsedDueAt, err := parseTime(dueAt)
	if err != nil {
		return SchedulerItemRow{}, err
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return SchedulerItemRow{}, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return SchedulerItemRow{}, err
	}
	row.DueAt = parsedDueAt
	row.CreatedAt = parsedCreatedAt
	row.UpdatedAt = parsedUpdatedAt
	return row, nil
}

func scanInboxItemRow(scanner interface{ Scan(...any) error }) (InboxItemRow, error) {
	var (
		row                         InboxItemRow
		dueAt, createdAt, updatedAt string
		claimedAt, deliveredAt      sql.NullString
	)
	if err := scanner.Scan(&row.ItemID, &row.SessionID, &row.Source, &row.SourceID, &row.Title, &row.Message, &row.Priority, &dueAt, &row.State, &row.BlockedReason, &row.DeliveredMessageID, &row.Error, &claimedAt, &deliveredAt, &createdAt, &updatedAt); err != nil {
		return InboxItemRow{}, fmt.Errorf("scan inbox item: %w", err)
	}
	parsedDueAt, err := parseTime(dueAt)
	if err != nil {
		return InboxItemRow{}, err
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return InboxItemRow{}, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return InboxItemRow{}, err
	}
	parsedClaimedAt, err := parseNullableTime(claimedAt)
	if err != nil {
		return InboxItemRow{}, err
	}
	parsedDeliveredAt, err := parseNullableTime(deliveredAt)
	if err != nil {
		return InboxItemRow{}, err
	}
	row.DueAt = parsedDueAt
	row.CreatedAt = parsedCreatedAt
	row.UpdatedAt = parsedUpdatedAt
	row.ClaimedAt = parsedClaimedAt
	row.DeliveredAt = parsedDeliveredAt
	return row, nil
}
