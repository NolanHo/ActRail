import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/api";
import { createMessagesStore } from "./store";

vi.mock("../../lib/api", () => ({
  api: {
    listMessages: vi.fn(),
  },
}));

describe("createMessagesStore", () => {
  afterEach(() => {
    vi.resetAllMocks();
  });

  it("loads initial messages for a session and clears loading state", async () => {
    vi.mocked(api.listMessages).mockResolvedValue({
      events: [{ id: "m1" }, { id: "m2" }],
      offset: 2,
    } as never);
    const store = createMessagesStore();
    const snapshots: Array<Record<string, unknown>> = [];

    store.subscribe(() => {
      const state = store.getState();
      snapshots.push({
        loading: state.loading,
        messages: state.bySessionId.s1 ?? [],
      });
    });

    await store.loadInitial("s1");

    expect(api.listMessages).toHaveBeenCalledWith("s1", true, undefined, undefined, undefined, 60, undefined, true, undefined, 0);
    expect(snapshots).toEqual([
      { loading: true, messages: [] },
      { loading: false, messages: [{ id: "m1" }, { id: "m2" }] },
    ]);
    expect(store.getState()).toEqual({
      bySessionId: {
        s1: [{ id: "m1" }, { id: "m2" }],
      },
      offsetsBySessionId: {
        s1: 2,
      },
      hasOlderBySessionId: {
        s1: false,
      },
      olderBeforeBySessionId: {
        s1: 0,
      },
      loadingOlderBySessionId: {
        s1: false,
      },
      loadingBySessionId: {
        s1: false,
      },
      loadedBySessionId: {
        s1: true,
      },
      loading: false,
    });
  });

  it("merges polled snapshots for one session without touching other sessions", async () => {
    vi.mocked(api.listMessages)
      .mockResolvedValueOnce({
        events: [{ id: "m1" }],
        offset: 1,
      } as never)
      .mockResolvedValueOnce({
        events: [{ id: "other" }],
        offset: 1,
      } as never)
      .mockResolvedValueOnce({
        events: [{ id: "m1" }, { id: "m2" }],
        offset: 2,
      } as never);
    const store = createMessagesStore();

    await store.loadInitial("s1");
    await store.loadInitial("s2");
    await store.poll("s1");

    expect(api.listMessages).toHaveBeenNthCalledWith(1, "s1", true, undefined, undefined, undefined, 60, undefined, true, undefined, 0);
    expect(api.listMessages).toHaveBeenNthCalledWith(2, "s2", true, undefined, undefined, undefined, 60, undefined, true, undefined, 0);
    expect(api.listMessages).toHaveBeenNthCalledWith(3, "s1", false, undefined, 1, undefined, 200, undefined, true, undefined, 0);
    expect(store.getState()).toEqual({
      bySessionId: {
        s1: [{ id: "m1" }, { id: "m2" }],
        s2: [{ id: "other" }],
      },
      offsetsBySessionId: {
        s1: 2,
        s2: 1,
      },
      hasOlderBySessionId: {
        s1: false,
        s2: false,
      },
      olderBeforeBySessionId: {
        s1: 0,
        s2: 0,
      },
      loadingOlderBySessionId: {
        s1: false,
        s2: false,
      },
      loadingBySessionId: {
        s1: false,
        s2: false,
      },
      loadedBySessionId: {
        s1: true,
        s2: true,
      },
      loading: false,
    });
  });

  it("clears loading when message fetch fails", async () => {
    vi.mocked(api.listMessages).mockRejectedValue(new Error("boom"));
    const store = createMessagesStore();

    await expect(store.loadInitial("s1")).rejects.toThrow("boom");
    expect(store.getState()).toEqual({
      bySessionId: {},
      offsetsBySessionId: {},
      hasOlderBySessionId: {},
      olderBeforeBySessionId: {},
      loadingOlderBySessionId: {},
      loadingBySessionId: {
        s1: false,
      },
      loadedBySessionId: {},
      loading: false,
    });
  });

  it("ignores stale message responses when a newer explicit reload supersedes them", async () => {
    let resolveFirst: (v: any) => void;
    let resolveSecond: (v: any) => void;
    vi.mocked(api.listMessages)
      .mockReturnValueOnce(new Promise((r) => { resolveFirst = r; }))
      .mockReturnValueOnce(new Promise((r) => { resolveSecond = r; }));
    const store = createMessagesStore();

    const firstPromise = store.loadInitial("s1");
    const secondPromise = store.loadInitial("s1");

    resolveSecond!({ events: [{ id: "m2" }], offset: 2 });
    await secondPromise;
    expect(store.getState().bySessionId.s1).toEqual([{ id: "m2" }]);

    resolveFirst!({ events: [{ id: "m1" }], offset: 1 });
    await firstPromise;
    expect(store.getState().bySessionId.s1).toEqual([{ id: "m2" }]);
  });

  it("does not start an overlapping poll while the same session is still loading", async () => {
    let resolveFirst: (value: any) => void;
    vi.mocked(api.listMessages).mockReturnValueOnce(new Promise((resolve) => {
      resolveFirst = resolve;
    }) as never);
    const store = createMessagesStore();

    const initialPromise = store.loadInitial("s1");
    const pollPromise = store.poll("s1");

    expect(api.listMessages).toHaveBeenCalledTimes(1);
    expect(api.listMessages).toHaveBeenCalledWith("s1", true, undefined, undefined, undefined, 60, undefined, true, undefined, 0);

    resolveFirst!({ events: [{ id: "m1" }], offset: 1 });
    await Promise.all([initialPromise, pollPromise]);

    expect(store.getState().bySessionId.s1).toEqual([{ id: "m1" }]);
    expect(store.getState().offsetsBySessionId.s1).toBe(1);
  });

  it("uses a bounded latest page when polling from a zero offset", async () => {
    vi.mocked(api.listMessages).mockResolvedValueOnce({ events: [{ id: "m5", seq: 5 }], offset: 5 } as never);
    const store = createMessagesStore();

    await store.poll("s1");

    expect(api.listMessages).toHaveBeenCalledWith("s1", true, undefined, undefined, undefined, 60, undefined, true, undefined, 0);
    expect(store.getState().bySessionId.s1).toEqual([{ id: "m5", seq: 5 }]);
    expect(store.getState().offsetsBySessionId.s1).toBe(5);
  });

  it("keeps transcript cardinality stable across repeated snapshot polls with stable ids", async () => {
    vi.mocked(api.listMessages)
      .mockResolvedValueOnce({ events: [{ id: "m1", role: "user", seq: 1 }], offset: 1 } as never)
      .mockResolvedValueOnce({ events: [{ id: "m1", role: "user", seq: 1 }, { id: "m2", seq: 2 }], offset: 2 } as never)
      .mockResolvedValueOnce({ events: [{ id: "m1", role: "user", seq: 1 }, { id: "m2", seq: 2 }], offset: 2 } as never);
    const store = createMessagesStore();

    await store.loadInitial("s1");
    await store.poll("s1");
    await store.poll("s1");

    expect(api.listMessages).toHaveBeenNthCalledWith(2, "s1", false, undefined, 1, undefined, 200, undefined, true, undefined, 1);
    expect(api.listMessages).toHaveBeenNthCalledWith(3, "s1", false, undefined, 2, undefined, 200, undefined, true, undefined, 1);
    expect(store.getState().bySessionId.s1).toEqual([{ id: "m1", role: "user", seq: 1 }, { id: "m2", seq: 2 }]);
  });

  it("dedupes SessionMessages rows by event id before seq", async () => {
    const store = createMessagesStore();

    store.applyLive("s1", [{ seq: 1, event_id: "codex:message:user-1", role: "user", text: "continue" } as any], { replace: true, offset: 1 });
    store.applySnapshot("s1", [{ seq: 2, event_id: "codex:message:user-1", role: "user", text: "continue" } as any], { offset: 2 });

    expect(store.getState().bySessionId.s1).toEqual([
      { seq: 2, event_id: "codex:message:user-1", role: "user", text: "continue" },
    ]);
  });

  it("replaces snapshot pages that do not expose a stable merge key", async () => {
    vi.mocked(api.listMessages)
      .mockResolvedValueOnce({ events: [{ role: "assistant", text: "hello" }], offset: 1 } as never)
      .mockResolvedValueOnce({ events: [{ role: "assistant", text: "hello" }], offset: 1 } as never);
    const store = createMessagesStore();

    await store.loadInitial("s1");
    await store.poll("s1");

    expect(store.getState().bySessionId.s1).toEqual([{ role: "assistant", text: "hello" }]);
  });

  it("drops a live user echo when Pi snapshot contains the same prompt at the same time", async () => {
    const store = createMessagesStore();

    store.applyLive("s1", [{ seq: 22, role: "user", text: "continue", ts: 1000 } as any], { replace: false, offset: 22 });
    store.applySnapshot("s1", [{ seq: 301, event_id: "pi:message:u1", role: "user", text: "continue", ts: 1030 } as any], { offset: 301 });

    expect(store.getState().bySessionId.s1).toEqual([
      { seq: 301, event_id: "pi:message:u1", role: "user", text: "continue", ts: 1030 },
    ]);
  });

  it("keeps repeated user text when timestamps indicate separate turns", async () => {
    const store = createMessagesStore();

    store.applyLive("s1", [{ seq: 22, role: "user", text: "continue", ts: 2000 } as any], { replace: false, offset: 22 });
    store.applySnapshot("s1", [{ seq: 1, event_id: "pi:message:u1", role: "user", text: "continue", ts: 1000 } as any], { offset: 1 });

    expect(store.getState().bySessionId.s1).toEqual([
      { seq: 1, event_id: "pi:message:u1", role: "user", text: "continue", ts: 1000 },
      { seq: 22, role: "user", text: "continue", ts: 2000 },
    ]);
  });

  it("preserves live user messages when a stale initial snapshot resolves later", async () => {
    let resolveInitial: (v: any) => void;
    vi.mocked(api.listMessages).mockReturnValueOnce(new Promise((resolve) => {
      resolveInitial = resolve;
    }) as never);
    const store = createMessagesStore();

    const initialPromise = store.loadInitial("s1");
    store.applyLive("s1", [{ seq: 2, role: "user", text: "new prompt" } as any], { replace: false, offset: 2 });

    resolveInitial!({ events: [{ seq: 1, role: "assistant", text: "old" }], offset: 1 });
    await initialPromise;

    expect(store.getState().bySessionId.s1).toEqual([
      { seq: 1, role: "assistant", text: "old" },
      { seq: 2, role: "user", text: "new prompt" },
    ]);
  });

  it("applies snapshot replacement and live appends without duplicating stable ids", () => {
    const store = createMessagesStore();

    store.applySnapshot("s1", [{ id: "m1" } as any], { offset: 4 });
    store.applySnapshot("s1", [{ id: "m1" }, { id: "m2" }] as any, { offset: 5 });
    store.applyLive("s1", [{ id: "m2", text: "updated" } as any], { replace: false, offset: 5 });

    expect(store.getState().bySessionId.s1).toEqual([{ id: "m1" }, { id: "m2", text: "updated" }]);
    expect(store.getState().offsetsBySessionId.s1).toBe(5);
    expect(store.getState().loadedBySessionId.s1).toBe(true);
  });

  it("upserts one streamed assistant row per stream_id and replaces it with durable history", () => {
    const store = createMessagesStore();

    store.applyLive(
      "s1",
      [{ role: "assistant", text: "hel", streaming: true, stream_id: "pi-stream:turn-001", turn_id: "turn-001" } as any],
      { replace: true, offset: 1 },
    );

    store.applyLive(
      "s1",
      [{ role: "assistant", text: "hello", streaming: true, stream_id: "pi-stream:turn-001", turn_id: "turn-001" } as any],
      { replace: false, offset: 2 },
    );

    expect(store.getState().bySessionId.s1).toEqual([
      { role: "assistant", text: "hello", streaming: true, stream_id: "pi-stream:turn-001", turn_id: "turn-001" },
    ]);

    store.applyLive(
      "s1",
      [{ role: "assistant", text: "hello", turn_id: "turn-001" } as any],
      { replace: false, offset: 3 },
    );

    expect(store.getState().bySessionId.s1).toEqual([
      { role: "assistant", text: "hello", turn_id: "turn-001" },
    ]);
  });

  it("replaces a streaming assistant row when the durable commit arrives with its own event_id", () => {
    const store = createMessagesStore();

    store.applyLive(
      "s1",
      [{ role: "assistant", text: "State the", streaming: true, stream_id: "turn-001", turn_id: "turn-001" } as any],
      { replace: true, offset: 1 },
    );

    store.applyLive(
      "s1",
      [{ event_id: "evt-commit-1", role: "assistant", text: "State the task.", turn_id: "turn-001" } as any],
      { replace: false, offset: 2 },
    );

    expect(store.getState().bySessionId.s1).toEqual([
      { event_id: "evt-commit-1", role: "assistant", text: "State the task.", turn_id: "turn-001" },
    ]);
  });

  it("only treats a trailing streaming assistant as active generation", () => {
    const store = createMessagesStore();

    store.applyLive(
      "s1",
      [
        { role: "assistant", text: "old partial", streaming: true, stream_id: "turn-001", turn_id: "turn-001" },
        { seq: 2, event_id: "evt-final-2", role: "assistant", text: "newer answer" },
      ] as any,
      { replace: true, offset: 2 },
    );

    expect(store.hasStreamingAssistant("s1")).toBe(false);

    store.applyLive(
      "s1",
      [
        { seq: 3, event_id: "evt-user-3", role: "user", text: "continue" },
        { role: "assistant", text: "new partial", streaming: true, stream_id: "turn-002", turn_id: "turn-002" },
        { type: "tool", event_id: "evt-tool-4", text: "running" },
      ] as any,
      { replace: false, offset: 4 },
    );

    expect(store.hasStreamingAssistant("s1")).toBe(true);
  });

  it("drops duplicate durable assistant commits with different event ids", () => {
    const store = createMessagesStore();

    store.applyLive(
      "s1",
      [{ seq: 1, event_id: "evt-1", role: "assistant", text: "same answer", ts: 1000 } as any],
      { replace: true, offset: 1 },
    );
    store.applyLive(
      "s1",
      [{ seq: 2, event_id: "evt-2", role: "assistant", text: "same answer", ts: 1001 } as any],
      { replace: false, offset: 2 },
    );

    expect(store.getState().bySessionId.s1).toEqual([
      { seq: 1, event_id: "evt-1", role: "assistant", text: "same answer", ts: 1000 },
    ]);
    expect(store.getState().offsetsBySessionId.s1).toBe(2);
  });

  it("upserts live tool calls by tool_call_id before render", () => {
    const store = createMessagesStore();

    store.applyLive(
      "s1",
      [{ event_id: "pi:tool_call:call-1", type: "tool", name: "read", tool_call_id: "call-1", text: "read", ts: 1000 } as any],
      { replace: true, offset: 1 },
    );
    store.applyLive(
      "s1",
      [{ seq: 2, event_id: "pi:tool_call:entry-a", type: "tool", name: "read", details: { tool_call_id: "call-1", arguments: { path: "package.json" } }, text: "read package.json", ts: 1001 } as any],
      { replace: false, offset: 2 },
    );

    expect(store.getState().bySessionId.s1).toEqual([
      { event_id: "pi:tool_call:entry-a", seq: 2, type: "tool", name: "read", tool_call_id: "call-1", details: { tool_call_id: "call-1", arguments: { path: "package.json" } }, text: "read package.json", ts: 1001 },
    ]);
    expect(store.getState().offsetsBySessionId.s1).toBe(2);
  });

  it("keeps a tool call and its result distinct while deduping repeated results", () => {
    const store = createMessagesStore();

    store.applyLive(
      "s1",
      [
        { event_id: "pi:tool_call:call-1", type: "tool", name: "read", tool_call_id: "call-1", text: "read", ts: 1000 } as any,
        { event_id: "pi:tool_result:call-1:0", type: "tool_result", name: "read", tool_call_id: "call-1", text: "old", ts: 1001 } as any,
      ],
      { replace: true, offset: 2 },
    );
    store.applyLive(
      "s1",
      [{ event_id: "pi:tool_result:turn-1:0", type: "tool_result", name: "read", details: { tool_call_id: "call-1" }, text: "new", ts: 1002 } as any],
      { replace: false, offset: 3 },
    );

    expect(store.getState().bySessionId.s1).toEqual([
      { event_id: "pi:tool_call:call-1", type: "tool", name: "read", tool_call_id: "call-1", text: "read", ts: 1000 },
      { event_id: "pi:tool_result:turn-1:0", type: "tool_result", name: "read", tool_call_id: "call-1", details: { tool_call_id: "call-1" }, text: "new", ts: 1002 },
    ]);
  });

  it("prepends older replay pages and tracks the next history cursor", async () => {
    vi.mocked(api.listMessages)
      .mockResolvedValueOnce({
        events: [{ id: "m2", role: "user", seq: 1 }, { id: "m3", seq: 2 }],
        offset: 4,
        has_older: true,
        next_before: 2,
      } as never)
      .mockResolvedValueOnce({
        events: [{ id: "m0" }, { id: "m1" }],
        offset: 4,
        has_older: false,
        next_before: 0,
      } as never);
    const store = createMessagesStore();

    await store.loadInitial("s1");
    await store.loadOlder("s1");

    expect(api.listMessages).toHaveBeenNthCalledWith(2, "s1", true, undefined, undefined, 2, 150, undefined, true, undefined, 1);
    expect(store.getState().bySessionId.s1).toEqual([{ id: "m0" }, { id: "m1" }, { id: "m2", role: "user", seq: 1 }, { id: "m3", seq: 2 }]);
    expect(store.getState().hasOlderBySessionId.s1).toBe(false);
    expect(store.getState().olderBeforeBySessionId.s1).toBe(0);
  });

  it("keeps fetching older pages until one visible conversation message is found", async () => {
    vi.mocked(api.listMessages)
      .mockResolvedValueOnce({
        events: [{ id: "m2", role: "user", text: "current", seq: 1 }],
        offset: 3,
        has_older: true,
        next_before: 2,
      } as never)
      .mockResolvedValueOnce({
        events: [
          { id: "tool-1", type: "tool", name: "read" },
          { id: "tool-2", type: "tool_result", text: "ok" },
        ],
        offset: 3,
        has_older: true,
        next_before: 4,
      } as never)
      .mockResolvedValueOnce({
        events: [{ id: "m1", role: "user", text: "anchor" }],
        offset: 3,
        has_older: false,
        next_before: 0,
      } as never);
    const store = createMessagesStore();

    await store.loadInitial("s1");
    await store.loadOlder("s1");

    expect(api.listMessages).toHaveBeenNthCalledWith(2, "s1", true, undefined, undefined, 2, 150, undefined, true, undefined, 1);
    expect(api.listMessages).toHaveBeenNthCalledWith(3, "s1", true, undefined, undefined, 4, 150, undefined, true, undefined, 1);
    expect(store.getState().bySessionId.s1).toEqual([
      { id: "m1", role: "user", text: "anchor" },
      { id: "tool-1", type: "tool", name: "read" },
      { id: "tool-2", type: "tool_result", text: "ok" },
      { id: "m2", role: "user", text: "current", seq: 1 },
    ]);
    expect(store.getState().hasOlderBySessionId.s1).toBe(false);
    expect(store.getState().olderBeforeBySessionId.s1).toBe(0);
  });
});
