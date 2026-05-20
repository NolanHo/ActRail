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
	record.runtime = s.runtimeForRecord(record)
	if transport, ok := s.helperMissingCodexChildTransport(record); ok {
		return transport
	}
	return sessionTransportSnapshot(record)
}

func (s *Stub) publicSessionTransportSnapshot(record sessionRecord) SessionTransportSnapshot {
	transport := s.sessionTransportSnapshot(record)
	if record.identity.Backend() == session.BackendCodex {
		transport.GenerationID = ""
	}
	return transport
}

func (s *Stub) sessionProbing(record sessionRecord) bool {
	record.runtime = s.runtimeForRecord(record)
	if transport, ok := s.helperMissingCodexChildTransport(record); ok && transport.ResetRequired {
		return false
	}
	return sessionProbing(record)
}

func sessionProbing(record sessionRecord) bool {
	transport := sessionTransportSnapshot(record)
	busy, _ := effectiveBusy(record)
	if record.identity.Historical() || transport.ResetRequired || transport.State != SessionTransportStateAttached || busy {
		return false
	}
	if record.identity.Backend() == session.BackendPI {
		return record.runtime.protocol == runtimeProtocolPIRPC && record.runtime.helper != nil
	}
	return record.identity.Backend() == session.BackendCodex && record.runtime.protocol == runtimeProtocolCodexRPC && record.runtime.codex != nil
}

func sessionTransportSnapshot(record sessionRecord) SessionTransportSnapshot {
	snapshot := record.transport
	if record.identity.Backend() == session.BackendCodex {
		if binding, err := record.runtime.CurrentHelperBinding(record.identity.SessionID()); err == nil && binding != nil && binding.GenerationID != "" {
			boundGenerationID := strings.TrimSpace(binding.GenerationID.String())
			if boundGenerationID != "" && strings.TrimSpace(snapshot.GenerationID) == "codex_app_server" {
				snapshot.GenerationID = boundGenerationID
			}
		}
	}
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
