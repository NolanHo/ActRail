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
	if activity := state.activity(); activity.Phase != codexRuntimePhaseThreadStarting || !activity.Busy {
		t.Fatalf("activity after threadStartRequest = %+v, want thread_starting busy", activity)
	}
	if request := state.threadStartRequest(); request != nil {
		t.Fatalf("threadStartRequest() while pending = %#v, want nil", request)
	}

	accepted, changed := state.setThreadID("thread-1")
	if !accepted || !changed {
		t.Fatalf("setThreadID() = accepted=%v changed=%v, want true true", accepted, changed)
	}
	initialized, threadID, activeTurnID := state.snapshot()
	if !initialized || threadID != "thread-1" || activeTurnID != "" {
		t.Fatalf("snapshot = initialized=%v threadID=%q activeTurnID=%q", initialized, threadID, activeTurnID)
	}
	if activity := state.activity(); activity.Phase != codexRuntimePhaseIdle || activity.Busy {
		t.Fatalf("activity after thread id = %+v, want idle", activity)
	}
	accepted, changed = state.setThreadID("thread-other")
	if accepted || changed {
		t.Fatalf("setThreadID(other) = accepted=%v changed=%v, want false false", accepted, changed)
	}
}

func TestCodexRuntimeStateBootstrapsInitializeThenThreadResume(t *testing.T) {
	state := newCodexRuntimeStateWithResumeThread(session.BackendCodex, "thread-existing")
	if state == nil {
		t.Fatal("newCodexRuntimeStateWithResumeThread(codex) = nil")
	}

	requests := state.bootstrapRequests()
	if len(requests) != 1 {
		t.Fatalf("initial bootstrap request count = %d, want 1", len(requests))
	}
	assertRuntimeRequest(t, requests[0], "initialize", "initialize-1")
	if activity := state.activity(); activity.Phase != codexRuntimePhaseInitializing || !activity.Busy {
		t.Fatalf("activity after initialize = %+v, want initializing busy", activity)
	}

	state.markInitialized()
	request := state.threadStartRequest()
	assertRuntimeRequest(t, request, "thread/resume", "thread-resume-2")
	params, ok := request.(map[string]any)["params"].(map[string]any)
	if !ok || params["threadId"] != "thread-existing" {
		t.Fatalf("thread/resume params = %#v, want threadId", request)
	}
	if activity := state.activity(); activity.Phase != codexRuntimePhaseThreadStarting || activity.Reason != "codex_thread_resuming" || !activity.Busy {
		t.Fatalf("activity after resume request = %+v, want resuming busy", activity)
	}
	if request := state.threadStartRequest(); request != nil {
		t.Fatalf("threadStartRequest() while resume pending = %#v, want nil", request)
	}

	accepted, changed := state.setThreadID("thread-existing")
	if !accepted || !changed {
		t.Fatalf("setThreadID(existing) = accepted=%v changed=%v, want true true", accepted, changed)
	}
	initialized, threadID, activeTurnID := state.snapshot()
	if !initialized || threadID != "thread-existing" || activeTurnID != "" {
		t.Fatalf("snapshot = initialized=%v threadID=%q activeTurnID=%q", initialized, threadID, activeTurnID)
	}
	if pending := state.pendingResumeThreadID(); pending != "" {
		t.Fatalf("pendingResumeThreadID() = %q, want empty", pending)
	}
	if activity := state.activity(); activity.Phase != codexRuntimePhaseIdle || activity.Busy {
		t.Fatalf("activity after resumed thread id = %+v, want idle", activity)
	}
}

func TestCodexRuntimeStateTracksActiveTurn(t *testing.T) {
	state := newCodexRuntimeState(session.BackendCodex)
	state.setActiveTurnID("turn-1")
	_, _, activeTurnID := state.snapshot()
	if activeTurnID != "turn-1" {
		t.Fatalf("active turn = %q, want turn-1", activeTurnID)
	}
	if activity := state.activity(); activity.Phase != codexRuntimePhaseRunning || !activity.Busy {
		t.Fatalf("activity after active turn = %+v, want running busy", activity)
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
	if activity := state.activity(); activity.Phase != codexRuntimePhaseIdle || activity.Busy {
		t.Fatalf("activity after clearing turn = %+v, want idle", activity)
	}
}

func TestCodexRuntimeStateDefersInterruptUntilTurnID(t *testing.T) {
	state := newCodexRuntimeState(session.BackendCodex)
	accepted, _ := state.setThreadID("thread-1")
	if !accepted {
		t.Fatal("setThreadID(thread-1) = false, want true")
	}
	state.transition(codexRuntimePhaseTurnStarting, "codex_turn_starting")
	state.requestInterrupt()
	if activity := state.activity(); activity.Phase != codexRuntimePhaseInterrupting || !activity.Busy {
		t.Fatalf("activity after pending interrupt = %+v, want interrupting busy", activity)
	}
	state.clearActiveTurnID("")
	if activity := state.activity(); activity.Phase != codexRuntimePhaseInterrupting || !activity.Busy {
		t.Fatalf("activity after empty clear during pending interrupt = %+v, want interrupting busy", activity)
	}
	state.applyProtocolBusy(false)
	if activity := state.activity(); activity.Phase != codexRuntimePhaseInterrupting || !activity.Busy {
		t.Fatalf("activity after idle protocol busy during pending interrupt = %+v, want interrupting busy", activity)
	}
	if _, _, ok := state.pendingInterruptCommand(); ok {
		t.Fatal("pendingInterruptCommand() ok = true before turn id, want false")
	}
	state.setActiveTurnID("turn-1")
	threadID, turnID, ok := state.pendingInterruptCommand()
	if !ok || threadID != "thread-1" || turnID != "turn-1" {
		t.Fatalf("pendingInterruptCommand() = (%q, %q, %v), want thread-1 turn-1 true", threadID, turnID, ok)
	}
	state.markInterruptSent("turn-1")
	if _, _, ok := state.pendingInterruptCommand(); ok {
		t.Fatal("pendingInterruptCommand() ok = true after mark sent, want false")
	}
	state.clearActiveTurnID("")
	if !state.pendingInterrupt() {
		t.Fatal("pendingInterrupt() = false after empty clear with active interrupted turn, want true")
	}
	if activity := state.activity(); activity.Phase != codexRuntimePhaseInterrupting || !activity.Busy {
		t.Fatalf("activity after empty clear with active interrupted turn = %+v, want interrupting busy", activity)
	}
	state.applyProtocolBusy(false)
	if !state.pendingInterrupt() {
		t.Fatal("pendingInterrupt() = false after protocol idle with active interrupted turn, want true")
	}
	if activity := state.activity(); activity.Phase != codexRuntimePhaseInterrupting || !activity.Busy {
		t.Fatalf("activity after protocol idle with active interrupted turn = %+v, want interrupting busy", activity)
	}
	state.clearActiveTurnID("turn-1")
	if state.pendingInterrupt() {
		t.Fatal("pendingInterrupt() = true after clearing turn, want false")
	}
	if activity := state.activity(); activity.Phase != codexRuntimePhaseIdle || activity.Busy {
		t.Fatalf("activity after clearing interrupted turn = %+v, want idle", activity)
	}
}

func TestCodexRuntimeStateTransitionsControlAndProtocolPhases(t *testing.T) {
	state := newCodexRuntimeState(session.BackendCodex)
	activity, changed := state.transition(codexRuntimePhaseSending, "codex_sending")
	if !changed || activity.Phase != codexRuntimePhaseSending || !activity.Busy || activity.Reason != "codex_sending" {
		t.Fatalf("transition(sending) = %+v changed=%v, want busy sending", activity, changed)
	}
	activity, changed = state.transition(codexRuntimePhaseTurnStarting, "")
	if !changed || activity.Phase != codexRuntimePhaseTurnStarting || !activity.Busy || activity.Reason != "codex_turn_starting" {
		t.Fatalf("transition(turn_starting) = %+v changed=%v, want busy turn_starting default reason", activity, changed)
	}
	activity, changed = state.applyProtocolBusy(true)
	if !changed || activity.Phase != codexRuntimePhaseRunning || !activity.Busy || activity.Reason != "codex_running" {
		t.Fatalf("applyProtocolBusy(true) = %+v changed=%v, want running", activity, changed)
	}
	activity, changed = state.applyProtocolBusy(false)
	if !changed || activity.Phase != codexRuntimePhaseIdle || activity.Busy || activity.Reason != "" {
		t.Fatalf("applyProtocolBusy(false) = %+v changed=%v, want idle", activity, changed)
	}
	activity, changed = state.transition(codexRuntimePhaseFailed, "runtime input unavailable")
	if !changed || activity.Phase != codexRuntimePhaseFailed || activity.Busy {
		t.Fatalf("transition(failed) = %+v changed=%v, want non-busy failed", activity, changed)
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
