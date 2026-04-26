package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

type Service interface {
	Bootstrap(context.Context) BootstrapSnapshot
	ListSessions(context.Context, ListSessionsRequest) (ListSessionsResponse, error)
	CreateSession(context.Context, CreateSessionRequest) (CreateSessionResponse, error)
	SessionResumeCandidates(context.Context, SessionResumeCandidatesRequest) (SessionResumeCandidatesResponse, error)
	SessionDetails(context.Context, SessionDetailsRequest) (SessionDetailsResponse, error)
	SessionMessages(context.Context, SessionMessagesRequest) (SessionMessagesResponse, error)
	SessionState(context.Context, SessionStateRequest) (SessionStateResponse, error)
	SessionWorkspace(context.Context, SessionWorkspaceRequest) (SessionWorkspaceResponse, error)
	UpdateSessionWorkspace(context.Context, UpdateSessionWorkspaceRequest) (SessionWorkspaceResponse, error)
	WorkspaceFileList(context.Context, WorkspaceFileListRequest) (WorkspaceFileListResponse, error)
	WorkspaceFileRead(context.Context, WorkspaceFileReadRequest) (WorkspaceFileReadResponse, error)
	GitFileVersions(context.Context, GitFileVersionsRequest) (GitFileVersionsResponse, error)
	RenameSession(context.Context, RenameSessionRequest) (RenameSessionResponse, error)
	FocusSession(context.Context, FocusSessionRequest) (FocusSessionResponse, error)
	EditSession(context.Context, EditSessionRequest) (EditSessionResponse, error)
	EditCwdGroup(context.Context, EditCwdGroupRequest) (EditCwdGroupResponse, error)
	SwitchSessionModel(context.Context, SwitchSessionModelRequest) (SwitchSessionModelResponse, error)
	DeleteSession(context.Context, DeleteSessionRequest) (DeleteSessionResponse, error)
	RestartSession(context.Context, RestartSessionRequest) (RestartSessionResponse, error)
	HandoffSession(context.Context, HandoffSessionRequest) (HandoffSessionResponse, error)
}

type Stub struct {
	cfg            config.Config
	registry       *sessionRegistry
	launcher       runtimeLauncher
	sink           RuntimeEventSink
	appStore       appStateStore
	helperDialer   helperDialer
	helperBindings helperBindingStore
	helpers        *helperRegistry
	appStateMu     sync.RWMutex
	recentCwds     []string
	cwdGroups      map[string]CwdGroupMeta
}

func NewStub(cfg config.Config) (*Stub, error) {
	return newPersistentStubWithRuntime(cfg, time.Now, RuntimeConfig{UseIODHelper: true})
}

func NewStubForTest(cfg config.Config, now func() time.Time, runtimeCfg RuntimeConfig) *Stub {
	return newStubWithRuntime(cfg, now, runtimeCfg)
}

func NewPersistentStubForTest(cfg config.Config, now func() time.Time, runtimeCfg RuntimeConfig) (*Stub, error) {
	return newPersistentStubWithRuntime(cfg, now, runtimeCfg)
}

func newStub(cfg config.Config, now func() time.Time) *Stub {
	return newStubWithRuntime(cfg, now, RuntimeConfig{Runner: &process.FakeRunner{}})
}

func newStubWithRuntime(cfg config.Config, now func() time.Time, runtimeCfg RuntimeConfig) *Stub {
	if strings.TrimSpace(runtimeCfg.IODRuntimeRoot) == "" {
		runtimeCfg.IODRuntimeRoot = iodclient.RuntimeRoot(cfg.Storage.DataDir)
	}
	return &Stub{
		cfg:            cfg,
		registry:       newSessionRegistry(now),
		launcher:       newRuntimeLauncher(runtimeCfg),
		helperDialer:   runtimeCfg.IODDialer,
		helperBindings: newHelperBindingStore(cfg.Storage.DataDir),
		helpers:        newHelperRegistry(),
		recentCwds:     []string{},
		cwdGroups:      map[string]CwdGroupMeta{},
	}
}

type BootstrapSnapshot struct {
	ProtocolVersion int                     `json:"protocol_version"`
	Capabilities    Capabilities            `json:"capabilities"`
	WS              WSConfig                `json:"ws"`
	LaunchDefaults  LaunchConfig            `json:"launch_defaults"`
	UI              UIConfig                `json:"ui"`
	RecentCwds      []string                `json:"recent_cwds,omitempty"`
	CwdGroups       map[string]CwdGroupMeta `json:"cwd_groups,omitempty"`
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

type CwdGroupMeta struct {
	Label     string `json:"label,omitempty"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

type SessionSummary struct {
	SessionID           string  `json:"session_id"`
	RuntimeID           string  `json:"runtime_id,omitempty"`
	ThreadID            string  `json:"thread_id,omitempty"`
	AgentBackend        string  `json:"agent_backend"`
	Title               string  `json:"title"`
	Alias               string  `json:"alias,omitempty"`
	DisplayName         string  `json:"display_name,omitempty"`
	FirstUserMessage    string  `json:"first_user_message,omitempty"`
	CWD                 string  `json:"cwd"`
	Busy                bool    `json:"busy"`
	Focused             bool    `json:"focused,omitempty"`
	QueueLen            int     `json:"queue_len,omitempty"`
	LastUpdatedTS       float64 `json:"last_updated_ts"`
	UpdatedTS           float64 `json:"updated_ts,omitempty"`
	Historical          bool    `json:"historical"`
	Model               string  `json:"model,omitempty"`
	ProviderChoice      string  `json:"provider_choice,omitempty"`
	ReasoningEffort     string  `json:"reasoning_effort,omitempty"`
	PriorityOffset      float64 `json:"priority_offset,omitempty"`
	SnoozeUntil         *int64  `json:"snooze_until,omitempty"`
	DependencySessionID string  `json:"dependency_session_id,omitempty"`
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

type EditCwdGroupRequest struct {
	CWD       string  `json:"cwd"`
	Label     *string `json:"label,omitempty"`
	Collapsed *bool   `json:"collapsed,omitempty"`
}

type EditCwdGroupResponse struct {
	OK        bool   `json:"ok"`
	CWD       string `json:"cwd"`
	Label     string `json:"label,omitempty"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

type CreatedSession struct {
	SessionID    string `json:"session_id"`
	RuntimeID    string `json:"runtime_id,omitempty"`
	ThreadID     string `json:"thread_id,omitempty"`
	AgentBackend string `json:"agent_backend"`
	Alias        string `json:"alias,omitempty"`
	CWD          string `json:"cwd"`
	Busy         bool   `json:"busy"`
	Focused      bool   `json:"focused,omitempty"`
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
	recentCwds, cwdGroups := s.bootstrapAppStateSnapshot()
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
		RecentCwds: recentCwds,
		CwdGroups:  cwdGroups,
	}
}

func (s *Stub) ListSessions(_ context.Context, req ListSessionsRequest) (ListSessionsResponse, error) {
	var groupKey *string
	if req.GroupKey != "" {
		v := req.GroupKey
		groupKey = &v
	}
	items := sortSessionsForDisplay(s.registry.List(), s.registry.now())
	offset, limit := listWindow(req)
	start, end := paginate(len(items), offset, limit)
	summaries := make([]SessionSummary, 0, end-start)
	for _, item := range items[start:end] {
		summaries = append(summaries, sessionSummaryFromRecord(item.record, item.updatedAt))
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
	if err := s.recordRecentCWD(cwd); err != nil {
		return CreateSessionResponse{}, err
	}
	identity, err := s.registry.ReserveIdentity(backend)
	if err != nil {
		return CreateSessionResponse{}, err
	}
	runtime, err := s.launcher.Launch(ctx, runtimeLaunchRequest{
		SessionID:       identity.SessionID(),
		Backend:         backend,
		CWD:             cwd,
		Provider:        optionalString(req.Provider),
		Model:           optionalString(req.Model),
		ReasoningEffort: optionalString(req.ReasoningEffort),
	})
	if err != nil {
		_ = runtime.CleanupHelperArtifacts()
		return CreateSessionResponse{}, err
	}
	bindingSaved := false
	rollbackRuntime := func() {
		_ = runtime.Kill(context.Background())
		if bindingSaved {
			_ = s.helperBindings.Delete(identity.SessionID())
		}
		_ = runtime.CleanupHelperArtifacts()
	}
	if err := s.bindRuntimeCurrentGeneration(identity.SessionID(), runtime); err != nil {
		rollbackRuntime()
		return CreateSessionResponse{}, err
	}
	bindingSaved = true
	record, err := s.registry.Create(sessionCreateSpec{
		Identity:        &identity,
		Backend:         backend,
		CWD:             cwd,
		Provider:        optionalString(req.Provider),
		Model:           optionalString(req.Model),
		ReasoningEffort: optionalString(req.ReasoningEffort),
		Title:           optionalString(req.Title),
		Runtime:         runtime,
	})
	if err != nil {
		rollbackRuntime()
		return CreateSessionResponse{}, err
	}
	s.startRuntimeIngest(record.identity.SessionID(), backend, runtime)
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

func sessionSummaryFromRecord(record sessionRecord, updatedAt time.Time) SessionSummary {
	runtimeID, _ := record.identity.RuntimeID()
	threadID, _ := record.identity.ThreadID()
	return SessionSummary{
		SessionID:           record.identity.SessionID().String(),
		RuntimeID:           runtimeID.String(),
		ThreadID:            threadID.String(),
		AgentBackend:        record.identity.Backend().String(),
		Title:               record.title,
		Alias:               displayAlias(record),
		DisplayName:         sessionDisplayName(record),
		FirstUserMessage:    firstUserMessageForRecord(record),
		CWD:                 record.cwd,
		Busy:                record.state.Busy(),
		Focused:             record.focused,
		QueueLen:            record.state.Queue().Len(),
		LastUpdatedTS:       timestampSeconds(updatedAt),
		UpdatedTS:           timestampSeconds(updatedAt),
		Historical:          record.identity.Historical(),
		Model:               record.model,
		ProviderChoice:      record.provider,
		ReasoningEffort:     record.reasoningEffort,
		PriorityOffset:      record.priorityOffset,
		SnoozeUntil:         unixSecondsPtr(record.snoozeUntil),
		DependencySessionID: sessionIDString(record.dependencySessionID),
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
		Alias:        displayAlias(record),
		CWD:          record.cwd,
		Busy:         record.state.Busy(),
		Focused:      record.focused,
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

func sessionIDString(id *session.SessionID) string {
	if id == nil {
		return ""
	}
	return id.String()
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
