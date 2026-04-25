package app

import (
	"context"

	"actrail/internal/config"
)

type Service interface {
	Bootstrap(context.Context) BootstrapSnapshot
	ListSessions(context.Context, ListSessionsRequest) (ListSessionsResponse, error)
	CreateSession(context.Context, CreateSessionRequest) (CreateSessionResponse, error)
}

type Stub struct {
	cfg config.Config
}

func NewStub(cfg config.Config) *Stub {
	return &Stub{cfg: cfg}
}

type BootstrapSnapshot struct {
	ProtocolVersion int          `json:"protocol_version"`
	Capabilities    Capabilities `json:"capabilities"`
	WS              WSConfig     `json:"ws"`
	LaunchDefaults  LaunchConfig `json:"launch_defaults"`
	UI              UIConfig     `json:"ui"`
}

type Capabilities struct {
	WSRealtime     bool `json:"ws_realtime"`
	Voice          bool `json:"voice"`
	Harness        bool `json:"harness"`
	Notifications  bool `json:"notifications"`
	PIUI           bool `json:"pi_ui"`
	WorkspaceRead  bool `json:"workspace_read"`
	WorkspaceWrite bool `json:"workspace_write"`
}

type WSConfig struct {
	URL                 string `json:"url"`
	HeartbeatIntervalMS int    `json:"heartbeat_interval_ms"`
	ResumeBufferEvents  int    `json:"resume_buffer_events"`
}

type LaunchConfig struct {
	DefaultBackend    string   `json:"default_backend"`
	AvailableBackends []string `json:"available_backends"`
	Providers         []string `json:"providers"`
	Models            []string `json:"models"`
}

type UIConfig struct {
	DeferredFeatures []string `json:"deferred_features"`
}

type SessionSummary struct {
	SessionID     string  `json:"session_id"`
	RuntimeID     string  `json:"runtime_id,omitempty"`
	ThreadID      string  `json:"thread_id,omitempty"`
	AgentBackend  string  `json:"agent_backend"`
	Title         string  `json:"title"`
	CWD           string  `json:"cwd"`
	Busy          bool    `json:"busy"`
	LastUpdatedTS float64 `json:"last_updated_ts"`
	Historical    bool    `json:"historical"`
}

type ListSessionsRequest struct {
	GroupKey    string
	Offset      int
	Limit       int
	GroupOffset int
	GroupLimit  int
}

type ListSessionsResponse struct {
	Items          []SessionSummary `json:"items"`
	RemainingCount int              `json:"remaining_count"`
	GroupKey       *string          `json:"group_key"`
}

type CreateSessionRequest struct {
	AgentBackend    string  `json:"agent_backend"`
	CWD             string  `json:"cwd"`
	Provider        *string `json:"provider"`
	Model           *string `json:"model"`
	ReasoningEffort *string `json:"reasoning_effort"`
	ResumeSessionID *string `json:"resume_session_id"`
	Title           *string `json:"title"`
}

type CreateSessionResponse struct {
	OK bool `json:"ok"`
}

type Error struct {
	Code    string
	Message string
	Field   string
}

func (e *Error) Error() string {
	return e.Message
}

func Unsupported(message string) *Error {
	return &Error{Code: "unsupported", Message: message}
}

func (s *Stub) Bootstrap(_ context.Context) BootstrapSnapshot {
	return BootstrapSnapshot{
		ProtocolVersion: s.cfg.Protocol.Version,
		Capabilities: Capabilities{
			WSRealtime:     s.cfg.Features.WebSocketRealtime,
			Voice:          s.cfg.Features.Voice,
			Harness:        s.cfg.Features.Harness,
			Notifications:  s.cfg.Features.Notifications,
			PIUI:           s.cfg.Features.PIUI,
			WorkspaceRead:  s.cfg.Features.WorkspaceRead,
			WorkspaceWrite: s.cfg.Features.WorkspaceWrite,
		},
		WS: WSConfig{
			URL:                 s.cfg.Protocol.WebSocketPath,
			HeartbeatIntervalMS: s.cfg.HeartbeatIntervalMillis(),
			ResumeBufferEvents:  s.cfg.Protocol.ResumeBuffer,
		},
		LaunchDefaults: LaunchConfig{
			DefaultBackend:    s.cfg.Launch.DefaultBackend,
			AvailableBackends: append([]string(nil), s.cfg.Launch.AvailableBackends...),
			Providers:         append([]string(nil), s.cfg.Launch.Providers...),
			Models:            append([]string(nil), s.cfg.Launch.Models...),
		},
		UI: UIConfig{
			DeferredFeatures: append([]string(nil), s.cfg.DisabledUI...),
		},
	}
}

func (s *Stub) ListSessions(_ context.Context, req ListSessionsRequest) (ListSessionsResponse, error) {
	var groupKey *string
	if req.GroupKey != "" {
		v := req.GroupKey
		groupKey = &v
	}
	return ListSessionsResponse{
		Items:          []SessionSummary{},
		RemainingCount: 0,
		GroupKey:       groupKey,
	}, nil
}

func (s *Stub) CreateSession(_ context.Context, _ CreateSessionRequest) (CreateSessionResponse, error) {
	return CreateSessionResponse{}, Unsupported("session creation not implemented")
}
