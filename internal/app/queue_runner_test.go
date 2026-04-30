package app

import (
	"context"
	"testing"

	"actrail/internal/domain/session"
)

func TestEnqueueDispatchesWhenSessionAlreadyIdle(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)

	queued, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "queued prompt"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if queued.Busy {
		t.Fatalf("Enqueue().Busy = %v, want false before async dispatch", queued.Busy)
	}
	if len(queued.Queue.Items) != 1 || queued.Queue.Items[0].Text != "queued prompt" {
		t.Fatalf("Enqueue().Queue = %+v, want queued prompt", queued.Queue.Items)
	}

	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return state.Busy && len(state.Queue.Items) == 0
	})

	writes := pty.Writes()
	if len(writes) != 1 || writes[0] != "{\"type\":\"prompt\",\"message\":\"queued prompt\"}\n" {
		t.Fatalf("pty writes after idle Enqueue() = %#v, want queued RPC prompt command", writes)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Role != "user" || messages.Items[0].Text != "queued prompt" {
		t.Fatalf("SessionMessages() = %+v, want one queued user message", messages)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if !state.Busy || len(state.Queue.Items) != 0 || state.TailSeq != 1 {
		t.Fatalf("SessionState() after idle dispatch = %+v, want busy true empty queue tail_seq 1", state)
	}
}

func TestPIRPCIdleStateDispatchesQueuedPromptWithoutManualResend(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	if _, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "first prompt"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	queued, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "second prompt"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if !queued.Busy || len(queued.Queue.Items) != 1 || queued.Queue.Items[0].Text != "second prompt" {
		t.Fatalf("Enqueue() = %+v, want retained queued second prompt while busy", queued)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() before completion error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "first prompt" {
		t.Fatalf("SessionMessages() before completion = %+v, want only first prompt", messages)
	}
	before := sink.snapshot()

	decoder := runtimeEventDecoder{backend: session.BackendPI}
	if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(`{"id":"actrail-state-idle","type":"response","command":"get_state","success":true,"data":{"isStreaming":false,"isCompacting":false,"pendingMessageCount":0}}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(get_state idle) error = %v", err)
	}

	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return state.Busy && len(state.Queue.Items) == 0 && len(pty.Writes()) == 2
	})

	writes := pty.Writes()
	if len(writes) != 2 || writes[1] != "{\"type\":\"prompt\",\"message\":\"second prompt\"}\n" {
		t.Fatalf("pty writes after turn completion = %#v, want queued second prompt RPC command", writes)
	}
	messages, err = svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() after completion error = %v", err)
	}
	if len(messages.Items) != 2 || messages.Items[1].Role != "user" || messages.Items[1].Text != "second prompt" {
		t.Fatalf("SessionMessages() after completion = %+v, want queued second prompt committed", messages)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after completion error = %v", err)
	}
	if !state.Busy || len(state.Queue.Items) != 0 || state.TailSeq != 2 {
		t.Fatalf("SessionState() after completion = %+v, want busy true empty queue tail_seq 2", state)
	}

	after := sink.snapshot()
	if len(after.commits) <= len(before.commits) {
		t.Fatalf("commit events count = %d, want > %d", len(after.commits), len(before.commits))
	}
	if got := after.commits[len(after.commits)-1].Message.Text; got != "second prompt" {
		t.Fatalf("last commit event text = %q, want %q", got, "second prompt")
	}
	if len(after.queueStates) == 0 || len(after.queueStates[len(after.queueStates)-1].Queue.Items) != 0 {
		t.Fatalf("last queue event = %+v, want empty queue", after.queueStates)
	}
	if len(after.states) == 0 || !after.states[len(after.states)-1].Busy || after.states[len(after.states)-1].QueueLen != 0 {
		t.Fatalf("last state event = %+v, want busy true queue_len 0", after.states)
	}
}
