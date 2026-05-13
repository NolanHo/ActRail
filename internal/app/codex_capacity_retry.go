package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

const codexCapacityRetryMaxAttempts = 3

var codexCapacityRetryDelays = []time.Duration{
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
}

var codexCapacityRetryDelayForAttempt = codexCapacityRetryDelay

type codexCapacityRetryState struct {
	Prompt    string
	Attempts  int
	Revision  uint64
	Scheduled bool
}

func codexErrorIsCapacity(event pi.Event) bool {
	if event.Error == nil {
		return false
	}
	if strings.TrimSpace(event.Error.Source) != "codex_app_server" {
		return false
	}
	return isCodexCapacityError(event.Error.Message)
}

func isCodexCapacityError(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	if strings.Contains(normalized, "selected model is at capacity") {
		return true
	}
	return strings.Contains(normalized, "model") &&
		strings.Contains(normalized, "capacity") &&
		strings.Contains(normalized, "try a different model")
}

func runtimeProjectionHasCodexCapacityError(projection runtimeProjection) bool {
	for _, event := range projection.events {
		if codexErrorIsCapacity(event) {
			return true
		}
	}
	return false
}

func codexCapacityErrorMessage(projection runtimeProjection) string {
	for _, event := range projection.events {
		if codexErrorIsCapacity(event) {
			return event.Error.Message
		}
	}
	return ""
}

func (s *Stub) trackCodexCapacityRetryPrompt(sessionID session.SessionID, text string) {
	if s == nil {
		return
	}
	prompt := strings.TrimSpace(text)
	if prompt == "" {
		return
	}
	s.codexRetryMu.Lock()
	defer s.codexRetryMu.Unlock()
	if s.codexCapacityRetry == nil {
		s.codexCapacityRetry = map[session.SessionID]codexCapacityRetryState{}
	}
	current := s.codexCapacityRetry[sessionID]
	current.Prompt = prompt
	current.Attempts = 0
	current.Revision++
	current.Scheduled = false
	s.codexCapacityRetry[sessionID] = current
}

func (s *Stub) clearCodexCapacityRetryPromptForSession(sessionID session.SessionID) {
	if s == nil {
		return
	}
	s.codexRetryMu.Lock()
	defer s.codexRetryMu.Unlock()
	if s.codexCapacityRetry != nil {
		delete(s.codexCapacityRetry, sessionID)
	}
}

func (s *Stub) scheduleCodexCapacityRetry(sessionID session.SessionID, reason string) (int, bool) {
	if s == nil {
		return 0, false
	}
	var (
		prompt   string
		attempt  int
		revision uint64
		delay    time.Duration
	)
	s.codexRetryMu.Lock()
	if s.codexCapacityRetry == nil {
		s.codexCapacityRetry = map[session.SessionID]codexCapacityRetryState{}
	}
	state := s.codexCapacityRetry[sessionID]
	if strings.TrimSpace(state.Prompt) == "" || state.Scheduled || state.Attempts >= codexCapacityRetryMaxAttempts {
		s.codexRetryMu.Unlock()
		return 0, false
	}
	state.Attempts++
	state.Revision++
	state.Scheduled = true
	prompt = state.Prompt
	attempt = state.Attempts
	revision = state.Revision
	delay = codexCapacityRetryDelayForAttempt(attempt)
	s.codexCapacityRetry[sessionID] = state
	s.codexRetryMu.Unlock()

	_ = s.emitCodexCapacityRetryDiagnostic(sessionID, attempt, reason)
	go s.runCodexCapacityRetryAfter(sessionID, prompt, revision, attempt, delay)
	return attempt, true
}

func codexCapacityRetryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return codexCapacityRetryDelays[0]
	}
	idx := attempt - 1
	if idx >= len(codexCapacityRetryDelays) {
		idx = len(codexCapacityRetryDelays) - 1
	}
	return codexCapacityRetryDelays[idx]
}

func (s *Stub) runCodexCapacityRetryAfter(sessionID session.SessionID, prompt string, revision uint64, attempt int, delay time.Duration) {
	if delay > 0 {
		time.Sleep(delay)
	}
	if !s.claimCodexCapacityRetry(sessionID, prompt, revision) {
		return
	}
	var (
		runtime               sessionRuntime
		watchCodexTurnStart   bool
		codexTurnWatchRuntime sessionRuntime
	)
	err := s.withSessionInputLock(sessionID, func(record sessionRecord) error {
		record.runtime = s.runtimeForRecord(record)
		if record.identity.Backend() != session.BackendCodex || record.runtime.protocol != runtimeProtocolCodexRPC {
			return nil
		}
		if !s.codexCapacityRetryPromptStillCurrent(sessionID, prompt) {
			return nil
		}
		if err := transportControlError(s.sessionTransportSnapshot(record)); err != nil {
			return err
		}
		if err := s.prepareRuntimeSend(context.Background(), sessionID, record.runtime); err != nil {
			return err
		}
		current, err := s.lookupSession(sessionID)
		if err != nil {
			return err
		}
		current.runtime = s.runtimeForRecord(current)
		if !sameRuntimeHandle(record.runtime, current.runtime) {
			return errRuntimeChanged
		}
		runtime = current.runtime
		s.trackCodexOutboundPrompt(sessionID, prompt)
		_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseSending, "codex_capacity_retry_sending", "capacity_retry_send")
		if err := runtime.SendPromptWithStaleCheck(context.Background(), prompt, func() bool {
			current, err := s.lookupSession(sessionID)
			if err == nil {
				current.runtime = s.runtimeForRecord(current)
			}
			return err != nil || !sameRuntimeHandle(runtime, current.runtime) || !s.codexCapacityRetryPromptStillCurrent(sessionID, prompt)
		}); err != nil {
			s.clearCodexOutboundPrompt(sessionID, prompt)
			return err
		}
		_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseTurnStarting, "codex_capacity_retry_turn_starting", "capacity_retry_turn_starting")
		codexTurnWatchRuntime = runtime
		watchCodexTurnStart = true
		return nil
	})
	if err != nil {
		if errors.Is(err, errRuntimeChanged) {
			_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_capacity_retry", err)
			return
		}
		_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseFailed, "codex_capacity_retry_failed", "capacity_retry_failed")
		_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_capacity_retry", err)
		return
	}
	if watchCodexTurnStart {
		s.emitSessionState(sessionID)
		s.startCodexTurnStartWatch(sessionID, codexTurnWatchRuntime)
		_ = attempt
	}
}

func (s *Stub) claimCodexCapacityRetry(sessionID session.SessionID, prompt string, revision uint64) bool {
	if s == nil {
		return false
	}
	s.codexRetryMu.Lock()
	defer s.codexRetryMu.Unlock()
	state := s.codexCapacityRetry[sessionID]
	if state.Revision != revision || strings.TrimSpace(state.Prompt) != strings.TrimSpace(prompt) || !state.Scheduled {
		return false
	}
	state.Scheduled = false
	s.codexCapacityRetry[sessionID] = state
	return true
}

func (s *Stub) codexCapacityRetryPromptStillCurrent(sessionID session.SessionID, prompt string) bool {
	if s == nil {
		return false
	}
	s.codexRetryMu.Lock()
	defer s.codexRetryMu.Unlock()
	state := s.codexCapacityRetry[sessionID]
	return strings.TrimSpace(state.Prompt) == strings.TrimSpace(prompt)
}

func (s *Stub) emitCodexCapacityRetryDiagnostic(sessionID session.SessionID, attempt int, reason string) error {
	if s == nil {
		return nil
	}
	message := fmt.Sprintf("Codex model is at capacity; retrying request (%d/%d)", attempt, codexCapacityRetryMaxAttempts)
	committed, err := s.AppendSessionMessage(sessionID, "system", "pi_event", message)
	if err != nil {
		return err
	}
	committed.Role = ""
	committed.Type = "pi_event"
	committed.Summary = "Codex capacity retrying"
	committed.Details = map[string]any{
		"raw_type": "codex_capacity_retry",
		"attempt":  attempt,
		"max":      codexCapacityRetryMaxAttempts,
		"reason":   strings.TrimSpace(reason),
	}
	s.emitMessageCommit(sessionID, "", committed)
	s.emitSessionState(sessionID)
	return nil
}
