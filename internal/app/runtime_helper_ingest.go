package app

import (
	"context"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/domain/session"
)

var runtimeHelperProjectors sync.Map

const (
	runtimeHelperRedialTimeout        = 500 * time.Millisecond
	runtimeHelperReconnectInitialWait = 250 * time.Millisecond
	runtimeHelperReconnectMaxWait     = 5 * time.Second
)

type runtimeHelperProjector struct {
	mu      sync.Mutex
	decoder runtimeEventDecoder
}

type helperReadErrorResult struct {
	reattached      bool
	retry           bool
	client          *iodclient.Client
	orphanCandidate *iod.GenerationManifest
}

func (s *Stub) readRuntimeHelper(sessionID session.SessionID, backend session.Backend, helper *runtimeIODHelper) {
	if s == nil || helper == nil || helper.streamClient == nil {
		return
	}
	client := helper.streamClient
	reconnectWait := runtimeHelperReconnectInitialWait
	for {
		packet, err := client.ReadPacket(context.Background())
		if err != nil {
			if !s.runtimeHelperStreamCurrent(sessionID, helper.generationID, client) {
				return
			}
			result := s.handleHelperReadError(sessionID, backend, helper.generationID, err)
			if result.reattached && result.client != nil {
				client = result.client
				helper.streamClient = result.client
				reconnectWait = runtimeHelperReconnectInitialWait
				continue
			}
			if !result.retry {
				return
			}
			time.Sleep(reconnectWait)
			reconnectWait *= 2
			if reconnectWait > runtimeHelperReconnectMaxWait {
				reconnectWait = runtimeHelperReconnectMaxWait
			}
			continue
		}
		reconnectWait = runtimeHelperReconnectInitialWait
		if err := s.applyRuntimeHelperPacket(sessionID, backend, helper.generationID, packet); err != nil {
			continue
		}
	}
}

func (s *Stub) runtimeHelperStreamCurrent(sessionID session.SessionID, generationID iod.GenerationID, client *iodclient.Client) bool {
	if s == nil || client == nil {
		return false
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return false
	}
	runtime := s.runtimeForRecord(record)
	if helper := runtime.helper; helper != nil && helper.generationID == generationID && helper.streamClient == client {
		return true
	}
	if s.helpers != nil {
		attachment, ok := s.helpers.Attachment(sessionID)
		if ok && attachment.Binding.GenerationID == generationID && attachment.Client == client {
			return true
		}
	}
	return false
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

func (s *Stub) markHelperTransportReconnecting(sessionID session.SessionID, generationID iod.GenerationID) error {
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
	if transport.State == SessionTransportStateSilent && transport.Reason == "attach_lost_reconnecting" && !transport.ResetRequired {
		return nil
	}
	if transport.State == SessionTransportStateEnded && transport.Reason == iod.FactChildExit.String() {
		return nil
	}
	if _, _, err := s.registry.SetTransport(sessionID, SessionTransportSnapshot{
		GenerationID: generationID.String(),
		State:        SessionTransportStateSilent,
		Reason:       "attach_lost_reconnecting",
	}); err != nil {
		return err
	}
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) reconcileLiveCodexAttachLostTransport(record sessionRecord) (sessionRecord, bool) {
	if s == nil || record.identity.Historical() || record.identity.Backend() != session.BackendCodex {
		return record, false
	}
	transport := sessionTransportSnapshot(record)
	if transport.State != SessionTransportStateBroken || !transport.ResetRequired || transport.Reason != iod.GenerationBreakAttachLost.String() {
		return record, false
	}
	generationID := mustTransportGenerationID(transport.GenerationID)
	if generationID == "" {
		return record, false
	}
	result := s.tryRedialHelperAfterReadError(record.identity.SessionID(), record.identity.Backend(), generationID, transport)
	if !result.reattached || result.client == nil {
		return record, false
	}
	updated, ok := s.registry.Lookup(record.identity.SessionID())
	if !ok {
		return record, false
	}
	updated.runtime = s.runtimeForRecord(updated)
	if updated.runtime.helper != nil {
		go s.readRuntimeHelper(updated.identity.SessionID(), updated.identity.Backend(), updated.runtime.helper)
	}
	return updated, true
}

func (s *Stub) helperGenerationAppearsAlive(sessionID session.SessionID, generationID iod.GenerationID) bool {
	if s == nil || generationID == "" {
		return false
	}
	attachment, ok := s.helperAttachmentForRedial(sessionID, generationID)
	if !ok {
		return false
	}
	manifest := attachment.Manifest
	if manifest.GenerationID != generationID || manifest.SessionID != sessionID {
		return false
	}
	if manifest.HelperPID <= 0 {
		return false
	}
	if err := syscall.Kill(manifest.HelperPID, 0); err != nil {
		return false
	}
	return true
}

func (s *Stub) tryRedialHelperAfterReadError(sessionID session.SessionID, backend session.Backend, generationID iod.GenerationID, transport SessionTransportSnapshot) helperReadErrorResult {
	if s == nil || generationID == "" {
		return helperReadErrorResult{}
	}
	attachment, ok := s.helperAttachmentForRedial(sessionID, generationID)
	if !ok {
		return helperReadErrorResult{}
	}
	manifest := attachment.Manifest
	if manifest.GenerationID != generationID || manifest.SessionID != sessionID {
		return helperReadErrorResult{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeHelperRedialTimeout)
	defer cancel()
	client, hello, err := redialHelper(ctx, manifest, s.helperDialer)
	if err != nil {
		if s.helperGenerationAppearsAlive(sessionID, generationID) {
			_ = s.markHelperTransportReconnecting(sessionID, generationID)
			return helperReadErrorResult{retry: true}
		}
		return helperReadErrorResult{orphanCandidate: &manifest}
	}
	if attachment.Client != nil && attachment.Client != client {
		_ = attachment.Client.Close()
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
		return helperReadErrorResult{}
	}
	if _, ok, err := s.registry.SetRuntimeTransport(sessionID, runtime, transportSnapshotAttached(generationID)); err != nil || !ok {
		_ = client.Close()
		return helperReadErrorResult{}
	}
	s.emitSessionState(sessionID)
	return helperReadErrorResult{reattached: true, client: client}
}

func (s *Stub) helperAttachmentForRedial(sessionID session.SessionID, generationID iod.GenerationID) (attachedHelper, bool) {
	if s == nil || generationID == "" {
		return attachedHelper{}, false
	}
	if s.helpers != nil {
		if attachment, ok := s.helpers.Attachment(sessionID); ok && attachment.Binding.GenerationID == generationID {
			return attachment, true
		}
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return attachedHelper{}, false
	}
	helper := record.runtime.helper
	if helper == nil || helper.generationID != generationID {
		return attachedHelper{}, false
	}
	manifest := helper.manifest
	manifestPath := ""
	if helper.runtimeDir != "" {
		manifestPath = filepath.Join(helper.runtimeDir, iodclient.ManifestFilename)
	}
	if err := manifest.Validate(); err != nil {
		if manifestPath == "" {
			return attachedHelper{}, false
		}
		loaded, loadErr := iod.ReadGenerationManifest(manifestPath)
		if loadErr != nil {
			return attachedHelper{}, false
		}
		manifest = loaded
	}
	if manifest.SessionID != sessionID || manifest.GenerationID != generationID {
		return attachedHelper{}, false
	}
	binding := helperGenerationBinding{SessionID: sessionID, GenerationID: generationID}
	if record.runtime.helperBinding != nil && record.runtime.helperBinding.GenerationID == generationID {
		binding.LastReplayOffset = record.runtime.helperBinding.LastReplayOffset
	}
	return attachedHelper{
		Binding:      binding,
		ManifestPath: manifestPath,
		Manifest:     manifest,
		Client:       helper.streamClient,
	}, true
}
