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

var runtimeHelperProjectors sync.Map

type iodTerminalOutputPayload struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type runtimeEventDecoder struct {
	backend     session.Backend
	helperLines runtimeLineBuffer
}

type runtimeProjection struct {
	events           []pi.Event
	codexThreadID    string
	codexTurnID      string
	clearCodexTurn   bool
	codexInitialized bool
}

type runtimeLineBuffer struct {
	pending bytes.Buffer
}

type runtimeHelperProjector struct {
	mu      sync.Mutex
	decoder runtimeEventDecoder
}

func mergeRuntimeProjection(dst, src runtimeProjection) runtimeProjection {
	if len(src.events) > 0 {
		dst.events = append(dst.events, src.events...)
	}
	if strings.TrimSpace(src.codexThreadID) != "" {
		dst.codexThreadID = strings.TrimSpace(src.codexThreadID)
	}
	if strings.TrimSpace(src.codexTurnID) != "" {
		dst.codexTurnID = strings.TrimSpace(src.codexTurnID)
	}
	if src.clearCodexTurn {
		dst.clearCodexTurn = true
	}
	if src.codexInitialized {
		dst.codexInitialized = true
	}
	return dst
}

func runtimeProjectionSupported(backend session.Backend) bool {
	switch backend {
	case session.BackendPI, session.BackendCodex:
		return true
	default:
		return false
	}
}

func (s *Stub) startRuntimeIngest(sessionID session.SessionID, backend session.Backend, runtime sessionRuntime) {
	if s == nil || !runtimeProjectionSupported(backend) {
		return
	}
	if runtime.helper != nil {
		go s.readRuntimeHelper(sessionID, backend, runtime.helper)
		return
	}
	if runtime.handle == nil {
		return
	}
	for _, src := range runtimeOutputSources(runtime) {
		if src == nil {
			continue
		}
		go s.readRuntimeOutput(sessionID, backend, src)
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

func (s *Stub) readRuntimeOutput(sessionID session.SessionID, backend session.Backend, src io.Reader) {
	decoder := runtimeEventDecoder{backend: backend}
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRuntimeLineBytes)
	for scanner.Scan() {
		_ = s.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine(scanner.Bytes()))
	}
}

func (s *Stub) readRuntimeHelper(sessionID session.SessionID, backend session.Backend, helper *runtimeIODHelper) {
	if s == nil || helper == nil || helper.streamClient == nil {
		return
	}
	for {
		packet, err := helper.streamClient.ReadPacket(context.Background())
		if err != nil {
			return
		}
		if err := s.applyRuntimeHelperPacket(sessionID, backend, packet); err != nil {
			return
		}
	}
}

func (s *Stub) applyRuntimeHelperPacket(sessionID session.SessionID, backend session.Backend, packet any) error {
	if s == nil {
		return nil
	}
	key := struct {
		stub      *Stub
		sessionID session.SessionID
		backend   session.Backend
	}{stub: s, sessionID: sessionID, backend: backend}
	projectorAny, _ := runtimeHelperProjectors.LoadOrStore(key, &runtimeHelperProjector{decoder: runtimeEventDecoder{backend: backend}})
	projector := projectorAny.(*runtimeHelperProjector)
	projector.mu.Lock()
	defer projector.mu.Unlock()
	projection, err := projector.decoder.decodeHelperPacket(packet)
	if err != nil {
		return err
	}
	return s.applyRuntimeProjection(sessionID, projection)
}

func (d *runtimeEventDecoder) decodeHelperPacket(packet any) (runtimeProjection, error) {
	switch v := packet.(type) {
	case iod.StatePacket:
		return d.decodeHelperFact(v.Fact)
	case iod.ReplayItemPacket:
		return d.decodeHelperFact(v.Item.Fact)
	default:
		return runtimeProjection{}, nil
	}
}

func (d *runtimeEventDecoder) decodeHelperFact(fact iod.HelperFact) (runtimeProjection, error) {
	if fact.FactKind != iod.FactOutputDelta {
		return runtimeProjection{}, nil
	}
	var payload iodTerminalOutputPayload
	if err := json.Unmarshal(fact.Payload, &payload); err != nil {
		return runtimeProjection{}, fmt.Errorf("decode helper output payload: %w", err)
	}
	return d.decodeHelperOutput(payload), nil
}

func (d *runtimeEventDecoder) decodeHelperOutput(payload iodTerminalOutputPayload) runtimeProjection {
	if payload.Data == "" {
		return runtimeProjection{}
	}
	d.helperLines.append(payload.Data)
	projection := runtimeProjection{}

	for {
		line, ok := d.helperLines.nextLine()
		if !ok {
			return projection
		}
		projection = mergeRuntimeProjection(projection, d.decodeRuntimeLine(line))
	}
}

// PI still emits legacy type-tagged JSON objects. Codex app-server emits line-delimited request/response/notification objects.
func (d *runtimeEventDecoder) decodeRuntimeLine(raw []byte) runtimeProjection {
	line := bytes.TrimSpace(raw)
	if len(line) == 0 || line[0] != '{' {
		return runtimeProjection{}
	}
	if d.backend == session.BackendCodex {
		if projection, ok := decodeCodexAppServerLine(line); ok {
			return projection
		}
	}
	material, err := pi.ParseObjectJSON(line)
	if err != nil {
		return runtimeProjection{}
	}
	return runtimeProjection{events: material.Events}
}

type codexAppServerLine struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

type codexThreadEnvelope struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type codexTurnEnvelope struct {
	Turn struct {
		ID     string `json:"id"`
		Status any    `json:"status"`
		Error  any    `json:"error"`
	} `json:"turn"`
}

type codexAgentMessageDeltaParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type codexItemNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Item     struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"item"`
}

func decodeCodexAppServerLine(raw []byte) (runtimeProjection, bool) {
	var line codexAppServerLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return runtimeProjection{}, false
	}
	if strings.TrimSpace(line.Method) != "" {
		switch strings.TrimSpace(line.Method) {
		case "thread/started":
			var params codexThreadEnvelope
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return runtimeProjection{}, true
			}
			return runtimeProjection{codexThreadID: strings.TrimSpace(params.Thread.ID)}, true
		case "turn/started":
			var params codexTurnEnvelope
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return runtimeProjection{}, true
			}
			turnID := strings.TrimSpace(params.Turn.ID)
			return runtimeProjection{
				codexTurnID: turnID,
				events:      []pi.Event{{Kind: pi.EventKindBoundary, RawType: line.Method, TurnID: turnID, Boundary: &pi.Boundary{Kind: pi.BoundaryKindTurnStarted}}},
			}, true
		case "turn/completed":
			var params codexTurnEnvelope
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return runtimeProjection{}, true
			}
			turnID := strings.TrimSpace(params.Turn.ID)
			return runtimeProjection{
				clearCodexTurn: true,
				codexTurnID:    turnID,
				events:         []pi.Event{{Kind: pi.EventKindBoundary, RawType: line.Method, TurnID: turnID, Boundary: &pi.Boundary{Kind: pi.BoundaryKindTurnCompleted}}},
			}, true
		case "item/agentMessage/delta":
			var params codexAgentMessageDeltaParams
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return runtimeProjection{}, true
			}
			if strings.TrimSpace(params.Delta) == "" {
				return runtimeProjection{}, true
			}
			return runtimeProjection{events: []pi.Event{{
				Kind:    pi.EventKindMessageDelta,
				RawType: line.Method,
				RawID:   strings.TrimSpace(params.ItemID),
				TurnID:  strings.TrimSpace(params.TurnID),
				Delta:   &pi.MessageDelta{Role: pi.MessageRoleAssistant, Text: params.Delta},
			}}}, true
		case "item/completed":
			var params codexItemNotification
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return runtimeProjection{}, true
			}
			if strings.TrimSpace(params.Item.Type) != "agentMessage" || strings.TrimSpace(params.Item.Text) == "" {
				return runtimeProjection{}, true
			}
			return runtimeProjection{events: []pi.Event{{
				Kind:    pi.EventKindMessage,
				RawType: line.Method,
				RawID:   strings.TrimSpace(params.Item.ID),
				TurnID:  strings.TrimSpace(params.TurnID),
				Message: &pi.Message{ID: strings.TrimSpace(params.Item.ID), Role: pi.MessageRoleAssistant, Text: params.Item.Text, Class: pi.MessageClassCommitted, CommitLike: true},
			}}}, true
		default:
			return runtimeProjection{}, true
		}
	}
	if len(line.Result) > 0 && string(line.Result) != "null" {
		var thread codexThreadEnvelope
		if err := json.Unmarshal(line.Result, &thread); err == nil && strings.TrimSpace(thread.Thread.ID) != "" {
			return runtimeProjection{codexThreadID: strings.TrimSpace(thread.Thread.ID)}, true
		}
		var turn codexTurnEnvelope
		if err := json.Unmarshal(line.Result, &turn); err == nil && strings.TrimSpace(turn.Turn.ID) != "" {
			return runtimeProjection{codexTurnID: strings.TrimSpace(turn.Turn.ID)}, true
		}
		return runtimeProjection{codexInitialized: true}, true
	}
	return runtimeProjection{}, false
}

func (s *Stub) applyRuntimeProjection(sessionID session.SessionID, projection runtimeProjection) error {
	if projection.codexInitialized {
		s.noteCodexInitialized(sessionID)
	}
	if strings.TrimSpace(projection.codexThreadID) != "" {
		s.noteCodexThreadID(sessionID, projection.codexThreadID)
	}
	if strings.TrimSpace(projection.codexTurnID) != "" {
		s.noteCodexTurnID(sessionID, projection.codexTurnID)
	}
	if projection.clearCodexTurn {
		s.clearCodexTurnID(sessionID, projection.codexTurnID)
	}
	return s.applyPIEvents(sessionID, projection.events)
}

func (s *Stub) withCodexRuntimeState(sessionID session.SessionID, apply func(*codexRuntimeState)) {
	if s == nil || apply == nil {
		return
	}
	_, _, err := s.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = s.runtimeForSession(record.identity.SessionID(), record.identity.Backend(), record.runtime)
		if record.runtime.codex != nil {
			apply(record.runtime.codex)
		}
		return nil
	})
	if err != nil {
		return
	}
}

func (s *Stub) noteCodexInitialized(sessionID session.SessionID) {
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.markInitialized()
	})
}

func (s *Stub) noteCodexThreadID(sessionID session.SessionID, threadID string) {
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.setThreadID(threadID)
	})
}

func (s *Stub) noteCodexTurnID(sessionID session.SessionID, turnID string) {
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.setActiveTurnID(turnID)
	})
}

func (s *Stub) clearCodexTurnID(sessionID session.SessionID, turnID string) {
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.clearActiveTurnID(turnID)
	})
}

func (s *Stub) applyPIEvents(sessionID session.SessionID, events []pi.Event) error {
	for _, event := range events {
		if err := s.applyPIEvent(sessionID, event); err != nil {
			return err
		}
	}
	return nil
}

func (b *runtimeLineBuffer) append(chunk string) {
	if b == nil || chunk == "" {
		return
	}
	_, _ = b.pending.WriteString(chunk)
}

func (b *runtimeLineBuffer) nextLine() ([]byte, bool) {
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
