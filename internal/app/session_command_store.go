package app

import (
	"context"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
)

type sessionCommandStore interface {
	ListOpenCodexSessionCommands(ctx context.Context, sessionID string) ([]sqlitestore.CodexSessionCommandRow, error)
	UpdateCodexSessionCommandState(ctx context.Context, commandID, state, runtimeID, lastError string, updatedAt time.Time) (bool, error)
}
