package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/message"
	"actrail/internal/domain/session"
)

type sessionStore interface {
	UpsertSession(context.Context, sqlitestore.SessionRow) error
	ListSessions(context.Context, bool) ([]sqlitestore.SessionRow, error)
}

func durableSessionRowFromRecord(record sessionRecord) sqlitestore.SessionRow {
	return sqlitestore.SessionRow{
		SessionID:           record.identity.SessionID().String(),
		Backend:             record.identity.Backend().String(),
		CWD:                 strings.TrimSpace(record.cwd),
		Title:               strings.TrimSpace(record.title),
		Alias:               strings.TrimSpace(record.alias),
		Provider:            strings.TrimSpace(record.provider),
		Model:               strings.TrimSpace(record.model),
		ReasoningEffort:     strings.TrimSpace(record.reasoningEffort),
		CreatedAt:           record.createdAt.UTC(),
		UpdatedAt:           record.updatedAt.UTC(),
		ActivityAt:          record.activityAt.UTC(),
		Focused:             record.focused,
		Hidden:              record.hidden,
		PriorityOffset:      record.priorityOffset,
		SnoozeUntil:         copyTimePtr(record.snoozeUntil),
		DependencySessionID: sessionIDPtrString(record.dependencySessionID),
		ArchivedAt:          copyTimePtr(record.archivedAt),
	}
}

func sessionRecordFromDurableRow(row sqlitestore.SessionRow) (sessionRecord, error) {
	identity, err := session.NewDetachedIdentity(row.SessionID, row.Backend)
	if err != nil {
		return sessionRecord{}, err
	}
	transcript := message.NewTranscript()
	state, err := session.NewState(identity, false, session.EmptyQueueSnapshot(), transcript.Tail())
	if err != nil {
		return sessionRecord{}, err
	}
	return sessionRecord{
		identity:            identity,
		title:               strings.TrimSpace(row.Title),
		alias:               strings.TrimSpace(row.Alias),
		cwd:                 strings.TrimSpace(row.CWD),
		provider:            strings.TrimSpace(row.Provider),
		model:               strings.TrimSpace(row.Model),
		reasoningEffort:     strings.TrimSpace(row.ReasoningEffort),
		focused:             row.Focused,
		hidden:              row.Hidden,
		priorityOffset:      row.PriorityOffset,
		snoozeUntil:         copyTimePtr(row.SnoozeUntil),
		dependencySessionID: parseDependencySessionID(row.DependencySessionID),
		createdAt:           row.CreatedAt.UTC(),
		updatedAt:           row.UpdatedAt.UTC(),
		activityAt:          row.ActivityAt.UTC(),
		archivedAt:          copyTimePtr(row.ArchivedAt),
		state:               state,
		transcript:          transcript,
		inputMu:             &sync.Mutex{},
	}, nil
}

func parseDependencySessionID(raw *string) *session.SessionID {
	if raw == nil {
		return nil
	}
	parsed, err := session.ParseSessionID(*raw)
	if err != nil {
		return nil
	}
	return &parsed
}

func sessionDisplayName(record sessionRecord) string {
	for _, candidate := range []string{
		strings.TrimSpace(record.alias),
		strings.TrimSpace(record.title),
		strings.TrimSpace(record.cwd),
		record.identity.SessionID().String(),
	} {
		if candidate != "" {
			return candidate
		}
	}
	return "session"
}

func seedSessionCounter(current uint64, sessionID session.SessionID) uint64 {
	const prefix = "s_"
	raw := sessionID.String()
	if !strings.HasPrefix(raw, prefix) {
		return current
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(raw, prefix), 10, 64)
	if err != nil || n <= current {
		return current
	}
	return n
}

func loadPersistedSessions(store sessionStore) ([]sessionRecord, error) {
	if store == nil {
		return nil, nil
	}
	rows, err := store.ListSessions(context.Background(), false)
	if err != nil {
		return nil, fmt.Errorf("load persisted sessions: %w", err)
	}
	records := make([]sessionRecord, 0, len(rows))
	for _, row := range rows {
		record, err := sessionRecordFromDurableRow(row)
		if err != nil {
			return nil, fmt.Errorf("rehydrate session %q: %w", row.SessionID, err)
		}
		records = append(records, record)
	}
	return records, nil
}

func (r *sessionRegistry) persistLocked(record sessionRecord) error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.UpsertSession(context.Background(), durableSessionRowFromRecord(record))
}

func (r *sessionRegistry) Rehydrate(records []sessionRecord) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = make(map[session.SessionID]sessionRecord, len(records))
	r.order = make([]session.SessionID, 0, len(records))
	r.nextID.session = 0
	for _, raw := range records {
		record := copySessionRecord(raw)
		sessionID := record.identity.SessionID()
		if _, exists := r.sessions[sessionID]; exists {
			return fmt.Errorf("duplicate persisted session %q", sessionID)
		}
		r.sessions[sessionID] = record
		r.order = append(r.order, sessionID)
		r.nextID.session = seedSessionCounter(r.nextID.session, sessionID)
	}
	return nil
}
