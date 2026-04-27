import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";

import { FileViewerDialog } from "./FileViewerDialog";
import { clearRememberedFileSelections } from "./fileSelectionState";

vi.mock("../../lib/api", () => ({
  api: {
    getFiles: vi.fn(),
    getFileRead: vi.fn(),
    getGitFileVersions: vi.fn(),
    getWorkspace: vi.fn().mockResolvedValue({}),
    updateWorkspace: vi.fn().mockResolvedValue({}),
  },
}));

vi.mock("./MonacoWorkspace", () => ({
  MonacoWorkspace: (props: any) => (
    <div
      data-testid="monaco-workspace"
      data-line={props.line == null ? "" : String(props.line)}
      data-mode={props.mode}
      data-path={props.path}
    >
      {props.mode}:{props.path}
    </div>
  ),
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

describe("FileViewerDialog", () => {
  afterEach(() => {
    clearRememberedFileSelections();
    vi.clearAllMocks();
    if (root) {
      render(null, root);
      root.remove();
      root = null;
    }
  });

  it("restores persisted workspace selection and writes it back after load", async () => {
    const { api } = await import("../../lib/api");
    (api as any).getWorkspace.mockResolvedValue({
      selected_path: "src/main.tsx",
      history_items: [{ path: "README.md", label: "README" }],
    });
    (api as any).getFiles.mockResolvedValue({ path: "", items: [] });
    (api as any).getGitFileVersions.mockResolvedValue({
      path: "src/main.tsx",
      items: [{ version_id: "workspace", label: "Workspace", current: true }],
    } as any);

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <FileViewerDialog open sessionId="sess-persisted" onClose={() => undefined} />,
        root!,
      );
      await settle(16);
    });
    await settle(16);

    expect((api as any).getWorkspace).toHaveBeenCalledWith("sess-persisted", expect.any(AbortSignal));
    expect((api as any).getGitFileVersions).toHaveBeenCalledWith("sess-persisted", "src/main.tsx", expect.any(AbortSignal));
    await waitForCalls((api as any).updateWorkspace);
    expect((api as any).updateWorkspace).toHaveBeenCalledWith("sess-persisted", {
      selected_path: "src/main.tsx",
      open_paths: ["src/main.tsx", "src"],
      history_items: [
        { path: "src/main.tsx", label: "main.tsx" },
        { path: "README.md", label: "README" },
      ],
    });
  });

  it("loads the root directory, expands a folder, and opens a selected file in diff mode", async () => {
    const { api } = await import("../../lib/api");
    (api as any).getFiles.mockImplementation((_sessionId: string, nextPath?: string) => Promise.resolve(
      nextPath === "src"
        ? {
            path: "src",
            items: [{ name: "main.tsx", path: "src/main.tsx", kind: "file" }],
          }
        : {
            path: "",
            items: [
              { name: "src", path: "src", kind: "dir" },
              { name: "README.md", path: "README.md", kind: "file" },
            ],
          },
    ));
    (api as any).getGitFileVersions.mockResolvedValue({
      path: "src/main.tsx",
      fallback_reason: "workspace only",
      items: [{ version_id: "workspace", label: "Workspace", current: true }],
    } as any);
    (api as any).getFileRead.mockResolvedValue({ ok: true, kind: "text", text: "const after = true;" });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <FileViewerDialog open sessionId="sess-diff" onClose={() => undefined} />,
        root!,
      );
      await settle(8);
    });
    await settle(8);

    expect((api as any).getFiles).toHaveBeenCalledWith("sess-diff", undefined, expect.any(AbortSignal));
    expect(root?.textContent).toContain("src");
    expect(root?.textContent).toContain("README.md");

    const expandButton = root?.querySelector('button[aria-label="Expand src"]') as HTMLButtonElement | null;
    expect(expandButton).not.toBeNull();
    act(() => {
      expandButton?.click();
    });
    await settle(8);

    expect((api as any).getFiles).toHaveBeenCalledWith("sess-diff", "src", expect.any(AbortSignal));

    const fileButton = Array.from(root?.querySelectorAll("button") || []).find((button) => button.textContent === "main.tsx") as HTMLButtonElement | undefined;
    expect(fileButton).toBeDefined();
    act(() => {
      fileButton?.click();
    });
    await settle(8);

    expect((api as any).getGitFileVersions).toHaveBeenCalledWith("sess-diff", "src/main.tsx", expect.any(AbortSignal));
    expect(root.textContent).toContain("Workspace");
    expect(root.textContent).toContain("workspace only");
  });

  it("loads a directory only once when it is collapsed and re-expanded", async () => {
    const { api } = await import("../../lib/api");
    (api as any).getFiles.mockImplementation((_sessionId: string, nextPath?: string) => Promise.resolve(
      nextPath === "docs"
        ? {
            path: "docs",
            items: [{ name: "intro.md", path: "docs/intro.md", kind: "file" }],
          }
        : {
            path: "",
            items: [{ name: "docs", path: "docs", kind: "dir" }],
          },
    ));

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <FileViewerDialog open sessionId="sess-cache" onClose={() => undefined} />,
        root!,
      );
      await settle(8);
    });
    await settle(8);

    const expandButton = root?.querySelector('button[aria-label="Expand docs"]') as HTMLButtonElement | null;
    expect(expandButton).not.toBeNull();

    act(() => {
      expandButton?.click();
    });
    await settle(8);

    const collapseButton = root?.querySelector('button[aria-label="Collapse docs"]') as HTMLButtonElement | null;
    expect(collapseButton).not.toBeNull();
    act(() => {
      collapseButton?.click();
    });
    await settle(4);

    const reExpandButton = root?.querySelector('button[aria-label="Expand docs"]') as HTMLButtonElement | null;
    expect(reExpandButton).not.toBeNull();
    act(() => {
      reExpandButton?.click();
    });
    await settle(8);

    expect((api as any).getFiles).toHaveBeenCalledTimes(2);
  });

  it("opens explicit files in file mode on compact viewports and exposes the browser toggle", async () => {
    const originalMatchMedia = window.matchMedia;
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: query === "(max-width: 880px), (pointer: coarse)",
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn().mockReturnValue(false),
      })),
    });

    const { api } = await import("../../lib/api");
    (api as any).getFiles.mockResolvedValue({ path: "", items: [] });
    (api as any).getFileRead.mockResolvedValue({ ok: true, kind: "text", text: "# Hello\n\nBody" });

    root = document.createElement("div");
    document.body.appendChild(root);
    try {
      await act(async () => {
        render(
          <FileViewerDialog open sessionId="sess-mobile-file" initialPath="README.md" onClose={() => undefined} />,
          root!,
        );
        await settle(8);
      });

      expect((api as any).getFileRead).toHaveBeenCalledWith("sess-mobile-file", "README.md", expect.any(AbortSignal));
      expect(root.textContent).toContain("Browser");
      expect(root.textContent).toContain("README.md");
    } finally {
      Object.defineProperty(window, "matchMedia", {
        configurable: true,
        value: originalMatchMedia,
      });
    }
  });

  it("can switch from diff mode to file and markdown preview modes", async () => {
    const { api } = await import("../../lib/api");
    (api as any).getFiles.mockResolvedValue({ path: "", items: [] });
    (api as any).getGitFileVersions.mockResolvedValue({
      path: "docs/intro.md",
      items: [{ version_id: "workspace", label: "Workspace", current: true }],
      fallback_reason: "workspace only",
    } as any);
    (api as any).getFileRead.mockResolvedValue({ ok: true, kind: "text", text: "# Hello\n\nBody" });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <FileViewerDialog open sessionId="sess-preview" initialPath="docs/intro.md" onClose={() => undefined} />,
        root!,
      );
      await settle(8);
    });

    const fileButton = Array.from(root.querySelectorAll("button")).find((button) => button.textContent === "File") as HTMLButtonElement | undefined;
    const previewButton = Array.from(root.querySelectorAll("button")).find((button) => button.textContent === "Preview") as HTMLButtonElement | undefined;
    expect(fileButton).toBeDefined();
    expect(previewButton).toBeDefined();

    act(() => {
      fileButton?.click();
    });
    await settle(6);
    expect(api.getFileRead).toHaveBeenCalledWith("sess-preview", "docs/intro.md", expect.any(AbortSignal));
    expect(root.textContent).toContain("docs/intro.md");

    act(() => {
      previewButton?.click();
    });
    await settle(6);
    expect(root.querySelector(".filePreview article h1")?.textContent).toBe("Hello");
    expect(root.querySelector(".filePreview article")?.textContent).toContain("Body");
  });

  it("opens explicit file references in file mode and preserves the requested line", async () => {
    const { api } = await import("../../lib/api");
    (api as any).getFiles.mockResolvedValue({ path: "", items: [] });
    (api as any).getFileRead.mockResolvedValue({ ok: true, kind: "text", text: "line 1\nline 2" });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <FileViewerDialog
          open
          sessionId="sess-line"
         
          initialPath="src/main.tsx"
          initialLine={18}
          onClose={() => undefined}
        />,
        root!,
      );
      await settle(8);
    });

    expect((api as any).getFileRead).toHaveBeenCalledWith("sess-line", "src/main.tsx", expect.any(AbortSignal));
    expect(root.textContent).toContain("line 18");
  });

  it("shows a friendly error when the file list payload is malformed", async () => {
    const { api } = await import("../../lib/api");
    (api as any).getFiles.mockResolvedValue({ path: "", items: null });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <FileViewerDialog open sessionId="sess-bad-files" onClose={() => undefined} />,
        root!,
      );
      await settle(8);
    });
    await settle(8);

    expect(root.textContent).toContain("Unable to list files");
    expect(root.textContent).not.toContain("Cannot read properties of null");
  });
});
