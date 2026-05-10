package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func TestStaleHelperReadErrorDoesNotBreakCurrentCodexGeneration(t *testing.T) {
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	oldGeneration := mustHelperGenerationID(t, "g_old_helper_read_error")
	newGeneration := mustHelperGenerationID(t, "g_new_helper_read_error")
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper:   &runtimeIODHelper{generationID: newGeneration},
			codex:    newCodexRuntimeState(session.BackendCodex),
		}
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-helper-read-error")
		record.transport = transportSnapshotAttached(newGeneration)
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	svc.handleHelperReadError(sessionID, session.BackendCodex, oldGeneration, errors.New("old stream closed"))

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.GenerationID != newGeneration.String() || state.Transport.ResetRequired {
		t.Fatalf("SessionState() after stale read error = %+v, want current generation attached", state)
	}
	if state.RuntimeState != string(codexRuntimePhaseIdle) {
		t.Fatalf("SessionState().RuntimeState = %q, want idle", state.RuntimeState)
	}
}

func TestCurrentHelperReadErrorMarksCodexTransportResetRequired(t *testing.T) {
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_current_helper_read_error")
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper:   &runtimeIODHelper{generationID: generationID},
			codex:    newCodexRuntimeState(session.BackendCodex),
		}
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-helper-read-error")
		record.transport = transportSnapshotAttached(generationID)
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	svc.handleHelperReadError(sessionID, session.BackendCodex, generationID, errors.New("current stream closed"))

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateBroken || !state.Transport.ResetRequired || state.Transport.Reason != iod.GenerationBreakAttachLost.String() {
		t.Fatalf("SessionState() after current read error = %+v, want reset-required attach_lost", state)
	}
	if state.RuntimeState != string(codexRuntimePhaseFailed) || state.RuntimeStateReason != iod.GenerationBreakAttachLost.String() {
		t.Fatalf("SessionState() runtime = (%q, %q), want failed attach_lost", state.RuntimeState, state.RuntimeStateReason)
	}
}
