package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/session"
)

const codexCommandRecoveryRestartedBeforeAck = "actrail restarted before codex runtime accepted command"

var errRecoveredCodexCommandRuntimeActive = errors.New("codex runtime is still running; recovered command remains pending")

func (s *Stub) recoverOpenCodexSessionCommands(ctx context.Context) {
	if s == nil || s.sessionCommandStore == nil {
		return
	}
	for _, record := range s.registry.ListAll() {
		if record.identity.Backend() != session.BackendCodex || record.archivedAt != nil {
			continue
		}
		commands, err := s.sessionCommandStore.ListOpenCodexSessionCommands(ctx, record.identity.SessionID().String())
		if err != nil {
			_ = s.emitRuntimeControlDiagnostic(record.identity.SessionID(), "codex_command_recovery", err)
			continue
		}
		commands = recoverableCodexSessionCommands(commands)
		if len(commands) == 0 {
			continue
		}
		s.recoverCodexSessionCommands(ctx, record.identity.SessionID(), commands)
	}
}

func recoverableCodexSessionCommands(commands []sqlitestore.CodexSessionCommandRow) []sqlitestore.CodexSessionCommandRow {
	items := make([]sqlitestore.CodexSessionCommandRow, 0, len(commands))
	for _, command := range commands {
		if strings.TrimSpace(command.Kind) != "send" {
			continue
		}
		if strings.TrimSpace(command.MessageID) == "" {
			continue
		}
		items = append(items, command)
	}
	return items
}

func (s *Stub) recoverCodexSessionCommands(ctx context.Context, sessionID session.SessionID, commands []sqlitestore.CodexSessionCommandRow) {
	if s == nil || len(commands) == 0 {
		return
	}
	for _, command := range commands {
		switch strings.TrimSpace(command.State) {
		case codexCommandPending.String():
			if s.tryRecoverPendingCodexSend(ctx, sessionID, command) {
				return
			}
		case codexCommandDispatching.String():
			if s.codexCommandRecoveryRuntimeActive(ctx, sessionID) {
				_, _, _ = s.registry.SetBusy(sessionID, true)
				s.emitSessionState(sessionID)
				return
			}
			s.failRecoveredCodexCommand(sessionID, command, codexCommandRecoveryRestartedBeforeAck)
		case codexCommandAccepted.String(), codexCommandReflected.String():
			if handled, completed := s.reconcileRecoveredCodexCommandFromAuthoritativeState(ctx, sessionID, command); handled {
				if completed {
					return
				}
				return
			}
			s.failRecoveredCodexCommand(sessionID, command, "codex command was not reflected after runtime recovery")
		default:
			continue
		}
	}
}

func (s *Stub) tryRecoverPendingCodexSend(ctx context.Context, sessionID session.SessionID, command sqlitestore.CodexSessionCommandRow) bool {
	text := strings.TrimSpace(command.Text)
	if text == "" || strings.TrimSpace(command.CommandID) == "" {
		s.failRecoveredCodexCommand(sessionID, command, "codex command recovery skipped empty command")
		return false
	}
	var (
		runtime          sessionRuntime
		recordAtDispatch sessionRecord
		runtimeID        session.RuntimeID
	)
	if err := s.withSessionInputLock(sessionID, func(record sessionRecord) error {
		record.runtime = s.runtimeForRecord(record)
		if s.activeWaitForSession(sessionID) != nil {
			return Conflict("session is waiting on user")
		}
		if err := transportControlError(s.sessionTransportSnapshot(record)); err != nil {
			return err
		}
		if err := preflightRuntimeSend(record.runtime); err != nil {
			return err
		}
		if record.runtime.protocol == runtimeProtocolCodexRPC {
			active, err := s.codexAuthoritativeActiveTurn(ctx, record)
			if err != nil {
				return err
			}
			if active {
				_, _, _ = s.registry.SetBusy(sessionID, true)
				return errRecoveredCodexCommandRuntimeActive
			}
		}
		runtime = record.runtime
		recordAtDispatch = record
		runtimeID, _ = record.identity.RuntimeID()
		if _, ok, err := s.registry.SetBusyIfCurrent(sessionID, runtimeID, true); err != nil {
			return err
		} else if !ok {
			return errRuntimeChanged
		}
		return nil
	}); err != nil {
		if errors.Is(err, errRecoveredCodexCommandRuntimeActive) {
			_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseRunning, "codex_authoritative_running", "codex_command_recovery_pre_send_state")
			s.emitSessionState(sessionID)
			return true
		}
		_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_command_recovery_pre_send_state", err)
		s.failRecoveredCodexCommand(sessionID, command, err.Error())
		return false
	}
	if runtime.protocol == runtimeProtocolCodexRPC {
		s.trackCodexOutboundPrompt(sessionID, text)
		_ = s.transitionCodexRuntimeIfCurrent(sessionID, runtimeID, codexRuntimePhaseSending, "codex_recovered_sending", "codex_command_recovery_send")
	}
	s.startAsyncRuntimeSend(sessionID, runtimeID, command.CommandID, text, command.FollowUp, runtime, recordAtDispatch)
	s.emitSessionState(sessionID)
	return true
}

func (s *Stub) codexCommandRecoveryRuntimeActive(ctx context.Context, sessionID session.SessionID) bool {
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return false
	}
	record.runtime = s.runtimeForRecord(record)
	if record.runtime.protocol != runtimeProtocolCodexRPC {
		return false
	}
	transport := s.sessionTransportSnapshot(record)
	if transport.State != SessionTransportStateAttached {
		return false
	}
	active, err := s.codexAuthoritativeActiveTurn(ctx, record)
	if err != nil {
		_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_command_recovery_state", err)
		return true
	}
	return active
}

func (s *Stub) failRecoveredCodexCommand(sessionID session.SessionID, command sqlitestore.CodexSessionCommandRow, reason string) {
	if s == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "codex command recovery failed"
	}
	runtimeID := strings.TrimSpace(command.RuntimeID)
	_, _ = s.sessionCommandStore.UpdateCodexSessionCommandState(context.Background(), command.CommandID, codexCommandFailed.String(), runtimeID, reason, time.Now().UTC())
	if strings.TrimSpace(command.Text) != "" {
		s.clearCodexOutboundPrompt(sessionID, command.Text)
	}
	if runtimeIDValue, err := session.NewRuntimeID(runtimeID); err == nil {
		if !s.recoveredCommandMatchesCurrentRuntime(sessionID, runtimeIDValue) {
			_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_command_recovery", fmt.Errorf("%s", reason))
			s.emitSessionState(sessionID)
			return
		}
		_ = s.transitionCodexRuntimeIfCurrent(sessionID, runtimeIDValue, codexRuntimePhaseFailed, reason, "codex_command_recovery_failed")
		_, _, _ = s.registry.SetBusyIfCurrent(sessionID, runtimeIDValue, false)
	} else {
		_ = s.transitionCodexRuntime(sessionID, codexRuntimePhaseFailed, reason, "codex_command_recovery_failed")
		_, _, _ = s.registry.SetBusy(sessionID, false)
	}
	_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_command_recovery", fmt.Errorf("%s", reason))
	s.emitSessionState(sessionID)
}

func (s *Stub) recoveredCommandMatchesCurrentRuntime(sessionID session.SessionID, commandRuntimeID session.RuntimeID) bool {
	if commandRuntimeID == "" {
		return true
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return false
	}
	currentRuntimeID, ok := record.identity.RuntimeID()
	if !ok {
		return true
	}
	return currentRuntimeID == commandRuntimeID
}
