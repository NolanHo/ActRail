package ws

import (
	"fmt"
	"sort"
	"sync"
	"time"
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

type ConnectionState struct {
	id            string
	ids           IDSource
	heartbeat     HeartbeatState
	subscriptions SubscriptionSet
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

func (c *ConnectionState) ID() string {
	return c.id
}

func (c *ConnectionState) Subscriptions() []Subscription {
	return c.subscriptions.Snapshot()
}

func (c *ConnectionState) Heartbeat() HeartbeatState {
	return c.heartbeat
}

func (c *ConnectionState) BuildHeartbeatFrame(now time.Time) Frame {
	frame := NewHeartbeatFrame(now, c.ids.Next(), c.id)
	c.heartbeat.ObserveServerSend(now)
	return frame
}

func (c *ConnectionState) HandleFrame(now time.Time, frame RawFrame, replay *ReplayBuffer) ([]Frame, error) {
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
		if err := c.subscriptions.ApplyUnsubscribe(cmd.Streams); err != nil {
			return c.errorFrames(now, frame, ErrorCodeInvalidRequest, err.Error(), "payload"), nil
		}
		return c.serverFrames(now, NewAckFrame(now, c.ids.Next(), cmd.Stream, cmd.RequestID, FrameTypeUnsubscribe)), nil
	case FrameTypePing:
		if _, err := DecodePingCommand(frame); err != nil {
			return c.errorFrames(now, frame, ErrorCodeInvalidRequest, err.Error(), "payload"), nil
		}
		c.heartbeat.ObserveClientPing(now)
		return nil, nil
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
				return c.serverFrames(now, resetFrame), nil
			}
			return nil, err
		}
		replayed = append(replayed, frames...)
	}
	if err := c.subscriptions.ApplySubscribe(cmd.Payload); err != nil {
		return c.errorFrames(now, RawFrame{RequestID: cmd.RequestID, Stream: cmd.Stream.String()}, ErrorCodeInvalidRequest, err.Error(), "payload", cmd.Stream), nil
	}
	frames := []Frame{NewAckFrame(now, c.ids.Next(), cmd.Stream, cmd.RequestID, FrameTypeSubscribe)}
	frames = append(frames, replayed...)
	return c.serverFrames(now, frames...), nil
}

func (c *ConnectionState) serverFrames(now time.Time, frames ...Frame) []Frame {
	if len(frames) > 0 {
		c.heartbeat.ObserveServerSend(now)
	}
	return frames
}

func (c *ConnectionState) errorFrames(now time.Time, incoming RawFrame, code ErrorCode, message, field string, streamOverride ...StreamName) []Frame {
	stream := SystemStream
	if len(streamOverride) > 0 {
		stream = streamOverride[0]
	} else if parsed, err := ParseStreamName(incoming.Stream); err == nil {
		stream = parsed
	}
	return c.serverFrames(now, NewErrorFrame(now, c.ids.Next(), stream, incoming.RequestID, code, message, field))
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
