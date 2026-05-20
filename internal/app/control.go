package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/message"
	"actrail/internal/domain/session"
	"go.opentelemetry.io/otel/attribute"
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

func (s *Stub) resolveCommandSessionID(routeID session.SessionID) (session.SessionID, error) {
	record, err := s.lookupSession(routeID)
	if err != nil {
		return "", err
	}
	return record.identity.SessionID(), nil
}

func (s *Stub) Send(ctx context.Context, req SendRequest) (SendResponse, error) {
	if err := s.waitRuntimeRestore(ctx); err != nil {
		return SendResponse{}, err
	}
	var err error
	req.SessionID, err = s.resolveCommandSessionID(req.SessionID)
	if err != nil {
		return SendResponse{}, err
	}
	response, err := s.send(ctx, req, false)
	if errors.Is(err, errRuntimeChanged) || isRuntimeChangedConflict(err) {
		return s.send(ctx, req, false)
	}
	return response, err
}

type sendLockedPrecondition func(sessionRecord) error

func (s *Stub) send(ctx context.Context, req SendRequest, followUp bool) (SendResponse, error) {
	return s.sendWithOptions(ctx, req, followUp, nil, true)
}

func (s *Stub) sendWithPrecondition(ctx context.Context, req SendRequest, followUp bool, precondition sendLockedPrecondition) (SendResponse, error) {
	return s.sendWithOptions(ctx, req, followUp, precondition, true)
}

func (s *Stub) sendWithOptions(ctx context.Context, req SendRequest, followUp bool, precondition sendLockedPrecondition, trackCapacityRetry bool) (SendResponse, error) {
	if !s.asyncSQLiteActions {
		return s.sendWithOptionsSync(ctx, req, followUp, precondition, trackCapacityRetry)
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return SendResponse{}, Invalid("text", "text required")
	}
	var response SendResponse
	var runtime sessionRuntime
	var recordAtCommit sessionRecord
	var expectedRuntimeID session.RuntimeID
	if err := s.withSessionInputLock(req.SessionID, func(record sessionRecord) error {
		record.runtime = s.runtimeForRecord(record)
		if reconciled, ok := s.reconcileClearedCodexMissingChildTransport(record); ok {
			record = reconciled
		}
		if reconciled, ok := s.reconcileLiveCodexAttachLostTransport(record); ok {
			record = reconciled
		}
		if precondition != nil {
			if err := precondition(record); err != nil {
				return err
			}
		}
		if s.activeWaitForSession(req.SessionID) != nil {
			return Conflict("session is waiting on user")
		}
		if err := transportControlError(s.sessionTransportSnapshot(record)); err != nil {
			return err
		}
		if err := s.sendIdlePrecondition(ctx, record); err != nil {
			return err
		}
		if err := preflightRuntimeSend(record.runtime); err != nil {
			return mapRuntimeControlError(err)
		}
		if record.runtime.protocol == runtimeProtocolCodexRPC {
			active, err := s.codexAuthoritativeActiveTurn(ctx, record)
			if err != nil {
				_ = s.emitRuntimeControlDiagnostic(req.SessionID, "pre_send_codex_state", err)
			}
			if active {
				_ = s.transitionCodexRuntime(req.SessionID, codexRuntimePhaseRunning, "codex_authoritative_running", "pre_send_codex_state")
				return Conflict("codex runtime is still running; use queue or interrupt")
			}
		}
		runtime = record.runtime
		recordAtCommit = record
		expectedRuntimeID, _ = record.identity.RuntimeID()
		busyOnSend := true
		var (
			item      message.CommittedMessage
			state     session.State
			uiRequest *SessionUIRequestSnapshot
			ok        bool
			err       error
		)
		if record.identity.Backend() == session.BackendCodex {
			item, state, uiRequest, ok, err = s.registry.ActivateCodexSendWithCommand(req.SessionID, text, busyOnSend, expectedRuntimeID)
		} else {
			item, state, uiRequest, ok, err = s.registry.ActivateSendWithBusy(req.SessionID, text, busyOnSend)
		}
		if err != nil {
			return err
		}
		if !ok {
			return NotFound(fmt.Sprintf("session %q not found", req.SessionID))
		}
		s.invalidateSessionHistoryCaches(req.SessionID)
		response = SendResponse{
			Message: sessionMessageFromCommitted(item),
			Busy:    state.Busy(),
			Queue:   queueSnapshotFromState(state),
			UI:      copySessionUIRequest(uiRequest),
		}
		if record.identity.Backend() == session.BackendCodex {
			s.trackCodexOutboundPrompt(req.SessionID, text)
			if trackCapacityRetry {
				s.trackCodexCapacityRetryPrompt(req.SessionID, text)
			}
			_ = s.transitionCodexRuntime(req.SessionID, codexRuntimePhaseSending, "codex_sending", "send")
		}
		return nil
	}); err != nil {
		return SendResponse{}, err
	}
	s.emitMessageCommit(req.SessionID, "", response.Message)
	s.emitQueueState(req.SessionID, response.Queue)
	s.emitSessionState(req.SessionID)
	commandID := ""
	if recordAtCommit.identity.Backend() == session.BackendCodex {
		commandID = codexSendCommandID(recordAtCommit.identity.SessionID(), response.Message.Seq)
		s.recordCodexReducerEvent(req.SessionID, codexReducerSourceCommandLedger, "command_created",
			attribute.String("codex.command.state", codexCommandPending.String()),
			attribute.Bool("codex.command.follow_up", followUp),
		)
	}
	s.startAsyncRuntimeSend(req.SessionID, expectedRuntimeID, commandID, text, followUp, runtime, recordAtCommit)
	return response, nil
}

func (s *Stub) sendWithOptionsSync(ctx context.Context, req SendRequest, followUp bool, precondition sendLockedPrecondition, trackCapacityRetry bool) (SendResponse, error) {
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
		record.runtime = s.runtimeForRecord(record)
		if reconciled, ok := s.reconcileClearedCodexMissingChildTransport(record); ok {
			record = reconciled
		}
		if reconciled, ok := s.reconcileLiveCodexAttachLostTransport(record); ok {
			record = reconciled
		}
		if precondition != nil {
			if err := precondition(record); err != nil {
				return err
			}
		}
		if s.activeWaitForSession(req.SessionID) != nil {
			return Conflict("session is waiting on user")
		}
		if err := transportControlError(s.sessionTransportSnapshot(record)); err != nil {
			return err
		}
		if err := s.sendIdlePrecondition(ctx, record); err != nil {
			return err
		}
		if err := s.prepareRuntimeSend(ctx, req.SessionID, record.runtime); err != nil {
			if errors.Is(err, errRuntimeChanged) {
				return err
			}
			_ = s.transitionCodexRuntime(req.SessionID, codexRuntimePhaseFailed, "codex_prepare_send_failed", "prepare_send_failed")
			_ = s.emitRuntimeControlDiagnostic(req.SessionID, "prepare_send", err)
			return mapRuntimeControlError(err)
		}
		current, err := s.lookupSession(req.SessionID)
		if err != nil {
			return err
		}
		current.runtime = s.runtimeForRecord(current)
		if !sameRuntimeHandle(record.runtime, current.runtime) {
			return mapRuntimeControlError(errRuntimeChanged)
		}
		runtime := current.runtime
		sendRuntimePrompt := runtime.SendPromptWithStaleCheck
		if followUp {
			sendRuntimePrompt = func(ctx context.Context, text string, stale func() bool) error {
				if stale != nil && stale() {
					return errRuntimeChanged
				}
				return runtime.SendFollowUp(ctx, text)
			}
		}
		if runtime.protocol == runtimeProtocolCodexRPC {
			active, err := s.codexAuthoritativeActiveTurn(ctx, current)
			if err != nil {
				_ = s.emitRuntimeControlDiagnostic(req.SessionID, "pre_send_codex_state", err)
			}
			if active {
				_ = s.transitionCodexRuntime(req.SessionID, codexRuntimePhaseRunning, "codex_authoritative_running", "pre_send_codex_state")
				return Conflict("codex runtime is still running; use queue or interrupt")
			}
		}
		if runtime.protocol == runtimeProtocolCodexRPC {
			s.trackCodexOutboundPrompt(req.SessionID, text)
			if trackCapacityRetry {
				s.trackCodexCapacityRetryPrompt(req.SessionID, text)
			}
		}
		if runtime.protocol == runtimeProtocolCodexRPC {
			_ = s.transitionCodexRuntime(req.SessionID, codexRuntimePhaseSending, "codex_sending", "send")
		}
		if err := sendRuntimePrompt(ctx, text, func() bool {
			current, err := s.lookupSession(req.SessionID)
			if err == nil {
				current.runtime = s.runtimeForRecord(current)
			}
			return err != nil || !sameRuntimeHandle(runtime, current.runtime)
		}); err != nil {
			if errors.Is(err, errRuntimeChanged) {
				return err
			}
			_ = s.transitionCodexRuntime(req.SessionID, codexRuntimePhaseFailed, "codex_send_failed", "send_failed")
			_ = s.emitRuntimeControlDiagnostic(req.SessionID, "send", err)
			if runtime.protocol == runtimeProtocolCodexRPC {
				s.clearCodexOutboundPrompt(req.SessionID, text)
			}
			return mapRuntimeControlError(err)
		}
		if runtime.protocol == runtimeProtocolCodexRPC {
			_ = s.transitionCodexRuntime(req.SessionID, codexRuntimePhaseTurnStarting, "codex_turn_starting", "turn_starting")
			codexTurnWatchRuntime = runtime
			watchCodexTurnStart = true
		}
		busyOnSend := true
		if record.identity.Backend() == session.BackendPI {
			pollRuntime = runtime
			pollPIState = true
			if runtime.protocol == runtimeProtocolPIRPC && runtime.helper != nil {
				s.holdPIRPCBusy(req.SessionID, runtime.helper.generationID)
				s.kickPIRPCStateProbe(req.SessionID, runtime.helper.generationID)
			}
		}
		item, state, uiRequest, ok, err := s.registry.ActivateSendWithBusy(req.SessionID, text, busyOnSend)
		if err != nil {
			return err
		}
		if !ok {
			return NotFound(fmt.Sprintf("session %q not found", req.SessionID))
		}
		s.invalidateSessionHistoryCaches(req.SessionID)
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

func (s *Stub) startAsyncRuntimeSend(sessionID session.SessionID, expectedRuntimeID session.RuntimeID, commandID string, text string, followUp bool, runtime sessionRuntime, record sessionRecord) {
	if s == nil {
		return
	}
	go s.finishAsyncRuntimeSend(sessionID, expectedRuntimeID, commandID, text, followUp, runtime, record)
}

func (s *Stub) finishAsyncRuntimeSend(sessionID session.SessionID, expectedRuntimeID session.RuntimeID, commandID string, text string, followUp bool, runtime sessionRuntime, record sessionRecord) {
	s.updateCodexSendCommandState(commandID, codexCommandDispatching, expectedRuntimeID, "")
	sendRuntimePrompt := runtime.SendPromptWithStaleCheck
	if followUp {
		sendRuntimePrompt = func(ctx context.Context, text string, stale func() bool) error {
			if stale != nil && stale() {
				return errRuntimeChanged
			}
			return runtime.SendFollowUp(ctx, text)
		}
	}
	if err := s.withSessionInputLock(sessionID, func(current sessionRecord) error {
		current.runtime = s.runtimeForRecord(current)
		currentRuntimeID, ok := current.identity.RuntimeID()
		if expectedRuntimeID != "" && (!ok || currentRuntimeID != expectedRuntimeID) {
			return errRuntimeChanged
		}
		if !sameRuntimeHandle(runtime, current.runtime) {
			return errRuntimeChanged
		}
		if err := s.prepareRuntimeSend(context.Background(), sessionID, runtime); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if errors.Is(err, errRuntimeChanged) {
			s.retryAsyncRuntimeSendWithCurrent(sessionID, expectedRuntimeID, commandID, text, followUp)
			return
		}
		s.handleAsyncRuntimeSendError(sessionID, expectedRuntimeID, commandID, text, runtime, record, err)
		return
	}
	if err := sendRuntimePrompt(context.Background(), text, func() bool {
		current, err := s.lookupSession(sessionID)
		if err == nil {
			current.runtime = s.runtimeForRecord(current)
		}
		if err != nil || !sameRuntimeHandle(runtime, current.runtime) {
			return true
		}
		if expectedRuntimeID == "" {
			return false
		}
		currentRuntimeID, ok := current.identity.RuntimeID()
		return !ok || currentRuntimeID != expectedRuntimeID
	}); err != nil {
		if errors.Is(err, errRuntimeChanged) {
			s.retryAsyncRuntimeSendWithCurrent(sessionID, expectedRuntimeID, commandID, text, followUp)
			return
		}
		s.handleAsyncRuntimeSendError(sessionID, expectedRuntimeID, commandID, text, runtime, record, err)
		return
	}
	s.updateCodexSendCommandState(commandID, codexCommandAccepted, expectedRuntimeID, "")
	if expectedRuntimeID != "" {
		if current, ok := s.registry.Lookup(sessionID); ok {
			currentRuntimeID, ok := current.identity.RuntimeID()
			if !ok || currentRuntimeID != expectedRuntimeID {
				return
			}
		}
	}
	if runtime.protocol == runtimeProtocolCodexRPC {
		_ = s.transitionCodexRuntimeIfCurrent(sessionID, expectedRuntimeID, codexRuntimePhaseTurnStarting, "codex_turn_starting", "turn_starting")
		s.startCodexTurnStartWatch(sessionID, runtime)
	}
	if record.identity.Backend() == session.BackendPI {
		if runtime.protocol == runtimeProtocolPIRPC && runtime.helper != nil {
			s.holdPIRPCBusy(sessionID, runtime.helper.generationID)
			s.kickPIRPCStateProbe(sessionID, runtime.helper.generationID)
		}
		s.startPIRPCStatePolling(sessionID, runtime)
	}
	s.emitSessionState(sessionID)
}

func (s *Stub) retryAsyncRuntimeSendWithCurrent(sessionID session.SessionID, expectedRuntimeID session.RuntimeID, commandID string, text string, followUp bool) {
	s.updateCodexSendCommandState(commandID, codexCommandDispatching, expectedRuntimeID, "")
	var runtime sessionRuntime
	var record sessionRecord
	if err := s.withSessionInputLock(sessionID, func(current sessionRecord) error {
		current.runtime = s.runtimeForRecord(current)
		currentRuntimeID, ok := current.identity.RuntimeID()
		if expectedRuntimeID != "" && (!ok || currentRuntimeID != expectedRuntimeID) {
			return errRuntimeChanged
		}
		if err := preflightRuntimeSend(current.runtime); err != nil {
			return err
		}
		if current.runtime.protocol == runtimeProtocolCodexRPC {
			active, err := s.codexAuthoritativeActiveTurn(context.Background(), current)
			if err != nil {
				_ = s.emitRuntimeControlDiagnostic(sessionID, "async_retry_pre_send_codex_state", err)
			}
			if active {
				_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseRunning, "codex_authoritative_running", "async_retry_pre_send_codex_state")
				return Conflict("codex runtime is still running; use queue or interrupt")
			}
		}
		if err := s.prepareRuntimeSend(context.Background(), sessionID, current.runtime); err != nil {
			return err
		}
		runtime = current.runtime
		record = current
		return nil
	}); err != nil {
		if errors.Is(err, errRuntimeChanged) {
			_ = s.emitRuntimeControlDiagnostic(sessionID, "async_retry_send", err)
			return
		}
		s.handleAsyncRuntimeSendError(sessionID, expectedRuntimeID, commandID, text, runtime, record, err)
		return
	}
	sendRuntimePrompt := runtime.SendPromptWithStaleCheck
	if followUp {
		sendRuntimePrompt = func(ctx context.Context, text string, stale func() bool) error {
			if stale != nil && stale() {
				return errRuntimeChanged
			}
			return runtime.SendFollowUp(ctx, text)
		}
	}
	if err := sendRuntimePrompt(context.Background(), text, func() bool {
		current, err := s.lookupSession(sessionID)
		if err == nil {
			current.runtime = s.runtimeForRecord(current)
		}
		if err != nil || !sameRuntimeHandle(runtime, current.runtime) {
			return true
		}
		if expectedRuntimeID == "" {
			return false
		}
		currentRuntimeID, ok := current.identity.RuntimeID()
		return !ok || currentRuntimeID != expectedRuntimeID
	}); err != nil {
		s.handleAsyncRuntimeSendError(sessionID, expectedRuntimeID, commandID, text, runtime, record, err)
		return
	}
	s.updateCodexSendCommandState(commandID, codexCommandAccepted, expectedRuntimeID, "")
	if runtime.protocol == runtimeProtocolCodexRPC {
		_ = s.transitionCodexRuntimeIfCurrent(sessionID, expectedRuntimeID, codexRuntimePhaseTurnStarting, "codex_turn_starting", "turn_starting")
		s.startCodexTurnStartWatch(sessionID, runtime)
	}
	if record.identity.Backend() == session.BackendPI {
		if runtime.protocol == runtimeProtocolPIRPC && runtime.helper != nil {
			s.holdPIRPCBusy(sessionID, runtime.helper.generationID)
			s.kickPIRPCStateProbe(sessionID, runtime.helper.generationID)
		}
		s.startPIRPCStatePolling(sessionID, runtime)
	}
	s.emitSessionState(sessionID)
}

func (s *Stub) handleAsyncRuntimeSendError(sessionID session.SessionID, expectedRuntimeID session.RuntimeID, commandID string, text string, runtime sessionRuntime, record sessionRecord, err error) {
	if s == nil || err == nil {
		return
	}
	if expectedRuntimeID != "" {
		if current, ok := s.registry.Lookup(sessionID); ok {
			currentRuntimeID, ok := current.identity.RuntimeID()
			if !ok || currentRuntimeID != expectedRuntimeID {
				return
			}
		}
	}
	if record.identity.Backend() == session.BackendCodex || runtime.protocol == runtimeProtocolCodexRPC {
		s.updateCodexSendCommandState(commandID, codexCommandFailed, expectedRuntimeID, err.Error())
		_ = s.transitionCodexRuntimeIfCurrent(sessionID, expectedRuntimeID, codexRuntimePhaseFailed, "codex_send_failed", "send_failed")
		s.clearCodexOutboundPrompt(sessionID, text)
	}
	_, _, _ = s.registry.SetBusyIfCurrent(sessionID, expectedRuntimeID, false)
	_ = s.emitRuntimeControlDiagnostic(sessionID, "send", err)
	s.emitSessionState(sessionID)
}

func (s *Stub) updateCodexSendCommandState(commandID string, state codexCommandAxis, runtimeID session.RuntimeID, lastError string) bool {
	if s == nil || s.sessionCommandStore == nil || strings.TrimSpace(commandID) == "" {
		return false
	}
	updated, _ := s.sessionCommandStore.UpdateCodexSessionCommandState(context.Background(), commandID, state.String(), runtimeID.String(), lastError, time.Now().UTC())
	if sessionID, ok := codexSendCommandSessionID(commandID); ok {
		s.recordCodexReducerEvent(sessionID, codexReducerSourceCommandLedger, "command_state",
			codexReducerBool(updated),
			attribute.String("codex.command.state", state.String()),
			attribute.Bool("codex.command.has_error", strings.TrimSpace(lastError) != ""),
		)
	}
	return updated
}

func (s *Stub) sendIdlePrecondition(ctx context.Context, record sessionRecord) error {
	if busy, reason := effectiveBusy(record); busy {
		if record.identity.Backend() == session.BackendCodex && reason == "codex_authoritative_running" {
			active, err := s.confirmCodexRuntimeActiveTurn(ctx, record)
			if err != nil {
				_ = s.emitRuntimeControlDiagnostic(record.identity.SessionID(), "pre_send_codex_state", err)
			}
			if !active {
				return nil
			}
		}
		if reason != "" {
			return Conflict("session is busy: " + reason)
		}
		return Conflict("session is busy")
	}
	if record.state.Queue().Len() > 0 {
		return Conflict("session has queued prompts")
	}
	if record.uiRequest != nil {
		return Conflict("session has unresolved UI request")
	}
	return nil
}

func (s *Stub) Enqueue(_ context.Context, req EnqueueRequest) (EnqueueResponse, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return EnqueueResponse{}, Invalid("text", "text required")
	}
	var err error
	req.SessionID, err = s.resolveCommandSessionID(req.SessionID)
	if err != nil {
		return EnqueueResponse{}, err
	}
	var response EnqueueResponse
	if err := s.withSessionInputLock(req.SessionID, func(record sessionRecord) error {
		if s.activeWaitForSession(req.SessionID) != nil {
			return Conflict("session is waiting on user")
		}
		if _, ok := s.registry.Lookup(req.SessionID); !ok {
			return NotFound(fmt.Sprintf("session %q not found", req.SessionID))
		}
		if err := s.replaceManualInboxItem(context.Background(), req.SessionID, text); err != nil {
			return err
		}
		busy, _ := effectiveBusy(record)
		response = EnqueueResponse{Busy: busy, Queue: queueSnapshotFromState(record.state)}
		return nil
	}); err != nil {
		return EnqueueResponse{}, err
	}
	s.emitQueueState(req.SessionID, response.Queue)
	s.emitSessionState(req.SessionID)
	return response, nil
}

func (s *Stub) CancelQueue(_ context.Context, req CancelQueueRequest) (CancelQueueResponse, error) {
	var err error
	req.SessionID, err = s.resolveCommandSessionID(req.SessionID)
	if err != nil {
		return CancelQueueResponse{}, err
	}
	var response CancelQueueResponse
	if err := s.withSessionInputLock(req.SessionID, func(sessionRecord) error {
		state, ok, err := s.registry.ClearQueue(req.SessionID)
		if err != nil {
			return err
		}
		if !ok {
			return NotFound(fmt.Sprintf("session %q not found", req.SessionID))
		}
		if err := s.finishManualInboxMirror(context.Background(), req.SessionID, "", "cancelled", ""); err != nil {
			return err
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

func (s *Stub) replaceManualInboxItem(ctx context.Context, sessionID session.SessionID, text string) error {
	if s == nil || s.schedulerStore == nil {
		return nil
	}
	now := s.registry.now().UTC()
	openItems, err := s.schedulerStore.ListInboxItems(ctx, sessionID.String(), 500)
	if err != nil {
		return err
	}
	for _, item := range openItems {
		if item.Source != schedulerInboxSourceManual || (item.State != "pending" && item.State != "claimed") {
			continue
		}
		item.Title = "Manual follow-up"
		item.Message = strings.TrimSpace(text)
		item.DueAt = now
		item.State = "pending"
		item.BlockedReason = ""
		item.Error = ""
		item.ClaimedAt = nil
		item.DeliveredAt = nil
		item.UpdatedAt = now
		return s.schedulerStore.UpdateInboxItem(ctx, item)
	}
	return s.schedulerStore.InsertInboxItem(ctx, sqlitestore.InboxItemRow{
		ItemID:    newID("inbox"),
		SessionID: sessionID.String(),
		Source:    schedulerInboxSourceManual,
		SourceID:  "composer",
		Title:     "Manual follow-up",
		Message:   strings.TrimSpace(text),
		DueAt:     now,
		State:     "pending",
		CreatedAt: now,
		UpdatedAt: now,
	})
}

func (s *Stub) finishManualInboxMirror(ctx context.Context, sessionID session.SessionID, sourceID string, state string, deliveredMessageID string) error {
	if s == nil || s.schedulerStore == nil {
		return nil
	}
	now := s.registry.now().UTC()
	items, err := s.schedulerStore.ListInboxItems(ctx, sessionID.String(), 500)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Source != schedulerInboxSourceManual || (item.State != "pending" && item.State != "claimed") {
			continue
		}
		if sourceID != "" && item.SourceID != sourceID {
			continue
		}
		item.State = state
		item.UpdatedAt = now
		item.Error = ""
		if deliveredMessageID != "" {
			deliveredAt := now
			item.DeliveredMessageID = deliveredMessageID
			item.DeliveredAt = &deliveredAt
		}
		if err := s.schedulerStore.UpdateInboxItem(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stub) Interrupt(ctx context.Context, req InterruptRequest) (InterruptResponse, error) {
	var err error
	req.SessionID, err = s.resolveCommandSessionID(req.SessionID)
	if err != nil {
		return InterruptResponse{}, err
	}
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return InterruptResponse{}, err
	}
	record.runtime = s.runtimeForRecord(record)
	if err := transportControlError(s.sessionTransportSnapshot(record)); err != nil {
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
		s.invalidateSessionHistoryCaches(req.SessionID)
		if err := s.syncCodexRuntimeActivity(req.SessionID, "interrupt", true); err != nil {
			return InterruptResponse{}, err
		}
		s.startCodexStaleInterruptWatch(req.SessionID, record.runtime)
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
	var err error
	req.SessionID, err = s.resolveCommandSessionID(req.SessionID)
	if err != nil {
		return UIResponseResponse{}, err
	}
	var response UIResponseResponse
	if err := s.withSessionInputLock(req.SessionID, func(record sessionRecord) error {
		record.runtime = s.runtimeForRecord(record)
		if err := transportControlError(s.sessionTransportSnapshot(record)); err != nil {
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
	codexRuntimeBootstrapTimeout  = 2 * time.Second
	codexTurnCompletionProbeDelay = 1500 * time.Millisecond
	codexRuntimePollInterval      = 10 * time.Millisecond
)

var codexStaleInterruptWatchDelay = 10 * time.Minute

func preflightRuntimeSend(runtime sessionRuntime) error {
	switch runtime.protocol {
	case runtimeProtocolTTY:
		if !runtime.canWriteInput() {
			return errRuntimeInputUnavailable
		}
	case runtimeProtocolPIRPC:
		if runtime.piAgentGRPC == nil && !runtime.canWriteInput() {
			return errRuntimeInputUnavailable
		}
	case runtimeProtocolCodexRPC:
		if runtime.codex == nil || !runtime.canWriteInput() {
			return errRuntimeInputUnavailable
		}
	}
	return nil
}

func (s *Stub) prepareRuntimeSend(ctx context.Context, sessionID session.SessionID, runtime sessionRuntime) error {
	if runtime.protocol != runtimeProtocolCodexRPC {
		return nil
	}
	if runtime.codex == nil {
		return errRuntimeInputUnavailable
	}
	if _, threadID, _ := runtime.codex.snapshot(); strings.TrimSpace(threadID) == "" {
		reason := "codex_thread_starting"
		if runtime.PendingCodexResumeThreadID() != "" {
			reason = "codex_thread_resuming"
		}
		_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseThreadStarting, reason, "prepare_send")
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
			if s.seedCodexRuntimeThreadFromRecord(sessionID, runtime) {
				continue
			}
			return errRuntimeInputUnavailable
		}
		time.Sleep(codexRuntimePollInterval)
	}
}

func (s *Stub) seedCodexRuntimeThreadFromRecord(sessionID session.SessionID, runtime sessionRuntime) bool {
	if s == nil || runtime.protocol != runtimeProtocolCodexRPC || runtime.codex == nil {
		return false
	}
	_, beforeThreadID, _ := runtime.codex.snapshot()
	if strings.TrimSpace(beforeThreadID) != "" {
		return true
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return false
	}
	threadID := strings.TrimSpace(record.importedBackendSessionID)
	if threadID == "" {
		threadID = runtime.PendingCodexResumeThreadID()
	}
	if threadID == "" {
		return false
	}
	runtime.codex.attachInitializedThread(threadID)
	s.noteCodexThreadID(sessionID, threadID, record.importedSourcePath)
	_, afterThreadID, _ := runtime.codex.snapshot()
	return strings.TrimSpace(afterThreadID) != ""
}

func (s *Stub) startCodexThreadBootstrap(sessionID session.SessionID, runtime sessionRuntime) {
	if runtime.protocol != runtimeProtocolCodexRPC || !runtime.canWriteInput() {
		return
	}
	if runtime.attachedExistingIOD {
		return
	}
	reason := "codex_initializing"
	if runtime.PendingCodexResumeThreadID() != "" {
		reason = "codex_thread_resuming"
	}
	_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseInitializing, reason, "thread_bootstrap")
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
			if !errors.Is(err, errCodexThreadNotReady) {
				_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_turn_start_probe", err)
			}
		}
	}()
}

type codexInterruptWatchSnapshot struct {
	threadID    string
	turnID      string
	progressSeq uint64
}

func (s *Stub) startCodexStaleInterruptWatch(sessionID session.SessionID, runtime sessionRuntime) {
	if s == nil || runtime.protocol != runtimeProtocolCodexRPC || runtime.codex == nil {
		return
	}
	threadID, turnID, progressSeq, ok := runtime.codex.interruptWatchSnapshot()
	if !ok {
		return
	}
	snapshot := codexInterruptWatchSnapshot{threadID: threadID, turnID: turnID, progressSeq: progressSeq}
	go func() {
		time.Sleep(codexStaleInterruptWatchDelay)
		if err := s.recoverStaleCodexInterrupt(sessionID, runtime, snapshot); err != nil {
			_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_stale_interrupt_recover", err)
		}
	}()
}

func (s *Stub) recoverStaleCodexInterrupt(sessionID session.SessionID, runtime sessionRuntime, snapshot codexInterruptWatchSnapshot) error {
	if s == nil || runtime.protocol != runtimeProtocolCodexRPC || runtime.codex == nil {
		return nil
	}
	var updated sessionRecord
	restarted := false
	if err := s.withSessionInputLock(sessionID, func(record sessionRecord) error {
		record.runtime = s.runtimeForRecord(record)
		if record.identity.Backend() != session.BackendCodex || record.runtime.protocol != runtimeProtocolCodexRPC || record.runtime.codex == nil {
			return nil
		}
		if !sameRuntimeHandle(record.runtime, runtime) {
			return nil
		}
		transport := s.sessionTransportSnapshot(record)
		if transport.State != SessionTransportStateAttached || transport.ResetRequired {
			return nil
		}
		if record.state.Queue().Len() > 0 || record.uiRequest != nil || s.activeWaitForSession(sessionID) != nil {
			return nil
		}
		if !record.runtime.codex.staleInterruptMatches(snapshot.threadID, snapshot.turnID, snapshot.progressSeq) {
			return nil
		}
		var err error
		updated, _, err = s.replaceSessionRuntime(context.Background(), sessionID, record, restartSessionUsesPIAgentGRPC(record), true)
		if err != nil {
			return err
		}
		restarted = true
		return nil
	}); err != nil {
		return err
	}
	if restarted {
		s.emitQueueState(updated.identity.SessionID(), queueSnapshotFromState(updated.state))
		s.emitSessionState(updated.identity.SessionID())
	}
	return nil
}

func (s *Stub) startCodexTurnCompletionWatch(sessionID session.SessionID, threadID, turnID string) {
	if s == nil {
		return
	}
	expectedThreadID := strings.TrimSpace(threadID)
	expectedTurnID := strings.TrimSpace(turnID)
	if expectedTurnID == "" {
		return
	}
	go func() {
		time.Sleep(codexTurnCompletionProbeDelay)
		record, err := s.lookupSession(sessionID)
		if err != nil || record.identity.Backend() != session.BackendCodex || record.runtime.protocol != runtimeProtocolCodexRPC || record.runtime.codex == nil {
			return
		}
		_, currentThreadID, currentTurnID := record.runtime.codex.snapshot()
		if expectedThreadID != "" && strings.TrimSpace(currentThreadID) != expectedThreadID {
			return
		}
		if strings.TrimSpace(currentTurnID) != expectedTurnID {
			return
		}
		activity := codexVisibleActivity(record)
		if activity.Phase != codexRuntimePhaseRunning {
			return
		}
		if err := record.runtime.RequestCodexThreadState(context.Background()); err != nil {
			if !errors.Is(err, errCodexThreadNotReady) {
				_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_turn_completion_probe", err)
			}
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
	if aOK != bOK && sameRuntimeHandle(a.runtime, b.runtime) {
		return true
	}
	return sameRuntimeHandle(a.runtime, b.runtime)
}

func sameRuntimeHandle(a, b sessionRuntime) bool {
	if a.helper != nil || b.helper != nil {
		return sameRuntimeIODHelper(a.helper, b.helper) && a.piAgentGRPC == b.piAgentGRPC && a.handle == b.handle
	}
	return a.piAgentGRPC == b.piAgentGRPC && a.handle == b.handle && a.codex == b.codex
}

func sameRuntimeIODHelper(a, b *runtimeIODHelper) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.sessionID != b.sessionID || a.generationID != b.generationID {
		return false
	}
	aSocket := strings.TrimSpace(a.manifest.ControlSocketPath)
	bSocket := strings.TrimSpace(b.manifest.ControlSocketPath)
	if aSocket != "" || bSocket != "" {
		return aSocket == bSocket
	}
	return true
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

func isRuntimeChangedConflict(err error) bool {
	var appErr *Error
	return errors.As(err, &appErr) && appErr.Code == "conflict" && strings.Contains(appErr.Message, errRuntimeChanged.Error())
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
