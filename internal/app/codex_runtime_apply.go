package app

import (
	"context"
	"strings"

	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

func (s *Stub) applyCodexBusy(sessionID session.SessionID, busy bool) error {
	if s == nil {
		return nil
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return nil
	}
	var changed bool
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		_, changed = state.applyProtocolBusy(busy)
	})
	return s.syncCodexRuntimeActivity(sessionID, "protocol_busy", changed)
}

func (s *Stub) withCodexRuntimeState(sessionID session.SessionID, apply func(*codexRuntimeState)) {
	if s == nil || apply == nil {
		return
	}
	_, _, err := s.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = s.runtimeForRecord(*record)
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
	changed := false
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		before := state.activity()
		state.markInitialized()
		after := state.activity()
		changed = before != after
	})
	_ = s.syncCodexRuntimeActivity(sessionID, "initialized", changed)
	if record, ok := s.registry.Lookup(sessionID); ok {
		runtime = record.runtime
	}
	if runtime.protocol == runtimeProtocolCodexRPC && runtime.canWriteInput() {
		reason := "codex_thread_starting"
		if runtime.PendingCodexResumeThreadID() != "" {
			reason = "codex_thread_resuming"
		}
		_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseThreadStarting, reason, "thread_start")
		go func() {
			if err := runtime.EnsureCodexThreadStarted(context.Background()); err != nil {
				_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseFailed, "codex_thread_start_failed", "thread_start_failed")
				_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_thread_start", err)
			}
		}()
	}
}

func (s *Stub) noteCodexThreadID(sessionID session.SessionID, threadID string, sourcePath ...string) {
	changed := false
	accepted := false
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		before := state.activity()
		var stateChanged bool
		accepted, stateChanged = state.setThreadID(threadID)
		if !accepted {
			return
		}
		after := state.activity()
		changed = stateChanged || before != after
	})
	if !accepted {
		return
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return
	}
	s.rememberCodexThreadBinding(record, threadID, sourcePath...)
	transport := sessionTransportSnapshot(record)
	if transport.State != SessionTransportStateStarting {
		_ = s.syncCodexRuntimeActivity(sessionID, "thread_id", changed)
		return
	}
	if _, err := s.setSessionTransport(sessionID, transportSnapshotCodexAttached()); err != nil {
		return
	}
	_ = s.syncCodexRuntimeActivity(sessionID, "thread_attached", true)
}

func (s *Stub) noteCodexProtocolDesynced(sessionID session.SessionID) {
	var runtime sessionRuntime
	fallbackThreadID := ""
	if record, ok := s.registry.Lookup(sessionID); ok {
		fallbackThreadID = record.importedBackendSessionID
	}
	changed := false
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		before := state.activity()
		_, stateChanged := state.resetProtocolForResume(fallbackThreadID)
		after := state.activity()
		changed = stateChanged || before != after
	})
	_ = s.syncCodexRuntimeActivity(sessionID, "protocol_desynced", changed)
	if record, ok := s.registry.Lookup(sessionID); ok {
		record.runtime = s.runtimeForRecord(record)
		runtime = record.runtime
	}
	if runtime.protocol == runtimeProtocolCodexRPC && runtime.canWriteInput() {
		pendingPrompt := s.codexOutboundPromptText(sessionID)
		go func() {
			if err := runtime.EnsureCodexThread(context.Background()); err != nil {
				_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseFailed, "codex_protocol_recovery_failed", "protocol_recovery_failed")
				_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_protocol_recovery", err)
				return
			}
			if pendingPrompt == "" {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), codexRuntimeBootstrapTimeout)
			defer cancel()
			if err := runtime.WaitCodexThreadReady(ctx); err != nil {
				_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseFailed, "codex_recovered_thread_unavailable", "protocol_recovered_thread_unavailable")
				_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_protocol_recovered_send", err)
				return
			}
			_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseSending, "codex_recovered_sending", "protocol_recovered_send")
			if err := runtime.SendPrompt(context.Background(), pendingPrompt); err != nil {
				_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseFailed, "codex_recovered_send_failed", "protocol_recovered_send_failed")
				_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_protocol_recovered_send", err)
				return
			}
			_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseTurnStarting, "codex_recovered_turn_starting", "protocol_recovered_turn_starting")
			s.startCodexTurnStartWatch(sessionID, runtime)
		}()
	}
}

func (s *Stub) noteCodexTurnID(sessionID session.SessionID, turnID string) {
	changed := false
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		before := state.activity()
		state.setActiveTurnID(turnID)
		after := state.activity()
		changed = before != after
	})
	s.clearCodexOutboundPromptForSession(sessionID)
	_ = s.syncCodexRuntimeActivity(sessionID, "turn_started", changed)
}

func (s *Stub) flushCodexPendingInterrupt(sessionID session.SessionID) {
	if s == nil {
		return
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex || record.runtime.protocol != runtimeProtocolCodexRPC {
		return
	}
	if err := record.runtime.FlushCodexPendingInterrupt(context.Background()); err != nil {
		_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_pending_interrupt", err)
	}
}

func (s *Stub) clearCodexTurnID(sessionID session.SessionID, turnID string) {
	changed := false
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		before := state.activity()
		state.clearActiveTurnID(turnID)
		after := state.activity()
		changed = before != after
	})
	_ = s.syncCodexRuntimeActivity(sessionID, "turn_cleared", changed)
}

func (s *Stub) transitionCodexRuntime(sessionID session.SessionID, phase codexRuntimePhase, reason string, cause string) error {
	if s == nil {
		return nil
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return nil
	}
	changed := false
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		_, changed = state.transition(phase, reason)
	})
	return s.syncCodexRuntimeActivity(sessionID, cause, changed)
}

func (s *Stub) syncCodexRuntimeActivity(sessionID session.SessionID, cause string, forceEmit bool) error {
	if s == nil {
		return nil
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return nil
	}
	visible := codexVisibleActivity(record)
	registryBusy := codexRegistryBusy(record, visible)
	rawBusy := record.state.Busy()
	queueLen := record.state.Queue().Len()

	tracer := otel.Tracer("actrail/app")
	ctx, span := tracer.Start(context.Background(), "app.codexRuntime.sync")
	_ = ctx
	span.SetAttributes(
		attribute.String("session.id", sessionID.String()),
		attribute.String("codex.runtime.phase", string(visible.Phase)),
		attribute.String("codex.runtime.reason", visible.Reason),
		attribute.Bool("codex.runtime.busy", visible.Busy),
		attribute.Bool("session.state.busy", rawBusy),
		attribute.Bool("session.state.target_busy", registryBusy),
		attribute.Int("session.queue.len", queueLen),
		attribute.String("codex.runtime.cause", strings.TrimSpace(cause)),
	)
	defer span.End()

	if err := s.setRuntimeAgentRunning(sessionID, visible.Busy); err != nil {
		return err
	}
	updatedStateBusy := rawBusy
	if rawBusy != registryBusy {
		updated, setOK, err := s.registry.SetBusy(sessionID, registryBusy)
		if err != nil || !setOK {
			return err
		}
		updatedStateBusy = updated.Busy()
		forceEmit = true
	}
	if forceEmit {
		s.emitSessionState(sessionID)
	}
	if !updatedStateBusy && (rawBusy || queueLen > 0) {
		s.scheduleQueuedDispatch(sessionID)
	}
	return nil
}

func codexRegistryBusy(record sessionRecord, visible codexRuntimeActivity) bool {
	if record.identity.Historical() {
		return false
	}
	if visible.Phase == codexRuntimePhaseFailed || visible.Phase == codexRuntimePhaseEnded {
		return false
	}
	if _, ok := record.transcript.PartialAssistantTurn(); ok {
		return true
	}
	return visible.Busy
}

func codexVisibleActivity(record sessionRecord) codexRuntimeActivity {
	if record.identity.Historical() || record.identity.Backend() != session.BackendCodex {
		return codexRuntimeActivity{Phase: codexRuntimePhaseIdle}
	}
	transport := sessionTransportSnapshot(record)
	if record.transport.ResetRequired || transport.ResetRequired || transport.State == SessionTransportStateBroken || transport.State == SessionTransportStateFailed {
		reason := strings.TrimSpace(firstNonEmptyString(record.transport.Reason, transport.Reason))
		if reason == "" {
			reason = "transport_" + strings.TrimSpace(transport.State.String())
		}
		return codexRuntimeActivity{Phase: codexRuntimePhaseFailed, Reason: reason}
	}
	if transport.State == SessionTransportStateEnded {
		if strings.TrimSpace(transport.Reason) == "authoritative_final_answer" {
			return codexRuntimeActivity{Phase: codexRuntimePhaseIdle}
		}
		reason := strings.TrimSpace(transport.Reason)
		if reason == "" {
			reason = "transport_ended"
		}
		return codexRuntimeActivity{Phase: codexRuntimePhaseEnded, Reason: reason}
	}
	if record.uiRequest != nil {
		return codexRuntimeActivity{Phase: codexRuntimePhaseWaitingUser, Reason: "ui_request", Busy: true}
	}
	if _, ok := record.transcript.PartialAssistantTurn(); ok {
		return codexRuntimeActivity{Phase: codexRuntimePhaseRunning, Reason: "partial_assistant_turn", Busy: true}
	}
	if record.runtime.codex == nil {
		if record.state.Busy() {
			return codexRuntimeActivity{Phase: codexRuntimePhaseRunning, Reason: "state_busy", Busy: true}
		}
		return codexRuntimeActivity{Phase: codexRuntimePhaseIdle}
	}
	if transport.State == SessionTransportStateStarting {
		phase := codexRuntimePhaseThreadStarting
		if strings.TrimSpace(transport.Reason) == "codex_initializing" {
			phase = codexRuntimePhaseInitializing
		}
		return codexRuntimeActivity{Phase: phase, Reason: transport.Reason, Busy: true}
	}
	activity := record.runtime.codex.activity()
	if activity.Busy {
		return activity
	}
	return codexRuntimeActivity{Phase: activity.Phase}
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
	if record.runtime.codex == nil {
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
	if strings.TrimSpace(event.ThreadID) != "" {
		return s.codexThreadIDInMainThread(sessionID, event.ThreadID)
	}
	rawType := strings.TrimSpace(event.RawType)
	if rawType != "item/completed" &&
		rawType != "item/agentMessage/delta" &&
		rawType != "item/reasoning/summaryTextDelta" &&
		rawType != "item/reasoning/textDelta" &&
		rawType != "turn/started" &&
		rawType != "turn/completed" {
		return true
	}
	return s.codexThreadIDInMainThread(sessionID, event.ThreadID)
}

func (s *Stub) codexThreadIDInMainThread(sessionID session.SessionID, threadID string) bool {
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return true
	}
	eventThreadID := strings.TrimSpace(threadID)
	if eventThreadID == "" {
		return true
	}
	if record.runtime.codex == nil {
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
	return s.codexThreadIDInMainThread(sessionID, event.ThreadID)
}
