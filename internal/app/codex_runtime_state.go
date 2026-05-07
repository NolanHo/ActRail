package app

import (
	"fmt"
	"strings"
	"sync"

	"actrail/internal/domain/session"
)

type codexRuntimeState struct {
	mu              sync.Mutex
	requestSeq      uint64
	initialized     bool
	initializeSent  bool
	threadStartSent bool
	threadID        string
	activeTurnID    string
}

func newCodexRuntimeState(backend session.Backend) *codexRuntimeState {
	if backend != session.BackendCodex {
		return nil
	}
	return &codexRuntimeState{}
}

func (s *codexRuntimeState) nextRequestID(prefix string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestSeq++
	return fmt.Sprintf("%s-%d", strings.TrimSpace(prefix), s.requestSeq)
}

func (s *codexRuntimeState) bootstrapRequests() []any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := make([]any, 0, 2)
	if !s.initialized && !s.initializeSent {
		s.requestSeq++
		s.initializeSent = true
		requests = append(requests, map[string]any{
			"method": "initialize",
			"id":     fmt.Sprintf("initialize-%d", s.requestSeq),
			"params": map[string]any{
				"clientInfo":   map[string]any{"name": "actrail", "version": "0"},
				"capabilities": nil,
			},
		})
	}
	if s.initialized && s.threadID == "" && !s.threadStartSent {
		s.requestSeq++
		s.threadStartSent = true
		requests = append(requests, map[string]any{
			"method": "thread/start",
			"id":     fmt.Sprintf("thread-start-%d", s.requestSeq),
			"params": map[string]any{
				"experimentalRawEvents":  false,
				"persistExtendedHistory": false,
			},
		})
	}
	return requests
}

func (s *codexRuntimeState) threadStartRequest() any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialized || s.threadID != "" || s.threadStartSent {
		return nil
	}
	s.requestSeq++
	s.threadStartSent = true
	return map[string]any{
		"method": "thread/start",
		"id":     fmt.Sprintf("thread-start-%d", s.requestSeq),
		"params": map[string]any{
			"experimentalRawEvents":  false,
			"persistExtendedHistory": false,
		},
	}
}

func (s *codexRuntimeState) markInitialized() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initialized = true
	s.initializeSent = false
}

func (s *codexRuntimeState) setThreadID(threadID string) {
	if s == nil {
		return
	}
	resolved := strings.TrimSpace(threadID)
	if resolved == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threadID = resolved
	s.threadStartSent = false
}

func (s *codexRuntimeState) setActiveTurnID(turnID string) {
	if s == nil {
		return
	}
	resolved := strings.TrimSpace(turnID)
	if resolved == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeTurnID = resolved
}

func (s *codexRuntimeState) clearActiveTurnID(turnID string) {
	if s == nil {
		return
	}
	resolved := strings.TrimSpace(turnID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if resolved == "" || s.activeTurnID == resolved {
		s.activeTurnID = ""
	}
}

func (s *codexRuntimeState) snapshot() (initialized bool, threadID, activeTurnID string) {
	if s == nil {
		return false, "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initialized, s.threadID, s.activeTurnID
}
