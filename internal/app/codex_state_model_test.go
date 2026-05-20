package app

import (
	"testing"

	"actrail/internal/domain/session"
)

func TestCodexStateAxesDirectSendPredicate(t *testing.T) {
	base := codexStateAxes{
		Transport:       codexTransportAttached,
		RuntimeActivity: codexRuntimeActivityIdle,
		History:         codexHistoryUncertain,
	}
	if !base.canDirectSend() {
		t.Fatal("canDirectSend() = false, want true for attached idle runtime even with uncertain history")
	}
	for name, state := range map[string]codexStateAxes{
		"transport unavailable": {Transport: codexTransportUnavailable, RuntimeActivity: codexRuntimeActivityIdle},
		"runtime running":       {Transport: codexTransportAttached, RuntimeActivity: codexRuntimeActivityRunning},
		"command pending":       {Transport: codexTransportAttached, RuntimeActivity: codexRuntimeActivityIdle, ActiveCommand: codexCommandPending},
		"command accepted":      {Transport: codexTransportAttached, RuntimeActivity: codexRuntimeActivityIdle, ActiveCommand: codexCommandAccepted},
		"ui wait":               {Transport: codexTransportAttached, RuntimeActivity: codexRuntimeActivityIdle, UIWaitActive: true},
	} {
		t.Run(name, func(t *testing.T) {
			if state.canDirectSend() {
				t.Fatalf("canDirectSend() = true for %+v, want false", state)
			}
		})
	}
	reflected := base
	reflected.ActiveCommand = codexCommandReflected
	if !reflected.canDirectSend() {
		t.Fatal("canDirectSend() = false, want reflected command not to block idle direct send")
	}
}

func TestCodexStateAxesDisplayState(t *testing.T) {
	tests := []struct {
		name string
		in   codexStateAxes
		want codexDerivedDisplayState
	}{
		{
			name: "failed transport wins",
			in: codexStateAxes{
				Transport:       codexTransportUnavailable,
				RuntimeActivity: codexRuntimeActivityIdle,
			},
			want: codexDisplayFailed,
		},
		{
			name: "attach in flight",
			in: codexStateAxes{
				Transport:      codexTransportUnknown,
				AttachInFlight: true,
			},
			want: codexDisplayStarting,
		},
		{
			name: "runtime working wins over accepted command",
			in: codexStateAxes{
				Transport:       codexTransportAttached,
				RuntimeActivity: codexRuntimeActivityRunning,
				ActiveCommand:   codexCommandAccepted,
			},
			want: codexDisplayWorking,
		},
		{
			name: "pending command is sending",
			in: codexStateAxes{
				Transport:       codexTransportAttached,
				RuntimeActivity: codexRuntimeActivityIdle,
				ActiveCommand:   codexCommandPending,
			},
			want: codexDisplaySending,
		},
		{
			name: "accepted command before turn id",
			in: codexStateAxes{
				Transport:       codexTransportAttached,
				RuntimeActivity: codexRuntimeActivityIdle,
				ActiveCommand:   codexCommandAccepted,
			},
			want: codexDisplayTurnStarting,
		},
		{
			name: "idle",
			in: codexStateAxes{
				Transport:       codexTransportAttached,
				RuntimeActivity: codexRuntimeActivityIdle,
			},
			want: codexDisplayIdle,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.displayState(); got != tt.want {
				t.Fatalf("displayState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCodexSendCommandSessionID(t *testing.T) {
	sessionID, err := session.ParseSessionID("s_observe")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	commandID := codexSendCommandID(sessionID, 42)
	got, ok := codexSendCommandSessionID(commandID)
	if !ok || got != sessionID {
		t.Fatalf("codexSendCommandSessionID(%q) = (%q, %v), want (%q, true)", commandID, got, ok, sessionID)
	}
	if _, ok := codexSendCommandSessionID("not-a-command"); ok {
		t.Fatal("codexSendCommandSessionID(invalid) ok = true, want false")
	}
}
