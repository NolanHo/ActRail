package app

import (
	"context"
	"strings"

	"actrail/internal/domain/session"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	codexReducerSourceLiveUDS       = "live_uds"
	codexReducerSourceSessionFile   = "session_file"
	codexReducerSourceCommandLedger = "command_ledger"
	codexReducerSourceRuntimeHealth = "runtime_health"
	codexReducerSourceReattach      = "reattach"
)

func (s *Stub) recordCodexReducerEvent(sessionID session.SessionID, source, action string, attrs ...attribute.KeyValue) {
	if s == nil {
		return
	}
	source = strings.TrimSpace(source)
	action = strings.TrimSpace(action)
	if source == "" || action == "" {
		return
	}
	eventAttrs := []attribute.KeyValue{
		attribute.String("session.id", sessionID.String()),
		attribute.String("codex.reducer.source", source),
		attribute.String("codex.reducer.action", action),
	}
	eventAttrs = append(eventAttrs, attrs...)
	ctx, eventSpan := otel.Tracer("actrail/app").Start(context.Background(), "app.codexReducer.event")
	_ = ctx
	eventSpan.SetAttributes(eventAttrs...)
	eventSpan.AddEvent("codex.reducer.transition", trace.WithAttributes(eventAttrs...))
	eventSpan.End()
}

func codexReducerBool(value bool) attribute.KeyValue {
	return attribute.Bool("codex.reducer.changed", value)
}
