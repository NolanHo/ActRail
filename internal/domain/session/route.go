package session

import (
	"fmt"
	"strings"
)

// StreamKind identifies the logical realtime channel attached to a live session.
type StreamKind string

const (
	StreamKindMain      StreamKind = ""
	StreamKindUI        StreamKind = "ui"
	StreamKindTransport StreamKind = "transport"
)

func ParseStreamKind(raw string) (StreamKind, error) {
	kind := StreamKind(strings.ToLower(strings.TrimSpace(raw)))
	if err := kind.Validate(); err != nil {
		return "", err
	}
	return kind, nil
}

func (k StreamKind) Validate() error {
	switch k {
	case StreamKindMain, StreamKindUI, StreamKindTransport:
		return nil
	default:
		return fmt.Errorf("stream kind %q is not supported", string(k))
	}
}

func (k StreamKind) suffix() string {
	if k == StreamKindMain {
		return ""
	}
	return ":" + string(k)
}

// StreamRoute is the canonical websocket stream route for one live session.
type StreamRoute struct {
	sessionID SessionID
	kind      StreamKind
}

func NewStreamRoute(sessionID SessionID, kind StreamKind) (StreamRoute, error) {
	if err := sessionID.Validate(); err != nil {
		return StreamRoute{}, err
	}
	if sessionID.IsHistorical() {
		return StreamRoute{}, fmt.Errorf("historical session %q has no live stream route", sessionID)
	}
	if err := kind.Validate(); err != nil {
		return StreamRoute{}, err
	}
	return StreamRoute{sessionID: sessionID, kind: kind}, nil
}

func MainStream(identity Identity) (StreamRoute, error) {
	if err := identity.Validate(); err != nil {
		return StreamRoute{}, err
	}
	return NewStreamRoute(identity.SessionID(), StreamKindMain)
}

func UIStream(identity Identity) (StreamRoute, error) {
	if err := identity.Validate(); err != nil {
		return StreamRoute{}, err
	}
	return NewStreamRoute(identity.SessionID(), StreamKindUI)
}

func TransportStream(identity Identity) (StreamRoute, error) {
	if err := identity.Validate(); err != nil {
		return StreamRoute{}, err
	}
	return NewStreamRoute(identity.SessionID(), StreamKindTransport)
}

func ParseStreamRoute(raw string) (StreamRoute, error) {
	value := strings.TrimSpace(raw)
	if !strings.HasPrefix(value, "session:") {
		return StreamRoute{}, fmt.Errorf("stream route %q must start with \"session:\"", value)
	}
	rest := strings.TrimPrefix(value, "session:")
	parts := strings.Split(rest, ":")
	if len(parts) == 0 || len(parts) > 2 {
		return StreamRoute{}, fmt.Errorf("stream route %q is malformed", value)
	}
	sessionID, err := ParseSessionID(parts[0])
	if err != nil {
		return StreamRoute{}, err
	}
	kind := StreamKindMain
	if len(parts) == 2 {
		kind, err = ParseStreamKind(parts[1])
		if err != nil {
			return StreamRoute{}, err
		}
	}
	return NewStreamRoute(sessionID, kind)
}

func (r StreamRoute) Validate() error {
	_, err := NewStreamRoute(r.sessionID, r.kind)
	return err
}

func (r StreamRoute) SessionID() SessionID {
	return r.sessionID
}

func (r StreamRoute) Kind() StreamKind {
	return r.kind
}

func (r StreamRoute) String() string {
	return "session:" + r.sessionID.String() + r.kind.suffix()
}
