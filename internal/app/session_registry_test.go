package app

import (
	"testing"
	"time"

	"actrail/internal/domain/session"
)

func TestSessionRegistryResolvePrefersRuntimeIDOverSessionID(t *testing.T) {
	registry := newSessionRegistry(func() time.Time { return time.Unix(1760000000, 0).UTC() }, nil)
	firstIdentity, err := session.NewLiveIdentity("s_1", "r_2", "t_1", "pi")
	if err != nil {
		t.Fatalf("NewLiveIdentity(first) error = %v", err)
	}
	secondIdentity, err := session.NewLiveIdentity("r_2", "r_3", "t_2", "pi")
	if err != nil {
		t.Fatalf("NewLiveIdentity(second) error = %v", err)
	}
	first, err := registry.Create(sessionCreateSpec{Backend: session.BackendPI, Identity: &firstIdentity, CWD: "/tmp/first"})
	if err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}
	second, err := registry.Create(sessionCreateSpec{Backend: session.BackendPI, Identity: &secondIdentity, CWD: "/tmp/second"})
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	runtimeID, ok := first.identity.RuntimeID()
	if !ok {
		t.Fatal("first.identity.RuntimeID() ok = false")
	}
	actualID, resolved, ok := registry.resolveLocked(session.SessionID(runtimeID.String()))
	if !ok {
		t.Fatal("resolveLocked(runtime) ok = false")
	}
	if actualID != first.identity.SessionID() || resolved.identity.SessionID() != first.identity.SessionID() {
		t.Fatalf("resolveLocked(runtime) = (%q, %q), want first session %q; second session %q", actualID, resolved.identity.SessionID(), first.identity.SessionID(), second.identity.SessionID())
	}
}

func TestSessionRegistryCreateStoresLiveSessionState(t *testing.T) {
	now := time.Unix(1760000000, 0).UTC()
	registry := newSessionRegistry(func() time.Time { return now })

	record, err := registry.Create(sessionCreateSpec{
		Backend: session.BackendPI,
		CWD:     "/root/code/ActRail",
		Title:   "Current task",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if record.identity.Historical() {
		t.Fatal("record.identity.Historical() = true, want false")
	}
	if got := record.identity.SessionID().String(); got != "s_1" {
		t.Fatalf("SessionID() = %q, want %q", got, "s_1")
	}
	if got := record.identity.DurableID().String(); got != "s_1" {
		t.Fatalf("DurableID() = %q, want %q", got, "s_1")
	}
	runtimeID, ok := record.identity.RuntimeID()
	if !ok || runtimeID.String() != "r_1" {
		t.Fatalf("RuntimeID() = (%q, %v), want (%q, true)", runtimeID, ok, "r_1")
	}
	threadID, ok := record.identity.ThreadID()
	if !ok || threadID.String() != "t_1" {
		t.Fatalf("ThreadID() = (%q, %v), want (%q, true)", threadID, ok, "t_1")
	}
	if record.state.Busy() {
		t.Fatal("record.state.Busy() = true, want false")
	}
	if !record.state.Queue().Empty() {
		t.Fatalf("record.state.Queue().Len() = %d, want 0", record.state.Queue().Len())
	}
	if got := record.state.Tail().Seq().Uint64(); got != 0 {
		t.Fatalf("Tail().Seq() = %d, want 0", got)
	}
	stored, ok := registry.Lookup(record.identity.SessionID())
	if !ok {
		t.Fatal("Lookup() ok = false, want true")
	}
	if stored.title != "Current task" {
		t.Fatalf("stored.title = %q, want %q", stored.title, "Current task")
	}
	if !stored.updatedAt.Equal(now) {
		t.Fatalf("stored.updatedAt = %v, want %v", stored.updatedAt, now)
	}
	if !stored.activityAt.Equal(now) {
		t.Fatalf("stored.activityAt = %v, want %v", stored.activityAt, now)
	}
}

func TestSessionRegistryListPreservesCreationOrder(t *testing.T) {
	registry := newSessionRegistry(func() time.Time { return time.Unix(1760000000, 0).UTC() })
	first, err := registry.Create(sessionCreateSpec{Backend: session.BackendPI, CWD: "/tmp/one"})
	if err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}
	second, err := registry.Create(sessionCreateSpec{Backend: session.BackendCodex, CWD: "/tmp/two"})
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}

	items := registry.List()
	if len(items) != 2 {
		t.Fatalf("len(List()) = %d, want 2", len(items))
	}
	if items[0].identity.SessionID() != first.identity.SessionID() {
		t.Fatalf("items[0].SessionID() = %q, want %q", items[0].identity.SessionID(), first.identity.SessionID())
	}
	if items[1].identity.SessionID() != second.identity.SessionID() {
		t.Fatalf("items[1].SessionID() = %q, want %q", items[1].identity.SessionID(), second.identity.SessionID())
	}
	if items[0].title != "/tmp/one" {
		t.Fatalf("items[0].title = %q, want %q", items[0].title, "/tmp/one")
	}
	if items[1].title != "/tmp/two" {
		t.Fatalf("items[1].title = %q, want %q", items[1].title, "/tmp/two")
	}
}

func TestNormalizeSessionTitleFallsBackToCWD(t *testing.T) {
	if got := normalizeSessionTitle("  ", "/root/code/ActRail"); got != "/root/code/ActRail" {
		t.Fatalf("normalizeSessionTitle() = %q, want %q", got, "/root/code/ActRail")
	}
	if got := normalizeSessionTitle("  Focus  ", "/root/code/ActRail"); got != "Focus" {
		t.Fatalf("normalizeSessionTitle() explicit = %q, want %q", got, "Focus")
	}
}
