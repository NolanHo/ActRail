package app

import (
	"context"
	"testing"
	"time"

	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func TestSchedulerSettingsDefaultAndUpdate(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	svc := NewStubForTest(config.Config{}, func() time.Time { return now }, RuntimeConfig{})

	snapshot, err := svc.SchedulerSnapshot(context.Background(), SchedulerSnapshotRequest{})
	if err != nil {
		t.Fatalf("SchedulerSnapshot() error = %v", err)
	}
	if snapshot.Settings.IdleBeforeDeliverySeconds != 30 {
		t.Fatalf("default idle = %d, want 30", snapshot.Settings.IdleBeforeDeliverySeconds)
	}

	idle := 45
	updated, err := svc.UpdateSchedulerSettings(context.Background(), UpdateSchedulerSettingsRequest{IdleBeforeDeliverySeconds: &idle})
	if err != nil {
		t.Fatalf("UpdateSchedulerSettings() error = %v", err)
	}
	if updated.IdleBeforeDeliverySeconds != 45 {
		t.Fatalf("updated idle = %d, want 45", updated.IdleBeforeDeliverySeconds)
	}
}

func TestCreateSelfReminderPersistsSchedulerItem(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	svc := NewStubForTest(config.Config{}, func() time.Time { return now }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	resp, err := svc.CreateSelfReminder(context.Background(), CreateSelfReminderRequest{SessionID: mustSchedulerSessionID(t, created.Session.SessionID), DurationSeconds: 60, Message: "check build"})
	if err != nil {
		t.Fatalf("CreateSelfReminder() error = %v", err)
	}
	if resp.SelfReminder.Kind != "self_reminder" || resp.SelfReminder.State != "scheduled" || resp.SelfReminder.Title != "Self Reminder" {
		t.Fatalf("self reminder = %+v", resp.SelfReminder)
	}
	snapshot, err := svc.SchedulerSnapshot(context.Background(), SchedulerSnapshotRequest{})
	if err != nil {
		t.Fatalf("SchedulerSnapshot() error = %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Message != "check build" {
		t.Fatalf("snapshot items = %+v", snapshot.Items)
	}
}

func TestSchedulerSweepStagesDueSelfRemindersIntoInbox(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	svc := NewStubForTest(config.Config{}, func() time.Time { return now }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSchedulerSessionID(t, created.Session.SessionID)
	if _, err := svc.CreateSelfReminder(context.Background(), CreateSelfReminderRequest{SessionID: sessionID, DurationSeconds: 0, Message: "check build"}); err != nil {
		t.Fatalf("CreateSelfReminder() error = %v", err)
	}

	if err := svc.runSchedulerDeliverySweep(context.Background()); err != nil {
		t.Fatalf("runSchedulerDeliverySweep() error = %v", err)
	}

	snapshot, err := svc.SchedulerSnapshot(context.Background(), SchedulerSnapshotRequest{})
	if err != nil {
		t.Fatalf("SchedulerSnapshot() error = %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].State != "delivered" {
		t.Fatalf("scheduler items = %+v", snapshot.Items)
	}
	if len(snapshot.Inbox) != 1 || snapshot.Inbox[0].Source != "self_reminder" || snapshot.Inbox[0].State != "pending" {
		t.Fatalf("inbox = %+v", snapshot.Inbox)
	}
}

func TestCancelSelfReminder(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	svc := NewStubForTest(config.Config{}, func() time.Time { return now }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSchedulerSessionID(t, created.Session.SessionID)
	reminder, err := svc.CreateSelfReminder(context.Background(), CreateSelfReminderRequest{SessionID: sessionID, DurationSeconds: 60, Message: "check build"})
	if err != nil {
		t.Fatalf("CreateSelfReminder() error = %v", err)
	}

	cancelled, err := svc.CancelSelfReminder(context.Background(), CancelSelfReminderRequest{ItemID: reminder.SelfReminder.ItemID})
	if err != nil {
		t.Fatalf("CancelSelfReminder() error = %v", err)
	}
	if cancelled.SelfReminder.State != "cancelled" {
		t.Fatalf("cancelled self reminder = %+v", cancelled.SelfReminder)
	}
}

func TestSchedulerSweepDeliversIdleInboxItemsToSession(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	current := now
	svc := NewStubForTest(config.Config{}, func() time.Time { return current }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSchedulerSessionID(t, created.Session.SessionID)
	if _, err := svc.CreateSelfReminder(context.Background(), CreateSelfReminderRequest{SessionID: sessionID, DurationSeconds: 0, Message: "check build"}); err != nil {
		t.Fatalf("CreateSelfReminder() error = %v", err)
	}
	if err := svc.runSchedulerDeliverySweep(context.Background()); err != nil {
		t.Fatalf("stage runSchedulerDeliverySweep() error = %v", err)
	}
	current = current.Add(31 * time.Second)
	if err := svc.runSchedulerDeliverySweep(context.Background()); err != nil {
		t.Fatalf("deliver runSchedulerDeliverySweep() error = %v", err)
	}

	inbox, err := svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox() error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].State != "delivered" || inbox.Items[0].DeliveredMessageID == "" {
		t.Fatalf("inbox items = %+v", inbox.Items)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Role != "user" || messages.Items[0].Text == "" {
		t.Fatalf("messages = %+v", messages.Items)
	}
	if messages.Items[0].Text != "<Inbox>\n<title>Self Reminder</title>\n<source>self_reminder</source>\n<message>check build</message>\n</Inbox>" {
		t.Fatalf("delivered text = %q", messages.Items[0].Text)
	}
}

func mustSchedulerSessionID(t *testing.T, raw string) session.SessionID {
	t.Helper()
	id, err := session.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q) error = %v", raw, err)
	}
	return id
}
