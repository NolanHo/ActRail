package app

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func TestStubGitFileVersionsReturnsRepositoryHistory(t *testing.T) {
	requireGitBinary(t)
	repo := initGitRepo(t)
	mustWriteWorkspaceFile(t, filepath.Join(repo, "notes.txt"), "v1\n")
	gitRun(t, repo, "add", "notes.txt")
	gitRun(t, repo, "commit", "-m", "first")
	mustWriteWorkspaceFile(t, filepath.Join(repo, "notes.txt"), "v2\n")
	gitRun(t, repo, "commit", "-am", "second")

	svc := newStub(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() })
	sessionID := createWorkspaceSession(t, svc, repo)

	versions, err := svc.GitFileVersions(context.Background(), GitFileVersionsRequest{SessionID: sessionID, Path: "./notes.txt"})
	if err != nil {
		t.Fatalf("GitFileVersions() error = %v", err)
	}
	if versions.Path != "notes.txt" || versions.FallbackReason != "" {
		t.Fatalf("GitFileVersions() unexpected envelope: %+v", versions)
	}
	if len(versions.Items) != 2 {
		t.Fatalf("len(GitFileVersions().Items) = %d, want 2", len(versions.Items))
	}
	if !versions.Items[0].Current || versions.Items[0].Message != "second" || versions.Items[0].CommitHash == "" || versions.Items[0].CommitTS == 0 {
		t.Fatalf("GitFileVersions().Items[0] = %+v", versions.Items[0])
	}
	if versions.Items[1].Current || versions.Items[1].Message != "first" {
		t.Fatalf("GitFileVersions().Items[1] = %+v", versions.Items[1])
	}
}

func TestStubGitFileVersionsFallsBackOutsideRepository(t *testing.T) {
	rootDir := t.TempDir()
	mustWriteWorkspaceFile(t, filepath.Join(rootDir, "notes.txt"), "v1\n")

	svc := newStub(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() })
	sessionID := createWorkspaceSession(t, svc, rootDir)

	versions, err := svc.GitFileVersions(context.Background(), GitFileVersionsRequest{SessionID: sessionID, Path: "notes.txt"})
	if err != nil {
		t.Fatalf("GitFileVersions() error = %v", err)
	}
	if versions.Path != "notes.txt" || len(versions.Items) != 1 {
		t.Fatalf("GitFileVersions() unexpected payload: %+v", versions)
	}
	if versions.Items[0].VersionID != "workspace" || versions.Items[0].Label != "Workspace" || !versions.Items[0].Current {
		t.Fatalf("GitFileVersions().Items[0] = %+v", versions.Items[0])
	}
	if !strings.Contains(versions.FallbackReason, "git repository unavailable") {
		t.Fatalf("GitFileVersions().FallbackReason = %q", versions.FallbackReason)
	}
}

func TestStubGitFileVersionsReturnsNotFoundAndRejectsEscapingPaths(t *testing.T) {
	svc := newStub(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() })
	unknown, err := session.ParseSessionID("s_404")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	_, err = svc.GitFileVersions(context.Background(), GitFileVersionsRequest{SessionID: unknown, Path: "notes.txt"})
	assertNotFound(t, err)

	repo := t.TempDir()
	mustWriteWorkspaceFile(t, filepath.Join(repo, "notes.txt"), "v1\n")
	sessionID := createWorkspaceSession(t, svc, repo)

	_, err = svc.GitFileVersions(context.Background(), GitFileVersionsRequest{SessionID: sessionID, Path: "../notes.txt"})
	assertInvalid(t, err, "path")
}

func requireGitBinary(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func initGitRepo(t *testing.T) string {
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
