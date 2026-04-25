package config

import (
	"reflect"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ACTRAIL_HOST", "")
	t.Setenv("ACTRAIL_PORT", "")
	t.Setenv("ACTRAIL_AVAILABLE_BACKENDS", "")

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
	if !reflect.DeepEqual(cfg.Launch.AvailableBackends, []string{"pi", "codex"}) {
		t.Fatalf("unexpected default backends: %#v", cfg.Launch.AvailableBackends)
	}
	if cfg.Auth.Password != "" {
		t.Fatalf("expected empty auth password by default, got %q", cfg.Auth.Password)
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
	t.Setenv("ACTRAIL_AVAILABLE_BACKENDS", "pi")
	t.Setenv("ACTRAIL_AVAILABLE_PROVIDERS", "openrouter,anthropic")
	t.Setenv("ACTRAIL_AVAILABLE_MODELS", "claude-sonnet,gemini-2.5-pro")
	t.Setenv("ACTRAIL_AUTH_PASSWORD", "secret")

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
	if !reflect.DeepEqual(cfg.Launch.AvailableBackends, []string{"pi"}) {
		t.Fatalf("unexpected backend override: %#v", cfg.Launch.AvailableBackends)
	}
	if !reflect.DeepEqual(cfg.Launch.Providers, []string{"openrouter", "anthropic"}) {
		t.Fatalf("unexpected provider override: %#v", cfg.Launch.Providers)
	}
	if !reflect.DeepEqual(cfg.Launch.Models, []string{"claude-sonnet", "gemini-2.5-pro"}) {
		t.Fatalf("unexpected model override: %#v", cfg.Launch.Models)
	}
	if cfg.Auth.Password != "secret" {
		t.Fatalf("expected auth password override, got %q", cfg.Auth.Password)
	}
	if cfg.Auth.Mode() != AuthModePassword {
		t.Fatalf("expected auth mode %q, got %q", AuthModePassword, cfg.Auth.Mode())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestAuthValidateRejectsEmptyCookieNameInPasswordMode(t *testing.T) {
	cfg := Load()
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
