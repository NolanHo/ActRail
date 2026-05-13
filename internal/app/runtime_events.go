package app

import (
	"context"
	"strconv"
	"time"

	"actrail/internal/domain/session"
)

const sessionStateHeartbeatInterval = 30 * time.Minute

// RuntimeEventSink publishes live session mutations onto external transports.
type RuntimeEventSink interface {
	PublishSessionState(SessionStateEvent)
	PublishMessageDelta(MessageDeltaEvent)
	PublishMessageCommit(MessageCommitEvent)
	PublishQueueState(QueueStateEvent)
	PublishUIRequest(UIRequestEvent)
	PublishUIResolved(UIResolvedEvent)
	PublishGenerationBroken(GenerationBrokenEvent)
	PublishTransportResetRequired(TransportResetRequiredEvent)
	PublishNotification(NotificationEvent)
}

// SessionResumeCursorWriter stores the latest published stream cursor for reconnect snapshots.
type SessionResumeCursorWriter interface {
	SetSessionResumeCursor(session.SessionID, session.StreamKind, int64) error
}

type SessionStateEvent struct {
	SessionID          session.SessionID
	Busy               bool
	BusyReason         string
	RuntimeState       string
	RuntimeStateReason string
	QueueLen           int
	TailSeq            uint64
	Transport          SessionTransportSnapshot
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
	Request   SessionUIRequestSnapshot
}

type UIResolvedEvent struct {
	SessionID session.SessionID
	RequestID string
}

type WaitLifecycleEvent struct {
	Type       string
	SessionID  session.SessionID
	Wait       WaitRecord
	ActiveWait *ActiveWaitSummary
}

type WaitsUpdatedEvent struct {
	Waits []ActiveWaitSummary
}

type GenerationBrokenEvent struct {
	SessionID    session.SessionID
	GenerationID string
	Reason       string
}

type TransportResetRequiredEvent struct {
	SessionID    session.SessionID
	GenerationID string
	Reason       string
}

type NotificationEvent struct {
	SessionID string
	Title     string
	Body      string
	MessageID string
	Kind      string
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
	s.emitSessionStateRecord(record)
}

func (s *Stub) emitSessionStateRecord(record sessionRecord) {
	if s == nil || s.sink == nil {
		return
	}
	record.runtime = s.runtimeForRecord(record)
	busy, busyReason := effectiveBusy(record)
	runtimeState, runtimeStateReason := runtimeStateFields(record)
	tailSeq := record.transcript.TailSeq().Uint64()
	if record.identity.Backend() == session.BackendCodex {
		if mirrored := s.codexLiveMirroredTail(record.identity.SessionID()); mirrored > tailSeq {
			tailSeq = mirrored
		}
	}
	s.sink.PublishSessionState(SessionStateEvent{
		SessionID:          record.identity.SessionID(),
		Busy:               busy,
		BusyReason:         busyReason,
		RuntimeState:       runtimeState,
		RuntimeStateReason: runtimeStateReason,
		QueueLen:           record.state.Queue().Len(),
		TailSeq:            tailSeq,
		Transport:          s.sessionTransportSnapshot(record),
	})
}

func (s *Stub) EmitAllSessionStates() int {
	if s == nil || s.sink == nil {
		return 0
	}
	records := s.registry.ListAll()
	for _, record := range records {
		s.emitSessionStateRecord(record)
	}
	return len(records)
}

func (s *Stub) RunSessionStateHeartbeat(ctx context.Context) {
	s.RunSessionStateHeartbeatEvery(ctx, sessionStateHeartbeatInterval)
}

func (s *Stub) RunSessionStateHeartbeatEvery(ctx context.Context, interval time.Duration) {
	if s == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.EmitAllSessionStates()
		}
	}
}

func (s *Stub) emitQueueState(sessionID session.SessionID, queue SessionQueueSnapshot) {
	if s == nil || s.sink == nil {
		return
	}
	s.sink.PublishQueueState(QueueStateEvent{SessionID: sessionID, Queue: queue})
}

func (s *Stub) emitMessageCommit(sessionID session.SessionID, turnID string, msg SessionMessage) {
	if s == nil {
		return
	}
	s.appendTeamMessageCommitEvent(sessionID, turnID, msg)
	if s.sink == nil {
		return
	}
	s.sink.PublishMessageCommit(MessageCommitEvent{SessionID: sessionID, TurnID: turnID, Message: msg})
}

func (s *Stub) emitMessageDelta(sessionID session.SessionID, turnID, role, delta string) {
	if s == nil {
		return
	}
	s.appendTeamMessageDeltaEvent(sessionID, turnID, role, delta)
	if s.sink == nil {
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

func (s *Stub) emitGenerationBroken(sessionID session.SessionID, generationID, reason string) {
	if s == nil || s.sink == nil {
		return
	}
	s.sink.PublishGenerationBroken(GenerationBrokenEvent{SessionID: sessionID, GenerationID: generationID, Reason: reason})
}

func (s *Stub) emitTransportResetRequired(sessionID session.SessionID, generationID, reason string) {
	if s == nil || s.sink == nil {
		return
	}
	s.sink.PublishTransportResetRequired(TransportResetRequiredEvent{SessionID: sessionID, GenerationID: generationID, Reason: reason})
}

func (s *Stub) emitNotification(event NotificationEvent) {
	if s == nil || s.sink == nil || !s.cfg.Features.Notifications {
		return
	}
	s.sink.PublishNotification(event)
}
