import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/api";
import { createMessagesStore } from "../messages/store";
import { createLiveSessionStore } from "./store";

vi.mock("../../lib/api", () => ({
  api: {
    getSessionState: vi.fn(),
    listMessages: vi.fn(),
  },
}));

describe("createLiveSessionStore", () => {
  afterEach(() => {
    vi.resetAllMocks();
    vi.useRealTimers();
  });

  it("loads canonical state snapshots into pending ui state, partial turns, and resume cursors", async () => {
    vi.mocked(api.listMessages).mockResolvedValue({
      items: [{ seq: 1, role: "assistant", text: "durable" }],
      has_more: true,
      next_before_seq: 40,
      tail_seq: 1,
    } as never);
    vi.mocked(api.getSessionState).mockResolvedValue({
      busy: true,
      ui_request: {
        request_id: "ask-1",
        method: "select",
        question: "Choose one option",
        options: [{ label: "A", value: "A" }, { label: "B", value: "B" }],
        allow_freeform: false,
      },
      partial_assistant_turn: {
        turn_id: "turn-1",
        text: "partial",
      },
      tail_seq: 1,
      resume_cursors: {
        session: "12",
        ui: "18",
      },
    } as never);
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    await liveStore.loadInitial("s1");

    expect(api.listMessages).toHaveBeenCalledWith("s1", true, undefined, undefined, undefined, 200, undefined, true);
    expect(api.getSessionState).toHaveBeenCalledWith("s1");
    expect(messagesStore.getState().bySessionId.s1).toEqual([
      { seq: 1, role: "assistant", text: "durable" },
      { role: "assistant", streaming: true, completed: false, stream_id: "turn-1", turn_id: "turn-1", text: "partial" },
    ]);
    expect(messagesStore.getState().hasOlderBySessionId.s1).toBe(true);
    expect(messagesStore.getState().olderBeforeBySessionId.s1).toBe(40);
    expect(liveStore.getState().requestsBySessionId.s1).toEqual([
      {
        id: "ask-1",
        request_id: "ask-1",
        method: "select",
        question: "Choose one option",
        options: [{ label: "A", value: "A" }, { label: "B", value: "B" }],
        allow_freeform: false,
      },
    ]);
    expect(liveStore.getState().busyBySessionId.s1).toBe(true);
    expect(liveStore.getState().streamCursorsBySessionId.s1).toBe(12);
    expect(liveStore.getState().uiStreamCursorsBySessionId.s1).toBe(18);
    expect(liveStore.getState().offsetsBySessionId.s1).toBe(1);
  });

  it("polls snapshot state without appending duplicate transcript rows", async () => {
    vi.mocked(api.listMessages)
      .mockResolvedValueOnce({
        items: [{ seq: 1, role: "assistant", text: "durable" }],
        tail_seq: 1,
      } as never)
      .mockResolvedValueOnce({
        items: [{ seq: 2, role: "assistant", text: "committed" }],
        tail_seq: 2,
      } as never);
    vi.mocked(api.getSessionState)
      .mockResolvedValueOnce({
        busy: true,
        ui_request: {
          request_id: "ask-1",
          method: "select",
          question: "Choose one option",
          options: [{ label: "A", value: "A" }],
        },
        partial_assistant_turn: {
          turn_id: "turn-1",
          text: "partial",
        },
        tail_seq: 1,
        resume_cursors: {
          session: "12",
          ui: "18",
        },
      } as never)
      .mockResolvedValueOnce({
        busy: false,
        tail_seq: 2,
        resume_cursors: {
          session: "13",
          ui: "19",
        },
      } as never);
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    await liveStore.loadInitial("s1");
    await liveStore.poll("s1");

    expect(api.listMessages).toHaveBeenNthCalledWith(2, "s1", false, undefined, 1);
    expect(messagesStore.getState().bySessionId.s1).toEqual([
      { seq: 1, role: "assistant", text: "durable" },
      { seq: 2, role: "assistant", text: "committed" },
    ]);
    expect(liveStore.getState().requestsBySessionId.s1).toEqual([]);
    expect(liveStore.getState().busyBySessionId.s1).toBe(false);
    expect(liveStore.getState().streamCursorsBySessionId.s1).toBe(13);
    expect(liveStore.getState().uiStreamCursorsBySessionId.s1).toBe(19);
    expect(liveStore.getState().offsetsBySessionId.s1).toBe(2);
  });

  it("keeps transcript cardinality stable across repeated post-send snapshot polls", async () => {
    vi.mocked(api.listMessages)
      .mockResolvedValueOnce({
        items: [{ seq: 1, role: "user", text: "hello" }],
        tail_seq: 1,
      } as never)
      .mockResolvedValueOnce({
        items: [{ seq: 1, role: "user", text: "hello" }, { seq: 2, role: "assistant", text: "world" }],
        tail_seq: 2,
      } as never)
      .mockResolvedValueOnce({
        items: [{ seq: 1, role: "user", text: "hello" }, { seq: 2, role: "assistant", text: "world" }],
        tail_seq: 2,
      } as never);
    vi.mocked(api.getSessionState)
      .mockResolvedValueOnce({ busy: true, tail_seq: 1, resume_cursors: { session: "1", ui: "1" } } as never)
      .mockResolvedValueOnce({ busy: false, tail_seq: 2, resume_cursors: { session: "2", ui: "2" } } as never)
      .mockResolvedValueOnce({ busy: false, tail_seq: 2, resume_cursors: { session: "2", ui: "2" } } as never);
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    await liveStore.loadInitial("s1");
    await liveStore.poll("s1");
    await liveStore.poll("s1");

    expect(api.listMessages).toHaveBeenCalledTimes(2);
    expect(api.getSessionState).toHaveBeenCalledTimes(3);
    expect(messagesStore.getState().bySessionId.s1).toEqual([
      { seq: 1, role: "user", text: "hello" },
      { seq: 2, role: "assistant", text: "world" },
    ]);
    expect(liveStore.getState().offsetsBySessionId.s1).toBe(2);
  });

  it("uses state probes to skip unchanged transcript snapshots", async () => {
    vi.mocked(api.listMessages).mockResolvedValueOnce({
      items: [{ seq: 1, role: "assistant", text: "durable" }],
      tail_seq: 1,
    } as never);
    vi.mocked(api.getSessionState)
      .mockResolvedValueOnce({ busy: true, tail_seq: 1, resume_cursors: { session: "1", ui: "1" } } as never)
      .mockResolvedValueOnce({ busy: false, tail_seq: 1, resume_cursors: { session: "2", ui: "1" } } as never);
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    await liveStore.loadInitial("s1");
    await liveStore.poll("s1");

    expect(api.listMessages).toHaveBeenCalledTimes(1);
    expect(api.getSessionState).toHaveBeenCalledTimes(2);
    expect(messagesStore.getState().bySessionId.s1).toEqual([
      { seq: 1, role: "assistant", text: "durable" },
    ]);
    expect(liveStore.getState().busyBySessionId.s1).toBe(false);
    expect(liveStore.getState().streamCursorsBySessionId.s1).toBe(2);
    expect(liveStore.getState().offsetsBySessionId.s1).toBe(1);
  });

  it("treats polled idle state as authoritative over stale local generating state", async () => {
    vi.mocked(api.listMessages).mockResolvedValueOnce({ items: [{ seq: 1, role: "assistant", text: "done" }], tail_seq: 1 } as never);
    vi.mocked(api.getSessionState)
      .mockResolvedValueOnce({
        busy: false,
        tail_seq: 1,
        resume_cursors: { session: "1", ui: "1" },
      } as never)
      .mockResolvedValueOnce({
        busy: false,
        tail_seq: 1,
        resume_cursors: { session: "2", ui: "1" },
      } as never);
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    await liveStore.loadInitial("s1");
    liveStore.applyFrame({
      type: "message.generating",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 1, turn_id: "turn-1", role: "assistant", active: true },
    });
    await liveStore.poll("s1");

    expect(api.listMessages).toHaveBeenCalledTimes(1);
    expect(liveStore.getState().busyBySessionId.s1).toBe(false);
    expect(liveStore.getState().generatingBySessionId.s1).toBe(false);
  });

  it("queues a trailing poll when a non-replace poll arrives during an in-flight snapshot", async () => {
    let resolveMessages!: (value: unknown) => void;
    let resolveState!: (value: unknown) => void;
    vi.mocked(api.listMessages)
      .mockReturnValueOnce(new Promise((resolve) => {
        resolveMessages = resolve;
      }) as never)
      .mockResolvedValueOnce({ items: [{ seq: 2, role: "assistant", text: "new" }], tail_seq: 2 } as never);
    vi.mocked(api.getSessionState)
      .mockReturnValueOnce(new Promise((resolve) => {
        resolveState = resolve;
      }) as never)
      .mockResolvedValueOnce({ busy: false, tail_seq: 2, resume_cursors: { session: "2", ui: "2" } } as never);
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    const first = liveStore.poll("s1");
    const second = liveStore.poll("s1");

    expect(api.listMessages).toHaveBeenCalledTimes(1);
    expect(api.getSessionState).toHaveBeenCalledTimes(1);
    resolveMessages({ items: [{ seq: 1, role: "assistant", text: "old" }], tail_seq: 1 });
    resolveState({ busy: true, tail_seq: 1, resume_cursors: { session: "1", ui: "1" } });
    await Promise.all([first, second]);

    expect(api.listMessages).toHaveBeenCalledTimes(2);
    expect(api.getSessionState).toHaveBeenCalledTimes(2);
    expect(messagesStore.getState().bySessionId.s1).toEqual([
      { seq: 1, role: "assistant", text: "old" },
      { seq: 2, role: "assistant", text: "new" },
    ]);
    expect(liveStore.getState().loadingBySessionId.s1).toBe(false);
  });

  it("records and clears transport errors per session", async () => {
    vi.mocked(api.listMessages)
      .mockRejectedValueOnce(new Error("broker unavailable"))
      .mockResolvedValueOnce({ items: [{ seq: 1, role: "assistant", text: "durable" }], tail_seq: 1 } as never);
    vi.mocked(api.getSessionState)
      .mockResolvedValueOnce({ busy: false, tail_seq: 0, resume_cursors: { session: "0", ui: "0" } } as never)
      .mockResolvedValueOnce({ busy: false, tail_seq: 1, resume_cursors: { session: "1", ui: "1" } } as never);
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    await expect(liveStore.loadInitial("s1")).rejects.toThrow("broker unavailable");
    expect(liveStore.getState().errorBySessionId.s1).toBe("broker unavailable");

    await liveStore.loadInitial("s1");
    expect(liveStore.getState().errorBySessionId.s1).toBe("");
  });

  it("retries an empty initial snapshot until history appears", async () => {
    vi.useFakeTimers();
    vi.mocked(api.listMessages)
      .mockResolvedValueOnce({ items: [], tail_seq: 0 } as never)
      .mockResolvedValueOnce({ items: [{ seq: 1, role: "user", text: "hello" }], tail_seq: 1 } as never);
    vi.mocked(api.getSessionState).mockResolvedValue({
      busy: false,
      tail_seq: 0,
      resume_cursors: { session: "0", ui: "0" },
    } as never);
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    await liveStore.loadInitial("s1");

    expect(api.listMessages).toHaveBeenCalledTimes(1);
    expect(messagesStore.getState().loadedBySessionId.s1).toBe(false);

    await vi.advanceTimersByTimeAsync(1000);

    expect(api.listMessages).toHaveBeenCalledTimes(2);
    expect(messagesStore.getState().bySessionId.s1).toEqual([
      { seq: 1, role: "user", text: "hello" },
    ]);
    expect(messagesStore.getState().loadedBySessionId.s1).toBe(true);
    expect(liveStore.getState().offsetsBySessionId.s1).toBe(1);

    await vi.advanceTimersByTimeAsync(8000);
    expect(api.listMessages).toHaveBeenCalledTimes(2);
  });

  it("loads broken transport snapshots into live session error state", async () => {
    vi.mocked(api.listMessages).mockResolvedValue({ items: [], tail_seq: 0 } as never);
    vi.mocked(api.getSessionState).mockResolvedValue({
      busy: true,
      tail_seq: 0,
      resume_cursors: { session: "7", ui: "3" },
      transport: {
        generation_id: "g-1",
        state: "broken",
        reason: "write_failed",
      },
    } as never);
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    await liveStore.loadInitial("s1");

    expect(liveStore.getState().busyBySessionId.s1).toBe(false);
    expect(liveStore.getState().errorBySessionId.s1).toBe("write_failed");
  });

  it("buffers assistant deltas and tracks generating state when assistant buffering is enabled", () => {
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);
    liveStore.setBufferAssistantOutput(true);

    liveStore.applyFrame({
      type: "session.state",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 1, busy: true },
    });
    liveStore.applyFrame({
      type: "message.generating",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 2, turn_id: "turn-1", role: "assistant", active: true },
    });
    liveStore.applyFrame({
      type: "message.delta",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 3, turn_id: "turn-1", role: "assistant", delta: "partial" },
    });

    expect(liveStore.getState().busyBySessionId.s1).toBe(true);
    expect(liveStore.getState().generatingBySessionId.s1).toBe(true);
    expect(messagesStore.getState().bySessionId.s1 ?? []).toEqual([]);

    liveStore.applyFrame({
      type: "message.commit",
      stream: "session:s1",
      payload: {
        session_id: "s1",
        stream_seq: 4,
        turn_id: "turn-1",
        message: { seq: 1, role: "assistant", text: "final" },
      },
    });

    expect(liveStore.getState().generatingBySessionId.s1).toBe(false);
    expect(messagesStore.getState().bySessionId.s1).toHaveLength(1);
    expect(messagesStore.getState().bySessionId.s1[0]).toMatchObject({ seq: 1, role: "assistant", text: "final", turn_id: "turn-1" });
  });

  it("coalesces visible assistant deltas before writing transcript rows", () => {
    vi.useFakeTimers();
    try {
      const messagesStore = createMessagesStore();
      const liveStore = createLiveSessionStore(messagesStore);

      liveStore.applyFrame({
        type: "session.state",
        stream: "session:s1",
        payload: { session_id: "s1", stream_seq: 1, busy: true },
      });
      liveStore.applyFrame({
        id: "frame-1",
        ts: 1000,
        type: "message.delta",
        stream: "session:s1",
        payload: { session_id: "s1", stream_seq: 2, turn_id: "turn-1", role: "assistant", delta: "hel" },
      });
      liveStore.applyFrame({
        id: "frame-2",
        ts: 1001,
        type: "message.delta",
        stream: "session:s1",
        payload: { session_id: "s1", stream_seq: 3, turn_id: "turn-1", role: "assistant", delta: "lo" },
      });

      expect(messagesStore.getState().bySessionId.s1 ?? []).toEqual([]);

      vi.advanceTimersByTime(119);
      expect(messagesStore.getState().bySessionId.s1 ?? []).toEqual([]);

      vi.advanceTimersByTime(1);
      expect(messagesStore.getState().bySessionId.s1).toEqual([
        {
          event_id: "frame-1",
          role: "assistant",
          streaming: true,
          completed: false,
          stream_id: "turn-1",
          turn_id: "turn-1",
          text: "hello",
          ts: 1000,
        },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("ignores stale main stream frames by stream cursor", () => {
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    liveStore.applyFrame({
      type: "message.commit",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 5, turn_id: "turn-1", message: { seq: 1, role: "assistant", text: "final" } },
    });
    liveStore.applyFrame({
      type: "message.delta",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 4, turn_id: "turn-1", role: "assistant", delta: "stale" },
    });
    liveStore.applyFrame({
      type: "session.state",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 4, busy: true },
    });

    expect(liveStore.getState().busyBySessionId.s1).toBeUndefined();
    expect(liveStore.getState().generatingBySessionId.s1).toBe(false);
    expect(messagesStore.getState().bySessionId.s1).toEqual([{ seq: 1, role: "assistant", text: "final", turn_id: "turn-1" }]);
  });

  it("does not stop assistant generation on non-assistant commits", () => {
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    liveStore.applyFrame({
      type: "session.state",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 1, busy: true },
    });
    liveStore.applyFrame({
      type: "message.delta",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 2, turn_id: "turn-1", role: "assistant", delta: "partial" },
    });
    liveStore.applyFrame({
      type: "message.commit",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 3, turn_id: "turn-1", message: { seq: 1, type: "tool", text: "bash" } },
    });
    liveStore.applyFrame({
      type: "message.generating",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 4, turn_id: "turn-1", role: "", active: false },
    });

    expect(liveStore.getState().generatingBySessionId.s1).toBe(true);

    liveStore.applyFrame({
      type: "message.commit",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 5, turn_id: "turn-1", message: { seq: 2, role: "assistant", text: "final" } },
    });

    expect(liveStore.getState().generatingBySessionId.s1).toBe(false);
  });

  it("keeps assistant generation active for commentary commits until final_answer", () => {
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    liveStore.applyFrame({
      type: "session.state",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 1, busy: true },
    });
    liveStore.applyFrame({
      type: "message.delta",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 2, turn_id: "turn-1", role: "assistant", delta: "partial" },
    });
    liveStore.applyFrame({
      type: "message.commit",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 3, turn_id: "turn-1", message: { seq: 1, role: "assistant", text: "checking", details: { phase: "commentary" } } },
    });

    expect(liveStore.getState().generatingBySessionId.s1).toBe(true);
    expect(messagesStore.getState().bySessionId.s1).toEqual([
      { seq: 1, role: "assistant", text: "checking", details: { phase: "commentary" }, turn_id: "turn-1" },
    ]);

    liveStore.applyFrame({
      type: "message.commit",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 4, turn_id: "turn-1", message: { seq: 2, role: "assistant", text: "done", details: { phase: "final_answer" } } },
    });

    expect(liveStore.getState().generatingBySessionId.s1).toBe(false);
  });

  it("treats realtime idle state as authoritative over stale local generating state", () => {
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    liveStore.applyFrame({
      type: "message.generating",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 1, turn_id: "turn-1", role: "assistant", active: true },
    });
    liveStore.applyFrame({
      type: "session.state",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 2, busy: false, runtime_state: "idle" },
    });

    expect(liveStore.getState().busyBySessionId.s1).toBe(false);
    expect(liveStore.getState().generatingBySessionId.s1).toBe(false);
    expect(liveStore.getState().runtimeStateBySessionId.s1).toBe("idle");
  });

  it("does not become busy or append streaming output from assistant delta or generating frames after backend state reports idle", async () => {
    vi.useFakeTimers();
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    liveStore.applyFrame({
      type: "session.state",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 10, busy: false, runtime_state: "idle" },
    });
    liveStore.applyFrame({
      type: "message.delta",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 11, turn_id: "turn-stale", role: "assistant", delta: "late" },
    });
    await vi.advanceTimersByTimeAsync(200);
    liveStore.applyFrame({
      type: "message.generating",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 12, turn_id: "turn-stale", role: "assistant", active: true },
    });

    expect(liveStore.getState().busyBySessionId.s1).toBe(false);
    expect(liveStore.getState().generatingBySessionId.s1).toBe(false);
    expect(liveStore.getState().runtimeStateBySessionId.s1).toBe("idle");
    expect(messagesStore.getState().bySessionId.s1 ?? []).toEqual([]);

    liveStore.applyFrame({
      type: "session.state",
      stream: "session:s1",
      payload: { session_id: "s1", stream_seq: 13, busy: true, runtime_state: "running" },
    });

    expect(liveStore.getState().busyBySessionId.s1).toBe(true);
    expect(liveStore.getState().generatingBySessionId.s1).toBe(false);
    expect(messagesStore.getState().bySessionId.s1 ?? []).toEqual([]);

    vi.useRealTimers();
  });

  it("applies generation broken and transport reset frames", () => {
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    liveStore.applyFrame({
      type: "session.generation.broken",
      stream: "session:s1",
      payload: {
        session_id: "s1",
        stream_seq: 12,
        generation_id: "g-1",
        reason: "write_failed",
      },
    });

    expect(liveStore.getState().busyBySessionId.s1).toBe(false);
    expect(liveStore.getState().errorBySessionId.s1).toBe("write_failed");
    expect(liveStore.getState().streamCursorsBySessionId.s1).toBe(12);

    liveStore.applyFrame({
      type: "transport.reset_required",
      stream: "session:s1",
      payload: {
        session_id: "s1",
        stream_seq: 20,
        reason: "attach_lost",
      },
    });

    expect(liveStore.getState().busyBySessionId.s1).toBe(false);
    expect(liveStore.getState().errorBySessionId.s1).toBe("attach_lost");
    expect(liveStore.getState().streamCursorsBySessionId.s1).toBe(0);
    expect(liveStore.getState().uiStreamCursorsBySessionId.s1).toBe(0);
  });
});
