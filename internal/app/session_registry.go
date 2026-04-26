package app

import (
	"errors"
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
	store    sessionStore
	nextID   sessionIDGenerator
	order    []session.SessionID
	sessions map[session.SessionID]sessionRecord
}

type sessionIDGenerator struct {
	session uint64
	runtime uint64
	thread  uint64
	queue   uint64
}

type sessionCreateSpec struct {
	Backend         session.Backend
	CWD             string
	Provider        string
	Model           string
	ReasoningEffort string
	Title           string
	Runtime         sessionRuntime
}

type sessionRecord struct {
	identity                 session.Identity
	title                    string
	alias                    string
	cwd                      string
	provider                 string
	model                    string
	reasoningEffort          string
	focused                  bool
	hidden                   bool
	priorityOffset           float64
	snoozeUntil              *time.Time
	dependencySessionID      *session.SessionID
	createdAt                time.Time
	updatedAt                time.Time
	activityAt               time.Time
	archivedAt               *time.Time
	state                    session.State
	workspace                workspaceBrowserState
	transcript               message.Transcript
	importedSourcePath       string
	importedFirstUserMessage string
	runtime                  sessionRuntime
	uiRequest                *SessionUIRequestSnapshot
	resumeCursors            SessionResumeCursors
	inputMu                  *sync.Mutex
}

func newSessionRegistry(now func() time.Time, stores ...sessionStore) *sessionRegistry {
	if now == nil {
		now = time.Now
	}
	var store sessionStore
	if len(stores) > 0 {
		store = stores[0]
	}
	return &sessionRegistry{
		now:      now,
		store:    store,
		sessions: make(map[session.SessionID]sessionRecord),
		order:    make([]session.SessionID, 0),
	}
}

func (r *sessionRegistry) Create(spec sessionCreateSpec) (sessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now().UTC()
	var identity session.Identity
	for {
		r.nextID.session++
		r.nextID.runtime++
		r.nextID.thread++
		candidate, err := session.NewLiveIdentity(
			fmt.Sprintf("s_%d", r.nextID.session),
			fmt.Sprintf("r_%d", r.nextID.runtime),
			fmt.Sprintf("t_%d", r.nextID.thread),
			spec.Backend.String(),
		)
		if err != nil {
			return sessionRecord{}, err
		}
		if _, exists := r.sessions[candidate.SessionID()]; exists {
			continue
		}
		identity = candidate
		break
	}
	transcript := message.NewTranscript()
	state, err := session.NewState(identity, false, session.EmptyQueueSnapshot(), transcript.Tail())
	if err != nil {
		return sessionRecord{}, err
	}
	name := normalizeSessionTitle(spec.Title, spec.CWD)
	record := sessionRecord{
		identity:        identity,
		title:           name,
		alias:           name,
		cwd:             strings.TrimSpace(spec.CWD),
		provider:        strings.TrimSpace(spec.Provider),
		model:           strings.TrimSpace(spec.Model),
		reasoningEffort: strings.TrimSpace(spec.ReasoningEffort),
		createdAt:       now,
		updatedAt:       now,
		activityAt:      now,
		state:           state,
		transcript:      transcript,
		runtime:         spec.Runtime,
		inputMu:         &sync.Mutex{},
	}
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return sessionRecord{}, err
	}
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

func (r *sessionRegistry) LookupRoute(routeID session.SessionID) (sessionRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, record, ok := r.resolveLocked(routeID)
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
		if !ok || record.hidden {
			continue
		}
		items = append(items, copySessionRecord(record))
	}
	return items
}

func (r *sessionRegistry) Update(routeID session.SessionID, touchActivity bool, apply func(*sessionRecord) error) (sessionRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actualID, record, ok := r.resolveLocked(routeID)
	if !ok {
		return sessionRecord{}, false, nil
	}
	if err := apply(&record); err != nil {
		return sessionRecord{}, true, err
	}
	now := r.now().UTC()
	record.updatedAt = now
	if touchActivity {
		record.activityAt = now
	}
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return sessionRecord{}, true, err
	}
	r.sessions[actualID] = cp
	return copySessionRecord(cp), true, nil
}

func (r *sessionRegistry) Delete(routeID session.SessionID) (sessionRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actualID, record, ok := r.resolveLocked(routeID)
	if !ok {
		return sessionRecord{}, false, nil
	}
	now := r.now().UTC()
	record.focused = false
	record.updatedAt = now
	record.activityAt = now
	record.archivedAt = &now
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return sessionRecord{}, true, err
	}
	delete(r.sessions, actualID)
	for idx, sessionID := range r.order {
		if sessionID != actualID {
			continue
		}
		r.order = append(r.order[:idx], r.order[idx+1:]...)
		break
	}
	return copySessionRecord(cp), true, nil
}

func (r *sessionRegistry) resolveLocked(routeID session.SessionID) (session.SessionID, sessionRecord, bool) {
	if record, ok := r.sessions[routeID]; ok {
		return routeID, record, true
	}
	token := routeID.String()
	for actualID, record := range r.sessions {
		runtimeID, ok := record.identity.RuntimeID()
		if !ok {
			continue
		}
		if runtimeID.String() == token {
			return actualID, record, true
		}
	}
	return "", sessionRecord{}, false
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
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return message.CommittedMessage{}, true, err
	}
	r.sessions[sessionID] = cp
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
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return message.PartialAssistantTurn{}, true, err
	}
	r.sessions[sessionID] = cp
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
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return message.CommittedMessage{}, true, err
	}
	r.sessions[sessionID] = cp
	return item, true, nil
}

var (
	errNoPendingUIRequest  = errors.New("session ui request not found")
	errUnexpectedUIRequest = errors.New("session ui request does not match")
)

func (r *sessionRegistry) ActivateSend(sessionID session.SessionID, text string) (message.CommittedMessage, session.State, *SessionUIRequestSnapshot, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return message.CommittedMessage{}, session.State{}, nil, false, nil
	}
	now := r.now().UTC()
	item, err := record.transcript.AppendMessage(message.RoleUser.String(), message.KindMessage.String(), strings.TrimSpace(text), now)
	if err != nil {
		return message.CommittedMessage{}, session.State{}, nil, true, err
	}
	record.updatedAt = now
	record.activityAt = now
	if err := syncSessionRecordStateWithQueue(&record, true, session.EmptyQueueSnapshot()); err != nil {
		return message.CommittedMessage{}, session.State{}, nil, true, err
	}
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return message.CommittedMessage{}, session.State{}, nil, true, err
	}
	r.sessions[sessionID] = cp
	return item, copySessionState(cp.state), copySessionUIRequest(cp.uiRequest), true, nil
}

func (r *sessionRegistry) ReplaceQueue(sessionID session.SessionID, text string) (session.State, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return session.State{}, false, nil
	}
	now := r.now().UTC()
	r.nextID.queue++
	item, err := session.NewQueueItem(fmt.Sprintf("q_%d", r.nextID.queue), strings.TrimSpace(text), session.QueueItemStateQueued)
	if err != nil {
		return session.State{}, true, err
	}
	queue, err := session.NewQueueSnapshot([]session.QueueItem{item})
	if err != nil {
		return session.State{}, true, err
	}
	record.updatedAt = now
	record.activityAt = now
	if err := syncSessionRecordStateWithQueue(&record, record.state.Busy(), queue); err != nil {
		return session.State{}, true, err
	}
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return session.State{}, true, err
	}
	r.sessions[sessionID] = cp
	return copySessionState(cp.state), true, nil
}

func (r *sessionRegistry) ActivateQueued(sessionID session.SessionID, itemID session.QueueItemID) (message.CommittedMessage, session.State, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return message.CommittedMessage{}, session.State{}, false, nil
	}
	if record.state.Busy() || record.uiRequest != nil {
		return message.CommittedMessage{}, copySessionState(record.state), false, nil
	}
	items := record.state.Queue().Items()
	if len(items) == 0 {
		return message.CommittedMessage{}, copySessionState(record.state), false, nil
	}
	queued := items[0]
	if itemID != "" && queued.ID() != itemID {
		return message.CommittedMessage{}, copySessionState(record.state), false, nil
	}
	now := r.now().UTC()
	committed, err := record.transcript.AppendMessage(message.RoleUser.String(), message.KindMessage.String(), queued.Text(), now)
	if err != nil {
		return message.CommittedMessage{}, session.State{}, true, err
	}
	queue, err := session.NewQueueSnapshot(items[1:])
	if err != nil {
		return message.CommittedMessage{}, session.State{}, true, err
	}
	record.updatedAt = now
	record.activityAt = now
	if err := syncSessionRecordStateWithQueue(&record, true, queue); err != nil {
		return message.CommittedMessage{}, session.State{}, true, err
	}
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return message.CommittedMessage{}, session.State{}, true, err
	}
	r.sessions[sessionID] = cp
	return committed, copySessionState(cp.state), true, nil
}

func (r *sessionRegistry) SetBusy(sessionID session.SessionID, busy bool) (session.State, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return session.State{}, false, nil
	}
	now := r.now().UTC()
	record.updatedAt = now
	record.activityAt = now
	if err := syncSessionRecordState(&record, busy); err != nil {
		return session.State{}, true, err
	}
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return session.State{}, true, err
	}
	r.sessions[sessionID] = cp
	return copySessionState(cp.state), true, nil
}

func (r *sessionRegistry) SetUIRequest(sessionID session.SessionID, request SessionUIRequestSnapshot) (*SessionUIRequestSnapshot, bool, error) {
	normalized, err := normalizeSessionUIRequest(request)
	if err != nil {
		return nil, true, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return nil, false, nil
	}
	now := r.now().UTC()
	record.updatedAt = now
	record.activityAt = now
	record.uiRequest = &normalized
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return nil, true, err
	}
	r.sessions[sessionID] = cp
	return copySessionUIRequest(cp.uiRequest), true, nil
}

func (r *sessionRegistry) ClearUIRequest(sessionID session.SessionID, requestID string) (SessionUIRequestSnapshot, session.State, bool, error) {
	id := strings.TrimSpace(requestID)
	if id == "" {
		return SessionUIRequestSnapshot{}, session.State{}, true, fmt.Errorf("ui request id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return SessionUIRequestSnapshot{}, session.State{}, false, nil
	}
	if record.uiRequest == nil {
		return SessionUIRequestSnapshot{}, session.State{}, true, errNoPendingUIRequest
	}
	if record.uiRequest.RequestID != id {
		return SessionUIRequestSnapshot{}, session.State{}, true, errUnexpectedUIRequest
	}
	resolved := *record.uiRequest
	now := r.now().UTC()
	record.updatedAt = now
	record.activityAt = now
	record.uiRequest = nil
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return SessionUIRequestSnapshot{}, session.State{}, true, err
	}
	r.sessions[sessionID] = cp
	return resolved, copySessionState(cp.state), true, nil
}

func (r *sessionRegistry) SetResumeCursor(sessionID session.SessionID, kind session.StreamKind, cursor string) error {
	if err := sessionID.Validate(); err != nil {
		return err
	}
	if err := kind.Validate(); err != nil {
		return err
	}
	value := strings.TrimSpace(cursor)
	if value == "" {
		return fmt.Errorf("resume cursor is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}
	switch kind {
	case session.StreamKindMain:
		record.resumeCursors.Session = value
	case session.StreamKindUI:
		record.resumeCursors.UI = value
	case session.StreamKindTransport:
		record.resumeCursors.Transport = value
	default:
		return fmt.Errorf("stream kind %q is not supported", kind)
	}
	r.sessions[sessionID] = copySessionRecord(record)
	return nil
}

func normalizeSessionUIRequest(raw SessionUIRequestSnapshot) (SessionUIRequestSnapshot, error) {
	request := SessionUIRequestSnapshot{
		RequestID:     strings.TrimSpace(raw.RequestID),
		Kind:          strings.TrimSpace(raw.Kind),
		Method:        strings.TrimSpace(raw.Method),
		Title:         strings.TrimSpace(raw.Title),
		Message:       strings.TrimSpace(raw.Message),
		Prompt:        strings.TrimSpace(raw.Prompt),
		Question:      strings.TrimSpace(raw.Question),
		Context:       strings.TrimSpace(raw.Context),
		AllowFreeform: raw.AllowFreeform,
		AllowMultiple: raw.AllowMultiple,
		Options:       copySessionUIOptions(raw.Options),
		Questions:     copySessionUIQuestions(raw.Questions),
		Metadata:      copyAnyMap(raw.Metadata),
	}
	if request.RequestID == "" {
		return SessionUIRequestSnapshot{}, fmt.Errorf("ui request id is required")
	}
	if request.Kind == "" {
		return SessionUIRequestSnapshot{}, fmt.Errorf("ui request kind is required")
	}
	if request.Prompt == "" {
		return SessionUIRequestSnapshot{}, fmt.Errorf("ui request prompt is required")
	}
	return request, nil
}

func syncSessionRecordState(record *sessionRecord, busy bool) error {
	return syncSessionRecordStateWithQueue(record, busy, record.state.Queue())
}

func syncSessionRecordStateWithQueue(record *sessionRecord, busy bool, queue session.QueueSnapshot) error {
	state, err := session.NewState(record.identity, busy, queue, record.transcript.Tail())
	if err != nil {
		return err
	}
	record.state = state
	return nil
}

func (r *sessionRegistry) UpdateWorkspace(sessionID session.SessionID, workspaceState workspaceBrowserState) (workspaceBrowserState, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[sessionID]
	if !ok {
		return workspaceBrowserState{}, false, nil
	}
	now := r.now().UTC()
	record.updatedAt = now
	record.workspace = copyWorkspaceBrowserState(workspaceState)
	cp := copySessionRecord(record)
	if err := r.persistLocked(cp); err != nil {
		return workspaceBrowserState{}, true, err
	}
	r.sessions[sessionID] = cp
	return copyWorkspaceBrowserState(cp.workspace), true, nil
}

func copySessionRecord(record sessionRecord) sessionRecord {
	return sessionRecord{
		identity:                 record.identity,
		title:                    record.title,
		alias:                    record.alias,
		cwd:                      record.cwd,
		provider:                 record.provider,
		model:                    record.model,
		reasoningEffort:          record.reasoningEffort,
		focused:                  record.focused,
		hidden:                   record.hidden,
		priorityOffset:           record.priorityOffset,
		snoozeUntil:              copyTimePtr(record.snoozeUntil),
		dependencySessionID:      copySessionIDPtr(record.dependencySessionID),
		createdAt:                record.createdAt,
		updatedAt:                record.updatedAt,
		activityAt:               record.activityAt,
		archivedAt:               copyTimePtr(record.archivedAt),
		state:                    copySessionState(record.state),
		workspace:                copyWorkspaceBrowserState(record.workspace),
		transcript:               record.transcript.Clone(),
		importedSourcePath:       record.importedSourcePath,
		importedFirstUserMessage: record.importedFirstUserMessage,
		runtime:                  record.runtime,
		uiRequest:                copySessionUIRequest(record.uiRequest),
		resumeCursors:            record.resumeCursors,
		inputMu:                  record.inputMu,
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

func copySessionUIRequest(raw *SessionUIRequestSnapshot) *SessionUIRequestSnapshot {
	if raw == nil {
		return nil
	}
	copied := *raw
	copied.Options = copySessionUIOptions(raw.Options)
	copied.Questions = copySessionUIQuestions(raw.Questions)
	copied.Metadata = copyAnyMap(raw.Metadata)
	return &copied
}

func copySessionUIOptions(raw []SessionUIOptionSnapshot) []SessionUIOptionSnapshot {
	if len(raw) == 0 {
		return nil
	}
	return append([]SessionUIOptionSnapshot(nil), raw...)
}

func copySessionUIQuestions(raw []SessionUIQuestionSnapshot) []SessionUIQuestionSnapshot {
	if len(raw) == 0 {
		return nil
	}
	copied := make([]SessionUIQuestionSnapshot, 0, len(raw))
	for _, question := range raw {
		copied = append(copied, SessionUIQuestionSnapshot{
			Header:      question.Header,
			Question:    question.Question,
			Options:     copySessionUIOptions(question.Options),
			MultiSelect: question.MultiSelect,
		})
	}
	return copied
}

func copyAnyMap(raw map[string]any) map[string]any {
	if raw == nil {
		return nil
	}
	copied := make(map[string]any, len(raw))
	for key, value := range raw {
		copied[key] = value
	}
	return copied
}

func copyTimePtr(raw *time.Time) *time.Time {
	if raw == nil {
		return nil
	}
	copied := raw.UTC()
	return &copied
}

func copySessionIDPtr(raw *session.SessionID) *session.SessionID {
	if raw == nil {
		return nil
	}
	copied := *raw
	return &copied
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
	return filepath.Clean(cleaned)
}
