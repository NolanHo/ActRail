package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"actrail/internal/app"
	"actrail/internal/domain/session"
)

type sessionStatePayload struct {
	SessionID string                       `json:"session_id"`
	StreamSeq int64                        `json:"stream_seq"`
	Busy      bool                         `json:"busy"`
	QueueLen  int                          `json:"queue_len"`
	TailSeq   uint64                       `json:"tail_seq"`
	Transport app.SessionTransportSnapshot `json:"transport"`
}

type messageDeltaPayload struct {
	SessionID string `json:"session_id"`
	StreamSeq int64  `json:"stream_seq"`
	TurnID    string `json:"turn_id"`
	Role      string `json:"role"`
	Delta     string `json:"delta"`
}

type messageGeneratingPayload struct {
	SessionID string `json:"session_id"`
	StreamSeq int64  `json:"stream_seq"`
	TurnID    string `json:"turn_id"`
	Role      string `json:"role"`
	Active    bool   `json:"active"`
}

type committedMessagePayload struct {
	Seq           uint64         `json:"seq"`
	Role          string         `json:"role,omitempty"`
	Kind          string         `json:"kind,omitempty"`
	Type          string         `json:"type,omitempty"`
	Text          string         `json:"text"`
	TS            float64        `json:"ts,omitempty"`
	EventID       string         `json:"event_id,omitempty"`
	ParentEventID string         `json:"parent_event_id,omitempty"`
	SourceOrder   string         `json:"source_order,omitempty"`
	Name          string         `json:"name,omitempty"`
	Summary       string         `json:"summary,omitempty"`
	ToolCallID    string         `json:"tool_call_id,omitempty"`
	IsError       bool           `json:"is_error,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
}

type messageCommitPayload struct {
	SessionID string                  `json:"session_id"`
	StreamSeq int64                   `json:"stream_seq"`
	TurnID    string                  `json:"turn_id,omitempty"`
	Message   committedMessagePayload `json:"message"`
}

type queueItemPayload struct {
	QueueID string `json:"queue_id"`
	Text    string `json:"text"`
	State   string `json:"state"`
}

type queueStatePayload struct {
	SessionID string             `json:"session_id"`
	StreamSeq int64              `json:"stream_seq"`
	Items     []queueItemPayload `json:"items"`
}

type uiRequestPayload struct {
	SessionID string                       `json:"session_id"`
	StreamSeq int64                        `json:"stream_seq"`
	Request   app.SessionUIRequestSnapshot `json:"request"`
}

type uiResolvedPayload struct {
	SessionID string `json:"session_id"`
	StreamSeq int64  `json:"stream_seq"`
	RequestID string `json:"request_id"`
}

type generationBrokenPayload struct {
	SessionID    string `json:"session_id"`
	StreamSeq    int64  `json:"stream_seq"`
	GenerationID string `json:"generation_id"`
	Reason       string `json:"reason"`
}

type transportResetRequiredPayload struct {
	SessionID    string `json:"session_id"`
	StreamSeq    int64  `json:"stream_seq"`
	GenerationID string `json:"generation_id"`
	Reason       string `json:"reason"`
}

type notificationPayload struct {
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	MessageID string `json:"message_id,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

// AppBridge maps app-side live events and control requests onto websocket frames.
type AppBridge struct {
	controller app.SessionController
	cursors    app.SessionResumeCursorWriter
	publisher  *Publisher
	now        func() time.Time
	frameIDs   IDSource

	mu               sync.Mutex
	streamSeqs       map[StreamName]int64
	generatingByTurn map[string]struct{}
}

func NewAppBridge(controller app.SessionController, cursors app.SessionResumeCursorWriter, publisher *Publisher) *AppBridge {
	return &AppBridge{
		controller:       controller,
		cursors:          cursors,
		publisher:        publisher,
		now:              time.Now,
		frameIDs:         NewCounterIDSource("evt"),
		streamSeqs:       make(map[StreamName]int64),
		generatingByTurn: make(map[string]struct{}),
	}
}

func (b *AppBridge) HandleSend(cmd SendCommand) error {
	if b.controller == nil {
		return NewCommandError(ErrorCodeUnsupported, "session control is unavailable", "type")
	}
	_, err := b.controller.Send(context.Background(), app.SendRequest{SessionID: cmd.SessionID, Text: cmd.Text})
	if err != nil {
		return mapAppCommandError(err)
	}
	return nil
}

func (b *AppBridge) HandleEnqueue(cmd EnqueueCommand) error {
	if b.controller == nil {
		return NewCommandError(ErrorCodeUnsupported, "session control is unavailable", "type")
	}
	_, err := b.controller.Enqueue(context.Background(), app.EnqueueRequest{SessionID: cmd.SessionID, Text: cmd.Text})
	if err != nil {
		return mapAppCommandError(err)
	}
	return nil
}

func (b *AppBridge) HandleQueueCancel(cmd QueueCancelCommand) error {
	if b.controller == nil {
		return NewCommandError(ErrorCodeUnsupported, "session control is unavailable", "type")
	}
	_, err := b.controller.CancelQueue(context.Background(), app.CancelQueueRequest{SessionID: cmd.SessionID})
	if err != nil {
		return mapAppCommandError(err)
	}
	return nil
}

func (b *AppBridge) HandleInterrupt(cmd InterruptCommand) error {
	if b.controller == nil {
		return NewCommandError(ErrorCodeUnsupported, "session control is unavailable", "type")
	}
	_, err := b.controller.Interrupt(context.Background(), app.InterruptRequest{SessionID: cmd.SessionID})
	if err != nil {
		return mapAppCommandError(err)
	}
	return nil
}

func (b *AppBridge) HandleUIResponse(cmd UIResponseCommand) error {
	if b.controller == nil {
		return NewCommandError(ErrorCodeUnsupported, "session control is unavailable", "type")
	}
	value, err := decodeUIResponseValue(cmd.Value)
	if err != nil {
		return WrapCommandError(ErrorCodeInvalidRequest, err.Error(), "value", err)
	}
	_, err = b.controller.RespondUI(context.Background(), app.UIResponseRequest{
		SessionID:  cmd.SessionID,
		ResponseTo: cmd.ResponseTo,
		Value:      value,
	})
	if err != nil {
		return mapAppCommandError(err)
	}
	return nil
}

func (b *AppBridge) PublishSessionState(event app.SessionStateEvent) {
	b.publish(event.SessionID, session.StreamKindMain, FrameTypeSessionState, func(cursor int64) any {
		return sessionStatePayload{
			SessionID: event.SessionID.String(),
			StreamSeq: cursor,
			Busy:      event.Busy,
			QueueLen:  event.QueueLen,
			TailSeq:   event.TailSeq,
			Transport: event.Transport,
		}
	})
}

func (b *AppBridge) PublishMessageDelta(event app.MessageDeltaEvent) {
	b.publishGenerating(event.SessionID, event.TurnID, event.Role, true)
	b.publish(event.SessionID, session.StreamKindMain, FrameTypeMessageDelta, func(cursor int64) any {
		return messageDeltaPayload{
			SessionID: event.SessionID.String(),
			StreamSeq: cursor,
			TurnID:    event.TurnID,
			Role:      event.Role,
			Delta:     event.Delta,
		}
	})
}

func (b *AppBridge) PublishMessageCommit(event app.MessageCommitEvent) {
	b.publish(event.SessionID, session.StreamKindMain, FrameTypeMessageCommit, func(cursor int64) any {
		return messageCommitPayload{
			SessionID: event.SessionID.String(),
			StreamSeq: cursor,
			TurnID:    event.TurnID,
			Message: committedMessagePayload{
				Seq:           event.Message.Seq,
				Role:          event.Message.Role,
				Kind:          event.Message.Kind,
				Type:          event.Message.Type,
				Text:          event.Message.Text,
				TS:            event.Message.TS,
				EventID:       event.Message.EventID,
				ParentEventID: event.Message.ParentEventID,
				SourceOrder:   event.Message.SourceOrder,
				Name:          event.Message.Name,
				Summary:       event.Message.Summary,
				ToolCallID:    event.Message.ToolCallID,
				IsError:       event.Message.IsError,
				Details:       event.Message.Details,
			},
		}
	})
	b.publishGenerating(event.SessionID, event.TurnID, event.Message.Role, false)
}

func (b *AppBridge) PublishQueueState(event app.QueueStateEvent) {
	b.publish(event.SessionID, session.StreamKindMain, FrameTypeQueueState, func(cursor int64) any {
		items := make([]queueItemPayload, 0, len(event.Queue.Items))
		for _, item := range event.Queue.Items {
			items = append(items, queueItemPayload{QueueID: item.ID, Text: item.Text, State: item.State})
		}
		return queueStatePayload{SessionID: event.SessionID.String(), StreamSeq: cursor, Items: items}
	})
}

func (b *AppBridge) PublishUIRequest(event app.UIRequestEvent) {
	b.publish(event.SessionID, session.StreamKindUI, FrameTypeUIRequest, func(cursor int64) any {
		return uiRequestPayload{
			SessionID: event.SessionID.String(),
			StreamSeq: cursor,
			Request:   event.Request,
		}
	})
}

func (b *AppBridge) PublishUIResolved(event app.UIResolvedEvent) {
	b.publish(event.SessionID, session.StreamKindUI, FrameTypeUIResolved, func(cursor int64) any {
		return uiResolvedPayload{SessionID: event.SessionID.String(), StreamSeq: cursor, RequestID: event.RequestID}
	})
}

func (b *AppBridge) PublishGenerationBroken(event app.GenerationBrokenEvent) {
	b.publish(event.SessionID, session.StreamKindMain, FrameTypeSessionGenerationBroken, func(cursor int64) any {
		return generationBrokenPayload{
			SessionID:    event.SessionID.String(),
			StreamSeq:    cursor,
			GenerationID: event.GenerationID,
			Reason:       event.Reason,
		}
	})
}

func (b *AppBridge) PublishTransportResetRequired(event app.TransportResetRequiredEvent) {
	b.publish(event.SessionID, session.StreamKindMain, FrameTypeTransportResetRequired, func(cursor int64) any {
		return transportResetRequiredPayload{
			SessionID:    event.SessionID.String(),
			StreamSeq:    cursor,
			GenerationID: event.GenerationID,
			Reason:       event.Reason,
		}
	})
}

func (b *AppBridge) PublishNotification(event app.NotificationEvent) {
	if b == nil || b.publisher == nil {
		return
	}
	frame := Frame{Type: FrameTypeNotification, ID: b.frameIDs.Next(), TS: UnixTS(b.now()), Stream: SystemStream.String(), Payload: notificationPayload{SessionID: event.SessionID, Title: event.Title, Body: event.Body, MessageID: event.MessageID, Kind: event.Kind}}
	_, _ = b.publisher.Publish(0, frame)
}

func (b *AppBridge) publishGenerating(sessionID session.SessionID, turnID, role string, active bool) {
	if b == nil || turnID == "" {
		return
	}
	key := sessionID.String() + ":" + turnID
	b.mu.Lock()
	_, alreadyActive := b.generatingByTurn[key]
	if active && alreadyActive {
		b.mu.Unlock()
		return
	}
	if active {
		b.generatingByTurn[key] = struct{}{}
	} else {
		delete(b.generatingByTurn, key)
	}
	b.mu.Unlock()
	b.publish(sessionID, session.StreamKindMain, FrameTypeMessageGenerating, func(cursor int64) any {
		return messageGeneratingPayload{
			SessionID: sessionID.String(),
			StreamSeq: cursor,
			TurnID:    turnID,
			Role:      role,
			Active:    active,
		}
	})
}

func (b *AppBridge) publish(sessionID session.SessionID, kind session.StreamKind, typ FrameType, payload func(int64) any) {
	if b == nil || b.publisher == nil {
		return
	}
	stream, err := bridgeStreamName(sessionID, kind)
	if err != nil {
		return
	}
	cursor := b.nextCursor(stream)
	frame := Frame{
		Type:    typ,
		ID:      b.frameIDs.Next(),
		TS:      UnixTS(b.now()),
		Stream:  stream.String(),
		Payload: payload(cursor),
	}
	result, _ := b.publisher.Publish(cursor, frame)
	if !result.Stored || b.cursors == nil {
		return
	}
	_ = b.cursors.SetSessionResumeCursor(sessionID, kind, cursor)
}

func (b *AppBridge) nextCursor(stream StreamName) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.streamSeqs[stream]++
	return b.streamSeqs[stream]
}

func bridgeStreamName(sessionID session.SessionID, kind session.StreamKind) (StreamName, error) {
	route, err := session.NewStreamRoute(sessionID, kind)
	if err != nil {
		return "", err
	}
	return StreamFromRoute(route), nil
}

func mapAppCommandError(err error) error {
	var appErr *app.Error
	if errors.As(err, &appErr) {
		switch appErr.Code {
		case "invalid_request":
			return NewCommandError(ErrorCodeInvalidRequest, appErr.Message, appErr.Field)
		case "conflict":
			return NewCommandError(ErrorCodeConflict, appErr.Message, appErr.Field)
		case "not_found":
			return NewCommandError(ErrorCodeNotFound, appErr.Message, appErr.Field)
		case "unsupported":
			return NewCommandError(ErrorCodeUnsupported, appErr.Message, appErr.Field)
		default:
			return WrapCommandError(ErrorCodeInternal, appErr.Message, appErr.Field, err)
		}
	}
	return err
}

func decodeUIResponseValue(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", errors.New("ui.response value is required")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", err
		}
		return text, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return "", err
	}
	return compact.String(), nil
}
