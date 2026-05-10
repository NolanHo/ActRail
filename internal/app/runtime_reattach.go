package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"actrail/internal/domain/session"
)

const piAgentGRPCReattachTimeout = 500 * time.Millisecond

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
	for _, record := range s.registry.ListAll() {
		if !shouldReattachPIAgentGRPC(record) {
			continue
		}
		attachCtx, cancel := context.WithTimeout(ctx, piAgentGRPCReattachTimeout)
		attached, err := s.launcher.AttachPIAgentGRPC(attachCtx, runtimeLaunchRequest{
			SessionID:       record.identity.SessionID(),
			Backend:         record.identity.Backend(),
			CWD:             record.cwd,
			Provider:        record.provider,
			Model:           record.model,
			ReasoningEffort: record.reasoningEffort,
			PIAgentGRPC:     true,
			AttachOnly:      true,
		})
		cancel()
		if err != nil {
			if markErr := s.markPIAgentGRPCReattachFailed(record.identity.SessionID(), record.transport.GenerationID, "pi_agent_grpc_unavailable"); markErr != nil {
				return markErr
			}
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
	return transportSnapshotPIAgentGRPCAttached()
}

func (s *Stub) markPIAgentGRPCReattachFailed(sessionID session.SessionID, generationID, reason string) error {
	if _, ok, err := s.registry.MarkRuntimeCompleted(sessionID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("session %q not found while marking grpc runtime ended", sessionID)
	}
	_, ok, err := s.registry.SetTransport(sessionID, SessionTransportSnapshot{
		GenerationID: strings.TrimSpace(generationID),
		State:        SessionTransportStateEnded,
		Reason:       strings.TrimSpace(reason),
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session %q not found while marking grpc transport ended", sessionID)
	}
	s.runtimeAgentMu.Lock()
	s.runtimeAgentRunning[sessionID] = false
	s.runtimeAgentMu.Unlock()
	return nil
}

func shouldReattachPIAgentGRPC(record sessionRecord) bool {
	if record.identity.Backend() != session.BackendPI || record.identity.Historical() || record.transport.ResetRequired {
		return false
	}
	if record.runtimeAgentRunning || record.runtime.UsesPIAgentGRPC() || record.transport.State == SessionTransportStateAttached {
		return true
	}
	return record.transport.State == SessionTransportStateEnded && record.transport.Reason == "helper_binding_missing"
}
