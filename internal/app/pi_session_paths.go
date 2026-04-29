package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"actrail/internal/domain/session"
)

func newPISessionSourcePath(cwd string, sessionID session.SessionID, now time.Time) (string, error) {
	if err := sessionID.Validate(); err != nil {
		return "", err
	}
	root := piHistoryBaseRoot()
	dir := filepath.Join(root, piSessionDirName(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir pi session history dir %q: %w", dir, err)
	}
	stamp := now.UTC().Format("2006-01-02T15-04-05-000Z")
	path := filepath.Join(dir, fmt.Sprintf("%s_actrail_%s.jsonl", stamp, sessionID.String()))
	return path, nil
}

func piHistoryBaseRoot() string {
	if piHome := strings.TrimSpace(os.Getenv("PI_HOME")); piHome != "" {
		return filepath.Join(piHome, "agent", "sessions")
	}
	return "/root/.pi/agent/sessions"
}
