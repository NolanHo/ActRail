package app

import (
	"encoding/json"
	"fmt"
	"strings"

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
	s.invalidateSessionHistoryCachesForRuntimeMutation(sessionID)
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
	s.invalidateSessionHistoryCachesForRuntimeMutation(sessionID)
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
	case "pi_event":
		msg.Role = ""
		msg.Type = "pi_event"
	case "reasoning":
		msg.Role = ""
		msg.Type = "reasoning"
		msg.Summary = item.Text()
	case "tool", "tool_result":
		msg.Role = ""
		msg.Type = item.Kind().String()
		msg.Name = item.Text()
		msg.Summary = item.Text()
		msg.Details = map[string]any{"name": item.Text()}
	case "custom_message":
		msg.Role = ""
		msg.Type = "custom_message"
		if payload, ok := decodeCodexSubagentMessage(item.Text()); ok {
			applyCodexSubagentMessageFields(&msg, payload)
		}
	}
	return msg
}

type codexSubagentMessagePayload struct {
	Kind     string `json:"kind"`
	Role     string `json:"role"`
	Text     string `json:"text"`
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id,omitempty"`
	ItemID   string `json:"item_id,omitempty"`
}

func encodeCodexSubagentMessage(payload codexSubagentMessagePayload) (string, error) {
	payload.Kind = "codex_subagent_message"
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode codex subagent message: %w", err)
	}
	return string(body), nil
}

func decodeCodexSubagentMessage(text string) (codexSubagentMessagePayload, bool) {
	var payload codexSubagentMessagePayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &payload); err != nil {
		return codexSubagentMessagePayload{}, false
	}
	if payload.Kind != "codex_subagent_message" || strings.TrimSpace(payload.Text) == "" {
		return codexSubagentMessagePayload{}, false
	}
	return payload, true
}

func applyCodexSubagentMessageFields(msg *SessionMessage, payload codexSubagentMessagePayload) {
	if msg == nil {
		return
	}
	role := strings.TrimSpace(payload.Role)
	text := strings.TrimSpace(payload.Text)
	threadID := strings.TrimSpace(payload.ThreadID)
	msg.Role = ""
	msg.Type = "custom_message"
	msg.Text = text
	msg.Name = "Codex Subagent"
	msg.Summary = role
	msg.Details = map[string]any{
		"custom_type": "codex-subagent-message",
		"role":        role,
		"text":        text,
		"thread_id":   threadID,
	}
	if turnID := strings.TrimSpace(payload.TurnID); turnID != "" {
		msg.Details["turn_id"] = turnID
	}
	if itemID := strings.TrimSpace(payload.ItemID); itemID != "" {
		msg.Details["item_id"] = itemID
	}
}
