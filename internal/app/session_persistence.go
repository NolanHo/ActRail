package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/message"
	"actrail/internal/domain/session"
)

type sessionStore interface {
	UpsertSessionSnapshot(context.Context, sqlitestore.SessionSnapshotRow) error
	UpsertSessionSnapshotWithCodexCommand(context.Context, sqlitestore.SessionSnapshotRow, sqlitestore.CodexSessionCommandRow) error
	UpsertSessionSourceRef(context.Context, sqlitestore.SessionSourceRefRow) error
	UpsertCodexRuntimeClaim(context.Context, sqlitestore.CodexRuntimeClaimRow) error
	DeleteCodexRuntimeClaim(context.Context, string) error
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

func durableLiveStateFromRecord(record sessionRecord) sqlitestore.LiveStateRow {
	tail := record.state.Tail()
	turnID := ""
	if id, ok := tail.TurnID(); ok {
		turnID = id.String()
	}
	partialTurnID := ""
	partialText := ""
	if partial, ok := record.transcript.PartialAssistantTurn(); ok {
		partialTurnID = partial.TurnID().String()
		partialText = partial.Text()
	}
	return sqlitestore.LiveStateRow{
		Busy:                   record.state.Busy(),
		TailSeq:                tail.Seq().Uint64(),
		TailOwner:              string(tail.Owner()),
		TailTurnID:             turnID,
		PartialTurnID:          partialTurnID,
		PartialText:            partialText,
		UIRequestJSON:          marshalLiveJSON(record.uiRequest),
		TransportGenerationID:  strings.TrimSpace(record.transport.GenerationID),
		TransportState:         strings.TrimSpace(record.transport.State.String()),
		TransportResetRequired: record.transport.ResetRequired,
		TransportReason:        strings.TrimSpace(record.transport.Reason),
		ResumeSessionCursor:    strings.TrimSpace(record.resumeCursors.Session),
		ResumeUICursor:         strings.TrimSpace(record.resumeCursors.UI),
		ResumeTransportCursor:  strings.TrimSpace(record.resumeCursors.Transport),
		ContextUsageJSON:       marshalLiveJSON(record.contextUsage),
		TurnTimingJSON:         marshalLiveJSON(record.turnTiming),
		RuntimeAgentRunning:    record.runtimeAgentRunning,
		UpdatedAt:              record.updatedAt.UTC(),
	}
}

func marshalLiveJSON(value any) string {
	if value == nil {
		return ""
	}
	body, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	if string(body) == "null" {
		return ""
	}
	return string(body)
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
		Live: durableLiveStateFromRecord(record),
	}
}

func durableCodexSendCommandFromRecord(record sessionRecord, item message.CommittedMessage, runtimeID session.RuntimeID) sqlitestore.CodexSessionCommandRow {
	now := record.updatedAt.UTC()
	messageID := ""
	if item.Seq().Uint64() != 0 {
		messageID = fmt.Sprintf("seq:%d", item.Seq().Uint64())
	}
	return sqlitestore.CodexSessionCommandRow{
		CommandID: fmt.Sprintf("%s:%s", record.identity.SessionID(), messageID),
		SessionID: record.identity.SessionID().String(),
		RuntimeID: runtimeID.String(),
		Kind:      "send",
		Text:      item.Text(),
		MessageID: messageID,
		State:     codexCommandPending.String(),
		CreatedAt: now,
		UpdatedAt: now,
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
	if strings.TrimSpace(snapshot.Live.PartialTurnID) != "" && strings.TrimSpace(snapshot.Live.PartialText) != "" {
		if _, err := transcript.AppendAssistantDelta(snapshot.Live.PartialTurnID, snapshot.Live.PartialText); err != nil {
			return sessionRecord{}, err
		}
	}
	tail, err := tailSnapshotFromDurableLive(snapshot.Live, transcript)
	if err != nil {
		return sessionRecord{}, err
	}
	busy := snapshot.Live.Busy
	if identity.Historical() {
		busy = false
	}
	state, err := session.NewState(identity, busy, queue, tail)
	if err != nil {
		return sessionRecord{}, err
	}
	uiRequest, err := uiRequestFromLiveJSON(snapshot.Live.UIRequestJSON)
	if err != nil {
		return sessionRecord{}, err
	}
	contextUsage, err := contextUsageFromLiveJSON(snapshot.Live.ContextUsageJSON)
	if err != nil {
		return sessionRecord{}, err
	}
	turnTiming, err := turnTimingFromLiveJSON(snapshot.Live.TurnTimingJSON)
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
		uiRequest:           uiRequest,
		transport: SessionTransportSnapshot{
			GenerationID:  strings.TrimSpace(snapshot.Live.TransportGenerationID),
			State:         SessionTransportState(strings.TrimSpace(snapshot.Live.TransportState)),
			ResetRequired: snapshot.Live.TransportResetRequired,
			Reason:        strings.TrimSpace(snapshot.Live.TransportReason),
		},
		resumeCursors: SessionResumeCursors{
			Session:   strings.TrimSpace(snapshot.Live.ResumeSessionCursor),
			UI:        strings.TrimSpace(snapshot.Live.ResumeUICursor),
			Transport: strings.TrimSpace(snapshot.Live.ResumeTransportCursor),
		},
		contextUsage:        contextUsage,
		turnTiming:          turnTiming,
		runtimeAgentRunning: snapshot.Live.RuntimeAgentRunning,
		inputMu:             &sync.Mutex{},
	}, nil
}

func tailSnapshotFromDurableLive(live sqlitestore.LiveStateRow, transcript message.Transcript) (message.TailSnapshot, error) {
	ownerRaw := strings.TrimSpace(live.TailOwner)
	if ownerRaw == "" {
		return transcript.Tail(), nil
	}
	owner, err := message.ParseTailOwner(ownerRaw)
	if err != nil {
		return message.TailSnapshot{}, err
	}
	seq := message.Seq(live.TailSeq)
	if owner == message.TailOwnerTranscript {
		return message.NewCommittedTail(seq), nil
	}
	turnID, err := message.NewTurnID(live.TailTurnID)
	if err != nil {
		return message.TailSnapshot{}, err
	}
	return message.NewTailSnapshot(seq, owner, &turnID)
}

func uiRequestFromLiveJSON(raw string) (*SessionUIRequestSnapshot, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var snapshot SessionUIRequestSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func contextUsageFromLiveJSON(raw string) (*SessionContextUsageSnapshot, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var snapshot SessionContextUsageSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func turnTimingFromLiveJSON(raw string) (*SessionTurnTimingSnapshot, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var snapshot SessionTurnTimingSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
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
	// Cold-start rehydrate must include imported historical rows; active list filtering
	// still happens at the service layer.
	snapshots, err := store.ListSessionSnapshots(context.Background(), true)
	if err != nil {
		return nil, fmt.Errorf("load persisted sessions: %w", err)
	}
	records := make([]sessionRecord, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.Session.ArchivedAt != nil {
			continue
		}
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
	if err := r.store.UpsertSessionSnapshot(context.Background(), durableSessionSnapshotFromRecord(record)); err != nil {
		return err
	}
	return r.persistCodexSourceAndClaimLocked(record)
}

func (r *sessionRegistry) persistCodexSourceAndClaimLocked(record sessionRecord) error {
	if r == nil || r.store == nil {
		return nil
	}
	if strings.TrimSpace(record.importedSourcePath) != "" || strings.TrimSpace(record.importedBackendSessionID) != "" {
		if err := r.store.UpsertSessionSourceRef(context.Background(), sqlitestore.SessionSourceRefRow{
			SessionID:        record.identity.SessionID().String(),
			Backend:          record.identity.Backend().String(),
			BackendSessionID: strings.TrimSpace(record.importedBackendSessionID),
			SourcePath:       strings.TrimSpace(record.importedSourcePath),
			SourceConfidence: strings.TrimSpace(record.importedSourceConfidence),
			FirstUserMessage: strings.TrimSpace(record.importedFirstUserMessage),
		}); err != nil {
			return err
		}
	}
	if record.identity.Backend() != session.BackendCodex {
		return nil
	}
	backendSessionID := strings.TrimSpace(record.importedBackendSessionID)
	sourcePath := strings.TrimSpace(record.importedSourcePath)
	if record.archivedAt != nil || (backendSessionID == "" && sourcePath == "") {
		return r.store.DeleteCodexRuntimeClaim(context.Background(), record.identity.SessionID().String())
	}
	return r.store.UpsertCodexRuntimeClaim(context.Background(), sqlitestore.CodexRuntimeClaimRow{
		SessionID:         record.identity.SessionID().String(),
		BackendSessionID:  backendSessionID,
		SourcePath:        sourcePath,
		RuntimeInstanceID: codexRuntimeInstanceID(record),
		HelperPID:         codexRuntimeHelperPID(record.runtime),
		ChildPID:          codexRuntimeChildPID(record.runtime),
		ControlSocketPath: codexRuntimeControlSocketPath(record.runtime),
		ChildSocketPath:   codexRuntimeChildSocketPath(record.runtime),
		State:             codexRuntimeClaimStateFromRecord(record),
		UpdatedAt:         record.updatedAt.UTC(),
	})
}

func codexRuntimeInstanceID(record sessionRecord) string {
	runtimeID, _ := record.identity.RuntimeID()
	if runtimeID.String() != "" {
		return runtimeID.String()
	}
	return ""
}

func codexRuntimeChildPID(runtime sessionRuntime) int {
	if runtime.helper == nil || runtime.helper.childPID == nil {
		return 0
	}
	return *runtime.helper.childPID
}

func codexRuntimeHelperPID(runtime sessionRuntime) int {
	if runtime.helper == nil {
		return 0
	}
	return runtime.helper.helperPID
}

func codexRuntimeControlSocketPath(runtime sessionRuntime) string {
	if runtime.helper == nil {
		return ""
	}
	return strings.TrimSpace(runtime.helper.manifest.ControlSocketPath)
}

func codexRuntimeChildSocketPath(runtime sessionRuntime) string {
	if runtime.helper == nil || strings.TrimSpace(runtime.helper.runtimeDir) == "" {
		return ""
	}
	return strings.TrimSpace(filepath.Join(runtime.helper.runtimeDir, "child.sock"))
}

func codexRuntimeClaimStateFromRecord(record sessionRecord) string {
	snapshot := sessionTransportSnapshot(record)
	switch snapshot.State {
	case SessionTransportStateAttached:
		return "attached"
	case SessionTransportStateStarting:
		return "unknown"
	case SessionTransportStateBroken, SessionTransportStateFailed, SessionTransportStateEnded, SessionTransportStateSilent, SessionTransportStateStalled:
		return "unavailable"
	default:
		return "unknown"
	}
}

func (r *sessionRegistry) PersistAll() error {
	if r == nil || r.store == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, sessionID := range r.order {
		record, ok := r.sessions[sessionID]
		if !ok {
			continue
		}
		if err := r.persistLocked(copySessionRecord(record)); err != nil {
			return err
		}
	}
	return nil
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
