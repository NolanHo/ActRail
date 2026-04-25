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
	SessionID string `json:"session_id"`
	StreamSeq int64  `json:"stream_seq"`
	Busy      bool   `json:"busy"`
	QueueLen  int    `json:"queue_len"`
	TailSeq   uint64 `json:"tail_seq"`
}

type messageDeltaPayload struct {
	SessionID string `json:"session_id"`
	StreamSeq int64  `json:"stream_seq"`
	TurnID    string `json:"turn_id"`
	Role      string `json:"role"`
	Delta     string `json:"delta"`
}

type committedMessagePayload struct {
	Seq  uint64 `json:"seq"`
	Role string `json:"role"`
	Text string `json:"text"`
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

type uiRequestBody struct {
	RequestID string   `json:"request_id"`
	Kind      string   `json:"kind"`
	Prompt    string   `json:"prompt"`
	Options   []string `json:"options,omitempty"`
}

type uiRequestPayload struct {
	SessionID string        `json:"session_id"`
	StreamSeq int64         `json:"stream_seq"`
	Request   uiRequestBody `json:"request"`
}

type uiResolvedPayload struct {
	SessionID string `json:"session_id"`
	StreamSeq int64  `json:"stream_seq"`
	RequestID string `json:"request_id"`
}

type sessionStateReader interface {
	SessionState(context.Context, app.SessionStateRequest) (app.SessionStateResponse, error)
}

// AppBridge maps app-side live events and control requests onto websocket frames.
type AppBridge struct {
	controller app.SessionController
	cursors    app.SessionResumeCursorWriter
	publisher  *Publisher
	now        func() time.Time
	frameIDs   IDSource

	mu         sync.Mutex
	streamSeqs map[StreamName]int64
}

func NewAppBridge(controller app.SessionController, cursors app.SessionResumeCursorWriter, publisher *Publisher) *AppBridge {
	return &AppBridge{
		controller: controller,
		cursors:    cursors,
		publisher:  publisher,
		now:        time.Now,
		frameIDs:   NewCounterIDSource("evt"),
		streamSeqs: make(map[StreamName]int64),
	}
}

func (b *AppBridge) HandleSend(cmd SendCommand) error {
	if b.controller == nil {
		return NewCommandError(ErrorCodeUnsupported, "session control is unavailable", "type")
	}
	response, err := b.controller.Send(context.Background(), app.SendRequest{SessionID: cmd.SessionID, Text: cmd.Text})
	if err != nil {
		return mapAppCommandError(err)
	}
	b.PublishMessageCommit(app.MessageCommitEvent{SessionID: cmd.SessionID, Message: response.Message})
	b.PublishQueueState(app.QueueStateEvent{SessionID: cmd.SessionID, Queue: response.Queue})
	b.PublishSessionState(b.currentStateEvent(cmd.SessionID, response.Busy, len(response.Queue.Items), response.Message.Seq))
	return nil
}

func (b *AppBridge) HandleEnqueue(cmd EnqueueCommand) error {
	if b.controller == nil {
		return NewCommandError(ErrorCodeUnsupported, "session control is unavailable", "type")
	}
	response, err := b.controller.Enqueue(context.Background(), app.EnqueueRequest{SessionID: cmd.SessionID, Text: cmd.Text})
	if err != nil {
		return mapAppCommandError(err)
	}
	b.PublishQueueState(app.QueueStateEvent{SessionID: cmd.SessionID, Queue: response.Queue})
	b.PublishSessionState(b.currentStateEvent(cmd.SessionID, response.Busy, len(response.Queue.Items), 0))
	return nil
}

func (b *AppBridge) HandleInterrupt(cmd InterruptCommand) error {
	if b.controller == nil {
		return NewCommandError(ErrorCodeUnsupported, "session control is unavailable", "type")
	}
	response, err := b.controller.Interrupt(context.Background(), app.InterruptRequest{SessionID: cmd.SessionID})
	if err != nil {
		return mapAppCommandError(err)
	}
	b.PublishQueueState(app.QueueStateEvent{SessionID: cmd.SessionID, Queue: response.Queue})
	b.PublishSessionState(b.currentStateEvent(cmd.SessionID, response.Busy, len(response.Queue.Items), 0))
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
	response, err := b.controller.RespondUI(context.Background(), app.UIResponseRequest{
		SessionID:  cmd.SessionID,
		ResponseTo: cmd.ResponseTo,
		Value:      value,
	})
	if err != nil {
		return mapAppCommandError(err)
	}
	b.PublishUIResolved(app.UIResolvedEvent{SessionID: cmd.SessionID, RequestID: response.ResolvedRequestID})
	b.PublishSessionState(b.currentStateEvent(cmd.SessionID, response.Busy, len(response.Queue.Items), 0))
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
		}
	})
}

func (b *AppBridge) PublishMessageDelta(event app.MessageDeltaEvent) {
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
				Seq:  event.Message.Seq,
				Role: event.Message.Role,
				Text: event.Message.Text,
			},
		}
	})
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
			Request: uiRequestBody{
				RequestID: event.RequestID,
				Kind:      event.Kind,
				Prompt:    event.Prompt,
				Options:   append([]string(nil), event.Options...),
			},
		}
	})
}

func (b *AppBridge) PublishUIResolved(event app.UIResolvedEvent) {
	b.publish(event.SessionID, session.StreamKindUI, FrameTypeUIResolved, func(cursor int64) any {
		return uiResolvedPayload{SessionID: event.SessionID.String(), StreamSeq: cursor, RequestID: event.RequestID}
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

func (b *AppBridge) currentStateEvent(sessionID session.SessionID, busy bool, queueLen int, tailSeq uint64) app.SessionStateEvent {
	if reader, ok := b.controller.(sessionStateReader); ok {
		state, err := reader.SessionState(context.Background(), app.SessionStateRequest{SessionID: sessionID})
		if err == nil {
			return app.SessionStateEvent{SessionID: sessionID, Busy: state.Busy, QueueLen: len(state.Queue.Items), TailSeq: state.TailSeq}
		}
	}
	return app.SessionStateEvent{SessionID: sessionID, Busy: busy, QueueLen: queueLen, TailSeq: tailSeq}
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
