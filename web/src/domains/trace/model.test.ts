import { describe, expect, it } from "vitest";

import type { MessageEvent } from "../../lib/types";
import { buildTrace } from "./model";

function event(partial: MessageEvent): MessageEvent {
  return partial;
}

describe("buildTrace", () => {
  it("places events under explicit parent_event_id edges", () => {
    const trace = buildTrace([
      event({ seq: 1, type: "reasoning", event_id: "root", summary: "plan" }),
      event({ seq: 2, type: "todo_snapshot", event_id: "child", parent_event_id: "root", summary: "todos" }),
    ]);

    expect(trace).toHaveLength(1);
    expect(trace[0].eventId).toBe("root");
    expect(trace[0].edgeConfidence).toBe("inferred");
    expect(trace[0].children).toHaveLength(1);
    expect(trace[0].children[0].eventId).toBe("child");
    expect(trace[0].children[0].edgeConfidence).toBe("explicit");
  });

  it("compresses a tool call and result into one node", () => {
    const trace = buildTrace([
      event({ seq: 1, type: "tool", event_id: "call", tool_call_id: "tc_1", name: "read", summary: "read file", ts: 10 }),
      event({ seq: 2, type: "tool_result", event_id: "result", tool_call_id: "tc_1", summary: "file contents", ts: 13 }),
    ]);

    expect(trace).toHaveLength(1);
    expect(trace[0]).toMatchObject({
      kind: "tool",
      label: "read",
      summary: "file contents",
      status: "pass",
      toolCallId: "tc_1",
      durationSeconds: 3,
    });
    expect(trace[0].call?.event_id).toBe("call");
    expect(trace[0].result?.event_id).toBe("result");
  });

  it("marks tool nodes failed when the result is an error", () => {
    const trace = buildTrace([
      event({ seq: 1, type: "tool", tool_call_id: "tc_1", name: "bash", ts: 10 }),
      event({ seq: 2, type: "tool_result", tool_call_id: "tc_1", is_error: true, summary: "exit 1", ts: 11 }),
    ]);

    expect(trace[0].status).toBe("fail");
  });

  it("marks unpaired tool calls as running", () => {
    const trace = buildTrace([
      event({ seq: 1, type: "tool", tool_call_id: "tc_1", name: "bash", summary: "npm test", ts: 10 }),
    ]);

    expect(trace[0]).toMatchObject({ kind: "tool", status: "running", summary: "npm test" });
  });

  it("places children under compressed tool nodes by tool event id", () => {
    const trace = buildTrace([
      event({ seq: 1, type: "tool", event_id: "call", tool_call_id: "tc_1", name: "bash", ts: 10 }),
      event({ seq: 2, type: "tool_result", event_id: "result", tool_call_id: "tc_1", summary: "ok", ts: 11 }),
      event({ seq: 3, type: "reasoning", event_id: "child", parent_event_id: "call", summary: "after call" }),
    ]);

    expect(trace).toHaveLength(1);
    expect(trace[0].kind).toBe("tool");
    expect(trace[0].children).toHaveLength(1);
    expect(trace[0].children[0].eventId).toBe("child");
    expect(trace[0].children[0].edgeConfidence).toBe("explicit");
  });

  it("keeps sequence-only structure as inferred display grouping", () => {
    const trace = buildTrace([
      event({ seq: 1, role: "user", text: "inspect this" }),
      event({ seq: 2, type: "reasoning", summary: "thinking" }),
      event({ seq: 3, role: "assistant", text: "answer" }),
    ]);

    expect(trace).toHaveLength(3);
    expect(trace.map((node) => node.edgeConfidence)).toEqual(["inferred", "inferred", "inferred"]);
    expect(trace.map((node) => node.summary)).toEqual(["inspect this", "thinking", "answer"]);
  });
});
