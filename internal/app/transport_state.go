package app

import (
	"strings"

	"actrail/internal/domain/session"
)

type SessionTransportState string

const (
	SessionTransportStateStarting SessionTransportState = "starting"
	SessionTransportStateAttached SessionTransportState = "attached"
	SessionTransportStateSilent   SessionTransportState = "silent"
	SessionTransportStateStalled  SessionTransportState = "stalled"
	SessionTransportStateBroken   SessionTransportState = "broken"
	SessionTransportStateFailed   SessionTransportState = "failed"
	SessionTransportStateEnded    SessionTransportState = "ended"
)

func isRecoverableTransportProbeIssue(transport SessionTransportSnapshot) bool {
	reason := strings.TrimSpace(transport.Reason)
	return reason == "get_state failed" || reason == "rpc unavailable"
}

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

func (s *Stub) sessionProbing(record sessionRecord) bool {
	return sessionProbing(record)
}

func sessionProbing(record sessionRecord) bool {
	transport := sessionTransportSnapshot(record)
	if record.identity.Historical() || transport.ResetRequired || transport.State != SessionTransportStateAttached || record.state.Busy() {
		return false
	}
	if record.identity.Backend() == session.BackendPI {
		return record.runtime.protocol == runtimeProtocolPIRPC && record.runtime.helper != nil
	}
	return record.identity.Backend() == session.BackendCodex && record.runtime.protocol == runtimeProtocolCodexRPC && record.runtime.codex != nil
}

func sessionTransportSnapshot(record sessionRecord) SessionTransportSnapshot {
	snapshot := record.transport
	if snapshot.State == SessionTransportStateAttached && !record.identity.Historical() && record.runtime.helper == nil && record.runtime.piAgentGRPC == nil && record.runtime.handle == nil {
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
