package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"actrail/internal/domain/workspace"
)

func TestFileVersionsReadsGitHistory(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	mustWriteFile(t, filepath.Join(repo, "notes.txt"), "v1\n")
	gitRun(t, repo, "add", "notes.txt")
	gitRun(t, repo, "commit", "-m", "first")
	mustWriteFile(t, filepath.Join(repo, "notes.txt"), "v2\n")
	gitRun(t, repo, "commit", "-am", "second")
	root, err := workspace.NewRoot(repo)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	path, err := workspace.ParseRelPath("notes.txt")
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}
	svc, err := New(Options{HistoryLimit: 5, Timeout: time.Second})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	versions, err := svc.FileVersions(context.Background(), root, path)
	if err != nil {
		t.Fatalf("file versions: %v", err)
	}
	if versions.FallbackReason != "" {
		t.Fatalf("expected git history, got fallback %q", versions.FallbackReason)
	}
	if len(versions.Items) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions.Items))
	}
	if !versions.Items[0].Current || versions.Items[0].Message != "second" {
		t.Fatalf("unexpected head version: %+v", versions.Items[0])
	}
	if versions.Items[1].Current || versions.Items[1].Message != "first" {
		t.Fatalf("unexpected prior version: %+v", versions.Items[1])
	}
	if versions.Items[0].Label == "" || len(versions.Items[0].Label) > 12 {
		t.Fatalf("unexpected label: %+v", versions.Items[0])
	}
}

func TestFileVersionsFallsBackWhenRepoUnavailable(t *testing.T) {
	rootDir := t.TempDir()
	mustWriteFile(t, filepath.Join(rootDir, "notes.txt"), "v1\n")
	root, err := workspace.NewRoot(rootDir)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	path, err := workspace.ParseRelPath("notes.txt")
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}
	svc, err := New(Options{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	versions, err := svc.FileVersions(context.Background(), root, path)
	if err != nil {
		t.Fatalf("file versions: %v", err)
	}
	if len(versions.Items) != 1 || versions.Items[0].VersionID != fallbackVersionID || !versions.Items[0].Current {
		t.Fatalf("unexpected fallback items: %+v", versions.Items)
	}
	if !strings.Contains(versions.FallbackReason, "git repository unavailable") {
		t.Fatalf("unexpected fallback reason: %q", versions.FallbackReason)
	}
}

func TestFileVersionsRespectsHistoryLimit(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	mustWriteFile(t, filepath.Join(repo, "notes.txt"), "v1\n")
	gitRun(t, repo, "add", "notes.txt")
	gitRun(t, repo, "commit", "-m", "first")
	mustWriteFile(t, filepath.Join(repo, "notes.txt"), "v2\n")
	gitRun(t, repo, "commit", "-am", "second")
	mustWriteFile(t, filepath.Join(repo, "notes.txt"), "v3\n")
	gitRun(t, repo, "commit", "-am", "third")
	root, err := workspace.NewRoot(repo)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	path, err := workspace.ParseRelPath("notes.txt")
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}
	svc, err := New(Options{HistoryLimit: 2})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	versions, err := svc.FileVersions(context.Background(), root, path)
	if err != nil {
		t.Fatalf("file versions: %v", err)
	}
	if len(versions.Items) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions.Items))
	}
	if versions.Items[0].Message != "third" || versions.Items[1].Message != "second" {
		t.Fatalf("unexpected limited versions: %+v", versions.Items)
	}
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	if _, err := New(Options{HistoryLimit: -1}); err == nil {
		t.Fatal("expected negative history limit to fail")
	}
	if _, err := New(Options{Timeout: -time.Second}); err == nil {
		t.Fatal("expected negative timeout to fail")
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init")
	gitRun(t, repo, "config", "user.email", "tests@example.com")
	gitRun(t, repo, "config", "user.name", "ActRail Tests")
	return repo
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %q: %v\n%s", args, dir, err, out)
	}
}

func mustWriteFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
