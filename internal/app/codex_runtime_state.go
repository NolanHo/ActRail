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
	phase           codexRuntimePhase
	phaseReason     string
}

type codexRuntimePhase string

const (
	codexRuntimePhaseIdle           codexRuntimePhase = "idle"
	codexRuntimePhaseInitializing   codexRuntimePhase = "initializing"
	codexRuntimePhaseThreadStarting codexRuntimePhase = "thread_starting"
	codexRuntimePhaseSending        codexRuntimePhase = "sending"
	codexRuntimePhaseTurnStarting   codexRuntimePhase = "turn_starting"
	codexRuntimePhaseRunning        codexRuntimePhase = "running"
	codexRuntimePhaseWaitingUser    codexRuntimePhase = "waiting_user"
	codexRuntimePhaseFailed         codexRuntimePhase = "failed"
	codexRuntimePhaseEnded          codexRuntimePhase = "ended"
)

type codexRuntimeActivity struct {
	Phase  codexRuntimePhase
	Reason string
	Busy   bool
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
		s.setPhaseLocked(codexRuntimePhaseInitializing, "codex_initializing")
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
		s.setPhaseLocked(codexRuntimePhaseThreadStarting, "codex_thread_starting")
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
	s.setPhaseLocked(codexRuntimePhaseThreadStarting, "codex_thread_starting")
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
	if s.threadID == "" && s.phase == codexRuntimePhaseInitializing {
		s.setPhaseLocked(codexRuntimePhaseThreadStarting, "codex_thread_starting")
	}
}

func (s *codexRuntimeState) setThreadID(threadID string) (bool, bool) {
	if s == nil {
		return false, false
	}
	resolved := strings.TrimSpace(threadID)
	if resolved == "" {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threadID != "" && s.threadID != resolved {
		return false, false
	}
	changed := s.threadID != resolved || s.threadStartSent || phaseIsStarting(s.phase)
	s.threadID = resolved
	s.threadStartSent = false
	if s.activeTurnID == "" && phaseIsStarting(s.phase) {
		s.setPhaseLocked(codexRuntimePhaseIdle, "")
	}
	return true, changed
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
	s.setPhaseLocked(codexRuntimePhaseRunning, "codex_running")
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
		if phaseIsTurnActive(s.phase) {
			s.setPhaseLocked(codexRuntimePhaseIdle, "")
		}
	}
}

func (s *codexRuntimeState) transition(phase codexRuntimePhase, reason string) (codexRuntimeActivity, bool) {
	if s == nil {
		return codexRuntimeActivity{Phase: codexRuntimePhaseIdle}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.activityLocked()
	s.setPhaseLocked(phase, reason)
	after := s.activityLocked()
	return after, before != after
}

func (s *codexRuntimeState) applyProtocolBusy(busy bool) (codexRuntimeActivity, bool) {
	if busy {
		return s.transition(codexRuntimePhaseRunning, "codex_running")
	}
	return s.transition(codexRuntimePhaseIdle, "")
}

func (s *codexRuntimeState) activity() codexRuntimeActivity {
	if s == nil {
		return codexRuntimeActivity{Phase: codexRuntimePhaseIdle}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activityLocked()
}

func (s *codexRuntimeState) snapshot() (initialized bool, threadID, activeTurnID string) {
	if s == nil {
		return false, "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initialized, s.threadID, s.activeTurnID
}

func (s *codexRuntimeState) setPhaseLocked(phase codexRuntimePhase, reason string) {
	normalized := normalizeCodexRuntimePhase(phase)
	s.phase = normalized
	if normalized == codexRuntimePhaseIdle {
		s.phaseReason = ""
		return
	}
	s.phaseReason = strings.TrimSpace(reason)
	if s.phaseReason == "" {
		s.phaseReason = "codex_" + string(normalized)
	}
}

func (s *codexRuntimeState) activityLocked() codexRuntimeActivity {
	phase := normalizeCodexRuntimePhase(s.phase)
	reason := strings.TrimSpace(s.phaseReason)
	if reason == "" && codexRuntimePhaseBusy(phase) {
		reason = "codex_" + string(phase)
	}
	return codexRuntimeActivity{Phase: phase, Reason: reason, Busy: codexRuntimePhaseBusy(phase)}
}

func normalizeCodexRuntimePhase(phase codexRuntimePhase) codexRuntimePhase {
	switch phase {
	case codexRuntimePhaseInitializing,
		codexRuntimePhaseThreadStarting,
		codexRuntimePhaseSending,
		codexRuntimePhaseTurnStarting,
		codexRuntimePhaseRunning,
		codexRuntimePhaseWaitingUser,
		codexRuntimePhaseFailed,
		codexRuntimePhaseEnded:
		return phase
	default:
		return codexRuntimePhaseIdle
	}
}

func codexRuntimePhaseBusy(phase codexRuntimePhase) bool {
	switch normalizeCodexRuntimePhase(phase) {
	case codexRuntimePhaseInitializing,
		codexRuntimePhaseThreadStarting,
		codexRuntimePhaseSending,
		codexRuntimePhaseTurnStarting,
		codexRuntimePhaseRunning,
		codexRuntimePhaseWaitingUser:
		return true
	default:
		return false
	}
}

func phaseIsStarting(phase codexRuntimePhase) bool {
	switch normalizeCodexRuntimePhase(phase) {
	case codexRuntimePhaseInitializing, codexRuntimePhaseThreadStarting:
		return true
	default:
		return false
	}
}

func phaseIsTurnActive(phase codexRuntimePhase) bool {
	switch normalizeCodexRuntimePhase(phase) {
	case codexRuntimePhaseSending, codexRuntimePhaseTurnStarting, codexRuntimePhaseRunning:
		return true
	default:
		return false
	}
}
