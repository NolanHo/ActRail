package app

import (
	"context"
	"fmt"
	"time"
)

const runtimeRestoreCloseWaitTimeout = 30 * time.Second

// Close flushes synchronous durable session state before process shutdown.
func (s *Stub) Close() error {
	if s == nil {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), runtimeRestoreCloseWaitTimeout)
	defer cancel()
	if err := s.waitRuntimeRestore(waitCtx); err != nil {
		return fmt.Errorf("wait for runtime restore before shutdown: %w", err)
	}
	if err := s.registry.PersistAll(); err != nil {
		return fmt.Errorf("persist sessions before shutdown: %w", err)
	}
	if closer, ok := s.appStore.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			return fmt.Errorf("close sqlite catalog: %w", err)
		}
	}
	return nil
}
