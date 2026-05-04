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

func TestSetAlarmPersistsSchedulerItem(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	svc := NewStubForTest(config.Config{}, func() time.Time { return now }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	resp, err := svc.SetAlarm(context.Background(), SetAlarmRequest{SessionID: mustSchedulerSessionID(t, created.Session.SessionID), DurationSeconds: 60, Message: "check build"})
	if err != nil {
		t.Fatalf("SetAlarm() error = %v", err)
	}
	if resp.Alarm.Kind != "alarm" || resp.Alarm.State != "scheduled" || resp.Alarm.Title != "Alarm Response" {
		t.Fatalf("alarm = %+v", resp.Alarm)
	}
	snapshot, err := svc.SchedulerSnapshot(context.Background(), SchedulerSnapshotRequest{})
	if err != nil {
		t.Fatalf("SchedulerSnapshot() error = %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Message != "check build" {
		t.Fatalf("snapshot items = %+v", snapshot.Items)
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
