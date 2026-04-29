package app

import "actrail/internal/domain/session"

func (s *Stub) setRuntimeAgentRunning(sessionID session.SessionID, running bool) error {
	if s == nil {
		return nil
	}
	if err := s.registry.SetRuntimeAgentRunning(sessionID, running); err != nil {
		return err
	}
	s.runtimeAgentMu.Lock()
	if s.runtimeAgentRunning == nil {
		s.runtimeAgentRunning = map[session.SessionID]bool{}
	}
	if running {
		s.runtimeAgentRunning[sessionID] = true
	} else {
		delete(s.runtimeAgentRunning, sessionID)
	}
	s.runtimeAgentMu.Unlock()
	return nil
}

func (s *Stub) isRuntimeAgentRunning(sessionID session.SessionID) bool {
	if s == nil {
		return false
	}
	s.runtimeAgentMu.RLock()
	defer s.runtimeAgentMu.RUnlock()
	return s.runtimeAgentRunning[sessionID]
}
