package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/domain/session"
)

var runtimeHelperProjectors sync.Map

const runtimeHelperRedialTimeout = 500 * time.Millisecond

type runtimeHelperProjector struct {
	mu      sync.Mutex
	decoder runtimeEventDecoder
}

func (s *Stub) readRuntimeHelper(sessionID session.SessionID, backend session.Backend, helper *runtimeIODHelper) {
	if s == nil || helper == nil || helper.streamClient == nil {
		return
	}
	client := helper.streamClient
	for {
		packet, err := client.ReadPacket(context.Background())
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			reattached, nextClient := s.handleHelperReadError(sessionID, backend, helper.generationID, err)
			if !reattached || nextClient == nil {
				return
			}
			client = nextClient
			helper.streamClient = nextClient
			continue
		}
		if err := s.applyRuntimeHelperPacket(sessionID, backend, helper.generationID, packet); err != nil {
			continue
		}
	}
}

func (s *Stub) applyRuntimeHelperPacket(sessionID session.SessionID, backend session.Backend, generationID iod.GenerationID, packet any) error {
	if s == nil {
		return nil
	}
	if generationID != "" && !s.runtimeHelperGenerationCurrent(sessionID, generationID) {
		return nil
	}
	key := struct {
		stub       *Stub
		sessionID  session.SessionID
		backend    session.Backend
		generation iod.GenerationID
	}{stub: s, sessionID: sessionID, backend: backend, generation: generationID}
	projectorAny, _ := runtimeHelperProjectors.LoadOrStore(key, &runtimeHelperProjector{decoder: runtimeEventDecoder{backend: backend}})
	projector := projectorAny.(*runtimeHelperProjector)
	projector.mu.Lock()
	defer projector.mu.Unlock()
	if err := s.applyPITransportPacket(sessionID, packet); err != nil {
		return err
	}
	projection, err := projector.decoder.decodeHelperPacket(packet)
	if err != nil {
		return err
	}
	if projectionHasRuntimeActivity(projection) {
		if err := s.markHelperTransportAlive(sessionID, generationID); err != nil {
			return err
		}
	}
	return s.applyRuntimeProjection(sessionID, projection)
}

func (s *Stub) runtimeHelperGenerationCurrent(sessionID session.SessionID, generationID iod.GenerationID) bool {
	if s == nil || generationID == "" {
		return true
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return false
	}
	if record.runtime.helper != nil && record.runtime.helper.generationID == generationID {
		return true
	}
	if s.helpers != nil {
		if attachment, ok := s.helpers.Attachment(sessionID); ok && attachment.Binding.GenerationID == generationID {
			return true
		}
	}
	if binding, err := record.runtime.CurrentHelperBinding(sessionID); err == nil && binding != nil {
		return binding.GenerationID == generationID
	}
	if s.helperBindings.root != "" {
		bindings, err := s.helperBindings.Load()
		if err != nil {
			return false
		}
		binding, ok := bindings[sessionID]
		return ok && binding.GenerationID == generationID
	}
	if record.transport.GenerationID != "" {
		return record.transport.GenerationID == generationID.String()
	}
	return false
}

func projectionHasRuntimeActivity(projection runtimeProjection) bool {
	if len(projection.events) > 0 || len(projection.waitRequests) > 0 {
		return true
	}
	if projection.codexInitialized || projection.codexDesynced || projection.clearCodexTurn || projection.probeCodexTurn {
		return true
	}
	if projection.codexBusy != nil || projection.piRPCState != nil || projection.piRPCStateFailure != nil {
		return true
	}
	return projection.codexThreadID != "" || projection.codexTurnID != "" || projection.model != "" || projection.provider != "" || projection.contextUsage != nil || projection.turnTiming != nil
}

func (s *Stub) markHelperTransportAlive(sessionID session.SessionID, generationID iod.GenerationID) error {
	if s == nil || generationID == "" {
		return nil
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return nil
	}
	transport := sessionTransportSnapshot(record)
	if transport.GenerationID != "" && transport.GenerationID != generationID.String() {
		return nil
	}
	if transport.State == SessionTransportStateAttached && !transport.ResetRequired {
		return nil
	}
	if transport.State == SessionTransportStateEnded && transport.Reason == iod.FactChildExit.String() {
		return nil
	}
	if _, _, err := s.registry.SetTransport(sessionID, transportSnapshotAttached(generationID)); err != nil {
		return err
	}
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) tryRedialHelperAfterReadError(sessionID session.SessionID, backend session.Backend, generationID iod.GenerationID, transport SessionTransportSnapshot) (bool, *iodclient.Client) {
	if s == nil || generationID == "" {
		return false, nil
	}
	attachment, ok := s.helpers.Attachment(sessionID)
	if !ok || attachment.Binding.GenerationID != generationID {
		return false, nil
	}
	manifest := attachment.Manifest
	if manifest.GenerationID != generationID || manifest.SessionID != sessionID {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeHelperRedialTimeout)
	defer cancel()
	client, hello, err := redialHelper(ctx, manifest, s.helperDialer)
	if err != nil {
		return false, nil
	}
	nextAttachment := attachment
	nextAttachment.Client = client
	nextAttachment.Hello = hello
	s.helpers.Set(sessionID, nextAttachment)
	runtime := sessionRuntime{
		protocol:            runtimeProtocolCodexRPC,
		helper:              runtimeIODHelperFromAttachment(nextAttachment, s.helperDialer),
		helperBinding:       &RuntimeHelperBinding{GenerationID: nextAttachment.Binding.GenerationID, LastReplayOffset: nextAttachment.Binding.LastReplayOffset},
		attachedExistingIOD: true,
	}
	if backend == session.BackendPI {
		runtime.protocol = runtimeProtocolPIRPC
	} else if backend == session.BackendCodex {
		if record, ok := s.registry.Lookup(sessionID); ok {
			runtime.codex = record.runtime.codex
		}
	}
	runtime.currentHelperBinding = func(session.SessionID) (*RuntimeHelperBinding, error) {
		resolved := RuntimeHelperBinding{GenerationID: nextAttachment.Binding.GenerationID, LastReplayOffset: nextAttachment.Binding.LastReplayOffset}
		return &resolved, nil
	}
	if transport.GenerationID != "" && transport.GenerationID != generationID.String() {
		_ = client.Close()
		return false, nil
	}
	if _, ok, err := s.registry.SetRuntimeTransport(sessionID, runtime, transportSnapshotAttached(generationID)); err != nil || !ok {
		_ = client.Close()
		return false, nil
	}
	s.emitSessionState(sessionID)
	return true, client
}
