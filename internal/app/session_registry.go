package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"actrail/internal/domain/message"
	"actrail/internal/domain/session"
)

type sessionRegistry struct {
	mu       sync.RWMutex
	now      func() time.Time
	nextID   sessionIDGenerator
	order    []session.SessionID
	sessions map[session.SessionID]sessionRecord
}

type sessionIDGenerator struct {
	session uint64
	runtime uint64
	thread  uint64
}

type sessionCreateSpec struct {
	Backend         session.Backend
	CWD             string
	Provider        string
	Model           string
	ReasoningEffort string
	Title           string
}

type sessionRecord struct {
	identity        session.Identity
	title           string
	cwd             string
	provider        string
	model           string
	reasoningEffort string
	createdAt       time.Time
	updatedAt       time.Time
	activityAt      time.Time
	state           session.State
	transcript      message.Transcript
}

func newSessionRegistry(now func() time.Time) *sessionRegistry {
	if now == nil {
		now = time.Now
	}
	return &sessionRegistry{
		now:      now,
		sessions: make(map[session.SessionID]sessionRecord),
		order:    make([]session.SessionID, 0),
	}
}

func (r *sessionRegistry) Create(spec sessionCreateSpec) (sessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now().UTC()
	r.nextID.session++
	r.nextID.runtime++
	r.nextID.thread++

	identity, err := session.NewLiveIdentity(
		fmt.Sprintf("s_%d", r.nextID.session),
		fmt.Sprintf("r_%d", r.nextID.runtime),
		fmt.Sprintf("t_%d", r.nextID.thread),
		spec.Backend.String(),
	)
	if err != nil {
		return sessionRecord{}, err
	}
	transcript := message.NewTranscript()
	state, err := session.NewState(identity, false, session.EmptyQueueSnapshot(), transcript.Tail())
	if err != nil {
		return sessionRecord{}, err
	}
	record := sessionRecord{
		identity:        identity,
		title:           normalizeSessionTitle(spec.Title, spec.CWD),
		cwd:             strings.TrimSpace(spec.CWD),
		provider:        strings.TrimSpace(spec.Provider),
		model:           strings.TrimSpace(spec.Model),
		reasoningEffort: strings.TrimSpace(spec.ReasoningEffort),
		createdAt:       now,
		updatedAt:       now,
		activityAt:      now,
		state:           state,
		transcript:      transcript,
	}
	if _, exists := r.sessions[identity.SessionID()]; exists {
		return sessionRecord{}, fmt.Errorf("session %q already exists", identity.SessionID())
	}
	cp := copySessionRecord(record)
	id := cp.identity.SessionID()
	r.sessions[id] = cp
	r.order = append(r.order, id)
	return copySessionRecord(cp), nil
}

func (r *sessionRegistry) Lookup(sessionID session.SessionID) (sessionRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return sessionRecord{}, false
	}
	return copySessionRecord(record), true
}

func (r *sessionRegistry) List() []sessionRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]sessionRecord, 0, len(r.order))
	for _, sessionID := range r.order {
		record, ok := r.sessions[sessionID]
		if !ok {
			continue
		}
		items = append(items, copySessionRecord(record))
	}
	return items
}

func (r *sessionRegistry) AppendMessage(sessionID session.SessionID, role, kind, text string) (message.CommittedMessage, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return message.CommittedMessage{}, false, nil
	}
	now := r.now().UTC()
	item, err := record.transcript.AppendMessage(role, kind, text, now)
	if err != nil {
		return message.CommittedMessage{}, true, err
	}
	record.updatedAt = now
	record.activityAt = now
	if err := syncSessionRecordState(&record, false); err != nil {
		return message.CommittedMessage{}, true, err
	}
	r.sessions[sessionID] = copySessionRecord(record)
	return item, true, nil
}

func (r *sessionRegistry) AppendAssistantDelta(sessionID session.SessionID, turnID, delta string) (message.PartialAssistantTurn, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return message.PartialAssistantTurn{}, false, nil
	}
	now := r.now().UTC()
	partial, err := record.transcript.AppendAssistantDelta(turnID, delta)
	if err != nil {
		return message.PartialAssistantTurn{}, true, err
	}
	record.updatedAt = now
	record.activityAt = now
	if err := syncSessionRecordState(&record, true); err != nil {
		return message.PartialAssistantTurn{}, true, err
	}
	r.sessions[sessionID] = copySessionRecord(record)
	return partial, true, nil
}

func (r *sessionRegistry) CommitAssistantTurn(sessionID session.SessionID, turnID, text string) (message.CommittedMessage, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return message.CommittedMessage{}, false, nil
	}
	now := r.now().UTC()
	item, err := record.transcript.CommitAssistantTurn(turnID, text, now)
	if err != nil {
		return message.CommittedMessage{}, true, err
	}
	record.updatedAt = now
	record.activityAt = now
	if err := syncSessionRecordState(&record, false); err != nil {
		return message.CommittedMessage{}, true, err
	}
	r.sessions[sessionID] = copySessionRecord(record)
	return item, true, nil
}

func syncSessionRecordState(record *sessionRecord, busy bool) error {
	state, err := session.NewState(record.identity, busy, record.state.Queue(), record.transcript.Tail())
	if err != nil {
		return err
	}
	record.state = state
	return nil
}

func copySessionRecord(record sessionRecord) sessionRecord {
	return sessionRecord{
		identity:        record.identity,
		title:           record.title,
		cwd:             record.cwd,
		provider:        record.provider,
		model:           record.model,
		reasoningEffort: record.reasoningEffort,
		createdAt:       record.createdAt,
		updatedAt:       record.updatedAt,
		activityAt:      record.activityAt,
		state:           copySessionState(record.state),
		transcript:      record.transcript.Clone(),
	}
}

func copySessionState(state session.State) session.State {
	queue, err := session.NewQueueSnapshot(state.Queue().Items())
	if err != nil {
		panic(err)
	}
	copied, err := session.NewState(state.Identity(), state.Busy(), queue, state.Tail())
	if err != nil {
		panic(err)
	}
	return copied
}

func normalizeSessionTitle(raw, cwd string) string {
	title := strings.TrimSpace(raw)
	if title != "" {
		return title
	}
	cleaned := strings.TrimSpace(cwd)
	if cleaned == "" {
		return "session"
	}
	base := filepath.Base(cleaned)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return cleaned
	}
	return base
}
