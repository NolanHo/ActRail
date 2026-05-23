package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func TestDetachedImportedPIHistoryUsesTailPageForInitialLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "2026-01-01T00-00-00-000Z_pi-tail.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(file, `{"type":"session","version":3,"id":"pi-tail","cwd":"/tmp/project"}`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		if _, err := fmt.Fprintf(file, `{"type":"message","id":"u-%04d","message":{"role":"user","content":[{"type":"text","text":"prompt %04d"}]}}`+"\n", i, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	identity, err := session.NewDetachedIdentity("s_tail", "pi")
	if err != nil {
		t.Fatal(err)
	}
	svc := newStub(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() })
	record, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendPI,
		CWD:              "/tmp/project",
		SourcePath:       path,
		BackendSessionID: "pi-tail",
		SourceConfidence: sourceConfidenceExact,
	})
	if err != nil {
		t.Fatal(err)
	}

	response, ok, err := svc.loadDetachedImportedPIHistory(context.Background(), record, SessionMessagesRequest{Limit: 3000, Deferred: true})
	if err != nil {
		t.Fatalf("loadDetachedImportedPIHistory() error = %v", err)
	}
	if !ok {
		t.Fatal("loadDetachedImportedPIHistory() ok = false, want true")
	}
	if len(response.Items) != maxSessionMessagesConversationPage {
		t.Fatalf("len(response.Items) = %d, want %d", len(response.Items), maxSessionMessagesConversationPage)
	}
	if response.Items[0].Text != "prompt 4800" || response.Items[len(response.Items)-1].Text != "prompt 4999" {
		t.Fatalf("response page = %q..%q, want prompt 4800..prompt 4999", response.Items[0].Text, response.Items[len(response.Items)-1].Text)
	}
	if !response.HasMore || response.NextBeforeSeq == nil {
		t.Fatalf("response cursors = hasMore %v next %v, want older cursor", response.HasMore, response.NextBeforeSeq)
	}
}

func TestPIAuthoritativeHistoryFallsBackToTailPage(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_HOME", filepath.Join(root, "pi-home"))
	sourceDir := filepath.Join(os.Getenv("PI_HOME"), "agent", "sessions", piSessionDirName(cwd))
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sourceDir, "2026-01-01T00-00-00-000Z_pi-authoritative_pi-authoritative.jsonl")
	var b strings.Builder
	b.WriteString(`{"type":"session","version":3,"id":"pi-authoritative","cwd":`)
	b.WriteString(fmt.Sprintf("%q", cwd))
	b.WriteString("}\n")
	for i := 0; i < 5000; i++ {
		b.WriteString(fmt.Sprintf(`{"type":"message","id":"u-%04d","message":{"role":"user","content":[{"type":"text","text":"prompt %04d"}]}}`+"\n", i, i))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	identity, err := session.NewDetachedIdentity("s_authoritative", "pi")
	if err != nil {
		t.Fatal(err)
	}
	svc := newStub(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() })
	record, err := svc.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          session.BackendPI,
		CWD:              cwd,
		SourcePath:       path,
		BackendSessionID: "pi-authoritative",
		SourceConfidence: sourceConfidenceExact,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, ok, err := svc.loadPIAuthoritativeHistory(context.Background(), record, svc.cfg.Storage.DataDir, SessionMessagesRequest{Limit: 3000, Deferred: true})
	if err != nil {
		t.Fatalf("loadPIAuthoritativeHistory() error = %v", err)
	}
	if !ok {
		t.Fatal("loadPIAuthoritativeHistory() ok = false, want true")
	}
	if len(response.Items) != maxSessionMessagesConversationPage || response.Items[0].Text != "prompt 4800" || response.Items[len(response.Items)-1].Text != "prompt 4999" {
		t.Fatalf("response page len/text = %d %q..%q, want %d prompt 4800..prompt 4999", len(response.Items), response.Items[0].Text, response.Items[len(response.Items)-1].Text, maxSessionMessagesConversationPage)
	}
}
