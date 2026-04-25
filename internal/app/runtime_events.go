package app

import (
	"strconv"

	"actrail/internal/domain/session"
)

// RuntimeEventSink publishes live session mutations onto external transports.
type RuntimeEventSink interface {
	PublishSessionState(SessionStateEvent)
	PublishMessageDelta(MessageDeltaEvent)
	PublishMessageCommit(MessageCommitEvent)
	PublishQueueState(QueueStateEvent)
	PublishUIRequest(UIRequestEvent)
	PublishUIResolved(UIResolvedEvent)
}

// SessionResumeCursorWriter stores the latest published stream cursor for reconnect snapshots.
type SessionResumeCursorWriter interface {
	SetSessionResumeCursor(session.SessionID, session.StreamKind, int64) error
}

type SessionStateEvent struct {
	SessionID session.SessionID
	Busy      bool
	QueueLen  int
	TailSeq   uint64
}

type MessageDeltaEvent struct {
	SessionID session.SessionID
	TurnID    string
	Role      string
	Delta     string
}

type MessageCommitEvent struct {
	SessionID session.SessionID
	TurnID    string
	Message   SessionMessage
}

type QueueStateEvent struct {
	SessionID session.SessionID
	Queue     SessionQueueSnapshot
}

type UIRequestEvent struct {
	SessionID session.SessionID
	RequestID string
	Kind      string
	Prompt    string
	Options   []string
}

type UIResolvedEvent struct {
	SessionID session.SessionID
	RequestID string
}

func (s *Stub) SetRuntimeEventSink(sink RuntimeEventSink) {
	s.sink = sink
}

func (s *Stub) SetSessionResumeCursor(sessionID session.SessionID, kind session.StreamKind, cursor int64) error {
	return s.registry.SetResumeCursor(sessionID, kind, strconv.FormatInt(cursor, 10))
}

func (s *Stub) emitSessionState(sessionID session.SessionID) {
	if s == nil || s.sink == nil {
		return
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return
	}
	s.sink.PublishSessionState(SessionStateEvent{
		SessionID: sessionID,
		Busy:      record.state.Busy(),
		QueueLen:  record.state.Queue().Len(),
		TailSeq:   record.transcript.TailSeq().Uint64(),
	})
}

func (s *Stub) emitQueueState(sessionID session.SessionID, queue SessionQueueSnapshot) {
	if s == nil || s.sink == nil {
		return
	}
	s.sink.PublishQueueState(QueueStateEvent{SessionID: sessionID, Queue: queue})
}

func (s *Stub) emitMessageCommit(sessionID session.SessionID, turnID string, msg SessionMessage) {
	if s == nil || s.sink == nil {
		return
	}
	s.sink.PublishMessageCommit(MessageCommitEvent{SessionID: sessionID, TurnID: turnID, Message: msg})
}

func (s *Stub) emitMessageDelta(sessionID session.SessionID, turnID, role, delta string) {
	if s == nil || s.sink == nil {
		return
	}
	s.sink.PublishMessageDelta(MessageDeltaEvent{SessionID: sessionID, TurnID: turnID, Role: role, Delta: delta})
}

func (s *Stub) emitUIRequest(event UIRequestEvent) {
	if s == nil || s.sink == nil {
		return
	}
	s.sink.PublishUIRequest(event)
}

func (s *Stub) emitUIResolved(sessionID session.SessionID, requestID string) {
	if s == nil || s.sink == nil {
		return
	}
	s.sink.PublishUIResolved(UIResolvedEvent{SessionID: sessionID, RequestID: requestID})
}
