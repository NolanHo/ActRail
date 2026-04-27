package iod

import (
	"bytes"
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

	"actrail/internal/adapters/process"
	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

const DefaultProtocolVersion = 1

type HelperOptions struct {
	SessionID        session.SessionID
	GenerationID     GenerationID
	Paths            GenerationPaths
	Command          process.Command
	CWD              string
	Environment      process.Environment
	PTYSize          process.PTYSize
	ProtocolVersion  int
	Runner           process.Runner
	Now              func() time.Time
	OnChildExitStart func()
}

type Helper struct {
	sessionID       session.SessionID
	generationID    GenerationID
	paths           GenerationPaths
	launchSpec      process.LaunchSpec
	protocolVersion int
	runner          process.Runner
	now             func() time.Time

	wal      *WAL
	listener net.Listener
	handle   process.Handle
	proof    HelloProof
	manifest GenerationManifest

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

// turnCommitPayload is the helper-owned durable payload for one committed
// assistant turn. App-side recovery should consume this fact directly instead
// of reparsing raw PI JSON output.
type turnCommitPayload struct {
	TurnID        string          `json:"turn_id,omitempty"`
	MessageID     string          `json:"message_id,omitempty"`
	Role          pi.MessageRole  `json:"role"`
	Text          string          `json:"text"`
	Class         pi.MessageClass `json:"class,omitempty"`
	StopReason    string          `json:"stop_reason,omitempty"`
	ToolCallCount int             `json:"tool_call_count,omitempty"`
	ThinkingCount int             `json:"thinking_count,omitempty"`
	Timestamp     float64         `json:"timestamp,omitempty"`
}

// uiRequestOpenedPayload is the helper-owned durable payload for one opened PI
// UI request. App-side recovery should consume this fact directly instead of
// rebuilding pending-request state from raw output text.
type uiRequestOpenedPayload struct {
	TurnID    string       `json:"turn_id,omitempty"`
	Timestamp float64      `json:"timestamp,omitempty"`
	Request   pi.UIRequest `json:"request"`
}

type uiResponseSubmitCommandPayload struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Value string `json:"value"`
}

// uiResponseForwardedPayload is the helper-owned durable payload for one UI
// response that was written to the child boundary.
type uiResponseForwardedPayload struct {
	CommandID   CommandID `json:"command_id"`
	RequestID   string    `json:"request_id"`
	Value       string    `json:"value"`
	ForwardedAt time.Time `json:"forwarded_at"`
}

type piRuntimeLineBuffer struct {
	pending bytes.Buffer
}

type piSemanticState struct {
	committedTurns   map[string]struct{}
	openedUIRequests map[string]struct{}
	lines            piRuntimeLineBuffer
}

func newPISemanticState() piSemanticState {
	return piSemanticState{
		committedTurns:   make(map[string]struct{}),
		openedUIRequests: make(map[string]struct{}),
	}
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
	ioSpec, err := process.PTYIO(size, process.LogPaths{})
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
		protocolVersion:  protocolVersion,
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
	handle, err := h.runner.Start(ctx, h.launchSpec)
	if err != nil {
		return fmt.Errorf("start helper child: %w", err)
	}
	if handle == nil {
		return fmt.Errorf("start helper child: nil process handle")
	}
	if handle.PTY() == nil {
		return fmt.Errorf("helper child must expose PTY transport")
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
			IO:       "pty",
			Argv:     h.launchSpec.Command().Argv(),
			CWD:      h.launchSpec.CWD().String(),
		}); err != nil {
			return err
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	h.wg.Add(3)
	go h.acceptLoop(runCtx)
	go h.commandLoop(runCtx)
	go h.readPTY(runCtx)
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
		if pty := h.handle.PTY(); pty != nil {
			_ = pty.Close()
		}
		if !h.isChildExited() {
			_ = h.handle.Kill()
		}
	}
	return nil
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
	writer := h.handle.PTY()
	if writer == nil {
		return fmt.Errorf("helper child input is unavailable")
	}
	data := append([]byte(nil), cmd.payload...)
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if cmd.kind == PacketCommandUIResponseSubmit {
		if err := h.emitUIResponseForwarded(cmd); err != nil {
			return err
		}
	}
	return nil
}

func (h *Helper) readPTY(ctx context.Context) {
	defer h.wg.Done()
	pty := h.handle.PTY()
	if pty == nil {
		_ = h.emitGenerationBreak(GenerationBreakAttachLost)
		return
	}
	semantic := newPISemanticState()
	buf := make([]byte, 4096)
	for {
		n, err := pty.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			_, _ = h.emitStateRecord(WALRecordOutputDelta, terminalOutputPayload{Stream: "pty", Data: chunk})
			h.emitPISemantics(&semantic, chunk)
		}
		if err != nil {
			if err == io.EOF || errors.Is(err, os.ErrClosed) || ctx.Err() != nil {
				return
			}
			if isPostExitPTYReadError(err) {
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

func (h *Helper) emitPISemantics(state *piSemanticState, chunk string) {
	if state == nil || chunk == "" {
		return
	}
	state.lines.append(chunk)
	for {
		line, ok := state.lines.nextLine()
		if !ok {
			return
		}
		h.emitPISemanticsFromLine(state, line)
	}
}

func (h *Helper) emitPISemanticsFromLine(state *piSemanticState, line []byte) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return
	}
	material, err := pi.ParseObjectJSON(trimmed)
	if err != nil {
		return
	}
	for _, event := range material.Events {
		h.emitPISemanticEvent(state, event)
	}
}

func (h *Helper) emitPISemanticEvent(state *piSemanticState, event pi.Event) {
	if state == nil {
		return
	}
	if event.Message != nil && event.Kind == pi.EventKindMessage && event.Message.Role == pi.MessageRoleAssistant && event.Message.CommitLike {
		turnID := helperTurnID(event)
		if turnID != "" {
			if _, ok := state.committedTurns[turnID]; !ok {
				state.committedTurns[turnID] = struct{}{}
				_, _ = h.emitStateRecord(WALRecordTurnCommit, turnCommitPayload{
					TurnID:        turnID,
					MessageID:     strings.TrimSpace(event.Message.ID),
					Role:          event.Message.Role,
					Text:          event.Message.Text,
					Class:         event.Message.Class,
					StopReason:    event.Message.StopReason,
					ToolCallCount: event.Message.ToolCallCount,
					ThinkingCount: event.Message.ThinkingCount,
					Timestamp:     event.Timestamp,
				})
			}
		}
	}
	if event.UIRequest != nil && event.Kind == pi.EventKindUIRequest {
		requestID := helperUIRequestID(event)
		if requestID == "" {
			return
		}
		if _, ok := state.openedUIRequests[requestID]; ok {
			return
		}
		state.openedUIRequests[requestID] = struct{}{}
		request := *event.UIRequest
		_, _ = h.emitStateRecord(WALRecordUIRequestOpened, uiRequestOpenedPayload{
			TurnID:    helperTurnID(event),
			Timestamp: event.Timestamp,
			Request:   request,
		})
	}
}

func (h *Helper) emitUIResponseForwarded(cmd queuedCommand) error {
	var payload uiResponseSubmitCommandPayload
	if err := json.Unmarshal(cmd.payload, &payload); err != nil {
		return nil
	}
	_, err := h.emitStateRecord(WALRecordUIResponseForwarded, uiResponseForwardedPayload{
		CommandID:   cmd.commandID,
		RequestID:   strings.TrimSpace(payload.ID),
		Value:       strings.TrimSpace(payload.Value),
		ForwardedAt: h.now().UTC(),
	})
	return err
}

func helperTurnID(event pi.Event) string {
	for _, candidate := range []string{
		strings.TrimSpace(event.TurnID),
		strings.TrimSpace(event.RawID),
		strings.TrimSpace(event.ParentID),
	} {
		if candidate != "" {
			return candidate
		}
	}
	if event.Message != nil {
		if messageID := strings.TrimSpace(event.Message.ID); messageID != "" {
			return messageID
		}
	}
	if event.Timestamp > 0 {
		return fmt.Sprintf("turn_%d", int64(event.Timestamp*1000))
	}
	return ""
}

func helperUIRequestID(event pi.Event) string {
	if event.UIRequest == nil {
		return ""
	}
	for _, candidate := range []string{
		strings.TrimSpace(event.UIRequest.RequestID),
		strings.TrimSpace(event.RawID),
		strings.TrimSpace(event.ParentID),
	} {
		if candidate != "" {
			return candidate
		}
	}
	return helperTurnID(event)
}

func (b *piRuntimeLineBuffer) append(chunk string) {
	if b == nil || chunk == "" {
		return
	}
	_, _ = b.pending.WriteString(chunk)
}

func (b *piRuntimeLineBuffer) nextLine() ([]byte, bool) {
	if b == nil {
		return nil, false
	}
	data := b.pending.Bytes()
	idx := bytes.IndexByte(data, '\n')
	if idx < 0 {
		return nil, false
	}
	line := append([]byte(nil), data[:idx]...)
	b.pending.Next(idx + 1)
	return line, true
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
