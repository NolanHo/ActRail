package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/session"
)

const defaultSchedulerIdleBeforeDeliverySeconds = 30

type schedulerStore interface {
	LookupSchedulerSettings(context.Context) (sqlitestore.SchedulerSettingsRow, bool, error)
	UpsertSchedulerSettings(context.Context, sqlitestore.SchedulerSettingsRow) error
	InsertSchedulerItem(context.Context, sqlitestore.SchedulerItemRow) error
	ListSchedulerItems(context.Context, int) ([]sqlitestore.SchedulerItemRow, error)
	InsertInboxItem(context.Context, sqlitestore.InboxItemRow) error
	ListInboxItems(context.Context, string, int) ([]sqlitestore.InboxItemRow, error)
}

type SchedulerSnapshotRequest struct {
	Limit int
}

type SchedulerSnapshotResponse struct {
	OK       bool              `json:"ok"`
	Settings SchedulerSettings `json:"settings"`
	Items    []SchedulerItem   `json:"items"`
	Inbox    []InboxItem       `json:"inbox"`
}

type SchedulerSettings struct {
	IdleBeforeDeliverySeconds int     `json:"idle_before_delivery_seconds"`
	UpdatedTS                 float64 `json:"updated_ts,omitempty"`
}

type UpdateSchedulerSettingsRequest struct {
	IdleBeforeDeliverySeconds *int
}

type SchedulerItem struct {
	ItemID    string  `json:"item_id"`
	SessionID string  `json:"session_id"`
	Kind      string  `json:"kind"`
	SourceRef string  `json:"source_ref,omitempty"`
	Title     string  `json:"title,omitempty"`
	Message   string  `json:"message,omitempty"`
	DueTS     float64 `json:"due_ts"`
	State     string  `json:"state"`
	CreatedBy string  `json:"created_by,omitempty"`
	CreatedTS float64 `json:"created_ts"`
	UpdatedTS float64 `json:"updated_ts"`
}

type SessionInboxRequest struct {
	SessionID session.SessionID
	Limit     int
}

type SessionInboxResponse struct {
	OK    bool        `json:"ok"`
	Items []InboxItem `json:"items"`
}

type InboxItem struct {
	ItemID             string  `json:"item_id"`
	SessionID          string  `json:"session_id"`
	Source             string  `json:"source"`
	SourceID           string  `json:"source_id,omitempty"`
	Title              string  `json:"title,omitempty"`
	Message            string  `json:"message,omitempty"`
	Priority           int     `json:"priority,omitempty"`
	DueTS              float64 `json:"due_ts"`
	State              string  `json:"state"`
	BlockedReason      string  `json:"blocked_reason,omitempty"`
	DeliveredMessageID string  `json:"delivered_message_id,omitempty"`
	Error              string  `json:"error,omitempty"`
	ClaimedTS          float64 `json:"claimed_ts,omitempty"`
	DeliveredTS        float64 `json:"delivered_ts,omitempty"`
	CreatedTS          float64 `json:"created_ts"`
	UpdatedTS          float64 `json:"updated_ts"`
}

type SetAlarmRequest struct {
	SessionID       session.SessionID
	DurationSeconds int
	Title           string
	Message         string
	CreatedBy       string
}

type SetAlarmResponse struct {
	OK    bool          `json:"ok"`
	Alarm SchedulerItem `json:"alarm"`
}

func (s *Stub) SchedulerSnapshot(ctx context.Context, req SchedulerSnapshotRequest) (SchedulerSnapshotResponse, error) {
	settings, err := s.schedulerSettings(ctx)
	if err != nil {
		return SchedulerSnapshotResponse{}, err
	}
	items, err := s.schedulerStore.ListSchedulerItems(ctx, req.Limit)
	if err != nil {
		return SchedulerSnapshotResponse{}, err
	}
	inbox, err := s.schedulerStore.ListInboxItems(ctx, "", req.Limit)
	if err != nil {
		return SchedulerSnapshotResponse{}, err
	}
	return SchedulerSnapshotResponse{OK: true, Settings: schedulerSettingsResponse(settings), Items: schedulerItemResponses(items), Inbox: inboxItemResponses(inbox)}, nil
}

func (s *Stub) UpdateSchedulerSettings(ctx context.Context, req UpdateSchedulerSettingsRequest) (SchedulerSettings, error) {
	settings, err := s.schedulerSettings(ctx)
	if err != nil {
		return SchedulerSettings{}, err
	}
	if req.IdleBeforeDeliverySeconds != nil {
		if *req.IdleBeforeDeliverySeconds < 0 {
			return SchedulerSettings{}, Invalid("idle_before_delivery_seconds", "idle_before_delivery_seconds must be non-negative")
		}
		settings.IdleBeforeDeliverySeconds = *req.IdleBeforeDeliverySeconds
	}
	settings.UpdatedAt = s.registry.now()
	if err := s.schedulerStore.UpsertSchedulerSettings(ctx, settings); err != nil {
		return SchedulerSettings{}, err
	}
	return schedulerSettingsResponse(settings), nil
}

func (s *Stub) SessionInbox(ctx context.Context, req SessionInboxRequest) (SessionInboxResponse, error) {
	if _, err := s.lookupSession(req.SessionID); err != nil {
		return SessionInboxResponse{}, err
	}
	items, err := s.schedulerStore.ListInboxItems(ctx, req.SessionID.String(), req.Limit)
	if err != nil {
		return SessionInboxResponse{}, err
	}
	return SessionInboxResponse{OK: true, Items: inboxItemResponses(items)}, nil
}

func (s *Stub) SetAlarm(ctx context.Context, req SetAlarmRequest) (SetAlarmResponse, error) {
	if strings.TrimSpace(req.Message) == "" {
		return SetAlarmResponse{}, Invalid("message", "message required")
	}
	if req.DurationSeconds < 0 {
		return SetAlarmResponse{}, Invalid("duration_seconds", "duration_seconds must be non-negative")
	}
	if _, err := s.lookupSession(req.SessionID); err != nil {
		return SetAlarmResponse{}, err
	}
	now := s.registry.now()
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Alarm Response"
	}
	createdBy := strings.TrimSpace(req.CreatedBy)
	if createdBy == "" {
		createdBy = "agent"
	}
	row := sqlitestore.SchedulerItemRow{
		ItemID:    newID("alarm"),
		SessionID: req.SessionID.String(),
		Kind:      "alarm",
		Title:     title,
		Message:   strings.TrimSpace(req.Message),
		DueAt:     now.Add(time.Duration(req.DurationSeconds) * time.Second),
		State:     "scheduled",
		CreatedBy: createdBy,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.schedulerStore.InsertSchedulerItem(ctx, row); err != nil {
		return SetAlarmResponse{}, err
	}
	return SetAlarmResponse{OK: true, Alarm: schedulerItemResponse(row)}, nil
}

func (s *Stub) schedulerSettings(ctx context.Context) (sqlitestore.SchedulerSettingsRow, error) {
	settings, ok, err := s.schedulerStore.LookupSchedulerSettings(ctx)
	if err != nil {
		return sqlitestore.SchedulerSettingsRow{}, err
	}
	if ok {
		return settings, nil
	}
	settings = sqlitestore.SchedulerSettingsRow{IdleBeforeDeliverySeconds: defaultSchedulerIdleBeforeDeliverySeconds, UpdatedAt: s.registry.now()}
	if err := s.schedulerStore.UpsertSchedulerSettings(ctx, settings); err != nil {
		return sqlitestore.SchedulerSettingsRow{}, err
	}
	return settings, nil
}

func schedulerSettingsResponse(row sqlitestore.SchedulerSettingsRow) SchedulerSettings {
	return SchedulerSettings{IdleBeforeDeliverySeconds: row.IdleBeforeDeliverySeconds, UpdatedTS: timestampSeconds(row.UpdatedAt)}
}

func schedulerItemResponses(rows []sqlitestore.SchedulerItemRow) []SchedulerItem {
	items := make([]SchedulerItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, schedulerItemResponse(row))
	}
	return items
}

func schedulerItemResponse(row sqlitestore.SchedulerItemRow) SchedulerItem {
	return SchedulerItem{
		ItemID:    row.ItemID,
		SessionID: row.SessionID,
		Kind:      row.Kind,
		SourceRef: row.SourceRef,
		Title:     row.Title,
		Message:   row.Message,
		DueTS:     timestampSeconds(row.DueAt),
		State:     row.State,
		CreatedBy: row.CreatedBy,
		CreatedTS: timestampSeconds(row.CreatedAt),
		UpdatedTS: timestampSeconds(row.UpdatedAt),
	}
}

func inboxItemResponses(rows []sqlitestore.InboxItemRow) []InboxItem {
	items := make([]InboxItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, inboxItemResponse(row))
	}
	return items
}

func inboxItemResponse(row sqlitestore.InboxItemRow) InboxItem {
	return InboxItem{
		ItemID:             row.ItemID,
		SessionID:          row.SessionID,
		Source:             row.Source,
		SourceID:           row.SourceID,
		Title:              row.Title,
		Message:            row.Message,
		Priority:           row.Priority,
		DueTS:              timestampSeconds(row.DueAt),
		State:              row.State,
		BlockedReason:      row.BlockedReason,
		DeliveredMessageID: row.DeliveredMessageID,
		Error:              row.Error,
		ClaimedTS:          timestampSecondsPtr(row.ClaimedAt),
		DeliveredTS:        timestampSecondsPtr(row.DeliveredAt),
		CreatedTS:          timestampSeconds(row.CreatedAt),
		UpdatedTS:          timestampSeconds(row.UpdatedAt),
	}
}

func timestampSecondsPtr(ts *time.Time) float64 {
	if ts == nil || ts.IsZero() {
		return 0
	}
	return timestampSeconds(*ts)
}

func formatInboxEnvelope(title, source, message string) string {
	return fmt.Sprintf("<Inbox>\n<title>%s</title>\n<source>%s</source>\n<message>%s</message>\n</Inbox>", strings.TrimSpace(title), strings.TrimSpace(source), strings.TrimSpace(message))
}
