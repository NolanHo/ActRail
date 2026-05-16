import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../../lib/api";
import { SessionFileView } from "./SessionFileView";

vi.mock("../../lib/api", () => ({
  api: {
    getCodexSessionFiles: vi.fn(),
    getCodexSessionFile: vi.fn(),
    renameCodexSessionFile: vi.fn(),
  },
}));

let root: HTMLDivElement | null = null;

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 0));
}

async function settle(count = 4) {
  for (let index = 0; index < count; index += 1) {
    await flush();
  }
}

async function waitForCalls(mock: { mock: { calls: unknown[][] } }, minimumCalls = 1, attempts = 24) {
  for (let index = 0; index < attempts; index += 1) {
    if (mock.mock.calls.length >= minimumCalls) {
      return;
    }
    await settle(2);
  }
}

async function renderView(props?: Partial<Parameters<typeof SessionFileView>[0]>) {
  root = document.createElement("div");
  document.body.appendChild(root);
  await act(async () => {
    render(
      <SessionFileView
        active
        activeCwd="/workspace/project"
        {...props}
      />,
      root!,
    );
    await settle(8);
  });
}

describe("SessionFileView", () => {
  afterEach(() => {
    vi.clearAllMocks();
    if (root) {
      render(null, root);
      root.remove();
      root = null;
    }
  });

  it("loads cwd sessions and renders user assistant turns", async () => {
    (api as any).getCodexSessionFiles.mockResolvedValue({
      items: [
        {
          thread_id: "thread-1",
          display_name: "Build status",
          cwd: "/workspace/project",
          path: "/home/user/.codex/sessions/rollout.jsonl",
          source: "state_db",
          updated_ts: 1710000000,
        },
      ],
    });
    (api as any).getCodexSessionFile.mockResolvedValue({
      summary: { thread_id: "thread-1", display_name: "Build status", path: "/home/user/.codex/sessions/rollout.jsonl" },
      turns: [
        {
          index: 0,
          user: { role: "user", text: "Run **tests**" },
          assistant: { role: "assistant", text: "Tests **passed**" },
          messages: [],
        },
      ],
    });

    await renderView();

    expect((api as any).getCodexSessionFiles).toHaveBeenCalledWith(
      expect.objectContaining({ scope: "cwd", cwd: "/workspace/project", limit: 100 }),
      expect.any(AbortSignal),
    );
    expect((api as any).getCodexSessionFile).not.toHaveBeenCalled();

    const listItem = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => button.textContent?.includes("Build status"));
    expect(listItem).toBeDefined();
    await act(async () => {
      listItem?.click();
      await settle(8);
    });
    await waitForCalls((api as any).getCodexSessionFile);

    expect((api as any).getCodexSessionFile).toHaveBeenCalledWith("thread-1", { limit: 500 }, expect.any(AbortSignal));
    expect(root?.textContent).toContain("Build status");
    expect(root?.textContent).toContain("Run tests");
    expect(root?.textContent).toContain("Tests passed");
    expect(root?.querySelector(".sessionFileMessageMarkdown strong")?.textContent).toBe("tests");
  });

  it("defaults cwd scope to /root/docs when no active cwd is provided", async () => {
    (api as any).getCodexSessionFiles.mockResolvedValue({ items: [] });

    await renderView({ activeCwd: "" });

    expect((api as any).getCodexSessionFiles).toHaveBeenCalledWith(
      expect.objectContaining({ scope: "cwd", cwd: "/root/docs", limit: 100 }),
      expect.any(AbortSignal),
    );
    expect(root?.textContent).toContain("/root/docs");
  });

  it("switches to all scope and renames selected session", async () => {
    const onRenamed = vi.fn();
    (api as any).getCodexSessionFiles.mockResolvedValue({
      items: [
        {
          thread_id: "thread-1",
          display_name: "Old name",
          cwd: "/workspace/project",
          source: "state_db",
        },
      ],
    });
    (api as any).getCodexSessionFile.mockResolvedValue({
      summary: { thread_id: "thread-1", display_name: "Old name" },
      turns: [],
      items: [{ role: "user", text: "Hello" }],
    });
    (api as any).renameCodexSessionFile.mockResolvedValue({
      ok: true,
      summary: { thread_id: "thread-1", title: "New name", display_name: "New name" },
    });

    await renderView({ onRenamed });
    const listItem = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => button.textContent?.includes("Old name"));
    expect(listItem).toBeDefined();
    await act(async () => {
      listItem?.click();
      await settle(8);
    });

    const allButton = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => button.textContent === "all");
    expect(allButton).toBeDefined();
    await act(async () => {
      allButton?.click();
      await settle(8);
    });
    expect((api as any).getCodexSessionFiles).toHaveBeenLastCalledWith(
      expect.objectContaining({ scope: "all", cwd: undefined }),
      expect.any(AbortSignal),
    );

    const input = root?.querySelector<HTMLInputElement>('input[aria-label="Session name"]');
    expect(input).not.toBeNull();
    await act(async () => {
      input!.value = "New name";
      input!.dispatchEvent(new InputEvent("input", { bubbles: true }));
      await flush();
    });
    const renameButton = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => button.textContent === "Rename");
    expect(renameButton).toBeDefined();
    await act(async () => {
      renameButton?.click();
      await settle(8);
    });

    expect((api as any).renameCodexSessionFile).toHaveBeenCalledWith("thread-1", "New name");
    expect(onRenamed).toHaveBeenCalled();
    expect(root?.textContent).toContain("Renamed");
  });
});
