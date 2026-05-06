package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/session"
)

type WaitState string

const (
	WaitPendingUnread WaitState = "pending_unread"
	WaitClaimed       WaitState = "claimed"
	WaitAnswered      WaitState = "answered"
	WaitTimedOut      WaitState = "timed_out_locked"
	WaitCancelled     WaitState = "cancelled"
	WaitOrphaned      WaitState = "orphaned"
)

type ActiveWaitSummary struct {
	WaitID           string    `json:"wait_id"`
	ThreadID         string    `json:"thread_id"`
	SessionID        string    `json:"session_id,omitempty"`
	State            WaitState `json:"state"`
	Question         string    `json:"question"`
	BlockingReason   string    `json:"blocking_reason,omitempty"`
	Attempted        string    `json:"attempted,omitempty"`
	DefaultIfNoReply string    `json:"default_if_no_reply,omitempty"`
	ClaimedAt        *int64    `json:"claimed_at,omitempty"`
	TimeoutAt        *int64    `json:"timeout_at,omitempty"`
	CreatedAt        *int64    `json:"created_at,omitempty"`
	UpdatedAt        *int64    `json:"updated_at,omitempty"`
}

type WaitRecord struct {
	ActiveWaitSummary
	Context      string   `json:"context,omitempty"`
	Answer       string   `json:"answer,omitempty"`
	FallbackUsed string   `json:"fallback_used,omitempty"`
	Files        []string `json:"files,omitempty"`
	AnsweredAt   *int64   `json:"answered_at,omitempty"`
	CancelledAt  *int64   `json:"cancelled_at,omitempty"`
	TimedOutAt   *int64   `json:"timed_out_at,omitempty"`
	OrphanedAt   *int64   `json:"orphaned_at,omitempty"`
}

type WaitThreadSummary struct {
	ThreadID   string             `json:"thread_id"`
	SessionID  string             `json:"session_id"`
	Title      string             `json:"title,omitempty"`
	ActiveWait *ActiveWaitSummary `json:"active_wait,omitempty"`
	CreatedAt  *int64             `json:"created_at,omitempty"`
	UpdatedAt  *int64             `json:"updated_at,omitempty"`
	ClosedAt   *int64             `json:"closed_at,omitempty"`
	WaitCount  int                `json:"wait_count,omitempty"`
}

type RuntimeWaitState string

const (
	RuntimeWaitAnswered  RuntimeWaitState = "answered"
	RuntimeWaitTimedOut  RuntimeWaitState = "timed_out"
	RuntimeWaitCancelled RuntimeWaitState = "cancelled"
	RuntimeWaitOrphaned  RuntimeWaitState = "orphaned"
)

type RuntimeWaitRequest struct {
	SessionID           session.SessionID
	RequestID           string
	Question            string
	Context             string
	BlockingReason      string
	Attempted           string
	DefaultIfNoReply    string
	TimeoutAfterMinutes *int
	Files               []string
}

type RuntimeWaitResult struct {
	State        RuntimeWaitState `json:"state"`
	Answer       string           `json:"answer,omitempty"`
	FallbackUsed string           `json:"fallback_used,omitempty"`
	WaitID       string           `json:"wait_id"`
}

type waitBlocker struct {
	waitID string
	result chan RuntimeWaitResult
}

type CreateWaitRequest struct {
	SessionID           session.SessionID `json:"-"`
	RequestID           string            `json:"request_id,omitempty"`
	ThreadID            string            `json:"thread_id,omitempty"`
	Question            string            `json:"question"`
	Context             string            `json:"context,omitempty"`
	BlockingReason      string            `json:"blocking_reason"`
	Attempted           string            `json:"attempted"`
	DefaultIfNoReply    string            `json:"default_if_no_reply"`
	TimeoutAfterMinutes *int              `json:"timeout_after_minutes,omitempty"`
	Files               []string          `json:"files,omitempty"`
}

type WaitLifecycleRequest struct {
	SessionID session.SessionID
	WaitID    string
	Answer    string
}

type WaitInboxResponse struct {
	OK    bool                `json:"ok"`
	Waits []ActiveWaitSummary `json:"waits"`
}

type WaitThreadsRequest struct {
	SessionID session.SessionID
}

type WaitThreadsResponse struct {
	OK      bool                `json:"ok"`
	Threads []WaitThreadSummary `json:"threads"`
}

type WaitThreadRequest struct {
	SessionID session.SessionID
	ThreadID  string
}

type WaitThreadResponse struct {
	OK     bool              `json:"ok"`
	Thread WaitThreadSummary `json:"thread"`
	Waits  []WaitRecord      `json:"waits"`
}

type WaitLifecycleResponse struct {
	OK         bool               `json:"ok"`
	Wait       *WaitRecord        `json:"wait,omitempty"`
	ActiveWait *ActiveWaitSummary `json:"active_wait,omitempty"`
}

type waitStore interface {
	InsertWait(context.Context, sqlitestore.WaitThreadRow, sqlitestore.WaitRow) error
	LookupWait(context.Context, string, string) (sqlitestore.WaitRow, bool, error)
	UpdateWait(context.Context, sqlitestore.WaitRow) error
	ListActiveWaits(context.Context) ([]sqlitestore.WaitRow, error)
	ListTimedOutPendingWaits(context.Context, time.Time) ([]sqlitestore.WaitRow, error)
	ListSessionWaitThreads(context.Context, string) ([]sqlitestore.WaitThreadRow, error)
	ListThreadWaits(context.Context, string, string) (sqlitestore.WaitThreadRow, []sqlitestore.WaitRow, bool, error)
}

type memoryWaitStore struct {
	mu      sync.Mutex
	threads map[string]sqlitestore.WaitThreadRow
	waits   map[string]sqlitestore.WaitRow
}

func newMemoryWaitStore() *memoryWaitStore {
	return &memoryWaitStore{threads: map[string]sqlitestore.WaitThreadRow{}, waits: map[string]sqlitestore.WaitRow{}}
}

func (m *memoryWaitStore) InsertWait(_ context.Context, thread sqlitestore.WaitThreadRow, wait sqlitestore.WaitRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.waits {
		if existing.SessionID == wait.SessionID && activeWaitState(existing.State) {
			return fmt.Errorf("active wait already exists for session")
		}
	}
	m.threads[thread.ThreadID] = thread
	m.waits[wait.WaitID] = wait
	return nil
}

func (m *memoryWaitStore) LookupWait(_ context.Context, sessionID, waitID string) (sqlitestore.WaitRow, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	wait, ok := m.waits[waitID]
	if !ok || wait.SessionID != sessionID {
		return sqlitestore.WaitRow{}, false, nil
	}
	return wait, true, nil
}

func (m *memoryWaitStore) UpdateWait(_ context.Context, wait sqlitestore.WaitRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.waits[wait.WaitID]; !ok {
		return fmt.Errorf("wait not found")
	}
	m.waits[wait.WaitID] = wait
	thread := m.threads[wait.ThreadID]
	thread.UpdatedAt = wait.UpdatedAt
	if !activeWaitState(wait.State) {
		thread.ClosedAt = &wait.UpdatedAt
	}
	m.threads[thread.ThreadID] = thread
	return nil
}

func (m *memoryWaitStore) ListActiveWaits(_ context.Context) ([]sqlitestore.WaitRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []sqlitestore.WaitRow{}
	for _, wait := range m.waits {
		if activeWaitState(wait.State) {
			out = append(out, wait)
		}
	}
	sortWaitRows(out)
	return out, nil
}

func (m *memoryWaitStore) ListTimedOutPendingWaits(_ context.Context, now time.Time) ([]sqlitestore.WaitRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []sqlitestore.WaitRow{}
	for _, wait := range m.waits {
		if wait.State == string(WaitPendingUnread) && wait.TimeoutAt != nil && !wait.TimeoutAt.After(now) {
			out = append(out, wait)
		}
	}
	sortWaitRows(out)
	return out, nil
}

func (m *memoryWaitStore) ListSessionWaitThreads(_ context.Context, sessionID string) ([]sqlitestore.WaitThreadRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []sqlitestore.WaitThreadRow{}
	for _, thread := range m.threads {
		if thread.SessionID == sessionID {
			out = append(out, thread)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (m *memoryWaitStore) ListThreadWaits(_ context.Context, sessionID, threadID string) (sqlitestore.WaitThreadRow, []sqlitestore.WaitRow, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	thread, ok := m.threads[threadID]
	if !ok || thread.SessionID != sessionID {
		return sqlitestore.WaitThreadRow{}, nil, false, nil
	}
	out := []sqlitestore.WaitRow{}
	for _, wait := range m.waits {
		if wait.SessionID == sessionID && wait.ThreadID == threadID {
			out = append(out, wait)
		}
	}
	sortWaitRows(out)
	return thread, out, true, nil
}

func (s *Stub) WaitInbox(ctx context.Context) (WaitInboxResponse, error) {
	rows, err := s.waitStoreOrMemory().ListActiveWaits(ctx)
	if err != nil {
		return WaitInboxResponse{}, err
	}
	waits := make([]ActiveWaitSummary, 0, len(rows))
	for _, row := range rows {
		waits = append(waits, activeWaitSummaryFromRow(row))
	}
	return WaitInboxResponse{OK: true, Waits: waits}, nil
}

func (s *Stub) WaitThreads(ctx context.Context, req WaitThreadsRequest) (WaitThreadsResponse, error) {
	if _, err := s.lookupSession(req.SessionID); err != nil {
		return WaitThreadsResponse{}, err
	}
	threads, err := s.waitStoreOrMemory().ListSessionWaitThreads(ctx, req.SessionID.String())
	if err != nil {
		return WaitThreadsResponse{}, err
	}
	out := make([]WaitThreadSummary, 0, len(threads))
	for _, thread := range threads {
		_, waits, _, err := s.waitStoreOrMemory().ListThreadWaits(ctx, req.SessionID.String(), thread.ThreadID)
		if err != nil {
			return WaitThreadsResponse{}, err
		}
		out = append(out, waitThreadSummaryFromRows(thread, waits))
	}
	return WaitThreadsResponse{OK: true, Threads: out}, nil
}

func (s *Stub) WaitThread(ctx context.Context, req WaitThreadRequest) (WaitThreadResponse, error) {
	if _, err := s.lookupSession(req.SessionID); err != nil {
		return WaitThreadResponse{}, err
	}
	thread, rows, ok, err := s.waitStoreOrMemory().ListThreadWaits(ctx, req.SessionID.String(), strings.TrimSpace(req.ThreadID))
	if err != nil {
		return WaitThreadResponse{}, err
	}
	if !ok {
		return WaitThreadResponse{}, NotFound("wait thread not found")
	}
	waits := make([]WaitRecord, 0, len(rows))
	for _, row := range rows {
		waits = append(waits, waitRecordFromRow(row))
	}
	return WaitThreadResponse{OK: true, Thread: waitThreadSummaryFromRows(thread, rows), Waits: waits}, nil
}

func (s *Stub) CreateWait(ctx context.Context, req CreateWaitRequest) (WaitLifecycleResponse, error) {
	response, wait, err := s.createWait(ctx, req)
	if err != nil {
		return response, err
	}
	s.emitWaitLifecycle("wait.created", req.SessionID, wait, response)
	return response, nil
}

func (s *Stub) createWait(ctx context.Context, req CreateWaitRequest) (WaitLifecycleResponse, sqlitestore.WaitRow, error) {
	if _, err := s.lookupSession(req.SessionID); err != nil {
		return WaitLifecycleResponse{}, sqlitestore.WaitRow{}, err
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return WaitLifecycleResponse{}, sqlitestore.WaitRow{}, Invalid("question", "question is required")
	}
	if strings.TrimSpace(req.BlockingReason) == "" {
		return WaitLifecycleResponse{}, sqlitestore.WaitRow{}, Invalid("blocking_reason", "blocking_reason is required")
	}
	if strings.TrimSpace(req.Attempted) == "" {
		return WaitLifecycleResponse{}, sqlitestore.WaitRow{}, Invalid("attempted", "attempted is required")
	}
	if strings.TrimSpace(req.DefaultIfNoReply) == "" {
		return WaitLifecycleResponse{}, sqlitestore.WaitRow{}, Invalid("default_if_no_reply", "default_if_no_reply is required")
	}
	now := s.registry.now().UTC()
	threadID := strings.TrimSpace(req.ThreadID)
	if threadID == "" {
		threadID = newID("thread")
	}
	waitID := newID("wait")
	var timeoutAt *time.Time
	if req.TimeoutAfterMinutes != nil && *req.TimeoutAfterMinutes > 0 {
		ts := now.Add(time.Duration(*req.TimeoutAfterMinutes) * time.Minute)
		timeoutAt = &ts
	}
	thread := sqlitestore.WaitThreadRow{ThreadID: threadID, SessionID: req.SessionID.String(), Title: question, CreatedAt: now, UpdatedAt: now}
	wait := sqlitestore.WaitRow{
		WaitID: waitID, ThreadID: threadID, SessionID: req.SessionID.String(), RequestID: strings.TrimSpace(req.RequestID),
		State: string(WaitPendingUnread), Question: question, Context: strings.TrimSpace(req.Context), BlockingReason: strings.TrimSpace(req.BlockingReason),
		Attempted: strings.TrimSpace(req.Attempted), DefaultIfNoReply: strings.TrimSpace(req.DefaultIfNoReply), TimeoutAt: timeoutAt,
		CreatedAt: now, UpdatedAt: now, Files: normalizeStringList(req.Files),
	}
	if err := s.waitStoreOrMemory().InsertWait(ctx, thread, wait); err != nil {
		return WaitLifecycleResponse{}, sqlitestore.WaitRow{}, Conflict("session already has an active wait")
	}
	record := waitRecordFromRow(wait)
	active := activeWaitSummaryFromRow(wait)
	response := WaitLifecycleResponse{OK: true, Wait: &record, ActiveWait: &active}
	return response, wait, nil
}

func (s *Stub) ClaimWait(ctx context.Context, req WaitLifecycleRequest) (WaitLifecycleResponse, error) {
	return s.transitionWait(ctx, req, "wait.claimed", func(now time.Time, wait *sqlitestore.WaitRow) error {
		if wait.State != string(WaitPendingUnread) {
			return Conflict("only pending waits can be claimed")
		}
		wait.State = string(WaitClaimed)
		wait.ClaimedAt = &now
		return nil
	})
}

func (s *Stub) AnswerWait(ctx context.Context, req WaitLifecycleRequest) (WaitLifecycleResponse, error) {
	answer := strings.TrimSpace(req.Answer)
	if answer == "" {
		return WaitLifecycleResponse{}, Invalid("answer", "answer is required")
	}
	return s.transitionWait(ctx, req, "wait.answered", func(now time.Time, wait *sqlitestore.WaitRow) error {
		if wait.State != string(WaitClaimed) {
			return Conflict("only claimed waits can be answered")
		}
		wait.State = string(WaitAnswered)
		wait.Answer = answer
		wait.AnsweredAt = &now
		return nil
	})
}

func (s *Stub) CancelWait(ctx context.Context, req WaitLifecycleRequest) (WaitLifecycleResponse, error) {
	return s.transitionWait(ctx, req, "wait.cancelled", func(now time.Time, wait *sqlitestore.WaitRow) error {
		if !activeWaitState(wait.State) {
			return Conflict("only active waits can be cancelled")
		}
		wait.State = string(WaitCancelled)
		wait.CancelledAt = &now
		return nil
	})
}

func (s *Stub) transitionWait(ctx context.Context, req WaitLifecycleRequest, eventType string, apply func(time.Time, *sqlitestore.WaitRow) error) (WaitLifecycleResponse, error) {
	if _, err := s.lookupSession(req.SessionID); err != nil {
		return WaitLifecycleResponse{}, err
	}
	wait, ok, err := s.waitStoreOrMemory().LookupWait(ctx, req.SessionID.String(), strings.TrimSpace(req.WaitID))
	if err != nil {
		return WaitLifecycleResponse{}, err
	}
	if !ok {
		return WaitLifecycleResponse{}, NotFound("wait not found")
	}
	now := s.registry.now().UTC()
	if err := apply(now, &wait); err != nil {
		return WaitLifecycleResponse{}, err
	}
	wait.UpdatedAt = now
	if err := s.waitStoreOrMemory().UpdateWait(ctx, wait); err != nil {
		return WaitLifecycleResponse{}, err
	}
	record := waitRecordFromRow(wait)
	var active *ActiveWaitSummary
	if activeWaitState(wait.State) {
		summary := activeWaitSummaryFromRow(wait)
		active = &summary
	}
	response := WaitLifecycleResponse{OK: true, Wait: &record, ActiveWait: active}
	s.emitWaitLifecycle(eventType, req.SessionID, wait, response)
	if !activeWaitState(wait.State) {
		s.wakeRuntimeWaiter(wait)
	}
	return response, nil
}

func (s *Stub) AskUserWait(ctx context.Context, req RuntimeWaitRequest) (RuntimeWaitResult, error) {
	response, wait, err := s.createWait(ctx, CreateWaitRequest{
		SessionID:           req.SessionID,
		RequestID:           req.RequestID,
		Question:            req.Question,
		Context:             req.Context,
		BlockingReason:      req.BlockingReason,
		Attempted:           req.Attempted,
		DefaultIfNoReply:    req.DefaultIfNoReply,
		TimeoutAfterMinutes: req.TimeoutAfterMinutes,
		Files:               req.Files,
	})
	if err != nil {
		return RuntimeWaitResult{}, err
	}
	if response.Wait == nil {
		return RuntimeWaitResult{}, fmt.Errorf("created wait without wait record")
	}
	blocker := waitBlocker{waitID: wait.WaitID, result: make(chan RuntimeWaitResult, 1)}
	s.waitBlockersMu.Lock()
	if s.waitBlockers == nil {
		s.waitBlockers = map[string]waitBlocker{}
	}
	s.waitBlockers[wait.WaitID] = blocker
	s.waitBlockersMu.Unlock()
	s.emitWaitLifecycle("wait.created", req.SessionID, wait, response)
	if response.Wait != nil && response.Wait.State != WaitPendingUnread {
		return RuntimeWaitResult{State: runtimeWaitStateFromWaitState(string(response.Wait.State)), Answer: response.Wait.Answer, FallbackUsed: response.Wait.FallbackUsed, WaitID: wait.WaitID}, nil
	}
	defer s.removeRuntimeWaiter(wait.WaitID)

	select {
	case result := <-blocker.result:
		return result, nil
	case <-ctx.Done():
		_, _ = s.CancelWait(context.Background(), WaitLifecycleRequest{SessionID: req.SessionID, WaitID: wait.WaitID})
		return RuntimeWaitResult{}, ctx.Err()
	}
}

func (s *Stub) runWaitTimeoutSweep(ctx context.Context) {
	if err := s.sweepWaitTimeouts(ctx); err != nil {
		return
	}
}

func (s *Stub) RunWaitTimeoutSweep(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runWaitTimeoutSweep(ctx)
		}
	}
}

func (s *Stub) sweepWaitTimeouts(ctx context.Context) error {
	now := s.registry.now().UTC()
	rows, err := s.waitStoreOrMemory().ListTimedOutPendingWaits(ctx, now)
	if err != nil {
		return err
	}
	for _, wait := range rows {
		if wait.State != string(WaitPendingUnread) || wait.TimeoutAt == nil || wait.TimeoutAt.After(now) {
			continue
		}
		wait.State = string(WaitTimedOut)
		wait.FallbackUsed = wait.DefaultIfNoReply
		wait.TimedOutAt = &now
		wait.UpdatedAt = now
		if err := s.waitStoreOrMemory().UpdateWait(ctx, wait); err != nil {
			return err
		}
		record := waitRecordFromRow(wait)
		response := WaitLifecycleResponse{OK: true, Wait: &record}
		s.emitWaitLifecycle("wait.timed_out", mustSessionIDFromString(wait.SessionID), wait, response)
		s.wakeRuntimeWaiter(wait)
	}
	return nil
}

func (s *Stub) orphanActiveWaits(ctx context.Context, sessionID *session.SessionID) error {
	now := s.registry.now().UTC()
	rows, err := s.waitStoreOrMemory().ListActiveWaits(ctx)
	if err != nil {
		return err
	}
	for _, wait := range rows {
		if sessionID != nil && wait.SessionID != sessionID.String() {
			continue
		}
		wait.State = string(WaitOrphaned)
		wait.OrphanedAt = &now
		wait.UpdatedAt = now
		if err := s.waitStoreOrMemory().UpdateWait(ctx, wait); err != nil {
			return err
		}
		record := waitRecordFromRow(wait)
		response := WaitLifecycleResponse{OK: true, Wait: &record}
		s.emitWaitLifecycle("wait.orphaned", mustSessionIDFromString(wait.SessionID), wait, response)
		s.wakeRuntimeWaiter(wait)
	}
	return nil
}

func (s *Stub) removeRuntimeWaiter(waitID string) {
	s.waitBlockersMu.Lock()
	delete(s.waitBlockers, strings.TrimSpace(waitID))
	s.waitBlockersMu.Unlock()
}

func (s *Stub) wakeRuntimeWaiter(wait sqlitestore.WaitRow) {
	s.waitBlockersMu.Lock()
	blocker, ok := s.waitBlockers[wait.WaitID]
	if ok {
		delete(s.waitBlockers, wait.WaitID)
	}
	s.waitBlockersMu.Unlock()
	if !ok {
		return
	}
	result := RuntimeWaitResult{State: runtimeWaitStateFromWaitState(wait.State), Answer: wait.Answer, FallbackUsed: wait.FallbackUsed, WaitID: blocker.waitID}
	select {
	case blocker.result <- result:
	default:
	}
}

func runtimeWaitStateFromWaitState(state string) RuntimeWaitState {
	switch state {
	case string(WaitAnswered):
		return RuntimeWaitAnswered
	case string(WaitTimedOut):
		return RuntimeWaitTimedOut
	case string(WaitCancelled):
		return RuntimeWaitCancelled
	case string(WaitOrphaned):
		return RuntimeWaitOrphaned
	default:
		return RuntimeWaitState(state)
	}
}

func mustSessionIDFromString(raw string) session.SessionID {
	id, _ := session.ParseSessionID(raw)
	return id
}

func (s *Stub) emitWaitLifecycle(eventType string, sessionID session.SessionID, wait sqlitestore.WaitRow, response WaitLifecycleResponse) {
	if s == nil || s.sink == nil {
		return
	}
	publisher, ok := s.sink.(interface {
		PublishWaitLifecycle(WaitLifecycleEvent)
		PublishWaitsUpdated(WaitsUpdatedEvent)
	})
	if !ok {
		return
	}
	publisher.PublishWaitLifecycle(WaitLifecycleEvent{Type: eventType, SessionID: sessionID, Wait: waitRecordFromRow(wait), ActiveWait: response.ActiveWait})
	publisher.PublishWaitsUpdated(WaitsUpdatedEvent{Waits: activeWaitsForEvent(s.waitStoreOrMemory())})
}

func activeWaitsForEvent(store waitStore) []ActiveWaitSummary {
	rows, err := store.ListActiveWaits(context.Background())
	if err != nil {
		return nil
	}
	out := make([]ActiveWaitSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, activeWaitSummaryFromRow(row))
	}
	return out
}

func (s *Stub) waitStoreOrMemory() waitStore {
	if s.waitStore != nil {
		return s.waitStore
	}
	s.appStateMu.Lock()
	defer s.appStateMu.Unlock()
	if s.waitStore == nil {
		s.waitStore = newMemoryWaitStore()
	}
	return s.waitStore
}

func (s *Stub) activeWaitForSession(sessionID session.SessionID) *ActiveWaitSummary {
	rows, err := s.waitStoreOrMemory().ListActiveWaits(context.Background())
	if err != nil {
		return nil
	}
	for _, row := range rows {
		if row.SessionID == sessionID.String() {
			summary := activeWaitSummaryFromRow(row)
			return &summary
		}
	}
	return nil
}

func waitThreadSummaryFromRows(thread sqlitestore.WaitThreadRow, waits []sqlitestore.WaitRow) WaitThreadSummary {
	summary := WaitThreadSummary{
		ThreadID:  thread.ThreadID,
		SessionID: thread.SessionID,
		Title:     thread.Title,
		CreatedAt: unixSecondsPtrFromTime(&thread.CreatedAt),
		UpdatedAt: unixSecondsPtrFromTime(&thread.UpdatedAt),
		ClosedAt:  unixSecondsPtrFromTime(thread.ClosedAt),
		WaitCount: len(waits),
	}
	for _, wait := range waits {
		if activeWaitState(wait.State) {
			active := activeWaitSummaryFromRow(wait)
			summary.ActiveWait = &active
			break
		}
	}
	return summary
}

func waitRecordFromRow(row sqlitestore.WaitRow) WaitRecord {
	return WaitRecord{
		ActiveWaitSummary: activeWaitSummaryFromRow(row),
		Context:           row.Context,
		Answer:            row.Answer,
		FallbackUsed:      row.FallbackUsed,
		Files:             append([]string(nil), row.Files...),
		AnsweredAt:        unixSecondsPtrFromTime(row.AnsweredAt),
		CancelledAt:       unixSecondsPtrFromTime(row.CancelledAt),
		TimedOutAt:        unixSecondsPtrFromTime(row.TimedOutAt),
		OrphanedAt:        unixSecondsPtrFromTime(row.OrphanedAt),
	}
}

func activeWaitSummaryFromRow(row sqlitestore.WaitRow) ActiveWaitSummary {
	return ActiveWaitSummary{
		WaitID:           row.WaitID,
		ThreadID:         row.ThreadID,
		SessionID:        row.SessionID,
		State:            WaitState(row.State),
		Question:         row.Question,
		BlockingReason:   row.BlockingReason,
		Attempted:        row.Attempted,
		DefaultIfNoReply: row.DefaultIfNoReply,
		ClaimedAt:        unixSecondsPtrFromTime(row.ClaimedAt),
		TimeoutAt:        unixSecondsPtrFromTime(row.TimeoutAt),
		CreatedAt:        unixSecondsPtrFromTime(&row.CreatedAt),
		UpdatedAt:        unixSecondsPtrFromTime(&row.UpdatedAt),
	}
}

func unixSecondsPtrFromTime(ts *time.Time) *int64 {
	if ts == nil || ts.IsZero() {
		return nil
	}
	value := ts.Unix()
	return &value
}

func activeWaitState(state string) bool {
	return state == string(WaitPendingUnread) || state == string(WaitClaimed)
}

func sortWaitRows(rows []sqlitestore.WaitRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		}
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
}

func normalizeStringList(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func newID(prefix string) string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}
