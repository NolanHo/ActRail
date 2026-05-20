package app

import (
	"context"
	"time"
)

type sessionCommandStore interface {
	UpdateCodexSessionCommandState(ctx context.Context, commandID, state, runtimeID, lastError string, updatedAt time.Time) (bool, error)
}
