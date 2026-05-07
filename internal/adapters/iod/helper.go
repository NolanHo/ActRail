package iod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

const DefaultProtocolVersion = 1

type ChildIOMode string

const (
	ChildIOModePTY   ChildIOMode = "pty"
	ChildIOModeStdio ChildIOMode = "stdio"
	ChildIOModeUnix  ChildIOMode = "unix"
)

func ParseChildIOMode(raw string) (ChildIOMode, error) {
	mode := ChildIOMode(strings.ToLower(strings.TrimSpace(raw)))
	switch mode {
	case ChildIOModePTY, ChildIOModeStdio, ChildIOModeUnix:
		return mode, nil
	case "":
		return ChildIOModePTY, nil
	default:
		return "", fmt.Errorf("child io mode %q is not supported", raw)
	}
}

type HelperOptions struct {
	SessionID          session.SessionID
	GenerationID       GenerationID
	Paths              GenerationPaths
	Command            process.Command
	CWD                string
	Environment        process.Environment
	PTYSize            process.PTYSize
	ChildIOMode        ChildIOMode
	ProtocolVersion    int
	SessionHistoryPath string
	BuildDate          string
	GitSHA             string
	Runner             process.Runner
	Now                func() time.Time
	OnChildExitStart   func()
}

type Helper struct {
	sessionID       session.SessionID
	generationID    GenerationID
	paths           GenerationPaths
	launchSpec      process.LaunchSpec
	childIOMode     ChildIOMode
	protocolVersion int
	buildDate       string
	gitSHA          string
	runner          process.Runner
	now             func() time.Time

	wal       *WAL
	listener  net.Listener
	handle    process.Handle
	childConn net.Conn
	proof     HelloProof
	manifest  GenerationManifest
	history   *sessionHistoryCache

	mu                sync.Mutex
	conns             map[*helperConn]struct{}
	outcomes          map[CommandID]storedOutcome
	broken            bool
	childExited       bool
	childExitStarted  atomic.Bool
	closed            bool
	closeListenerOnce sync.Once
	childResolved     chan struct{}

	commands            chan queuedCommand
	beforeResolveAppend func(CommandID)
	onChildExitStart    func()
	wg                  sync.WaitGroup
}

type helperConn struct {
	conn net.Conn
	enc  *json.Encoder
	mu   sync.Mutex
}

type storedOutcome struct {
	accepted bool
	ack      WALOffset
	payload  json.RawMessage
}

type queuedCommand struct {
	commandID CommandID
	kind      PacketKind
	payload   json.RawMessage
}

type helperStartPayload struct {
	ProtocolVersion int       `json:"protocol_version"`
	HelperPID       int       `json:"helper_pid"`
	ChildPID        *int      `json:"child_pid,omitempty"`
	WALPath         string    `json:"wal_path"`
	SocketPath      string    `json:"control_socket_path"`
	StartTS         float64   `json:"start_ts"`
	StartedAt       time.Time `json:"started_at"`
}

type attachEstablishedPayload struct {
	ChildPID int      `json:"child_pid"`
	IO       string   `json:"io"`
	Argv     []string `json:"argv"`
	CWD      string   `json:"cwd"`
}

type terminalOutputPayload struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type commandFactPayload struct {
	CommandID   CommandID  `json:"command_id"`
	CommandKind PacketKind `json:"command_kind"`
	Reason      string     `json:"reason,omitempty"`
}

type childExitPayload struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
}

type helperExitPayload struct {
	Reason string `json:"reason"`
}

type generationBreakPayload struct {
	Reason GenerationBreakReason `json:"reason"`
}

type inboundEnvelope struct {
	SessionID    session.SessionID `json:"session_id"`
	GenerationID GenerationID      `json:"generation_id"`
	Kind         PacketKind        `json:"kind"`
}

func NewHelper(opts HelperOptions) (*Helper, error) {
	if err := opts.SessionID.Validate(); err != nil {
		return nil, err
	}
	if opts.SessionID.IsHistorical() {
		return nil, fmt.Errorf("session id %q cannot use historical replay identity", opts.SessionID)
	}
	if err := opts.GenerationID.Validate(); err != nil {
		return nil, err
	}
	if err := opts.Paths.Validate(); err != nil {
		return nil, err
	}
	if err := opts.Command.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.CWD) == "" {
		return nil, fmt.Errorf("helper cwd is required")
	}
	env := opts.Environment
	if env.Mode() == "" && len(env.Vars()) == 0 {
		var err error
		env, err = process.InheritEnv()
		if err != nil {
			return nil, err
		}
	}
	if err := env.Validate(); err != nil {
		return nil, err
	}
	size := opts.PTYSize
	if size.Rows == 0 || size.Cols == 0 {
		size = process.PTYSize{Rows: 24, Cols: 80}
	}
	if err := size.Validate(); err != nil {
		return nil, err
	}
	childIOMode := opts.ChildIOMode
	if childIOMode == "" {
		childIOMode = ChildIOModePTY
	}
	if _, err := ParseChildIOMode(string(childIOMode)); err != nil {
		return nil, err
	}
	var ioSpec process.IO
	var err error
	if childIOMode == ChildIOModeStdio || childIOMode == ChildIOModeUnix {
		ioSpec, err = process.PipeIO(process.LogPaths{})
	} else {
		ioSpec, err = process.PTYIO(size, process.LogPaths{})
	}
	if err != nil {
		return nil, err
	}
	launchSpec, err := process.NewLaunchSpec(opts.Command, opts.CWD, env, ioSpec)
	if err != nil {
		return nil, err
	}
	protocolVersion := opts.ProtocolVersion
	if protocolVersion <= 0 {
		protocolVersion = DefaultProtocolVersion
	}
	runner := opts.Runner
	if runner == nil {
		runner = process.NewExecRunner()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Helper{
		sessionID:        opts.SessionID,
		generationID:     opts.GenerationID,
		paths:            opts.Paths,
		launchSpec:       launchSpec,
		childIOMode:      childIOMode,
		protocolVersion:  protocolVersion,
		buildDate:        strings.TrimSpace(opts.BuildDate),
		gitSHA:           strings.TrimSpace(opts.GitSHA),
		history:          newSessionHistoryCache(opts.SessionHistoryPath),
		runner:           runner,
		now:              now,
		conns:            make(map[*helperConn]struct{}),
		outcomes:         make(map[CommandID]storedOutcome),
		childResolved:    make(chan struct{}),
		commands:         make(chan queuedCommand, 128),
		onChildExitStart: opts.OnChildExitStart,
	}, nil
}

func (h *Helper) Run(ctx context.Context) error {
	if h == nil {
		return fmt.Errorf("helper is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := h.paths.EnsureDir(); err != nil {
		return err
	}
	_ = os.Remove(h.paths.ControlSocketPath)
	listener, err := net.Listen("unix", h.paths.ControlSocketPath)
	if err != nil {
		return fmt.Errorf("listen control socket: %w", err)
	}
	h.listener = listener
	defer func() {
		_ = listener.Close()
		_ = os.Remove(h.paths.ControlSocketPath)
	}()
	wal, err := OpenWAL(h.paths.WALPath, h.sessionID, h.generationID)
	if err != nil {
		return err
	}
	h.wal = wal
	defer wal.Close()
	if h.history != nil {
		h.history.Start(ctx)
		defer h.history.Stop()
	}
	if h.childIOMode == ChildIOModeUnix {
		_ = os.Remove(h.paths.ChildSocketPath)
		defer func() {
			_ = os.Remove(h.paths.ChildSocketPath)
		}()
	}
	handle, err := h.runner.Start(ctx, h.launchSpec)
	if err != nil {
		return fmt.Errorf("start helper child: %w", err)
	}
	if handle == nil {
		return fmt.Errorf("start helper child: nil process handle")
	}
	if h.childIOMode == ChildIOModePTY && handle.PTY() == nil {
		return fmt.Errorf("helper child must expose PTY transport")
	}
	if h.childIOMode == ChildIOModeStdio && (handle.Stdin() == nil || handle.Stdout() == nil) {
		return fmt.Errorf("helper child must expose stdio transport")
	}
	if h.childIOMode == ChildIOModeUnix {
		conn, err := h.dialChildSocket(ctx)
		if err != nil {
			return err
		}
		h.childConn = conn
	}
	h.handle = handle
	start := h.now().UTC()
	startTS := float64(start.UnixNano()) / float64(time.Second)
	var childPID *int
	if pid := handle.PID(); pid > 0 {
		child := pid
		childPID = &child
	}
	proof, err := NewHelloProof(os.Getpid(), childPID, h.paths.WALPath, h.paths.ControlSocketPath, startTS)
	if err != nil {
		return err
	}
	manifest, err := NewGenerationManifest(h.sessionID, h.generationID, proof)
	if err != nil {
		return err
	}
	if h.history != nil {
		manifest.SessionHistoryPath = h.history.path
	}
	if err := WriteGenerationManifest(h.paths.ManifestPath, manifest); err != nil {
		return err
	}
	h.proof = proof
	h.manifest = manifest
	if _, err := h.wal.Append(WALRecordHelperStart, helperStartPayload{
		ProtocolVersion: h.protocolVersion,
		HelperPID:       proof.HelperPID,
		ChildPID:        proof.ChildPID,
		WALPath:         proof.WALPath,
		SocketPath:      proof.ControlSocketPath,
		StartTS:         proof.StartTS,
		StartedAt:       start,
	}); err != nil {
		return err
	}
	if childPID != nil {
		if _, err := h.wal.Append(WALRecordAttachEstablished, attachEstablishedPayload{
			ChildPID: *childPID,
			IO:       string(h.childIOMode),
			Argv:     h.launchSpec.Command().Argv(),
			CWD:      h.launchSpec.CWD().String(),
		}); err != nil {
			return err
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	h.wg.Add(2)
	go h.acceptLoop(runCtx)
	go h.commandLoop(runCtx)
	if h.childIOMode == ChildIOModeStdio {
		h.wg.Add(2)
		go h.readPipe(runCtx, "stdout", h.handle.Stdout())
		go h.readPipe(runCtx, "stderr", h.handle.Stderr())
	} else if h.childIOMode == ChildIOModeUnix {
		h.wg.Add(1)
		go h.readChildSocket(runCtx)
	} else {
		h.wg.Add(1)
		go h.readPTY(runCtx)
	}
	childDone := make(chan struct{})
	go func() {
		defer close(childDone)
		h.waitChild()
	}()

	select {
	case <-ctx.Done():
		cancel()
	case <-childDone:
		cancel()
	}
	if err := h.shutdown(); err != nil {
		return err
	}

	h.wg.Wait()
	return nil
}

func (h *Helper) shutdown() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()
	if !h.isBroken() && !h.isChildExitStarted() {
		_ = h.emitGenerationBreak(GenerationBreakHelperExit)
	}
	if _, err := h.emitStateRecord(WALRecordHelperExit, helperExitPayload{Reason: GenerationBreakHelperExit.String()}); err != nil {
		return err
	}
	h.closeListener()
	for _, conn := range h.connSnapshot() {
		h.closeConn(conn)
	}
	if h.handle != nil {
		if stdin := h.handle.Stdin(); stdin != nil {
			_ = stdin.Close()
		}
		if stdout := h.handle.Stdout(); stdout != nil {
			_ = stdout.Close()
		}
		if stderr := h.handle.Stderr(); stderr != nil {
			_ = stderr.Close()
		}
		if pty := h.handle.PTY(); pty != nil {
			_ = pty.Close()
		}
		if h.childConn != nil {
			_ = h.childConn.Close()
		}
		if !h.isChildExited() {
			_ = h.handle.Kill()
		}
	}
	return nil
}

func (h *Helper) dialChildSocket(ctx context.Context) (net.Conn, error) {
	path := strings.TrimSpace(h.paths.ChildSocketPath)
	if path == "" {
		return nil, fmt.Errorf("child socket path is required")
	}
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "unix", path)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("dial child socket %q: %w", path, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("dial child socket %q: %w", path, lastErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *Helper) acceptLoop(ctx context.Context) {
	defer h.wg.Done()
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		hc := &helperConn{conn: conn, enc: json.NewEncoder(conn)}
		h.addConn(hc)
		if err := h.sendHello(hc); err != nil {
			h.closeConn(hc)
			continue
		}
		h.wg.Add(1)
		go h.handleConn(ctx, hc)
	}
}

func (h *Helper) handleConn(ctx context.Context, hc *helperConn) {
	defer h.wg.Done()
	defer h.closeConn(hc)
	dec := json.NewDecoder(hc.conn)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF || errors.Is(err, net.ErrClosed) {
				return
			}
			_ = h.sendError(hc, true, ErrorMalformedEnvelope, err.Error(), nil)
			return
		}
		if err := h.dispatch(hc, raw); err != nil {
			return
		}
	}
}

func (h *Helper) dispatch(hc *helperConn, raw json.RawMessage) error {
	var env inboundEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return h.sendError(hc, true, ErrorMalformedEnvelope, err.Error(), nil)
	}
	if env.SessionID != h.sessionID || env.GenerationID != h.generationID {
		return h.sendError(hc, true, ErrorGenerationNotCurrent, "command targeted a stale generation", nil)
	}
	if err := env.Kind.Validate(); err != nil {
		return h.sendError(hc, true, ErrorMalformedEnvelope, err.Error(), nil)
	}
	switch env.Kind {
	case PacketReplayRequest:
		var request ReplayRequestPacket
		if err := json.Unmarshal(raw, &request); err != nil {
			return h.sendError(hc, true, ErrorMalformedEnvelope, err.Error(), nil)
		}
		return h.handleReplay(hc, request)
	case PacketSessionHistoryRequest:
		var request SessionHistoryRequestPacket
		if err := json.Unmarshal(raw, &request); err != nil {
			return h.sendError(hc, true, ErrorMalformedEnvelope, err.Error(), nil)
		}
		return h.handleSessionHistory(hc, request)
	case PacketCommandSend, PacketCommandEnqueue, PacketCommandInterrupt, PacketCommandUIResponseSubmit:
		var packet CommandPacket
		if err := json.Unmarshal(raw, &packet); err != nil {
			return h.sendError(hc, true, ErrorMalformedEnvelope, err.Error(), nil)
		}
		return h.handleCommand(hc, packet)
	default:
		return h.sendError(hc, true, ErrorUnsupportedCommandKind, fmt.Sprintf("packet kind %q is not supported by helper", env.Kind), nil)
	}
}

func (h *Helper) handleSessionHistory(hc *helperConn, request SessionHistoryRequestPacket) error {
	if err := request.Validate(); err != nil {
		return h.sendError(hc, true, ErrorMalformedEnvelope, err.Error(), nil)
	}
	var snapshot SessionHistorySnapshot
	if h.history != nil {
		var err error
		snapshot, err = h.history.Snapshot(context.Background())
		if err != nil {
			return h.sendError(hc, true, ErrorHelperBroken, err.Error(), nil)
		}
	}
	packet, err := NewSessionHistoryResponsePacket(h.sessionID, h.generationID, snapshot)
	if err != nil {
		return err
	}
	return h.writePacket(hc, packet)
}

func (h *Helper) handleReplay(hc *helperConn, request ReplayRequestPacket) error {
	replay, err := h.wal.Replay(request.AfterOffset)
	if err != nil {
		return h.sendError(hc, true, ErrorReplayCursorInvalid, err.Error(), nil)
	}
	for _, record := range replay.Records {
		fact, err := helperFactFromRecord(record)
		if err != nil {
			return h.sendError(hc, false, ErrorReplayCorruptTail, err.Error(), nil)
		}
		item, err := NewReplayItem(record.Header.Offset, fact)
		if err != nil {
			return h.sendError(hc, false, ErrorReplayCorruptTail, err.Error(), nil)
		}
		packet, err := NewReplayItemPacket(h.sessionID, h.generationID, item)
		if err != nil {
			return h.sendError(hc, false, ErrorReplayCorruptTail, err.Error(), nil)
		}
		if err := h.writePacket(hc, packet); err != nil {
			return err
		}
	}
	done, err := NewReplayDonePacket(h.sessionID, h.generationID, request.AfterOffset, replay.LastOffset, replay.CorruptTail)
	if err != nil {
		return err
	}
	return h.writePacket(hc, done)
}

func (h *Helper) handleCommand(hc *helperConn, packet CommandPacket) error {
	outcomeKind, outcome, queue, err := h.resolveCommand(packet)
	if err != nil {
		return h.sendError(hc, false, ErrorHelperBroken, err.Error(), &packet.CommandID)
	}
	if outcomeKind == PacketCommandAccepted {
		response, err := NewCommandAcceptedPacket(h.sessionID, h.generationID, outcome)
		if err != nil {
			return err
		}
		if err := h.writePacket(hc, response); err != nil {
			return err
		}
		if queue {
			select {
			case h.commands <- queuedCommand{commandID: packet.CommandID, kind: packet.Kind, payload: packet.Payload}:
			default:
				if err := h.emitGenerationBreak(GenerationBreakWriteFailed); err != nil {
					return h.sendError(hc, false, ErrorHelperBroken, err.Error(), &packet.CommandID)
				}
			}
		}
		return nil
	}
	response, err := NewCommandRejectedPacket(h.sessionID, h.generationID, outcome)
	if err != nil {
		return err
	}
	return h.writePacket(hc, response)
}

func (h *Helper) resolveCommand(packet CommandPacket) (PacketKind, CommandOutcome, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if outcome, ok := h.outcomes[packet.CommandID]; ok {
		resolved, err := NewCommandOutcome(packet.CommandID, outcome.ack, true, outcome.payload)
		if err != nil {
			return "", CommandOutcome{}, false, err
		}
		if outcome.accepted {
			return PacketCommandAccepted, resolved, false, nil
		}
		return PacketCommandRejected, resolved, false, nil
	}

	payload := commandFactPayload{CommandID: packet.CommandID, CommandKind: packet.Kind}
	if h.broken || h.childExited || h.childExitStarted.Load() {
		payload.Reason = rejectReason(h.broken, h.childExited || h.childExitStarted.Load())
		record, err := h.wal.Append(WALRecordCommandRejected, payload)
		if err != nil {
			return "", CommandOutcome{}, false, err
		}
		outcome, err := NewCommandOutcome(packet.CommandID, record.Header.Offset, false, record.Payload)
		if err != nil {
			return "", CommandOutcome{}, false, err
		}
		h.outcomes[packet.CommandID] = storedOutcome{accepted: false, ack: record.Header.Offset, payload: record.Payload}
		return PacketCommandRejected, outcome, false, nil
	}

	if h.beforeResolveAppend != nil {
		h.beforeResolveAppend(packet.CommandID)
	}
	record, err := h.wal.Append(WALRecordCommandAccepted, payload)
	if err != nil {
		return "", CommandOutcome{}, false, err
	}
	outcome, err := NewCommandOutcome(packet.CommandID, record.Header.Offset, false, record.Payload)
	if err != nil {
		return "", CommandOutcome{}, false, err
	}
	h.outcomes[packet.CommandID] = storedOutcome{accepted: true, ack: record.Header.Offset, payload: record.Payload}
	return PacketCommandAccepted, outcome, true, nil
}

func (h *Helper) commandLoop(ctx context.Context) {
	defer h.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case cmd, ok := <-h.commands:
			if !ok {
				return
			}
			if err := h.forwardCommand(cmd); err != nil {
				_ = h.emitGenerationBreak(GenerationBreakWriteFailed)
			}
		}
	}
}

func (h *Helper) forwardCommand(cmd queuedCommand) error {
	if h.handle == nil {
		return fmt.Errorf("helper child is unavailable")
	}
	if cmd.kind == PacketCommandInterrupt {
		return h.handle.Interrupt()
	}
	var writer io.Writer
	if h.childIOMode == ChildIOModeStdio {
		writer = h.handle.Stdin()
	} else if h.childIOMode == ChildIOModeUnix {
		writer = h.childConn
	} else {
		writer = h.handle.PTY()
	}
	if writer == nil {
		return fmt.Errorf("helper child input is unavailable")
	}
	data, err := normalizeCommandInputPayload(cmd.payload)
	if err != nil {
		return err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	return nil
}

func normalizeCommandInputPayload(payload json.RawMessage) ([]byte, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	if payload[0] != '"' {
		return append([]byte(nil), payload...), nil
	}
	var text string
	if err := json.Unmarshal(payload, &text); err != nil {
		return nil, fmt.Errorf("decode command input payload: %w", err)
	}
	return []byte(text), nil
}

type utf8ChunkDecoder struct {
	pending []byte
}

func (d *utf8ChunkDecoder) Append(chunk []byte) string {
	if len(chunk) == 0 {
		return ""
	}
	data := make([]byte, 0, len(d.pending)+len(chunk))
	data = append(data, d.pending...)
	data = append(data, chunk...)
	d.pending = d.pending[:0]

	cut := len(data)
	for back := 1; back <= minInt(utf8.UTFMax-1, len(data)); back++ {
		idx := len(data) - back
		if !utf8.RuneStart(data[idx]) {
			continue
		}
		if !utf8.FullRune(data[idx:]) {
			cut = idx
		}
		break
	}
	if cut < len(data) {
		d.pending = append(d.pending, data[cut:]...)
		data = data[:cut]
	}
	return string(data)
}

func (d *utf8ChunkDecoder) Flush() string {
	if len(d.pending) == 0 {
		return ""
	}
	out := string(d.pending)
	d.pending = d.pending[:0]
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (h *Helper) readPTY(ctx context.Context) {
	defer h.wg.Done()
	pty := h.handle.PTY()
	if pty == nil {
		_ = h.emitGenerationBreak(GenerationBreakAttachLost)
		return
	}
	h.readOutput(ctx, "pty", pty, true)
}

func (h *Helper) readPipe(ctx context.Context, stream string, reader io.Reader) {
	defer h.wg.Done()
	if reader == nil {
		if stream == "stdout" {
			_ = h.emitGenerationBreak(GenerationBreakAttachLost)
		}
		return
	}
	h.readOutput(ctx, stream, reader, false)
}

func (h *Helper) readChildSocket(ctx context.Context) {
	defer h.wg.Done()
	if h.childConn == nil {
		_ = h.emitGenerationBreak(GenerationBreakAttachLost)
		return
	}
	h.readOutput(ctx, "unix", h.childConn, false)
}

func (h *Helper) readOutput(ctx context.Context, stream string, reader io.Reader, pty bool) {
	buf := make([]byte, 4096)
	var decoder utf8ChunkDecoder
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := decoder.Append(buf[:n])
			if chunk != "" {
				_, _ = h.emitStateRecord(WALRecordOutputDelta, terminalOutputPayload{Stream: stream, Data: chunk})
			}
		}
		if err != nil {
			if tail := decoder.Flush(); tail != "" {
				_, _ = h.emitStateRecord(WALRecordOutputDelta, terminalOutputPayload{Stream: stream, Data: tail})
			}
			if err == io.EOF || errors.Is(err, os.ErrClosed) || ctx.Err() != nil {
				return
			}
			if pty && isPostExitPTYReadError(err) {
				h.beginChildExit()
				h.awaitChildResolution(ctx)
				return
			}
			if !h.isChildExited() && !h.isBroken() {
				_ = h.emitGenerationBreak(GenerationBreakAttachLost)
			}
			return
		}
	}
}

func (h *Helper) waitChild() {
	defer close(h.childResolved)
	status, err := h.handle.Wait(context.Background())
	if err != nil {
		if !h.isBroken() {
			_ = h.emitGenerationBreak(GenerationBreakAttachLost)
		}
		return
	}
	h.beginChildExit()
	h.mu.Lock()
	h.childExited = true
	h.mu.Unlock()
	_, _ = h.emitStateRecord(WALRecordChildExit, childExitPayload{Code: status.Code, Signal: status.Signal})
}

func (h *Helper) emitStateRecord(class WALRecordClass, payload any) (WALRecord, error) {
	record, err := h.wal.Append(class, payload)
	if err != nil {
		return WALRecord{}, err
	}
	fact, err := helperFactFromRecord(record)
	if err != nil {
		return WALRecord{}, err
	}
	packet, err := NewStatePacket(h.sessionID, h.generationID, fact)
	if err != nil {
		return WALRecord{}, err
	}
	h.broadcast(packet)
	return record, nil
}

func (h *Helper) emitGenerationBreak(reason GenerationBreakReason) error {
	h.mu.Lock()
	if h.broken {
		h.mu.Unlock()
		return nil
	}
	h.broken = true
	h.mu.Unlock()
	record, err := h.wal.Append(WALRecordGenerationBreak, generationBreakPayload{Reason: reason})
	if err != nil {
		return err
	}
	if record.Header.Seq == nil {
		return fmt.Errorf("generation break wal record missing seq")
	}
	packet, err := NewGenerationBreakPacket(h.sessionID, h.generationID, *record.Header.Seq, reason)
	if err != nil {
		return err
	}
	h.broadcast(packet)
	return nil
}

func helperFactFromRecord(record WALRecord) (HelperFact, error) {
	var seq *EventSeq
	if record.Header.Seq != nil {
		copySeq := *record.Header.Seq
		seq = &copySeq
	}
	return NewHelperFact(record.Header.Class.FactKind(), seq, record.Payload)
}

func rejectReason(broken, childExited bool) string {
	switch {
	case broken:
		return "generation_broken"
	case childExited:
		return "child_exited"
	default:
		return "rejected"
	}
}

func (h *Helper) sendHello(hc *helperConn) error {
	if h.isChildExitStarted() {
		return fmt.Errorf("child exit is underway")
	}
	packet, err := NewHelloPacket(h.sessionID, h.generationID, h.protocolVersion, h.proof)
	if err != nil {
		return err
	}
	packet.IODBuildDate = h.buildDate
	packet.IODGitSHA = h.gitSHA
	return h.writePacket(hc, packet)
}

func (h *Helper) sendError(hc *helperConn, recoverable bool, code ErrorCode, message string, commandID *CommandID) error {
	packet, err := NewErrorPacket(h.sessionID, h.generationID, recoverable, code, message, commandID)
	if err != nil {
		return err
	}
	return h.writePacket(hc, packet)
}

func (h *Helper) writePacket(hc *helperConn, packet any) error {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	_ = hc.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = hc.conn.SetWriteDeadline(time.Time{}) }()
	if err := hc.enc.Encode(packet); err != nil {
		return err
	}
	return nil
}

func (h *Helper) broadcast(packet any) {
	for _, conn := range h.connSnapshot() {
		if err := h.writePacket(conn, packet); err != nil {
			h.closeConn(conn)
		}
	}
}

func (h *Helper) connSnapshot() []*helperConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := make([]*helperConn, 0, len(h.conns))
	for conn := range h.conns {
		list = append(list, conn)
	}
	return list
}

func (h *Helper) addConn(conn *helperConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[conn] = struct{}{}
}

func (h *Helper) closeConn(conn *helperConn) {
	h.mu.Lock()
	if _, ok := h.conns[conn]; ok {
		delete(h.conns, conn)
	}
	h.mu.Unlock()
	_ = conn.conn.Close()
}

func (h *Helper) beginChildExit() {
	started := h.childExitStarted.CompareAndSwap(false, true)
	h.closeListener()
	if started && h.onChildExitStart != nil {
		h.onChildExitStart()
	}
}

func (h *Helper) closeListener() {
	if h.listener == nil {
		return
	}
	h.closeListenerOnce.Do(func() {
		_ = h.listener.Close()
		_ = os.Remove(h.paths.ControlSocketPath)
	})
}

func (h *Helper) awaitChildResolution(ctx context.Context) {
	select {
	case <-h.childResolved:
	case <-ctx.Done():
	}
}

func isPostExitPTYReadError(err error) bool {
	return errors.Is(err, syscall.EIO)
}

func (h *Helper) isBroken() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.broken
}

func (h *Helper) isChildExitStarted() bool {
	return h.childExitStarted.Load()
}

func (h *Helper) isChildExited() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.childExited
}

func RunHelper(ctx context.Context, opts HelperOptions) error {
	helper, err := NewHelper(opts)
	if err != nil {
		return err
	}
	return helper.Run(ctx)
}

func ResolveHelperCWD(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve helper cwd: %w", err)
		}
		return cwd, nil
	}
	resolved, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve helper cwd: %w", err)
	}
	return resolved, nil
}
