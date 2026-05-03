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
		committed   SessionMessage
		queue       SessionQueueSnapshot
		activated   bool
		pollRuntime sessionRuntime
		pollPIState bool
	)
	if err := s.withSessionInputLock(sessionID, func(record sessionRecord) error {
		if record.state.Busy() || record.uiRequest != nil {
			return nil
		}
		items := record.state.Queue().Items()
		if len(items) == 0 {
			return nil
		}
		queued := items[0]
		if err := transportControlError(sessionTransportSnapshot(record)); err != nil {
			_ = s.emitRuntimeControlDiagnostic(sessionID, "queued_send", err)
			return nil
		}
		if err := record.runtime.SendPromptWithStaleCheck(context.Background(), queued.Text(), func() bool {
			current, err := s.lookupSession(sessionID)
			return err != nil || !sameRuntime(record, current)
		}); err != nil {
			_ = s.emitRuntimeControlDiagnostic(sessionID, "queued_send", err)
			return nil
		}
		busyOnSend := record.identity.Backend() != session.BackendPI
		if record.identity.Backend() == session.BackendPI {
			pollRuntime = record.runtime
			pollPIState = true
			if record.runtime.protocol == runtimeProtocolPIRPC && record.runtime.helper != nil {
				busyOnSend = true
				s.holdPIRPCBusy(sessionID, record.runtime.helper.generationID)
				s.startPIRPCStartupProbe(sessionID, record.runtime.helper.generationID)
			}
		}
		item, state, ok, err := s.registry.ActivateQueuedWithBusy(sessionID, queued.ID(), busyOnSend)
		if err != nil || !ok {
			return err
		}
		s.messageCache.Invalidate(sessionID)
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
