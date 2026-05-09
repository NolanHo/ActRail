package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ACTRAIL_HOST", "")
	t.Setenv("ACTRAIL_PORT", "")
	t.Setenv("ACTRAIL_AVAILABLE_BACKENDS", "")
	t.Setenv("ACTRAIL_DATA_DIR", "")

	cfg := Load()

	if cfg.Server.Host != "127.0.0.1" {
		t.Fatalf("expected default host 127.0.0.1, got %q", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Fatalf("expected default port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Protocol.Version != 1 {
		t.Fatalf("expected protocol version 1, got %d", cfg.Protocol.Version)
	}
	if got, want := cfg.HeartbeatIntervalMillis(), 15000; got != want {
		t.Fatalf("expected heartbeat interval %dms, got %dms", want, got)
	}
	if cfg.Launch.DefaultBackend != "codex" {
		t.Fatalf("expected default backend codex, got %q", cfg.Launch.DefaultBackend)
	}
	if !reflect.DeepEqual(cfg.Launch.AvailableBackends, []string{"pi", "codex"}) {
		t.Fatalf("unexpected default backends: %#v", cfg.Launch.AvailableBackends)
	}
	if !cfg.Launch.CodexDangerousBypass {
		t.Fatal("expected codex dangerous bypass to default to true")
	}
	if cfg.Storage.DataDir != "./data" {
		t.Fatalf("expected default data dir ./data, got %q", cfg.Storage.DataDir)
	}
	if cfg.Storage.SQLiteDir() != "."+string(os.PathSeparator)+filepath.Join("data", "sqlite") {
		t.Fatalf("expected default sqlite dir under data dir, got %q", cfg.Storage.SQLiteDir())
	}
	if cfg.Storage.IODRuntimeRoot() != "."+string(os.PathSeparator)+filepath.Join("data", "runtime", "iod") {
		t.Fatalf("expected default iod runtime root under data dir, got %q", cfg.Storage.IODRuntimeRoot())
	}
	if cfg.Storage.IODBindingsDir() != "."+string(os.PathSeparator)+filepath.Join("data", "runtime", "iod-bindings") {
		t.Fatalf("expected default iod bindings dir under runtime dir, got %q", cfg.Storage.IODBindingsDir())
	}
	if cfg.SQLitePath() != "."+string(os.PathSeparator)+filepath.Join("data", "sqlite", "actrail.db") {
		t.Fatalf("expected default sqlite path under data dir, got %q", cfg.SQLitePath())
	}
	if cfg.Auth.Password != "" {
		t.Fatalf("expected empty auth password by default, got %q", cfg.Auth.Password)
	}
	if cfg.Auth.Username != "" {
		t.Fatalf("expected empty auth username by default, got %q", cfg.Auth.Username)
	}
	if cfg.Auth.Mode() != AuthModeLocal {
		t.Fatalf("expected default auth mode %q, got %q", AuthModeLocal, cfg.Auth.Mode())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("ACTRAIL_HOST", "0.0.0.0")
	t.Setenv("ACTRAIL_PORT", "9090")
	t.Setenv("ACTRAIL_WS_HEARTBEAT_INTERVAL", "20s")
	t.Setenv("ACTRAIL_DEFAULT_BACKEND", "pi")
	t.Setenv("ACTRAIL_AVAILABLE_BACKENDS", "pi")
	t.Setenv("ACTRAIL_AVAILABLE_PROVIDERS", "openrouter,anthropic")
	t.Setenv("ACTRAIL_AVAILABLE_MODELS", "claude-sonnet,gemini-2.5-pro")
	t.Setenv("ACTRAIL_CODEX_DANGEROUS_BYPASS", "false")
	t.Setenv("ACTRAIL_AUTH_USERNAME", "nolan")
	t.Setenv("ACTRAIL_AUTH_PASSWORD", "secret")
	t.Setenv("ACTRAIL_DATA_DIR", "/tmp/actrail-data")

	cfg := Load()

	if cfg.Server.Host != "0.0.0.0" {
		t.Fatalf("expected host override, got %q", cfg.Server.Host)
	}
	if cfg.Server.Port != 9090 {
		t.Fatalf("expected port override, got %d", cfg.Server.Port)
	}
	if cfg.Protocol.HeartbeatInterval != 20*time.Second {
		t.Fatalf("expected heartbeat override, got %s", cfg.Protocol.HeartbeatInterval)
	}
	if cfg.Launch.DefaultBackend != "pi" {
		t.Fatalf("expected default backend override pi, got %q", cfg.Launch.DefaultBackend)
	}
	if !reflect.DeepEqual(cfg.Launch.AvailableBackends, []string{"pi"}) {
		t.Fatalf("unexpected backend override: %#v", cfg.Launch.AvailableBackends)
	}
	if !reflect.DeepEqual(cfg.Launch.Providers, []string{"openrouter", "anthropic"}) {
		t.Fatalf("unexpected provider override: %#v", cfg.Launch.Providers)
	}
	if !reflect.DeepEqual(cfg.Launch.Models, []string{"claude-sonnet", "gemini-2.5-pro"}) {
		t.Fatalf("unexpected model override: %#v", cfg.Launch.Models)
	}
	if cfg.Launch.CodexDangerousBypass {
		t.Fatal("expected codex dangerous bypass override to disable bypass")
	}
	if cfg.Storage.DataDir != "/tmp/actrail-data" {
		t.Fatalf("expected data dir override, got %q", cfg.Storage.DataDir)
	}
	if cfg.Storage.SQLiteDir() != filepath.Join("/tmp/actrail-data", "sqlite") {
		t.Fatalf("expected sqlite dir override, got %q", cfg.Storage.SQLiteDir())
	}
	if cfg.Storage.RuntimeDir() != filepath.Join("/tmp/actrail-data", "runtime") {
		t.Fatalf("expected runtime dir override, got %q", cfg.Storage.RuntimeDir())
	}
	if cfg.Storage.IODRuntimeRoot() != filepath.Join("/tmp/actrail-data", "runtime", "iod") {
		t.Fatalf("expected iod runtime root override, got %q", cfg.Storage.IODRuntimeRoot())
	}
	if cfg.Storage.IODBindingsDir() != filepath.Join("/tmp/actrail-data", "runtime", "iod-bindings") {
		t.Fatalf("expected iod bindings dir override, got %q", cfg.Storage.IODBindingsDir())
	}
	if cfg.SQLitePath() != filepath.Join("/tmp/actrail-data", "sqlite", "actrail.db") {
		t.Fatalf("expected sqlite path override, got %q", cfg.SQLitePath())
	}
	if cfg.Auth.Password != "secret" {
		t.Fatalf("expected auth password override, got %q", cfg.Auth.Password)
	}
	if cfg.Auth.Username != "nolan" {
		t.Fatalf("expected auth username override, got %q", cfg.Auth.Username)
	}
	if cfg.Auth.Mode() != AuthModePassword {
		t.Fatalf("expected auth mode %q, got %q", AuthModePassword, cfg.Auth.Mode())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestStorageEnsureDirCreatesNestedDataDir(t *testing.T) {
	root := t.TempDir()
	storage := Storage{DataDir: filepath.Join(root, "runtime", "data")}

	if err := storage.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir() error = %v", err)
	}
	for _, path := range []string{storage.DataDir, storage.SQLiteDir(), storage.RuntimeDir()} {
		if info, err := os.Stat(path); err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		} else if !info.IsDir() {
			t.Fatalf("expected %q to be a directory", path)
		}
	}
}

func TestAuthValidateRejectsEmptyCookieNameInPasswordMode(t *testing.T) {
	cfg := Load()
	cfg.Auth.Username = "nolan"
	cfg.Auth.Password = "secret"
	cfg.Auth.CookieName = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil for password auth without cookie name")
	}
}

func TestAuthModeTrimsWhitespacePassword(t *testing.T) {
	cfg := Load()
	cfg.Auth.Password = "   "

	if cfg.Auth.Mode() != AuthModeLocal {
		t.Fatalf("expected whitespace password to select %q mode, got %q", AuthModeLocal, cfg.Auth.Mode())
	}
}

func TestAuthValidateRejectsEmptyUsernameInPasswordMode(t *testing.T) {
	cfg := Load()
	cfg.Auth.Password = "secret"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil for password auth without username")
	}
}
