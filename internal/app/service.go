package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

type Service interface {
	Bootstrap(context.Context) BootstrapSnapshot
	ListSessions(context.Context, ListSessionsRequest) (ListSessionsResponse, error)
	CreateSession(context.Context, CreateSessionRequest) (CreateSessionResponse, error)
	SessionDetails(context.Context, SessionDetailsRequest) (SessionDetailsResponse, error)
	SessionMessages(context.Context, SessionMessagesRequest) (SessionMessagesResponse, error)
	SessionState(context.Context, SessionStateRequest) (SessionStateResponse, error)
	SessionWorkspace(context.Context, SessionWorkspaceRequest) (SessionWorkspaceResponse, error)
	WorkspaceFileList(context.Context, WorkspaceFileListRequest) (WorkspaceFileListResponse, error)
	WorkspaceFileRead(context.Context, WorkspaceFileReadRequest) (WorkspaceFileReadResponse, error)
	GitFileVersions(context.Context, GitFileVersionsRequest) (GitFileVersionsResponse, error)
}

type Stub struct {
	cfg      config.Config
	registry *sessionRegistry
	launcher runtimeLauncher
}

func NewStub(cfg config.Config) *Stub {
	return newStubWithRuntime(cfg, time.Now, RuntimeConfig{})
}

func newStub(cfg config.Config, now func() time.Time) *Stub {
	return newStubWithRuntime(cfg, now, RuntimeConfig{Runner: &process.FakeRunner{}})
}

func newStubWithRuntime(cfg config.Config, now func() time.Time, runtimeCfg RuntimeConfig) *Stub {
	return &Stub{
		cfg:      cfg,
		registry: newSessionRegistry(now),
		launcher: newRuntimeLauncher(runtimeCfg),
	}
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
	OK       bool                  `json:"ok"`
	Session  *CreatedSession       `json:"session,omitempty"`
	WSAttach *SessionAttachRequest `json:"ws_attach,omitempty"`
}

type CreatedSession struct {
	SessionID    string `json:"session_id"`
	RuntimeID    string `json:"runtime_id,omitempty"`
	ThreadID     string `json:"thread_id,omitempty"`
	AgentBackend string `json:"agent_backend"`
	CWD          string `json:"cwd"`
	Busy         bool   `json:"busy"`
}

type SessionAttachRequest struct {
	SessionID            string   `json:"session_id"`
	SuggestSubscriptions []string `json:"suggest_subscriptions"`
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

func Invalid(field, message string) *Error {
	return &Error{Code: "invalid_request", Message: message, Field: field}
}

func Conflict(message string) *Error {
	return &Error{Code: "conflict", Message: message}
}

func NotFound(message string) *Error {
	return &Error{Code: "not_found", Message: message}
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
	items := s.registry.List()
	offset, limit := listWindow(req)
	start, end := paginate(len(items), offset, limit)
	summaries := make([]SessionSummary, 0, end-start)
	for _, record := range items[start:end] {
		summaries = append(summaries, sessionSummaryFromRecord(record))
	}
	return ListSessionsResponse{
		Items:          summaries,
		RemainingCount: len(items) - end,
		GroupKey:       groupKey,
	}, nil
}

func (s *Stub) CreateSession(ctx context.Context, req CreateSessionRequest) (CreateSessionResponse, error) {
	cwd := strings.TrimSpace(req.CWD)
	if cwd == "" {
		return CreateSessionResponse{}, Invalid("cwd", "cwd required")
	}
	backend, err := session.ParseBackend(req.AgentBackend)
	if err != nil {
		return CreateSessionResponse{}, Invalid("agent_backend", err.Error())
	}
	if resumeID := optionalString(req.ResumeSessionID); resumeID != "" {
		parsed, err := session.ParseSessionID(resumeID)
		if err != nil {
			return CreateSessionResponse{}, Invalid("resume_session_id", err.Error())
		}
		if _, ok := s.registry.Lookup(parsed); !ok {
			return CreateSessionResponse{}, NotFound(fmt.Sprintf("session %q not found", parsed))
		}
		return CreateSessionResponse{}, Unsupported("session resume not implemented")
	}
	runtime, err := s.launcher.Launch(ctx, runtimeLaunchRequest{
		Backend:         backend,
		CWD:             cwd,
		Provider:        optionalString(req.Provider),
		Model:           optionalString(req.Model),
		ReasoningEffort: optionalString(req.ReasoningEffort),
	})
	if err != nil {
		return CreateSessionResponse{}, err
	}
	record, err := s.registry.Create(sessionCreateSpec{
		Backend:         backend,
		CWD:             cwd,
		Provider:        optionalString(req.Provider),
		Model:           optionalString(req.Model),
		ReasoningEffort: optionalString(req.ReasoningEffort),
		Title:           optionalString(req.Title),
		Runtime:         runtime,
	})
	if err != nil {
		return CreateSessionResponse{}, err
	}
	stream, err := session.MainStream(record.identity)
	if err != nil {
		return CreateSessionResponse{}, err
	}
	return CreateSessionResponse{
		OK:      true,
		Session: createdSessionFromRecord(record),
		WSAttach: &SessionAttachRequest{
			SessionID:            record.identity.SessionID().String(),
			SuggestSubscriptions: []string{stream.String()},
		},
	}, nil
}

func sessionSummaryFromRecord(record sessionRecord) SessionSummary {
	runtimeID, _ := record.identity.RuntimeID()
	threadID, _ := record.identity.ThreadID()
	return SessionSummary{
		SessionID:     record.identity.SessionID().String(),
		RuntimeID:     runtimeID.String(),
		ThreadID:      threadID.String(),
		AgentBackend:  record.identity.Backend().String(),
		Title:         record.title,
		CWD:           record.cwd,
		Busy:          record.state.Busy(),
		LastUpdatedTS: timestampSeconds(record.updatedAt),
		Historical:    record.identity.Historical(),
	}
}

func createdSessionFromRecord(record sessionRecord) *CreatedSession {
	runtimeID, _ := record.identity.RuntimeID()
	threadID, _ := record.identity.ThreadID()
	return &CreatedSession{
		SessionID:    record.identity.SessionID().String(),
		RuntimeID:    runtimeID.String(),
		ThreadID:     threadID.String(),
		AgentBackend: record.identity.Backend().String(),
		CWD:          record.cwd,
		Busy:         record.state.Busy(),
	}
}

func (s *Stub) capabilitiesSnapshot() SessionCapabilitySnapshot {
	return SessionCapabilitySnapshot{
		WSRealtime:     s.cfg.Features.WebSocketRealtime,
		Voice:          s.cfg.Features.Voice,
		Harness:        s.cfg.Features.Harness,
		Notifications:  s.cfg.Features.Notifications,
		PIUI:           s.cfg.Features.PIUI,
		WorkspaceRead:  s.cfg.Features.WorkspaceRead,
		WorkspaceWrite: s.cfg.Features.WorkspaceWrite,
	}
}

func optionalString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func timestampSeconds(ts time.Time) float64 {
	if ts.IsZero() {
		return 0
	}
	return float64(ts.UnixNano()) / float64(time.Second)
}

func listWindow(req ListSessionsRequest) (int, int) {
	offset := req.Offset
	limit := req.Limit
	if strings.TrimSpace(req.GroupKey) != "" {
		if req.GroupOffset > 0 {
			offset = req.GroupOffset
		}
		if req.GroupLimit > 0 {
			limit = req.GroupLimit
		}
	}
	return offset, limit
}

func paginate(total, offset, limit int) (int, int) {
	if offset >= total {
		return total, total
	}
	if limit <= 0 {
		return offset, total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return offset, end
}
