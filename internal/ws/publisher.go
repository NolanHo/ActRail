package ws

import (
	"errors"
	"fmt"
	"time"
)

type PublishResult struct {
	Stream    StreamName
	Stored    bool
	Delivered int
}

type PublisherOption func(*Publisher)

type Publisher struct {
	registry *Registry
	replay   *ReplayBuffer
	now      func() time.Time
}

func NewPublisher(registry *Registry, replay *ReplayBuffer, opts ...PublisherOption) *Publisher {
	p := &Publisher{
		registry: registry,
		replay:   replay,
		now:      time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	if p.registry == nil {
		p.registry = NewRegistry()
	}
	return p
}

func WithPublisherNow(now func() time.Time) PublisherOption {
	return func(p *Publisher) {
		if now != nil {
			p.now = now
		}
	}
}

func (p *Publisher) Publish(cursor int64, frame Frame) (PublishResult, error) {
	stream, err := ParseStreamName(frame.Stream)
	if err != nil {
		return PublishResult{}, err
	}
	if _, err := ParseSessionStream(stream); err != nil {
		return PublishResult{}, fmt.Errorf("publish requires session stream: %w", err)
	}
	if cursor < 1 {
		return PublishResult{}, fmt.Errorf("publish cursor must be at least 1")
	}
	if p.replay == nil {
		return PublishResult{}, fmt.Errorf("publish requires replay buffer")
	}
	if err := p.replay.Append(stream, cursor, frame); err != nil {
		return PublishResult{}, err
	}
	delivered, err := p.broadcastAt(p.now(), stream, frame)
	return PublishResult{Stream: stream, Stored: true, Delivered: delivered}, err
}

func (p *Publisher) PublishSession(cursor int64, frame Frame) (PublishResult, error) {
	return p.Publish(cursor, frame)
}

func (p *Publisher) Broadcast(frame Frame) (PublishResult, error) {
	stream, err := ParseStreamName(frame.Stream)
	if err != nil {
		return PublishResult{}, err
	}
	delivered, err := p.broadcastAt(p.now(), stream, frame)
	return PublishResult{Stream: stream, Delivered: delivered}, err
}

func (p *Publisher) BroadcastSessions(frame Frame) (PublishResult, error) {
	stream, err := ParseStreamName(frame.Stream)
	if err != nil {
		return PublishResult{}, err
	}
	if stream != SessionsStream {
		return PublishResult{}, fmt.Errorf("sessions broadcast requires %q stream", SessionsStream)
	}
	return p.Broadcast(frame)
}

func (p *Publisher) broadcastAt(now time.Time, stream StreamName, frame Frame) (int, error) {
	if p.registry == nil {
		return 0, nil
	}
	subscribers := p.registry.Subscribers(stream)
	delivered := 0
	var errs []error
	for _, conn := range subscribers {
		if frame.Type == FrameTypeMessageDelta && conn.SuppressesMessageDeltas(stream) {
			continue
		}
		if err := conn.WriteFrames(now, frame); err != nil {
			errs = append(errs, fmt.Errorf("connection %q: %w", conn.ID(), err))
			continue
		}
		delivered++
	}
	return delivered, errors.Join(errs...)
}
