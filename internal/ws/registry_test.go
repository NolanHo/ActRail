package ws

import (
	"testing"
	"time"
)

func TestRegistryAddGetRemove(t *testing.T) {
	registry := NewRegistry()
	conn, err := NewConnectionState("conn_1", 15*time.Second, nil, time.Unix(1760000000, 0))
	if err != nil {
		t.Fatalf("NewConnectionState() error = %v", err)
	}
	if err := registry.Add(conn); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if registry.Count() != 1 {
		t.Fatalf("Count() = %d, want 1", registry.Count())
	}
	got, ok := registry.Get("conn_1")
	if !ok || got != conn {
		t.Fatalf("Get() = (%v, %v), want (%v, true)", got, ok, conn)
	}
	registry.Remove("conn_1")
	if registry.Count() != 0 {
		t.Fatalf("Count() = %d, want 0", registry.Count())
	}
}
