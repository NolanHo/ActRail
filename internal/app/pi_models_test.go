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
