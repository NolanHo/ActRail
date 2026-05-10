package iodclient

import (
	"testing"

	"actrail/internal/adapters/iod"
	"actrail/internal/domain/session"
)

func TestManifestIndexCandidatesRestrictToPreferredBinding(t *testing.T) {
	sessionID := mustSessionID(t, "s_123")
	preferred := mustGenerationID(t, "g_2")
	old := discoveredManifestForTest(t, sessionID, "g_1", 10)
	bound := discoveredManifestForTest(t, sessionID, "g_2", 20)
	newer := discoveredManifestForTest(t, sessionID, "g_3", 30)
	otherSession := discoveredManifestForTest(t, mustSessionID(t, "s_456"), "g_9", 99)

	index := NewManifestIndex([]DiscoveredManifest{old, newer, bound, otherSession})
	got := index.Candidates(sessionID, &preferred)
	if len(got) != 1 {
		t.Fatalf("len(Candidates()) = %d, want only preferred generation", len(got))
	}
	if got[0].Manifest.GenerationID != preferred {
		t.Fatalf("first candidate generation = %q, want preferred %q", got[0].Manifest.GenerationID, preferred)
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
