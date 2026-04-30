package app

import (
	"context"
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
