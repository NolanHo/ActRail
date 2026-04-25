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
	UpsertSessionSnapshot(context.Context, sqlitestore.SessionSnapshotRow) error
	ListSessionSnapshots(context.Context, bool) ([]sqlitestore.SessionSnapshotRow, error)
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

func durableSessionSnapshotFromRecord(record sessionRecord) sqlitestore.SessionSnapshotRow {
	queueItems := record.state.Queue().Items()
	durableQueue := make([]sqlitestore.QueueItemRow, 0, len(queueItems))
	for idx, item := range queueItems {
		durableQueue = append(durableQueue, sqlitestore.QueueItemRow{
			Ordinal: idx,
			ItemID:  item.ID().String(),
			Text:    item.Text(),
			State:   item.State().String(),
		})
	}
	history := make([]sqlitestore.WorkspaceHistoryItemRow, 0, len(record.workspace.HistoryItems))
	for idx, item := range record.workspace.HistoryItems {
		history = append(history, sqlitestore.WorkspaceHistoryItemRow{
			Ordinal: idx,
			Path:    item.Path,
			Label:   item.Label,
		})
	}
	return sqlitestore.SessionSnapshotRow{
		Session: durableSessionRowFromRecord(record),
		Queue:   durableQueue,
		Workspace: sqlitestore.WorkspaceStateRow{
			SelectedPath: strings.TrimSpace(record.workspace.SelectedPath),
			OpenPaths:    append([]string(nil), record.workspace.OpenPaths...),
			HistoryItems: history,
		},
	}
}

func sessionRecordFromDurableSnapshot(snapshot sqlitestore.SessionSnapshotRow) (sessionRecord, error) {
	identity, err := session.NewDetachedIdentity(snapshot.Session.SessionID, snapshot.Session.Backend)
	if err != nil {
		return sessionRecord{}, err
	}
	queue, err := queueSnapshotFromDurableRows(snapshot.Queue)
	if err != nil {
		return sessionRecord{}, err
	}
	transcript := message.NewTranscript()
	state, err := session.NewState(identity, false, queue, transcript.Tail())
	if err != nil {
		return sessionRecord{}, err
	}
	return sessionRecord{
		identity:            identity,
		title:               strings.TrimSpace(snapshot.Session.Title),
		alias:               strings.TrimSpace(snapshot.Session.Alias),
		cwd:                 strings.TrimSpace(snapshot.Session.CWD),
		provider:            strings.TrimSpace(snapshot.Session.Provider),
		model:               strings.TrimSpace(snapshot.Session.Model),
		reasoningEffort:     strings.TrimSpace(snapshot.Session.ReasoningEffort),
		focused:             snapshot.Session.Focused,
		hidden:              snapshot.Session.Hidden,
		priorityOffset:      snapshot.Session.PriorityOffset,
		snoozeUntil:         copyTimePtr(snapshot.Session.SnoozeUntil),
		dependencySessionID: parseDependencySessionID(snapshot.Session.DependencySessionID),
		createdAt:           snapshot.Session.CreatedAt.UTC(),
		updatedAt:           snapshot.Session.UpdatedAt.UTC(),
		activityAt:          snapshot.Session.ActivityAt.UTC(),
		archivedAt:          copyTimePtr(snapshot.Session.ArchivedAt),
		state:               state,
		workspace:           workspaceStateFromDurableRow(snapshot.Workspace),
		transcript:          transcript,
		inputMu:             &sync.Mutex{},
	}, nil
}

func queueSnapshotFromDurableRows(rows []sqlitestore.QueueItemRow) (session.QueueSnapshot, error) {
	items := make([]session.QueueItem, 0, len(rows))
	for _, row := range rows {
		item, err := session.NewQueueItem(row.ItemID, row.Text, session.QueueItemState(row.State))
		if err != nil {
			return session.QueueSnapshot{}, err
		}
		items = append(items, item)
	}
	return session.NewQueueSnapshot(items)
}

func workspaceStateFromDurableRow(row sqlitestore.WorkspaceStateRow) workspaceBrowserState {
	historyItems := make([]WorkspaceHistoryItem, 0, len(row.HistoryItems))
	for _, item := range row.HistoryItems {
		historyItems = append(historyItems, WorkspaceHistoryItem{Path: strings.TrimSpace(item.Path), Label: strings.TrimSpace(item.Label)})
	}
	return workspaceBrowserState{
		SelectedPath: strings.TrimSpace(row.SelectedPath),
		OpenPaths:    append([]string(nil), row.OpenPaths...),
		HistoryItems: historyItems,
	}
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

func seedQueueCounter(current uint64, itemID session.QueueItemID) uint64 {
	const prefix = "q_"
	raw := itemID.String()
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
	snapshots, err := store.ListSessionSnapshots(context.Background(), false)
	if err != nil {
		return nil, fmt.Errorf("load persisted sessions: %w", err)
	}
	records := make([]sessionRecord, 0, len(snapshots))
	for _, snapshot := range snapshots {
		record, err := sessionRecordFromDurableSnapshot(snapshot)
		if err != nil {
			return nil, fmt.Errorf("rehydrate session %q: %w", snapshot.Session.SessionID, err)
		}
		records = append(records, record)
	}
	return records, nil
}

func (r *sessionRegistry) persistLocked(record sessionRecord) error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.UpsertSessionSnapshot(context.Background(), durableSessionSnapshotFromRecord(record))
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
	r.nextID.queue = 0
	for _, raw := range records {
		record := copySessionRecord(raw)
		sessionID := record.identity.SessionID()
		if _, exists := r.sessions[sessionID]; exists {
			return fmt.Errorf("duplicate persisted session %q", sessionID)
		}
		r.sessions[sessionID] = record
		r.order = append(r.order, sessionID)
		r.nextID.session = seedSessionCounter(r.nextID.session, sessionID)
		for _, item := range record.state.Queue().Items() {
			r.nextID.queue = seedQueueCounter(r.nextID.queue, item.ID())
		}
	}
	return nil
}
