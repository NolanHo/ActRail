package app

import (
	"strings"

	"actrail/internal/domain/session"
)

func (s *Stub) trackCodexOutboundPrompt(sessionID session.SessionID, text string) {
	if s == nil {
		return
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	s.codexOutboundMu.Lock()
	defer s.codexOutboundMu.Unlock()
	if s.codexOutboundPrompt == nil {
		s.codexOutboundPrompt = map[session.SessionID]string{}
	}
	s.codexOutboundPrompt[sessionID] = trimmed
}

func (s *Stub) clearCodexOutboundPrompt(sessionID session.SessionID, text string) {
	if s == nil {
		return
	}
	trimmed := strings.TrimSpace(text)
	s.codexOutboundMu.Lock()
	defer s.codexOutboundMu.Unlock()
	if s.codexOutboundPrompt == nil {
		return
	}
	if current := strings.TrimSpace(s.codexOutboundPrompt[sessionID]); current == "" || current == trimmed {
		delete(s.codexOutboundPrompt, sessionID)
	}
}

func (s *Stub) clearCodexOutboundPromptForSession(sessionID session.SessionID) {
	if s == nil {
		return
	}
	s.codexOutboundMu.Lock()
	defer s.codexOutboundMu.Unlock()
	if s.codexOutboundPrompt != nil {
		delete(s.codexOutboundPrompt, sessionID)
	}
}

func (s *Stub) codexOutboundPromptText(sessionID session.SessionID) string {
	if s == nil {
		return ""
	}
	s.codexOutboundMu.Lock()
	defer s.codexOutboundMu.Unlock()
	return strings.TrimSpace(s.codexOutboundPrompt[sessionID])
}

func (s *Stub) codexOutboundPromptMatches(sessionID session.SessionID, text string) bool {
	if s == nil {
		return false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	s.codexOutboundMu.Lock()
	defer s.codexOutboundMu.Unlock()
	return strings.TrimSpace(s.codexOutboundPrompt[sessionID]) == trimmed
}
