package ws

import (
	"encoding/json"
	"fmt"
	"strings"

	"actrail/internal/domain/session"
)

type Subscription struct {
	Name                  StreamName `json:"name"`
	ResumeFrom            *int64     `json:"resume_from,omitempty"`
	SuppressMessageDeltas bool       `json:"suppress_message_deltas,omitempty"`
}

func (s Subscription) Validate() error {
	if err := s.Name.Validate(); err != nil {
		return err
	}
	if s.ResumeFrom != nil {
		if *s.ResumeFrom < 0 {
			return fmt.Errorf("resume cursor for %q must be non-negative", s.Name)
		}
		if _, err := ParseSessionStream(s.Name); err != nil {
			return fmt.Errorf("resume cursor is only supported on session streams: %w", err)
		}
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

type SendCommand struct {
	RequestID string
	Stream    StreamName
	SessionID session.SessionID
	Text      string
}

type EnqueueCommand struct {
	RequestID string
	Stream    StreamName
	SessionID session.SessionID
	Text      string
}

type QueueCancelCommand struct {
	RequestID string
	Stream    StreamName
	SessionID session.SessionID
}

type InterruptCommand struct {
	RequestID string
	Stream    StreamName
	SessionID session.SessionID
}

type UIResponseCommand struct {
	RequestID  string
	Stream     StreamName
	SessionID  session.SessionID
	ResponseTo string
	Value      json.RawMessage
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

func DecodeSendCommand(frame RawFrame) (SendCommand, error) {
	requestID, stream, route, err := decodeSessionCommand(frame, FrameTypeSend, session.StreamKindMain)
	if err != nil {
		return SendCommand{}, err
	}
	var payload struct {
		SessionID string `json:"session_id"`
		RuntimeID string `json:"runtime_id"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return SendCommand{}, fmt.Errorf("decode send payload: %w", err)
	}
	sessionID, err := resolvePayloadRouteID(payload.SessionID, payload.RuntimeID, route, FrameTypeSend)
	if err != nil {
		return SendCommand{}, err
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		return SendCommand{}, fmt.Errorf("send text is required")
	}
	return SendCommand{RequestID: requestID, Stream: stream, SessionID: sessionID, Text: text}, nil
}

func DecodeEnqueueCommand(frame RawFrame) (EnqueueCommand, error) {
	requestID, stream, route, err := decodeSessionCommand(frame, FrameTypeEnqueue, session.StreamKindMain)
	if err != nil {
		return EnqueueCommand{}, err
	}
	var payload struct {
		SessionID string `json:"session_id"`
		RuntimeID string `json:"runtime_id"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return EnqueueCommand{}, fmt.Errorf("decode enqueue payload: %w", err)
	}
	sessionID, err := resolvePayloadRouteID(payload.SessionID, payload.RuntimeID, route, FrameTypeEnqueue)
	if err != nil {
		return EnqueueCommand{}, err
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		return EnqueueCommand{}, fmt.Errorf("enqueue text is required")
	}
	return EnqueueCommand{RequestID: requestID, Stream: stream, SessionID: sessionID, Text: text}, nil
}

func DecodeQueueCancelCommand(frame RawFrame) (QueueCancelCommand, error) {
	requestID, stream, route, err := decodeSessionCommand(frame, FrameTypeQueueCancel, session.StreamKindMain)
	if err != nil {
		return QueueCancelCommand{}, err
	}
	var payload struct {
		SessionID string `json:"session_id"`
		RuntimeID string `json:"runtime_id"`
	}
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return QueueCancelCommand{}, fmt.Errorf("decode queue cancel payload: %w", err)
	}
	sessionID, err := resolvePayloadRouteID(payload.SessionID, payload.RuntimeID, route, FrameTypeQueueCancel)
	if err != nil {
		return QueueCancelCommand{}, err
	}
	return QueueCancelCommand{RequestID: requestID, Stream: stream, SessionID: sessionID}, nil
}

func DecodeInterruptCommand(frame RawFrame) (InterruptCommand, error) {
	requestID, stream, route, err := decodeSessionCommand(frame, FrameTypeInterrupt, session.StreamKindMain)
	if err != nil {
		return InterruptCommand{}, err
	}
	var payload struct {
		SessionID string `json:"session_id"`
		RuntimeID string `json:"runtime_id"`
	}
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return InterruptCommand{}, fmt.Errorf("decode interrupt payload: %w", err)
	}
	sessionID, err := resolvePayloadRouteID(payload.SessionID, payload.RuntimeID, route, FrameTypeInterrupt)
	if err != nil {
		return InterruptCommand{}, err
	}
	return InterruptCommand{RequestID: requestID, Stream: stream, SessionID: sessionID}, nil
}

func DecodeUIResponseCommand(frame RawFrame) (UIResponseCommand, error) {
	requestID, stream, route, err := decodeSessionCommand(frame, FrameTypeUIResponse, session.StreamKindUI)
	if err != nil {
		return UIResponseCommand{}, err
	}
	var payload struct {
		SessionID  string          `json:"session_id"`
		RuntimeID  string          `json:"runtime_id"`
		ResponseTo string          `json:"response_to"`
		Value      json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return UIResponseCommand{}, fmt.Errorf("decode ui.response payload: %w", err)
	}
	sessionID, err := resolvePayloadRouteID(payload.SessionID, payload.RuntimeID, route, FrameTypeUIResponse)
	if err != nil {
		return UIResponseCommand{}, err
	}
	responseTo := strings.TrimSpace(payload.ResponseTo)
	if responseTo == "" {
		return UIResponseCommand{}, fmt.Errorf("ui.response response_to is required")
	}
	if len(payload.Value) == 0 {
		return UIResponseCommand{}, fmt.Errorf("ui.response value is required")
	}
	value := append(json.RawMessage(nil), payload.Value...)
	return UIResponseCommand{RequestID: requestID, Stream: stream, SessionID: sessionID, ResponseTo: responseTo, Value: value}, nil
}

func decodeSessionCommand(frame RawFrame, want FrameType, kind session.StreamKind) (string, StreamName, session.StreamRoute, error) {
	if err := frame.Validate(); err != nil {
		return "", "", session.StreamRoute{}, err
	}
	if frame.Type != want {
		return "", "", session.StreamRoute{}, fmt.Errorf("frame type %q is not %s", frame.Type, want)
	}
	if strings.TrimSpace(frame.RequestID) == "" {
		return "", "", session.StreamRoute{}, fmt.Errorf("%s request_id is required", want)
	}
	stream, err := ParseStreamName(frame.Stream)
	if err != nil {
		return "", "", session.StreamRoute{}, err
	}
	route, err := ParseSessionStream(stream)
	if err != nil {
		return "", "", session.StreamRoute{}, fmt.Errorf("%s stream must target a session stream: %w", want, err)
	}
	if route.Kind() != kind {
		return "", "", session.StreamRoute{}, fmt.Errorf("%s stream must target session stream kind %q", want, kind)
	}
	return frame.RequestID, stream, route, nil
}

func resolvePayloadRouteID(rawSessionID, rawRuntimeID string, route session.StreamRoute, command FrameType) (session.SessionID, error) {
	sessionID, err := validatePayloadSessionID(rawSessionID, route, command)
	if err != nil {
		return "", err
	}
	runtimeID := strings.TrimSpace(rawRuntimeID)
	if runtimeID == "" {
		return sessionID, nil
	}
	routeID, err := session.ParseSessionID(runtimeID)
	if err != nil {
		return "", fmt.Errorf("%s payload runtime_id: %w", command, err)
	}
	return routeID, nil
}

func validatePayloadSessionID(raw string, route session.StreamRoute, command FrameType) (session.SessionID, error) {
	sessionID, err := session.ParseSessionID(raw)
	if err != nil {
		return "", fmt.Errorf("%s payload session_id: %w", command, err)
	}
	if sessionID != route.SessionID() {
		return "", fmt.Errorf("%s payload session_id %q does not match stream session %q", command, sessionID, route.SessionID())
	}
	return sessionID, nil
}
