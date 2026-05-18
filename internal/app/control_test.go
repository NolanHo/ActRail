package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

type fakePTY struct {
	mu     sync.Mutex
	writes []string
}

func (p *fakePTY) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (p *fakePTY) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, string(data))
	return len(data), nil
}

func (p *fakePTY) Close() error {
	return nil
}

func (p *fakePTY) Resize(process.PTYSize) error {
	return nil
}

func (p *fakePTY) Writes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	copied := make([]string, len(p.writes))
	copy(copied, p.writes)
	return copied
}

func newControlFixture(t *testing.T) (*Stub, session.SessionID, *process.FakeHandle, *fakePTY) {
	t.Helper()
	t.Setenv("PI_HOME", t.TempDir())
	handle := process.NewFakeHandle(process.LaunchSpec{})
	pty := &fakePTY{}
	handle.SetPTY(pty)
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
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

func TestSessionTransportSnapshotMarksStaleAttachedHelperEnded(t *testing.T) {
	identity, err := session.NewLiveIdentity("s_stale", "r_1", "t_1", session.BackendPI.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	record := sessionRecord{
		identity: identity,
		transport: SessionTransportSnapshot{
			GenerationID: "g_dead",
			State:        SessionTransportStateAttached,
		},
	}
	snapshot := sessionTransportSnapshot(record)
	if snapshot.State != SessionTransportStateEnded || snapshot.Reason != "helper_not_running" {
		t.Fatalf("sessionTransportSnapshot() = %+v, want ended helper_not_running", snapshot)
	}
}

func TestSessionTransportSnapshotMarksStaleCodexAttachedHelperEnded(t *testing.T) {
	identity, err := session.NewLiveIdentity("s_stale_codex", "r_1", "t_1", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	record := sessionRecord{
		identity: identity,
		runtime:  sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: newCodexRuntimeState(session.BackendCodex)},
		transport: SessionTransportSnapshot{
			GenerationID: "g_dead_codex",
			State:        SessionTransportStateAttached,
		},
	}
	snapshot := sessionTransportSnapshot(record)
	if snapshot.State != SessionTransportStateEnded || snapshot.Reason != "helper_not_running" {
		t.Fatalf("sessionTransportSnapshot() = %+v, want ended helper_not_running", snapshot)
	}
}

func TestSameRuntimeHandleAllowsSameHelperGenerationReattachment(t *testing.T) {
	sessionID := mustSessionID(t, "s_same_helper")
	generationID := mustHelperGenerationID(t, "g_same_helper")
	left := sessionRuntime{
		protocol: runtimeProtocolCodexRPC,
		helper: &runtimeIODHelper{
			sessionID:    sessionID,
			generationID: generationID,
			manifest:     iod.GenerationManifest{HelloProof: iod.HelloProof{ControlSocketPath: "/tmp/actrail/same-helper.sock"}},
		},
		codex: newCodexRuntimeState(session.BackendCodex),
	}
	right := sessionRuntime{
		protocol: runtimeProtocolCodexRPC,
		helper: &runtimeIODHelper{
			sessionID:    sessionID,
			generationID: generationID,
			manifest:     iod.GenerationManifest{HelloProof: iod.HelloProof{ControlSocketPath: "/tmp/actrail/same-helper.sock"}},
		},
		codex: newCodexRuntimeState(session.BackendCodex),
	}
	if !sameRuntimeHandle(left, right) {
		t.Fatal("sameRuntimeHandle() = false, want true for reattached helper with same session/generation/socket")
	}

	right.helper.generationID = mustHelperGenerationID(t, "g_other_helper")
	if sameRuntimeHandle(left, right) {
		t.Fatal("sameRuntimeHandle() = true, want false for different helper generation")
	}
}

func TestSendWithRuntimeIDRouteWritesCanonicalSession(t *testing.T) {
	svc, sessionID, _, _ := newControlFixture(t)
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	runtimeID, ok := record.identity.RuntimeID()
	if !ok {
		t.Fatal("RuntimeID() ok = false, want true")
	}
	if _, err := svc.Send(context.Background(), SendRequest{SessionID: session.SessionID(runtimeID.String()), Text: "hello via runtime"}); err != nil {
		t.Fatalf("Send(runtime route) error = %v", err)
	}
	sent, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(sent.Items) != 1 || sent.Items[0].Text != "hello via runtime" {
		t.Fatalf("SessionMessages() = %+v, want canonical committed prompt", sent.Items)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: session.SessionID(runtimeID.String())})
	if err != nil {
		t.Fatalf("SessionMessages(runtime route) error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "hello via runtime" {
		t.Fatalf("SessionMessages(runtime route) = %+v, want canonical committed prompt", messages.Items)
	}
}

func TestEnqueueAcceptsEndedSessionAndCancelClearsManualInbox(t *testing.T) {
	svc, sessionID, _, _ := newControlFixture(t)
	if _, ok, err := svc.registry.SetTransport(sessionID, SessionTransportSnapshot{State: SessionTransportStateEnded, Reason: "helper_not_running"}); err != nil || !ok {
		t.Fatalf("SetTransport() = (_, %v, %v), want ok", ok, err)
	}
	queued, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "send after restart"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if len(queued.Queue.Items) != 0 {
		t.Fatalf("Enqueue().Queue = %+v, want empty runtime queue", queued.Queue)
	}
	inbox, err := svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox() error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Message != "send after restart" || inbox.Items[0].State != "pending" {
		t.Fatalf("SessionInbox() = %+v, want pending manual inbox item", inbox.Items)
	}
	cancelled, err := svc.CancelQueue(context.Background(), CancelQueueRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("CancelQueue() error = %v", err)
	}
	if len(cancelled.Queue.Items) != 0 {
		t.Fatalf("CancelQueue().Queue = %+v, want empty", cancelled.Queue)
	}
	inbox, err = svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox(after cancel) error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].State != "cancelled" {
		t.Fatalf("SessionInbox(after cancel) = %+v, want cancelled manual inbox item", inbox.Items)
	}
}

func TestDispatchQueuedPromptSkipsBrokenTransport(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)
	if _, _, err := svc.registry.ReplaceQueue(sessionID, "queued"); err != nil {
		t.Fatalf("registry.ReplaceQueue() error = %v", err)
	}
	if _, ok, err := svc.registry.SetTransport(sessionID, SessionTransportSnapshot{State: SessionTransportStateBroken, Reason: "stale runtime"}); err != nil || !ok {
		t.Fatalf("SetTransport() = (_, %v, %v), want ok", ok, err)
	}
	svc.dispatchQueuedPrompt(sessionID)
	if writes := pty.Writes(); len(writes) != 0 {
		t.Fatalf("pty writes = %#v, want none", writes)
	}
}

func TestSendPromptStaleCheckRunsInsideHelperCommandWindow(t *testing.T) {
	runtime := sessionRuntime{
		protocol: runtimeProtocolPIRPC,
		helper: &runtimeIODHelper{
			streamClient: &iodclient.Client{},
		},
	}
	called := false
	err := runtime.SendPromptWithStaleCheck(context.Background(), "prompt", func() bool {
		called = true
		return true
	})
	if !called {
		t.Fatal("stale check was not called")
	}
	if !errors.Is(err, errRuntimeChanged) {
		t.Fatalf("SendPromptWithStaleCheck() error = %v, want errRuntimeChanged", err)
	}
}

func TestSameRuntimeAllowsSameHandleWhenRuntimeIDVisibilityChanges(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	live, err := session.NewLiveIdentity("s_runtime_compare", "r_runtime_compare", "t_runtime_compare", "codex")
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	detached, err := session.NewDetachedIdentity("s_runtime_compare", "codex")
	if err != nil {
		t.Fatalf("NewDetachedIdentity() error = %v", err)
	}
	left := sessionRecord{identity: live, runtime: sessionRuntime{protocol: runtimeProtocolCodexRPC, handle: handle, codex: newCodexRuntimeState(session.BackendCodex)}}
	right := sessionRecord{identity: detached, runtime: left.runtime}
	if !sameRuntime(left, right) || !sameRuntime(right, left) {
		t.Fatalf("sameRuntime() = false, want true for same runtime handle with live/detached identity")
	}
}

func TestSendRetriesRuntimeChangedOnce(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	replacement := process.NewFakeHandle(process.LaunchSpec{})
	replacementPTY := &fakePTY{}
	replacement.SetPTY(replacementPTY)
	swapped := false
	record.runtime.helper = &runtimeIODHelper{
		streamClient: &iodclient.Client{},
		commandFunc: func(context.Context, iod.CommandName, json.RawMessage) error {
			if !swapped {
				swapped = true
				identity, err := session.NewLiveIdentity(sessionID.String(), "r_runtime_changed_retry", "t_runtime_changed_retry", session.BackendPI.String())
				if err != nil {
					return err
				}
				_, ok, err := svc.registry.SwapRuntime(sessionID, identity, sessionRuntime{protocol: runtimeProtocolTTY, handle: replacement}, "")
				if err != nil || !ok {
					return fmt.Errorf("SwapRuntime() = (%v, %v)", ok, err)
				}
				return errRuntimeChanged
			}
			return nil
		},
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_runtime_changed_original", "t_runtime_changed_original", session.BackendPI.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, ok, err := svc.registry.SwapRuntime(sessionID, identity, record.runtime, ""); err != nil || !ok {
		t.Fatalf("SwapRuntime(original) = (%v, %v)", ok, err)
	}

	sent, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "retry after runtime changed"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if sent.Message.Text != "retry after runtime changed" {
		t.Fatalf("Send() = %+v", sent)
	}
	if len(pty.Writes()) != 0 {
		t.Fatalf("old runtime writes = %#v, want none after stale helper", pty.Writes())
	}
	if writes := replacementPTY.Writes(); len(writes) != 1 || !strings.Contains(writes[0], "retry after runtime changed") {
		t.Fatalf("replacement runtime writes = %#v, want retried prompt", writes)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "retry after runtime changed" {
		t.Fatalf("SessionMessages() = %+v, want one committed user message", messages.Items)
	}
}

func TestSendRebindsRuntimeAfterMetadataRefresh(t *testing.T) {
	svc, sessionID, _, _ := newControlFixture(t)
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	refreshed := false
	commands := 0
	record.runtime.helper = &runtimeIODHelper{
		streamClient: &iodclient.Client{},
		commandFunc: func(context.Context, iod.CommandName, json.RawMessage) error {
			commands++
			if !refreshed {
				refreshed = true
				identity, err := session.NewLiveIdentity(sessionID.String(), "r_runtime_metadata_refresh", "t_runtime_metadata_refresh", session.BackendPI.String())
				if err != nil {
					return err
				}
				_, ok, err := svc.registry.SwapRuntime(sessionID, identity, record.runtime, "")
				if err != nil || !ok {
					return fmt.Errorf("SwapRuntime(metadata refresh) = (%v, %v)", ok, err)
				}
			}
			return nil
		},
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_runtime_metadata_original", "t_runtime_metadata_original", session.BackendPI.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, ok, err := svc.registry.SwapRuntime(sessionID, identity, record.runtime, ""); err != nil || !ok {
		t.Fatalf("SwapRuntime(original) = (%v, %v)", ok, err)
	}

	sent, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "send after metadata refresh"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if sent.Message.Text != "send after metadata refresh" {
		t.Fatalf("Send() = %+v", sent)
	}
	if commands != 1 {
		t.Fatalf("helper commands = %d, want one successful prompt command", commands)
	}
}

func TestSendRehydratesCodexHelperRuntimeInsideInputLock(t *testing.T) {
	cfg := persistentTestConfig(t)
	cfg.Storage.DataDir = filepath.Join("/tmp", fmt.Sprintf("arsend-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(cfg.Storage.DataDir) })
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_codex_send_rehydrate")
	threadID := "thread-codex-send-rehydrate"
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-send-rehydrate"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000007)
	commandCh := make(chan iod.CommandPacket, 4)
	cleanup := startCommandHelper(t, manifest, commandCh)
	defer cleanup()
	if err := svc.bindCurrentGeneration(helperGenerationBinding{SessionID: sessionID, GenerationID: generationID}); err != nil {
		t.Fatalf("bindCurrentGeneration() error = %v", err)
	}
	if _, _, err := svc.registry.SetSourceBinding(sessionID, threadID, "", sourceConfidenceExact); err != nil {
		t.Fatalf("SetSourceBinding() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	type sendResult struct {
		response SendResponse
		err      error
	}
	sendCh := make(chan sendResult, 1)
	go func() {
		sent, err := rehydrated.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "send after rehydrate"})
		sendCh <- sendResult{response: sent, err: err}
	}()
	deadline := time.After(time.Second)
	sawInitialize := false
	sawResume := false
	for {
		select {
		case packet := <-commandCh:
			if packet.Kind != iod.PacketCommandSend {
				t.Fatalf("command kind = %q, want %q", packet.Kind, iod.PacketCommandSend)
			}
			var request struct {
				Method string `json:"method"`
				Params struct {
					ThreadID string `json:"threadId"`
					Input    []struct {
						Text string `json:"text"`
					} `json:"input"`
				} `json:"params"`
			}
			if err := json.Unmarshal(packet.Payload, &request); err != nil {
				t.Fatalf("decode command payload %q: %v", string(packet.Payload), err)
			}
			if request.Method == "initialize" {
				sawInitialize = true
				rehydrated.noteCodexInitialized(sessionID)
				continue
			}
			if request.Method == "thread/resume" {
				sawResume = true
				if request.Params.ThreadID != threadID {
					t.Fatalf("thread/resume thread id = %q, want %q", request.Params.ThreadID, threadID)
				}
				rehydrated.noteCodexThreadID(sessionID, threadID)
				continue
			}
			if request.Method == "thread/start" {
				t.Fatalf("unexpected thread/start after rehydrate")
			}
			if request.Method != "turn/start" {
				continue
			}
			if !sawInitialize || !sawResume {
				t.Fatalf("turn/start arrived before bootstrap: sawInitialize=%v sawResume=%v", sawInitialize, sawResume)
			}
			if request.Params.ThreadID != threadID || len(request.Params.Input) != 1 || request.Params.Input[0].Text != "send after rehydrate" {
				t.Fatalf("command payload = %+v, want turn/start on restored thread", request)
			}
			result := <-sendCh
			if result.err != nil {
				t.Fatalf("Send() error = %v", result.err)
			}
			if result.response.Message.Text != "send after rehydrate" {
				t.Fatalf("Send() = %+v", result.response)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for helper turn/start command")
		}
	}
}

func TestSendRejectsWhenCodexAuthoritativeHistoryIsActive(t *testing.T) {
	svc, _, sessionID, _ := newSessionActionFixtureForBackend(t, "codex")
	generationID := mustHelperGenerationID(t, "g_codex_active_send")
	root := filepath.Join("/tmp", fmt.Sprintf("aractive-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000008)
	packet, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		SourcePath: "/tmp/codex/active.jsonl",
		Lines: []string{
			`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-active"}}`,
		},
		Warmed:   true,
		Complete: true,
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket() error = %v", err)
	}
	cleanup := startReplayHelper(t, manifest, helperReplayScript{SkipReplay: true, History: &packet})
	defer cleanup()
	runtimeState := newCodexRuntimeState(session.BackendCodex)
	runtimeState.markInitialized()
	runtimeState.setThreadID("thread-active")
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_codex_active_send", "t_codex_active_send", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtime := sessionRuntime{
		protocol: runtimeProtocolCodexRPC,
		codex:    runtimeState,
		helper: &runtimeIODHelper{
			manifest:     manifest,
			sessionID:    sessionID,
			generationID: generationID,
		},
	}
	if _, ok, err := svc.registry.SwapRuntime(sessionID, identity, runtime, ""); err != nil || !ok {
		t.Fatalf("SwapRuntime(active codex) = (%v, %v)", ok, err)
	}

	_, err = svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "should not go in directly"})
	if err == nil || !strings.Contains(err.Error(), "codex runtime is still running") {
		t.Fatalf("Send() error = %v, want authoritative running conflict", err)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if !state.Busy || state.RuntimeState != string(codexRuntimePhaseRunning) {
		t.Fatalf("SessionState() = busy:%v runtime:%q, want authoritative running", state.Busy, state.RuntimeState)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 0 {
		t.Fatalf("SessionMessages() = %+v, want no direct send committed", messages.Items)
	}
}

func TestCodexInterruptInvalidatesIODHistoryCache(t *testing.T) {
	svc, _, sessionID, _ := newSessionActionFixtureForBackend(t, "codex")
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	record.runtime = svc.runtimeForRecord(record)
	record.runtime.codex.markInitialized()
	record.runtime.codex.setThreadID("thread-cache-interrupt")
	if _, err := svc.setSessionTransport(sessionID, transportSnapshotCodexAttached()); err != nil {
		t.Fatalf("setSessionTransport() error = %v", err)
	}
	svc.codexIODHistoryMu.Lock()
	svc.codexIODHistory[sessionID] = codexIODHistoryCacheEntry{
		packet: iod.SessionHistoryResponsePacket{
			SourcePath: "stale.jsonl",
			Lines:      []string{"stale"},
			Warmed:     true,
			Complete:   true,
		},
	}
	svc.codexIODHistoryGen[sessionID] = 4
	svc.codexIODHistoryMu.Unlock()

	if _, err := svc.Interrupt(context.Background(), InterruptRequest{SessionID: sessionID}); err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}

	svc.codexIODHistoryMu.Lock()
	_, cached := svc.codexIODHistory[sessionID]
	gen := svc.codexIODHistoryGen[sessionID]
	svc.codexIODHistoryMu.Unlock()
	if cached {
		t.Fatal("codex IOD history cache still present after interrupt")
	}
	if gen <= 4 {
		t.Fatalf("codex IOD history generation = %d, want > 4", gen)
	}
}

func TestStubControlMethodsMutateRuntimeAndSessionState(t *testing.T) {
	svc, sessionID, handle, pty := newControlFixture(t)

	sent, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "Implement runtime control"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if sent.Message.Seq != 1 || sent.Message.Role != "user" || sent.Message.Text != "Implement runtime control" {
		t.Fatalf("Send() = %+v, want seq 1 user message", sent)
	}
	if !sent.Busy {
		t.Fatal("Send().Busy = false, want true immediately after Pi RPC send")
	}
	if len(sent.Queue.Items) != 0 {
		t.Fatalf("Send().Queue.Items = %+v, want empty", sent.Queue.Items)
	}
	writes := pty.Writes()
	if len(writes) != 1 || writes[0] != "{\"type\":\"prompt\",\"message\":\"Implement runtime control\"}\n" {
		t.Fatalf("pty writes after Send() = %#v, want RPC prompt command", writes)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after Send() error = %v", err)
	}
	if !state.Busy || state.TailSeq != 1 {
		t.Fatalf("SessionState() after Send() = %+v, want busy tail_seq 1 immediately after Pi RPC send", state)
	}
	decoder := runtimeEventDecoder{backend: session.BackendPI}
	if err := svc.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine([]byte(`{"id":"actrail-state-busy","type":"response","command":"get_state","success":true,"data":{"isStreaming":true,"isCompacting":false,"pendingMessageCount":0}}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(get_state busy) error = %v", err)
	}

	queued, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "first queued"})
	if err != nil {
		t.Fatalf("Enqueue(first) error = %v", err)
	}
	if len(queued.Queue.Items) != 0 {
		t.Fatalf("Enqueue(first) = %+v, want empty runtime queue", queued)
	}
	queued, err = svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "replacement queued"})
	if err != nil {
		t.Fatalf("Enqueue(replacement) error = %v", err)
	}
	if len(queued.Queue.Items) != 0 {
		t.Fatalf("Enqueue(replacement) = %+v, want empty runtime queue", queued)
	}
	if !queued.Busy {
		t.Fatal("Enqueue().Busy = false, want true")
	}
	inbox, err := svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox() error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Message != "replacement queued" || inbox.Items[0].State != "pending" {
		t.Fatalf("SessionInbox() = %+v, want replacement pending manual item", inbox.Items)
	}

	interrupted, err := svc.Interrupt(context.Background(), InterruptRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	if interrupted.Busy {
		t.Fatalf("Interrupt() = %+v, want busy false", interrupted)
	}
	if len(interrupted.Queue.Items) != 0 {
		t.Fatalf("Interrupt().Queue = %+v, want empty runtime queue", interrupted.Queue.Items)
	}
	inbox, err = svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox(after interrupt) error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Message != "replacement queued" || inbox.Items[0].State != "pending" {
		t.Fatalf("SessionInbox(after interrupt) = %+v, want retained pending manual item", inbox.Items)
	}
	if handle.InterruptCalls() != 0 {
		t.Fatalf("handle.InterruptCalls() = %d, want 0 when Pi RPC uses abort command", handle.InterruptCalls())
	}
	if handle.KillCalls() != 0 {
		t.Fatalf("handle.KillCalls() = %d, want 0 when cancelling Pi RPC loop", handle.KillCalls())
	}
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after Interrupt() error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy after Interrupt() = true, want false")
	}
	if len(state.Queue.Items) != 0 {
		t.Fatalf("SessionState().Queue after Interrupt() = %+v, want empty runtime queue", state.Queue.Items)
	}
	inbox, err = svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox(after state check) error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Message != "replacement queued" || inbox.Items[0].State != "pending" {
		t.Fatalf("SessionInbox(after state check) = %+v, want retained pending manual item", inbox.Items)
	}

	if err := svc.SetSessionUIRequest(sessionID, SessionUIRequestSnapshot{RequestID: "ask_1", Kind: "ask_user", Prompt: "Choose one option"}); err != nil {
		t.Fatalf("SetSessionUIRequest() error = %v", err)
	}
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() with ui request error = %v", err)
	}
	if state.UIRequest == nil || state.UIRequest.RequestID != "ask_1" {
		t.Fatalf("SessionState().UIRequest = %+v, want ask_1", state.UIRequest)
	}
	resolved, err := svc.RespondUI(context.Background(), UIResponseRequest{SessionID: sessionID, ResponseTo: "ask_1", Value: "A"})
	if err != nil {
		t.Fatalf("RespondUI() error = %v", err)
	}
	if resolved.ResolvedRequestID != "ask_1" {
		t.Fatalf("RespondUI().ResolvedRequestID = %q, want %q", resolved.ResolvedRequestID, "ask_1")
	}
	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after RespondUI() error = %v", err)
	}
	if state.UIRequest != nil {
		t.Fatalf("SessionState().UIRequest after RespondUI() = %+v, want nil", state.UIRequest)
	}
	writes = pty.Writes()
	abortIdx := slices.Index(writes, "{\"type\":\"abort\"}\n")
	responseIdx := slices.Index(writes, "{\"type\":\"extension_ui_response\",\"id\":\"ask_1\",\"value\":\"A\"}\n")
	if abortIdx < 0 || responseIdx < 0 || abortIdx > responseIdx {
		t.Fatalf("pty writes after RespondUI() = %#v, want RPC abort and ui response commands", writes)
	}
}

func TestStubControlMethodsReturnNotFoundForUnknownSession(t *testing.T) {
	svc := newStub(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() })
	unknown, err := session.ParseSessionID("s_404")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	cases := []struct {
		name string
		run  func() error
	}{
		{name: "send", run: func() error {
			_, err := svc.Send(context.Background(), SendRequest{SessionID: unknown, Text: "prompt"})
			return err
		}},
		{name: "enqueue", run: func() error {
			_, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: unknown, Text: "prompt"})
			return err
		}},
		{name: "interrupt", run: func() error {
			_, err := svc.Interrupt(context.Background(), InterruptRequest{SessionID: unknown})
			return err
		}},
		{name: "ui response", run: func() error {
			_, err := svc.RespondUI(context.Background(), UIResponseRequest{SessionID: unknown, ResponseTo: "ask_1", Value: "A"})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertNotFound(t, tc.run())
		})
	}
}

func TestSendAndUIResponseReturnConflictWithoutRuntimeInput(t *testing.T) {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	runner := &process.FakeRunner{NextHandle: handle}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	_, err = svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "prompt"})
	assertConflict(t, err)
	assertRuntimeControlDiagnostic(t, svc, sink, sessionID, "send")
	if err := svc.SetSessionUIRequest(sessionID, SessionUIRequestSnapshot{RequestID: "ask_1", Kind: "ask_user", Prompt: "Choose"}); err != nil {
		t.Fatalf("SetSessionUIRequest() error = %v", err)
	}
	_, err = svc.RespondUI(context.Background(), UIResponseRequest{SessionID: sessionID, ResponseTo: "ask_1", Value: "A"})
	assertConflict(t, err)
	assertRuntimeControlDiagnostic(t, svc, sink, sessionID, "ui_response")
}

func assertRuntimeControlDiagnostic(t *testing.T, svc *Stub, sink *captureRuntimeSink, sessionID session.SessionID, operation string) {
	t.Helper()
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) == 0 {
		t.Fatalf("SessionMessages() = %+v, want runtime control diagnostic", messages.Items)
	}
	msg := messages.Items[len(messages.Items)-1]
	if msg.Type != "pi_event" || msg.Text == "" {
		t.Fatalf("SessionMessages() last = %+v, want runtime control pi_event", msg)
	}
	snapshot := sink.snapshot()
	if len(snapshot.commits) == 0 {
		t.Fatalf("runtime commits = %+v, want runtime control diagnostic", snapshot.commits)
	}
	commit := snapshot.commits[len(snapshot.commits)-1]
	if commit.Message.Details["raw_type"] != "runtime_control_diagnostic" || commit.Message.Details["operation"] != operation {
		t.Fatalf("runtime commit last = %+v, want runtime_control_diagnostic %q", commit, operation)
	}
}

func TestControlMethodsReturnTransportResetRequired(t *testing.T) {
	svc, sessionID, _, _ := newControlFixture(t)
	_, ok, err := svc.registry.SetTransport(sessionID, SessionTransportSnapshot{
		GenerationID:  "g_reset",
		State:         SessionTransportStateBroken,
		ResetRequired: true,
		Reason:        iod.GenerationBreakAttachLost.String(),
	})
	if err != nil {
		t.Fatalf("SetTransport() error = %v", err)
	}
	if !ok {
		t.Fatalf("SetTransport(%q) ok = false", sessionID)
	}
	if err := svc.SetSessionUIRequest(sessionID, SessionUIRequestSnapshot{RequestID: "ask_1", Kind: "ask_user", Prompt: "Choose"}); err != nil {
		t.Fatalf("SetSessionUIRequest() error = %v", err)
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{name: "send", run: func() error {
			_, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "prompt"})
			return err
		}},
		{name: "interrupt", run: func() error {
			_, err := svc.Interrupt(context.Background(), InterruptRequest{SessionID: sessionID})
			return err
		}},
		{name: "respond_ui", run: func() error {
			_, err := svc.RespondUI(context.Background(), UIResponseRequest{SessionID: sessionID, ResponseTo: "ask_1", Value: "A"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var appErr *Error
			err := tc.run()
			if !errors.As(err, &appErr) {
				t.Fatalf("error = %v, want *Error", err)
			}
			if appErr.Code != "transport_reset_required" {
				t.Fatalf("error code = %q, want transport_reset_required", appErr.Code)
			}
		})
	}
}

func assertConflict(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("err = nil, want conflict")
	}
	var appErr *Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if appErr.Code != "conflict" {
		t.Fatalf("error code = %q, want %q", appErr.Code, "conflict")
	}
}
