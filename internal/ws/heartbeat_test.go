package ws

import (
	"testing"
	"time"
)

func TestHeartbeatStateSchedulesFromLastServerSend(t *testing.T) {
	start := time.Unix(1760000000, 0)
	state, err := NewHeartbeatState(15*time.Second, start)
	if err != nil {
		t.Fatalf("NewHeartbeatState() error = %v", err)
	}
	if state.ServerHeartbeatDue(start.Add(14 * time.Second)) {
		t.Fatal("ServerHeartbeatDue(14s) = true, want false")
	}
	if !state.ServerHeartbeatDue(start.Add(15 * time.Second)) {
		t.Fatal("ServerHeartbeatDue(15s) = false, want true")
	}
	state.ObserveServerSend(start.Add(10 * time.Second))
	if got := state.NextServerHeartbeat(); !got.Equal(start.Add(25 * time.Second)) {
		t.Fatalf("NextServerHeartbeat() = %s, want %s", got, start.Add(25*time.Second))
	}
}

func TestHeartbeatStateTracksClientPingSeparately(t *testing.T) {
	start := time.Unix(1760000000, 0)
	state, err := NewHeartbeatState(15*time.Second, start)
	if err != nil {
		t.Fatalf("NewHeartbeatState() error = %v", err)
	}
	pingAt := start.Add(5 * time.Second)
	state.ObserveClientPing(pingAt)
	if got := state.LastClientPing(); !got.Equal(pingAt) {
		t.Fatalf("LastClientPing() = %s, want %s", got, pingAt)
	}
	if !state.ServerHeartbeatDue(start.Add(15 * time.Second)) {
		t.Fatal("ServerHeartbeatDue() should still depend on last server send")
	}
}
