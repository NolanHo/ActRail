package session

import "testing"

func TestNewLiveIdentityUsesDurableSessionIDAsRouteKey(t *testing.T) {
	identity, err := NewLiveIdentity("  s_123  ", "  r_123  ", "  t_123  ", "  PI  ")
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if identity.Historical() {
		t.Fatal("identity.Historical() = true, want false")
	}
	if got := identity.SessionID().String(); got != "s_123" {
		t.Fatalf("SessionID() = %q, want %q", got, "s_123")
	}
	if got := identity.DurableID().String(); got != "s_123" {
		t.Fatalf("DurableID() = %q, want %q", got, "s_123")
	}
	if got := identity.HTTPRouteKey(); got != "s_123" {
		t.Fatalf("HTTPRouteKey() = %q, want %q", got, "s_123")
	}
	runtimeID, ok := identity.RuntimeID()
	if !ok || runtimeID.String() != "r_123" {
		t.Fatalf("RuntimeID() = (%q, %v), want (%q, true)", runtimeID, ok, "r_123")
	}
	threadID, ok := identity.ThreadID()
	if !ok || threadID.String() != "t_123" {
		t.Fatalf("ThreadID() = (%q, %v), want (%q, true)", threadID, ok, "t_123")
	}
	if identity.Backend() != BackendPI {
		t.Fatalf("Backend() = %q, want %q", identity.Backend(), BackendPI)
	}
}

func TestNewLiveIdentityRejectsHistoricalSessionID(t *testing.T) {
	if _, err := NewLiveIdentity("history:pi:resume-1", "r_123", "t_123", "pi"); err == nil {
		t.Fatal("NewLiveIdentity() error = nil, want error")
	}
}

func TestNewHistoricalIdentityBuildsSyntheticSessionID(t *testing.T) {
	identity, err := NewHistoricalIdentity(" resume-1 ", " PI ")
	if err != nil {
		t.Fatalf("NewHistoricalIdentity() error = %v", err)
	}
	if !identity.Historical() {
		t.Fatal("identity.Historical() = false, want true")
	}
	if got := identity.SessionID().String(); got != "history:pi:resume-1" {
		t.Fatalf("SessionID() = %q, want %q", got, "history:pi:resume-1")
	}
	if got := identity.DurableID().String(); got != "resume-1" {
		t.Fatalf("DurableID() = %q, want %q", got, "resume-1")
	}
	if _, ok := identity.RuntimeID(); ok {
		t.Fatal("RuntimeID() ok = true, want false")
	}
}

func TestParseHistoricalSessionIDNormalizesBackend(t *testing.T) {
	ref, err := ParseHistoricalSessionID("history:PI:resume-1")
	if err != nil {
		t.Fatalf("ParseHistoricalSessionID() error = %v", err)
	}
	if ref.Backend != BackendPI {
		t.Fatalf("Backend = %q, want %q", ref.Backend, BackendPI)
	}
	if ref.Durable.String() != "resume-1" {
		t.Fatalf("Durable = %q, want %q", ref.Durable, "resume-1")
	}
}

func TestParseHistoricalSessionIDRejectsMissingDurableID(t *testing.T) {
	if _, err := ParseHistoricalSessionID("history:pi:"); err == nil {
		t.Fatal("ParseHistoricalSessionID() error = nil, want error")
	}
}

func TestDurableIDRejectsRouteDelimiters(t *testing.T) {
	for _, raw := range []string{"s:123", "s/123", "s 123"} {
		if _, err := NewDurableID(raw); err == nil {
			t.Fatalf("NewDurableID(%q) error = nil, want error", raw)
		}
	}
}

func TestRuntimeAndThreadIDsRejectRouteDelimiters(t *testing.T) {
	for _, raw := range []string{"r:123", "r/123", "r 123"} {
		if _, err := NewRuntimeID(raw); err == nil {
			t.Fatalf("NewRuntimeID(%q) error = nil, want error", raw)
		}
		if _, err := NewThreadID(raw); err == nil {
			t.Fatalf("NewThreadID(%q) error = nil, want error", raw)
		}
	}
}
