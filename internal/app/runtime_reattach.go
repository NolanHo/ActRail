package app

import (
	"context"
	"fmt"

	"actrail/internal/domain/session"
)

func (s *Stub) reattachSurvivingRuntimes(ctx context.Context) error {
	if err := s.reattachSurvivingPIAgentGRPCRuntimes(ctx); err != nil {
		return err
	}
	return s.reattachSurvivingHelpers(ctx)
}

func (s *Stub) reattachSurvivingPIAgentGRPCRuntimes(ctx context.Context) error {
	if s == nil || s.launcher == nil {
		return nil
	}
	for _, record := range s.registry.List() {
		if !shouldReattachPIAgentGRPC(record) {
			continue
		}
		attached, err := s.launcher.AttachPIAgentGRPC(ctx, runtimeLaunchRequest{
			SessionID:       record.identity.SessionID(),
			Backend:         record.identity.Backend(),
			CWD:             record.cwd,
			Provider:        record.provider,
			Model:           record.model,
			ReasoningEffort: record.reasoningEffort,
			PIAgentGRPC:     true,
			AttachOnly:      true,
		})
		if err != nil {
			if _, ok, markErr := s.registry.MarkRuntimeCompleted(record.identity.SessionID()); markErr != nil {
				return markErr
			} else if !ok {
				return fmt.Errorf("session %q not found while marking grpc runtime ended", record.identity.SessionID())
			}
			s.runtimeAgentMu.Lock()
			s.runtimeAgentRunning[record.identity.SessionID()] = false
			s.runtimeAgentMu.Unlock()
			continue
		}
		updated, ok, err := s.registry.Update(record.identity.SessionID(), false, func(next *sessionRecord) error {
			next.runtime = attached
			next.transport = piAgentGRPCTransportSnapshot()
			return nil
		})
		if err != nil {
			_ = attached.Kill(context.Background())
			return err
		}
		if !ok {
			_ = attached.Kill(context.Background())
			return fmt.Errorf("session %q not found while reattaching grpc runtime", record.identity.SessionID())
		}
		s.startRuntimeIngest(updated.identity.SessionID(), updated.identity.Backend(), attached)
	}
	return nil
}

func piAgentGRPCTransportSnapshot() SessionTransportSnapshot {
	return SessionTransportSnapshot{State: SessionTransportStateAttached, GenerationID: "pi_agent_grpc", Reason: "pi_agent_grpc"}
}

func shouldReattachPIAgentGRPC(record sessionRecord) bool {
	if !record.runtimeAgentRunning || record.identity.Backend() != session.BackendPI || record.identity.Historical() {
		return false
	}
	if record.runtime.UsesPIAgentGRPC() {
		return true
	}
	return record.transport.State == SessionTransportStateAttached
}
