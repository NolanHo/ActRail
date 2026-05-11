import { describe, expect, it } from "vitest";
import type { MessageEvent } from "../../lib/types";
import { buildToolActivitySummary, type MachineTraceKind, type ToolActivityOptions } from "./toolActivity";

const MACHINE_KINDS = new Set<MachineTraceKind>(["reasoning", "tool", "tool_result", "todo_snapshot", "custom_message", "pi_event"]);

function options(overrides: Partial<ToolActivityOptions> = {}): ToolActivityOptions {
  return {
    nowSeconds: 140,
    isBusy: false,
    visibleLimit: 12,
    staleAfterSeconds: 30,
    kindForEvent(event) {
      const kind = String(event.type || "");
      return MACHINE_KINDS.has(kind as MachineTraceKind) ? (kind as MachineTraceKind) : null;
    },
    eventKey: (_event, kind, index) => `${kind}:${index}`,
    eventTimestampSeconds: (event) => (typeof event.ts === "number" ? event.ts : null),
    toolCallID: (event) => (typeof event.tool_call_id === "string" ? event.tool_call_id : ""),
    toolName: (event) => event.name || event.toolName || "",
    piEventVariant: (event) => {
      const summary = `${event.summary || ""} ${event.text || ""}`.toLowerCase();
      if (summary.includes("empty message")) return "empty_output";
      if (summary.includes("turn finished")) return "turn_terminal";
      if (summary.includes("retry")) return "retry_error";
      return null;
    },
    ...overrides,
  };
}

describe("buildToolActivitySummary", () => {
  it("aggregates paired, running, failed, reasoning, todo, and pi activity", () => {
    const events: MessageEvent[] = [
      { type: "reasoning", text: "thinking", ts: 100 },
      { type: "tool", name: "read", tool_call_id: "read-1", ts: 101 },
      { type: "tool_result", name: "read", tool_call_id: "read-1", text: "ok", ts: 103 },
      { type: "tool", name: "bash", tool_call_id: "run-1", text: "npm test", ts: 104 },
      { type: "tool_result", name: "bash", tool_call_id: "orphan-fail", text: "failed", is_error: true, ts: 105 },
      { type: "pi_event", summary: "Assistant returned empty message", text: "empty", is_error: true, ts: 106 },
      { type: "todo_snapshot", progress_text: "1/2", ts: 107 },
    ];

    const summary = buildToolActivitySummary(events, options({ isBusy: true, nowSeconds: 120 }));

    expect(summary.totalTools).toBe(3);
    expect(summary.toolCalls).toBe(2);
    expect(summary.toolResults).toBe(2);
    expect(summary.running).toBe(1);
    expect(summary.ok).toBe(1);
    expect(summary.failed).toBe(2);
    expect(summary.reasoning).toBe(1);
    expect(summary.todoSnapshots).toBe(1);
    expect(summary.systemEvents).toBe(1);
    expect(summary.runningToolNames).toEqual(["bash"]);
    expect(summary.summaryText).toContain("Ran 3 tools");
    expect(summary.summaryText).toContain("1 ok");
    expect(summary.summaryText).toContain("1 running");
    expect(summary.summaryText).toContain("2 errors");
    expect(summary.statusText).toContain("running 1: bash");
  });

  it("bounds visible trace events and reports hidden count", () => {
    const events: MessageEvent[] = Array.from({ length: 30 }, (_value, index) => ({
      type: "tool_result",
      name: "bash",
      text: `result ${index}`,
      ts: index,
    }));

    const summary = buildToolActivitySummary(events, options({ visibleLimit: 5 }));

    expect(summary.visibleEvents).toHaveLength(5);
    expect(summary.hiddenEventCount).toBe(25);
    expect(summary.visibleEvents.map((item) => item.index)).toEqual([25, 26, 27, 28, 29]);
  });

  it("marks busy unresolved tools as stalled only after stale activity", () => {
    const events: MessageEvent[] = [
      { type: "tool", name: "bash", tool_call_id: "build", text: "npm run build", ts: 100 },
    ];

    const fresh = buildToolActivitySummary(events, options({ isBusy: true, nowSeconds: 120, staleAfterSeconds: 30 }));
    const stale = buildToolActivitySummary(events, options({ isBusy: true, nowSeconds: 140, staleAfterSeconds: 30 }));
    const idle = buildToolActivitySummary(events, options({ isBusy: false, nowSeconds: 140, staleAfterSeconds: 30 }));

    expect(fresh.stalled).toBe(false);
    expect(fresh.running).toBe(1);
    expect(stale.stalled).toBe(true);
    expect(stale.summaryText).toContain("no output 40s");
    expect(stale.maxRunningSeconds).toBe(40);
    expect(idle.stalled).toBe(false);
    expect(idle.running).toBe(0);
  });
});
