package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/session"
)

const (
	defaultSupervisorIdleMinutes        = 5
	defaultSupervisorMaxConsecutiveRuns = 10
)

type supervisorStore interface {
	LookupSupervisorProviderSettings(context.Context) (sqlitestore.SupervisorProviderSettingsRow, bool, error)
	UpsertSupervisorProviderSettings(context.Context, sqlitestore.SupervisorProviderSettingsRow) error
	LookupSessionSupervisorConfig(context.Context, string) (sqlitestore.SessionSupervisorConfigRow, bool, error)
	UpsertSessionSupervisorConfig(context.Context, sqlitestore.SessionSupervisorConfigRow) error
	InsertSupervisorRun(context.Context, sqlitestore.SupervisorRunRow) error
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

func (s *Stub) SessionSupervisor(ctx context.Context, req SessionSupervisorRequest) (SessionSupervisorResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionSupervisorResponse{}, err
	}
	if record.identity.Backend() != session.BackendPI {
		return SessionSupervisorResponse{}, UnsupportedBackend("supervisor mode is only supported for pi sessions")
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
	if record.identity.Backend() != session.BackendPI {
		return SessionSupervisorResponse{}, UnsupportedBackend("supervisor mode is only supported for pi sessions")
	}
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
	if record.identity.Backend() != session.BackendPI {
		return SupervisorRunOnceResponse{}, UnsupportedBackend("supervisor mode is only supported for pi sessions")
	}
	config, err := s.sessionSupervisorConfig(ctx, record.identity.SessionID())
	if err != nil {
		return SupervisorRunOnceResponse{}, err
	}
	if !config.Enabled {
		return SupervisorRunOnceResponse{}, Conflict("supervisor mode is disabled")
	}
	provider, err := s.SupervisorProvider(ctx, SupervisorProviderRequest{})
	if err != nil {
		return SupervisorRunOnceResponse{}, err
	}
	if !provider.Complete {
		return SupervisorRunOnceResponse{}, Conflict("supervisor provider settings are incomplete")
	}
	anchor, err := s.lastStablePIAssistantAnchor(ctx, record)
	if err != nil {
		return SupervisorRunOnceResponse{}, err
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
		Status:                  "stop",
		Action:                  "stop",
		Reason:                  "dry run placeholder: model evaluation not executed",
		Model:                   provider.Model,
		BaseURL:                 provider.BaseURL,
		SnapshotJSON:            "{}",
		CreatedAt:               s.registry.now(),
	}
	if !req.DryRun {
		run.Status = "error"
		run.Action = ""
		run.Reason = ""
		run.Error = "non-dry-run supervisor execution is not implemented"
	}
	if err := s.supervisorStore.InsertSupervisorRun(ctx, run); err != nil {
		if existing, ok, lookupErr := s.supervisorStore.LookupSupervisorRunByAnchor(ctx, record.identity.SessionID().String(), anchor.EventID); lookupErr == nil && ok {
			return SupervisorRunOnceResponse{OK: true, Run: supervisorRunSummary(existing)}, nil
		}
		return SupervisorRunOnceResponse{}, err
	}
	return SupervisorRunOnceResponse{OK: true, Run: supervisorRunSummary(run)}, nil
}

func (s *Stub) lastStablePIAssistantAnchor(ctx context.Context, record sessionRecord) (SessionMessage, error) {
	response, err := s.SessionMessages(ctx, SessionMessagesRequest{SessionID: record.identity.SessionID(), Limit: 200})
	if err != nil {
		return SessionMessage{}, err
	}
	for i := len(response.Items) - 1; i >= 0; i-- {
		item := response.Items[i]
		if item.Role != "assistant" || strings.TrimSpace(item.Text) == "" {
			continue
		}
		if !strings.HasPrefix(item.EventID, "pi:message:") {
			return SessionMessage{}, Invalid("anchor_assistant_event_id", "last stable assistant message has no pi:message event id")
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
