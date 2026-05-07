package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"actrail/internal/config"
)

func TestBootstrapReadsCodexLaunchDefaultsFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
model_provider = "crs"
model_reasoning_effort = "xhigh"
preferred_auth_method = "apikey"
model = "gpt-5.5"

[model_providers.crs]
name = "OpenAI"
base_url = "https://pi-api.macaron.xin"
models = ["gpt-5.4"]
`
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	stub := NewStubForTest(config.Load(), func() time.Time { return time.Unix(1770000000, 0) }, RuntimeConfig{})
	bootstrap := stub.Bootstrap(context.Background(), BootstrapRequest{})
	if bootstrap.NewSessionDefaults.DefaultBackend != "codex" {
		t.Fatalf("new session default backend = %q, want codex", bootstrap.NewSessionDefaults.DefaultBackend)
	}
	codex := bootstrap.NewSessionDefaults.Backends["codex"]

	if codex.ProviderChoice != "crs" || codex.ModelProvider != "crs" {
		t.Fatalf("codex provider defaults = %q/%q, want crs/crs", codex.ProviderChoice, codex.ModelProvider)
	}
	if codex.Model != "gpt-5.5" {
		t.Fatalf("codex model = %q, want gpt-5.5", codex.Model)
	}
	if !containsString(codex.ProviderChoices, "crs") || !containsString(codex.ModelProviders, "crs") {
		t.Fatalf("codex provider choices = %#v model providers = %#v, want crs", codex.ProviderChoices, codex.ModelProviders)
	}
	if !containsString(codex.Models, "gpt-5.5") || !containsString(codex.Models, "gpt-5.4") {
		t.Fatalf("codex models = %#v, want gpt-5.5 and gpt-5.4", codex.Models)
	}
	if got := codex.ProviderModels["crs"]; len(got) != 2 || got[0] != "gpt-5.4" || got[1] != "gpt-5.5" {
		t.Fatalf("codex provider models = %#v, want crs -> gpt-5.4,gpt-5.5", codex.ProviderModels)
	}
	if codex.PreferredAuthMethod != "apikey" || codex.ReasoningEffort != "high" {
		t.Fatalf("codex auth/reasoning = %q/%q, want apikey/high", codex.PreferredAuthMethod, codex.ReasoningEffort)
	}
}

func TestBootstrapRefreshPIModelsDiscoversAndCachesProviderModels(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.4"},{"id":"gpt-5.5"},{"id":"gpt-5.4-mini"}]}`))
	}))
	defer server.Close()

	piHome := t.TempDir()
	t.Setenv("PI_HOME", piHome)
	modelsDir := filepath.Join(piHome, "agent")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"providers":{"openai":{"baseUrl":%q,"apiKey":"test-key","api":"openai-responses"}}}`, server.URL)
	if err := os.WriteFile(filepath.Join(modelsDir, "models.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	stub := NewStubForTest(config.Load(), func() time.Time { return time.Unix(1770000000, 0) }, RuntimeConfig{})
	initial := stub.Bootstrap(context.Background(), BootstrapRequest{})
	if models := initial.NewSessionDefaults.Backends["pi"].ProviderModels["openai"]; len(models) != 0 {
		t.Fatalf("initial provider models = %#v, want none before refresh", models)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0 before refresh", requests)
	}

	refreshed := stub.Bootstrap(context.Background(), BootstrapRequest{RefreshPIModels: true})
	models := refreshed.NewSessionDefaults.Backends["pi"].ProviderModels["openai"]
	if len(models) != 3 || models[0] != "gpt-5.4" || models[1] != "gpt-5.4-mini" || models[2] != "gpt-5.5" {
		t.Fatalf("refreshed provider models = %#v", models)
	}
	if refreshed.NewSessionDefaults.Backends["pi"].Model != "gpt-5.5" {
		t.Fatalf("default model = %q", refreshed.NewSessionDefaults.Backends["pi"].Model)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1 after refresh", requests)
	}

	cached := stub.Bootstrap(context.Background(), BootstrapRequest{})
	models = cached.NewSessionDefaults.Backends["pi"].ProviderModels["openai"]
	if len(models) != 3 || requests != 1 {
		t.Fatalf("cached models = %#v requests=%d", models, requests)
	}
}

func TestBootstrapIncludesBackendCapabilityMatrix(t *testing.T) {
	stub := NewStubForTest(config.Load(), func() time.Time { return time.Unix(1770000000, 0) }, RuntimeConfig{})
	bootstrap := stub.Bootstrap(context.Background(), BootstrapRequest{})

	codex := bootstrap.NewSessionDefaults.BackendCapabilities["codex"]
	if !codex.LaunchProvider || !codex.LaunchModel || codex.LaunchReasoningEffort || !codex.RuntimeToolTrace || !codex.RuntimeReasoningTrace || !codex.RuntimeContextUsage || !codex.RuntimeProbe || !codex.IODUnix || codex.IODStdio || codex.GRPC {
		t.Fatalf("codex capability matrix = %+v", codex)
	}
	pi := bootstrap.NewSessionDefaults.BackendCapabilities["pi"]
	if !pi.LaunchProvider || !pi.LaunchModel || !pi.LaunchReasoningEffort || !pi.RuntimeToolTrace || !pi.RuntimeUIRequests || !pi.RuntimeProbe || !pi.IODStdio || !pi.GRPC || pi.IODUnix {
		t.Fatalf("pi capability matrix = %+v", pi)
	}
}
