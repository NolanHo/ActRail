package sqlite

import (
	"context"
	"fmt"
	"time"
)

type SessionReadStateRow struct {
	SessionID string
	ReadSeq   uint64
	ReadAt    time.Time
}

func (c *SessionCatalog) UpsertSessionReadState(ctx context.Context, row SessionReadStateRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	readAt := row.ReadAt
	if readAt.IsZero() {
		readAt = time.Now().UTC()
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO session_read_state(session_id, read_seq, read_at)
		VALUES(?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			read_seq = MAX(session_read_state.read_seq, excluded.read_seq),
			read_at = excluded.read_at`,
		row.SessionID,
		row.ReadSeq,
		formatTime(readAt),
	)
	if err != nil {
		return fmt.Errorf("upsert session read state %q: %w", row.SessionID, err)
	}
	return nil
}

func (c *SessionCatalog) ListSessionReadStates(ctx context.Context) ([]SessionReadStateRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	rows, err := c.db.QueryContext(ctx, `SELECT session_id, read_seq, read_at FROM session_read_state ORDER BY session_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query session read states: %w", err)
	}
	defer rows.Close()

	items := make([]SessionReadStateRow, 0)
	for rows.Next() {
		var (
			row    SessionReadStateRow
			readAt string
		)
		if err := rows.Scan(&row.SessionID, &row.ReadSeq, &readAt); err != nil {
			return nil, fmt.Errorf("scan session read state row: %w", err)
		}
		parsed, err := parseTime(readAt)
		if err != nil {
			return nil, fmt.Errorf("parse session read state %q read_at: %w", row.SessionID, err)
		}
		row.ReadAt = parsed
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session read states: %w", err)
	}
	return items, nil
}
