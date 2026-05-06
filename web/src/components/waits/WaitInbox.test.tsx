import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppProviders } from "../../app/providers";
import { createWaitsStore } from "../../domains/waits/store";
import { WaitInbox } from "./WaitInbox";

function createStore<TState extends object, TActions extends Record<string, (...args: any[]) => any>>(
  initialState: TState,
  actions: TActions,
) {
  return {
    getState: () => initialState,
    subscribe() {
      return () => undefined;
    },
    ...actions,
  };
}

let root: HTMLDivElement | null = null;

afterEach(() => {
  if (root) {
    render(null, root);
    root.remove();
    root = null;
  }
});

describe("WaitInbox", () => {
  it("lists active waits and selects session on click", async () => {
    const waitsStore = createWaitsStore();
    waitsStore.loadInbox = vi.fn().mockResolvedValue(undefined);
    waitsStore.applyFrame({ type: "wait.created", stream: "session:s1:ui", payload: { wait: { wait_id: "w1", thread_id: "t1", session_id: "s1", state: "pending_unread", question: "Proceed?", blocking_reason: "blocked" } } } as never);
    const select = vi.fn();
    const sessionsStore = createStore({ items: [], activeSessionId: null }, { select });
    root = document.createElement("div");
    document.body.appendChild(root);
    act(() => {
      render(
        <AppProviders sessionsStore={sessionsStore as never} waitsStore={waitsStore}>
          <WaitInbox />
        </AppProviders>,
        root!,
      );
    });

    expect(root.textContent).toContain("Proceed?");
    const item = root.querySelector(".waitInboxItem") as HTMLButtonElement;
    await act(async () => {
      item.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    expect(select).toHaveBeenCalledWith("s1");
    expect(waitsStore.getState().selectedThreadBySessionId.s1).toBe("t1");
  });
});
