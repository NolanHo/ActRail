package realtime

import (
	"encoding/json"
	"time"
)

const SystemStream = "system"

type Event struct {
	Type       string
	Stream     string
	UnixMillis int64
	Payload    any
}

func (e Event) PayloadJSON() json.RawMessage {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return payload
}

func (e Event) EventUnixMillis(now func() time.Time) int64 {
	if e.UnixMillis > 0 {
		return e.UnixMillis
	}
	if now == nil {
		now = time.Now
	}
	return now().UnixMilli()
}

type Observer interface {
	ObserveEvent(Event)
}
