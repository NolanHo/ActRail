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

    expect(api.listMessages).toHaveBeenCalledWith("s1", true);
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

    expect(api.listMessages).toHaveBeenNthCalledWith(2, "s1", false);
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

    expect(messagesStore.getState().bySessionId.s1).toEqual([
      { seq: 1, role: "user", text: "hello" },
      { seq: 2, role: "assistant", text: "world" },
    ]);
    expect(liveStore.getState().offsetsBySessionId.s1).toBe(2);
  });

  it("does not start overlapping state polls for the same session", async () => {
    let resolveMessages!: (value: unknown) => void;
    let resolveState!: (value: unknown) => void;
    vi.mocked(api.listMessages).mockReturnValueOnce(new Promise((resolve) => {
      resolveMessages = resolve;
    }) as never);
    vi.mocked(api.getSessionState).mockReturnValueOnce(new Promise((resolve) => {
      resolveState = resolve;
    }) as never);
    const messagesStore = createMessagesStore();
    const liveStore = createLiveSessionStore(messagesStore);

    const first = liveStore.poll("s1");
    const second = liveStore.poll("s1");

    expect(api.listMessages).toHaveBeenCalledTimes(1);
    expect(api.getSessionState).toHaveBeenCalledTimes(1);
    resolveMessages({ items: [{ seq: 1, role: "assistant", text: "durable" }], tail_seq: 1 });
    resolveState({ busy: false, tail_seq: 1, resume_cursors: { session: "1", ui: "1" } });
    await Promise.all([first, second]);

    expect(messagesStore.getState().bySessionId.s1).toEqual([{ seq: 1, role: "assistant", text: "durable" }]);
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
