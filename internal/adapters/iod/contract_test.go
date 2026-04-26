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

	t.Run("packet kinds stay stable", func(t *testing.T) {
		got := []string{
			PacketHello.String(),
			PacketState.String(),
			CommandSend.Kind().String(),
			CommandEnqueue.Kind().String(),
			CommandInterrupt.Kind().String(),
			CommandUIResponseSubmit.Kind().String(),
			EventOutputDelta.Kind().String(),
			EventTurnCommit.Kind().String(),
			EventUIRequestOpened.Kind().String(),
			EventUIResponseForwarded.Kind().String(),
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
			"iod.event.output.delta",
			"iod.event.turn.commit",
			"iod.event.ui_request.opened",
			"iod.event.ui_response.forwarded",
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

	t.Run("envelope shapes stay stable", func(t *testing.T) {
		hello, err := NewHelloPacket(sessionID, generationID, 1)
		if err != nil {
			t.Fatalf("NewHelloPacket() error = %v", err)
		}
		assertKeys(t, hello, []string{"generation_id", "kind", "protocol_version", "session_id"})

		state, err := NewStatePacket(sessionID, generationID, TransportStateAttached, 4, 3)
		if err != nil {
			t.Fatalf("NewStatePacket() error = %v", err)
		}
		assertKeys(t, state, []string{"generation_id", "kind", "last_offset", "last_seq", "session_id", "transport_state"})

		command, err := NewCommandPacket(sessionID, generationID, CommandSend, commandID, json.RawMessage(`{"text":"stack trace"}`))
		if err != nil {
			t.Fatalf("NewCommandPacket() error = %v", err)
		}
		assertKeys(t, command, []string{"command_id", "generation_id", "kind", "payload", "session_id"})

		event, err := NewEventPacket(sessionID, generationID, EventOutputDelta, 5, json.RawMessage(`{"delta":"line 84 panics"}`))
		if err != nil {
			t.Fatalf("NewEventPacket() error = %v", err)
		}
		assertKeys(t, event, []string{"generation_id", "kind", "payload", "seq", "session_id"})

		replay, err := NewReplayRequestPacket(sessionID, generationID, 7)
		if err != nil {
			t.Fatalf("NewReplayRequestPacket() error = %v", err)
		}
		assertKeys(t, replay, []string{"after_offset", "generation_id", "kind", "session_id"})

		generationBreak, err := NewGenerationBreakPacket(sessionID, generationID, 8, GenerationBreakHelperExit)
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
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%T) error = %v", value, err)
	}
	var gotMap map[string]any
	if err := json.Unmarshal(body, &gotMap); err != nil {
		t.Fatalf("json.Unmarshal(%T) error = %v", value, err)
	}
	got := make([]string, 0, len(gotMap))
	for key := range gotMap {
		got = append(got, key)
	}
	if !reflect.DeepEqual(sortStrings(got), sortStrings(want)) {
		t.Fatalf("json keys = %#v, want %#v", sortStrings(got), sortStrings(want))
	}
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
