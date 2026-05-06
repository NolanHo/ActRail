import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppProviders } from "../../app/providers";
import { createWaitsStore } from "../../domains/waits/store";
import { WaitThreadPanel } from "./WaitThreadPanel";

function createStore<TState extends object, TActions extends Record<string, (...args: any[]) => any>>(
  initialState: TState,
  actions: TActions,
) {
  let state = initialState;
  const listeners = new Set<() => void>();
  return {
    getState: () => state,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    setState(patch: Partial<TState>) {
      state = { ...state, ...patch };
      listeners.forEach((listener) => listener());
    },
    ...actions,
  };
}

let root: HTMLDivElement | null = null;

function getRoot() {
  if (!root) {
    throw new Error("test root missing");
  }
  return root;
}

function renderPanel(waitsStore = createWaitsStore()) {
  const sessionsStore = createStore({
    items: [{ session_id: "s1", runtime_id: "r1", agent_backend: "pi" }],
    activeSessionId: "s1",
  }, { select: vi.fn() });
  root = document.createElement("div");
  document.body.appendChild(root);
  act(() => {
    render(
      <AppProviders sessionsStore={sessionsStore as never} waitsStore={waitsStore}>
        <WaitThreadPanel sessionId="s1" runtimeId="r1" />
      </AppProviders>,
      root!,
    );
  });
}

afterEach(() => {
  if (root) {
    render(null, root);
    root.remove();
    root = null;
  }
});

describe("WaitThreadPanel", () => {
  it("claims, answers, and renders terminal state", async () => {
    const waitsStore = createWaitsStore();
    waitsStore.openWait({ wait_id: "w1", thread_id: "t1", session_id: "s1", state: "pending_unread", question: "Proceed?", blocking_reason: "blocked" });
    waitsStore.loadThread = vi.fn().mockResolvedValue(undefined);
    waitsStore.claimWait = vi.fn().mockImplementation(async () => {
      waitsStore.applyFrame({ type: "wait.claimed", stream: "session:s1:ui", payload: { wait: { wait_id: "w1", thread_id: "t1", session_id: "s1", state: "claimed", question: "Proceed?", blocking_reason: "blocked" } } } as never);
    });
    waitsStore.answerWait = vi.fn().mockImplementation(async () => {
      waitsStore.applyFrame({ type: "wait.answered", stream: "session:s1:ui", payload: { wait: { wait_id: "w1", thread_id: "t1", session_id: "s1", state: "answered", question: "Proceed?", answer: "Yes" } } } as never);
    });
    renderPanel(waitsStore);

    const claim = getRoot().querySelector("button") as HTMLButtonElement;
    expect(claim.textContent).toContain("Claim");
    await act(async () => {
      claim.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    expect(waitsStore.claimWait).toHaveBeenCalledWith("s1", "w1", "r1");

    const textarea = getRoot().querySelector("textarea") as HTMLTextAreaElement;
    await act(async () => {
      textarea.value = "Yes";
      textarea.dispatchEvent(new Event("input", { bubbles: true }));
      await Promise.resolve();
    });
    const submit = Array.from(getRoot().querySelectorAll("button")).find((button) => button.textContent?.includes("Submit answer")) as HTMLButtonElement;
    await act(async () => {
      submit.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    expect(waitsStore.answerWait).toHaveBeenCalledWith("s1", "w1", "Yes", "r1");
    expect(getRoot().textContent).toContain("Answer");
    expect(getRoot().textContent).toContain("Yes");
    expect(getRoot().querySelector("textarea")).toBeNull();
  });
});
