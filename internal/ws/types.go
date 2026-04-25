package ws

import "time"

type Frame struct {
	Type      string  `json:"type"`
	ID        string  `json:"id,omitempty"`
	RequestID string  `json:"request_id,omitempty"`
	TS        float64 `json:"ts"`
	Stream    string  `json:"stream"`
	Payload   any     `json:"payload"`
}

type HelloPayload struct {
	ProtocolVersion     int    `json:"protocol_version"`
	ConnectionID        string `json:"connection_id"`
	HeartbeatIntervalMS int    `json:"heartbeat_interval_ms"`
	ResumeBufferEvents  int    `json:"resume_buffer_events"`
}

type HeartbeatPayload struct {
	ConnectionID string `json:"connection_id"`
}

func UnixTS(t time.Time) float64 {
	return float64(t.UnixNano()) / float64(time.Second)
}
