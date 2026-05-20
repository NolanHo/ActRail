package app

import (
	"context"
	"strings"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

type codexCommandReconcileState struct {
	Reflected bool
	Completed bool
}

func (s *Stub) reconcileOpenCodexCommandsFromRuntimeEvent(sessionID session.SessionID, event pi.Event) {
	if s == nil || event.Message == nil {
		return
	}
	if event.Message.Role == pi.MessageRoleUser {
		s.reconcileOpenCodexCommands(sessionID, codexCommandReconcileState{Reflected: true}, event.Message.Text)
		return
	}
	if event.Message.Role == pi.MessageRoleAssistant && runtimeAssistantMessageCompletesTurn(event) {
		s.reconcileOpenCodexCommands(sessionID, codexCommandReconcileState{Completed: true}, "")
	}
}

func (s *Stub) completeOpenCodexCommandsFromTurnBoundary(sessionID session.SessionID) {
	s.reconcileOpenCodexCommands(sessionID, codexCommandReconcileState{Completed: true}, "")
}

func (s *Stub) reconcileOpenCodexCommandsFromMessages(sessionID session.SessionID, items []SessionMessage, complete bool) {
	if s == nil || len(items) == 0 {
		return
	}
	state := codexCommandReconcileState{}
	reflectedText := ""
	for _, item := range items {
		if item.Role != "user" || item.Kind != "message" {
			continue
		}
		if text := strings.TrimSpace(item.Text); text != "" {
			state.Reflected = true
			reflectedText = text
		}
	}
	if complete {
		state.Completed = true
	}
	if !state.Reflected && !state.Completed {
		return
	}
	s.reconcileOpenCodexCommands(sessionID, state, reflectedText)
}

func (s *Stub) reconcileOpenCodexCommands(sessionID session.SessionID, state codexCommandReconcileState, reflectedText string) bool {
	if s == nil || s.sessionCommandStore == nil || (!state.Reflected && !state.Completed) {
		return false
	}
	commands, err := s.sessionCommandStore.ListOpenCodexSessionCommands(context.Background(), sessionID.String())
	if err != nil {
		_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_command_reconcile", err)
		return false
	}
	changed := false
	for _, command := range recoverableCodexSessionCommands(commands) {
		next := ""
		switch strings.TrimSpace(command.State) {
		case codexCommandPending.String(), codexCommandDispatching.String(), codexCommandAccepted.String():
			if state.Completed {
				next = codexCommandCompleted.String()
			} else if state.Reflected && codexCommandMatchesReflectedText(command, reflectedText) {
				next = codexCommandReflected.String()
			}
		case codexCommandReflected.String():
			if state.Completed {
				next = codexCommandCompleted.String()
			}
		}
		if next == "" {
			continue
		}
		if updated := s.updateCodexSendCommandState(command.CommandID, codexCommandAxis(next), commandRuntimeID(command), ""); updated {
			changed = true
			s.clearCodexOutboundPrompt(sessionID, command.Text)
		}
	}
	if changed {
		s.emitSessionState(sessionID)
	}
	return changed
}

func codexCommandMatchesReflectedText(command sqlitestore.CodexSessionCommandRow, reflectedText string) bool {
	reflected := strings.TrimSpace(reflectedText)
	if reflected == "" {
		return true
	}
	return strings.TrimSpace(command.Text) == reflected
}

func commandRuntimeID(command sqlitestore.CodexSessionCommandRow) session.RuntimeID {
	runtimeID, err := session.NewRuntimeID(strings.TrimSpace(command.RuntimeID))
	if err != nil {
		return ""
	}
	return runtimeID
}

func (s *Stub) reconcileRecoveredCodexCommandFromAuthoritativeState(ctx context.Context, sessionID session.SessionID, command sqlitestore.CodexSessionCommandRow) (bool, bool) {
	reflected, complete := s.recoveredCodexCommandAuthoritativeFileState(ctx, sessionID, command)
	if complete {
		s.reconcileOpenCodexCommands(sessionID, codexCommandReconcileState{Reflected: reflected, Completed: true}, command.Text)
		return true, true
	}
	if reflected {
		s.reconcileOpenCodexCommands(sessionID, codexCommandReconcileState{Reflected: true}, command.Text)
		return true, false
	}
	if s.codexCommandRecoveryRuntimeActive(ctx, sessionID) {
		_, _, _ = s.registry.SetBusy(sessionID, true)
		s.emitSessionState(sessionID)
		return true, false
	}
	return false, false
}

func (s *Stub) recoveredCodexCommandAuthoritativeFileState(ctx context.Context, sessionID session.SessionID, command sqlitestore.CodexSessionCommandRow) (bool, bool) {
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendCodex {
		return false, false
	}
	path, _, err := s.codexSessionFileForRecord(record)
	if err != nil || strings.TrimSpace(path) == "" {
		return false, false
	}
	items, err := codexSessionMessagesFromFile(ctx, path)
	if err != nil {
		_ = s.emitRuntimeControlDiagnostic(sessionID, "codex_command_recovery_session_file", err)
		return false, false
	}
	reflected := false
	for _, item := range items {
		if item.Role == "user" && item.Kind == "message" && strings.TrimSpace(item.Text) == strings.TrimSpace(command.Text) {
			reflected = true
			break
		}
	}
	complete := codexSessionMessagesHaveAuthoritativeCompletion(items) || codexSessionFileHasTaskComplete(ctx, path)
	return reflected, complete
}
