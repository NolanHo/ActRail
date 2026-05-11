package iod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTailLinesReturnsNewestLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		if _, err := fmt.Fprintf(file, "{\"type\":\"message\",\"id\":\"m-%04d\"}\n", i); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	lines, _, _, err := tailLines(path, 3000)
	if err != nil {
		t.Fatalf("tailLines() error = %v", err)
	}
	if len(lines) != 3000 {
		t.Fatalf("len(tailLines()) = %d, want 3000", len(lines))
	}
	if lines[0] != `{"type":"message","id":"m-2000"}` {
		t.Fatalf("first tail line = %q, want m-2000", lines[0])
	}
	if lines[len(lines)-1] != `{"type":"message","id":"m-4999"}` {
		t.Fatalf("last tail line = %q, want m-4999", lines[len(lines)-1])
	}
}

func TestTailLinesHandlesFileWithoutTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("a\nb\nc"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, _, _, err := tailLines(path, 2)
	if err != nil {
		t.Fatalf("tailLines() error = %v", err)
	}
	if len(lines) != 2 || lines[0] != "b" || lines[1] != "c" {
		t.Fatalf("tailLines() = %#v, want b,c", lines)
	}
}

func TestCodexSessionPathFromOutput(t *testing.T) {
	output := `{"method":"turn/started","params":{"turn":{"id":"turn-1"}}}` + "\n" +
		`{"method":"thread/started","params":{"thread":{"id":"thread-1","path":"/tmp/codex/session.jsonl"}}}` + "\n"

	if got := codexSessionPathFromOutput(output); got != "/tmp/codex/session.jsonl" {
		t.Fatalf("codexSessionPathFromOutput() = %q, want session path", got)
	}
	threadID, path := codexSessionFromOutput(output)
	if threadID != "thread-1" || path != "/tmp/codex/session.jsonl" {
		t.Fatalf("codexSessionFromOutput() = (%q, %q), want thread/path", threadID, path)
	}
}

func TestCodexSessionFromOutputExtractsThreadIDWithoutPath(t *testing.T) {
	output := `{"method":"thread/started","params":{"thread":{"id":"thread-only"}}}` + "\n"
	threadID, path := codexSessionFromOutput(output)
	if threadID != "thread-only" || path != "" {
		t.Fatalf("codexSessionFromOutput() = (%q, %q), want thread id without path", threadID, path)
	}
}

func TestSessionHistoryCacheCodexDiscoversPathFromThreadID(t *testing.T) {
	codexHome := t.TempDir()
	root := filepath.Join(codexHome, "sessions")
	threadID := "019e084e-63e0-7320-9a4a-84f68f656827"
	path := filepath.Join(root, "2026", "05", "11", "rollout-2026-05-11T01-02-03-"+threadID+".jsonl")
	body := strings.Join([]string{
		`{"timestamp":"2026-05-11T01:02:03.000Z","type":"session_meta","payload":{"id":"` + threadID + `","cwd":"/tmp/codex-discovery"}}`,
		`{"timestamp":"2026-05-11T01:02:04.000Z","type":"event_msg","payload":{"type":"user_message","message":"found by IOD"}}`,
		`{"timestamp":"2026-05-11T01:02:05.000Z","type":"event_msg","payload":{"type":"agent_message","message":"history restored","phase":"final_answer"}}`,
	}, "\n") + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cache := newSessionHistoryCache("", true)
	cache.codexRoot = root
	cache.SetCodexThreadID(context.Background(), threadID)
	t.Cleanup(cache.Stop)
	snapshot := waitForHistorySnapshot(t, cache, func(snapshot SessionHistorySnapshot) bool {
		return snapshot.SourcePath == filepath.Clean(path) && len(snapshot.Messages) == 2
	})
	if snapshot.SourcePath != filepath.Clean(path) {
		t.Fatalf("Snapshot().SourcePath = %q, want %q", snapshot.SourcePath, filepath.Clean(path))
	}
	if len(snapshot.Messages) != 2 || snapshot.Messages[0].Text != "found by IOD" || snapshot.Messages[1].Text != "history restored" {
		t.Fatalf("Snapshot().Messages = %#v, want discovered Codex messages", snapshot.Messages)
	}
}

func TestSessionHistoryCacheSetPathWarmsTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	body := strings.Join([]string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"thread-1"}}`,
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
		`{"timestamp":"2026-05-08T15:58:04.000Z","type":"event_msg","payload":{"type":"agent_message","message":"world","phase":"final_answer"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cache := newSessionHistoryCache("", true)
	cache.SetPath(context.Background(), path)
	t.Cleanup(cache.Stop)

	snapshot := waitForHistorySnapshot(t, cache, func(snapshot SessionHistorySnapshot) bool {
		return snapshot.Complete && snapshot.IndexedCount == 2 && len(snapshot.Messages) == 2
	})
	if snapshot.SourcePath != path {
		t.Fatalf("Snapshot().SourcePath = %q, want %q", snapshot.SourcePath, path)
	}
	if len(snapshot.Lines) != 3 {
		t.Fatalf("Snapshot().Lines = %#v, want warmed session lines", snapshot.Lines)
	}
	if !snapshot.Complete {
		t.Fatal("Snapshot().Complete = false, want true after Codex full load")
	}
	if snapshot.IndexedCount != 2 {
		t.Fatalf("Snapshot().IndexedCount = %d, want 2", snapshot.IndexedCount)
	}
	if len(snapshot.Messages) != 2 || snapshot.Messages[0].Role != "user" || snapshot.Messages[0].Text != "hello" || snapshot.Messages[1].Role != "assistant" || snapshot.Messages[1].Text != "world" {
		t.Fatalf("Snapshot().Messages = %#v, want parsed user/assistant messages", snapshot.Messages)
	}
}

func TestSessionHistoryCacheCodexAppendUpdatesIndexAndTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	body := strings.Join([]string{
		`{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"thread-1"}}`,
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cache := newSessionHistoryCache(path, true)
	cache.Start(context.Background())
	t.Cleanup(cache.Stop)
	first := waitForHistorySnapshot(t, cache, func(snapshot SessionHistorySnapshot) bool {
		return snapshot.IndexedCount == 1 && len(snapshot.Messages) == 1
	})
	if first.IndexedCount != 1 || len(first.Messages) != 1 || first.Messages[0].Text != "hello" {
		t.Fatalf("first Snapshot() = %#v, want one indexed user message", first)
	}
	appendBody := strings.Join([]string{
		`{"timestamp":"2026-05-08T15:58:04.000Z","type":"event_msg","payload":{"type":"agent_message","message":"world","phase":"final_answer"}}`,
		`{"timestamp":"2026-05-08T15:58:05.000Z","type":"event_msg","payload":{"type":"task_complete"}}`,
	}, "\n") + "\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	if _, err := file.WriteString(appendBody); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	second := waitForHistorySnapshot(t, cache, func(snapshot SessionHistorySnapshot) bool {
		return snapshot.IndexedCount == 2 && snapshot.TaskComplete && len(snapshot.Messages) == 2
	})
	if second.IndexedCount != 2 {
		t.Fatalf("second Snapshot().IndexedCount = %d, want 2", second.IndexedCount)
	}
	if !second.TaskComplete {
		t.Fatal("second Snapshot().TaskComplete = false, want true after appended task_complete")
	}
	if len(second.Messages) != 2 || second.Messages[1].Role != "assistant" || second.Messages[1].Text != "world" {
		t.Fatalf("second Snapshot().Messages = %#v, want appended assistant message", second.Messages)
	}
	if got := second.Lines[len(second.Lines)-1]; !strings.Contains(got, `"task_complete"`) {
		t.Fatalf("last tail line = %q, want appended task_complete", got)
	}
}

func TestSessionHistoryCacheCodexIgnoresPartialTailUntilComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	body := strings.Join([]string{
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
		`{"timestamp":"2026-05-08T15:58:04.000Z","type":"event_msg","payload":{"type":"agent_message","message":"wor`,
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cache := newSessionHistoryCache(path, true)
	first := waitForHistorySnapshot(t, cache, func(snapshot SessionHistorySnapshot) bool {
		return snapshot.IndexedCount == 1 && len(snapshot.Messages) == 1
	})
	t.Cleanup(cache.Stop)
	if first.IndexedCount != 1 || len(first.Messages) != 1 || first.Messages[0].Text != "hello" {
		t.Fatalf("Snapshot() = %#v, want only complete first line", first)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	if _, err := file.WriteString(`ld","phase":"final_answer"}}` + "\n"); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	second := waitForHistorySnapshot(t, cache, func(snapshot SessionHistorySnapshot) bool {
		return snapshot.IndexedCount == 2 && len(snapshot.Messages) == 2
	})
	if second.IndexedCount != 2 || len(second.Messages) != 2 || second.Messages[1].Text != "world" {
		t.Fatalf("Snapshot() = %#v, want completed assistant line indexed once", second)
	}
}

func TestSessionHistoryCacheCodexIgnoresValidJSONTailUntilNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	body := strings.Join([]string{
		`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
		`{"timestamp":"2026-05-08T15:58:04.000Z","type":"event_msg","payload":{"type":"task_complete"}}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cache := newSessionHistoryCache(path, true)
	first := waitForHistorySnapshot(t, cache, func(snapshot SessionHistorySnapshot) bool {
		return snapshot.IndexedCount == 1 && len(snapshot.Messages) == 1
	})
	t.Cleanup(cache.Stop)
	if first.TaskComplete {
		t.Fatal("Snapshot().TaskComplete = true, want false before newline-committed task_complete")
	}
	if first.IndexedCount != 1 || len(first.Messages) != 1 || first.Messages[0].Text != "hello" {
		t.Fatalf("Snapshot() = %#v, want only newline-committed first message", first)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		t.Fatalf("WriteString(newline) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	second := waitForHistorySnapshot(t, cache, func(snapshot SessionHistorySnapshot) bool {
		return snapshot.TaskComplete
	})
	if !second.TaskComplete {
		t.Fatal("Snapshot().TaskComplete = false, want true after newline commit")
	}
}

func TestSessionHistoryCacheCodexDedupeKeepsRepeatedPrompts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	body := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"user_message","message":"continue"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"continue"}}`,
		`{"timestamp":"2026-05-08T15:58:04.000Z","type":"event_msg","payload":{"type":"agent_message","message":"done","phase":"final_answer"}}`,
		`{"timestamp":"2026-05-08T15:58:05.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}],"phase":"final_answer"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cache := newSessionHistoryCache(path, true)
	t.Cleanup(cache.Stop)
	snapshot := waitForHistorySnapshot(t, cache, func(snapshot SessionHistorySnapshot) bool {
		return snapshot.IndexedCount == 3 && len(snapshot.Messages) == 3
	})
	if snapshot.IndexedCount != 3 {
		t.Fatalf("Snapshot().IndexedCount = %d, want two repeated prompts plus one assistant", snapshot.IndexedCount)
	}
	if len(snapshot.Messages) != 3 || snapshot.Messages[0].Text != "continue" || snapshot.Messages[1].Text != "continue" || snapshot.Messages[2].Text != "done" {
		t.Fatalf("Snapshot().Messages = %#v, want repeated prompts preserved and event/response assistant collapsed", snapshot.Messages)
	}
}

func waitForHistorySnapshot(t *testing.T, cache *sessionHistoryCache, ready func(SessionHistorySnapshot) bool) SessionHistorySnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last SessionHistorySnapshot
	for {
		snapshot, err := cache.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		last = snapshot
		if ready(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("history snapshot not ready before deadline: %#v", last)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
