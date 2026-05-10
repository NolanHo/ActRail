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

func TestSessionMessagesDefersOnlyToolEventsBeforeLatestTurn(t *testing.T) {
	items := []SessionMessage{
		{Seq: 1, Role: "user", Kind: "message", Text: "old prompt"},
		{Seq: 2, Kind: "tool", Type: "tool", Text: "old tool", EventID: "old-tool", ToolCallID: "call-old", Details: map[string]any{"arg": "old"}},
		{Seq: 3, Kind: "tool_result", Type: "tool_result", Text: "old result", EventID: "old-result", ToolCallID: "call-old", Details: map[string]any{"result": "old"}},
		{Seq: 4, Role: "assistant", Kind: "message", Text: "old answer"},
		{Seq: 5, Role: "user", Kind: "message", Text: "latest prompt"},
		{Seq: 6, Kind: "tool", Type: "tool", Text: "latest tool", EventID: "new-tool", ToolCallID: "call-new", Details: map[string]any{"arg": "new"}},
		{Seq: 7, Kind: "tool_result", Type: "tool_result", Text: "latest result", EventID: "new-result", ToolCallID: "call-new", Details: map[string]any{"result": "new"}},
	}
	response := paginateSessionMessagesForRequest(items, SessionMessagesRequest{Deferred: true, IncludeToolEvents: true})
	if len(response.Items) != len(items) {
		t.Fatalf("len(response.Items) = %d, want %d", len(response.Items), len(items))
	}
	if response.Items[1].Text != "" || response.Items[1].Details["deferred"] != true || response.Items[2].Text != "" || response.Items[2].Details["deferred"] != true {
		t.Fatalf("old tool details = %+v %+v, want deferred", response.Items[1], response.Items[2])
	}
	if response.Items[5].Text != "latest tool" || response.Items[6].Text != "latest result" {
		t.Fatalf("latest tool details = %+v %+v, want hydrated", response.Items[5], response.Items[6])
	}
}

func TestSessionMessagesFiltersToolEventsByDefaultBeforePagination(t *testing.T) {
	items := []SessionMessage{
		{Seq: 1, Role: "user", Kind: "message", Text: "old prompt"},
		{Seq: 2, Kind: "tool", Type: "tool", Text: "hidden tool", Name: "read", ToolCallID: "call-read", TS: 10},
		{Seq: 3, Kind: "tool_result", Type: "tool_result", Text: "hidden result", ToolCallID: "call-read", TS: 12},
		{Seq: 4, Role: "assistant", Kind: "message", Text: "old answer"},
		{Seq: 5, Role: "user", Kind: "message", Text: "new prompt"},
		{Seq: 6, Role: "assistant", Kind: "message", Text: "new answer"},
	}

	response := paginateSessionMessagesForRequest(items, SessionMessagesRequest{Limit: 4})
	if len(response.Items) != 4 {
		t.Fatalf("len(response.Items) = %d, want 4 visible conversation messages", len(response.Items))
	}
	for _, item := range response.Items {
		if item.Kind == "tool" || item.Kind == "tool_result" {
			t.Fatalf("response item = %+v, want tool events hidden by default", item)
		}
	}
	if response.TailSeq != 6 {
		t.Fatalf("TailSeq = %d, want visible tail seq 6", response.TailSeq)
	}
	summary, ok := response.Items[1].Details[toolActivitySummaryDetailsKey].(sessionToolActivitySummary)
	if !ok {
		t.Fatalf("assistant details = %+v, want hidden tool activity summary", response.Items[1].Details)
	}
	if summary.TotalTools != 1 || summary.OK != 1 || summary.SummaryText != "Ran 1 tool · 1 ok · 2s" {
		t.Fatalf("tool activity summary = %+v, want one successful tool", summary)
	}

	withTools := paginateSessionMessagesForRequest(items, SessionMessagesRequest{Limit: 6, IncludeToolEvents: true})
	if len(withTools.Items) != 6 {
		t.Fatalf("len(withTools.Items) = %d, want all items when tool events requested", len(withTools.Items))
	}
	if _, ok := withTools.Items[3].Details[toolActivitySummaryDetailsKey]; ok {
		t.Fatalf("assistant details = %+v, want no synthetic summary when raw tools are requested", withTools.Items[3].Details)
	}
}

func TestSessionMessagesByIDReturnsDeferredToolDetails(t *testing.T) {
	items := []SessionMessage{
		{Seq: 1, Role: "user", Kind: "message", Text: "old prompt"},
		{Seq: 2, Kind: "tool", Type: "tool", Text: "old tool", EventID: "old-tool", ToolCallID: "call-old", Details: map[string]any{"arg": "old"}},
		{Seq: 3, Role: "user", Kind: "message", Text: "latest prompt"},
	}
	if msg, ok := findSessionMessageByID(items, "old-tool", ""); !ok || msg.Text != "old tool" || msg.Details["arg"] != "old" {
		t.Fatalf("find by event = %+v, %v", msg, ok)
	}
	if msg, ok := findSessionMessageByID(items, "", "call-old"); !ok || msg.Text != "old tool" || msg.Details["arg"] != "old" {
		t.Fatalf("find by tool_call_id = %+v, %v", msg, ok)
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
