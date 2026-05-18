package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/process"
	"actrail/internal/domain/message"
	"actrail/internal/domain/session"
)

type helperReplayScript struct {
	AfterOffset iod.WALOffset
	SkipReplay  bool
	Items       []iod.ReplayItemPacket
	Done        iod.ReplayDonePacket
	LivePackets []any
	History     *iod.SessionHistoryResponsePacket
}

func TestHelperDiscovery(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_7")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/helper-discovery"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000000)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		AfterOffset: 0,
		Done:        mustReplayDonePacket(t, sessionID, generationID, 0, 0),
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	attachment, ok := rehydrated.helpers.Attachment(sessionID)
	if !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if attachment.Binding.GenerationID != generationID {
		t.Fatalf("attachment generation id = %q, want %q", attachment.Binding.GenerationID, generationID)
	}
	if attachment.ManifestPath != manifestPath {
		t.Fatalf("attachment manifest path = %q, want %q", attachment.ManifestPath, manifestPath)
	}
	if len(rehydrated.helpers.Fenced()) != 0 {
		t.Fatalf("fenced helpers = %#v, want none", rehydrated.helpers.Fenced())
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.GenerationID != generationID.String() {
		t.Fatalf("SessionState().Transport = %+v, want attached generation %q", state.Transport, generationID)
	}
}

func TestStartupHealthMarksMissingHelperEnded(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_missing_helper")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/missing-helper"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateEnded || state.Transport.Reason != "helper_not_running" || state.Transport.GenerationID != generationID.String() {
		t.Fatalf("SessionState().Transport = %+v, want ended missing helper generation %q", state.Transport, generationID)
	}
}

func TestStartupHealthMarksUndialableHelperEnded(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_undialable_helper")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/undialable-helper"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	_ = writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000000)

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateEnded || state.Transport.Reason != "helper_not_running" || state.Transport.GenerationID != generationID.String() {
		t.Fatalf("SessionState().Transport = %+v, want ended undialable helper generation %q", state.Transport, generationID)
	}
}

func TestStartupHealthMarksCodexMissingHelperWithLiveChildBroken(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_codex_orphan_child")
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatalf("start child process error = %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-orphan-child"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	_ = writeHelperManifestWithPID(t, manifestPath, sessionID, generationID, os.Getpid(), 1760000000)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", manifestPath, err)
	}
	var manifest iod.GenerationManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", manifestPath, err)
	}
	childPID := child.Process.Pid
	manifest.ChildPID = &childPID
	if err := iodclient.WriteGenerationManifest(manifestPath, manifest); err != nil {
		t.Fatalf("WriteGenerationManifest(%q) error = %v", manifestPath, err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateBroken || state.Transport.Reason != "helper_missing_child_alive" || !state.Transport.ResetRequired || state.Transport.GenerationID != generationID.String() {
		t.Fatalf("SessionState().Transport = %+v, want broken reset_required orphan child generation %q", state.Transport, generationID)
	}
}

func TestServerReattach(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_11")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID, LastReplayOffset: 5}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/server-reattach"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000001)
	replayItem := mustReplayOutputPacket(t, sessionID, generationID, 1, 3,
		"{\"type\":\"extension_ui_request\",\"id\":\"ui-reattach\",\"method\":\"select\",\"question\":\"Where should this go?\",\"options\":[\"Details\",\"Sidebar\"]}\n"+
			"{\"type\":\"message.delta\",\"turn_id\":\"turn-reattach\",\"role\":\"assistant\",\"delta\":\"Replay and live projection works.\"}\n"+
			"{\"type\":\"message_end\",\"message\":{\"role\":\"toolResult\",\"toolCallId\":\"ui-reattach\",\"toolName\":\"ask_user\",\"details\":{\"answer\":\"Sidebar\",\"cancelled\":false}}}\n"+
			"{\"type\":\"turn.completed\",\"turn_id\":\"turn-reattach\",\"role\":\"assistant\",\"text\":\"Replay and live projection works.\"}\n")
	livePacket := mustStateOutputPacket(t, sessionID, generationID, 4,
		"{\"type\":\"turn_end\"}\n")
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		AfterOffset: 0,
		Items:       []iod.ReplayItemPacket{replayItem},
		Done:        mustReplayDonePacket(t, sessionID, generationID, 0, 1),
		LivePackets: []any{livePacket},
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	attachment, ok := rehydrated.helpers.Attachment(sessionID)
	if !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if attachment.Binding.GenerationID != generationID {
		t.Fatalf("attachment generation id = %q, want %q", attachment.Binding.GenerationID, generationID)
	}
	if attachment.Binding.LastReplayOffset != 1 {
		t.Fatalf("attachment last replay offset = %d, want 1", attachment.Binding.LastReplayOffset)
	}
	messages, err := rehydrated.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "Replay and live projection works." {
		t.Fatalf("SessionMessages().Items = %#v, want replayed committed message", messages.Items)
	}
	bindings, err := rehydrated.helperBindings.Load()
	if err != nil {
		t.Fatalf("helperBindings.Load() error = %v", err)
	}
	if bindings[sessionID].GenerationID != generationID || bindings[sessionID].LastReplayOffset != 1 {
		t.Fatalf("saved binding = %+v, want generation %q offset 1", bindings[sessionID], generationID)
	}
}

func TestCodexReattachSkipsReplayAndProjectsLiveOutput(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_codex_reattach")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID, LastReplayOffset: 5}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-reattach"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	livePacket := mustStateOutputPacket(t, sessionID, generationID, 4,
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-reattach-1\"}}}\n"+
			"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-reattach-1\",\"turn\":{\"id\":\"turn-codex-reattach-1\",\"status\":\"inProgress\",\"error\":null}}}\n"+
			"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-reattach-1\",\"turnId\":\"turn-codex-reattach-1\",\"itemId\":\"item-codex-reattach-1\",\"delta\":\"Live Codex projection works.\"}}\n"+
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-reattach-1\",\"turnId\":\"turn-codex-reattach-1\",\"item\":{\"type\":\"agentMessage\",\"id\":\"item-codex-reattach-1\",\"text\":\"Replay and live Codex projection works.\"}}}\n"+
			"{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-reattach-1\",\"turn\":{\"id\":\"turn-codex-reattach-1\",\"status\":\"completed\",\"error\":null}}}\n")
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		SkipReplay:  true,
		LivePackets: []any{livePacket},
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	attachment, ok := rehydrated.helpers.Attachment(sessionID)
	if !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if attachment.Binding.GenerationID != generationID {
		t.Fatalf("attachment generation id = %q, want %q", attachment.Binding.GenerationID, generationID)
	}
	if attachment.Binding.LastReplayOffset != 5 {
		t.Fatalf("attachment last replay offset = %d, want preserved cursor 5", attachment.Binding.LastReplayOffset)
	}
	waitForAppCondition(t, func() bool {
		messages, err := rehydrated.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		if err != nil || len(messages.Items) != 1 {
			return false
		}
		state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		return !state.Busy && messages.Items[0].Text == "Replay and live Codex projection works."
	})
	messages, err := rehydrated.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "Replay and live Codex projection works." {
		t.Fatalf("SessionMessages().Items = %#v, want committed replay/live codex message", messages.Items)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy = true, want false after live codex projection")
	}
	if state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState().PartialAssistantTurn = %+v, want nil after codex turn end", state.PartialAssistantTurn)
	}
	record, err := rehydrated.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	_, threadID, turnID := record.runtime.codex.snapshot()
	if threadID != "thread-codex-reattach-1" || turnID != "" {
		t.Fatalf("codex runtime state = (thread=%q turn=%q), want (thread-codex-reattach-1, empty)", threadID, turnID)
	}
	bindings, err := rehydrated.helperBindings.Load()
	if err != nil {
		t.Fatalf("helperBindings.Load() error = %v", err)
	}
	if bindings[sessionID].GenerationID != generationID || bindings[sessionID].LastReplayOffset != 5 {
		t.Fatalf("saved binding = %+v, want generation %q preserved offset 5", bindings[sessionID], generationID)
	}
}

func TestRuntimeLauncherAttachesExistingIODBeforeStartingNewHelper(t *testing.T) {
	sessionID := mustSessionID(t, "s_attach_existing_iod")
	generationID := mustHelperGenerationID(t, "g_attach_existing")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{SkipReplay: true})
	defer cleanup()

	launcher := processRuntimeLauncher{
		iodRuntimeRoot: root,
		useIODHelper:   true,
		resolveIODHelperBinPath: func() (string, error) {
			t.Fatal("resolveIODHelperBinPath called; existing IOD should be attached before launching")
			return "", nil
		},
		currentHelperBinding: func(session.SessionID) (*RuntimeHelperBinding, error) {
			return &RuntimeHelperBinding{GenerationID: generationID}, nil
		},
	}
	runtime, err := launcher.Launch(context.Background(), runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex, CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if runtime.helper == nil {
		t.Fatal("runtime.helper = nil, want attached existing IOD helper")
	}
	binding, err := runtime.CurrentHelperBinding(sessionID)
	if err != nil {
		t.Fatalf("CurrentHelperBinding() error = %v", err)
	}
	if binding == nil || binding.GenerationID != generationID {
		t.Fatalf("binding = %+v, want generation %q", binding, generationID)
	}
	if runtime.handle != nil {
		t.Fatalf("runtime.handle = %+v, want nil for attached existing IOD", runtime.handle)
	}
}

func TestRuntimeLauncherAttachesExistingCodexIODWithoutCurrentBinding(t *testing.T) {
	sessionID := mustSessionID(t, "s_attach_existing_unbound_iod")
	generationID := mustHelperGenerationID(t, "g_attach_existing_unbound")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{SkipReplay: true})
	defer cleanup()

	launcher := processRuntimeLauncher{
		iodRuntimeRoot: root,
		useIODHelper:   true,
		resolveIODHelperBinPath: func() (string, error) {
			t.Fatal("resolveIODHelperBinPath called; unbound existing Codex IOD should be attached by session id")
			return "", nil
		},
	}
	runtime, err := launcher.Launch(context.Background(), runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex, CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if runtime.helper == nil || runtime.helper.generationID != generationID {
		t.Fatalf("runtime.helper = %+v, want attached generation %q", runtime.helper, generationID)
	}
	binding, err := runtime.CurrentHelperBinding(sessionID)
	if err != nil {
		t.Fatalf("CurrentHelperBinding() error = %v", err)
	}
	if binding == nil || binding.GenerationID != generationID {
		t.Fatalf("binding = %+v, want generation %q", binding, generationID)
	}
}

func TestPersistentStubAdoptsUnboundCodexIODOnRestart(t *testing.T) {
	cfg := persistentTestConfig(t)
	cfg.Storage.DataDir = filepath.Join("/tmp", fmt.Sprintf("ar-adopt-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(cfg.Storage.DataDir) })
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	cwd := t.TempDir()
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: cwd})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_codex_unbound_adopt")
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{SkipReplay: true})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	attachment, ok := rehydrated.helpers.Attachment(sessionID)
	if !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if attachment.Binding.GenerationID != generationID {
		t.Fatalf("attachment generation id = %q, want %q", attachment.Binding.GenerationID, generationID)
	}
	if hasFenceReason(rehydrated.helpers.Fenced(), generationID, helperFenceCurrentGenerationUnbound) {
		t.Fatalf("fenced helpers = %+v, want unbound Codex helper adopted", rehydrated.helpers.Fenced())
	}
	bindings, err := rehydrated.helperBindings.Load()
	if err != nil {
		t.Fatalf("helperBindings.Load() error = %v", err)
	}
	if bindings[sessionID].GenerationID != generationID {
		t.Fatalf("saved binding = %+v, want adopted generation %q", bindings[sessionID], generationID)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.GenerationID != generationID.String() {
		t.Fatalf("SessionState().Transport = %+v, want attached generation %q", state.Transport, generationID)
	}
}

func TestRuntimeLauncherForceNewIODReusesHealthyCodexAttach(t *testing.T) {
	sessionID := mustSessionID(t, "s_force_new_iod")
	generationID := mustHelperGenerationID(t, "g_force_new_existing")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{SkipReplay: true})
	defer cleanup()
	launcher := processRuntimeLauncher{
		iodRuntimeRoot: root,
		useIODHelper:   true,
		resolveIODHelperBinPath: func() (string, error) {
			t.Fatal("resolveIODHelperBinPath called; healthy same-session Codex IOD should be reused before launching")
			return "", nil
		},
		currentHelperBinding: func(session.SessionID) (*RuntimeHelperBinding, error) {
			return &RuntimeHelperBinding{GenerationID: generationID}, nil
		},
	}
	runtime, err := launcher.Launch(context.Background(), runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex, CWD: t.TempDir(), ForceNewIOD: true})
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if runtime.helper == nil || runtime.helper.generationID != generationID || !runtime.attachedExistingIOD {
		t.Fatalf("runtime.helper = %+v attached=%v, want reused generation %q", runtime.helper, runtime.attachedExistingIOD, generationID)
	}
}

func TestRuntimeLauncherRejectsMismatchedCodexIODHistoryOnAttach(t *testing.T) {
	sessionID := mustSessionID(t, "s_mismatched_codex_attach")
	generationID := mustHelperGenerationID(t, "g_mismatched_codex_attach")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", root, err)
	}
	sourcePath := filepath.Join(root, "rollout-thread-other.jsonl")
	if err := os.WriteFile(sourcePath, []byte(`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"thread-other","cwd":"/tmp/fake-codex"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", sourcePath, err)
	}
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	packet, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		SourcePath:   sourcePath,
		Messages:     []iod.SessionHistoryMessage{{Seq: 1, Role: "assistant", Kind: "message", Text: "other thread"}},
		IndexedCount: 1,
		TaskComplete: true,
		Warmed:       true,
		Complete:     true,
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket() error = %v", err)
	}
	cleanup := startReplayHelper(t, manifest, helperReplayScript{SkipReplay: true, History: &packet})
	defer cleanup()
	wantErr := errors.New("new helper requested")
	launcher := processRuntimeLauncher{
		iodRuntimeRoot: root,
		useIODHelper:   true,
		resolveIODHelperBinPath: func() (string, error) {
			return "", wantErr
		},
		currentHelperBinding: func(session.SessionID) (*RuntimeHelperBinding, error) {
			return &RuntimeHelperBinding{GenerationID: generationID}, nil
		},
	}
	runtime, err := launcher.Launch(context.Background(), runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex, CWD: t.TempDir(), CodexThreadID: "thread-wanted"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Launch() error = %v, want new helper after mismatched attach", err)
	}
	if runtime.helper != nil {
		t.Fatalf("runtime.helper = %+v, want no mismatched attachment", runtime.helper)
	}
}

func TestRuntimeLauncherRejectsMismatchedCodexIODManifestHistoryOnAttach(t *testing.T) {
	sessionID := mustSessionID(t, "s_mismatched_codex_manifest_attach")
	generationID := mustHelperGenerationID(t, "g_mismatched_codex_manifest_attach")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	oldSourcePath := filepath.Join(root, "rollout-thread-old.jsonl")
	if err := os.MkdirAll(filepath.Dir(oldSourcePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(oldSourcePath), err)
	}
	if err := os.WriteFile(oldSourcePath, []byte(`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"thread-old","cwd":"/tmp/fake-codex"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", oldSourcePath, err)
	}
	newSourcePath := filepath.Join(root, "rollout-thread-wanted.jsonl")
	if err := os.WriteFile(newSourcePath, []byte(`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"thread-wanted","cwd":"/tmp/fake-codex"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", newSourcePath, err)
	}
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	manifest.SessionHistoryPath = oldSourcePath
	if err := iodclient.WriteGenerationManifest(manifestPath, manifest); err != nil {
		t.Fatalf("WriteGenerationManifest(%q) error = %v", manifestPath, err)
	}
	cleanup := startReplayHelper(t, manifest, helperReplayScript{SkipReplay: true})
	defer cleanup()
	wantErr := errors.New("new helper requested")
	launcher := processRuntimeLauncher{
		iodRuntimeRoot: root,
		useIODHelper:   true,
		resolveIODHelperBinPath: func() (string, error) {
			return "", wantErr
		},
		currentHelperBinding: func(session.SessionID) (*RuntimeHelperBinding, error) {
			return &RuntimeHelperBinding{GenerationID: generationID}, nil
		},
	}
	runtime, err := launcher.Launch(context.Background(), runtimeLaunchRequest{
		SessionID:     sessionID,
		Backend:       session.BackendCodex,
		CWD:           t.TempDir(),
		CodexThreadID: "thread-wanted",
		SessionPath:   newSourcePath,
		ForceNewIOD:   true,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Launch() error = %v, want new helper after mismatched manifest attach", err)
	}
	if runtime.helper != nil {
		t.Fatalf("runtime.helper = %+v, want no mismatched attachment", runtime.helper)
	}
}

func TestRuntimeLauncherFallsBackToSameSessionCodexIODWhenCurrentBindingFails(t *testing.T) {
	sessionID := mustSessionID(t, "s_fallback_same_session_attach")
	currentGeneration := mustHelperGenerationID(t, "g_current_attach")
	staleGeneration := mustHelperGenerationID(t, "g_stale_attach")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	currentManifestPath := iodclient.GenerationManifestPath(root, sessionID, currentGeneration)
	_ = writeHelperManifest(t, currentManifestPath, sessionID, currentGeneration, 1760000007)
	staleManifestPath := iodclient.GenerationManifestPath(root, sessionID, staleGeneration)
	staleManifest := writeHelperManifest(t, staleManifestPath, sessionID, staleGeneration, 1760000006)
	cleanup := startReplayHelper(t, staleManifest, helperReplayScript{SkipReplay: true})
	defer cleanup()
	launcher := processRuntimeLauncher{
		iodRuntimeRoot: root,
		useIODHelper:   true,
		resolveIODHelperBinPath: func() (string, error) {
			t.Fatal("resolveIODHelperBinPath called; same-session Codex IOD fallback should attach before launching")
			return "", nil
		},
		currentHelperBinding: func(session.SessionID) (*RuntimeHelperBinding, error) {
			return &RuntimeHelperBinding{GenerationID: currentGeneration}, nil
		},
	}
	runtime, err := launcher.Launch(context.Background(), runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex, CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if runtime.helper == nil || runtime.helper.generationID != staleGeneration {
		t.Fatalf("runtime.helper = %+v, want fallback generation %q", runtime.helper, staleGeneration)
	}
}

func TestAttachedExistingIODRollbackReleaseDoesNotShutdownOrCleanupHelper(t *testing.T) {
	sessionID := mustSessionID(t, "s_attach_rollback")
	generationID := mustHelperGenerationID(t, "g_attach_rollback")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	history := iod.SessionHistorySnapshot{
		SourcePath:   "/tmp/codex/session.jsonl",
		Messages:     []iod.SessionHistoryMessage{{Seq: 1, Role: "user", Kind: "message", Text: "still alive"}},
		IndexedCount: 1,
		Warmed:       true,
		Complete:     true,
	}
	packet, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, history)
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket() error = %v", err)
	}
	cleanup := startReplayHelper(t, manifest, helperReplayScript{SkipReplay: true, History: &packet})
	defer cleanup()
	launcher := processRuntimeLauncher{}
	runtime, err := launcher.attachIODManifest(context.Background(), runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex}, iodclient.DiscoveredManifest{Path: manifestPath, Manifest: manifest})
	if err != nil {
		t.Fatalf("attachIODManifest() error = %v", err)
	}
	if !runtime.attachedExistingIOD {
		t.Fatal("runtime.attachedExistingIOD = false, want true")
	}
	runtime.ReleaseAttachedHelperRollback()
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("Stat(%q) after attached rollback cleanup error = %v", manifestPath, err)
	}
	client, hello, err := launcher.attachHelper(context.Background(), manifest)
	if err != nil {
		t.Fatalf("attachHelper() after rollback error = %v", err)
	}
	defer client.Close()
	if hello.GenerationID != generationID {
		t.Fatalf("hello generation = %q, want %q", hello.GenerationID, generationID)
	}
	req, err := iod.NewSessionHistoryRequestPacket(sessionID, generationID)
	if err != nil {
		t.Fatalf("NewSessionHistoryRequestPacket() error = %v", err)
	}
	response, err := client.SessionHistory(context.Background(), req)
	if err != nil {
		t.Fatalf("SessionHistory() after rollback error = %v", err)
	}
	if len(response.Messages) != 1 || response.Messages[0].Text != "still alive" {
		t.Fatalf("SessionHistory() = %+v, want live helper history after rollback", response)
	}
}

func TestAttachedExistingIODOwnerKillShutsDownAndCleansArtifacts(t *testing.T) {
	sessionID := mustSessionID(t, "s_attach_owner_kill")
	generationID := mustHelperGenerationID(t, "g_attach_owner_kill")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	_ = writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetWaitResult(process.ExitStatus{Code: 0}, nil)
	runtime := sessionRuntime{
		helper:              &runtimeIODHelper{handle: handle, runtimeDir: filepath.Dir(manifestPath)},
		attachedExistingIOD: true,
	}
	if err := runtime.Kill(context.Background()); err != nil {
		t.Fatalf("Kill(attached owner) error = %v", err)
	}
	if handle.InterruptCalls() == 0 {
		t.Fatal("Kill(attached owner) did not interrupt helper handle")
	}
	if err := runtime.CleanupHelperArtifacts(); err != nil {
		t.Fatalf("CleanupHelperArtifacts(attached owner) error = %v", err)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) after owner cleanup error = %v, want not exist", manifestPath, err)
	}
}

func TestPIDOnlyIODShutdownWaitsForProcessExit(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("sleep start error = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		default:
		}
	})
	sessionID := mustSessionID(t, "s_pid_shutdown")
	generationID := mustHelperGenerationID(t, "g_pid_shutdown")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifestWithPID(t, manifestPath, sessionID, generationID, cmd.Process.Pid, 1760000007)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{SkipReplay: true})
	defer cleanup()
	helper := &runtimeIODHelper{manifest: manifest, sessionID: sessionID, generationID: generationID, helperPID: cmd.Process.Pid}
	if err := helper.shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown(pid-only) error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(helperStopTimeout + time.Second):
		t.Fatal("pid-only helper process still running after shutdown")
	}
}

func TestPIDOnlyIODShutdownTerminatesDetachedProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $! > \"$1\"; wait", "sh", pidFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("detached shell start error = %v", err)
	}
	childPID := waitForAppPIDFile(t, pidFile)
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		default:
		}
	})
	sessionID := mustSessionID(t, "s_pid_group_shutdown")
	generationID := mustHelperGenerationID(t, "g_pid_group_shutdown")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifestWithPID(t, manifestPath, sessionID, generationID, cmd.Process.Pid, 1760000007)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{SkipReplay: true})
	defer cleanup()
	helper := &runtimeIODHelper{manifest: manifest, sessionID: sessionID, generationID: generationID, helperPID: cmd.Process.Pid}
	if err := helper.shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown(pid-only process group) error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(helperStopTimeout + time.Second):
		t.Fatal("pid-only helper process group leader still running after shutdown")
	}
	deadline := time.Now().Add(3 * time.Second)
	for appProcessRunning(childPID) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if appProcessRunning(childPID) {
		t.Fatalf("pid-only helper process group child pid %d is still running after shutdown", childPID)
	}
}

func TestPIDOnlyIODShutdownSkipsUnverifiedProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("sleep start error = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		default:
		}
	})
	sessionID := mustSessionID(t, "s_pid_skip")
	generationID := mustHelperGenerationID(t, "g_pid_skip")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifestWithPID(t, manifestPath, sessionID, generationID, cmd.Process.Pid, 1760000007)
	helper := &runtimeIODHelper{manifest: manifest, sessionID: sessionID, generationID: generationID, helperPID: cmd.Process.Pid}
	if err := helper.shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown(unverified pid-only) error = %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("unverified pid-only shutdown exited process unexpectedly: %v", err)
	default:
	}
}

func waitForAppPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if parseErr != nil {
				t.Fatalf("parse pid file %q: %v", path, parseErr)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid file %q was not written: %v", path, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func appProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return false
	}
	return fields[2] != "Z"
}

func TestPIDOnlyIODShutdownRejectsHelloProofMismatch(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("sleep start error = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		default:
		}
	})
	sessionID := mustSessionID(t, "s_pid_mismatch")
	generationID := mustHelperGenerationID(t, "g_pid_mismatch")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifestWithPID(t, manifestPath, sessionID, generationID, cmd.Process.Pid, 1760000007)
	forged := manifest
	forged.HelperPID = os.Getpid()
	cleanup := startReplayHelper(t, forged, helperReplayScript{SkipReplay: true})
	defer cleanup()
	helper := &runtimeIODHelper{manifest: manifest, sessionID: sessionID, generationID: generationID, helperPID: cmd.Process.Pid}
	if err := helper.shutdown(context.Background()); err == nil {
		t.Fatal("shutdown(proof mismatch) error = nil, want proof mismatch")
	}
	select {
	case err := <-done:
		t.Fatalf("proof mismatch shutdown exited process unexpectedly: %v", err)
	default:
	}
}

func TestRuntimeIODHelperFromAttachmentPreservesMetadata(t *testing.T) {
	sessionID := mustSessionID(t, "s_attach_metadata")
	generationID := mustHelperGenerationID(t, "g_attach_metadata")
	proof, err := iod.NewHelloProof(123, nil, "/tmp/metadata.wal", "/tmp/metadata.sock", 1760000006)
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	hello, err := iod.NewHelloPacket(sessionID, generationID, 1, proof)
	if err != nil {
		t.Fatalf("NewHelloPacket() error = %v", err)
	}
	hello.IODBuildDate = "2026-05-10"
	hello.IODGitSHA = "abc123"
	helper := runtimeIODHelperFromAttachment(attachedHelper{
		Binding: helperGenerationBinding{
			SessionID:    sessionID,
			GenerationID: generationID,
		},
		ManifestPath: "/tmp/metadata/generation-manifest.json",
		Hello:        hello,
	}, nil)
	summary := helper.iodSummary()
	if summary == nil || summary.BuildDate != "2026-05-10" || summary.GitSHA != "abc123" || summary.StartTS != 1760000006 {
		t.Fatalf("iodSummary() = %+v, want restored hello metadata", summary)
	}
}

func TestSessionHistoryRejectsHelloProofMismatch(t *testing.T) {
	sessionID := mustSessionID(t, "s_history_proof")
	generationID := mustHelperGenerationID(t, "g_history_proof")
	root := filepath.Join("/tmp", fmt.Sprintf("ariod-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestPath := iodclient.GenerationManifestPath(root, sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	packet, err := iod.NewSessionHistoryResponsePacket(sessionID, generationID, iod.SessionHistorySnapshot{
		SourcePath:   "/tmp/codex/session.jsonl",
		Messages:     []iod.SessionHistoryMessage{{Seq: 1, Role: "assistant", Kind: "message", Text: "forged"}},
		IndexedCount: 1,
		Warmed:       true,
		Complete:     true,
	})
	if err != nil {
		t.Fatalf("NewSessionHistoryResponsePacket() error = %v", err)
	}
	forged := manifest
	forged.HelperPID = manifest.HelperPID + 1
	cleanup := startReplayHelper(t, forged, helperReplayScript{SkipReplay: true, History: &packet})
	defer cleanup()
	helper := &runtimeIODHelper{
		manifest:     manifest,
		sessionID:    sessionID,
		generationID: generationID,
	}
	if _, err := helper.sessionHistory(context.Background()); err == nil {
		t.Fatal("sessionHistory() error = nil, want hello proof mismatch")
	}
}

func TestHelperReplayAfterOffset(t *testing.T) {
	sessionID := mustSessionID(t, "s_replay_after_offset")
	generationID := mustHelperGenerationID(t, "g_replay_after_offset")
	binding := helperGenerationBinding{SessionID: sessionID, GenerationID: generationID, LastReplayOffset: 7}

	empty := sessionRecord{transcript: message.NewTranscript()}
	if got := helperReplayAfterOffset(empty, binding); got != 0 {
		t.Fatalf("helperReplayAfterOffset(empty transcript) = %d, want 0", got)
	}

	tailOnly := sessionRecord{transcript: message.NewTranscript()}
	var err error
	tailOnly.identity, err = session.NewDetachedIdentity(sessionID.String(), "codex")
	if err != nil {
		t.Fatalf("NewDetachedIdentity() error = %v", err)
	}
	tailOnly.state, err = session.NewState(tailOnly.identity, false, session.EmptyQueueSnapshot(), message.NewCommittedTail(8))
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}
	if got := helperReplayAfterOffset(tailOnly, binding); got != 0 {
		t.Fatalf("helperReplayAfterOffset(tail-only transcript) = %d, want 0", got)
	}

	withMessage := sessionRecord{transcript: message.NewTranscript()}
	if _, err := withMessage.transcript.AppendMessage(message.RoleUser.String(), message.KindMessage.String(), "hello", time.Unix(1760000000, 0).UTC()); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	if got := helperReplayAfterOffset(withMessage, binding); got != 7 {
		t.Fatalf("helperReplayAfterOffset(committed transcript) = %d, want 7", got)
	}

	withPartial := sessionRecord{transcript: message.NewTranscript()}
	if _, err := withPartial.transcript.AppendAssistantDelta("turn_replay_after_offset", "partial"); err != nil {
		t.Fatalf("AppendAssistantDelta() error = %v", err)
	}
	if got := helperReplayAfterOffset(withPartial, binding); got != 7 {
		t.Fatalf("helperReplayAfterOffset(partial transcript) = %d, want 7", got)
	}
}

func TestHelperReplayStateAdvancesOffsetWhenProjectionFails(t *testing.T) {
	sessionID := mustSessionID(t, "s_1")
	generationID := mustHelperGenerationID(t, "g_projection_failure")
	state := newHelperReplayState(5, func(packet iod.ReplayItemPacket) error {
		return fmt.Errorf("reject wal offset %d", packet.Item.WALOffset)
	})
	packet := mustReplayItemPacket(t, sessionID, generationID, 6, 9)
	if err := state.accept(packet); err != nil {
		t.Fatalf("accept() error = %v, want projection failure ignored", err)
	}
	if state.lastOffset != 6 {
		t.Fatalf("last replay offset = %d, want 6 after projector failure", state.lastOffset)
	}
}

func TestCodexReattachIgnoresProjectionError(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_codex_projection_error")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID, LastReplayOffset: 5}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-reattach"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000007)
	livePartial := mustStateOutputPacket(t, sessionID, generationID, 1,
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-projection-error\"}}}\n"+
			"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turn\":{\"id\":\"turn-codex-projection-error\",\"status\":\"inProgress\",\"error\":null}}}\n"+
			"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turnId\":\"turn-codex-projection-error\",\"itemId\":\"item-codex-projection-error\",\"delta\":\"Recovered \"}}\n")
	liveToolDuringPartial := mustStateOutputPacket(t, sessionID, generationID, 2,
		"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turnId\":\"turn-codex-projection-error\",\"item\":{\"type\":\"commandExecution\",\"id\":\"tool-codex-projection-error\",\"command\":\"echo stale\",\"aggregatedOutput\":\"stale tool result\",\"status\":\"completed\"}}}\n")
	liveFinal := mustStateOutputPacket(t, sessionID, generationID, 3,
		"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turnId\":\"turn-codex-projection-error\",\"itemId\":\"item-codex-projection-error\",\"delta\":\"after projection error.\"}}\n"+
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turnId\":\"turn-codex-projection-error\",\"item\":{\"type\":\"agentMessage\",\"id\":\"item-codex-projection-error\",\"text\":\"Recovered after projection error.\"}}}\n"+
			"{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turn\":{\"id\":\"turn-codex-projection-error\",\"status\":\"completed\",\"error\":null}}}\n")
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		SkipReplay:  true,
		LivePackets: []any{livePartial, liveToolDuringPartial, liveFinal},
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	attachment, ok := rehydrated.helpers.Attachment(sessionID)
	if !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if attachment.Binding.LastReplayOffset != 5 {
		t.Fatalf("attachment last replay offset = %d, want preserved cursor 5", attachment.Binding.LastReplayOffset)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached {
		t.Fatalf("SessionState().Transport = %+v, want attached after projection error", state.Transport)
	}
	var messages SessionMessagesResponse
	waitForTestCondition(t, func() bool {
		var err error
		messages, err = rehydrated.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		return err == nil && len(messages.Items) == 1
	})
	if len(messages.Items) != 1 || messages.Items[0].Text != "Recovered after projection error." {
		t.Fatalf("SessionMessages().Items = %#v, want live attach to continue through projection error", messages.Items)
	}
}

func TestCodexLiveHelperIgnoresProjectionError(t *testing.T) {
	cfg := persistentTestConfig(t)
	cfg.Storage.DataDir = filepath.Join("/tmp", fmt.Sprintf("arlh-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(cfg.Storage.DataDir) })
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_codex_live_err")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-live-helper"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000007)
	livePartial := mustStateOutputPacket(t, sessionID, generationID, 1,
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-live-projection-error\"}}}\n"+
			"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-live-projection-error\",\"turn\":{\"id\":\"turn-codex-live-projection-error\",\"status\":\"inProgress\",\"error\":null}}}\n"+
			"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-live-projection-error\",\"turnId\":\"turn-codex-live-projection-error\",\"itemId\":\"item-codex-live-projection-error\",\"delta\":\"Recovered \"}}\n")
	liveToolDuringPartial := mustStateOutputPacket(t, sessionID, generationID, 2,
		"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-live-projection-error\",\"turnId\":\"turn-codex-live-projection-error\",\"item\":{\"type\":\"commandExecution\",\"id\":\"tool-codex-live-projection-error\",\"command\":\"echo stale\",\"aggregatedOutput\":\"stale tool result\",\"status\":\"completed\"}}}\n")
	liveFinal := mustStateOutputPacket(t, sessionID, generationID, 3,
		"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-live-projection-error\",\"turnId\":\"turn-codex-live-projection-error\",\"itemId\":\"item-codex-live-projection-error\",\"delta\":\"after projection error.\"}}\n"+
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-live-projection-error\",\"turnId\":\"turn-codex-live-projection-error\",\"item\":{\"type\":\"agentMessage\",\"id\":\"item-codex-live-projection-error\",\"text\":\"Recovered after live projection error.\"}}}\n"+
			"{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-live-projection-error\",\"turn\":{\"id\":\"turn-codex-live-projection-error\",\"status\":\"completed\",\"error\":null}}}\n")
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		SkipReplay:  true,
		LivePackets: []any{livePartial, liveToolDuringPartial, liveFinal},
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	waitForTestCondition(t, func() bool {
		messages, err := rehydrated.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		if err != nil || len(messages.Items) != 1 || messages.Items[0].Text != "Recovered after live projection error." {
			return false
		}
		state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && !state.Busy && state.PartialAssistantTurn == nil
	})

	messages, err := rehydrated.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "Recovered after live projection error." {
		t.Fatalf("SessionMessages().Items = %#v, want live helper to continue through projection error", messages.Items)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy || state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState() = %+v, want idle without partial after live helper recovery", state)
	}
}

func TestCodexReplayFailedProjectionDoesNotStayWorking(t *testing.T) {
	identity, err := session.NewLiveIdentity("s_codex_replay_failed", "r_codex_replay_failed", "t_codex_replay_failed", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	runtimeState := newCodexRuntimeStateWithResumeThread(session.BackendCodex, "thread-replay-failed")
	runtimeState.setActiveTurnID("turn-replay-failed")
	record := sessionRecord{
		identity:  identity,
		runtime:   sessionRuntime{protocol: runtimeProtocolCodexRPC, codex: runtimeState},
		transport: SessionTransportSnapshot{State: SessionTransportStateAttached, Reason: "codex_replay_failed:replay_failed"},
	}
	visible := codexVisibleActivity(record)
	if visible.Phase != codexRuntimePhaseEnded || visible.Reason != "codex_replay_failed:replay_failed" || visible.Busy {
		t.Fatalf("codexVisibleActivity() = %+v, want terminal replay_failed projection", visible)
	}
	if codexRegistryBusy(record, visible) {
		t.Fatal("codexRegistryBusy() = true, want false for replay_failed projection")
	}
}

func TestCodexReplayFailedTransportStaysAttached(t *testing.T) {
	sessionID := mustSessionID(t, "s_codex_replay_failed_transport")
	generationID := mustHelperGenerationID(t, "g_codex_replay_failed_transport")
	attachment := attachedHelper{
		Binding: helperGenerationBinding{
			SessionID:    sessionID,
			GenerationID: generationID,
		},
		ReplayFailed: true,
		ReplayReason: helperFenceReplayFailed,
	}
	transport := startupTransportForSession(sessionID, nil, map[session.SessionID]attachedHelper{sessionID: attachment}, nil)
	if transport.State != SessionTransportStateAttached || transport.ResetRequired || transport.Reason != "codex_replay_failed:replay_failed" {
		t.Fatalf("startupTransportForSession() = %+v, want attached replay_failed marker", transport)
	}
}

func TestReattachRebuildsEmptyTranscript(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_rebuild_empty")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID, LastReplayOffset: 8}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-reattach"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000009)
	liveFinal := mustStateOutputPacket(t, sessionID, generationID, 1,
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-rebuild-empty\"}}}\n"+
			"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-rebuild-empty\",\"turn\":{\"id\":\"turn-rebuild-empty\",\"status\":\"inProgress\",\"error\":null}}}\n"+
			"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-rebuild-empty\",\"turnId\":\"turn-rebuild-empty\",\"itemId\":\"item-rebuild-empty\",\"delta\":\"Rebuilt from saved cursor.\"}}\n"+
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-rebuild-empty\",\"turnId\":\"turn-rebuild-empty\",\"item\":{\"type\":\"agentMessage\",\"id\":\"item-rebuild-empty\",\"text\":\"Rebuilt from saved cursor.\"}}}\n"+
			"{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-rebuild-empty\",\"turn\":{\"id\":\"turn-rebuild-empty\",\"status\":\"completed\",\"error\":null}}}\n")
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		SkipReplay:  true,
		LivePackets: []any{liveFinal},
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	attachment, ok := rehydrated.helpers.Attachment(sessionID)
	if !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if attachment.Binding.LastReplayOffset != 8 {
		t.Fatalf("attachment last replay offset = %d, want preserved cursor 8", attachment.Binding.LastReplayOffset)
	}
	var messages SessionMessagesResponse
	waitForTestCondition(t, func() bool {
		var err error
		messages, err = rehydrated.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
		return err == nil && len(messages.Items) == 1
	})
	if len(messages.Items) != 1 || messages.Items[0].Text != "Rebuilt from saved cursor." {
		t.Fatalf("SessionMessages().Items = %#v, want transcript rebuilt from live attach", messages.Items)
	}
}

func TestStaleHelperFence(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	currentGeneration := mustHelperGenerationID(t, "g_7")
	staleGeneration := mustHelperGenerationID(t, "g_6")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: currentGeneration}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/stale-fence"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)

	runtimeRoot := iodclient.RuntimeRoot(cfg.Storage.DataDir)
	staleManifestPath := iodclient.GenerationManifestPath(runtimeRoot, sessionID, staleGeneration)
	_ = writeHelperManifest(t, staleManifestPath, sessionID, staleGeneration, 1760000000)
	currentManifestPath := iodclient.GenerationManifestPath(runtimeRoot, sessionID, currentGeneration)
	currentManifest := writeHelperManifest(t, currentManifestPath, sessionID, currentGeneration, 1760000001)
	duplicateManifestPath := filepath.Join(runtimeRoot, "zz-duplicate", sessionID.String(), currentGeneration.String(), iodclient.ManifestFilename)
	_ = writeHelperManifest(t, duplicateManifestPath, sessionID, currentGeneration, 1760000002)
	cleanup := startReplayHelper(t, currentManifest, helperReplayScript{
		AfterOffset: 0,
		Done:        mustReplayDonePacket(t, sessionID, currentGeneration, 0, 0),
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	attachment, ok := rehydrated.helpers.Attachment(sessionID)
	if !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if attachment.Binding.GenerationID != currentGeneration {
		t.Fatalf("attachment generation id = %q, want %q", attachment.Binding.GenerationID, currentGeneration)
	}
	fenced := rehydrated.helpers.Fenced()
	if len(fenced) != 2 {
		t.Fatalf("len(fenced) = %d, want 2: %#v", len(fenced), fenced)
	}
	if !hasFenceReason(fenced, staleGeneration, helperFenceGenerationNotCurrent) {
		t.Fatalf("missing stale generation fence in %#v", fenced)
	}
	if !hasFenceReason(fenced, currentGeneration, helperFenceDuplicateHelper) {
		t.Fatalf("missing duplicate helper fence in %#v", fenced)
	}
}

func TestCorruptManifestSkipped(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_9")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/corrupt-manifest-skipped"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000003)
	corruptPath := filepath.Join(iodclient.RuntimeRoot(cfg.Storage.DataDir), "corrupt", iodclient.ManifestFilename)
	if err := os.MkdirAll(filepath.Dir(corruptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(corruptPath), err)
	}
	if err := os.WriteFile(corruptPath, []byte("not json\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", corruptPath, err)
	}
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		AfterOffset: 0,
		Done:        mustReplayDonePacket(t, sessionID, generationID, 0, 0),
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	if _, ok := rehydrated.helpers.Attachment(sessionID); !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if len(rehydrated.helpers.Fenced()) != 0 {
		t.Fatalf("fenced helpers = %#v, want none", rehydrated.helpers.Fenced())
	}
}

func TestReplayCursorNotAdvancedOnCorruptTail(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_13")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID, LastReplayOffset: 5}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/replay-corrupt-tail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000004)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		AfterOffset: 0,
		Items:       []iod.ReplayItemPacket{mustReplayItemPacket(t, sessionID, generationID, 1, 4)},
		Done:        mustReplayDonePacketWithCorruptTail(t, sessionID, generationID, 0, 1),
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	assertReplayCursorPreserved(t, rehydrated, sessionID, generationID, 5, helperFenceReplayCorruptTail)
}

func TestReplayCursorNotAdvancedOnReplayGap(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_15")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID, LastReplayOffset: 5}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/replay-gap"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000005)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		AfterOffset: 0,
		Items:       []iod.ReplayItemPacket{mustReplayItemPacket(t, sessionID, generationID, 7, 5)},
		Done:        mustReplayDonePacket(t, sessionID, generationID, 0, 7),
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	assertReplayCursorPreserved(t, rehydrated, sessionID, generationID, 5, helperFenceReplayGap)
}

func TestCodexReattachToleratesReplayGap(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_codex_replay_gap")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID, LastReplayOffset: 5}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-replay-gap"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		SkipReplay: true,
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	attachment, ok := rehydrated.helpers.Attachment(sessionID)
	if !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if attachment.ReplayFailed || attachment.ReplayReason != "" {
		t.Fatalf("attachment replay = failed:%v reason:%q, want codex replay skipped", attachment.ReplayFailed, attachment.ReplayReason)
	}
	if hasFenceReason(rehydrated.helpers.Fenced(), generationID, helperFenceReplayGap) {
		t.Fatalf("fenced helpers = %+v, want no codex replay gap fence", rehydrated.helpers.Fenced())
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.ResetRequired {
		t.Fatalf("SessionState().Transport = %+v, want attached without reset", state.Transport)
	}
	if state.Transport.Reason != "" {
		t.Fatalf("SessionState().Transport.Reason = %q, want clean attached transport", state.Transport.Reason)
	}
	bindings, err := rehydrated.helperBindings.Load()
	if err != nil {
		t.Fatalf("helperBindings.Load() error = %v", err)
	}
	if bindings[sessionID].LastReplayOffset != 5 {
		t.Fatalf("saved binding = %+v, want last replay offset preserved", bindings[sessionID])
	}
}

func TestCodexReattachUsesSavedReplayCursorWhenSourceBindingExists(t *testing.T) {
	cfg := persistentTestConfig(t)
	cfg.Storage.DataDir = filepath.Join("/tmp", fmt.Sprintf("arcursor-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(cfg.Storage.DataDir) })
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_codex_replay_cursor")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID, LastReplayOffset: 5}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-replay-cursor"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	if err := svc.bindCurrentGeneration(helperGenerationBinding{SessionID: sessionID, GenerationID: generationID, LastReplayOffset: 5}); err != nil {
		t.Fatalf("bindCurrentGeneration() error = %v", err)
	}
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	if _, _, err := svc.registry.SetSourceBinding(sessionID, "thread-codex-replay-cursor", manifestPath, sourceConfidenceExact); err != nil {
		t.Fatalf("SetSourceBinding() error = %v", err)
	}
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		SkipReplay: true,
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	attachment, ok := rehydrated.helpers.Attachment(sessionID)
	if !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if attachment.ReplayFailed || attachment.Binding.LastReplayOffset != 5 {
		t.Fatalf("attachment = %+v, want successful reattach at replay cursor 5", attachment)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.ResetRequired || state.Transport.Reason != "" {
		t.Fatalf("SessionState().Transport = %+v, want clean attached transport", state.Transport)
	}
}

func TestCodexReattachToleratesReplayFailure(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_codex_replay_failure")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID, LastReplayOffset: 5}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-replay-failure"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000006)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		SkipReplay: true,
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	attachment, ok := rehydrated.helpers.Attachment(sessionID)
	if !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if attachment.ReplayFailed || attachment.ReplayReason != "" {
		t.Fatalf("attachment replay = failed:%v reason:%q, want codex replay skipped", attachment.ReplayFailed, attachment.ReplayReason)
	}
	if hasFenceReason(rehydrated.helpers.Fenced(), generationID, helperFenceReplayCorruptTail) {
		t.Fatalf("fenced helpers = %+v, want no codex replay failure fence", rehydrated.helpers.Fenced())
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.ResetRequired {
		t.Fatalf("SessionState().Transport = %+v, want attached without reset", state.Transport)
	}
	if state.Transport.Reason != "" {
		t.Fatalf("SessionState().Transport.Reason = %q, want clean attached transport", state.Transport.Reason)
	}
	bindings, err := rehydrated.helperBindings.Load()
	if err != nil {
		t.Fatalf("helperBindings.Load() error = %v", err)
	}
	if bindings[sessionID].LastReplayOffset != 5 {
		t.Fatalf("saved binding = %+v, want last replay offset preserved", bindings[sessionID])
	}
}

func assertReplayCursorPreserved(t *testing.T, rehydrated *Stub, sessionID session.SessionID, generationID iod.GenerationID, offset iod.WALOffset, reason helperFenceReason) {
	t.Helper()
	if attachment, ok := rehydrated.helpers.Attachment(sessionID); ok {
		t.Fatalf("helper attachment = %+v, want fenced replay failure", attachment)
	}
	if !hasFenceReason(rehydrated.helpers.Fenced(), generationID, reason) {
		t.Fatalf("fenced helpers = %+v, want reason %q for generation %q", rehydrated.helpers.Fenced(), reason, generationID)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateBroken || state.Transport.GenerationID != generationID.String() {
		t.Fatalf("SessionState().Transport = %+v, want broken generation %q", state.Transport, generationID)
	}
	bindings, err := rehydrated.helperBindings.Load()
	if err != nil {
		t.Fatalf("helperBindings.Load() error = %v", err)
	}
	if bindings[sessionID].LastReplayOffset != offset {
		t.Fatalf("saved binding = %+v, want last replay offset %d", bindings[sessionID], offset)
	}
}

func hasFenceReason(fences []helperFence, generationID iod.GenerationID, reason helperFenceReason) bool {
	for _, fence := range fences {
		if fence.GenerationID == generationID && fence.Reason == reason {
			return true
		}
	}
	return false
}

func writeHelperManifest(t *testing.T, manifestPath string, sessionID session.SessionID, generationID iod.GenerationID, startUnix int64) iod.GenerationManifest {
	t.Helper()
	return writeHelperManifestWithPID(t, manifestPath, sessionID, generationID, os.Getpid(), startUnix)
}

func writeHelperManifestWithPID(t *testing.T, manifestPath string, sessionID session.SessionID, generationID iod.GenerationID, helperPID int, startUnix int64) iod.GenerationManifest {
	t.Helper()
	childPID := helperPID
	proof, err := iod.NewHelloProof(helperPID, &childPID, filepath.Join(filepath.Dir(manifestPath), "transport.wal"), filepath.Join(filepath.Dir(manifestPath), "io"), float64(startUnix))
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	if err := iodclient.WriteGenerationManifest(manifestPath, manifest); err != nil {
		t.Fatalf("WriteGenerationManifest() error = %v", err)
	}
	return manifest
}

func startReplayHelper(t *testing.T, manifest iod.GenerationManifest, script helperReplayScript) func() {
	t.Helper()
	if err := os.RemoveAll(manifest.ControlSocketPath); err != nil {
		t.Fatalf("RemoveAll(%q) error = %v", manifest.ControlSocketPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(manifest.ControlSocketPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(manifest.ControlSocketPath), err)
	}
	listener, err := net.Listen("unix", manifest.ControlSocketPath)
	if err != nil {
		t.Fatalf("Listen(unix, %q) error = %v", manifest.ControlSocketPath, err)
	}
	errCh := make(chan error, 8)
	doneCh := make(chan struct{})
	var connMu sync.Mutex
	conns := make([]net.Conn, 0, 2)
	go func() {
		defer close(doneCh)
		for {
			conn, err := listener.Accept()
			if err != nil {
				if !strings.Contains(err.Error(), "closed network connection") {
					errCh <- err
				}
				return
			}
			connMu.Lock()
			conns = append(conns, conn)
			connMu.Unlock()
			go func() {
				if err := serveReplayHelperConn(conn, manifest, script); err != nil {
					if helperConnClosed(err) {
						return
					}
					errCh <- err
				}
			}()
		}
	}()
	return func() {
		_ = listener.Close()
		connMu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		connMu.Unlock()
		<-doneCh
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("helper server error = %v", err)
			}
		default:
		}
		_ = os.Remove(manifest.ControlSocketPath)
	}
}

func startCommandHelper(t *testing.T, manifest iod.GenerationManifest, commands chan<- iod.CommandPacket) func() {
	t.Helper()
	if err := os.RemoveAll(manifest.ControlSocketPath); err != nil {
		t.Fatalf("RemoveAll(%q) error = %v", manifest.ControlSocketPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(manifest.ControlSocketPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(manifest.ControlSocketPath), err)
	}
	listener, err := net.Listen("unix", manifest.ControlSocketPath)
	if err != nil {
		t.Fatalf("Listen(unix, %q) error = %v", manifest.ControlSocketPath, err)
	}
	errCh := make(chan error, 8)
	doneCh := make(chan struct{})
	var connMu sync.Mutex
	conns := make([]net.Conn, 0, 2)
	go func() {
		defer close(doneCh)
		for {
			conn, err := listener.Accept()
			if err != nil {
				if !strings.Contains(err.Error(), "closed network connection") {
					errCh <- err
				}
				return
			}
			connMu.Lock()
			conns = append(conns, conn)
			connMu.Unlock()
			go func() {
				if err := serveCommandHelperConn(conn, manifest, commands); err != nil {
					if helperConnClosed(err) {
						return
					}
					errCh <- err
				}
			}()
		}
	}()
	return func() {
		_ = listener.Close()
		connMu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		connMu.Unlock()
		<-doneCh
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("helper server error = %v", err)
			}
		default:
		}
		_ = os.Remove(manifest.ControlSocketPath)
	}
}

func serveCommandHelperConn(conn net.Conn, manifest iod.GenerationManifest, commands chan<- iod.CommandPacket) error {
	defer conn.Close()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	hello, err := iod.NewHelloPacket(manifest.SessionID, manifest.GenerationID, 1, manifest.HelloProof)
	if err != nil {
		return err
	}
	if err := enc.Encode(hello); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "use of closed network connection") {
			return nil
		}
		return err
	}
	var peek struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return err
	}
	switch iod.PacketKind(peek.Kind) {
	case iod.PacketReplayRequest:
		var request iod.ReplayRequestPacket
		if err := json.Unmarshal(raw, &request); err != nil {
			return err
		}
		done, err := iod.NewReplayDonePacket(manifest.SessionID, manifest.GenerationID, request.AfterOffset, request.AfterOffset, false)
		if err != nil {
			return err
		}
		return enc.Encode(done)
	case iod.PacketSessionHistoryRequest:
		var request iod.SessionHistoryRequestPacket
		if err := json.Unmarshal(raw, &request); err != nil {
			return err
		}
		if request.SessionID != manifest.SessionID || request.GenerationID != manifest.GenerationID {
			return fmt.Errorf("session history request = %q/%q, want %q/%q", request.SessionID, request.GenerationID, manifest.SessionID, manifest.GenerationID)
		}
		response, err := iod.NewSessionHistoryResponsePacket(manifest.SessionID, manifest.GenerationID, iod.SessionHistorySnapshot{Warmed: true, Complete: true})
		if err != nil {
			return err
		}
		return enc.Encode(response)
	case iod.PacketCommandSend, iod.PacketCommandEnqueue, iod.PacketCommandInterrupt, iod.PacketCommandUIResponseSubmit:
		var command iod.CommandPacket
		if err := json.Unmarshal(raw, &command); err != nil {
			return err
		}
		select {
		case commands <- command:
		default:
		}
		outcome, err := iod.NewCommandOutcome(command.CommandID, 1, false, nil)
		if err != nil {
			return err
		}
		accepted, err := iod.NewCommandAcceptedPacket(manifest.SessionID, manifest.GenerationID, outcome)
		if err != nil {
			return err
		}
		return enc.Encode(accepted)
	default:
		return fmt.Errorf("unexpected helper packet kind %q", peek.Kind)
	}
}

func serveReplayHelperConn(conn net.Conn, manifest iod.GenerationManifest, script helperReplayScript) error {
	defer conn.Close()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	hello, err := iod.NewHelloPacket(manifest.SessionID, manifest.GenerationID, 1, manifest.HelloProof)
	if err != nil {
		return err
	}
	if err := enc.Encode(hello); err != nil {
		return err
	}
	if !script.SkipReplay {
		var replayReq iod.ReplayRequestPacket
		if err := dec.Decode(&replayReq); err != nil {
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "use of closed network connection") {
				return nil
			}
			return err
		}
		if replayReq.SessionID != manifest.SessionID || replayReq.GenerationID != manifest.GenerationID {
			return fmt.Errorf("replay request = %q/%q, want %q/%q", replayReq.SessionID, replayReq.GenerationID, manifest.SessionID, manifest.GenerationID)
		}
		if replayReq.AfterOffset != script.AfterOffset {
			return fmt.Errorf("replay after offset = %d, want %d", replayReq.AfterOffset, script.AfterOffset)
		}
		for _, packet := range script.Items {
			if err := enc.Encode(packet); err != nil {
				if helperConnClosed(err) {
					return nil
				}
				return err
			}
		}
		if err := enc.Encode(script.Done); err != nil {
			if helperConnClosed(err) {
				return nil
			}
			return err
		}
	}
	if script.SkipReplay && len(script.LivePackets) == 0 && script.History != nil {
		for {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "use of closed network connection") {
					return nil
				}
				return err
			}
			var peek struct {
				Kind iod.PacketKind `json:"kind"`
			}
			if err := json.Unmarshal(raw, &peek); err != nil {
				return err
			}
			if peek.Kind != iod.PacketSessionHistoryRequest {
				return fmt.Errorf("unexpected helper packet kind %q", peek.Kind)
			}
			if err := enc.Encode(*script.History); err != nil {
				if helperConnClosed(err) {
					return nil
				}
				return err
			}
		}
	}
	for _, packet := range script.LivePackets {
		if err := enc.Encode(packet); err != nil {
			if helperConnClosed(err) {
				return nil
			}
			return err
		}
	}
	return nil
}

func helperConnClosed(err error) bool {
	return errors.Is(err, io.EOF) || strings.Contains(err.Error(), "use of closed network connection")
}

func fakeRuntimeConfigWithHelperBinding(binding RuntimeHelperBinding) RuntimeConfig {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPID(321)
	return RuntimeConfig{
		Runner: &process.FakeRunner{NextHandle: handle},
		CurrentHelperBinding: func(session.SessionID) (*RuntimeHelperBinding, error) {
			resolved := binding
			return &resolved, nil
		},
	}
}

func mustHelperGenerationID(t *testing.T, raw string) iod.GenerationID {
	t.Helper()
	generationID, err := iod.NewGenerationID(raw)
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	return generationID
}

func mustReplayItemPacket(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, offset iod.WALOffset, seqValue uint64) iod.ReplayItemPacket {
	t.Helper()
	seq := iod.EventSeq(seqValue)
	fact, err := iod.NewHelperFact(iod.FactOutputDelta, &seq, json.RawMessage(`{"delta":"x"}`))
	if err != nil {
		t.Fatalf("NewHelperFact() error = %v", err)
	}
	item, err := iod.NewReplayItem(offset, fact)
	if err != nil {
		t.Fatalf("NewReplayItem() error = %v", err)
	}
	packet, err := iod.NewReplayItemPacket(sessionID, generationID, item)
	if err != nil {
		t.Fatalf("NewReplayItemPacket() error = %v", err)
	}
	return packet
}

func mustReplayOutputPacket(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, offset iod.WALOffset, seqValue uint64, data string) iod.ReplayItemPacket {
	t.Helper()
	seq := iod.EventSeq(seqValue)
	payload, err := json.Marshal(iodTerminalOutputPayload{Stream: "pty", Data: data})
	if err != nil {
		t.Fatalf("Marshal(helper output payload) error = %v", err)
	}
	fact, err := iod.NewHelperFact(iod.FactOutputDelta, &seq, payload)
	if err != nil {
		t.Fatalf("NewHelperFact() error = %v", err)
	}
	item, err := iod.NewReplayItem(offset, fact)
	if err != nil {
		t.Fatalf("NewReplayItem() error = %v", err)
	}
	packet, err := iod.NewReplayItemPacket(sessionID, generationID, item)
	if err != nil {
		t.Fatalf("NewReplayItemPacket() error = %v", err)
	}
	return packet
}

func mustStateOutputPacket(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, seqValue uint64, data string) iod.StatePacket {
	t.Helper()
	seq := iod.EventSeq(seqValue)
	payload, err := json.Marshal(iodTerminalOutputPayload{Stream: "pty", Data: data})
	if err != nil {
		t.Fatalf("Marshal(helper output payload) error = %v", err)
	}
	fact, err := iod.NewHelperFact(iod.FactOutputDelta, &seq, payload)
	if err != nil {
		t.Fatalf("NewHelperFact() error = %v", err)
	}
	packet, err := iod.NewStatePacket(sessionID, generationID, fact)
	if err != nil {
		t.Fatalf("NewStatePacket() error = %v", err)
	}
	return packet
}

func mustReplayDonePacket(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, afterOffset, lastOffset iod.WALOffset) iod.ReplayDonePacket {
	t.Helper()
	packet, err := iod.NewReplayDonePacket(sessionID, generationID, afterOffset, lastOffset, false)
	if err != nil {
		t.Fatalf("NewReplayDonePacket() error = %v", err)
	}
	return packet
}

func mustReplayDonePacketWithCorruptTail(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, afterOffset, lastOffset iod.WALOffset) iod.ReplayDonePacket {
	t.Helper()
	packet, err := iod.NewReplayDonePacket(sessionID, generationID, afterOffset, lastOffset, true)
	if err != nil {
		t.Fatalf("NewReplayDonePacket() error = %v", err)
	}
	return packet
}
