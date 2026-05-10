package iod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	snapshot, err := cache.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
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
	first, err := cache.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
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
	second, err := cache.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("second Snapshot() error = %v", err)
	}
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
