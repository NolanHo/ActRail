package session

import "testing"

func TestStreamRouteUsesLiveSessionIDOnly(t *testing.T) {
	identity, err := NewLiveIdentity("s_123", "r_123", "t_123", "pi")
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	mainRoute, err := MainStream(identity)
	if err != nil {
		t.Fatalf("MainStream() error = %v", err)
	}
	if got := mainRoute.String(); got != "session:s_123" {
		t.Fatalf("MainStream() = %q, want %q", got, "session:s_123")
	}
	uiRoute, err := UIStream(identity)
	if err != nil {
		t.Fatalf("UIStream() error = %v", err)
	}
	if got := uiRoute.String(); got != "session:s_123:ui" {
		t.Fatalf("UIStream() = %q, want %q", got, "session:s_123:ui")
	}
	transportRoute, err := TransportStream(identity)
	if err != nil {
		t.Fatalf("TransportStream() error = %v", err)
	}
	if got := transportRoute.String(); got != "session:s_123:transport" {
		t.Fatalf("TransportStream() = %q, want %q", got, "session:s_123:transport")
	}
}

func TestStreamRouteRejectsHistoricalSessionID(t *testing.T) {
	historical, err := NewHistoricalIdentity("resume-1", "pi")
	if err != nil {
		t.Fatalf("NewHistoricalIdentity() error = %v", err)
	}
	if _, err := MainStream(historical); err == nil {
		t.Fatal("MainStream() error = nil, want error")
	}
}

func TestParseStreamRouteNormalizesWhitespace(t *testing.T) {
	route, err := ParseStreamRoute("  session:s_123:ui  ")
	if err != nil {
		t.Fatalf("ParseStreamRoute() error = %v", err)
	}
	if got := route.String(); got != "session:s_123:ui" {
		t.Fatalf("String() = %q, want %q", got, "session:s_123:ui")
	}
}

func TestParseStreamRouteRejectsHistoricalSyntheticID(t *testing.T) {
	if _, err := ParseStreamRoute("session:history:pi:resume-1"); err == nil {
		t.Fatal("ParseStreamRoute() error = nil, want error")
	}
}
