package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"actrail/internal/domain/session"
)

// SessionController exposes command-side session control seams for HTTP and WebSocket wiring.
type SessionController interface {
	Send(context.Context, SendRequest) (SendResponse, error)
	Enqueue(context.Context, EnqueueRequest) (EnqueueResponse, error)
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
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SendResponse{}, err
	}
	if err := record.runtime.WriteInput(ctx, text); err != nil {
		return SendResponse{}, mapRuntimeControlError(err)
	}
	item, state, uiRequest, ok, err := s.registry.ActivateSend(req.SessionID, text)
	if err != nil {
		return SendResponse{}, err
	}
	if !ok {
		return SendResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	return SendResponse{
		Message: sessionMessageFromCommitted(item),
		Busy:    state.Busy(),
		Queue:   queueSnapshotFromState(state),
		UI:      copySessionUIRequest(uiRequest),
	}, nil
}

func (s *Stub) Enqueue(_ context.Context, req EnqueueRequest) (EnqueueResponse, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return EnqueueResponse{}, Invalid("text", "text required")
	}
	state, ok, err := s.registry.ReplaceQueue(req.SessionID, text)
	if err != nil {
		return EnqueueResponse{}, err
	}
	if !ok {
		return EnqueueResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	return EnqueueResponse{Busy: state.Busy(), Queue: queueSnapshotFromState(state)}, nil
}

func (s *Stub) Interrupt(ctx context.Context, req InterruptRequest) (InterruptResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return InterruptResponse{}, err
	}
	if err := record.runtime.Interrupt(ctx); err != nil {
		return InterruptResponse{}, mapRuntimeControlError(err)
	}
	state, ok, err := s.registry.SetBusy(req.SessionID, false)
	if err != nil {
		return InterruptResponse{}, err
	}
	if !ok {
		return InterruptResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	return InterruptResponse{Busy: state.Busy(), Queue: queueSnapshotFromState(state)}, nil
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
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return UIResponseResponse{}, err
	}
	if record.uiRequest == nil {
		return UIResponseResponse{}, NotFound(fmt.Sprintf("session %q ui request not found", req.SessionID))
	}
	if record.uiRequest.RequestID != responseTo {
		return UIResponseResponse{}, Conflict(fmt.Sprintf("session %q pending ui request is %q", req.SessionID, record.uiRequest.RequestID))
	}
	if err := record.runtime.WriteInput(ctx, value); err != nil {
		return UIResponseResponse{}, mapRuntimeControlError(err)
	}
	resolved, state, ok, err := s.registry.ClearUIRequest(req.SessionID, responseTo)
	if err != nil {
		if errors.Is(err, errNoPendingUIRequest) {
			return UIResponseResponse{}, NotFound(fmt.Sprintf("session %q ui request not found", req.SessionID))
		}
		if errors.Is(err, errUnexpectedUIRequest) {
			return UIResponseResponse{}, Conflict(fmt.Sprintf("session %q pending ui request does not match %q", req.SessionID, responseTo))
		}
		return UIResponseResponse{}, err
	}
	if !ok {
		return UIResponseResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	return UIResponseResponse{
		ResolvedRequestID: resolved.RequestID,
		Busy:              state.Busy(),
		Queue:             queueSnapshotFromState(state),
	}, nil
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

func mapRuntimeControlError(err error) error {
	if errors.Is(err, errRuntimeInputUnavailable) {
		return Conflict("session runtime input is unavailable")
	}
	return err
}
