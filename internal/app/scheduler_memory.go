package app

import (
	"context"
	"sort"
	"sync"
	"time"

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

func (m *memorySchedulerStore) LookupSchedulerItem(_ context.Context, itemID string) (sqlitestore.SchedulerItemRow, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.schedule {
		if row.ItemID == itemID {
			return row, true, nil
		}
	}
	return sqlitestore.SchedulerItemRow{}, false, nil
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

func (m *memorySchedulerStore) ListDueSchedulerItems(_ context.Context, now time.Time, limit int) ([]sqlitestore.SchedulerItemRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := []sqlitestore.SchedulerItemRow{}
	for _, row := range m.schedule {
		if row.State == "scheduled" && !row.DueAt.After(now) {
			rows = append(rows, row)
		}
	}
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

func (m *memorySchedulerStore) CountDueSchedulerItemsForSession(_ context.Context, sessionID string, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, row := range m.schedule {
		if row.SessionID == sessionID && row.State == "scheduled" && !row.DueAt.After(now) {
			count++
		}
	}
	return count, nil
}

func (m *memorySchedulerStore) UpdateSchedulerItem(_ context.Context, row sqlitestore.SchedulerItemRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.schedule {
		if m.schedule[i].ItemID == row.ItemID {
			m.schedule[i] = row
			return nil
		}
	}
	m.schedule = append(m.schedule, row)
	return nil
}

func (m *memorySchedulerStore) InsertInboxItem(_ context.Context, row sqlitestore.InboxItemRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inbox = append(m.inbox, row)
	return nil
}

func (m *memorySchedulerStore) ListReadyInboxItems(_ context.Context, now time.Time, limit int) ([]sqlitestore.InboxItemRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := []sqlitestore.InboxItemRow{}
	for _, row := range m.inbox {
		if row.State == "pending" && !row.DueAt.After(now) {
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

func (m *memorySchedulerStore) CountOpenInboxItemsForSession(_ context.Context, sessionID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, row := range m.inbox {
		if row.SessionID == sessionID && (row.State == "pending" || row.State == "claimed") {
			count++
		}
	}
	return count, nil
}

func (m *memorySchedulerStore) UpdateInboxItem(_ context.Context, row sqlitestore.InboxItemRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.inbox {
		if m.inbox[i].ItemID == row.ItemID {
			m.inbox[i] = row
			return nil
		}
	}
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
