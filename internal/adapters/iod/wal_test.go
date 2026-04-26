package iod

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWalSchema(t *testing.T) {
	sessionID := mustSessionID(t, "s_123")
	generationID := mustGenerationID(t, "g_7")

	t.Run("record classes stay stable", func(t *testing.T) {
		got := []string{
			WALRecordHelperStart.String(),
			WALRecordAttachEstablished.String(),
			WALRecordCommandAccepted.String(),
			WALRecordOutputDelta.String(),
			WALRecordTurnCommit.String(),
			WALRecordUIRequestOpened.String(),
			WALRecordUIResponseForwarded.String(),
			WALRecordHelperExit.String(),
			WALRecordGenerationBreak.String(),
		}
		want := []string{
			"helper_start",
			"pi_attach_established",
			"command_accepted",
			"output_delta",
			"turn_commit",
			"ui_request_opened",
			"ui_response_forwarded",
			"helper_exit",
			"generation_break",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wal record classes = %#v, want %#v", got, want)
		}
	})

	t.Run("header shape and seq rules stay stable", func(t *testing.T) {
		header, err := NewWALRecordHeader(sessionID, generationID, 1, WALRecordHelperStart, nil, 17)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(helper_start) error = %v", err)
		}
		record, err := NewWALRecord(header, json.RawMessage(`{"pid":123}`))
		if err != nil {
			t.Fatalf("NewWALRecord() error = %v", err)
		}
		assertKeys(t, record, []string{"header", "payload"})
		assertKeys(t, header, []string{"checksum", "class", "generation_id", "offset", "session_id"})

		seq := EventSeq(2)
		browserEvent, err := NewWALRecordHeader(sessionID, generationID, 2, WALRecordOutputDelta, &seq, 23)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(output_delta) error = %v", err)
		}
		assertKeys(t, browserEvent, []string{"checksum", "class", "generation_id", "offset", "seq", "session_id"})

		if _, err := NewWALRecordHeader(sessionID, generationID, 3, WALRecordOutputDelta, nil, 29); err == nil {
			t.Fatal("NewWALRecordHeader(output_delta without seq) error = nil, want error")
		}
		if _, err := NewWALRecordHeader(sessionID, generationID, 4, WALRecordHelperStart, &seq, 31); err == nil {
			t.Fatal("NewWALRecordHeader(helper_start with seq) error = nil, want error")
		}
	})

	t.Run("projection boundaries stay stable", func(t *testing.T) {
		got := map[WALRecordClass]ProjectionBoundary{
			WALRecordHelperStart:         WALRecordHelperStart.ProjectionBoundary(),
			WALRecordAttachEstablished:   WALRecordAttachEstablished.ProjectionBoundary(),
			WALRecordCommandAccepted:     WALRecordCommandAccepted.ProjectionBoundary(),
			WALRecordOutputDelta:         WALRecordOutputDelta.ProjectionBoundary(),
			WALRecordTurnCommit:          WALRecordTurnCommit.ProjectionBoundary(),
			WALRecordUIRequestOpened:     WALRecordUIRequestOpened.ProjectionBoundary(),
			WALRecordUIResponseForwarded: WALRecordUIResponseForwarded.ProjectionBoundary(),
			WALRecordHelperExit:          WALRecordHelperExit.ProjectionBoundary(),
			WALRecordGenerationBreak:     WALRecordGenerationBreak.ProjectionBoundary(),
		}
		want := map[WALRecordClass]ProjectionBoundary{
			WALRecordHelperStart:         ProjectionBoundaryStateOnly,
			WALRecordAttachEstablished:   ProjectionBoundaryStateOnly,
			WALRecordCommandAccepted:     ProjectionBoundaryStateOnly,
			WALRecordOutputDelta:         ProjectionBoundaryBrowserEvent,
			WALRecordTurnCommit:          ProjectionBoundaryBrowserEvent,
			WALRecordUIRequestOpened:     ProjectionBoundaryBrowserEvent,
			WALRecordUIResponseForwarded: ProjectionBoundaryBrowserEvent,
			WALRecordHelperExit:          ProjectionBoundaryStateOnly,
			WALRecordGenerationBreak:     ProjectionBoundaryGenerationTerminal,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("projection boundaries = %#v, want %#v", got, want)
		}
	})

	t.Run("replay and projection stop at generation break", func(t *testing.T) {
		replay, err := NewReplayCursor(sessionID, generationID, 0)
		if err != nil {
			t.Fatalf("NewReplayCursor() error = %v", err)
		}
		helperStart, err := NewWALRecordHeader(sessionID, generationID, 1, WALRecordHelperStart, nil, 41)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(helper_start) error = %v", err)
		}
		replay, err = replay.Advance(helperStart)
		if err != nil {
			t.Fatalf("Advance(helper_start) error = %v", err)
		}
		if replay.AfterOffset != 1 {
			t.Fatalf("replay.AfterOffset = %d, want 1", replay.AfterOffset)
		}
		if _, err := replay.Advance(helperStart); err == nil {
			t.Fatal("Advance(repeated offset) error = nil, want error")
		}
		otherGeneration := mustGenerationID(t, "g_8")
		otherRecord, err := NewWALRecordHeader(sessionID, otherGeneration, 2, WALRecordHelperStart, nil, 43)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(other generation) error = %v", err)
		}
		if _, err := replay.Advance(otherRecord); err == nil {
			t.Fatal("Advance(other generation) error = nil, want error")
		}

		projection, err := NewProjectionCursor(sessionID, generationID, 0)
		if err != nil {
			t.Fatalf("NewProjectionCursor() error = %v", err)
		}
		projection, err = projection.Advance(helperStart)
		if err != nil {
			t.Fatalf("Projection Advance(helper_start) error = %v", err)
		}
		breakSeq := EventSeq(2)
		generationBreak, err := NewWALRecordHeader(sessionID, generationID, 2, WALRecordGenerationBreak, &breakSeq, 47)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(generation_break) error = %v", err)
		}
		projection, err = projection.Advance(generationBreak)
		if err != nil {
			t.Fatalf("Projection Advance(generation_break) error = %v", err)
		}
		if !projection.Broken {
			t.Fatal("projection.Broken = false, want true")
		}
		nextSeq := EventSeq(3)
		lateRecord, err := NewWALRecordHeader(sessionID, generationID, 3, WALRecordOutputDelta, &nextSeq, 53)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(output_delta after break) error = %v", err)
		}
		if _, err := projection.Advance(lateRecord); err == nil {
			t.Fatal("Projection Advance(after generation break) error = nil, want error")
		}
	})
}
