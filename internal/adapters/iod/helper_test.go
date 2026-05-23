package iod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
	"github.com/gorilla/websocket"
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

func TestNewHelperUsesPipeIOForStdioChildMode(t *testing.T) {
	sessionID := mustSessionID(t, "s_stdio")
	generationID := mustGenerationID(t, "g_stdio")
	paths, err := NewGenerationPaths(t.TempDir(), sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	command, err := process.NewCommand("/bin/pi", "--mode", "rpc")
	if err != nil {
		t.Fatalf("NewCommand() error = %v", err)
	}
	env, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	helper, err := NewHelper(HelperOptions{
		SessionID:       sessionID,
		GenerationID:    generationID,
		Paths:           paths,
		Command:         command,
		CWD:             mustAbsDir(t, paths.RuntimeDir),
		Environment:     env,
		ChildIOMode:     ChildIOModeStdio,
		ProtocolVersion: 1,
	})
	if err != nil {
		t.Fatalf("NewHelper() error = %v", err)
	}
	if helper.childIOMode != ChildIOModeStdio {
		t.Fatalf("helper.childIOMode = %q, want %q", helper.childIOMode, ChildIOModeStdio)
	}
	if helper.launchSpec.IO().Mode() != process.IOModePipes {
		t.Fatalf("helper launch io mode = %q, want %q", helper.launchSpec.IO().Mode(), process.IOModePipes)
	}
}

func TestIodUnixChildModeForwardsOverChildSocket(t *testing.T) {
	sessionID := mustSessionID(t, "s_unix")
	generationID := mustGenerationID(t, "g_unix")
	paths, err := NewGenerationPaths(t.TempDir(), sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	if err := paths.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir() error = %v", err)
	}
	if err := os.WriteFile(paths.WALPath, []byte("stale wal"), 0o644); err != nil {
		t.Fatalf("write stale WAL error = %v", err)
	}
	listenErr := make(chan error, 1)
	listeners := make(chan net.Listener, 1)
	accepted := make(chan *websocket.Conn, 1)
	defer func() {
		select {
		case listener := <-listeners:
			_ = listener.Close()
		default:
		}
	}()

	handle := newTestHandle(4321, nil)
	runner := &process.FakeRunner{HandleBuild: func(spec process.LaunchSpec) process.Handle {
		handle.spec = spec
		childListener, err := net.Listen("unix", paths.ChildSocketPath)
		if err != nil {
			listenErr <- err
			return handle
		}
		listeners <- childListener
		go func() {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					close(accepted)
					return
				}
				accepted <- conn
			})}
			if err := server.Serve(childListener); err != nil && !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "use of closed network connection") {
				listenErr <- err
			}
		}()
		return handle
	}}
	command, err := process.NewCommand("/bin/test-child", "--listen", "unix://"+paths.ChildSocketPath)
	if err != nil {
		t.Fatalf("process.NewCommand() error = %v", err)
	}
	env, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("process.InheritEnv() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunHelper(ctx, HelperOptions{
			SessionID:       sessionID,
			GenerationID:    generationID,
			Paths:           paths,
			Command:         command,
			CWD:             mustAbsDir(t, paths.RuntimeDir),
			Environment:     env,
			ChildIOMode:     ChildIOModeUnix,
			ProtocolVersion: 1,
			Runner:          runner,
			Now: func() time.Time {
				return time.Unix(1760000000, 0).UTC()
			},
		})
	}()
	var childConn *websocket.Conn
	select {
	case conn, ok := <-accepted:
		if !ok {
			t.Fatal("child socket websocket upgrade failed")
		}
		childConn = conn
	case err := <-listenErr:
		t.Fatalf("Listen(%q) error = %v", paths.ChildSocketPath, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for helper to connect to child socket")
	}
	defer childConn.Close()

	waitForSocket(t, paths.ControlSocketPath)
	control, err := net.Dial("unix", paths.ControlSocketPath)
	if err != nil {
		t.Fatalf("Dial(control) error = %v", err)
	}
	defer control.Close()
	dec := json.NewDecoder(control)
	enc := json.NewEncoder(control)
	var hello HelloPacket
	if err := decodeWithin(t, dec, &hello); err != nil {
		t.Fatalf("decode hello error = %v", err)
	}
	commandID := mustCommandID(t, "cmd_unix")
	packet, err := NewCommandPacket(sessionID, generationID, CommandSend, commandID, json.RawMessage(`{"method":"initialize"}`))
	if err != nil {
		t.Fatalf("NewCommandPacket() error = %v", err)
	}
	if err := enc.Encode(packet); err != nil {
		t.Fatalf("encode command error = %v", err)
	}
	var response CommandAcceptedPacket
	if err := decodeWithin(t, dec, &response); err != nil {
		t.Fatalf("decode accepted error = %v", err)
	}
	if response.AckCursor != 1 {
		t.Fatalf("accepted ack cursor = %d, want in-memory cursor 1", response.AckCursor)
	}
	if err := enc.Encode(packet); err != nil {
		t.Fatalf("encode duplicate command error = %v", err)
	}
	var duplicate CommandAcceptedPacket
	if err := decodeWithin(t, dec, &duplicate); err != nil {
		t.Fatalf("decode duplicate accepted error = %v", err)
	}
	if duplicate.AckCursor != response.AckCursor || !duplicate.Deduped {
		t.Fatalf("duplicate outcome = %+v, want deduped cursor %d", duplicate.CommandOutcome, response.AckCursor)
	}
	_ = childConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	messageType, msg, err := childConn.ReadMessage()
	if err != nil {
		t.Fatalf("read child socket websocket command error = %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("child socket message type = %d, want text", messageType)
	}
	if got, want := string(msg), "{\"method\":\"initialize\"}"; got != want {
		t.Fatalf("child socket command = %q, want %q", got, want)
	}
	if err := childConn.WriteMessage(websocket.TextMessage, []byte("{\"method\":\"thread/started\"}")); err != nil {
		t.Fatalf("write child socket websocket output error = %v", err)
	}
	var state StatePacket
	if err := decodeWithin(t, dec, &state); err != nil {
		t.Fatalf("decode child socket state error = %v", err)
	}
	if state.Fact.FactKind != FactOutputDelta {
		t.Fatalf("state fact kind = %q, want %q", state.Fact.FactKind, FactOutputDelta)
	}
	var payload terminalOutputPayload
	if err := json.Unmarshal(state.Fact.Payload, &payload); err != nil {
		t.Fatalf("decode output payload error = %v", err)
	}
	if payload.Stream != "unix" || payload.Data != "{\"method\":\"thread/started\"}\n" {
		t.Fatalf("output payload = %+v, want unix thread/started", payload)
	}
	if state.Fact.Seq == nil || *state.Fact.Seq != 1 {
		t.Fatalf("state output seq = %#v, want 1", state.Fact.Seq)
	}
	if _, err := os.Stat(paths.WALPath); !os.IsNotExist(err) {
		t.Fatalf("unix helper WAL stat error = %v, want not exist", err)
	}
	replayRequest, err := NewReplayRequestPacket(sessionID, generationID, 0)
	if err != nil {
		t.Fatalf("NewReplayRequestPacket() error = %v", err)
	}
	if err := enc.Encode(replayRequest); err != nil {
		t.Fatalf("encode replay request error = %v", err)
	}
	var replayDone ReplayDonePacket
	if err := decodeWithin(t, dec, &replayDone); err != nil {
		t.Fatalf("decode unix replay done error = %v", err)
	}
	if replayDone.AfterOffset != 0 || replayDone.LastOffset != 0 || replayDone.CorruptTail {
		t.Fatalf("unix replay done = %+v, want empty replay", replayDone)
	}

	cancel()
	_ = control.Close()
	_ = childConn.Close()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("helper returned error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper stop timed out")
	}
}

func TestIodUnixChildModeGenerationBreakIsLiveOnly(t *testing.T) {
	sessionID := mustSessionID(t, "s_unix_break")
	generationID := mustGenerationID(t, "g_unix_break")
	paths, err := NewGenerationPaths(t.TempDir(), sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	if err := paths.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir() error = %v", err)
	}
	listenErr := make(chan error, 1)
	listeners := make(chan net.Listener, 1)
	accepted := make(chan *websocket.Conn, 1)
	defer func() {
		select {
		case listener := <-listeners:
			_ = listener.Close()
		default:
		}
	}()

	handle := newTestHandle(4324, nil)
	runner := &process.FakeRunner{HandleBuild: func(spec process.LaunchSpec) process.Handle {
		handle.spec = spec
		childListener, err := net.Listen("unix", paths.ChildSocketPath)
		if err != nil {
			listenErr <- err
			return handle
		}
		listeners <- childListener
		go func() {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					close(accepted)
					return
				}
				accepted <- conn
			})}
			if err := server.Serve(childListener); err != nil && !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "use of closed network connection") {
				listenErr <- err
			}
		}()
		return handle
	}}
	command, err := process.NewCommand("/bin/test-child", "--listen", "unix://"+paths.ChildSocketPath)
	if err != nil {
		t.Fatalf("process.NewCommand() error = %v", err)
	}
	env, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("process.InheritEnv() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunHelper(ctx, HelperOptions{
			SessionID:       sessionID,
			GenerationID:    generationID,
			Paths:           paths,
			Command:         command,
			CWD:             mustAbsDir(t, paths.RuntimeDir),
			Environment:     env,
			ChildIOMode:     ChildIOModeUnix,
			ProtocolVersion: 1,
			Runner:          runner,
			Now: func() time.Time {
				return time.Unix(1760000000, 0).UTC()
			},
		})
	}()
	childConn := acceptChildConn(t, accepted, listenErr, paths.ChildSocketPath)
	defer childConn.Close()

	waitForSocket(t, paths.ControlSocketPath)
	control, err := net.Dial("unix", paths.ControlSocketPath)
	if err != nil {
		t.Fatalf("Dial(control) error = %v", err)
	}
	defer control.Close()
	dec := json.NewDecoder(control)
	var hello HelloPacket
	if err := decodeWithin(t, dec, &hello); err != nil {
		t.Fatalf("decode hello error = %v", err)
	}

	handle.SetWaitResult(process.ExitStatus{Code: 0}, nil)
	var childExit StatePacket
	if err := decodeWithin(t, dec, &childExit); err != nil {
		t.Fatalf("decode child exit state error = %v", err)
	}
	if childExit.Fact.FactKind != FactChildExit || childExit.Fact.Seq != nil {
		t.Fatalf("child exit state = %+v, want live child_exit without seq", childExit.Fact)
	}
	if _, err := os.Stat(paths.WALPath); !os.IsNotExist(err) {
		t.Fatalf("unix helper WAL stat error = %v, want not exist after child exit", err)
	}

	cancel()
	_ = control.Close()
	_ = childConn.Close()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("helper returned error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper stop timed out")
	}
}

func TestIodUnixChildModeReconnectsChildSocket(t *testing.T) {
	sessionID := mustSessionID(t, "s_unix_reconnect")
	generationID := mustGenerationID(t, "g_unix_reconnect")
	paths, err := NewGenerationPaths(t.TempDir(), sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	if err := paths.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir() error = %v", err)
	}
	listenErr := make(chan error, 1)
	listeners := make(chan net.Listener, 1)
	accepted := make(chan *websocket.Conn, 2)
	defer func() {
		select {
		case listener := <-listeners:
			_ = listener.Close()
		default:
		}
	}()

	handle := newTestHandle(4323, nil)
	runner := &process.FakeRunner{HandleBuild: func(spec process.LaunchSpec) process.Handle {
		handle.spec = spec
		childListener, err := net.Listen("unix", paths.ChildSocketPath)
		if err != nil {
			listenErr <- err
			return handle
		}
		listeners <- childListener
		go func() {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					listenErr <- err
					return
				}
				accepted <- conn
			})}
			if err := server.Serve(childListener); err != nil && !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "use of closed network connection") {
				listenErr <- err
			}
		}()
		return handle
	}}
	command, err := process.NewCommand("/bin/test-child", "--listen", "unix://"+paths.ChildSocketPath)
	if err != nil {
		t.Fatalf("process.NewCommand() error = %v", err)
	}
	env, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("process.InheritEnv() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunHelper(ctx, HelperOptions{
			SessionID:       sessionID,
			GenerationID:    generationID,
			Paths:           paths,
			Command:         command,
			CWD:             mustAbsDir(t, paths.RuntimeDir),
			Environment:     env,
			ChildIOMode:     ChildIOModeUnix,
			ProtocolVersion: 1,
			Runner:          runner,
			Now: func() time.Time {
				return time.Unix(1760000000, 0).UTC()
			},
		})
	}()
	firstConn := acceptChildConn(t, accepted, listenErr, paths.ChildSocketPath)

	waitForSocket(t, paths.ControlSocketPath)
	control, err := net.Dial("unix", paths.ControlSocketPath)
	if err != nil {
		t.Fatalf("Dial(control) error = %v", err)
	}
	defer control.Close()
	dec := json.NewDecoder(control)
	enc := json.NewEncoder(control)
	var hello HelloPacket
	if err := decodeWithin(t, dec, &hello); err != nil {
		t.Fatalf("decode hello error = %v", err)
	}

	sendHelperCommand(t, enc, dec, sessionID, generationID, "cmd_unix_reconnect_1", `{"method":"initialize"}`)
	assertChildMessage(t, firstConn, `{"method":"initialize"}`)
	_ = firstConn.Close()

	secondConn := acceptChildConn(t, accepted, listenErr, paths.ChildSocketPath)
	defer secondConn.Close()
	sendHelperCommand(t, enc, dec, sessionID, generationID, "cmd_unix_reconnect_2", `{"method":"thread/read"}`)
	assertChildMessage(t, secondConn, `{"method":"thread/read"}`)

	cancel()
	_ = control.Close()
	_ = secondConn.Close()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("helper returned error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper stop timed out")
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

	tc.waitForPTYWritten(t, `{"text":"alpha"}`+"\n")

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

	if conn, err := net.Dial("unix", tc.paths.ControlSocketPath); err == nil {
		_ = conn.Close()
	} else {
		t.Fatalf("helper not reattachable after child exit: %v", err)
	}

	replay, err := ReplayWAL(tc.paths.WALPath, tc.sessionID, tc.generationID, 2)
	if err != nil {
		t.Fatalf("ReplayWAL() error = %v", err)
	}
	wantKinds := []FactKind{FactChildExit}
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

	tc.stop()
	if _, err := os.Stat(tc.paths.ControlSocketPath); !os.IsNotExist(err) {
		t.Fatalf("control socket stat error = %v, want not exist after helper stop", err)
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

func TestOutputDeltaOnlyForStructuredPIOutput(t *testing.T) {
	tc := newHelperTestCase(t)
	defer tc.stop()

	tc.pty.FeedOutput(`{"type":"turn.completed","turn_id":"turn-001","role":"assistant","text":"Committed output."}` + "\n")

	output := tc.mustStatePacket(t)
	if output.Fact.FactKind != FactOutputDelta {
		t.Fatalf("output fact kind = %q, want %q", output.Fact.FactKind, FactOutputDelta)
	}
	if output.Fact.Seq == nil || *output.Fact.Seq != 1 {
		t.Fatalf("output seq = %#v, want 1", output.Fact.Seq)
	}

	tc.sendReplayRequest(t, 2)
	items := tc.mustReplayItems(t)
	if len(items) != 1 {
		t.Fatalf("len(replay items) = %d, want 1", len(items))
	}
	item := items[0]
	if item.Item.WALOffset != 3 {
		t.Fatalf("replay item offset = %d, want 3", item.Item.WALOffset)
	}
	if item.Item.Fact.FactKind != FactOutputDelta {
		t.Fatalf("replay item fact kind = %q, want %q", item.Item.Fact.FactKind, FactOutputDelta)
	}
	if item.Item.Fact.Seq == nil || *item.Item.Fact.Seq != 1 {
		t.Fatalf("replay item seq = %#v, want 1", item.Item.Fact.Seq)
	}
}

func TestSplitStructuredOutputStillProducesOutputDeltaOnly(t *testing.T) {
	tc := newHelperTestCase(t)
	defer tc.stop()

	tc.pty.FeedOutput(`{"type":"extension_ui_request","id":"ui-req-1","method":"select","question":"Where should this go?","options":["Details"`)
	output1 := tc.mustStatePacket(t)
	if output1.Fact.FactKind != FactOutputDelta {
		t.Fatalf("first output fact kind = %q, want %q", output1.Fact.FactKind, FactOutputDelta)
	}
	if output1.Fact.Seq == nil || *output1.Fact.Seq != 1 {
		t.Fatalf("first output seq = %#v, want 1", output1.Fact.Seq)
	}

	tc.pty.FeedOutput(`,"Sidebar"]}` + "\n")
	output2 := tc.mustStatePacket(t)
	if output2.Fact.FactKind != FactOutputDelta {
		t.Fatalf("second output fact kind = %q, want %q", output2.Fact.FactKind, FactOutputDelta)
	}
	if output2.Fact.Seq == nil || *output2.Fact.Seq != 2 {
		t.Fatalf("second output seq = %#v, want 2", output2.Fact.Seq)
	}

	tc.sendReplayRequest(t, 2)
	items := tc.mustReplayItems(t)
	if len(items) != 2 {
		t.Fatalf("len(replay items) = %d, want 2", len(items))
	}
	wantKinds := []FactKind{FactOutputDelta, FactOutputDelta}
	wantOffsets := []WALOffset{3, 4}
	for i, item := range items {
		if item.Item.WALOffset != wantOffsets[i] {
			t.Fatalf("replay item %d offset = %d, want %d", i, item.Item.WALOffset, wantOffsets[i])
		}
		if item.Item.Fact.FactKind != wantKinds[i] {
			t.Fatalf("replay item %d fact kind = %q, want %q", i, item.Item.Fact.FactKind, wantKinds[i])
		}
	}
}

func TestUIResponseSubmitDoesNotEmitSemanticFact(t *testing.T) {
	tc := newHelperTestCase(t)
	defer tc.stop()

	commandID := mustCommandID(t, "cmd_ui_response")
	accepted := tc.sendCommand(t, CommandUIResponseSubmit, commandID, json.RawMessage(`{"type":"extension_ui_response","id":"ui-req-1","value":"Details"}`))
	if accepted.AckCursor != 3 {
		t.Fatalf("accepted ack cursor = %d, want 3", accepted.AckCursor)
	}
	tc.waitForPTYWritten(t, `{"type":"extension_ui_response","id":"ui-req-1","value":"Details"}`+"\n")

	tc.sendReplayRequest(t, 2)
	items := tc.mustReplayItems(t)
	if len(items) != 1 {
		t.Fatalf("len(replay items) = %d, want 1", len(items))
	}
	item := items[0]
	if item.Item.WALOffset != 3 {
		t.Fatalf("replay item offset = %d, want 3", item.Item.WALOffset)
	}
	if item.Item.Fact.FactKind != FactCommandAccepted {
		t.Fatalf("replay item fact kind = %q, want %q", item.Item.Fact.FactKind, FactCommandAccepted)
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

func TestHealthRequestReturnsShallowFactsWithoutTouchingChildPromptChannel(t *testing.T) {
	tc := newHelperTestCase(t)
	defer tc.stop()

	request := map[string]any{
		"session_id":    tc.sessionID.String(),
		"generation_id": tc.generationID.String(),
		"kind":          "iod.health.request",
	}
	if err := tc.enc.Encode(request); err != nil {
		t.Fatalf("encode health request error = %v", err)
	}

	raw, err := decodeRawWithin(t, tc.dec)
	if err != nil {
		t.Fatalf("decode health response error = %v", err)
	}
	if got := tc.pty.Written(); got != "" {
		t.Fatalf("child prompt channel writes after health request = %q, want none", got)
	}

	var response struct {
		Kind              string `json:"kind"`
		SessionID         string `json:"session_id"`
		GenerationID      string `json:"generation_id"`
		HelperPID         int    `json:"helper_pid"`
		ChildPID          int    `json:"child_pid"`
		ChildIOMode       string `json:"child_io_mode"`
		LegacyTransport   bool   `json:"legacy_transport"`
		Deprecated        bool   `json:"deprecated"`
		EnsureSupported   bool   `json:"ensure_supported"`
		PromptProbe       bool   `json:"prompt_probe"`
		ControlSocketPath string `json:"control_socket_path"`
		WALPath           string `json:"wal_path"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("json.Unmarshal(health response) error = %v; raw=%s", err, raw)
	}
	if response.Kind != "iod.health.response" {
		t.Fatalf("health response kind = %q, want iod.health.response; raw=%s", response.Kind, raw)
	}
	if response.SessionID != tc.sessionID.String() || response.GenerationID != tc.generationID.String() {
		t.Fatalf("health identity = (%q, %q), want (%q, %q)", response.SessionID, response.GenerationID, tc.sessionID, tc.generationID)
	}
	if response.HelperPID <= 0 || response.ChildPID != tc.handle.PID() {
		t.Fatalf("health pids = helper:%d child:%d, want helper>0 child=%d", response.HelperPID, response.ChildPID, tc.handle.PID())
	}
	if response.ChildIOMode != string(ChildIOModePTY) {
		t.Fatalf("health child_io_mode = %q, want %q", response.ChildIOMode, ChildIOModePTY)
	}
	if !response.LegacyTransport || !response.Deprecated || response.EnsureSupported || response.PromptProbe {
		t.Fatalf("health legacy semantics = legacy:%t deprecated:%t ensure:%t prompt_probe:%t, want legacy deprecated without ensure or prompt probe", response.LegacyTransport, response.Deprecated, response.EnsureSupported, response.PromptProbe)
	}
	if response.ControlSocketPath != tc.paths.ControlSocketPath || response.WALPath != tc.paths.WALPath {
		t.Fatalf("health paths = (%q, %q), want (%q, %q)", response.ControlSocketPath, response.WALPath, tc.paths.ControlSocketPath, tc.paths.WALPath)
	}
}

func TestStdioHealthMarksLegacyDeprecatedWithoutBreakingCompatibility(t *testing.T) {
	sessionID := mustSessionID(t, "s_stdio_health")
	generationID := mustGenerationID(t, "g_stdio_health")
	root, err := os.MkdirTemp("", "iod-stdio-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	defer os.RemoveAll(root)
	paths, err := NewGenerationPaths(root, sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	handle := newTestHandle(4322, nil)
	stdio := newTestStdio()
	handle.stdin = stdio.stdin
	handle.stdout = stdio.stdout
	handle.stderr = stdio.stderr
	runner := &process.FakeRunner{HandleBuild: func(spec process.LaunchSpec) process.Handle {
		handle.spec = spec
		return handle
	}}
	command, err := process.NewCommand("/bin/test-child", "--stdio")
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
			ChildIOMode:     ChildIOModeStdio,
			ProtocolVersion: 1,
			Runner:          runner,
			Now:             func() time.Time { return time.Unix(1760000000, 0).UTC() },
		})
	}()
	waitForSocket(t, paths.ControlSocketPath)
	conn, err := net.Dial("unix", paths.ControlSocketPath)
	if err != nil {
		cancel()
		t.Fatalf("net.Dial(unix) error = %v", err)
	}
	defer func() {
		cancel()
		_ = conn.Close()
		stdio.close()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("helper returned error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("helper stop timed out")
		}
	}()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	var hello HelloPacket
	if err := decodeWithin(t, dec, &hello); err != nil {
		t.Fatalf("decode hello error = %v", err)
	}
	request := map[string]any{
		"session_id":    sessionID.String(),
		"generation_id": generationID.String(),
		"kind":          "iod.health.request",
	}
	if err := enc.Encode(request); err != nil {
		t.Fatalf("encode health request error = %v", err)
	}
	raw, err := decodeRawWithin(t, dec)
	if err != nil {
		t.Fatalf("decode health response error = %v", err)
	}
	if got := stdio.Written(); got != "" {
		t.Fatalf("child stdin writes after health request = %q, want none", got)
	}
	var response struct {
		Kind            string `json:"kind"`
		ChildIOMode     string `json:"child_io_mode"`
		LegacyTransport bool   `json:"legacy_transport"`
		Deprecated      bool   `json:"deprecated"`
		EnsureSupported bool   `json:"ensure_supported"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("json.Unmarshal(health response) error = %v; raw=%s", err, raw)
	}
	if response.Kind != "iod.health.response" {
		t.Fatalf("health response kind = %q, want iod.health.response; raw=%s", response.Kind, raw)
	}
	if response.ChildIOMode != string(ChildIOModeStdio) {
		t.Fatalf("health child_io_mode = %q, want %q", response.ChildIOMode, ChildIOModeStdio)
	}
	if !response.LegacyTransport || !response.Deprecated || response.EnsureSupported {
		t.Fatalf("health stdio semantics = legacy:%t deprecated:%t ensure:%t, want deprecated legacy without new ensure support", response.LegacyTransport, response.Deprecated, response.EnsureSupported)
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

func (tc *helperTestCase) waitForPTYWritten(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := tc.pty.Written()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child stdin writes = %q, want %q", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
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

func acceptChildConn(t *testing.T, accepted <-chan *websocket.Conn, listenErr <-chan error, socketPath string) *websocket.Conn {
	t.Helper()
	select {
	case conn, ok := <-accepted:
		if !ok {
			t.Fatal("child socket websocket upgrade failed")
		}
		return conn
	case err := <-listenErr:
		t.Fatalf("Listen(%q) error = %v", socketPath, err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for helper to connect to child socket %q", socketPath)
	}
	return nil
}

func sendHelperCommand(t *testing.T, enc *json.Encoder, dec *json.Decoder, sessionID session.SessionID, generationID GenerationID, commandID string, payload string) {
	t.Helper()
	packet, err := NewCommandPacket(sessionID, generationID, CommandSend, mustCommandID(t, commandID), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("NewCommandPacket() error = %v", err)
	}
	if err := enc.Encode(packet); err != nil {
		t.Fatalf("encode command error = %v", err)
	}
	var response CommandAcceptedPacket
	if err := decodeWithin(t, dec, &response); err != nil {
		t.Fatalf("decode accepted error = %v", err)
	}
}

func assertChildMessage(t *testing.T, conn *websocket.Conn, want string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	messageType, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read child socket websocket command error = %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("child socket message type = %d, want text", messageType)
	}
	if got := string(msg); got != want {
		t.Fatalf("child socket command = %q, want %q", got, want)
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

func TestUTF8ChunkDecoderPreservesSplitRunes(t *testing.T) {
	var decoder utf8ChunkDecoder
	input := []byte("审计结果写入完成")
	var out string
	out += decoder.Append(input[:1])
	out += decoder.Append(input[1:5])
	out += decoder.Append(input[5:8])
	out += decoder.Append(input[8:])
	out += decoder.Flush()
	if out != string(input) {
		t.Fatalf("decoded output = %q, want %q", out, string(input))
	}
	if strings.ContainsRune(out, '\uFFFD') {
		t.Fatalf("decoded output contains replacement rune: %q", out)
	}
}

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
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
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
func (h *testHandle) Stdin() io.WriteCloser    { return h.stdin }
func (h *testHandle) Stdout() io.ReadCloser    { return h.stdout }
func (h *testHandle) Stderr() io.ReadCloser    { return h.stderr }
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

type testStdio struct {
	stdinReader  *io.PipeReader
	stdin        *recordingWriteCloser
	stdout       *io.PipeReader
	stdoutWriter *io.PipeWriter
	stderr       *io.PipeReader
	stderrWriter *io.PipeWriter
}

func newTestStdio() *testStdio {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	return &testStdio{
		stdinReader:  stdinReader,
		stdin:        &recordingWriteCloser{writer: stdinWriter},
		stdout:       stdoutReader,
		stdoutWriter: stdoutWriter,
		stderr:       stderrReader,
		stderrWriter: stderrWriter,
	}
}

func (s *testStdio) Written() string {
	return s.stdin.Written()
}

func (s *testStdio) close() {
	_ = s.stdin.Close()
	_ = s.stdinReader.Close()
	_ = s.stdout.Close()
	_ = s.stdoutWriter.Close()
	_ = s.stderr.Close()
	_ = s.stderrWriter.Close()
}

type recordingWriteCloser struct {
	writer *io.PipeWriter
	mu     sync.Mutex
	writes bytes.Buffer
}

func (w *recordingWriteCloser) Write(data []byte) (int, error) {
	w.mu.Lock()
	_, _ = w.writes.Write(data)
	w.mu.Unlock()
	return len(data), nil
}

func (w *recordingWriteCloser) Close() error {
	return w.writer.Close()
}

func (w *recordingWriteCloser) Written() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes.String()
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
