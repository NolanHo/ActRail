package app

import (
	"context"
	"errors"
	"io"
	"sync"

	"actrail/internal/adapters/iod"
	"actrail/internal/domain/session"
)

var runtimeHelperProjectors sync.Map

type runtimeHelperProjector struct {
	mu      sync.Mutex
	decoder runtimeEventDecoder
}

func (s *Stub) readRuntimeHelper(sessionID session.SessionID, backend session.Backend, helper *runtimeIODHelper) {
	if s == nil || helper == nil || helper.streamClient == nil {
		return
	}
	for {
		packet, err := helper.streamClient.ReadPacket(context.Background())
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.handleHelperReadError(sessionID, backend, helper.generationID, err)
			}
			return
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
