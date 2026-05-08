package app

import (
	"context"
	"errors"
	"io"
	"sync"

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
				s.handlePIHelperReadError(sessionID, err)
			}
			return
		}
		if err := s.applyRuntimeHelperPacket(sessionID, backend, packet); err != nil {
			continue
		}
	}
}

func (s *Stub) applyRuntimeHelperPacket(sessionID session.SessionID, backend session.Backend, packet any) error {
	if s == nil {
		return nil
	}
	key := struct {
		stub      *Stub
		sessionID session.SessionID
		backend   session.Backend
	}{stub: s, sessionID: sessionID, backend: backend}
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
