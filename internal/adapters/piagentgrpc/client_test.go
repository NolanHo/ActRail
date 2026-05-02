package piagentgrpc

import (
	"context"
	"testing"

	piagentv1 "actrail/proto/pi/agent/v1"
	"google.golang.org/grpc"
)

func TestNormalizeTargetDefaultsToPiAgentSocket(t *testing.T) {
	if got := normalizeTarget(" "); got != DefaultTarget {
		t.Fatalf("normalizeTarget() = %q, want %q", got, DefaultTarget)
	}
	if got := SocketPathForTarget(" "); got != DefaultSocketPath {
		t.Fatalf("SocketPathForTarget() = %q, want %q", got, DefaultSocketPath)
	}
}

func TestTargetForSessionUsesPerSessionSocket(t *testing.T) {
	if got := TargetForSession("s_123"); got != "unix:///tmp/pi-agent/actrail/s_123.sock" {
		t.Fatalf("TargetForSession() = %q", got)
	}
}

func TestStateFromProtoCopiesSessionState(t *testing.T) {
	state := stateFromProto(&piagentv1.SessionState{
		SessionId:           " s_pi ",
		SessionFile:         ptr(" /tmp/session.jsonl "),
		SessionName:         ptr(" Draft "),
		ThinkingLevel:       "high",
		IsStreaming:         true,
		PendingMessageCount: 2,
		Model: &piagentv1.Model{
			Id:       " gpt-5.5 ",
			Provider: " openai ",
		},
	})
	if state.SessionID != "s_pi" || state.SessionFile != "/tmp/session.jsonl" || state.ModelID != "gpt-5.5" || state.Provider != "openai" || !state.IsStreaming || state.PendingMessageCount != 2 {
		t.Fatalf("stateFromProto() = %+v", state)
	}
}

func TestCommandsFromProtoCopiesSourceInfo(t *testing.T) {
	commands := commandsFromProto([]*piagentv1.SlashCommand{
		{
			Name:        " review ",
			Description: " Review current diff ",
			Source:      " prompt ",
			SourceInfo: &piagentv1.SourceInfo{
				Path:    " /tmp/review.md ",
				Source:  " project ",
				Scope:   " project ",
				Origin:  " top-level ",
				BaseDir: " /tmp ",
			},
		},
	})
	if len(commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(commands))
	}
	got := commands[0]
	if got.Name != "review" || got.Description != "Review current diff" || got.Source != "prompt" {
		t.Fatalf("command = %+v", got)
	}
	if got.SourceInfo.Path != "/tmp/review.md" || got.SourceInfo.BaseDir != "/tmp" {
		t.Fatalf("source info = %+v", got.SourceInfo)
	}
}

func TestEventFromProtoCopiesBoundaryAndPayload(t *testing.T) {
	event := eventFromProto(&piagentv1.Event{
		Type:     " session_boundary ",
		Sequence: 7,
		Payload:  &piagentv1.Payload{Json: []byte(`{"type":"x"}`)},
		SessionBoundary: &piagentv1.SessionBoundary{
			SessionId:   " s_remote ",
			SessionFile: ptr(" /tmp/pi.jsonl "),
			Reason:      " subscribe ",
		},
	})
	if event.Type != "session_boundary" || event.Sequence != 7 || string(event.PayloadJSON) != `{"type":"x"}` {
		t.Fatalf("eventFromProto() = %+v", event)
	}
	if event.SessionBoundary == nil || event.SessionBoundary.SessionID != "s_remote" || event.SessionBoundary.SessionFile != "/tmp/pi.jsonl" || event.SessionBoundary.Reason != "subscribe" {
		t.Fatalf("event boundary = %+v", event.SessionBoundary)
	}
}

func TestConnectUsesInjectedDialer(t *testing.T) {
	called := ""
	client := New("unix:///tmp/custom.sock", func(_ context.Context, target string) (*grpc.ClientConn, error) {
		called = target
		return nil, context.Canceled
	})
	if err := client.Connect(context.Background()); err != context.Canceled {
		t.Fatalf("Connect() error = %v, want context.Canceled", err)
	}
	if called != "unix:///tmp/custom.sock" {
		t.Fatalf("dial target = %q", called)
	}
}

func ptr[T any](v T) *T { return &v }
