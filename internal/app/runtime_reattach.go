package app

import (
	"context"
	"fmt"

	"actrail/internal/domain/session"
)

func (s *Stub) reattachSurvivingRuntimes(ctx context.Context) error {
	if err := s.reattachSurvivingHelpers(ctx); err != nil {
		return err
	}
	return s.reattachSurvivingPIAgentGRPCRuntimes(ctx)
}

func (s *Stub) reattachSurvivingPIAgentGRPCRuntimes(ctx context.Context) error {
	if s == nil || s.registry == nil {
		return nil
	}
	for _, record := range s.registry.List() {
		if record.identity.Historical() || record.identity.Backend() != session.BackendPI {
			continue
		}
		if record.transport.State == SessionTransportStateBroken {
			continue
		}
		if record.runtime.helper != nil || record.runtime.piAgentGRPC != nil {
			continue
		}
		if record.runtime.handle != nil {
			continue
		}
		if !record.runtimeAgentRunning && !record.state.Busy() {
			continue
		}
		attached, err := s.launcher.Launch(ctx, runtimeLaunchRequest{
			SessionID:       record.identity.SessionID(),
			Backend:         record.identity.Backend(),
			CWD:             record.cwd,
			Provider:        record.provider,
			Model:           record.model,
			ReasoningEffort: record.reasoningEffort,
			SessionPath:     record.importedSourcePath,
			PIAgentGRPC:     true,
			AttachOnly:      true,
		})
		if err != nil {
			if record.state.Busy() {
				if _, ok, markErr := s.registry.MarkRuntimeCompleted(record.identity.SessionID()); markErr != nil {
					return markErr
				} else if !ok {
					return fmt.Errorf("session %q not found while clearing grpc startup busy state", record.identity.SessionID())
				}
			}
			if err := s.setRuntimeAgentRunning(record.identity.SessionID(), false); err != nil {
				return err
			}
			continue
		}
		updated, ok, err := s.registry.Update(record.identity.SessionID(), false, func(record *sessionRecord) error {
			record.runtime = attached
			record.transport = SessionTransportSnapshot{State: SessionTransportStateAttached, Reason: "grpc_reattached"}
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
