package ws

import (
	"reflect"
	"testing"

	"actrail/internal/domain/session"
)

func TestParseStreamNameSupportsSystemAndSessions(t *testing.T) {
	for _, raw := range []string{"system", "sessions"} {
		stream, err := ParseStreamName(raw)
		if err != nil {
			t.Fatalf("ParseStreamName(%q) error = %v", raw, err)
		}
		if got := stream.String(); got != raw {
			t.Fatalf("ParseStreamName(%q) = %q, want %q", raw, got, raw)
		}
	}
}

func TestParseStreamNameSupportsSessionRoutes(t *testing.T) {
	stream, err := ParseStreamName("session:s_123:ui")
	if err != nil {
		t.Fatalf("ParseStreamName() error = %v", err)
	}
	if got := stream.String(); got != "session:s_123:ui" {
		t.Fatalf("ParseStreamName() = %q, want %q", got, "session:s_123:ui")
	}
}

func TestMainStreamNameUsesSessionRouteRules(t *testing.T) {
	identity, err := session.NewLiveIdentity("s_123", "r_123", "t_123", "pi")
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	mainStream, err := MainStreamName(identity)
	if err != nil {
		t.Fatalf("MainStreamName() error = %v", err)
	}
	uiStream, err := UIStreamName(identity)
	if err != nil {
		t.Fatalf("UIStreamName() error = %v", err)
	}
	transportStream, err := TransportStreamName(identity)
	if err != nil {
		t.Fatalf("TransportStreamName() error = %v", err)
	}
	if got := []string{mainStream.String(), uiStream.String(), transportStream.String()}; !reflect.DeepEqual(got, []string{"session:s_123", "session:s_123:ui", "session:s_123:transport"}) {
		t.Fatalf("stream names = %#v", got)
	}
}

func TestRefreshPathsForSessionStream(t *testing.T) {
	paths, ok := RefreshPathsForStream(StreamName("session:s_123"))
	if !ok {
		t.Fatal("RefreshPathsForStream() ok = false, want true")
	}
	want := []string{"/api/sessions/s_123/state", "/api/sessions/s_123/messages?limit=100"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("RefreshPathsForStream() = %#v, want %#v", paths, want)
	}
}

func TestRefreshPathsForSystemStreamReturnsFalse(t *testing.T) {
	if _, ok := RefreshPathsForStream(SystemStream); ok {
		t.Fatal("RefreshPathsForStream(system) ok = true, want false")
	}
}
