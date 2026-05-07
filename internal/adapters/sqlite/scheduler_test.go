package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSchedulerAndInboxPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actrail.db")
	catalog, err := OpenSessionCatalog(path)
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	settings := SchedulerSettingsRow{IdleBeforeDeliverySeconds: 45, UpdatedAt: now}
	if err := catalog.UpsertSchedulerSettings(context.Background(), settings); err != nil {
		t.Fatalf("UpsertSchedulerSettings() error = %v", err)
	}
	alarm := SchedulerItemRow{ItemID: "alarm_1", SessionID: "sess-1", Kind: "alarm", Title: "Alarm Response", Message: "check", DueAt: now.Add(time.Minute), State: "scheduled", CreatedBy: "agent", CreatedAt: now, UpdatedAt: now}
	if err := catalog.InsertSchedulerItem(context.Background(), alarm); err != nil {
		t.Fatalf("InsertSchedulerItem() error = %v", err)
	}
	inbox := InboxItemRow{ItemID: "inbox_1", SessionID: "sess-1", Source: "alarm", SourceID: "alarm_1", Title: "Alarm Response", Message: "check", DueAt: now.Add(time.Minute), State: "pending", CreatedAt: now, UpdatedAt: now}
	if err := catalog.InsertInboxItem(context.Background(), inbox); err != nil {
		t.Fatalf("InsertInboxItem() error = %v", err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reloaded, err := OpenSessionCatalog(path)
	if err != nil {
		t.Fatalf("OpenSessionCatalog(reload) error = %v", err)
	}
	defer reloaded.Close()
	gotSettings, ok, err := reloaded.LookupSchedulerSettings(context.Background())
	if err != nil {
		t.Fatalf("LookupSchedulerSettings() error = %v", err)
	}
	if !ok || gotSettings.IdleBeforeDeliverySeconds != 45 {
		t.Fatalf("settings = %+v, ok=%v", gotSettings, ok)
	}
	items, err := reloaded.ListSchedulerItems(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListSchedulerItems() error = %v", err)
	}
	if len(items) != 1 || items[0].ItemID != "alarm_1" {
		t.Fatalf("scheduler items = %+v", items)
	}
	dueItems, err := reloaded.ListDueSchedulerItems(context.Background(), now.Add(2*time.Minute), 10)
	if err != nil {
		t.Fatalf("ListDueSchedulerItems() error = %v", err)
	}
	if len(dueItems) != 1 || dueItems[0].ItemID != "alarm_1" {
		t.Fatalf("due scheduler items = %+v", dueItems)
	}
	alarm.State = "delivered"
	alarm.UpdatedAt = now.Add(2 * time.Minute)
	if err := reloaded.UpdateSchedulerItem(context.Background(), alarm); err != nil {
		t.Fatalf("UpdateSchedulerItem() error = %v", err)
	}
	inboxItems, err := reloaded.ListInboxItems(context.Background(), "sess-1", 10)
	if err != nil {
		t.Fatalf("ListInboxItems() error = %v", err)
	}
	if len(inboxItems) != 1 || inboxItems[0].SourceID != "alarm_1" {
		t.Fatalf("inbox items = %+v", inboxItems)
	}
	readyItems, err := reloaded.ListReadyInboxItems(context.Background(), now.Add(2*time.Minute), 10)
	if err != nil {
		t.Fatalf("ListReadyInboxItems() error = %v", err)
	}
	if len(readyItems) != 1 || readyItems[0].ItemID != "inbox_1" {
		t.Fatalf("ready inbox items = %+v", readyItems)
	}
	deliveredAt := now.Add(3 * time.Minute)
	inbox.State = "delivered"
	inbox.DeliveredMessageID = "seq:1"
	inbox.DeliveredAt = &deliveredAt
	inbox.UpdatedAt = deliveredAt
	if err := reloaded.UpdateInboxItem(context.Background(), inbox); err != nil {
		t.Fatalf("UpdateInboxItem() error = %v", err)
	}
	updatedInboxItems, err := reloaded.ListInboxItems(context.Background(), "sess-1", 10)
	if err != nil {
		t.Fatalf("ListInboxItems(updated) error = %v", err)
	}
	if len(updatedInboxItems) != 1 || updatedInboxItems[0].State != "delivered" || updatedInboxItems[0].DeliveredMessageID != "seq:1" {
		t.Fatalf("updated inbox items = %+v", updatedInboxItems)
	}
}
