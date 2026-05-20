package app

import "testing"

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
		"command reflected":     {Transport: codexTransportAttached, RuntimeActivity: codexRuntimeActivityIdle, ActiveCommand: codexCommandReflected},
		"ui wait":               {Transport: codexTransportAttached, RuntimeActivity: codexRuntimeActivityIdle, UIWaitActive: true},
	} {
		t.Run(name, func(t *testing.T) {
			if state.canDirectSend() {
				t.Fatalf("canDirectSend() = true for %+v, want false", state)
			}
		})
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
