package app

import (
	"context"
	"fmt"

	"actrail/internal/domain/message"
	"actrail/internal/domain/session"
)

type SessionDetailsRequest struct {
	SessionID session.SessionID
}

type SessionDetailsResponse struct {
	SessionID           string                    `json:"session_id"`
	RuntimeID           string                    `json:"runtime_id,omitempty"`
	ThreadID            string                    `json:"thread_id,omitempty"`
	Title               string                    `json:"title"`
	Alias               string                    `json:"alias,omitempty"`
	DisplayName         string                    `json:"display_name,omitempty"`
	FirstUserMessage    string                    `json:"first_user_message,omitempty"`
	CWD                 string                    `json:"cwd"`
	AgentBackend        string                    `json:"agent_backend"`
	Provider            string                    `json:"provider,omitempty"`
	Model               string                    `json:"model,omitempty"`
	Busy                bool                      `json:"busy"`
	Focused             bool                      `json:"focused,omitempty"`
	QueueLength         int                       `json:"queue_length"`
	PriorityOffset      float64                   `json:"priority_offset,omitempty"`
	SnoozeUntil         *int64                    `json:"snooze_until,omitempty"`
	DependencySessionID string                    `json:"dependency_session_id,omitempty"`
	LastUpdatedTS       float64                   `json:"last_updated_ts"`
	LastActivityTS      float64                   `json:"last_activity_ts"`
	Historical          bool                      `json:"historical"`
	Capabilities        SessionCapabilitySnapshot `json:"capabilities"`
}

type SessionCapabilitySnapshot struct {
	WSRealtime     bool `json:"ws_realtime"`
	Voice          bool `json:"voice"`
	Harness        bool `json:"harness"`
	Notifications  bool `json:"notifications"`
	PIUI           bool `json:"pi_ui"`
	WorkspaceRead  bool `json:"workspace_read"`
	WorkspaceWrite bool `json:"workspace_write"`
}

type SessionMessagesRequest struct {
	SessionID session.SessionID
	BeforeSeq *uint64
	Limit     int
	Init      bool
}

type SessionMessage struct {
	Seq           uint64         `json:"seq"`
	Role          string         `json:"role,omitempty"`
	Kind          string         `json:"kind"`
	Type          string         `json:"type,omitempty"`
	Text          string         `json:"text"`
	TS            float64        `json:"ts"`
	EventID       string         `json:"event_id,omitempty"`
	ParentEventID string         `json:"parent_event_id,omitempty"`
	SourceOrder   string         `json:"source_order,omitempty"`
	Name          string         `json:"name,omitempty"`
	Summary       string         `json:"summary,omitempty"`
	ToolCallID    string         `json:"tool_call_id,omitempty"`
	IsError       bool           `json:"is_error,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
}

type SessionMessagesResponse struct {
	Items         []SessionMessage `json:"items"`
	NextBeforeSeq *uint64          `json:"next_before_seq,omitempty"`
	HasMore       bool             `json:"has_more"`
	TailSeq       uint64           `json:"tail_seq"`
}

type SessionStateRequest struct {
	SessionID session.SessionID
}

type SessionStateResponse struct {
	Busy                 bool                          `json:"busy"`
	Queue                SessionQueueSnapshot          `json:"queue"`
	Transport            SessionTransportSnapshot      `json:"transport"`
	UIRequest            *SessionUIRequestSnapshot     `json:"ui_request,omitempty"`
	PartialAssistantTurn *PartialAssistantTurnSnapshot `json:"partial_assistant_turn,omitempty"`
	TailSeq              uint64                        `json:"tail_seq"`
	ResumeCursors        SessionResumeCursors          `json:"resume_cursors"`
	ContextUsage         *SessionContextUsageSnapshot  `json:"context_usage,omitempty"`
	TurnTiming           *SessionTurnTimingSnapshot    `json:"turn_timing,omitempty"`
	ActiveWait           *ActiveWaitSummary            `json:"active_wait,omitempty"`
}

type SessionContextUsageSnapshot struct {
	UsedTokens  *int `json:"used_tokens,omitempty"`
	TotalTokens *int `json:"total_tokens,omitempty"`
	PercentUsed *int `json:"percent_used,omitempty"`
}

type SessionTurnTimingSnapshot struct {
	StartedTS   float64  `json:"started_ts,omitempty"`
	LastEventTS *float64 `json:"last_event_ts,omitempty"`
}

type SessionQueueSnapshot struct {
	Items []QueuedPromptSnapshot `json:"items"`
}

type QueuedPromptSnapshot struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	State string `json:"state"`
}

type SessionUIRequestSnapshot struct {
	RequestID     string                      `json:"request_id"`
	Kind          string                      `json:"kind"`
	Method        string                      `json:"method,omitempty"`
	Title         string                      `json:"title,omitempty"`
	Message       string                      `json:"message,omitempty"`
	Prompt        string                      `json:"prompt"`
	Question      string                      `json:"question,omitempty"`
	Context       string                      `json:"context,omitempty"`
	AllowFreeform bool                        `json:"allow_freeform,omitempty"`
	AllowMultiple bool                        `json:"allow_multiple,omitempty"`
	Options       []SessionUIOptionSnapshot   `json:"options,omitempty"`
	Questions     []SessionUIQuestionSnapshot `json:"questions,omitempty"`
	Metadata      map[string]any              `json:"metadata,omitempty"`
}

type SessionUIOptionSnapshot struct {
	Label       string `json:"label,omitempty"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
}

type SessionUIQuestionSnapshot struct {
	Header      string                    `json:"header,omitempty"`
	Question    string                    `json:"question"`
	Options     []SessionUIOptionSnapshot `json:"options,omitempty"`
	MultiSelect bool                      `json:"multiSelect,omitempty"`
}

type PartialAssistantTurnSnapshot struct {
	TurnID string `json:"turn_id"`
	Text   string `json:"text"`
}

type SessionResumeCursors struct {
	Session   string `json:"session,omitempty"`
	UI        string `json:"ui,omitempty"`
	Transport string `json:"transport,omitempty"`
}

type SessionWorkspaceRequest struct {
	SessionID session.SessionID
}

type UpdateSessionWorkspaceRequest struct {
	SessionID    session.SessionID
	SelectedPath string                 `json:"selected_path,omitempty"`
	OpenPaths    []string               `json:"open_paths"`
	HistoryItems []WorkspaceHistoryItem `json:"history_items"`
}

type SessionWorkspaceResponse struct {
	RootPath     string                 `json:"root_path"`
	SelectedPath string                 `json:"selected_path,omitempty"`
	OpenPaths    []string               `json:"open_paths"`
	HistoryItems []WorkspaceHistoryItem `json:"history_items"`
}

type WorkspaceHistoryItem struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

type WorkspaceFileListRequest struct {
	SessionID session.SessionID
	Path      string
	Search    string
	Limit     int
}

type WorkspaceFileListResponse struct {
	RootPath  string               `json:"root_path"`
	Path      string               `json:"path"`
	Items     []WorkspaceFileEntry `json:"items"`
	Truncated bool                 `json:"truncated"`
}

type WorkspaceFileEntry struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type WorkspaceFileReadRequest struct {
	SessionID session.SessionID
	Path      string
}

type WorkspaceFileReadResponse struct {
	Path              string `json:"path"`
	Kind              string `json:"kind"`
	MIMEType          string `json:"mime_type,omitempty"`
	Encoding          string `json:"encoding,omitempty"`
	SizeBytes         int64  `json:"size_bytes,omitempty"`
	Text              string `json:"text,omitempty"`
	DownloadName      string `json:"download_name,omitempty"`
	UnsupportedReason string `json:"unsupported_reason,omitempty"`
}

type GitFileVersionsRequest struct {
	SessionID session.SessionID
	Path      string
}

type GitFileVersionsResponse struct {
	Path           string           `json:"path"`
	FallbackReason string           `json:"fallback_reason,omitempty"`
	Items          []GitFileVersion `json:"items"`
}

type GitFileVersion struct {
	VersionID  string  `json:"version_id"`
	Label      string  `json:"label"`
	CommitHash string  `json:"commit_hash,omitempty"`
	Author     string  `json:"author,omitempty"`
	CommitTS   float64 `json:"commit_ts,omitempty"`
	Message    string  `json:"message,omitempty"`
	Current    bool    `json:"current"`
}

func (s *Stub) SessionDetails(_ context.Context, req SessionDetailsRequest) (SessionDetailsResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionDetailsResponse{}, err
	}
	runtimeID, _ := record.identity.RuntimeID()
	threadID, _ := record.identity.ThreadID()
	return SessionDetailsResponse{
		SessionID:           record.identity.SessionID().String(),
		RuntimeID:           runtimeID.String(),
		ThreadID:            threadID.String(),
		Title:               record.title,
		Alias:               displayAlias(record),
		DisplayName:         sessionDisplayName(record),
		FirstUserMessage:    firstUserMessageForRecord(record),
		CWD:                 record.cwd,
		AgentBackend:        record.identity.Backend().String(),
		Provider:            record.provider,
		Model:               record.model,
		Busy:                record.state.Busy(),
		Focused:             record.focused,
		QueueLength:         record.state.Queue().Len(),
		PriorityOffset:      record.priorityOffset,
		SnoozeUntil:         unixSecondsPtr(record.snoozeUntil),
		DependencySessionID: sessionIDString(record.dependencySessionID),
		LastUpdatedTS:       timestampSeconds(sessionDisplayUpdatedAt(record)),
		LastActivityTS:      timestampSeconds(record.activityAt),
		Historical:          record.identity.Historical(),
		Capabilities:        s.capabilitiesSnapshot(),
	}, nil
}

func (s *Stub) SessionMessages(ctx context.Context, req SessionMessagesRequest) (SessionMessagesResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionMessagesResponse{}, err
	}
	if response, ok, err := s.loadPIAuthoritativeHistory(ctx, record, s.cfg.Storage.DataDir, req); ok {
		return response, err
	}
	if response, ok, err := s.loadDetachedImportedPIHistory(ctx, record, req); ok {
		return response, err
	}
	page := record.transcript.History(messageBeforeSeq(req.BeforeSeq), req.Limit)
	items := page.Items()
	response := SessionMessagesResponse{
		Items:   make([]SessionMessage, 0, len(items)),
		HasMore: page.HasMore(),
		TailSeq: record.transcript.TailSeq().Uint64(),
	}
	for _, item := range items {
		response.Items = append(response.Items, sessionMessageFromCommitted(item))
	}
	if nextBefore, ok := page.NextBefore(); ok {
		value := nextBefore.Uint64()
		response.NextBeforeSeq = &value
	}
	return response, nil
}

func (s *Stub) SessionState(_ context.Context, req SessionStateRequest) (SessionStateResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionStateResponse{}, err
	}
	contextUsage := copyContextUsage(record.contextUsage)
	turnTiming := copyTurnTiming(record.turnTiming)
	return SessionStateResponse{
		Busy:                 record.state.Busy(),
		Queue:                queueSnapshotFromState(record.state),
		Transport:            s.sessionTransportSnapshot(record),
		UIRequest:            copySessionUIRequest(record.uiRequest),
		PartialAssistantTurn: partialAssistantTurn(record.transcript),
		TailSeq:              record.transcript.TailSeq().Uint64(),
		ResumeCursors:        record.resumeCursors,
		ContextUsage:         contextUsage,
		TurnTiming:           turnTiming,
		ActiveWait:           s.activeWaitForSession(record.identity.SessionID()),
	}, nil
}

func (s *Stub) lookupSession(sessionID session.SessionID) (sessionRecord, error) {
	record, ok := s.registry.LookupRoute(sessionID)
	if !ok {
		return sessionRecord{}, NotFound(fmt.Sprintf("session %q not found", sessionID))
	}
	record.runtime = s.runtimeForSession(record.identity.SessionID(), record.identity.Backend(), record.runtime)
	return record, nil
}

func queueSnapshotFromState(state session.State) SessionQueueSnapshot {
	items := state.Queue().Items()
	snapshot := SessionQueueSnapshot{Items: make([]QueuedPromptSnapshot, 0, len(items))}
	for _, item := range items {
		snapshot.Items = append(snapshot.Items, QueuedPromptSnapshot{
			ID:    item.ID().String(),
			Text:  item.Text(),
			State: item.State().String(),
		})
	}
	return snapshot
}

func partialAssistantTurn(transcript message.Transcript) *PartialAssistantTurnSnapshot {
	partial, ok := transcript.PartialAssistantTurn()
	if !ok {
		return nil
	}
	return &PartialAssistantTurnSnapshot{TurnID: partial.TurnID().String(), Text: partial.Text()}
}

func messageBeforeSeq(raw *uint64) *message.Seq {
	if raw == nil {
		return nil
	}
	seq := message.Seq(*raw)
	return &seq
}
