package app

import (
	"context"

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

func (s *Stub) SessionDetails(_ context.Context, _ SessionDetailsRequest) (SessionDetailsResponse, error) {
	return SessionDetailsResponse{}, Unsupported("session details not implemented")
}

func (s *Stub) SessionMessages(_ context.Context, _ SessionMessagesRequest) (SessionMessagesResponse, error) {
	return SessionMessagesResponse{}, Unsupported("session message snapshot not implemented")
}

func (s *Stub) SessionState(_ context.Context, _ SessionStateRequest) (SessionStateResponse, error) {
	return SessionStateResponse{}, Unsupported("session state snapshot not implemented")
}

func (s *Stub) SessionWorkspace(_ context.Context, _ SessionWorkspaceRequest) (SessionWorkspaceResponse, error) {
	return SessionWorkspaceResponse{}, Unsupported("session workspace snapshot not implemented")
}

func (s *Stub) WorkspaceFileList(_ context.Context, _ WorkspaceFileListRequest) (WorkspaceFileListResponse, error) {
	return WorkspaceFileListResponse{}, Unsupported("workspace file listing not implemented")
}

func (s *Stub) WorkspaceFileRead(_ context.Context, _ WorkspaceFileReadRequest) (WorkspaceFileReadResponse, error) {
	return WorkspaceFileReadResponse{}, Unsupported("workspace file read not implemented")
}

func (s *Stub) GitFileVersions(_ context.Context, _ GitFileVersionsRequest) (GitFileVersionsResponse, error) {
	return GitFileVersionsResponse{}, Unsupported("git file versions not implemented")
}
