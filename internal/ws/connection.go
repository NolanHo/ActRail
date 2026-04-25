package ws

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type IDSource interface {
	Next() string
}

type CounterIDSource struct {
	mu     sync.Mutex
	prefix string
	next   int
}

func NewCounterIDSource(prefix string) *CounterIDSource {
	if prefix == "" {
		prefix = "evt"
	}
	return &CounterIDSource{prefix: prefix}
}

func (s *CounterIDSource) Next() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return fmt.Sprintf("%s_%03d", s.prefix, s.next)
}

type SubscriptionSet struct {
	items map[StreamName]Subscription
}

func NewSubscriptionSet() SubscriptionSet {
	return SubscriptionSet{items: make(map[StreamName]Subscription)}
}

func (s *SubscriptionSet) ApplySubscribe(payload SubscribePayload) error {
	if err := payload.Validate(); err != nil {
		return err
	}
	if s.items == nil {
		s.items = make(map[StreamName]Subscription)
	}
	for _, sub := range payload.Streams {
		s.items[sub.Name] = sub
	}
	return nil
}

func (s *SubscriptionSet) ApplyUnsubscribe(streams []StreamName) error {
	if len(streams) == 0 {
		return fmt.Errorf("unsubscribe requires at least one stream")
	}
	for _, name := range streams {
		if err := name.Validate(); err != nil {
			return err
		}
		delete(s.items, name)
	}
	return nil
}

func (s SubscriptionSet) Snapshot() []Subscription {
	out := make([]Subscription, 0, len(s.items))
	for _, sub := range s.items {
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool {
		li := streamSortKey(out[i].Name)
		lj := streamSortKey(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].Name.String() < out[j].Name.String()
	})
	return out
}

func (s SubscriptionSet) Has(name StreamName) bool {
	_, ok := s.items[name]
	return ok
}

func streamSortKey(name StreamName) int {
	switch name {
	case SystemStream:
		return 0
	case SessionsStream:
		return 1
	default:
		return 2
	}
}

type frameWriter interface {
	WriteFrames(frames ...Frame) error
}

type websocketFrameWriter struct {
	mu    sync.Mutex
	conn  *websocket.Conn
	codec Codec
}

func newWebsocketFrameWriter(conn *websocket.Conn, codec Codec) *websocketFrameWriter {
	return &websocketFrameWriter{conn: conn, codec: codec}
}

func (w *websocketFrameWriter) WriteFrames(frames ...Frame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, frame := range frames {
		encoded, err := w.codec.Encode(frame)
		if err != nil {
			return err
		}
		if err := w.conn.WriteMessage(websocket.TextMessage, encoded); err != nil {
			return err
		}
	}
	return nil
}

type ConnectionState struct {
	mu            sync.RWMutex
	id            string
	ids           IDSource
	heartbeat     HeartbeatState
	subscriptions SubscriptionSet
	writer        frameWriter
}

func NewConnectionState(connectionID string, heartbeatInterval time.Duration, ids IDSource, now time.Time) (*ConnectionState, error) {
	if connectionID == "" {
		return nil, fmt.Errorf("connection id is required")
	}
	heartbeat, err := NewHeartbeatState(heartbeatInterval, now)
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = NewCounterIDSource("evt")
	}
	return &ConnectionState{
		id:            connectionID,
		ids:           ids,
		heartbeat:     heartbeat,
		subscriptions: NewSubscriptionSet(),
	}, nil
}

func (c *ConnectionState) AttachWriter(writer frameWriter) error {
	if writer == nil {
		return fmt.Errorf("connection writer is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writer != nil {
		return fmt.Errorf("connection %q writer already attached", c.id)
	}
	c.writer = writer
	return nil
}

func (c *ConnectionState) ID() string {
	return c.id
}

func (c *ConnectionState) Subscriptions() []Subscription {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.subscriptions.Snapshot()
}

func (c *ConnectionState) HasSubscription(name StreamName) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.subscriptions.Has(name)
}

func (c *ConnectionState) Heartbeat() HeartbeatState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.heartbeat
}

func (c *ConnectionState) BuildHeartbeatFrame(now time.Time) Frame {
	return NewHeartbeatFrame(now, c.ids.Next(), c.id)
}

func (c *ConnectionState) WriteFrames(now time.Time, frames ...Frame) error {
	if len(frames) == 0 {
		return nil
	}
	c.mu.RLock()
	writer := c.writer
	c.mu.RUnlock()
	if writer == nil {
		return fmt.Errorf("connection %q writer is not attached", c.id)
	}
	if err := writer.WriteFrames(frames...); err != nil {
		return err
	}
	c.mu.Lock()
	c.heartbeat.ObserveServerSend(now)
	c.mu.Unlock()
	return nil
}

func (c *ConnectionState) HandleFrame(now time.Time, frame RawFrame, replay *ReplayBuffer, target CommandTarget) ([]Frame, error) {
	switch frame.Type {
	case FrameTypeSubscribe:
		cmd, err := DecodeSubscribeCommand(frame)
		if err != nil {
			return c.errorFrames(now, frame, ErrorCodeInvalidRequest, err.Error(), "payload"), nil
		}
		return c.handleSubscribe(now, cmd, replay)
	case FrameTypeUnsubscribe:
		cmd, err := DecodeUnsubscribeCommand(frame)
		if err != nil {
			return c.errorFrames(now, frame, ErrorCodeInvalidRequest, err.Error(), "payload"), nil
		}
		c.mu.Lock()
		err = c.subscriptions.ApplyUnsubscribe(cmd.Streams)
		c.mu.Unlock()
		if err != nil {
			return c.errorFrames(now, frame, ErrorCodeInvalidRequest, err.Error(), "payload"), nil
		}
		return c.serverFrames(NewAckFrame(now, c.ids.Next(), cmd.Stream, cmd.RequestID, FrameTypeUnsubscribe)), nil
	case FrameTypePing:
		if _, err := DecodePingCommand(frame); err != nil {
			return c.errorFrames(now, frame, ErrorCodeInvalidRequest, err.Error(), "payload"), nil
		}
		c.mu.Lock()
		c.heartbeat.ObserveClientPing(now)
		c.mu.Unlock()
		return nil, nil
	case FrameTypeSend:
		cmd, err := DecodeSendCommand(frame)
		if err != nil {
			return c.errorFrames(now, frame, ErrorCodeInvalidRequest, err.Error(), "payload"), nil
		}
		return c.handleCommand(now, cmd.Stream, cmd.RequestID, FrameTypeSend, target, func(target CommandTarget) error {
			return target.HandleSend(cmd)
		})
	case FrameTypeEnqueue:
		cmd, err := DecodeEnqueueCommand(frame)
		if err != nil {
			return c.errorFrames(now, frame, ErrorCodeInvalidRequest, err.Error(), "payload"), nil
		}
		return c.handleCommand(now, cmd.Stream, cmd.RequestID, FrameTypeEnqueue, target, func(target CommandTarget) error {
			return target.HandleEnqueue(cmd)
		})
	case FrameTypeInterrupt:
		cmd, err := DecodeInterruptCommand(frame)
		if err != nil {
			return c.errorFrames(now, frame, ErrorCodeInvalidRequest, err.Error(), "payload"), nil
		}
		return c.handleCommand(now, cmd.Stream, cmd.RequestID, FrameTypeInterrupt, target, func(target CommandTarget) error {
			return target.HandleInterrupt(cmd)
		})
	case FrameTypeUIResponse:
		cmd, err := DecodeUIResponseCommand(frame)
		if err != nil {
			return c.errorFrames(now, frame, ErrorCodeInvalidRequest, err.Error(), "payload"), nil
		}
		return c.handleCommand(now, cmd.Stream, cmd.RequestID, FrameTypeUIResponse, target, func(target CommandTarget) error {
			return target.HandleUIResponse(cmd)
		})
	default:
		stream := SystemStream
		if parsed, err := ParseStreamName(frame.Stream); err == nil {
			stream = parsed
		}
		return c.errorFrames(now, frame, ErrorCodeUnsupported, fmt.Sprintf("command %q is not handled by websocket core", frame.Type), "type", stream), nil
	}
}

func (c *ConnectionState) handleSubscribe(now time.Time, cmd SubscribeCommand, replay *ReplayBuffer) ([]Frame, error) {
	replayed := make([]Frame, 0)
	for _, sub := range cmd.Payload.Streams {
		if sub.ResumeFrom == nil {
			continue
		}
		if replay == nil {
			return c.errorFrames(now, RawFrame{RequestID: cmd.RequestID, Stream: cmd.Stream.String()}, ErrorCodeConflict, "resume requested without replay buffer", "payload", cmd.Stream), nil
		}
		frames, err := replay.Replay(sub.Name, *sub.ResumeFrom)
		if err != nil {
			var resetErr ResetRequiredError
			if ok := AsResetRequired(err, &resetErr); ok {
				resetFrame, frameErr := NewResetRequiredFrame(now, c.ids.Next(), sub.Name, "resume_cursor_expired")
				if frameErr != nil {
					return nil, frameErr
				}
				return c.serverFrames(resetFrame), nil
			}
			return nil, err
		}
		replayed = append(replayed, frames...)
	}
	c.mu.Lock()
	err := c.subscriptions.ApplySubscribe(cmd.Payload)
	c.mu.Unlock()
	if err != nil {
		return c.errorFrames(now, RawFrame{RequestID: cmd.RequestID, Stream: cmd.Stream.String()}, ErrorCodeInvalidRequest, err.Error(), "payload", cmd.Stream), nil
	}
	frames := []Frame{NewAckFrame(now, c.ids.Next(), cmd.Stream, cmd.RequestID, FrameTypeSubscribe)}
	frames = append(frames, replayed...)
	return c.serverFrames(frames...), nil
}

func (c *ConnectionState) handleCommand(now time.Time, stream StreamName, requestID string, command FrameType, target CommandTarget, dispatch func(CommandTarget) error) ([]Frame, error) {
	if target == nil {
		return c.errorFrames(now, RawFrame{RequestID: requestID, Stream: stream.String()}, ErrorCodeUnsupported, fmt.Sprintf("command %q requires a command target", command), "type", stream), nil
	}
	if err := dispatch(target); err != nil {
		code, message, field := normalizeCommandError(err)
		return c.errorFrames(now, RawFrame{RequestID: requestID, Stream: stream.String()}, code, message, field, stream), nil
	}
	return c.serverFrames(NewAckFrame(now, c.ids.Next(), stream, requestID, command)), nil
}

func (c *ConnectionState) serverFrames(frames ...Frame) []Frame {
	return frames
}

func (c *ConnectionState) errorFrames(now time.Time, incoming RawFrame, code ErrorCode, message, field string, streamOverride ...StreamName) []Frame {
	stream := SystemStream
	if len(streamOverride) > 0 {
		stream = streamOverride[0]
	} else if parsed, err := ParseStreamName(incoming.Stream); err == nil {
		stream = parsed
	}
	return c.serverFrames(NewErrorFrame(now, c.ids.Next(), stream, incoming.RequestID, code, message, field))
}

func AsResetRequired(err error, target *ResetRequiredError) bool {
	resetErr, ok := err.(ResetRequiredError)
	if !ok {
		return false
	}
	if target != nil {
		*target = resetErr
	}
	return true
}
