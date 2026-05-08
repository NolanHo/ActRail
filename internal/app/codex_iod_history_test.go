package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/domain/session"
)

func TestSessionMessagesLoadsCodexHistoryAcrossIODGenerations(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_wal_history")
	firstGeneration := mustHelperGenerationID(t, "g_codex_history_1")
	secondGeneration := mustHelperGenerationID(t, "g_codex_history_2")

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewDetachedIdentity(sessionID.String(), session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewDetachedIdentity() error = %v", err)
	}
	if _, err := svc.registry.Create(sessionCreateSpec{
		Identity:  &identity,
		Backend:   session.BackendCodex,
		CWD:       "/tmp/codex-history",
		Transport: transportSnapshotAttached(secondGeneration),
	}); err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}

	writeCodexHistoryGeneration(t, cfg.Storage.IODRuntimeRoot(), sessionID, firstGeneration, 1760000001, []string{
		`{"method":"thread/started","params":{"thread":{"id":"thread-history"}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-history","turnId":"turn-1","item":{"type":"userMessage","id":"user-1","text":"first prompt"}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-history","turnId":"turn-1","item":{"type":"commandExecution","id":"tool-1","command":"pwd","status":"completed"}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-history","turnId":"turn-1","item":{"type":"agentMessage","id":"assistant-1","text":"first answer"}}}`,
	})
	writeCodexHistoryGeneration(t, cfg.Storage.IODRuntimeRoot(), sessionID, secondGeneration, 1760000002, []string{
		`{"method":"thread/started","params":{"thread":{"id":"thread-history"}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-history","turnId":"turn-2","item":{"type":"userMessage","id":"user-2","text":"second prompt"}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-history","turnId":"turn-2","item":{"type":"agentMessage","id":"assistant-2","text":"second answer"}}}`,
	})

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	got := messageRolesAndText(messages.Items)
	want := []string{"user:first prompt", "assistant:first answer", "user:second prompt", "assistant:second answer"}
	if len(got) != len(want) {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("messages[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if messages.TailSeq != 5 {
		t.Fatalf("TailSeq = %d, want raw tail 5", messages.TailSeq)
	}

	withTools, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 10, IncludeToolEvents: true})
	if err != nil {
		t.Fatalf("SessionMessages(include tools) error = %v", err)
	}
	if len(withTools.Items) != 5 || (withTools.Items[1].Kind != "tool" && withTools.Items[1].Kind != "tool_result") {
		t.Fatalf("SessionMessages(include tools) = %+v, want retained tool event", withTools.Items)
	}
}

func TestCodexThreadIDForRuntimeRestartFallsBackToLatestIODHistory(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sessionID := mustSessionID(t, "s_codex_wal_resume")
	firstGeneration := mustHelperGenerationID(t, "g_codex_resume_1")
	secondGeneration := mustHelperGenerationID(t, "g_codex_resume_2")

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	identity, err := session.NewLiveIdentity(sessionID.String(), "r_1", "t_1", session.BackendCodex.String())
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	record, err := svc.registry.Create(sessionCreateSpec{
		Identity: &identity,
		Backend:  session.BackendCodex,
		CWD:      "/tmp/codex-resume",
	})
	if err != nil {
		t.Fatalf("registry.Create() error = %v", err)
	}

	writeCodexHistoryGeneration(t, cfg.Storage.IODRuntimeRoot(), sessionID, firstGeneration, 1760000001, []string{
		`{"method":"thread/started","params":{"thread":{"id":"thread-old"}}}`,
	})
	writeCodexHistoryGeneration(t, cfg.Storage.IODRuntimeRoot(), sessionID, secondGeneration, 1760000002, []string{
		`{"method":"thread/started","params":{"thread":{"id":"thread-latest"}}}`,
	})

	threadID, err := svc.codexThreadIDForRuntimeRestart(context.Background(), record)
	if err != nil {
		t.Fatalf("codexThreadIDForRuntimeRestart() error = %v", err)
	}
	if threadID != "thread-latest" {
		t.Fatalf("codexThreadIDForRuntimeRestart() = %q, want latest thread", threadID)
	}
}

func writeCodexHistoryGeneration(t *testing.T, root string, sessionID session.SessionID, generationID iod.GenerationID, startTS float64, lines []string) {
	t.Helper()
	paths, err := iod.NewGenerationPaths(root, sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	if err := os.MkdirAll(paths.RuntimeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	proof, err := iod.NewHelloProof(os.Getpid(), nil, paths.WALPath, paths.ControlSocketPath, startTS)
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	if err := iodclient.WriteGenerationManifest(paths.ManifestPath, manifest); err != nil {
		t.Fatalf("WriteGenerationManifest() error = %v", err)
	}
	wal, err := iod.OpenWAL(paths.WALPath, sessionID, generationID)
	if err != nil {
		t.Fatalf("OpenWAL() error = %v", err)
	}
	defer wal.Close()
	for _, line := range lines {
		payload, err := json.Marshal(iodTerminalOutputPayload{Stream: "unix", Data: line + "\n"})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		if _, err := wal.AppendRaw(iod.WALRecordOutputDelta, payload); err != nil {
			t.Fatalf("AppendRaw() error = %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(paths.RuntimeDir, "transport.wal")); err != nil {
		t.Fatalf("expected wal file: %v", err)
	}
}

func messageRolesAndText(items []SessionMessage) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Role+":"+item.Text)
	}
	return out
}
