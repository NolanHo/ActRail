package workspace

import (
	"path/filepath"
	"testing"
)

func TestNewRootRejectsRelativePath(t *testing.T) {
	if _, err := NewRoot("relative/path"); err == nil {
		t.Fatal("expected relative root to fail")
	}
}

func TestParseRelPathNormalizesAndRejectsEscapes(t *testing.T) {
	rel, err := ParseRelPath("./internal/../go.mod")
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}
	if rel.String() != "go.mod" {
		t.Fatalf("expected normalized path, got %q", rel.String())
	}
	if _, err := ParseRelPath("../go.mod"); err == nil {
		t.Fatal("expected parent escape to fail")
	}
	if _, err := ParseRelPath("dir\\file.txt"); err == nil {
		t.Fatal("expected backslash path to fail")
	}
}

func TestRootResolveAndRelativeStayInsideRoot(t *testing.T) {
	rootDir := t.TempDir()
	root, err := NewRoot(rootDir)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	rel, err := ParseRelPath("nested/file.txt")
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}
	abs, err := root.Resolve(rel)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := filepath.Join(rootDir, "nested", "file.txt")
	if abs != want {
		t.Fatalf("expected %q, got %q", want, abs)
	}
	back, err := root.Relative(abs)
	if err != nil {
		t.Fatalf("relative: %v", err)
	}
	if back.String() != rel.String() {
		t.Fatalf("expected %q, got %q", rel.String(), back.String())
	}
	if _, err := root.Relative(filepath.Join(filepath.Dir(rootDir), "outside.txt")); err == nil {
		t.Fatal("expected outside path to fail")
	}
}

func TestParseRelPathAllowRoot(t *testing.T) {
	rel, err := ParseRelPathAllowRoot(".")
	if err != nil {
		t.Fatalf("parse root path: %v", err)
	}
	if !rel.IsRoot() {
		t.Fatal("expected root path")
	}
}
