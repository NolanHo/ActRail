import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../../lib/api";
import { TraceView } from "./TraceView";

vi.mock("../../lib/api", () => ({
  api: {
    listMessages: vi.fn(),
  },
}));

function flushAsyncRender(): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, 0));
}

describe("TraceView", () => {
  let root: HTMLDivElement | null = null;

  afterEach(() => {
    if (root) {
      render(null, root);
      root.remove();
      root = null;
    }
    vi.mocked(api.listMessages).mockReset();
  });

  it("renders collapsed trace nodes with inferred and explicit labels", () => {
    root = document.createElement("div");
    document.body.appendChild(root);

    render(
      <TraceView
        sessionId="sess-1"
        messages={[
          { seq: 1, type: "reasoning", event_id: "root", summary: "Plan" },
          { seq: 2, type: "todo_snapshot", event_id: "child", parent_event_id: "root", summary: "Todo" },
        ]}
      />,
      root,
    );

    const text = root.textContent || "";
    expect(root.querySelector("[data-testid='trace-view']")).not.toBeNull();
    expect(text).toContain("Trace uses loaded conversation events");
    expect(text).toContain("Plan");
    expect(text).toContain("Todo");
    expect(text).toContain("inferred");
    expect(text).toContain("explicit");
  });

  it("hydrates a tool node through the existing message detail API", async () => {
    vi.mocked(api.listMessages).mockResolvedValue({
      events: [],
      items: [{ type: "tool_result", tool_call_id: "tc_1", details: { output: "hydrated output" } }],
    });
    root = document.createElement("div");
    document.body.appendChild(root);

    render(
      <TraceView
        sessionId="sess-1"
        runtimeId="rt-1"
        messages={[
          { seq: 1, type: "tool", tool_call_id: "tc_1", name: "bash", summary: "npm test" },
          { seq: 2, type: "tool_result", tool_call_id: "tc_1", summary: "deferred", details: { deferred: true } },
        ]}
      />,
      root,
    );

    const button = Array.from(root.querySelectorAll("button")).find((item) => item.textContent?.includes("bash"));
    expect(button).toBeTruthy();
    await act(async () => {
      button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await flushAsyncRender();
    });

    expect(api.listMessages).toHaveBeenCalledWith(
      "sess-1",
      false,
      undefined,
      undefined,
      undefined,
      undefined,
      "rt-1",
      false,
      undefined,
      0,
      true,
      "",
      "tc_1",
    );
    expect(root.textContent || "").toContain("hydrated output");
  });
});
