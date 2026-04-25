package ws

import (
	"fmt"
	"strings"

	"actrail/internal/domain/session"
)

type StreamName string

const (
	SystemStream   StreamName = "system"
	SessionsStream StreamName = "sessions"
)

func ParseStreamName(raw string) (StreamName, error) {
	value := strings.TrimSpace(raw)
	switch value {
	case "":
		return "", fmt.Errorf("stream name is required")
	case string(SystemStream):
		return SystemStream, nil
	case string(SessionsStream):
		return SessionsStream, nil
	default:
		if _, err := session.ParseStreamRoute(value); err != nil {
			return "", err
		}
		return StreamName(value), nil
	}
}

func (s StreamName) Validate() error {
	_, err := ParseStreamName(string(s))
	return err
}

func (s StreamName) String() string {
	return string(s)
}

func StreamFromRoute(route session.StreamRoute) StreamName {
	return StreamName(route.String())
}

func ParseSessionStream(stream StreamName) (session.StreamRoute, error) {
	return session.ParseStreamRoute(stream.String())
}

func MainStreamName(identity session.Identity) (StreamName, error) {
	route, err := session.MainStream(identity)
	if err != nil {
		return "", err
	}
	return StreamFromRoute(route), nil
}

func UIStreamName(identity session.Identity) (StreamName, error) {
	route, err := session.UIStream(identity)
	if err != nil {
		return "", err
	}
	return StreamFromRoute(route), nil
}

func TransportStreamName(identity session.Identity) (StreamName, error) {
	route, err := session.TransportStream(identity)
	if err != nil {
		return "", err
	}
	return StreamFromRoute(route), nil
}

func RefreshPathsForStream(stream StreamName) ([]string, bool) {
	if stream == SessionsStream || stream == SystemStream {
		return nil, false
	}
	route, err := session.ParseStreamRoute(stream.String())
	if err != nil {
		return nil, false
	}
	sessionID := route.SessionID().String()
	return []string{
		"/api/sessions/" + sessionID + "/state",
		"/api/sessions/" + sessionID + "/messages?limit=100",
	}, true
}
