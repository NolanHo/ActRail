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

func TestRegistrySubscribersFiltersByStream(t *testing.T) {
	registry := NewRegistry()
	now := time.Unix(1760000000, 0)
	connA, err := NewConnectionState("conn_a", 15*time.Second, nil, now)
	if err != nil {
		t.Fatalf("NewConnectionState(conn_a) error = %v", err)
	}
	if err := connA.subscriptions.ApplySubscribe(SubscribePayload{Streams: []Subscription{{Name: SessionsStream}}}); err != nil {
		t.Fatalf("ApplySubscribe(conn_a) error = %v", err)
	}
	if err := registry.Add(connA); err != nil {
		t.Fatalf("registry.Add(conn_a) error = %v", err)
	}
	connB, err := NewConnectionState("conn_b", 15*time.Second, nil, now)
	if err != nil {
		t.Fatalf("NewConnectionState(conn_b) error = %v", err)
	}
	if err := connB.subscriptions.ApplySubscribe(SubscribePayload{Streams: []Subscription{{Name: StreamName("session:s_123")}}}); err != nil {
		t.Fatalf("ApplySubscribe(conn_b) error = %v", err)
	}
	if err := registry.Add(connB); err != nil {
		t.Fatalf("registry.Add(conn_b) error = %v", err)
	}
	subscribers := registry.Subscribers(SessionsStream)
	if len(subscribers) != 1 || subscribers[0].ID() != "conn_a" {
		t.Fatalf("Subscribers(sessions) = %#v", subscribers)
	}
}
