package message

import (
	"fmt"
	"strings"
)

// Seq is the durable transcript sequence number exposed through HTTP history.
type Seq uint64

func (s Seq) Uint64() uint64 {
	return uint64(s)
}

// TurnID identifies one live turn that may temporarily own the transcript tail.
type TurnID string

func NewTurnID(raw string) (TurnID, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("turn id is required")
	}
	return TurnID(value), nil
}

func (id TurnID) Validate() error {
	_, err := NewTurnID(string(id))
	return err
}

func (id TurnID) String() string {
	return string(id)
}

// TailOwner describes whether the tail belongs to committed transcript state or a live turn.
type TailOwner string

const (
	TailOwnerTranscript TailOwner = "transcript"
	TailOwnerLiveTurn   TailOwner = "live_turn"
)

func ParseTailOwner(raw string) (TailOwner, error) {
	owner := TailOwner(strings.ToLower(strings.TrimSpace(raw)))
	if err := owner.Validate(); err != nil {
		return "", err
	}
	return owner, nil
}

func (o TailOwner) Validate() error {
	switch o {
	case TailOwnerTranscript, TailOwnerLiveTurn:
		return nil
	case "":
		return fmt.Errorf("tail owner is required")
	default:
		return fmt.Errorf("tail owner %q is not supported", string(o))
	}
}

// TailSnapshot freezes the last committed transcript seq and who currently owns that tail.
type TailSnapshot struct {
	seq    Seq
	owner  TailOwner
	turnID *TurnID
}

func NewCommittedTail(seq Seq) TailSnapshot {
	return TailSnapshot{seq: seq, owner: TailOwnerTranscript}
}

func NewLiveTail(seq Seq, turnIDRaw string) (TailSnapshot, error) {
	turnID, err := NewTurnID(turnIDRaw)
	if err != nil {
		return TailSnapshot{}, err
	}
	return NewTailSnapshot(seq, TailOwnerLiveTurn, &turnID)
}

func NewTailSnapshot(seq Seq, owner TailOwner, turnID *TurnID) (TailSnapshot, error) {
	if err := owner.Validate(); err != nil {
		return TailSnapshot{}, err
	}
	if turnID != nil {
		if err := turnID.Validate(); err != nil {
			return TailSnapshot{}, err
		}
	}
	switch owner {
	case TailOwnerTranscript:
		if turnID != nil {
			return TailSnapshot{}, fmt.Errorf("transcript tail at seq %d cannot carry live turn id %q", seq, *turnID)
		}
	case TailOwnerLiveTurn:
		if turnID == nil {
			return TailSnapshot{}, fmt.Errorf("live turn tail at seq %d requires turn id", seq)
		}
	}
	return TailSnapshot{seq: seq, owner: owner, turnID: turnID}, nil
}

func (t TailSnapshot) Validate() error {
	_, err := NewTailSnapshot(t.seq, t.owner, t.turnID)
	return err
}

func (t TailSnapshot) Seq() Seq {
	return t.seq
}

func (t TailSnapshot) Owner() TailOwner {
	return t.owner
}

func (t TailSnapshot) Live() bool {
	return t.owner == TailOwnerLiveTurn
}

func (t TailSnapshot) TurnID() (TurnID, bool) {
	if t.turnID == nil {
		return "", false
	}
	return *t.turnID, true
}
