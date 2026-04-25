package ws

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type FrameType string

const (
	FrameTypeHello                  FrameType = "hello"
	FrameTypeAck                    FrameType = "ack"
	FrameTypeError                  FrameType = "error"
	FrameTypeHeartbeat              FrameType = "heartbeat"
	FrameTypeTransportResetRequired FrameType = "transport.reset_required"
	FrameTypeSubscribe              FrameType = "subscribe"
	FrameTypeUnsubscribe            FrameType = "unsubscribe"
	FrameTypePing                   FrameType = "ping"
	FrameTypeSend                   FrameType = "send"
	FrameTypeEnqueue                FrameType = "enqueue"
	FrameTypeInterrupt              FrameType = "interrupt"
	FrameTypeUIResponse             FrameType = "ui.response"
)

type ErrorCode string

const (
	ErrorCodeUnauthorized           ErrorCode = "unauthorized"
	ErrorCodeNotFound               ErrorCode = "not_found"
	ErrorCodeInvalidRequest         ErrorCode = "invalid_request"
	ErrorCodeConflict               ErrorCode = "conflict"
	ErrorCodeUnsupported            ErrorCode = "unsupported"
	ErrorCodeInternal               ErrorCode = "internal_error"
	ErrorCodeTransportResetRequired ErrorCode = "transport_reset_required"
)

type Frame struct {
	Type      FrameType `json:"type"`
	ID        string    `json:"id,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	TS        float64   `json:"ts"`
	Stream    string    `json:"stream"`
	Payload   any       `json:"payload"`
}

func (f Frame) Validate() error {
	if strings.TrimSpace(string(f.Type)) == "" {
		return fmt.Errorf("frame type is required")
	}
	if _, err := ParseStreamName(f.Stream); err != nil {
		return err
	}
	if f.TS < 0 {
		return fmt.Errorf("frame timestamp must be non-negative")
	}
	return nil
}

type RawFrame struct {
	Type      FrameType       `json:"type"`
	ID        string          `json:"id,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	TS        float64         `json:"ts"`
	Stream    string          `json:"stream"`
	Payload   json.RawMessage `json:"payload"`
}

func (f RawFrame) Validate() error {
	if strings.TrimSpace(string(f.Type)) == "" {
		return fmt.Errorf("frame type is required")
	}
	if _, err := ParseStreamName(f.Stream); err != nil {
		return err
	}
	if f.TS < 0 {
		return fmt.Errorf("frame timestamp must be non-negative")
	}
	if len(f.Payload) == 0 {
		return fmt.Errorf("frame payload is required")
	}
	return nil
}

type Codec struct{}

func (Codec) Encode(frame Frame) ([]byte, error) {
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(frame)
}

func (Codec) Decode(data []byte) (RawFrame, error) {
	var frame RawFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return RawFrame{}, err
	}
	if err := frame.Validate(); err != nil {
		return RawFrame{}, err
	}
	return frame, nil
}

func UnixTS(t time.Time) float64 {
	return float64(t.UnixNano()) / float64(time.Second)
}
