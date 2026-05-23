package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/session"
)

const (
	defaultSupervisorIdleMinutes        = 5
	defaultSupervisorMaxConsecutiveRuns = 10
	supervisorMaxFileCharsPerFile       = 20000
	supervisorMaxTotalFileChars         = 60000
	supervisorSystemPrompt              = `You supervise a coding-agent session for the human operator.

Decide whether the agent should receive one more user message.

Return only JSON in one of these forms:
{"action":"stop","reason":"short reason"}
{"action":"inject","message":"user-facing instruction","reason":"short reason"}

Use stop when the assistant appears done, asks no actionable next step, or further prompting would be speculative.
Use inject only when the assistant stopped prematurely and a human operator would likely send a short continuation message.
The injected message must be phrased exactly as a user instruction to the coding agent.
Do not mention supervisor, policy, JSON, evaluation, or hidden context in the injected message.
When uncertain, choose stop.`
)

type supervisorStore interface {
	LookupSupervisorProviderSettings(context.Context) (sqlitestore.SupervisorProviderSettingsRow, bool, error)
	UpsertSupervisorProviderSettings(context.Context, sqlitestore.SupervisorProviderSettingsRow) error
	LookupSessionSupervisorConfig(context.Context, string) (sqlitestore.SessionSupervisorConfigRow, bool, error)
	ListSessionSupervisorConfigs(context.Context, []string) ([]sqlitestore.SessionSupervisorConfigRow, error)
	UpsertSessionSupervisorConfig(context.Context, sqlitestore.SessionSupervisorConfigRow) error
	InsertSupervisorRun(context.Context, sqlitestore.SupervisorRunRow) error
	UpdateSupervisorRun(context.Context, sqlitestore.SupervisorRunRow) error
	ListSupervisorRuns(context.Context, string, int) ([]sqlitestore.SupervisorRunRow, error)
	LookupSupervisorRunByAnchor(context.Context, string, string) (sqlitestore.SupervisorRunRow, bool, error)
}

type SupervisorProviderRequest struct{}

type SupervisorProviderResponse struct {
	OK               bool   `json:"ok"`
	BaseURL          string `json:"base_url"`
	Model            string `json:"model"`
	APIKeyConfigured bool   `json:"api_key_configured"`
	Complete         bool   `json:"complete"`
}

type UpdateSupervisorProviderRequest struct {
	BaseURL string
	APIKey  *string
	Model   string
}

type TestSupervisorProviderRequest struct {
	BaseURL string
	APIKey  *string
	Model   string
}

type TestSupervisorProviderResponse struct {
	OK         bool   `json:"ok"`
	Status     string `json:"status"`
	StatusCode int    `json:"status_code,omitempty"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	Output     string `json:"output,omitempty"`
}

type SessionSupervisorRequest struct {
	SessionID session.SessionID
}

type SupervisorRunSummary struct {
	RunID                  string  `json:"run_id"`
	AnchorAssistantEventID string  `json:"anchor_assistant_event_id"`
	AnchorAssistantTS      float64 `json:"anchor_assistant_ts,omitempty"`
	Status                 string  `json:"status"`
	Action                 string  `json:"action,omitempty"`
	InjectedText           string  `json:"injected_text,omitempty"`
	Reason                 string  `json:"reason,omitempty"`
	Error                  string  `json:"error,omitempty"`
	Model                  string  `json:"model,omitempty"`
	BaseURL                string  `json:"base_url,omitempty"`
	CreatedTS              float64 `json:"created_ts,omitempty"`
}

type SupervisorRunsRequest struct {
	SessionID session.SessionID
	Limit     int
}

type SupervisorRunsResponse struct {
	OK   bool                   `json:"ok"`
	Runs []SupervisorRunSummary `json:"runs"`
}

type SupervisorRunOnceRequest struct {
	SessionID session.SessionID
	DryRun    bool
}

type SupervisorRunOnceResponse struct {
	OK  bool                 `json:"ok"`
	Run SupervisorRunSummary `json:"run"`
}

type supervisorPair struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

type supervisorSnapshotFile struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type supervisorSnapshot struct {
	RecentPairs        []supervisorPair         `json:"recent_pairs"`
	Goal               string                   `json:"goal,omitempty"`
	AcceptanceCriteria string                   `json:"acceptance_criteria,omitempty"`
	Files              []supervisorSnapshotFile `json:"files,omitempty"`
}

type supervisorDecision struct {
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	Reason  string `json:"reason"`
}

const (
	supervisorRunStatusEvaluating     = "evaluating"
	supervisorRunStatusStop           = "stop"
	supervisorRunStatusInjected       = "injected"
	supervisorRunStatusError          = "error"
	supervisorRunStatusSkippedStale   = "skipped_stale"
	supervisorRunStatusSkippedBlocked = "skipped_blocked"
)

type supervisorCommitSkip struct {
	status string
	reason string
}

func (e supervisorCommitSkip) Error() string {
	return e.reason
}

type supervisorInjectedWarning struct {
	reason string
}

func (e supervisorInjectedWarning) Error() string {
	return e.reason
}

type SessionSupervisorResponse struct {
	OK                       bool     `json:"ok"`
	Supported                bool     `json:"supported"`
	Enabled                  bool     `json:"enabled"`
	Status                   string   `json:"status"`
	IdleAfterMinutes         int      `json:"idle_after_minutes"`
	MaxConsecutiveInjections int      `json:"max_consecutive_injections"`
	ConsecutiveInjections    int      `json:"consecutive_injections"`
	Goal                     string   `json:"goal"`
	AcceptanceCriteria       string   `json:"acceptance_criteria"`
	ContextFiles             []string `json:"context_files"`
}

type UpdateSessionSupervisorRequest struct {
	SessionID                session.SessionID
	Enabled                  *bool
	IdleAfterMinutes         *int
	MaxConsecutiveInjections *int
	ConsecutiveInjections    *int
	Goal                     *string
	AcceptanceCriteria       *string
	ContextFiles             *[]string
}

type memorySupervisorStore struct {
	mu             sync.Mutex
	provider       sqlitestore.SupervisorProviderSettingsRow
	providerSet    bool
	sessionConfigs map[string]sqlitestore.SessionSupervisorConfigRow
	runs           []sqlitestore.SupervisorRunRow
}

func newMemorySupervisorStore() *memorySupervisorStore {
	return &memorySupervisorStore{sessionConfigs: map[string]sqlitestore.SessionSupervisorConfigRow{}}
}

func (m *memorySupervisorStore) LookupSupervisorProviderSettings(_ context.Context) (sqlitestore.SupervisorProviderSettingsRow, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.provider, m.providerSet, nil
}

func (m *memorySupervisorStore) UpsertSupervisorProviderSettings(_ context.Context, row sqlitestore.SupervisorProviderSettingsRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.provider = row
	m.providerSet = true
	return nil
}

func (m *memorySupervisorStore) LookupSessionSupervisorConfig(_ context.Context, sessionID string) (sqlitestore.SessionSupervisorConfigRow, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.sessionConfigs[sessionID]
	return row, ok, nil
}

func (m *memorySupervisorStore) ListSessionSupervisorConfigs(_ context.Context, sessionIDs []string) ([]sqlitestore.SessionSupervisorConfigRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]sqlitestore.SessionSupervisorConfigRow, 0, len(sessionIDs))
	seen := map[string]struct{}{}
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		if row, ok := m.sessionConfigs[sessionID]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *memorySupervisorStore) UpsertSessionSupervisorConfig(_ context.Context, row sqlitestore.SessionSupervisorConfigRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionConfigs[row.SessionID] = row
	return nil
}

func (m *memorySupervisorStore) InsertSupervisorRun(_ context.Context, row sqlitestore.SupervisorRunRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.runs {
		if existing.SessionID == row.SessionID && existing.AnchorAssistantEventID == row.AnchorAssistantEventID {
			return fmt.Errorf("supervisor run for anchor already exists")
		}
	}
	m.runs = append(m.runs, row)
	return nil
}

func (m *memorySupervisorStore) UpdateSupervisorRun(_ context.Context, row sqlitestore.SupervisorRunRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.runs {
		if m.runs[i].RunID == row.RunID {
			m.runs[i] = row
			return nil
		}
	}
	return fmt.Errorf("supervisor run %q not found", row.RunID)
}

func (m *memorySupervisorStore) ListSupervisorRuns(_ context.Context, sessionID string, limit int) ([]sqlitestore.SupervisorRunRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]sqlitestore.SupervisorRunRow, 0)
	for i := len(m.runs) - 1; i >= 0 && len(out) < limit; i-- {
		if m.runs[i].SessionID == sessionID {
			out = append(out, m.runs[i])
		}
	}
	return out, nil
}

func (m *memorySupervisorStore) LookupSupervisorRunByAnchor(_ context.Context, sessionID, anchor string) (sqlitestore.SupervisorRunRow, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.runs {
		if row.SessionID == sessionID && row.AnchorAssistantEventID == anchor {
			return row, true, nil
		}
	}
	return sqlitestore.SupervisorRunRow{}, false, nil
}

func (s *Stub) SupervisorProvider(ctx context.Context, _ SupervisorProviderRequest) (SupervisorProviderResponse, error) {
	row, ok, err := s.supervisorStore.LookupSupervisorProviderSettings(ctx)
	if err != nil {
		return SupervisorProviderResponse{}, err
	}
	if !ok {
		return supervisorProviderResponse(sqlitestore.SupervisorProviderSettingsRow{}), nil
	}
	return supervisorProviderResponse(row), nil
}

func (s *Stub) UpdateSupervisorProvider(ctx context.Context, req UpdateSupervisorProviderRequest) (SupervisorProviderResponse, error) {
	current, ok, err := s.supervisorStore.LookupSupervisorProviderSettings(ctx)
	if err != nil {
		return SupervisorProviderResponse{}, err
	}
	if !ok {
		current = sqlitestore.SupervisorProviderSettingsRow{}
	}
	apiKey := current.APIKey
	if req.APIKey != nil {
		apiKey = strings.TrimSpace(*req.APIKey)
	}
	next := sqlitestore.SupervisorProviderSettingsRow{
		BaseURL:   strings.TrimSpace(req.BaseURL),
		APIKey:    apiKey,
		Model:     strings.TrimSpace(req.Model),
		UpdatedAt: s.registry.now(),
	}
	if err := s.supervisorStore.UpsertSupervisorProviderSettings(ctx, next); err != nil {
		return SupervisorProviderResponse{}, err
	}
	return supervisorProviderResponse(next), nil
}

func (s *Stub) TestSupervisorProvider(ctx context.Context, req TestSupervisorProviderRequest) (TestSupervisorProviderResponse, error) {
	current, ok, err := s.supervisorStore.LookupSupervisorProviderSettings(ctx)
	if err != nil {
		return TestSupervisorProviderResponse{}, err
	}
	if !ok {
		current = sqlitestore.SupervisorProviderSettingsRow{}
	}
	provider := sqlitestore.SupervisorProviderSettingsRow{
		BaseURL: strings.TrimSpace(req.BaseURL),
		APIKey:  current.APIKey,
		Model:   strings.TrimSpace(req.Model),
	}
	if provider.BaseURL == "" {
		provider.BaseURL = strings.TrimSpace(current.BaseURL)
	}
	if provider.Model == "" {
		provider.Model = strings.TrimSpace(current.Model)
	}
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		provider.APIKey = strings.TrimSpace(*req.APIKey)
	}
	if strings.TrimSpace(provider.BaseURL) == "" {
		return TestSupervisorProviderResponse{}, Invalid("base_url", "base_url required")
	}
	if strings.TrimSpace(provider.Model) == "" {
		return TestSupervisorProviderResponse{}, Invalid("model", "model required")
	}
	if strings.TrimSpace(provider.APIKey) == "" {
		return TestSupervisorProviderResponse{}, Invalid("api_key", "api_key required")
	}
	messages := []supervisorChatMessage{
		{Role: "system", Content: "You are a provider connectivity test. Return a short plain response."},
		{Role: "user", Content: "Say hello from ActRail Supervisor provider test."},
	}
	content, _, statusCode, err := supervisorChatCompletion(ctx, provider, messages, 20*time.Second)
	if err != nil {
		return TestSupervisorProviderResponse{}, err
	}
	return TestSupervisorProviderResponse{
		OK:         true,
		Status:     "provider chat completion succeeded",
		StatusCode: statusCode,
		BaseURL:    strings.TrimSpace(provider.BaseURL),
		Model:      strings.TrimSpace(provider.Model),
		Output:     content,
	}, nil
}

func supervisorBackendSupported(backend session.Backend) bool {
	return backend == session.BackendPI || backend == session.BackendCodex
}

func (s *Stub) SessionSupervisor(ctx context.Context, req SessionSupervisorRequest) (SessionSupervisorResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionSupervisorResponse{}, err
	}
	if !supervisorBackendSupported(record.identity.Backend()) {
		return SessionSupervisorResponse{}, UnsupportedBackend("supervisor mode is only supported for pi and codex sessions")
	}
	config, err := s.sessionSupervisorConfig(ctx, record.identity.SessionID())
	if err != nil {
		return SessionSupervisorResponse{}, err
	}
	return sessionSupervisorResponse(config), nil
}

func (s *Stub) UpdateSessionSupervisor(ctx context.Context, req UpdateSessionSupervisorRequest) (SessionSupervisorResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionSupervisorResponse{}, err
	}
	if !supervisorBackendSupported(record.identity.Backend()) {
		return SessionSupervisorResponse{}, UnsupportedBackend("supervisor mode is only supported for pi and codex sessions")
	}
	s.deferredInputMu.Lock()
	defer s.deferredInputMu.Unlock()
	config, err := s.sessionSupervisorConfig(ctx, record.identity.SessionID())
	if err != nil {
		return SessionSupervisorResponse{}, err
	}
	if req.Enabled != nil {
		config.Enabled = *req.Enabled
	}
	if req.IdleAfterMinutes != nil {
		if *req.IdleAfterMinutes < 1 {
			return SessionSupervisorResponse{}, Invalid("idle_after_minutes", "idle_after_minutes must be at least 1")
		}
		config.IdleAfterMinutes = *req.IdleAfterMinutes
	}
	if req.MaxConsecutiveInjections != nil {
		if *req.MaxConsecutiveInjections < 1 {
			return SessionSupervisorResponse{}, Invalid("max_consecutive_injections", "max_consecutive_injections must be at least 1")
		}
		config.MaxConsecutiveInjections = *req.MaxConsecutiveInjections
	}
	if req.ConsecutiveInjections != nil {
		if *req.ConsecutiveInjections < 0 {
			return SessionSupervisorResponse{}, Invalid("consecutive_injections", "consecutive_injections must be non-negative")
		}
		config.ConsecutiveInjections = *req.ConsecutiveInjections
	}
	if config.ConsecutiveInjections > config.MaxConsecutiveInjections {
		return SessionSupervisorResponse{}, Invalid("consecutive_injections", "consecutive_injections cannot exceed max_consecutive_injections")
	}
	if req.Goal != nil {
		config.Goal = strings.TrimSpace(*req.Goal)
	}
	if req.AcceptanceCriteria != nil {
		config.AcceptanceCriteria = strings.TrimSpace(*req.AcceptanceCriteria)
	}
	if req.ContextFiles != nil {
		config.ContextFiles = cleanContextFiles(*req.ContextFiles)
	}
	config.UpdatedAt = s.registry.now()
	if err := s.supervisorStore.UpsertSessionSupervisorConfig(ctx, config); err != nil {
		return SessionSupervisorResponse{}, err
	}
	return sessionSupervisorResponse(config), nil
}

func (s *Stub) sessionSupervisorConfig(ctx context.Context, sessionID session.SessionID) (sqlitestore.SessionSupervisorConfigRow, error) {
	config, ok, err := s.supervisorStore.LookupSessionSupervisorConfig(ctx, sessionID.String())
	if err != nil {
		return sqlitestore.SessionSupervisorConfigRow{}, err
	}
	if ok {
		return normalizeSupervisorConfig(config, s.registry.now()), nil
	}
	return defaultSupervisorConfig(sessionID.String(), s.registry.now()), nil
}

func (s *Stub) sessionSupervisorResponsesBySessionID(ctx context.Context, sessionIDs []session.SessionID) (map[session.SessionID]SessionSupervisorResponse, bool) {
	rawSessionIDs := make([]string, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		rawSessionIDs = append(rawSessionIDs, sessionID.String())
	}
	rows, err := s.supervisorStore.ListSessionSupervisorConfigs(ctx, rawSessionIDs)
	if err != nil {
		return nil, false
	}
	now := s.registry.now()
	out := make(map[session.SessionID]SessionSupervisorResponse, len(rows))
	for _, row := range rows {
		sessionID, err := session.ParseSessionID(strings.TrimSpace(row.SessionID))
		if err != nil {
			continue
		}
		out[sessionID] = sessionSupervisorResponse(normalizeSupervisorConfig(row, now))
	}
	return out, true
}

func defaultSupervisorConfig(sessionID string, now time.Time) sqlitestore.SessionSupervisorConfigRow {
	return sqlitestore.SessionSupervisorConfigRow{
		SessionID:                sessionID,
		IdleAfterMinutes:         defaultSupervisorIdleMinutes,
		MaxConsecutiveInjections: defaultSupervisorMaxConsecutiveRuns,
		ContextFiles:             []string{},
		UpdatedAt:                now,
	}
}

func normalizeSupervisorConfig(config sqlitestore.SessionSupervisorConfigRow, now time.Time) sqlitestore.SessionSupervisorConfigRow {
	if config.IdleAfterMinutes < 1 {
		config.IdleAfterMinutes = defaultSupervisorIdleMinutes
	}
	if config.MaxConsecutiveInjections < 1 {
		config.MaxConsecutiveInjections = defaultSupervisorMaxConsecutiveRuns
	}
	if config.ConsecutiveInjections < 0 {
		config.ConsecutiveInjections = 0
	}
	if config.ContextFiles == nil {
		config.ContextFiles = []string{}
	}
	if config.UpdatedAt.IsZero() {
		config.UpdatedAt = now
	}
	return config
}

func supervisorProviderResponse(row sqlitestore.SupervisorProviderSettingsRow) SupervisorProviderResponse {
	baseURL := strings.TrimSpace(row.BaseURL)
	model := strings.TrimSpace(row.Model)
	apiKeyConfigured := strings.TrimSpace(row.APIKey) != ""
	return SupervisorProviderResponse{
		OK:               true,
		BaseURL:          baseURL,
		Model:            model,
		APIKeyConfigured: apiKeyConfigured,
		Complete:         baseURL != "" && model != "" && apiKeyConfigured,
	}
}

func sessionSupervisorResponse(config sqlitestore.SessionSupervisorConfigRow) SessionSupervisorResponse {
	status := "idle"
	if config.Enabled && config.ConsecutiveInjections >= config.MaxConsecutiveInjections {
		status = "limit_reached"
	}
	return SessionSupervisorResponse{
		OK:                       true,
		Supported:                true,
		Enabled:                  config.Enabled,
		Status:                   status,
		IdleAfterMinutes:         config.IdleAfterMinutes,
		MaxConsecutiveInjections: config.MaxConsecutiveInjections,
		ConsecutiveInjections:    config.ConsecutiveInjections,
		Goal:                     strings.TrimSpace(config.Goal),
		AcceptanceCriteria:       strings.TrimSpace(config.AcceptanceCriteria),
		ContextFiles:             append([]string(nil), config.ContextFiles...),
	}
}

func cleanContextFiles(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
func (s *Stub) SupervisorRuns(ctx context.Context, req SupervisorRunsRequest) (SupervisorRunsResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SupervisorRunsResponse{}, err
	}
	rows, err := s.supervisorStore.ListSupervisorRuns(ctx, record.identity.SessionID().String(), req.Limit)
	if err != nil {
		return SupervisorRunsResponse{}, err
	}
	return SupervisorRunsResponse{OK: true, Runs: supervisorRunSummaries(rows)}, nil
}

func (s *Stub) RunSupervisorOnce(ctx context.Context, req SupervisorRunOnceRequest) (SupervisorRunOnceResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SupervisorRunOnceResponse{}, err
	}
	if !supervisorBackendSupported(record.identity.Backend()) {
		return SupervisorRunOnceResponse{}, UnsupportedBackend("supervisor mode is only supported for pi and codex sessions")
	}
	config, err := s.sessionSupervisorConfig(ctx, record.identity.SessionID())
	if err != nil {
		return SupervisorRunOnceResponse{}, err
	}
	if !config.Enabled {
		return SupervisorRunOnceResponse{}, Conflict("supervisor mode is disabled")
	}
	if config.ConsecutiveInjections >= config.MaxConsecutiveInjections {
		return SupervisorRunOnceResponse{}, Conflict("supervisor consecutive injection limit reached")
	}
	if busy, reason := effectiveBusy(record); busy {
		if reason != "" {
			return SupervisorRunOnceResponse{}, Conflict("session is busy: " + reason)
		}
		return SupervisorRunOnceResponse{}, Conflict("session is busy")
	}
	if record.state.Queue().Len() > 0 {
		return SupervisorRunOnceResponse{}, Conflict("session has queued prompts")
	}
	if record.uiRequest != nil {
		return SupervisorRunOnceResponse{}, Conflict("session has unresolved UI request")
	}
	if s.activeWaitForSession(record.identity.SessionID()) != nil {
		return SupervisorRunOnceResponse{}, Conflict("session has active wait")
	}
	providerRow, ok, err := s.supervisorStore.LookupSupervisorProviderSettings(ctx)
	if err != nil {
		return SupervisorRunOnceResponse{}, err
	}
	provider := supervisorProviderResponse(providerRow)
	if !ok || !provider.Complete {
		return SupervisorRunOnceResponse{}, Conflict("supervisor provider settings are incomplete")
	}
	messages, anchor, err := s.supervisorMessagesAndAnchor(ctx, record)
	if err != nil {
		return SupervisorRunOnceResponse{}, err
	}
	if anchor.TS > 0 && s.registry.now().Sub(time.Unix(int64(anchor.TS), 0)) < time.Duration(config.IdleAfterMinutes)*time.Minute {
		return SupervisorRunOnceResponse{}, Conflict("assistant message has not been idle long enough")
	}
	if existing, ok, err := s.supervisorStore.LookupSupervisorRunByAnchor(ctx, record.identity.SessionID().String(), anchor.EventID); err != nil {
		return SupervisorRunOnceResponse{}, err
	} else if ok {
		return SupervisorRunOnceResponse{OK: true, Run: supervisorRunSummary(existing)}, nil
	}
	run := sqlitestore.SupervisorRunRow{
		RunID:                   newID("supervisor"),
		SessionID:               record.identity.SessionID().String(),
		AnchorAssistantEventID:  anchor.EventID,
		AnchorAssistantTS:       anchor.TS,
		AnchorAssistantTextHash: textHash(anchor.Text),
		Status:                  supervisorRunStatusEvaluating,
		Model:                   provider.Model,
		BaseURL:                 provider.BaseURL,
		SnapshotJSON:            "{}",
		CreatedAt:               s.registry.now(),
	}
	if err := s.supervisorStore.InsertSupervisorRun(ctx, run); err != nil {
		if existing, ok, lookupErr := s.supervisorStore.LookupSupervisorRunByAnchor(ctx, record.identity.SessionID().String(), anchor.EventID); lookupErr == nil && ok {
			return SupervisorRunOnceResponse{OK: true, Run: supervisorRunSummary(existing)}, nil
		}
		return SupervisorRunOnceResponse{}, err
	}
	snapshot, snapshotErr := buildSupervisorSnapshot(record.cwd, messages, anchor.EventID, config)
	if snapshotErr != nil {
		run.Status = supervisorRunStatusError
		run.Error = snapshotErr.Error()
	} else {
		snapshotJSON, _ := json.Marshal(snapshot)
		run.SnapshotJSON = string(snapshotJSON)
		decision, rawOutput, evalErr := evaluateSupervisorModel(ctx, providerRow, snapshot)
		run.RawOutput = rawOutput
		if evalErr != nil {
			run.Status = supervisorRunStatusError
			run.Error = evalErr.Error()
		} else {
			run.Action = decision.Action
			run.Reason = decision.Reason
			if decision.Action == "inject" {
				run.InjectedText = decision.Message
				if req.DryRun {
					run.Status = supervisorRunStatusStop
				} else {
					if err := s.commitSupervisorInjection(ctx, record.identity.SessionID(), anchor, decision.Message); err != nil {
						var skip supervisorCommitSkip
						var warning supervisorInjectedWarning
						if errors.As(err, &skip) {
							run.Status = skip.status
						} else if errors.As(err, &warning) {
							run.Status = supervisorRunStatusInjected
						} else {
							run.Status = supervisorRunStatusError
						}
						run.Error = err.Error()
					} else {
						run.Status = supervisorRunStatusInjected
					}
				}
			} else {
				run.Status = supervisorRunStatusStop
			}
		}
	}
	if err := s.supervisorStore.UpdateSupervisorRun(ctx, run); err != nil {
		return SupervisorRunOnceResponse{}, err
	}
	return SupervisorRunOnceResponse{OK: true, Run: supervisorRunSummary(run)}, nil
}

func (s *Stub) commitSupervisorInjection(ctx context.Context, sessionID session.SessionID, anchor SessionMessage, message string) error {
	s.deferredInputMu.Lock()
	defer s.deferredInputMu.Unlock()

	var config sqlitestore.SessionSupervisorConfigRow
	if _, err := s.sendWithPrecondition(ctx, SendRequest{SessionID: sessionID, Text: message}, false, func(record sessionRecord) error {
		currentConfig, err := s.sessionSupervisorConfig(ctx, sessionID)
		if err != nil {
			return err
		}
		config = currentConfig
		return s.supervisorCommitPrecondition(ctx, record, anchor, currentConfig)
	}); err != nil {
		return err
	}
	config.ConsecutiveInjections++
	config.UpdatedAt = s.registry.now()
	if err := s.supervisorStore.UpsertSessionSupervisorConfig(ctx, config); err != nil {
		return supervisorInjectedWarning{reason: "supervisor injected but failed to update consecutive injection count: " + err.Error()}
	}
	return nil
}

func (s *Stub) supervisorCommitPrecondition(ctx context.Context, record sessionRecord, anchor SessionMessage, config sqlitestore.SessionSupervisorConfigRow) error {
	sessionID := record.identity.SessionID()
	if !supervisorBackendSupported(record.identity.Backend()) {
		return supervisorSkip(supervisorRunStatusSkippedBlocked, "supervisor mode is only supported for pi and codex sessions")
	}
	if !config.Enabled {
		return supervisorSkip(supervisorRunStatusSkippedBlocked, "supervisor mode is disabled")
	}
	if config.ConsecutiveInjections >= config.MaxConsecutiveInjections {
		return supervisorSkip(supervisorRunStatusSkippedBlocked, "supervisor consecutive injection limit reached")
	}
	if busy, reason := effectiveBusy(record); busy {
		if reason != "" {
			return supervisorSkip(supervisorRunStatusSkippedBlocked, "session is busy: "+reason)
		}
		return supervisorSkip(supervisorRunStatusSkippedBlocked, "session is busy")
	}
	if record.state.Queue().Len() > 0 {
		return supervisorSkip(supervisorRunStatusSkippedBlocked, "session has queued prompts")
	}
	if record.uiRequest != nil {
		return supervisorSkip(supervisorRunStatusSkippedBlocked, "session has unresolved UI request")
	}
	if s.activeWaitForSession(sessionID) != nil {
		return supervisorSkip(supervisorRunStatusSkippedBlocked, "session has active wait")
	}
	if err := transportControlError(sessionTransportSnapshot(record)); err != nil {
		return supervisorSkip(supervisorRunStatusSkippedBlocked, err.Error())
	}
	now := s.registry.now().UTC()
	due, err := s.schedulerStore.CountDueSchedulerItemsForSession(ctx, sessionID.String(), now)
	if err != nil {
		return err
	}
	if due > 0 {
		return supervisorSkip(supervisorRunStatusSkippedBlocked, "session has due scheduler items")
	}
	openInbox, err := s.schedulerStore.CountOpenInboxItemsForSession(ctx, sessionID.String())
	if err != nil {
		return err
	}
	if openInbox > 0 {
		return supervisorSkip(supervisorRunStatusSkippedBlocked, "session inbox is not empty")
	}
	_, currentAnchor, err := s.supervisorMessagesAndAnchor(ctx, record)
	if err != nil {
		return supervisorSkip(supervisorRunStatusSkippedStale, err.Error())
	}
	if currentAnchor.EventID != anchor.EventID || textHash(currentAnchor.Text) != textHash(anchor.Text) {
		return supervisorSkip(supervisorRunStatusSkippedStale, "last assistant anchor changed before supervisor injection")
	}
	if currentAnchor.TS > 0 && now.Sub(time.Unix(int64(currentAnchor.TS), 0)) < time.Duration(config.IdleAfterMinutes)*time.Minute {
		return supervisorSkip(supervisorRunStatusSkippedBlocked, "assistant message has not been idle long enough")
	}
	return nil
}

func supervisorSkip(status string, reason string) error {
	return supervisorCommitSkip{status: status, reason: strings.TrimSpace(reason)}
}

func (s *Stub) lastStableAssistantAnchor(ctx context.Context, record sessionRecord) (SessionMessage, error) {
	response, err := s.SessionMessages(ctx, SessionMessagesRequest{SessionID: record.identity.SessionID(), Limit: 200})
	if err != nil {
		return SessionMessage{}, err
	}
	for i := len(response.Items) - 1; i >= 0; i-- {
		item := response.Items[i]
		if item.Role != "assistant" || strings.TrimSpace(item.Text) == "" {
			continue
		}
		if strings.TrimSpace(item.EventID) == "" {
			return SessionMessage{}, Invalid("anchor_assistant_event_id", "last stable assistant message has no event id")
		}
		return item, nil
	}
	return SessionMessage{}, Conflict("no stable assistant message found")
}

func (s *Stub) annotateSupervisorRuns(ctx context.Context, sessionID session.SessionID, response SessionMessagesResponse) SessionMessagesResponse {
	rows, err := s.supervisorStore.ListSupervisorRuns(ctx, sessionID.String(), 1000)
	if err != nil || len(rows) == 0 {
		return response
	}
	byAnchor := map[string][]SupervisorRunSummary{}
	for _, row := range rows {
		byAnchor[row.AnchorAssistantEventID] = append(byAnchor[row.AnchorAssistantEventID], supervisorRunSummary(row))
	}
	for i := range response.Items {
		if response.Items[i].Role != "assistant" || response.Items[i].EventID == "" {
			continue
		}
		if runs := byAnchor[response.Items[i].EventID]; len(runs) > 0 {
			response.Items[i].SupervisorRuns = append([]SupervisorRunSummary(nil), runs...)
		}
	}
	return response
}

func supervisorRunSummaries(rows []sqlitestore.SupervisorRunRow) []SupervisorRunSummary {
	out := make([]SupervisorRunSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, supervisorRunSummary(row))
	}
	return out
}

func supervisorRunSummary(row sqlitestore.SupervisorRunRow) SupervisorRunSummary {
	return SupervisorRunSummary{
		RunID:                  row.RunID,
		AnchorAssistantEventID: row.AnchorAssistantEventID,
		AnchorAssistantTS:      row.AnchorAssistantTS,
		Status:                 row.Status,
		Action:                 row.Action,
		InjectedText:           row.InjectedText,
		Reason:                 row.Reason,
		Error:                  row.Error,
		Model:                  row.Model,
		BaseURL:                row.BaseURL,
		CreatedTS:              timestampSeconds(row.CreatedAt),
	}
}

func textHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (s *Stub) supervisorMessagesAndAnchor(ctx context.Context, record sessionRecord) ([]SessionMessage, SessionMessage, error) {
	response, err := s.SessionMessages(ctx, SessionMessagesRequest{SessionID: record.identity.SessionID(), Limit: 200})
	if err != nil {
		return nil, SessionMessage{}, err
	}
	for i := len(response.Items) - 1; i >= 0; i-- {
		item := response.Items[i]
		if item.Role != "assistant" || strings.TrimSpace(item.Text) == "" {
			continue
		}
		if strings.TrimSpace(item.EventID) == "" {
			return nil, SessionMessage{}, Invalid("anchor_assistant_event_id", "last stable assistant message has no event id")
		}
		return response.Items, item, nil
	}
	return nil, SessionMessage{}, Conflict("no stable assistant message found")
}

func buildSupervisorSnapshot(cwd string, messages []SessionMessage, anchorEventID string, config sqlitestore.SessionSupervisorConfigRow) (supervisorSnapshot, error) {
	pairs := make([]supervisorPair, 0, 2)
	pendingUser := ""
	for _, item := range messages {
		if item.Kind != "message" {
			continue
		}
		if item.Role == "user" {
			pendingUser = strings.TrimSpace(item.Text)
			continue
		}
		if item.Role == "assistant" && pendingUser != "" {
			pairs = append(pairs, supervisorPair{User: pendingUser, Assistant: strings.TrimSpace(item.Text)})
			if item.EventID == anchorEventID {
				break
			}
			pendingUser = ""
		}
	}
	if len(pairs) > 2 {
		pairs = pairs[len(pairs)-2:]
	}
	files, err := readSupervisorContextFiles(cwd, config.ContextFiles)
	if err != nil {
		return supervisorSnapshot{}, err
	}
	return supervisorSnapshot{
		RecentPairs:        pairs,
		Goal:               strings.TrimSpace(config.Goal),
		AcceptanceCriteria: strings.TrimSpace(config.AcceptanceCriteria),
		Files:              files,
	}, nil
}

func readSupervisorContextFiles(cwd string, paths []string) ([]supervisorSnapshotFile, error) {
	files := make([]supervisorSnapshotFile, 0, len(paths))
	totalChars := 0
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}
		resolved := path
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(cwd, resolved)
		}
		body, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read supervisor context file %q: %w", path, err)
		}
		if !utf8.Valid(body) {
			return nil, fmt.Errorf("supervisor context file %q is not valid UTF-8 text", path)
		}
		content := string(body)
		runes := []rune(content)
		truncated := false
		remaining := supervisorMaxTotalFileChars - totalChars
		if remaining <= 0 {
			content = ""
			truncated = true
		} else {
			limit := supervisorMaxFileCharsPerFile
			if remaining < limit {
				limit = remaining
			}
			if len(runes) > limit {
				content = string(runes[:limit])
				truncated = true
			}
		}
		totalChars += len([]rune(content))
		files = append(files, supervisorSnapshotFile{Path: path, Content: content, Truncated: truncated})
	}
	return files, nil
}

type supervisorChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func evaluateSupervisorModel(ctx context.Context, provider sqlitestore.SupervisorProviderSettingsRow, snapshot supervisorSnapshot) (supervisorDecision, string, error) {
	messages := []supervisorChatMessage{
		{Role: "system", Content: supervisorSystemPrompt},
		{Role: "user", Content: supervisorSnapshotPrompt(snapshot)},
	}
	raw, resBody, _, err := supervisorChatCompletion(ctx, provider, messages, 60*time.Second)
	if err != nil {
		return supervisorDecision{}, resBody, err
	}
	decision, err := parseSupervisorDecision(raw)
	return decision, raw, err
}

func supervisorChatCompletion(ctx context.Context, provider sqlitestore.SupervisorProviderSettingsRow, messages []supervisorChatMessage, timeout time.Duration) (string, string, int, error) {
	body, err := json.Marshal(map[string]any{
		"model":       strings.TrimSpace(provider.Model),
		"messages":    messages,
		"temperature": 0,
	})
	if err != nil {
		return "", "", 0, err
	}
	url := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/") + "/chat/completions"
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(provider.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer res.Body.Close()
	resBody, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", "", res.StatusCode, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", string(resBody), res.StatusCode, fmt.Errorf("supervisor model http %d", res.StatusCode)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resBody, &parsed); err != nil {
		return "", string(resBody), res.StatusCode, fmt.Errorf("parse supervisor model response: %w", err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", string(resBody), res.StatusCode, fmt.Errorf("supervisor model returned no content")
	}
	raw := strings.TrimSpace(parsed.Choices[0].Message.Content)
	return raw, string(resBody), res.StatusCode, nil
}

func supervisorSnapshotPrompt(snapshot supervisorSnapshot) string {
	body, _ := json.MarshalIndent(snapshot, "", "  ")
	return "Evaluate this ActRail supervisor snapshot. Return strict JSON only.\n" + string(body)
}

func parseSupervisorDecision(raw string) (supervisorDecision, error) {
	var decision supervisorDecision
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decision); err != nil {
		return supervisorDecision{}, fmt.Errorf("invalid supervisor JSON: %w", err)
	}
	decision.Action = strings.TrimSpace(decision.Action)
	decision.Message = strings.TrimSpace(decision.Message)
	decision.Reason = strings.TrimSpace(decision.Reason)
	if decision.Reason == "" {
		return supervisorDecision{}, fmt.Errorf("supervisor reason required")
	}
	switch decision.Action {
	case "stop":
		if decision.Message != "" {
			return supervisorDecision{}, fmt.Errorf("stop action must not include message")
		}
	case "inject":
		if decision.Message == "" {
			return supervisorDecision{}, fmt.Errorf("inject action requires message")
		}
	default:
		return supervisorDecision{}, fmt.Errorf("unknown supervisor action %q", decision.Action)
	}
	return decision, nil
}
func (s *Stub) RunSupervisorScheduler(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSupervisorSweep(ctx)
		}
	}
}

func (s *Stub) runSupervisorSweep(ctx context.Context) {
	for _, record := range s.registry.List() {
		if !supervisorBackendSupported(record.identity.Backend()) {
			continue
		}
		config, err := s.sessionSupervisorConfig(ctx, record.identity.SessionID())
		if err != nil || !config.Enabled {
			continue
		}
		_, _ = s.RunSupervisorOnce(ctx, SupervisorRunOnceRequest{SessionID: record.identity.SessionID(), DryRun: false})
	}
}
