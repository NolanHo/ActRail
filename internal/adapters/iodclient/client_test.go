package iodclient

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"actrail/internal/adapters/iod"
	"actrail/internal/domain/session"
)

func TestClientHelloReplayAndCommandPaths(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	sessionID := mustSessionID(t, "s_123")
	generationID := mustGenerationID(t, "g_7")
	commandID := mustCommandID(t, "cmd_41")
	proof := mustHelloProof(t, 1760000000)
	hello := mustHelloPacket(t, sessionID, generationID, proof)
	accepted := mustAcceptedPacket(t, sessionID, generationID, commandID, 9)
	replayItem := mustReplayItemPacket(t, sessionID, generationID, 6, 3)
	replayDone := mustReplayDonePacket(t, sessionID, generationID, 5, 6)

	go func() {
		enc := json.NewEncoder(serverConn)
		dec := json.NewDecoder(serverConn)
		if err := enc.Encode(hello); err != nil {
			return
		}
		var command iod.CommandPacket
		if err := dec.Decode(&command); err != nil {
			return
		}
		if command.Kind != iod.PacketCommandSend || command.CommandID != commandID {
			return
		}
		if err := enc.Encode(accepted); err != nil {
			return
		}
		var replayReq iod.ReplayRequestPacket
		if err := dec.Decode(&replayReq); err != nil {
			return
		}
		if replayReq.AfterOffset != 5 {
			return
		}
		if err := enc.Encode(replayItem); err != nil {
			return
		}
		_ = enc.Encode(replayDone)
	}()

	client := NewClient(clientConn)
	gotHello, err := client.Hello(context.Background())
	if err != nil {
		t.Fatalf("Hello() error = %v", err)
	}
	if err := VerifyHelloProof(mustManifest(t, sessionID, generationID, proof), gotHello); err != nil {
		t.Fatalf("VerifyHelloProof() error = %v", err)
	}
	command := mustCommandPacket(t, sessionID, generationID, commandID)
	result, err := client.Command(context.Background(), command)
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	if result.Accepted == nil || result.Accepted.AckCursor != 9 {
		t.Fatalf("Command() result = %+v, want accepted ack_cursor 9", result)
	}
	var replayOffsets []iod.WALOffset
	done, err := client.Replay(context.Background(), mustReplayRequestPacket(t, sessionID, generationID, 5), func(packet iod.ReplayItemPacket) error {
		replayOffsets = append(replayOffsets, packet.Item.WALOffset)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(replayOffsets) != 1 || replayOffsets[0] != 6 {
		t.Fatalf("replay offsets = %#v, want [6]", replayOffsets)
	}
	if done.LastOffset != 6 {
		t.Fatalf("Replay().LastOffset = %d, want 6", done.LastOffset)
	}
}

func TestVerifyHelloProofRejectsMismatch(t *testing.T) {
	sessionID := mustSessionID(t, "s_123")
	generationID := mustGenerationID(t, "g_7")
	proof := mustHelloProof(t, 1760000000)
	manifest := mustManifest(t, sessionID, generationID, proof)
	alteredProof := proof
	alteredProof.WALPath = "/tmp/iod/other.wal"
	hello := mustHelloPacket(t, sessionID, generationID, alteredProof)
	if err := VerifyHelloProof(manifest, hello); err == nil {
		t.Fatal("VerifyHelloProof() error = nil, want mismatch")
	}
}

func mustSessionID(t *testing.T, raw string) session.SessionID {
	t.Helper()
	sessionID, err := session.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	return sessionID
}

func mustGenerationID(t *testing.T, raw string) iod.GenerationID {
	t.Helper()
	generationID, err := iod.NewGenerationID(raw)
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	return generationID
}

func mustCommandID(t *testing.T, raw string) iod.CommandID {
	t.Helper()
	commandID, err := iod.NewCommandID(raw)
	if err != nil {
		t.Fatalf("NewCommandID() error = %v", err)
	}
	return commandID
}

func mustHelloProof(t *testing.T, startTS float64) iod.HelloProof {
	t.Helper()
	childPID := 456
	proof, err := iod.NewHelloProof(123, &childPID, "/tmp/iod/g_7.wal", "/tmp/iod/g_7.sock", startTS)
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	return proof
}

func mustManifest(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, proof iod.HelloProof) iod.GenerationManifest {
	t.Helper()
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	return manifest
}

func mustHelloPacket(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, proof iod.HelloProof) iod.HelloPacket {
	t.Helper()
	packet, err := iod.NewHelloPacket(sessionID, generationID, 1, proof)
	if err != nil {
		t.Fatalf("NewHelloPacket() error = %v", err)
	}
	return packet
}

func mustCommandPacket(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, commandID iod.CommandID) iod.CommandPacket {
	t.Helper()
	packet, err := iod.NewCommandPacket(sessionID, generationID, iod.CommandSend, commandID, json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("NewCommandPacket() error = %v", err)
	}
	return packet
}

func mustAcceptedPacket(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, commandID iod.CommandID, ackCursor iod.WALOffset) iod.CommandAcceptedPacket {
	t.Helper()
	outcome, err := iod.NewCommandOutcome(commandID, ackCursor, false, nil)
	if err != nil {
		t.Fatalf("NewCommandOutcome() error = %v", err)
	}
	packet, err := iod.NewCommandAcceptedPacket(sessionID, generationID, outcome)
	if err != nil {
		t.Fatalf("NewCommandAcceptedPacket() error = %v", err)
	}
	return packet
}

func mustReplayRequestPacket(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, afterOffset iod.WALOffset) iod.ReplayRequestPacket {
	t.Helper()
	packet, err := iod.NewReplayRequestPacket(sessionID, generationID, afterOffset)
	if err != nil {
		t.Fatalf("NewReplayRequestPacket() error = %v", err)
	}
	return packet
}

func mustReplayItemPacket(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, offset iod.WALOffset, seqValue uint64) iod.ReplayItemPacket {
	t.Helper()
	seq := iod.EventSeq(seqValue)
	fact, err := iod.NewHelperFact(iod.FactOutputDelta, &seq, json.RawMessage(`{"delta":"x"}`))
	if err != nil {
		t.Fatalf("NewHelperFact() error = %v", err)
	}
	item, err := iod.NewReplayItem(offset, fact)
	if err != nil {
		t.Fatalf("NewReplayItem() error = %v", err)
	}
	packet, err := iod.NewReplayItemPacket(sessionID, generationID, item)
	if err != nil {
		t.Fatalf("NewReplayItemPacket() error = %v", err)
	}
	return packet
}

func mustReplayDonePacket(t *testing.T, sessionID session.SessionID, generationID iod.GenerationID, afterOffset, lastOffset iod.WALOffset) iod.ReplayDonePacket {
	t.Helper()
	packet, err := iod.NewReplayDonePacket(sessionID, generationID, afterOffset, lastOffset, false)
	if err != nil {
		t.Fatalf("NewReplayDonePacket() error = %v", err)
	}
	return packet
}
