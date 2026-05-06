package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

type Service interface {
	Bootstrap(context.Context, BootstrapRequest) BootstrapSnapshot
	ListSessions(context.Context, ListSessionsRequest) (ListSessionsResponse, error)
	CreateSession(context.Context, CreateSessionRequest) (CreateSessionResponse, error)
	ListTeams(context.Context, ListTeamsRequest) (ListTeamsResponse, error)
	SpawnTeam(context.Context, SpawnTeamRequest) (TeamCommandResponse, error)
	PromptTeam(context.Context, PromptTeamRequest) (TeamCommandResponse, error)
	FollowupTeam(context.Context, FollowupTeamRequest) (TeamCommandResponse, error)
	SendTeam(context.Context, SendTeamRequest) (TeamDeliveryResponse, error)
	AskParent(context.Context, AskParentRequest) (AskParentResponse, error)
	ResumeAskParent(context.Context, AskParentRequest) (AskParentResponse, error)
	AnswerTeam(context.Context, AnswerTeamRequest) (TeamCommandResponse, error)
	AbortTeam(context.Context, AbortTeamRequest) (TeamCommandResponse, error)
	CloseTeam(context.Context, CloseTeamRequest) (TeamCommandResponse, error)
	StatusTeam(context.Context, StatusTeamRequest) (TeamCommandResponse, error)
	TeamEvents(context.Context, TeamEventsRequest) (TeamEventsResponse, error)
	SessionResumeCandidates(context.Context, SessionResumeCandidatesRequest) (SessionResumeCandidatesResponse, error)
	SessionDetails(context.Context, SessionDetailsRequest) (SessionDetailsResponse, error)
	SessionMessages(context.Context, SessionMessagesRequest) (SessionMessagesResponse, error)
	SessionState(context.Context, SessionStateRequest) (SessionStateResponse, error)
	ProbeSessionState(context.Context, ProbeSessionStateRequest) (ProbeSessionStateResponse, error)
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
	SessionCommands(context.Context, SessionCommandsRequest) (SessionCommandsResponse, error)
	ExecuteSessionCommand(context.Context, ExecuteSessionCommandRequest) (ExecuteSessionCommandResponse, error)
	WaitInbox(context.Context) (WaitInboxResponse, error)
	WaitThreads(context.Context, WaitThreadsRequest) (WaitThreadsResponse, error)
	WaitThread(context.Context, WaitThreadRequest) (WaitThreadResponse, error)
	CreateWait(context.Context, CreateWaitRequest) (WaitLifecycleResponse, error)
	ClaimWait(context.Context, WaitLifecycleRequest) (WaitLifecycleResponse, error)
	AnswerWait(context.Context, WaitLifecycleRequest) (WaitLifecycleResponse, error)
	CancelWait(context.Context, WaitLifecycleRequest) (WaitLifecycleResponse, error)
	DeleteSession(context.Context, DeleteSessionRequest) (DeleteSessionResponse, error)
	RestartSession(context.Context, RestartSessionRequest) (RestartSessionResponse, error)
	HandoffSession(context.Context, HandoffSessionRequest) (HandoffSessionResponse, error)
	SupervisorProvider(context.Context, SupervisorProviderRequest) (SupervisorProviderResponse, error)
	UpdateSupervisorProvider(context.Context, UpdateSupervisorProviderRequest) (SupervisorProviderResponse, error)
	SessionSupervisor(context.Context, SessionSupervisorRequest) (SessionSupervisorResponse, error)
	UpdateSessionSupervisor(context.Context, UpdateSessionSupervisorRequest) (SessionSupervisorResponse, error)
	SupervisorRuns(context.Context, SupervisorRunsRequest) (SupervisorRunsResponse, error)
	RunSupervisorOnce(context.Context, SupervisorRunOnceRequest) (SupervisorRunOnceResponse, error)
	SchedulerSnapshot(context.Context, SchedulerSnapshotRequest) (SchedulerSnapshotResponse, error)
	UpdateSchedulerSettings(context.Context, UpdateSchedulerSettingsRequest) (SchedulerSettings, error)
	SessionInbox(context.Context, SessionInboxRequest) (SessionInboxResponse, error)
	SetAlarm(context.Context, SetAlarmRequest) (SetAlarmResponse, error)
}

type Stub struct {
	cfg                 config.Config
	registry            *sessionRegistry
	launcher            runtimeLauncher
	sink                RuntimeEventSink
	appStore            appStateStore
	helperDialer        helperDialer
	helperBindings      helperBindingStore
	helpers             *helperRegistry
	messageCache        *sessionMessageCache
	waitStore           waitStore
	waitBlockersMu      sync.Mutex
	waitBlockers        map[string]waitBlocker
	supervisorStore     supervisorStore
	schedulerStore      schedulerStore
	teams               *teamRegistry
	runtimeAgentMu      sync.RWMutex
	runtimeAgentRunning map[session.SessionID]bool
	piRPCStateMu        sync.Mutex
	piRPCStates         map[session.SessionID]piRPCStateCache
	piResumePaths       piResumePathCache
	piModels            piModelCache
	appStateMu          sync.RWMutex
	recentCwds          []string
	cwdGroups           map[string]CwdGroupMeta
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
		runtimeCfg.IODRuntimeRoot = cfg.Storage.IODRuntimeRoot()
	}
	return &Stub{
		cfg:                 cfg,
		registry:            newSessionRegistry(now),
		launcher:            newRuntimeLauncher(runtimeCfg),
		helperDialer:        runtimeCfg.IODDialer,
		helperBindings:      newHelperBindingStore(cfg.Storage.IODBindingsDir()),
		helpers:             newHelperRegistry(),
		messageCache:        newSessionMessageCache(defaultSessionMessageCacheEntries),
		waitStore:           newMemoryWaitStore(),
		waitBlockers:        map[string]waitBlocker{},
		supervisorStore:     newMemorySupervisorStore(),
		schedulerStore:      newMemorySchedulerStore(),
		teams:               newTeamRegistry(now),
		runtimeAgentRunning: map[session.SessionID]bool{},
		piRPCStates:         map[session.SessionID]piRPCStateCache{},
		piResumePaths:       piResumePathCache{},
		piModels:            piModelCache{},
		recentCwds:          []string{},
		cwdGroups:           map[string]CwdGroupMeta{},
	}
}

type BootstrapRequest struct {
	RefreshPIModels bool
}

type BootstrapSnapshot struct {
	ProtocolVersion    int                     `json:"protocol_version"`
	Capabilities       Capabilities            `json:"capabilities"`
	WS                 WSConfig                `json:"ws"`
	Transport          TransportConfig         `json:"transport"`
	LaunchDefaults     LaunchConfig            `json:"launch_defaults"`
	NewSessionDefaults NewSessionDefaults      `json:"new_session_defaults,omitempty"`
	UI                 UIConfig                `json:"ui"`
	RecentCwds         []string                `json:"recent_cwds,omitempty"`
	CwdGroups          map[string]CwdGroupMeta `json:"cwd_groups,omitempty"`
}

type Capabilities struct {
	WSRealtime          bool `json:"ws_realtime"`
	Voice               bool `json:"voice"`
	Harness             bool `json:"harness"`
	Notifications       bool `json:"notifications"`
	PIUI                bool `json:"pi_ui"`
	WorkspaceRead       bool `json:"workspace_read"`
	WorkspaceWrite      bool `json:"workspace_write"`
	ExpConnectTransport bool `json:"exp_connect_transport"`
}

type TransportConfig struct {
	Default      string   `json:"default"`
	Experimental []string `json:"experimental,omitempty"`
	ConnectPath  string   `json:"connect_path,omitempty"`
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

type NewSessionDefaults struct {
	DefaultBackend     string                           `json:"default_backend,omitempty"`
	Backends           map[string]LaunchBackendDefaults `json:"backends,omitempty"`
	PIAgentGRPCDefault bool                             `json:"pi_agent_grpc_default"`
}

type LaunchBackendDefaults struct {
	ProviderChoice   string              `json:"provider_choice,omitempty"`
	ProviderChoices  []string            `json:"provider_choices,omitempty"`
	Model            string              `json:"model,omitempty"`
	Models           []string            `json:"models,omitempty"`
	ProviderModels   map[string][]string `json:"provider_models,omitempty"`
	ModelProvider    string              `json:"model_provider,omitempty"`
	ModelProviders   []string            `json:"model_providers,omitempty"`
	ReasoningEffort  string              `json:"reasoning_effort,omitempty"`
	ReasoningEfforts []string            `json:"reasoning_efforts,omitempty"`
	ServiceTier      string              `json:"service_tier,omitempty"`
	SupportsFast     bool                `json:"supports_fast,omitempty"`
	ModelsCachedAt   int64               `json:"models_cached_at,omitempty"`
}

type UIConfig struct {
	DeferredFeatures []string `json:"deferred_features"`
}

type CwdGroupMeta struct {
	Label     string `json:"label,omitempty"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

type SessionSummary struct {
	SessionID           string                     `json:"session_id"`
	RuntimeID           string                     `json:"runtime_id,omitempty"`
	ThreadID            string                     `json:"thread_id,omitempty"`
	GenerationID        string                     `json:"generation_id,omitempty"`
	AgentBackend        string                     `json:"agent_backend"`
	Title               string                     `json:"title"`
	Alias               string                     `json:"alias,omitempty"`
	DisplayName         string                     `json:"display_name,omitempty"`
	FirstUserMessage    string                     `json:"first_user_message,omitempty"`
	CWD                 string                     `json:"cwd"`
	Busy                bool                       `json:"busy"`
	Focused             bool                       `json:"focused,omitempty"`
	QueueLen            int                        `json:"queue_len,omitempty"`
	TransportState      string                     `json:"transport_state,omitempty"`
	Probing             bool                       `json:"probing,omitempty"`
	ResetRequired       bool                       `json:"reset_required,omitempty"`
	TransportReason     string                     `json:"transport_reason,omitempty"`
	PendingStartup      bool                       `json:"pending_startup,omitempty"`
	LastUpdatedTS       float64                    `json:"last_updated_ts"`
	UpdatedTS           float64                    `json:"updated_ts,omitempty"`
	LastAssistantTS     float64                    `json:"last_assistant_message_ts,omitempty"`
	Historical          bool                       `json:"historical"`
	Model               string                     `json:"model,omitempty"`
	ProviderChoice      string                     `json:"provider_choice,omitempty"`
	ReasoningEffort     string                     `json:"reasoning_effort,omitempty"`
	PriorityOffset      float64                    `json:"priority_offset,omitempty"`
	SnoozeUntil         *int64                     `json:"snooze_until,omitempty"`
	DependencySessionID string                     `json:"dependency_session_id,omitempty"`
	SessionFilePath     string                     `json:"session_file_path,omitempty"`
	BackendSessionID    string                     `json:"backend_session_id,omitempty"`
	ActiveWait          *ActiveWaitSummary         `json:"active_wait,omitempty"`
	Supervisor          *SessionSupervisorResponse `json:"supervisor,omitempty"`
	IOD                 *IODRuntimeSummary         `json:"iod,omitempty"`
}

type IODRuntimeSummary struct {
	BuildDate string  `json:"build_date,omitempty"`
	GitSHA    string  `json:"git_sha,omitempty"`
	StartTS   float64 `json:"start_ts,omitempty"`
	Mode      string  `json:"mode"`
}

type ListSessionsRequest struct {
	GroupKey     string
	Offset       int
	Limit        int
	GroupOffset  int
	GroupLimit   int
	AgentBackend string
	CWD          string
	Title        string
}

type ListSessionsResponse struct {
	Items          []SessionSummary `json:"items"`
	RemainingCount int              `json:"remaining_count"`
	TotalCount     int              `json:"total_count"`
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
	PIAgentGRPC     *bool   `json:"pi_agent_grpc"`
	Hidden          bool    `json:"-"`
}

type CreateSessionResponse struct {
	OK       bool                  `json:"ok"`
	Session  *CreatedSession       `json:"session,omitempty"`
	WSAttach *SessionAttachRequest `json:"ws_attach,omitempty"`
}

func createdSessionFromRecord(record sessionRecord) *CreatedSession {
	return (&Stub{}).createdSessionFromRecord(record)
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
	SessionID       string `json:"session_id"`
	RuntimeID       string `json:"runtime_id,omitempty"`
	ThreadID        string `json:"thread_id,omitempty"`
	GenerationID    string `json:"generation_id,omitempty"`
	AgentBackend    string `json:"agent_backend"`
	Alias           string `json:"alias,omitempty"`
	CWD             string `json:"cwd"`
	Busy            bool   `json:"busy"`
	Focused         bool   `json:"focused,omitempty"`
	TransportState  string `json:"transport_state,omitempty"`
	Probing         bool   `json:"probing,omitempty"`
	ResetRequired   bool   `json:"reset_required,omitempty"`
	TransportReason string `json:"transport_reason,omitempty"`
	PendingStartup  bool   `json:"pending_startup,omitempty"`
	SessionFilePath string `json:"session_file_path,omitempty"`
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

func UnsupportedBackend(message string) *Error {
	return &Error{Code: "unsupported_backend", Message: message}
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

func Forbidden(message string) *Error {
	return &Error{Code: "forbidden", Message: message}
}

func (s *Stub) Bootstrap(ctx context.Context, req BootstrapRequest) BootstrapSnapshot {
	recentCwds, cwdGroups := s.bootstrapAppStateSnapshot()
	return BootstrapSnapshot{
		ProtocolVersion: s.cfg.Protocol.Version,
		Capabilities: Capabilities{
			WSRealtime:          s.cfg.Features.WebSocketRealtime,
			Voice:               s.cfg.Features.Voice,
			Harness:             s.cfg.Features.Harness,
			Notifications:       s.cfg.Features.Notifications,
			PIUI:                s.cfg.Features.PIUI,
			WorkspaceRead:       s.cfg.Features.WorkspaceRead,
			WorkspaceWrite:      s.cfg.Features.WorkspaceWrite,
			ExpConnectTransport: true,
		},
		WS: WSConfig{
			URL:                 s.cfg.Protocol.WebSocketPath,
			HeartbeatIntervalMS: s.cfg.HeartbeatIntervalMillis(),
			ResumeBufferEvents:  s.cfg.Protocol.ResumeBuffer,
		},
		Transport: TransportConfig{
			Default:      "ws",
			Experimental: []string{"connect"},
			ConnectPath:  "/api/connect",
		},
		LaunchDefaults: LaunchConfig{
			DefaultBackend:    s.cfg.Launch.DefaultBackend,
			AvailableBackends: append([]string(nil), s.cfg.Launch.AvailableBackends...),
			Providers:         append([]string(nil), s.cfg.Launch.Providers...),
			Models:            append([]string(nil), s.cfg.Launch.Models...),
		},
		NewSessionDefaults: s.newSessionDefaults(ctx, req),
		UI: UIConfig{
			DeferredFeatures: append([]string(nil), s.cfg.DisabledUI...),
		},
		RecentCwds: recentCwds,
		CwdGroups:  cwdGroups,
	}
}

func filterSessionRecords(records []sessionRecord, req ListSessionsRequest) []sessionRecord {
	backend := strings.TrimSpace(req.AgentBackend)
	cwd := normalizeSessionCWD(req.CWD)
	title := strings.TrimSpace(req.Title)
	if backend == "" && cwd == "" && title == "" {
		return records
	}
	out := make([]sessionRecord, 0, len(records))
	for _, record := range records {
		if backend != "" && !strings.EqualFold(record.identity.Backend().String(), backend) {
			continue
		}
		if cwd != "" && normalizeSessionCWD(record.cwd) != cwd {
			continue
		}
		if title != "" && record.title != title && record.alias != title && sessionDisplayName(record) != title {
			continue
		}
		out = append(out, record)
	}
	return out
}

func (s *Stub) ListSessions(_ context.Context, req ListSessionsRequest) (ListSessionsResponse, error) {
	var groupKey *string
	if req.GroupKey != "" {
		v := req.GroupKey
		groupKey = &v
	}
	items := sortSessionsForDisplay(filterSessionRecords(s.registry.List(), req), s.registry.now())
	offset, limit := listWindow(req)
	start, end := paginate(len(items), offset, limit)
	summaries := make([]SessionSummary, 0, end-start)
	for _, item := range items[start:end] {
		record := item.record
		record.runtime = s.runtimeForSession(record.identity.SessionID(), record.identity.Backend(), record.runtime)
		summaries = append(summaries, s.sessionSummaryFromRecord(record, item.updatedAt))
	}
	return ListSessionsResponse{
		Items:          summaries,
		RemainingCount: len(items) - end,
		TotalCount:     len(items),
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
	resumeSourcePath := ""
	resumeBackendSessionID := ""
	if resumeID := optionalString(req.ResumeSessionID); resumeID != "" {
		parsed, err := session.ParseSessionID(resumeID)
		if err != nil {
			return CreateSessionResponse{}, Invalid("resume_session_id", err.Error())
		}
		if parsed.IsHistorical() {
			if backend != session.BackendPI {
				return CreateSessionResponse{}, Invalid("resume_session_id", "historical Pi resume requires pi backend")
			}
			path, backendSessionID, ok := piSourcePathForHistoricalSession(cwd, parsed)
			if !ok {
				return CreateSessionResponse{}, NotFound(fmt.Sprintf("pi session %q not found for cwd %q", parsed, cwd))
			}
			resumeSourcePath = path
			resumeBackendSessionID = backendSessionID
		} else {
			record, ok := s.registry.Lookup(parsed)
			if !ok {
				return CreateSessionResponse{}, NotFound(fmt.Sprintf("session %q not found", parsed))
			}
			if backend != record.identity.Backend() {
				return CreateSessionResponse{}, Invalid("resume_session_id", "resume session backend does not match requested backend")
			}
			if backend != session.BackendPI {
				return CreateSessionResponse{}, Unsupported("session resume is only implemented for pi backend history")
			}
			resumeSourcePath = strings.TrimSpace(record.importedSourcePath)
			if resumeSourcePath == "" {
				return CreateSessionResponse{}, NotFound(fmt.Sprintf("pi session %q has no source path to resume", parsed))
			}
			resumeBackendSessionID = strings.TrimSpace(record.importedBackendSessionID)
			if resumeBackendSessionID == "" {
				if backendSessionID, ok, err := piSessionIDFromSourcePath(resumeSourcePath); err == nil && ok {
					resumeBackendSessionID = backendSessionID
				}
			}
		}
	}
	if !req.Hidden {
		if err := s.recordRecentCWD(cwd); err != nil {
			return CreateSessionResponse{}, err
		}
	}
	identity, err := s.registry.ReserveIdentity(backend)
	if err != nil {
		return CreateSessionResponse{}, err
	}
	sourcePath := resumeSourcePath
	if backend == session.BackendPI && sourcePath == "" {
		var err error
		sourcePath, err = newPISessionSourcePath(cwd, identity.SessionID(), s.registry.now())
		if err != nil {
			return CreateSessionResponse{}, err
		}
	}
	runtime, err := s.launcher.Launch(ctx, runtimeLaunchRequest{
		SessionID:       identity.SessionID(),
		Backend:         backend,
		CWD:             cwd,
		Provider:        optionalString(req.Provider),
		Model:           optionalString(req.Model),
		ReasoningEffort: optionalString(req.ReasoningEffort),
		SessionPath:     sourcePath,
		PIAgentGRPC:     createSessionUsesPIAgentGRPC(req, backend),
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
	transport := SessionTransportSnapshot{}
	if runtime.PendingPIAgentGRPCReady() {
		transport = transportSnapshotPIAgentGRPCStarting()
	} else if runtime.UsesPIAgentGRPC() {
		transport = piAgentGRPCTransportSnapshot()
	}
	record, err := s.registry.Create(sessionCreateSpec{
		Identity:         &identity,
		Backend:          backend,
		CWD:              cwd,
		Provider:         optionalString(req.Provider),
		Model:            optionalString(req.Model),
		ReasoningEffort:  optionalString(req.ReasoningEffort),
		Title:            optionalString(req.Title),
		Hidden:           req.Hidden,
		SourcePath:       sourcePath,
		BackendSessionID: resumeBackendSessionID,
		SourceConfidence: map[bool]string{true: sourceConfidenceExact, false: sourceConfidenceProvisional}[resumeBackendSessionID != ""],
		Runtime:          runtime,
		Transport:        transport,
	})
	if err != nil {
		rollbackRuntime()
		return CreateSessionResponse{}, err
	}
	s.startRuntimeIngest(record.identity.SessionID(), backend, runtime)
	s.startPIAgentGRPCReadyTransition(record.identity.SessionID(), runtime)
	stream, err := session.MainStream(record.identity)
	if err != nil {
		return CreateSessionResponse{}, err
	}
	return CreateSessionResponse{
		OK:      true,
		Session: s.createdSessionFromRecord(record),
		WSAttach: &SessionAttachRequest{
			SessionID:            record.identity.SessionID().String(),
			SuggestSubscriptions: []string{stream.String()},
		},
	}, nil
}

func createSessionUsesPIAgentGRPC(req CreateSessionRequest, backend session.Backend) bool {
	return backend == session.BackendPI && (req.PIAgentGRPC == nil || *req.PIAgentGRPC)
}

func (s *Stub) startPIAgentGRPCReadyTransition(sessionID session.SessionID, runtime sessionRuntime) {
	if s == nil || !runtime.PendingPIAgentGRPCReady() {
		return
	}
	go func() {
		readyTimeout := defaultHelperReadyTimeout
		if processRuntime, ok := s.launcher.(processRuntimeLauncher); ok {
			readyTimeout = processRuntime.piAgentGRPCReadyTimeout
		}
		deadline := time.Now().Add(readyTimeout)
		for {
			state, err := runtime.PIAgentGRPCState(context.Background())
			if err != nil {
				if !isPIAgentGRPCStartupTransient(err) || time.Now().After(deadline) {
					_ = runtime.Kill(context.Background())
					s.markPIAgentGRPCStartupFailed(sessionID, fmt.Errorf("wait for pi agent grpc: %w", err))
					return
				}
				time.Sleep(helperReadyPollInterval)
				continue
			}
			if state.RuntimeStarting() {
				if err := s.applyRuntimeProjection(sessionID, runtimeProjection{piRPCState: piRPCStateSnapshotFromGRPC(state)}); err != nil {
					_ = runtime.Kill(context.Background())
					s.markPIAgentGRPCStartupFailed(sessionID, err)
					return
				}
				if time.Now().After(deadline) {
					_ = runtime.Kill(context.Background())
					s.markPIAgentGRPCStartupFailed(sessionID, fmt.Errorf("pi agent grpc runtime starting: %s", firstNonEmptyString(state.RuntimeMessage(), "runtime starting")))
					return
				}
				time.Sleep(helperReadyPollInterval)
				continue
			}
			if state.RuntimeFailed() {
				_ = runtime.Kill(context.Background())
				s.markPIAgentGRPCStartupFailed(sessionID, fmt.Errorf("pi agent grpc runtime failed: %s", firstNonEmptyString(state.RuntimeMessage(), "runtime failed")))
				return
			}
			if err := s.applyRuntimeProjection(sessionID, runtimeProjection{piRPCState: piRPCStateSnapshotFromGRPC(state)}); err != nil {
				_ = runtime.Kill(context.Background())
				s.markPIAgentGRPCStartupFailed(sessionID, err)
				return
			}
			s.markPIAgentGRPCStartupReady(sessionID, runtime)
			return
		}
	}()
}

func isPIAgentGRPCStartupTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "dial unix") || strings.Contains(text, "connect: no such file or directory") || strings.Contains(text, "connection refused")
}

func (s *Stub) markPIAgentGRPCStartupReady(sessionID session.SessionID, runtime sessionRuntime) {
	runtime.piAgentGRPCReady = nil
	updated, ok, err := s.registry.SetRuntimeTransport(sessionID, runtime, transportSnapshotPIAgentGRPCAttached())
	if err != nil || !ok {
		_ = runtime.Kill(context.Background())
		return
	}
	s.emitSessionState(updated.identity.SessionID())
}

func (s *Stub) markPIAgentGRPCStartupFailed(sessionID session.SessionID, err error) {
	reason := "pi_agent_grpc_start_failed"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		reason = err.Error()
	}
	if _, ok, markErr := s.registry.MarkRuntimeCompleted(sessionID); markErr != nil || !ok {
		return
	}
	if _, ok, setErr := s.registry.SetTransport(sessionID, transportSnapshotPIAgentGRPCFailed(reason)); setErr != nil || !ok {
		return
	}
	_ = s.setRuntimeAgentRunning(sessionID, false)
	s.emitSessionState(sessionID)
}

func (s *Stub) sessionSummaryFromRecord(record sessionRecord, updatedAt time.Time) SessionSummary {
	runtimeID, _ := record.identity.RuntimeID()
	threadID, _ := record.identity.ThreadID()
	transport := s.sessionTransportSnapshot(record)
	var supervisor *SessionSupervisorResponse
	if record.identity.Backend() == session.BackendPI {
		if config, err := s.sessionSupervisorConfig(context.Background(), record.identity.SessionID()); err == nil {
			response := sessionSupervisorResponse(config)
			supervisor = &response
		}
	}
	return SessionSummary{
		SessionID:           record.identity.SessionID().String(),
		RuntimeID:           runtimeID.String(),
		ThreadID:            threadID.String(),
		GenerationID:        transport.GenerationID,
		AgentBackend:        record.identity.Backend().String(),
		Title:               record.title,
		Alias:               displayAlias(record),
		DisplayName:         sessionDisplayName(record),
		FirstUserMessage:    firstUserMessageForRecord(record),
		CWD:                 record.cwd,
		Busy:                record.state.Busy(),
		Focused:             record.focused,
		QueueLen:            record.state.Queue().Len(),
		TransportState:      transport.State.String(),
		Probing:             s.sessionProbing(record),
		ResetRequired:       transport.ResetRequired,
		TransportReason:     transport.Reason,
		PendingStartup:      transport.State == SessionTransportStateStarting,
		LastUpdatedTS:       timestampSeconds(updatedAt),
		UpdatedTS:           timestampSeconds(updatedAt),
		LastAssistantTS:     lastAssistantMessageTimestamp(record),
		Historical:          record.identity.Historical(),
		Model:               record.model,
		ProviderChoice:      record.provider,
		ReasoningEffort:     record.reasoningEffort,
		PriorityOffset:      record.priorityOffset,
		SnoozeUntil:         unixSecondsPtr(record.snoozeUntil),
		DependencySessionID: sessionIDString(record.dependencySessionID),
		SessionFilePath:     record.importedSourcePath,
		BackendSessionID:    record.importedBackendSessionID,
		ActiveWait:          s.activeWaitForSession(record.identity.SessionID()),
		Supervisor:          supervisor,
		IOD:                 s.iodRuntimeSummary(record),
	}
}

func lastAssistantMessageTimestamp(record sessionRecord) float64 {
	items := record.transcript.Items()
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if item.Role().String() != "assistant" {
			continue
		}
		return timestampSeconds(item.TS())
	}
	return 0
}

func (s *Stub) iodRuntimeSummary(record sessionRecord) *IODRuntimeSummary {
	if record.identity.Backend() != session.BackendPI || record.identity.Historical() {
		return nil
	}
	if record.runtime.piAgentGRPC != nil || shouldReattachPIAgentGRPC(record) {
		return grpcIODSummary()
	}
	if helper := record.runtime.helper; helper != nil {
		return helper.iodSummary()
	}
	if s != nil && s.helpers != nil {
		if attachment, ok := s.helpers.Attachment(record.identity.SessionID()); ok {
			return iodSummaryFromHello(attachment.Hello)
		}
	}
	return nil
}

func (s *Stub) createdSessionFromRecord(record sessionRecord) *CreatedSession {
	runtimeID, _ := record.identity.RuntimeID()
	threadID, _ := record.identity.ThreadID()
	transport := sessionTransportSnapshot(record)
	return &CreatedSession{
		SessionID:       record.identity.SessionID().String(),
		RuntimeID:       runtimeID.String(),
		ThreadID:        threadID.String(),
		GenerationID:    transport.GenerationID,
		AgentBackend:    record.identity.Backend().String(),
		Alias:           displayAlias(record),
		CWD:             record.cwd,
		Busy:            record.state.Busy(),
		Focused:         record.focused,
		TransportState:  transport.State.String(),
		Probing:         s.sessionProbing(record),
		ResetRequired:   transport.ResetRequired,
		TransportReason: transport.Reason,
		PendingStartup:  transport.State == SessionTransportStateStarting,
		SessionFilePath: record.importedSourcePath,
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
