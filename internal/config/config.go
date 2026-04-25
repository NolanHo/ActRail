package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHost              = "127.0.0.1"
	defaultPort              = 8080
	defaultProtocolVersion   = 1
	defaultHeartbeatInterval = 15 * time.Second
	defaultResumeBuffer      = 500
	defaultReadTimeout       = 15 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
	defaultCookieName        = "actrail_auth"
	defaultWSPath            = "/api/ws"
)

type Config struct {
	Server       Server
	Protocol     Protocol
	Auth         Auth
	Features     Features
	Launch       Launch
	DisabledUI   []string
	AllowedHosts []string
}

type Server struct {
	Host            string
	Port            int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

type Protocol struct {
	Version           int
	WebSocketPath     string
	HeartbeatInterval time.Duration
	ResumeBuffer      int
}

type Auth struct {
	CookieName string
}

type Features struct {
	WebSocketRealtime bool
	Voice             bool
	Harness           bool
	Notifications     bool
	PIUI              bool
	WorkspaceRead     bool
	WorkspaceWrite    bool
}

type Launch struct {
	DefaultBackend    string
	AvailableBackends []string
	Providers         []string
	Models            []string
}

func Load() Config {
	cfg := Config{
		Server: Server{
			Host:            envString("ACTRAIL_HOST", defaultHost),
			Port:            envInt("ACTRAIL_PORT", defaultPort),
			ReadTimeout:     envDuration("ACTRAIL_READ_TIMEOUT", defaultReadTimeout),
			WriteTimeout:    envDuration("ACTRAIL_WRITE_TIMEOUT", defaultWriteTimeout),
			IdleTimeout:     envDuration("ACTRAIL_IDLE_TIMEOUT", defaultIdleTimeout),
			ShutdownTimeout: envDuration("ACTRAIL_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		},
		Protocol: Protocol{
			Version:           envInt("ACTRAIL_PROTOCOL_VERSION", defaultProtocolVersion),
			WebSocketPath:     envString("ACTRAIL_WS_PATH", defaultWSPath),
			HeartbeatInterval: envDuration("ACTRAIL_WS_HEARTBEAT_INTERVAL", defaultHeartbeatInterval),
			ResumeBuffer:      envInt("ACTRAIL_WS_RESUME_BUFFER", defaultResumeBuffer),
		},
		Auth: Auth{
			CookieName: envString("ACTRAIL_AUTH_COOKIE", defaultCookieName),
		},
		Features: Features{
			WebSocketRealtime: true,
			Voice:             false,
			Harness:           false,
			Notifications:     false,
			PIUI:              true,
			WorkspaceRead:     true,
			WorkspaceWrite:    false,
		},
		Launch: Launch{
			DefaultBackend:    envString("ACTRAIL_DEFAULT_BACKEND", "pi"),
			AvailableBackends: csvEnv("ACTRAIL_AVAILABLE_BACKENDS", []string{"pi", "codex"}),
			Providers:         csvEnv("ACTRAIL_AVAILABLE_PROVIDERS", nil),
			Models:            csvEnv("ACTRAIL_AVAILABLE_MODELS", nil),
		},
		DisabledUI: []string{"voice", "harness", "notifications"},
	}

	cfg.AllowedHosts = []string{cfg.Server.Host}
	return cfg
}

func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

func (c Config) HeartbeatIntervalMillis() int {
	return int(c.Protocol.HeartbeatInterval / time.Millisecond)
}

func envString(key, fallback string) string {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func envInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func csvEnv(key string, fallback []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return append([]string(nil), fallback...)
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return append([]string(nil), fallback...)
	}
	return out
}
