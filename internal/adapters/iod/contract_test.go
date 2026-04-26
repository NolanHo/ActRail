package iod

import (
	"encoding/json"
	"reflect"
	"testing"

	"actrail/internal/domain/session"
)

func TestIodContract(t *testing.T) {
	sessionID := mustSessionID(t, "s_123")
	generationID := mustGenerationID(t, "g_7")
	commandID := mustCommandID(t, "cmd_41")
	childPID := 456
	proof, err := NewHelloProof(123, &childPID, "/tmp/iod/g_7.wal", "/tmp/iod/g_7.sock", 1760000000.0)
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}

	t.Run("packet kinds stay stable", func(t *testing.T) {
		got := []string{
			PacketHello.String(),
			PacketState.String(),
			PacketCommandSend.String(),
			PacketCommandEnqueue.String(),
			PacketCommandInterrupt.String(),
			PacketCommandUIResponseSubmit.String(),
			PacketCommandAccepted.String(),
			PacketCommandRejected.String(),
			PacketReplayRequest.String(),
			PacketReplayItem.String(),
			PacketReplayDone.String(),
			PacketGenerationBreak.String(),
			PacketError.String(),
		}
		want := []string{
			"iod.hello",
			"iod.state",
			"iod.command.send",
			"iod.command.enqueue",
			"iod.command.interrupt",
			"iod.command.ui_response.submit",
			"iod.command.accepted",
			"iod.command.rejected",
			"iod.replay.request",
			"iod.replay.item",
			"iod.replay.done",
			"iod.generation.break",
			"iod.error",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("packet kinds = %#v, want %#v", got, want)
		}
	})

	t.Run("error codes stay stable", func(t *testing.T) {
		got := []string{
			ErrorGenerationNotCurrent.String(),
			ErrorMalformedEnvelope.String(),
			ErrorUnsupportedCommandKind.String(),
			ErrorReplayCursorInvalid.String(),
			ErrorReplayCorruptTail.String(),
			ErrorHelperBroken.String(),
		}
		want := []string{
			"generation_not_current",
			"malformed_envelope",
			"unsupported_command_kind",
			"replay_cursor_invalid",
			"replay_corrupt_tail",
			"helper_broken",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("error codes = %#v, want %#v", got, want)
		}
		for _, raw := range want {
			code, err := ParseErrorCode(raw)
			if err != nil {
				t.Fatalf("ParseErrorCode(%q) error = %v", raw, err)
			}
			if code.String() != raw {
				t.Fatalf("ParseErrorCode(%q) = %q, want %q", raw, code, raw)
			}
		}
		for _, raw := range []string{"malformed", "unsupported_command", "helper-broken"} {
			if _, err := ParseErrorCode(raw); err == nil {
				t.Fatalf("ParseErrorCode(%q) error = nil, want error", raw)
			}
		}
	})

	t.Run("parsing rejects unfrozen suffixes", func(t *testing.T) {
		badKinds := []string{
			"iod.command.future",
			"iod.command.send.more",
			"iod.event.output.delta",
			"iod.event.future",
		}
		for _, raw := range badKinds {
			if _, err := ParsePacketKind(raw); err == nil {
				t.Fatalf("ParsePacketKind(%q) error = nil, want error", raw)
			}
		}
		if _, err := ParseCommandName("accepted"); err == nil {
			t.Fatal("ParseCommandName(accepted) error = nil, want error")
		}
		if _, err := ParseCommandName("future"); err == nil {
			t.Fatal("ParseCommandName(future) error = nil, want error")
		}
	})

	t.Run("envelope shapes stay stable", func(t *testing.T) {
		hello, err := NewHelloPacket(sessionID, generationID, 1, proof)
		if err != nil {
			t.Fatalf("NewHelloPacket() error = %v", err)
		}
		assertKeys(t, hello, []string{"child_pid", "control_socket_path", "generation_id", "helper_pid", "kind", "protocol_version", "session_id", "start_ts", "wal_path"})

		manifest, err := NewGenerationManifest(sessionID, generationID, proof)
		if err != nil {
			t.Fatalf("NewGenerationManifest() error = %v", err)
		}
		assertKeys(t, manifest, []string{"child_pid", "control_socket_path", "generation_id", "helper_pid", "session_id", "start_ts", "wal_path"})

		seq := EventSeq(3)
		stateFact, err := NewHelperFact(FactOutputDelta, &seq, json.RawMessage(`{"delta":"line 84 panics"}`))
		if err != nil {
			t.Fatalf("NewHelperFact(output_delta) error = %v", err)
		}
		state, err := NewStatePacket(sessionID, generationID, stateFact)
		if err != nil {
			t.Fatalf("NewStatePacket() error = %v", err)
		}
		assertKeys(t, state, []string{"fact", "generation_id", "kind", "session_id"})
		assertNestedKeys(t, state, "fact", []string{"fact_kind", "payload", "seq"})

		command, err := NewCommandPacket(sessionID, generationID, CommandSend, commandID, json.RawMessage(`{"text":"stack trace"}`))
		if err != nil {
			t.Fatalf("NewCommandPacket() error = %v", err)
		}
		assertKeys(t, command, []string{"command_id", "generation_id", "kind", "payload", "session_id"})

		outcome, err := NewCommandOutcome(commandID, 912, false, nil)
		if err != nil {
			t.Fatalf("NewCommandOutcome() error = %v", err)
		}
		accepted, err := NewCommandAcceptedPacket(sessionID, generationID, outcome)
		if err != nil {
			t.Fatalf("NewCommandAcceptedPacket() error = %v", err)
		}
		assertKeys(t, accepted, []string{"ack_cursor", "command_id", "deduped", "generation_id", "kind", "session_id"})

		rejected, err := NewCommandRejectedPacket(sessionID, generationID, outcome)
		if err != nil {
			t.Fatalf("NewCommandRejectedPacket() error = %v", err)
		}
		assertKeys(t, rejected, []string{"ack_cursor", "command_id", "deduped", "generation_id", "kind", "session_id"})

		replay, err := NewReplayRequestPacket(sessionID, generationID, 7)
		if err != nil {
			t.Fatalf("NewReplayRequestPacket() error = %v", err)
		}
		assertKeys(t, replay, []string{"after_offset", "generation_id", "kind", "session_id"})

		replayItem, err := NewReplayItem(8, stateFact)
		if err != nil {
			t.Fatalf("NewReplayItem() error = %v", err)
		}
		replayPacket, err := NewReplayItemPacket(sessionID, generationID, replayItem)
		if err != nil {
			t.Fatalf("NewReplayItemPacket() error = %v", err)
		}
		assertKeys(t, replayPacket, []string{"generation_id", "item", "kind", "session_id"})
		assertNestedKeys(t, replayPacket, "item", []string{"fact", "wal_offset"})
		assertNestedKeys(t, replayPacket, "item.fact", []string{"fact_kind", "payload", "seq"})

		generationBreak, err := NewGenerationBreakPacket(sessionID, generationID, 9, GenerationBreakHelperExit)
		if err != nil {
			t.Fatalf("NewGenerationBreakPacket() error = %v", err)
		}
		assertKeys(t, generationBreak, []string{"generation_id", "kind", "reason", "seq", "session_id"})

		errPacket, err := NewErrorPacket(sessionID, generationID, false, ErrorGenerationNotCurrent, "stale generation", &commandID)
		if err != nil {
			t.Fatalf("NewErrorPacket() error = %v", err)
		}
		assertKeys(t, errPacket, []string{"code", "command_id", "generation_id", "kind", "message", "recoverable", "session_id"})
	})

	t.Run("replay items carry wal offset and optional seq only for seq-bearing facts", func(t *testing.T) {
		seq := EventSeq(5)
		outputFact, err := NewHelperFact(FactOutputDelta, &seq, nil)
		if err != nil {
			t.Fatalf("NewHelperFact(output_delta) error = %v", err)
		}
		item, err := NewReplayItem(21, outputFact)
		if err != nil {
			t.Fatalf("NewReplayItem(output_delta) error = %v", err)
		}
		assertNestedKeys(t, item, "fact", []string{"fact_kind", "seq"})

		childExitFact, err := NewHelperFact(FactChildExit, nil, json.RawMessage(`{"status":0}`))
		if err != nil {
			t.Fatalf("NewHelperFact(child_exit) error = %v", err)
		}
		childExitItem, err := NewReplayItem(22, childExitFact)
		if err != nil {
			t.Fatalf("NewReplayItem(child_exit) error = %v", err)
		}
		assertNestedKeys(t, childExitItem, "fact", []string{"fact_kind", "payload"})
		if _, err := NewHelperFact(FactChildExit, &seq, nil); err == nil {
			t.Fatal("NewHelperFact(child_exit with seq) error = nil, want error")
		}
	})

	t.Run("command outcomes pin ack cursor to one durable wal offset", func(t *testing.T) {
		acceptedOutcome, err := NewCommandOutcome(commandID, 912, false, nil)
		if err != nil {
			t.Fatalf("NewCommandOutcome(accepted) error = %v", err)
		}
		dupAccepted, err := NewCommandOutcome(commandID, 912, true, nil)
		if err != nil {
			t.Fatalf("NewCommandOutcome(duplicate accepted) error = %v", err)
		}
		if acceptedOutcome.AckCursor != dupAccepted.AckCursor {
			t.Fatalf("duplicate ack cursor = %d, want %d", dupAccepted.AckCursor, acceptedOutcome.AckCursor)
		}
		rejectedOutcome, err := NewCommandOutcome(commandID, 913, false, nil)
		if err != nil {
			t.Fatalf("NewCommandOutcome(rejected) error = %v", err)
		}
		dupRejected, err := NewCommandOutcome(commandID, 913, true, nil)
		if err != nil {
			t.Fatalf("NewCommandOutcome(duplicate rejected) error = %v", err)
		}
		if rejectedOutcome.AckCursor != dupRejected.AckCursor {
			t.Fatalf("duplicate rejected ack cursor = %d, want %d", dupRejected.AckCursor, rejectedOutcome.AckCursor)
		}
		if _, err := NewCommandOutcome(commandID, 0, false, nil); err == nil {
			t.Fatal("NewCommandOutcome(ack_cursor=0) error = nil, want error")
		}
	})

	t.Run("state packet excludes command outcomes and generation break facts", func(t *testing.T) {
		badFacts := []FactKind{FactCommandAccepted, FactCommandRejected, FactGenerationBreak}
		for _, kind := range badFacts {
			fact, err := NewHelperFact(kind, nil, nil)
			if kind == FactGenerationBreak {
				seq := EventSeq(1)
				fact, err = NewHelperFact(kind, &seq, nil)
			}
			if err != nil {
				t.Fatalf("NewHelperFact(%q) error = %v", kind, err)
			}
			if _, err := NewStatePacket(sessionID, generationID, fact); err == nil {
				t.Fatalf("NewStatePacket(%q) error = nil, want error", kind)
			}
		}
	})

	t.Run("event seq stays scoped to one generation", func(t *testing.T) {
		cursor, err := NewEventCursor(sessionID, generationID, 0)
		if err != nil {
			t.Fatalf("NewEventCursor() error = %v", err)
		}
		cursor, err = cursor.Advance(sessionID, generationID, 1)
		if err != nil {
			t.Fatalf("Advance(seq=1) error = %v", err)
		}
		cursor, err = cursor.Advance(sessionID, generationID, 2)
		if err != nil {
			t.Fatalf("Advance(seq=2) error = %v", err)
		}
		if cursor.AfterSeq != 2 {
			t.Fatalf("cursor.AfterSeq = %d, want 2", cursor.AfterSeq)
		}
		if _, err := cursor.Advance(sessionID, generationID, 2); err == nil {
			t.Fatal("Advance(repeated seq) error = nil, want error")
		}
		otherGeneration := mustGenerationID(t, "g_8")
		if _, err := cursor.Advance(sessionID, otherGeneration, 3); err == nil {
			t.Fatal("Advance(other generation) error = nil, want error")
		}
	})
}

func mustSessionID(t *testing.T, raw string) session.SessionID {
	t.Helper()
	id, err := session.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q) error = %v", raw, err)
	}
	return id
}

func mustGenerationID(t *testing.T, raw string) GenerationID {
	t.Helper()
	id, err := NewGenerationID(raw)
	if err != nil {
		t.Fatalf("NewGenerationID(%q) error = %v", raw, err)
	}
	return id
}

func mustCommandID(t *testing.T, raw string) CommandID {
	t.Helper()
	id, err := NewCommandID(raw)
	if err != nil {
		t.Fatalf("NewCommandID(%q) error = %v", raw, err)
	}
	return id
}

func assertKeys(t *testing.T, value any, want []string) {
	t.Helper()
	gotMap := mustJSONMap(t, value)
	got := make([]string, 0, len(gotMap))
	for key := range gotMap {
		got = append(got, key)
	}
	if !reflect.DeepEqual(sortStrings(got), sortStrings(want)) {
		t.Fatalf("json keys = %#v, want %#v", sortStrings(got), sortStrings(want))
	}
}

func assertNestedKeys(t *testing.T, value any, path string, want []string) {
	t.Helper()
	current := any(mustJSONMap(t, value))
	for _, part := range stringsSplit(path, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("json path %q does not resolve to object", path)
		}
		next, ok := m[part]
		if !ok {
			t.Fatalf("json path %q missing key %q", path, part)
		}
		current = next
	}
	m, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("json path %q does not resolve to object", path)
	}
	got := make([]string, 0, len(m))
	for key := range m {
		got = append(got, key)
	}
	if !reflect.DeepEqual(sortStrings(got), sortStrings(want)) {
		t.Fatalf("json keys at %q = %#v, want %#v", path, sortStrings(got), sortStrings(want))
	}
}

func mustJSONMap(t *testing.T, value any) map[string]any {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%T) error = %v", value, err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("json.Unmarshal(%T) error = %v", value, err)
	}
	return got
}

func stringsSplit(value, sep string) []string {
	if value == "" {
		return []string{}
	}
	parts := []string{}
	start := 0
	for i := 0; i <= len(value)-len(sep); {
		if value[i:i+len(sep)] == sep {
			parts = append(parts, value[start:i])
			start = i + len(sep)
			i = start
			continue
		}
		i++
	}
	parts = append(parts, value[start:])
	return parts
}

func sortStrings(items []string) []string {
	copyItems := make([]string, len(items))
	copy(copyItems, items)
	for i := 0; i < len(copyItems); i++ {
		for j := i + 1; j < len(copyItems); j++ {
			if copyItems[j] < copyItems[i] {
				copyItems[i], copyItems[j] = copyItems[j], copyItems[i]
			}
		}
	}
	return copyItems
}
