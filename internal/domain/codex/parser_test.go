package codex

import (
	"testing"

	runtimeevent "actrail/internal/domain/runtimeevent"
)

func TestDecodeAppServerLineThreadStatusAndActiveTurn(t *testing.T) {
	projection, ok := DecodeAppServerLine([]byte(`{"id":"thread-read-3","result":{"thread":{"id":"thread-codex-read-1","status":{"type":"active","activeFlags":[]},"turns":[{"id":"turn-codex-read-1","status":"inProgress"}]}}}`))
	if !ok {
		t.Fatal("DecodeAppServerLine() ok = false")
	}
	if projection.ThreadID != "thread-codex-read-1" || projection.TurnID != "turn-codex-read-1" {
		t.Fatalf("projection ids = (%q, %q), want thread and active turn", projection.ThreadID, projection.TurnID)
	}
	if projection.Busy == nil || !*projection.Busy {
		t.Fatalf("projection.Busy = %v, want true", projection.Busy)
	}

	projection, ok = DecodeAppServerLine([]byte(`{"method":"thread/status/changed","params":{"threadId":"thread-codex-read-1","status":{"type":"idle"}}}`))
	if !ok {
		t.Fatal("DecodeAppServerLine(idle) ok = false")
	}
	if projection.Busy == nil || *projection.Busy || !projection.ClearTurn {
		t.Fatalf("idle projection busy/clear = (%v, %v), want false/true", projection.Busy, projection.ClearTurn)
	}
}

func TestDecodeAppServerLineAlreadyInitializedError(t *testing.T) {
	projection, ok := DecodeAppServerLine([]byte(`{"error":{"code":-32600,"message":"Already initialized"},"id":"initialize-1"}`))
	if !ok {
		t.Fatal("DecodeAppServerLine(already initialized) ok = false")
	}
	if !projection.Initialized {
		t.Fatalf("projection.Initialized = false, want true")
	}
}

func TestDecodeAppServerLineNotInitializedError(t *testing.T) {
	projection, ok := DecodeAppServerLine([]byte(`{"error":{"code":-32600,"message":"Not initialized"},"id":"turn-start-1"}`))
	if !ok {
		t.Fatal("DecodeAppServerLine(not initialized) ok = false")
	}
	if !projection.Desynced {
		t.Fatalf("projection.Desynced = false, want true")
	}
	if projection.Initialized {
		t.Fatalf("projection.Initialized = true, want false")
	}
}

func TestDecodeAppServerLineToolReasoningUsageAndError(t *testing.T) {
	toolProjection, ok := DecodeAppServerLine([]byte(`{"method":"item/completed","params":{"threadId":"thread-codex-2","turnId":"turn-codex-2","item":{"type":"commandExecution","id":"cmd-1","command":"go test ./...","cwd":"/root/code/ActRail","status":"completed","aggregatedOutput":"ok\n","exitCode":0,"durationMs":1200}}}`))
	if !ok {
		t.Fatal("DecodeAppServerLine(tool) ok = false")
	}
	if len(toolProjection.Events) != 1 || toolProjection.Events[0].Kind != runtimeevent.EventKindTool {
		t.Fatalf("tool events = %+v, want one tool event", toolProjection.Events)
	}
	if toolProjection.Events[0].ThreadID != "thread-codex-2" || toolProjection.Events[0].TurnID != "turn-codex-2" {
		t.Fatalf("tool event ids = (%q, %q), want thread and turn ids", toolProjection.Events[0].ThreadID, toolProjection.Events[0].TurnID)
	}
	tool := toolProjection.Events[0].Tool
	if tool == nil || tool.Name != "commandExecution" || tool.Text != "ok" || !tool.Result || tool.IsError {
		t.Fatalf("tool event = %+v, want completed command output without error", tool)
	}
	if tool.Arguments["command"] != "go test ./..." || tool.Arguments["durationMs"] == nil {
		t.Fatalf("tool arguments = %+v, want command and duration", tool.Arguments)
	}

	reasoningProjection, ok := DecodeAppServerLine([]byte(`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"thread-codex-2","turnId":"turn-codex-2","itemId":"reason-1","delta":"Inspecting runtime schema","summaryIndex":0}}`))
	if !ok {
		t.Fatal("DecodeAppServerLine(reasoning) ok = false")
	}
	if len(reasoningProjection.Events) != 1 || reasoningProjection.Events[0].Message == nil || reasoningProjection.Events[0].Message.StopReason != "reasoning" {
		t.Fatalf("reasoning event = %+v, want reasoning narration", reasoningProjection.Events)
	}
	if reasoningProjection.Events[0].ThreadID != "thread-codex-2" {
		t.Fatalf("reasoning thread id = %q, want thread-codex-2", reasoningProjection.Events[0].ThreadID)
	}

	usageProjection, ok := DecodeAppServerLine([]byte(`{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-codex-2","turnId":"turn-codex-2","tokenUsage":{"total":{"totalTokens":2048,"inputTokens":1024,"cachedInputTokens":0,"outputTokens":512,"reasoningOutputTokens":512},"modelContextWindow":8192}}}`))
	if !ok {
		t.Fatal("DecodeAppServerLine(usage) ok = false")
	}
	if usageProjection.ThreadID != "thread-codex-2" {
		t.Fatalf("usage thread id = %q, want thread-codex-2", usageProjection.ThreadID)
	}
	if usageProjection.Usage == nil || usageProjection.Usage.UsedTokens == nil || *usageProjection.Usage.UsedTokens != 2048 || usageProjection.Usage.TotalTokens == nil || *usageProjection.Usage.TotalTokens != 8192 || usageProjection.Usage.PercentUsed == nil || *usageProjection.Usage.PercentUsed != 25 {
		t.Fatalf("usage = %+v, want 2048/8192/25", usageProjection.Usage)
	}

	errorProjection, ok := DecodeAppServerLine([]byte(`{"method":"warning","params":{"message":"Codex warning surfaced","threadId":"thread-codex-2","turnId":"turn-codex-2"}}`))
	if !ok {
		t.Fatal("DecodeAppServerLine(error) ok = false")
	}
	if len(errorProjection.Events) != 1 || errorProjection.Events[0].Error == nil || errorProjection.Events[0].Error.Message != "Codex warning surfaced" {
		t.Fatalf("error event = %+v, want diagnostic error", errorProjection.Events)
	}
	if errorProjection.Events[0].ThreadID != "thread-codex-2" {
		t.Fatalf("error thread id = %q, want thread-codex-2", errorProjection.Events[0].ThreadID)
	}
}

func TestDecodeAppServerLineAssistantCompletedDoesNotEndTurn(t *testing.T) {
	projection, ok := DecodeAppServerLine([]byte(`{"method":"item/completed","params":{"item":{"type":"agentMessage","id":"agent-final-1","threadId":"thread-codex-final","turnId":"turn-codex-final","text":"final answer"}}}`))
	if !ok {
		t.Fatal("DecodeAppServerLine(assistant completed) ok = false")
	}
	if projection.ThreadID != "thread-codex-final" || projection.TurnID != "turn-codex-final" {
		t.Fatalf("projection ids = (%q, %q), want ids from completed item", projection.ThreadID, projection.TurnID)
	}
	if projection.ClearTurn || projection.Busy != nil {
		t.Fatalf("projection terminal flags = clear:%v busy:%v, want no terminal state before turn/completed", projection.ClearTurn, projection.Busy)
	}
	if !projection.ProbeTurn {
		t.Fatal("projection.ProbeTurn = false, want completion probe after assistant final")
	}
	if len(projection.Events) != 1 {
		t.Fatalf("len(projection.Events) = %d, want 1", len(projection.Events))
	}
	event := projection.Events[0]
	if event.ThreadID != "thread-codex-final" || event.TurnID != "turn-codex-final" {
		t.Fatalf("event ids = (%q, %q), want ids from completed item", event.ThreadID, event.TurnID)
	}
	if event.Message == nil || event.Message.Role != runtimeevent.MessageRoleAssistant || !event.Message.CommitLike || event.Message.Text != "final answer" {
		t.Fatalf("assistant completed event = %+v, want committed assistant final", event)
	}
}

func TestDecodeAppServerLineTurnTimingAndCompletion(t *testing.T) {
	started, ok := DecodeAppServerLine([]byte(`{"method":"turn/started","params":{"threadId":"thread-codex-2","turn":{"id":"turn-codex-2","status":"inProgress","startedAt":1760000001000,"error":null}}}`))
	if !ok {
		t.Fatal("DecodeAppServerLine(turn started) ok = false")
	}
	if started.Timing == nil || started.Timing.StartedTS != 1760000001 {
		t.Fatalf("started timing = %+v, want normalized started timestamp", started.Timing)
	}
	if started.ThreadID != "thread-codex-2" || started.Events[0].ThreadID != "thread-codex-2" {
		t.Fatalf("started thread ids = (%q, %q), want thread-codex-2", started.ThreadID, started.Events[0].ThreadID)
	}
	if started.Busy == nil || !*started.Busy || len(started.Events) != 1 || started.Events[0].Boundary == nil || started.Events[0].Boundary.Kind != runtimeevent.BoundaryKindTurnStarted {
		t.Fatalf("started projection = %+v, want busy turn boundary", started)
	}

	completed, ok := DecodeAppServerLine([]byte(`{"method":"turn/completed","params":{"threadId":"thread-codex-2","turn":{"id":"turn-codex-2","status":"completed","startedAt":1760000001,"completedAt":1760000002,"error":null}}}`))
	if !ok {
		t.Fatal("DecodeAppServerLine(turn completed) ok = false")
	}
	if !completed.ClearTurn || completed.Busy == nil || *completed.Busy {
		t.Fatalf("completed busy/clear = (%v, %v), want false/true", completed.Busy, completed.ClearTurn)
	}
	if completed.ThreadID != "thread-codex-2" || completed.Events[0].ThreadID != "thread-codex-2" {
		t.Fatalf("completed thread ids = (%q, %q), want thread-codex-2", completed.ThreadID, completed.Events[0].ThreadID)
	}
	if completed.Timing == nil || completed.Timing.LastEventTS == nil || *completed.Timing.LastEventTS != 1760000002 {
		t.Fatalf("completed timing = %+v, want last event timestamp", completed.Timing)
	}
	if len(completed.Events) != 1 || completed.Events[0].Boundary == nil || !completed.Events[0].Boundary.CommitLike {
		t.Fatalf("completed events = %+v, want commit-like boundary", completed.Events)
	}
}
