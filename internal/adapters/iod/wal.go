package iod

import (
	"encoding/json"
	"fmt"
	"strings"

	"actrail/internal/domain/session"
)

// WALOffset is the append-order cursor for helper-server replay.
// Offset 0 means no durable record has been acknowledged yet.
type WALOffset uint64

func (o WALOffset) Uint64() uint64 {
	return uint64(o)
}

func (o WALOffset) ValidateAppend() error {
	if o == 0 {
		return fmt.Errorf("wal offset must be greater than zero")
	}
	return nil
}

func (o WALOffset) ValidateState() error {
	return nil
}

// WALRecordClass freezes the durable record classes shared by helper and server.
type WALRecordClass string

const (
	WALRecordHelperStart       WALRecordClass = "helper_start"
	WALRecordAttachEstablished WALRecordClass = "attach_established"
	WALRecordCommandAccepted   WALRecordClass = "command_accepted"
	WALRecordCommandRejected   WALRecordClass = "command_rejected"
	WALRecordOutputDelta       WALRecordClass = "output_delta"
	WALRecordChildExit         WALRecordClass = "child_exit"
	WALRecordHelperExit        WALRecordClass = "helper_exit"
	WALRecordGenerationBreak   WALRecordClass = "generation_break"
)

func ParseWALRecordClass(raw string) (WALRecordClass, error) {
	class := WALRecordClass(strings.ToLower(strings.TrimSpace(raw)))
	if err := class.Validate(); err != nil {
		return "", err
	}
	return class, nil
}

func (c WALRecordClass) Validate() error {
	switch c {
	case WALRecordHelperStart,
		WALRecordAttachEstablished,
		WALRecordCommandAccepted,
		WALRecordCommandRejected,
		WALRecordOutputDelta,
		WALRecordChildExit,
		WALRecordHelperExit,
		WALRecordGenerationBreak:
		return nil
	case "":
		return fmt.Errorf("wal record class is required")
	default:
		return fmt.Errorf("wal record class %q is not supported", c)
	}
}

func (c WALRecordClass) String() string {
	return string(c)
}

func (c WALRecordClass) FactKind() FactKind {
	return FactKind(c)
}

// ProjectionBoundary freezes how one WAL class crosses into server-visible projection.
// state_only records mutate helper/session state but do not advance browser-visible seq.
// browser_event records advance browser replay and must carry seq.
// generation_terminal records end the generation and block further projection on that generation.
type ProjectionBoundary string

const (
	ProjectionBoundaryStateOnly          ProjectionBoundary = "state_only"
	ProjectionBoundaryBrowserEvent       ProjectionBoundary = "browser_event"
	ProjectionBoundaryGenerationTerminal ProjectionBoundary = "generation_terminal"
)

func (b ProjectionBoundary) Validate() error {
	switch b {
	case ProjectionBoundaryStateOnly, ProjectionBoundaryBrowserEvent, ProjectionBoundaryGenerationTerminal:
		return nil
	case "":
		return fmt.Errorf("projection boundary is required")
	default:
		return fmt.Errorf("projection boundary %q is not supported", b)
	}
}

func (b ProjectionBoundary) String() string {
	return string(b)
}

func (c WALRecordClass) ProjectionBoundary() ProjectionBoundary {
	return c.FactKind().ProjectionBoundary()
}

func (c WALRecordClass) RequiresSeq() bool {
	return c.FactKind().RequiresSeq()
}

// WALRecordHeader is the stable durable header for helper replay and server projection.
type WALRecordHeader struct {
	SessionID    session.SessionID `json:"session_id"`
	GenerationID GenerationID      `json:"generation_id"`
	Offset       WALOffset         `json:"offset"`
	Class        WALRecordClass    `json:"class"`
	Seq          *EventSeq         `json:"seq,omitempty"`
	Checksum     uint32            `json:"checksum"`
}

func NewWALRecordHeader(sessionID session.SessionID, generationID GenerationID, offset WALOffset, class WALRecordClass, seq *EventSeq, checksum uint32) (WALRecordHeader, error) {
	header := WALRecordHeader{
		SessionID:    sessionID,
		GenerationID: generationID,
		Offset:       offset,
		Class:        class,
		Seq:          seq,
		Checksum:     checksum,
	}
	if err := header.Validate(); err != nil {
		return WALRecordHeader{}, err
	}
	return header, nil
}

func (h WALRecordHeader) Validate() error {
	if err := h.SessionID.Validate(); err != nil {
		return err
	}
	if h.SessionID.IsHistorical() {
		return fmt.Errorf("session id %q cannot use historical replay identity", h.SessionID)
	}
	if err := h.GenerationID.Validate(); err != nil {
		return err
	}
	if err := h.Offset.ValidateAppend(); err != nil {
		return err
	}
	if err := h.Class.Validate(); err != nil {
		return err
	}
	if h.Class.RequiresSeq() {
		if h.Seq == nil {
			return fmt.Errorf("wal record class %q requires seq", h.Class)
		}
		if err := h.Seq.Validate(); err != nil {
			return err
		}
	} else if h.Seq != nil {
		return fmt.Errorf("wal record class %q must not carry seq", h.Class)
	}
	return nil
}

// WALRecord keeps the class-specific payload opaque at the contract-freeze layer.
// Packet 1 freezes headers, ordering, and boundaries. Later packets own payload schemas.
type WALRecord struct {
	Header  WALRecordHeader `json:"header"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func NewWALRecord(header WALRecordHeader, payload json.RawMessage) (WALRecord, error) {
	record := WALRecord{Header: header, Payload: payload}
	if err := record.Validate(); err != nil {
		return WALRecord{}, err
	}
	return record, nil
}

func (r WALRecord) Validate() error {
	return r.Header.Validate()
}

// ProjectionCursor is the server-side application cursor for one generation.
// It consumes WAL records strictly in append order and becomes terminal after generation_break.
type ProjectionCursor struct {
	SessionID    session.SessionID
	GenerationID GenerationID
	AfterOffset  WALOffset
	Broken       bool
}

func NewProjectionCursor(sessionID session.SessionID, generationID GenerationID, afterOffset WALOffset) (ProjectionCursor, error) {
	if err := sessionID.Validate(); err != nil {
		return ProjectionCursor{}, err
	}
	if sessionID.IsHistorical() {
		return ProjectionCursor{}, fmt.Errorf("session id %q cannot use historical replay identity", sessionID)
	}
	if err := generationID.Validate(); err != nil {
		return ProjectionCursor{}, err
	}
	if err := afterOffset.ValidateState(); err != nil {
		return ProjectionCursor{}, err
	}
	return ProjectionCursor{SessionID: sessionID, GenerationID: generationID, AfterOffset: afterOffset}, nil
}

func (c ProjectionCursor) Advance(record WALRecordHeader) (ProjectionCursor, error) {
	if c.Broken {
		return ProjectionCursor{}, fmt.Errorf("projection for generation %q is terminal after generation break", c.GenerationID)
	}
	if record.SessionID != c.SessionID {
		return ProjectionCursor{}, fmt.Errorf("projection cursor session id = %q, record session id = %q", c.SessionID, record.SessionID)
	}
	if record.GenerationID != c.GenerationID {
		return ProjectionCursor{}, fmt.Errorf("projection cursor generation id = %q, record generation id = %q", c.GenerationID, record.GenerationID)
	}
	if err := record.Validate(); err != nil {
		return ProjectionCursor{}, err
	}
	if record.Offset != c.AfterOffset+1 {
		return ProjectionCursor{}, fmt.Errorf("projection cursor after offset %d requires next offset %d, got %d", c.AfterOffset, c.AfterOffset+1, record.Offset)
	}
	next := ProjectionCursor{
		SessionID:    c.SessionID,
		GenerationID: c.GenerationID,
		AfterOffset:  record.Offset,
		Broken:       record.Class.ProjectionBoundary() == ProjectionBoundaryGenerationTerminal,
	}
	return next, nil
}
