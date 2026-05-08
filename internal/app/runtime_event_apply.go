package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"actrail/internal/domain/message"
	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

func (s *Stub) applyPIEvents(sessionID session.SessionID, events []pi.Event) error {
	for _, event := range events {
		if err := s.applyPIEvent(sessionID, event); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stub) applyPIEvent(sessionID session.SessionID, event pi.Event) error {
	s.messageCache.Invalidate(sessionID)
	if !s.codexRuntimeEventInMainThread(sessionID, event) && !codexSubagentMessageEvent(event) {
		s.emitSessionState(sessionID)
		return nil
	}
	if event.Kind == pi.EventKindMessageDelta || event.Kind == pi.EventKindTool || event.Kind == pi.EventKindUIRequest || (event.Kind == pi.EventKindMessage && event.Message != nil && event.Message.Role == pi.MessageRoleAssistant && event.Message.ToolCallCount > 0) {
		if err := s.markRuntimeActiveFromPIEvent(sessionID); err != nil {
			return err
		}
	}
	switch event.Kind {
	case pi.EventKindMessageDelta:
		return s.applyPIDelta(sessionID, event)
	case pi.EventKindMessage:
		return s.applyPIMessage(sessionID, event)
	case pi.EventKindTool:
		return s.applyPITool(sessionID, event)
	case pi.EventKindError:
		return s.applyPIError(sessionID, event)
	case pi.EventKindUIRequest:
		return s.applyPIUIRequest(sessionID, event)
	case pi.EventKindUIResolved:
		return s.applyPIUIResolved(sessionID, event)
	case pi.EventKindBoundary:
		return s.applyPIBoundary(sessionID, event)
	}
	return nil
}

func codexSubagentMessageEvent(event pi.Event) bool {
	if strings.TrimSpace(event.RawType) != "item/completed" || event.Message == nil {
		return false
	}
	if strings.TrimSpace(event.Message.StopReason) != "" {
		return false
	}
	return event.Message.Role == pi.MessageRoleUser || event.Message.Role == pi.MessageRoleAssistant
}

func (s *Stub) markRuntimeActiveFromPIEvent(sessionID session.SessionID) error {
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendPI {
		return nil
	}
	if record.runtime.protocol == runtimeProtocolPIRPC && record.runtime.helper != nil {
		s.holdPIRPCBusy(sessionID, record.runtime.helper.generationID)
		s.kickPIRPCStateProbe(sessionID, record.runtime.helper.generationID)
	}
	if err := s.setRuntimeAgentRunning(sessionID, true); err != nil {
		return err
	}
	if record.state.Busy() {
		return nil
	}
	if _, _, err := s.registry.SetBusy(sessionID, true); err != nil {
		return err
	}
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) applyPIDelta(sessionID session.SessionID, event pi.Event) error {
	if event.Delta == nil {
		return nil
	}
	if !s.codexRuntimeEventInMainThread(sessionID, event) {
		s.emitSessionState(sessionID)
		return nil
	}
	turnID := runtimeTurnID(event)
	if turnID == "" {
		return nil
	}
	if _, err := s.AppendAssistantDelta(sessionID, turnID, event.Delta.Text); err != nil {
		return err
	}
	s.emitMessageDelta(sessionID, turnID, string(event.Delta.Role), event.Delta.Text)
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) applyPIMessage(sessionID session.SessionID, event pi.Event) error {
	if event.Message == nil || strings.TrimSpace(event.Message.Text) == "" {
		return nil
	}
	if strings.TrimSpace(event.Message.StopReason) == "reasoning" {
		committed, err := s.AppendSessionMessage(sessionID, "system", "reasoning", event.Message.Text)
		if err != nil {
			return err
		}
		committed.Role = ""
		committed.Type = "reasoning"
		committed.EventID = piMessageEventID(event)
		committed.ParentEventID = piParentEventID(event)
		committed.Summary = firstLine(strings.TrimSpace(event.Message.Text))
		committed.Details = map[string]any{
			"raw_type": strings.TrimSpace(event.RawType),
		}
		s.emitMessageCommit(sessionID, runtimeTurnID(event), committed)
		s.emitSessionState(sessionID)
		return nil
	}
	if strings.TrimSpace(event.Message.StopReason) == "status" {
		committed, err := s.AppendSessionMessage(sessionID, "system", "pi_event", event.Message.Text)
		if err != nil {
			return err
		}
		committed.Role = ""
		committed.Type = "pi_event"
		committed.EventID = piMessageEventID(event)
		committed.ParentEventID = piParentEventID(event)
		committed.Details = map[string]any{
			"raw_type": strings.TrimSpace(event.RawType),
			"status":   true,
		}
		if event.Compaction != nil {
			committed.Details["compaction"] = compactionEventDetails(*event.Compaction)
			committed.Summary = compactionEventSummary(*event.Compaction)
		}
		s.emitMessageCommit(sessionID, runtimeTurnID(event), committed)
		s.emitSessionState(sessionID)
		return nil
	}
	role := strings.TrimSpace(string(event.Message.Role))
	if role == "" {
		return nil
	}
	if event.Message.Role == pi.MessageRoleUser {
		if !s.codexRuntimeUserMessageInMainThread(sessionID, event) {
			if err := s.applyCodexSubagentMessage(sessionID, event); err != nil {
				return err
			}
			s.emitSessionState(sessionID)
			return nil
		}
		if s.codexOutboundPromptMatches(sessionID, event.Message.Text) {
			s.emitSessionState(sessionID)
			return nil
		}
		if s.duplicateRuntimeUserMessage(sessionID, event.Message.Text) {
			s.emitSessionState(sessionID)
			return nil
		}
		committed, err := s.AppendSessionMessage(sessionID, role, "message", event.Message.Text)
		if err != nil {
			return err
		}
		committed.EventID = piMessageEventID(event)
		committed.ParentEventID = piParentEventID(event)
		s.emitMessageCommit(sessionID, runtimeTurnID(event), committed)
		s.emitSessionState(sessionID)
		return nil
	}
	if !event.Message.CommitLike {
		if event.Message.Role != pi.MessageRoleAssistant {
			return nil
		}
		turnID := runtimeTurnID(event)
		if turnID == "" {
			return nil
		}
		if _, err := s.AppendAssistantDelta(sessionID, turnID, event.Message.Text); err != nil {
			return err
		}
		s.emitMessageDelta(sessionID, turnID, role, event.Message.Text)
		s.emitSessionState(sessionID)
		return nil
	}

	turnID := runtimeTurnID(event)
	if event.Message.Role == pi.MessageRoleAssistant && !s.codexRuntimeMessageInMainThread(sessionID, event) {
		if err := s.applyCodexSubagentMessage(sessionID, event); err != nil {
			return err
		}
		s.emitSessionState(sessionID)
		return nil
	}
	committed, committedNew, err := s.commitRuntimeMessage(sessionID, turnID, role, event.Message.Text)
	if err != nil {
		return err
	}
	if event.Message.Role == pi.MessageRoleAssistant && event.Message.CommitLike && strings.TrimSpace(event.Message.StopReason) != "status" && event.Message.ToolCallCount == 0 {
		if record, ok := s.registry.Lookup(sessionID); ok && record.identity.Backend() == session.BackendPI {
			if record.runtime.protocol == runtimeProtocolPIRPC && record.runtime.helper != nil {
				s.holdPIRPCIdle(sessionID, record.runtime.helper.generationID)
			}
			if err := s.setRuntimeAgentRunning(sessionID, false); err != nil {
				return err
			}
			if _, _, err := s.registry.SetBusy(sessionID, false); err != nil {
				return err
			}
		} else if err := s.setRuntimeAgentRunning(sessionID, false); err != nil {
			return err
		}
	}
	if !committedNew {
		s.emitSessionState(sessionID)
		return nil
	}
	committed.EventID = piMessageEventID(event)
	committed.ParentEventID = piParentEventID(event)
	s.emitMessageCommit(sessionID, turnID, committed)
	s.emitAssistantFinalNotification(sessionID, committed)
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) duplicateRuntimeUserMessage(sessionID session.SessionID, text string) bool {
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return false
	}
	return duplicateRuntimeUserMessage(record.transcript.Items(), text)
}

func duplicateRuntimeUserMessage(items []message.CommittedMessage, text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if item.Role().String() != "user" || item.Kind().String() != "message" {
			continue
		}
		return strings.TrimSpace(item.Text()) == trimmed
	}
	return false
}

func (s *Stub) emitAssistantFinalNotification(sessionID session.SessionID, msg SessionMessage) {
	if msg.Role != "assistant" || strings.TrimSpace(msg.Text) == "" {
		return
	}
	title := "Session"
	if record, ok := s.registry.Lookup(sessionID); ok {
		title = firstNonEmptyString(record.alias, record.title, record.cwd, sessionID.String())
	}
	messageID := strings.TrimSpace(msg.EventID)
	if messageID == "" && msg.Seq > 0 {
		messageID = fmt.Sprintf("%s:%d", sessionID, msg.Seq)
	}
	s.emitNotification(NotificationEvent{SessionID: sessionID.String(), Title: title, Body: msg.Text, MessageID: messageID, Kind: "assistant_final"})
}

func (s *Stub) commitRuntimeMessage(sessionID session.SessionID, turnID, role, text string) (SessionMessage, bool, error) {
	record, err := s.lookupSession(sessionID)
	if err != nil {
		return SessionMessage{}, false, err
	}
	if partial, ok := record.transcript.PartialAssistantTurn(); ok {
		resolvedTurnID := strings.TrimSpace(turnID)
		if resolvedTurnID == "" {
			resolvedTurnID = partial.TurnID().String()
		}
		if partial.TurnID().String() == resolvedTurnID {
			msg, err := s.CommitAssistantTurn(sessionID, resolvedTurnID, text)
			return msg, true, err
		}
	}
	trimmedText := strings.TrimSpace(text)
	if role == "assistant" && trimmedText != "" {
		items := record.transcript.Items()
		if len(items) > 0 {
			last := items[len(items)-1]
			if last.Role().String() == role && last.Kind().String() == "message" && strings.TrimSpace(last.Text()) == trimmedText {
				return sessionMessageFromCommitted(last), false, nil
			}
		}
	}
	msg, err := s.AppendSessionMessage(sessionID, role, "message", text)
	return msg, true, err
}

func (s *Stub) applyPITool(sessionID session.SessionID, event pi.Event) error {
	if event.Tool == nil {
		return nil
	}
	kind := "tool"
	if event.Tool.Result {
		kind = "tool_result"
	}
	text := strings.TrimSpace(event.Tool.Text)
	if text == "" {
		text = strings.TrimSpace(event.Tool.Name)
	}
	if text == "" {
		text = kind
	}
	committed, err := s.AppendSessionMessage(sessionID, "system", kind, text)
	if err != nil {
		return err
	}
	committed.Role = ""
	committed.Type = kind
	committed.EventID = piToolEventID(event)
	committed.ParentEventID = piParentEventID(event)
	committed.Name = strings.TrimSpace(event.Tool.Name)
	committed.Summary = strings.TrimSpace(event.Tool.Name)
	committed.ToolCallID = strings.TrimSpace(event.Tool.CallID)
	committed.IsError = event.Tool.IsError
	committed.Details = map[string]any{}
	if committed.Name != "" {
		committed.Details["name"] = committed.Name
	}
	if committed.ToolCallID != "" {
		committed.Details["tool_call_id"] = committed.ToolCallID
	}
	if len(event.Tool.Arguments) > 0 {
		committed.Details["arguments"] = event.Tool.Arguments
	}
	s.emitMessageCommit(sessionID, runtimeTurnID(event), committed)
	s.emitSessionState(sessionID)
	return nil
}

func compactionEventSummary(event pi.CompactionEvent) string {
	if event.Phase == "start" {
		return "Compaction started"
	}
	if event.ErrorMessage != "" {
		return "Compaction failed"
	}
	if event.Aborted {
		return "Compaction aborted"
	}
	if event.WillRetry {
		return "Compaction ended, retrying"
	}
	return "Compaction ended"
}

func compactionEventDetails(event pi.CompactionEvent) map[string]any {
	details := map[string]any{
		"phase":     event.Phase,
		"reason":    event.Reason,
		"aborted":   event.Aborted,
		"willRetry": event.WillRetry,
	}
	if event.InputTokens > 0 {
		details["inputTokens"] = event.InputTokens
	}
	if event.InputTokensK > 0 {
		details["inputTokensK"] = event.InputTokensK
	}
	if event.TokensBefore > 0 {
		details["tokensBefore"] = event.TokensBefore
	}
	if event.TokensAfter > 0 {
		details["tokensAfter"] = event.TokensAfter
	}
	if event.TokensAfterK > 0 {
		details["tokensAfterK"] = event.TokensAfterK
	}
	if event.DurationMS > 0 {
		details["durationMs"] = event.DurationMS
	}
	if event.ErrorMessage != "" {
		details["errorMessage"] = event.ErrorMessage
	}
	if event.Model != nil {
		details["model"] = event.Model
	}
	if event.Result != nil {
		details["result"] = event.Result
	}
	return details
}

func (s *Stub) applyPIError(sessionID session.SessionID, event pi.Event) error {
	if event.Error == nil || strings.TrimSpace(event.Error.Message) == "" {
		return nil
	}
	committed, err := s.AppendSessionMessage(sessionID, "system", "error", event.Error.Message)
	if err != nil {
		return err
	}
	committed.Type = "error"
	committed.IsError = true
	committed.Details = map[string]any{
		"source":      strings.TrimSpace(event.Error.Source),
		"stop_reason": strings.TrimSpace(event.Error.StopReason),
	}
	s.emitMessageCommit(sessionID, runtimeTurnID(event), committed)
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) applyPIUIRequest(sessionID session.SessionID, event pi.Event) error {
	if event.UIRequest == nil {
		return nil
	}
	snapshot := SessionUIRequestSnapshot{
		RequestID:     strings.TrimSpace(event.UIRequest.RequestID),
		Kind:          strings.TrimSpace(string(event.UIRequest.Kind)),
		Method:        strings.TrimSpace(string(event.UIRequest.Method)),
		Title:         strings.TrimSpace(event.UIRequest.Title),
		Message:       strings.TrimSpace(event.UIRequest.Message),
		Prompt:        runtimeUIPrompt(*event.UIRequest),
		Question:      strings.TrimSpace(event.UIRequest.Prompt),
		Context:       strings.TrimSpace(event.UIRequest.Context),
		AllowFreeform: event.UIRequest.AllowFreeform,
		AllowMultiple: event.UIRequest.AllowMultiple,
		Options:       runtimeUIOptionsSnapshot(*event.UIRequest),
		Questions:     runtimeUIQuestionsSnapshot(*event.UIRequest),
		Metadata:      copyAnyMap(event.UIRequest.Metadata),
	}
	if err := s.SetSessionUIRequest(sessionID, snapshot); err != nil {
		return err
	}
	_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseWaitingUser, "ui_request", "ui_request")
	s.emitUIRequest(UIRequestEvent{SessionID: sessionID, Request: snapshot})
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) applyPIUIResolved(sessionID session.SessionID, event pi.Event) error {
	if event.UIResolved == nil {
		return nil
	}
	requestID := strings.TrimSpace(event.UIResolved.RequestID)
	if requestID == "" {
		return nil
	}
	if err := s.ClearSessionUIRequest(sessionID, requestID); err != nil {
		return err
	}
	_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseRunning, "codex_running", "ui_resolved")
	s.emitUIResolved(sessionID, requestID)
	s.emitSessionState(sessionID)
	s.scheduleQueuedDispatch(sessionID)
	return nil
}

func (s *Stub) applyPIBoundary(sessionID session.SessionID, event pi.Event) error {
	if event.Boundary == nil {
		return nil
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return nil
	}
	if record.identity.Backend() == session.BackendCodex {
		switch event.Boundary.Kind {
		case pi.BoundaryKindTurnCompleted, pi.BoundaryKindTurnAborted:
			_, _, err := s.registry.DiscardPartialAssistantTurn(sessionID)
			return err
		default:
			return nil
		}
	}
	piRPCSession := record.identity.Backend() == session.BackendPI && record.runtime.protocol == runtimeProtocolPIRPC
	if record.identity.Backend() == session.BackendPI && !piRPCSession {
		return nil
	}
	switch event.Boundary.Kind {
	case pi.BoundaryKindAgentStarted:
		if piRPCSession && record.runtime.helper != nil {
			s.holdPIRPCBusy(sessionID, record.runtime.helper.generationID)
		}
		if err := s.setRuntimeAgentRunning(sessionID, true); err != nil {
			return err
		}
		if _, _, err := s.registry.SetBusy(sessionID, true); err != nil {
			return err
		}
		s.emitSessionState(sessionID)
	case pi.BoundaryKindAgentCompleted:
		if piRPCSession && record.runtime.helper != nil {
			s.holdPIRPCIdle(sessionID, record.runtime.helper.generationID)
		}
		if err := s.setRuntimeAgentRunning(sessionID, false); err != nil {
			return err
		}
		state, ok, err := s.registry.SetBusy(sessionID, false)
		if err != nil {
			return err
		}
		if ok {
			s.emitSessionState(sessionID)
			if !state.Busy() {
				s.scheduleQueuedDispatch(sessionID)
			}
		}
	case pi.BoundaryKindTurnStarted:
		if piRPCSession {
			if record.runtime.helper != nil {
				s.holdPIRPCBusy(sessionID, record.runtime.helper.generationID)
			}
			if err := s.setRuntimeAgentRunning(sessionID, true); err != nil {
				return err
			}
		}
		if _, _, err := s.registry.SetBusy(sessionID, true); err != nil {
			return err
		}
		s.emitSessionState(sessionID)
	case pi.BoundaryKindTurnCompleted, pi.BoundaryKindTurnAborted:
		if event.Boundary.Kind == pi.BoundaryKindTurnCompleted && !event.Boundary.CommitLike && event.Boundary.Reason != "turn_end" {
			return nil
		}
		if piRPCSession {
			if record.runtime.helper != nil {
				s.holdPIRPCIdle(sessionID, record.runtime.helper.generationID)
			}
			if err := s.setRuntimeAgentRunning(sessionID, false); err != nil {
				return err
			}
			if _, _, err := s.registry.DiscardPartialAssistantTurn(sessionID); err != nil {
				return err
			}
			state, ok, err := s.registry.SetBusy(sessionID, false)
			if err != nil {
				return err
			}
			if ok {
				s.emitSessionState(sessionID)
				if !state.Busy() {
					s.scheduleQueuedDispatch(sessionID)
				}
			}
			return nil
		}
		if s.isRuntimeAgentRunning(sessionID) {
			if _, _, err := s.registry.SetBusy(sessionID, true); err != nil {
				return err
			}
			s.emitSessionState(sessionID)
			return nil
		}
		if _, _, err := s.registry.DiscardPartialAssistantTurn(sessionID); err != nil {
			return err
		}
		state, ok, err := s.registry.SetBusy(sessionID, false)
		if err != nil {
			return err
		}
		if ok {
			s.emitSessionState(sessionID)
			if !state.Busy() {
				s.scheduleQueuedDispatch(sessionID)
			}
		}
	}
	return nil
}

func (s *Stub) isPISession(sessionID session.SessionID) bool {
	if s == nil {
		return false
	}
	record, ok := s.registry.Lookup(sessionID)
	return ok && record.identity.Backend() == session.BackendPI
}

func runtimeTurnID(event pi.Event) string {
	for _, candidate := range []string{event.TurnID, event.RawID, event.ParentID} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	if event.Timestamp <= 0 {
		return ""
	}
	return fmt.Sprintf("turn_%d", int64(event.Timestamp*1000))
}

func runtimeUIPrompt(request pi.UIRequest) string {
	for _, candidate := range []string{request.Prompt, request.Message, request.Title, request.Context} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return "ui request"
}

func runtimeUIOptions(request pi.UIRequest) []string {
	options := make([]string, 0, len(request.Options))
	appendOption := func(option pi.UIOption) {
		label := strings.TrimSpace(option.Label)
		if label == "" {
			label = strings.TrimSpace(option.Value)
		}
		if label != "" {
			options = append(options, label)
		}
	}
	for _, option := range request.Options {
		appendOption(option)
	}
	if len(options) > 0 {
		return options
	}
	for _, question := range request.Questions {
		for _, option := range question.Options {
			appendOption(option)
		}
	}
	return options
}

func runtimeUIOptionsSnapshot(request pi.UIRequest) []SessionUIOptionSnapshot {
	options := make([]SessionUIOptionSnapshot, 0, len(request.Options))
	for _, option := range request.Options {
		options = append(options, SessionUIOptionSnapshot{
			Label:       strings.TrimSpace(option.Label),
			Value:       strings.TrimSpace(option.Value),
			Description: strings.TrimSpace(option.Description),
		})
	}
	if len(options) > 0 {
		return options
	}
	for _, question := range request.Questions {
		for _, option := range question.Options {
			options = append(options, SessionUIOptionSnapshot{
				Label:       strings.TrimSpace(option.Label),
				Value:       strings.TrimSpace(option.Value),
				Description: strings.TrimSpace(option.Description),
			})
		}
	}
	return options
}

func runtimeUIQuestionsSnapshot(request pi.UIRequest) []SessionUIQuestionSnapshot {
	if len(request.Questions) == 0 {
		return nil
	}
	questions := make([]SessionUIQuestionSnapshot, 0, len(request.Questions))
	for _, question := range request.Questions {
		questions = append(questions, SessionUIQuestionSnapshot{
			Header:      strings.TrimSpace(question.Header),
			Question:    strings.TrimSpace(question.Prompt),
			Options:     runtimeUIQuestionOptionsSnapshot(question.Options),
			MultiSelect: question.MultiSelect,
		})
	}
	return questions
}

func runtimeUIQuestionOptionsSnapshot(raw []pi.UIOption) []SessionUIOptionSnapshot {
	if len(raw) == 0 {
		return nil
	}
	options := make([]SessionUIOptionSnapshot, 0, len(raw))
	for _, option := range raw {
		options = append(options, SessionUIOptionSnapshot{
			Label:       strings.TrimSpace(option.Label),
			Value:       strings.TrimSpace(option.Value),
			Description: strings.TrimSpace(option.Description),
		})
	}
	return options
}

func (s *Stub) startRuntimeAskUserWait(sessionID session.SessionID, event pi.Event) {
	if s == nil || event.UIRequest == nil {
		return
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return
	}
	runtime := s.runtimeForSession(sessionID, record.identity.Backend(), record.runtime)
	request := *event.UIRequest
	go s.runRuntimeAskUserWait(sessionID, runtime, request)
}

func (s *Stub) runRuntimeAskUserWait(sessionID session.SessionID, runtime sessionRuntime, request pi.UIRequest) {
	question := strings.TrimSpace(request.Prompt)
	if question == "" {
		question = strings.TrimSpace(request.Message)
	}
	if question == "" {
		question = strings.TrimSpace(request.Title)
	}
	if question == "" {
		question = "Runtime requested user input"
	}
	blockingReason := strings.TrimSpace(stringValueFromMap(request.Metadata, "blocking_reason", "blockingReason"))
	if blockingReason == "" {
		blockingReason = "runtime requested ask_user input"
	}
	attempted := strings.TrimSpace(stringValueFromMap(request.Metadata, "attempted"))
	if attempted == "" {
		attempted = "runtime emitted ask_user"
	}
	fallback := strings.TrimSpace(stringValueFromMap(request.Metadata, "default_if_no_reply", "defaultIfNoReply"))
	if fallback == "" {
		fallback = "No reply received. Continue with the safest reversible assumption and state the assumption."
	}
	result, err := s.AskUserWait(context.Background(), RuntimeWaitRequest{
		SessionID:           sessionID,
		RequestID:           strings.TrimSpace(request.RequestID),
		Question:            question,
		Context:             strings.TrimSpace(request.Context),
		BlockingReason:      blockingReason,
		Attempted:           attempted,
		DefaultIfNoReply:    fallback,
		TimeoutAfterMinutes: timeoutMinutes(request.TimeoutMS),
	})
	if err != nil {
		return
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return
	}
	value := string(payload)
	requestID := strings.TrimSpace(request.RequestID)
	if requestID == "" {
		requestID = result.WaitID
	}
	if err := runtime.RespondUI(context.Background(), requestID, value); err != nil {
		_ = s.emitRuntimeControlDiagnostic(sessionID, "ask_user_wait_response", err)
	}
	if state, ok, err := s.registry.SetBusy(sessionID, false); err == nil && ok {
		s.emitSessionState(sessionID)
		if !state.Busy() {
			s.scheduleQueuedDispatch(sessionID)
		}
	}
}

func stringValueFromMap(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw == nil {
			return ""
		}
		if value, ok := raw[key]; ok {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return ""
}

func timeoutMinutes(timeoutMS *int) *int {
	if timeoutMS == nil || *timeoutMS <= 0 {
		return nil
	}
	minutes := (*timeoutMS + int(time.Minute/time.Millisecond) - 1) / int(time.Minute/time.Millisecond)
	if minutes < 1 {
		minutes = 1
	}
	return &minutes
}
