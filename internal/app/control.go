package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"actrail/internal/domain/session"
)

// SessionController exposes command-side session control seams for HTTP and WebSocket wiring.
type SessionController interface {
	ListSessions(context.Context, ListSessionsRequest) (ListSessionsResponse, error)
	Send(context.Context, SendRequest) (SendResponse, error)
	Enqueue(context.Context, EnqueueRequest) (EnqueueResponse, error)
	CancelQueue(context.Context, CancelQueueRequest) (CancelQueueResponse, error)
	Interrupt(context.Context, InterruptRequest) (InterruptResponse, error)
	RespondUI(context.Context, UIResponseRequest) (UIResponseResponse, error)
	SessionState(context.Context, SessionStateRequest) (SessionStateResponse, error)
	SessionMessages(context.Context, SessionMessagesRequest) (SessionMessagesResponse, error)
}

// SessionUIRequestWriter exposes runtime-side UI request state mutation.
type SessionUIRequestWriter interface {
	SetSessionUIRequest(session.SessionID, SessionUIRequestSnapshot) error
	ClearSessionUIRequest(session.SessionID, string) error
}

type SendRequest struct {
	SessionID session.SessionID
	Text      string
}

type SendResponse struct {
	Message SessionMessage            `json:"message"`
	Busy    bool                      `json:"busy"`
	Queue   SessionQueueSnapshot      `json:"queue"`
	UI      *SessionUIRequestSnapshot `json:"ui_request,omitempty"`
}

type EnqueueRequest struct {
	SessionID session.SessionID
	Text      string
}

type EnqueueResponse struct {
	Busy  bool                 `json:"busy"`
	Queue SessionQueueSnapshot `json:"queue"`
}

type CancelQueueRequest struct {
	SessionID session.SessionID
}

type CancelQueueResponse struct {
	Busy  bool                 `json:"busy"`
	Queue SessionQueueSnapshot `json:"queue"`
}

type InterruptRequest struct {
	SessionID session.SessionID
}

type InterruptResponse struct {
	Busy  bool                 `json:"busy"`
	Queue SessionQueueSnapshot `json:"queue"`
}

type UIResponseRequest struct {
	SessionID  session.SessionID
	ResponseTo string
	Value      string
}

type UIResponseResponse struct {
	ResolvedRequestID string               `json:"resolved_request_id"`
	Busy              bool                 `json:"busy"`
	Queue             SessionQueueSnapshot `json:"queue"`
}

func (s *Stub) Send(ctx context.Context, req SendRequest) (SendResponse, error) {
	return s.send(ctx, req, false)
}

func (s *Stub) send(ctx context.Context, req SendRequest, followUp bool) (SendResponse, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return SendResponse{}, Invalid("text", "text required")
	}
	var response SendResponse
	var pollRuntime sessionRuntime
	var pollPIState bool
	var codexTurnWatchRuntime sessionRuntime
	watchCodexTurnStart := false
	if err := s.withSessionInputLock(req.SessionID, func(record sessionRecord) error {
		if s.activeWaitForSession(req.SessionID) != nil {
			return Conflict("session is waiting on user")
		}
		if err := transportControlError(sessionTransportSnapshot(record)); err != nil {
			return err
		}
		if err := s.prepareRuntimeSend(ctx, req.SessionID, record.runtime); err != nil {
			_ = s.transitionCodexRuntime(req.SessionID, codexRuntimePhaseFailed, "codex_prepare_send_failed", "prepare_send_failed")
			_ = s.emitRuntimeControlDiagnostic(req.SessionID, "prepare_send", err)
			return mapRuntimeControlError(err)
		}
		current, err := s.lookupSession(req.SessionID)
		if err != nil {
			return err
		}
		if !sameRuntime(record, current) {
			return errRuntimeChanged
		}
		sendRuntimePrompt := record.runtime.SendPromptWithStaleCheck
		if followUp {
			sendRuntimePrompt = func(ctx context.Context, text string, stale func() bool) error {
				if stale != nil && stale() {
					return errRuntimeChanged
				}
				return record.runtime.SendFollowUp(ctx, text)
			}
		}
		if record.runtime.protocol == runtimeProtocolCodexRPC {
			s.trackCodexOutboundPrompt(req.SessionID, text)
			defer s.clearCodexOutboundPrompt(req.SessionID, text)
		}
		if record.runtime.protocol == runtimeProtocolCodexRPC {
			_ = s.transitionCodexRuntime(req.SessionID, codexRuntimePhaseSending, "codex_sending", "send")
		}
		if err := sendRuntimePrompt(ctx, text, func() bool {
			current, err := s.lookupSession(req.SessionID)
			return err != nil || !sameRuntime(record, current)
		}); err != nil {
			_ = s.transitionCodexRuntime(req.SessionID, codexRuntimePhaseFailed, "codex_send_failed", "send_failed")
			_ = s.emitRuntimeControlDiagnostic(req.SessionID, "send", err)
			return mapRuntimeControlError(err)
		}
		if record.runtime.protocol == runtimeProtocolCodexRPC {
			_ = s.transitionCodexRuntime(req.SessionID, codexRuntimePhaseTurnStarting, "codex_turn_starting", "turn_starting")
			codexTurnWatchRuntime = record.runtime
			watchCodexTurnStart = true
		}
		busyOnSend := true
		if record.identity.Backend() == session.BackendPI {
			pollRuntime = record.runtime
			pollPIState = true
			if record.runtime.protocol == runtimeProtocolPIRPC && record.runtime.helper != nil {
				s.holdPIRPCBusy(req.SessionID, record.runtime.helper.generationID)
				s.kickPIRPCStateProbe(req.SessionID, record.runtime.helper.generationID)
			}
		}
		item, state, uiRequest, ok, err := s.registry.ActivateSendWithBusy(req.SessionID, text, busyOnSend)
		if err != nil {
			return err
		}
		if !ok {
			return NotFound(fmt.Sprintf("session %q not found", req.SessionID))
		}
		s.messageCache.Invalidate(req.SessionID)
		response = SendResponse{
			Message: sessionMessageFromCommitted(item),
			Busy:    state.Busy(),
			Queue:   queueSnapshotFromState(state),
			UI:      copySessionUIRequest(uiRequest),
		}
		return nil
	}); err != nil {
		return SendResponse{}, err
	}
	s.emitMessageCommit(req.SessionID, "", response.Message)
	s.emitQueueState(req.SessionID, response.Queue)
	s.emitSessionState(req.SessionID)
	if pollPIState {
		s.startPIRPCStatePolling(req.SessionID, pollRuntime)
	}
	if watchCodexTurnStart {
		s.startCodexTurnStartWatch(req.SessionID, codexTurnWatchRuntime)
	}
	return response, nil
}

func (s *Stub) Enqueue(_ context.Context, req EnqueueRequest) (EnqueueResponse, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return EnqueueResponse{}, Invalid("text", "text required")
	}
	var response EnqueueResponse
	shouldDispatch := false
	if err := s.withSessionInputLock(req.SessionID, func(record sessionRecord) error {
		if s.activeWaitForSession(req.SessionID) != nil {
			return Conflict("session is waiting on user")
		}
		state, ok, err := s.registry.ReplaceQueue(req.SessionID, text)
		if err != nil {
			return err
		}
		if !ok {
			return NotFound(fmt.Sprintf("session %q not found", req.SessionID))
		}
		updatedRecord := record
		updatedRecord.state = state
		busy, _ := effectiveBusy(updatedRecord)
		response = EnqueueResponse{Busy: busy, Queue: queueSnapshotFromState(state)}
		shouldDispatch = !busy && transportControlError(sessionTransportSnapshot(record)) == nil
		return nil
	}); err != nil {
		return EnqueueResponse{}, err
	}
	s.emitQueueState(req.SessionID, response.Queue)
	s.emitSessionState(req.SessionID)
	if shouldDispatch {
		s.scheduleQueuedDispatch(req.SessionID)
	}
	return response, nil
}

func (s *Stub) CancelQueue(_ context.Context, req CancelQueueRequest) (CancelQueueResponse, error) {
	var response CancelQueueResponse
	if err := s.withSessionInputLock(req.SessionID, func(sessionRecord) error {
		state, ok, err := s.registry.ClearQueue(req.SessionID)
		if err != nil {
			return err
		}
		if !ok {
			return NotFound(fmt.Sprintf("session %q not found", req.SessionID))
		}
		response = CancelQueueResponse{Busy: state.Busy(), Queue: queueSnapshotFromState(state)}
		return nil
	}); err != nil {
		return CancelQueueResponse{}, err
	}
	s.emitQueueState(req.SessionID, response.Queue)
	s.emitSessionState(req.SessionID)
	return response, nil
}

func (s *Stub) Interrupt(ctx context.Context, req InterruptRequest) (InterruptResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return InterruptResponse{}, err
	}
	if err := transportControlError(sessionTransportSnapshot(record)); err != nil {
		return InterruptResponse{}, err
	}
	if err := record.runtime.Interrupt(ctx); err != nil {
		_ = s.emitRuntimeControlDiagnostic(req.SessionID, "interrupt", err)
		return InterruptResponse{}, mapRuntimeControlError(err)
	}
	if record.identity.Backend() == session.BackendPI && record.runtime.protocol == runtimeProtocolPIRPC && record.runtime.helper != nil {
		s.holdPIRPCIdle(req.SessionID, record.runtime.helper.generationID)
	}
	if record.identity.Backend() == session.BackendCodex && record.runtime.protocol == runtimeProtocolCodexRPC {
		if err := s.syncCodexRuntimeActivity(req.SessionID, "interrupt", true); err != nil {
			return InterruptResponse{}, err
		}
		updated, ok := s.registry.Lookup(req.SessionID)
		if !ok {
			return InterruptResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
		}
		busy, _ := effectiveBusy(updated)
		return InterruptResponse{Busy: busy, Queue: queueSnapshotFromState(updated.state)}, nil
	}
	if err := s.setRuntimeAgentRunning(req.SessionID, false); err != nil {
		return InterruptResponse{}, err
	}
	if _, _, err := s.registry.DiscardPartialAssistantTurn(req.SessionID); err != nil {
		return InterruptResponse{}, err
	}
	state, ok, err := s.registry.SetBusy(req.SessionID, false)
	if err != nil {
		return InterruptResponse{}, err
	}
	if !ok {
		return InterruptResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	response := InterruptResponse{Busy: state.Busy(), Queue: queueSnapshotFromState(state)}
	s.emitQueueState(req.SessionID, response.Queue)
	s.emitSessionState(req.SessionID)
	if !state.Busy() {
		s.scheduleQueuedDispatch(req.SessionID)
	}
	return response, nil
}

func (s *Stub) RespondUI(ctx context.Context, req UIResponseRequest) (UIResponseResponse, error) {
	responseTo := strings.TrimSpace(req.ResponseTo)
	if responseTo == "" {
		return UIResponseResponse{}, Invalid("response_to", "response_to required")
	}
	value := strings.TrimSpace(req.Value)
	if value == "" {
		return UIResponseResponse{}, Invalid("value", "value required")
	}
	var response UIResponseResponse
	if err := s.withSessionInputLock(req.SessionID, func(record sessionRecord) error {
		if err := transportControlError(sessionTransportSnapshot(record)); err != nil {
			return err
		}
		if record.uiRequest == nil {
			return NotFound(fmt.Sprintf("session %q ui request not found", req.SessionID))
		}
		if record.uiRequest.RequestID != responseTo {
			return Conflict(fmt.Sprintf("session %q pending ui request is %q", req.SessionID, record.uiRequest.RequestID))
		}
		if err := record.runtime.RespondUI(ctx, responseTo, value); err != nil {
			_ = s.emitRuntimeControlDiagnostic(req.SessionID, "ui_response", err)
			return mapRuntimeControlError(err)
		}
		resolved, state, ok, err := s.registry.ClearUIRequest(req.SessionID, responseTo)
		if err != nil {
			if errors.Is(err, errNoPendingUIRequest) {
				return NotFound(fmt.Sprintf("session %q ui request not found", req.SessionID))
			}
			if errors.Is(err, errUnexpectedUIRequest) {
				return Conflict(fmt.Sprintf("session %q pending ui request does not match %q", req.SessionID, responseTo))
			}
			return err
		}
		if !ok {
			return NotFound(fmt.Sprintf("session %q not found", req.SessionID))
		}
		response = UIResponseResponse{
			ResolvedRequestID: resolved.RequestID,
			Busy:              state.Busy(),
			Queue:             queueSnapshotFromState(state),
		}
		return nil
	}); err != nil {
		return UIResponseResponse{}, err
	}
	s.emitUIResolved(req.SessionID, response.ResolvedRequestID)
	s.emitSessionState(req.SessionID)
	return response, nil
}

func (s *Stub) SetSessionUIRequest(sessionID session.SessionID, request SessionUIRequestSnapshot) error {
	_, ok, err := s.registry.SetUIRequest(sessionID, request)
	if err != nil {
		return err
	}
	if !ok {
		return NotFound(fmt.Sprintf("session %q not found", sessionID))
	}
	return nil
}

func (s *Stub) ClearSessionUIRequest(sessionID session.SessionID, requestID string) error {
	_, _, ok, err := s.registry.ClearUIRequest(sessionID, requestID)
	if err != nil {
		if errors.Is(err, errNoPendingUIRequest) {
			return NotFound(fmt.Sprintf("session %q ui request not found", sessionID))
		}
		if errors.Is(err, errUnexpectedUIRequest) {
			return Conflict(fmt.Sprintf("session %q pending ui request does not match %q", sessionID, strings.TrimSpace(requestID)))
		}
		return err
	}
	if !ok {
		return NotFound(fmt.Sprintf("session %q not found", sessionID))
	}
	return nil
}

const (
	codexRuntimeBootstrapTimeout = 2 * time.Second
	codexRuntimePollInterval     = 10 * time.Millisecond
)

func (s *Stub) prepareRuntimeSend(ctx context.Context, sessionID session.SessionID, runtime sessionRuntime) error {
	if runtime.protocol != runtimeProtocolCodexRPC {
		return nil
	}
	if runtime.codex == nil {
		return errRuntimeInputUnavailable
	}
	if _, threadID, _ := runtime.codex.snapshot(); strings.TrimSpace(threadID) == "" {
		_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseThreadStarting, "codex_thread_starting", "prepare_send")
	}
	if err := runtime.EnsureCodexThread(ctx); err != nil {
		return err
	}
	deadline := time.Now().Add(codexRuntimeBootstrapTimeout)
	for {
		_, threadID, _ := runtime.codex.snapshot()
		if strings.TrimSpace(threadID) != "" {
			record, ok := s.registry.Lookup(sessionID)
			if ok && record.state.Busy() && record.identity.Backend() == session.BackendCodex {
				activity := codexVisibleActivity(record)
				if activity.Phase == codexRuntimePhaseIdle && record.uiRequest == nil {
					_, _, _ = s.registry.SetBusy(sessionID, codexRegistryBusy(record, activity))
				}
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errRuntimeInputUnavailable
		}
		time.Sleep(codexRuntimePollInterval)
	}
}

func (s *Stub) startCodexThreadBootstrap(sessionID session.SessionID, runtime sessionRuntime) {
	if runtime.protocol != runtimeProtocolCodexRPC || !runtime.canWriteInput() {
		return
	}
	_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseInitializing, "codex_initializing", "thread_bootstrap")
	go func() {
		if err := runtime.EnsureCodexThread(context.Background()); err != nil {
			_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseFailed, "codex_thread_bootstrap_failed", "thread_bootstrap_failed")
			_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_thread_bootstrap", err)
		}
	}()
}

func (s *Stub) startCodexTurnStartWatch(sessionID session.SessionID, runtime sessionRuntime) {
	if s == nil || runtime.protocol != runtimeProtocolCodexRPC || runtime.codex == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), codexRuntimeBootstrapTimeout)
		defer cancel()
		if waitRuntimeTurnStart(ctx, runtime) {
			return
		}
		if !s.runtimeStillCurrent(sessionID, runtime) {
			return
		}
		_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseTurnStarting, "codex_turn_start_probe", "turn_start_watch_timeout")
		if err := runtime.RequestCodexThreadState(context.Background()); err != nil {
			_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_turn_start_probe", err)
		}
	}()
}

func (s *Stub) runtimeStillCurrent(sessionID session.SessionID, runtime sessionRuntime) bool {
	record, err := s.lookupSession(sessionID)
	if err != nil {
		return false
	}
	return sameRuntimeHandle(record.runtime, runtime)
}

func waitRuntimeTurnStart(ctx context.Context, runtime sessionRuntime) bool {
	if runtime.protocol != runtimeProtocolCodexRPC {
		return true
	}
	if runtime.codex == nil {
		return false
	}
	for {
		if _, _, turnID := runtime.codex.snapshot(); strings.TrimSpace(turnID) != "" {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		time.Sleep(codexRuntimePollInterval)
	}
}

func (s *Stub) emitRuntimeControlDiagnostic(sessionID session.SessionID, operation string, err error) error {
	if s == nil || err == nil {
		return nil
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "runtime control failed"
	}
	committed, appendErr := s.AppendSessionMessage(sessionID, "system", "pi_event", fmt.Sprintf("Runtime control %s failed: %s", operation, message))
	if appendErr != nil {
		return appendErr
	}
	committed.Role = ""
	committed.Type = "pi_event"
	committed.Summary = "Runtime control failed"
	committed.Details = map[string]any{
		"raw_type":  "runtime_control_diagnostic",
		"operation": operation,
		"error":     message,
	}
	s.emitMessageCommit(sessionID, "", committed)
	return nil
}

var errRuntimeChanged = errors.New("session runtime changed before send; retry with current session state")

func sameRuntime(a, b sessionRecord) bool {
	aRuntimeID, aOK := a.identity.RuntimeID()
	bRuntimeID, bOK := b.identity.RuntimeID()
	if aOK && bOK {
		return aRuntimeID == bRuntimeID
	}
	return sameRuntimeHandle(a.runtime, b.runtime)
}

func sameRuntimeHandle(a, b sessionRuntime) bool {
	return a.helper == b.helper && a.piAgentGRPC == b.piAgentGRPC && a.handle == b.handle && a.codex == b.codex
}

func mapRuntimeControlError(err error) error {
	if errors.Is(err, errRuntimeChanged) {
		return Conflict(errRuntimeChanged.Error())
	}
	if errors.Is(err, errRuntimeInputUnavailable) {
		return Conflict("session runtime input is unavailable")
	}
	return err
}

func transportControlError(transport SessionTransportSnapshot) error {
	if isRecoverableTransportProbeIssue(transport) {
		return nil
	}
	if transport.ResetRequired {
		message := "session transport reset is required"
		if transport.Reason != "" {
			message = fmt.Sprintf("session transport reset is required: %s", transport.Reason)
		}
		return &Error{Code: "transport_reset_required", Message: message}
	}
	if transport.State == SessionTransportStateStarting {
		return Conflict("session runtime is starting")
	}
	if transport.State == SessionTransportStateBroken && isRecoverableTransportProbeIssue(transport) {
		return nil
	}
	if transport.State == SessionTransportStateBroken {
		message := "session generation is broken"
		if transport.Reason != "" {
			message = fmt.Sprintf("session generation is broken: %s", transport.Reason)
		}
		return Conflict(message)
	}
	if transport.State == SessionTransportStateFailed {
		message := "session runtime failed to start"
		if transport.Reason != "" {
			message = fmt.Sprintf("session runtime failed to start: %s", transport.Reason)
		}
		return Conflict(message)
	}
	if transport.State == SessionTransportStateEnded {
		message := "session generation has ended"
		if transport.Reason != "" {
			message = fmt.Sprintf("session generation has ended: %s", transport.Reason)
		}
		return Conflict(message)
	}
	return nil
}
