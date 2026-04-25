package app

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"

	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

const maxRuntimeLineBytes = 1 << 20

func (s *Stub) startRuntimeIngest(sessionID session.SessionID, backend session.Backend, runtime sessionRuntime) {
	if s == nil || backend != session.BackendPI || runtime.handle == nil {
		return
	}
	for _, src := range runtimeOutputSources(runtime) {
		if src == nil {
			continue
		}
		go s.readPIRuntime(sessionID, src)
	}
}

func runtimeOutputSources(runtime sessionRuntime) []io.Reader {
	if runtime.handle == nil {
		return nil
	}
	if pty := runtime.handle.PTY(); pty != nil {
		return []io.Reader{pty}
	}
	sources := make([]io.Reader, 0, 2)
	if stdout := runtime.handle.Stdout(); stdout != nil {
		sources = append(sources, stdout)
	}
	if stderr := runtime.handle.Stderr(); stderr != nil {
		sources = append(sources, stderr)
	}
	return sources
}

func (s *Stub) readPIRuntime(sessionID session.SessionID, src io.Reader) {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRuntimeLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		material, err := pi.ParseObjectJSON(line)
		if err != nil {
			continue
		}
		for _, event := range material.Events {
			s.applyPIEvent(sessionID, event)
		}
	}
}

func (s *Stub) applyPIEvent(sessionID session.SessionID, event pi.Event) {
	switch event.Kind {
	case pi.EventKindMessageDelta:
		s.applyPIDelta(sessionID, event)
	case pi.EventKindMessage:
		s.applyPIMessage(sessionID, event)
	case pi.EventKindUIRequest:
		s.applyPIUIRequest(sessionID, event)
	case pi.EventKindUIResolved:
		s.applyPIUIResolved(sessionID, event)
	case pi.EventKindBoundary:
		s.applyPIBoundary(sessionID, event)
	}
}

func (s *Stub) applyPIDelta(sessionID session.SessionID, event pi.Event) {
	if event.Delta == nil {
		return
	}
	turnID := runtimeTurnID(event)
	if turnID == "" {
		return
	}
	if _, err := s.AppendAssistantDelta(sessionID, turnID, event.Delta.Text); err != nil {
		return
	}
	s.emitMessageDelta(sessionID, turnID, string(event.Delta.Role), event.Delta.Text)
	s.emitSessionState(sessionID)
}

func (s *Stub) applyPIMessage(sessionID session.SessionID, event pi.Event) {
	if event.Message == nil || strings.TrimSpace(event.Message.Text) == "" {
		return
	}
	role := strings.TrimSpace(string(event.Message.Role))
	if role == "" || event.Message.Role == pi.MessageRoleUser {
		return
	}
	if !event.Message.CommitLike {
		if event.Message.Role != pi.MessageRoleAssistant {
			return
		}
		turnID := runtimeTurnID(event)
		if turnID == "" {
			return
		}
		if _, err := s.AppendAssistantDelta(sessionID, turnID, event.Message.Text); err != nil {
			return
		}
		s.emitMessageDelta(sessionID, turnID, role, event.Message.Text)
		s.emitSessionState(sessionID)
		return
	}

	turnID := runtimeTurnID(event)
	committed, err := s.commitRuntimeMessage(sessionID, turnID, role, event.Message.Text)
	if err != nil {
		return
	}
	s.emitMessageCommit(sessionID, turnID, committed)
	s.emitSessionState(sessionID)
}

func (s *Stub) commitRuntimeMessage(sessionID session.SessionID, turnID, role, text string) (SessionMessage, error) {
	record, err := s.lookupSession(sessionID)
	if err != nil {
		return SessionMessage{}, err
	}
	if partial, ok := record.transcript.PartialAssistantTurn(); ok {
		resolvedTurnID := strings.TrimSpace(turnID)
		if resolvedTurnID == "" {
			resolvedTurnID = partial.TurnID().String()
		}
		if partial.TurnID().String() == resolvedTurnID {
			return s.CommitAssistantTurn(sessionID, resolvedTurnID, text)
		}
	}
	return s.AppendSessionMessage(sessionID, role, "message", text)
}

func (s *Stub) applyPIUIRequest(sessionID session.SessionID, event pi.Event) {
	if event.UIRequest == nil {
		return
	}
	snapshot := SessionUIRequestSnapshot{
		RequestID: strings.TrimSpace(event.UIRequest.RequestID),
		Kind:      strings.TrimSpace(string(event.UIRequest.Kind)),
		Prompt:    runtimeUIPrompt(*event.UIRequest),
	}
	if err := s.SetSessionUIRequest(sessionID, snapshot); err != nil {
		return
	}
	s.emitUIRequest(UIRequestEvent{
		SessionID: sessionID,
		RequestID: snapshot.RequestID,
		Kind:      snapshot.Kind,
		Prompt:    snapshot.Prompt,
		Options:   runtimeUIOptions(*event.UIRequest),
	})
	s.emitSessionState(sessionID)
}

func (s *Stub) applyPIUIResolved(sessionID session.SessionID, event pi.Event) {
	if event.UIResolved == nil {
		return
	}
	requestID := strings.TrimSpace(event.UIResolved.RequestID)
	if requestID == "" {
		return
	}
	_ = s.ClearSessionUIRequest(sessionID, requestID)
	s.emitUIResolved(sessionID, requestID)
	s.emitSessionState(sessionID)
	s.scheduleQueuedDispatch(sessionID)
}

func (s *Stub) applyPIBoundary(sessionID session.SessionID, event pi.Event) {
	if event.Boundary == nil {
		return
	}
	switch event.Boundary.Kind {
	case pi.BoundaryKindTurnStarted:
		if _, _, err := s.registry.SetBusy(sessionID, true); err == nil {
			s.emitSessionState(sessionID)
		}
	case pi.BoundaryKindTurnCompleted, pi.BoundaryKindTurnAborted:
		if state, ok, err := s.registry.SetBusy(sessionID, false); err == nil && ok {
			s.emitSessionState(sessionID)
			if !state.Busy() {
				s.scheduleQueuedDispatch(sessionID)
			}
		}
	}
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
