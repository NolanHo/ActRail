package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSourceHistoryPageReturnsNewestMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	file, err := os.Create(path)
	if err != nil {
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

	page, ok, err := loadSourceHistoryPage(path, SessionMessagesRequest{Limit: 3000})
	if err != nil {
		t.Fatalf("loadSourceHistoryPage() error = %v", err)
	}
	if !ok {
		t.Fatal("loadSourceHistoryPage() ok = false, want true")
	}
	response := sourceHistorySessionMessagesResponse(page, SessionMessagesRequest{Limit: 3000})
	if len(response.Items) != 3000 {
		t.Fatalf("len(response.Items) = %d, want 3000", len(response.Items))
	}
	if response.Items[0].Text != "prompt 2000" || response.Items[len(response.Items)-1].Text != "prompt 4999" {
		t.Fatalf("response page = %q..%q, want prompt 2000..prompt 4999", response.Items[0].Text, response.Items[len(response.Items)-1].Text)
	}
	if !response.HasMore || response.NextBeforeSeq == nil {
		t.Fatalf("response cursors = hasMore %v next %v, want older cursor", response.HasMore, response.NextBeforeSeq)
	}
}

func TestLoadSourceHistoryPageDefersOlderToolDetails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	body := "" +
		`{"type":"message","id":"u-old","message":{"role":"user","content":[{"type":"text","text":"old prompt"}]}}` + "\n" +
		`{"type":"message","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"toolCall","id":"call-old","name":"read","arguments":{"path":"old.txt"}}]}}` + "\n" +
		`{"type":"turn_end","id":"turn-old","toolResults":[{"role":"toolResult","toolCallId":"call-old","toolName":"read","content":[{"type":"text","text":"old result"}],"isError":false}]}` + "\n" +
		`{"type":"message","id":"a-old","message":{"role":"assistant","content":[{"type":"text","text":"old answer"}],"stopReason":"stop"}}` + "\n" +
		`{"type":"message","id":"u-new","message":{"role":"user","content":[{"type":"text","text":"new prompt"}]}}` + "\n" +
		`{"type":"message","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"toolCall","id":"call-new","name":"read","arguments":{"path":"new.txt"}}]}}` + "\n" +
		`{"type":"turn_end","id":"turn-new","toolResults":[{"role":"toolResult","toolCallId":"call-new","toolName":"read","content":[{"type":"text","text":"new result"}],"isError":false}]}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	req := SessionMessagesRequest{Limit: 20, Deferred: true}
	page, ok, err := loadSourceHistoryPage(path, req)
	if err != nil {
		t.Fatalf("loadSourceHistoryPage() error = %v", err)
	}
	if !ok {
		t.Fatal("loadSourceHistoryPage() ok = false, want true")
	}
	response := sourceHistorySessionMessagesResponse(page, req)
	oldTool := findItemByToolCallID(response.Items, "call-old", "tool")
	oldResult := findItemByToolCallID(response.Items, "call-old", "tool_result")
	newTool := findItemByToolCallID(response.Items, "call-new", "tool")
	newResult := findItemByToolCallID(response.Items, "call-new", "tool_result")
	if oldTool.Text != "" || oldTool.Details["deferred"] != true || oldResult.Text != "" || oldResult.Details["deferred"] != true {
		t.Fatalf("old tool details = %+v %+v, want deferred", oldTool, oldResult)
	}
	if newTool.Details["deferred"] == true || newTool.Details["arguments"] == nil || newResult.Text != "new result" {
		t.Fatalf("new tool details = %+v %+v, want hydrated latest turn", newTool, newResult)
	}
}

func findItemByToolCallID(items []SessionMessage, callID string, typ string) SessionMessage {
	for _, item := range items {
		if item.ToolCallID == callID && item.Type == typ {
			return item
		}
	}
	return SessionMessage{}
}
