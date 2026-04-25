package ws

import (
	"fmt"
	"time"
)

type HelloPayload struct {
	ProtocolVersion     int    `json:"protocol_version"`
	ConnectionID        string `json:"connection_id"`
	HeartbeatIntervalMS int    `json:"heartbeat_interval_ms"`
	ResumeBufferEvents  int    `json:"resume_buffer_events"`
}

type AckPayload struct {
	RequestID string `json:"request_id"`
	Accepted  bool   `json:"accepted"`
	Command   string `json:"command"`
}

type ErrorPayload struct {
	RequestID string    `json:"request_id,omitempty"`
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Field     string    `json:"field,omitempty"`
}

type HeartbeatPayload struct {
	ConnectionID string `json:"connection_id"`
}

type ResetRequiredPayload struct {
	SessionID string   `json:"session_id,omitempty"`
	Reason    string   `json:"reason"`
	Refresh   []string `json:"refresh,omitempty"`
}

func NewHelloFrame(now time.Time, id, connectionID string, protocolVersion, heartbeatIntervalMS, resumeBufferEvents int) Frame {
	return Frame{
		Type:   FrameTypeHello,
		ID:     id,
		TS:     UnixTS(now),
		Stream: SystemStream.String(),
		Payload: HelloPayload{
			ProtocolVersion:     protocolVersion,
			ConnectionID:        connectionID,
			HeartbeatIntervalMS: heartbeatIntervalMS,
			ResumeBufferEvents:  resumeBufferEvents,
		},
	}
}

func NewAckFrame(now time.Time, id string, stream StreamName, requestID string, command FrameType) Frame {
	return Frame{
		Type:   FrameTypeAck,
		ID:     id,
		TS:     UnixTS(now),
		Stream: stream.String(),
		Payload: AckPayload{
			RequestID: requestID,
			Accepted:  true,
			Command:   string(command),
		},
	}
}

func NewErrorFrame(now time.Time, id string, stream StreamName, requestID string, code ErrorCode, message, field string) Frame {
	return Frame{
		Type:   FrameTypeError,
		ID:     id,
		TS:     UnixTS(now),
		Stream: stream.String(),
		Payload: ErrorPayload{
			RequestID: requestID,
			Code:      code,
			Message:   message,
			Field:     field,
		},
	}
}

func NewHeartbeatFrame(now time.Time, id, connectionID string) Frame {
	return Frame{
		Type:   FrameTypeHeartbeat,
		ID:     id,
		TS:     UnixTS(now),
		Stream: SystemStream.String(),
		Payload: HeartbeatPayload{
			ConnectionID: connectionID,
		},
	}
}

func NewResetRequiredFrame(now time.Time, id string, stream StreamName, reason string) (Frame, error) {
	refresh, ok := RefreshPathsForStream(stream)
	if !ok {
		return Frame{}, fmt.Errorf("stream %q does not support reset refresh paths", stream)
	}
	route, err := ParseSessionStream(stream)
	if err != nil {
		return Frame{}, err
	}
	return Frame{
		Type:   FrameTypeTransportResetRequired,
		ID:     id,
		TS:     UnixTS(now),
		Stream: stream.String(),
		Payload: ResetRequiredPayload{
			SessionID: route.SessionID().String(),
			Reason:    reason,
			Refresh:   refresh,
		},
	}, nil
}
