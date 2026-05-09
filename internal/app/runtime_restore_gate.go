package app

import (
	"context"
)

var closedRuntimeRestoreDoneCh = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

func closedRuntimeRestoreDone() chan struct{} {
	return closedRuntimeRestoreDoneCh
}

func (s *Stub) markRuntimeRestorePending() {
	if s == nil {
		return
	}
	s.runtimeRestoreMu.Lock()
	defer s.runtimeRestoreMu.Unlock()
	if s.runtimeRestoring {
		return
	}
	s.runtimeRestoring = true
	s.runtimeRestoreOwner = false
	s.runtimeRestoreDone = make(chan struct{})
	s.runtimeRestoreErr = nil
}

func (s *Stub) beginRuntimeRestore() bool {
	if s == nil {
		return false
	}
	s.runtimeRestoreMu.Lock()
	defer s.runtimeRestoreMu.Unlock()
	if s.runtimeRestoring {
		if !s.runtimeRestoreOwner {
			s.runtimeRestoreOwner = true
			return true
		}
		return false
	}
	s.runtimeRestoring = true
	s.runtimeRestoreOwner = true
	s.runtimeRestoreDone = make(chan struct{})
	s.runtimeRestoreErr = nil
	return true
}

func (s *Stub) endRuntimeRestore(err error) {
	if s == nil {
		return
	}
	s.runtimeRestoreMu.Lock()
	defer s.runtimeRestoreMu.Unlock()
	if !s.runtimeRestoring || !s.runtimeRestoreOwner {
		return
	}
	done := s.runtimeRestoreDone
	s.runtimeRestoring = false
	s.runtimeRestoreOwner = false
	s.runtimeRestoreErr = err
	if done != nil {
		close(done)
	}
}

func (s *Stub) waitRuntimeRestore(ctx context.Context) error {
	done, err := s.runtimeRestoreSnapshot()
	if done == nil {
		return err
	}
	select {
	case <-done:
		_, err = s.runtimeRestoreSnapshot()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Stub) runtimeRestoreSnapshot() (<-chan struct{}, error) {
	if s == nil {
		return nil, nil
	}
	s.runtimeRestoreMu.RLock()
	defer s.runtimeRestoreMu.RUnlock()
	if s.runtimeRestoring {
		return s.runtimeRestoreDone, nil
	}
	return nil, s.runtimeRestoreErr
}
