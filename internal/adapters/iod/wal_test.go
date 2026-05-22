package iod

import (
	"encoding/json"
	"os"
	"path/filepath"
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
			WALRecordCommandRejected.String(),
			WALRecordOutputDelta.String(),
			WALRecordChildExit.String(),
			WALRecordHelperExit.String(),
			WALRecordGenerationBreak.String(),
		}
		want := []string{
			"helper_start",
			"attach_established",
			"command_accepted",
			"command_rejected",
			"output_delta",
			"child_exit",
			"helper_exit",
			"generation_break",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wal record classes = %#v, want %#v", got, want)
		}
		for _, raw := range want {
			class, err := ParseWALRecordClass(raw)
			if err != nil {
				t.Fatalf("ParseWALRecordClass(%q) error = %v", raw, err)
			}
			if class.String() != raw {
				t.Fatalf("ParseWALRecordClass(%q) = %q, want %q", raw, class, raw)
			}
		}
		for _, raw := range []string{"helper", "generation-break"} {
			if _, err := ParseWALRecordClass(raw); err == nil {
				t.Fatalf("ParseWALRecordClass(%q) error = nil, want error", raw)
			}
		}
	})

	t.Run("header shape and seq rules stay stable", func(t *testing.T) {
		helperStart, err := NewWALRecordHeader(sessionID, generationID, 1, WALRecordHelperStart, nil, 17)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(helper_start) error = %v", err)
		}
		record, err := NewWALRecord(helperStart, json.RawMessage(`{"pid":123}`))
		if err != nil {
			t.Fatalf("NewWALRecord() error = %v", err)
		}
		assertKeys(t, record, []string{"header", "payload"})
		assertKeys(t, helperStart, []string{"checksum", "class", "generation_id", "offset", "session_id"})

		seq := EventSeq(2)
		browserEvent, err := NewWALRecordHeader(sessionID, generationID, 2, WALRecordOutputDelta, &seq, 23)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(output_delta) error = %v", err)
		}
		assertKeys(t, browserEvent, []string{"checksum", "class", "generation_id", "offset", "seq", "session_id"})

		childExit, err := NewWALRecordHeader(sessionID, generationID, 3, WALRecordChildExit, nil, 29)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(child_exit) error = %v", err)
		}
		assertKeys(t, childExit, []string{"checksum", "class", "generation_id", "offset", "session_id"})

		if _, err := NewWALRecordHeader(sessionID, generationID, 4, WALRecordOutputDelta, nil, 31); err == nil {
			t.Fatal("NewWALRecordHeader(output_delta without seq) error = nil, want error")
		}
		if _, err := NewWALRecordHeader(sessionID, generationID, 5, WALRecordChildExit, &seq, 37); err == nil {
			t.Fatal("NewWALRecordHeader(child_exit with seq) error = nil, want error")
		}
		if _, err := NewWALRecordHeader(sessionID, generationID, 6, WALRecordHelperStart, &seq, 41); err == nil {
			t.Fatal("NewWALRecordHeader(helper_start with seq) error = nil, want error")
		}
	})

	t.Run("projection boundaries stay stable", func(t *testing.T) {
		got := map[WALRecordClass]ProjectionBoundary{
			WALRecordHelperStart:       WALRecordHelperStart.ProjectionBoundary(),
			WALRecordAttachEstablished: WALRecordAttachEstablished.ProjectionBoundary(),
			WALRecordCommandAccepted:   WALRecordCommandAccepted.ProjectionBoundary(),
			WALRecordCommandRejected:   WALRecordCommandRejected.ProjectionBoundary(),
			WALRecordOutputDelta:       WALRecordOutputDelta.ProjectionBoundary(),
			WALRecordChildExit:         WALRecordChildExit.ProjectionBoundary(),
			WALRecordHelperExit:        WALRecordHelperExit.ProjectionBoundary(),
			WALRecordGenerationBreak:   WALRecordGenerationBreak.ProjectionBoundary(),
		}
		want := map[WALRecordClass]ProjectionBoundary{
			WALRecordHelperStart:       ProjectionBoundaryStateOnly,
			WALRecordAttachEstablished: ProjectionBoundaryStateOnly,
			WALRecordCommandAccepted:   ProjectionBoundaryStateOnly,
			WALRecordCommandRejected:   ProjectionBoundaryStateOnly,
			WALRecordOutputDelta:       ProjectionBoundaryBrowserEvent,
			WALRecordChildExit:         ProjectionBoundaryStateOnly,
			WALRecordHelperExit:        ProjectionBoundaryStateOnly,
			WALRecordGenerationBreak:   ProjectionBoundaryGenerationTerminal,
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

	t.Run("child exit helper exit and generation break stay distinct", func(t *testing.T) {
		childExit, err := ParseWALRecordClass("child_exit")
		if err != nil {
			t.Fatalf("ParseWALRecordClass(child_exit) error = %v", err)
		}
		helperExit, err := ParseWALRecordClass("helper_exit")
		if err != nil {
			t.Fatalf("ParseWALRecordClass(helper_exit) error = %v", err)
		}
		generationBreak, err := ParseWALRecordClass("generation_break")
		if err != nil {
			t.Fatalf("ParseWALRecordClass(generation_break) error = %v", err)
		}
		if childExit == helperExit || childExit == generationBreak || helperExit == generationBreak {
			t.Fatalf("distinct terminal facts collapsed: child=%q helper=%q break=%q", childExit, helperExit, generationBreak)
		}
	})
}

func TestWalReplay(t *testing.T) {
	sessionID := mustSessionID(t, "s_123")
	generationID := mustGenerationID(t, "g_7")

	t.Run("replay cursor advances only by append order", func(t *testing.T) {
		replay, err := NewReplayCursor(sessionID, generationID, 0)
		if err != nil {
			t.Fatalf("NewReplayCursor() error = %v", err)
		}
		helperStart, err := NewWALRecordHeader(sessionID, generationID, 1, WALRecordHelperStart, nil, 41)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(helper_start) error = %v", err)
		}
		seq := EventSeq(1)
		outputDelta, err := NewWALRecordHeader(sessionID, generationID, 2, WALRecordOutputDelta, &seq, 43)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(output_delta) error = %v", err)
		}
		commandRejected, err := NewWALRecordHeader(sessionID, generationID, 3, WALRecordCommandRejected, nil, 47)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(command_rejected) error = %v", err)
		}
		for _, record := range []WALRecordHeader{helperStart, outputDelta, commandRejected} {
			replay, err = replay.Advance(record)
			if err != nil {
				t.Fatalf("Advance(offset=%d) error = %v", record.Offset, err)
			}
		}
		if replay.AfterOffset != 3 {
			t.Fatalf("replay.AfterOffset = %d, want 3", replay.AfterOffset)
		}
		gap, err := NewWALRecordHeader(sessionID, generationID, 5, WALRecordHelperExit, nil, 53)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(gap) error = %v", err)
		}
		if _, err := replay.Advance(gap); err == nil {
			t.Fatal("Advance(gap offset) error = nil, want error")
		}
	})

	t.Run("replay done freezes corruption framing", func(t *testing.T) {
		done, err := NewReplayDonePacket(sessionID, generationID, 2, 4, true)
		if err != nil {
			t.Fatalf("NewReplayDonePacket(corrupt_tail=true) error = %v", err)
		}
		assertKeys(t, done, []string{"after_offset", "corrupt_tail", "generation_id", "kind", "last_offset", "session_id"})
		clean, err := NewReplayDonePacket(sessionID, generationID, 2, 4, false)
		if err != nil {
			t.Fatalf("NewReplayDonePacket(corrupt_tail=false) error = %v", err)
		}
		assertKeys(t, clean, []string{"after_offset", "generation_id", "kind", "last_offset", "session_id"})
		if _, err := NewReplayDonePacket(sessionID, generationID, 5, 4, false); err == nil {
			t.Fatal("NewReplayDonePacket(last_offset<after_offset) error = nil, want error")
		}
	})
}

func TestWALSummaryFastPaths(t *testing.T) {
	sessionID := mustSessionID(t, "s_wal_summary")
	generationID := mustGenerationID(t, "g_wal_summary")
	path := filepath.Join(t.TempDir(), "transport.wal")
	wal, err := OpenWAL(path, sessionID, generationID)
	if err != nil {
		t.Fatalf("OpenWAL() error = %v", err)
	}
	if _, err := wal.Append(WALRecordHelperStart, helperStartPayload{ProtocolVersion: 1, HelperPID: 101, ChildPID: intPtr(202), WALPath: path, SocketPath: "io", StartTS: 1760000000.0}); err != nil {
		t.Fatalf("Append(helper_start) error = %v", err)
	}
	if _, err := wal.Append(WALRecordOutputDelta, terminalOutputPayload{Stream: "pty", Data: "one"}); err != nil {
		t.Fatalf("Append(output_delta one) error = %v", err)
	}
	if _, err := wal.Append(WALRecordCommandAccepted, commandFactPayload{CommandID: mustCommandID(t, "cmd_1"), CommandKind: PacketCommandSend}); err != nil {
		t.Fatalf("Append(command_accepted) error = %v", err)
	}
	if _, err := wal.Append(WALRecordOutputDelta, terminalOutputPayload{Stream: "pty", Data: "two"}); err != nil {
		t.Fatalf("Append(output_delta two) error = %v", err)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	summary, err := SummarizeWAL(path, sessionID, generationID)
	if err != nil {
		t.Fatalf("SummarizeWAL() error = %v", err)
	}
	if summary.LastOffset != 4 || summary.LastSeq != 2 || summary.CorruptTail {
		t.Fatalf("SummarizeWAL() = %+v, want last offset 4 last seq 2 clean", summary)
	}

	reopened, err := OpenWAL(path, sessionID, generationID)
	if err != nil {
		t.Fatalf("OpenWAL(reopen) error = %v", err)
	}
	record, err := reopened.Append(WALRecordOutputDelta, terminalOutputPayload{Stream: "pty", Data: "three"})
	if err != nil {
		t.Fatalf("Append(after reopen) error = %v", err)
	}
	if record.Header.Offset != 5 || record.Header.Seq == nil || *record.Header.Seq != 3 {
		t.Fatalf("append after reopen header = %+v, want offset 5 seq 3", record.Header)
	}
	liveReplay, err := reopened.Replay(5)
	if err != nil {
		t.Fatalf("WAL.Replay(after live tail) error = %v", err)
	}
	if liveReplay.LastOffset != 5 || len(liveReplay.Records) != 0 || liveReplay.CorruptTail {
		t.Fatalf("WAL.Replay(after live tail) = %+v, want empty replay at last offset 5", liveReplay)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close(reopened) error = %v", err)
	}

	replay, err := ReplayWAL(path, sessionID, generationID, 5)
	if err != nil {
		t.Fatalf("ReplayWAL(after tail) error = %v", err)
	}
	if replay.LastOffset != 5 || len(replay.Records) != 0 || replay.CorruptTail {
		t.Fatalf("ReplayWAL(after tail) = %+v, want empty replay at last offset 5", replay)
	}
}

func TestReplayWALFastPathPreservesCorruptTail(t *testing.T) {
	sessionID := mustSessionID(t, "s_wal_corrupt_tail")
	generationID := mustGenerationID(t, "g_wal_corrupt_tail")
	path := filepath.Join(t.TempDir(), "transport.wal")
	wal, err := OpenWAL(path, sessionID, generationID)
	if err != nil {
		t.Fatalf("OpenWAL() error = %v", err)
	}
	if _, err := wal.Append(WALRecordHelperStart, helperStartPayload{ProtocolVersion: 1, HelperPID: 101, ChildPID: intPtr(202), WALPath: path, SocketPath: "io", StartTS: 1760000000.0}); err != nil {
		t.Fatalf("Append(helper_start) error = %v", err)
	}
	if _, err := wal.Append(WALRecordOutputDelta, terminalOutputPayload{Stream: "pty", Data: "one"}); err != nil {
		t.Fatalf("Append(output_delta) error = %v", err)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("OpenFile(append corrupt tail) error = %v", err)
	}
	if _, err := file.WriteString("{not-json"); err != nil {
		t.Fatalf("WriteString(corrupt tail) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(corrupt tail) error = %v", err)
	}
	replay, err := ReplayWAL(path, sessionID, generationID, 2)
	if err != nil {
		t.Fatalf("ReplayWAL(after tail with corrupt suffix) error = %v", err)
	}
	if replay.LastOffset != 2 || len(replay.Records) != 0 || !replay.CorruptTail {
		t.Fatalf("ReplayWAL(after tail with corrupt suffix) = %+v, want empty replay with corrupt tail", replay)
	}
}

func TestGenerationBreak(t *testing.T) {
	sessionID := mustSessionID(t, "s_123")
	generationID := mustGenerationID(t, "g_7")

	t.Run("reasons stay stable and exact", func(t *testing.T) {
		got := []string{
			GenerationBreakHelperExit.String(),
			GenerationBreakAttachLost.String(),
			GenerationBreakWriteFailed.String(),
			GenerationBreakReplayGap.String(),
		}
		want := []string{
			"helper_exit",
			"attach_lost",
			"write_failed",
			"replay_gap",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("generation break reasons = %#v, want %#v", got, want)
		}
		for _, raw := range want {
			reason, err := ParseGenerationBreakReason(raw)
			if err != nil {
				t.Fatalf("ParseGenerationBreakReason(%q) error = %v", raw, err)
			}
			if reason.String() != raw {
				t.Fatalf("ParseGenerationBreakReason(%q) = %q, want %q", raw, reason, raw)
			}
		}
		for _, raw := range []string{"helper", "replay-gap"} {
			if _, err := ParseGenerationBreakReason(raw); err == nil {
				t.Fatalf("ParseGenerationBreakReason(%q) error = nil, want error", raw)
			}
		}
	})

	t.Run("live packet and replay item stay distinct", func(t *testing.T) {
		seq := EventSeq(7)
		packet, err := NewGenerationBreakPacket(sessionID, generationID, seq, GenerationBreakHelperExit)
		if err != nil {
			t.Fatalf("NewGenerationBreakPacket() error = %v", err)
		}
		assertKeys(t, packet, []string{"generation_id", "kind", "reason", "seq", "session_id"})

		fact, err := NewHelperFact(FactGenerationBreak, &seq, json.RawMessage(`{"reason":"helper_exit"}`))
		if err != nil {
			t.Fatalf("NewHelperFact(generation_break) error = %v", err)
		}
		item, err := NewReplayItem(11, fact)
		if err != nil {
			t.Fatalf("NewReplayItem(generation_break) error = %v", err)
		}
		assertKeys(t, item, []string{"fact", "wal_offset"})
		assertNestedKeys(t, item, "fact", []string{"fact_kind", "payload", "seq"})
		if _, err := NewStatePacket(sessionID, generationID, fact); err == nil {
			t.Fatal("NewStatePacket(generation_break) error = nil, want error")
		}
	})

	t.Run("projection becomes terminal at generation break", func(t *testing.T) {
		projection, err := NewProjectionCursor(sessionID, generationID, 0)
		if err != nil {
			t.Fatalf("NewProjectionCursor() error = %v", err)
		}
		helperStart, err := NewWALRecordHeader(sessionID, generationID, 1, WALRecordHelperStart, nil, 41)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(helper_start) error = %v", err)
		}
		projection, err = projection.Advance(helperStart)
		if err != nil {
			t.Fatalf("Advance(helper_start) error = %v", err)
		}
		breakSeq := EventSeq(2)
		generationBreak, err := NewWALRecordHeader(sessionID, generationID, 2, WALRecordGenerationBreak, &breakSeq, 47)
		if err != nil {
			t.Fatalf("NewWALRecordHeader(generation_break) error = %v", err)
		}
		projection, err = projection.Advance(generationBreak)
		if err != nil {
			t.Fatalf("Advance(generation_break) error = %v", err)
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
			t.Fatal("Advance(after generation break) error = nil, want error")
		}
	})
}
