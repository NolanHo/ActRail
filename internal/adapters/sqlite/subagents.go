package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type SubagentActorRow struct {
	ActorID           string
	ChildSessionID    string
	ParentActorID     string
	ParentSessionID   string
	Name              string
	Role              string
	Status            string
	TurnID            string
	QuestionID        string
	QuestionTurnID    string
	Question          string
	QuestionContext   string
	QuestionCreatedTS float64
	QuestionDone      bool
	QuestionAnswer    string
	QuestionTerminal  string
	LastEventID       string
	LastEventAt       *time.Time
	Model             string
	CWD               string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type SubagentEventRow struct {
	ActorID         string
	Ordinal         int
	EventID         string
	Type            string
	ChildSessionID  string
	ParentActorID   string
	ParentSessionID string
	TurnID          string
	QuestionID      string
	Message         string
	Status          string
	TS              float64
}

type SubagentMessageRow struct {
	ActorID   string
	Ordinal   int
	MessageID string
	Kind      string
	Label     string
	Body      string
	TS        float64
	Meta      string
}

type SubagentSnapshotRow struct {
	Actor    SubagentActorRow
	Events   []SubagentEventRow
	Messages []SubagentMessageRow
}

func (c *SessionCatalog) ReplaceSubagentSnapshot(ctx context.Context, snapshot SubagentSnapshotRow) error {
	if c == nil || c.db == nil {
		return fmt.Errorf("sqlite catalog is not initialized")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin subagent snapshot replace: %w", err)
	}
	if err := replaceSubagentSnapshotTx(ctx, tx, snapshot); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit subagent snapshot replace: %w", err)
	}
	return nil
}

func (c *SessionCatalog) ListSubagentSnapshots(ctx context.Context) ([]SubagentSnapshotRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("sqlite catalog is not initialized")
	}
	actors, err := c.listSubagentActors(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SubagentSnapshotRow, 0, len(actors))
	for _, actor := range actors {
		events, err := c.listSubagentEvents(ctx, actor.ActorID)
		if err != nil {
			return nil, err
		}
		messages, err := c.listSubagentMessages(ctx, actor.ActorID)
		if err != nil {
			return nil, err
		}
		out = append(out, SubagentSnapshotRow{Actor: actor, Events: events, Messages: messages})
	}
	return out, nil
}

func replaceSubagentSnapshotTx(ctx context.Context, tx *sql.Tx, snapshot SubagentSnapshotRow) error {
	row := snapshot.Actor
	_, err := tx.ExecContext(ctx, `INSERT INTO subagent_actors(
		actor_id, child_session_id, parent_actor_id, parent_session_id, name, role, status, turn_id,
		question_id, question_turn_id, question, question_context, question_created_ts, question_done, question_answer, question_terminal,
		last_event_id, last_event_at, model, cwd, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(actor_id) DO UPDATE SET
		child_session_id = excluded.child_session_id,
		parent_actor_id = excluded.parent_actor_id,
		parent_session_id = excluded.parent_session_id,
		name = excluded.name,
		role = excluded.role,
		status = excluded.status,
		turn_id = excluded.turn_id,
		question_id = excluded.question_id,
		question_turn_id = excluded.question_turn_id,
		question = excluded.question,
		question_context = excluded.question_context,
		question_created_ts = excluded.question_created_ts,
		question_done = excluded.question_done,
		question_answer = excluded.question_answer,
		question_terminal = excluded.question_terminal,
		last_event_id = excluded.last_event_id,
		last_event_at = excluded.last_event_at,
		model = excluded.model,
		cwd = excluded.cwd,
		updated_at = excluded.updated_at`,
		strings.TrimSpace(row.ActorID), strings.TrimSpace(row.ChildSessionID), strings.TrimSpace(row.ParentActorID), strings.TrimSpace(row.ParentSessionID), strings.TrimSpace(row.Name), strings.TrimSpace(row.Role), strings.TrimSpace(row.Status), strings.TrimSpace(row.TurnID),
		strings.TrimSpace(row.QuestionID), strings.TrimSpace(row.QuestionTurnID), strings.TrimSpace(row.Question), strings.TrimSpace(row.QuestionContext), row.QuestionCreatedTS, boolToInt(row.QuestionDone), strings.TrimSpace(row.QuestionAnswer), strings.TrimSpace(row.QuestionTerminal), strings.TrimSpace(row.LastEventID), formatNullableTime(row.LastEventAt), strings.TrimSpace(row.Model), strings.TrimSpace(row.CWD), formatTime(row.CreatedAt), formatTime(row.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert subagent actor %q: %w", row.ActorID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM subagent_events WHERE actor_id = ?`, row.ActorID); err != nil {
		return fmt.Errorf("delete subagent events %q: %w", row.ActorID, err)
	}
	for i, event := range snapshot.Events {
		ordinal := event.Ordinal
		if ordinal <= 0 {
			ordinal = i + 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO subagent_events(actor_id, ordinal, event_id, type, child_session_id, parent_actor_id, parent_session_id, turn_id, question_id, message, status, ts)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			strings.TrimSpace(event.ActorID), ordinal, strings.TrimSpace(event.EventID), strings.TrimSpace(event.Type), strings.TrimSpace(event.ChildSessionID), strings.TrimSpace(event.ParentActorID), strings.TrimSpace(event.ParentSessionID), strings.TrimSpace(event.TurnID), strings.TrimSpace(event.QuestionID), strings.TrimSpace(event.Message), strings.TrimSpace(event.Status), event.TS); err != nil {
			return fmt.Errorf("insert subagent event %q: %w", event.EventID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM subagent_messages WHERE actor_id = ?`, row.ActorID); err != nil {
		return fmt.Errorf("delete subagent messages %q: %w", row.ActorID, err)
	}
	for i, msg := range snapshot.Messages {
		ordinal := msg.Ordinal
		if ordinal <= 0 {
			ordinal = i + 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO subagent_messages(actor_id, ordinal, message_id, kind, label, body, ts, meta)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			strings.TrimSpace(msg.ActorID), ordinal, strings.TrimSpace(msg.MessageID), strings.TrimSpace(msg.Kind), strings.TrimSpace(msg.Label), strings.TrimSpace(msg.Body), msg.TS, strings.TrimSpace(msg.Meta)); err != nil {
			return fmt.Errorf("insert subagent message %q: %w", msg.MessageID, err)
		}
	}
	return nil
}

func (c *SessionCatalog) listSubagentActors(ctx context.Context) ([]SubagentActorRow, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT actor_id, child_session_id, parent_actor_id, parent_session_id, name, role, status, turn_id,
		question_id, question_turn_id, question, question_context, question_created_ts, question_done, question_answer, question_terminal,
		last_event_id, last_event_at, model, cwd, created_at, updated_at
		FROM subagent_actors ORDER BY created_at ASC, actor_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubagentActorRow
	for rows.Next() {
		row, err := scanSubagentActorRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (c *SessionCatalog) listSubagentEvents(ctx context.Context, actorID string) ([]SubagentEventRow, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT actor_id, ordinal, event_id, type, child_session_id, parent_actor_id, parent_session_id, turn_id, question_id, message, status, ts
		FROM subagent_events WHERE actor_id = ? ORDER BY ordinal ASC`, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubagentEventRow
	for rows.Next() {
		var row SubagentEventRow
		if err := rows.Scan(&row.ActorID, &row.Ordinal, &row.EventID, &row.Type, &row.ChildSessionID, &row.ParentActorID, &row.ParentSessionID, &row.TurnID, &row.QuestionID, &row.Message, &row.Status, &row.TS); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (c *SessionCatalog) listSubagentMessages(ctx context.Context, actorID string) ([]SubagentMessageRow, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT actor_id, ordinal, message_id, kind, label, body, ts, meta
		FROM subagent_messages WHERE actor_id = ? ORDER BY ordinal ASC`, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubagentMessageRow
	for rows.Next() {
		var row SubagentMessageRow
		if err := rows.Scan(&row.ActorID, &row.Ordinal, &row.MessageID, &row.Kind, &row.Label, &row.Body, &row.TS, &row.Meta); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanSubagentActorRow(scanner interface{ Scan(...any) error }) (SubagentActorRow, error) {
	var row SubagentActorRow
	var questionDone int
	var lastEventAt sql.NullString
	var createdAt string
	var updatedAt string
	if err := scanner.Scan(&row.ActorID, &row.ChildSessionID, &row.ParentActorID, &row.ParentSessionID, &row.Name, &row.Role, &row.Status, &row.TurnID, &row.QuestionID, &row.QuestionTurnID, &row.Question, &row.QuestionContext, &row.QuestionCreatedTS, &questionDone, &row.QuestionAnswer, &row.QuestionTerminal, &row.LastEventID, &lastEventAt, &row.Model, &row.CWD, &createdAt, &updatedAt); err != nil {
		return SubagentActorRow{}, err
	}
	row.QuestionDone = questionDone != 0
	lastEventTS, err := parseNullableTime(lastEventAt)
	if err != nil {
		return SubagentActorRow{}, err
	}
	row.LastEventAt = lastEventTS
	if row.CreatedAt, err = parseTime(createdAt); err != nil {
		return SubagentActorRow{}, err
	}
	if row.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return SubagentActorRow{}, err
	}
	return row, nil
}
