package app

import (
	"context"
	"fmt"

	"actrail/internal/domain/session"
)

type SessionDetailsRequest struct {
	SessionID session.SessionID
}

type SessionDetailsResponse struct {
	SessionID      string                    `json:"session_id"`
	RuntimeID      string                    `json:"runtime_id,omitempty"`
	ThreadID       string                    `json:"thread_id,omitempty"`
	Title          string                    `json:"title"`
	CWD            string                    `json:"cwd"`
	AgentBackend   string                    `json:"agent_backend"`
	Provider       string                    `json:"provider,omitempty"`
	Model          string                    `json:"model,omitempty"`
	Busy           bool                      `json:"busy"`
	QueueLength    int                       `json:"queue_length"`
	LastUpdatedTS  float64                   `json:"last_updated_ts"`
	LastActivityTS float64                   `json:"last_activity_ts"`
	Historical     bool                      `json:"historical"`
	Capabilities   SessionCapabilitySnapshot `json:"capabilities"`
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
	Seq  uint64  `json:"seq"`
	Role string  `json:"role"`
	Kind string  `json:"kind"`
	Text string  `json:"text"`
	TS   float64 `json:"ts"`
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
	UIRequest            *SessionUIRequestSnapshot     `json:"ui_request,omitempty"`
	PartialAssistantTurn *PartialAssistantTurnSnapshot `json:"partial_assistant_turn,omitempty"`
	TailSeq              uint64                        `json:"tail_seq"`
	ResumeCursors        SessionResumeCursors          `json:"resume_cursors"`
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
	RequestID string `json:"request_id"`
	Kind      string `json:"kind"`
	Prompt    string `json:"prompt"`
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
	Path  string           `json:"path"`
	Items []GitFileVersion `json:"items"`
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
		SessionID:      record.identity.SessionID().String(),
		RuntimeID:      runtimeID.String(),
		ThreadID:       threadID.String(),
		Title:          record.title,
		CWD:            record.cwd,
		AgentBackend:   record.identity.Backend().String(),
		Provider:       record.provider,
		Model:          record.model,
		Busy:           record.state.Busy(),
		QueueLength:    record.state.Queue().Len(),
		LastUpdatedTS:  timestampSeconds(record.updatedAt),
		LastActivityTS: timestampSeconds(record.activityAt),
		Historical:     record.identity.Historical(),
		Capabilities:   s.capabilitiesSnapshot(),
	}, nil
}

func (s *Stub) SessionMessages(_ context.Context, req SessionMessagesRequest) (SessionMessagesResponse, error) {
	if _, err := s.lookupSession(req.SessionID); err != nil {
		return SessionMessagesResponse{}, err
	}
	return SessionMessagesResponse{}, Unsupported("session message snapshot not implemented")
}

func (s *Stub) SessionState(_ context.Context, req SessionStateRequest) (SessionStateResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionStateResponse{}, err
	}
	return SessionStateResponse{
		Busy:                 record.state.Busy(),
		Queue:                queueSnapshotFromState(record.state),
		UIRequest:            nil,
		PartialAssistantTurn: partialAssistantTurn(record.state),
		TailSeq:              record.state.Tail().Seq().Uint64(),
		ResumeCursors:        SessionResumeCursors{},
	}, nil
}

func (s *Stub) GitFileVersions(_ context.Context, req GitFileVersionsRequest) (GitFileVersionsResponse, error) {
	if _, err := s.lookupSession(req.SessionID); err != nil {
		return GitFileVersionsResponse{}, err
	}
	return GitFileVersionsResponse{}, Unsupported("git file versions not implemented")
}

func (s *Stub) lookupSession(sessionID session.SessionID) (sessionRecord, error) {
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return sessionRecord{}, NotFound(fmt.Sprintf("session %q not found", sessionID))
	}
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

func partialAssistantTurn(state session.State) *PartialAssistantTurnSnapshot {
	if !state.Tail().Live() {
		return nil
	}
	turnID, ok := state.Tail().TurnID()
	if !ok {
		return nil
	}
	return &PartialAssistantTurnSnapshot{TurnID: turnID.String()}
}
