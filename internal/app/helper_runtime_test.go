package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

type helperReplayScript struct {
	AfterOffset iod.WALOffset
	Items       []iod.ReplayItemPacket
	Done        iod.ReplayDonePacket
	LivePackets []any
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
	replayItem := mustReplayOutputPacket(t, sessionID, generationID, 6, 3,
		"{\"type\":\"extension_ui_request\",\"id\":\"ui-reattach\",\"method\":\"select\",\"question\":\"Where should this go?\",\"options\":[\"Details\",\"Sidebar\"]}\n"+
			"{\"type\":\"message.delta\",\"turn_id\":\"turn-reattach\",\"role\":\"assistant\",\"delta\":\"Replay and live projection works.\"}\n"+
			"{\"type\":\"message_end\",\"message\":{\"role\":\"toolResult\",\"toolCallId\":\"ui-reattach\",\"toolName\":\"ask_user\",\"details\":{\"answer\":\"Sidebar\",\"cancelled\":false}}}\n"+
			"{\"type\":\"turn.completed\",\"turn_id\":\"turn-reattach\",\"role\":\"assistant\",\"text\":\"Replay and live projection works.\"}\n")
	livePacket := mustStateOutputPacket(t, sessionID, generationID, 4,
		"{\"type\":\"turn_end\"}\n")
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		AfterOffset: 5,
		Items:       []iod.ReplayItemPacket{replayItem},
		Done:        mustReplayDonePacket(t, sessionID, generationID, 5, 6),
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
	if attachment.Binding.LastReplayOffset != 6 {
		t.Fatalf("attachment last replay offset = %d, want 6", attachment.Binding.LastReplayOffset)
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
	if bindings[sessionID].GenerationID != generationID || bindings[sessionID].LastReplayOffset != 6 {
		t.Fatalf("saved binding = %+v, want generation %q offset 6", bindings[sessionID], generationID)
	}
}

func TestServerReattachProjectsCodexReplayAndLiveOutput(t *testing.T) {
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
	replayItem := mustReplayOutputPacket(t, sessionID, generationID, 6, 3,
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-reattach-1\"}}}\n"+
			"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-reattach-1\",\"turn\":{\"id\":\"turn-codex-reattach-1\",\"status\":\"inProgress\",\"error\":null}}}\n"+
			"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-reattach-1\",\"turnId\":\"turn-codex-reattach-1\",\"itemId\":\"item-codex-reattach-1\",\"delta\":\"Replay and live Codex \"}}\n")
	livePacket := mustStateOutputPacket(t, sessionID, generationID, 4,
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-reattach-1\"}}}\n"+
			"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-reattach-1\",\"turn\":{\"id\":\"turn-codex-reattach-1\",\"status\":\"inProgress\",\"error\":null}}}\n"+
			"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-reattach-1\",\"turnId\":\"turn-codex-reattach-1\",\"itemId\":\"item-codex-reattach-1\",\"delta\":\"projection works.\"}}\n"+
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-reattach-1\",\"turnId\":\"turn-codex-reattach-1\",\"item\":{\"type\":\"agentMessage\",\"id\":\"item-codex-reattach-1\",\"text\":\"Replay and live Codex projection works.\"}}}\n"+
			"{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-reattach-1\",\"turn\":{\"id\":\"turn-codex-reattach-1\",\"status\":\"completed\",\"error\":null}}}\n")
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		AfterOffset: 5,
		Items:       []iod.ReplayItemPacket{replayItem},
		Done:        mustReplayDonePacket(t, sessionID, generationID, 5, 6),
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
	if attachment.Binding.LastReplayOffset != 6 {
		t.Fatalf("attachment last replay offset = %d, want 6", attachment.Binding.LastReplayOffset)
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
	if bindings[sessionID].GenerationID != generationID || bindings[sessionID].LastReplayOffset != 6 {
		t.Fatalf("saved binding = %+v, want generation %q offset 6", bindings[sessionID], generationID)
	}
}

func TestHelperReplayStateDoesNotAdvanceOffsetOnProjectionFailure(t *testing.T) {
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
	replayPartial := mustReplayOutputPacket(t, sessionID, generationID, 6, 3,
		"{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread-codex-projection-error\"}}}\n"+
			"{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turn\":{\"id\":\"turn-codex-projection-error\",\"status\":\"inProgress\",\"error\":null}}}\n"+
			"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turnId\":\"turn-codex-projection-error\",\"itemId\":\"item-codex-projection-error\",\"delta\":\"Recovered \"}}\n")
	replayToolDuringPartial := mustReplayOutputPacket(t, sessionID, generationID, 7, 4,
		"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turnId\":\"turn-codex-projection-error\",\"item\":{\"type\":\"commandExecution\",\"id\":\"tool-codex-projection-error\",\"command\":\"echo stale\",\"aggregatedOutput\":\"stale tool result\",\"status\":\"completed\"}}}\n")
	replayFinal := mustReplayOutputPacket(t, sessionID, generationID, 8, 5,
		"{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turnId\":\"turn-codex-projection-error\",\"itemId\":\"item-codex-projection-error\",\"delta\":\"after projection error.\"}}\n"+
			"{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turnId\":\"turn-codex-projection-error\",\"item\":{\"type\":\"agentMessage\",\"id\":\"item-codex-projection-error\",\"text\":\"Recovered after projection error.\"}}}\n"+
			"{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-codex-projection-error\",\"turn\":{\"id\":\"turn-codex-projection-error\",\"status\":\"completed\",\"error\":null}}}\n")
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		AfterOffset: 5,
		Items:       []iod.ReplayItemPacket{replayPartial, replayToolDuringPartial, replayFinal},
		Done:        mustReplayDonePacket(t, sessionID, generationID, 5, 8),
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
		t.Fatalf("attachment last replay offset = %d, want 8", attachment.Binding.LastReplayOffset)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached {
		t.Fatalf("SessionState().Transport = %+v, want attached after projection error", state.Transport)
	}
	messages, err := rehydrated.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Text != "Recovered after projection error." {
		t.Fatalf("SessionMessages().Items = %#v, want replay to continue through projection error", messages.Items)
	}
}

func TestReattachClearsReplayFailure(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	generationID := mustHelperGenerationID(t, "g_clear_fail")
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfigWithHelperBinding(RuntimeHelperBinding{GenerationID: generationID, LastReplayOffset: 5}))
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/codex-reattach"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	if _, err := svc.setSessionTransport(sessionID, transportSnapshotBroken(generationID, "replay_failed", true)); err != nil {
		t.Fatalf("setSessionTransport() error = %v", err)
	}
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, generationID)
	manifest := writeHelperManifest(t, manifestPath, sessionID, generationID, 1760000008)
	cleanup := startReplayHelper(t, manifest, helperReplayScript{
		AfterOffset: 5,
		Done:        mustReplayDonePacket(t, sessionID, generationID, 5, 5),
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.ResetRequired || state.Transport.Reason != "" {
		t.Fatalf("SessionState().Transport = %+v, want attached without stale replay_failed", state.Transport)
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
		AfterOffset: 5,
		Items:       []iod.ReplayItemPacket{mustReplayItemPacket(t, sessionID, generationID, 6, 4)},
		Done:        mustReplayDonePacketWithCorruptTail(t, sessionID, generationID, 5, 6),
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
		AfterOffset: 5,
		Items:       []iod.ReplayItemPacket{mustReplayItemPacket(t, sessionID, generationID, 7, 5)},
		Done:        mustReplayDonePacket(t, sessionID, generationID, 5, 7),
	})
	defer cleanup()

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	assertReplayCursorPreserved(t, rehydrated, sessionID, generationID, 5, helperFenceReplayGap)
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
	childPID := os.Getpid()
	proof, err := iod.NewHelloProof(os.Getpid(), &childPID, filepath.Join(filepath.Dir(manifestPath), "transport.wal"), filepath.Join(filepath.Dir(manifestPath), "io"), float64(startUnix))
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
	errCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			if !strings.Contains(err.Error(), "closed network connection") {
				errCh <- err
			}
			close(errCh)
			return
		}
		defer close(errCh)
		defer conn.Close()
		enc := json.NewEncoder(conn)
		dec := json.NewDecoder(conn)
		hello, err := iod.NewHelloPacket(manifest.SessionID, manifest.GenerationID, 1, manifest.HelloProof)
		if err != nil {
			errCh <- err
			return
		}
		if err := enc.Encode(hello); err != nil {
			errCh <- err
			return
		}
		var replayReq iod.ReplayRequestPacket
		if err := dec.Decode(&replayReq); err != nil {
			errCh <- err
			return
		}
		if replayReq.SessionID != manifest.SessionID || replayReq.GenerationID != manifest.GenerationID {
			errCh <- fmt.Errorf("replay request = %q/%q, want %q/%q", replayReq.SessionID, replayReq.GenerationID, manifest.SessionID, manifest.GenerationID)
			return
		}
		if replayReq.AfterOffset != script.AfterOffset {
			errCh <- fmt.Errorf("replay after offset = %d, want %d", replayReq.AfterOffset, script.AfterOffset)
			return
		}
		for _, packet := range script.Items {
			if err := enc.Encode(packet); err != nil {
				errCh <- err
				return
			}
		}
		if err := enc.Encode(script.Done); err != nil {
			errCh <- err
			return
		}
		for _, packet := range script.LivePackets {
			if err := enc.Encode(packet); err != nil {
				errCh <- err
				return
			}
		}
	}()
	return func() {
		_ = listener.Close()
		if err := <-errCh; err != nil {
			t.Fatalf("helper server error = %v", err)
		}
		_ = os.Remove(manifest.ControlSocketPath)
	}
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
