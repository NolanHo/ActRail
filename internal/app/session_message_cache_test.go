package app

import (
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
