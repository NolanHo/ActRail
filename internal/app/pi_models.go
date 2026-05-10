package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"actrail/internal/adapters/agent"
)

const piModelDiscoveryTimeout = 3 * time.Second

type piModelsConfig struct {
	Providers map[string]piProviderConfig `json:"providers"`
}

type piProviderConfig struct {
	BaseURL string             `json:"baseUrl"`
	APIKey  string             `json:"apiKey"`
	Models  []piModelConfigDef `json:"models"`
}

type piModelConfigDef struct {
	ID string `json:"id"`
}

type piModelCache struct {
	mu       sync.Mutex
	provider map[string][]string
	cachedAt time.Time
}

func piModelsJSONPath() string {
	if home := strings.TrimSpace(os.Getenv("PI_HOME")); home != "" {
		return filepath.Join(home, "agent", "models.json")
	}
	return filepath.Join("/root", ".pi", "agent", "models.json")
}

func readPIModelsConfig(path string) (piModelsConfig, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return piModelsConfig{}, false
	}
	var cfg piModelsConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return piModelsConfig{}, false
	}
	if len(cfg.Providers) == 0 {
		return piModelsConfig{}, false
	}
	return cfg, true
}

func piOpenAIModelsURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return ""
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed + "/models"
	}
	return trimmed + "/v1/models"
}

func discoverPIProviderModels(ctx context.Context, cfg piProviderConfig) []string {
	url := piOpenAIModelsURL(cfg.BaseURL)
	if url == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, piModelDiscoveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil
	}
	ids := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		ids = append(ids, item.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	return uniqueSortedStrings(ids)
}

func explicitPIProviderModels(cfg piProviderConfig) []string {
	ids := make([]string, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		ids = append(ids, model.ID)
	}
	return uniqueSortedStrings(ids)
}

func (c *piModelCache) snapshot() (map[string][]string, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneProviderModels(c.provider), c.cachedAt
}

func (c *piModelCache) store(providerModels map[string][]string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider == nil {
		c.provider = map[string][]string{}
	}
	for provider, models := range providerModels {
		cleaned := uniqueSortedStrings(models)
		if len(cleaned) == 0 {
			continue
		}
		c.provider[provider] = cleaned
	}
	if len(providerModels) > 0 {
		c.cachedAt = now
	}
}

func cloneProviderModels(input map[string][]string) map[string][]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string][]string, len(input))
	for provider, models := range input {
		out[provider] = append([]string(nil), models...)
	}
	return out
}

func uniqueSortedStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func chooseFirst(values []string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstProviderModel(providerModels map[string][]string, provider string) string {
	if provider == "" {
		return ""
	}
	models := providerModels[provider]
	if len(models) == 0 {
		return ""
	}
	return models[0]
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func preferredPIModel(providerModels map[string][]string, provider string) string {
	preferred := map[string]string{
		"openai":         "gpt-5.5",
		"openai-codex":   "gpt-5.5",
		"github-copilot": "gpt-4o",
		"anthropic":      "claude-opus-4-6",
	}
	model := preferred[provider]
	if model != "" && containsString(providerModels[provider], model) {
		return model
	}
	return firstProviderModel(providerModels, provider)
}

func (s *Stub) piLaunchBackendDefaults(ctx context.Context, refresh bool) LaunchBackendDefaults {
	providerModels := map[string][]string{}
	providers := []string{}
	cfg, ok := readPIModelsConfig(piModelsJSONPath())
	if ok {
		for provider, providerCfg := range cfg.Providers {
			trimmedProvider := strings.TrimSpace(provider)
			if trimmedProvider == "" {
				continue
			}
			providers = append(providers, trimmedProvider)
			explicit := explicitPIProviderModels(providerCfg)
			if len(explicit) > 0 {
				providerModels[trimmedProvider] = explicit
			}
		}
	}
	cached, cachedAt := s.piModels.snapshot()
	for provider, models := range cached {
		if len(providerModels[provider]) == 0 {
			providerModels[provider] = models
		}
	}
	if refresh && ok {
		discovered := map[string][]string{}
		for provider, providerCfg := range cfg.Providers {
			trimmedProvider := strings.TrimSpace(provider)
			if trimmedProvider == "" || len(providerModels[trimmedProvider]) > 0 && len(explicitPIProviderModels(providerCfg)) > 0 {
				continue
			}
			if models := discoverPIProviderModels(ctx, providerCfg); len(models) > 0 {
				discovered[trimmedProvider] = models
				providerModels[trimmedProvider] = models
			}
		}
		s.piModels.store(discovered, s.registry.now())
		if len(discovered) > 0 {
			cachedAt = s.registry.now()
		}
	}
	for _, provider := range s.cfg.Launch.Providers {
		providers = append(providers, provider)
	}
	for provider := range providerModels {
		providers = append(providers, provider)
	}
	providers = uniqueSortedStrings(providers)
	defaultProvider := chooseFirst(s.cfg.Launch.Providers)
	if defaultProvider == "" && containsString(providers, "openai") {
		defaultProvider = "openai"
	}
	if defaultProvider == "" {
		defaultProvider = chooseFirst(providers)
	}
	defaults := LaunchBackendDefaults{
		ProviderChoice:   defaultProvider,
		ProviderChoices:  append([]string(nil), providers...),
		ModelProviders:   append([]string(nil), providers...),
		ProviderModels:   cloneProviderModels(providerModels),
		ReasoningEffort:  "high",
		ReasoningEfforts: []string{"off", "minimal", "low", "medium", "high", "xhigh"},
	}
	if model := preferredPIModel(providerModels, defaultProvider); model != "" {
		defaults.Model = model
	} else if model := chooseFirst(s.cfg.Launch.Models); model != "" {
		defaults.Model = model
	}
	if !cachedAt.IsZero() {
		defaults.ModelsCachedAt = cachedAt.Unix()
	}
	return defaults
}

func (s *Stub) newSessionDefaults(ctx context.Context, req BootstrapRequest) NewSessionDefaults {
	codex := s.codexLaunchBackendDefaults()
	backends := map[string]LaunchBackendDefaults{}
	addBackend := func(backend string) {
		switch strings.TrimSpace(backend) {
		case "codex":
			if _, ok := backends["codex"]; !ok {
				backends["codex"] = codex
			}
		case "pi":
			if _, ok := backends["pi"]; !ok {
				backends["pi"] = s.piLaunchBackendDefaults(ctx, req.RefreshPIModels)
			}
		}
	}
	for _, backend := range s.cfg.Launch.AvailableBackends {
		addBackend(backend)
	}
	if len(backends) == 0 {
		addBackend("pi")
		addBackend("codex")
	}
	addBackend(s.cfg.Launch.DefaultBackend)
	capabilities := map[string]BackendCapabilitySnapshot{}
	for backend := range backends {
		capabilities[backend] = backendCapabilitySnapshot(backend)
	}
	return NewSessionDefaults{
		DefaultBackend:      s.cfg.Launch.DefaultBackend,
		Backends:            backends,
		BackendCapabilities: capabilities,
		PIAgentGRPCDefault:  true,
	}
}

func (s *Stub) codexLaunchBackendDefaults() LaunchBackendDefaults {
	cfg := readCodexConfig()
	providerModels := cloneProviderModels(cfg.ProviderModels)
	if cfg.ModelProvider != "" && cfg.Model != "" {
		if providerModels == nil {
			providerModels = map[string][]string{}
		}
		providerModels[cfg.ModelProvider] = uniqueSortedStrings(append(providerModels[cfg.ModelProvider], cfg.Model))
	}
	providers := append(append([]string{}, s.cfg.Launch.Providers...), cfg.Providers...)
	models := append([]string{}, s.cfg.Launch.Models...)
	if cfg.Model != "" {
		models = append(models, cfg.Model)
	}
	for provider, providerModelChoices := range providerModels {
		providers = append(providers, provider)
		models = append(models, providerModelChoices...)
	}
	providers = uniqueSortedStrings(providers)
	models = uniqueSortedStrings(models)
	defaultProvider := chooseFirst(s.cfg.Launch.Providers)
	if defaultProvider == "" {
		defaultProvider = cfg.ModelProvider
	}
	if defaultProvider == "" {
		defaultProvider = chooseFirst(providers)
	}
	defaultModel := chooseFirst(s.cfg.Launch.Models)
	if defaultModel == "" {
		defaultModel = cfg.Model
	}
	if defaultModel == "" {
		defaultModel = firstProviderModel(providerModels, defaultProvider)
	}
	if defaultModel == "" {
		defaultModel = chooseFirst(models)
	}
	return LaunchBackendDefaults{
		ProviderChoice:      defaultProvider,
		ProviderChoices:     providers,
		Model:               defaultModel,
		Models:              models,
		ProviderModels:      providerModels,
		ModelProvider:       defaultProvider,
		ModelProviders:      providers,
		PreferredAuthMethod: cfg.PreferredAuthMethod,
		ReasoningEffort:     agent.CodexDefaultReasoningEffort(),
	}
}

func backendCapabilitySnapshot(backend string) BackendCapabilitySnapshot {
	switch strings.TrimSpace(backend) {
	case "pi":
		return BackendCapabilitySnapshot{
			LaunchProvider:        true,
			LaunchModel:           true,
			LaunchReasoningEffort: true,
			RuntimeStreaming:      true,
			RuntimeToolTrace:      true,
			RuntimeReasoningTrace: true,
			RuntimeContextUsage:   true,
			RuntimeUIRequests:     true,
			RuntimeInterrupt:      true,
			RuntimeProbe:          true,
			IODStdio:              true,
			IODUnix:               false,
			GRPC:                  true,
			Supervisor:            true,
			ResumeHistory:         true,
		}
	case "codex":
		return BackendCapabilitySnapshot{
			LaunchProvider:        true,
			LaunchModel:           true,
			LaunchReasoningEffort: false,
			RuntimeStreaming:      true,
			RuntimeToolTrace:      true,
			RuntimeReasoningTrace: true,
			RuntimeContextUsage:   true,
			RuntimeUIRequests:     false,
			RuntimeInterrupt:      true,
			RuntimeProbe:          true,
			IODStdio:              false,
			IODUnix:               true,
			GRPC:                  false,
			Supervisor:            false,
			ResumeHistory:         true,
		}
	default:
		return BackendCapabilitySnapshot{}
	}
}
