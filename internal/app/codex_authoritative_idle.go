package app

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/domain/session"
)

const codexPreSendHistoryTimeout = 750 * time.Millisecond

func (s *Stub) codexAuthoritativeActiveTurn(ctx context.Context, record sessionRecord) (bool, error) {
	if s == nil || record.identity.Backend() != session.BackendCodex || record.runtime.helper == nil {
		return false, nil
	}
	historyCtx, cancel := context.WithTimeout(ctx, codexPreSendHistoryTimeout)
	defer cancel()
	packet, err := record.runtime.helper.sessionHistory(historyCtx)
	if err != nil {
		return false, err
	}
	s.storeCodexIODHistoryPacket(record.identity.SessionID(), packet)
	return codexIODHistoryPacketActiveTurn(packet), nil
}

func (s *Stub) storeCodexIODHistoryPacket(sessionID session.SessionID, packet iod.SessionHistoryResponsePacket) {
	if s == nil {
		return
	}
	s.codexIODHistoryMu.Lock()
	defer s.codexIODHistoryMu.Unlock()
	if s.codexIODHistory == nil {
		s.codexIODHistory = map[session.SessionID]codexIODHistoryCacheEntry{}
	}
	s.codexIODHistory[sessionID] = codexIODHistoryCacheEntry{
		packet:    packet,
		checkedAt: time.Now(),
	}
}

func codexIODHistoryPacketActiveTurn(packet iod.SessionHistoryResponsePacket) bool {
	if packet.TaskComplete {
		return false
	}
	if codexSessionMessagesHaveAuthoritativeCompletion(sessionMessagesFromIODHistory(packet.Messages)) {
		return false
	}
	if codexSessionLinesIndicateActiveTurn(packet.Lines) {
		return true
	}
	if len(packet.Lines) > 0 {
		return false
	}
	return len(packet.Messages) > 0
}

func codexSessionLinesIndicateActiveTurn(lines []string) bool {
	lastRelevant := ""
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var entry codexSessionLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return false
		}
		switch strings.TrimSpace(entry.Type) {
		case "event_msg":
			switch kind := strings.TrimSpace(stringValue(entry.Payload["type"])); kind {
			case "user_message", "agent_message", "task_started", "task_complete", "turn_aborted":
				lastRelevant = kind
			}
		case "response_item":
			switch strings.TrimSpace(stringValue(entry.Payload["type"])) {
			case "message", "function_call", "function_call_output", "reasoning":
				lastRelevant = "response_item"
			}
		}
	}
	return lastRelevant != "" && !codexHistoryTerminalKind(lastRelevant)
}

func codexHistoryTerminalKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "task_complete", "turn_aborted":
		return true
	default:
		return false
	}
}
