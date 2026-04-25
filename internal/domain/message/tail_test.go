package message

import "testing"

func TestNewCommittedTailOwnsSeqWithoutTurnID(t *testing.T) {
	tail := NewCommittedTail(181)
	if err := tail.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if tail.Owner() != TailOwnerTranscript {
		t.Fatalf("Owner() = %q, want %q", tail.Owner(), TailOwnerTranscript)
	}
	if _, ok := tail.TurnID(); ok {
		t.Fatal("TurnID() ok = true, want false")
	}
}

func TestNewLiveTailRequiresTurnID(t *testing.T) {
	if _, err := NewLiveTail(181, "  "); err == nil {
		t.Fatal("NewLiveTail() error = nil, want error")
	}
}

func TestNewTailSnapshotRejectsTranscriptOwnerWithTurnID(t *testing.T) {
	turnID, err := NewTurnID("turn_9")
	if err != nil {
		t.Fatalf("NewTurnID() error = %v", err)
	}
	if _, err := NewTailSnapshot(181, TailOwnerTranscript, &turnID); err == nil {
		t.Fatal("NewTailSnapshot() error = nil, want error")
	}
}

func TestParseTailOwnerNormalizesWhitespace(t *testing.T) {
	owner, err := ParseTailOwner("  LIVE_TURN  ")
	if err != nil {
		t.Fatalf("ParseTailOwner() error = %v", err)
	}
	if owner != TailOwnerLiveTurn {
		t.Fatalf("owner = %q, want %q", owner, TailOwnerLiveTurn)
	}
}
