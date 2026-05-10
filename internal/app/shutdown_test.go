package app

import (
	"testing"
	"time"

	"actrail/internal/config"
)

func TestCloseWaitsForRuntimeRestore(t *testing.T) {
	cfg := config.Load()
	cfg.Storage.DataDir = t.TempDir()
	s := newStub(cfg, func() time.Time { return time.Unix(1760000400, 0).UTC() })
	s.markRuntimeRestorePending()
	if !s.beginRuntimeRestore() {
		t.Fatal("beginRuntimeRestore() = false, want owner for pending restore")
	}
	done := make(chan error, 1)
	go func() {
		done <- s.Close()
	}()

	select {
	case err := <-done:
		t.Fatalf("Close() returned before restore completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	s.endRuntimeRestore(nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not return after runtime restore completed")
	}
}
