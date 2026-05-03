package app

import (
	"context"
	"testing"
	"time"

	"actrail/internal/config"
)

func TestPersistentStubListSessionsUseImportedPISourceActivityWithSidebarMetadata(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourceDir := t.TempDir()
	olderActivity := time.Unix(1759992800, 0).UTC()
	newerActivity := time.Unix(1759996400, 0).UTC()
	olderSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-1.jsonl", olderActivity)
	newerSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-2.jsonl", newerActivity)

	seedImportedPIDetachedSessions(t, cfg, now,
		importedPIDetachedFixture{
			SessionID:  "imported-pi-1",
			CWD:        "/workspace/older",
			Title:      "Imported Pi 1",
			Alias:      "Pinned older",
			UpdatedAt:  now.Add(-30 * time.Minute),
			ActivityAt: now.Add(-30 * time.Minute),
			SourcePath: olderSourcePath,
		},
		importedPIDetachedFixture{
			SessionID:  "imported-pi-2",
			CWD:        "/workspace/newer",
			Title:      "Imported Pi 2",
			Alias:      "Pinned newer",
			UpdatedAt:  now.Add(-90 * time.Minute),
			ActivityAt: now.Add(-90 * time.Minute),
			SourcePath: newerSourcePath,
		},
	)

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("len(ListSessions().Items) = %d, want 2", len(listed.Items))
	}
	if listed.Items[0].SessionID != "imported-pi-2" || listed.Items[1].SessionID != "imported-pi-1" {
		t.Fatalf("ListSessions() order = [%q %q], want [imported-pi-2 imported-pi-1]", listed.Items[0].SessionID, listed.Items[1].SessionID)
	}
	if listed.Items[0].UpdatedTS != timestampSeconds(newerActivity) || listed.Items[0].LastUpdatedTS != timestampSeconds(newerActivity) {
		t.Fatalf("ListSessions().Items[0] timestamps = (%v, %v), want %v", listed.Items[0].LastUpdatedTS, listed.Items[0].UpdatedTS, timestampSeconds(newerActivity))
	}
	if listed.Items[1].UpdatedTS != timestampSeconds(olderActivity) || listed.Items[1].LastUpdatedTS != timestampSeconds(olderActivity) {
		t.Fatalf("ListSessions().Items[1] timestamps = (%v, %v), want %v", listed.Items[1].LastUpdatedTS, listed.Items[1].UpdatedTS, timestampSeconds(olderActivity))
	}
}

func TestPersistentStubListSessionsUsePersistedUpdatedAtWithoutImportedPISidebarMetadata(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourceDir := t.TempDir()
	olderActivity := now.Add(-2 * time.Hour)
	newerActivity := now.Add(-30 * time.Minute)
	olderSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-1.jsonl", olderActivity)
	newerSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-2.jsonl", newerActivity)
	firstUpdatedAt := now.Add(-10 * time.Minute)
	secondUpdatedAt := now.Add(-3 * time.Hour)

	seedImportedPIDetachedSessions(t, cfg, now,
		importedPIDetachedFixture{
			SessionID:  "imported-pi-1",
			CWD:        "/workspace/older",
			Title:      "Imported Pi 1",
			UpdatedAt:  firstUpdatedAt,
			ActivityAt: firstUpdatedAt,
			SourcePath: olderSourcePath,
		},
		importedPIDetachedFixture{
			SessionID:  "imported-pi-2",
			CWD:        "/workspace/newer",
			Title:      "Imported Pi 2",
			UpdatedAt:  secondUpdatedAt,
			ActivityAt: secondUpdatedAt,
			SourcePath: newerSourcePath,
		},
	)

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("len(ListSessions().Items) = %d, want 2", len(listed.Items))
	}
	if listed.Items[0].SessionID != "imported-pi-1" || listed.Items[1].SessionID != "imported-pi-2" {
		t.Fatalf("ListSessions() order = [%q %q], want [imported-pi-1 imported-pi-2]", listed.Items[0].SessionID, listed.Items[1].SessionID)
	}
	if listed.Items[0].UpdatedTS != timestampSeconds(firstUpdatedAt) || listed.Items[0].LastUpdatedTS != timestampSeconds(firstUpdatedAt) {
		t.Fatalf("ListSessions().Items[0] timestamps = (%v, %v), want %v", listed.Items[0].LastUpdatedTS, listed.Items[0].UpdatedTS, timestampSeconds(firstUpdatedAt))
	}
	if listed.Items[1].UpdatedTS != timestampSeconds(secondUpdatedAt) || listed.Items[1].LastUpdatedTS != timestampSeconds(secondUpdatedAt) {
		t.Fatalf("ListSessions().Items[1] timestamps = (%v, %v), want %v", listed.Items[1].LastUpdatedTS, listed.Items[1].UpdatedTS, timestampSeconds(secondUpdatedAt))
	}
}

func TestStubListSessionsDemotesBlockedAndSnoozedPriority(t *testing.T) {
	cfg := config.Load()
	now := time.Unix(1760000000, 0).UTC()
	svc := newStub(cfg, func() time.Time { return now })
	for _, cwd := range []string{"/tmp/active", "/tmp/blocked", "/tmp/snoozed"} {
		if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: cwd}); err != nil {
			t.Fatalf("CreateSession(%q) error = %v", cwd, err)
		}
	}
	blockedPriority := 1.0
	blockedDependency := "s_1"
	if _, err := svc.EditSession(context.Background(), EditSessionRequest{
		SessionID:           mustSessionID(t, "s_2"),
		PriorityOffset:      Float64Patch{Present: true, Value: &blockedPriority},
		DependencySessionID: StringPatch{Present: true, Value: &blockedDependency},
	}); err != nil {
		t.Fatalf("EditSession(blocked) error = %v", err)
	}
	snoozedPriority := 1.0
	snoozeUntil := now.Add(time.Hour).Unix()
	if _, err := svc.EditSession(context.Background(), EditSessionRequest{
		SessionID:      mustSessionID(t, "s_3"),
		PriorityOffset: Float64Patch{Present: true, Value: &snoozedPriority},
		SnoozeUntil:    Int64Patch{Present: true, Value: &snoozeUntil},
	}); err != nil {
		t.Fatalf("EditSession(snoozed) error = %v", err)
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 3 {
		t.Fatalf("len(ListSessions().Items) = %d, want 3", len(listed.Items))
	}
	got := []string{listed.Items[0].SessionID, listed.Items[1].SessionID, listed.Items[2].SessionID}
	want := []string{"s_1", "s_2", "s_3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListSessions() order = %#v, want %#v", got, want)
		}
	}
}
