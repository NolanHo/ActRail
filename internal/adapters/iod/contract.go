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
	PacketHello                   PacketKind = "iod.hello"
	PacketHealthRequest           PacketKind = "iod.health.request"
	PacketHealthResponse          PacketKind = "iod.health.response"
	PacketState                   PacketKind = "iod.state"
	PacketCommandSend             PacketKind = "iod.command.send"
	PacketCommandEnqueue          PacketKind = "iod.command.enqueue"
	PacketCommandInterrupt        PacketKind = "iod.command.interrupt"
	PacketCommandUIResponseSubmit PacketKind = "iod.command.ui_response.submit"
	PacketCommandAccepted         PacketKind = "iod.command.accepted"
	PacketCommandRejected         PacketKind = "iod.command.rejected"
	PacketReplayRequest           PacketKind = "iod.replay.request"
	PacketReplayItem              PacketKind = "iod.replay.item"
	PacketReplayDone              PacketKind = "iod.replay.done"
	PacketSessionHistoryRequest   PacketKind = "iod.session_history.request"
	PacketSessionHistoryResponse  PacketKind = "iod.session_history.response"
	PacketGenerationBreak         PacketKind = "iod.generation.break"
	PacketError                   PacketKind = "iod.error"
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
	case PacketHello,
		PacketHealthRequest,
		PacketHealthResponse,
		PacketState,
		PacketCommandSend,
		PacketCommandEnqueue,
		PacketCommandInterrupt,
		PacketCommandUIResponseSubmit,
		PacketCommandAccepted,
		PacketCommandRejected,
		PacketReplayRequest,
		PacketReplayItem,
		PacketReplayDone,
		PacketSessionHistoryRequest,
		PacketSessionHistoryResponse,
		PacketGenerationBreak,
		PacketError:
		return nil
	case "":
		return fmt.Errorf("packet kind is required")
	default:
		return fmt.Errorf("packet kind %q is not supported", k)
	}
}

func (k PacketKind) String() string {
	return string(k)
}

// CommandName freezes the helper command request suffixes.
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
	name := CommandName(value)
	if err := name.Validate(); err != nil {
		return "", err
	}
	return name, nil
}

func (n CommandName) Validate() error {
	switch n {
	case CommandSend, CommandEnqueue, CommandInterrupt, CommandUIResponseSubmit:
		return nil
	case "":
		return fmt.Errorf("command name is required")
	default:
		return fmt.Errorf("command name %q is not supported", n)
	}
}

func (n CommandName) String() string {
	return string(n)
}

func (n CommandName) Kind() PacketKind {
	switch n {
	case CommandSend:
		return PacketCommandSend
	case CommandEnqueue:
		return PacketCommandEnqueue
	case CommandInterrupt:
		return PacketCommandInterrupt
	case CommandUIResponseSubmit:
		return PacketCommandUIResponseSubmit
	default:
		return PacketKind("")
	}
}

// FactKind freezes the helper fact taxonomy shared by live iod.state packets,
// replay items, and WAL-backed projection boundaries.
type FactKind string

const (
	FactHelperStart       FactKind = "helper_start"
	FactAttachEstablished FactKind = "attach_established"
	FactCommandAccepted   FactKind = "command_accepted"
	FactCommandRejected   FactKind = "command_rejected"
	FactOutputDelta       FactKind = "output_delta"
	FactChildExit         FactKind = "child_exit"
	FactHelperExit        FactKind = "helper_exit"
	FactGenerationBreak   FactKind = "generation_break"
)

func ParseFactKind(raw string) (FactKind, error) {
	kind := FactKind(strings.ToLower(strings.TrimSpace(raw)))
	if err := kind.Validate(); err != nil {
		return "", err
	}
	return kind, nil
}

func (k FactKind) Validate() error {
	switch k {
	case FactHelperStart,
		FactAttachEstablished,
		FactCommandAccepted,
		FactCommandRejected,
		FactOutputDelta,
		FactChildExit,
		FactHelperExit,
		FactGenerationBreak:
		return nil
	case "":
		return fmt.Errorf("fact kind is required")
	default:
		return fmt.Errorf("fact kind %q is not supported", k)
	}
}

func (k FactKind) String() string {
	return string(k)
}

func (k FactKind) ProjectionBoundary() ProjectionBoundary {
	switch k {
	case FactOutputDelta:
		return ProjectionBoundaryBrowserEvent
	case FactGenerationBreak:
		return ProjectionBoundaryGenerationTerminal
	default:
		return ProjectionBoundaryStateOnly
	}
}

func (k FactKind) RequiresSeq() bool {
	switch k.ProjectionBoundary() {
	case ProjectionBoundaryBrowserEvent, ProjectionBoundaryGenerationTerminal:
		return true
	default:
		return false
	}
}

func (k FactKind) AllowedInStatePacket() bool {
	switch k {
	case FactCommandAccepted, FactCommandRejected, FactGenerationBreak:
		return false
	default:
		return true
	}
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

// HelloProof is the helper-side reattach proof echoed by iod.hello.
type HelloProof struct {
	HelperPID         int     `json:"helper_pid"`
	ChildPID          *int    `json:"child_pid,omitempty"`
	WALPath           string  `json:"wal_path"`
	ControlSocketPath string  `json:"control_socket_path"`
	StartTS           float64 `json:"start_ts"`
}

func NewHelloProof(helperPID int, childPID *int, walPath, controlSocketPath string, startTS float64) (HelloProof, error) {
	proof := HelloProof{
		HelperPID:         helperPID,
		ChildPID:          childPID,
		WALPath:           strings.TrimSpace(walPath),
		ControlSocketPath: strings.TrimSpace(controlSocketPath),
		StartTS:           startTS,
	}
	if err := proof.Validate(); err != nil {
		return HelloProof{}, err
	}
	return proof, nil
}

func (p HelloProof) Validate() error {
	if p.HelperPID <= 0 {
		return fmt.Errorf("helper pid must be greater than zero")
	}
	if p.ChildPID != nil && *p.ChildPID <= 0 {
		return fmt.Errorf("child pid must be greater than zero")
	}
	if p.WALPath == "" {
		return fmt.Errorf("wal path is required")
	}
	if p.ControlSocketPath == "" {
		return fmt.Errorf("control socket path is required")
	}
	if p.StartTS <= 0 {
		return fmt.Errorf("start ts must be greater than zero")
	}
	return nil
}

// GenerationManifest freezes the durable proof fields used for helper discovery
// and same-generation reattach.
type GenerationManifest struct {
	SessionID          session.SessionID `json:"session_id"`
	GenerationID       GenerationID      `json:"generation_id"`
	CodexThreadID      string            `json:"codex_thread_id,omitempty"`
	SessionHistoryPath string            `json:"session_history_path,omitempty"`
	HelloProof
}

func NewGenerationManifest(sessionID session.SessionID, generationID GenerationID, proof HelloProof) (GenerationManifest, error) {
	manifest := GenerationManifest{SessionID: sessionID, GenerationID: generationID, HelloProof: proof}
	if err := manifest.Validate(); err != nil {
		return GenerationManifest{}, err
	}
	return manifest, nil
}

func (m GenerationManifest) Validate() error {
	if err := m.SessionID.Validate(); err != nil {
		return err
	}
	if m.SessionID.IsHistorical() {
		return fmt.Errorf("session id %q cannot use historical replay identity", m.SessionID)
	}
	if err := m.GenerationID.Validate(); err != nil {
		return err
	}
	return m.HelloProof.Validate()
}

// HelloPacket is the initial helper handshake for one session generation.
type HelloPacket struct {
	Envelope
	ProtocolVersion int    `json:"protocol_version"`
	IODBuildDate    string `json:"iod_build_date,omitempty"`
	IODGitSHA       string `json:"iod_git_sha,omitempty"`
	HelloProof
}

func NewHelloPacket(sessionID session.SessionID, generationID GenerationID, protocolVersion int, proof HelloProof) (HelloPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketHello)
	if err != nil {
		return HelloPacket{}, err
	}
	packet := HelloPacket{Envelope: env, ProtocolVersion: protocolVersion, HelloProof: proof}
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
	return p.HelloProof.Validate()
}

// HealthRequestPacket asks the helper for shallow local transport facts.
// It must not touch child prompt/stdout/stderr channels.
type HealthRequestPacket struct {
	Envelope
}

func NewHealthRequestPacket(sessionID session.SessionID, generationID GenerationID) (HealthRequestPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketHealthRequest)
	if err != nil {
		return HealthRequestPacket{}, err
	}
	packet := HealthRequestPacket{Envelope: env}
	if err := packet.Validate(); err != nil {
		return HealthRequestPacket{}, err
	}
	return packet, nil
}

func (p HealthRequestPacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketHealthRequest {
		return fmt.Errorf("health request kind = %q, want %q", p.Kind, PacketHealthRequest)
	}
	return nil
}

// HealthResponsePacket reports shallow helper-owned liveness facts.
// Deprecated/legacy transport flags describe whether the child command channel
// is suitable for new request/response health and command-state-machine work.
type HealthResponsePacket struct {
	Envelope
	OK                bool   `json:"ok"`
	HelperPID         int    `json:"helper_pid"`
	ChildPID          int    `json:"child_pid,omitempty"`
	ChildIOMode       string `json:"child_io_mode"`
	LegacyTransport   bool   `json:"legacy_transport"`
	Deprecated        bool   `json:"deprecated"`
	EnsureSupported   bool   `json:"ensure_supported"`
	PromptProbe       bool   `json:"prompt_probe"`
	ControlSocketPath string `json:"control_socket_path"`
	WALPath           string `json:"wal_path"`
}

func NewHealthResponsePacket(sessionID session.SessionID, generationID GenerationID, response HealthResponsePacket) (HealthResponsePacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketHealthResponse)
	if err != nil {
		return HealthResponsePacket{}, err
	}
	packet := response
	packet.Envelope = env
	if err := packet.Validate(); err != nil {
		return HealthResponsePacket{}, err
	}
	return packet, nil
}

func (p HealthResponsePacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketHealthResponse {
		return fmt.Errorf("health response kind = %q, want %q", p.Kind, PacketHealthResponse)
	}
	if p.HelperPID <= 0 {
		return fmt.Errorf("helper pid must be greater than zero")
	}
	if strings.TrimSpace(p.ChildIOMode) == "" {
		return fmt.Errorf("child io mode is required")
	}
	if strings.TrimSpace(p.ControlSocketPath) == "" {
		return fmt.Errorf("control socket path is required")
	}
	if strings.TrimSpace(p.WALPath) == "" {
		return fmt.Errorf("wal path is required")
	}
	if p.PromptProbe {
		return fmt.Errorf("health response must not report prompt probe")
	}
	return nil
}

// HelperFact is one helper-owned fact shared by live iod.state and replay items.
type HelperFact struct {
	FactKind FactKind        `json:"fact_kind"`
	Seq      *EventSeq       `json:"seq,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

func NewHelperFact(kind FactKind, seq *EventSeq, payload json.RawMessage) (HelperFact, error) {
	fact := HelperFact{FactKind: kind, Seq: seq, Payload: payload}
	if err := fact.Validate(); err != nil {
		return HelperFact{}, err
	}
	return fact, nil
}

func (f HelperFact) Validate() error {
	if err := f.FactKind.Validate(); err != nil {
		return err
	}
	if f.FactKind.RequiresSeq() {
		if f.Seq == nil {
			return fmt.Errorf("fact kind %q requires seq", f.FactKind)
		}
		if err := f.Seq.Validate(); err != nil {
			return err
		}
	} else if f.Seq != nil {
		return fmt.Errorf("fact kind %q must not carry seq", f.FactKind)
	}
	return nil
}

// StatePacket reports one live helper fact for the current generation.
type StatePacket struct {
	Envelope
	Fact HelperFact `json:"fact"`
}

func NewStatePacket(sessionID session.SessionID, generationID GenerationID, fact HelperFact) (StatePacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketState)
	if err != nil {
		return StatePacket{}, err
	}
	packet := StatePacket{Envelope: env, Fact: fact}
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
	if err := p.Fact.Validate(); err != nil {
		return err
	}
	if !p.Fact.FactKind.AllowedInStatePacket() {
		return fmt.Errorf("fact kind %q must not use %q", p.Fact.FactKind, PacketState)
	}
	return nil
}

// CommandPacket is the stable helper command request envelope.
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
	switch p.Kind {
	case PacketCommandSend, PacketCommandEnqueue, PacketCommandInterrupt, PacketCommandUIResponseSubmit:
	default:
		return fmt.Errorf("command packet kind %q is not a command request", p.Kind)
	}
	if err := p.CommandID.Validate(); err != nil {
		return err
	}
	return nil
}

// CommandOutcome is the stable durable result for one command_id in one generation.
// ack_cursor is the WAL offset of the durable outcome record for that command_id.
type CommandOutcome struct {
	CommandID CommandID       `json:"command_id"`
	AckCursor WALOffset       `json:"ack_cursor"`
	Deduped   bool            `json:"deduped"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func NewCommandOutcome(commandID CommandID, ackCursor WALOffset, deduped bool, payload json.RawMessage) (CommandOutcome, error) {
	outcome := CommandOutcome{CommandID: commandID, AckCursor: ackCursor, Deduped: deduped, Payload: payload}
	if err := outcome.Validate(); err != nil {
		return CommandOutcome{}, err
	}
	return outcome, nil
}

func (o CommandOutcome) Validate() error {
	if err := o.CommandID.Validate(); err != nil {
		return err
	}
	return o.AckCursor.ValidateAppend()
}

// CommandAcceptedPacket reports a durable accepted command outcome.
type CommandAcceptedPacket struct {
	Envelope
	CommandOutcome
}

func NewCommandAcceptedPacket(sessionID session.SessionID, generationID GenerationID, outcome CommandOutcome) (CommandAcceptedPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketCommandAccepted)
	if err != nil {
		return CommandAcceptedPacket{}, err
	}
	packet := CommandAcceptedPacket{Envelope: env, CommandOutcome: outcome}
	if err := packet.Validate(); err != nil {
		return CommandAcceptedPacket{}, err
	}
	return packet, nil
}

func (p CommandAcceptedPacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketCommandAccepted {
		return fmt.Errorf("command accepted packet kind = %q, want %q", p.Kind, PacketCommandAccepted)
	}
	return p.CommandOutcome.Validate()
}

// CommandRejectedPacket reports a durable rejected command outcome.
type CommandRejectedPacket struct {
	Envelope
	CommandOutcome
}

func NewCommandRejectedPacket(sessionID session.SessionID, generationID GenerationID, outcome CommandOutcome) (CommandRejectedPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketCommandRejected)
	if err != nil {
		return CommandRejectedPacket{}, err
	}
	packet := CommandRejectedPacket{Envelope: env, CommandOutcome: outcome}
	if err := packet.Validate(); err != nil {
		return CommandRejectedPacket{}, err
	}
	return packet, nil
}

func (p CommandRejectedPacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketCommandRejected {
		return fmt.Errorf("command rejected packet kind = %q, want %q", p.Kind, PacketCommandRejected)
	}
	return p.CommandOutcome.Validate()
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

// ReplayItem is one WAL-backed fact replayed by helper WAL offset.
type ReplayItem struct {
	WALOffset WALOffset  `json:"wal_offset"`
	Fact      HelperFact `json:"fact"`
}

func NewReplayItem(walOffset WALOffset, fact HelperFact) (ReplayItem, error) {
	item := ReplayItem{WALOffset: walOffset, Fact: fact}
	if err := item.Validate(); err != nil {
		return ReplayItem{}, err
	}
	return item, nil
}

func (i ReplayItem) Validate() error {
	if err := i.WALOffset.ValidateAppend(); err != nil {
		return err
	}
	return i.Fact.Validate()
}

// ReplayItemPacket returns one WAL-backed fact in strict append order.
type ReplayItemPacket struct {
	Envelope
	Item ReplayItem `json:"item"`
}

func NewReplayItemPacket(sessionID session.SessionID, generationID GenerationID, item ReplayItem) (ReplayItemPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketReplayItem)
	if err != nil {
		return ReplayItemPacket{}, err
	}
	packet := ReplayItemPacket{Envelope: env, Item: item}
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
	return p.Item.Validate()
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

// SessionHistoryRequestPacket asks the helper for its in-memory session JSONL cache.
type SessionHistoryRequestPacket struct {
	Envelope
}

func NewSessionHistoryRequestPacket(sessionID session.SessionID, generationID GenerationID) (SessionHistoryRequestPacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketSessionHistoryRequest)
	if err != nil {
		return SessionHistoryRequestPacket{}, err
	}
	packet := SessionHistoryRequestPacket{Envelope: env}
	if err := packet.Validate(); err != nil {
		return SessionHistoryRequestPacket{}, err
	}
	return packet, nil
}

func (p SessionHistoryRequestPacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketSessionHistoryRequest {
		return fmt.Errorf("session history request kind = %q, want %q", p.Kind, PacketSessionHistoryRequest)
	}
	return nil
}

// SessionHistoryResponsePacket returns cached authoritative session JSONL lines.
type SessionHistoryResponsePacket struct {
	Envelope
	SourcePath   string                  `json:"source_path,omitempty"`
	Lines        []string                `json:"lines,omitempty"`
	Messages     []SessionHistoryMessage `json:"messages,omitempty"`
	IndexedCount int                     `json:"indexed_count,omitempty"`
	TaskComplete bool                    `json:"task_complete,omitempty"`
	Warmed       bool                    `json:"warmed"`
	Complete     bool                    `json:"complete"`
}

type SessionHistoryMessage struct {
	Seq         uint64         `json:"seq"`
	Role        string         `json:"role,omitempty"`
	Kind        string         `json:"kind"`
	Type        string         `json:"type,omitempty"`
	Text        string         `json:"text,omitempty"`
	TS          float64        `json:"ts"`
	EventID     string         `json:"event_id,omitempty"`
	SourceOrder string         `json:"source_order,omitempty"`
	Name        string         `json:"name,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	ToolCallID  string         `json:"tool_call_id,omitempty"`
	IsError     bool           `json:"is_error,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

func NewSessionHistoryResponsePacket(sessionID session.SessionID, generationID GenerationID, snapshot SessionHistorySnapshot) (SessionHistoryResponsePacket, error) {
	env, err := NewEnvelope(sessionID, generationID, PacketSessionHistoryResponse)
	if err != nil {
		return SessionHistoryResponsePacket{}, err
	}
	packet := SessionHistoryResponsePacket{
		Envelope:     env,
		SourcePath:   strings.TrimSpace(snapshot.SourcePath),
		Lines:        append([]string(nil), snapshot.Lines...),
		Messages:     append([]SessionHistoryMessage(nil), snapshot.Messages...),
		IndexedCount: snapshot.IndexedCount,
		TaskComplete: snapshot.TaskComplete,
		Warmed:       snapshot.Warmed,
		Complete:     snapshot.Complete,
	}
	if err := packet.Validate(); err != nil {
		return SessionHistoryResponsePacket{}, err
	}
	return packet, nil
}

func (p SessionHistoryResponsePacket) Validate() error {
	if err := p.Envelope.Validate(); err != nil {
		return err
	}
	if p.Kind != PacketSessionHistoryResponse {
		return fmt.Errorf("session history response kind = %q, want %q", p.Kind, PacketSessionHistoryResponse)
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

// GenerationBreakPacket is the live helper terminal transport fact for one generation.
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
	ErrorGenerationNotCurrent   ErrorCode = "generation_not_current"
	ErrorMalformedEnvelope      ErrorCode = "malformed_envelope"
	ErrorUnsupportedCommandKind ErrorCode = "unsupported_command_kind"
	ErrorReplayCursorInvalid    ErrorCode = "replay_cursor_invalid"
	ErrorReplayCorruptTail      ErrorCode = "replay_corrupt_tail"
	ErrorHelperBroken           ErrorCode = "helper_broken"
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
	case ErrorGenerationNotCurrent, ErrorMalformedEnvelope, ErrorUnsupportedCommandKind, ErrorReplayCursorInvalid, ErrorReplayCorruptTail, ErrorHelperBroken:
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
