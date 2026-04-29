package app

import (
	"fmt"

	"actrail/internal/domain/message"
	"actrail/internal/domain/session"
)

// SessionTranscriptWriter exposes explicit transcript mutation seams for runtime adapters.
type SessionTranscriptWriter interface {
	AppendSessionMessage(session.SessionID, string, string, string) (SessionMessage, error)
	AppendAssistantDelta(session.SessionID, string, string) (*PartialAssistantTurnSnapshot, error)
	CommitAssistantTurn(session.SessionID, string, string) (SessionMessage, error)
}

func (s *Stub) AppendSessionMessage(sessionID session.SessionID, role, kind, text string) (SessionMessage, error) {
	item, ok, err := s.registry.AppendMessage(sessionID, role, kind, text)
	if err != nil {
		return SessionMessage{}, err
	}
	if !ok {
		return SessionMessage{}, NotFound(fmt.Sprintf("session %q not found", sessionID))
	}
	s.messageCache.Invalidate(sessionID)
	return sessionMessageFromCommitted(item), nil
}

func (s *Stub) AppendAssistantDelta(sessionID session.SessionID, turnID, delta string) (*PartialAssistantTurnSnapshot, error) {
	partial, ok, err := s.registry.AppendAssistantDelta(sessionID, turnID, delta)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, NotFound(fmt.Sprintf("session %q not found", sessionID))
	}
	return &PartialAssistantTurnSnapshot{TurnID: partial.TurnID().String(), Text: partial.Text()}, nil
}

func (s *Stub) CommitAssistantTurn(sessionID session.SessionID, turnID, text string) (SessionMessage, error) {
	item, ok, err := s.registry.CommitAssistantTurn(sessionID, turnID, text)
	if err != nil {
		return SessionMessage{}, err
	}
	if !ok {
		return SessionMessage{}, NotFound(fmt.Sprintf("session %q not found", sessionID))
	}
	s.messageCache.Invalidate(sessionID)
	return sessionMessageFromCommitted(item), nil
}

func sessionMessageFromCommitted(item message.CommittedMessage) SessionMessage {
	msg := SessionMessage{
		Seq:  item.Seq().Uint64(),
		Role: item.Role().String(),
		Kind: item.Kind().String(),
		Text: item.Text(),
		TS:   timestampSeconds(item.TS()),
	}
	switch item.Kind().String() {
	case "error":
		msg.Type = "error"
		msg.IsError = true
	case "tool", "tool_result":
		msg.Role = ""
		msg.Type = item.Kind().String()
		msg.Name = item.Text()
		msg.Summary = item.Text()
		msg.Details = map[string]any{"name": item.Text()}
	}
	return msg
}
