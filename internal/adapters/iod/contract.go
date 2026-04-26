package iod

import (
	"encoding/json"
	"fmt"
	"strings"

	"actrail/internal/domain/session"
)

// GenerationID identifies one helper-owned transport lifetime for one session.
// Server restart may reattach only while the same GenerationID still survives.
type GenerationID string

func NewGenerationID(raw string) (GenerationID, error) {
	value, err := normalizeToken(raw, "generation id")
	if err != nil {
		return "", err
	}
	return GenerationID(value), nil
}

func (id GenerationID) Validate() error {
	_, err := NewGenerationID(string(id))
	return err
}

func (id GenerationID) String() string {
	return string(id)
}

// CommandID dedupes accepted commands inside one helper generation.
type CommandID string

func NewCommandID(raw string) (CommandID, error) {
	value, err := normalizeToken(raw, "command id")
	if err != nil {
		return "", err
	}
	return CommandID(value), nil
}

func (id CommandID) Validate() error {
	_, err := NewCommandID(string(id))
	return err
}

func (id CommandID) String() string {
	return string(id)
}

// EventSeq is the browser-visible event sequence inside one generation.
// It starts at 1 for the first replayable event of the generation.
type EventSeq uint64

func (s EventSeq) Uint64() uint64 {
	return uint64(s)
}

func (s EventSeq) Validate() error {
	if s == 0 {
		return fmt.Errorf("event seq must be greater than zero")
	}
	return nil
}

// PacketKind freezes the wire-visible packet family names for actrail-iod.
type PacketKind string

const (
	PacketHello           PacketKind = "iod.hello"
	PacketState           PacketKind = "iod.state"
	PacketReplayRequest   PacketKind = "iod.replay.request"
	PacketReplayItem      PacketKind = "iod.replay.item"
	PacketReplayDone      PacketKind = "iod.replay.done"
	PacketGenerationBreak PacketKind = "iod.generation.break"
	PacketError           PacketKind = "iod.error"
)

const (
	packetCommandPrefix = "iod.command."
	packetEventPrefix   = "iod.event."
)

func ParsePacketKind(raw string) (PacketKind, error) {
	kind := PacketKind(strings.TrimSpace(raw))
	if err := kind.Validate(); err != nil {
		return "", err
	}
	return kind, nil
}

func (k PacketKind) Validate() error {
	switch k {
	case PacketHello, PacketState, PacketReplayRequest, PacketReplayItem, PacketReplayDone, PacketGenerationBreak, PacketError:
		return nil
	}
	if strings.HasPrefix(string(k), packetCommandPrefix) {
		_, err := ParseCommandName(strings.TrimPrefix(string(k), packetCommandPrefix))
		return err
	}
	if strings.HasPrefix(string(k), packetEventPrefix) {
		_, err := ParseEventName(strings.TrimPrefix(string(k), packetEventPrefix))
		return err
	}
	if strings.TrimSpace(string(k)) == "" {
		return fmt.Errorf("packet kind is required")
	}
	return fmt.Errorf("packet kind %q is not supported", k)
}

func (k PacketKind) String() string {
	return string(k)
}

// CommandName freezes the packet suffixes currently reserved for helper commands.
type CommandName string

const (
	CommandSend             CommandName = "send"
	CommandEnqueue          CommandName = "enqueue"
	CommandInterrupt        CommandName = "interrupt"
	CommandUIResponseSubmit CommandName = "ui_response.submit"
)

func ParseCommandName(raw string) (CommandName, error) {
	value, err := normalizeHierToken(raw, "command name")
	if err != nil {
		return "", err
	}
	return CommandName(value), nil
}

func (n CommandName) Validate() error {
	_, err := ParseCommandName(string(n))
	return err
}

func (n CommandName) String() string {
	return string(n)
}

func (n CommandName) Kind() PacketKind {
	return PacketKind(packetCommandPrefix + string(n))
}

// EventName freezes the replayable helper event suffixes.
type EventName string

const (
	EventOutputDelta         EventName = "output.delta"
	EventTurnCommit          EventName = "turn.commit"
	EventUIRequestOpened     EventName = "ui_request.opened"
	EventUIResponseForwarded EventName = "ui_response.forwarded"
)

func ParseEventName(raw string) (EventName, error) {
	value, err := normalizeHierToken(raw, "event name")
	if err != nil {
		return "", err
	}
	return EventName(value), nil
}

func (n EventName) Validate() error {
	_, err := ParseEventName(string(n))
	return err
}

func (n EventName) String() string {
	return string(n)
}

func (n EventName) Kind() PacketKind {
	return PacketKind(packetEventPrefix + string(n))
}

// TransportState is the helper-reported current generation state.
type TransportState string

const (
	TransportStateStarting TransportState = "starting"
	TransportStateAttached TransportState = "attached"
	TransportStateBroken   TransportState = "broken"
)

func ParseTransportState(raw string) (TransportState, error) {
	state := TransportState(strings.ToLower(strings.TrimSpace(raw)))
	if err := state.Validate(); err != nil {
		return "", err
	}
	return state, nil
}

func (s TransportState) Validate() error {
	switch s {
	case TransportStateStarting, TransportStateAttached, TransportStateBroken:
		return nil
	case "":
		return fmt.Errorf("transport state is required")
	default:
		return fmt.Errorf("transport state %q is not supported", s)
	}
}

func (s TransportState) String() string {
	return string(s)
}

// Envelope is the stable top-level contract shared by every actrail-iod packet.
type Envelope struct {
	SessionID    session.SessionID `json:"session_id"`
	GenerationID GenerationID      `json:"generation_id"`
	Kind         PacketKind        `json:"kind"`
}

func NewEnvelope(sessionID session.SessionID, generationID GenerationID, kind PacketKind) (Envelope, error) {
	if err := sessionID.Validate(); err != nil {
		return Envelope{}, err
	}
	if sessionID.IsHistorical() {
		return Envelope{}, fmt.Errorf("session id %q cannot use historical replay identity", sessionID)
	}
	if err := generationID.Validate(); err != nil {
		return Envelope{}, err
	}
	if err := kind.Validate(); err != nil {
		return Envelope{}, err
	}
	return Envelope{SessionID: sessionID, GenerationID: generationID, Kind: kind}, nil
}

func (e Envelope) Validate() error {
	_, err := NewEnvelope(e.SessionID, e.GenerationID, e.Kind)
	return err
}

// HelloPacket is the initial helper handshake for one session generation.
type HelloPacket struct {
	Envelope
	ProtocolVersion int `json:"protocol_version"`
}

func NewHelloPacket(sessionID session.SessionID, generationID GenerationID, protocolVersion int) (HelloPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketHello)
	if err != nil {
		return HelloPacket{}, err
	}
	packet := HelloPacket{Envelope: env, ProtocolVersion: protocolVersion}
	if err := packet.Validate(); err != nil {
		return HelloPacket{}, err
	}
	return packet, nil
}

func (p HelloPacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketHello {
		return fmt.Errorf("hello packet kind = %q, want %q", p.Kind, PacketHello)
	}
	if p.ProtocolVersion <= 0 {
		return fmt.Errorf("protocol version must be greater than zero")
	}
	return nil
}

// StatePacket reports the current helper generation state and both replay cursors.
// last_offset is the helper-server replay cursor. last_seq is the browser-visible cursor.
type StatePacket struct {
	Envelope
	TransportState TransportState `json:"transport_state"`
	LastOffset     WALOffset      `json:"last_offset"`
	LastSeq        EventSeq       `json:"last_seq"`
}

func NewStatePacket(sessionID session.SessionID, generationID GenerationID, state TransportState, lastOffset WALOffset, lastSeq EventSeq) (StatePacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketState)
	if err != nil {
		return StatePacket{}, err
	}
	packet := StatePacket{Envelope: env, TransportState: state, LastOffset: lastOffset, LastSeq: lastSeq}
	if err := packet.Validate(); err != nil {
		return StatePacket{}, err
	}
	return packet, nil
}

func (p StatePacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketState {
		return fmt.Errorf("state packet kind = %q, want %q", p.Kind, PacketState)
	}
	if err := p.TransportState.Validate(); err != nil {
		return err
	}
	if err := p.LastOffset.ValidateState(); err != nil {
		return err
	}
	if p.LastSeq > 0 {
		if err := p.LastSeq.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// CommandPacket is the stable helper command envelope.
// Kind identifies the command family member. Payload is command-specific and stays opaque here.
type CommandPacket struct {
	Envelope
	CommandID CommandID       `json:"command_id"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func NewCommandPacket(sessionID session.SessionID, generationID GenerationID, name CommandName, commandID CommandID, payload json.RawMessage) (CommandPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, name.Kind())
	if err != nil {
		return CommandPacket{}, err
	}
	packet := CommandPacket{Envelope: env, CommandID: commandID, Payload: payload}
	if err := packet.Validate(); err != nil {
		return CommandPacket{}, err
	}
	return packet, nil
}

func (p CommandPacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if !strings.HasPrefix(p.Kind.String(), packetCommandPrefix) {
		return fmt.Errorf("command packet kind %q must use %q prefix", p.Kind, packetCommandPrefix)
	}
	if err := p.CommandID.Validate(); err != nil {
		return err
	}
	return nil
}

// EventPacket is the stable replayable event envelope.
// Seq is monotonic only inside one GenerationID.
type EventPacket struct {
	Envelope
	Seq     EventSeq        `json:"seq"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func NewEventPacket(sessionID session.SessionID, generationID GenerationID, name EventName, seq EventSeq, payload json.RawMessage) (EventPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, name.Kind())
	if err != nil {
		return EventPacket{}, err
	}
	packet := EventPacket{Envelope: env, Seq: seq, Payload: payload}
	if err := packet.Validate(); err != nil {
		return EventPacket{}, err
	}
	return packet, nil
}

func (p EventPacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if !strings.HasPrefix(p.Kind.String(), packetEventPrefix) {
		return fmt.Errorf("event packet kind %q must use %q prefix", p.Kind, packetEventPrefix)
	}
	if err := p.Seq.Validate(); err != nil {
		return err
	}
	return nil
}

// ReplayRequestPacket asks the helper to resume WAL replay after one durable cursor.
// Replay is by WAL offset, not by browser-visible seq.
type ReplayRequestPacket struct {
	Envelope
	AfterOffset WALOffset `json:"after_offset"`
}

func NewReplayRequestPacket(sessionID session.SessionID, generationID GenerationID, afterOffset WALOffset) (ReplayRequestPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketReplayRequest)
	if err != nil {
		return ReplayRequestPacket{}, err
	}
	packet := ReplayRequestPacket{Envelope: env, AfterOffset: afterOffset}
	if err := packet.Validate(); err != nil {
		return ReplayRequestPacket{}, err
	}
	return packet, nil
}

func (p ReplayRequestPacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketReplayRequest {
		return fmt.Errorf("replay request kind = %q, want %q", p.Kind, PacketReplayRequest)
	}
	return p.AfterOffset.ValidateState()
}

// ReplayItemPacket returns one WAL item in strict append order.
type ReplayItemPacket struct {
	Envelope
	Record WALRecord `json:"record"`
}

func NewReplayItemPacket(sessionID session.SessionID, generationID GenerationID, record WALRecord) (ReplayItemPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketReplayItem)
	if err != nil {
		return ReplayItemPacket{}, err
	}
	packet := ReplayItemPacket{Envelope: env, Record: record}
	if err := packet.Validate(); err != nil {
		return ReplayItemPacket{}, err
	}
	return packet, nil
}

func (p ReplayItemPacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketReplayItem {
		return fmt.Errorf("replay item kind = %q, want %q", p.Kind, PacketReplayItem)
	}
	if err := p.Record.Validate(); err != nil {
		return err
	}
	if p.Record.Header.SessionID != p.SessionID {
		return fmt.Errorf("replay item record session id = %q, want %q", p.Record.Header.SessionID, p.SessionID)
	}
	if p.Record.Header.GenerationID != p.GenerationID {
		return fmt.Errorf("replay item record generation id = %q, want %q", p.Record.Header.GenerationID, p.GenerationID)
	}
	return nil
}

// ReplayDonePacket closes one replay stream for the generation.
// If corrupt_tail is true, the server must stop pretending continuity and mark replay unsafe.
type ReplayDonePacket struct {
	Envelope
	AfterOffset WALOffset `json:"after_offset"`
	LastOffset  WALOffset `json:"last_offset"`
	CorruptTail bool      `json:"corrupt_tail,omitempty"`
}

func NewReplayDonePacket(sessionID session.SessionID, generationID GenerationID, afterOffset, lastOffset WALOffset, corruptTail bool) (ReplayDonePacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketReplayDone)
	if err != nil {
		return ReplayDonePacket{}, err
	}
	packet := ReplayDonePacket{Envelope: env, AfterOffset: afterOffset, LastOffset: lastOffset, CorruptTail: corruptTail}
	if err := packet.Validate(); err != nil {
		return ReplayDonePacket{}, err
	}
	return packet, nil
}

func (p ReplayDonePacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketReplayDone {
		return fmt.Errorf("replay done kind = %q, want %q", p.Kind, PacketReplayDone)
	}
	if err := p.AfterOffset.ValidateState(); err != nil {
		return err
	}
	if err := p.LastOffset.ValidateState(); err != nil {
		return err
	}
	if p.LastOffset < p.AfterOffset {
		return fmt.Errorf("replay done last offset %d cannot be before after offset %d", p.LastOffset, p.AfterOffset)
	}
	return nil
}

// GenerationBreakReason classifies the terminal loss of a generation.
type GenerationBreakReason string

const (
	GenerationBreakHelperExit  GenerationBreakReason = "helper_exit"
	GenerationBreakAttachLost  GenerationBreakReason = "attach_lost"
	GenerationBreakWriteFailed GenerationBreakReason = "write_failed"
	GenerationBreakReplayGap   GenerationBreakReason = "replay_gap"
)

func ParseGenerationBreakReason(raw string) (GenerationBreakReason, error) {
	reason := GenerationBreakReason(strings.ToLower(strings.TrimSpace(raw)))
	if err := reason.Validate(); err != nil {
		return "", err
	}
	return reason, nil
}

func (r GenerationBreakReason) Validate() error {
	switch r {
	case GenerationBreakHelperExit, GenerationBreakAttachLost, GenerationBreakWriteFailed, GenerationBreakReplayGap:
		return nil
	case "":
		return fmt.Errorf("generation break reason is required")
	default:
		return fmt.Errorf("generation break reason %q is not supported", r)
	}
}

func (r GenerationBreakReason) String() string {
	return string(r)
}

// GenerationBreakPacket is the terminal browser-visible fact for one generation.
// Later helper output must move to a new GenerationID.
type GenerationBreakPacket struct {
	Envelope
	Seq    EventSeq              `json:"seq"`
	Reason GenerationBreakReason `json:"reason"`
}

func NewGenerationBreakPacket(sessionID session.SessionID, generationID GenerationID, seq EventSeq, reason GenerationBreakReason) (GenerationBreakPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketGenerationBreak)
	if err != nil {
		return GenerationBreakPacket{}, err
	}
	packet := GenerationBreakPacket{Envelope: env, Seq: seq, Reason: reason}
	if err := packet.Validate(); err != nil {
		return GenerationBreakPacket{}, err
	}
	return packet, nil
}

func (p GenerationBreakPacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketGenerationBreak {
		return fmt.Errorf("generation break kind = %q, want %q", p.Kind, PacketGenerationBreak)
	}
	if err := p.Seq.Validate(); err != nil {
		return err
	}
	return p.Reason.Validate()
}

// ErrorCode freezes machine-readable helper error classes.
type ErrorCode string

const (
	ErrorGenerationNotCurrent ErrorCode = "generation_not_current"
	ErrorReplayCursorInvalid  ErrorCode = "replay_cursor_invalid"
	ErrorReplayCorruptTail    ErrorCode = "replay_corrupt_tail"
	ErrorHelperBroken         ErrorCode = "helper_broken"
)

func ParseErrorCode(raw string) (ErrorCode, error) {
	code := ErrorCode(strings.ToLower(strings.TrimSpace(raw)))
	if err := code.Validate(); err != nil {
		return "", err
	}
	return code, nil
}

func (c ErrorCode) Validate() error {
	switch c {
	case ErrorGenerationNotCurrent, ErrorReplayCursorInvalid, ErrorReplayCorruptTail, ErrorHelperBroken:
		return nil
	case "":
		return fmt.Errorf("error code is required")
	default:
		return fmt.Errorf("error code %q is not supported", c)
	}
}

func (c ErrorCode) String() string {
	return string(c)
}

// ErrorPacket reports recoverable or terminal helper-side failures.
type ErrorPacket struct {
	Envelope
	Recoverable bool       `json:"recoverable"`
	Code        ErrorCode  `json:"code"`
	Message     string     `json:"message"`
	CommandID   *CommandID `json:"command_id,omitempty"`
}

func NewErrorPacket(sessionID session.SessionID, generationID GenerationID, recoverable bool, code ErrorCode, message string, commandID *CommandID) (ErrorPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketError)
	if err != nil {
		return ErrorPacket{}, err
	}
	packet := ErrorPacket{Envelope: env, Recoverable: recoverable, Code: code, Message: strings.TrimSpace(message), CommandID: commandID}
	if err := packet.Validate(); err != nil {
		return ErrorPacket{}, err
	}
	return packet, nil
}

func (p ErrorPacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketError {
		return fmt.Errorf("error packet kind = %q, want %q", p.Kind, PacketError)
	}
	if err := p.Code.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(p.Message) == "" {
		return fmt.Errorf("error message is required")
	}
	if p.CommandID != nil {
		if err := p.CommandID.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ReplayCursor is the durable helper-server resume cursor.
// It advances by WAL offset and is invalid across GenerationID boundaries.
type ReplayCursor struct {
	SessionID    session.SessionID
	GenerationID GenerationID
	AfterOffset  WALOffset
}

func NewReplayCursor(sessionID session.SessionID, generationID GenerationID, afterOffset WALOffset) (ReplayCursor, error) {
	if err := sessionID.Validate(); err != nil {
		return ReplayCursor{}, err
	}
	if sessionID.IsHistorical() {
		return ReplayCursor{}, fmt.Errorf("session id %q cannot use historical replay identity", sessionID)
	}
	if err := generationID.Validate(); err != nil {
		return ReplayCursor{}, err
	}
	if err := afterOffset.ValidateState(); err != nil {
		return ReplayCursor{}, err
	}
	return ReplayCursor{SessionID: sessionID, GenerationID: generationID, AfterOffset: afterOffset}, nil
}

func (c ReplayCursor) Accepts(record WALRecordHeader) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if record.SessionID != c.SessionID {
		return fmt.Errorf("replay cursor session id = %q, record session id = %q", c.SessionID, record.SessionID)
	}
	if record.GenerationID != c.GenerationID {
		return fmt.Errorf("replay cursor generation id = %q, record generation id = %q", c.GenerationID, record.GenerationID)
	}
	if record.Offset != c.AfterOffset+1 {
		return fmt.Errorf("replay cursor after offset %d requires next offset %d, got %d", c.AfterOffset, c.AfterOffset+1, record.Offset)
	}
	return nil
}

func (c ReplayCursor) Advance(record WALRecordHeader) (ReplayCursor, error) {
	if err := c.Accepts(record); err != nil {
		return ReplayCursor{}, err
	}
	return ReplayCursor{SessionID: c.SessionID, GenerationID: c.GenerationID, AfterOffset: record.Offset}, nil
}

// EventCursor is the browser-visible resume cursor.
// It advances only on replayable EventSeq values inside one GenerationID.
type EventCursor struct {
	SessionID    session.SessionID
	GenerationID GenerationID
	AfterSeq     EventSeq
}

func NewEventCursor(sessionID session.SessionID, generationID GenerationID, afterSeq EventSeq) (EventCursor, error) {
	if err := sessionID.Validate(); err != nil {
		return EventCursor{}, err
	}
	if sessionID.IsHistorical() {
		return EventCursor{}, fmt.Errorf("session id %q cannot use historical replay identity", sessionID)
	}
	if err := generationID.Validate(); err != nil {
		return EventCursor{}, err
	}
	if afterSeq > 0 {
		if err := afterSeq.Validate(); err != nil {
			return EventCursor{}, err
		}
	}
	return EventCursor{SessionID: sessionID, GenerationID: generationID, AfterSeq: afterSeq}, nil
}

func (c EventCursor) Accepts(sessionID session.SessionID, generationID GenerationID, seq EventSeq) error {
	if err := sessionID.Validate(); err != nil {
		return err
	}
	if err := generationID.Validate(); err != nil {
		return err
	}
	if err := seq.Validate(); err != nil {
		return err
	}
	if sessionID != c.SessionID {
		return fmt.Errorf("event cursor session id = %q, packet session id = %q", c.SessionID, sessionID)
	}
	if generationID != c.GenerationID {
		return fmt.Errorf("event cursor generation id = %q, packet generation id = %q", c.GenerationID, generationID)
	}
	if seq != c.AfterSeq+1 {
		return fmt.Errorf("event cursor after seq %d requires next seq %d, got %d", c.AfterSeq, c.AfterSeq+1, seq)
	}
	return nil
}

func (c EventCursor) Advance(sessionID session.SessionID, generationID GenerationID, seq EventSeq) (EventCursor, error) {
	if err := c.Accepts(sessionID, generationID, seq); err != nil {
		return EventCursor{}, err
	}
	return EventCursor{SessionID: c.SessionID, GenerationID: c.GenerationID, AfterSeq: seq}, nil
}

func normalizeToken(raw, label string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-':
		default:
			return "", fmt.Errorf("%s %q must use only letters, digits, underscores, or hyphens", label, value)
		}
	}
	return value, nil
}

func normalizeHierToken(raw, label string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	parts := strings.Split(value, ".")
	for _, part := range parts {
		if _, err := normalizeToken(part, label); err != nil {
			return "", fmt.Errorf("%s %q must use dot-separated route tokens", label, value)
		}
	}
	return value, nil
}
