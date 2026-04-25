package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func TestStubWorkspaceUsesSessionRootAndFilesystem(t *testing.T) {
	rootDir := t.TempDir()
	mustWriteWorkspaceFile(t, filepath.Join(rootDir, "alpha.txt"), "alpha")
	mustWriteWorkspaceFile(t, filepath.Join(rootDir, "nested", "beta.txt"), "beta")

	svc := newStub(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() })
	sessionID := createWorkspaceSession(t, svc, rootDir)

	snapshot, err := svc.SessionWorkspace(context.Background(), SessionWorkspaceRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionWorkspace() error = %v", err)
	}
	if snapshot.RootPath != rootDir {
		t.Fatalf("SessionWorkspace().RootPath = %q, want %q", snapshot.RootPath, rootDir)
	}
	if snapshot.SelectedPath != "" || len(snapshot.OpenPaths) != 0 || len(snapshot.HistoryItems) != 0 {
		t.Fatalf("SessionWorkspace() unexpected snapshot fields: %+v", snapshot)
	}

	list, err := svc.WorkspaceFileList(context.Background(), WorkspaceFileListRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("WorkspaceFileList() error = %v", err)
	}
	if list.RootPath != rootDir || list.Path != "" || list.Truncated {
		t.Fatalf("WorkspaceFileList() unexpected envelope: %+v", list)
	}
	if len(list.Items) != 2 {
		t.Fatalf("len(WorkspaceFileList().Items) = %d, want 2", len(list.Items))
	}
	if list.Items[0].Path != "alpha.txt" || list.Items[0].Kind != "file" || list.Items[0].SizeBytes != 5 {
		t.Fatalf("WorkspaceFileList().Items[0] = %+v", list.Items[0])
	}
	if list.Items[1].Path != "nested" || list.Items[1].Kind != "directory" {
		t.Fatalf("WorkspaceFileList().Items[1] = %+v", list.Items[1])
	}

	read, err := svc.WorkspaceFileRead(context.Background(), WorkspaceFileReadRequest{SessionID: sessionID, Path: "./nested/../alpha.txt"})
	if err != nil {
		t.Fatalf("WorkspaceFileRead() error = %v", err)
	}
	if read.Path != "alpha.txt" || read.Kind != "text" || read.Text != "alpha" {
		t.Fatalf("WorkspaceFileRead() unexpected payload: %+v", read)
	}
	if read.MIMEType != "text/plain; charset=utf-8" || read.Encoding != "utf-8" || read.SizeBytes != 5 {
		t.Fatalf("WorkspaceFileRead() unexpected metadata: %+v", read)
	}
}

func TestStubWorkspaceReturnsNotFoundAndRejectsEscapingPaths(t *testing.T) {
	rootDir := t.TempDir()
	mustWriteWorkspaceFile(t, filepath.Join(rootDir, "alpha.txt"), "alpha")

	svc := newStub(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() })
	sessionID := createWorkspaceSession(t, svc, rootDir)
	unknown, err := session.ParseSessionID("s_404")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	_, err = svc.SessionWorkspace(context.Background(), SessionWorkspaceRequest{SessionID: unknown})
	assertNotFound(t, err)

	_, err = svc.WorkspaceFileList(context.Background(), WorkspaceFileListRequest{SessionID: unknown})
	assertNotFound(t, err)

	_, err = svc.WorkspaceFileRead(context.Background(), WorkspaceFileReadRequest{SessionID: unknown, Path: "alpha.txt"})
	assertNotFound(t, err)

	_, err = svc.WorkspaceFileList(context.Background(), WorkspaceFileListRequest{SessionID: sessionID, Path: "../alpha.txt"})
	assertInvalid(t, err, "path")

	_, err = svc.WorkspaceFileRead(context.Background(), WorkspaceFileReadRequest{SessionID: sessionID, Path: "../alpha.txt"})
	assertInvalid(t, err, "path")
}

func createWorkspaceSession(t *testing.T, svc *Stub, cwd string) session.SessionID {
	t.Helper()
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: cwd})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if created.Session == nil {
		t.Fatal("CreateSession().Session = nil")
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	return sessionID
}

func assertInvalid(t *testing.T, err error, field string) {
	t.Helper()
	var appErr *Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if appErr.Code != "invalid_request" {
		t.Fatalf("error code = %q, want %q", appErr.Code, "invalid_request")
	}
	if appErr.Field != field {
		t.Fatalf("error field = %q, want %q", appErr.Field, field)
	}
}

func mustWriteWorkspaceFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
