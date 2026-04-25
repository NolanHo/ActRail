package session

import (
	"fmt"
	"strings"

	"actrail/internal/domain/message"
)

// QueueItemID identifies one replaceable queued prompt.
type QueueItemID string

func NewQueueItemID(raw string) (QueueItemID, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("queue item id is required")
	}
	return QueueItemID(value), nil
}

func (id QueueItemID) Validate() error {
	_, err := NewQueueItemID(string(id))
	return err
}

func (id QueueItemID) String() string {
	return string(id)
}

// QueueItemState is the canonical state for items exposed in queue snapshots.
type QueueItemState string

const (
	QueueItemStateQueued QueueItemState = "queued"
)

func ParseQueueItemState(raw string) (QueueItemState, error) {
	state := QueueItemState(strings.ToLower(strings.TrimSpace(raw)))
	if err := state.Validate(); err != nil {
		return "", err
	}
	return state, nil
}

func (s QueueItemState) Validate() error {
	switch s {
	case QueueItemStateQueued:
		return nil
	case "":
		return fmt.Errorf("queue item state is required")
	default:
		return fmt.Errorf("queue item state %q is not supported", string(s))
	}
}

// QueueItem is one queued prompt awaiting activation.
type QueueItem struct {
	id    QueueItemID
	text  string
	state QueueItemState
}

func NewQueueItem(idRaw, textRaw string, stateRaw QueueItemState) (QueueItem, error) {
	id, err := NewQueueItemID(idRaw)
	if err != nil {
		return QueueItem{}, err
	}
	text := strings.TrimSpace(textRaw)
	if text == "" {
		return QueueItem{}, fmt.Errorf("queue item %q text is required", id)
	}
	state, err := ParseQueueItemState(stateRaw.String())
	if err != nil {
		return QueueItem{}, err
	}
	return QueueItem{id: id, text: text, state: state}, nil
}

func (s QueueItemState) String() string {
	return string(s)
}

func (i QueueItem) Validate() error {
	_, err := NewQueueItem(i.id.String(), i.text, i.state)
	return err
}

func (i QueueItem) ID() QueueItemID {
	return i.id
}

func (i QueueItem) Text() string {
	return i.text
}

func (i QueueItem) State() QueueItemState {
	return i.state
}

func (i QueueItem) Replaceable() bool {
	return i.state == QueueItemStateQueued
}

// QueueSnapshot is the canonical queued-work snapshot for one session.
type QueueSnapshot struct {
	items []QueueItem
}

func EmptyQueueSnapshot() QueueSnapshot {
	return QueueSnapshot{items: []QueueItem{}}
}

func NewQueueSnapshot(items []QueueItem) (QueueSnapshot, error) {
	seen := make(map[QueueItemID]struct{}, len(items))
	copied := make([]QueueItem, len(items))
	for idx, item := range items {
		if err := item.Validate(); err != nil {
			return QueueSnapshot{}, err
		}
		if _, exists := seen[item.ID()]; exists {
			return QueueSnapshot{}, fmt.Errorf("queue item id %q is duplicated", item.ID())
		}
		seen[item.ID()] = struct{}{}
		copied[idx] = item
	}
	return QueueSnapshot{items: copied}, nil
}

func (q QueueSnapshot) Validate() error {
	_, err := NewQueueSnapshot(q.items)
	return err
}

func (q QueueSnapshot) Len() int {
	return len(q.items)
}

func (q QueueSnapshot) Empty() bool {
	return len(q.items) == 0
}

func (q QueueSnapshot) Items() []QueueItem {
	copied := make([]QueueItem, len(q.items))
	copy(copied, q.items)
	return copied
}

// State freezes the non-streaming state snapshot for one session.
type State struct {
	identity Identity
	busy     bool
	queue    QueueSnapshot
	tail     message.TailSnapshot
}

func NewState(identity Identity, busy bool, queue QueueSnapshot, tail message.TailSnapshot) (State, error) {
	if err := identity.Validate(); err != nil {
		return State{}, err
	}
	if err := queue.Validate(); err != nil {
		return State{}, err
	}
	if err := tail.Validate(); err != nil {
		return State{}, err
	}
	if identity.Historical() {
		if busy {
			return State{}, fmt.Errorf("historical session %q cannot be busy", identity.SessionID())
		}
		if !queue.Empty() {
			return State{}, fmt.Errorf("historical session %q cannot expose queued live work", identity.SessionID())
		}
		if tail.Live() {
			return State{}, fmt.Errorf("historical session %q cannot own a live transcript tail", identity.SessionID())
		}
	}
	if tail.Live() && !busy {
		return State{}, fmt.Errorf("live transcript tail for session %q requires busy=true", identity.SessionID())
	}
	return State{identity: identity, busy: busy, queue: queue, tail: tail}, nil
}

func (s State) Identity() Identity {
	return s.identity
}

func (s State) Busy() bool {
	return s.busy
}

func (s State) Queue() QueueSnapshot {
	return s.queue
}

func (s State) Tail() message.TailSnapshot {
	return s.tail
}
