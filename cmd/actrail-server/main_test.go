package main

import (
	"os"
	"path/filepath"
	"testing"

	"actrail/internal/config"
)

func TestEnsureDataDirCreatesConfiguredDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "runtime", "data")
	cfg := config.Load()
	cfg.Storage.DataDir = dir

	if err := ensureDataDir(cfg); err != nil {
		t.Fatalf("ensureDataDir() error = %v", err)
	}
	if info, err := os.Stat(dir); err != nil {
		t.Fatalf("Stat(%q) error = %v", dir, err)
	} else if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", dir)
	}
}
