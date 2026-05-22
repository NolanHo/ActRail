package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"actrail/internal/domain/workspace"
)

func TestListReturnsRelativePathsAndMetadata(t *testing.T) {
	rootDir := t.TempDir()
	mustWriteFile(t, filepath.Join(rootDir, "alpha.txt"), "alpha")
	mustWriteFile(t, filepath.Join(rootDir, "nested", "beta.txt"), "beta")
	root, err := workspace.NewRoot(rootDir)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	svc, err := New(Options{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	list, err := svc.List(context.Background(), root, workspace.FileListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.RootPath != rootDir {
		t.Fatalf("expected root path %q, got %q", rootDir, list.RootPath)
	}
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(list.Items))
	}
	if list.Items[0].Path.String() != "alpha.txt" || list.Items[0].Kind != workspace.EntryKindFile || list.Items[0].SizeBytes != 5 {
		t.Fatalf("unexpected file entry: %+v", list.Items[0])
	}
	if list.Items[1].Path.String() != "nested" || list.Items[1].Kind != workspace.EntryKindDirectory {
		t.Fatalf("unexpected dir entry: %+v", list.Items[1])
	}
	if list.Items[1].Modified.IsZero() {
		t.Fatal("expected modified timestamp")
	}
}

func TestListSearchTraversesAndTruncates(t *testing.T) {
	rootDir := t.TempDir()
	mustWriteFile(t, filepath.Join(rootDir, "pkg", "router.go"), "package pkg")
	mustWriteFile(t, filepath.Join(rootDir, "pkg", "router_test.go"), "package pkg")
	mustWriteFile(t, filepath.Join(rootDir, "pkg", "other.txt"), "text")
	root, err := workspace.NewRoot(rootDir)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	base, err := workspace.ParseRelPath("pkg")
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}
	svc, err := New(Options{ListLimit: 1})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	list, err := svc.List(context.Background(), root, workspace.FileListOptions{Path: base, Search: "router", Limit: 5})
	if err != nil {
		t.Fatalf("list search: %v", err)
	}
	if !list.Truncated {
		t.Fatal("expected truncated search result")
	}
	if len(list.Items) != 1 || !strings.Contains(list.Items[0].Path.String(), "router") {
		t.Fatalf("unexpected search items: %+v", list.Items)
	}
}

func TestReadClassifiesTextImageAndBinary(t *testing.T) {
	rootDir := t.TempDir()
	mustWriteFile(t, filepath.Join(rootDir, "note.txt"), "hello")
	mustWriteFile(t, filepath.Join(rootDir, "report_zh.md"), "# 报告\n\n今晚完成了 ABCDE closeout。\n")
	mustWriteBytes(t, filepath.Join(rootDir, "legacy.md"), []byte("# legacy\n\nlatin1: caf\xe9\n"))
	mustWriteBytes(t, filepath.Join(rootDir, "pixel.png"), []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	mustWriteBytes(t, filepath.Join(rootDir, "blob.bin"), []byte{0x00, 0x01, 0x02, 0x03})
	root, err := workspace.NewRoot(rootDir)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	svc, err := New(Options{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	textPath, _ := workspace.ParseRelPath("note.txt")
	text, err := svc.Read(context.Background(), root, textPath)
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	if text.Kind != workspace.ContentKindText || text.Text != "hello" || text.Encoding != "utf-8" {
		t.Fatalf("unexpected text payload: %+v", text)
	}
	markdownPath, _ := workspace.ParseRelPath("report_zh.md")
	markdown, err := svc.Read(context.Background(), root, markdownPath)
	if err != nil {
		t.Fatalf("read markdown: %v", err)
	}
	if markdown.Kind != workspace.ContentKindText || !strings.Contains(markdown.Text, "今晚完成了") {
		t.Fatalf("unexpected markdown payload: %+v", markdown)
	}
	legacyPath, _ := workspace.ParseRelPath("legacy.md")
	legacy, err := svc.Read(context.Background(), root, legacyPath)
	if err != nil {
		t.Fatalf("read legacy markdown: %v", err)
	}
	if legacy.Kind != workspace.ContentKindText || !strings.Contains(legacy.Text, "\uFFFD") {
		t.Fatalf("unexpected legacy markdown payload: %+v", legacy)
	}
	imagePath, _ := workspace.ParseRelPath("pixel.png")
	image, err := svc.Read(context.Background(), root, imagePath)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	if image.Kind != workspace.ContentKindImage || image.Text != "" || image.DownloadName != "pixel.png" {
		t.Fatalf("unexpected image payload: %+v", image)
	}
	binaryPath, _ := workspace.ParseRelPath("blob.bin")
	binary, err := svc.Read(context.Background(), root, binaryPath)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	if binary.Kind != workspace.ContentKindUnsupported || binary.UnsupportedReason == "" {
		t.Fatalf("unexpected binary payload: %+v", binary)
	}
}

func TestReadAbsoluteMarkdownWithUnicode(t *testing.T) {
	rootDir := t.TempDir()
	path := filepath.Join(rootDir, "报告.md")
	mustWriteFile(t, path, "# 标题\n\n中文 markdown 应该可以预览。\n")
	svc, err := New(Options{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	result, err := svc.ReadAbsolute(context.Background(), path)
	if err != nil {
		t.Fatalf("read absolute: %v", err)
	}
	if result.Kind != workspace.ContentKindText || result.Path != filepath.Clean(path) || result.DownloadName != "报告.md" || !strings.Contains(result.Text, "中文 markdown") {
		t.Fatalf("unexpected absolute markdown payload: %+v", result)
	}
}

func TestReadRejectsLargeText(t *testing.T) {
	rootDir := t.TempDir()
	mustWriteFile(t, filepath.Join(rootDir, "large.txt"), strings.Repeat("a", 32))
	root, err := workspace.NewRoot(rootDir)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	svc, err := New(Options{MaxTextBytes: 16})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	path, _ := workspace.ParseRelPath("large.txt")
	result, err := svc.Read(context.Background(), root, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if result.Kind != workspace.ContentKindUnsupported || !strings.Contains(result.UnsupportedReason, "16") {
		t.Fatalf("unexpected large file payload: %+v", result)
	}
}

func TestNewRejectsInvalidLimits(t *testing.T) {
	if _, err := New(Options{MaxTextBytes: -1}); err == nil {
		t.Fatal("expected negative text limit to fail")
	}
}

func mustWriteFile(t *testing.T, path, text string) {
	t.Helper()
	mustWriteBytes(t, path, []byte(text))
}

func mustWriteBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
