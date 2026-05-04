package app

import (
	"context"
	"sort"
	"sync"

	sqlitestore "actrail/internal/adapters/sqlite"
)

type memorySchedulerStore struct {
	mu       sync.Mutex
	settings *sqlitestore.SchedulerSettingsRow
	schedule []sqlitestore.SchedulerItemRow
	inbox    []sqlitestore.InboxItemRow
}

func newMemorySchedulerStore() *memorySchedulerStore {
	return &memorySchedulerStore{schedule: []sqlitestore.SchedulerItemRow{}, inbox: []sqlitestore.InboxItemRow{}}
}

func (m *memorySchedulerStore) LookupSchedulerSettings(context.Context) (sqlitestore.SchedulerSettingsRow, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings == nil {
		return sqlitestore.SchedulerSettingsRow{}, false, nil
	}
	return *m.settings, true, nil
}

func (m *memorySchedulerStore) UpsertSchedulerSettings(_ context.Context, row sqlitestore.SchedulerSettingsRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := row
	m.settings = &cp
	return nil
}

func (m *memorySchedulerStore) InsertSchedulerItem(_ context.Context, row sqlitestore.SchedulerItemRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedule = append(m.schedule, row)
	return nil
}

func (m *memorySchedulerStore) ListSchedulerItems(_ context.Context, limit int) ([]sqlitestore.SchedulerItemRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := append([]sqlitestore.SchedulerItemRow(nil), m.schedule...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].DueAt.Equal(rows[j].DueAt) {
			return rows[i].CreatedAt.Before(rows[j].CreatedAt)
		}
		return rows[i].DueAt.Before(rows[j].DueAt)
	})
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (m *memorySchedulerStore) InsertInboxItem(_ context.Context, row sqlitestore.InboxItemRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inbox = append(m.inbox, row)
	return nil
}

func (m *memorySchedulerStore) ListInboxItems(_ context.Context, sessionID string, limit int) ([]sqlitestore.InboxItemRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := []sqlitestore.InboxItemRow{}
	for _, row := range m.inbox {
		if sessionID == "" || row.SessionID == sessionID {
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].DueAt.Equal(rows[j].DueAt) {
			if rows[i].Priority == rows[j].Priority {
				return rows[i].CreatedAt.Before(rows[j].CreatedAt)
			}
			return rows[i].Priority > rows[j].Priority
		}
		return rows[i].DueAt.Before(rows[j].DueAt)
	})
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}
