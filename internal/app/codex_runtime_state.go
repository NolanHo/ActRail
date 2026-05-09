package app

import (
	"fmt"
	"strings"
	"sync"

	"actrail/internal/domain/session"
)

type codexRuntimeState struct {
	mu                  sync.Mutex
	requestSeq          uint64
	initialized         bool
	initializeSent      bool
	threadStartSent     bool
	threadResumeSent    bool
	resumeThreadID      string
	threadID            string
	activeTurnID        string
	interruptPending    bool
	interruptSentTurnID string
	phase               codexRuntimePhase
	phaseReason         string
}

type codexRuntimePhase string

const (
	codexRuntimePhaseIdle           codexRuntimePhase = "idle"
	codexRuntimePhaseInitializing   codexRuntimePhase = "initializing"
	codexRuntimePhaseThreadStarting codexRuntimePhase = "thread_starting"
	codexRuntimePhaseSending        codexRuntimePhase = "sending"
	codexRuntimePhaseTurnStarting   codexRuntimePhase = "turn_starting"
	codexRuntimePhaseRunning        codexRuntimePhase = "running"
	codexRuntimePhaseInterrupting   codexRuntimePhase = "interrupting"
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
	return newCodexRuntimeStateWithResumeThread(backend, "")
}

func newCodexRuntimeStateWithResumeThread(backend session.Backend, threadID string) *codexRuntimeState {
	if backend != session.BackendCodex {
		return nil
	}
	return &codexRuntimeState{resumeThreadID: strings.TrimSpace(threadID)}
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
		if s.phase != codexRuntimePhaseInitializing || strings.TrimSpace(s.phaseReason) != "codex_protocol_recovering" {
			s.setPhaseLocked(codexRuntimePhaseInitializing, "codex_initializing")
		}
		requests = append(requests, map[string]any{
			"method": "initialize",
			"id":     fmt.Sprintf("initialize-%d", s.requestSeq),
			"params": map[string]any{
				"clientInfo":   map[string]any{"name": "actrail", "version": "0"},
				"capabilities": nil,
			},
		})
	}
	if request := s.threadAttachRequestLocked(); request != nil {
		requests = append(requests, request)
	}
	return requests
}

func (s *codexRuntimeState) threadAttachRequestLocked() any {
	if !s.initialized || s.threadID != "" {
		return nil
	}
	if s.resumeThreadID != "" {
		if s.threadResumeSent {
			return nil
		}
		s.requestSeq++
		s.threadResumeSent = true
		s.setPhaseLocked(codexRuntimePhaseThreadStarting, "codex_thread_resuming")
		return map[string]any{
			"method": "thread/resume",
			"id":     fmt.Sprintf("thread-resume-%d", s.requestSeq),
			"params": map[string]any{
				"threadId":               s.resumeThreadID,
				"persistExtendedHistory": false,
			},
		}
	}
	if !s.threadStartSent {
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
	return nil
}

func (s *codexRuntimeState) threadStartRequest() any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadAttachRequestLocked()
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
		reason := "codex_thread_starting"
		if s.resumeThreadID != "" {
			reason = "codex_thread_resuming"
		}
		s.setPhaseLocked(codexRuntimePhaseThreadStarting, reason)
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
	changed := s.threadID != resolved || s.threadStartSent || s.threadResumeSent || phaseIsStarting(s.phase)
	s.threadID = resolved
	s.threadStartSent = false
	s.threadResumeSent = false
	if s.resumeThreadID == resolved {
		s.resumeThreadID = ""
	}
	if s.activeTurnID == "" && phaseIsStarting(s.phase) {
		s.setPhaseLocked(codexRuntimePhaseIdle, "")
	}
	return true, changed
}

func (s *codexRuntimeState) resetProtocolForResume(fallbackThreadID string) (codexRuntimeActivity, bool) {
	if s == nil {
		return codexRuntimeActivity{Phase: codexRuntimePhaseIdle}, false
	}
	resolved := strings.TrimSpace(fallbackThreadID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if resolved == "" {
		resolved = strings.TrimSpace(s.threadID)
	}
	if resolved == "" {
		resolved = strings.TrimSpace(s.resumeThreadID)
	}
	before := s.activityLocked()
	s.initialized = false
	s.initializeSent = false
	s.threadStartSent = false
	s.threadResumeSent = false
	s.threadID = ""
	s.activeTurnID = ""
	s.resumeThreadID = resolved
	s.setPhaseLocked(codexRuntimePhaseInitializing, "codex_protocol_recovering")
	after := s.activityLocked()
	return after, before != after || resolved != ""
}

func (s *codexRuntimeState) attachInitializedThread(threadID string) (bool, bool) {
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
	before := s.activityLocked()
	changed := !s.initialized ||
		s.initializeSent ||
		s.threadID != resolved ||
		s.threadStartSent ||
		s.threadResumeSent ||
		s.resumeThreadID != "" ||
		phaseIsStarting(s.phase)
	s.initialized = true
	s.initializeSent = false
	s.threadID = resolved
	s.threadStartSent = false
	s.threadResumeSent = false
	s.resumeThreadID = ""
	if s.activeTurnID == "" && phaseIsStarting(s.phase) {
		s.setPhaseLocked(codexRuntimePhaseIdle, "")
	}
	after := s.activityLocked()
	return true, changed || before != after
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
	if s.interruptPending {
		s.setPhaseLocked(codexRuntimePhaseInterrupting, "codex_interrupting")
		return
	}
	s.setPhaseLocked(codexRuntimePhaseRunning, "codex_running")
}

func (s *codexRuntimeState) clearActiveTurnID(turnID string) {
	if s == nil {
		return
	}
	resolved := strings.TrimSpace(turnID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruptPending {
		if resolved == "" || s.activeTurnID == "" || s.activeTurnID != resolved {
			s.setPhaseLocked(codexRuntimePhaseInterrupting, "codex_interrupting")
			return
		}
	}
	if resolved == "" || s.activeTurnID == "" || s.activeTurnID == resolved {
		s.activeTurnID = ""
		s.interruptPending = false
		s.interruptSentTurnID = ""
		if phaseIsTurnActive(s.phase) {
			s.setPhaseLocked(codexRuntimePhaseIdle, "")
		}
	}
}

func (s *codexRuntimeState) requestInterrupt() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interruptPending = true
	s.setPhaseLocked(codexRuntimePhaseInterrupting, "codex_interrupting")
}

func (s *codexRuntimeState) pendingInterruptCommand() (threadID, turnID string, ok bool) {
	if s == nil {
		return "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	threadID = strings.TrimSpace(s.threadID)
	turnID = strings.TrimSpace(s.activeTurnID)
	if !s.interruptPending || threadID == "" || turnID == "" || s.interruptSentTurnID == turnID {
		return "", "", false
	}
	return threadID, turnID, true
}

func (s *codexRuntimeState) markInterruptSent(turnID string) {
	if s == nil {
		return
	}
	resolved := strings.TrimSpace(turnID)
	if resolved == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruptPending && s.activeTurnID == resolved {
		s.interruptSentTurnID = resolved
	}
}

func (s *codexRuntimeState) pendingInterrupt() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interruptPending
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
	if s == nil {
		return codexRuntimeActivity{Phase: codexRuntimePhaseIdle}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.activityLocked()
	if busy {
		if s.interruptPending {
			s.setPhaseLocked(codexRuntimePhaseInterrupting, "codex_interrupting")
		} else {
			s.setPhaseLocked(codexRuntimePhaseRunning, "codex_running")
		}
	} else if s.interruptPending {
		s.setPhaseLocked(codexRuntimePhaseInterrupting, "codex_interrupting")
	} else {
		s.activeTurnID = ""
		s.interruptPending = false
		s.interruptSentTurnID = ""
		s.setPhaseLocked(codexRuntimePhaseIdle, "")
	}
	after := s.activityLocked()
	return after, before != after
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

func (s *codexRuntimeState) pendingResumeThreadID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threadID != "" {
		return ""
	}
	return s.resumeThreadID
}

func (s *codexRuntimeState) setPhaseLocked(phase codexRuntimePhase, reason string) {
	normalized := normalizeCodexRuntimePhase(phase)
	if s.interruptPending {
		switch normalized {
		case codexRuntimePhaseIdle, codexRuntimePhaseFailed, codexRuntimePhaseEnded:
			s.interruptPending = false
			s.interruptSentTurnID = ""
		case codexRuntimePhaseSending, codexRuntimePhaseTurnStarting, codexRuntimePhaseRunning:
			normalized = codexRuntimePhaseInterrupting
			if strings.TrimSpace(reason) == "" || strings.Contains(strings.TrimSpace(reason), "turn_start") || strings.Contains(strings.TrimSpace(reason), "sending") || strings.Contains(strings.TrimSpace(reason), "running") {
				reason = "codex_interrupting"
			}
		}
	}
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
		codexRuntimePhaseInterrupting,
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
		codexRuntimePhaseInterrupting,
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
	case codexRuntimePhaseSending, codexRuntimePhaseTurnStarting, codexRuntimePhaseRunning, codexRuntimePhaseInterrupting:
		return true
	default:
		return false
	}
}
