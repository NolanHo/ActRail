package app

import (
	"bufio"
	"context"
	"io"
	"strings"

	"actrail/internal/adapters/piagentgrpc"
	"actrail/internal/domain/session"
)

const (
	maxRuntimeLineBytes = 1 << 20
)

func runtimeProjectionSupported(backend session.Backend) bool {
	switch backend {
	case session.BackendPI, session.BackendCodex:
		return true
	default:
		return false
	}
}

func (s *Stub) startRuntimeIngest(sessionID session.SessionID, backend session.Backend, runtime sessionRuntime) {
	if s == nil || !runtimeProjectionSupported(backend) {
		return
	}
	if backend == session.BackendPI {
		if record, ok := s.registry.Lookup(sessionID); ok && record.state.Busy() {
			_ = s.setRuntimeAgentRunning(sessionID, true)
		}
		s.startPIRPCStatePolling(sessionID, runtime)
	}
	if runtime.helper != nil {
		go s.readRuntimeHelper(sessionID, backend, runtime.helper)
		return
	}
	if runtime.piAgentGRPC != nil {
		go s.readPIAgentGRPC(sessionID, runtime.piAgentGRPC)
		return
	}
	if runtime.handle == nil {
		return
	}
	for _, src := range runtimeOutputSources(runtime) {
		if src == nil {
			continue
		}
		go s.readRuntimeOutput(sessionID, backend, src)
	}
}

func runtimeOutputSources(runtime sessionRuntime) []io.Reader {
	if runtime.handle == nil {
		return nil
	}
	if pty := runtime.handle.PTY(); pty != nil {
		return []io.Reader{pty}
	}
	sources := make([]io.Reader, 0, 2)
	if stdout := runtime.handle.Stdout(); stdout != nil {
		sources = append(sources, stdout)
	}
	if stderr := runtime.handle.Stderr(); stderr != nil {
		sources = append(sources, stderr)
	}
	return sources
}

func (s *Stub) readRuntimeOutput(sessionID session.SessionID, backend session.Backend, src io.Reader) {
	decoder := runtimeEventDecoder{backend: backend}
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRuntimeLineBytes)
	for scanner.Scan() {
		_ = s.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine(scanner.Bytes()))
	}
	_ = s.orphanActiveWaits(context.Background(), &sessionID)
}

func (s *Stub) readPIAgentGRPC(sessionID session.SessionID, client *piagentgrpc.Client) {
	if s == nil || client == nil {
		return
	}
	if state, err := client.GetState(context.Background()); err == nil {
		_ = s.applyRuntimeProjection(sessionID, runtimeProjection{piRPCState: piRPCStateSnapshotFromGRPC(state)})
	}
	decoder := runtimeEventDecoder{backend: session.BackendPI}
	_ = client.Subscribe(context.Background(), func(event piagentgrpc.Event) error {
		if event.SessionBoundary != nil {
			if state, err := client.GetState(context.Background()); err == nil {
				_ = s.applyRuntimeProjection(sessionID, runtimeProjection{piRPCState: piRPCStateSnapshotFromGRPC(state)})
			}
			return nil
		}
		projection := decoder.decodeRuntimeLine(event.PayloadJSON)
		return s.applyRuntimeProjection(sessionID, projection)
	})
	_ = s.orphanActiveWaits(context.Background(), &sessionID)
}

func (s *Stub) applyRuntimeProjection(sessionID session.SessionID, projection runtimeProjection) error {
	if len(projection.events) > 0 {
		if err := s.applyPIEvents(sessionID, projection.events); err != nil {
			return err
		}
		projection.events = nil
	}
	if projection.codexInitialized {
		s.noteCodexInitialized(sessionID)
	}
	if strings.TrimSpace(projection.codexThreadID) != "" {
		s.noteCodexThreadID(sessionID, projection.codexThreadID, projection.codexSessionPath)
	}
	codexMainProjection := s.codexThreadIDInMainThread(sessionID, projection.codexThreadID)
	if codexMainProjection && !projection.clearCodexTurn && strings.TrimSpace(projection.codexTurnID) != "" {
		s.noteCodexTurnID(sessionID, projection.codexTurnID)
		s.flushCodexPendingInterrupt(sessionID)
	}
	if codexMainProjection && projection.clearCodexTurn {
		s.clearCodexTurnID(sessionID, projection.codexTurnID)
	}
	if codexMainProjection && (strings.TrimSpace(projection.model) != "" || strings.TrimSpace(projection.provider) != "" || projection.contextUsage != nil || projection.turnTiming != nil) {
		if record, ok, err := s.registry.UpdateRuntimeMetadata(sessionID, projection.model, projection.provider, projection.contextUsage, projection.turnTiming); err == nil && ok {
			if strings.TrimSpace(projection.model) != "" || strings.TrimSpace(projection.provider) != "" {
				s.emitSessionState(record.identity.SessionID())
			}
		}
	}
	if codexMainProjection && projection.probeCodexTurn {
		s.startCodexTurnCompletionWatch(sessionID, projection.codexThreadID, projection.codexTurnID)
	}
	if projection.piRPCStateFailure != nil {
		if s.applyPIRPCStateFailure(sessionID, *projection.piRPCStateFailure) {
			return nil
		}
	}
	if projection.piRPCState != nil {
		if err := s.applyPIRPCState(sessionID, *projection.piRPCState); err != nil {
			return err
		}
	}
	if projection.codexBusy != nil && codexMainProjection {
		if err := s.applyCodexBusy(sessionID, *projection.codexBusy); err != nil {
			return err
		}
	}
	for _, event := range projection.waitRequests {
		s.startRuntimeAskUserWait(sessionID, event)
	}
	return nil
}
