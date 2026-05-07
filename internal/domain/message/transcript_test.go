package message

import (
	"testing"
	"time"
)

func TestTranscriptStartsEmpty(t *testing.T) {
	transcript := NewTranscript()

	if transcript.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", transcript.Len())
	}
	if tail := transcript.Tail(); tail.Owner() != TailOwnerTranscript || tail.Seq() != 0 {
		t.Fatalf("Tail() = %+v, want committed seq 0", tail)
	}
	if _, ok := transcript.PartialAssistantTurn(); ok {
		t.Fatal("PartialAssistantTurn() ok = true, want false")
	}
	page := transcript.History(nil, 10)
	if len(page.Items()) != 0 {
		t.Fatalf("len(History().Items()) = %d, want 0", len(page.Items()))
	}
	if _, ok := page.NextBefore(); ok {
		t.Fatal("History().NextBefore() ok = true, want false")
	}
	if page.HasMore() {
		t.Fatal("History().HasMore() = true, want false")
	}
}

func TestTranscriptHistoryAssignsSeqAndPagesOldestToNewest(t *testing.T) {
	base := time.Unix(1760000000, 0).UTC()
	transcript := NewTranscript()
	if _, err := transcript.AppendMessage(RoleUser.String(), KindMessage.String(), "first", base); err != nil {
		t.Fatalf("AppendMessage(first) error = %v", err)
	}
	second, err := transcript.AppendMessage(RoleAssistant.String(), KindMessage.String(), "second", base.Add(time.Second))
	if err != nil {
		t.Fatalf("AppendMessage(second) error = %v", err)
	}
	third, err := transcript.AppendMessage(RoleAssistant.String(), KindMessage.String(), "third", base.Add(2*time.Second))
	if err != nil {
		t.Fatalf("AppendMessage(third) error = %v", err)
	}

	if second.Seq() != 2 || third.Seq() != 3 {
		t.Fatalf("seqs = (%d, %d), want (2, 3)", second.Seq(), third.Seq())
	}
	if tail := transcript.Tail(); tail.Owner() != TailOwnerTranscript || tail.Seq() != 3 {
		t.Fatalf("Tail() = %+v, want committed seq 3", tail)
	}

	page := transcript.History(nil, 2)
	items := page.Items()
	if len(items) != 2 {
		t.Fatalf("len(History(nil, 2).Items()) = %d, want 2", len(items))
	}
	if items[0].Seq() != 2 || items[1].Seq() != 3 {
		t.Fatalf("page seqs = (%d, %d), want (2, 3)", items[0].Seq(), items[1].Seq())
	}
	if !page.HasMore() {
		t.Fatal("History(nil, 2).HasMore() = false, want true")
	}
	nextBefore, ok := page.NextBefore()
	if !ok || nextBefore != 2 {
		t.Fatalf("History(nil, 2).NextBefore() = (%d, %v), want (2, true)", nextBefore, ok)
	}

	before := Seq(2)
	older := transcript.History(&before, 2)
	olderItems := older.Items()
	if len(olderItems) != 1 || olderItems[0].Seq() != 1 {
		t.Fatalf("older items = %+v, want seq 1", olderItems)
	}
	if older.HasMore() {
		t.Fatal("History(before=2).HasMore() = true, want false")
	}
}

func TestTranscriptHistoryAfterReturnsOnlyNewerItems(t *testing.T) {
	base := time.Unix(1760000000, 0).UTC()
	transcript := NewTranscript()
	for _, text := range []string{"first", "second", "third"} {
		if _, err := transcript.AppendMessage(RoleUser.String(), KindMessage.String(), text, base); err != nil {
			t.Fatalf("AppendMessage(%q) error = %v", text, err)
		}
	}

	page := transcript.HistoryAfter(1)
	items := page.Items()
	if len(items) != 2 || items[0].Seq() != 2 || items[1].Seq() != 3 {
		t.Fatalf("HistoryAfter(1).Items() = %+v, want seq 2 and 3", items)
	}
	if page.HasMore() {
		t.Fatal("HistoryAfter(1).HasMore() = true, want false")
	}
	if _, ok := page.NextBefore(); ok {
		t.Fatal("HistoryAfter(1).NextBefore() ok = true, want false")
	}

	if items := transcript.HistoryAfter(3).Items(); len(items) != 0 {
		t.Fatalf("HistoryAfter(3).Items() = %+v, want empty", items)
	}
}

func TestTranscriptAssistantDeltaOwnsTailUntilCommit(t *testing.T) {
	base := time.Unix(1760000000, 0).UTC()
	transcript := NewTranscript()
	if _, err := transcript.AppendMessage(RoleUser.String(), KindMessage.String(), "prompt", base); err != nil {
		t.Fatalf("AppendMessage(prompt) error = %v", err)
	}

	partial, err := transcript.AppendAssistantDelta("turn_1", "hel")
	if err != nil {
		t.Fatalf("AppendAssistantDelta(first) error = %v", err)
	}
	if partial.Text() != "hel" {
		t.Fatalf("partial.Text() = %q, want %q", partial.Text(), "hel")
	}
	partial, err = transcript.AppendAssistantDelta("turn_1", "lo")
	if err != nil {
		t.Fatalf("AppendAssistantDelta(second) error = %v", err)
	}
	if partial.Text() != "hello" {
		t.Fatalf("partial.Text() = %q, want %q", partial.Text(), "hello")
	}
	if tail := transcript.Tail(); !tail.Live() || tail.Seq() != 1 {
		t.Fatalf("Tail() = %+v, want live tail at seq 1", tail)
	}
	turnID, ok := transcript.Tail().TurnID()
	if !ok || turnID.String() != "turn_1" {
		t.Fatalf("Tail().TurnID() = (%q, %v), want (%q, true)", turnID, ok, "turn_1")
	}

	committed, err := transcript.CommitAssistantTurn("turn_1", "", base.Add(time.Second))
	if err != nil {
		t.Fatalf("CommitAssistantTurn() error = %v", err)
	}
	if committed.Seq() != 2 {
		t.Fatalf("committed.Seq() = %d, want 2", committed.Seq())
	}
	if committed.Text() != "hello" {
		t.Fatalf("committed.Text() = %q, want %q", committed.Text(), "hello")
	}
	if _, ok := transcript.PartialAssistantTurn(); ok {
		t.Fatal("PartialAssistantTurn() ok = true after commit, want false")
	}
	if tail := transcript.Tail(); tail.Live() || tail.Seq() != 2 {
		t.Fatalf("Tail() after commit = %+v, want committed seq 2", tail)
	}
}

func TestTranscriptCommitCanOverrideBufferedAssistantText(t *testing.T) {
	transcript := NewTranscript()
	if _, err := transcript.AppendAssistantDelta("turn_1", "draft"); err != nil {
		t.Fatalf("AppendAssistantDelta() error = %v", err)
	}
	item, err := transcript.CommitAssistantTurn("turn_1", "final", time.Unix(1760000001, 0).UTC())
	if err != nil {
		t.Fatalf("CommitAssistantTurn() error = %v", err)
	}
	if item.Text() != "final" {
		t.Fatalf("item.Text() = %q, want %q", item.Text(), "final")
	}
}

func TestTranscriptRejectsConflictingAssistantTurnsAndOutOfOrderDurableAppend(t *testing.T) {
	transcript := NewTranscript()
	if _, err := transcript.AppendAssistantDelta("turn_1", "hel"); err != nil {
		t.Fatalf("AppendAssistantDelta() error = %v", err)
	}
	if _, err := transcript.AppendAssistantDelta("turn_2", "x"); err == nil {
		t.Fatal("AppendAssistantDelta(conflict) error = nil, want error")
	}
	if _, err := transcript.AppendMessage(RoleUser.String(), KindMessage.String(), "prompt", time.Unix(1760000000, 0).UTC()); err == nil {
		t.Fatal("AppendMessage() with live partial error = nil, want error")
	}
	if _, err := transcript.CommitAssistantTurn("turn_2", "final", time.Unix(1760000001, 0).UTC()); err == nil {
		t.Fatal("CommitAssistantTurn(wrong turn) error = nil, want error")
	}
}
