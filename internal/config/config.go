package config

import (
	"fmt"
	"os"
	"path/filepath"
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
	defaultDataDir           = "./data"
	defaultSQLiteDir         = "sqlite"
	defaultSQLiteFilename    = "actrail.db"
	defaultRuntimeDir        = "runtime"
	defaultIODRuntimeDir     = "iod"
	defaultIODBindingsDir    = "iod-bindings"
	defaultReminderDelay     = 4 * time.Second
	defaultReminderTimeout   = 5 * time.Second
	defaultLarkCLIBin        = "/root/.local/bin/lark-cli"
)

type Config struct {
	Server        Server
	Protocol      Protocol
	Auth          Auth
	Features      Features
	Notifications Notifications
	Launch        Launch
	Storage       Storage
	DisabledUI    []string
	AllowedHosts  []string
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
	HeartbeatInterval time.Duration
	ResumeBuffer      int
}

type Auth struct {
	CookieName string
	Username   string
	Password   string
}

type AuthMode string

const (
	AuthModeLocal    AuthMode = "local"
	AuthModePassword AuthMode = "password"
)

type Features struct {
	Voice          bool
	Harness        bool
	Notifications  bool
	PIUI           bool
	WorkspaceRead  bool
	WorkspaceWrite bool
}

type Notifications struct {
	FeishuWebhookURL string
	LarkCLIBin       string
	LarkCLIAs        string
	LarkCLIChatID    string
	LarkCLIUserID    string
	ReminderDelay    time.Duration
	ReminderTimeout  time.Duration
}

type Launch struct {
	DefaultBackend       string
	AvailableBackends    []string
	Providers            []string
	Models               []string
	CodexDangerousBypass bool
}

type Storage struct {
	DataDir string
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
			HeartbeatInterval: envDuration("ACTRAIL_WS_HEARTBEAT_INTERVAL", defaultHeartbeatInterval),
			ResumeBuffer:      envInt("ACTRAIL_WS_RESUME_BUFFER", defaultResumeBuffer),
		},
		Auth: Auth{
			CookieName: envString("ACTRAIL_AUTH_COOKIE", defaultCookieName),
			Username:   envString("ACTRAIL_AUTH_USERNAME", ""),
			Password:   envString("ACTRAIL_AUTH_PASSWORD", ""),
		},
		Features: Features{
			Voice:          false,
			Harness:        false,
			Notifications:  true,
			PIUI:           true,
			WorkspaceRead:  true,
			WorkspaceWrite: false,
		},
		Notifications: Notifications{
			FeishuWebhookURL: envString("ACTRAIL_FEISHU_WEBHOOK_URL", ""),
			LarkCLIBin:       envString("ACTRAIL_LARK_CLI_BIN", defaultLarkCLIBin),
			LarkCLIAs:        envString("ACTRAIL_LARK_CLI_AS", "bot"),
			LarkCLIChatID:    envString("ACTRAIL_LARK_CHAT_ID", ""),
			LarkCLIUserID:    envString("ACTRAIL_LARK_USER_ID", ""),
			ReminderDelay:    envDuration("ACTRAIL_UNREAD_REMINDER_DELAY", envDuration("ACTRAIL_FEISHU_REMINDER_DELAY", defaultReminderDelay)),
			ReminderTimeout:  envDuration("ACTRAIL_UNREAD_REMINDER_TIMEOUT", envDuration("ACTRAIL_FEISHU_TIMEOUT", defaultReminderTimeout)),
		},
		Launch: Launch{
			DefaultBackend:       envString("ACTRAIL_DEFAULT_BACKEND", "codex"),
			AvailableBackends:    csvEnv("ACTRAIL_AVAILABLE_BACKENDS", []string{"codex"}),
			Providers:            csvEnv("ACTRAIL_AVAILABLE_PROVIDERS", nil),
			Models:               csvEnv("ACTRAIL_AVAILABLE_MODELS", nil),
			CodexDangerousBypass: envBool("ACTRAIL_CODEX_DANGEROUS_BYPASS", true),
		},
		Storage: Storage{
			DataDir: envString("ACTRAIL_DATA_DIR", defaultDataDir),
		},
		DisabledUI: []string{"voice", "harness"},
	}

	cfg.AllowedHosts = []string{cfg.Server.Host}
	return cfg
}

func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

func (c Config) SQLitePath() string {
	return c.Storage.SQLitePath()
}

func (c Config) Validate() error {
	return c.Auth.Validate()
}

func (c Config) HeartbeatIntervalMillis() int {
	return int(c.Protocol.HeartbeatInterval / time.Millisecond)
}

func (s Storage) EnsureDir() error {
	for _, path := range []string{s.DataDir, s.SQLiteDir(), s.RuntimeDir()} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (s Storage) SQLitePath() string {
	return joinPath(s.SQLiteDir(), defaultSQLiteFilename)
}

func (s Storage) SQLiteDir() string {
	return joinPath(s.DataDir, defaultSQLiteDir)
}

func (s Storage) RuntimeDir() string {
	return joinPath(s.DataDir, defaultRuntimeDir)
}

func (s Storage) IODRuntimeRoot() string {
	return joinPath(s.RuntimeDir(), defaultIODRuntimeDir)
}

func (s Storage) IODBindingsDir() string {
	return joinPath(s.RuntimeDir(), defaultIODBindingsDir)
}

func (a Auth) Mode() AuthMode {
	if strings.TrimSpace(a.Password) == "" {
		return AuthModeLocal
	}
	return AuthModePassword
}

func (a Auth) Validate() error {
	if a.Mode() != AuthModePassword {
		return nil
	}
	if strings.TrimSpace(a.Username) == "" {
		return fmt.Errorf("auth username required when password auth is enabled")
	}
	if strings.TrimSpace(a.Password) == "" {
		return fmt.Errorf("auth password required when password auth is enabled")
	}
	if strings.TrimSpace(a.CookieName) == "" {
		return fmt.Errorf("auth cookie name required when password auth is enabled")
	}
	return nil
}

func envString(key, fallback string) string {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
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

func joinPath(base, name string) string {
	if base == "." {
		return "." + string(os.PathSeparator) + name
	}
	joined := filepath.Join(base, name)
	prefix := "." + string(os.PathSeparator)
	if strings.HasPrefix(base, prefix) && !strings.HasPrefix(joined, prefix) && !filepath.IsAbs(joined) {
		return prefix + joined
	}
	return joined
}
