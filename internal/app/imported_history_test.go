package app

import (
	"os"
	"path/filepath"
	"testing"
)

func testUint64Ptr(value uint64) *uint64 {
	return &value
}

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

func TestSessionMessagesAddsOpenToolActivitySummaryWhenToolsHidden(t *testing.T) {
	items := []SessionMessage{
		{Seq: 1, Role: "user", Kind: "message", Text: "latest prompt", TS: 10},
		{Seq: 2, Kind: "tool", Type: "tool", Text: "running tool", Name: "bash", ToolCallID: "call-run", TS: 12},
	}

	response := paginateSessionMessagesForRequest(items, SessionMessagesRequest{AfterSeq: testUint64Ptr(1)})
	if len(response.Items) != 1 {
		t.Fatalf("len(response.Items) = %d, want one compact activity event", len(response.Items))
	}
	item := response.Items[0]
	if item.Kind != "tool_activity_summary" || item.Seq != 2 {
		t.Fatalf("summary item = %+v, want compact activity at raw tail seq", item)
	}
	summary, ok := item.Details[toolActivitySummaryDetailsKey].(sessionToolActivitySummary)
	if !ok {
		t.Fatalf("summary details = %+v, want tool activity summary", item.Details)
	}
	if summary.Running != 1 || summary.TotalTools != 1 || summary.SummaryText != "Running 1/1 tool" {
		t.Fatalf("tool activity summary = %+v, want running tool", summary)
	}

	withTools := paginateSessionMessagesForRequest(items, SessionMessagesRequest{AfterSeq: testUint64Ptr(1), IncludeToolEvents: true})
	if len(withTools.Items) != 1 || withTools.Items[0].Kind != "tool" {
		t.Fatalf("with tools = %+v, want raw tool only", withTools.Items)
	}
}

func TestSessionMessagesAfterSeqRespectsLimit(t *testing.T) {
	items := []SessionMessage{
		{Seq: 1, Role: "user", Kind: "message", Text: "prompt 1"},
		{Seq: 2, Role: "assistant", Kind: "message", Text: "answer 1"},
		{Seq: 3, Role: "user", Kind: "message", Text: "prompt 2"},
		{Seq: 4, Role: "assistant", Kind: "message", Text: "answer 2"},
		{Seq: 5, Role: "assistant", Kind: "message", Text: "answer 3"},
	}

	response := paginateSessionMessagesForRequest(items, SessionMessagesRequest{AfterSeq: testUint64Ptr(0), Limit: 2})
	if len(response.Items) != 2 {
		t.Fatalf("len(response.Items) = %d, want bounded page of 2", len(response.Items))
	}
	if response.Items[0].Seq != 4 || response.Items[1].Seq != 5 {
		t.Fatalf("response.Items = %+v, want latest seq 4 and 5", response.Items)
	}
	if !response.HasMore || response.NextBeforeSeq == nil || *response.NextBeforeSeq != 4 {
		t.Fatalf("response paging = hasMore %v next %v, want older cursor 4", response.HasMore, response.NextBeforeSeq)
	}
	if response.TailSeq != 5 {
		t.Fatalf("TailSeq = %d, want 5", response.TailSeq)
	}
}

func TestSessionMessagesFastPaginationAnnotatesWindowedToolActivity(t *testing.T) {
	items := make([]SessionMessage, 0, 1105)
	for i := 1; i <= 1100; i++ {
		role := "assistant"
		if i%2 == 1 {
			role = "user"
		}
		items = append(items, SessionMessage{Seq: uint64(i), Role: role, Kind: "message", Text: "history"})
	}
	items = append(items,
		SessionMessage{Seq: 1101, Role: "user", Kind: "message", Text: "latest prompt", TS: 10},
		SessionMessage{Seq: 1102, Kind: "tool", Type: "tool", Name: "read", ToolCallID: "call-read", TS: 11},
		SessionMessage{Seq: 1103, Kind: "tool_result", Type: "tool_result", ToolCallID: "call-read", TS: 13},
		SessionMessage{Seq: 1104, Role: "assistant", Kind: "message", Text: "latest answer", TS: 14},
	)

	response := paginateSessionMessagesForRequest(items, SessionMessagesRequest{Limit: 2})
	if len(response.Items) != 2 || response.Items[0].Seq != 1101 || response.Items[1].Seq != 1104 {
		t.Fatalf("response.Items = %+v, want latest user and assistant", response.Items)
	}
	if !response.HasMore || response.NextBeforeSeq == nil || *response.NextBeforeSeq != 1101 {
		t.Fatalf("response paging = hasMore %v next %v, want older cursor 1101", response.HasMore, response.NextBeforeSeq)
	}
	summary, ok := response.Items[1].Details[toolActivitySummaryDetailsKey].(sessionToolActivitySummary)
	if !ok {
		t.Fatalf("assistant details = %+v, want hidden tool activity summary", response.Items[1].Details)
	}
	if summary.TotalTools != 1 || summary.OK != 1 {
		t.Fatalf("tool activity summary = %+v, want one successful tool", summary)
	}
}

func TestSessionMessagesFastPaginationAnchorsLatestLongTurnAtUser(t *testing.T) {
	items := make([]SessionMessage, 0, 1300)
	for i := 1; i <= 1100; i++ {
		role := "assistant"
		if i%2 == 1 {
			role = "user"
		}
		items = append(items, SessionMessage{Seq: uint64(i), Role: role, Kind: "message", Text: "older history"})
	}
	items = append(items, SessionMessage{Seq: 1101, Role: "user", Kind: "message", Text: "latest prompt"})
	for i := 1102; i <= 1299; i++ {
		items = append(items, SessionMessage{Seq: uint64(i), Kind: "tool_result", Type: "tool_result", Text: "hidden tool output"})
	}
	items = append(items, SessionMessage{Seq: 1300, Kind: "custom_message", Type: "custom_message", Text: "subagent update"})

	response := paginateSessionMessagesForRequest(items, SessionMessagesRequest{Limit: 20})
	if len(response.Items) != 2 {
		t.Fatalf("response.Items = %+v, want latest user plus compact activity", response.Items)
	}
	if response.Items[0].Seq != 1101 || response.Items[0].Role != "user" || response.Items[0].Text != "latest prompt" {
		t.Fatalf("first item = %+v, want latest user prompt", response.Items[0])
	}
	if response.Items[1].Seq != 1300 || response.Items[1].Kind != "custom_message" {
		t.Fatalf("second item = %+v, want latest visible activity at tail", response.Items[1])
	}
	if response.TailSeq != 1300 {
		t.Fatalf("TailSeq = %d, want 1300", response.TailSeq)
	}
}

func TestSessionMessagesTailStartsAtLatestTurnBoundary(t *testing.T) {
	items := []SessionMessage{
		{Seq: 1, Role: "user", Kind: "message", Text: "old prompt"},
		{Seq: 2, Role: "assistant", Kind: "message", Text: "old answer"},
		{Seq: 3, Role: "user", Kind: "message", Text: "latest prompt"},
		{Seq: 4, Role: "assistant", Kind: "message", Text: "commentary 1"},
		{Seq: 5, Role: "assistant", Kind: "message", Text: "commentary 2"},
		{Seq: 6, Role: "assistant", Kind: "message", Text: "final answer"},
	}

	response := paginateSessionMessagesForRequest(items, SessionMessagesRequest{Limit: 2})
	if got := messageSeqs(response.Items); !sameUint64s(got, []uint64{3, 4, 5, 6}) {
		t.Fatalf("response seqs = %#v, want latest turn from user boundary", got)
	}
	if !response.HasMore || response.NextBeforeSeq == nil || *response.NextBeforeSeq != 3 {
		t.Fatalf("response paging = hasMore %v next %v, want cursor at latest user seq 3", response.HasMore, response.NextBeforeSeq)
	}
}

func TestSessionMessagesLoadOlderStartsAtTurnBoundary(t *testing.T) {
	items := []SessionMessage{
		{Seq: 1, Role: "user", Kind: "message", Text: "first prompt"},
		{Seq: 2, Role: "assistant", Kind: "message", Text: "first answer"},
		{Seq: 3, Role: "user", Kind: "message", Text: "middle prompt"},
		{Seq: 4, Role: "assistant", Kind: "message", Text: "middle commentary 1"},
		{Seq: 5, Role: "assistant", Kind: "message", Text: "middle commentary 2"},
		{Seq: 6, Role: "assistant", Kind: "message", Text: "middle final"},
		{Seq: 7, Role: "user", Kind: "message", Text: "latest prompt"},
		{Seq: 8, Role: "assistant", Kind: "message", Text: "latest answer"},
	}

	response := paginateSessionMessagesForRequest(items, SessionMessagesRequest{BeforeSeq: testUint64Ptr(7), Limit: 2})
	if got := messageSeqs(response.Items); !sameUint64s(got, []uint64{3, 4, 5, 6}) {
		t.Fatalf("response seqs = %#v, want older page from user boundary", got)
	}
	if !response.HasMore || response.NextBeforeSeq == nil || *response.NextBeforeSeq != 3 {
		t.Fatalf("response paging = hasMore %v next %v, want cursor at middle user seq 3", response.HasMore, response.NextBeforeSeq)
	}
}

func TestSessionMessagesConversationLimitDoesNotCountToolEvents(t *testing.T) {
	items := []SessionMessage{
		{Seq: 1, Role: "user", Kind: "message", Text: "old prompt"},
		{Seq: 2, Kind: "tool", Type: "tool", Text: "old tool"},
		{Seq: 3, Kind: "tool_result", Type: "tool_result", Text: "old result"},
		{Seq: 4, Role: "assistant", Kind: "message", Text: "old answer"},
		{Seq: 5, Role: "user", Kind: "message", Text: "new prompt"},
		{Seq: 6, Kind: "tool", Type: "tool", Text: "new tool"},
		{Seq: 7, Kind: "tool_result", Type: "tool_result", Text: "new result"},
		{Seq: 8, Role: "assistant", Kind: "message", Text: "new answer"},
	}

	response := paginateSessionMessagesForRequest(items, SessionMessagesRequest{Limit: 2, IncludeToolEvents: true})
	if got := messageSeqs(response.Items); !sameUint64s(got, []uint64{5, 6, 7, 8}) {
		t.Fatalf("response seqs = %#v, want latest turn including tools without counting them", got)
	}
}

func TestSessionMessagesConversationLimitCapsPreviousTurnsAtTwoHundred(t *testing.T) {
	items := make([]SessionMessage, 0, 260)
	items = append(items, SessionMessage{Seq: 1, Role: "user", Kind: "message", Text: "old prompt"})
	for seq := 2; seq <= 51; seq++ {
		items = append(items, SessionMessage{Seq: uint64(seq), Role: "assistant", Kind: "message", Text: "old commentary"})
	}
	items = append(items, SessionMessage{Seq: 52, Role: "user", Kind: "message", Text: "latest prompt"})
	for seq := 53; seq <= 251; seq++ {
		items = append(items, SessionMessage{Seq: uint64(seq), Role: "assistant", Kind: "message", Text: "latest commentary"})
	}

	response := paginateSessionMessagesForRequest(items, SessionMessagesRequest{Limit: 500})
	if len(response.Items) != 200 || response.Items[0].Seq != 52 {
		t.Fatalf("response window = len %d first %+v, want latest 200-message turn from user seq 52", len(response.Items), response.Items[0])
	}
	if !response.HasMore || response.NextBeforeSeq == nil || *response.NextBeforeSeq != 52 {
		t.Fatalf("response paging = hasMore %v next %v, want cursor at latest user seq 52", response.HasMore, response.NextBeforeSeq)
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

func messageSeqs(items []SessionMessage) []uint64 {
	seqs := make([]uint64, 0, len(items))
	for _, item := range items {
		seqs = append(seqs, item.Seq)
	}
	return seqs
}

func sameUint64s(a []uint64, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for idx := range a {
		if a[idx] != b[idx] {
			return false
		}
	}
	return true
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
