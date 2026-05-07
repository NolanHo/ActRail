package app

import (
	"context"
	"strings"

	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

func (s *Stub) applyCodexBusy(sessionID session.SessionID, busy bool) error {
	if s == nil {
		return nil
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return nil
	}
	if err := s.setRuntimeAgentRunning(sessionID, busy); err != nil {
		return err
	}
	record, ok = s.registry.Lookup(sessionID)
	if !ok {
		return nil
	}
	if record.state.Busy() == busy {
		return nil
	}
	updated, ok, err := s.registry.SetBusy(sessionID, busy)
	if err != nil || !ok {
		return err
	}
	s.emitSessionState(sessionID)
	if !updated.Busy() {
		s.scheduleQueuedDispatch(sessionID)
	}
	return nil
}

func (s *Stub) withCodexRuntimeState(sessionID session.SessionID, apply func(*codexRuntimeState)) {
	if s == nil || apply == nil {
		return
	}
	_, _, err := s.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = s.runtimeForSession(record.identity.SessionID(), record.identity.Backend(), record.runtime)
		if record.runtime.codex != nil {
			apply(record.runtime.codex)
		}
		return nil
	})
	if err != nil {
		return
	}
}

func (s *Stub) noteCodexInitialized(sessionID session.SessionID) {
	var runtime sessionRuntime
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.markInitialized()
	})
	if record, ok := s.registry.Lookup(sessionID); ok {
		runtime = record.runtime
	}
	if runtime.protocol == runtimeProtocolCodexRPC && runtime.canWriteInput() {
		go func() {
			if err := runtime.EnsureCodexThreadStarted(context.Background()); err != nil {
				_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_thread_start", err)
			}
		}()
	}
}

func (s *Stub) noteCodexThreadID(sessionID session.SessionID, threadID string) {
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.setThreadID(threadID)
	})
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return
	}
	transport := sessionTransportSnapshot(record)
	if transport.State != SessionTransportStateStarting {
		return
	}
	if _, err := s.setSessionTransport(sessionID, transportSnapshotCodexAttached()); err != nil {
		return
	}
	s.emitSessionState(sessionID)
}

func (s *Stub) noteCodexTurnID(sessionID session.SessionID, turnID string) {
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.setActiveTurnID(turnID)
	})
}

func (s *Stub) clearCodexTurnID(sessionID session.SessionID, turnID string) {
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.clearActiveTurnID(turnID)
	})
}

func (s *Stub) applyCodexSubagentMessage(sessionID session.SessionID, event pi.Event) error {
	if event.Message == nil || strings.TrimSpace(event.Message.Text) == "" {
		return nil
	}
	if strings.TrimSpace(event.RawType) != "item/completed" {
		return nil
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return nil
	}
	threadID := strings.TrimSpace(event.ThreadID)
	if threadID == "" {
		return nil
	}
	_, mainThreadID, _ := record.runtime.codex.snapshot()
	if threadID == strings.TrimSpace(mainThreadID) {
		return nil
	}
	payload := codexSubagentMessagePayload{
		Role:     strings.TrimSpace(string(event.Message.Role)),
		Text:     strings.TrimSpace(event.Message.Text),
		ThreadID: threadID,
		TurnID:   strings.TrimSpace(event.TurnID),
		ItemID:   strings.TrimSpace(event.RawID),
	}
	encoded, err := encodeCodexSubagentMessage(payload)
	if err != nil {
		return err
	}
	committed, err := s.AppendSessionMessage(sessionID, "system", "custom_message", encoded)
	if err != nil {
		return err
	}
	committed.Role = ""
	committed.Type = "custom_message"
	committed.EventID = piMessageEventID(event)
	committed.ParentEventID = piParentEventID(event)
	applyCodexSubagentMessageFields(&committed, payload)
	s.emitMessageCommit(sessionID, runtimeTurnID(event), committed)
	return nil
}

func (s *Stub) codexRuntimeEventInMainThread(sessionID session.SessionID, event pi.Event) bool {
	if strings.TrimSpace(event.RawType) != "item/completed" {
		rawType := strings.TrimSpace(event.RawType)
		if rawType != "item/agentMessage/delta" && rawType != "item/reasoning/summaryTextDelta" && rawType != "item/reasoning/textDelta" {
			return true
		}
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return true
	}
	eventThreadID := strings.TrimSpace(event.ThreadID)
	if eventThreadID == "" {
		return true
	}
	_, mainThreadID, _ := record.runtime.codex.snapshot()
	mainThreadID = strings.TrimSpace(mainThreadID)
	if mainThreadID == "" {
		return true
	}
	return eventThreadID == mainThreadID
}

func (s *Stub) codexRuntimeMessageInMainThread(sessionID session.SessionID, event pi.Event) bool {
	if event.Message == nil {
		return true
	}
	return s.codexRuntimeEventInMainThread(sessionID, event)
}

func (s *Stub) codexRuntimeUserMessageInMainThread(sessionID session.SessionID, event pi.Event) bool {
	if strings.TrimSpace(event.RawType) != "item/completed" {
		return true
	}
	if event.Message == nil || event.Message.Role != pi.MessageRoleUser {
		return true
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return true
	}
	eventThreadID := strings.TrimSpace(event.ThreadID)
	if eventThreadID == "" {
		return true
	}
	_, mainThreadID, _ := record.runtime.codex.snapshot()
	mainThreadID = strings.TrimSpace(mainThreadID)
	if mainThreadID == "" {
		return true
	}
	return eventThreadID == mainThreadID
}
