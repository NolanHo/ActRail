import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/api";
import { createWaitsStore } from "./store";

vi.mock("../../lib/api", () => ({
  api: {
    getWaitInbox: vi.fn(),
    getWaitThreads: vi.fn(),
    getWaitThread: vi.fn(),
    claimWait: vi.fn(),
    answerWait: vi.fn(),
    cancelWait: vi.fn(),
  },
}));

describe("createWaitsStore", () => {
  afterEach(() => {
    vi.resetAllMocks();
  });

  it("applies wait lifecycle frames and waits.updated", () => {
    const store = createWaitsStore();
    store.applyFrame({
      type: "wait.created",
      stream: "session:s1:ui",
      payload: {
        wait: {
          wait_id: "w1",
          thread_id: "t1",
          session_id: "s1",
          state: "pending_unread",
          question: "Proceed?",
        },
      },
    } as never);

    expect(store.getState().activeBySessionId.s1?.wait_id).toBe("w1");
    expect(store.getState().inbox.map((wait) => wait.wait_id)).toEqual(["w1"]);

    store.applyFrame({
      type: "wait.answered",
      stream: "session:s1:ui",
      payload: {
        wait: {
          wait_id: "w1",
          thread_id: "t1",
          session_id: "s1",
          state: "answered",
          question: "Proceed?",
          answer: "Yes",
        },
      },
    } as never);

    expect(store.getState().activeBySessionId.s1).toBeNull();
    expect(store.getState().inbox).toEqual([]);

    store.applyFrame({
      type: "waits.updated",
      stream: "system",
      payload: {
        waits: [
          { wait_id: "w2", thread_id: "t2", session_id: "s2", state: "claimed", question: "Choose?" },
          { wait_id: "w3", thread_id: "t3", session_id: "s3", state: "pending_unread", question: "Path?" },
        ],
      },
    } as never);

    expect(store.getState().inbox.map((wait) => wait.wait_id)).toEqual(["w2", "w3"]);
    expect(store.getState().activeBySessionId.s2?.state).toBe("claimed");
    expect(store.getState().activeBySessionId.s3?.state).toBe("pending_unread");
  });

  it("loads thread, claims, answers, and cancels through API", async () => {
    vi.mocked(api.getWaitThread).mockResolvedValue({
      ok: true,
      thread: { thread_id: "t1", session_id: "s1", title: "Proceed?" },
      waits: [{ wait_id: "w1", thread_id: "t1", session_id: "s1", state: "pending_unread", question: "Proceed?" }],
    } as never);
    vi.mocked(api.claimWait).mockResolvedValue({
      ok: true,
      wait: { wait_id: "w1", thread_id: "t1", session_id: "s1", state: "claimed", question: "Proceed?" },
      active_wait: { wait_id: "w1", thread_id: "t1", session_id: "s1", state: "claimed", question: "Proceed?" },
    } as never);
    vi.mocked(api.answerWait).mockResolvedValue({
      ok: true,
      wait: { wait_id: "w1", thread_id: "t1", session_id: "s1", state: "answered", question: "Proceed?", answer: "Yes" },
    } as never);
    vi.mocked(api.cancelWait).mockResolvedValue({
      ok: true,
      wait: { wait_id: "w2", thread_id: "t2", session_id: "s1", state: "cancelled", question: "Cancel?" },
    } as never);
    const store = createWaitsStore();

    await store.loadThread("s1", "t1", "r1");
    expect(api.getWaitThread).toHaveBeenCalledWith("s1", "t1", undefined, "r1");
    expect(store.getState().selectedThreadBySessionId.s1).toBe("t1");
    expect(store.getState().waitsByThreadId.t1?.[0]?.wait_id).toBe("w1");

    await store.claimWait("s1", "w1", "r1");
    expect(api.claimWait).toHaveBeenCalledWith("s1", "w1", "r1");
    expect(store.getState().activeBySessionId.s1?.state).toBe("claimed");

    await store.answerWait("s1", "w1", "Yes", "r1");
    expect(api.answerWait).toHaveBeenCalledWith("s1", "w1", "Yes", "r1");
    expect(store.getState().activeBySessionId.s1).toBeNull();

    await store.cancelWait("s1", "w2", "r1");
    expect(api.cancelWait).toHaveBeenCalledWith("s1", "w2", "r1");
    expect(store.getState().inbox).toEqual([]);
  });
});
