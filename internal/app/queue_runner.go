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
		committed SessionMessage
		queue     SessionQueueSnapshot
		activated bool
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
		if err := record.runtime.WriteInput(context.Background(), queued.Text()); err != nil {
			return nil
		}
		item, state, ok, err := s.registry.ActivateQueued(sessionID, queued.ID())
		if err != nil || !ok {
			return err
		}
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
