package app

import (
	"strings"
	"testing"

	"actrail/internal/domain/session"
)

func TestSessionMessageCacheKeepsCompletionMetadata(t *testing.T) {
	sessionID, err := session.ParseSessionID("s_1")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	cache := newSessionMessageCache(2)
	items := []SessionMessage{{Seq: 1, Role: "assistant", Text: "done"}}

	cache.PutWithCompletion(sessionID, "sig:one", items, true)

	got, complete, ok := cache.GetWithCompletion(sessionID, "sig:one")
	if !ok {
		t.Fatal("GetWithCompletion() ok = false")
	}
	if !complete {
		t.Fatal("GetWithCompletion() complete = false, want true")
	}
	if len(got) != 1 || got[0].Text != "done" {
		t.Fatalf("GetWithCompletion() items = %+v", got)
	}

	if _, _, ok := cache.GetWithCompletion(sessionID, "sig:two"); ok {
		t.Fatal("GetWithCompletion() with stale signature ok = true")
	}
}

func TestSessionMessageCacheSkipsOversizedEntries(t *testing.T) {
	sessionID, err := session.ParseSessionID("s_1")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	cache := newSessionMessageCacheWithBudget(2, 1024, 512)

	cache.PutWithCompletion(sessionID, "sig:large", []SessionMessage{{Seq: 1, Role: "assistant", Text: strings.Repeat("x", 1024)}}, true)

	if _, _, ok := cache.GetWithCompletion(sessionID, "sig:large"); ok {
		t.Fatal("GetWithCompletion() ok = true for oversized entry")
	}
}

func TestSessionMessageCacheEvictsToStayWithinByteBudget(t *testing.T) {
	first, err := session.ParseSessionID("s_1")
	if err != nil {
		t.Fatalf("ParseSessionID() first error = %v", err)
	}
	second, err := session.ParseSessionID("s_2")
	if err != nil {
		t.Fatalf("ParseSessionID() second error = %v", err)
	}
	cache := newSessionMessageCacheWithBudget(8, 900, 900)

	cache.PutWithCompletion(first, "sig:first", []SessionMessage{{Seq: 1, Role: "assistant", Text: strings.Repeat("a", 300)}}, true)
	cache.PutWithCompletion(second, "sig:second", []SessionMessage{{Seq: 1, Role: "assistant", Text: strings.Repeat("b", 300)}}, true)

	if _, _, ok := cache.GetWithCompletion(first, "sig:first"); ok {
		t.Fatal("GetWithCompletion() kept least-recent entry beyond byte budget")
	}
	if _, _, ok := cache.GetWithCompletion(second, "sig:second"); !ok {
		t.Fatal("GetWithCompletion() evicted newest entry")
	}
}
