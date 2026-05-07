package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/session"
)

const (
	defaultSchedulerIdleBeforeDeliverySeconds = 30
	defaultSelfReminderTitle                  = "Self Reminder"
	schedulerItemKindSelfReminder             = "self_reminder"
	schedulerInboxSourceSelfReminder          = "self_reminder"
)

type schedulerStore interface {
	LookupSchedulerSettings(context.Context) (sqlitestore.SchedulerSettingsRow, bool, error)
	UpsertSchedulerSettings(context.Context, sqlitestore.SchedulerSettingsRow) error
	InsertSchedulerItem(context.Context, sqlitestore.SchedulerItemRow) error
	LookupSchedulerItem(context.Context, string) (sqlitestore.SchedulerItemRow, bool, error)
	ListSchedulerItems(context.Context, int) ([]sqlitestore.SchedulerItemRow, error)
	ListDueSchedulerItems(context.Context, time.Time, int) ([]sqlitestore.SchedulerItemRow, error)
	UpdateSchedulerItem(context.Context, sqlitestore.SchedulerItemRow) error
	InsertInboxItem(context.Context, sqlitestore.InboxItemRow) error
	ListReadyInboxItems(context.Context, time.Time, int) ([]sqlitestore.InboxItemRow, error)
	UpdateInboxItem(context.Context, sqlitestore.InboxItemRow) error
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

type CreateSelfReminderRequest struct {
	SessionID       session.SessionID
	DurationSeconds int
	Title           string
	Message         string
	CreatedBy       string
}

type SelfReminderResponse struct {
	OK           bool          `json:"ok"`
	SelfReminder SchedulerItem `json:"self_reminder"`
}

type CancelSelfReminderRequest struct {
	ItemID string
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

func (s *Stub) CreateSelfReminder(ctx context.Context, req CreateSelfReminderRequest) (SelfReminderResponse, error) {
	if strings.TrimSpace(req.Message) == "" {
		return SelfReminderResponse{}, Invalid("message", "message required")
	}
	if req.DurationSeconds < 0 {
		return SelfReminderResponse{}, Invalid("duration_seconds", "duration_seconds must be non-negative")
	}
	if _, err := s.lookupSession(req.SessionID); err != nil {
		return SelfReminderResponse{}, err
	}
	now := s.registry.now()
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = defaultSelfReminderTitle
	}
	createdBy := strings.TrimSpace(req.CreatedBy)
	if createdBy == "" {
		createdBy = "agent"
	}
	row := sqlitestore.SchedulerItemRow{
		ItemID:    newID("self_reminder"),
		SessionID: req.SessionID.String(),
		Kind:      schedulerItemKindSelfReminder,
		Title:     title,
		Message:   strings.TrimSpace(req.Message),
		DueAt:     now.Add(time.Duration(req.DurationSeconds) * time.Second),
		State:     "scheduled",
		CreatedBy: createdBy,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.schedulerStore.InsertSchedulerItem(ctx, row); err != nil {
		return SelfReminderResponse{}, err
	}
	return SelfReminderResponse{OK: true, SelfReminder: schedulerItemResponse(row)}, nil
}

func (s *Stub) CancelSelfReminder(ctx context.Context, req CancelSelfReminderRequest) (SelfReminderResponse, error) {
	itemID := strings.TrimSpace(req.ItemID)
	if itemID == "" {
		return SelfReminderResponse{}, Invalid("item_id", "item_id required")
	}
	item, ok, err := s.schedulerStore.LookupSchedulerItem(ctx, itemID)
	if err != nil {
		return SelfReminderResponse{}, err
	}
	if !ok {
		return SelfReminderResponse{}, NotFound("self reminder not found")
	}
	if item.Kind != schedulerItemKindSelfReminder {
		return SelfReminderResponse{}, Conflict("scheduler item is not a self reminder")
	}
	if item.State != "scheduled" {
		return SelfReminderResponse{}, Conflict("only scheduled self reminders can be cancelled")
	}
	now := s.registry.now()
	item.State = "cancelled"
	item.UpdatedAt = now
	if err := s.schedulerStore.UpdateSchedulerItem(ctx, item); err != nil {
		return SelfReminderResponse{}, err
	}
	return SelfReminderResponse{OK: true, SelfReminder: schedulerItemResponse(item)}, nil
}

func (s *Stub) RunSchedulerDeliverySweep(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.runSchedulerDeliverySweep(ctx)
		}
	}
}

func (s *Stub) runSchedulerDeliverySweep(ctx context.Context) error {
	now := s.registry.now().UTC()
	if err := s.stageDueSchedulerItems(ctx, now); err != nil {
		return err
	}
	return s.deliverReadyInboxItems(ctx, now)
}

func (s *Stub) stageDueSchedulerItems(ctx context.Context, now time.Time) error {
	items, err := s.schedulerStore.ListDueSchedulerItems(ctx, now, 100)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Kind != schedulerItemKindSelfReminder {
			item.State = "unsupported"
			item.UpdatedAt = now
			if err := s.schedulerStore.UpdateSchedulerItem(ctx, item); err != nil {
				return err
			}
			continue
		}
		parsedSessionID, err := session.ParseSessionID(item.SessionID)
		if err != nil {
			item.State = "error"
			item.UpdatedAt = now
			if updateErr := s.schedulerStore.UpdateSchedulerItem(ctx, item); updateErr != nil {
				return updateErr
			}
			continue
		}
		if _, err := s.lookupSession(parsedSessionID); err != nil {
			item.State = "orphaned"
			item.UpdatedAt = now
			if updateErr := s.schedulerStore.UpdateSchedulerItem(ctx, item); updateErr != nil {
				return updateErr
			}
			continue
		}
		inbox := sqlitestore.InboxItemRow{
			ItemID:    newID("inbox"),
			SessionID: item.SessionID,
			Source:    schedulerInboxSourceSelfReminder,
			SourceID:  item.ItemID,
			Title:     item.Title,
			Message:   item.Message,
			DueAt:     item.DueAt,
			State:     "pending",
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := s.schedulerStore.InsertInboxItem(ctx, inbox); err != nil {
			return err
		}
		item.State = "delivered"
		item.UpdatedAt = now
		if err := s.schedulerStore.UpdateSchedulerItem(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stub) deliverReadyInboxItems(ctx context.Context, now time.Time) error {
	settings, err := s.schedulerSettings(ctx)
	if err != nil {
		return err
	}
	items, err := s.schedulerStore.ListReadyInboxItems(ctx, now, 100)
	if err != nil {
		return err
	}
	for _, item := range items {
		ready, err := s.inboxItemReadyForDelivery(item, settings, now)
		if err != nil {
			item.State = "error"
			item.Error = err.Error()
			item.UpdatedAt = now
			if updateErr := s.schedulerStore.UpdateInboxItem(ctx, item); updateErr != nil {
				return updateErr
			}
			continue
		}
		if !ready {
			continue
		}
		claimedAt := now
		item.State = "claimed"
		item.ClaimedAt = &claimedAt
		item.UpdatedAt = now
		if err := s.schedulerStore.UpdateInboxItem(ctx, item); err != nil {
			return err
		}
		sessionID, _ := session.ParseSessionID(item.SessionID)
		message, err := s.Send(ctx, SendRequest{SessionID: sessionID, Text: formatInboxEnvelope(item.Title, item.Source, item.Message)})
		now = s.registry.now().UTC()
		if err != nil {
			item.State = "error"
			item.Error = err.Error()
			item.UpdatedAt = now
			if updateErr := s.schedulerStore.UpdateInboxItem(ctx, item); updateErr != nil {
				return updateErr
			}
			continue
		}
		deliveredAt := now
		item.State = "delivered"
		item.DeliveredMessageID = deliveredMessageID(message.Message)
		item.DeliveredAt = &deliveredAt
		item.Error = ""
		item.UpdatedAt = now
		if err := s.schedulerStore.UpdateInboxItem(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stub) inboxItemReadyForDelivery(item sqlitestore.InboxItemRow, settings sqlitestore.SchedulerSettingsRow, now time.Time) (bool, error) {
	sessionID, err := session.ParseSessionID(item.SessionID)
	if err != nil {
		return false, err
	}
	record, err := s.lookupSession(sessionID)
	if err != nil {
		return false, err
	}
	busy, _ := effectiveBusy(record)
	if busy || record.state.Queue().Len() > 0 || record.uiRequest != nil || s.activeWaitForSession(sessionID) != nil {
		return false, nil
	}
	idleSince := sessionDisplayUpdatedAt(record)
	if lastAssistantTS := lastAssistantMessageTimestamp(record); lastAssistantTS > 0 {
		lastAssistantAt := time.Unix(int64(lastAssistantTS), 0).UTC()
		if lastAssistantAt.After(idleSince) {
			idleSince = lastAssistantAt
		}
	}
	return !now.Before(idleSince.Add(time.Duration(settings.IdleBeforeDeliverySeconds) * time.Second)), nil
}

func deliveredMessageID(message SessionMessage) string {
	if strings.TrimSpace(message.EventID) != "" {
		return strings.TrimSpace(message.EventID)
	}
	if message.Seq > 0 {
		return fmt.Sprintf("seq:%d", message.Seq)
	}
	return ""
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
