import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/api";
import { createComposerStore } from "./store";

vi.mock("../../lib/api", () => ({
  api: {
    executeSessionCommand: vi.fn(),
    sendMessage: vi.fn(),
  },
}));

describe("createComposerStore", () => {
  afterEach(() => {
    vi.clearAllMocks();
    vi.useRealTimers();
    window.localStorage.clear();
  });

  it("loads persisted drafts from localStorage", () => {
    window.localStorage.setItem("actrail.composerDrafts.v1", JSON.stringify({ s1: "persisted draft" }));

    const store = createComposerStore();

    expect(store.getState().draftBySessionId).toEqual({ s1: "persisted draft" });
  });

  it("stores separate drafts for different sessions", () => {
    vi.useFakeTimers();
    const store = createComposerStore();

    store.setDraft("s1", "first");
    store.setDraft("s2", "second");

    expect(store.getState().draftBySessionId).toEqual({ s1: "first", s2: "second" });
    expect(window.localStorage.getItem("actrail.composerDrafts.v1")).toBeNull();

    vi.advanceTimersByTime(250);

    expect(JSON.parse(window.localStorage.getItem("actrail.composerDrafts.v1") || "{}")).toEqual({ s1: "first", s2: "second" });
  });

  it("coalesces draft persistence while typing", () => {
    vi.useFakeTimers();
    const setItem = vi.spyOn(window.localStorage, "setItem");
    const store = createComposerStore();

    store.setDraft("s1", "a");
    store.setDraft("s1", "ab");
    store.setDraft("s1", "abc");

    expect(store.getState().draftBySessionId).toEqual({ s1: "abc" });
    expect(setItem).not.toHaveBeenCalled();

    vi.advanceTimersByTime(249);
    expect(setItem).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1);

    expect(setItem).toHaveBeenCalledTimes(1);
    expect(JSON.parse(window.localStorage.getItem("actrail.composerDrafts.v1") || "{}")).toEqual({ s1: "abc" });
  });

  it("does not notify subscribers when draft text is unchanged", () => {
    const store = createComposerStore();
    const listener = vi.fn();
    store.subscribe(listener);

    store.setDraft("s1", "same");
    store.setDraft("s1", "same");

    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("copies a draft to a new session id", () => {
    vi.useFakeTimers();
    const store = createComposerStore();
    store.setDraft("s1", "persist me");
    vi.advanceTimersByTime(250);

    store.copyDraft("s1", "s2");

    expect(store.getState().draftBySessionId).toEqual({ s1: "persist me", s2: "persist me" });
    expect(JSON.parse(window.localStorage.getItem("actrail.composerDrafts.v1") || "{}")).toEqual({ s1: "persist me", s2: "persist me" });
  });

  it("clears sending state after a successful submit", async () => {
    vi.mocked(api.sendMessage).mockResolvedValue({ ok: true } as never);
    const store = createComposerStore();
    store.setDraft("s1", "hello");

    await store.submit("s1");

    expect(api.sendMessage).toHaveBeenCalledWith("s1", "hello");
    expect(store.getState()).toEqual({
      draftBySessionId: {},
      sending: false,
      sendingBySessionId: {},
      pendingBySessionId: {
        s1: [
          {
            localId: "local-pending-1",
            role: "user",
            text: "hello",
            pending: true,
            ts: expect.any(Number),
            request_state: "sending",
          },
        ],
      },
    });
  });

  it("executes slash commands through the command endpoint", async () => {
    vi.mocked(api.executeSessionCommand).mockResolvedValue({ ok: true, session_id: "s2" } as never);
    const store = createComposerStore();
    store.setDraft("s1", "  /handoff now  ");

    await store.submit("s1", "rt1");

    expect(api.executeSessionCommand).toHaveBeenCalledWith("s1", { name: "handoff", args: "now" }, "rt1");
    expect(api.sendMessage).not.toHaveBeenCalled();
    expect(store.getState().pendingBySessionId.s1 ?? []).toEqual([]);
  });

  it("trims normal prompts before dispatch", async () => {
    vi.mocked(api.sendMessage).mockResolvedValue({ ok: true } as never);
    const store = createComposerStore();
    store.setDraft("s1", "  hello  ");

    await store.submit("s1");

    expect(api.sendMessage).toHaveBeenCalledWith("s1", "hello");
  });

  it("restores sending=false on prompt failure without clearing the session draft", async () => {
    vi.mocked(api.sendMessage).mockRejectedValue(new Error("fail"));
    const store = createComposerStore();
    store.setDraft("s1", "keep me");

    await expect(store.submit("s1")).rejects.toThrow("fail");

    expect(store.getState()).toEqual({ draftBySessionId: { s1: "keep me" }, sending: false, sendingBySessionId: {}, pendingBySessionId: { s1: [] } });
  });

  it("restores the draft after command failure without creating a pending prompt", async () => {
    vi.mocked(api.executeSessionCommand).mockRejectedValue(new Error("fail"));
    const store = createComposerStore();
    store.setDraft("s1", "/rename next");

    await expect(store.submit("s1")).rejects.toThrow("fail");

    expect(api.sendMessage).not.toHaveBeenCalled();
    expect(store.getState()).toEqual({ draftBySessionId: { s1: "/rename next" }, sending: false, sendingBySessionId: {}, pendingBySessionId: {} });
  });

  it("adds an optimistic pending message immediately and keeps it until persistence catches up", async () => {
    let resolveSend: (value: unknown) => void = () => undefined;
    vi.mocked(api.sendMessage).mockReturnValueOnce(new Promise((resolve) => {
      resolveSend = resolve;
    }) as never);
    const store = createComposerStore();
    store.setDraft("s1", "hello");

    const submitPromise = store.submit("s1");

    expect(store.getState().draftBySessionId.s1 ?? "").toBe("");
    expect(store.getState().sending).toBe(true);
    expect(store.getState().sendingBySessionId.s1).toBe(true);
    expect(store.getState().pendingBySessionId.s1).toHaveLength(1);
    expect(store.getState().pendingBySessionId.s1[0]).toMatchObject({ role: "user", text: "hello", pending: true });

    resolveSend({ ok: true });
    await submitPromise;

    expect(store.getState().draftBySessionId.s1 ?? "").toBe("");
    expect(store.getState().sending).toBe(false);
    expect(store.getState().sendingBySessionId.s1).toBeUndefined();
    expect(store.getState().pendingBySessionId.s1).toHaveLength(1);
  });

  it("allows a second session to submit while another session is still sending", async () => {
    let resolveFirst: (value: unknown) => void = () => undefined;
    vi.mocked(api.sendMessage)
      .mockReturnValueOnce(new Promise((resolve) => {
        resolveFirst = resolve;
      }) as never)
      .mockResolvedValueOnce({ ok: true } as never);
    const store = createComposerStore();
    store.setDraft("s1", "first");
    store.setDraft("s2", "second");

    const firstSubmit = store.submit("s1");
    await Promise.resolve();
    await store.submit("s2");

    expect(api.sendMessage).toHaveBeenNthCalledWith(1, "s1", "first");
    expect(api.sendMessage).toHaveBeenNthCalledWith(2, "s2", "second");
    expect(store.getState().sending).toBe(true);
    expect(store.getState().sendingBySessionId).toEqual({ s1: true });

    resolveFirst({ ok: true });
    await firstSubmit;

    expect(store.getState().sending).toBe(false);
    expect(store.getState().sendingBySessionId).toEqual({});
  });

  it("removes the optimistic pending message and restores the draft after failure", async () => {
    let rejectSend: (error?: unknown) => void = () => undefined;
    vi.mocked(api.sendMessage).mockReturnValueOnce(new Promise((_resolve, reject) => {
      rejectSend = reject;
    }) as never);
    const store = createComposerStore();
    store.setDraft("s1", "keep me");

    const submitPromise = store.submit("s1");

    expect(store.getState().draftBySessionId.s1 ?? "").toBe("");
    expect(store.getState().pendingBySessionId.s1).toHaveLength(1);

    rejectSend(new Error("fail"));
    await expect(submitPromise).rejects.toThrow("fail");

    expect(store.getState()).toEqual({ draftBySessionId: { s1: "keep me" }, sending: false, sendingBySessionId: {}, pendingBySessionId: { s1: [] } });
  });

  it("clears acknowledged pending messages when persisted user messages arrive", async () => {
    vi.mocked(api.sendMessage).mockResolvedValue({ ok: true } as never);
    const store = createComposerStore();
    store.setDraft("s1", "hello");

    await store.submit("s1");
    expect(store.getState().pendingBySessionId.s1).toHaveLength(1);

    const pendingTS = store.getState().pendingBySessionId.s1[0].ts;
    store.clearAcknowledgedPending("s1", [
      { role: "assistant", text: "working" },
      { role: "user", text: "hello", ts: pendingTS + 0.1 },
    ]);

    expect(store.getState()).toEqual({ draftBySessionId: {}, sending: false, sendingBySessionId: {}, pendingBySessionId: { s1: [] } });
  });

  it("does not clear a repeated pending prompt against older durable text", async () => {
    vi.mocked(api.sendMessage).mockResolvedValue({ ok: true } as never);
    const store = createComposerStore();
    store.setDraft("s1", "continue");

    await store.submit("s1");
    const pending = store.getState().pendingBySessionId.s1[0];
    store.clearAcknowledgedPending("s1", [
      { role: "user", text: "continue", ts: pending.ts - 120 },
    ]);

    expect(store.getState().pendingBySessionId.s1).toHaveLength(1);
    expect(store.getState().pendingBySessionId.s1[0]).toMatchObject({ text: "continue", pending: true });
  });
});
