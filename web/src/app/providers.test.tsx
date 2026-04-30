import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AppProviders, useComposerStoreApi, useComposerStoreSelector } from "./providers";
import { createComposerStore } from "../domains/composer/store";

function PendingProbe({ onRender }: { onRender: (count: number) => void }) {
  const pending = useComposerStoreSelector((state) => state.pendingBySessionId);
  onRender(Object.keys(pending).length);
  return <div data-testid="pending-count">{Object.keys(pending).length}</div>;
}

function DraftWriter() {
  const store = useComposerStoreApi();
  return <button type="button" onClick={() => store.setDraft("s1", "typed")}>type</button>;
}

describe("provider selector hooks", () => {
  let root: HTMLDivElement | null = null;

  afterEach(() => {
    if (root) {
      render(null, root);
      root.remove();
      root = null;
    }
    localStorage.clear();
  });

  it("does not re-render a selected composer slice when unrelated draft state changes", () => {
    const composerStore = createComposerStore();
    const renders = vi.fn();
    root = document.createElement("div");
    document.body.appendChild(root);

    render(
      <AppProviders composerStore={composerStore}>
        <PendingProbe onRender={renders} />
        <DraftWriter />
      </AppProviders>,
      root,
    );

    expect(renders).toHaveBeenCalledTimes(1);

    act(() => {
      root?.querySelector("button")?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(renders).toHaveBeenCalledTimes(1);
  });
});
