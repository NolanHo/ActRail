package iod

import (
	"fmt"
	"os"
	"path/filepath"
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
