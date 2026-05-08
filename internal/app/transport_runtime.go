package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"actrail/internal/adapters/iod"
	"actrail/internal/domain/session"
)

func transportSnapshotAttached(generationID iod.GenerationID) SessionTransportSnapshot {
	return SessionTransportSnapshot{
		GenerationID: strings.TrimSpace(generationID.String()),
		State:        SessionTransportStateAttached,
	}
}

func transportSnapshotPIAgentGRPCStarting() SessionTransportSnapshot {
	return SessionTransportSnapshot{State: SessionTransportStateStarting, GenerationID: "pi_agent_grpc", Reason: "pi_agent_grpc_starting"}
}

func transportSnapshotPIAgentGRPCAttached() SessionTransportSnapshot {
	return SessionTransportSnapshot{State: SessionTransportStateAttached, GenerationID: "pi_agent_grpc", Reason: "pi_agent_grpc"}
}

func transportSnapshotPIAgentGRPCFailed(reason string) SessionTransportSnapshot {
	return SessionTransportSnapshot{State: SessionTransportStateFailed, GenerationID: "pi_agent_grpc", Reason: strings.TrimSpace(reason)}
}

func transportSnapshotCodexStarting() SessionTransportSnapshot {
	return SessionTransportSnapshot{State: SessionTransportStateStarting, GenerationID: "codex_app_server", Reason: "codex_thread_starting"}
}

func transportSnapshotCodexAttached() SessionTransportSnapshot {
	return SessionTransportSnapshot{State: SessionTransportStateAttached, GenerationID: "codex_app_server", Reason: "codex_thread"}
}

func transportSnapshotEnded(generationID iod.GenerationID, reason string) SessionTransportSnapshot {
	return SessionTransportSnapshot{
		GenerationID: strings.TrimSpace(generationID.String()),
		State:        SessionTransportStateEnded,
		Reason:       strings.TrimSpace(reason),
	}
}

func transportSnapshotBroken(generationID iod.GenerationID, reason string, resetRequired bool) SessionTransportSnapshot {
	return SessionTransportSnapshot{
		GenerationID:  strings.TrimSpace(generationID.String()),
		State:         SessionTransportStateBroken,
		ResetRequired: resetRequired,
		Reason:        strings.TrimSpace(reason),
	}
}

func transportSnapshotFromFence(fence helperFence) (SessionTransportSnapshot, bool) {
	switch fence.Reason {
	case helperFenceAttachFailed:
		return transportSnapshotEnded(fence.GenerationID, "helper_not_running"), true
	case helperFenceHelloProofMismatch, helperFenceReplayFailed, helperFenceReplayGap, helperFenceReplayCorruptTail, helperFenceCurrentGenerationUnbound:
		return transportSnapshotBroken(fence.GenerationID, string(fence.Reason), true), true
	default:
		return SessionTransportSnapshot{}, false
	}
}

func (s *Stub) setSessionTransport(sessionID session.SessionID, transport SessionTransportSnapshot) (SessionTransportSnapshot, error) {
	updated, ok, err := s.registry.SetTransport(sessionID, transport)
	if err != nil {
		return SessionTransportSnapshot{}, err
	}
	if !ok {
		return SessionTransportSnapshot{}, nil
	}
	return updated, nil
}

func (s *Stub) setSessionTransportAttached(sessionID session.SessionID, generationID iod.GenerationID) error {
	_, err := s.setSessionTransport(sessionID, transportSnapshotAttached(generationID))
	return err
}

func (s *Stub) markSessionGenerationEnded(sessionID session.SessionID, generationID iod.GenerationID, reason string) error {
	if err := s.clearRuntimeTerminalState(sessionID); err != nil {
		return err
	}
	_, err := s.setSessionTransport(sessionID, transportSnapshotEnded(generationID, reason))
	if err != nil {
		return err
	}
	if err := s.emitTransportDiagnostic(sessionID, generationID, "generation_ended", reason, false); err != nil {
		return err
	}
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) markSessionGenerationBroken(sessionID session.SessionID, generationID iod.GenerationID, reason string) error {
	if err := s.clearRuntimeTerminalState(sessionID); err != nil {
		return err
	}
	updated, err := s.setSessionTransport(sessionID, transportSnapshotBroken(generationID, reason, false))
	if err != nil {
		return err
	}
	if updated.GenerationID != "" {
		s.emitGenerationBroken(sessionID, updated.GenerationID, updated.Reason)
	}
	if err := s.emitTransportDiagnostic(sessionID, generationID, "generation_broken", reason, false); err != nil {
		return err
	}
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) markSessionTransportResetRequired(sessionID session.SessionID, generationID iod.GenerationID, reason string) error {
	if err := s.clearRuntimeTerminalState(sessionID); err != nil {
		return err
	}
	updated, err := s.setSessionTransport(sessionID, transportSnapshotBroken(generationID, reason, true))
	if err != nil {
		return err
	}
	if updated.GenerationID != "" {
		s.emitTransportResetRequired(sessionID, updated.GenerationID, updated.Reason)
	}
	if err := s.emitTransportDiagnostic(sessionID, generationID, "transport_reset_required", reason, true); err != nil {
		return err
	}
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) clearRuntimeTerminalState(sessionID session.SessionID) error {
	if s == nil {
		return nil
	}
	_, _, err := s.registry.MarkRuntimeCompleted(sessionID)
	return err
}

func (s *Stub) emitTransportDiagnostic(sessionID session.SessionID, generationID iod.GenerationID, eventType, reason string, resetRequired bool) error {
	resolvedReason := strings.TrimSpace(reason)
	if resolvedReason == "" {
		resolvedReason = eventType
	}
	committed, err := s.AppendSessionMessage(sessionID, "system", "pi_event", fmt.Sprintf("IOD %s: %s", strings.ReplaceAll(eventType, "_", " "), resolvedReason))
	if err != nil {
		return err
	}
	committed.Role = ""
	committed.Type = "pi_event"
	committed.Summary = "IOD " + strings.ReplaceAll(eventType, "_", " ")
	committed.Details = map[string]any{
		"raw_type":       "iod_transport_diagnostic",
		"event_type":     eventType,
		"generation_id":  strings.TrimSpace(generationID.String()),
		"reason":         resolvedReason,
		"reset_required": resetRequired,
	}
	s.emitMessageCommit(sessionID, "", committed)
	return nil
}

func (s *Stub) applyPITransportFact(sessionID session.SessionID, generationID iod.GenerationID, fact iod.HelperFact) error {
	switch fact.FactKind {
	case iod.FactChildExit:
		return s.markSessionGenerationEnded(sessionID, generationID, iod.FactChildExit.String())
	case iod.FactGenerationBreak:
		reason, err := helperGenerationBreakReason(fact)
		if err != nil {
			return err
		}
		return s.markSessionGenerationBroken(sessionID, generationID, reason)
	default:
		return nil
	}
}

func (s *Stub) applyPIGenerationBreakPacket(sessionID session.SessionID, packet iod.GenerationBreakPacket) error {
	return s.markSessionGenerationBroken(sessionID, packet.GenerationID, packet.Reason.String())
}

func (s *Stub) applyPITransportPacket(sessionID session.SessionID, packet any) error {
	switch v := packet.(type) {
	case iod.StatePacket:
		return s.applyPITransportFact(sessionID, v.GenerationID, v.Fact)
	case iod.ReplayItemPacket:
		return s.applyPITransportFact(sessionID, v.GenerationID, v.Item.Fact)
	case iod.GenerationBreakPacket:
		return s.applyPIGenerationBreakPacket(sessionID, v)
	default:
		return nil
	}
}

func (s *Stub) handlePIHelperReadError(sessionID session.SessionID, err error) {
	if s == nil || err == nil {
		return
	}
	state, stateErr := s.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if stateErr != nil {
		return
	}
	if state.Transport.State == SessionTransportStateEnded || state.Transport.State == SessionTransportStateBroken {
		return
	}
	generationID := mustTransportGenerationID(state.Transport.GenerationID)
	if generationID == "" {
		return
	}
	_ = s.markSessionTransportResetRequired(sessionID, generationID, iod.GenerationBreakAttachLost.String())
}

func helperGenerationBreakReason(fact iod.HelperFact) (string, error) {
	var payload struct {
		Reason iod.GenerationBreakReason `json:"reason"`
	}
	if len(fact.Payload) == 0 {
		return "", fmt.Errorf("generation break fact payload is required")
	}
	if err := json.Unmarshal(fact.Payload, &payload); err != nil {
		return "", fmt.Errorf("decode generation break payload: %w", err)
	}
	if err := payload.Reason.Validate(); err != nil {
		return "", err
	}
	return payload.Reason.String(), nil
}

func mustTransportGenerationID(raw string) iod.GenerationID {
	generationID, err := iod.NewGenerationID(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return generationID
}
