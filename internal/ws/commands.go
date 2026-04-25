package ws

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Subscription struct {
	Name       StreamName `json:"name"`
	ResumeFrom *int64     `json:"resume_from,omitempty"`
}

func (s Subscription) Validate() error {
	if err := s.Name.Validate(); err != nil {
		return err
	}
	if s.ResumeFrom != nil && *s.ResumeFrom < 0 {
		return fmt.Errorf("resume cursor for %q must be non-negative", s.Name)
	}
	return nil
}

type SubscribePayload struct {
	Streams []Subscription `json:"streams"`
}

func (p SubscribePayload) Validate() error {
	if len(p.Streams) == 0 {
		return fmt.Errorf("subscribe requires at least one stream")
	}
	seen := make(map[StreamName]struct{}, len(p.Streams))
	for _, sub := range p.Streams {
		if err := sub.Validate(); err != nil {
			return err
		}
		if _, ok := seen[sub.Name]; ok {
			return fmt.Errorf("duplicate stream %q in subscribe payload", sub.Name)
		}
		seen[sub.Name] = struct{}{}
	}
	return nil
}

type UnsubscribePayload struct {
	Streams []string `json:"streams"`
}

func (p UnsubscribePayload) Names() ([]StreamName, error) {
	if len(p.Streams) == 0 {
		return nil, fmt.Errorf("unsubscribe requires at least one stream")
	}
	seen := make(map[StreamName]struct{}, len(p.Streams))
	out := make([]StreamName, 0, len(p.Streams))
	for _, raw := range p.Streams {
		name, err := ParseStreamName(raw)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate stream %q in unsubscribe payload", name)
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

type SubscribeCommand struct {
	RequestID string
	Stream    StreamName
	Payload   SubscribePayload
}

type UnsubscribeCommand struct {
	RequestID string
	Stream    StreamName
	Streams   []StreamName
}

type PingCommand struct {
	RequestID string
	Stream    StreamName
}

func DecodeSubscribeCommand(frame RawFrame) (SubscribeCommand, error) {
	if err := frame.Validate(); err != nil {
		return SubscribeCommand{}, err
	}
	if frame.Type != FrameTypeSubscribe {
		return SubscribeCommand{}, fmt.Errorf("frame type %q is not subscribe", frame.Type)
	}
	if strings.TrimSpace(frame.RequestID) == "" {
		return SubscribeCommand{}, fmt.Errorf("subscribe request_id is required")
	}
	stream, err := ParseStreamName(frame.Stream)
	if err != nil {
		return SubscribeCommand{}, err
	}
	if stream != SystemStream {
		return SubscribeCommand{}, fmt.Errorf("subscribe stream must be %q", SystemStream)
	}
	var payload SubscribePayload
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return SubscribeCommand{}, fmt.Errorf("decode subscribe payload: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return SubscribeCommand{}, err
	}
	return SubscribeCommand{RequestID: frame.RequestID, Stream: stream, Payload: payload}, nil
}

func DecodeUnsubscribeCommand(frame RawFrame) (UnsubscribeCommand, error) {
	if err := frame.Validate(); err != nil {
		return UnsubscribeCommand{}, err
	}
	if frame.Type != FrameTypeUnsubscribe {
		return UnsubscribeCommand{}, fmt.Errorf("frame type %q is not unsubscribe", frame.Type)
	}
	if strings.TrimSpace(frame.RequestID) == "" {
		return UnsubscribeCommand{}, fmt.Errorf("unsubscribe request_id is required")
	}
	stream, err := ParseStreamName(frame.Stream)
	if err != nil {
		return UnsubscribeCommand{}, err
	}
	if stream != SystemStream {
		return UnsubscribeCommand{}, fmt.Errorf("unsubscribe stream must be %q", SystemStream)
	}
	var payload UnsubscribePayload
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return UnsubscribeCommand{}, fmt.Errorf("decode unsubscribe payload: %w", err)
	}
	names, err := payload.Names()
	if err != nil {
		return UnsubscribeCommand{}, err
	}
	return UnsubscribeCommand{RequestID: frame.RequestID, Stream: stream, Streams: names}, nil
}

func DecodePingCommand(frame RawFrame) (PingCommand, error) {
	if err := frame.Validate(); err != nil {
		return PingCommand{}, err
	}
	if frame.Type != FrameTypePing {
		return PingCommand{}, fmt.Errorf("frame type %q is not ping", frame.Type)
	}
	stream, err := ParseStreamName(frame.Stream)
	if err != nil {
		return PingCommand{}, err
	}
	if stream != SystemStream {
		return PingCommand{}, fmt.Errorf("ping stream must be %q", SystemStream)
	}
	return PingCommand{RequestID: frame.RequestID, Stream: stream}, nil
}
