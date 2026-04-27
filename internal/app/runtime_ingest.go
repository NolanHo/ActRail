package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"actrail/internal/adapters/iod"
	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

const maxRuntimeLineBytes = 1 << 20

var piHelperProjectors sync.Map

type iodTerminalOutputPayload struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type piRuntimeDecoder struct {
	helperLines piRuntimeLineBuffer
}

type piRuntimeLineBuffer struct {
	pending bytes.Buffer
}

type piHelperProjector struct {
	mu      sync.Mutex
	decoder piRuntimeDecoder
}

func (s *Stub) startRuntimeIngest(sessionID session.SessionID, backend session.Backend, runtime sessionRuntime) {
	if s == nil || backend != session.BackendPI {
		return
	}
	if runtime.helper != nil {
		go s.readPIHelperRuntime(sessionID, runtime.helper)
		return
	}
	if runtime.handle == nil {
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
	decoder := piRuntimeDecoder{}
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRuntimeLineBytes)
	for scanner.Scan() {
		_ = s.applyPIEvents(sessionID, decoder.decodeRuntimeLine(scanner.Bytes()))
	}
}

func (s *Stub) readPIHelperRuntime(sessionID session.SessionID, helper *runtimeIODHelper) {
	if s == nil || helper == nil || helper.streamClient == nil {
		return
	}
	for {
		packet, err := helper.streamClient.ReadPacket(context.Background())
		if err != nil {
			return
		}
		if err := s.applyPIHelperPacket(sessionID, packet); err != nil {
			return
		}
	}
}

func (s *Stub) applyPIHelperPacket(sessionID session.SessionID, packet any) error {
	if s == nil {
		return nil
	}
	projectorAny, _ := piHelperProjectors.LoadOrStore(struct {
		stub      *Stub
		sessionID session.SessionID
	}{stub: s, sessionID: sessionID}, &piHelperProjector{})
	projector := projectorAny.(*piHelperProjector)
	projector.mu.Lock()
	defer projector.mu.Unlock()
	events, err := projector.decoder.decodeHelperPacket(packet)
	if err != nil {
		return err
	}
	return s.applyPIEvents(sessionID, events)
}

func (d *piRuntimeDecoder) decodeHelperPacket(packet any) ([]pi.Event, error) {
	switch v := packet.(type) {
	case iod.StatePacket:
		return d.decodeHelperFact(v.Fact)
	case iod.ReplayItemPacket:
		return d.decodeHelperFact(v.Item.Fact)
	default:
		return nil, nil
	}
}

func (d *piRuntimeDecoder) decodeHelperFact(fact iod.HelperFact) ([]pi.Event, error) {
	if fact.FactKind != iod.FactOutputDelta {
		return nil, nil
	}
	var payload iodTerminalOutputPayload
	if err := json.Unmarshal(fact.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode helper output payload: %w", err)
	}
	return d.decodeHelperOutput(payload), nil
}

func (d *piRuntimeDecoder) decodeHelperOutput(payload iodTerminalOutputPayload) []pi.Event {
	if payload.Data == "" {
		return nil
	}
	d.helperLines.append(payload.Data)
	var events []pi.Event

	for {
		line, ok := d.helperLines.nextLine()
		if !ok {
			return events
		}
		events = append(events, d.decodeRuntimeLine(line)...)
	}
}

func (d *piRuntimeDecoder) decodeRuntimeLine(raw []byte) []pi.Event {
	line := bytes.TrimSpace(raw)
	if len(line) == 0 || line[0] != '{' {
		return nil
	}
	material, err := pi.ParseObjectJSON(line)
	if err != nil {
		return nil
	}
	return material.Events
}

func (s *Stub) applyPIEvents(sessionID session.SessionID, events []pi.Event) error {
	for _, event := range events {
		if err := s.applyPIEvent(sessionID, event); err != nil {
			return err
		}
	}
	return nil
}

func (b *piRuntimeLineBuffer) append(chunk string) {
	if b == nil || chunk == "" {
		return
	}
	_, _ = b.pending.WriteString(chunk)
}

func (b *piRuntimeLineBuffer) nextLine() ([]byte, bool) {
	if b == nil {
		return nil, false
	}
	data := b.pending.Bytes()
	idx := bytes.IndexByte(data, '\n')
	if idx < 0 {
		return nil, false
	}
	line := append([]byte(nil), data[:idx]...)
	b.pending.Next(idx + 1)
	return line, true
}

func (s *Stub) applyPIEvent(sessionID session.SessionID, event pi.Event) error {
	switch event.Kind {
	case pi.EventKindMessageDelta:
		return s.applyPIDelta(sessionID, event)
	case pi.EventKindMessage:
		return s.applyPIMessage(sessionID, event)
	case pi.EventKindUIRequest:
		return s.applyPIUIRequest(sessionID, event)
	case pi.EventKindUIResolved:
		return s.applyPIUIResolved(sessionID, event)
	case pi.EventKindBoundary:
		return s.applyPIBoundary(sessionID, event)
	}
	return nil
}

func (s *Stub) applyPIDelta(sessionID session.SessionID, event pi.Event) error {
	if event.Delta == nil {
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
	role := strings.TrimSpace(string(event.Message.Role))
	if role == "" || event.Message.Role == pi.MessageRoleUser {
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
	committed, err := s.commitRuntimeMessage(sessionID, turnID, role, event.Message.Text)
	if err != nil {
		return err
	}
	s.emitMessageCommit(sessionID, turnID, committed)
	s.emitSessionState(sessionID)
	return nil
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
	s.emitUIResolved(sessionID, requestID)
	s.emitSessionState(sessionID)
	s.scheduleQueuedDispatch(sessionID)
	return nil
}

func (s *Stub) applyPIBoundary(sessionID session.SessionID, event pi.Event) error {
	if event.Boundary == nil {
		return nil
	}
	switch event.Boundary.Kind {
	case pi.BoundaryKindTurnStarted:
		if _, _, err := s.registry.SetBusy(sessionID, true); err != nil {
			return err
		}
		s.emitSessionState(sessionID)
	case pi.BoundaryKindTurnCompleted, pi.BoundaryKindTurnAborted:
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
