package iodclient

import (
	"testing"

	"actrail/internal/adapters/iod"
	"actrail/internal/domain/session"
)

func TestManifestIndexCandidatesPreferBindingAndKeepSessionFallbacks(t *testing.T) {
	sessionID := mustSessionID(t, "s_123")
	preferred := mustGenerationID(t, "g_2")
	old := discoveredManifestForTest(t, sessionID, "g_1", 10)
	bound := discoveredManifestForTest(t, sessionID, "g_2", 20)
	newer := discoveredManifestForTest(t, sessionID, "g_3", 30)
	otherSession := discoveredManifestForTest(t, mustSessionID(t, "s_456"), "g_9", 99)

	index := NewManifestIndex([]DiscoveredManifest{old, newer, bound, otherSession})
	got := index.Candidates(sessionID, &preferred)
	if len(got) != 3 {
		t.Fatalf("len(Candidates()) = %d, want preferred plus same-session fallbacks", len(got))
	}
	if got[0].Manifest.GenerationID != preferred {
		t.Fatalf("first candidate generation = %q, want preferred %q", got[0].Manifest.GenerationID, preferred)
	}
	if got[1].Manifest.GenerationID != newer.Manifest.GenerationID || got[2].Manifest.GenerationID != old.Manifest.GenerationID {
		t.Fatalf("fallback candidates = [%q %q], want newest-to-oldest [%q %q]",
			got[1].Manifest.GenerationID, got[2].Manifest.GenerationID,
			newer.Manifest.GenerationID, old.Manifest.GenerationID)
	}
}

func TestManifestIndexCodexThreadCandidatesCrossSession(t *testing.T) {
	firstSession := mustSessionID(t, "s_123")
	secondSession := mustSessionID(t, "s_456")
	old := discoveredManifestForTest(t, firstSession, "g_1", 10)
	old.Manifest.CodexThreadID = "thread-wanted"
	newer := discoveredManifestForTest(t, secondSession, "g_2", 20)
	newer.Manifest.CodexThreadID = "thread-wanted"
	other := discoveredManifestForTest(t, secondSession, "g_3", 30)
	other.Manifest.CodexThreadID = "thread-other"

	index := NewManifestIndex([]DiscoveredManifest{old, newer, other})
	got := index.CodexThreadCandidates("thread-wanted", nil)
	if len(got) != 2 {
		t.Fatalf("len(CodexThreadCandidates()) = %d, want 2", len(got))
	}
	if got[0].Manifest.SessionID != secondSession || got[1].Manifest.SessionID != firstSession {
		t.Fatalf("candidate session order = [%q %q], want newest cross-session first", got[0].Manifest.SessionID, got[1].Manifest.SessionID)
	}
}

func TestManifestIndexCodexThreadCandidatesFallbackUsesMatcher(t *testing.T) {
	sessionID := mustSessionID(t, "s_123")
	item := discoveredManifestForTest(t, sessionID, "g_1", 10)
	item.Manifest.SessionHistoryPath = "/tmp/rollout-thread-wanted.jsonl"

	index := NewManifestIndex([]DiscoveredManifest{item})
	if got := index.CodexThreadCandidates("thread-wanted", nil); len(got) != 0 {
		t.Fatalf("CodexThreadCandidates(nil matcher) = %+v, want no path fallback", got)
	}
	got := index.CodexThreadCandidates("thread-wanted", func(path, threadID string) bool {
		return path == "/tmp/rollout-thread-wanted.jsonl" && threadID == "thread-wanted"
	})
	if len(got) != 1 || got[0].Manifest.SessionID != sessionID {
		t.Fatalf("CodexThreadCandidates(matcher) = %+v, want one fallback candidate", got)
	}
}

func discoveredManifestForTest(t *testing.T, sessionID session.SessionID, generation string, startTS float64) DiscoveredManifest {
	t.Helper()
	generationID := mustGenerationID(t, generation)
	proof, err := iod.NewHelloProof(123, nil, "/tmp/"+generation+".wal", "/tmp/"+generation+".sock", startTS)
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	return DiscoveredManifest{Path: "/tmp/" + generation + "/generation-manifest.json", Manifest: manifest}
}
