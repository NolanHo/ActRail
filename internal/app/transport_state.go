package app

import "strings"

type SessionTransportState string

const (
	SessionTransportStateAttached SessionTransportState = "attached"
	SessionTransportStateSilent   SessionTransportState = "silent"
	SessionTransportStateStalled  SessionTransportState = "stalled"
	SessionTransportStateBroken   SessionTransportState = "broken"
	SessionTransportStateEnded    SessionTransportState = "ended"
)

type SessionTransportSnapshot struct {
	GenerationID  string                `json:"generation_id,omitempty"`
	State         SessionTransportState `json:"state"`
	ResetRequired bool                  `json:"reset_required,omitempty"`
	Reason        string                `json:"reason,omitempty"`
}

func (s SessionTransportState) String() string {
	return string(s)
}

func (s *Stub) sessionTransportSnapshot(record sessionRecord) SessionTransportSnapshot {
	return sessionTransportSnapshot(record)
}

func sessionTransportSnapshot(record sessionRecord) SessionTransportSnapshot {
	snapshot := record.transport
	if snapshot.State == SessionTransportStateAttached && !record.identity.Historical() && record.runtime.helper == nil && record.runtime.handle == nil {
		snapshot.State = SessionTransportStateEnded
		if snapshot.Reason == "" {
			snapshot.Reason = "helper_not_running"
		}
	}
	if snapshot.GenerationID == "" {
		if binding, err := record.runtime.CurrentHelperBinding(record.identity.SessionID()); err == nil && binding != nil {
			snapshot.GenerationID = binding.GenerationID.String()
			if snapshot.State == "" {
				snapshot.State = SessionTransportStateAttached
			}
		}
	}
	if snapshot.State == "" {
		switch {
		case record.identity.Historical():
			snapshot.State = SessionTransportStateEnded
		case record.runtime.helper != nil || record.runtime.handle != nil:
			snapshot.State = SessionTransportStateAttached
		default:
			snapshot.State = SessionTransportStateEnded
		}
	}
	if snapshot.Reason != "" {
		snapshot.Reason = strings.TrimSpace(snapshot.Reason)
	}
	return snapshot
}
