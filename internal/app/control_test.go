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
	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/config"
	"actrail/internal/domain/codex"
	"actrail/internal/domain/session"
)

type fakePTY struct {
	mu           sync.Mutex
	writes       []string
	blockWrite   <-chan struct{}
	writeStarted chan<- struct{}
}

func (p *fakePTY) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (p *fakePTY) Write(data []byte) (int, error) {
	if p.writeStarted != nil {
		select {
		case p.writeStarted <- struct{}{}:
		default:
		}
	}
	if p.blockWrite != nil {
		<-p.blockWrite
	}
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

func TestNoteCodexThreadIDPreservesHelperGenerationTransport(t *testing.T) {
	cfg := persistentTestConfig(t)
	sessionID := mustSessionID(t, "s_codex_thread_generation")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_codex_thread_generation", "t_codex_thread_generation", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:  &identity,
		Backend:   session.BackendCodex,
		CWD:       t.TempDir(),
		Runtime:   sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: newCodexRuntimeState(session.BackendCodex)},
		Transport: SessionTransportSnapshot{GenerationID: "g_current", State: SessionTransportStateStarting, Reason: "codex_thread_resuming"},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}

	svc.noteCodexThreadID(sessionID, threadID)

	record, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatal("registry.Lookup() missing session")
	}
	if record.transport.State != SessionTransportStateAttached || record.transport.GenerationID != "g_current" || record.transport.Reason != "codex_thread" {
		t.Fatalf("transport = %+v, want attached g_current codex_thread", record.transport)
	}
}

func TestSessionTransportSnapshotPrefersCodexHelperGenerationOverAppServerPlaceholder(t *testing.T) {
	sessionID := mustSessionID(t, "s_codex_helper_generation_snapshot")
	generationID := mustHelperGenerationID(t, "g_codex_helper_generation_snapshot")
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_1", "t_1", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	record := sessionRecord{
		identity: identity,
		runtime: sessionRuntime{
			protocol:      runtimeProtocolCodexRPC,
			codex:         newCodexRuntimeState(session.BackendCodex),
			helper:        &runtimeIODHelper{generationID: generationID},
			helperBinding: &RuntimeHelperBinding{GenerationID: generationID},
		},
		transport: SessionTransportSnapshot{
			GenerationID: "codex_app_server",
			State:        SessionTransportStateAttached,
			Reason:       "codex_thread",
		},
	}

	snapshot := sessionTransportSnapshot(record)

	if snapshot.GenerationID != generationID.String() || snapshot.State != SessionTransportStateAttached || snapshot.Reason != "codex_thread" {
		t.Fatalf("sessionTransportSnapshot() = %+v, want attached generation %q", snapshot, generationID)
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

func TestAsyncSQLiteSendRetriesCurrentRuntimeAfterStaleCommit(t *testing.T) {
	svc, sessionID, _, pty := newControlFixture(t)
	svc.asyncSQLiteActions = true
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	replacement := process.NewFakeHandle(process.LaunchSpec{})
	replacementPTY := &fakePTY{}
	replacement.SetPTY(replacementPTY)
	originalRuntime := record.runtime
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_async_stale_original", "t_async_stale_original", session.BackendPI.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity(original) error = %v", err)
	}
	if _, ok, err := svc.registry.SwapRuntime(sessionID, identity, originalRuntime, ""); err != nil || !ok {
		t.Fatalf("SwapRuntime(original) = (%v, %v)", ok, err)
	}
	committed := make(chan struct{})
	release := make(chan struct{})
	originalRuntime.helper = &runtimeIODHelper{
		streamClient: &iodclient.Client{},
		commandFunc: func(context.Context, iod.CommandName, json.RawMessage) error {
			close(committed)
			<-release
			return errRuntimeChanged
		},
	}
	if _, ok, err := svc.registry.SwapRuntime(sessionID, identity, originalRuntime, ""); err != nil || !ok {
		t.Fatalf("SwapRuntime(original helper) = (%v, %v)", ok, err)
	}

	response, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "async retry after stale"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if response.Message.Text != "async retry after stale" || !response.Busy {
		t.Fatalf("Send() = %+v, want optimistic busy user message", response)
	}
	select {
	case <-committed:
	case <-time.After(time.Second):
		t.Fatal("async send did not reach original runtime")
	}
	if _, ok, err := svc.registry.SwapRuntime(sessionID, identity, sessionRuntime{protocol: runtimeProtocolTTY, handle: replacement}, ""); err != nil || !ok {
		t.Fatalf("SwapRuntime(replacement) = (%v, %v)", ok, err)
	}
	close(release)
	waitForTestCondition(t, func() bool {
		writes := replacementPTY.Writes()
		return len(writes) == 1 && strings.Contains(writes[0], "async retry after stale")
	})
	if writes := pty.Writes(); len(writes) != 0 {
		t.Fatalf("old runtime writes = %#v, want none after stale async send", writes)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "async retry after stale" {
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
	deadline := time.After(5 * time.Second)
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
			if sawResume && !sawInitialize {
				t.Fatalf("thread/resume arrived before initialize")
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

func TestAsyncSQLiteSendUsesMaterializedCodexRuntimeAfterRehydrate(t *testing.T) {
	cfg := persistentTestConfig(t)
	cfg.Storage.DataDir = filepath.Join("/tmp", fmt.Sprintf("arasyncsend-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(cfg.Storage.DataDir) })
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_codex_async_send_rehydrate")
	threadID := "thread-codex-async-send-rehydrate"
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-async-send-rehydrate"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000009)
	commandCh := make(chan iod.CommandPacket, 4)
	cleanup := startCommandHelper(t, manifest, commandCh)
	defer cleanup()
	if err := svc.bindCurrentGeneration(helperGenerationBinding{SessionID: sessionID, GenerationID: generationID}); err != nil {
		t.Fatalf("bindCurrentGeneration() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	if _, _, err := rehydrated.registry.SetSourceBinding(sessionID, threadID, "", sourceConfidenceExact); err != nil {
		t.Fatalf("SetSourceBinding() error = %v", err)
	}
	rehydrated.asyncSQLiteActions = true
	response, err := rehydrated.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "async send after rehydrate"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if response.Message.Text != "async send after rehydrate" || !response.Busy {
		t.Fatalf("Send() = %+v, want optimistic busy user message", response)
	}
	deadline := time.After(5 * time.Second)
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
			switch request.Method {
			case "initialize":
				sawInitialize = true
				rehydrated.noteCodexInitialized(sessionID)
			case "thread/resume":
				sawResume = true
				if request.Params.ThreadID != threadID {
					t.Fatalf("thread/resume thread id = %q, want %q", request.Params.ThreadID, threadID)
				}
				rehydrated.noteCodexThreadID(sessionID, threadID)
			case "thread/start":
				t.Fatal("unexpected thread/start after rehydrate")
			case "turn/start":
				if !sawInitialize || !sawResume {
					t.Fatalf("turn/start arrived before bootstrap: sawInitialize=%v sawResume=%v", sawInitialize, sawResume)
				}
				if request.Params.ThreadID != threadID || len(request.Params.Input) != 1 || request.Params.Input[0].Text != "async send after rehydrate" {
					t.Fatalf("command payload = %+v, want turn/start on restored thread", request)
				}
				return
			}
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

func TestSendAllowsWhenCodexHistoryActiveButThreadProbeIdle(t *testing.T) {
	svc, _, sessionID, _ := newSessionActionFixtureForBackend(t, "codex")
	generationID := mustHelperGenerationID(t, "g_codex_idle_probe_send")
	packet, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		SourcePath: "/tmp/codex/stale-active.jsonl",
		Lines: []string{
			`{"timestamp":"2026-05-08T15:58:02.548Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-stale-active"}}`,
		},
		Warmed:   true,
		Complete: true,
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket() error = %v", err)
	}
	runtimeState := newCodexRuntimeState(session.BackendCodex)
	runtimeState.markInitialized()
	runtimeState.setThreadID("thread-idle")
	runtimeState.transition(codexRuntimePhaseRunning, "codex_authoritative_running")
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_codex_idle_probe_send", "t_codex_idle_probe_send", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
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
					Params struct {
						Input []struct {
							Text string `json:"text"`
						} `json:"input"`
					} `json:"params"`
				}
				if err := json.Unmarshal(payload, &request); err != nil {
					return err
				}
				sent = append(sent, request.Method)
				if request.Method == "thread/read" {
					_ = svc.applyRuntimeProjection(sessionID, runtimeProjectionFromCodex(mustDecodeCodexProjection(t, `{"id":"thread-read-1","result":{"thread":{"id":"thread-idle","status":{"type":"idle"},"turns":[{"id":"turn-stale-active","status":"interrupted"}]}}}`)))
				}
				return nil
			},
		},
	}
	if _, ok, err := svc.registry.SwapRuntime(sessionID, identity, runtime, ""); err != nil || !ok {
		t.Fatalf("SwapRuntime(idle probe codex) = (%v, %v)", ok, err)
	}

	_, err = svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "continue after stale active"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !slices.Contains(sent, "thread/read") || !slices.Contains(sent, "turn/start") {
		t.Fatalf("sent methods = %#v, want thread/read and turn/start", sent)
	}
}

func TestAsyncCodexSendPersistsCommandLedgerWithUserMessage(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	defer func() { _ = svc.Close() }()
	svc.asyncSQLiteActions = true
	sessionID := mustSessionID(t, "s_codex_ledger")
	runtimeState := newCodexRuntimeState(session.BackendCodex)
	runtimeState.markInitialized()
	runtimeState.setThreadID("thread-ledger")
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_codex_ledger", "t_codex_ledger", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	blockSend := make(chan struct{})
	sendStarted := make(chan struct{}, 1)
	runtime := sessionRuntime{
		protocol: runtimeProtocolCodexRPC,
		codex:    runtimeState,
		helper: &runtimeIODHelper{
			streamClient: &iodclient.Client{},
			sessionID:    sessionID,
			generationID: mustHelperGenerationID(t, "g_codex_ledger"),
			commandFunc: func(context.Context, iod.CommandName, json.RawMessage) error {
				select {
				case sendStarted <- struct{}{}:
				default:
				}
				<-blockSend
				return nil
			},
		},
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              t.TempDir(),
		Title:            "codex ledger",
		BackendSessionID: "thread-ledger",
		Runtime:          runtime,
		Transport:        SessionTransportSnapshot{GenerationID: "g_codex_ledger", State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create(codex ledger) error = %v", err)
	}

	response, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "persist me"})
	if err != nil {
		close(blockSend)
		t.Fatalf("Send() error = %v", err)
	}
	if response.Message.Text != "persist me" || !response.Busy {
		close(blockSend)
		t.Fatalf("Send() = %+v, want committed busy prompt", response)
	}
	store, ok := svc.appStore.(*sqlitestore.SessionCatalog)
	if !ok {
		close(blockSend)
		t.Fatalf("appStore = %T, want sqlite catalog", svc.appStore)
	}
	open, err := store.ListOpenCodexSessionCommands(context.Background(), sessionID.String())
	if err != nil {
		close(blockSend)
		t.Fatalf("ListOpenCodexSessionCommands() error = %v", err)
	}
	if len(open) != 1 {
		close(blockSend)
		t.Fatalf("len(open commands) = %d, want 1: %+v", len(open), open)
	}
	wantMessageID := fmt.Sprintf("seq:%d", response.Message.Seq)
	if open[0].State != codexCommandPending.String() || open[0].Text != "persist me" || open[0].MessageID != wantMessageID {
		close(blockSend)
		t.Fatalf("open command = %+v, want pending send command for %s", open[0], wantMessageID)
	}
	select {
	case <-sendStarted:
	case <-time.After(time.Second):
		close(blockSend)
		t.Fatal("async Codex send did not start")
	}
	open, err = store.ListOpenCodexSessionCommands(context.Background(), sessionID.String())
	if err != nil {
		close(blockSend)
		t.Fatalf("ListOpenCodexSessionCommands(dispatching) error = %v", err)
	}
	if len(open) != 1 || open[0].State != codexCommandDispatching.String() || open[0].AttemptCount != 1 {
		close(blockSend)
		t.Fatalf("open command while send blocked = %+v, want dispatching attempt 1", open)
	}
	close(blockSend)
	waitForTestCondition(t, func() bool {
		open, err = store.ListOpenCodexSessionCommands(context.Background(), sessionID.String())
		return err == nil && len(open) == 1 && open[0].State == codexCommandAccepted.String()
	})
}

func TestAsyncCodexSendIgnoresDuplicateCommandLedgerRow(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	defer func() { _ = svc.Close() }()
	svc.asyncSQLiteActions = true
	sessionID := mustSessionID(t, "s_codex_ledger_duplicate")
	runtimeState := newCodexRuntimeState(session.BackendCodex)
	runtimeState.markInitialized()
	runtimeState.setThreadID("thread-ledger-duplicate")
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_codex_ledger_duplicate", "t_codex_ledger_duplicate", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              t.TempDir(),
		Title:            "codex duplicate ledger",
		BackendSessionID: "thread-ledger-duplicate",
		Runtime: sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			codex:    runtimeState,
			helper: &runtimeIODHelper{
				streamClient: &iodclient.Client{},
				sessionID:    sessionID,
				generationID: mustHelperGenerationID(t, "g_codex_ledger_duplicate"),
				commandFunc:  func(context.Context, iod.CommandName, json.RawMessage) error { return nil },
			},
		},
		Transport: SessionTransportSnapshot{GenerationID: "g_codex_ledger_duplicate", State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create(codex duplicate ledger) error = %v", err)
	}
	store, ok := svc.appStore.(*sqlitestore.SessionCatalog)
	if !ok {
		t.Fatalf("appStore = %T, want sqlite catalog", svc.appStore)
	}
	if err := store.InsertCodexSessionCommand(context.Background(), sqlitestore.CodexSessionCommandRow{
		CommandID: codexSendCommandID(sessionID, 2),
		SessionID: sessionID.String(),
		Kind:      "send",
		Text:      "persist me",
		MessageID: "seq:2",
		State:     codexCommandPending.String(),
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("preload duplicate command row error = %v", err)
	}

	response, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "persist me"})
	if err != nil {
		t.Fatalf("Send() error = %v, want duplicate command ledger row to be idempotent", err)
	}
	if response.Message.Seq != 2 || response.Message.Text != "persist me" || !response.Busy {
		t.Fatalf("Send() = %+v, want committed seq 2 busy prompt", response)
	}
}

func TestAsyncCodexCommandLedgerReflectsAndCompletesFromLiveEvents(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	defer func() { _ = svc.Close() }()
	svc.asyncSQLiteActions = true
	sessionID := mustSessionID(t, "s_codex_ledger_live")
	runtimeState := newCodexRuntimeState(session.BackendCodex)
	runtimeState.markInitialized()
	runtimeState.setThreadID("thread-ledger-live")
	runtime := sessionRuntime{
		protocol: runtimeProtocolCodexRPC,
		codex:    runtimeState,
		helper: &runtimeIODHelper{
			streamClient: &iodclient.Client{},
			sessionID:    sessionID,
			generationID: mustHelperGenerationID(t, "g_codex_ledger_live"),
			commandFunc:  func(context.Context, iod.CommandName, json.RawMessage) error { return nil },
		},
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_codex_ledger_live", "t_codex_ledger_live", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              t.TempDir(),
		Title:            "codex ledger live",
		BackendSessionID: "thread-ledger-live",
		Runtime:          runtime,
		Transport:        SessionTransportSnapshot{GenerationID: "g_codex_ledger_live", State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}
	response, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "reflect me"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	store, ok := svc.appStore.(*sqlitestore.SessionCatalog)
	if !ok {
		t.Fatalf("appStore = %T, want sqlite catalog", svc.appStore)
	}
	waitForTestCondition(t, func() bool {
		open, err := store.ListOpenCodexSessionCommands(context.Background(), sessionID.String())
		return err == nil && len(open) == 1 && open[0].State == codexCommandAccepted.String()
	})
	if err := svc.applyRuntimeProjection(sessionID, runtimeProjectionFromCodex(mustDecodeCodexProjection(t, `{"method":"item/completed","params":{"threadId":"thread-ledger-live","turnId":"turn-ledger-live","item":{"type":"userMessage","id":"user-live-1","text":"reflect me"}}}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(user) error = %v", err)
	}
	open, err := store.ListOpenCodexSessionCommands(context.Background(), sessionID.String())
	if err != nil {
		t.Fatalf("ListOpenCodexSessionCommands(reflected) error = %v", err)
	}
	if len(open) != 1 || open[0].State != codexCommandReflected.String() {
		t.Fatalf("open command after user reflection = %+v, want reflected", open)
	}
	if prompt := svc.codexOutboundPromptText(sessionID); prompt != "" {
		t.Fatalf("codex outbound prompt = %q, want cleared after reflection", prompt)
	}
	if err := svc.applyRuntimeProjection(sessionID, runtimeProjectionFromCodex(mustDecodeCodexProjection(t, `{"method":"turn/completed","params":{"threadId":"thread-ledger-live","turn":{"id":"turn-ledger-live","status":"completed","error":null}}}`))); err != nil {
		t.Fatalf("applyRuntimeProjection(turn completed) error = %v", err)
	}
	waitForTestCondition(t, func() bool {
		open, err = store.ListOpenCodexSessionCommands(context.Background(), sessionID.String())
		if err != nil || len(open) != 0 {
			return false
		}
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy && state.RuntimeState == string(codexRuntimePhaseIdle)
	})
	if response.Message.Text != "reflect me" {
		t.Fatalf("Send() message = %+v, want reflect me", response.Message)
	}
}

func TestAsyncCodexSendMarksCommandLedgerFailed(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	defer func() { _ = svc.Close() }()
	svc.asyncSQLiteActions = true
	sessionID := mustSessionID(t, "s_codex_ledger_failed")
	runtimeState := newCodexRuntimeState(session.BackendCodex)
	runtimeState.markInitialized()
	runtimeState.setThreadID("thread-ledger-failed")
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_codex_ledger_failed", "t_codex_ledger_failed", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	sendErr := errors.New("runtime send failed")
	runtime := sessionRuntime{
		protocol: runtimeProtocolCodexRPC,
		codex:    runtimeState,
		helper: &runtimeIODHelper{
			streamClient: &iodclient.Client{},
			sessionID:    sessionID,
			generationID: mustHelperGenerationID(t, "g_codex_ledger_failed"),
			commandFunc: func(context.Context, iod.CommandName, json.RawMessage) error {
				return sendErr
			},
		},
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendCodex,
		CWD:              t.TempDir(),
		Title:            "codex ledger failed",
		BackendSessionID: "thread-ledger-failed",
		Runtime:          runtime,
		Transport:        SessionTransportSnapshot{GenerationID: "g_codex_ledger_failed", State: SessionTransportStateAttached},
	}); err != nil {
		t.Fatalf("registry.Create(codex ledger failed) error = %v", err)
	}

	response, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "fail me"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	store, ok := svc.appStore.(*sqlitestore.SessionCatalog)
	if !ok {
		t.Fatalf("appStore = %T, want sqlite catalog", svc.appStore)
	}
	waitForTestCondition(t, func() bool {
		open, err := store.ListOpenCodexSessionCommands(context.Background(), sessionID.String())
		if err != nil || len(open) != 0 {
			return false
		}
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy && state.RuntimeState == string(codexRuntimePhaseFailed)
	})
	commands, err := store.ListOpenCodexSessionCommands(context.Background(), sessionID.String())
	if err != nil {
		t.Fatalf("ListOpenCodexSessionCommands() error = %v", err)
	}
	if len(commands) != 0 {
		t.Fatalf("open commands = %+v, want none after failed terminal state", commands)
	}
	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	foundPrompt := false
	for _, item := range messages.Items {
		if item.Role == "user" && item.Text == response.Message.Text {
			foundPrompt = true
			break
		}
	}
	if !foundPrompt {
		t.Fatalf("SessionMessages() = %+v, want committed failed prompt visible", messages.Items)
	}
}

func mustDecodeCodexProjection(t *testing.T, raw string) codex.Projection {
	t.Helper()
	projection, ok := codex.DecodeAppServerLine([]byte(raw))
	if !ok {
		t.Fatalf("DecodeAppServerLine(%s) ok = false", raw)
	}
	return projection
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
