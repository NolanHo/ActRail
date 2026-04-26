//go:build unix

package iod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

func TestCleanChildExitRealPTYRace(t *testing.T) {
	tc := newRealPTYHelperTestCase(t, 500*time.Millisecond, "sleep-exit", "50")
	defer tc.stop()

	waitForDialFailure(t, tc.paths.ControlSocketPath, 300*time.Millisecond)

	childExit := tc.mustNextStatePacket(t, FactChildExit)
	if childExit.Fact.Seq != nil {
		t.Fatalf("child exit seq = %#v, want nil", childExit.Fact.Seq)
	}

	helperExit := tc.mustNextStatePacket(t, FactHelperExit)
	if helperExit.Fact.Seq != nil {
		t.Fatalf("helper exit seq = %#v, want nil", helperExit.Fact.Seq)
	}

	tc.mustConnClosed(t)
	tc.stop()

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

	if _, err := os.Stat(tc.paths.ControlSocketPath); !os.IsNotExist(err) {
		t.Fatalf("control socket stat error = %v, want not exist", err)
	}
	if conn, err := net.Dial("unix", tc.paths.ControlSocketPath); err == nil {
		_ = conn.Close()
		t.Fatal("helper remained reattachable after real PTY child exit")
	}
}

func TestIodHelperRealPTYChildProcess(t *testing.T) {
	if os.Getenv("GO_WANT_IOD_REAL_PTY_HELPER") != "1" {
		return
	}
	args := iodHelperArgs(os.Args)
	if len(args) == 0 {
		_, _ = io.WriteString(os.Stderr, "missing helper mode")
		os.Exit(2)
	}
	switch args[0] {
	case "sleep-exit":
		delay := 0
		if len(args) > 1 {
			parsed, err := strconv.Atoi(args[1])
			if err != nil {
				_, _ = io.WriteString(os.Stderr, err.Error())
				os.Exit(2)
			}
			delay = parsed
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
		os.Exit(0)
	default:
		_, _ = io.WriteString(os.Stderr, fmt.Sprintf("unknown mode %q", args[0]))
		os.Exit(2)
	}
}

type realPTYHelperTestCase struct {
	t            *testing.T
	sessionID    session.SessionID
	generationID GenerationID
	paths        GenerationPaths
	conn         net.Conn
	dec          *json.Decoder
	cancel       context.CancelFunc
	errCh        chan error
	stopOnce     sync.Once
}

func newRealPTYHelperTestCase(t *testing.T, waitDelay time.Duration, mode string, args ...string) *realPTYHelperTestCase {
	t.Helper()
	sessionID := mustSessionID(t, "s_real_pty")
	generationID := mustGenerationID(t, "g_real_pty")
	paths, err := NewGenerationPaths(t.TempDir(), sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	childFlag, err := process.NewEnvVar("GO_WANT_IOD_REAL_PTY_HELPER", "1")
	if err != nil {
		t.Fatalf("process.NewEnvVar() error = %v", err)
	}
	env, err := process.InheritEnv(childFlag)
	if err != nil {
		t.Fatalf("process.InheritEnv() error = %v", err)
	}
	command := iodHelperCommand(t, mode, args...)
	runner := &delayedWaitRunner{base: process.NewExecRunner(), delay: waitDelay}
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
			Now:             func() time.Time { return time.Unix(1760000000, 0).UTC() },
		})
	}()
	waitForSocket(t, paths.ControlSocketPath)
	conn, err := net.Dial("unix", paths.ControlSocketPath)
	if err != nil {
		cancel()
		t.Fatalf("net.Dial(unix) error = %v", err)
	}
	dec := json.NewDecoder(conn)
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
	return &realPTYHelperTestCase{t: t, sessionID: sessionID, generationID: generationID, paths: paths, conn: conn, dec: dec, cancel: cancel, errCh: errCh}
}

func (tc *realPTYHelperTestCase) stop() {
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

func (tc *realPTYHelperTestCase) mustNextStatePacket(t *testing.T, want FactKind) StatePacket {
	t.Helper()
	raw, err := decodeRawWithin(t, tc.dec)
	if err != nil {
		t.Fatalf("decode raw packet error = %v", err)
	}
	var peek struct {
		Kind PacketKind `json:"kind"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		t.Fatalf("json.Unmarshal(packet peek) error = %v", err)
	}
	if peek.Kind != PacketState {
		t.Fatalf("next packet kind = %q, want %q", peek.Kind, PacketState)
	}
	var packet StatePacket
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatalf("json.Unmarshal(state packet) error = %v", err)
	}
	if packet.Fact.FactKind != want {
		t.Fatalf("state fact kind = %q, want %q", packet.Fact.FactKind, want)
	}
	return packet
}

func (tc *realPTYHelperTestCase) mustConnClosed(t *testing.T) {
	t.Helper()
	var packet json.RawMessage
	if err := decodeWithin(t, tc.dec, &packet); err == nil {
		t.Fatalf("decode after helper exit = %#v, want connection closed", packet)
	} else if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("decode after helper exit error = %v, want EOF", err)
	}
}

type delayedWaitRunner struct {
	base  process.Runner
	delay time.Duration
}

func (r *delayedWaitRunner) Start(ctx context.Context, spec process.LaunchSpec) (process.Handle, error) {
	handle, err := r.base.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &delayedWaitHandle{Handle: handle, delay: r.delay}, nil
}

type delayedWaitHandle struct {
	process.Handle
	delay time.Duration
}

func (h *delayedWaitHandle) Wait(ctx context.Context) (process.ExitStatus, error) {
	if h.delay > 0 {
		timer := time.NewTimer(h.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return process.ExitStatus{}, ctx.Err()
		case <-timer.C:
		}
	}
	return h.Handle.Wait(ctx)
}

func iodHelperCommand(t *testing.T, mode string, args ...string) process.Command {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	argv := []string{"-test.run=TestIodHelperRealPTYChildProcess", "--", mode}
	argv = append(argv, args...)
	cmd, err := process.NewCommand(bin, argv...)
	if err != nil {
		t.Fatalf("process.NewCommand() error = %v", err)
	}
	return cmd
}

func iodHelperArgs(argv []string) []string {
	for i, arg := range argv {
		if arg == "--" && i+1 < len(argv) {
			return argv[i+1:]
		}
	}
	return nil
}

func waitForDialFailure(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("control socket %q remained reattachable for %v", path, timeout)
}
