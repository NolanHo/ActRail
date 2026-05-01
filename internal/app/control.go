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
	Send(context.Context, SendRequest) (SendResponse, error)
	Enqueue(context.Context, EnqueueRequest) (EnqueueResponse, error)
	CancelQueue(context.Context, CancelQueueRequest) (CancelQueueResponse, error)
	Interrupt(context.Context, InterruptRequest) (InterruptResponse, error)
	RespondUI(context.Context, UIResponseRequest) (UIResponseResponse, error)
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
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return SendResponse{}, Invalid("text", "text required")
	}
	var response SendResponse
	var pollRuntime sessionRuntime
	var pollPIState bool
	if err := s.withSessionInputLock(req.SessionID, func(record sessionRecord) error {
		if s.activeWaitForSession(req.SessionID) != nil {
			return Conflict("session is waiting on user")
		}
		if err := transportControlError(sessionTransportSnapshot(record)); err != nil {
			return err
		}
		if err := s.prepareRuntimeSend(ctx, req.SessionID, record.runtime); err != nil {
			return mapRuntimeControlError(err)
		}
		if err := record.runtime.SendPrompt(ctx, text); err != nil {
			return mapRuntimeControlError(err)
		}
		busyOnSend := true
		if record.identity.Backend() == session.BackendPI {
			busyOnSend = record.state.Busy()
			pollRuntime = record.runtime
			pollPIState = true
		}
		s.awaitRuntimeTurnStart(ctx, record.runtime)
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
		response = EnqueueResponse{Busy: state.Busy(), Queue: queueSnapshotFromState(state)}
		shouldDispatch = !state.Busy() && transportControlError(sessionTransportSnapshot(record)) == nil
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
		return InterruptResponse{}, mapRuntimeControlError(err)
	}
	if err := s.setRuntimeAgentRunning(req.SessionID, false); err != nil {
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
	if err := runtime.EnsureCodexThread(ctx); err != nil {
		return err
	}
	deadline := time.Now().Add(codexRuntimeBootstrapTimeout)
	for {
		if _, threadID, _ := runtime.codex.snapshot(); strings.TrimSpace(threadID) != "" {
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

func (s *Stub) awaitRuntimeTurnStart(ctx context.Context, runtime sessionRuntime) {
	if runtime.protocol != runtimeProtocolCodexRPC {
		return
	}
	deadline := time.Now().Add(codexRuntimeBootstrapTimeout)
	for {
		if _, _, turnID := runtime.codex.snapshot(); strings.TrimSpace(turnID) != "" {
			return
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return
		}
		time.Sleep(codexRuntimePollInterval)
	}
}

func mapRuntimeControlError(err error) error {
	if errors.Is(err, errRuntimeInputUnavailable) {
		return Conflict("session runtime input is unavailable")
	}
	return err
}

func transportControlError(transport SessionTransportSnapshot) error {
	if transport.ResetRequired {
		message := "session transport reset is required"
		if transport.Reason != "" {
			message = fmt.Sprintf("session transport reset is required: %s", transport.Reason)
		}
		return &Error{Code: "transport_reset_required", Message: message}
	}
	if transport.State == SessionTransportStateBroken {
		message := "session generation is broken"
		if transport.Reason != "" {
			message = fmt.Sprintf("session generation is broken: %s", transport.Reason)
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
