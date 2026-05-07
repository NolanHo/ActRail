package app

import (
	"testing"

	"actrail/internal/domain/session"
)

func TestCodexRuntimeStateBootstrapsInitializeThenThreadStart(t *testing.T) {
	state := newCodexRuntimeState(session.BackendCodex)
	if state == nil {
		t.Fatal("newCodexRuntimeState(codex) = nil")
	}

	requests := state.bootstrapRequests()
	if len(requests) != 1 {
		t.Fatalf("initial bootstrap request count = %d, want 1", len(requests))
	}
	assertRuntimeRequest(t, requests[0], "initialize", "initialize-1")
	if requests := state.bootstrapRequests(); len(requests) != 0 {
		t.Fatalf("bootstrap while initialize pending returned %#v, want none", requests)
	}

	state.markInitialized()
	request := state.threadStartRequest()
	assertRuntimeRequest(t, request, "thread/start", "thread-start-2")
	if request := state.threadStartRequest(); request != nil {
		t.Fatalf("threadStartRequest() while pending = %#v, want nil", request)
	}

	state.setThreadID("thread-1")
	initialized, threadID, activeTurnID := state.snapshot()
	if !initialized || threadID != "thread-1" || activeTurnID != "" {
		t.Fatalf("snapshot = initialized=%v threadID=%q activeTurnID=%q", initialized, threadID, activeTurnID)
	}
}

func TestCodexRuntimeStateTracksActiveTurn(t *testing.T) {
	state := newCodexRuntimeState(session.BackendCodex)
	state.setActiveTurnID("turn-1")
	_, _, activeTurnID := state.snapshot()
	if activeTurnID != "turn-1" {
		t.Fatalf("active turn = %q, want turn-1", activeTurnID)
	}
	state.clearActiveTurnID("turn-2")
	_, _, activeTurnID = state.snapshot()
	if activeTurnID != "turn-1" {
		t.Fatalf("active turn after clearing other turn = %q, want turn-1", activeTurnID)
	}
	state.clearActiveTurnID("turn-1")
	_, _, activeTurnID = state.snapshot()
	if activeTurnID != "" {
		t.Fatalf("active turn after clearing matching turn = %q, want empty", activeTurnID)
	}
}

func assertRuntimeRequest(t *testing.T, raw any, method, id string) {
	t.Helper()
	request, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("request = %T, want map[string]any", raw)
	}
	if request["method"] != method || request["id"] != id {
		t.Fatalf("request = %#v, want method=%q id=%q", request, method, id)
	}
}
