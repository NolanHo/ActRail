package message

import (
	"fmt"
	"strings"
	"time"
)

// Role is the canonical transcript role label.
type Role string

const (
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
	RoleUser      Role = "user"
)

func ParseRole(raw string) (Role, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", fmt.Errorf("message role is required")
	}
	return Role(value), nil
}

func (r Role) Validate() error {
	_, err := ParseRole(string(r))
	return err
}

func (r Role) String() string {
	return string(r)
}

// Kind is the canonical transcript item kind.
type Kind string

const (
	KindMessage Kind = "message"
)

func ParseKind(raw string) (Kind, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", fmt.Errorf("message kind is required")
	}
	return Kind(value), nil
}

func (k Kind) Validate() error {
	_, err := ParseKind(string(k))
	return err
}

func (k Kind) String() string {
	return string(k)
}

// CommittedMessage is one durable transcript item with a stable seq.
type CommittedMessage struct {
	seq  Seq
	role Role
	kind Kind
	text string
	ts   time.Time
}

func NewCommittedMessage(seq Seq, roleRaw, kindRaw, text string, ts time.Time) (CommittedMessage, error) {
	role, err := ParseRole(roleRaw)
	if err != nil {
		return CommittedMessage{}, err
	}
	kind, err := ParseKind(kindRaw)
	if err != nil {
		return CommittedMessage{}, err
	}
	if text == "" {
		return CommittedMessage{}, fmt.Errorf("message text is required")
	}
	if !ts.IsZero() {
		ts = ts.UTC()
	}
	return CommittedMessage{seq: seq, role: role, kind: kind, text: text, ts: ts}, nil
}

func (m CommittedMessage) Validate() error {
	_, err := NewCommittedMessage(m.seq, m.role.String(), m.kind.String(), m.text, m.ts)
	return err
}

func (m CommittedMessage) Seq() Seq {
	return m.seq
}

func (m CommittedMessage) Role() Role {
	return m.role
}

func (m CommittedMessage) Kind() Kind {
	return m.kind
}

func (m CommittedMessage) Text() string {
	return m.text
}

func (m CommittedMessage) TS() time.Time {
	return m.ts
}

// PartialAssistantTurn is the provisional assistant output buffered before commit.
type PartialAssistantTurn struct {
	turnID TurnID
	text   string
}

func NewPartialAssistantTurn(turnIDRaw, text string) (PartialAssistantTurn, error) {
	turnID, err := NewTurnID(turnIDRaw)
	if err != nil {
		return PartialAssistantTurn{}, err
	}
	if text == "" {
		return PartialAssistantTurn{}, fmt.Errorf("assistant delta text is required")
	}
	return PartialAssistantTurn{turnID: turnID, text: text}, nil
}

func (t PartialAssistantTurn) Validate() error {
	_, err := NewPartialAssistantTurn(t.turnID.String(), t.text)
	return err
}

func (t PartialAssistantTurn) TurnID() TurnID {
	return t.turnID
}

func (t PartialAssistantTurn) Text() string {
	return t.text
}

// HistoryPage is one HTTP replay page from the durable transcript.
type HistoryPage struct {
	items      []CommittedMessage
	nextBefore *Seq
	hasMore    bool
}

func (p HistoryPage) Items() []CommittedMessage {
	items := make([]CommittedMessage, len(p.items))
	copy(items, p.items)
	return items
}

func (p HistoryPage) NextBefore() (Seq, bool) {
	if p.nextBefore == nil {
		return 0, false
	}
	return *p.nextBefore, true
}

func (p HistoryPage) HasMore() bool {
	return p.hasMore
}

// Transcript is the canonical in-memory model behind HTTP history and live assistant state.
type Transcript struct {
	items   []CommittedMessage
	partial *PartialAssistantTurn
}

func NewTranscript() Transcript {
	return Transcript{items: []CommittedMessage{}}
}

func (t Transcript) Clone() Transcript {
	items := make([]CommittedMessage, len(t.items))
	copy(items, t.items)
	var partial *PartialAssistantTurn
	if t.partial != nil {
		cp := *t.partial
		partial = &cp
	}
	return Transcript{items: items, partial: partial}
}

func (t Transcript) Tail() TailSnapshot {
	seq := t.TailSeq()
	if t.partial == nil {
		return NewCommittedTail(seq)
	}
	tail, err := NewLiveTail(seq, t.partial.turnID.String())
	if err != nil {
		panic(err)
	}
	return tail
}

func (t Transcript) TailSeq() Seq {
	if len(t.items) == 0 {
		return 0
	}
	return t.items[len(t.items)-1].Seq()
}

func (t Transcript) Len() int {
	return len(t.items)
}

func (t Transcript) Items() []CommittedMessage {
	items := make([]CommittedMessage, len(t.items))
	copy(items, t.items)
	return items
}

func (t Transcript) PartialAssistantTurn() (PartialAssistantTurn, bool) {
	if t.partial == nil {
		return PartialAssistantTurn{}, false
	}
	return *t.partial, true
}

func (t *Transcript) DiscardPartialAssistantTurn() bool {
	if t.partial == nil {
		return false
	}
	t.partial = nil
	return true
}

func (t Transcript) History(before *Seq, limit int) HistoryPage {
	upper := len(t.items)
	if before != nil {
		upper = 0
		for idx, item := range t.items {
			if item.Seq() >= *before {
				upper = idx
				break
			}
			upper = idx + 1
		}
	}
	start := 0
	if limit > 0 && upper > limit {
		start = upper - limit
	}
	items := make([]CommittedMessage, upper-start)
	copy(items, t.items[start:upper])
	page := HistoryPage{items: items, hasMore: start > 0}
	if page.hasMore && len(items) > 0 {
		next := items[0].Seq()
		page.nextBefore = &next
	}
	return page
}

func (t *Transcript) AppendMessage(roleRaw, kindRaw, text string, ts time.Time) (CommittedMessage, error) {
	if t.partial != nil {
		return CommittedMessage{}, fmt.Errorf("live assistant turn %q must commit before appending durable transcript", t.partial.turnID)
	}
	item, err := NewCommittedMessage(t.TailSeq()+1, roleRaw, kindRaw, text, ts)
	if err != nil {
		return CommittedMessage{}, err
	}
	t.items = append(t.items, item)
	return item, nil
}

func (t *Transcript) AppendAssistantDelta(turnIDRaw, delta string) (PartialAssistantTurn, error) {
	partial, err := NewPartialAssistantTurn(turnIDRaw, delta)
	if err != nil {
		return PartialAssistantTurn{}, err
	}
	if t.partial == nil {
		t.partial = &partial
		return partial, nil
	}
	if t.partial.turnID != partial.turnID {
		return PartialAssistantTurn{}, fmt.Errorf("live assistant turn %q already owns transcript tail", t.partial.turnID)
	}
	t.partial.text += delta
	return *t.partial, nil
}

func (t *Transcript) CommitAssistantTurn(turnIDRaw, finalText string, ts time.Time) (CommittedMessage, error) {
	turnID, err := NewTurnID(turnIDRaw)
	if err != nil {
		return CommittedMessage{}, err
	}
	if t.partial == nil {
		return CommittedMessage{}, fmt.Errorf("assistant turn %q is not active", turnID)
	}
	if t.partial.turnID != turnID {
		return CommittedMessage{}, fmt.Errorf("assistant turn %q cannot commit live turn %q", turnID, t.partial.turnID)
	}
	text := finalText
	if text == "" {
		text = t.partial.text
	}
	item, err := NewCommittedMessage(t.TailSeq()+1, RoleAssistant.String(), KindMessage.String(), text, ts)
	if err != nil {
		return CommittedMessage{}, err
	}
	t.items = append(t.items, item)
	t.partial = nil
	return item, nil
}
