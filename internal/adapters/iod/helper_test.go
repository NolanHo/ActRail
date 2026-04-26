package iod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

func TestIodWalAppend(t *testing.T) {
	sessionID := mustSessionID(t, "s_123")
	generationID := mustGenerationID(t, "g_7")
	paths, err := NewGenerationPaths(t.TempDir(), sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	proof, err := NewHelloProof(101, intPtr(202), paths.WALPath, paths.ControlSocketPath, 1760000000.0)
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	if err := WriteGenerationManifest(paths.ManifestPath, manifest); err != nil {
		t.Fatalf("WriteGenerationManifest() error = %v", err)
	}
	loaded, err := ReadGenerationManifest(paths.ManifestPath)
	if err != nil {
		t.Fatalf("ReadGenerationManifest() error = %v", err)
	}
	if loaded.SessionID != manifest.SessionID || loaded.GenerationID != manifest.GenerationID || loaded.HelperPID != manifest.HelperPID || loaded.WALPath != manifest.WALPath || loaded.ControlSocketPath != manifest.ControlSocketPath || loaded.StartTS != manifest.StartTS {
		t.Fatalf("loaded manifest = %#v, want %#v", loaded, manifest)
	}
	if loaded.ChildPID == nil || manifest.ChildPID == nil || *loaded.ChildPID != *manifest.ChildPID {
		t.Fatalf("loaded child pid = %#v, want %#v", loaded.ChildPID, manifest.ChildPID)
	}
	wal, err := OpenWAL(paths.WALPath, sessionID, generationID)
	if err != nil {
		t.Fatalf("OpenWAL() error = %v", err)
	}
	defer wal.Close()
	if _, err := wal.Append(WALRecordHelperStart, helperStartPayload{ProtocolVersion: 1, HelperPID: 101, ChildPID: intPtr(202), WALPath: paths.WALPath, SocketPath: paths.ControlSocketPath, StartTS: 1760000000.0}); err != nil {
		t.Fatalf("wal.Append(helper_start) error = %v", err)
	}
	if _, err := wal.Append(WALRecordCommandAccepted, commandFactPayload{CommandID: mustCommandID(t, "cmd_1"), CommandKind: PacketCommandSend}); err != nil {
		t.Fatalf("wal.Append(command_accepted) error = %v", err)
	}
	record, err := wal.Append(WALRecordOutputDelta, terminalOutputPayload{Stream: "pty", Data: "hello"})
	if err != nil {
		t.Fatalf("wal.Append(output_delta) error = %v", err)
	}
	if record.Header.Offset != 3 {
		t.Fatalf("output delta offset = %d, want 3", record.Header.Offset)
	}
	if record.Header.Seq == nil || *record.Header.Seq != 1 {
		t.Fatalf("output delta seq = %#v, want 1", record.Header.Seq)
	}
	replay, err := ReplayWAL(paths.WALPath, sessionID, generationID, 0)
	if err != nil {
		t.Fatalf("ReplayWAL() error = %v", err)
	}
	if replay.LastOffset != 3 {
		t.Fatalf("replay.LastOffset = %d, want 3", replay.LastOffset)
	}
	if replay.CorruptTail {
		t.Fatal("replay.CorruptTail = true, want false")
	}
	if len(replay.Records) != 3 {
		t.Fatalf("len(replay.Records) = %d, want 3", len(replay.Records))
	}
	for i, record := range replay.Records {
		ok, err := walRecordChecksumOK(record)
		if err != nil {
			t.Fatalf("walRecordChecksumOK(record[%d]) error = %v", i, err)
		}
		if !ok {
			t.Fatalf("wal record %d checksum mismatch", i)
		}
	}
}

func TestIodReplay(t *testing.T) {
	tc := newHelperTestCase(t)
	defer tc.stop()

	commandID := mustCommandID(t, "cmd_accepted")
	accepted := tc.sendCommand(t, CommandSend, commandID, json.RawMessage(`{"text":"alpha"}`))
	if accepted.AckCursor != 3 {
		t.Fatalf("accepted ack cursor = %d, want 3", accepted.AckCursor)
	}
	if accepted.Deduped {
		t.Fatal("accepted.Deduped = true, want false")
	}

	dupAccepted := tc.sendCommand(t, CommandSend, commandID, json.RawMessage(`{"text":"alpha"}`))
	if dupAccepted.AckCursor != accepted.AckCursor {
		t.Fatalf("duplicate accepted ack cursor = %d, want %d", dupAccepted.AckCursor, accepted.AckCursor)
	}
	if !dupAccepted.Deduped {
		t.Fatal("duplicate accepted.Deduped = false, want true")
	}

	if got := tc.pty.Written(); got != `{"text":"alpha"}`+"\n" {
		t.Fatalf("child stdin writes = %q, want %q", got, `{"text":"alpha"}`+"\n")
	}

	tc.pty.FeedOutput("delta-one")
	output := tc.mustStatePacket(t)
	if output.Fact.FactKind != FactOutputDelta {
		t.Fatalf("output fact kind = %q, want %q", output.Fact.FactKind, FactOutputDelta)
	}
	if output.Fact.Seq == nil || *output.Fact.Seq != 1 {
		t.Fatalf("output seq = %#v, want 1", output.Fact.Seq)
	}

	tc.sendReplayRequest(t, 2)
	items := tc.mustReplayItems(t)
	if len(items) != 2 {
		t.Fatalf("len(replay items) = %d, want 2", len(items))
	}
	wantKinds := []FactKind{FactCommandAccepted, FactOutputDelta}
	wantOffsets := []WALOffset{3, 4}
	for i, item := range items {
		if item.Item.WALOffset != wantOffsets[i] {
			t.Fatalf("replay item %d offset = %d, want %d", i, item.Item.WALOffset, wantOffsets[i])
		}
		if item.Item.Fact.FactKind != wantKinds[i] {
			t.Fatalf("replay item %d fact kind = %q, want %q", i, item.Item.Fact.FactKind, wantKinds[i])
		}
	}
	done := tc.mustReplayDone(t)
	if done.AfterOffset != 2 || done.LastOffset != 4 || done.CorruptTail {
		t.Fatalf("replay done = %#v, want after_offset=2 last_offset=4 corrupt_tail=false", done)
	}
}

func TestCleanChildExit(t *testing.T) {
	tc := newHelperTestCase(t)
	defer tc.stop()

	tc.handle.SetWaitResult(process.ExitStatus{Code: 0}, nil)

	childExit := tc.mustStatePacket(t)
	if childExit.Fact.FactKind != FactChildExit {
		t.Fatalf("child exit fact kind = %q, want %q", childExit.Fact.FactKind, FactChildExit)
	}
	if childExit.Fact.Seq != nil {
		t.Fatalf("child exit seq = %#v, want nil", childExit.Fact.Seq)
	}

	helperExit := tc.mustStatePacket(t)
	if helperExit.Fact.FactKind != FactHelperExit {
		t.Fatalf("helper exit fact kind = %q, want %q", helperExit.Fact.FactKind, FactHelperExit)
	}
	if helperExit.Fact.Seq != nil {
		t.Fatalf("helper exit seq = %#v, want nil", helperExit.Fact.Seq)
	}

	tc.mustConnClosed(t)
	tc.stop()

	if _, err := os.Stat(tc.paths.ControlSocketPath); !os.IsNotExist(err) {
		t.Fatalf("control socket stat error = %v, want not exist", err)
	}
	if conn, err := net.Dial("unix", tc.paths.ControlSocketPath); err == nil {
		_ = conn.Close()
		t.Fatal("helper remained reattachable after child exit")
	}

	replay, err := ReplayWAL(tc.paths.WALPath, tc.sessionID, tc.generationID, 2)
	if err != nil {
		t.Fatalf("ReplayWAL() error = %v", err)
	}
	wantKinds := []FactKind{FactChildExit, FactHelperExit}
	if len(replay.Records) != len(wantKinds) {
		t.Fatalf("len(replay.Records) = %d, want %d", len(replay.Records), len(wantKinds))
	}
	for i, record := range replay.Records {
		if got := record.Header.Class.FactKind(); got != wantKinds[i] {
			t.Fatalf("replay record %d fact kind = %q, want %q", i, got, wantKinds[i])
		}
		if got := record.Header.Class.FactKind(); got == FactGenerationBreak {
			t.Fatalf("replay record %d fact kind = %q, want no generation break", i, got)
		}
	}
}

func TestConcurrentDuplicateCommandID(t *testing.T) {
	sessionID := mustSessionID(t, "s_dup")
	generationID := mustGenerationID(t, "g_dup")
	paths, err := NewGenerationPaths(t.TempDir(), sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	command, err := process.NewCommand("/bin/test-child", "--session", "s_dup")
	if err != nil {
		t.Fatalf("process.NewCommand() error = %v", err)
	}
	env, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("process.InheritEnv() error = %v", err)
	}
	helper, err := NewHelper(HelperOptions{
		SessionID:       sessionID,
		GenerationID:    generationID,
		Paths:           paths,
		Command:         command,
		CWD:             mustAbsDir(t, paths.RuntimeDir),
		Environment:     env,
		PTYSize:         process.PTYSize{Rows: 24, Cols: 80},
		ProtocolVersion: 1,
	})
	if err != nil {
		t.Fatalf("NewHelper() error = %v", err)
	}
	wal, err := OpenWAL(paths.WALPath, sessionID, generationID)
	if err != nil {
		t.Fatalf("OpenWAL() error = %v", err)
	}
	defer wal.Close()
	helper.wal = wal

	duplicateID := mustCommandID(t, "cmd_dup")
	var appendCalls atomic.Int32
	firstReady := make(chan struct{}, 1)
	release := make(chan struct{})
	helper.beforeResolveAppend = func(commandID CommandID) {
		if commandID != duplicateID {
			return
		}
		if appendCalls.Add(1) != 1 {
			return
		}
		firstReady <- struct{}{}
		<-release
	}

	packet, err := NewCommandPacket(sessionID, generationID, CommandSend, duplicateID, json.RawMessage(`{"text":"alpha"}`))
	if err != nil {
		t.Fatalf("NewCommandPacket() error = %v", err)
	}
	type result struct {
		kind    PacketKind
		outcome CommandOutcome
		queue   bool
		err     error
	}
	results := make(chan result, 2)
	runResolve := func() {
		kind, outcome, queue, err := helper.resolveCommand(packet)
		results <- result{kind: kind, outcome: outcome, queue: queue, err: err}
	}

	go runResolve()
	<-firstReady
	go runResolve()
	time.Sleep(50 * time.Millisecond)
	close(release)

	r1 := <-results
	r2 := <-results
	for i, r := range []result{r1, r2} {
		if r.err != nil {
			t.Fatalf("resolve result %d error = %v", i, r.err)
		}
		if r.kind != PacketCommandAccepted {
			t.Fatalf("resolve result %d kind = %q, want %q", i, r.kind, PacketCommandAccepted)
		}
	}
	if r1.outcome.AckCursor != r2.outcome.AckCursor {
		t.Fatalf("duplicate ack cursors = %d and %d, want one durable outcome", r1.outcome.AckCursor, r2.outcome.AckCursor)
	}
	if r1.outcome.Deduped == r2.outcome.Deduped {
		t.Fatalf("duplicate dedupe flags = %v and %v, want one original and one deduped", r1.outcome.Deduped, r2.outcome.Deduped)
	}
	if r1.queue == r2.queue {
		t.Fatalf("duplicate queue flags = %v and %v, want one queued command", r1.queue, r2.queue)
	}

	replay, err := ReplayWAL(paths.WALPath, sessionID, generationID, 0)
	if err != nil {
		t.Fatalf("ReplayWAL() error = %v", err)
	}
	accepted := 0
	for _, record := range replay.Records {
		if record.Header.Class != WALRecordCommandAccepted {
			continue
		}
		var payload commandFactPayload
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatalf("json.Unmarshal(command accepted payload) error = %v", err)
		}
		if payload.CommandID == packet.CommandID {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted wal records for %q = %d, want 1", packet.CommandID, accepted)
	}
}

func TestHelperGenerationBreak(t *testing.T) {
	tc := newHelperTestCase(t)
	defer tc.stop()

	tc.pty.SetWriteErr(errors.New("broken pipe"))
	accepted := tc.sendCommand(t, CommandSend, mustCommandID(t, "cmd_break"), json.RawMessage(`{"text":"break"}`))
	if accepted.AckCursor != 3 {
		t.Fatalf("accepted ack cursor = %d, want 3", accepted.AckCursor)
	}
	breakPacket := tc.mustGenerationBreak(t)
	if breakPacket.Seq != 1 {
		t.Fatalf("generation break seq = %d, want 1", breakPacket.Seq)
	}
	if breakPacket.Reason != GenerationBreakWriteFailed {
		t.Fatalf("generation break reason = %q, want %q", breakPacket.Reason, GenerationBreakWriteFailed)
	}

	rejected := tc.sendRejectedCommand(t, CommandSend, mustCommandID(t, "cmd_after_break"), json.RawMessage(`{"text":"gamma"}`))
	if rejected.AckCursor != 5 {
		t.Fatalf("rejected ack cursor = %d, want 5", rejected.AckCursor)
	}

	manifest, err := ReadGenerationManifest(tc.paths.ManifestPath)
	if err != nil {
		t.Fatalf("ReadGenerationManifest() error = %v", err)
	}
	if manifest.SessionID != tc.sessionID || manifest.GenerationID != tc.generationID {
		t.Fatalf("manifest identity = %#v, want session=%q generation=%q", manifest, tc.sessionID, tc.generationID)
	}

	tc.stop()
	replay, err := ReplayWAL(tc.paths.WALPath, tc.sessionID, tc.generationID, 2)
	if err != nil {
		t.Fatalf("ReplayWAL() error = %v", err)
	}
	wantKinds := []FactKind{FactCommandAccepted, FactGenerationBreak, FactCommandRejected, FactHelperExit}
	if len(replay.Records) != len(wantKinds) {
		t.Fatalf("len(replay.Records) = %d, want %d", len(replay.Records), len(wantKinds))
	}
	for i, record := range replay.Records {
		if got := record.Header.Class.FactKind(); got != wantKinds[i] {
			t.Fatalf("replay record %d fact kind = %q, want %q", i, got, wantKinds[i])
		}
	}
	if replay.Records[1].Header.Seq == nil || *replay.Records[1].Header.Seq != 1 {
		t.Fatalf("generation break replay seq = %#v, want 1", replay.Records[1].Header.Seq)
	}
}

type helperTestCase struct {
	t                 *testing.T
	sessionID         session.SessionID
	generationID      GenerationID
	paths             GenerationPaths
	pty               *testPTY
	handle            *testHandle
	conn              net.Conn
	dec               *json.Decoder
	enc               *json.Encoder
	cancel            context.CancelFunc
	errCh             chan error
	stopOnce          sync.Once
	pendingReplayDone json.RawMessage
}

func newHelperTestCase(t *testing.T) *helperTestCase {
	t.Helper()
	sessionID := mustSessionID(t, "s_123")
	generationID := mustGenerationID(t, "g_7")
	paths, err := NewGenerationPaths(t.TempDir(), sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	pty := newTestPTY()
	handle := newTestHandle(4321, pty)
	runner := &process.FakeRunner{HandleBuild: func(spec process.LaunchSpec) process.Handle {
		handle.spec = spec
		return handle
	}}
	command, err := process.NewCommand("/bin/test-child", "--session", "s_123")
	if err != nil {
		t.Fatalf("process.NewCommand() error = %v", err)
	}
	env, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("process.InheritEnv() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunHelper(ctx, HelperOptions{
			SessionID:       sessionID,
			GenerationID:    generationID,
			Paths:           paths,
			Command:         command,
			CWD:             mustAbsDir(t, paths.RuntimeDir),
			Environment:     env,
			PTYSize:         process.PTYSize{Rows: 24, Cols: 80},
			ProtocolVersion: 1,
			Runner:          runner,
			Now: func() time.Time {
				return time.Unix(1760000000, 0).UTC()
			},
		})
	}()
	waitForSocket(t, paths.ControlSocketPath)
	conn, err := net.Dial("unix", paths.ControlSocketPath)
	if err != nil {
		cancel()
		t.Fatalf("net.Dial(unix) error = %v", err)
	}
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	var hello HelloPacket
	if err := decodeWithin(t, dec, &hello); err != nil {
		cancel()
		_ = conn.Close()
		t.Fatalf("decode hello error = %v", err)
	}
	manifest, err := ReadGenerationManifest(paths.ManifestPath)
	if err != nil {
		cancel()
		_ = conn.Close()
		t.Fatalf("ReadGenerationManifest() error = %v", err)
	}
	if hello.SessionID != manifest.SessionID || hello.GenerationID != manifest.GenerationID || hello.HelperPID != manifest.HelperPID || hello.WALPath != manifest.WALPath || hello.ControlSocketPath != manifest.ControlSocketPath || hello.StartTS != manifest.StartTS {
		cancel()
		_ = conn.Close()
		t.Fatalf("hello proof does not match manifest: hello=%#v manifest=%#v", hello, manifest)
	}
	return &helperTestCase{t: t, sessionID: sessionID, generationID: generationID, paths: paths, pty: pty, handle: handle, conn: conn, dec: dec, enc: enc, cancel: cancel, errCh: errCh}
}

func (tc *helperTestCase) stop() {
	tc.stopOnce.Do(func() {
		tc.cancel()
		_ = tc.conn.Close()
		select {
		case err := <-tc.errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				tc.t.Fatalf("helper returned error = %v", err)
			}
		case <-time.After(2 * time.Second):
			tc.t.Fatal("helper stop timed out")
		}
	})
}

func (tc *helperTestCase) sendCommand(t *testing.T, name CommandName, commandID CommandID, payload json.RawMessage) CommandOutcome {
	t.Helper()
	packet, err := NewCommandPacket(tc.sessionID, tc.generationID, name, commandID, payload)
	if err != nil {
		t.Fatalf("NewCommandPacket() error = %v", err)
	}
	if err := tc.enc.Encode(packet); err != nil {
		t.Fatalf("encode command packet error = %v", err)
	}
	var response CommandAcceptedPacket
	if err := decodeWithin(t, tc.dec, &response); err != nil {
		t.Fatalf("decode accepted packet error = %v", err)
	}
	return response.CommandOutcome
}

func (tc *helperTestCase) sendRejectedCommand(t *testing.T, name CommandName, commandID CommandID, payload json.RawMessage) CommandOutcome {
	t.Helper()
	packet, err := NewCommandPacket(tc.sessionID, tc.generationID, name, commandID, payload)
	if err != nil {
		t.Fatalf("NewCommandPacket() error = %v", err)
	}
	if err := tc.enc.Encode(packet); err != nil {
		t.Fatalf("encode rejected command packet error = %v", err)
	}
	var response CommandRejectedPacket
	if err := decodeWithin(t, tc.dec, &response); err != nil {
		t.Fatalf("decode rejected packet error = %v", err)
	}
	return response.CommandOutcome
}

func (tc *helperTestCase) mustStatePacket(t *testing.T) StatePacket {
	t.Helper()
	var packet StatePacket
	if err := decodeWithin(t, tc.dec, &packet); err != nil {
		t.Fatalf("decode state packet error = %v", err)
	}
	return packet
}

func (tc *helperTestCase) mustGenerationBreak(t *testing.T) GenerationBreakPacket {
	t.Helper()
	var packet GenerationBreakPacket
	if err := decodeWithin(t, tc.dec, &packet); err != nil {
		t.Fatalf("decode generation break packet error = %v", err)
	}
	return packet
}

func (tc *helperTestCase) mustConnClosed(t *testing.T) {
	t.Helper()
	var packet json.RawMessage
	if err := decodeWithin(t, tc.dec, &packet); err == nil {
		t.Fatalf("decode after helper exit = %#v, want connection closed", packet)
	} else if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("decode after helper exit error = %v, want EOF", err)
	}
}

func (tc *helperTestCase) sendReplayRequest(t *testing.T, after WALOffset) {
	t.Helper()
	packet, err := NewReplayRequestPacket(tc.sessionID, tc.generationID, after)
	if err != nil {
		t.Fatalf("NewReplayRequestPacket() error = %v", err)
	}
	if err := tc.enc.Encode(packet); err != nil {
		t.Fatalf("encode replay request error = %v", err)
	}
}

func (tc *helperTestCase) mustReplayItems(t *testing.T) []ReplayItemPacket {
	t.Helper()
	items := []ReplayItemPacket{}
	for {
		raw, err := decodeRawWithin(t, tc.dec)
		if err != nil {
			t.Fatalf("decode replay raw error = %v", err)
		}
		var peek struct {
			Kind PacketKind `json:"kind"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			t.Fatalf("json.Unmarshal(replay peek) error = %v", err)
		}
		if peek.Kind == PacketReplayDone {
			tc.pendingReplayDone = raw
			return items
		}
		var item ReplayItemPacket
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("json.Unmarshal(replay item) error = %v", err)
		}
		items = append(items, item)
	}
}

func (tc *helperTestCase) mustReplayDone(t *testing.T) ReplayDonePacket {
	t.Helper()
	var raw json.RawMessage
	if len(tc.pendingReplayDone) > 0 {
		raw = append(json.RawMessage(nil), tc.pendingReplayDone...)
		tc.pendingReplayDone = nil
	} else {
		var err error
		raw, err = decodeRawWithin(t, tc.dec)
		if err != nil {
			t.Fatalf("decode replay done raw error = %v", err)
		}
	}
	var packet ReplayDonePacket
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatalf("json.Unmarshal(replay done) error = %v", err)
	}
	return packet
}

func decodeWithin(t *testing.T, dec *json.Decoder, dst any) error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() {
		errCh <- dec.Decode(dst)
	}()
	select {
	case err := <-errCh:
		return err
	case <-time.After(2 * time.Second):
		return errors.New("decode timed out")
	}
}

func decodeRawWithin(t *testing.T, dec *json.Decoder) (json.RawMessage, error) {
	t.Helper()
	var raw json.RawMessage
	if err := decodeWithin(t, dec, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("control socket %q was not created", path)
}

func mustAbsDir(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("filepath.Abs(%q) error = %v", path, err)
	}
	return abs
}

func intPtr(v int) *int { return &v }

type testPTY struct {
	reader   *io.PipeReader
	writer   *io.PipeWriter
	mu       sync.Mutex
	writes   bytes.Buffer
	writeErr error
}

func newTestPTY() *testPTY {
	r, w := io.Pipe()
	return &testPTY{reader: r, writer: w}
}

func (p *testPTY) Read(b []byte) (int, error) {
	return p.reader.Read(b)
}

func (p *testPTY) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writeErr != nil {
		return 0, p.writeErr
	}
	return p.writes.Write(b)
}

func (p *testPTY) Close() error {
	_ = p.writer.Close()
	return p.reader.Close()
}

func (p *testPTY) Resize(process.PTYSize) error { return nil }

func (p *testPTY) FeedOutput(text string) {
	_, _ = io.WriteString(p.writer, text)
}

func (p *testPTY) SetWriteErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writeErr = err
}

func (p *testPTY) Written() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writes.String()
}

type testHandle struct {
	mu      sync.Mutex
	pid     int
	spec    process.LaunchSpec
	pty     process.PTY
	waitCh  chan struct{}
	wait    process.ExitStatus
	waitErr error
}

func newTestHandle(pid int, pty process.PTY) *testHandle {
	return &testHandle{pid: pid, pty: pty, waitCh: make(chan struct{})}
}

func (h *testHandle) PID() int                 { return h.pid }
func (h *testHandle) Spec() process.LaunchSpec { return h.spec }
func (h *testHandle) Logs() process.LogPaths   { return process.LogPaths{} }
func (h *testHandle) Stdout() io.ReadCloser    { return nil }
func (h *testHandle) Stderr() io.ReadCloser    { return nil }
func (h *testHandle) PTY() process.PTY         { return h.pty }
func (h *testHandle) Signal(os.Signal) error   { return nil }
func (h *testHandle) Interrupt() error         { return nil }
func (h *testHandle) Kill() error              { return nil }

func (h *testHandle) Wait(ctx context.Context) (process.ExitStatus, error) {
	select {
	case <-ctx.Done():
		return process.ExitStatus{}, ctx.Err()
	case <-h.waitCh:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.wait, h.waitErr
	}
}

func (h *testHandle) SetWaitResult(status process.ExitStatus, err error) {
	h.mu.Lock()
	h.wait = status
	h.waitErr = err
	h.mu.Unlock()
	select {
	case <-h.waitCh:
	default:
		close(h.waitCh)
	}
}
