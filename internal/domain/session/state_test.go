package session

import (
	"testing"

	"actrail/internal/domain/message"
)

func TestParseQueueItemStateNormalizesWhitespace(t *testing.T) {
	state, err := ParseQueueItemState("  QUEUED  ")
	if err != nil {
		t.Fatalf("ParseQueueItemState() error = %v", err)
	}
	if state != QueueItemStateQueued {
		t.Fatalf("state = %q, want %q", state, QueueItemStateQueued)
	}
}

func TestNewQueueSnapshotRejectsDuplicateIDs(t *testing.T) {
	first, err := NewQueueItem("q_1", "next request", QueueItemStateQueued)
	if err != nil {
		t.Fatalf("NewQueueItem() error = %v", err)
	}
	second, err := NewQueueItem("q_1", "another request", QueueItemStateQueued)
	if err != nil {
		t.Fatalf("NewQueueItem() error = %v", err)
	}
	if _, err := NewQueueSnapshot([]QueueItem{first, second}); err == nil {
		t.Fatal("NewQueueSnapshot() error = nil, want error")
	}
}

func TestNewStateRejectsHistoricalBusyQueueAndLiveTail(t *testing.T) {
	identity, err := NewHistoricalIdentity("resume-1", "pi")
	if err != nil {
		t.Fatalf("NewHistoricalIdentity() error = %v", err)
	}
	queued, err := NewQueueItem("q_1", "next request", QueueItemStateQueued)
	if err != nil {
		t.Fatalf("NewQueueItem() error = %v", err)
	}
	queue, err := NewQueueSnapshot([]QueueItem{queued})
	if err != nil {
		t.Fatalf("NewQueueSnapshot() error = %v", err)
	}
	liveTail, err := message.NewLiveTail(181, "turn_9")
	if err != nil {
		t.Fatalf("NewLiveTail() error = %v", err)
	}
	if _, err := NewState(identity, false, queue, message.NewCommittedTail(180)); err == nil {
		t.Fatal("NewState() with historical queue error = nil, want error")
	}
	if _, err := NewState(identity, true, EmptyQueueSnapshot(), message.NewCommittedTail(180)); err == nil {
		t.Fatal("NewState() with historical busy error = nil, want error")
	}
	if _, err := NewState(identity, false, EmptyQueueSnapshot(), liveTail); err == nil {
		t.Fatal("NewState() with historical live tail error = nil, want error")
	}
}

func TestNewStateRequiresBusyForLiveTail(t *testing.T) {
	identity, err := NewLiveIdentity("s_123", "r_123", "t_123", "pi")
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	liveTail, err := message.NewLiveTail(181, "turn_9")
	if err != nil {
		t.Fatalf("NewLiveTail() error = %v", err)
	}
	if _, err := NewState(identity, false, EmptyQueueSnapshot(), liveTail); err == nil {
		t.Fatal("NewState() error = nil, want error")
	}
}

func TestNewStateAcceptsLiveSnapshotWithReplaceableQueue(t *testing.T) {
	identity, err := NewLiveIdentity("s_123", "r_123", "t_123", "pi")
	if err != nil {
		t.Fatalf("NewLiveIdentity() error = %v", err)
	}
	queued, err := NewQueueItem("q_1", "next request", QueueItemStateQueued)
	if err != nil {
		t.Fatalf("NewQueueItem() error = %v", err)
	}
	queue, err := NewQueueSnapshot([]QueueItem{queued})
	if err != nil {
		t.Fatalf("NewQueueSnapshot() error = %v", err)
	}
	state, err := NewState(identity, true, queue, message.NewCommittedTail(180))
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}
	items := state.Queue().Items()
	if len(items) != 1 {
		t.Fatalf("len(Items()) = %d, want 1", len(items))
	}
	if !items[0].Replaceable() {
		t.Fatal("Replaceable() = false, want true")
	}
}
