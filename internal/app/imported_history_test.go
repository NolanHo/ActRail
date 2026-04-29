package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPISessionSourcePathsUseStrictBackendSessionID(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pi-home")
	t.Setenv("PI_HOME", root)
	dir := filepath.Join(root, "agent", "sessions", piSessionDirName("/tmp/project"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exact := filepath.Join(dir, "2026-01-01T00-00-00-000Z_pi-exact.jsonl")
	locator := filepath.Join(dir, "2026-01-02T00-00-00-000Z_locator.jsonl")
	if err := os.WriteFile(exact, []byte(`{"type":"session","version":3,"id":"pi-exact","cwd":"/tmp/project"}
{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"ForkKV real work"}]}}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(locator, []byte(`{"type":"session","version":3,"id":"pi-locator","cwd":"/tmp/project"}
{"type":"message","id":"u2","message":{"role":"user","content":[{"type":"text","text":"ForkKV locator"}]}}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	paths := piHistorySourcePaths(sessionRecord{cwd: "/tmp/project", alias: "ForkKV", importedBackendSessionID: "pi-exact", importedSourcePath: locator})
	if len(paths) != 1 || paths[0] != exact {
		t.Fatalf("piHistorySourcePaths() = %#v, want exact Pi session path", paths)
	}
}

func TestPISessionSourcePathsDoNotInferFromSessionTitle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pi-home")
	t.Setenv("PI_HOME", root)
	dir := filepath.Join(root, "agent", "sessions", piSessionDirName("/tmp/project"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "2026-01-01T00-00-00-000Z_main.jsonl")
	locator := filepath.Join(dir, "2026-01-02T00-00-00-000Z_locator.jsonl")
	mainBody := `{"type":"session","version":3,"id":"pi-main","cwd":"/tmp/project"}
{"type":"session_info","name":"ForkKV"}
{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"ForkKV real work"}]}}
`
	for i := 0; i < 100; i++ {
		mainBody += `{"type":"message","id":"a` + string(rune('a'+(i%26))) + `","message":{"role":"assistant","content":[{"type":"text","text":"filler"}]}}
`
	}
	if err := os.WriteFile(main, []byte(mainBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(locator, []byte(`{"type":"session","version":3,"id":"pi-locator","cwd":"/tmp/project"}
{"type":"message","id":"u2","message":{"role":"user","content":[{"type":"text","text":"帮我找 ForkKV session"}]}}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	actrailPath := filepath.Join(dir, "missing_actrail_s_1.jsonl")
	paths := piHistorySourcePaths(sessionRecord{cwd: "/tmp/project", alias: "ForkKV", importedSourcePath: actrailPath})
	if len(paths) != 0 {
		t.Fatalf("piHistorySourcePaths() = %#v, want no title-only inferred source", paths)
	}
}

func TestImportedSessionMessagesUsePIEntryIDAsEventID(t *testing.T) {
	items, err := importedSessionMessagesFromJSONLBytes("fixture.jsonl", []byte(`{"type":"message","id":"entry-a","parentId":"entry-user","message":{"role":"assistant","content":[{"type":"text","text":"answer","textSignature":"sig"}],"stopReason":"stop"}}
{"type":"turn_end","id":"entry-turn","toolResults":[{"role":"toolResult","toolCallId":"call-1","toolName":"read","content":[{"type":"text","text":"result"}],"isError":false}]}
`))
	if err != nil {
		t.Fatalf("importedSessionMessagesFromJSONLBytes() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].EventID != "pi:message:entry-a" || items[0].ParentEventID != "pi:message:entry-user" {
		t.Fatalf("assistant ids = (%q, %q), want pi entry ids", items[0].EventID, items[0].ParentEventID)
	}
	if items[1].EventID != "pi:tool_result:call-1:0" {
		t.Fatalf("tool result event_id = %q, want call id key", items[1].EventID)
	}
}

func TestDuplicateWALMessageMatchesUserWithSameTextAndCloseTimestamp(t *testing.T) {
	items := []SessionMessage{{Role: "user", Kind: "message", Text: "continue", TS: 1000}}
	for i := 0; i < 12; i++ {
		items = append(items, SessionMessage{Kind: "tool_result", Type: "tool_result", Text: "tool result filler line"})
	}
	if !duplicateWALMessage(items, SessionMessage{Role: "user", Kind: "message", Text: "continue", TS: 1060}) {
		t.Fatal("duplicateWALMessage() = false, want true for close duplicate user projection")
	}
}

func TestDuplicateWALMessageKeepsRepeatedUserTextFarApart(t *testing.T) {
	items := []SessionMessage{{Role: "user", Kind: "message", Text: "continue", TS: 1000}}
	if duplicateWALMessage(items, SessionMessage{Role: "user", Kind: "message", Text: "continue", TS: 2000}) {
		t.Fatal("duplicateWALMessage() = true, want false for repeated user text in separate turns")
	}
}

func TestDuplicateWALMessageMatchesLongAssistantBeyondShortWindow(t *testing.T) {
	text := "assistant final answer that is long enough to represent a real final message rather than a repeated acknowledgement across separate turns"
	items := []SessionMessage{{Role: "assistant", Kind: "message", Text: text}}
	for i := 0; i < 12; i++ {
		items = append(items, SessionMessage{Kind: "tool_result", Type: "tool_result", Text: "tool result filler line"})
	}
	if !duplicateWALMessage(items, SessionMessage{Role: "assistant", Kind: "message", Text: text}) {
		t.Fatal("duplicateWALMessage() = false, want true for long assistant replay beyond short tail window")
	}
}

func TestDuplicateWALMessageDoesNotMatchShortAssistantBeyondShortWindow(t *testing.T) {
	items := []SessionMessage{{Role: "assistant", Kind: "message", Text: "OK"}}
	for i := 0; i < 12; i++ {
		items = append(items, SessionMessage{Kind: "tool_result", Type: "tool_result", Text: "tool result filler line"})
	}
	if duplicateWALMessage(items, SessionMessage{Role: "assistant", Kind: "message", Text: "OK"}) {
		t.Fatal("duplicateWALMessage() = true, want false for short assistant text outside short tail window")
	}
}
