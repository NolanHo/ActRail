package app

import (
	"context"

	"actrail/internal/domain/session"
)

func (s *Stub) scheduleQueuedDispatch(sessionID session.SessionID) {
	if s == nil {
		return
	}
	go s.dispatchQueuedPrompt(sessionID)
}

func (s *Stub) dispatchQueuedPrompt(sessionID session.SessionID) {
	var (
		committed           SessionMessage
		queue               SessionQueueSnapshot
		activated           bool
		pollRuntime         sessionRuntime
		pollPIState         bool
		codexRuntime        sessionRuntime
		watchCodexTurnStart bool
	)
	if err := s.withSessionInputLock(sessionID, func(record sessionRecord) error {
		record.runtime = s.runtimeForRecord(record)
		busy, reason := effectiveBusy(record)
		if busy || record.uiRequest != nil {
			if busy && record.identity.Backend() == session.BackendCodex && reason == "codex_authoritative_running" {
				active, err := s.confirmCodexRuntimeActiveTurn(context.Background(), record)
				if err != nil {
					_ = s.emitRuntimeControlDiagnostic(sessionID, "queued_pre_send_codex_state", err)
					return nil
				}
				if !active {
					busy = false
				}
			}
		}
		if busy || record.uiRequest != nil {
			return nil
		}
		items := record.state.Queue().Items()
		if len(items) == 0 {
			return nil
		}
		queued := items[0]
		if err := transportControlError(s.sessionTransportSnapshot(record)); err != nil {
			_ = s.emitRuntimeControlDiagnostic(sessionID, "queued_send", err)
			return nil
		}
		if err := s.prepareRuntimeSend(context.Background(), sessionID, record.runtime); err != nil {
			_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseFailed, "codex_queued_prepare_failed", "queued_prepare_failed")
			_ = s.emitRuntimeControlDiagnostic(sessionID, "queued_prepare_send", err)
			return nil
		}
		current, err := s.lookupSession(sessionID)
		if err == nil {
			current.runtime = s.runtimeForRecord(current)
		}
		if err != nil || !sameRuntimeHandle(record.runtime, current.runtime) {
			_ = s.emitRuntimeControlDiagnostic(sessionID, "queued_send", errRuntimeChanged)
			return nil
		}
		runtime := current.runtime
		if runtime.protocol == runtimeProtocolCodexRPC {
			active, err := s.codexAuthoritativeActiveTurn(context.Background(), current)
			if err != nil {
				_ = s.emitRuntimeControlDiagnostic(sessionID, "queued_pre_send_codex_state", err)
				return nil
			}
			if active {
				_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseRunning, "codex_authoritative_running", "queued_pre_send_codex_state")
				return nil
			}
			s.trackCodexOutboundPrompt(sessionID, queued.Text())
			s.trackCodexCapacityRetryPrompt(sessionID, queued.Text())
			_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseSending, "codex_queued_sending", "queued_send")
		}
		if err := runtime.SendPromptWithStaleCheck(context.Background(), queued.Text(), func() bool {
			current, err := s.lookupSession(sessionID)
			if err == nil {
				current.runtime = s.runtimeForRecord(current)
			}
			return err != nil || !sameRuntimeHandle(runtime, current.runtime)
		}); err != nil {
			_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseFailed, "codex_queued_send_failed", "queued_send_failed")
			_ = s.emitRuntimeControlDiagnostic(sessionID, "queued_send", err)
			if runtime.protocol == runtimeProtocolCodexRPC {
				s.clearCodexOutboundPrompt(sessionID, queued.Text())
			}
			return nil
		}
		if runtime.protocol == runtimeProtocolCodexRPC {
			_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseTurnStarting, "codex_queued_turn_starting", "queued_turn_starting")
			codexRuntime = runtime
			watchCodexTurnStart = true
		}
		busyOnSend := record.identity.Backend() != session.BackendPI
		if record.identity.Backend() == session.BackendPI {
			pollRuntime = runtime
			pollPIState = true
			if runtime.protocol == runtimeProtocolPIRPC && runtime.helper != nil {
				busyOnSend = true
				s.holdPIRPCBusy(sessionID, runtime.helper.generationID)
				s.kickPIRPCStateProbe(sessionID, runtime.helper.generationID)
			}
		}
		item, state, ok, err := s.registry.ActivateQueuedWithBusy(sessionID, queued.ID(), busyOnSend)
		if err != nil || !ok {
			return err
		}
		s.invalidateSessionHistoryCaches(sessionID)
		committed = sessionMessageFromCommitted(item)
		queue = queueSnapshotFromState(state)
		activated = true
		return nil
	}); err != nil || !activated {
		return
	}
	s.emitMessageCommit(sessionID, "", committed)
	s.emitQueueState(sessionID, queue)
	s.emitSessionState(sessionID)
	if pollPIState {
		s.startPIRPCStatePolling(sessionID, pollRuntime)
	}
	if watchCodexTurnStart {
		s.startCodexTurnStartWatch(sessionID, codexRuntime)
	}
}

func (s *Stub) withSessionInputLock(sessionID session.SessionID, fn func(sessionRecord) error) error {
	record, err := s.lookupSession(sessionID)
	if err != nil {
		return err
	}
	if record.inputMu == nil {
		return fn(record)
	}
	record.inputMu.Lock()
	defer record.inputMu.Unlock()
	record, err = s.lookupSession(sessionID)
	if err != nil {
		return err
	}
	return fn(record)
}
