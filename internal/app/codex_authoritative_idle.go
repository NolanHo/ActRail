package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/domain/session"
)

const codexPreSendHistoryTimeout = 750 * time.Millisecond
const codexPreSendThreadProbeTimeout = 1200 * time.Millisecond
const codexPreSendThreadProbePollInterval = 25 * time.Millisecond

var errCodexAuthoritativeStateUnavailable = errors.New("codex authoritative state unavailable")

func (s *Stub) codexAuthoritativeActiveTurn(ctx context.Context, record sessionRecord) (bool, error) {
	if s == nil || record.identity.Backend() != session.BackendCodex || record.runtime.helper == nil {
		return false, nil
	}
	if packet, ok := s.cachedCodexIODHistorySnapshot(record); ok {
		if !codexIODHistoryPacketActiveTurn(packet) {
			return false, nil
		}
		return s.confirmCodexRuntimeActiveTurn(ctx, record)
	}
	historyCtx, cancel := context.WithTimeout(ctx, codexPreSendHistoryTimeout)
	defer cancel()
	packet, err := record.runtime.helper.sessionHistory(historyCtx)
	if err != nil {
		return false, fmt.Errorf("%w: %v", errCodexAuthoritativeStateUnavailable, err)
	}
	s.storeCodexIODHistoryPacket(record.identity.SessionID(), packet)
	if !codexIODHistoryPacketActiveTurn(packet) {
		return false, nil
	}
	return s.confirmCodexRuntimeActiveTurn(ctx, record)
}

func isCodexAuthoritativeStateUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errCodexAuthoritativeStateUnavailable) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (s *Stub) confirmCodexRuntimeActiveTurn(ctx context.Context, record sessionRecord) (bool, error) {
	if s == nil || record.identity.Backend() != session.BackendCodex || record.runtime.protocol != runtimeProtocolCodexRPC || record.runtime.codex == nil {
		return true, nil
	}
	if record.runtime.helper != nil && record.runtime.helper.streamClient == nil {
		return true, nil
	}
	if err := record.runtime.RequestCodexThreadState(ctx); err != nil {
		return true, err
	}
	deadline := time.NewTimer(codexPreSendThreadProbeTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(codexPreSendThreadProbePollInterval)
	defer ticker.Stop()
	for {
		updated, err := s.lookupSession(record.identity.SessionID())
		if err != nil {
			return true, err
		}
		updated.runtime = s.runtimeForRecord(updated)
		if !sameRuntimeHandle(record.runtime, updated.runtime) {
			return true, errRuntimeChanged
		}
		activity := codexVisibleActivity(updated)
		switch activity.Phase {
		case codexRuntimePhaseIdle, codexRuntimePhaseEnded, codexRuntimePhaseFailed:
			return false, nil
		case codexRuntimePhaseRunning, codexRuntimePhaseInterrupting, codexRuntimePhaseWaitingUser:
			if activity.Reason != "codex_authoritative_running" {
				return true, nil
			}
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-deadline.C:
			return true, nil
		case <-ticker.C:
		}
	}
}

func (s *Stub) storeCodexIODHistoryPacket(sessionID session.SessionID, packet iod.SessionHistoryResponsePacket) {
	if s == nil {
		return
	}
	s.codexIODHistoryMu.Lock()
	defer s.codexIODHistoryMu.Unlock()
	s.storeCodexIODHistoryPacketLocked(sessionID, packet)
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
