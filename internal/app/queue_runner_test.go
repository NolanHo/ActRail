package app

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func TestEnqueueAddsManualInboxItemWithoutRuntimeQueue(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)

	queued, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "queued prompt"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if queued.Busy {
		t.Fatalf("Enqueue().Busy = %v, want false", queued.Busy)
	}
	if len(queued.Queue.Items) != 0 {
		t.Fatalf("Enqueue().Queue = %+v, want empty runtime queue", queued.Queue.Items)
	}
	inbox, err := svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox(after enqueue) error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Source != "manual" || inbox.Items[0].State != "pending" || inbox.Items[0].Message != "queued prompt" {
		t.Fatalf("SessionInbox(after enqueue) = %+v, want one pending manual item", inbox.Items)
	}
	if writes := pty.Writes(); len(writes) != 0 {
		t.Fatalf("pty writes after Enqueue() = %#v, want none before scheduler delivery", writes)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 0 {
		t.Fatalf("SessionMessages() = %+v, want no committed runtime message before scheduler delivery", messages)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || len(state.Queue.Items) != 0 || state.TailSeq != 0 {
		t.Fatalf("SessionState() after Enqueue() = %+v, want idle empty queue tail_seq 0", state)
	}
}

func newControlFixtureWithNow(t *testing.T, now func() time.Time) (*Stub, session.SessionID, *process.FakeHandle, *fakePTY) {
	t.Helper()
	t.Setenv("PI_HOME", t.TempDir())
	handle := process.NewFakeHandle(process.LaunchSpec{})
	pty := &fakePTY{}
	handle.SetPTY(pty)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), now, RuntimeConfig{Runner: runner})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	return svc, sessionID, handle, pty
}

func TestSchedulerDeliversManualInboxItemAsPlainUserMessage(t *testing.T) {
	current := time.Unix(1760000000, 0).UTC()
	svc, sessionID, _, pty := newControlFixtureWithNow(t, func() time.Time { return current })

	if _, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "manual follow-up"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	current = current.Add(31 * time.Second)
	if err := svc.runSchedulerDeliverySweep(context.Background()); err != nil {
		t.Fatalf("runSchedulerDeliverySweep() error = %v", err)
	}

	writes := pty.Writes()
	if len(writes) != 1 || writes[0] != "{\"type\":\"prompt\",\"message\":\"manual follow-up\"}\n" {
		t.Fatalf("pty writes after scheduler delivery = %#v, want plain manual prompt", writes)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Role != "user" || messages.Items[0].Text != "manual follow-up" {
		t.Fatalf("SessionMessages() = %+v, want plain manual user message", messages.Items)
	}
	inbox, err := svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox(after dispatch) error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].State != "delivered" || inbox.Items[0].DeliveredMessageID == "" {
		t.Fatalf("SessionInbox(after scheduler delivery) = %+v, want delivered manual item", inbox.Items)
	}
}

func TestSchedulerDeliversCodexManualInboxAfterAuthoritativeIdleProbe(t *testing.T) {
	current := time.Unix(1760000000, 0).UTC()
	svc := newStubWithRuntime(config.Load(), func() time.Time { return current }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_codex_inbox_idle_probe")
	packet, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		SourcePath: "/tmp/codex/stale-inbox.jsonl",
		Lines: []string{
			`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-stale-inbox"}}`,
		},
		Warmed:   true,
		Complete: true,
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket() error = %v", err)
	}
	runtimeState := newCodexRuntimeState(session.BackendCodex)
	runtimeState.markInitialized()
	runtimeState.setThreadID("thread-inbox-idle")
	runtimeState.transition(codexRuntimePhaseRunning, "codex_authoritative_running")
	var sent []string
	runtime := sessionRuntime{
		protocol: runtimeProtocolCodexRPC,
		codex:    runtimeState,
		helper: &runtimeIODHelper{
			streamClient: &iodclient.Client{},
			sessionID:    sessionID,
			generationID: generationID,
			historyFunc: func(context.Context) (iod.SessionHistoryResponsePacket, error) {
				return packet, nil
			},
			commandFunc: func(_ context.Context, _ iod.CommandName, payload json.RawMessage) error {
				var request struct {
					Method string `json:"method"`
				}
				if err := json.Unmarshal(payload, &request); err != nil {
					return err
				}
				sent = append(sent, request.Method)
				if request.Method == "thread/read" {
					_ = svc.applyRuntimeProjection(sessionID, runtimeProjectionFromCodex(mustDecodeCodexProjection(t, `{"id":"thread-read-1","result":{"thread":{"id":"thread-inbox-idle","status":{"type":"idle"},"turns":[{"id":"turn-stale-inbox","status":"interrupted"}]}}}`)))
				}
				return nil
			},
		},
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), created.Session.RuntimeID, created.Session.ThreadID, session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, ok, err := svc.registry.SwapRuntime(sessionID, identity, runtime, ""); err != nil || !ok {
		t.Fatalf("SwapRuntime(codex inbox idle probe) = (%v, %v)", ok, err)
	}
	if _, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "continue from inbox"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	current = current.Add(31 * time.Second)
	if err := svc.runSchedulerDeliverySweep(context.Background()); err != nil {
		t.Fatalf("runSchedulerDeliverySweep() error = %v", err)
	}
	if !slices.Contains(sent, "thread/read") || !slices.Contains(sent, "turn/start") {
		t.Fatalf("sent methods = %#v, want thread/read and turn/start", sent)
	}
	inbox, err := svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox(after codex scheduler delivery) error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].State != "delivered" || inbox.Items[0].DeliveredMessageID == "" {
		t.Fatalf("SessionInbox(after codex scheduler delivery) = %+v, want delivered manual item", inbox.Items)
	}
}

func TestEnqueueReplacesManualInboxItem(t *testing.T) {
	svc, sessionID, _, _ := newControlFixture(t)

	if _, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "first follow-up"}); err != nil {
		t.Fatalf("Enqueue(first) error = %v", err)
	}
	if _, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "replacement follow-up"}); err != nil {
		t.Fatalf("Enqueue(replacement) error = %v", err)
	}

	inbox, err := svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox() error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Source != "manual" || inbox.Items[0].State != "pending" || inbox.Items[0].Message != "replacement follow-up" {
		t.Fatalf("SessionInbox() = %+v, want one replaced pending manual item", inbox.Items)
	}
}

func TestCancelQueueCancelsManualInboxItem(t *testing.T) {
	svc, sessionID, _, _ := newControlFixture(t)

	if _, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "cancel me"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if _, err := svc.CancelQueue(context.Background(), CancelQueueRequest{SessionID: sessionID}); err != nil {
		t.Fatalf("CancelQueue() error = %v", err)
	}

	inbox, err := svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox() error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Source != "manual" || inbox.Items[0].State != "cancelled" {
		t.Fatalf("SessionInbox() = %+v, want cancelled manual item", inbox.Items)
	}
}

type blockingWritePTY struct {
	mu      sync.Mutex
	writes  []string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingWritePTY() *blockingWritePTY {
	return &blockingWritePTY{entered: make(chan struct{}), release: make(chan struct{})}
}

func (p *blockingWritePTY) Read([]byte) (int, error) { return 0, io.EOF }

func (p *blockingWritePTY) Write(data []byte) (int, error) {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, string(data))
	return len(data), nil
}

func (p *blockingWritePTY) Close() error { return nil }

func (p *blockingWritePTY) Resize(process.PTYSize) error { return nil }

func (p *blockingWritePTY) Writes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	copied := make([]string, len(p.writes))
	copy(copied, p.writes)
	return copied
}

func TestCancelQueueWaitsForQueuedDispatchCriticalSection(t *testing.T) {
	pty := newBlockingWritePTY()
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPTY(pty)
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: &process.FakeRunner{NextHandle: handle}})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	if _, _, err := svc.registry.ReplaceQueue(sessionID, "queued prompt"); err != nil {
		t.Fatalf("registry.ReplaceQueue() error = %v", err)
	}
	svc.scheduleQueuedDispatch(sessionID)
	select {
	case <-pty.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("queued dispatch did not reach runtime write")
	}

	cancelDone := make(chan error, 1)
	go func() {
		_, err := svc.CancelQueue(context.Background(), CancelQueueRequest{SessionID: sessionID})
		cancelDone <- err
	}()
	select {
	case err := <-cancelDone:
		t.Fatalf("CancelQueue returned before queued dispatch finished; err=%v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(pty.release)
	select {
	case err := <-cancelDone:
		if err != nil {
			t.Fatalf("CancelQueue() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelQueue did not return after queued dispatch finished")
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "queued prompt" {
		t.Fatalf("SessionMessages() = %+v, want queued prompt committed", messages.Items)
	}
	if _, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "next prompt"}); err != nil {
		t.Fatalf("Send() after CancelQueue() error = %v", err)
	}
	if writes := pty.Writes(); len(writes) != 2 {
		t.Fatalf("pty writes = %#v, want queued and next prompts", writes)
	}
}

func TestPIRPCQueuedPromptWaitsWhileRuntimeBusy(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	if _, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "first prompt"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	queueState, _, err := svc.registry.ReplaceQueue(sessionID, "second prompt")
	if err != nil {
		t.Fatalf("registry.ReplaceQueue() error = %v", err)
	}
	queued := queueSnapshotFromState(queueState)
	if len(queued.Items) != 1 || queued.Items[0].Text != "second prompt" {
		t.Fatalf("queued state = %+v, want queued second prompt while first send is busy", queued)
	}

	decoder := runtimeEventDecoder{backend: session.BackendPI}
	if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(`{"type":"turn_end"}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(turn_end) error = %v", err)
	}
	waitForAppCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return !state.Busy && len(state.Queue.Items) == 0 && state.TailSeq == 2 && len(pty.Writes()) == 2
	})

	writes := pty.Writes()
	if len(writes) != 2 || writes[1] != "{\"type\":\"prompt\",\"message\":\"second prompt\"}\n" {
		t.Fatalf("pty writes after queued dispatch = %#v, want queued second prompt RPC command", writes)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() after dispatch error = %v", err)
	}
	if len(messages.Items) != 2 || messages.Items[1].Role != "user" || messages.Items[1].Text != "second prompt" {
		t.Fatalf("SessionMessages() after dispatch = %+v, want queued second prompt committed", messages)
	}

	if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(`{"id":"actrail-state-busy","type":"response","command":"get_state","success":true,"data":{"isStreaming":true,"isCompacting":false,"pendingMessageCount":0}}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(get_state busy) error = %v", err)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after busy state error = %v", err)
	}
	if !state.Busy || len(state.Queue.Items) != 0 || state.TailSeq != 2 {
		t.Fatalf("SessionState() after busy state = %+v, want busy empty queue tail_seq 2", state)
	}

	after := sink.snapshot()
	if len(after.commits) != 2 {
		t.Fatalf("commit events count = %d, want 2", len(after.commits))
	}
	if got := after.commits[len(after.commits)-1].Message.Text; got != "second prompt" {
		t.Fatalf("last commit event text = %q, want %q", got, "second prompt")
	}
	if len(after.queueStates) == 0 || len(after.queueStates[len(after.queueStates)-1].Queue.Items) != 0 {
		t.Fatalf("last queue event = %+v, want empty queue", after.queueStates)
	}
	if len(after.states) == 0 || !after.states[len(after.states)-1].Busy || after.states[len(after.states)-1].QueueLen != 0 {
		t.Fatalf("last state event = %+v, want busy true queue_len 0 from Pi runtime state", after.states)
	}
}
