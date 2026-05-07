import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppProviders } from "../../app/providers";
import { createWaitsStore } from "../../domains/waits/store";
import { AskUserView } from "./AskUserView";

function createStore<TState extends object, TActions extends Record<string, (...args: any[]) => any>>(
  initialState: TState,
  actions: TActions,
) {
  const listeners = new Set<() => void>();
  return {
    getState: () => initialState,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    ...actions,
  };
}

let root: HTMLDivElement | null = null;

function renderView(waitsStore = createWaitsStore(), select = vi.fn()) {
  const sessionsStore = createStore({
    items: [{ session_id: "s1", runtime_id: "r1", agent_backend: "pi" }],
    activeSessionId: "s1",
  }, { select });
  root = document.createElement("div");
  document.body.appendChild(root);
  act(() => {
    render(
      <AppProviders sessionsStore={sessionsStore as never} waitsStore={waitsStore}>
        <AskUserView />
      </AppProviders>,
      root!,
    );
  });
  return { select };
}

afterEach(() => {
  if (root) {
    render(null, root);
    root.remove();
    root = null;
  }
  vi.clearAllMocks();
});

describe("AskUserView", () => {
  it("loads the wait inbox when opened", async () => {
    const waitsStore = createWaitsStore();
    waitsStore.loadInbox = vi.fn().mockResolvedValue(undefined);

    renderView(waitsStore);

    await act(async () => {
      await Promise.resolve();
    });

    expect(waitsStore.loadInbox).toHaveBeenCalledTimes(1);
  });

  it("lists active waits and opens selected wait", async () => {
    const waitsStore = createWaitsStore();
    waitsStore.loadInbox = vi.fn().mockResolvedValue(undefined);
    waitsStore.loadThread = vi.fn().mockResolvedValue(undefined);
    waitsStore.applyFrame({ type: "wait.created", stream: "session:s1:ui", payload: { wait: { wait_id: "w1", thread_id: "t1", session_id: "s1", state: "pending_unread", question: "Proceed?", blocking_reason: "blocked" } } } as never);
    waitsStore.applyFrame({ type: "wait.created", stream: "session:s2:ui", payload: { wait: { wait_id: "w2", thread_id: "t2", session_id: "s2", state: "claimed", question: "Choose?" } } } as never);
    const { select } = renderView(waitsStore);

    expect(root?.textContent).toContain("AskUser");
    expect(root?.textContent).toContain("Proceed?");
    expect(root?.textContent).toContain("Choose?");

    const item = Array.from(root!.querySelectorAll(".askUserInboxItem")).find((button) => button.textContent?.includes("Choose?")) as HTMLButtonElement;
    await act(async () => {
      item.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });

    expect(select).toHaveBeenCalledWith("s2");
    expect(waitsStore.getState().selectedThreadBySessionId.s2).toBe("t2");
  });
});
