package ws

import (
	"fmt"
	"time"
)

type HeartbeatState struct {
	interval       time.Duration
	lastServerSend time.Time
	lastClientPing time.Time
}

func NewHeartbeatState(interval time.Duration, now time.Time) (HeartbeatState, error) {
	if interval <= 0 {
		return HeartbeatState{}, fmt.Errorf("heartbeat interval must be positive")
	}
	return HeartbeatState{interval: interval, lastServerSend: now, lastClientPing: now}, nil
}

func (h *HeartbeatState) ObserveServerSend(now time.Time) {
	h.lastServerSend = now
}

func (h *HeartbeatState) ObserveClientPing(now time.Time) {
	h.lastClientPing = now
}

func (h HeartbeatState) NextServerHeartbeat() time.Time {
	return h.lastServerSend.Add(h.interval)
}

func (h HeartbeatState) ServerHeartbeatDue(now time.Time) bool {
	return !now.Before(h.NextServerHeartbeat())
}

func (h HeartbeatState) LastClientPing() time.Time {
	return h.lastClientPing
}

func (h HeartbeatState) Interval() time.Duration {
	return h.interval
}
